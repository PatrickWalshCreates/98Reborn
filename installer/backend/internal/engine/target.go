package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/win9x-modern/w9xm/internal/fat"
)

// Target is the Windows 98 system drive being patched: a mounted folder, or a
// FAT volume inside a disk image or raw device (no mounting needed, any host OS).
// Paths are slash-separated and relative to the drive root.
type Target interface {
	ReadDir(rel string) ([]string, error)
	Stat(rel string) (TargetInfo, error) // error wraps fs.ErrNotExist when missing
	ReadFile(rel string) ([]byte, error)
	// WriteFile creates or replaces a file. A mode without the owner-write bit
	// marks it read-only (the DOS read-only attribute on FAT).
	WriteFile(rel string, data []byte, mode fs.FileMode) error
	Mkdir(rel string) error
	Remove(rel string) error // file or empty directory
	Sync() error
	Close() error
	String() string
}

type TargetInfo struct {
	IsDir bool
	Mode  fs.FileMode // permission bits; 0444 = read-only
}

// OpenTarget opens a directory (mounted drive) or a disk/floppy image file.
func OpenTarget(path string) (Target, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return DirTarget(path), nil
	}
	return OpenImageTarget(path)
}

// --- mounted folder ---

type dirTarget string

func DirTarget(root string) Target { return dirTarget(root) }

func (d dirTarget) abs(rel string) string { return filepath.Join(string(d), filepath.FromSlash(rel)) }
func (d dirTarget) String() string        { return string(d) }
func (d dirTarget) Sync() error           { return nil }
func (d dirTarget) Close() error          { return nil }

func (d dirTarget) ReadDir(rel string) ([]string, error) {
	ents, err := os.ReadDir(d.abs(rel))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Name()
	}
	return out, nil
}

func (d dirTarget) Stat(rel string) (TargetInfo, error) {
	st, err := os.Lstat(d.abs(rel))
	if err != nil {
		return TargetInfo{}, err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return TargetInfo{}, fmt.Errorf("refusing to follow symlink %s", d.abs(rel))
	}
	return TargetInfo{IsDir: st.IsDir(), Mode: st.Mode().Perm()}, nil
}

func (d dirTarget) ReadFile(rel string) ([]byte, error) { return os.ReadFile(d.abs(rel)) }
func (d dirTarget) Mkdir(rel string) error              { return os.Mkdir(d.abs(rel), 0o755) }

// WriteFile replaces atomically, temporarily lifting a read-only mode
// (MSDOS.SYS ships read-only/hidden/system).
func (d dirTarget) WriteFile(rel string, data []byte, mode fs.FileMode) error {
	path := d.abs(rel)
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o200 == 0 {
		if err := os.Chmod(path, st.Mode().Perm()|0o200); err != nil {
			return err
		}
	}
	tmp := path + ".w9t"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if mode != 0 && mode != 0o644 {
		return os.Chmod(path, mode)
	}
	return nil
}

func (d dirTarget) Remove(rel string) error {
	path := d.abs(rel)
	if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode().Perm()&0o200 == 0 {
		_ = os.Chmod(path, st.Mode().Perm()|0o200)
	}
	return os.Remove(path)
}

// --- FAT volume in an image ---

type imageTarget struct {
	path string
	file *os.File
	fs   *fat.FS
}

// OpenImageTarget opens the first FAT partition of a raw disk image (or a
// floppy image) for read-write. Call Close to flush.
func OpenImageTarget(path string) (Target, error) {
	fh, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	v, err := fat.OpenImage(fh)
	if err != nil {
		fh.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &imageTarget{path: path, file: fh, fs: v}, nil
}

func (t *imageTarget) String() string { return t.path + " (disk image)" }

func (t *imageTarget) ReadDir(rel string) ([]string, error) {
	ents, err := t.fs.ReadDir(rel)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Name
	}
	return out, nil
}

func (t *imageTarget) Stat(rel string) (TargetInfo, error) {
	e, err := t.fs.Stat(rel)
	if err != nil {
		return TargetInfo{}, err
	}
	mode := fs.FileMode(0o644)
	if e.Attr&fat.AttrReadOnly != 0 {
		mode = 0o444
	}
	return TargetInfo{IsDir: e.IsDir(), Mode: mode}, nil
}

func (t *imageTarget) ReadFile(rel string) ([]byte, error) { return t.fs.ReadFile(rel) }
func (t *imageTarget) Mkdir(rel string) error              { return t.fs.Mkdir(rel) }
func (t *imageTarget) Remove(rel string) error             { return t.fs.Remove(rel) }

func (t *imageTarget) WriteFile(rel string, data []byte, mode fs.FileMode) error {
	if err := t.fs.WriteFile(rel, data, time.Now()); err != nil {
		return err
	}
	e, err := t.fs.Stat(rel)
	if err != nil {
		return err
	}
	attr := e.Attr &^ (fat.AttrDir | fat.AttrVolume)
	if mode != 0 && mode&0o200 == 0 {
		attr |= fat.AttrReadOnly
	} else {
		attr &^= fat.AttrReadOnly
	}
	if attr != e.Attr&^(fat.AttrDir|fat.AttrVolume) {
		return t.fs.SetAttr(rel, attr)
	}
	return nil
}

func (t *imageTarget) Sync() error {
	if err := t.fs.Sync(); err != nil {
		return err
	}
	return t.file.Sync()
}

func (t *imageTarget) Close() error {
	err := t.Sync()
	if cerr := t.file.Close(); err == nil {
		err = cerr
	}
	return err
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// IsNotExist reports whether err means the file doesn't exist on a Target.
func IsNotExist(err error) bool { return isNotExist(err) }
