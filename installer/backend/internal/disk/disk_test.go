package disk

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/fat"
)

const GiB = int64(1<<30) / Sector

type memDev struct{ b []byte }

func (m *memDev) ReadAt(p []byte, off int64) (int, error)  { return copy(p, m.b[off:]), nil }
func (m *memDev) WriteAt(p []byte, off int64) (int, error) { return copy(m.b[off:], p), nil }

func find(opts []Option, kind string) *Option {
	for i := range opts {
		if opts[i].Kind == kind {
			return &opts[i]
		}
	}
	return nil
}

func TestEmptyDiskRecommended(t *testing.T) {
	d := &Disk{Name: "sda", Bus: "sata", Size: 500 * GiB * Sector, Table: TableNone}
	o := Plan([]*Disk{d}, Requirements{})
	if len(o) != 1 || !o[0].Recommended || !o[0].Available || o[0].Sectors != WantSize || len(o[0].Erases) != 0 {
		t.Fatalf("%+v", o)
	}
}

func TestShrinkWindowsOnMBR(t *testing.T) {
	win := &Part{Num: 1, Start: 2048, Sectors: 100 * GiB, Type: 7, FS: "ntfs", OS: "Windows 10/11", Active: true, MinBytes: 40 << 30}
	d := &Disk{Name: "sda", Bus: "sata", Size: 500 * GiB * Sector, Table: TableMBR, Parts: []*Part{win}}
	opts := Plan([]*Disk{d}, Requirements{})
	if !opts[0].Recommended || opts[0].Kind != KindFree {
		t.Fatalf("free space after Windows should be recommended, got %+v", opts[0])
	}
	// The free space is at 100 GB; the new partition must end below 128 GiB.
	if f := opts[0]; f.Start+f.Sectors > MaxEnd || f.Sectors < 27*GiB {
		t.Fatalf("free option %d+%d", f.Start, f.Sectors)
	}
	s := find(opts, KindShrink)
	if s == nil || !s.Available || s.Shrink != win || s.ShrinkTo >= win.Sectors || s.Start+s.Sectors > MaxEnd {
		t.Fatalf("shrink: %+v", s)
	}
	if s.ShrinkTo*Sector < 50<<30 {
		t.Fatalf("Windows shrunk too far: %d", s.ShrinkTo)
	}
	t.Log("\n" + Layout(d, nil) + "->\n" + Layout(d, s))
}

func TestBigWindowsDiskRefusesShrink(t *testing.T) {
	win := &Part{Num: 1, Start: 2048, Sectors: 500*GiB - 4096, Type: 7, FS: "ntfs", OS: "Windows 10/11", MinBytes: 100 << 30}
	d := &Disk{Name: "sda", Bus: "sata", Size: 500 * GiB * Sector, Table: TableMBR, Parts: []*Part{win}}
	opts := Plan([]*Disk{d}, Requirements{})
	s := find(opts, KindShrink)
	if s == nil || s.Available || !strings.Contains(s.Reason, "128 GB") {
		t.Fatalf("%+v", s)
	}
	w := find(opts, KindWholeDisk)
	if w.Recommended || len(w.Erases) != 1 {
		t.Fatalf("erasing must never be recommended: %+v", w)
	}
}

func TestBlocks(t *testing.T) {
	bl := &Part{Num: 1, Start: 2048, Sectors: 60 * GiB, FS: "bitlocker", BitLocker: true}
	d := &Disk{Name: "sda", Bus: "sata", Size: 120 * GiB * Sector, Table: TableMBR, Parts: []*Part{bl}}
	gpt := &Disk{Name: "sdb", Bus: "sata", Size: 1000 * GiB * Sector, Table: TableGPT, Parts: []*Part{{Num: 1, Start: 2048, Sectors: GiB, FS: "ntfs", OS: "Windows 10/11"}}}
	nvme := &Disk{Name: "nvme0n1", Bus: "nvme", Size: 1000 * GiB * Sector, Table: TableNone}
	usb := &Disk{Name: "sdc", Bus: "usb", Size: 64 * GiB * Sector, Table: TableNone}
	opts := Plan([]*Disk{d, gpt, nvme, usb}, Requirements{})
	reasons := ""
	for _, o := range opts {
		if !o.Available {
			reasons += o.Reason + "\n"
		}
	}
	for _, want := range []string{"BitLocker", "GPT", "NVMe", "USB"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("no %s reason in:\n%s", want, reasons)
		}
	}
	nv := Plan([]*Disk{nvme}, Requirements{AllowNVMe: true})
	if !nv[0].Available {
		t.Fatal("AllowNVMe should allow NVMe disks")
	}
}

