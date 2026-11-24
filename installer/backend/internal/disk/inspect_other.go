//go:build !linux

package disk

import "errors"

// On macOS and Windows (development) partitions are identified by signature
// only, and shrinking isn't available.
func inspectOS(d *Disk, p *Part) {}

func PartDevice(d *Disk, num int) string { return "" }

func ShrinkFS(dev, fs string, bytesMax int64) error {
	return errors.New("shrinking partitions needs the 98Reborn installer (Linux)")
}

func Reread(path string) {}
