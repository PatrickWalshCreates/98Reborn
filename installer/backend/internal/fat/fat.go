// Package fat reads and writes FAT12/16/32 file systems inside disk images or
// raw devices, with long file names, so the toolchain can prepare and patch
// Windows 98 drives on any host OS without mounting them.
package fat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
)

// Device is anything we can read and write at offsets: an *os.File, a raw disk.
type Device interface {
	io.ReaderAt
	io.WriterAt
}

const (
	AttrReadOnly  = 0x01
	AttrHidden    = 0x02
	AttrSystem    = 0x04
	AttrVolume    = 0x08
	AttrDir       = 0x10
	AttrArchive   = 0x20
	attrLFN       = 0x0F
	dirEntrySize  = 32
	entryDeleted  = 0xE5
	maxChainSteps = 1 << 28
)

type FS struct {
	dev  Device
	base int64

	Type         int // 12, 16 or 32
	bps          int
	spc          int
	clusterSize  int
	reserved     int
	nFATs        int
	fatSectors   int
	rootEntries  int
	totalSectors int64
	rootCluster  uint32
	fsInfo       int

	fatStart  int64
	rootStart int64 // FAT12/16 fixed root directory
	dataStart int64
	clusters  uint32 // number of data clusters (valid cluster numbers are 2..clusters+1)

	fat      []uint32
	dirty    bool
	nextFree uint32
	eoc      uint32
}

// Open parses the volume whose boot sector is at byte offset base.
func Open(dev Device, base int64) (*FS, error) {
	bs := make([]byte, 512)
	if _, err := dev.ReadAt(bs, base); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if bs[510] != 0x55 || bs[511] != 0xAA {
		return nil, errors.New("fat: no boot sector signature")
	}
	f := &FS{dev: dev, base: base}
	f.bps = int(le.Uint16(bs[11:]))
	f.spc = int(bs[13])
	f.reserved = int(le.Uint16(bs[14:]))
	f.nFATs = int(bs[16])
	f.rootEntries = int(le.Uint16(bs[17:]))
	f.totalSectors = int64(le.Uint16(bs[19:]))
	if f.totalSectors == 0 {
		f.totalSectors = int64(le.Uint32(bs[32:]))
	}
	f.fatSectors = int(le.Uint16(bs[22:]))
	if f.fatSectors == 0 {
		f.fatSectors = int(le.Uint32(bs[36:]))
		f.rootCluster = le.Uint32(bs[44:])
		f.fsInfo = int(le.Uint16(bs[48:]))
	}
	switch f.bps {
	case 512, 1024, 2048, 4096:
	default:
		return nil, fmt.Errorf("fat: unsupported sector size %d", f.bps)
	}
	if f.spc == 0 || f.spc&(f.spc-1) != 0 || f.nFATs == 0 || f.reserved == 0 || f.fatSectors == 0 {
		return nil, errors.New("fat: invalid BPB")
	}
	f.clusterSize = f.bps * f.spc
	rootSectors := (f.rootEntries*dirEntrySize + f.bps - 1) / f.bps
	f.fatStart = int64(f.reserved) * int64(f.bps)
	f.rootStart = f.fatStart + int64(f.nFATs*f.fatSectors)*int64(f.bps)
	f.dataStart = f.rootStart + int64(rootSectors)*int64(f.bps)
	dataSectors := f.totalSectors - int64(f.reserved) - int64(f.nFATs*f.fatSectors) - int64(rootSectors)
	if dataSectors <= 0 {
		return nil, errors.New("fat: volume too small")
	}
	f.clusters = uint32(dataSectors / int64(f.spc))
	switch {
	case f.clusters < 4085:
		f.Type, f.eoc = 12, 0xFFF
	case f.clusters < 65525:
		f.Type, f.eoc = 16, 0xFFFF
	default:
		f.Type, f.eoc = 32, 0x0FFFFFFF
		if f.rootCluster < 2 {
			return nil, errors.New("fat: invalid FAT32 root cluster")
		}
	}
	if err := f.loadFAT(); err != nil {
		return nil, err
	}
	f.nextFree = 2
	return f, nil
}

