// Package ini edits Windows 9x INI files (SYSTEM.INI, WIN.INI, MSDOS.SYS)
// in place: case-insensitive sections and keys, comments, ordering and
// formatting of untouched lines are preserved.
package ini

import (
	"strings"

	"github.com/win9x-modern/w9xm/internal/textfile"
)

type File struct{ Doc *textfile.Doc }

func Parse(b []byte) *File { return &File{textfile.Parse(b)} }
func New() *File           { return &File{textfile.New()} }
func (f *File) Bytes() []byte {
	return f.Doc.Bytes()
}

func sectionHeader(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "[") {
		if j := strings.Index(t, "]"); j > 0 {
			return strings.TrimSpace(t[1:j]), true
		}
	}
	return "", false
}

func keyValue(line string) (string, string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || t[0] == ';' || t[0] == '[' {
		return "", "", false
	}
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:]), true
}

// span returns the header index and end (exclusive) of the first matching section.
func (f *File) span(section string) (int, int) {
	hdr := -1
	for i, l := range f.Doc.Lines {
		if name, ok := sectionHeader(l); ok {
			if hdr >= 0 {
				return hdr, i
			}
			if strings.EqualFold(name, section) {
				hdr = i
			}
		}
	}
	if hdr < 0 {
		return -1, -1
	}
	return hdr, len(f.Doc.Lines)
}

func (f *File) insertPoint(hdr, end int) int {
	i := end
	for i > hdr+1 && strings.TrimSpace(f.Doc.Lines[i-1]) == "" {
		i--
	}
	return i
}

func (f *File) appendSection(section, line string) {
	if n := len(f.Doc.Lines); n > 0 && strings.TrimSpace(f.Doc.Lines[n-1]) != "" {
		f.Doc.Lines = append(f.Doc.Lines, "")
	}
	f.Doc.Lines = append(f.Doc.Lines, "["+section+"]", line)
}

// Get returns the first value of key in section.
func (f *File) Get(section, key string) (string, bool) {
	hdr, end := f.span(section)
	for i := hdr + 1; hdr >= 0 && i < end; i++ {
		if k, v, ok := keyValue(f.Doc.Lines[i]); ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// Set gives key a single value: the first occurrence is updated, later
// duplicates are removed, and a missing key/section is appended.
func (f *File) Set(section, key, value string) bool {
	hdr, end := f.span(section)
	if hdr < 0 {
		f.appendSection(section, key+"="+value)
		return true
	}
	changed, found := false, false
	for i := hdr + 1; i < end; i++ {
		k, v, ok := keyValue(f.Doc.Lines[i])
		if !ok || !strings.EqualFold(k, key) {
			continue
		}
		if !found {
			found = true
			if v != value {
				f.Doc.Lines[i] = k + "=" + value
				changed = true
			}
			continue
		}
		f.Doc.Remove(i, i+1)
		i--
		end--
		changed = true
	}
	if !found {
		f.Doc.Insert(f.insertPoint(hdr, end), key+"="+value)
		changed = true
	}
	return changed
}

// Add appends key=value unless that exact pair already exists. For
// multi-valued keys such as device= in [386Enh].
func (f *File) Add(section, key, value string) bool {
	hdr, end := f.span(section)
	if hdr < 0 {
		f.appendSection(section, key+"="+value)
		return true
	}
	for i := hdr + 1; i < end; i++ {
		if k, v, ok := keyValue(f.Doc.Lines[i]); ok && strings.EqualFold(k, key) && strings.EqualFold(v, value) {
			return false
		}
	}
	f.Doc.Insert(f.insertPoint(hdr, end), key+"="+value)
	return true
}

// Delete removes key from section; if value is non-nil only matching pairs are removed.
func (f *File) Delete(section, key string, value *string) bool {
	hdr, end := f.span(section)
	changed := false
	for i := hdr + 1; hdr >= 0 && i < end; i++ {
		k, v, ok := keyValue(f.Doc.Lines[i])
		if ok && strings.EqualFold(k, key) && (value == nil || strings.EqualFold(v, *value)) {
			f.Doc.Remove(i, i+1)
			i--
			end--
			changed = true
		}
	}
	return changed
}
