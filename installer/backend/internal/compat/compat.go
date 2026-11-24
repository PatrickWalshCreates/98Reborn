// Package compat grades a PC for 98Reborn (Gold / Silver / Bronze / In
// development / Not supported) and explains why, from a hardware scan.
package compat

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/win9x-modern/w9xm/internal/module"
)

// Tier, best to worst.
type Tier int

const (
	None Tier = iota // Not supported
	Dev              // In development (e.g. NVMe-only)
	Bronze
	Silver
	Gold
)

var tierNames = map[Tier]string{Gold: "Gold", Silver: "Silver", Bronze: "Bronze", Dev: "In development", None: "Not supported"}
var tierKeys = map[string]Tier{"gold": Gold, "silver": Silver, "bronze": Bronze, "dev": Dev, "none": None}

func (t Tier) String() string { return tierNames[t] }

func (t Tier) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

var tierMeaning = map[Tier]string{
	Gold:   "Everything should work, including hardware 3D.",
	Silver: "A stable Windows 98 desktop; some devices or 3D won't be available.",
	Bronze: "Should boot with workarounds; expect limits.",
	Dev:    "Not installable yet; support for this machine is being built.",
	None:   "This PC can't run Windows 98.",
}

// Disk is a physical disk the PC has.
type Disk struct {
	Name   string `json:"name"`
	Bus    string `json:"bus"` // nvme, sata, ide, usb, scsi, raid, unknown
	SizeGB int    `json:"size_gb"`
}

// Device is a PCI device; Name is optional (from the OS).
type Device struct {
	ID    string `json:"id"`    // vvvv:dddd
	Class string `json:"class"` // ccsspp hex, e.g. 010601
	Name  string `json:"name,omitempty"`
}

// Scan is everything the scanner collected.
type Scan struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"` // x86_64, x86, arm64
	CPU        string `json:"cpu"`
	CPUMHz     int    `json:"cpu_mhz"`
	RAMMB      int    `json:"ram_mb"`
	Firmware   string `json:"firmware"`    // bios (legacy boot) | uefi | unknown
	SecureBoot string `json:"secure_boot"` // on | off | unknown
	// Hardware virtualization (VT-x/AMD-V) and IOMMU (VT-d/AMD-Vi), as the
	// firmware has them set: on | off | "" (unknown). 98Reborn Boot's best
	// BIOS layer (ReBIOS) needs both on UEFI-only PCs.
	Virtualization string   `json:"virtualization,omitempty"`
	IOMMU          string   `json:"iommu,omitempty"`
	PCI            []Device `json:"pci"`
	Disks          []Disk   `json:"disks"`
}

// Facts converts a scan into the engine's hardware facts (for --facts).
func (s *Scan) Facts() *module.Facts {
	f := &module.Facts{RAMMB: s.RAMMB, CPUMHz: s.CPUMHz, Extra: map[string]string{"firmware": s.Firmware}}
	for _, d := range s.PCI {
		f.PCI = append(f.PCI, module.PCIDevice{ID: d.ID, Class: d.Class, Name: d.Name})
	}
	return f
}

type Finding struct {
	Area   string `json:"area"`
	Level  string `json:"level"` // ok | info | warn | block
	Cap    Tier   `json:"cap"`
	Device string `json:"device,omitempty"`
	Reason string `json:"reason"`
	Fix    string `json:"fix,omitempty"`
}

type Report struct {
	Tier     Tier      `json:"tier"`
	Meaning  string    `json:"meaning"`
	Findings []Finding `json:"findings"`
	Scan     *Scan     `json:"scan"`
}

