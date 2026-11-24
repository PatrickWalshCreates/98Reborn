// Package module defines the patch-module format: loading, validation,
// dependency ordering, parameters, hardware conditions and templating.
package module

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/win9x-modern/w9xm/internal/delta"
	"github.com/win9x-modern/w9xm/internal/reg"
)

// FileName is the manifest file inside each module directory.
const FileName = "module.json"

type Module struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Category    string           `json:"category,omitempty"`
	License     string           `json:"license"`
	Requires    []string         `json:"requires,omitempty"`
	Conflicts   []string         `json:"conflicts,omitempty"`
	After       []string         `json:"after,omitempty"`
	When        *Condition       `json:"when,omitempty"`
	Params      map[string]Param `json:"params,omitempty"`
	Ops         []Op             `json:"ops"`

	Dir string `json:"-"` // directory the module was loaded from (payload root)
}

type Param struct {
	Type        string   `json:"type"` // int | string | bool
	Default     any      `json:"default"`
	Min         *int64   `json:"min,omitempty"`
	Max         *int64   `json:"max,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

type BytePatch struct {
	Offset int64  `json:"offset"`
	Old    string `json:"old"` // hex
	New    string `json:"new"` // hex
}

// Op is a single operation. Which fields apply depends on Op (see docs/MODULES.md).
type Op struct {
	Op       string `json:"op"`
	Optional bool   `json:"optional,omitempty"`
	Comment  string `json:"comment,omitempty"`

	// file.* / ini.* / lines.*
	File string `json:"file,omitempty"`

	// file.copy
	Src       string `json:"src,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`

	// file.delta / file.bytes
	Delta        string      `json:"delta,omitempty"`
	SHA256Before []string    `json:"sha256_before,omitempty"`
	SHA256After  string      `json:"sha256_after,omitempty"`
	Patches      []BytePatch `json:"patches,omitempty"`

	// file.pattern: hex bytes with "??" wildcards; every match of Find is
	// rewritten with Replace ("??" keeps the byte).
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`

	// vxd.extract: copy driver Name out of the VMM32.VXD container File to
	// Dest, where Windows loads it in place of the packed copy.
	Dest string `json:"dest,omitempty"`

	// ini.*
	Section string  `json:"section,omitempty"`
	Key     string  `json:"key,omitempty"`
	Value   *string `json:"value,omitempty"`

	// lines.*
	Block    string   `json:"block,omitempty"`
	Lines    []string `json:"lines,omitempty"`
	Position string   `json:"position,omitempty"`
	Match    string   `json:"match,omitempty"`
	Action   string   `json:"action,omitempty"`

	// reg.*
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Data string `json:"data,omitempty"`

	// media.extract: copy a file out of the user's original CD cabinets
	Source string `json:"source,omitempty"` // file name inside the setup cabinets, e.g. "USBSTOR.SYS"
	Media  string `json:"media,omitempty"`  // "98se" (default), "me", or "any"
}

var (
	idRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	paramRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	blockRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	shaRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var OpTypes = []string{
	"file.copy", "file.delete", "file.delta", "file.bytes", "file.pattern", "vxd.extract",
	"ini.set", "ini.add", "ini.delete",
	"lines.block", "lines.remove_block", "lines.edit",
	"reg.set", "reg.delete",
	"media.extract",
}

// Load reads and validates a single module directory.
func Load(dir string) (*Module, error) {
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Module
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, FileName), err)
	}
	m.Dir = dir
	if base := filepath.Base(dir); base != m.ID {
		return nil, fmt.Errorf("%s: id %q must match directory name %q", dir, m.ID, base)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("module %s: %w", m.ID, err)
	}
	return &m, nil
}

// LoadDir loads every module under dir (one subdirectory per module).
func LoadDir(dir string) (map[string]*Module, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*Module{}
	var errs []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(sub, FileName)); err != nil {
			continue
		}
		m, err := Load(sub)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out[m.ID] = m
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return out, nil
}

// Validate checks the manifest, op fields, payloads and template references.
func (m *Module) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	if !idRe.MatchString(m.ID) {
		add("invalid id %q (lowercase letters, digits, dashes)", m.ID)
	}
	if m.Name == "" {
		add("name is required")
	}
	if m.Version == "" {
		add("version is required")
	}
	if m.License == "" {
		add("license is required (licence of the module's payload)")
	}
	for _, lst := range [][]string{m.Requires, m.Conflicts, m.After} {
		for _, id := range lst {
			if !idRe.MatchString(id) {
				add("invalid module reference %q", id)
			}
			if id == m.ID {
				add("module references itself")
			}
		}
	}
	if m.When != nil {
		if err := m.When.validate(); err != nil {
			add("when: %v", err)
		}
	}
	for name, p := range m.Params {
		if !paramRe.MatchString(name) {
			add("param %q: invalid name", name)
			continue
		}
		if _, err := p.normalize(p.Default); err != nil {
			add("param %q: default: %v", name, err)
		}
	}
	if len(m.Ops) == 0 {
		add("module has no ops")
	}
	for i, op := range m.Ops {
		if err := m.validateOp(op); err != nil {
			add("op %d (%s): %v", i+1, op.Op, err)
		}
		for _, s := range op.templated() {
			if err := m.checkTemplate(s); err != nil {
				add("op %d (%s): %v", i+1, op.Op, err)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (op Op) templated() []string {
	s := []string{op.File, op.Section, op.Key, op.Path, op.Name, op.Data, op.Source, op.Dest}
	if op.Value != nil {
		s = append(s, *op.Value)
	}
	return append(s, op.Lines...)
}

func (m *Module) checkTemplate(s string) error {
	for _, ref := range TemplateRefs(s) {
		name, filter := ref[0], ref[1]
		if _, ok := filters[filter]; !ok {
			return fmt.Errorf("unknown template filter %q", filter)
		}
		if name == "windir" || strings.HasPrefix(name, "facts.") {
			continue
		}
		if _, ok := m.Params[name]; !ok {
			return fmt.Errorf("template references undeclared param %q", name)
		}
	}
	return nil
}

func (m *Module) payloadPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("payload path is empty")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("payload path %q must stay inside the module directory", rel)
	}
	return filepath.Join(m.Dir, clean), nil
}

// ReadPayload reads a file shipped inside the module, verifying sha256 when given.
func (m *Module) ReadPayload(rel, wantSHA string) ([]byte, error) {
	p, err := m.payloadPath(rel)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if wantSHA != "" {
		if got := SHA256(b); got != wantSHA {
			return nil, fmt.Errorf("payload %s: sha256 %s, manifest says %s", rel, got, wantSHA)
		}
	}
	return b, nil
}

func SHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (m *Module) validateOp(op Op) error {
	need := func(v string, name string) error {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	checkSHAs := func(list ...string) error {
		for _, s := range list {
			if s != "" && !shaRe.MatchString(s) {
				return fmt.Errorf("invalid sha256 %q (64 lowercase hex chars)", s)
			}
		}
		return nil
	}
	switch op.Op {
	case "file.copy":
		if err := need(op.File, "file"); err != nil {
			return err
		}
		if err := checkSHAs(op.SHA256); err != nil {
			return err
		}
		_, err := m.ReadPayload(op.Src, op.SHA256)
		return err
	case "file.delete":
		return need(op.File, "file")
	case "file.delta":
		if err := need(op.File, "file"); err != nil {
			return err
		}
		if err := checkSHAs(append(op.SHA256Before, op.SHA256After)...); err != nil {
			return err
		}
		b, err := m.ReadPayload(op.Delta, "")
		if err != nil {
			return err
		}
		switch {
		case delta.IsW9XD(b):
			_, err := delta.ReadHeader(b)
			return err
		case delta.IsBSDiff(b):
			if len(op.SHA256Before) == 0 || op.SHA256After == "" {
				return fmt.Errorf("bsdiff deltas need sha256_before and sha256_after")
			}
			return nil
		default:
			return fmt.Errorf("%s: unknown delta format", op.Delta)
		}
	case "file.bytes":
		if err := need(op.File, "file"); err != nil {
			return err
		}
		if err := checkSHAs(append(op.SHA256Before, op.SHA256After)...); err != nil {
			return err
		}
		if len(op.Patches) == 0 {
			return fmt.Errorf("patches is required")
		}
		for i, p := range op.Patches {
			o, err1 := hex.DecodeString(p.Old)
			n, err2 := hex.DecodeString(p.New)
			if err1 != nil || err2 != nil || len(o) == 0 || len(o) != len(n) || p.Offset < 0 {
				return fmt.Errorf("patch %d: old/new must be equal-length non-empty hex, offset >= 0", i+1)
			}
		}
		return nil
	case "file.pattern":
		if err := need(op.File, "file"); err != nil {
			return err
		}
		f, err1 := ParsePattern(op.Find)
		r, err2 := ParsePattern(op.Replace)
		if err1 != nil || err2 != nil || len(f) == 0 || len(f) != len(r) {
			return fmt.Errorf("find/replace must be equal-length hex patterns (?? = any byte)")
		}
		return nil
	case "vxd.extract":
		if op.File == "" || op.Name == "" || op.Dest == "" {
			return fmt.Errorf("file, name and dest are required")
		}
		return nil
	case "ini.set", "ini.add":
		if op.File == "" || op.Section == "" || op.Key == "" || op.Value == nil {
			return fmt.Errorf("file, section, key and value are required")
		}
		return nil
	case "ini.delete":
		if op.File == "" || op.Section == "" || op.Key == "" {
			return fmt.Errorf("file, section and key are required")
		}
		return nil
	case "lines.block":
		if op.File == "" || !blockRe.MatchString(op.Block) || len(op.Lines) == 0 {
			return fmt.Errorf("file, block (id) and lines are required")
		}
		if op.Position != "" && op.Position != "start" && op.Position != "end" {
			return fmt.Errorf("position must be start or end")
		}
		return nil
	case "lines.remove_block":
		if op.File == "" || !blockRe.MatchString(op.Block) {
			return fmt.Errorf("file and block are required")
		}
		return nil
	case "lines.edit":
		if op.File == "" || op.Match == "" {
			return fmt.Errorf("file and match are required")
		}
		if _, err := regexp.Compile("(?i)" + op.Match); err != nil {
			return fmt.Errorf("match: %v", err)
		}
		if op.Action != "comment" && op.Action != "delete" {
			return fmt.Errorf("action must be comment or delete")
		}
		return nil
	case "reg.set":
		if strings.Contains(op.Path+op.Data, "{{") {
			if op.Path == "" || op.Type == "" {
				return fmt.Errorf("path and type are required")
			}
			return nil
		}
		_, err := reg.NewValue(op.Path, op.Name, op.Type, op.Data)
		return err
	case "media.extract":
		if op.File == "" || op.Source == "" {
			return fmt.Errorf("file and source are required")
		}
		switch op.Media {
		case "", "98se", "98", "me", "any":
		default:
			return fmt.Errorf("media must be 98se, 98, me or any")
		}
		return checkSHAs(op.SHA256)
	case "reg.delete":
		if strings.Contains(op.Path, "{{") {
			return nil
		}
		_, _, err := reg.SplitRoot(op.Path)
		return err
	default:
		return fmt.Errorf("unknown op (valid: %s)", strings.Join(OpTypes, ", "))
	}
}

// ResolveParams merges defaults with overrides and validates every value.
func (m *Module) ResolveParams(overrides map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name, p := range m.Params {
		v, err := p.normalize(p.Default)
		if err != nil {
			return nil, fmt.Errorf("param %s: %w", name, err)
		}
		out[name] = v
	}
	for name, raw := range overrides {
		p, ok := m.Params[name]
		if !ok {
			return nil, fmt.Errorf("module %s has no param %q", m.ID, name)
		}
		v, err := p.normalize(raw)
		if err != nil {
			return nil, fmt.Errorf("param %s.%s: %w", m.ID, name, err)
		}
		out[name] = v
	}
	return out, nil
}

func (p Param) normalize(v any) (string, error) {
	var s string
	switch t := v.(type) {
	case nil:
		return "", fmt.Errorf("value is required")
	case string:
		s = t
	case float64:
		s = strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(t)
	default:
		return "", fmt.Errorf("unsupported value %v", v)
	}
	switch p.Type {
	case "int":
		n, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return "", fmt.Errorf("%q is not an integer", s)
		}
		if p.Min != nil && n < *p.Min {
			return "", fmt.Errorf("%d is below minimum %d", n, *p.Min)
		}
		if p.Max != nil && n > *p.Max {
			return "", fmt.Errorf("%d is above maximum %d", n, *p.Max)
		}
		return strconv.FormatInt(n, 10), nil
	case "bool":
		b, err := strconv.ParseBool(s)
		if err != nil {
			return "", fmt.Errorf("%q is not a boolean", s)
		}
		if b {
			return "1", nil
		}
		return "0", nil
	case "string":
		if len(p.Enum) > 0 {
			for _, e := range p.Enum {
				if e == s {
					return s, nil
				}
			}
			return "", fmt.Errorf("%q not one of %v", s, p.Enum)
		}
		return s, nil
	default:
		return "", fmt.Errorf("unknown param type %q (int, string, bool)", p.Type)
	}
}

// Resolve selects the wanted modules plus their transitive requirements,
// checks conflicts, and returns them in dependency order (stable).
func Resolve(all map[string]*Module, wanted []string) ([]*Module, error) {
	state := map[string]int{} // 1 visiting, 2 done
	var order []string
	var visit func(id, from string) error
	visit = func(id, from string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("dependency cycle involving %q", id)
		}
		m, ok := all[id]
		if !ok {
			if from == "" {
				return fmt.Errorf("unknown module %q", id)
			}
			return fmt.Errorf("module %s requires unknown module %q", from, id)
		}
		state[id] = 1
		for _, r := range m.Requires {
			if err := visit(r, id); err != nil {
				return err
			}
		}
		state[id] = 2
		order = append(order, id)
		return nil
	}
	for _, id := range wanted {
		if err := visit(id, ""); err != nil {
			return nil, err
		}
	}
	sel := map[string]bool{}
	for _, id := range order {
		sel[id] = true
	}
	for _, id := range order {
		for _, c := range all[id].Conflicts {
			if sel[c] {
				return nil, fmt.Errorf("module %s conflicts with %s", id, c)
			}
		}
	}
	placed := map[string]bool{}
	var out []*Module
	for len(out) < len(order) {
		progress := false
		for _, id := range order {
			if placed[id] {
				continue
			}
			m := all[id]
			ready := true
			for _, d := range append(append([]string{}, m.Requires...), m.After...) {
				if sel[d] && !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				placed[id] = true
				out = append(out, m)
				progress = true
				break
			}
		}
		if !progress {
			var rest []string
			for _, id := range order {
				if !placed[id] {
					rest = append(rest, id)
				}
			}
			sort.Strings(rest)
			return nil, fmt.Errorf("ordering cycle (requires/after) among: %s", strings.Join(rest, ", "))
		}
	}
	return out, nil
}

// PatternByte is one byte of a file.pattern; Any matches (or keeps) any byte.
type PatternByte struct {
	B   byte
	Any bool
}

// ParsePattern reads "29 C9 ?? 20" style hex (spaces optional).
func ParsePattern(s string) ([]PatternByte, error) {
	s = strings.Join(strings.Fields(s), "")
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("pattern has an odd number of digits")
	}
	out := make([]PatternByte, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		if s[i:i+2] == "??" {
			out = append(out, PatternByte{Any: true})
			continue
		}
		b, err := hex.DecodeString(s[i : i+2])
		if err != nil {
			return nil, err
		}
		out = append(out, PatternByte{B: b[0]})
	}
	return out, nil
}
