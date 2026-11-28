//go:build linux

package scan

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/win9x-modern/w9xm/internal/compat"
)

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func run() (*compat.Scan, error) {
	s := &compat.Scan{Arch: map[string]string{"amd64": "x86_64", "386": "x86", "arm64": "arm64"}[runtime.GOARCH]}
	if s.Arch == "" {
		s.Arch = runtime.GOARCH
	}
	if r := readTrim("/etc/os-release"); r != "" {
		for _, l := range strings.Split(r, "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				s.OS = strings.Trim(v, `"`)
			}
		}
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k == "model name" && s.CPU == "" {
				s.CPU = v
			}
			if k == "flags" && s.Virtualization == "" {
				s.Virtualization = "off"
				for _, fl := range strings.Fields(v) {
					if fl == "vmx" || fl == "svm" {
						s.Virtualization = "on"
					}
				}
			}
			if k == "cpu MHz" && s.CPUMHz == 0 {
				mhz, _ := strconv.ParseFloat(v, 64)
				s.CPUMHz = int(mhz)
			}
		}
		f.Close()
	}
	if m := readTrim("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); m != "" {
		if khz, err := strconv.Atoi(m); err == nil {
			s.CPUMHz = khz / 1000
		}
	}
	for _, l := range strings.Split(readTrim("/proc/meminfo"), "\n") {
		if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			kb, _ := strconv.Atoi(strings.Fields(v)[0])
			s.RAMMB = kb / 1024
		}
	}
	s.MemAbove4G = memAbove4G()
	// An IOMMU shows up as an ACPI table (Intel DMAR, AMD IVRS) when the
	// firmware has it switched on.
	s.IOMMU = "off"
	for _, t := range []string{"/sys/firmware/acpi/tables/DMAR", "/sys/firmware/acpi/tables/IVRS"} {
		if _, err := os.Stat(t); err == nil {
			s.IOMMU = "on"
		}
	}
	s.Firmware, s.SecureBoot = "bios", "unknown"
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		s.Firmware = "uefi"
		if m, _ := filepath.Glob("/sys/firmware/efi/efivars/SecureBoot-*"); len(m) > 0 {
			if b, err := os.ReadFile(m[0]); err == nil && len(b) > 0 {
				s.SecureBoot = map[bool]string{true: "on", false: "off"}[b[len(b)-1] == 1]
			}
		}
	}
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		v := strings.TrimPrefix(readTrim(d+"/vendor"), "0x")
		id := strings.TrimPrefix(readTrim(d+"/device"), "0x")
		cl := strings.TrimPrefix(readTrim(d+"/class"), "0x")
		if v == "" || id == "" {
			continue
		}
		s.PCI = append(s.PCI, compat.Device{ID: v + ":" + id, Class: cl})
	}
	blocks, _ := filepath.Glob("/sys/block/*")
	for _, b := range blocks {
		name := filepath.Base(b)
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "sr") {
			continue
		}
		link, _ := filepath.EvalSymlinks(b)
		bus := "unknown"
		switch {
		case strings.HasPrefix(name, "nvme"):
			bus = "nvme"
		case strings.Contains(link, "/usb"):
			bus = "usb"
		case strings.Contains(link, "/ata"):
			bus = "sata"
		}
		sectors, _ := strconv.ParseInt(readTrim(b+"/size"), 10, 64)
		model := readTrim(b + "/device/model")
		if model == "" {
			model = name
		}
		s.Disks = append(s.Disks, compat.Disk{Name: model, Bus: bus, SizeGB: int(sectors * 512 / 1e9)})
	}
	return s, nil
}

// memAbove4G reads the firmware's memory map (/sys/firmware/memmap, in the
// firmware's own order): "no" if every range is below 4 GiB, "unsorted" if
// a range below 4 GiB follows one above it, else "yes"; "" if unreadable.
func memAbove4G() string {
	ents, err := os.ReadDir("/sys/firmware/memmap")
	if err != nil || len(ents) == 0 {
		return ""
	}
	type rng struct {
		idx   int
		start uint64
	}
	var rs []rng
	for _, e := range ents {
		i, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := strconv.ParseUint(strings.TrimPrefix(readTrim("/sys/firmware/memmap/"+e.Name()+"/start"), "0x"), 16, 64)
		if err != nil {
			continue
		}
		rs = append(rs, rng{i, st})
	}
	sort.Slice(rs, func(a, b int) bool { return rs[a].idx < rs[b].idx })
	seenHigh, res := false, "no"
	for _, r := range rs {
		switch {
		case r.start >= 1<<32:
			seenHigh = true
			if res == "no" {
				res = "yes"
			}
		case seenHigh:
			res = "unsorted"
		}
	}
	return res
}
