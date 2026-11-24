package disk

import (
	"io"
	"strings"

	"github.com/win9x-modern/w9xm/internal/fat"
)

// partDevice gives the FAT reader a view of one partition.
type section struct {
	r    io.ReaderAt
	base int64
}

func (s section) ReadAt(p []byte, off int64) (int, error) { return s.r.ReadAt(p, s.base+off) }
func (s section) WriteAt(p []byte, off int64) (int, error) {
	return 0, io.ErrClosedPipe // read-only view
}

// InspectFAT names what's on a FAT partition without mounting it: DOS /
// Windows 9x installs and EFI system partitions.
func InspectFAT(r io.ReaderAt, p *Part) {
	if !strings.HasPrefix(p.FS, "fat") && p.FS != "efi" {
		return
	}
	v, err := fat.Open(section{r, 0}, p.Start*Sector)
	if err != nil {
		return
	}
	has := func(path string) bool { _, err := v.Stat(path); return err == nil }
	switch {
	// Windows 98 first: 98Reborn Boot puts an EFI folder on its partition.
	case has("WINDOWS/WIN.COM") && has("IO.SYS"):
		p.OS = "Windows 9x"
		if b, err := v.ReadFile("MSDOS.SYS"); err == nil && strings.Contains(string(b), "WinVer=4.10.2222") {
			p.OS = "Windows 98 SE"
		}
	case has("IO.SYS") || has("IBMBIO.COM"):
		p.OS = "DOS"
	case has("EFI/Microsoft/Boot/bootmgfw.efi"):
		p.FS, p.OS = "efi", "Windows Boot Manager"
	case has("EFI"):
		p.FS = "efi"
	}
}

// Inspect reads the partition table and identifies every partition, using
// signatures and (on Linux) read-only mounts for OS detection and shrink limits.
func Inspect(dev io.ReaderAt, d *Disk) error {
	if err := ReadTable(dev, d); err != nil {
		return err
	}
	for _, p := range d.Parts {
		if p.Extended {
			continue
		}
		Probe(dev, p)
		InspectFAT(dev, p)
		inspectOS(d, p) // platform-specific
	}
	return nil
}
