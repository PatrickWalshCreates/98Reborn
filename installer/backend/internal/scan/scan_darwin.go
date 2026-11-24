//go:build darwin

package scan

import (
	"os/exec"
	"strconv"
	"strings"

	"github.com/win9x-modern/w9xm/internal/compat"
)

func sysctl(name string) string {
	out, _ := exec.Command("sysctl", "-n", name).Output()
	return strings.TrimSpace(string(out))
}

// run checks a Mac. Apple Silicon can't run Windows 98 at all; Intel Macs are
// reported with what macOS shows without admin rights (no PCI device IDs).
func run() (*compat.Scan, error) {
	s := &compat.Scan{OS: "macOS " + strings.TrimSpace(func() string { b, _ := exec.Command("sw_vers", "-productVersion").Output(); return string(b) }()),
		Firmware: "uefi", SecureBoot: "unknown"}
	// An x86 build running under Rosetta still reports the real CPU here.
	if sysctl("hw.optional.arm64") == "1" || sysctl("sysctl.proc_translated") == "1" {
		s.Arch = "arm64"
	} else {
		s.Arch = "x86_64"
	}
	s.CPU = sysctl("machdep.cpu.brand_string")
	if hz, err := strconv.ParseInt(sysctl("hw.cpufrequency"), 10, 64); err == nil {
		s.CPUMHz = int(hz / 1e6)
	}
	if b, err := strconv.ParseInt(sysctl("hw.memsize"), 10, 64); err == nil {
		s.RAMMB = int(b >> 20)
	}
	return s, nil
}
