package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/win9x-modern/w9xm/internal/compose"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/install"
	"github.com/win9x-modern/w9xm/internal/lab"
	"github.com/win9x-modern/w9xm/internal/media"
)

// cmdMakeInstaller builds the bootable 98Reborn installer ISO from the base
// image and the user's own Windows 98 SE CD.
func cmdMakeInstaller(args []string) error {
	fs := flag.NewFlagSet("make-installer", flag.ContinueOnError)
	base := fs.String("base", "", "98Reborn installer base image (98reborn-base.iso)")
	cd := fs.String("cd", "", "your Windows 98 SE CD image")
	out := fs.String("out", "98Reborn-installer.iso", "output ISO")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *cd == "" {
		return errors.New("make-installer needs --base and --cd")
	}
	err := compose.Compose(compose.Options{Base: *base, CD: *cd, Out: *out,
		Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }})
	if err != nil {
		return err
	}
	fi, _ := os.Stat(*out)
	fmt.Printf("wrote %s (%d MB). Write it to a USB stick with Rufus (ISO mode) and boot the target PC from it.\n", *out, fi.Size()>>20)
	return nil
}

// cmdInstallDisk runs the installer's partition planner and install pipeline
// against a disk image (development and testing on any OS).
func cmdInstallDisk(args []string) error {
	fs := flag.NewFlagSet("install-disk", flag.ContinueOnError)
	diskPath := fs.String("disk", "", "disk image to install onto (must exist)")
	cd := fs.String("cd", "", "your Windows 98 SE CD image")
	keyFile := fs.String("key-file", "", "file with the product key (default: product-key.txt next to the CD)")
	edition := fs.String("edition", "core", "edition")
	modDir := fs.String("modules", "modules", "")
	edDir := fs.String("editions", "editions", "")
	pick := fs.Int("option", -1, "option number from the plan (default: the recommended one)")
	wantGB := fs.Int("size-gb", 32, "Windows 98 partition size")
	work := fs.String("work", "lab/install-disk", "scratch directory")
	makeDefault := fs.Bool("make-default", true, "make Windows 98 the active (BIOS boot) partition")
	accept := fs.Bool("accept-eula", false, "you accept the Windows 98 license agreement")
	planOnly := fs.Bool("plan", false, "only show the options")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *diskPath == "" {
		return errors.New("install-disk needs --disk")
	}
	f, err := os.OpenFile(*diskPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	d := &disk.Disk{Name: filepath.Base(*diskPath), Path: *diskPath, Size: st.Size(), Bus: "sata", Model: "image", LogicalSec: 512}
	if err := disk.Inspect(f, d); err != nil {
		return err
	}
	fmt.Printf("%s (%s partitioning):\n%s\n", d.Name, d.Table, disk.Layout(d, nil))
	opts := disk.Plan([]*disk.Disk{d}, disk.Requirements{WantSize: int64(*wantGB) << 30 / disk.Sector})
	choice := -1
	for i, o := range opts {
		tag := ""
		if o.Recommended {
			tag = " (recommended)"
		}
		if !o.Available {
			fmt.Printf("  -  %s: not possible: %s\n", o.Title, o.Reason)
			continue
		}
		fmt.Printf("  %d. %s%s\n     %s\n", i, o.Title, tag, o.Detail)
		if (*pick < 0 && o.Recommended) || *pick == i {
			choice = i
		}
	}
	if *planOnly {
		return nil
	}
	if choice < 0 {
		return errors.New("no usable option selected")
	}
	if *cd == "" {
		return errors.New("install-disk needs --cd")
	}
	o := opts[choice]
	fmt.Printf("\nInstalling with option %d. After:\n%s\n", choice, disk.Layout(d, &o))
	m, err := media.Open(*cd)
	if err != nil {
		return err
	}
	defer m.Close()
	if *keyFile == "" {
		*keyFile = filepath.Join(filepath.Dir(*cd), "product-key.txt")
	}
	key, err := lab.ReadProductKey(*keyFile)
	if err != nil {
		return err
	}
	start := time.Now()
	err = install.Run(install.Options{
		Device: f, Disk: d, Choice: o, MakeDefault: *makeDefault,
		Source: lab.ISOSource(m), Key: key, Answers: lab.Answers{Name: "98Reborn Test", Computer: "RETRO98", TimeZone: "Pacific"},
		AcceptEULA: *accept, Edition: *edition, ModulesDir: *modDir, EditionsDir: *edDir,
		WorkDir: *work, Log: os.Stdout,
		Stage: func(s string) { fmt.Printf("[%s] stage: %s\n", time.Since(start).Round(time.Second), s) },
	})
	if err != nil {
		return err
	}
	disk.Inspect(f, d)
	fmt.Printf("Done in %s. The disk now:\n%s", time.Since(start).Round(time.Second), disk.Layout(d, nil))
	return nil
}
