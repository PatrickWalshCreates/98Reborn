// Package disk reads partition tables, identifies file systems and installed
// operating systems, and plans where Windows 98 can go on a PC that may already
// run other systems ("smart partitioner"). Planning is pure: nothing is
// written until Apply.
package disk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const Sector = 512

// Partition table kinds.
const (
	TableNone = "none"
	TableMBR  = "mbr"
	TableGPT  = "gpt"
)

// Disk is one physical disk.
type Disk struct {
	Name       string // sda, nvme0n1
	Path       string // /dev/sda, or an image file
	Model      string
	Bus        string // sata, nvme, usb, virtio, ide, unknown
	Size       int64  // bytes
	LogicalSec int    // logical sector size; Windows 98 needs 512
	Removable  bool
	IsMedium   bool // the installer booted from it
	Table      string
	DiskSig    uint32 // MBR disk signature
	Parts      []*Part
}

// Sectors is the disk size in 512-byte sectors.
func (d *Disk) Sectors() int64 { return d.Size / Sector }

// Part is one partition.
type Part struct {
	Num       int    // 1-4 primary (MBR slot+1), 5+ logical, or GPT entry number
	Start     int64  // first sector
	Sectors   int64  // length in sectors
	Type      byte   // MBR type
	GPTType   string // GPT type GUID (lower case)
	Active    bool   // MBR boot flag
	Extended  bool   // MBR extended container
	FS        string // ntfs, fat12, fat16, fat32, exfat, ext4, btrfs, xfs, swap, bitlocker, apfs, hfs+, "" unknown
	Label     string
	OS        string // detected operating system, e.g. "Windows 11", "Ubuntu 24.04", "Windows 98"
	BitLocker bool
	Hibernate bool // Windows hibernated or Fast Startup on: unsafe to resize
	// Shrinking (filled by inspection on Linux).
	MinBytes     int64  // smallest safe size for the file system; 0 = unknown
	ShrinkReason string // why it can't be shrunk ("" = it can, if MinBytes > 0)
}

func (p *Part) End() int64 { return p.Start + p.Sectors }

// Describe is a one-line, human description.
func (p *Part) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", human(p.Sectors*Sector))
	switch {
	case p.Extended:
		b.WriteString(" extended partition")
	case p.FS != "":
		b.WriteString(" " + fsName(p.FS))
	default:
		b.WriteString(" partition")
	}
	if p.Label != "" {
		fmt.Fprintf(&b, " %q", p.Label)
	}
	if p.OS != "" {
		b.WriteString(" with " + p.OS)
	}
	return b.String()
}

func fsName(fs string) string {
	switch fs {
	case "ntfs":
		return "NTFS"
	case "fat12", "fat16", "fat32":
		return strings.ToUpper(fs)
	case "exfat":
		return "exFAT"
	case "bitlocker":
		return "BitLocker"
	case "apfs":
		return "APFS"
	case "hfs+":
		return "HFS+"
	case "swap":
		return "Linux swap"
	case "efi":
		return "EFI system"
	}
	return fs
}

func human(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 10<<30:
		return fmt.Sprintf("%.0f GB", float64(n)/(1<<30))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%d MB", n>>20)
	}
}

// ReadTable fills d.Table, d.DiskSig and d.Parts from the device.
func ReadTable(r io.ReaderAt, d *Disk) error {
	mbr := make([]byte, Sector)
	if _, err := r.ReadAt(mbr, 0); err != nil {
		return err
	}
	d.Parts = nil
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		d.Table = TableNone
		return nil
	}
	le := binary.LittleEndian
	for i := 0; i < 4; i++ {
		if mbr[446+16*i+4] == 0xEE {
			d.Table = TableGPT
			return readGPT(r, d)
		}
	}
	d.Table = TableMBR
	d.DiskSig = le.Uint32(mbr[440:])
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i:]
		if e[4] == 0 {
			continue
		}
		p := &Part{Num: i + 1, Active: e[0] == 0x80, Type: e[4],
			Start: int64(le.Uint32(e[8:])), Sectors: int64(le.Uint32(e[12:]))}
		if p.Type == 0x05 || p.Type == 0x0F || p.Type == 0x85 {
			p.Extended = true
			d.Parts = append(d.Parts, p)
			d.Parts = append(d.Parts, readLogical(r, p.Start)...)
			continue
		}
		d.Parts = append(d.Parts, p)
	}
	// A FAT/NTFS boot sector at LBA 0 means a "superfloppy" (no table).
	if len(d.Parts) == 0 && (bytes.Equal(mbr[3:7], []byte("NTFS")) || bytes.Equal(mbr[82:87], []byte("FAT32"))) {
		d.Table = TableNone
	}
	return nil
}

