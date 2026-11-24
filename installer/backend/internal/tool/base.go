package tool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/win9x-modern/w9xm/internal/compose"
	"github.com/win9x-modern/w9xm/internal/iso9660"
)

// ReleaseURL is where CI publishes the installer base image.
const ReleaseURL = "https://github.com/PatrickWalshCreates/98Reborn/releases/latest/download/"

// BaseStore finds or downloads the open-source installer base image.
type BaseStore struct {
	Override string // --base: use this file
	Dir      string // cache directory
}

func (b *BaseStore) cached() string { return filepath.Join(b.Dir, "98reborn-base.iso") }

func (b *BaseStore) path() string {
	if b.Override != "" {
		return b.Override
	}
	return b.cached()
}

// Status describes the base image for the page.
func (b *BaseStore) Status() map[string]any {
	p := b.path()
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

// Ensure returns a usable base image, downloading and verifying it if needed.
func (b *BaseStore) Ensure(progress func(done, total int64)) (string, error) {
	if p := b.path(); fileOK(p) {
		return p, nil
	}
	if b.Override != "" {
		return "", fmt.Errorf("base image %s not found", b.Override)
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return "", err
	}
	sumResp, err := http.Get(ReleaseURL + "98reborn-base.iso.sha256")
	if err != nil {
		return "", fmt.Errorf("can't reach GitHub to download the installer base image: %w", err)
	}
	sumBody, _ := io.ReadAll(sumResp.Body)
	sumResp.Body.Close()
	if sumResp.StatusCode == http.StatusNotFound {
		return "", errors.New("no installer base image has been published yet. Developers: build one with 98reborn-mkbase and start the Tool with --base")
	}
	if sumResp.StatusCode != 200 {
		return "", fmt.Errorf("downloading the checksum: %s", sumResp.Status)
	}
	want := strings.Fields(string(sumBody))
	if len(want) == 0 || len(want[0]) != 64 {
		return "", errors.New("the published checksum is malformed")
	}
	resp, err := http.Get(ReleaseURL + "98reborn-base.iso")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading the base image: %s", resp.Status)
	}
	part := b.cached() + ".part"
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
	if got := hex.EncodeToString(h.Sum(nil)); !bytes.EqualFold([]byte(got), []byte(want[0])) {
		os.Remove(part)
		return "", errors.New("the downloaded base image failed its checksum; try again")
	}
	if err := os.Rename(part, b.cached()); err != nil {
		return "", err
	}
	return b.cached(), nil
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
