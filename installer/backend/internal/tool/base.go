package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/compose"
	"github.com/win9x-modern/w9xm/internal/iso9660"
)

// ReleasesAPI lists 98Reborn's releases (pre-releases included, newest
// first); CI attaches the installer base image to each.
const ReleasesAPI = "https://api.github.com/repos/PatrickWalshCreates/98Reborn/releases?per_page=20"

// BaseStore finds or downloads the open-source installer base image. Each
// release's image is cached in its own folder (Dir/<tag>/).
type BaseStore struct {
	Override string // --base: use this file
	Dir      string // cache directory
}

// newestCached returns the most recently downloaded cached image, if any.
func (b *BaseStore) newestCached() string {
	m, _ := filepath.Glob(filepath.Join(b.Dir, "*", "98reborn-base.iso"))
	best, bt := "", int64(0)
	for _, p := range m {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().Unix() > bt && fileOK(p) {
			best, bt = p, fi.ModTime().Unix()
		}
	}
	return best
}

func (b *BaseStore) path() string {
	if b.Override != "" {
		return b.Override
	}
	return b.newestCached()
}

// Status describes the base image for the page.
func (b *BaseStore) Status() map[string]any {
	p := b.path()
	if p == "" {
		return map[string]any{"present": false}
	}
	im, err := iso9660.OpenFile(p)
	if err != nil {
		return map[string]any{"present": false}
	}
	defer im.Close()
	var info struct{ Version, Built string }
	if raw, err := im.ReadFile(compose.BaseInfo); err == nil {
		json.Unmarshal(raw, &info)
	}
	fi, _ := os.Stat(p)
	return map[string]any{"present": true, "version": info.Version, "built": info.Built, "path": p, "size_mb": fi.Size() >> 20}
}

type release struct {
	Tag       string    `json:"tag_name"`
	Draft     bool      `json:"draft"`
	Published time.Time `json:"published_at"`
	Assets    []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// latest finds the newest release that carries a base image.
func latest() (tag, isoURL, sumURL string, err error) {
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(ReleasesAPI)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", "", fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rs []release
	if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
		return "", "", "", err
	}
	return newestBase(rs)
}

// newestBase picks the most recently published release that carries a base
// image. The API doesn't list releases newest first: it sorts them by tag
// name as text, which puts "alpha.9" before "alpha.11".
func newestBase(rs []release) (tag, isoURL, sumURL string, err error) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Published.After(rs[j].Published) })
	for _, r := range rs {
		if r.Draft {
			continue
		}
		iso, sum := "", ""
		for _, a := range r.Assets {
			switch a.Name {
			case "98reborn-base.iso":
				iso = a.URL
			case "98reborn-base.iso.sha256":
				sum = a.URL
			}
		}
		if iso != "" && sum != "" {
			return r.Tag, iso, sum, nil
		}
	}
	return "", "", "", errors.New("no installer base image has been published yet. Developers: build one with 98reborn-mkbase and start the Tool with --base")
}

// Ensure returns a usable base image: the newest release's (downloaded and
// verified once), or the newest cached one when GitHub can't be reached.
func (b *BaseStore) Ensure(progress func(done, total int64)) (string, error) {
	if b.Override != "" {
		if fileOK(b.Override) {
			return b.Override, nil
		}
		return "", fmt.Errorf("base image %s not found", b.Override)
	}
	tag, isoURL, sumURL, err := latest()
	if err != nil {
		if p := b.newestCached(); p != "" {
			return p, nil // offline: use what we have
		}
		return "", fmt.Errorf("can't get the installer base image: %w", err)
	}
	dst := filepath.Join(b.Dir, filepath.Base(tag), "98reborn-base.iso")
	if fileOK(dst) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	sumResp, err := http.Get(sumURL)
	if err != nil {
		return "", err
	}
	sumBody, _ := io.ReadAll(sumResp.Body)
	sumResp.Body.Close()
	want := strings.Fields(string(sumBody))
	if sumResp.StatusCode != 200 || len(want) == 0 || len(want[0]) != 64 {
		return "", errors.New("the published checksum is missing or malformed")
	}
	resp, err := http.Get(isoURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading the base image: %s", resp.Status)
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress}
	_, err = io.Copy(io.MultiWriter(f, h), pr)
	f.Close()
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want[0]) {
		os.Remove(part)
		return "", errors.New("the downloaded base image failed its checksum; try again")
	}
	if err := os.Rename(part, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func fileOK(p string) bool {
	im, err := iso9660.OpenFile(p)
	if err != nil {
		return false
	}
	defer im.Close()
	_, err = im.Stat(compose.BaseInfo)
	return err == nil
}

type progressReader struct {
	r           io.Reader
	done, total int64
	fn          func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
	return n, err
}
