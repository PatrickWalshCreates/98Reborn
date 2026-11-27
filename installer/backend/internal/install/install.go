// Package install puts Windows 98 SE on a real disk, next to whatever else is
// there:
//
//  1. Windows Setup runs unattended in a VM against a sparse scratch image
//     whose partition has exactly the start and size of the real target
//     partition. Setup never sees the real disk.
//  2. The edition's modules (and drivers matched to this PC) are applied to
//     that image by the patch engine.
//  3. Only then is the real disk changed: an existing partition is shrunk if
//     the plan says so, the used parts of the finished partition are copied
//     into place, and the new partition entry is written.
//
// Other partitions, the MBR boot code (unless the whole disk is replaced)
// and the gap after the MBR (used by GRUB and others) are never written.
package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/engine"
	"github.com/win9x-modern/w9xm/internal/fat"
	"github.com/win9x-modern/w9xm/internal/lab"
	"github.com/win9x-modern/w9xm/internal/module"
)

type Options struct {
	Device      disk.Device // the whole target disk, opened read-write
	Disk        *disk.Disk
	Choice      disk.Option
	MakeDefault bool // make Windows 98 the BIOS boot (active) partition

	Source     lab.Source
	Key        string
	Answers    lab.Answers
	AcceptEULA bool // the user accepted the Windows 98 license agreement

	Edition     string // edition id, e.g. "core"
	ModulesDir  string
	EditionsDir string
	Facts       *module.Facts

	// BootDir holds 98Reborn Boot and its BIOS layers (98reborn/boot on the
	// medium). They go into \EFI on the Windows 98 partition, so UEFI-only
	// PCs can start Windows 98. "" skips this.
	BootDir string
	// RegisterBoot adds a "98Reborn Boot" UEFI boot entry, first in the boot
	// order (the installer only sets this when it was itself started by UEFI).
	RegisterBoot bool
	// ACPI installs Windows 98 in ACPI mode (for UEFI PCs, through ReBIOS).
	ACPI bool

	WorkDir string // scratch space: the setup image lives here (sparse)
	QEMU    string
	Log     io.Writer
	Stage   func(name string) // progress: "setup", "patch", "shrink", "copy", "finish"
}

func (o *Options) stage(s string) {
	if o.Stage != nil {
		o.Stage(s)
	}
}

// Run performs the install. On error before the "shrink" stage the real disk
// is untouched.
func Run(o Options) error {
	c := o.Choice
	if !c.Available || c.Sectors < disk.MinSize {
		return errors.New("install: the chosen option isn't available")
	}
	log := o.Log
	if log == nil {
		log = io.Discard
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return err
	}
	backup, err := disk.Backup(o.Device, o.Disk.Sectors())
	if err != nil {
		return fmt.Errorf("backing up the partition table: %w", err)
	}
	os.WriteFile(filepath.Join(o.WorkDir, "partition-backup.bin"), backup, 0o644)

	// 1. Setup in a VM on the scratch image.
	o.stage("setup")
	img := filepath.Join(o.WorkDir, "win98.img")
	os.Remove(img)
	err = lab.RunSetup(lab.SetupOptions{
		Source: o.Source, Image: img, PartStart: c.Start, PartSectors: c.Sectors,
		Key: o.Key, Answers: o.Answers, AcceptEULA: o.AcceptEULA, QEMU: o.QEMU,
		WorkDir: o.WorkDir, Log: log, ACPI: o.ACPI,
	})
	if err != nil {
		return err
	}

	// 2. Patch.
	o.stage("patch")
	if err := patch(o, img); err != nil {
		return fmt.Errorf("applying the %s edition: %w", o.Edition, err)
	}
	if err := withFAT(img, func(v *fat.FS) error {
		if err := lab.RemoveLabHooks(v); err != nil {
			return err
		}
		v.MkdirAll("98REBORN")
		if err := v.WriteFile("98REBORN/PARTBACK.BIN", backup, time.Now()); err != nil {
			return err
		}
		return addBoot(v, o.BootDir)
	}); err != nil {
		return err
	}

	// 3. The real disk.
	if c.Shrink != nil {
		o.stage("shrink")
		dev := disk.PartDevice(o.Disk, c.Shrink.Num)
		if dev == "" {
			return errors.New("can't find the device of the partition to shrink")
		}
		fmt.Fprintf(log, "Shrinking %s to %d MB...\n", dev, c.ShrinkTo*disk.Sector>>20)
		if err := disk.ShrinkFS(dev, c.Shrink.FS, c.ShrinkTo*disk.Sector); err != nil {
			return err
		}
		if err := disk.ResizePartition(o.Device, c.Shrink.Num-1, c.ShrinkTo); err != nil {
			return err
		}
	}
	o.stage("copy")
	fh, err := os.Open(img)
	if err != nil {
		return err
	}
	defer fh.Close()
	v, err := fat.Open(readOnly{fh}, c.Start*disk.Sector)
	if err != nil {
		return err
	}
	last := time.Now()
	err = v.CopyUsed(o.Device, c.Start*disk.Sector, func(done, total int64) {
		if time.Since(last) > 2*time.Second {
			fmt.Fprintf(log, "Copying Windows 98 to the disk: %d%%\n", done*100/max(total, 1))
			last = time.Now()
		}
	})
	if err != nil {
		return fmt.Errorf("copying to the disk: %w", err)
	}

	o.stage("finish")
	if c.Kind == disk.KindWholeDisk {
		code := make([]byte, 440) // Microsoft's MBR boot code, from FDISK /MBR in Setup
		if _, err := fh.ReadAt(code, 0); err != nil {
			return err
		}
		err = disk.NewTable(o.Device, o.Disk.Sectors(), c.Start, c.Sectors, 0x0C, code)
	} else {
		err = disk.AddPartition(o.Device, c.Slot, c.Start, c.Sectors, 0x0C, o.MakeDefault)
	}
	if err != nil {
		return fmt.Errorf("writing the partition table: %w", err)
	}
	if s, ok := o.Device.(interface{ Sync() error }); ok {
		s.Sync()
	}
	disk.Reread(o.Disk.Path)
	if o.RegisterBoot && o.BootDir != "" {
		slot := c.Slot
		if c.Kind == disk.KindWholeDisk {
			slot = 0
		}
		if err := registerBoot(o.Disk.Path, slot+1); err != nil {
			fmt.Fprintf(log, "Couldn't add the UEFI boot entry (%v). Choose the disk in the firmware's boot menu instead.\n", err)
		}
	}
	return nil
}

