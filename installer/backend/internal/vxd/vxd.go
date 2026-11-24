// Package vxd reads VMM32.VXD, the container Windows 9x keeps its core
// virtual device drivers in, and extracts single drivers as standalone LE
// files. Windows loads a standalone driver from SYSTEM\VMM32 in place of the
// copy inside VMM32.VXD, which is how fixes to VMM and friends are applied.
//
// Formats (as documented by patcher9x, MIT, JHRobotics, used as reference):
//   - W3: a table of (8-byte name, file offset, header size) followed by the
//     LE images, after an MZ stub.
//   - W4: the W3 file (after the MZ stub) split into chunks and each chunk
//     compressed with DoubleSpace-style LZ77 ("DS").
package vxd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

var le = binary.LittleEndian

// stub is the MZ program placed before an extracted LE image; its header
// points (offset 0x3C) right after itself.
var stub = []byte{
	0x4D, 0x5A, 0x80, 0x00, 0x1F, 0x03, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xFF, 0xFF, 0x00, 0x00,
	0xB8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00,
	0x0E, 0x1F, 0xBA, 0x0E, 0x00, 0xB4, 0x09, 0xCD, 0x21, 0xB8, 0x01, 0x4C, 0xCD, 0x21, 0x54, 0x68,
	0x69, 0x73, 0x20, 0x70, 0x72, 0x6F, 0x67, 0x72, 0x61, 0x6D, 0x20, 0x63, 0x61, 0x6E, 0x6E, 0x6F,
	0x74, 0x20, 0x62, 0x65, 0x20, 0x72, 0x75, 0x6E, 0x20, 0x69, 0x6E, 0x20, 0x44, 0x4F, 0x53, 0x20,
	0x6D, 0x6F, 0x64, 0x65, 0x2E, 0x0D, 0x0A, 0x24, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
}

const leHeaderSize = 176

// Entry is one driver in a W3 container.
type Entry struct {
	Name   string
	Offset uint32
}

// Container is a VMM32.VXD decompressed to its W3 form.
type Container struct {
	w3      []byte
	hdr     int // offset of the "W3" header
	Entries []Entry
}

// Open parses a VMM32.VXD (W3 or W4).
func Open(b []byte) (*Container, error) {
	if len(b) < 0x40 || string(b[:2]) != "MZ" {
		return nil, errors.New("vxd: not an MZ file")
	}
	next := int(le.Uint32(b[0x3C:]))
	if next+16 > len(b) {
		return nil, errors.New("vxd: bad header offset")
	}
	switch string(b[next : next+2]) {
	case "W3":
	case "W4":
		w3, err := decompressW4(b, next)
		if err != nil {
			return nil, err
		}
		b = w3
	default:
		return nil, fmt.Errorf("vxd: %q is not a W3/W4 container", b[next:next+2])
	}
	if string(b[next:next+2]) != "W3" {
		return nil, errors.New("vxd: decompressed data has no W3 header")
	}
	c := &Container{w3: b, hdr: next}
	n := int(le.Uint16(b[next+4:]))
	for i := 0; i < n; i++ {
		e := b[next+16+i*16:]
		c.Entries = append(c.Entries, Entry{Name: strings.TrimRight(string(e[:8]), " "), Offset: le.Uint32(e[8:])})
	}
	return c, nil
}

// Extract returns driver name (e.g. "VMM") as a standalone LE file.
func (c *Container) Extract(name string) ([]byte, error) {
	for i, e := range c.Entries {
		if !strings.EqualFold(e.Name, name) {
			continue
		}
		end := uint32(len(c.w3))
		if i+1 < len(c.Entries) {
			end = c.Entries[i+1].Offset
		}
		if e.Offset+leHeaderSize > end || end > uint32(len(c.w3)) {
			return nil, fmt.Errorf("vxd: %s: bad offsets", name)
		}
		img := c.w3[e.Offset:end]
		if string(img[:2]) != "LE" {
			return nil, fmt.Errorf("vxd: %s is not an LE image", name)
		}
		out := append(append([]byte{}, stub...), img...)
		h := out[len(stub):]
		// Offsets "from top of file" were relative to VMM32.VXD.
		for _, off := range []int{0x80, 0x88} {
			v := le.Uint32(h[off:])
			le.PutUint32(h[off:], v+uint32(len(stub))-e.Offset)
		}
		return out, nil
	}
	return nil, fmt.Errorf("vxd: %s not in the container", name)
}

