// Package cpio builds Linux initramfs archives ("newc" format) from an
// in-memory file tree.
package cpio

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const (
	TypeDir     = 0o040000
	TypeReg     = 0o100000
	TypeSymlink = 0o120000
	TypeChar    = 0o020000
)

// Node is one entry. Mode holds permission bits only.
type Node struct {
	Type     uint32
	Mode     uint32
	Data     []byte // file contents, or the symlink target
	UID, GID uint32
	Major    uint32 // device nodes
	Minor    uint32
}

// Tree maps slash-separated paths (no leading slash) to nodes.
type Tree map[string]*Node

// AddFile adds a regular file and its parent directories.
func (t Tree) AddFile(p string, mode uint32, data []byte) {
	t.MkdirAll(path.Dir(p))
	t[p] = &Node{Type: TypeReg, Mode: mode, Data: data}
}

// AddSymlink adds a symbolic link and its parent directories.
func (t Tree) AddSymlink(p, target string) {
	t.MkdirAll(path.Dir(p))
	t[p] = &Node{Type: TypeSymlink, Mode: 0o777, Data: []byte(target)}
}

// MkdirAll adds a directory and its parents (mode 0755) where missing.
func (t Tree) MkdirAll(p string) {
	for p != "." && p != "/" && p != "" {
		if _, ok := t[p]; !ok {
			t[p] = &Node{Type: TypeDir, Mode: 0o755}
		}
		p = path.Dir(p)
	}
}

// Size is the total size of file contents.
func (t Tree) Size() int64 {
	var n int64
	for _, e := range t {
		n += int64(len(e.Data))
	}
	return n
}

// Write writes the archive. Entries are sorted so directories precede their
// contents.
func (t Tree) Write(w io.Writer) error {
	names := make([]string, 0, len(t))
	for n := range t {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		// Compare component-wise so "a/b" sorts right after "a".
		return strings.ReplaceAll(names[i], "/", "\x00") < strings.ReplaceAll(names[j], "/", "\x00")
	})
	cw := &countW{w: w}
	ino := uint32(1)
	for _, n := range names {
		e := t[n]
		if err := header(cw, n, e, ino); err != nil {
			return err
		}
		ino++
	}
	if err := header(cw, "TRAILER!!!", &Node{}, 0); err != nil {
		return err
	}
	// Pad to 512 bytes like GNU cpio does.
	if r := cw.n % 512; r != 0 {
		_, err := cw.Write(make([]byte, 512-r))
		return err
	}
	return nil
}

type countW struct {
	w io.Writer
	n int64
}

func (c *countW) Write(p []byte) (int, error) {
	k, err := c.w.Write(p)
	c.n += int64(k)
	return k, err
}

func pad4(c *countW) error {
	if r := c.n % 4; r != 0 {
		_, err := c.Write(make([]byte, 4-r))
		return err
	}
	return nil
}

func header(c *countW, name string, e *Node, ino uint32) error {
	nlink := uint32(1)
	if e.Type == TypeDir {
		nlink = 2
	}
	h := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		ino, e.Type|e.Mode, e.UID, e.GID, nlink, 0, len(e.Data), 0, 0, e.Major, e.Minor, len(name)+1, 0)
	if _, err := io.WriteString(c, h+name+"\x00"); err != nil {
		return err
	}
	if err := pad4(c); err != nil {
		return err
	}
	if _, err := c.Write(e.Data); err != nil {
		return err
	}
	return pad4(c)
}
