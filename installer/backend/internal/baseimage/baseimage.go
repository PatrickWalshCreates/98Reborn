// Package baseimage builds the 98Reborn installer base image: a small Alpine
// Linux system (kernel, selected drivers, QEMU, the 98reborn-installer as
// /init) that boots on BIOS (ISOLINUX) and UEFI (systemd-boot) PCs.
//
// It contains no Microsoft code. The 98Reborn Tool later adds the user's own
// 98SE/ folder to it (see package compose).
package baseimage

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/apk"
	"github.com/win9x-modern/w9xm/internal/cpio"
	"github.com/win9x-modern/w9xm/internal/fat"
	"github.com/win9x-modern/w9xm/internal/isowriter"
)

// Options configures a build.
type Options struct {
	Client    *apk.Client
	Installer string // path to the linux/amd64 98reborn-installer binary
	Modules   string // modules/ directory (copied to 98reborn/modules)
	Editions  string // editions/ directory (copied to 98reborn/editions)
	Out       string // output ISO
	Version   string // 98Reborn version, e.g. 0.1.0-dev
	Now       time.Time
	Log       func(format string, a ...any)
}

// Packages in the live system. Their dependencies are added automatically.
var rootPackages = []string{"busybox", "busybox-binsh", "musl", "kmod", "qemu-system-i386", "ca-certificates-bundle",
	"ntfs-3g-progs", "e2fsprogs"} // shrinking Windows / Linux partitions for multi-boot

// Dependencies we leave out on purpose (boot-time tooling of a normal Alpine).
var skipDeps = map[string]bool{"initramfs-generator": true, "mkinitfs": true, "linux-firmware-any": true}

// Firmware packages added to the live system.
var firmwarePackages = []string{"linux-firmware-rtl_nic"}

// Kernel module directories the installer needs (below kernel/). Dependencies
// of anything selected come along via modules.dep.
var modulePrefixes = []string{
	"drivers/ata/", "drivers/nvme/", "drivers/scsi/", "drivers/cdrom/", "drivers/mmc/",
	"drivers/usb/host/", "drivers/usb/storage/", "drivers/usb/common/", "drivers/usb/core/",
	"drivers/hid/", "drivers/input/",
	"drivers/net/ethernet/", "drivers/net/phy/", "drivers/net/mdio/", "drivers/net/pcs/", "drivers/net/usb/",
	"drivers/net/mii.", "drivers/net/virtio_net.", "drivers/net/net_failover.",
	"drivers/virtio/", "drivers/block/virtio_blk.", "drivers/block/loop.",
	"drivers/pci/controller/vmd.",
	"fs/isofs/", "fs/fat/", "fs/exfat/", "fs/nls/", "fs/udf/", "fs/ntfs3/", "fs/overlayfs/",
	"fs/ext4/", "fs/jbd2/", "fs/btrfs/", "fs/xfs/", // recognizing installed Linux systems
	"arch/x86/kvm/", "net/packet/",
}

// Paths dropped from packages to keep the image small.
var dropPrefixes = []string{"usr/share/man/", "usr/share/doc/", "usr/share/info/", "usr/include/",
	"usr/share/locale/", "usr/lib/pkgconfig/", "usr/share/bash-completion/", "usr/share/zsh/"}

const cmdline = "console=tty1 console=ttyS0,115200 loglevel=3"

func (o *Options) logf(f string, a ...any) {
	if o.Log != nil {
		o.Log(f, a...)
	}
}