func decompressW4(b []byte, next int) ([]byte, error) {
	chunkSize := int(le.Uint16(b[next+4:]))
	count := int(le.Uint16(b[next+6:]))
	if chunkSize == 0 || next+16+count*4 > len(b) {
		return nil, errors.New("vxd: bad W4 header")
	}
	offs := make([]int, count+1)
	for i := 0; i < count; i++ {
		offs[i] = int(le.Uint32(b[next+16+i*4:]))
	}
	offs[count] = len(b)
	var out bytes.Buffer
	out.Write(b[:next])
	buf := make([]byte, chunkSize)
	for i := 0; i < count; i++ {
		if offs[i] > offs[i+1] || offs[i+1] > len(b) {
			return nil, fmt.Errorf("vxd: W4 chunk %d out of range", i)
		}
		raw := b[offs[i]:offs[i+1]]
		if len(raw) == chunkSize {
			out.Write(raw)
			continue
		}
		n := dsDecompress(raw, buf)
		if n == 0 {
			return nil, fmt.Errorf("vxd: W4 chunk %d is corrupt", i)
		}
		out.Write(buf[:n])
	}
	return out.Bytes(), nil
}

// bitReader reads bits least-significant first.
type bitReader struct {
	b   []byte
	pos int // in bits
}

func (r *bitReader) read(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		byteI := r.pos >> 3
		if byteI < len(r.b) && r.b[byteI]>>(r.pos&7)&1 != 0 {
			v |= 1 << i
		}
		r.pos++
	}
	return v
}

// dsCount decodes a copy length.
func dsCount(buf *uint32, bufLen *int) int {
	b := *buf
	mask := uint32(0)
	zeroes := 0
	for ; zeroes < 9; zeroes++ {
		if b&1 != 0 {
			break
		}
		b >>= 1
		mask = mask<<1 | 1
	}
	if zeroes == 9 {
		return 0
	}
	b >>= 1
	n := (1 << zeroes) + int(b&mask) + 1
	b >>= zeroes
	*bufLen -= 1 + zeroes*2
	*buf = b
	return n
}

// dsDecompress inflates one chunk into out and returns the bytes produced.
func dsDecompress(in []byte, out []byte) int {
	r := &bitReader{b: in}
	var buf uint32
	bufLen := 0
	pos := 0
	for pos < len(out) {
		if bufLen < 32 {
			buf |= r.read(32-bufLen) << bufLen
			bufLen = 32
		}
		var copyPos, copySize int
		switch buf & 3 {
		case 1:
			out[pos] = 0x80 | byte(buf>>2&0x7F)
			pos++
			buf >>= 9
			bufLen -= 9
			continue
		case 2:
			out[pos] = byte(buf >> 2 & 0x7F)
			pos++
			buf >>= 9
			bufLen -= 9
			continue
		case 0:
			copyPos = int(buf >> 2 & 0x3F)
			buf >>= 8
			bufLen -= 8
			if copyPos != 0 {
				copySize = dsCount(&buf, &bufLen)
			}
		case 3:
			switch buf & 7 {
			case 3:
				copyPos = int(buf>>3&0xFF) + 64
				buf >>= 11
				bufLen -= 11
				copySize = dsCount(&buf, &bufLen)
			case 7:
				copyPos = int(buf>>3&0xFFF) + 320
				buf >>= 15
				bufLen -= 15
				if copyPos == 4415 { // sector break
					continue
				}
				copySize = dsCount(&buf, &bufLen)
			}
		}
		if copyPos == 0 {
			return pos // end of block
		}
		if copySize == 0 || copyPos > pos {
			return 0
		}
		if pos+copySize > len(out) {
			return pos
		}
		for ; copySize > 0; copySize-- {
			out[pos] = out[pos-copyPos]
			pos++
		}
	}
	return pos
}
