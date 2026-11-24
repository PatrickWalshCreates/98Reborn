package module

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Templates: {{name}} or {{name|filter}}. Names are module params, "windir",
// or facts.<fact>. Filters below.
var tmplRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.]+)\s*(?:\|\s*([a-z_]+)\s*)?\}\}`)

var filters = map[string]func(string) (string, error){
	"":      func(s string) (string, error) { return s, nil },
	"upper": func(s string) (string, error) { return strings.ToUpper(s), nil },
	"lower": func(s string) (string, error) { return strings.ToLower(s), nil },
	// hex: integer -> uppercase hex without prefix
	"hex": func(s string) (string, error) {
		n, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return "", err
		}
		return strings.ToUpper(strconv.FormatInt(n, 16)), nil
	},
	// pages_hex: megabytes -> number of 4 KB pages in hex (SYSTEM.INI MaxPhysPage)
	"pages_hex": func(s string) (string, error) {
		n, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return "", err
		}
		return strings.ToUpper(strconv.FormatInt(n*256, 16)), nil
	},
	// kb: megabytes -> kilobytes (SYSTEM.INI [vcache])
	"kb": func(s string) (string, error) {
		n, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(n*1024, 10), nil
	},
}

// TemplateRefs returns [name, filter] pairs referenced in s.
func TemplateRefs(s string) [][2]string {
	var out [][2]string
	for _, m := range tmplRe.FindAllStringSubmatch(s, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

// Render substitutes template references using vars.
func Render(s string, vars map[string]string) (string, error) {
	var firstErr error
	out := tmplRe.ReplaceAllStringFunc(s, func(match string) string {
		m := tmplRe.FindStringSubmatch(match)
		v, ok := vars[m[1]]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("template variable %q has no value", m[1])
			}
			return match
		}
		f, ok := filters[m[2]]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown filter %q", m[2])
			}
			return match
		}
		r, err := f(v)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("filter %s(%q): %v", m[2], v, err)
		}
		return r
	})
	return out, firstErr
}
