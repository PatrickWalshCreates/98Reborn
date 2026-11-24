// Package compose makes the 98Reborn installer ISO: the open-source base image
// plus the 98SE/ folder copied from the user's own Windows 98 SE CD. This runs
// on the user's computer; the result is never distributed.
package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/iso9660"
	"github.com/win9x-modern/w9xm/internal/isowriter"
	"github.com/win9x-modern/w9xm/internal/media"
)

// Boot files in the base image (see package baseimage).
const (
	BIOSBoot = "boot/syslinux/isolinux.bin"
	EFIBoot  = "boot/efi.img"
	BaseInfo = "98reborn/base.json"
	// BootFloppy is the CD's El Torito boot floppy, copied by Compose.
	BootFloppy = "98reborn/cdboot.img"
)

type Options struct {
	Base string // base installer ISO (from CI or 98reborn-mkbase)
	CD   string // the user's Windows 98 SE CD image
	Out  string
	Now  time.Time
	Log  func(format string, a ...any)
}

// MediaInfo is written to 98reborn/media.json so the installer knows which
// release it is installing.
type MediaInfo struct {
	Product     string `json:"product"`
	SetupSHA256 string `json:"setup_sha256"`
	Files       int    `json:"files"`
	Composed    string `json:"composed"`
}

func isoFile(im *iso9660.Image, e iso9660.Entry, dst string) isowriter.File {
	return isowriter.File{Path: dst, Size: e.Size, ModTime: e.ModTime, Open: func() (io.ReadCloser, error) {
		sr, err := im.OpenEntry(e)
		return io.NopCloser(sr), err
	}}
}

// Compose writes the installer ISO.
func Compose(o Options) error {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	base, err := iso9660.OpenFile(o.Base)
	if err != nil {
		return fmt.Errorf("base image: %w", err)
	}
	defer base.Close()
	if _, err := base.Stat(BaseInfo); err != nil {
		return errors.New("base image: not a 98Reborn installer base (no " + BaseInfo + ")")
	}
	cd, err := media.Open(o.CD)
	if err != nil {
		return fmt.Errorf("your CD: %w", err)
	}
	defer cd.Close()
	if cd.Product != media.Win98SE {
		return fmt.Errorf("your CD is %s; 98Reborn needs Windows 98 Second Edition", cd.Product)
	}

	var files []isowriter.File
	err = base.Walk(func(e iso9660.Entry) error {
		if !e.IsDir {
			if strings.HasPrefix(strings.ToUpper(e.Path), "98SE/") {
				return nil // never carry over setup files from somewhere else
			}
			files = append(files, isoFile(base, e, e.Path))
		}
		return nil
	})
	if err != nil {
		return err
	}
	nBase := len(files)
	prefix := strings.ToUpper(cd.SetupDir) + "/"
	err = cd.ISO.Walk(func(e iso9660.Entry) error {
		if !e.IsDir && strings.HasPrefix(strings.ToUpper(e.Path), prefix) {
			files = append(files, isoFile(cd.ISO, e, "98SE/"+e.Path[len(prefix):]))
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The CD's boot floppy: the installer boots Setup's VM from it.
	floppy, _, err := cd.ISO.ReadBootImage()
	if err != nil {
		return fmt.Errorf("your CD: boot image: %w", err)
	}
	files = append(files, isowriter.File{Path: BootFloppy, Size: int64(len(floppy)), ModTime: o.Now,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(floppy)), nil }})
	info, _ := json.MarshalIndent(MediaInfo{Product: string(cd.Product), SetupSHA256: cd.SetupSHA256,
		Files: len(files) - nBase - 1, Composed: o.Now.Format(time.RFC3339)}, "", "  ")
	files = append(files, isowriter.File{Path: "98reborn/media.json", Size: int64(len(info)), ModTime: o.Now,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(info))), nil }})
	logf("base: %d files; your CD: %d setup files from %s", nBase, len(files)-nBase-2, path.Clean(cd.SetupDir))

	f, err := os.Create(o.Out)
	if err != nil {
		return err
	}
	err = isowriter.Write(f, isowriter.Options{
		VolumeID: "98REBORN",
		Files:    files,
		BIOS:     &isowriter.BootEntry{Path: BIOSBoot, Emulation: "none", LoadSectors: 4, InfoTable: true},
		EFI:      &isowriter.BootEntry{Path: EFIBoot, Emulation: "none"},
		Now:      o.Now,
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(o.Out)
	}
	return err
}