// Build writes the base installer ISO.
func Build(o Options) error {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	c := o.Client
	for _, r := range []string{"main", "community"} {
		if err := c.LoadIndex(r); err != nil {
			return err
		}
	}

	// 1. Root file system from packages.
	root := cpio.Tree{}
	pkgs, err := c.Resolve(append(append([]string{}, rootPackages...), firmwarePackages...), skipDeps)
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		b, err := c.Fetch(p)
		if err != nil {
			return err
		}
		if err := apk.Unpack(b, func(e apk.Entry) error { addEntry(root, e); return nil }); err != nil {
			return fmt.Errorf("%s: %w", p.File(), err)
		}
	}
	o.logf("root file system: %d packages, %d entries, %d MB", len(pkgs), len(root), root.Size()>>20)

	// 2. Kernel and drivers.
	kernel, kver, err := addKernel(c, root, o.logf)
	if err != nil {
		return err
	}

	// 3. Our files.
	inst, err := os.ReadFile(o.Installer)
	if err != nil {
		return err
	}
	root.AddFile("init", 0o755, inst)
	root.AddFile("usr/share/98reborn/udhcpc.script", 0o755, []byte(udhcpcScript))
	root.AddFile("etc/os-release", 0o644, []byte(fmt.Sprintf("NAME=\"98Reborn Installer\"\nPRETTY_NAME=\"98Reborn Installer %s\"\nID=98reborn\nVERSION_ID=%s\n", o.Version, o.Version)))
	root.AddFile("etc/passwd", 0o644, []byte("root:x:0:0:root:/root:/bin/sh\n"))
	root.AddFile("etc/group", 0o644, []byte("root:x:0:\n"))
	root.AddFile("etc/hostname", 0o644, []byte("98reborn\n"))
	root.MkdirAll("root")
	for _, d := range []string{"proc", "sys", "dev", "run", "tmp", "mnt"} {
		root.MkdirAll(d)
	}
	root["dev/console"] = &cpio.Node{Type: cpio.TypeChar, Mode: 0o600, Major: 5, Minor: 1}

	var ird bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&ird, gzip.BestCompression)
	if err := root.Write(zw); err != nil {
		return err
	}
	zw.Close()
	initrd := ird.Bytes()
	o.logf("initramfs: %d MB uncompressed, %d MB gzip", root.Size()>>20, len(initrd)>>20)

	// 4. Boot loaders.
	isolinux, ldlinux, err := pkgFiles(c, "syslinux", "usr/share/syslinux/isolinux.bin", "usr/share/syslinux/ldlinux.c32")
	if err != nil {
		return err
	}
	isohdpfx, _, err := pkgFiles(c, "syslinux", "usr/share/syslinux/isohdpfx.bin", "")
	if err != nil {
		return err
	}
	sdboot, _, err := pkgFiles(c, "systemd-boot", "usr/lib/systemd/boot/efi/systemd-bootx64.efi", "")
	if err != nil {
		return err
	}

	syslinuxCfg := []byte("DEFAULT 98reborn\nPROMPT 0\nTIMEOUT 0\nLABEL 98reborn\n  KERNEL /boot/vmlinuz\n  INITRD /boot/initrd.gz\n  APPEND " + cmdline + "\n")
	loaderConf := []byte("timeout 0\ndefault 98reborn.conf\nconsole-mode keep\n")
	entry := []byte("title 98Reborn Installer\nlinux /boot/vmlinuz\ninitrd /boot/initrd.gz\noptions " + cmdline + "\n")
	baseJSON, _ := json.MarshalIndent(map[string]string{
		"product": "98Reborn Installer", "version": o.Version, "kernel": kver,
		"alpine": c.Branch, "built": o.Now.Format(time.RFC3339),
	}, "", "  ")

	espFiles := map[string][]byte{
		"EFI/BOOT/BOOTX64.EFI":         sdboot,
		"loader/loader.conf":           loaderConf,
		"loader/entries/98reborn.conf": entry,
		"boot/vmlinuz":                 kernel,
		"boot/initrd.gz":               initrd,
	}
	esp, err := espImage(espFiles, o.Now)
	if err != nil {
		return err
	}

	files := map[string][]byte{
		"boot/syslinux/isolinux.bin": isolinux,
		"boot/syslinux/ldlinux.c32":  ldlinux,
		"boot/syslinux/isohdpfx.bin": isohdpfx,
		"boot/syslinux/isolinux.cfg": syslinuxCfg,
		"boot/syslinux/syslinux.cfg": syslinuxCfg,
		"boot/efi.img":               esp,
		"98reborn/base.json":         baseJSON,
		"README.TXT":                 []byte(readme),
	}
	for k, v := range espFiles {
		files[k] = v
	}
	for dir, dst := range map[string]string{o.Modules: "98reborn/modules", o.Editions: "98reborn/editions"} {
		if dir == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			files[dst+"/"+filepath.ToSlash(rel)] = b
			return err
		})
		if err != nil {
			return err
		}
	}
	return writeISO(o.Out, files, isohdpfx, o.Now)
}