func (f *FS) loadFAT() error {
	raw := make([]byte, f.fatSectors*f.bps)
	if _, err := f.dev.ReadAt(raw, f.base+f.fatStart); err != nil {
		return err
	}
	n := f.clusters + 2
	f.fat = make([]uint32, n)
	for c := uint32(0); c < n; c++ {
		switch f.Type {
		case 12:
			o := c + c/2
			if int(o)+1 >= len(raw) {
				return errors.New("fat: FAT table too small")
			}
			v := uint32(binary.LittleEndian.Uint16(raw[o:]))
			if c&1 == 1 {
				v >>= 4
			}
			f.fat[c] = v & 0xFFF
		case 16:
			f.fat[c] = uint32(binary.LittleEndian.Uint16(raw[2*c:]))
		case 32:
			f.fat[c] = binary.LittleEndian.Uint32(raw[4*c:])
		}
	}
	return nil
}

// Sync writes the FAT (all copies) and FSInfo back if anything changed.
func (f *FS) Sync() error {
	if !f.dirty {
		return nil
	}
	raw := make([]byte, f.fatSectors*f.bps)
	if _, err := f.dev.ReadAt(raw, f.base+f.fatStart); err != nil {
		return err
	}
	for c, v := range f.fat {
		switch f.Type {
		case 12:
			o := c + c/2
			cur := binary.LittleEndian.Uint16(raw[o:])
			if c&1 == 1 {
				cur = cur&0x000F | uint16(v&0xFFF)<<4
			} else {
				cur = cur&0xF000 | uint16(v&0xFFF)
			}
			binary.LittleEndian.PutUint16(raw[o:], cur)
		case 16:
			binary.LittleEndian.PutUint16(raw[2*c:], uint16(v))
		case 32:
			binary.LittleEndian.PutUint32(raw[4*c:], v)
		}
	}
	for i := 0; i < f.nFATs; i++ {
		if _, err := f.dev.WriteAt(raw, f.base+f.fatStart+int64(i*f.fatSectors*f.bps)); err != nil {
			return err
		}
	}
	if f.Type == 32 && f.fsInfo > 0 {
		var free uint32
		for c := uint32(2); c < f.clusters+2; c++ {
			if f.get(c) == 0 {
				free++
			}
		}
		info := make([]byte, 8)
		binary.LittleEndian.PutUint32(info[0:], free)
		binary.LittleEndian.PutUint32(info[4:], f.nextFree)
		if _, err := f.dev.WriteAt(info, f.base+int64(f.fsInfo*f.bps)+488); err != nil {
			return err
		}
	}
	f.dirty = false
	return nil
}

func (f *FS) isEOC(v uint32) bool {
	switch f.Type {
	case 12:
		return v >= 0xFF8
	case 16:
		return v >= 0xFFF8
	}
	return v&0x0FFFFFFF >= 0x0FFFFFF8
}

func (f *FS) get(c uint32) uint32 {
	if f.Type == 32 {
		return f.fat[c] & 0x0FFFFFFF
	}
	return f.fat[c]
}

func (f *FS) set(c, v uint32) {
	if f.Type == 32 {
		v = f.fat[c]&0xF0000000 | v&0x0FFFFFFF
	}
	f.fat[c] = v
	f.dirty = true
}

func (f *FS) validCluster(c uint32) bool { return c >= 2 && c < f.clusters+2 }

func (f *FS) chain(start uint32) ([]uint32, error) {
	var out []uint32
	for c := start; ; {
		if !f.validCluster(c) {
			return nil, fmt.Errorf("fat: broken cluster chain at %d", c)
		}
		out = append(out, c)
		if len(out) > maxChainSteps || len(out) > int(f.clusters) {
			return nil, errors.New("fat: cluster chain loops")
		}
		n := f.get(c)
		if f.isEOC(n) {
			return out, nil
		}
		c = n
	}
}

