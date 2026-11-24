package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repository's real modules and editions must always validate.
func TestRepoModulesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	all, err := LoadDir(filepath.Join(root, "modules"))
	if err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(root, "editions"))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		ed, err := LoadEdition(filepath.Join(root, "editions", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := ed.Check(all); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRender(t *testing.T) {
	v := map[string]string{"ram": "1024", "cache": "256", "windir": "WINDOWS"}
	for in, want := range map[string]string{
		"{{ram|pages_hex}}":     "40000",
		"{{ cache | kb }}":      "262144",
		`{{windir}}\SYSTEM.INI`: `WINDOWS\SYSTEM.INI`,
		"{{ram|hex}} {{ram}}":   "400 1024",
		"no templates":          "no templates",
	} {
		got, err := Render(in, v)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	if _, err := Render("{{missing}}", v); err == nil {
		t.Error("missing var accepted")
	}
}

func TestResolve(t *testing.T) {
	all := map[string]*Module{
		"a": {ID: "a", Requires: []string{"b"}},
		"b": {ID: "b"},
		"c": {ID: "c", After: []string{"a"}},
		"x": {ID: "x", Conflicts: []string{"b"}},
		"l": {ID: "l", Requires: []string{"m"}},
		"m": {ID: "m", Requires: []string{"l"}},
	}
	got, err := Resolve(all, []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "b,a,c" {
		t.Errorf("order %v", ids)
	}
	if _, err := Resolve(all, []string{"a", "x"}); err == nil {
		t.Error("conflict not detected")
	}
	if _, err := Resolve(all, []string{"l"}); err == nil {
		t.Error("cycle not detected")
	}
	if _, err := Resolve(all, []string{"nope"}); err == nil {
		t.Error("unknown module accepted")
	}
}

func TestParams(t *testing.T) {
	lo, hi := int64(256), int64(1024)
	m := &Module{ID: "m", Params: map[string]Param{
		"ram":  {Type: "int", Default: float64(1024), Min: &lo, Max: &hi},
		"logo": {Type: "bool", Default: false},
	}}
	p, err := m.ResolveParams(map[string]string{"ram": "0x200"})
	if err != nil || p["ram"] != "512" || p["logo"] != "0" {
		t.Fatal(p, err)
	}
	for _, bad := range []map[string]string{{"ram": "4096"}, {"ram": "abc"}, {"nope": "1"}} {
		if _, err := m.ResolveParams(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestCondition(t *testing.T) {
	f := &Facts{RAMMB: 2048, CPUMHz: 3600, PCI: []PCIDevice{{ID: "8086:A282", Class: "0106"}}, Extra: map[string]string{"sata_mode": "AHCI"}}
	cases := []struct {
		c    Condition
		want bool
	}{
		{Condition{PCIAny: []string{"8086:a282"}}, true},
		{Condition{PCIAny: []string{"8086:*"}}, true},
		{Condition{PCIAny: []string{"10de:*"}}, false},
		{Condition{PCIClassAny: []string{"01"}}, true},
		{Condition{RAMMBMin: 4096}, false},
		{Condition{RAMMBMin: 1025, CPUMHzMin: 2100}, true},
		{Condition{Facts: map[string]string{"sata_mode": "ahci"}}, true},
	}
	for i, c := range cases {
		if got, _ := c.c.Eval(f); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
	if ok, _ := (&Condition{RAMMBMin: 1}).Eval(nil); ok {
		t.Error("unknown facts should skip by default")
	}
	if ok, _ := (&Condition{RAMMBMin: 1, IfUnknown: "apply"}).Eval(nil); !ok {
		t.Error("if_unknown=apply ignored")
	}
}
