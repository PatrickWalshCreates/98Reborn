// Package lines manages CONFIG.SYS / AUTOEXEC.BAT style files with
// idempotent marked blocks:
//
//	REM W9XM BEGIN <id>
//	...
//	REM W9XM END <id>
package lines

import (
	"regexp"
	"strings"

	"github.com/win9x-modern/w9xm/internal/textfile"
)

func markers(id string) (string, string) {
	return "REM W9XM BEGIN " + id, "REM W9XM END " + id
}

func isMarker(line string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "REM W9XM ")
}

func findBlock(d *textfile.Doc, id string) (int, int) {
	b, e := markers(id)
	start := -1
	for i, l := range d.Lines {
		t := strings.TrimSpace(l)
		if start < 0 && strings.EqualFold(t, b) {
			start = i
		} else if start >= 0 && strings.EqualFold(t, e) {
			return start, i
		}
	}
	return -1, -1
}

// EnsureBlock inserts or replaces the block; position is "start" or "end".
func EnsureBlock(d *textfile.Doc, id string, body []string, position string) bool {
	b, e := markers(id)
	want := append(append([]string{b}, body...), e)
	if s, en := findBlock(d, id); s >= 0 {
		if equal(d.Lines[s:en+1], want) {
			return false
		}
		d.Remove(s, en+1)
		d.Insert(s, want...)
		return true
	}
	if position == "start" {
		d.Insert(0, want...)
	} else {
		d.Lines = append(d.Lines, want...)
	}
	return true
}

func RemoveBlock(d *textfile.Doc, id string) bool {
	s, e := findBlock(d, id)
	if s < 0 {
		return false
	}
	d.Remove(s, e+1)
	return true
}

func isComment(line string) bool {
	t := strings.ToUpper(strings.TrimSpace(line))
	return t == "REM" || strings.HasPrefix(t, "REM ") || strings.HasPrefix(t, ";")
}

// Edit comments out ("REM ") or deletes lines matching re (case-insensitive),
// skipping comments and anything inside W9XM blocks.
func Edit(d *textfile.Doc, match, action string) (bool, error) {
	re, err := regexp.Compile("(?i)" + match)
	if err != nil {
		return false, err
	}
	changed, inBlock := false, false
	for i := 0; i < len(d.Lines); i++ {
		l := d.Lines[i]
		if isMarker(l) {
			inBlock = strings.Contains(strings.ToUpper(l), " BEGIN ")
			continue
		}
		if inBlock || isComment(l) || !re.MatchString(l) {
			continue
		}
		if action == "delete" {
			d.Remove(i, i+1)
			i--
		} else {
			d.Lines[i] = "REM " + l
		}
		changed = true
	}
	return changed, nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
