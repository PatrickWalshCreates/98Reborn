// Package scan collects hardware facts on the PC that will run 98Reborn
// (under its current Windows or Linux), without admin rights or network.
package scan

import (
	"encoding/json"
	"regexp"

	"strings"

	"github.com/win9x-modern/w9xm/internal/compat"
)

// Run scans this machine.
func Run() (*compat.Scan, error) { return run() }

var (
	pciIDRe    = regexp.MustCompile(`(?i)PCI\\VEN_([0-9A-F]{4})&DEV_([0-9A-F]{4})`)
	pciClassRe = regexp.MustCompile(`(?i)PCI\\CC_([0-9A-F]{4,6})`)
)

// windowsJSON is the shape of the PowerShell query in scan_windows.go.
type windowsJSON struct {
	PCI []struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Compat []string `json:"compat"`
	} `json:"pci"`
	RAM        float64 `json:"ram"`
	CPU        string  `json:"cpu"`
	MHz        int     `json:"mhz"`
	Arch       int     `json:"arch"` // Win32_Processor.Architecture: 0 x86, 9 x64, 12 ARM64
	FW         int     `json:"fw"`   // PEFirmwareType: 1 BIOS, 2 UEFI
	SecureBoot *bool   `json:"sb"`
	OS         string  `json:"os"`
	Disks      []struct {
		Name string  `json:"name"`
		Bus  string  `json:"bus"`
		Size float64 `json:"size"`
	} `json:"disks"`
}

// ParseWindows converts the PowerShell scan output (exported for tests).
func ParseWindows(b []byte) (*compat.Scan, error) {
	var w windowsJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	s := &compat.Scan{OS: strings.TrimSpace(w.OS), CPU: strings.TrimSpace(w.CPU), CPUMHz: w.MHz, RAMMB: int(w.RAM / (1 << 20))}
	s.Arch = map[int]string{0: "x86", 9: "x86_64", 12: "arm64", 5: "arm"}[w.Arch]
	s.Firmware = map[int]string{1: "bios", 2: "uefi"}[w.FW]
	if s.Firmware == "" {
		s.Firmware = "unknown"
	}
	s.SecureBoot = "unknown"
	if w.SecureBoot != nil {
		s.SecureBoot = map[bool]string{true: "on", false: "off"}[*w.SecureBoot]
	}
	seen := map[string]bool{}
	for _, p := range w.PCI {
		m := pciIDRe.FindStringSubmatch(p.ID)
		if m == nil {
			continue
		}
		d := compat.Device{ID: strings.ToLower(m[1] + ":" + m[2]), Name: strings.TrimSpace(p.Name)}
		for _, c := range p.Compat {
			if cm := pciClassRe.FindStringSubmatch(c); cm != nil && len(cm[1]) > len(d.Class) {
				d.Class = strings.ToLower(cm[1])
			}
		}
		key := d.ID + "/" + d.Class + "/" + d.Name
		if !seen[key] {
			seen[key] = true
			s.PCI = append(s.PCI, d)
		}
	}
	for _, d := range w.Disks {
		bus := strings.ToLower(d.Bus)
		switch bus {
		case "sata", "ata", "nvme", "usb", "raid", "scsi", "sas":
		default:
			bus = "unknown"
		}
		s.Disks = append(s.Disks, compat.Disk{Name: strings.TrimSpace(d.Name), Bus: bus, SizeGB: int(d.Size / 1e9)})
	}
	return s, nil
}
