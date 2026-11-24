// Package gui is the 98Reborn installer's small graphics toolkit: a canvas
// with Terminus bitmap fonts, an immediate-mode widget set, and outputs for
// the Linux framebuffer (with evdev keyboard and mouse input) and PNG files
// (previews on any OS). It has no dependencies beyond the standard library.
package gui

import (
	"embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

// Color is 0xRRGGBB.
type Color uint32

func (c Color) RGB() (r, g, b uint8) { return uint8(c >> 16), uint8(c >> 8), uint8(c) }

// Mix blends a toward b by t/255.
func Mix(a, b Color, t int) Color {
	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()
	m := func(x, y uint8) uint32 { return uint32((int(x)*(255-t) + int(y)*t) / 255) }
	return Color(m(ar, br)<<16 | m(ag, bg)<<8 | m(ab, bb))
}

// Rect is a rectangle (x, y, width, height).
type Rect struct{ X, Y, W, H int }

// R makes a Rect.
func R(x, y, w, h int) Rect { return Rect{x, y, w, h} }

func (r Rect) Contains(x, y int) bool { return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H }
func (r Rect) Inset(d int) Rect       { return Rect{r.X + d, r.Y + d, r.W - 2*d, r.H - 2*d} }

// Canvas is a 32-bit image the toolkit draws on.
type Canvas struct {
	W, H int
	Pix  []Color
	clip Rect
}

func NewCanvas(w, h int) *Canvas {
	return &Canvas{W: w, H: h, Pix: make([]Color, w*h), clip: Rect{0, 0, w, h}}
}

// Clip limits drawing to r (intersected with the canvas); Unclip resets it.
func (c *Canvas) Clip(r Rect) {
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, c.W), min(r.Y+r.H, c.H)
	c.clip = Rect{x0, y0, max(0, x1-x0), max(0, y1-y0)}
}
func (c *Canvas) Unclip() { c.clip = Rect{0, 0, c.W, c.H} }

func (c *Canvas) Fill(r Rect, col Color) {
	x0, y0 := max(r.X, c.clip.X), max(r.Y, c.clip.Y)
	x1, y1 := min(r.X+r.W, c.clip.X+c.clip.W), min(r.Y+r.H, c.clip.Y+c.clip.H)
	for y := y0; y < y1; y++ {
		row := c.Pix[y*c.W:]
		for x := x0; x < x1; x++ {
			row[x] = col
		}
	}
}

func (c *Canvas) Set(x, y int, col Color) {
	if c.clip.Contains(x, y) {
		c.Pix[y*c.W+x] = col
	}
}

// Frame draws a 1-pixel outline.
func (c *Canvas) Frame(r Rect, col Color) {
	c.Fill(Rect{r.X, r.Y, r.W, 1}, col)
	c.Fill(Rect{r.X, r.Y + r.H - 1, r.W, 1}, col)
	c.Fill(Rect{r.X, r.Y, 1, r.H}, col)
	c.Fill(Rect{r.X + r.W - 1, r.Y, 1, r.H}, col)
}

// Bevel draws a Windows 98-style raised (or sunken) edge around r.
func (c *Canvas) Bevel(r Rect, hi, lo Color, sunken bool) {
	if sunken {
		hi, lo = lo, hi
	}
	c.Fill(Rect{r.X, r.Y, r.W, 1}, hi)
	c.Fill(Rect{r.X, r.Y, 1, r.H}, hi)
	c.Fill(Rect{r.X, r.Y + r.H - 1, r.W, 1}, lo)
	c.Fill(Rect{r.X + r.W - 1, r.Y, 1, r.H}, lo)
}

// VGradient fills r from top color a to bottom color b.
func (c *Canvas) VGradient(r Rect, a, b Color) {
	for i := 0; i < r.H; i++ {
		c.Fill(Rect{r.X, r.Y + i, r.W, 1}, Mix(a, b, i*255/max(1, r.H-1)))
	}
}

// Disc fills a circle.
func (c *Canvas) Disc(cx, cy, rad int, col Color) {
	for y := -rad; y <= rad; y++ {
		for x := -rad; x <= rad; x++ {
			if x*x+y*y <= rad*rad {
				c.Set(cx+x, cy+y, col)
			}
		}
	}
}

// Ring draws the upper half of a ring (the logo's arc) or a full ring.
func (c *Canvas) Ring(cx, cy, ro, ri int, col Color, upperOnly bool) {
	for y := -ro; y <= ro; y++ {
		if upperOnly && y > 0 {
			break
		}
		for x := -ro; x <= ro; x++ {
			d := x*x + y*y
			if d <= ro*ro && d >= ri*ri {
				c.Set(cx+x, cy+y, col)
			}
		}
	}
}

