package fat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const sector = 512

// Partition is a primary MBR partition entry.
type Partition struct {
	Bootable bool
	Type     byte // 0x0C = FAT32 LBA, 0x0B = FAT32 CHS, 0x06 = FAT16, 0x0E = FAT16 LBA
	StartLBA uint32
	Sectors  uint32
}

func chs(lba uint32) [3]byte {
	const heads, spt = 255, 63
	c := lba / (heads * spt)
	h := (lba / spt) % heads
	s := lba%spt + 1
	if c > 1023 {
		c, h, s = 1023, 254, 63
	}
	return [3]byte{byte(h), byte(s) | byte(c>>8&3)<<6, byte(c)}
}

// WriteMBR writes a partition table, keeping any existing boot code.
func WriteMBR(dev Device, parts []Partition) error {
	if len(parts) > 4 {
		return errors.New("fat: at most 4 primary partitions")
	}
	mbr := make([]byte, sector)
	if _, err := dev.ReadAt(mbr, 0); err != nil && !strings.Contains(err.Error(), "EOF") {
		return err
	}
	for i := 446; i < 510; i++ {
		mbr[i] = 0
	}
	for i, p := range parts {
		e := mbr[446+16*i:]
		if p.Bootable {
			e[0] = 0x80
		}
		s := chs(p.StartLBA)
		copy(e[1:4], s[:])
		e[4] = p.Type
		end := chs(p.StartLBA + p.Sectors - 1)
		copy(e[5:8], end[:])
		binary.LittleEndian.PutUint32(e[8:], p.StartLBA)
		binary.LittleEndian.PutUint32(e[12:], p.Sectors)
	}
	mbr[510], mbr[511] = 0x55, 0xAA
	_, err := dev.WriteAt(mbr, 0)
	return err
}

func ReadMBR(dev Device) ([]Partition, error) {
	mbr := make([]byte, sector)
	if _, err := dev.ReadAt(mbr, 0); err != nil {
		return nil, err
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, errors.New("fat: no MBR signature")
	}
	var out []Partition
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i:]
		if e[4] == 0 {
			continue
		}
		out = append(out, Partition{e[0] == 0x80, e[4], binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])})
	}
	return out, nil
}

func isFATType(t byte) bool {
	switch t {
	case 0x01, 0x04, 0x06, 0x0B, 0x0C, 0x0E:
		return true
	}
	return false
}

// OpenImage opens a floppy image (FAT boot sector at 0) or the first FAT
// partition of a partitioned disk image.
func OpenImage(dev Device) (*FS, error) {
	bs := make([]byte, sector)
	if _, err := dev.ReadAt(bs, 0); err != nil {
		return nil, err
	}
	if (bs[0] == 0xEB || bs[0] == 0xE9) && (strings.HasPrefix(string(bs[54:]), "FAT") || strings.HasPrefix(string(bs[82:]), "FAT32")) {
		return Open(dev, 0)
	}
	parts, err := ReadMBR(dev)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		if isFATType(p.Type) {
			return Open(dev, int64(p.StartLBA)*sector)
		}
	}
	return nil, errors.New("fat: no FAT partition found")
}

// Format32 creates a FAT32 file system of size bytes at base. The boot code
// only hands control back to the BIOS (int 18h); Windows Setup or SYS writes a
// real boot sector later.
func Format32(dev Device, base, size int64, label string) error {
	total := size / sector
	if total < 66600 {
		return errors.New("fat: FAT32 needs at least ~33 MB")
	}
	if total > 0xFFFFFFFF {
		return errors.New("fat: volume too large")
	}
	var spc int64
	switch { // Microsoft's FAT32 cluster-size table
	case total <= 532480: // 260 MB
		spc = 1
	case total <= 16777216: // 8 GB
		spc = 8
	case total <= 33554432:
		spc = 16
	case total <= 67108864:
		spc = 32
	default:
		spc = 64
	}
	const reserved, nFATs = 32, 2
	tmp1 := total - reserved
	tmp2 := (256*spc + nFATs) / 2
	fatSz := (tmp1 + tmp2 - 1) / tmp2
	if (total-reserved-nFATs*fatSz)/spc < 65525 {
		return errors.New("fat: volume too small for FAT32")
	}

	bs := make([]byte, sector)
	copy(bs, []byte{0xEB, 0x58, 0x90})
	copy(bs[3:], "MSWIN4.1")
	le := binary.LittleEndian
	le.PutUint16(bs[11:], sector)
	bs[13] = byte(spc)
	le.PutUint16(bs[14:], reserved)
	bs[16] = nFATs
	bs[21] = 0xF8
	le.PutUint16(bs[24:], 63)
	le.PutUint16(bs[26:], 255)
	le.PutUint32(bs[28:], uint32(base/sector))
	le.PutUint32(bs[32:], uint32(total))
	le.PutUint32(bs[36:], uint32(fatSz))
	le.PutUint32(bs[44:], 2)
	le.PutUint16(bs[48:], 1)
	le.PutUint16(bs[50:], 6)
	bs[64] = 0x80
	bs[66] = 0x29
	le.PutUint32(bs[67:], uint32(time.Now().Unix()))
	lab := strings.ToUpper(label)
	if lab == "" {
		lab = "NO NAME"
	}
	copy(bs[71:], fmt.Sprintf("%-11.11s", lab))
	copy(bs[82:], "FAT32   ")
	copy(bs[90:], []byte{0xCD, 0x18, 0xF4, 0xEB, 0xFD}) // int 18h; hlt; jmp $-1
	bs[510], bs[511] = 0x55, 0xAA

	info := make([]byte, sector)
	le.PutUint32(info[0:], 0x41615252)
	le.PutUint32(info[484:], 0x61417272)
	le.PutUint32(info[488:], 0xFFFFFFFF)
	le.PutUint32(info[492:], 0xFFFFFFFF)
	le.PutUint32(info[508:], 0xAA550000)
	third := make([]byte, sector)
	third[510], third[511] = 0x55, 0xAA

	// Zero reserved sectors, both FATs and the root cluster.
	zeroLen := (reserved + nFATs*fatSz + spc) * sector
	zero := make([]byte, 1<<20)
	for off := int64(0); off < zeroLen; off += int64(len(zero)) {
		n := min(int64(len(zero)), zeroLen-off)
		if _, err := dev.WriteAt(zero[:n], base+off); err != nil {
			return err
		}
	}
	for _, s := range []struct {
		lba  int64
		data []byte
	}{{0, bs}, {1, info}, {2, third}, {6, bs}, {7, info}, {8, third}} {
		if _, err := dev.WriteAt(s.data, base+s.lba*sector); err != nil {
			return err
		}
	}
	fat0 := make([]byte, 12)
	le.PutUint32(fat0[0:], 0x0FFFFFF8)
	le.PutUint32(fat0[4:], 0x0FFFFFFF)
	le.PutUint32(fat0[8:], 0x0FFFFFFF) // root directory: one cluster, end of chain
	for i := int64(0); i < nFATs; i++ {
		if _, err := dev.WriteAt(fat0, base+(reserved+i*fatSz)*sector); err != nil {
			return err
		}
	}
	if label != "" {
		vol := make([]byte, dirEntrySize)
		copy(vol, fmt.Sprintf("%-11.11s", lab))
		vol[11] = AttrVolume
		d, t := dosDateTime(time.Now())
		le.PutUint16(vol[22:], t)
		le.PutUint16(vol[24:], d)
		if _, err := dev.WriteAt(vol, base+(reserved+nFATs*fatSz)*sector); err != nil {
			return err
		}
	}
	return nil
}
