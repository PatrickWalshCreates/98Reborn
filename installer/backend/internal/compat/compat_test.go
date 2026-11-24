package compat

import (
	"strings"
	"testing"
)

func has(r *Report, area, level, text string) bool {
	for _, f := range r.Findings {
		if f.Area == area && f.Level == level && strings.Contains(f.Reason+f.Fix+f.Device, text) {
			return true
		}
	}
	return false
}

func TestTiers(t *testing.T) {
	cases := []struct {
		name string
		scan Scan
		want Tier
		why  []string // area:level:text that must appear
	}{
		{
			name: "period-friendly desktop",
			scan: Scan{Arch: "x86_64", CPU: "Core 2 Duo", RAMMB: 4096, Firmware: "bios",
				PCI: []Device{{ID: "10de:0391", Class: "030000"}, {ID: "8086:27c0", Class: "010180"}, {ID: "8086:27cc", Class: "0c0320"},
					{ID: "8086:107c", Class: "020000"}, {ID: "8086:27d8", Class: "040300"}, {ID: "1102:0008", Class: "040100"}},
				Disks: []Disk{{Name: "WD Blue", Bus: "sata", SizeGB: 500}}},
			// HD Audio caps at Silver even with a Creative card present.
			want: Silver,
			why:  []string{"Graphics:ok:NVIDIA", "Storage:ok:IDE", "Memory:info:1 GB", "Audio:warn:HD Audio"},
		},
		{
			name: "gold build",
			scan: Scan{Arch: "x86_64", Firmware: "bios",
				PCI:   []Device{{ID: "10de:0391", Class: "030000"}, {ID: "8086:27c0", Class: "010180"}, {ID: "10ec:8139", Class: "020000"}, {ID: "1102:0008", Class: "040100"}, {ID: "8086:27cc", Class: "0c0320"}},
				Disks: []Disk{{Name: "SSD", Bus: "sata", SizeGB: 120}}},
			want: Gold,
		},
		{
			name: "2018 gaming desktop booted UEFI",
			scan: Scan{Arch: "x86_64", Firmware: "uefi", SecureBoot: "on",
				PCI:   []Device{{ID: "10de:1c03", Class: "030000"}, {ID: "8086:a282", Class: "010601"}, {ID: "10ec:8168", Class: "020000"}, {ID: "8086:a2af", Class: "0c0330"}},
				Disks: []Disk{{Name: "Samsung 970", Bus: "nvme", SizeGB: 500}, {Name: "Crucial MX500", Bus: "sata", SizeGB: 1000}}},
			want: Silver,
			why:  []string{"Graphics:warn:VESA", "Storage:warn:AHCI", "Firmware:warn:CSM", "Firmware:warn:Secure Boot", "Disk:ok:Crucial", "USB:warn:USB 3 only"},
		},
		{
			name: "new NVMe-only laptop",
			scan: Scan{Arch: "x86_64", Firmware: "uefi",
				PCI:   []Device{{ID: "8086:a7a0", Class: "030000"}, {ID: "144d:a80a", Class: "010802"}, {ID: "8086:51f0", Class: "028000"}, {ID: "8086:51ed", Class: "0c0330"}},
				Disks: []Disk{{Name: "Samsung PM9A1", Bus: "nvme", SizeGB: 1000}}},
			want: Dev,
			why:  []string{"Disk:block:Only NVMe", "Network:info:Wi-Fi"},
		},
		{
			name: "intel RST raid mode",
			scan: Scan{Arch: "x86_64", Firmware: "bios", PCI: []Device{{ID: "8086:2822", Class: "010400"}}, Disks: []Disk{{Bus: "raid"}}},
			want: Dev,
			why:  []string{"Storage:block:RAID"},
		},
		{
			name: "arm",
			scan: Scan{Arch: "arm64"},
			want: None,
		},
	}
	for _, c := range cases {
		r := Evaluate(&c.scan)
		if r.Tier != c.want {
			t.Errorf("%s: tier %s, want %s\n%s", c.name, r.Tier, c.want, r.Text())
			continue
		}
		for _, w := range c.why {
			p := strings.SplitN(w, ":", 3)
			if !has(r, p[0], p[1], p[2]) {
				t.Errorf("%s: missing finding %s\n%s", c.name, w, r.Text())
			}
		}
	}
}

func TestRuleOrderSpecificBeforeClass(t *testing.T) {
	r := Evaluate(&Scan{Arch: "x86_64", Firmware: "bios", PCI: []Device{{ID: "10ec:8139", Class: "020000"}}, Disks: []Disk{{Bus: "sata"}}})
	if !has(r, "Network", "ok", "RTL8139") || has(r, "Network", "warn", "") {
		t.Fatalf("specific NIC rule should win over the class rule:\n%s", r.Text())
	}
}
