package module

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PCIDevice is one device from the hardware scan. ID is "vendor:device" in hex.
type PCIDevice struct {
	ID    string `json:"id"`
	Class string `json:"class,omitempty"` // e.g. "0106" (SATA AHCI)
	Name  string `json:"name,omitempty"`
}

// Facts describe the target machine, produced by the installer's hardware scan.
type Facts struct {
	RAMMB  int               `json:"ram_mb,omitempty"`
	CPUMHz int               `json:"cpu_mhz,omitempty"`
	PCI    []PCIDevice       `json:"pci,omitempty"`
	Extra  map[string]string `json:"extra,omitempty"`
}

func LoadFacts(path string) (*Facts, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Facts
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Vars exposes facts to templates as facts.<name>.
func (f *Facts) Vars() map[string]string {
	v := map[string]string{}
	if f == nil {
		return v
	}
	if f.RAMMB > 0 {
		v["facts.ram_mb"] = strconv.Itoa(f.RAMMB)
	}
	if f.CPUMHz > 0 {
		v["facts.cpu_mhz"] = strconv.Itoa(f.CPUMHz)
	}
	for k, val := range f.Extra {
		v["facts."+k] = val
	}
	return v
}

// Condition gates a module on hardware facts. All present criteria must pass.
type Condition struct {
	PCIAny      []string          `json:"pci_any,omitempty"`       // "8086:2922" or "8086:*"
	PCIClassAny []string          `json:"pci_class_any,omitempty"` // prefix match: "0106", "03"
	RAMMBMin    int               `json:"ram_mb_min,omitempty"`
	RAMMBMax    int               `json:"ram_mb_max,omitempty"`
	CPUMHzMin   int               `json:"cpu_mhz_min,omitempty"`
	Facts       map[string]string `json:"facts,omitempty"` // equality on Extra (case-insensitive)
	// IfUnknown decides what happens when a needed fact is missing:
	// "skip" (default) or "apply" (for protective fixes that are harmless when unneeded).
	IfUnknown string `json:"if_unknown,omitempty"`
}

func (c *Condition) validate() error {
	if c.IfUnknown != "" && c.IfUnknown != "skip" && c.IfUnknown != "apply" {
		return fmt.Errorf("if_unknown must be skip or apply")
	}
	for _, p := range c.PCIAny {
		v, d, ok := strings.Cut(p, ":")
		if !ok || len(v) != 4 || (d != "*" && len(d) != 4) {
			return fmt.Errorf("pci_any entry %q must look like 8086:2922 or 8086:*", p)
		}
	}
	return nil
}

// Eval reports whether the condition holds, and why not.
func (c *Condition) Eval(f *Facts) (bool, string) {
	if c == nil {
		return true, ""
	}
	check := func(known, ok bool, what string) (bool, string) {
		if !known {
			if c.IfUnknown == "apply" {
				return true, ""
			}
			return false, what + " unknown (no hardware facts)"
		}
		if !ok {
			return false, what + " does not match"
		}
		return true, ""
	}
	type crit struct {
		known, ok bool
		what      string
	}
	var crits []crit
	hasPCI := f != nil && len(f.PCI) > 0
	if len(c.PCIAny) > 0 {
		crits = append(crits, crit{hasPCI, hasPCI && f.anyPCI(c.PCIAny), "PCI devices"})
	}
	if len(c.PCIClassAny) > 0 {
		crits = append(crits, crit{hasPCI, hasPCI && f.anyClass(c.PCIClassAny), "PCI device classes"})
	}
	ram := f != nil && f.RAMMB > 0
	if c.RAMMBMin > 0 {
		crits = append(crits, crit{ram, ram && f.RAMMB >= c.RAMMBMin, fmt.Sprintf("RAM >= %d MB", c.RAMMBMin)})
	}
	if c.RAMMBMax > 0 {
		crits = append(crits, crit{ram, ram && f.RAMMB <= c.RAMMBMax, fmt.Sprintf("RAM <= %d MB", c.RAMMBMax)})
	}
	if c.CPUMHzMin > 0 {
		cpu := f != nil && f.CPUMHz > 0
		crits = append(crits, crit{cpu, cpu && f.CPUMHz >= c.CPUMHzMin, fmt.Sprintf("CPU >= %d MHz", c.CPUMHzMin)})
	}
	for k, want := range c.Facts {
		var got string
		known := false
		if f != nil {
			got, known = f.Extra[k]
		}
		crits = append(crits, crit{known, strings.EqualFold(got, want), fmt.Sprintf("fact %s=%s", k, want)})
	}
	for _, cr := range crits {
		if ok, why := check(cr.known, cr.ok, cr.what); !ok {
			return false, why
		}
	}
	return true, ""
}

func (f *Facts) anyPCI(patterns []string) bool {
	for _, d := range f.PCI {
		dv, dd, _ := strings.Cut(strings.ToLower(d.ID), ":")
		for _, p := range patterns {
			pv, pd, _ := strings.Cut(strings.ToLower(p), ":")
			if pv == dv && (pd == "*" || pd == dd) {
				return true
			}
		}
	}
	return false
}

func (f *Facts) anyClass(prefixes []string) bool {
	for _, d := range f.PCI {
		for _, p := range prefixes {
			if d.Class != "" && strings.HasPrefix(strings.ToLower(d.Class), strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}
