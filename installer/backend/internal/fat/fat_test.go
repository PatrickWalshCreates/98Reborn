package fat

import (
	"bytes"
	"errors"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memDev is an in-memory Device.
type memDev struct{ b []byte }

func (m *memDev) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.b)) {
		return 0, errors.New("EOF")
	}
	n := copy(p, m.b[off:])
	return n, nil
}

func (m *memDev) WriteAt(p []byte, off int64) (int, error) {
	if need := off + int64(len(p)); need > int64(len(m.b)) {
		m.b = append(m.b, make([]byte, need-int64(len(m.b)))...)
	}
	return copy(m.b[off:], p), nil
}

// newDisk makes a partitioned FAT32 disk like the installer will.
func newDisk(t testing.TB, size int64) (*memDev, *FS) {
	t.Helper()
	d := &memDev{b: make([]byte, size)}
	const start = 63
	if err := WriteMBR(d, []Partition{{true, 0x0C, start, uint32(size/sector) - start}}); err != nil {
		t.Fatal(err)
	}
	if err := Format32(d, start*sector, size-start*sector, "WIN98"); err != nil {
		t.Fatal(err)
	}
	f, err := OpenImage(d)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != 32 {
		t.Fatalf("type %d", f.Type)
	}
	return d, f
}

func TestFAT32RoundTrip(t *testing.T) {
	d, f := newDisk(t, 64<<20)
	r := rand.New(rand.NewSource(1))
	big := make([]byte, 3<<20+123)
	r.Read(big)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.MkdirAll("WINDOWS/SYSTEM"))
	must(f.WriteFile("WINDOWS/SYSTEM.INI", []byte("[boot]\r\n"), time.Time{}))
	must(f.WriteFile("WINDOWS/SYSTEM/BIG.BIN", big, time.Time{}))
	must(f.MkdirAll("Program Files/Internet Explorer"))
	must(f.WriteFile("Program Files/Internet Explorer/readme with spaces.txt", []byte("lfn"), time.Time{}))
	must(f.WriteFile("journal.json", []byte("{}"), time.Time{}))
	// Many entries force the directory to grow past one cluster.
	for i := 0; i < 300; i++ {
		must(f.WriteFile("WINDOWS/long file name number "+strings.Repeat("x", i%40)+string(rune('a'+i%26))+".dat", []byte{byte(i)}, time.Time{}))
	}
	must(f.Sync())

	f2, err := OpenImage(d) // fresh view from the bytes on "disk"
	must(err)
	got, err := f2.ReadFile("windows/system/big.bin")
	must(err)
	if !bytes.Equal(got, big) {
		t.Fatal("big file mismatch")
	}
	if b, _ := f2.ReadFile("PROGRAM FILES/internet explorer/README WITH SPACES.TXT"); string(b) != "lfn" {
		t.Fatalf("lfn file: %q", b)
	}
	e, err := f2.Stat("Program Files")
	must(err)
	if e.Name != "Program Files" || e.ShortName != "PROGRA~1" {
		t.Fatalf("names: %q %q", e.Name, e.ShortName)
	}
	if e, _ := f2.Stat("journal.json"); e.ShortName != "JOURNAL~1.JSO" && e.ShortName != "JOURNA~1.JSO" {
		t.Fatalf("alias %q", e.ShortName)
	}
	ents, err := f2.ReadDir("WINDOWS")
	must(err)
	if len(ents) != 302 {
		t.Fatalf("WINDOWS has %d entries", len(ents))
	}

	// Overwrite with a smaller file, set read-only, delete, re-create.
	must(f2.WriteFile("WINDOWS/SYSTEM/BIG.BIN", []byte("small"), time.Time{}))
	must(f2.SetAttr("WINDOWS/SYSTEM.INI", AttrReadOnly|AttrHidden|AttrSystem))
	must(f2.WriteFile("WINDOWS/SYSTEM.INI", []byte("[boot]\r\nx=1\r\n"), time.Time{}))
	if e, _ := f2.Stat("WINDOWS/SYSTEM.INI"); e.Attr&AttrReadOnly == 0 || e.Attr&AttrSystem == 0 {
		t.Fatalf("attributes lost on overwrite: %x", e.Attr)
	}
	must(f2.Remove("Program Files/Internet Explorer/readme with spaces.txt"))
	must(f2.Remove("Program Files/Internet Explorer"))
	if err := f2.Remove("WINDOWS"); err == nil {
		t.Fatal("removed non-empty directory")
	}
	if _, err := f2.Stat("Program Files/Internet Explorer"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat after remove: %v", err)
	}
	must(f2.Sync())

	// All clusters freed by the big file are reusable: fill the disk to check accounting.
	f3, _ := OpenImage(d)
	free := 0
	for c := uint32(2); c < f3.clusters+2; c++ {
		if f3.get(c) == 0 {
			free++
		}
	}
	if free < int(f3.clusters)-1000 {
		t.Fatalf("leaked clusters: %d free of %d", free, f3.clusters)
	}
}

func TestShortNames(t *testing.T) {
	for name, want := range map[string]bool{"SYSTEM.INI": true, "IO.SYS": true, "system.ini": false, "LONGNAME1.TXT": false, "A.B.C": false, "X": true} {
		if _, ok := to83(name); ok != want {
			t.Errorf("to83(%q) = %v", name, ok)
		}
	}
	taken := map[string]bool{"PROGRA~1   ": true}
	a, _ := alias("Program Files", taken)
	if string(a) != "PROGRA~2   " {
		t.Errorf("alias %q", a)
	}
}

// macOS: validate an image with the system's own fsck_msdos (independent implementation).
func TestFsckMsdos(t *testing.T) {
	_, err := lookPath("fsck_msdos")
	if err != nil {
		t.Skip("fsck_msdos not available")
	}

	dir := t.TempDir()
	img := filepath.Join(dir, "vol.img")
	fh, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	const size = 64 << 20
	fh.Truncate(size)
	if err := Format32(fh, 0, size, "TEST"); err != nil {
		t.Fatal(err)
	}
	f, err := Open(fh, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.MkdirAll("WINDOWS/SYSTEM32/DRIVERS")
	f.MkdirAll("Program Files/Accessories")
	for i := 0; i < 50; i++ {
		f.WriteFile("Program Files/Accessories/Some Long Name "+string(rune('A'+i%26))+strings.Repeat("y", i)+".txt", bytes.Repeat([]byte{byte(i)}, i*1000), time.Time{})
	}
	f.WriteFile("WINDOWS/WIN.COM", make([]byte, 50000), time.Time{})
	f.Remove("Program Files/Accessories/Some Long Name A.txt")
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	out, err := runCmd("fsck_msdos", "-n", img)
	if err != nil {
		t.Fatalf("fsck_msdos reported problems:\n%s", out)
	}
	t.Logf("fsck_msdos: %s", strings.TrimSpace(out))
}
