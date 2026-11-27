//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/scan"
)

// Files worth having from an installed Windows 98 (paths below the drive
// root, found case-insensitively). The registry hives are binary, for
// offline reading.
var win98Logs = []string{
	"BOOTLOG.TXT", "BOOTLOG.PRV", "DETLOG.TXT", "DETLOG.OLD", "DETCRASH.LOG",
	"SETUPLOG.TXT", "NETLOG.TXT", "FRUNLOG.TXT", "SUHDLOG.DAT", "XHCIINST.LOG",
	"MSDOS.SYS", "CONFIG.SYS", "AUTOEXEC.BAT",
	"WINDOWS/SYSTEM.INI", "WINDOWS/WIN.INI", "WINDOWS/IOS.LOG", "WINDOWS/TTYLOG.TXT",
	"WINDOWS/SYSTEM.DAT", "WINDOWS/USER.DAT",
	"WINDOWS/INF/98RVESA.INF", "WINDOWS/INF/XHCI98.INF",
	"EFI/98REBORN/REBIOS.INI",
}

// collectLogs saves Windows 98's logs and this PC's hardware details to a
// USB drive: another USB drive if one is plugged in, else the 98Reborn
// stick's own FAT partition. It never writes to an internal disk.
func collectLogs(med *medium, rep *compat.Report, progress func(string)) (string, error) {
	mediumDev := ""
	if med != nil {
		mediumDev = med.Dev
	}
	progress("Looking for a USB drive to save to")
	dest, what, err := mountLogTarget(mediumDev)
	if err != nil {
		return "", err
	}
	defer func() { syscall.Sync(); syscall.Unmount(dest, 0) }()
	out := filepath.Join(dest, "98REBORN-LOGS", time.Now().Format("2006-01-02_150405"))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", fmt.Errorf("can't write to %s: %v", what, err)
	}

	progress("Saving this PC's hardware details")
	writePCInfo(out, med, rep)

	progress("Reading Windows 98")
	found := 0
	for _, d := range readDisks(mediumDev) {
		if d.IsMedium || d.Bus == "usb" {
			continue
		}
		for _, p := range d.Parts {
			if !strings.HasPrefix(p.OS, "Windows 9") {
				continue
			}
			dev := disk.PartDevice(d, p.Num)
			if dev == "" {
				continue
			}
			progress("Copying logs from " + filepath.Base(dev))
			if n, err := copyWin98Logs(dev, filepath.Join(out, "win98-"+filepath.Base(dev))); err == nil {
				found += n
			} else {
				os.WriteFile(filepath.Join(out, "win98-"+filepath.Base(dev)+"-error.txt"), []byte(err.Error()+"\n"), 0o644)
			}
		}
	}
	os.WriteFile(filepath.Join(out, "README.TXT"), []byte(crlfText(
		"98Reborn logs, saved by the installer.\n"+
			"pc.txt: this PC's hardware and the installer's checks. dmesg.txt: the Linux kernel's log.\n"+
			"win98-*: files from each Windows 98 partition (the .DAT files are its registry).\n"+
			"Attach this folder (zipped) to a bug report at https://github.com/PatrickWalshCreates/98Reborn/issues\n")), 0o644)
	syscall.Sync()
	where := what + `, folder 98REBORN-LOGS\` + filepath.Base(out)
	if found == 0 {
		return where, errors.New("no Windows 98 installation was found on this PC's disks, so only the hardware details were saved")
	}
	marker(fmt.Sprintf("LOGS %d files", found))
	return where, nil
}

// mountLogTarget mounts a writable FAT/exFAT partition on a USB drive.
func mountLogTarget(mediumDev string) (dir, what string, err error) {
	mediumDisk := parentDisk(mediumDev)
	dir = "/run/98reborn-logs"
	os.MkdirAll(dir, 0o755)
	try := func(dev string) bool {
		for _, fs := range []string{"vfat", "exfat"} {
			if syscall.Mount("/dev/"+dev, dir, fs, 0, "") == nil {
				if f, err := os.CreateTemp(dir, ".w"); err == nil {
					f.Close()
					os.Remove(f.Name())
					return true
				}
				syscall.Unmount(dir, 0)
			}
		}
		return false
	}
	var own []string
	for _, n := range blockDevices() {
		if _, err := os.Stat("/sys/class/block/" + n + "/partition"); err != nil {
			continue // whole disks: their partitions are listed too
		}
		parent := parentDisk(n)
		link, _ := filepath.EvalSymlinks("/sys/class/block/" + parent)
		if !strings.Contains(link, "/usb") && parent != mediumDisk {
			continue // internal disks are never written
		}
		if n == mediumDev {
			continue // the read-only installer partition
		}
		if parent == mediumDisk {
			own = append(own, n)
			continue
		}
		if try(n) {
			return dir, "the USB drive " + n, nil
		}
	}
	for _, n := range own { // the stick's EFI partition (FAT)
		if try(n) {
			return dir, "the 98Reborn USB stick (its small FAT partition, " + n + ")", nil
		}
	}
	return "", "", errors.New("found no USB drive to save to. Plug in a USB drive formatted FAT32 or exFAT and try again")
}

// parentDisk is the disk a partition belongs to ("sdb2" -> "sdb").
func parentDisk(n string) string {
	if n == "" {
		return ""
	}
	if _, err := os.Stat("/sys/class/block/" + n + "/partition"); err != nil {
		return n
	}
	link, err := filepath.EvalSymlinks("/sys/class/block/" + n)
	if err != nil {
		return n
	}
	return filepath.Base(filepath.Dir(link))
}

func copyWin98Logs(dev, out string) (int, error) {
	mnt := "/run/98reborn-win98"
	os.MkdirAll(mnt, 0o755)
	if err := syscall.Mount(dev, mnt, "vfat", syscall.MS_RDONLY, ""); err != nil {
		return 0, fmt.Errorf("mounting %s: %v", dev, err)
	}
	defer syscall.Unmount(mnt, 0)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, rel := range win98Logs {
		src := findCI(mnt, rel)
		if src == "" {
			continue
		}
		if copyFile(src, filepath.Join(out, strings.ReplaceAll(rel, "/", "_"))) == nil {
			n++
		}
	}
	// xHCI98's and 98Reborn's own folders, whole (small text files).
	for _, d := range []string{"W9XM", "XHCI98"} {
		if src := findCI(mnt, d); src != "" {
			filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() || fi.Size() > 4<<20 {
					return nil
				}
				rel, _ := filepath.Rel(mnt, p)
				if copyFile(p, filepath.Join(out, strings.ReplaceAll(rel, "/", "_"))) == nil {
					n++
				}
				return nil
			})
		}
	}
	// What's on the drive: the root, WINDOWS and WINDOWS\INF.
	var ls strings.Builder
	for _, d := range []string{"", "WINDOWS", "WINDOWS/INF", "WINDOWS/SYSTEM"} {
		dir := mnt
		if d != "" {
			if dir = findCI(mnt, d); dir == "" {
				continue
			}
		}
		fmt.Fprintf(&ls, "== C:\\%s\n", strings.ReplaceAll(d, "/", `\`))
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if fi, err := e.Info(); err == nil {
				fmt.Fprintf(&ls, "%10d  %s  %s\n", fi.Size(), fi.ModTime().Format("2006-01-02 15:04"), e.Name())
			}
		}
	}
	os.WriteFile(filepath.Join(out, "dir.txt"), []byte(crlfText(ls.String())), 0o644)
	return n, nil
}

func writePCInfo(out string, med *medium, rep *compat.Report) {
	var b strings.Builder
	if med != nil {
		fmt.Fprintf(&b, "98Reborn installer %s (built %s)\n", med.Base.Version, med.Base.Built)
	}
	if v, err := os.ReadFile("/proc/version"); err == nil {
		b.WriteString("Kernel: " + strings.TrimSpace(string(v)) + "\n")
	}
	if c, err := os.ReadFile("/proc/cmdline"); err == nil {
		b.WriteString("Command line: " + strings.TrimSpace(string(c)) + "\n")
	}
	if rep == nil {
		if sc, err := scan.Run(); err == nil {
			rep = compat.Evaluate(sc)
		}
	}
	if rep != nil {
		b.WriteString("\n" + rep.Text() + "\n")
	}
	b.WriteString("\nPCI devices (address vendor:device class subsystem driver):\n")
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		rd := func(f string) string {
			v, _ := os.ReadFile(d + "/" + f)
			return strings.TrimPrefix(strings.TrimSpace(string(v)), "0x")
		}
		drv := "-"
		if l, err := os.Readlink(d + "/driver"); err == nil {
			drv = filepath.Base(l)
		}
		fmt.Fprintf(&b, "  %s %s:%s %s %s:%s %s\n", filepath.Base(d), rd("vendor"), rd("device"), rd("class"), rd("subsystem_vendor"), rd("subsystem_device"), drv)
	}
	b.WriteString("\nDisks:\n")
	for _, d := range readDisks("") {
		fmt.Fprintf(&b, "  %s (%s, %s)\n%s", d.Name, d.Bus, d.Model, disk.Layout(d, nil))
	}
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		if o, err := run(10*time.Second, "efibootmgr", "-v"); err == nil {
			b.WriteString("\nUEFI boot entries:\n" + string(o))
		}
	}
	// Input devices: which keyboard and pointer this PC has, and on which bus
	// (i8042/PS/2, USB, I2C) - what Windows 98 can or can't drive.
	if in, err := os.ReadFile("/proc/bus/input/devices"); err == nil {
		b.WriteString("\nInput devices:\n" + string(in))
	}
	os.WriteFile(filepath.Join(out, "pc.txt"), []byte(crlfText(b.String())), 0o644)
	// The firmware's ACPI tables, which Windows 98 reads to find devices.
	if tabs, _ := filepath.Glob("/sys/firmware/acpi/tables/*"); len(tabs) > 0 {
		dir := filepath.Join(out, "acpi")
		os.MkdirAll(dir, 0o755)
		for _, t := range tabs {
			if fi, err := os.Stat(t); err == nil && !fi.IsDir() {
				copyFile(t, filepath.Join(dir, filepath.Base(t)+".dat"))
			}
		}
	}
	if o, err := run(10*time.Second, "dmesg"); err == nil {
		os.WriteFile(filepath.Join(out, "dmesg.txt"), []byte(crlfText(string(o))), 0o644)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}

// crlfText uses Windows line endings, so the files read well in Notepad too.
func crlfText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
