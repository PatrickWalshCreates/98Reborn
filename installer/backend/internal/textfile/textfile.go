// Package textfile edits DOS/Windows text files (INI, CONFIG.SYS, AUTOEXEC.BAT)
// line by line while preserving line endings, a missing final newline, and a
// trailing DOS EOF (^Z). Content is treated as raw bytes (no codepage conversion).
package textfile

import (
	"bytes"
	"strings"
)

const dosEOF = 0x1A

type Doc struct {
	Lines    []string
	EOL      string
	FinalEOL bool
	Trailer  []byte
}

// New returns an empty document using CRLF, as Windows 98 expects.
func New() *Doc { return &Doc{EOL: "\r\n", FinalEOL: true} }

func Parse(b []byte) *Doc {
	d := New()
	end := len(b)
	for end > 0 && b[end-1] == dosEOF {
		end--
	}
	d.Trailer = append([]byte(nil), b[end:]...)
	b = b[:end]
	if len(b) == 0 {
		return d
	}
	if bytes.Contains(b, []byte("\r\n")) {
		d.EOL = "\r\n"
	} else if bytes.Contains(b, []byte("\n")) {
		d.EOL = "\n"
	}
	s := string(b)
	d.FinalEOL = strings.HasSuffix(s, "\n")
	if d.FinalEOL {
		s = s[:len(s)-1]
	}
	d.Lines = strings.Split(s, "\n")
	for i, l := range d.Lines {
		d.Lines[i] = strings.TrimSuffix(l, "\r")
	}
	return d
}

func (d *Doc) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString(strings.Join(d.Lines, d.EOL))
	if d.FinalEOL && len(d.Lines) > 0 {
		buf.WriteString(d.EOL)
	}
	buf.Write(d.Trailer)
	return buf.Bytes()
}

func (d *Doc) Insert(i int, lines ...string) {
	tail := append(append([]string(nil), lines...), d.Lines[i:]...)
	d.Lines = append(d.Lines[:i], tail...)
}

func (d *Doc) Remove(i, j int) { d.Lines = append(d.Lines[:i], d.Lines[j:]...) }
