package module

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Edition is a named list of modules plus parameter presets ("module.param": value).
type Edition struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Modules     []string       `json:"modules"`
	Params      map[string]any `json:"params,omitempty"`
}

// LoadEdition accepts an edition.json path or a directory containing one.
func LoadEdition(path string) (*Edition, error) {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, "edition.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Edition
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if e.ID == "" || len(e.Modules) == 0 {
		return nil, fmt.Errorf("%s: id and modules are required", path)
	}
	return &e, nil
}

// ParamOverrides converts the edition's presets into module -> param -> value.
func (e *Edition) ParamOverrides() (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for k, v := range e.Params {
		mod, p, ok := strings.Cut(k, ".")
		if !ok {
			return nil, fmt.Errorf("edition %s: param key %q must be module.param", e.ID, k)
		}
		var s string
		switch t := v.(type) {
		case string:
			s = t
		case float64:
			s = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(t)
		default:
			return nil, fmt.Errorf("edition %s: param %s has unsupported value", e.ID, k)
		}
		if out[mod] == nil {
			out[mod] = map[string]string{}
		}
		out[mod][p] = s
	}
	return out, nil
}

// Check verifies all referenced modules exist and presets are valid.
func (e *Edition) Check(all map[string]*Module) error {
	mods, err := Resolve(all, e.Modules)
	if err != nil {
		return fmt.Errorf("edition %s: %w", e.ID, err)
	}
	ov, err := e.ParamOverrides()
	if err != nil {
		return err
	}
	sel := map[string]*Module{}
	for _, m := range mods {
		sel[m.ID] = m
	}
	for id, params := range ov {
		m, ok := sel[id]
		if !ok {
			return fmt.Errorf("edition %s: preset for module %s, which is not in the edition", e.ID, id)
		}
		if _, err := m.ResolveParams(params); err != nil {
			return fmt.Errorf("edition %s: %w", e.ID, err)
		}
	}
	return nil
}
