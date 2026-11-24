// Package media opens a user's Windows 98 SE / Me CD image and indexes every
// file inside its cabinets, so modules and the ISO builder can pull original
// files without mounting or third-party tools.
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/win9x-modern/w9xm/internal/cab"
	"github.com/win9x-modern/w9xm/internal/iso9660"
)

type Product string

const (
	Win98SE Product = "Windows 98 Second Edition"
	Win98   Product = "Windows 98"
	WinMe   Product = "Windows Me"
	Unknown Product = "unknown"
)

type Media struct {
	ISO      *iso9660.Image
	Product  Product
	SetupDir string // "WIN98" or "WIN9X" (as named on the disc)
	// SetupSHA256 fingerprints the exact release (hash of the setup dir's SETUP.EXE).
	SetupSHA256 string
	Sets        []*cab.Set // all cabinet chains on the disc
	setDirs     []string   // disc directory of each set
	Files       []*File    // every file inside every cabinet
	NotCabinets []string   // *.CAB files that aren't Microsoft cabinets (e.g. InstallShield)
	Warnings    []string
}

// File is a file inside a cabinet on the disc.
type File struct {
	*cab.File
	Dir   string // disc directory holding the cabinet ("WIN98")
	Setup bool   // part of the main OS setup files
}

// Path is a display/selection path: DIR/CABINET/NAME.
func (f *File) Path() string { return path.Join(f.Dir, f.Cabinet, f.Name) }

func Open(isoPath string) (*Media, error) {
	im, err := iso9660.OpenFile(isoPath)
	if err != nil {
		return nil, err
	}
	m := &Media{ISO: im, Product: Unknown}
	if err := m.index(); err != nil {
		im.Close()
		return nil, err
	}
	return m, nil
}

func (m *Media) Close() error { return m.ISO.Close() }

// Key is the short id modules use to refer to this CD: "98se", "98", "me" or "".
func (m *Media) Key() string {
	switch m.Product {
	case Win98SE:
		return "98se"
	case Win98:
		return "98"
	case WinMe:
		return "me"
	}
	return ""
}

func (m *Media) index() error {
	root, err := m.ISO.ReadDir("")
	if err != nil {
		return err
	}
	for _, e := range root {
		if e.IsDir && (strings.EqualFold(e.Name, "WIN98") || strings.EqualFold(e.Name, "WIN9X")) {
			m.SetupDir = e.Name
		}
	}
	vol := strings.ToUpper(m.ISO.VolumeID)
	switch {
	case strings.EqualFold(m.SetupDir, "WIN9X") || strings.Contains(vol, " ME"):
		m.Product = WinMe
	case strings.EqualFold(m.SetupDir, "WIN98") && strings.Contains(vol, "SE"):
		m.Product = Win98SE
	case strings.EqualFold(m.SetupDir, "WIN98"):
		m.Product = Win98
	}
	if m.SetupDir != "" {
		if b, err := m.ISO.ReadFile(m.SetupDir + "/SETUP.EXE"); err == nil {
			s := sha256.Sum256(b)
			m.SetupSHA256 = hex.EncodeToString(s[:])
		}
	}

	byDir := map[string][]*cab.Cabinet{}
	var dirs []string
	err = m.ISO.Walk(func(e iso9660.Entry) error {
		if e.IsDir || !strings.EqualFold(path.Ext(e.Name), ".cab") {
			return nil
		}
		sr, err := m.ISO.OpenEntry(e)
		if err != nil {
			return err
		}
		c, err := cab.Parse(sr, e.Size, e.Name)
		if errors.Is(err, cab.ErrNotCabinet) {
			m.NotCabinets = append(m.NotCabinets, e.Path)
			return nil
		}
		if err != nil {
			m.Warnings = append(m.Warnings, err.Error())
			return nil
		}
		dir := path.Dir(e.Path)
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], c)
		return nil
	})
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		sets, err := cab.BuildSets(byDir[dir])
		if err != nil {
			m.Warnings = append(m.Warnings, dir+": "+err.Error())
		}
		for _, s := range sets {
			m.Sets = append(m.Sets, s)
			m.setDirs = append(m.setDirs, dir)
			for _, f := range s.Files {
				m.Files = append(m.Files, &File{File: f, Dir: dir, Setup: strings.EqualFold(dir, m.SetupDir)})
			}
		}
	}
	if m.SetupDir == "" {
		m.Warnings = append(m.Warnings, "no WIN98 or WIN9X setup directory found: is this a Windows 98/Me CD?")
	}
	return nil
}

// Find returns files whose name or DIR/CAB/NAME path matches any glob
// (case-insensitive). setupOnly limits results to the main OS setup files.
// No patterns matches everything.
func (m *Media) Find(patterns []string, setupOnly bool) []*File {
	var out []*File
	for _, f := range m.Files {
		if setupOnly && !f.Setup {
			continue
		}
		if len(patterns) == 0 {
			out = append(out, f)
			continue
		}
		for _, p := range patterns {
			if cab.Match(p, f.Name) || cab.Match(p, f.Path()) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// Extract decompresses files (grouped so each folder is decompressed once).
func Extract(files []*File, fn func(f *File, data []byte) error) error {
	byCab := map[*cab.File]*File{}
	raw := make([]*cab.File, len(files))
	for i, f := range files {
		raw[i] = f.File
		byCab[f.File] = f
	}
	return cab.Extract(raw, func(cf *cab.File, data []byte) error { return fn(byCab[cf], data) })
}

// ReadFile extracts a single setup file by name (first match in setup order).
func (m *Media) ReadFile(name string) ([]byte, error) {
	for _, f := range m.Files {
		if f.Setup && strings.EqualFold(f.Name, name) {
			var out []byte
			err := Extract([]*File{f}, func(_ *File, b []byte) error { out = b; return nil })
			return out, err
		}
	}
	return nil, fmt.Errorf("%s not found in %s setup files", name, m.Product)
}

// Summary returns per-directory counts for display.
func (m *Media) Summary() []string {
	type agg struct {
		cabs, files int
		bytes       int64
	}
	by := map[string]*agg{}
	for i, s := range m.Sets {
		dir := m.setDirs[i]
		if by[dir] == nil {
			by[dir] = &agg{}
		}
		by[dir].cabs += len(s.Cabinets)
	}
	for _, f := range m.Files {
		if by[f.Dir] == nil {
			by[f.Dir] = &agg{}
		}
		by[f.Dir].files++
		by[f.Dir].bytes += int64(f.Size)
	}
	var keys []string
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		a := by[k]
		out = append(out, fmt.Sprintf("%-36s %3d cabinets %5d files %8.1f MB", k, a.cabs, a.files, float64(a.bytes)/1e6))
	}
	return out
}
