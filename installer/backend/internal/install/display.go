package install

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"github.com/win9x-modern/w9xm/internal/engine"
	"github.com/win9x-modern/w9xm/internal/module"
)

// vesaINF is installed by the display-vesa module.
const vesaINF = "WINDOWS/INF/98RVESA.INF"

// AddDisplayIDs names this PC's graphics adapters in the VESA driver's INF.
// Windows 98 installs a driver silently only when the INF lists the device's
// own hardware ID; a match on the generic class (PCI\CC_0300) brings up the
// Add New Hardware wizard instead. Adapters that one of Windows' own INFs
// already names keep that driver.
func AddDisplayIDs(tgt engine.Target, f *module.Facts) error {
	if f == nil {
		return nil
	}
	inf, err := tgt.ReadFile(vesaINF)
	if err != nil {
		return nil // module not applied
	}
	var known [][]byte
	names, _ := tgt.ReadDir("WINDOWS/INF")
	for _, n := range names {
		if !strings.EqualFold(path.Ext(n), ".inf") || strings.EqualFold(n, path.Base(vesaINF)) {
			continue
		}
		if b, err := tgt.ReadFile("WINDOWS/INF/" + n); err == nil {
			known = append(known, bytes.ToUpper(b))
		}
	}
	var add []string
	for _, d := range f.PCI {
		if !strings.HasPrefix(d.Class, "0300") {
			continue
		}
		v, dev, ok := strings.Cut(strings.ToUpper(d.ID), ":")
		if !ok || len(v) != 4 || len(dev) != 4 {
			continue
		}
		id := "VEN_" + v + "&DEV_" + dev
		if bytes.Contains(inf, []byte(id)) || hasID(known, id) {
			continue
		}
		add = append(add, `%VESAPCI.DeviceDesc%=VESA, PCI\`+id)
	}
	if len(add) == 0 {
		return nil
	}
	out, ok := insertAfter(inf, `=VESA, PCI\CC_0300`, add)
	if !ok {
		return fmt.Errorf("%s: no PCI\\CC_0300 entry to extend", vesaINF)
	}
	return tgt.WriteFile(vesaINF, out, 0o644)
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
