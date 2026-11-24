package disk

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Device is a whole disk (or disk image) opened read-write.
type Device interface {
	io.ReaderAt
	io.WriterAt
}

func chs(lba int64) [3]byte {
	const heads, spt = 255, 63
	if lba >= 1024*heads*spt {
		return [3]byte{0xFE, 0xFF, 0xFF}
	}
	c := lba / (heads * spt)
	h := (lba / spt) % heads
	s := lba%spt + 1
	return [3]byte{byte(h), byte(s) | byte((c>>2)&0xC0), byte(c)}
}

func putEntry(e []byte, active bool, typ byte, start, n int64) {
	for i := range e[:16] {
		e[i] = 0
	}
	if active {
		e[0] = 0x80
	}
	s, end := chs(start), chs(start+n-1)
	copy(e[1:4], s[:])
	e[4] = typ
	copy(e[5:8], end[:])
	binary.LittleEndian.PutUint32(e[8:], uint32(start))
	binary.LittleEndian.PutUint32(e[12:], uint32(n))
}

func readMBR(dev Device) ([]byte, error) {
	mbr := make([]byte, Sector)
	if _, err := dev.ReadAt(mbr, 0); err != nil {
		return nil, err
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, errors.New("disk: no MBR")
	}
	return mbr, nil
}

// AddPartition writes a new primary entry in slot (0-3), leaving the other
// entries and the boot code untouched. With active, it becomes the only
// active partition.
func AddPartition(dev Device, slot int, start, n int64, typ byte, active bool) error {
	if slot < 0 || slot > 3 || start+n > 1<<32 {
		return fmt.Errorf("disk: bad partition slot %d / range", slot)
	}
	mbr, err := readMBR(dev)
	if err != nil {
		return err
	}
	if mbr[446+16*slot+4] != 0 {
		return fmt.Errorf("disk: slot %d is in use", slot)
	}
	if active {
		for i := 0; i < 4; i++ {
			mbr[446+16*i] = 0
		}
	}
	putEntry(mbr[446+16*slot:], active, typ, start, n)
	_, err = dev.WriteAt(mbr, 0)
	return err
}

// ResizePartition changes the length of the primary partition in slot.
func ResizePartition(dev Device, slot int, n int64) error {
	mbr, err := readMBR(dev)
	if err != nil {
		return err
	}
	e := mbr[446+16*slot:]
	if e[4] == 0 {
		return fmt.Errorf("disk: slot %d is empty", slot)
	}
	putEntry(e, e[0] == 0x80, e[4], int64(binary.LittleEndian.Uint32(e[8:])), n)
	_, err = dev.WriteAt(mbr, 0)
	return err
}

// SetActive makes slot the only active (BIOS boot) partition.
func SetActive(dev Device, slot int) error {
	mbr, err := readMBR(dev)
	if err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		mbr[446+16*i] = 0
		if i == slot {
			mbr[446+16*i] = 0x80
		}
	}
	_, err = dev.WriteAt(mbr, 0)
	return err
}

// NewTable replaces the disk's partitioning with a fresh MBR holding one
// active partition. Any GPT (both copies) is wiped so firmware and other
// tools don't see stale entries. bootCode (up to 440 bytes) is the MBR boot
// program.
func NewTable(dev Device, diskSectors, start, n int64, typ byte, bootCode []byte) error {
	zero := make([]byte, 34*Sector)
	if _, err := dev.WriteAt(zero, 0); err != nil {
		return err
	}
	if diskSectors > 34 {
		if _, err := dev.WriteAt(zero[:33*Sector], (diskSectors-33)*Sector); err != nil {
			return err
		}
	}
	mbr := make([]byte, Sector)
	copy(mbr[:440], bootCode)
	sig := make([]byte, 4)
	rand.Read(sig)
	copy(mbr[440:444], sig)
	putEntry(mbr[446:], true, typ, start, n)
	mbr[510], mbr[511] = 0x55, 0xAA
	_, err := dev.WriteAt(mbr, 0)
	return err
}

// Backup returns the partitioning areas of the disk (first 1 MiB and the
// last 33 sectors, where a backup GPT lives) for safekeeping.
func Backup(dev Device, diskSectors int64) ([]byte, error) {
	head := make([]byte, 2048*Sector)
	if _, err := dev.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	tail := make([]byte, 33*Sector)
	if diskSectors > 2048+33 {
		if _, err := dev.ReadAt(tail, (diskSectors-33)*Sector); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return append(head, tail...), nil
}
