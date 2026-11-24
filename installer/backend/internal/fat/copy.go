package fat

import "fmt"

// CopyUsed copies the volume to dst at byte offset dstBase, writing only what
// a FAT volume needs: the reserved sectors (boot sector, FSInfo, backup), the
// FATs, the fixed root directory (FAT12/16) and every allocated cluster. Free
// clusters are skipped, so a 600 MB install on a 64 GB volume copies ~600 MB.
// The destination must be at least as large as the volume. Pending changes
// are not included: call Sync first.
func (f *FS) CopyUsed(dst Device, dstBase int64, progress func(done, total int64)) error {
	copyRange := func(off, n int64) error {
		buf := make([]byte, 1<<20)
		for n > 0 {
			k := min(n, int64(len(buf)))
			if _, err := f.dev.ReadAt(buf[:k], f.base+off); err != nil {
				return fmt.Errorf("fat: read at %d: %w", f.base+off, err)
			}
			if _, err := dst.WriteAt(buf[:k], dstBase+off); err != nil {
				return fmt.Errorf("fat: write at %d: %w", dstBase+off, err)
			}
			off += k
			n -= k
		}
		return nil
	}
	var used uint32
	for c := uint32(2); c < f.clusters+2; c++ {
		if f.fat[c] != 0 {
			used++
		}
	}
	total := f.dataStart + int64(used)*int64(f.clusterSize)
	if err := copyRange(0, f.dataStart); err != nil { // metadata
		return err
	}
	done := f.dataStart
	// Runs of allocated clusters, copied as contiguous ranges.
	for c := uint32(2); c < f.clusters+2; {
		if f.fat[c] == 0 {
			c++
			continue
		}
		start := c
		for c < f.clusters+2 && f.fat[c] != 0 {
			c++
		}
		off := f.dataStart + int64(start-2)*int64(f.clusterSize)
		n := int64(c-start) * int64(f.clusterSize)
		if err := copyRange(off, n); err != nil {
			return err
		}
		done += n
		if progress != nil {
			progress(done, total)
		}
	}
	return nil
}

// Size is the volume's size in bytes.
func (f *FS) Size() int64 { return f.totalSectors * int64(f.bps) }