// QEMU files the installer's Setup VM uses; the rest of QEMU's data
// (firmware for other machines, other emulators) is left out.
var qemuKeep = map[string]bool{
	"usr/bin/qemu-system-i386": true, "usr/share/qemu/bios-256k.bin": true, "usr/share/qemu/bios.bin": true,
	"usr/share/qemu/vgabios.bin": true, "usr/share/qemu/vgabios-stdvga.bin": true,
	"usr/share/qemu/vgabios-cirrus.bin": true, "usr/share/qemu/kvmvapic.bin": true,
	"usr/share/qemu/linuxboot_dma.bin": true, "usr/share/qemu/multiboot_dma.bin": true,
}

func dropped(p string) bool {
	if (strings.HasPrefix(p, "usr/bin/qemu-") || strings.HasPrefix(p, "usr/share/qemu/")) && !qemuKeep[p] {
		return true
	}
	for _, d := range dropPrefixes {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

func addEntry(t cpio.Tree, e apk.Entry) {
	if e.Type != '5' && dropped(e.Path) || dropped(e.Path+"/") && e.Type == '5' && e.Path != "usr/share/qemu" {
		return
	}
	switch e.Type {
	case '0', 0: // regular
		t.MkdirAll(path.Dir(e.Path))
		t[e.Path] = &cpio.Node{Type: cpio.TypeReg, Mode: uint32(e.Mode), Data: e.Data, UID: uint32(e.UID), GID: uint32(e.GID)}
	case '5':
		t.MkdirAll(e.Path)
		t[e.Path].Mode = uint32(e.Mode)
	case '2':
		t.AddSymlink(e.Path, e.Link)
	case '1': // hard link: copy the target's contents
		if src := t[strings.TrimPrefix(e.Link, "./")]; src != nil {
			cp := *src
			t.MkdirAll(path.Dir(e.Path))
			t[e.Path] = &cp
		}
	}
}

// pkgFiles returns up to two files from a package (without its dependencies).
func pkgFiles(c *apk.Client, name, a, b string) ([]byte, []byte, error) {
	p := c.Lookup(name)
	if p == nil {
		return nil, nil, fmt.Errorf("package %s not found", name)
	}
	raw, err := c.Fetch(p)
	if err != nil {
		return nil, nil, err
	}
	var da, db []byte
	apk.Unpack(raw, func(e apk.Entry) error {
		switch e.Path {
		case a:
			da = e.Data
		case b:
			db = e.Data
		}
		return nil
	})
	if da == nil || (b != "" && db == nil) {
		return nil, nil, fmt.Errorf("%s: missing %s or %s", name, a, b)
	}
	return da, db, nil
}

// addKernel returns the kernel image and adds the selected modules (plus
// their dependencies and the module index files) to the tree.
func addKernel(c *apk.Client, root cpio.Tree, logf func(string, ...any)) ([]byte, string, error) {
	p := c.Lookup("linux-lts")
	if p == nil {
		return nil, "", errors.New("linux-lts not found")
	}
	raw, err := c.Fetch(p)
	if err != nil {
		return nil, "", err
	}
	// Pass 1: kernel image, module dependency list.
	var kernel []byte
	var kver string
	var dep []byte
	apk.Unpack(raw, func(e apk.Entry) error {
		if e.Path == "boot/vmlinuz-lts" {
			kernel = e.Data
		}
		if strings.HasPrefix(e.Path, "lib/modules/") && strings.HasSuffix(e.Path, "/modules.dep") {
			dep = e.Data
			kver = strings.Split(e.Path, "/")[2]
		}
		return nil
	})
	if kernel == nil || dep == nil {
		return nil, "", errors.New("linux-lts: kernel or modules.dep missing")
	}
	deps := map[string][]string{}
	for _, l := range strings.Split(string(dep), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok {
			deps[k] = strings.Fields(v)
		}
	}
	want := map[string]bool{}
	var add func(string)
	add = func(m string) {
		if want[m] {
			return
		}
		want[m] = true
		for _, d := range deps[m] {
			add(d)
		}
	}
	for m := range deps {
		rel := strings.TrimPrefix(m, "kernel/")
		for _, pre := range modulePrefixes {
			if strings.HasPrefix(rel, pre) {
				add(m)
				break
			}
		}
	}
	// Pass 2: copy the chosen modules and the index files.
	base := "lib/modules/" + kver + "/"
	n := 0
	apk.Unpack(raw, func(e apk.Entry) error {
		if !strings.HasPrefix(e.Path, base) || e.Type != '0' {
			return nil
		}
		rel := strings.TrimPrefix(e.Path, base)
		if want[rel] || (strings.HasPrefix(rel, "modules.") && !strings.Contains(rel, "/")) {
			root.AddFile(e.Path, 0o644, e.Data)
			if want[rel] {
				n++
			}
		}
		return nil
	})
	logf("kernel %s: %d of %d modules", kver, n, len(deps))
	return kernel, kver, nil
}

type memDev struct{ b []byte }

func (m *memDev) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.b)) {
		return 0, errors.New("read past end")
	}
	return copy(p, m.b[off:]), nil
}

