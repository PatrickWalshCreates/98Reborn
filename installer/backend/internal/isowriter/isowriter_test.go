package isowriter

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/win9x-modern/w9xm/internal/iso9660"
	"github.com/win9x-modern/w9xm/internal/media"
)

func memFile(path string, b []byte) File {
	return File{Path: path, Size: int64(len(b)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
}

// Round trip through our own reader: names, nesting, contents, Joliet long names.
func TestRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte("98Reborn"), 300000) // 2.4 MB, spans many sectors
	files := []File{
		memFile("README.TXT", []byte("hello")),
		memFile("98SE/SETUP.EXE", []byte("MZ fake setup")),
		memFile("98SE/BASE4.CAB", big),
		memFile("boot/isolinux/isolinux.cfg", []byte("default 98reborn")),
		memFile("98reborn/modules/show-file-extensions/module.json", []byte("{}")),
		memFile("Long File Name With Spaces.txt", []byte("joliet")),
		memFile("empty.dat", nil),
	}
	for i := 0; i < 120; i++ { // a directory bigger than one sector
		files = append(files, memFile("many/file"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt", []byte{byte(i)}))
	}
	out := filepath.Join(t.TempDir(), "t.iso")
	fh, _ := os.Create(out)
	if err := Write(fh, Options{VolumeID: "98REBORN", Files: files, Now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	im, err := iso9660.OpenFile(out)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	if !im.Joliet || im.VolumeID != "98REBORN" {
		t.Fatalf("joliet=%v vol=%q", im.Joliet, im.VolumeID)
	}
	for _, f := range files {
		want, _ := io.ReadAll(must(f.Open()))
		got, err := im.ReadFile(f.Path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v (got %d bytes, want %d)", f.Path, err, len(got), len(want))
		}
	}
	ents, _ := im.ReadDir("many")
	if len(ents) != 120 {
		t.Fatalf("many/ has %d entries", len(ents))
	}
}

func must(r io.ReadCloser, err error) io.ReadCloser {
	if err != nil {
		panic(err)
	}
	return r
}

// Rebuild the user's 98 SE CD (if present) with its floppy-emulation boot image.
// The result is checked here and, by the caller, booted in QEMU.
func TestRemaster98SE(t *testing.T) {
	src := filepath.Join("..", "..", "..", "..", "ISOs", "Windows 98 Second Edition.iso")
	if _, err := os.Stat(src); err != nil || testing.Short() {
		t.Skip("98 SE ISO not present (or -short)")
	}
	im, err := iso9660.OpenFile(src)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	var files []File
	im.Walk(func(e iso9660.Entry) error {
		if !e.IsDir {
			e := e
			files = append(files, File{Path: e.Path, Size: e.Size, ModTime: e.ModTime, Open: func() (io.ReadCloser, error) {
				sr, err := im.OpenEntry(e)
				return io.NopCloser(sr), err
			}})
		}
		return nil
	})
	boot, _, err := im.ReadBootImage()
	if err != nil {
		t.Fatal(err)
	}
	f := memFile("[BOOT]/floppy.img", boot)
	f.Hidden = true
	files = append(files, f)

	out := os.Getenv("REMASTER_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "98se-remaster.iso")
	}
	fh, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	err = Write(fh, Options{VolumeID: "WIN98 SE", Files: files, BIOS: &BootEntry{Path: "[BOOT]/floppy.img", Emulation: "floppy144"}})
	fh.Close()
	if err != nil {
		t.Fatal(err)
	}

	m, err := media.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Product != media.Win98SE {
		t.Fatalf("remastered disc detected as %q", m.Product)
	}
	orig, _ := media.Open(src)
	defer orig.Close()
	if m.SetupSHA256 != orig.SetupSHA256 || len(m.Files) != len(orig.Files) {
		t.Fatalf("setup sha %v, %d vs %d cabinet files", m.SetupSHA256 == orig.SetupSHA256, len(m.Files), len(orig.Files))
	}
	a, err1 := m.ReadFile("KERNEL32.DLL")
	b, err2 := orig.ReadFile("KERNEL32.DLL")
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatal("KERNEL32.DLL differs after remaster")
	}
	nb, _, err := m.ISO.ReadBootImage()
	if err != nil || !bytes.Equal(nb, boot) {
		t.Fatalf("boot image: %v", err)
	}
	t.Logf("remastered %d files to %s", len(files), out)
}