// readLogical walks the extended partition's EBR chain.
func readLogical(r io.ReaderAt, extStart int64) []*Part {
	le := binary.LittleEndian
	var out []*Part
	ebr := extStart
	for n := 5; n < 64; n++ {
		b := make([]byte, Sector)
		if _, err := r.ReadAt(b, ebr*Sector); err != nil || b[510] != 0x55 || b[511] != 0xAA {
			break
		}
		e := b[446:]
		if e[4] != 0 {
			out = append(out, &Part{Num: n, Type: e[4], Start: ebr + int64(le.Uint32(e[8:])), Sectors: int64(le.Uint32(e[12:]))})
		}
		next := b[462:]
		if next[4] == 0 {
			break
		}
		ebr = extStart + int64(le.Uint32(next[8:]))
	}
	return out
}

func guidString(b []byte) string {
	le := binary.LittleEndian
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", le.Uint32(b[0:]), le.Uint16(b[4:]), le.Uint16(b[6:]), b[8:10], b[10:16])
}

func readGPT(r io.ReaderAt, d *Disk) error {
	h := make([]byte, Sector)
	if _, err := r.ReadAt(h, Sector); err != nil {
		return err
	}
	if string(h[:8]) != "EFI PART" {
		return fmt.Errorf("%s: protective MBR but no GPT header", d.Name)
	}
	le := binary.LittleEndian
	lba := int64(le.Uint64(h[72:]))
	n := int(le.Uint32(h[80:]))
	size := int(le.Uint32(h[84:]))
	if n > 1024 || size < 128 || size > 4096 {
		return fmt.Errorf("%s: unusual GPT (%d entries of %d bytes)", d.Name, n, size)
	}
	tab := make([]byte, n*size)
	if _, err := r.ReadAt(tab, lba*Sector); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		e := tab[i*size:]
		if allZero(e[:16]) {
			continue
		}
		first, last := int64(le.Uint64(e[32:])), int64(le.Uint64(e[40:]))
		name := decodeUTF16(e[56:128])
		d.Parts = append(d.Parts, &Part{Num: i + 1, GPTType: guidString(e[:16]), Start: first, Sectors: last - first + 1, Label: name})
	}
	return nil
}

func decodeUTF16(b []byte) string {
	var r []rune
	for i := 0; i+1 < len(b); i += 2 {
		c := rune(binary.LittleEndian.Uint16(b[i:]))
		if c == 0 {
			break
		}
		r = append(r, c)
	}
	return string(r)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Probe identifies the file system at the partition's start from its
// signatures (no mounting).
func Probe(r io.ReaderAt, p *Part) {
	b := make([]byte, 4096)
	if _, err := r.ReadAt(b, p.Start*Sector); err != nil {
		return
	}
	switch {
	case bytes.Equal(b[3:11], []byte("-FVE-FS-")):
		p.FS, p.BitLocker = "bitlocker", true
	case bytes.Equal(b[3:11], []byte("NTFS    ")):
		p.FS = "ntfs"
	case bytes.Equal(b[3:11], []byte("EXFAT   ")):
		p.FS = "exfat"
	case bytes.Equal(b[82:87], []byte("FAT32")):
		p.FS = "fat32"
		p.Label = strings.TrimSpace(string(b[71:82]))
	case bytes.Equal(b[54:59], []byte("FAT16")):
		p.FS = "fat16"
		p.Label = strings.TrimSpace(string(b[43:54]))
	case bytes.Equal(b[54:59], []byte("FAT12")):
		p.FS = "fat12"
	case bytes.Equal(b[32:36], []byte("NXSB")):
		p.FS = "apfs"
	case bytes.Equal(b[1024:1026], []byte("H+")) || bytes.Equal(b[1024:1026], []byte("HX")):
		p.FS = "hfs+"
	case binary.LittleEndian.Uint16(b[1024+56:]) == 0xEF53:
		p.FS = "ext4" // ext2/3/4 share the magic; resize2fs handles all three
		p.Label = strings.TrimRight(string(b[1024+120:1024+136]), "\x00")
	case bytes.Equal(b[0:4], []byte("XFSB")):
		p.FS = "xfs"
	case bytes.Equal(b[4086:4096], []byte("SWAPSPACE2")):
		p.FS = "swap"
	}
	if p.FS == "" {
		// btrfs superblock lives at 64 KiB.
		s := make([]byte, 8)
		if _, err := r.ReadAt(s, p.Start*Sector+0x10040); err == nil && string(s) == "_BHRfS_M" {
			p.FS = "btrfs"
		}
	}
	if p.GPTType == "c12a7328-f81f-11d2-ba4b-00a0c93ec93b" || p.Type == 0xEF {
		p.FS = "efi"
	}
}
