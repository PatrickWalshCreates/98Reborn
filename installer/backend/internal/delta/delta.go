// Package delta implements the W9XD binary delta format (create + apply) and
// applying classic BSDIFF40 patches, so modules can ship changes to Microsoft
// files without shipping the files themselves.
//
// W9XD layout:
//
//	"W9XD" 0x01 | sha256(src) [32] | sha256(dst) [32] | uvarint len(src) | uvarint len(dst) | deflate(ops)
//
// ops: 0x00 uvarint(srcOffset) uvarint(length)  copy from source
//
//	0x01 uvarint(length) bytes                 insert literal bytes
package delta

import (
	"bufio"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic    = "W9XD\x01"
	opCopy   = 0x00
	opInsert = 0x01
	minMatch = 8
	maxCand  = 32
	// MaxSize bounds outputs; Win98 system files are far smaller.
	MaxSize = 256 << 20
)

var ErrSourceMismatch = errors.New("source file does not match the delta's expected original")

type Header struct {
	SrcSHA, DstSHA   [32]byte
	SrcSize, DstSize uint64
	bodyOffset       int
}

func IsW9XD(b []byte) bool   { return bytes.HasPrefix(b, []byte(magic)) }
func IsBSDiff(b []byte) bool { return bytes.HasPrefix(b, []byte("BSDIFF40")) }

func ReadHeader(p []byte) (*Header, error) {
	if !IsW9XD(p) {
		return nil, errors.New("not a W9XD delta")
	}
	h := &Header{}
	off := len(magic)
	if len(p) < off+64 {
		return nil, errors.New("delta truncated")
	}
	copy(h.SrcSHA[:], p[off:off+32])
	copy(h.DstSHA[:], p[off+32:off+64])
	off += 64
	var n int
	h.SrcSize, n = binary.Uvarint(p[off:])
	if n <= 0 {
		return nil, errors.New("delta header corrupt")
	}
	off += n
	h.DstSize, n = binary.Uvarint(p[off:])
	if n <= 0 || h.DstSize > MaxSize {
		return nil, errors.New("delta header corrupt")
	}
	h.bodyOffset = off + n
	return h, nil
}

// Make creates a W9XD delta transforming src into dst.
func Make(src, dst []byte) ([]byte, error) {
	if len(dst) > MaxSize || len(src) > MaxSize {
		return nil, errors.New("file too large")
	}
	idx := map[uint64][]int32{}
	for i := 0; i+minMatch <= len(src); i++ {
		k := binary.LittleEndian.Uint64(src[i:])
		if c := idx[k]; len(c) < maxCand {
			idx[k] = append(c, int32(i))
		}
	}
	var ops bytes.Buffer
	var tmp [binary.MaxVarintLen64]byte
	uv := func(v int) { ops.Write(tmp[:binary.PutUvarint(tmp[:], uint64(v))]) }
	insert := func(b []byte) {
		if len(b) > 0 {
			ops.WriteByte(opInsert)
			uv(len(b))
			ops.Write(b)
		}
	}
	lit, i := 0, 0
	for i+minMatch <= len(dst) {
		best, bestOff := 0, 0
		for _, c := range idx[binary.LittleEndian.Uint64(dst[i:])] {
			if l := matchLen(src[c:], dst[i:]); l > best {
				best, bestOff = l, int(c)
			}
		}
		if best >= minMatch {
			insert(dst[lit:i])
			ops.WriteByte(opCopy)
			uv(bestOff)
			uv(best)
			i += best
			lit = i
		} else {
			i++
		}
	}
	insert(dst[lit:])

	var out bytes.Buffer
	out.WriteString(magic)
	s, d := sha256.Sum256(src), sha256.Sum256(dst)
	out.Write(s[:])
	out.Write(d[:])
	out.Write(tmp[:binary.PutUvarint(tmp[:], uint64(len(src)))])
	out.Write(tmp[:binary.PutUvarint(tmp[:], uint64(len(dst)))])
	fw, err := flate.NewWriter(&out, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(ops.Bytes()); err != nil {
		return nil, err
	}
	if err := fw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func matchLen(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// Apply applies a W9XD delta. Returns ErrSourceMismatch if src is not the expected original.
func Apply(src, patch []byte) ([]byte, error) {
	h, err := ReadHeader(patch)
	if err != nil {
		return nil, err
	}
	if uint64(len(src)) != h.SrcSize || sha256.Sum256(src) != h.SrcSHA {
		return nil, ErrSourceMismatch
	}
	r := bufio.NewReader(flate.NewReader(bytes.NewReader(patch[h.bodyOffset:])))
	out := make([]byte, 0, h.DstSize)
	for {
		op, err := r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("delta body: %w", err)
		}
		switch op {
		case opCopy:
			off, err1 := binary.ReadUvarint(r)
			n, err2 := binary.ReadUvarint(r)
			if err1 != nil || err2 != nil {
				return nil, errors.New("delta body corrupt")
			}
			if off > uint64(len(src)) || n > uint64(len(src))-off || uint64(len(out))+n > h.DstSize {
				return nil, errors.New("delta copy out of range")
			}
			out = append(out, src[off:off+n]...)
		case opInsert:
			n, err := binary.ReadUvarint(r)
			if err != nil || uint64(len(out))+n > h.DstSize {
				return nil, errors.New("delta insert out of range")
			}
			start := len(out)
			out = append(out, make([]byte, n)...)
			if _, err := io.ReadFull(r, out[start:]); err != nil {
				return nil, errors.New("delta body truncated")
			}
		default:
			return nil, fmt.Errorf("delta: unknown op 0x%02x", op)
		}
	}
	if uint64(len(out)) != h.DstSize || sha256.Sum256(out) != h.DstSHA {
		return nil, errors.New("delta produced wrong output (corrupt delta)")
	}
	return out, nil
}