// Table round trip and signature probing on a real image.
func TestTableAndProbe(t *testing.T) {
	m := &memDev{b: make([]byte, 200<<20)}
	if err := NewTable(m, int64(len(m.b))/Sector, 2048, 80<<11, 0x0C, []byte{0xFA, 0x33}); err != nil {
		t.Fatal(err)
	}
	if err := fat.Format32(m, 2048*Sector, 80<<20, "WIN98"); err != nil {
		t.Fatal(err)
	}
	// A fake NTFS boot sector in a second partition.
	ntfsAt := int64(2048 + 80<<11)
	copy(m.b[ntfsAt*Sector+3:], "NTFS    ")
	if err := AddPartition(m, 1, ntfsAt, 50<<11, 0x07, false); err != nil {
		t.Fatal(err)
	}
	d := &Disk{Name: "img", Size: int64(len(m.b))}
	if err := Inspect(m, d); err != nil {
		t.Fatal(err)
	}
	if d.Table != TableMBR || len(d.Parts) != 2 || d.Parts[0].FS != "fat32" || !d.Parts[0].Active || d.Parts[1].FS != "ntfs" {
		for _, p := range d.Parts {
			t.Logf("%+v", *p)
		}
		t.Fatalf("table %s", d.Table)
	}
	if err := SetActive(m, 1); err != nil {
		t.Fatal(err)
	}
	if err := ResizePartition(m, 1, 40<<11); err != nil {
		t.Fatal(err)
	}
	ReadTable(m, d)
	if d.Parts[0].Active || !d.Parts[1].Active || d.Parts[1].Sectors != 40<<11 || m.b[0] != 0xFA {
		t.Fatalf("after edits: %+v %+v", *d.Parts[0], *d.Parts[1])
	}
	bk, _ := Backup(m, d.Sectors())
	if !bytes.Equal(bk[:Sector], m.b[:Sector]) {
		t.Fatal("backup")
	}
}

func TestWin98Detection(t *testing.T) {
	g := filepath.Join("..", "..", "..", "..", "lab", "verify", "golden.img")
	f, err := os.Open(g)
	if err != nil {
		t.Skip("no lab golden image")
	}
	defer f.Close()
	st, _ := f.Stat()
	d := &Disk{Name: "golden", Size: st.Size()}
	if err := Inspect(f, d); err != nil {
		t.Fatal(err)
	}
	if len(d.Parts) != 1 || d.Parts[0].OS != "Windows 98 SE" {
		t.Fatalf("%+v", d.Parts[0])
	}
}

// A Windows 98 partition carrying 98Reborn Boot's EFI folder is still Windows 98.
func TestWin98WithEFIFolder(t *testing.T) {
	g := filepath.Join("..", "..", "..", "..", "lab", "acpi", "disk.img")
	f, err := os.Open(g)
	if err != nil {
		t.Skip("no ACPI test disk")
	}
	defer f.Close()
	st, _ := f.Stat()
	d := &Disk{Name: "acpi", Size: st.Size()}
	Inspect(f, d)
	if len(d.Parts) != 1 || d.Parts[0].OS != "Windows 98 SE" || d.Parts[0].FS != "fat32" {
		t.Fatalf("%+v", *d.Parts[0])
	}
}
