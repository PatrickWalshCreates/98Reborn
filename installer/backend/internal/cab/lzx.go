package cab

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// LZX decompressor for Microsoft cabinets (window 2^15..2^21), written from the
// published format description (MS-PATCH LZX DELTA spec / LZX in CAB). It
// handles verbatim, aligned-offset and uncompressed blocks, repeated-offset
// state, 16-bit frame realignment, and Intel E8 call translation.

const (
	lzxMinMatch      = 2
	lzxNumChars      = 256
	lzxPretreeSyms   = 20
	lzxAlignedSyms   = 8
	lzxSecondaryLens = 249
	lzxFrameSize     = 32768

	lzxVerbatim     = 1
	lzxAligned      = 2
	lzxUncompressed = 3
)

var (
	lzxExtraBits    [52]uint
	lzxPositionBase [52]uint32
)

func init() {
	for i := range lzxExtraBits {
		e := 0
		if i >= 4 {
			e = i/2 - 1
		}
		if e > 17 {
			e = 17
		}
		lzxExtraBits[i] = uint(e)
	}
	for i := 1; i < len(lzxPositionBase); i++ {
		lzxPositionBase[i] = lzxPositionBase[i-1] + 1<<lzxExtraBits[i-1]
	}
}

func lzxPositionSlots(windowBits uint) (int, error) {
	switch {
	case windowBits >= 15 && windowBits <= 20:
		return int(windowBits) * 2, nil
	case windowBits == 21:
		return 50, nil
	}
	return 0, fmt.Errorf("unsupported LZX window size 2^%d", windowBits)
}

// bitReader reads 16-bit little-endian words, consuming bits MSB first.
type bitReader struct {
	in   []byte
	pos  int
	buf  uint64
	n    uint
	over int
}

func (b *bitReader) ensure(k uint) {
	for b.n < k {
		var w uint64
		switch {
		case b.pos+1 < len(b.in):
			w = uint64(b.in[b.pos]) | uint64(b.in[b.pos+1])<<8
		case b.pos < len(b.in):
			w = uint64(b.in[b.pos])
			b.over++
		default:
			b.over++
		}
		b.pos += 2
		b.buf = b.buf<<16 | w
		b.n += 16
	}
}

func (b *bitReader) peek(k uint) uint32 { return uint32(b.buf>>(b.n-k)) & (1<<k - 1) }

func (b *bitReader) remove(k uint) {
	b.n -= k
	b.buf &= 1<<b.n - 1
}

func (b *bitReader) read(k uint) uint32 {
	if k == 0 {
		return 0
	}
	b.ensure(k)
	v := b.peek(k)
	b.remove(k)
	return v
}

// alignFrame discards bits up to the next 16-bit boundary (end of each frame).
func (b *bitReader) alignFrame() { b.remove(b.n % 16) }

// alignUncompressed moves to byte mode for an uncompressed block: skip to the
// next 16-bit boundary, consuming a whole padding word if already aligned.
func (b *bitReader) alignUncompressed() {
	consumed := b.pos*8 - int(b.n)
	b.pos = (consumed/16 + 1) * 2
	b.n, b.buf = 0, 0
}

// huffman is a canonical Huffman decoder with a fast lookup table.
type huffman struct {
	tableBits uint
	table     []uint16 // sym<<5 | len, 0 = slow path
	count     [17]int
	sorted    []uint16
	empty     bool
}

func (h *huffman) build(lens []byte, tableBits uint) error {
	h.tableBits = tableBits
	if len(h.table) != 1<<tableBits {
		h.table = make([]uint16, 1<<tableBits)
	} else {
		clear(h.table)
	}
	h.count = [17]int{}
	for _, l := range lens {
		if l > 16 {
			return errors.New("huffman code length > 16")
		}
		h.count[l]++
	}
	h.count[0] = 0
	h.empty = true
	for l := 1; l <= 16; l++ {
		if h.count[l] > 0 {
			h.empty = false
		}
	}
	h.sorted = h.sorted[:0]
	for l := 1; l <= 16; l++ {
		for s, sl := range lens {
			if int(sl) == l {
				h.sorted = append(h.sorted, uint16(s))
			}
		}
	}
	code := 0
	idx := 0
	for l := uint(1); l <= 16; l++ {
		for i := 0; i < h.count[l]; i++ {
			if code >= 1<<l {
				return errors.New("huffman table oversubscribed")
			}
			if l <= tableBits {
				sym := h.sorted[idx]
				start := code << (tableBits - l)
				end := (code + 1) << (tableBits - l)
				for j := start; j < end; j++ {
					h.table[j] = sym<<5 | uint16(l)
				}
			}
			code++
			idx++
		}
		code <<= 1
	}
	return nil
}

