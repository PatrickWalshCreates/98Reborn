package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Overlay is an in-memory copy-on-write view of the target tree. All module ops
// run against it; nothing touches disk until a Plan is applied. Paths are
// Windows-style relative to the drive root and resolved case-insensitively,
// matching FAT semantics on any host OS.
type Overlay struct {
	t       Target
	entries map[string]*entry
}

type entry struct {
	rel        string // on-disk relative path (actual case), slash-separated
	origExists bool
	orig       []byte
	origMode   fs.FileMode
	exists     bool
	data       []byte
}

func NewOverlay(t Target) *Overlay {
	return &Overlay{t: t, entries: map[string]*entry{}}
}

// CleanRel normalises "C:\WINDOWS\SYSTEM.INI" or "windows/system.ini" to
// "WINDOWS/SYSTEM.INI" and rejects traversal and the engine's own state dir.
func CleanRel(p string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return "", errors.New("empty path")
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid path %q", p)
		}
	}
	rel := strings.Join(parts, "/")
	if l := strings.ToLower(rel); l == "w9xm/tx" || strings.HasPrefix(l, "w9xm/tx/") {
		return "", fmt.Errorf("path %q is reserved for the engine's journal", rel)
	}
	return rel, nil
}

// resolve maps rel onto existing on-disk names case-insensitively. Components
// that don't exist keep the given case.
func resolve(t Target, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	cur := ""
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		names, err := t.ReadDir(cur)
		match := ""
		if err == nil {
			for _, n := range names {
				if n == part {
					match = part
					break
				}
				if match == "" && strings.EqualFold(n, part) {
					match = n
				}
			}
		}
		if match == "" {
			out = append(out, parts[i:]...)
			break
		}
		out = append(out, match)
		cur = strings.Join(out, "/")
		if _, err := t.Stat(cur); err != nil && !isNotExist(err) {
			return "", err // e.g. a symlink on a host folder
		}
	}
	return strings.Join(out, "/"), nil
}

func (o *Overlay) get(p string) (*entry, error) {
	rel, err := CleanRel(p)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(rel)
	if e, ok := o.entries[key]; ok {
		return e, nil
	}
	actual, err := resolve(o.t, rel)
	if err != nil {
		return nil, err
	}
	e := &entry{rel: actual}
	st, err := o.t.Stat(actual)
	switch {
	case err == nil && st.IsDir:
		return nil, fmt.Errorf("%s is a directory", actual)
	case err == nil:
		b, err := o.t.ReadFile(actual)
		if err != nil {
			return nil, err
		}
		e.origExists, e.orig, e.origMode = true, b, st.Mode
		e.exists, e.data = true, b
	case !isNotExist(err):
		return nil, err
	}
	o.entries[key] = e
	return e, nil
}

// Read returns the current (overlaid) content of p.
func (o *Overlay) Read(p string) ([]byte, bool, error) {
	e, err := o.get(p)
	if err != nil {
		return nil, false, err
	}
	return e.data, e.exists, nil
}

func (o *Overlay) Write(p string, data []byte) error {
	e, err := o.get(p)
	if err != nil {
		return err
	}
	e.exists, e.data = true, append([]byte(nil), data...)
	return nil
}

func (o *Overlay) Delete(p string) error {
	e, err := o.get(p)
	if err != nil {
		return err
	}
	e.exists, e.data = false, nil
	return nil
}

func (e *entry) changed() bool {
	return e.exists != e.origExists || !bytes.Equal(e.data, e.orig)
}

// Change describes one file modification in a plan.
type Change struct {
	Path string // relative, actual case
	Kind string // add | modify | delete
	Old  []byte
	New  []byte
	e    *entry
}

func (o *Overlay) Changes() []Change {
	var out []Change
	for _, e := range o.entries {
		if !e.changed() {
			continue
		}
		kind := "modify"
		if !e.origExists {
			kind = "add"
		} else if !e.exists {
			kind = "delete"
		}
		out = append(out, Change{Path: e.rel, Kind: kind, Old: e.orig, New: e.data, e: e})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out
}