func (f *FS) clusterOffset(c uint32) int64 {
	return f.base + f.dataStart + int64(c-2)*int64(f.clusterSize)
}

func (f *FS) freeChain(start uint32) error {
	if start == 0 {
		return nil
	}
	ch, err := f.chain(start)
	if err != nil {
		return err
	}
	for _, c := range ch {
		f.set(c, 0)
	}
	if ch[0] < f.nextFree {
		f.nextFree = ch[0]
	}
	return nil
}

// alloc allocates n clusters (prefers contiguous runs) and links them.
func (f *FS) alloc(n int) ([]uint32, error) {
	if n == 0 {
		return nil, nil
	}
	out := make([]uint32, 0, n)
	c := f.nextFree
	for scanned := uint32(0); len(out) < n && scanned < f.clusters; scanned++ {
		if !f.validCluster(c) {
			c = 2
		}
		if f.get(c) == 0 {
			out = append(out, c)
		}
		c++
	}
	if len(out) < n {
		return nil, errors.New("fat: disk full")
	}
	for i, c := range out {
		if i+1 < len(out) {
			f.set(c, out[i+1])
		} else {
			f.set(c, f.eoc)
		}
	}
	f.nextFree = out[len(out)-1] + 1
	return out, nil
}

// writeClusters writes data across clusters, zero-padding the last one.
func (f *FS) writeClusters(cl []uint32, data []byte) error {
	for i := 0; i < len(cl); {
		j := i + 1
		for j < len(cl) && cl[j] == cl[j-1]+1 {
			j++
		}
		start := i * f.clusterSize
		end := min(j*f.clusterSize, len(data))
		buf := make([]byte, (j-i)*f.clusterSize)
		if start < len(data) {
			copy(buf, data[start:end])
		}
		if _, err := f.dev.WriteAt(buf, f.clusterOffset(cl[i])); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func (f *FS) readChain(start uint32, size int64) ([]byte, error) {
	if start == 0 {
		return []byte{}, nil
	}
	ch, err := f.chain(start)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		size = int64(len(ch)) * int64(f.clusterSize)
	}
	if size > int64(len(ch))*int64(f.clusterSize) {
		return nil, errors.New("fat: file size exceeds its cluster chain")
	}
	out := make([]byte, size)
	for i := 0; i < len(ch) && int64(i)*int64(f.clusterSize) < size; {
		j := i + 1
		for j < len(ch) && ch[j] == ch[j-1]+1 {
			j++
		}
		start := int64(i) * int64(f.clusterSize)
		end := min(int64(j)*int64(f.clusterSize), size)
		if _, err := f.dev.ReadAt(out[start:end], f.clusterOffset(ch[i])); err != nil {
			return nil, err
		}
		i = j
	}
	return out, nil
}

// Entry describes a file or directory.
type Entry struct {
	Name      string // long name if present, else 8.3 short name
	ShortName string
	Attr      byte
	Size      int64
	ModTime   time.Time
	cluster   uint32
	dir       uint32 // first cluster of the containing directory (0 = fixed root)
	slot      int    // index of the 8.3 entry within the directory
	lfnSlots  int    // number of LFN entries before it
}

func (e Entry) IsDir() bool { return e.Attr&AttrDir != 0 }

var errNotExist = fs.ErrNotExist

func dosDateTime(t time.Time) (uint16, uint16) {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	d := uint16(t.Year()-1980)<<9 | uint16(t.Month())<<5 | uint16(t.Day())
	tm := uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
	return d, tm
}

func fromDOS(d, t uint16) time.Time {
	if d == 0 {
		return time.Time{}
	}
	return time.Date(int(d>>9)+1980, time.Month(d>>5&15), int(d&31), int(t>>11), int(t>>5&63), int(t&31)*2, 0, time.Local)
}

func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}
