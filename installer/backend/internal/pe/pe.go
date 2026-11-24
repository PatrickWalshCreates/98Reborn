// Package pe inspects Windows PE executables (DLL, EXE, SYS, some VxD tools).
package pe

import "encoding/binary"

// Checksum returns the checksum stored in the PE optional header and the one
// computed over the file (IMAGEHLP CheckSumMappedFile algorithm). ok is false
// for non-PE files or files with no stored checksum.
func Checksum(b []byte) (stored, computed uint32, ok bool) {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return 0, 0, false
	}
	off := int(binary.LittleEndian.Uint32(b[0x3c:]))
	if off <= 0 || off+24+68 > len(b) || string(b[off:off+4]) != "PE\x00\x00" {
		return 0, 0, false
	}
	cs := off + 24 + 64
	stored = binary.LittleEndian.Uint32(b[cs:])
	if stored == 0 {
		return 0, 0, false
	}
	var sum uint64
	for i := 0; i+1 < len(b); i += 2 {
		if i == cs || i == cs+2 {
			continue
		}
		sum += uint64(binary.LittleEndian.Uint16(b[i:]))
		sum = sum&0xffff + sum>>16
	}
	if len(b)%2 == 1 {
		sum += uint64(b[len(b)-1])
		sum = sum&0xffff + sum>>16
	}
	sum = sum&0xffff + sum>>16
	return stored, uint32(sum) + uint32(len(b)), true
}