func (h *huffman) decode(b *bitReader) (int, error) {
	if h.empty {
		return 0, errors.New("decoding from an empty huffman tree")
	}
	b.ensure(16)
	v := b.peek(16)
	if e := h.table[v>>(16-h.tableBits)]; e != 0 {
		b.remove(uint(e & 31))
		return int(e >> 5), nil
	}
	code, first, index := 0, 0, 0
	for l := uint(1); l <= 16; l++ {
		code |= int(v>>(16-l)) & 1
		c := h.count[l]
		if code-first < c {
			b.remove(l)
			return int(h.sorted[index+code-first]), nil
		}
		index += c
		first = (first + c) << 1
		code <<= 1
	}
	return 0, errors.New("invalid huffman code")
}

type lzxDecoder struct {
	window     []byte
	wpos       int
	posnSlots  int
	r0, r1, r2 uint32

	mainLen    []byte
	lengthLen  [lzxSecondaryLens]byte
	alignedLen [lzxAlignedSyms]byte
	main       huffman
	length     huffman
	aligned    huffman
	pretree    huffman

	headerRead     bool
	intelFilesize  int32
	intelStarted   bool
	blockType      int
	blockLength    int
	blockRemaining int

	br       bitReader
	framePos int64
	frame    int
}

func newLZX(windowBits uint, in []byte) (*lzxDecoder, error) {
	slots, err := lzxPositionSlots(windowBits)
	if err != nil {
		return nil, err
	}
	return &lzxDecoder{
		window:    make([]byte, 1<<windowBits),
		posnSlots: slots,
		r0:        1, r1: 1, r2: 1,
		mainLen: make([]byte, lzxNumChars+slots*8),
		br:      bitReader{in: in},
	}, nil
}

func (d *lzxDecoder) readLengths(lens []byte, first, last int) error {
	var pre [lzxPretreeSyms]byte
	for i := range pre {
		pre[i] = byte(d.br.read(4))
	}
	if err := d.pretree.build(pre[:], 6); err != nil {
		return err
	}
	for x := first; x < last; {
		z, err := d.pretree.decode(&d.br)
		if err != nil {
			return err
		}
		switch z {
		case 17, 18:
			var y int
			if z == 17 {
				y = int(d.br.read(4)) + 4
			} else {
				y = int(d.br.read(5)) + 20
			}
			if x+y > last {
				return errors.New("lzx: code lengths overrun")
			}
			for ; y > 0; y-- {
				lens[x] = 0
				x++
			}
		case 19:
			y := int(d.br.read(1)) + 4
			z2, err := d.pretree.decode(&d.br)
			if err != nil {
				return err
			}
			if z2 > 16 || x+y > last {
				return errors.New("lzx: bad code length run")
			}
			v := byte((int(lens[x]) - z2 + 17) % 17)
			for ; y > 0; y-- {
				lens[x] = v
				x++
			}
		default:
			lens[x] = byte((int(lens[x]) - z + 17) % 17)
			x++
		}
	}
	return nil
}

func (d *lzxDecoder) readBlockHeader() error {
	if d.blockType == lzxUncompressed && d.blockLength&1 == 1 {
		d.br.pos++ // uncompressed blocks are padded to an even length
	}
	d.blockType = int(d.br.read(3))
	hi := d.br.read(16)
	lo := d.br.read(8)
	d.blockLength = int(hi<<8 | lo)
	d.blockRemaining = d.blockLength
	if d.blockLength == 0 {
		return errors.New("lzx: empty block")
	}
	switch d.blockType {
	case lzxAligned:
		for i := range d.alignedLen {
			d.alignedLen[i] = byte(d.br.read(3))
		}
		if err := d.aligned.build(d.alignedLen[:], 7); err != nil {
			return err
		}
		fallthrough
	case lzxVerbatim:
		if err := d.readLengths(d.mainLen, 0, lzxNumChars); err != nil {
			return err
		}
		if err := d.readLengths(d.mainLen, lzxNumChars, len(d.mainLen)); err != nil {
			return err
		}
		if err := d.main.build(d.mainLen, 12); err != nil {
			return err
		}
		if d.mainLen[0xE8] != 0 {
			d.intelStarted = true
		}
		if err := d.readLengths(d.lengthLen[:], 0, lzxSecondaryLens); err != nil {
			return err
		}
		return d.length.build(d.lengthLen[:], 12)
	case lzxUncompressed:
		d.intelStarted = true
		d.br.alignUncompressed()
		if d.br.pos+12 > len(d.br.in) {
			return errors.New("lzx: truncated uncompressed block header")
		}
		p := d.br.in[d.br.pos:]
		d.r0 = binary.LittleEndian.Uint32(p[0:])
		d.r1 = binary.LittleEndian.Uint32(p[4:])
		d.r2 = binary.LittleEndian.Uint32(p[8:])
		d.br.pos += 12
		return nil
	}
	return fmt.Errorf("lzx: invalid block type %d", d.blockType)
}

