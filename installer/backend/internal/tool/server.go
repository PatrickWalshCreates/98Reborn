// Package tool is the 98Reborn Tool: a small local web app that checks the PC
// it runs on and builds the bootable 98Reborn installer ISO from the user's
// own Windows 98 SE CD. It listens on 127.0.0.1 only, and every API call must
// carry a random token from the URL the Tool opens, so other web pages can't
// drive it.
package tool

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/compose"
	"github.com/win9x-modern/w9xm/internal/media"
	"github.com/win9x-modern/w9xm/internal/scan"
)

//go:embed web
var webFS embed.FS

type Server struct {
	Version string
	Base    *BaseStore
	token   string
	srv     *http.Server
	done    chan struct{}

	mu  sync.Mutex
	job Job
}

// Job is the state of the current build.
type Job struct {
	State    string  `json:"state"` // idle, running, done, error
	Stage    string  `json:"stage"`
	Progress float64 `json:"progress"` // 0..1 within the stage, -1 = unknown
	Message  string  `json:"message"`
	Out      string  `json:"out"`
	SizeMB   int64   `json:"size_mb"`
}

func New(version string, base *BaseStore) *Server {
	b := make([]byte, 16)
	rand.Read(b)
	return &Server{Version: version, Base: base, token: hex.EncodeToString(b), done: make(chan struct{}), job: Job{State: "idle"}}
}

// Start listens on a free local port and returns the URL to open.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	api := map[string]func(*http.Request) (any, error){
		"/api/info":     s.info,
		"/api/scan":     s.scan,
		"/api/pick":     s.pick,
		"/api/check-cd": s.checkCD,
		"/api/build":    s.build,
		"/api/job":      s.jobState,
		"/api/reveal":   s.reveal,
		"/api/quit":     s.quit,
	}
	for path, h := range api {
		h := h
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-98R-Token") != s.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			v, err := h(r)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				v = map[string]string{"error": err.Error()}
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return fmt.Sprintf("http://%s/?t=%s", ln.Addr(), s.token), nil
}

// Wait blocks until the user quits from the page.
func (s *Server) Wait() { <-s.done }

func decode(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return errors.New("POST required")
	}
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) info(r *http.Request) (any, error) {
	home, _ := os.UserHomeDir()
	out := filepath.Join(home, "Downloads", "98Reborn-installer.iso")
	if _, err := os.Stat(filepath.Dir(out)); err != nil {
		out = filepath.Join(home, "98Reborn-installer.iso")
	}
	return map[string]any{
		"version": s.Version, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"base": s.Base.Status(), "default_out": out,
	}, nil
}

func (s *Server) scan(r *http.Request) (any, error) {
	sc, err := scan.Run()
	if err != nil {
		return nil, err
	}
	rep := compat.Evaluate(sc)
	return map[string]any{"report": rep, "tier": rep.Tier.String()}, nil
}

func (s *Server) pick(r *http.Request) (any, error) {
	var req struct{ Kind, Current string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var p string
	var err error
	if req.Kind == "out" {
		p, err = saveDialog("Save the 98Reborn installer as", filepath.Base(req.Current), filepath.Dir(req.Current))
	} else {
		p, err = openDialog("Choose your Windows 98 SE CD image (.iso)")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": p, "cancelled": p == ""}, nil
}

func (s *Server) checkCD(r *http.Request) (any, error) {
	var req struct{ Path string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	m, err := media.Open(req.Path)
	if err != nil {
		return map[string]any{"ok": false, "message": "This isn't a readable CD image: " + err.Error()}, nil
	}
	defer m.Close()
	if m.Product != media.Win98SE {
		msg := fmt.Sprintf("This is %s. 98Reborn needs Windows 98 Second Edition.", m.Product)
		if m.Product == media.Unknown {
			msg = "This doesn't look like a Windows 98 SE CD."
		}
		return map[string]any{"ok": false, "message": msg, "product": m.Product}, nil
	}
	return map[string]any{"ok": true, "product": m.Product, "message": "Windows 98 Second Edition: ready to use."}, nil
}

func (s *Server) setJob(f func(j *Job)) {
	s.mu.Lock()
	f(&s.job)
	s.mu.Unlock()
}

func (s *Server) jobState(r *http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job, nil
}

func (s *Server) build(r *http.Request) (any, error) {
	var req struct{ CD, Out string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.CD == "" || req.Out == "" {
		return nil, errors.New("choose your CD image and where to save the installer")
	}
	s.mu.Lock()
	if s.job.State == "running" {
		s.mu.Unlock()
		return nil, errors.New("a build is already running")
	}
	s.job = Job{State: "running", Stage: "Getting the installer base image", Progress: -1}
	s.mu.Unlock()
	go func() {
		base, err := s.Base.Ensure(func(done, total int64) {
			s.setJob(func(j *Job) {
				j.Stage = "Downloading the installer base image"
				if total > 0 {
					j.Progress = float64(done) / float64(total)
				}
			})
		})
		if err == nil {
			s.setJob(func(j *Job) { j.Stage, j.Progress = "Building your installer ISO", -1 })
			os.MkdirAll(filepath.Dir(req.Out), 0o755)
			err = compose.Compose(compose.Options{Base: base, CD: req.CD, Out: req.Out})
		}
		s.setJob(func(j *Job) {
			if err != nil {
				j.State, j.Message = "error", err.Error()
				return
			}
			fi, _ := os.Stat(req.Out)
			j.State, j.Stage, j.Progress, j.Out = "done", "Done", 1, req.Out
			if fi != nil {
				j.SizeMB = fi.Size() >> 20
			}
		})
	}()
	return map[string]string{"ok": "started"}, nil
}

func (s *Server) reveal(r *http.Request) (any, error) {
	var req struct{ Path string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, revealFile(req.Path)
}

func (s *Server) quit(r *http.Request) (any, error) {
	go func() {
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.srv.Shutdown(ctx)
		close(s.done)
	}()
	return map[string]bool{"ok": true}, nil
}
