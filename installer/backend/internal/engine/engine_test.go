package engine

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/win9x-modern/w9xm/internal/fat"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/delta"
	"github.com/win9x-modern/w9xm/internal/module"
)

// fakeTarget builds a minimal installed-98SE tree. Names use mixed case to
// exercise case-insensitive resolution.
func fakeTarget(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"Windows/System.ini":      "[boot]\r\nshell=Explorer.exe\r\n\r\n[386Enh]\r\nwoafont=dosapp.fon\r\n\r\n[vcache]\r\n",
		"Windows/System/IOS.VXD":  strings.Repeat("ORIGINAL-VXD-CONTENT-", 500),
		"Windows/System/ESDI.PDR": "\x00\x01\x02\x03\x04\x05\x06\x07",
		"AUTOEXEC.BAT":            "@ECHO OFF\r\nSET PATH=C:\\WINDOWS;C:\\WINDOWS\\COMMAND\r\n",
		"Msdos.sys":               "[Paths]\r\nWinDir=C:\\WINDOWS\r\n\r\n[Options]\r\nBootGUI=1\r\nLogo=1\r\n;" + strings.Repeat("x", 70) + "\r\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(filepath.Join(root, "Msdos.sys"), 0o444) // read-only like the real file
	return root
}

func writeModule(t *testing.T, dir string, m map[string]any, payloads map[string][]byte) {
	t.Helper()
	md := filepath.Join(dir, m["id"].(string))
	os.MkdirAll(md, 0o755)
	for name, b := range payloads {
		os.WriteFile(filepath.Join(md, name), b, 0o644)
	}
	b, _ := json.MarshalIndent(m, "", " ")
	if err := os.WriteFile(filepath.Join(md, "module.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() || strings.HasPrefix(filepath.ToSlash(rel), "W9XM/TX") {
			return nil
		}
		b, _ := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b) + "|" + info.Mode().String()
		return nil
	})
	return out
}

