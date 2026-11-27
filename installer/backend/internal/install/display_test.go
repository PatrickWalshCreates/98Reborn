package install

import (
	"strings"
	"testing"
)

func TestInsertAfter(t *testing.T) {
	in := []byte("[Mfg.VM]\r\n%VESAPCI.DeviceDesc%=VESA, PCI\\CC_0300\r\n\r\n[VESA]\r\n")
	out, ok := insertAfter(in, `=VESA, PCI\CC_0300`, []string{`%VESAPCI.DeviceDesc%=VESA, PCI\VEN_8086&DEV_0166`})
	if !ok {
		t.Fatal("marker not found")
	}
	want := "PCI\\CC_0300\r\n%VESAPCI.DeviceDesc%=VESA, PCI\\VEN_8086&DEV_0166\r\n\r\n[VESA]"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got %q", out)
	}
}
