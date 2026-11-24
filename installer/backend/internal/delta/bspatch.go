package delta

import (
	"bytes"
	"compress/bzip2"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// BSPatch applies a classic BSDIFF40 patch (Colin Percival's bsdiff format),
// so existing community patches can be used as-is. bsdiff carries no checksums:
// callers must verify input and output hashes.
func BSPatch(old, patch []byte) ([]byte, error) {
	if len(patch) < 32 || !IsBSDiff(patch) {
		return nil, errors.New("not a BSDIFF40 patch")
	}
	ctrlLen, diffLen, newSize := offtin(patch[8:]), offtin(patch[16:]), offtin(patch[24:])
	if ctrlLen < 0 || diffLen < 0 || newSize < 0 || newSize > MaxSize ||
		ctrlLen > int64(len(patch)-32) || diffLen > int64(len(patch)-32)-ctrlLen {
		return nil, errors.New("bsdiff header corrupt")
	}
	body := patch[32:]
	ctrl := bzip2.NewReader(bytes.NewReader(body[:ctrlLen]))
	diff := bzip2.NewReader(bytes.NewReader(body[ctrlLen : ctrlLen+diffLen]))
	extra := bzip2.NewReader(bytes.NewReader(body[ctrlLen+diffLen:]))

	out := make([]byte, newSize)
	var oldPos, newPos int64
	var buf [24]byte
	for newPos < newSize {
		if _, err := io.ReadFull(ctrl, buf[:]); err != nil {
			return nil, fmt.Errorf("bsdiff control: %w", err)
		}
		x, y, z := offtin(buf[0:]), offtin(buf[8:]), offtin(buf[16:])
		if x < 0 || y < 0 || x > newSize-newPos {
			return nil, errors.New("bsdiff control out of range")
		}
		if _, err := io.ReadFull(diff, out[newPos:newPos+x]); err != nil {
			return nil, fmt.Errorf("bsdiff diff: %w", err)
		}
		for i := int64(0); i < x; i++ {
			if p := oldPos + i; p >= 0 && p < int64(len(old)) {
				out[newPos+i] += old[p]
			}
		}
		newPos += x
		oldPos += x
		if y > newSize-newPos {
			return nil, errors.New("bsdiff extra out of range")
		}
		if _, err := io.ReadFull(extra, out[newPos:newPos+y]); err != nil {
			return nil, fmt.Errorf("bsdiff extra: %w", err)
		}
		newPos += y
		oldPos += z
	}
	return out, nil
}

// offtin decodes bsdiff's sign-magnitude little-endian int64.
func offtin(b []byte) int64 {
	v := int64(binary.LittleEndian.Uint64(b) &^ (1 << 63))
	if b[7]&0x80 != 0 {
		v = -v
	}
	return v
}