func read(t *testing.T, root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setup(t *testing.T) (string, map[string]*module.Module) {
	root := fakeTarget(t)
	mods := t.TempDir()
	vxd := []byte(strings.Repeat("ORIGINAL-VXD-CONTENT-", 500))
	patched := append([]byte(nil), vxd...)
	copy(patched[4000:], "FASTCPU-FIX")
	d, err := delta.Make(vxd, patched)
	if err != nil {
		t.Fatal(err)
	}
	writeModule(t, mods, map[string]any{
		"id": "ram-cap", "name": "RAM cap", "version": "1.0.0", "license": "MIT",
		"when":   map[string]any{"ram_mb_min": 1025, "if_unknown": "apply"},
		"params": map[string]any{"ram_cap_mb": map[string]any{"type": "int", "default": 1024, "min": 256, "max": 1024}},
		"ops":    []any{map[string]any{"op": "ini.set", "file": `{{windir}}\SYSTEM.INI`, "section": "386Enh", "key": "MaxPhysPage", "value": "{{ram_cap_mb|pages_hex}}"}},
	}, nil)
	writeModule(t, mods, map[string]any{
		"id": "fast-cpu", "name": "Fast CPU fix", "version": "1.0.0", "license": "test",
		"requires": []string{"ram-cap"},
		"ops": []any{
			map[string]any{"op": "file.delta", "file": `C:\WINDOWS\SYSTEM\IOS.VXD`, "delta": "ios.w9xd"},
			map[string]any{"op": "file.bytes", "file": `WINDOWS\SYSTEM\ESDI.PDR`, "patches": []any{map[string]any{"offset": 2, "old": "0203", "new": "aabb"}}},
			map[string]any{"op": "file.copy", "file": `WINDOWS\SYSTEM\NEWDRV.VXD`, "src": "newdrv.vxd"},
			map[string]any{"op": "ini.set", "file": "MSDOS.SYS", "section": "Options", "key": "Logo", "value": "0"},
			map[string]any{"op": "lines.edit", "file": "AUTOEXEC.BAT", "match": "^SET PATH", "action": "comment"},
			map[string]any{"op": "reg.set", "path": `HKLM\Software\W9XM`, "name": "Edition", "type": "sz", "data": `Core "test" C:\x`},
			map[string]any{"op": "reg.delete", "path": `HKLM\Software\Junk`},
		},
	}, map[string][]byte{"ios.w9xd": d, "newdrv.vxd": []byte("NEW DRIVER")})
	writeModule(t, mods, map[string]any{
		"id": "ahci", "name": "AHCI", "version": "1.0.0", "license": "test",
		"when": map[string]any{"pci_class_any": []string{"0106"}},
		"ops":  []any{map[string]any{"op": "file.copy", "file": `WINDOWS\SYSTEM\IOSUBSYS\AHCI.PDR`, "src": "a.pdr"}},
	}, map[string][]byte{"a.pdr": []byte("AHCI")})
	all, err := module.LoadDir(mods)
	if err != nil {
		t.Fatal(err)
	}
	return root, all
}

func TestPlanApplyRollback(t *testing.T) {
	root, all := setup(t)
	before := snapshot(t, root)
	ordered, err := module.Resolve(all, []string{"fast-cpu", "ahci"})
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].ID != "ram-cap" {
		t.Fatalf("dependency not ordered first: %v", ordered[0].ID)
	}
	facts := &module.Facts{RAMMB: 32768, PCI: []module.PCIDevice{{ID: "8086:a282", Class: "0106"}}}
	opts := Options{Target: DirTarget(root), Facts: facts, Params: map[string]map[string]string{"ram-cap": {"ram_cap_mb": "768"}}}

	p, err := NewPlan(opts, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, root); len(got) != len(before) {
		t.Fatal("plan wrote to disk")
	}
	j, err := p.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "committed" {
		t.Fatal(j.Status)
	}

	if s := read(t, root, "Windows/System.ini"); !strings.Contains(s, "[386Enh]\r\nwoafont=dosapp.fon\r\nMaxPhysPage=30000\r\n") {
		t.Errorf("SYSTEM.INI: %q", s)
	}
	if s := read(t, root, "Windows/System/IOS.VXD"); s[4000:4011] != "FASTCPU-FIX" {
		t.Error("delta not applied")
	}
	if s := read(t, root, "Windows/System/ESDI.PDR"); s != "\x00\x01\xaa\xbb\x04\x05\x06\x07" {
		t.Errorf("bytes patch: %q", s)
	}
	if read(t, root, "Windows/System/IOSUBSYS/AHCI.PDR") != "AHCI" {
		t.Error("conditional module not applied")
	}
	if st, _ := os.Stat(filepath.Join(root, "Msdos.sys")); st.Mode().Perm() != 0o444 {
		t.Errorf("MSDOS.SYS mode not preserved: %v", st.Mode())
	}
	if s := read(t, root, "Msdos.sys"); !strings.Contains(s, "Logo=0") {
		t.Error("MSDOS.SYS not edited")
	}
	reg := read(t, root, "W9XM/PENDING.REG")
	if !strings.HasPrefix(reg, "REGEDIT4\r\n") || !strings.Contains(reg, `"Edition"="Core \"test\" C:\\x"`) ||
		!strings.Contains(reg, `rundll.exe setupx.dll,InstallHinfSection DefaultInstall 132 C:\\W9XM\\D`) {
		t.Errorf("PENDING.REG: %q", reg)
	}
	ae := read(t, root, "AUTOEXEC.BAT")
	if !strings.HasPrefix(ae, "REM W9XM BEGIN registry\r\n") || !strings.Contains(ae, `C:\WINDOWS\REGEDIT.EXE /L:C:\WINDOWS\SYSTEM.DAT`) ||
		!strings.Contains(ae, "REM SET PATH=") {
		t.Errorf("AUTOEXEC.BAT: %q", ae)
	}

	// Re-planning skips already-applied modules.
	p2, err := NewPlan(opts, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Modules) != 0 || len(p2.Changes) != 0 {
		t.Fatalf("expected idempotent no-op, got %d modules %d changes", len(p2.Modules), len(p2.Changes))
	}
	// Reapply still makes no file changes except the registry import (by design).
	opts.Reapply = true
	p3, _ := NewPlan(opts, ordered)
	for _, c := range p3.Changes {
		if c.Path != "W9XM/PENDING.REG" && !strings.HasPrefix(c.Path, "W9XM/D") {
			t.Errorf("reapply changed %s", c.Path)
		}
	}

	if _, err := Rollback(DirTarget(root), "", false); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, root)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("rollback did not restore %s", k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("rollback left %s behind", k)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Windows/System/IOSUBSYS")); !os.IsNotExist(err) {
		t.Error("created directory not removed")
	}
}

