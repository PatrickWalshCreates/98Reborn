package install

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/win9x-modern/w9xm/internal/engine"
	"github.com/win9x-modern/w9xm/internal/module"
)

// Drivers whose INFs match a whole device class get this PC's exact
// devices added. Windows 98 installs a driver silently only when the INF
// lists the device's own hardware ID; a match on the generic class brings up
// the Add New Hardware wizard instead. Devices that one of Windows' own INFs
// already names keep that driver.
var exactIDs = []struct {
	inf, marker, class, line string // line: format with the "VEN_xxxx&DEV_xxxx" ID
}{
	{"WINDOWS/INF/98RVESA.INF", `=VESA, PCI\CC_0300`, "0300", `%%VESAPCI.DeviceDesc%%=VESA, PCI\%s`}, // display-vesa
	{"WINDOWS/INF/XHCI98.INF", `=Xhci.Dev,PCI\CC_0C0330`, "0c0330", `%%XhciDesc%%=Xhci.Dev,PCI\%s`},  // usb3-xhci98
}

// AddExactIDs names this PC's devices in the class-wide driver INFs the
// modules installed.
func AddExactIDs(tgt engine.Target, f *module.Facts) error {
	if f == nil {
		return nil
	}
	var known [][]byte
	names, _ := tgt.ReadDir("WINDOWS/INF")
	for _, e := range exactIDs {
		inf, err := tgt.ReadFile(e.inf)
		if err != nil {
			continue // module not applied
		}
		if known == nil {
			for _, n := range names {
				if !strings.EqualFold(path.Ext(n), ".inf") || isOurs(n) {
					continue
				}
				if b, err := tgt.ReadFile("WINDOWS/INF/" + n); err == nil {
					known = append(known, bytes.ToUpper(b))
				}
			}
		}
		var add []string
		for _, d := range f.PCI {
			if !strings.HasPrefix(strings.ToLower(d.Class), e.class) {
				continue
			}
			v, dev, ok := strings.Cut(strings.ToUpper(d.ID), ":")
			if !ok || len(v) != 4 || len(dev) != 4 {
				continue
			}
			id := "VEN_" + v + "&DEV_" + dev
			if bytes.Contains(bytes.ToUpper(inf), []byte(id)) || hasID(known, id) {
				continue
			}
			add = append(add, fmt.Sprintf(e.line, id))
		}
		if len(add) == 0 {
			continue
		}
		out, ok := insertAfter(inf, e.marker, add)
		if !ok {
			return fmt.Errorf("%s: no %s entry to extend", e.inf, e.marker)
		}
		if err := tgt.WriteFile(e.inf, out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func isOurs(name string) bool {
	for _, e := range exactIDs {
		if strings.EqualFold(name, path.Base(e.inf)) {
			return true
		}
	}
	return false
}

func hasID(infs [][]byte, id string) bool {
	for _, b := range infs {
		if bytes.Contains(b, []byte(id)) {
			return true
		}
	}
	return false
}

// insertAfter adds lines (CRLF) after the first line containing marker.
func insertAfter(b []byte, marker string, lines []string) ([]byte, bool) {
	i := bytes.Index(b, []byte(marker))
	if i < 0 {
		return nil, false
	}
	j := bytes.IndexByte(b[i:], '\n')
	if j < 0 {
		return nil, false
	}
	j += i + 1
	var mid bytes.Buffer
	for _, l := range lines {
		mid.WriteString(l + "\r\n")
	}
	return append(append(append([]byte(nil), b[:j]...), mid.Bytes()...), b[j:]...), true
}
