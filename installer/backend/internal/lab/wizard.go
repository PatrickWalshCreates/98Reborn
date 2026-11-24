package lab

import (
	"hash/fnv"
	"image"
)

// headingPrint fingerprints the heading area of a Windows 98 Setup Wizard page
// (640x480): a hash of which pixels are dark text. It identifies the page
// without OCR; the caret and mouse pointer are outside the area.
func headingPrint(img image.Image) uint64 {
	h := fnv.New64a()
	b := img.Bounds()
	if b.Dx() != 640 || b.Dy() != 480 {
		return 0
	}
	var bits byte
	n := 0
	for y := 92; y < 120; y++ {
		for x := 215; x < 545; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			bits <<= 1
			if (r>>8)+(g>>8)+(bl>>8) < 3*64 {
				bits |= 1
			}
			if n++; n%8 == 0 {
				h.Write([]byte{bits})
				bits = 0
			}
		}
	}
	return h.Sum64()
}