func TestConditionsAndMismatch(t *testing.T) {
	root, all := setup(t)
	// 512 MB machine without AHCI: ram-cap skipped, so fast-cpu (requires it) is skipped too.
	ordered, _ := module.Resolve(all, []string{"fast-cpu", "ahci"})
	p, err := NewPlan(Options{Target: DirTarget(root), Facts: &module.Facts{RAMMB: 512, PCI: []module.PCIDevice{{ID: "8086:2922", Class: "0101"}}}}, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Modules) != 0 || len(p.Skipped) != 3 {
		t.Fatalf("modules=%d skipped=%v", len(p.Modules), p.Skipped)
	}
	// Unknown facts: ram-cap applies (if_unknown=apply), ahci is skipped.
	p, _ = NewPlan(Options{Target: DirTarget(root)}, ordered)
	if len(p.Modules) != 2 {
		t.Fatalf("unknown facts: %d modules", len(p.Modules))
	}

	// A different IOS.VXD must abort the plan with nothing written.
	os.WriteFile(filepath.Join(root, "Windows/System/IOS.VXD"), []byte("someone else's version"), 0o644)
	before := snapshot(t, root)
	if _, err := NewPlan(Options{Target: DirTarget(root)}, ordered); err == nil || !strings.Contains(err.Error(), "not the expected version") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	if len(snapshot(t, root)) != len(before) {
		t.Fatal("failed plan wrote files")
	}
}

func TestStaleAndRollbackGuards(t *testing.T) {
	root, all := setup(t)
	ordered, _ := module.Resolve(all, []string{"ram-cap"})
	p, _ := NewPlan(Options{Target: DirTarget(root)}, ordered)
	os.WriteFile(filepath.Join(root, "Windows/System.ini"), []byte("[386Enh]\r\nchanged\r\n"), 0o644)
	if _, err := p.Apply(); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("expected stale error, got %v", err)
	}
	p, _ = NewPlan(Options{Target: DirTarget(root)}, ordered)
	if _, err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "Windows/System.ini"), []byte("user edit"), 0o644)
	if _, err := Rollback(DirTarget(root), "", false); err == nil || !strings.Contains(err.Error(), "modified since") {
		t.Fatalf("expected drift refusal, got %v", err)
	}
	if _, err := Rollback(DirTarget(root), "", true); err != nil {
		t.Fatal(err)
	}
}

