package scan

import "testing"

// Shape of real Win32_PnPEntity / Get-PhysicalDisk output, as ConvertTo-Json emits it.
const sampleWindows = `{"pci":[{"id":"PCI\\VEN_10DE&DEV_1C03&SUBSYS_37311462&REV_A1\\4&2C3B1F6&0&0008","name":"NVIDIA GeForce GTX 1060 6GB","compat":["PCI\\VEN_10DE&DEV_1C03&REV_A1","PCI\\VEN_10DE&CC_030000","PCI\\CC_030000","PCI\\CC_0300"]},
{"id":"PCI\\VEN_8086&DEV_A282&SUBSYS_7A721462&REV_00\\3&11583659&0&B8","name":"Standard SATA AHCI Controller","compat":["PCI\\VEN_8086&CC_010601","PCI\\CC_010601","PCI\\CC_0106"]}],
"ram":17096982528,"cpu":"Intel(R) Core(TM) i7-8700K CPU @ 3.70GHz","mhz":3701,"arch":9,"fw":2,"sb":true,"os":"Microsoft Windows 11 Pro",
"disks":[{"name":"Samsung SSD 970 EVO 500GB","bus":"NVMe","size":500107862016},{"name":"CT1000MX500SSD1","bus":"SATA","size":1000204886016}]}`

func TestParseWindows(t *testing.T) {
	s, err := ParseWindows([]byte(sampleWindows))
	if err != nil {
		t.Fatal(err)
	}
	if s.Arch != "x86_64" || s.Firmware != "uefi" || s.SecureBoot != "on" || s.RAMMB != 16304 || s.CPUMHz != 3701 {
		t.Fatalf("%+v", s)
	}
	if len(s.PCI) != 2 || s.PCI[0].ID != "10de:1c03" || s.PCI[0].Class != "030000" || s.PCI[1].Class != "010601" {
		t.Fatalf("pci: %+v", s.PCI)
	}
	if len(s.Disks) != 2 || s.Disks[0].Bus != "nvme" || s.Disks[1].Bus != "sata" || s.Disks[1].SizeGB != 1000 {
		t.Fatalf("disks: %+v", s.Disks)
	}
}
