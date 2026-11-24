//go:build linux

package disk

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// PartDevice is the device node of partition num of disk d (/dev/sda1,
// /dev/nvme0n1p1). For image files it's "".
func PartDevice(d *Disk, num int) string {
	if !strings.HasPrefix(d.Path, "/dev/") {
		return ""
	}
	ents, _ := os.ReadDir("/sys/class/block/" + d.Name)
	for _, e := range ents {
		b, err := os.ReadFile("/sys/class/block/" + d.Name + "/" + e.Name() + "/partition")
		if err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(num) {
			return "/dev/" + e.Name()
		}
	}
	return ""
}

// inspectOS mounts the partition read-only to name the OS on it and asks the
// file-system tools how far it can shrink.
func inspectOS(d *Disk, p *Part) {
	dev := PartDevice(d, p.Num)
	if dev == "" {
		return
	}
	switch p.FS {
	case "ntfs":
		withMount(dev, "ntfs3", "ro", func(dir string) {
			switch {
			case exists(dir, "Windows/System32/ntoskrnl.exe"):
				p.OS = "Windows"
				if exists(dir, "Windows/System32/winver.exe") && exists(dir, "Windows/SystemApps") {
					p.OS = "Windows 10/11"
				}
			case exists(dir, "Recovery/WindowsRE"):
				p.OS = "Windows recovery tools"
			}
			if b := head(filepath.Join(dir, "hiberfil.sys"), 4); strings.EqualFold(string(b), "hibr") {
				p.Hibernate = true
			}
		})
		p.MinBytes, p.ShrinkReason = ntfsMin(dev)
	case "ext4", "btrfs", "xfs":
		opts := "ro"
		if p.FS == "ext4" {
			opts = "ro,noload"
		}
		withMount(dev, p.FS, opts, func(dir string) {
			for _, f := range []string{"etc/os-release", "usr/lib/os-release"} {
				if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
					p.OS = osRelease(b)
					break
				}
			}
		})
		if p.FS == "ext4" {
			p.MinBytes, p.ShrinkReason = ext4Min(dev)
		}
	}
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

func head(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b := make([]byte, n)
	k, _ := f.Read(b)
	return b[:k]
}

func osRelease(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return "Linux"
}

func withMount(dev, fs, opts string, fn func(dir string)) {
	dir, err := os.MkdirTemp("/run", "inspect-")
	if err != nil {
		return
	}
	defer os.Remove(dir)
	exec.Command("modprobe", "-q", fs).Run()
	flags := uintptr(syscall.MS_RDONLY | syscall.MS_NOATIME)
	data := strings.TrimPrefix(strings.TrimPrefix(opts, "ro"), ",")
	if err := syscall.Mount(dev, dir, fs, flags, data); err != nil {
		return
	}
	defer syscall.Unmount(dir, 0)
	fn(dir)
}

var ntfsMinRe = regexp.MustCompile(`resize at (\d+) bytes`)

// ntfsMin asks ntfsresize for the smallest size; its refusals become reasons.
func ntfsMin(dev string) (int64, string) {
	out, err := exec.Command("ntfsresize", "--info", "--no-progress-bar", dev).CombinedOutput()
	s := string(out)
	switch {
	case strings.Contains(s, "hibernat"):
		return 0, "Windows is hibernated or uses Fast Startup. In Windows, turn off Fast Startup and shut down fully, then run the installer again"
	case strings.Contains(s, "scheduled for check") || strings.Contains(s, "inconsistent") || strings.Contains(s, "dirty"):
		return 0, "Windows needs to check this disk first. Run chkdsk in Windows, restart it once, then run the installer again"
	}
	if m := ntfsMinRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n, ""
	}
	if err != nil {
		return 0, "ntfsresize couldn't read this partition: " + firstLine(s)
	}
	return 0, "couldn't check how far this partition can shrink"
}

var ext4MinRe = regexp.MustCompile(`minimum size of the filesystem: (\d+)`)

func ext4Min(dev string) (int64, string) {
	out, _ := exec.Command("resize2fs", "-P", dev).CombinedOutput()
	m := ext4MinRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, "resize2fs couldn't read this partition: " + firstLine(string(out))
	}
	blocks, _ := strconv.ParseInt(m[1], 10, 64)
	sb := make([]byte, 4)
	f, err := os.Open(dev)
	if err != nil {
		return 0, err.Error()
	}
	defer f.Close()
	f.ReadAt(sb, 1024+24)
	bs := int64(1024) << (uint32(sb[0]) | uint32(sb[1])<<8)
	return blocks * bs, ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "ntfsresize v") && !strings.HasPrefix(l, "resize2fs ") {
			return l
		}
	}
	return "unknown error"
}

// ShrinkFS shrinks the file system on partition dev to at most bytes. The
// caller then shrinks the partition entry.
func ShrinkFS(dev, fs string, bytesMax int64) error {
	target := bytesMax - 1<<20 // stay a little inside the new partition end
	switch fs {
	case "ntfs":
		size := strconv.FormatInt(target, 10)
		if out, err := exec.Command("ntfsresize", "--no-action", "--no-progress-bar", "-s", size, dev).CombinedOutput(); err != nil {
			return fmt.Errorf("ntfsresize test run: %s", firstLine(string(out)))
		}
		c := exec.Command("ntfsresize", "--no-progress-bar", "-s", size, dev)
		c.Stdin = bytes.NewReader([]byte("y\n"))
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("ntfsresize: %s", firstLine(string(out)))
		}
		return nil
	case "ext4":
		if out, err := exec.Command("e2fsck", "-f", "-y", dev).CombinedOutput(); err != nil {
			// exit 1 = errors corrected, still fine
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() > 1 {
				return fmt.Errorf("e2fsck: %s", firstLine(string(out)))
			}
		}
		if out, err := exec.Command("resize2fs", dev, strconv.FormatInt(target/1024, 10)+"K").CombinedOutput(); err != nil {
			return fmt.Errorf("resize2fs: %s", firstLine(string(out)))
		}
		return nil
	}
	return fmt.Errorf("can't shrink %s", fs)
}

// Reread asks the kernel to reload the partition table (best effort).
func Reread(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	const BLKRRPART = 0x125f
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), BLKRRPART, 0)
}
