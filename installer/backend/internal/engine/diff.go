package engine

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/win9x-modern/w9xm/internal/module"
	"github.com/win9x-modern/w9xm/internal/textfile"
)

// Describe renders a change for humans: a line diff for small text files,
// otherwise sizes and hashes.
func (c Change) Describe() string {
	if isText(c.Old) && isText(c.New) {
		if d, ok := lineDiff(textfile.Parse(c.Old).Lines, textfile.Parse(c.New).Lines); ok {
			if c.Kind == "add" {
				d = strings.TrimPrefix(d, "@@ line 1\n")
			}
			return d
		}
	}
	switch c.Kind {
	case "add":
		return fmt.Sprintf("binary, %d bytes, sha256 %s\n", len(c.New), module.SHA256(c.New))
	case "delete":
		return fmt.Sprintf("binary, %d bytes removed\n", len(c.Old))
	}
	return fmt.Sprintf("binary, %d -> %d bytes, sha256 %s -> %s\n",
		len(c.Old), len(c.New), module.SHA256(c.Old)[:12], module.SHA256(c.New)[:12])
}

func isText(b []byte) bool { return len(b) <= 256<<10 && !bytes.ContainsRune(b, 0) }

// lineDiff is an LCS diff printing only changed lines, with hunk markers.
func lineDiff(a, b []string) (string, bool) {
	if len(a)*len(b) > 4_000_000 {
		return "", false
	}
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	i, j, inHunk := 0, 0, false
	hunk := func() {
		if !inHunk {
			fmt.Fprintf(&sb, "@@ line %d\n", j+1)
			inHunk = true
		}
	}
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			i, j, inHunk = i+1, j+1, false
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			hunk()
			sb.WriteString("+" + b[j] + "\n")
			j++
		default:
			hunk()
			sb.WriteString("-" + a[i] + "\n")
			i++
		}
	}
	return sb.String(), true
}
