package gui

import (
	"image/png"
	"os"
)

// SavePNG writes the canvas to a PNG file (previews and tests).
func (c *Canvas) SavePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, c.Image())
}