func (m *memDev) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(m.b)) {
		return 0, errors.New("write past end")
	}
	return copy(m.b[off:], p), nil
}

// espImage builds the FAT32 image UEFI firmware boots from (El Torito EFI entry).
func espImage(files map[string][]byte, now time.Time) ([]byte, error) {
	var total int64
	for _, b := range files {
		total += int64(len(b))
	}
	size := total + total/16 + 4<<20
	if size < 40<<20 {
		size = 40 << 20
	}
	size = (size + 1<<20 - 1) &^ (1<<20 - 1)
	dev := &memDev{b: make([]byte, size)}
	if err := fat.Format32(dev, 0, size, "98REBORN"); err != nil {
		return nil, err
	}
	fs, err := fat.Open(dev, 0)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if d := path.Dir(n); d != "." {
			if err := fs.MkdirAll(d); err != nil {
				return nil, err
			}
		}
		if err := fs.WriteFile(n, files[n], now); err != nil {
			return nil, fmt.Errorf("efi.img %s: %w", n, err)
		}
	}
	if err := fs.Sync(); err != nil {
		return nil, err
	}
	return dev.b, nil
}

func writeISO(out string, files map[string][]byte, hybrid []byte, now time.Time) error {
	var fl []isowriter.File
	for p, b := range files {
		b := b
		fl = append(fl, isowriter.File{Path: p, Size: int64(len(b)), ModTime: now,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }})
	}
	sort.Slice(fl, func(i, j int) bool { return fl[i].Path < fl[j].Path })
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	err = isowriter.Write(f, isowriter.Options{
		VolumeID:  "98REBORN",
		Files:     fl,
		BIOS:      &isowriter.BootEntry{Path: "boot/syslinux/isolinux.bin", Emulation: "none", LoadSectors: 4, InfoTable: true},
		EFI:       &isowriter.BootEntry{Path: "boot/efi.img", Emulation: "none"},
		Now:       now,
		HybridMBR: hybrid,
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

const udhcpcScript = `#!/bin/sh
# Minimal udhcpc hook for the 98Reborn installer.
case "$1" in
deconfig)
	ip addr flush dev "$interface"
	ip link set "$interface" up
	;;
bound|renew)
	ip addr flush dev "$interface"
	ip addr add "$ip/${mask:-24}" dev "$interface"
	[ -n "$router" ] && ip route replace default via "${router%% *}" dev "$interface"
	: > /etc/resolv.conf
	for d in $dns; do echo "nameserver $d" >> /etc/resolv.conf; done
	;;
esac
exit 0
`

const readme = `98Reborn Installer
==================

Boot this medium on the PC you want to install Windows 98 SE on.
It works on BIOS and UEFI PCs (turn Secure Boot off).

This medium was made by the 98Reborn Tool. The installer is open source:
https://github.com/PatrickWalshCreates/98Reborn
`
