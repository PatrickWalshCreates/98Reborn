package lab

import (
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/fat"
	"github.com/win9x-modern/w9xm/internal/media"
)

func TestProductKeyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k.txt")
	os.WriteFile(p, []byte(" abcde-12345-ABCDE-12345-ABCDE\n"), 0o600)
	if k, err := ReadProductKey(p); err != nil || k != "ABCDE-12345-ABCDE-12345-ABCDE" {
		t.Fatal(k, err)
	}
	rtf := `{\rtf1\ansi\ansicpg1252\cocoartf2822 {\fonttbl\f0\fswiss Helvetica;}\f0\fs24 \cf0 abcde-12345-ABCDE-12345-ABCDE}`
	os.WriteFile(p, []byte(rtf), 0o600)
	if k, err := ReadProductKey(p); err != nil || k != "ABCDE-12345-ABCDE-12345-ABCDE" {
		t.Fatal("rtf:", k, err)
	}
	os.WriteFile(p, []byte("not a key"), 0o600)
	if _, err := ReadProductKey(p); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestRegHasLine(t *testing.T) {
	export := "REGEDIT4\r\n\r\n[HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\Advanced]\r\n" +
		"\"Hidden\"=dword:00000002\r\n\"HideFileExt\"=dword:00000000\r\n\r\n[HKEY_CURRENT_USER\\Other]\r\n\"X\"=\"1\"\r\n"
	key := `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Explorer\Advanced`
	if !regHasLine(export, key, `"HideFileExt"=dword:00000000`) {
		t.Error("value not found")
	}
	if regHasLine(export, key, `"HideFileExt"=dword:00000001`) {
		t.Error("wrong data matched")
	}
	if regHasLine(export, key, `"X"="1"`) {
		t.Error("value from another key matched")
	}
	if !regHasValue(export, key, "HideFileExt") || regHasValue(export, key, "Gone") || regHasValue(export, `HKEY_CURRENT_USER\Nope`, "") {
		t.Error("regHasValue")
	}
}

// Build the real boot floppy and install disk from the user's CD (skipped without it).
func TestBuildFromCD(t *testing.T) {
	iso := filepath.Join("..", "..", "..", "..", "ISOs", "Windows 98 Second Edition.iso")
	if _, err := os.Stat(iso); err != nil {
		t.Skip("98 SE ISO not present")
	}
	if testing.Short() {
		t.Skip("copies ~130 MB")
	}
	m, err := media.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	dir := t.TempDir()

	floppy := filepath.Join(dir, "a.img")
	if err := buildFloppy(ISOSource(m), floppy); err != nil {
		t.Fatal(err)
	}
	fh, _ := os.Open(floppy)
	v, err := fat.OpenImage(&roDev{fh})
	if err != nil {
		t.Fatal(err)
	}
	ae, _ := v.ReadFile("AUTOEXEC.BAT")
	if !strings.Contains(string(ae), `SETUP.EXE C:\MSBATCH.INF`) {
		t.Fatalf("AUTOEXEC.BAT: %q", ae)
	}
	if _, err := v.Stat("JO.SYS"); err == nil {
		t.Error("JO.SYS (CD boot menu) should be removed")
	}
	for _, f := range []string{"IO.SYS", "COMMAND.COM", "HIMEM.SYS", "FDISK.EXE"} {
		if _, err := v.Stat(f); err != nil {
			t.Errorf("floppy lacks %s", f)
		}
	}
	fh.Close()

	disk := filepath.Join(dir, "c.img")
	if err := buildInstallDisk(ISOSource(m), disk, 63, 2048<<11-63, 2048<<20, "ABCDE-12345-ABCDE-12345-ABCDE", LabAnswers); err != nil {
		t.Fatal(err)
	}
	fh, _ = os.Open(disk)
	defer fh.Close()
	v, err = fat.OpenImage(&roDev{fh})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"WIN98/SETUP.EXE", "WIN98/PRECOPY1.CAB", "WIN98/WIN98_74.CAB", "WIN98/SMARTDRV.EXE", "MSBATCH.INF", "W9XM/READY.BAT"} {
		if _, err := v.Stat(f); err != nil {
			t.Errorf("disk lacks %s", f)
		}
	}
	if b, _ := v.ReadFile("MSBATCH.INF"); !strings.Contains(string(b), `ProductKey="ABCDE-12345-ABCDE-12345-ABCDE"`) {
		t.Error("key not in MSBATCH.INF")
	}
	if _, err := exec.LookPath("fsck_msdos"); err == nil {
		all, _ := os.ReadFile(disk)
		part := filepath.Join(dir, "part.img")
		os.WriteFile(part, all[63*512:], 0o644)
		if out, err := exec.Command("fsck_msdos", "-n", part).CombinedOutput(); err != nil {
			t.Fatalf("fsck_msdos on install disk:\n%s", out)
		}
		if out, err := exec.Command("fsck_msdos", "-n", floppy).CombinedOutput(); err != nil {
			t.Fatalf("fsck_msdos on floppy:\n%s", out)
		}
	}
}

type roDev struct{ f *os.File }

func (r *roDev) ReadAt(p []byte, off int64) (int, error)  { return r.f.ReadAt(p, off) }
func (r *roDev) WriteAt(p []byte, off int64) (int, error) { return 0, os.ErrPermission }

func TestPixelDiffIgnoresCaret(t *testing.T) {
	a := image.NewRGBA(image.Rect(0, 0, 640, 480))
	b := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 180; y < 195; y++ { // a 1x15 text caret
		b.Set(283, y, color.Black)
	}
	if d := pixelDiff(a, b); d > idlePixels {
		t.Fatalf("caret blink counted as %d changed pixels", d)
	}
	for y := 0; y < 40; y++ { // a progress bar advancing
		for x := 0; x < 100; x++ {
			b.Set(x, y, color.White)
		}
	}
	if d := pixelDiff(a, b); d <= idlePixels {
		t.Fatal("real change judged idle")
	}
}

// The page recognizer must map the real screenshots to the right pages.
func TestWizardRecognizesPages(t *testing.T) {
	w := newWizard(true)
	dir := filepath.Join("..", "..", "..", "..", "lab", "ref")
	for file, want := range map[string]string{"page-license.png": "License", "page-key.png": "Product Key", "page-finish.png": "Start Wizard", "not-wizard.png": "", "page-userinfo.png": "User Information"} {
		img, err := readPNG(filepath.Join(dir, file))
		if err != nil {
			t.Skip("lab screenshots not present")
		}
		pg, ok := w.pages[headingPrint(img)]
		if want == "" && ok || want != "" && (!ok || !strings.HasPrefix(pg.name, want)) {
			t.Errorf("%s: recognized as %q, want %q", file, pg.name, want)
		}
	}
}
