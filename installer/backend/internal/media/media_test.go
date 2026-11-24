package media

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/win9x-modern/w9xm/internal/pe"
)

// These tests run against the user's own CD images in <repo>/ISOs (never
// committed). They skip when the images aren't present, e.g. in CI.
func openISO(t *testing.T, name string) *Media {
	t.Helper()
	p := filepath.Join("..", "..", "..", "..", "ISOs", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s not present", name)
	}
	m, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func checkPE(t *testing.T, m *Media, names ...string) {
	t.Helper()
	for _, n := range names {
		b, err := m.ReadFile(n)
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		stored, computed, ok := pe.Checksum(b)
		if ok && stored != computed {
			t.Errorf("%s: PE checksum %08x, computed %08x (decompression error?)", n, stored, computed)
		}
	}
}

// sweep extracts every setup file and requires the PE checksum pass rate seen
// on real media (a handful of vendor drivers ship with stale checksums).
func sweep(t *testing.T, m *Media) {
	if testing.Short() {
		t.Skip("full extraction skipped in -short mode")
	}
	good, bad := 0, 0
	var total int64
	err := Extract(m.Find(nil, true), func(f *File, b []byte) error {
		total += int64(len(b))
		if s, c, ok := pe.Checksum(b); ok {
			if s == c {
				good++
			} else {
				bad++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if good < 1000 || bad*20 > good {
		t.Fatalf("PE checksums: %d ok, %d bad", good, bad)
	}
	t.Logf("%s: %.0f MB extracted, PE checksums %d ok / %d stale", m.Product, float64(total)/1e6, good, bad)
}

func TestWin98SE(t *testing.T) {
	m := openISO(t, "Windows 98 Second Edition.iso")
	if m.Product != Win98SE || m.Key() != "98se" {
		t.Fatalf("product %q", m.Product)
	}
	if n := len(m.Find(nil, true)); n < 6000 {
		t.Fatalf("only %d setup files indexed", n)
	}
	checkPE(t, m, "KERNEL32.DLL", "EXPLORER.EXE", "REGEDIT.EXE", "USER32.DLL", "SHELL32.DLL")
	if b, err := m.ReadFile("SETUPPP.INF"); err != nil || len(b) == 0 {
		t.Errorf("SETUPPP.INF: %v", err)
	}
	sweep(t, m)
}

func TestWinMe(t *testing.T) {
	m := openISO(t, "Windows Me (115 - OEM Full).iso")
	if m.Product != WinMe || m.Key() != "me" {
		t.Fatalf("product %q", m.Product)
	}
	checkPE(t, m, "KERNEL32.DLL", "EXPLORER.EXE", "USBSTOR.SYS", "USBHUB.SYS")
	sweep(t, m)
}