// decodeMatches decodes symbols until at least run bytes are produced.
func (d *lzxDecoder) decodeMatches(run int, produced int64) (int, error) {
	n := 0
	mask := len(d.window) - 1
	for n < run {
		sym, err := d.main.decode(&d.br)
		if err != nil {
			return n, err
		}
		if sym < lzxNumChars {
			d.window[d.wpos] = byte(sym)
			d.wpos++
			n++
			continue
		}
		sym -= lzxNumChars
		matchLen := sym & 7
		if matchLen == 7 {
			extra, err := d.length.decode(&d.br)
			if err != nil {
				return n, err
			}
			matchLen += extra
		}
		matchLen += lzxMinMatch
		slot := sym >> 3
		var off uint32
		switch {
		case slot > 2:
			extra := lzxExtraBits[slot]
			off = lzxPositionBase[slot] - 2
			if d.blockType == lzxAligned {
				switch {
				case extra > 3:
					off += d.br.read(extra-3) << 3
					a, err := d.aligned.decode(&d.br)
					if err != nil {
						return n, err
					}
					off += uint32(a)
				case extra == 3:
					a, err := d.aligned.decode(&d.br)
					if err != nil {
						return n, err
					}
					off += uint32(a)
				default:
					off += d.br.read(extra)
				}
			} else {
				off += d.br.read(extra)
			}
			d.r2, d.r1, d.r0 = d.r1, d.r0, off
		case slot == 0:
			off = d.r0
		case slot == 1:
			off = d.r1
			d.r1, d.r0 = d.r0, off
		default:
			off = d.r2
			d.r2, d.r0 = d.r0, off
		}
		if d.wpos+matchLen > len(d.window) {
			return n, errors.New("lzx: match runs past window end")
		}
		if off == 0 || int64(off) > produced+int64(n) || int(off) > len(d.window) {
			return n, fmt.Errorf("lzx: match offset %d out of range", off)
		}
		src := d.wpos - int(off)
		for i := 0; i < matchLen; i++ {
			d.window[d.wpos] = d.window[(src+i)&mask]
			d.wpos++
		}
		n += matchLen
	}
	return n, nil
}

// decodeFrame produces the next len(out) bytes (one CFDATA block, <= 32 KB).
func (d *lzxDecoder) decodeFrame(out []byte) error {
	size := len(out)
	if size == 0 || size > lzxFrameSize {
		return fmt.Errorf("lzx: bad frame size %d", size)
	}
	if !d.headerRead {
		if d.br.read(1) == 1 {
			hi := d.br.read(16)
			lo := d.br.read(16)
			d.intelFilesize = int32(hi<<16 | lo)
		}
		d.headerRead = true
	}
	if d.wpos == len(d.window) {
		d.wpos = 0
	}
	start := d.wpos
	if start+size > len(d.window) {
		return errors.New("lzx: frame straddles window end")
	}
	for todo := size; todo > 0; {
		if d.blockRemaining == 0 {
			if err := d.readBlockHeader(); err != nil {
				return err
			}
		}
		run := min(d.blockRemaining, todo)
		var n int
		if d.blockType == lzxUncompressed {
			if d.br.pos+run > len(d.br.in) {
				return errors.New("lzx: truncated uncompressed block")
			}
			copy(d.window[d.wpos:], d.br.in[d.br.pos:d.br.pos+run])
			d.br.pos += run
			d.wpos += run
			n = run
		} else {
			var err error
			produced := d.framePos + int64(size-todo)
			if n, err = d.decodeMatches(run, produced); err != nil {
				return err
			}
			if n > run {
				return errors.New("lzx: match overruns block or frame")
			}
		}
		d.blockRemaining -= n
		todo -= n
	}
	if d.br.over > 4 {
		return errors.New("lzx: read past end of compressed data")
	}
	d.br.alignFrame()
	copy(out, d.window[start:start+size])

	if d.intelStarted && d.intelFilesize != 0 && d.frame < 32768 && size > 10 {
		e8Translate(out, int32(d.framePos), d.intelFilesize)
	}
	d.framePos += int64(size)
	d.frame++
	return nil
}

// e8Translate undoes the encoder's conversion of x86 CALL targets to absolute addresses.
func e8Translate(data []byte, curpos, filesize int32) {
	end := len(data) - 10
	for i := 0; i < end; {
		if data[i] != 0xE8 {
			i++
			curpos++
			continue
		}
		abs := int32(binary.LittleEndian.Uint32(data[i+1:]))
		if abs >= -curpos && abs < filesize {
			var rel int32
			if abs >= 0 {
				rel = abs - curpos
			} else {
				rel = abs + filesize
			}
			binary.LittleEndian.PutUint32(data[i+1:], uint32(rel))
		}
		i += 5
		curpos += 5
	}
}