func TestPathSafety(t *testing.T) {
	for _, p := range []string{`..\x`, `WINDOWS\..\..\etc`, "", `W9XM\TX\a`, "a//b"} {
		if _, err := CleanRel(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
	if r, _ := CleanRel(`C:\WINDOWS\SYSTEM.INI`); r != "WINDOWS/SYSTEM.INI" {
		t.Error(r)
	}
}

type fakeMedia map[string][]byte

func (f fakeMedia) ReadFile(name string) ([]byte, error) {
	if b, ok := f[strings.ToUpper(name)]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}

func TestMediaExtract(t *testing.T) {
	root := fakeTarget(t)
	mods := t.TempDir()
	usb := []byte("ME USBSTOR.SYS BYTES")
	writeModule(t, mods, map[string]any{
		"id": "me-usb", "name": "Me USB storage", "version": "1.0.0", "license": "user media",
		"ops": []any{
			map[string]any{"op": "media.extract", "media": "me", "source": "usbstor.sys", "file": `{{windir}}\SYSTEM32\DRIVERS\USBSTOR.SYS`, "sha256": module.SHA256(usb)},
			map[string]any{"op": "media.extract", "media": "any", "source": "NOTHERE.SYS", "file": `WINDOWS\X.SYS`, "optional": true},
		},
	}, nil)
	all, err := module.LoadDir(mods)
	if err != nil {
		t.Fatal(err)
	}
	ordered, _ := module.Resolve(all, []string{"me-usb"})

	// Without the Me CD the plan fails with a clear message.
	if _, err := NewPlan(Options{Target: DirTarget(root)}, ordered); err == nil || !strings.Contains(err.Error(), "--media") {
		t.Fatalf("expected missing-media error, got %v", err)
	}
	p, err := NewPlan(Options{Target: DirTarget(root), Media: map[string]MediaSource{"me": fakeMedia{"USBSTOR.SYS": usb}}}, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "Windows/SYSTEM32/DRIVERS/USBSTOR.SYS") != string(usb) {
		t.Fatal("file not extracted from media")
	}
	if len(p.Modules[0].Notes) != 2 {
		t.Fatalf("notes: %v", p.Modules[0].Notes)
	}
	// A different release of the file is rejected by its sha256.
	other := map[string]MediaSource{"me": fakeMedia{"USBSTOR.SYS": []byte("other build")}}
	if _, err := NewPlan(Options{Target: DirTarget(t.TempDir()), Media: other}, ordered); err == nil || !strings.Contains(err.Error(), "different release") {
		t.Fatalf("expected sha mismatch, got %v", err)
	}
}

// The same scenario on a FAT32 disk image instead of a folder.
func TestImageTarget(t *testing.T) {
	src := fakeTarget(t)
	img := filepath.Join(t.TempDir(), "c.img")
	fh, _ := os.Create(img)
	const size = 64 << 20
	fh.Truncate(size)
	if err := fat.WriteMBR(fh, []fat.Partition{{Bootable: true, Type: 0x0C, StartLBA: 63, Sectors: size/512 - 63}}); err != nil {
		t.Fatal(err)
	}
	if err := fat.Format32(fh, 63*512, size-63*512, "WIN98"); err != nil {
		t.Fatal(err)
	}
	v, _ := fat.OpenImage(fh)
	filepath.Walk(src, func(p string, info os.FileInfo, err error) error { // copy the fake tree in
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			return v.Mkdir(rel)
		}
		b, _ := os.ReadFile(p)
		if err := v.WriteFile(rel, b, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if rel == "Msdos.sys" {
			v.SetAttr(rel, fat.AttrReadOnly|fat.AttrHidden|fat.AttrSystem)
		}
		return nil
	})
	v.Sync()
	fh.Close()
	before, _ := os.ReadFile(img)

	_, all := setup(t)
	ordered, _ := module.Resolve(all, []string{"fast-cpu", "ahci"})
	tgt, err := OpenTarget(img)
	if err != nil {
		t.Fatal(err)
	}
	facts := &module.Facts{RAMMB: 4096, PCI: []module.PCIDevice{{ID: "8086:a282", Class: "0106"}}}
	p, err := NewPlan(Options{Target: tgt, Facts: facts}, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	b, _ := tgt.ReadFile("WINDOWS/SYSTEM.INI")
	if !strings.Contains(string(b), "MaxPhysPage=40000") {
		t.Errorf("SYSTEM.INI: %q", b)
	}
	if st, _ := tgt.Stat("MSDOS.SYS"); st.Mode&0o200 != 0 {
		t.Error("MSDOS.SYS lost its read-only attribute")
	}
	if b, _ := tgt.ReadFile("WINDOWS/SYSTEM/IOSUBSYS/AHCI.PDR"); string(b) != "AHCI" {
		t.Error("new file in new directory not written")
	}
	if h, _ := LoadHistory(tgt); len(h.Journals) != 1 || h.Journals[0].Status != "committed" {
		t.Fatal("journal not recorded on the image")
	}
	if _, err := Rollback(tgt, "", false); err != nil {
		t.Fatal(err)
	}
	tgt.Close()

	// Every file is back to its original content (the journal itself remains).
	tgt, _ = OpenTarget(img)
	defer tgt.Close()
	v2, _ := fat.OpenImage(mustOpen(t, before))
	for _, rel := range []string{"WINDOWS/SYSTEM.INI", "WINDOWS/SYSTEM/IOS.VXD", "WINDOWS/SYSTEM/ESDI.PDR", "AUTOEXEC.BAT", "MSDOS.SYS"} {
		want, _ := v2.ReadFile(rel)
		got, err := tgt.ReadFile(rel)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s not restored", rel)
		}
	}
	for _, gone := range []string{"WINDOWS/SYSTEM/IOSUBSYS", "W9XM/PENDING.REG"} {
		if _, err := tgt.Stat(gone); !isNotExist(err) {
			t.Errorf("%s should be gone after rollback", gone)
		}
	}
	if out, err := exec.Command("fsck_msdos", "-n", img).CombinedOutput(); err == nil || strings.Contains(string(out), "not found") {
		t.Logf("fsck_msdos: ok")
	} else if _, lerr := exec.LookPath("fsck_msdos"); lerr == nil {
		// fsck_msdos needs the partition, not the whole disk: check via offset copy
		part := filepath.Join(t.TempDir(), "p.img")
		all, _ := os.ReadFile(img)
		os.WriteFile(part, all[63*512:], 0o644)
		if out, err := exec.Command("fsck_msdos", "-n", part).CombinedOutput(); err != nil {
			t.Fatalf("fsck_msdos:\n%s", out)
		}
	}
}

type memFile struct{ *bytes.Reader }

func (memFile) WriteAt(p []byte, off int64) (int, error) { return 0, os.ErrPermission }

func mustOpen(t *testing.T, b []byte) fat.Device { return memFile{bytes.NewReader(b)} }

func TestPatternOp(t *testing.T) {
	p, err := module.ParsePattern("29 C9 ?? 20")
	if err != nil || len(p) != 4 || !p[2].Any {
		t.Fatalf("%v %v", p, err)
	}
	data := []byte{0, 0x29, 0xC9, 0x55, 0x20, 1, 0x29, 0xC9, 0x00, 0x20}
	if got := patternIndex(data, p); len(got) != 2 || got[0] != 1 || got[1] != 6 {
		t.Fatalf("matches %v", got)
	}
}