// Blit draws img scaled (nearest neighbour) into r.
func (c *Canvas) Blit(img image.Image, r Rect) {
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return
	}
	for y := 0; y < r.H; y++ {
		sy := b.Min.Y + y*b.Dy()/r.H
		for x := 0; x < r.W; x++ {
			sx := b.Min.X + x*b.Dx()/r.W
			cr, cg, cb, _ := img.At(sx, sy).RGBA()
			c.Set(r.X+x, r.Y+y, Color((cr>>8)<<16|(cg>>8)<<8|cb>>8))
		}
	}
}

// Image returns the canvas as an image (for PNG output).
func (c *Canvas) Image() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	for i, p := range c.Pix {
		r, g, b := p.RGB()
		img.Set(i%c.W, i/c.W, color.RGBA{r, g, b, 255})
	}
	return img
}

// ---------- fonts ----------

//go:embed fonts/*.psf
var fontFS embed.FS

// Font is a bitmap font (PSF2, 256 glyphs, Latin-1).
type Font struct {
	W, H, stride int
	bits         []byte
}

// LoadFont loads an embedded Terminus face, e.g. "ter-120n".
func LoadFont(name string) (*Font, error) {
	b, err := fontFS.ReadFile("fonts/" + name + ".psf")
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if len(b) < 32 || le.Uint32(b) != 0x864ab572 {
		return nil, fmt.Errorf("%s: not a PSF2 font", name)
	}
	hdr := int(le.Uint32(b[8:]))
	f := &Font{H: int(le.Uint32(b[24:])), W: int(le.Uint32(b[28:]))}
	f.stride = (f.W + 7) / 8
	f.bits = b[hdr:]
	return f, nil
}

func MustFont(name string) *Font {
	f, err := LoadFont(name)
	if err != nil {
		panic(err)
	}
	return f
}

func glyph(r rune) int {
	switch r {
	case '✓':
		return -1 // drawn by hand
	case '…':
		return '.'
	case '—', '–':
		return '-'
	case '’', '‘':
		return '\''
	case '“', '”':
		return '"'
	case '•', '·':
		return 0xB7
	}
	if r < 256 {
		return int(r)
	}
	return '?'
}

// Width is the pixel width of s.
func (f *Font) Width(s string) int {
	n := 0
	for range s {
		n++
	}
	return n * f.W
}

// Text draws s at (x, y) (top-left) and returns the x after it.
func (c *Canvas) Text(f *Font, x, y int, s string, col Color) int {
	for _, r := range s {
		g := glyph(r)
		if g < 0 {
			c.Check(x+f.W/6, y+f.H/4, f.W*2/3, col)
			x += f.W
			continue
		}
		base := g * f.stride * f.H
		for row := 0; row < f.H; row++ {
			for col2 := 0; col2 < f.W; col2++ {
				if base+row*f.stride+col2/8 < len(f.bits) && f.bits[base+row*f.stride+col2/8]&(0x80>>(col2%8)) != 0 {
					c.Set(x+col2, y+row, col)
				}
			}
		}
		x += f.W
	}
	return x
}

// Wrap splits s into lines no wider than w pixels.
func (f *Font) Wrap(s string, w int) []string {
	var out []string
	for _, para := range splitLines(s) {
		line := ""
		for _, word := range splitWords(para) {
			try := word
			if line != "" {
				try = line + " " + word
			}
			if f.Width(try) > w && line != "" {
				out = append(out, line)
				line = word
			} else {
				line = try
			}
		}
		out = append(out, line)
	}
	return out
}

// TextBox draws wrapped text and returns the height used.
func (c *Canvas) TextBox(f *Font, r Rect, s string, col Color, lineGap int) int {
	y := r.Y
	for _, l := range f.Wrap(s, r.W) {
		c.Text(f, r.X, y, l, col)
		y += f.H + lineGap
	}
	return y - r.Y
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// Check draws a check mark of the given size.
func (c *Canvas) Check(x, y, size int, col Color) {
	t := max(2, size/6)
	for i := 0; i < size/3; i++ {
		c.Fill(Rect{x + i, y + size/2 + i - t/2, t, t}, col)
	}
	for i := 0; i < size*2/3; i++ {
		c.Fill(Rect{x + size/3 + i, y + size/2 + size/3 - i - t/2, t, t}, col)
	}
}

// Logo draws the 98Reborn mark (a CRT with a rebirth arc) in a size×size box.
func (c *Canvas) Logo(x, y, size int, frame, screen, glow Color) {
	s := func(v int) int { return v * size / 64 }
	c.Fill(Rect{x + s(6), y + s(8), s(52), s(38)}, frame)
	c.Fill(Rect{x + s(11), y + s(13), s(42), s(28)}, screen)
	c.Fill(Rect{x + s(24), y + s(46), s(16), s(5)}, frame)
	c.Fill(Rect{x + s(16), y + s(51), s(32), s(5)}, frame)
	c.Ring(x+s(32), y+s(33), s(13), s(9), glow, true)
	for k := 0; k < s(6); k++ {
		c.Fill(Rect{x + s(38) + k/2, y + s(27) + k, s(10) - k, 1}, glow)
	}
}
