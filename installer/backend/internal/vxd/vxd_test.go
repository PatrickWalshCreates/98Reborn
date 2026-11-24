package vxd

import (
	"os"
	"path/filepath"
	"testing"
)

// Needs a real VMM32.VXD (Windows 98 SE, from an installed system), which
// can't be committed: VXD_TEST=path/to/VMM32.VXD go test ./internal/vxd
func TestRealVMM32(t *testing.T) {
	p := os.Getenv("VXD_TEST")
	if p == "" {
		p = filepath.Join("..", "..", "..", "..", "lab", "boot", "vmm32.vxd")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Skip("no VMM32.VXD")
	}
	c, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d drivers, W3 size %d", len(c.Entries), len(c.w3))
	for _, e := range c.Entries {
		img, err := c.Extract(e.Name)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if string(img[128:130]) != "LE" {
			t.Fatalf("%s: no LE header", e.Name)
		}
	}
	vmm, _ := c.Extract("VMM")
	t.Logf("VMM.VXD: %d bytes", len(vmm))
	if os.Getenv("VXD_OUT") != "" {
		os.WriteFile(os.Getenv("VXD_OUT"), vmm, 0o644)
	}
}