// addBoot puts 98Reborn Boot on the Windows 98 partition:
//
//	\EFI\BOOT\BOOTX64.EFI       the menu (UEFI firmware's default path)
//	\EFI\98REBORN\REBIOS.EFI    BIOS layer, needs VT-x/AMD-V + IOMMU
//	\EFI\98REBORN\CSMWRAP.EFI   BIOS layer for PCs without them
//	\EFI\98REBORN\BOOT.CFG      menu settings
func addBoot(v *fat.FS, dir string) error {
	if dir == "" {
		return nil
	}
	files := map[string]string{
		"EFI/BOOT/BOOTX64.EFI":     "98reborn-boot.efi",
		"EFI/98REBORN/REBIOS.EFI":  "rebios.efi",
		"EFI/98REBORN/CSMWRAP.EFI": "csmwrapx64.efi",
		"EFI/98REBORN/NOTICE.TXT":  "NOTICE.txt",
	}
	for dst, src := range files {
		b, err := os.ReadFile(filepath.Join(dir, src))
		if err != nil {
			return fmt.Errorf("98Reborn Boot: %w", err)
		}
		if err := v.MkdirAll(filepath.ToSlash(filepath.Dir(dst))); err != nil {
			return err
		}
		if err := v.WriteFile(dst, b, time.Now()); err != nil {
			return err
		}
	}
	cfg := "; 98Reborn Boot settings\r\ntimeout=5\r\ndefault=win98\r\n; auto: ReBIOS, or CSMWrap where ReBIOS can't run. Or: rebios, csmwrap\r\nengine=auto\r\n"
	return v.WriteFile("EFI/98REBORN/BOOT.CFG", []byte(cfg), time.Now())
}

type readOnly struct{ f *os.File }

func (r readOnly) ReadAt(p []byte, off int64) (int, error) { return r.f.ReadAt(p, off) }
func (r readOnly) WriteAt(p []byte, off int64) (int, error) {
	return 0, errors.New("read-only")
}

func withFAT(img string, fn func(*fat.FS) error) error {
	fh, err := os.OpenFile(img, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer fh.Close()
	v, err := fat.OpenImage(fh)
	if err != nil {
		return err
	}
	if err := fn(v); err != nil {
		return err
	}
	if err := v.Sync(); err != nil {
		return err
	}
	return fh.Sync()
}

// patch applies the edition plus the installer's own modules: cleanup, fresh
// hardware detection on the first real start (Setup ran in a virtual PC) and
// the VESA display driver for graphics adapters without a Windows 98 driver,
// and the USB 3 (xHCI) controller driver.
func patch(o Options, img string) error {
	all, err := module.LoadDir(o.ModulesDir)
	if err != nil {
		return err
	}
	wanted := []string{"installer-cleanup", "new-hardware", "display-vesa", "usb3-xhci98"}
	var params map[string]map[string]string
	if o.Edition != "" {
		ed, err := module.LoadEdition(filepath.Join(o.EditionsDir, o.Edition))
		if err != nil {
			return err
		}
		if params, err = ed.ParamOverrides(); err != nil {
			return err
		}
		wanted = append(ed.Modules, wanted...)
	}
	ordered, err := module.Resolve(all, wanted)
	if err != nil {
		return err
	}
	tgt, err := engine.OpenImageTarget(img)
	if err != nil {
		return err
	}
	defer tgt.Close()
	plan, err := engine.NewPlan(engine.Options{Target: tgt, Facts: o.Facts, Params: params, Tool: "98Reborn installer"}, ordered)
	if err != nil {
		return err
	}
	if _, err = plan.Apply(); err != nil {
		return err
	}
	return AddExactIDs(tgt, o.Facts)
}