type rule struct {
	Match  string `json:"match"`
	Area   string `json:"area"`
	Level  string `json:"level"`
	Cap    string `json:"cap"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

//go:embed rules.json
var rulesJSON []byte

var rules []rule

func init() {
	var doc struct {
		PCI []rule `json:"pci"`
	}
	if err := json.Unmarshal(rulesJSON, &doc); err != nil {
		panic("compat: bad rules.json: " + err.Error())
	}
	for _, r := range doc.PCI {
		if _, ok := tierKeys[r.Cap]; !ok {
			panic("compat: rules.json: bad cap " + r.Cap)
		}
	}
	rules = doc.PCI
}

func hex16(s string) (int, bool) {
	v, err := strconv.ParseUint(s, 16, 16)
	return int(v), err == nil
}

func (r rule) matches(d Device) bool {
	kind, val, _ := strings.Cut(r.Match, ":")
	id := strings.ToLower(d.ID)
	switch kind {
	case "class":
		return d.Class != "" && strings.HasPrefix(strings.ToLower(d.Class), strings.ToLower(val))
	case "vendor":
		v, _, _ := strings.Cut(id, ":")
		return v == strings.ToLower(val)
	case "id":
		vend, devs, _ := strings.Cut(strings.ToLower(val), ":")
		dv, dd, _ := strings.Cut(id, ":")
		if vend != dv {
			return false
		}
		lo, hi, isRange := strings.Cut(devs, "-")
		if !isRange {
			return dd == lo
		}
		d0, ok1 := hex16(dd)
		l, ok2 := hex16(lo)
		h, ok3 := hex16(hi)
		return ok1 && ok2 && ok3 && d0 >= l && d0 <= h
	}
	return false
}

func (d Device) label() string {
	if d.Name != "" {
		return fmt.Sprintf("%s [%s]", d.Name, d.ID)
	}
	return "PCI " + d.ID
}

// Evaluate grades a scan.
func Evaluate(s *Scan) *Report {
	var fs []Finding
	add := func(f Finding) { fs = append(fs, f) }

	// Architecture first: nothing else matters on ARM.
	if a := strings.ToLower(s.Arch); strings.Contains(a, "arm") || strings.Contains(a, "aarch") {
		add(Finding{Area: "CPU", Level: "block", Cap: None, Reason: "ARM processor: Windows 98 only runs on x86 PCs."})
		return finish(s, fs)
	}

	hasEHCI, hasXHCI := false, false
	hasSATA, hasNVMeCtl := false, false
	gpuSeen := false
	for _, d := range s.PCI {
		c := strings.ToLower(d.Class)
		switch {
		case strings.HasPrefix(c, "0c0320"), strings.HasPrefix(c, "0c0310"), strings.HasPrefix(c, "0c0300"):
			hasEHCI = true
		case strings.HasPrefix(c, "0c0330"):
			hasXHCI = true
		case strings.HasPrefix(c, "0101"), strings.HasPrefix(c, "0106"):
			hasSATA = true
		case strings.HasPrefix(c, "0108"):
			hasNVMeCtl = true
		}
		for _, r := range rules {
			if r.matches(d) {
				if r.Area == "Graphics" && strings.HasPrefix(c, "03") && !strings.HasPrefix(c, "0380") {
					gpuSeen = true
				}
				add(Finding{Area: r.Area, Level: r.Level, Cap: tierKeys[r.Cap], Device: d.label(), Reason: r.Reason, Fix: r.Fix})
				break
			}
		}
	}
	if !gpuSeen {
		add(Finding{Area: "Graphics", Level: "info", Cap: Silver, Reason: "No graphics adapter was detected: Windows 98 will use the generic VESA driver."})
	}

	// Firmware / boot mode.
	switch s.Firmware {
	case "bios":
		add(Finding{Area: "Firmware", Level: "ok", Cap: Gold, Reason: "This PC is booting in Legacy/CSM mode, which Windows 98 needs."})
	case "uefi":
		switch {
		case s.Virtualization == "on" && s.IOMMU == "on":
			add(Finding{Area: "Firmware", Level: "ok", Cap: Silver,
				Reason: "UEFI PC with VT-x/AMD-V and an IOMMU: 98Reborn Boot gives Windows 98 a full legacy BIOS (ReBIOS), with ACPI and VGA even without CSM.",
				Fix:    "If the firmware also offers 'CSM' or 'Legacy boot', that works too."})
		case s.Virtualization == "off" || s.IOMMU == "off":
			add(Finding{Area: "Firmware", Level: "warn", Cap: Bronze,
				Reason: "UEFI PC without hardware virtualization or an IOMMU switched on: 98Reborn Boot falls back to a simpler BIOS layer (CSMWrap) that works on fewer PCs.",
				Fix:    "Turn on 'Intel VT-x / AMD-V (SVM)' and 'VT-d / AMD-Vi (IOMMU)' in the firmware setup, or 'CSM / Legacy boot' if offered."})
		default:
			add(Finding{Area: "Firmware", Level: "warn", Cap: Silver,
				Reason: "This PC boots in UEFI mode. 98Reborn Boot provides the legacy BIOS Windows 98 needs; it works best with VT-x/AMD-V and an IOMMU (VT-d/AMD-Vi) switched on.",
				Fix:    "Check the firmware setup for 'Virtualization' and 'VT-d / IOMMU' (or 'CSM / Legacy boot')."})
		}
	default:
		add(Finding{Area: "Firmware", Level: "info", Cap: Silver, Reason: "Boot mode unknown: check the BIOS for 'CSM' or 'Legacy boot'."})
	}
	if s.SecureBoot == "on" {
		add(Finding{Area: "Firmware", Level: "warn", Cap: Gold, Reason: "Secure Boot is on.", Fix: "Turn Secure Boot off in the BIOS before installing (it's a setting, not a limitation)."})
	}

	// Where can Windows 98 live?
	var sata, nvme []string
	for _, d := range s.Disks {
		switch d.Bus {
		case "sata", "ide", "ata":
			sata = append(sata, fmt.Sprintf("%s (%d GB)", d.Name, d.SizeGB))
		case "nvme":
			nvme = append(nvme, fmt.Sprintf("%s (%d GB)", d.Name, d.SizeGB))
		}
	}
	switch {
	case len(sata) > 0:
		add(Finding{Area: "Disk", Level: "ok", Cap: Gold, Reason: "SATA disk available for Windows 98: " + strings.Join(sata, ", ") + "."})
		if len(nvme) > 0 {
			add(Finding{Area: "Disk", Level: "info", Cap: Gold, Reason: "Your NVMe drive won't be visible to Windows 98 until NVMe support ships; install to the SATA disk."})
		}
	case len(nvme) > 0 || hasNVMeCtl:
		fix := "Add a SATA SSD"
		if !hasSATA {
			fix = "This PC has no SATA controller, so NVMe support is required"
		}
		add(Finding{Area: "Disk", Level: "block", Cap: Dev,
			Reason: "Only NVMe storage was found. A Windows 98 NVMe driver is being built by 98Reborn but isn't ready yet.",
			Fix:    fix + ". Follow the project for NVMe support."})
	case len(s.Disks) == 0:
		add(Finding{Area: "Disk", Level: "info", Cap: Silver, Reason: "The scanner couldn't list disks; make sure a SATA disk is available."})
	default:
		add(Finding{Area: "Disk", Level: "block", Cap: Dev, Reason: "No internal SATA or NVMe disk was found to install on."})
	}

	// USB: keyboard and mouse.
	if hasXHCI && !hasEHCI {
		add(Finding{Area: "USB", Level: "warn", Cap: Silver,
			Reason: "USB 3 only: Windows 98 has no USB 3 driver yet. USB keyboards and mice will rely on the BIOS's legacy USB support; USB drives won't work inside Windows 98.",
			Fix:    "Make sure 'Legacy USB support' is enabled in the BIOS, or use a PS/2 keyboard and mouse if the board has the port."})
	}

	// Memory.
	switch {
	case s.RAMMB > 1024:
		add(Finding{Area: "Memory", Level: "info", Cap: Gold, Reason: fmt.Sprintf("%d GB of RAM: Windows 98 will be set to use 1 GB, which is normal and stable.", (s.RAMMB+512)/1024)})
	case s.RAMMB > 0 && s.RAMMB < 64:
		add(Finding{Area: "Memory", Level: "block", Cap: None, Reason: "Less than 64 MB of RAM."})
	}
	if s.CPU != "" {
		add(Finding{Area: "CPU", Level: "ok", Cap: Gold, Reason: s.CPU + ": compatible (fast-CPU fixes are applied automatically; Windows 98 uses one core)."})
	}
	return finish(s, fs)
}

var levelOrder = map[string]int{"block": 0, "warn": 1, "info": 2, "ok": 3}

func finish(s *Scan, fs []Finding) *Report {
	t := Gold
	for _, f := range fs {
		if f.Cap < t {
			t = f.Cap
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return levelOrder[fs[i].Level] < levelOrder[fs[j].Level] })
	return &Report{Tier: t, Meaning: tierMeaning[t], Findings: fs, Scan: s}
}

// Text renders the report for people.
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "98Reborn compatibility: %s\n%s\n", strings.ToUpper(r.Tier.String()), r.Meaning)
	if r.Scan.CPU != "" || r.Scan.RAMMB > 0 {
		boot := map[string]string{"bios": "Legacy/CSM boot", "uefi": "UEFI boot"}[r.Scan.Firmware]
		if boot == "" {
			boot = "boot mode unknown"
		}
		fmt.Fprintf(&b, "\nThis PC: %s, %d GB RAM, %s, %s\n", r.Scan.CPU, (r.Scan.RAMMB+512)/1024, boot, r.Scan.OS)
	}
	marks := map[string]string{"block": "[X]", "warn": "[!]", "info": "[i]", "ok": "[ok]"}
	heads := map[string]string{"block": "\nWhat stops it:\n", "warn": "\nWhat holds it back:\n", "info": "\nGood to know:\n", "ok": "\nWhat's fine:\n"}
	last := ""
	for _, f := range r.Findings {
		if f.Level != last {
			b.WriteString(heads[f.Level])
			last = f.Level
		}
		fmt.Fprintf(&b, "  %-4s %s: %s\n", marks[f.Level], f.Area, f.Reason)
		if f.Device != "" {
			fmt.Fprintf(&b, "       Device: %s\n", f.Device)
		}
		if f.Fix != "" {
			fmt.Fprintf(&b, "       What you can do: %s\n", f.Fix)
		}
	}
	return b.String()
}
