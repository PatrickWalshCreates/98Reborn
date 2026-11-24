package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/module"
)

// Every apply is a transaction recorded on the target under W9XM/TX/<id>/:
// journal.json plus a backup of every file it changed (0001.BAK, ...).

const txRoot = "W9XM/TX"

type FileRecord struct {
	Path      string `json:"path"`
	Existed   bool   `json:"existed"`
	Backup    string `json:"backup,omitempty"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	OldMode   uint32 `json:"old_mode,omitempty"`
	NewExists bool   `json:"new_exists"`
	NewSHA256 string `json:"new_sha256,omitempty"`
}

type ModuleRecord struct {
	ID      string            `json:"id"`
	Version string            `json:"version"`
	Params  map[string]string `json:"params,omitempty"`
	Tx      string            `json:"-"`
}

type Journal struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"` // pending | committed | failed | rolled_back
	Created    string         `json:"created"`
	RolledBack string         `json:"rolled_back,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Modules    []ModuleRecord `json:"modules"`
	Files      []FileRecord   `json:"files"`
	Dirs       []string       `json:"dirs,omitempty"` // directories created by this tx
	Error      string         `json:"error,omitempty"`

	dir string // W9XM/TX/<id>
}

type History struct{ Journals []*Journal } // sorted by ID ascending

func LoadHistory(t Target) (*History, error) {
	h := &History{}
	names, err := t.ReadDir(txRoot)
	if isNotExist(err) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		dir := txRoot + "/" + n
		b, err := t.ReadFile(dir + "/journal.json")
		if err != nil {
			continue
		}
		var j Journal
		if err := json.Unmarshal(b, &j); err != nil {
			return nil, fmt.Errorf("journal %s: %w", n, err)
		}
		j.dir = dir
		h.Journals = append(h.Journals, &j)
	}
	sort.Slice(h.Journals, func(a, b int) bool { return h.Journals[a].ID < h.Journals[b].ID })
	return h, nil
}

// Applied returns the latest committed record for each module.
func (h *History) Applied() map[string]ModuleRecord {
	out := map[string]ModuleRecord{}
	for _, j := range h.Journals {
		if j.Status != "committed" {
			continue
		}
		for _, m := range j.Modules {
			m.Tx = j.ID
			out[m.ID] = m
		}
	}
	return out
}

func (h *History) find(id string) *Journal {
	for _, j := range h.Journals {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (j *Journal) save(t Target) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return t.WriteFile(j.dir+"/journal.json", b, 0o644)
}

// mkdirAll creates missing directories for rel and returns the ones it created.
func mkdirAll(t Target, rel string) ([]string, error) {
	if rel == "." || rel == "" {
		return nil, nil
	}
	var created []string
	parts := strings.Split(rel, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		st, err := t.Stat(sub)
		if err == nil {
			if !st.IsDir {
				return created, fmt.Errorf("%s is not a directory", sub)
			}
			continue
		}
		if !isNotExist(err) {
			return created, err
		}
		if err := t.Mkdir(sub); err != nil {
			return created, err
		}
		created = append(created, sub)
	}
	return created, nil
}

func newTxDir(t Target) (string, string, error) {
	if _, err := mkdirAll(t, txRoot); err != nil {
		return "", "", err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	for n := 1; n < 100; n++ {
		id := fmt.Sprintf("%s-%02d", stamp, n)
		dir := txRoot + "/" + id
		if _, err := t.Stat(dir); isNotExist(err) {
			if err := t.Mkdir(dir); err != nil {
				return "", "", err
			}
			return id, dir, nil
		}
	}
	return "", "", errors.New("too many transactions this second")
}

func removeIfExists(t Target, rel string) error {
	if err := t.Remove(rel); err != nil && !isNotExist(err) {
		return err
	}
	return nil
}

// Apply commits the plan: verifies nothing changed since planning, backs up
// originals, writes changes, and records a journal. On a mid-way failure the
// already-written files are restored. Returns nil if there was nothing to do.
func (p *Plan) Apply() (*Journal, error) {
	if len(p.Modules) == 0 {
		return nil, nil
	}
	t := p.Opts.Target

	for _, c := range p.Changes { // staleness check before touching anything
		cur, err := t.ReadFile(c.Path)
		exists := err == nil
		if err != nil && !isNotExist(err) {
			return nil, err
		}
		if exists != c.e.origExists || (exists && !bytes.Equal(cur, c.e.orig)) {
			return nil, fmt.Errorf("%s changed on disk since the plan was made; re-run plan", c.Path)
		}
	}

	id, dir, err := newTxDir(t)
	if err != nil {
		return nil, err
	}
	j := &Journal{ID: id, Status: "pending", Created: time.Now().UTC().Format(time.RFC3339), Tool: p.Opts.Tool, dir: dir}
	for _, m := range p.Modules {
		j.Modules = append(j.Modules, ModuleRecord{ID: m.Module.ID, Version: m.Module.Version, Params: m.Params})
	}
	for i, c := range p.Changes {
		fr := FileRecord{Path: c.Path, Existed: c.e.origExists, NewExists: c.e.exists}
		if c.e.origExists {
			fr.Backup = fmt.Sprintf("%04d.BAK", i+1)
			fr.OldSHA256 = module.SHA256(c.e.orig)
			fr.OldMode = uint32(c.e.origMode)
			if err := t.WriteFile(dir+"/"+fr.Backup, c.e.orig, 0o644); err != nil {
				return nil, err
			}
		}
		if c.e.exists {
			fr.NewSHA256 = module.SHA256(c.e.data)
		}
		j.Files = append(j.Files, fr)
	}
	if err := j.save(t); err != nil {
		return nil, err
	}

	for i, c := range p.Changes {
		var werr error
		if c.e.exists {
			var created []string
			created, werr = mkdirAll(t, path.Dir(c.Path))
			j.Dirs = append(j.Dirs, created...)
			if werr == nil {
				mode := c.e.origMode
				if !c.e.origExists {
					mode = 0o644
				}
				werr = t.WriteFile(c.Path, c.e.data, mode)
			}
		} else {
			werr = removeIfExists(t, c.Path)
		}
		if werr != nil {
			restoreErr := j.restore(t, i+1)
			j.Status, j.Error = "failed", werr.Error()
			if restoreErr != nil {
				j.Error += "; restore: " + restoreErr.Error()
			}
			_ = j.save(t)
			_ = t.Sync()
			return nil, fmt.Errorf("writing %s: %w (changes reverted)", c.Path, werr)
		}
	}
	j.Status = "committed"
	if err := j.save(t); err != nil {
		return nil, err
	}
	return j, t.Sync()
}

// restore reverts the first n file records (in reverse) and removes created dirs.
func (j *Journal) restore(t Target, n int) error {
	var errs []string
	for i := n - 1; i >= 0; i-- {
		fr := j.Files[i]
		if fr.Existed {
			b, err := t.ReadFile(j.dir + "/" + fr.Backup)
			if err == nil && module.SHA256(b) != fr.OldSHA256 {
				err = errors.New("backup is corrupt")
			}
			if err == nil {
				err = t.WriteFile(fr.Path, b, fs.FileMode(fr.OldMode))
			}
			if err != nil {
				errs = append(errs, fr.Path+": "+err.Error())
			}
		} else if err := removeIfExists(t, fr.Path); err != nil {
			errs = append(errs, fr.Path+": "+err.Error())
		}
	}
	for i := len(j.Dirs) - 1; i >= 0; i-- {
		_ = t.Remove(j.Dirs[i]) // only succeeds if empty
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Rollback reverts a committed transaction (default: the latest). It refuses if
// a newer committed transaction touched the same files, or if files were
// changed since, unless force is set.
func Rollback(t Target, id string, force bool) (*Journal, error) {
	h, err := LoadHistory(t)
	if err != nil {
		return nil, err
	}
	var j *Journal
	if id == "" {
		for i := len(h.Journals) - 1; i >= 0 && j == nil; i-- {
			if h.Journals[i].Status == "committed" {
				j = h.Journals[i]
			}
		}
		if j == nil {
			return nil, errors.New("no committed transaction to roll back")
		}
	} else if j = h.find(id); j == nil {
		return nil, fmt.Errorf("no transaction %q", id)
	}
	if j.Status != "committed" {
		return nil, fmt.Errorf("transaction %s is %s, not committed", j.ID, j.Status)
	}

	var problems []string
	touched := map[string]bool{}
	for _, fr := range j.Files {
		touched[strings.ToLower(fr.Path)] = true
	}
	for _, o := range h.Journals {
		if o.ID <= j.ID || o.Status != "committed" {
			continue
		}
		for _, fr := range o.Files {
			if touched[strings.ToLower(fr.Path)] {
				problems = append(problems, fmt.Sprintf("newer transaction %s also changed %s (roll it back first)", o.ID, fr.Path))
			}
		}
	}
	for _, fr := range j.Files {
		b, err := t.ReadFile(fr.Path)
		exists := err == nil
		switch {
		case fr.NewExists && !exists:
			problems = append(problems, fr.Path+" was deleted since")
		case fr.NewExists && module.SHA256(b) != fr.NewSHA256:
			problems = append(problems, fr.Path+" was modified since")
		case !fr.NewExists && exists:
			problems = append(problems, fr.Path+" was re-created since")
		}
	}
	if len(problems) > 0 && !force {
		return nil, fmt.Errorf("refusing to roll back %s:\n  %s\n(use --force to override)", j.ID, strings.Join(problems, "\n  "))
	}
	if err := j.restore(t, len(j.Files)); err != nil {
		return nil, err
	}
	j.Status = "rolled_back"
	j.RolledBack = time.Now().UTC().Format(time.RFC3339)
	if err := j.save(t); err != nil {
		return nil, err
	}
	return j, t.Sync()
}
