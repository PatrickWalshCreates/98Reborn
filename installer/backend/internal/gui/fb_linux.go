//go:build linux

package gui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Screen is the Linux framebuffer (/dev/fb0), set up by the firmware (UEFI
// GOP or a VESA mode) and exposed by simpledrm/efifb.
type Screen struct {
	f                *os.File
	mem              []byte
	W, H             int
	stride           int
	bpp              int
	rOff, gOff, bOff uint32
	tty              *os.File
	Info             string // e.g. "1024x768 32bpp simpledrmdrmfb"
	Err              error  // the first write error, for diagnostics
	kms              *kms   // set when drawing through KMS
}

type fbVarInfo struct {
	Xres, Yres, XresVirtual, YresVirtual, Xoffset, Yoffset, BitsPerPixel, Grayscale uint32
	Red, Green, Blue, Transp                                                        [3]uint32
	Nonstd, Activate, Height, Width, AccelFlags                                     uint32
	Pixclock, LeftMargin, RightMargin, UpperMargin, LowerMargin, HsyncLen, VsyncLen uint32
	Sync, Vmode, Rotate, Colorspace                                                 uint32
	Reserved                                                                        [4]uint32
}

type fbFixInfo struct {
	ID                             [16]byte
	SmemStart                      uint64
	SmemLen, Type, TypeAux, Visual uint32
	Xpanstep, Ypanstep, Ywrapstep  uint16
	_                              uint16
	LineLength                     uint32
	MmioStart                      uint64
	MmioLen, Accel                 uint32
	Capabilities                   uint16
	Reserved                       [2]uint16
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// OpenScreen opens /dev/fb0 and switches tty1 to graphics mode so the
// kernel console doesn't draw over the installer.
func OpenScreen() (*Screen, error) {
	s, kerr := openKMS()
	if kerr == nil {
		s.hideConsole()
		return s, nil
	}
	s, err := openFB()
	if err != nil {
		return nil, fmt.Errorf("no display: KMS: %v; fbdev: %v", kerr, err)
	}
	return s, nil
}

func (s *Screen) hideConsole() {
	if tty, err := os.OpenFile("/dev/tty1", os.O_RDWR, 0); err == nil {
		s.tty = tty
		tty.WriteString("\x1b[?25l\x1b[9;0]\x1b[2J")
	}
}

func openFB() (*Screen, error) {
	f, err := os.OpenFile("/dev/fb0", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var v fbVarInfo
	var fx fbFixInfo
	if err := ioctl(f.Fd(), 0x4600, unsafe.Pointer(&v)); err != nil { // FBIOGET_VSCREENINFO
		f.Close()
		return nil, err
	}
	if err := ioctl(f.Fd(), 0x4602, unsafe.Pointer(&fx)); err != nil { // FBIOGET_FSCREENINFO
		f.Close()
		return nil, err
	}
	if v.BitsPerPixel != 32 && v.BitsPerPixel != 24 && v.BitsPerPixel != 16 {
		f.Close()
		return nil, errors.New("unsupported framebuffer depth")
	}
	// Frames are written with pwrite rather than through mmap: DRM's
	// emulated framebuffers (simpledrm) only pick up mmap writes lazily,
	// while a write updates the screen on every driver.
	s := &Screen{f: f, mem: make([]byte, int(fx.LineLength)*int(v.Yres)), W: int(v.Xres), H: int(v.Yres), stride: int(fx.LineLength), bpp: int(v.BitsPerPixel),
		rOff: v.Red[0], gOff: v.Green[0], bOff: v.Blue[0]}
	s.Info = fmt.Sprintf("%dx%d %dbpp %s", s.W, s.H, s.bpp, strings.TrimRight(string(fx.ID[:]), "\x00"))
	s.hideConsole()
	return s, nil
}

// Close returns tty1 to text mode.
func (s *Screen) Close() {
	if s.kms != nil {
		s.kms.close()
	}
	if s.tty != nil {
		s.tty.WriteString("\x1b[?25h\x1b[2J\x1b[H")
		s.tty.Close()
	}
	if s.f != nil {
		s.f.Close()
	}
}

// Show copies the canvas to the screen.
func (s *Screen) Show(c *Canvas) {
	w, h := min(c.W, s.W), min(c.H, s.H)
	if s.kms != nil {
		if err := s.kms.show(c, w, h, s.stride); err != nil && s.Err == nil {
			s.Err = err
		}
		return
	}
	bytes := s.bpp / 8
	for y := 0; y < h; y++ {
		row := s.mem[y*s.stride:]
		src := c.Pix[y*c.W:]
		for x := 0; x < w; x++ {
			p := src[x]
			r, g, b := uint32(p>>16&0xff), uint32(p>>8&0xff), uint32(p&0xff)
			o := x * bytes
			switch bytes {
			case 4:
				binary.LittleEndian.PutUint32(row[o:], r<<s.rOff|g<<s.gOff|b<<s.bOff)
			case 3:
				v := r<<s.rOff | g<<s.gOff | b<<s.bOff
				row[o], row[o+1], row[o+2] = byte(v), byte(v>>8), byte(v>>16)
			case 2: // RGB565
				binary.LittleEndian.PutUint16(row[o:], uint16(r>>3<<11|g>>2<<5|b>>3))
			}
		}
	}
	if _, err := s.f.WriteAt(s.mem[:h*s.stride], 0); err != nil && s.Err == nil {
		s.Err = err
	}
}

// ---------- input (evdev) ----------

const (
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03
)

// Input reads every keyboard and pointer under /dev/input and delivers
// events on C. Mouse positions are kept within the screen.
type Input struct {
	C     chan Event
	mu    sync.Mutex
	x, y  int
	w, h  int
	shift bool
}

func OpenInput(w, h int) *Input {
	in := &Input{C: make(chan Event, 64), x: w / 2, y: h / 2, w: w, h: h}
	go in.watch()
	return in
}

// watch opens input devices as they appear: drivers for touchpads and USB
// mice can come up after the screen does, and mice can be plugged in later.
func (in *Input) watch() {
	seen := map[string]bool{}
	for {
		devs, _ := filepath.Glob("/dev/input/event*")
		for _, d := range devs {
			in.mu.Lock()
			known := seen[d]
			in.mu.Unlock()
			if known {
				continue
			}
			f, err := os.Open(d)
			if err != nil {
				continue
			}
			in.mu.Lock()
			seen[d] = true
			in.mu.Unlock()
			go func(name string) {
				in.read(f)
				f.Close()
				in.mu.Lock()
				delete(seen, name) // gone (unplugged): reopen if it returns
				in.mu.Unlock()
			}(d)
		}
		time.Sleep(2 * time.Second)
	}
}

type absInfo struct{ Value, Min, Max, Fuzz, Flat, Res int32 }

// device is the state of one input device.
type device struct {
	ax, ay   absInfo
	abs      bool // reports absolute X/Y
	touchpad bool // absolute, but moves the pointer relatively (like a mouse)
	// touchpad tracking
	touching     bool
	lastX, lastY int32
	haveX, haveY bool
	downAt       time.Time
	moved        int
}

func hasKey(bits []byte, code int) bool { return code/8 < len(bits) && bits[code/8]&(1<<(code%8)) != 0 }

func (in *Input) read(f *os.File) {
	d := &device{}
	d.abs = ioctl(f.Fd(), 0x80184540, unsafe.Pointer(&d.ax)) == nil && d.ax.Max > d.ax.Min // EVIOCGABS(ABS_X)
	if d.abs {
		ioctl(f.Fd(), 0x80184541, unsafe.Pointer(&d.ay)) // EVIOCGABS(ABS_Y)
		keys := make([]byte, 96)
		if ioctl(f.Fd(), 0x80604521, unsafe.Pointer(&keys[0])) == nil { // EVIOCGBIT(EV_KEY, 96)
			d.touchpad = hasKey(keys, 0x145) // BTN_TOOL_FINGER: a touchpad, not a tablet or screen
		}
	}
	buf := make([]byte, 24*64)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i+24 <= n; i += 24 {
			typ := binary.LittleEndian.Uint16(buf[i+16:])
			code := binary.LittleEndian.Uint16(buf[i+18:])
			val := int32(binary.LittleEndian.Uint32(buf[i+20:]))
			in.handle(d, typ, code, val)
		}
	}
}

func (in *Input) send(e Event) {
	select {
	case in.C <- e:
	default:
	}
}

func (in *Input) move(dx, dy int) {
	in.x = max(0, min(in.w-1, in.x+dx))
	in.y = max(0, min(in.h-1, in.y+dy))
	in.send(Event{Mouse: true, X: in.x, Y: in.y})
}

func (in *Input) handle(d *device, typ, code uint16, val int32) {
	in.mu.Lock()
	defer in.mu.Unlock()
	switch typ {
	case evRel:
		switch code {
		case 0:
			in.move(int(val), 0)
		case 1:
			in.move(0, int(val))
		}
	case evAbs:
		if !d.abs || code > 1 {
			return
		}
		if d.touchpad {
			// Relative movement while a finger is down; the whole pad
			// width moves the pointer about 1.5 screen widths.
			span := int64(max(1, d.ax.Max-d.ax.Min))
			if code == 0 {
				if d.haveX && d.touching {
					dx := int(int64(val-d.lastX) * int64(in.w) * 3 / 2 / span)
					d.moved += abs(dx)
					in.move(dx, 0)
				}
				d.lastX, d.haveX = val, true
			} else {
				if d.haveY && d.touching {
					dy := int(int64(val-d.lastY) * int64(in.w) * 3 / 2 / span)
					d.moved += abs(dy)
					in.move(0, dy)
				}
				d.lastY, d.haveY = val, true
			}
			return
		}
		if code == 0 {
			in.x = int(int64(val-d.ax.Min) * int64(in.w-1) / int64(d.ax.Max-d.ax.Min))
		} else {
			in.y = int(int64(val-d.ay.Min) * int64(in.h-1) / int64(max(1, d.ay.Max-d.ay.Min)))
		}
		in.send(Event{Mouse: true, X: in.x, Y: in.y})
	case evKey:
		switch {
		case code == 0x110: // BTN_LEFT (mouse button, clickpad press)
			if val == 1 {
				in.send(Event{Mouse: true, X: in.x, Y: in.y, Click: true})
			}
			return
		case code == 0x14a: // BTN_TOUCH
			if d.touchpad {
				if val == 1 {
					d.touching, d.haveX, d.haveY = true, false, false
					d.downAt, d.moved = time.Now(), 0
				} else {
					d.touching = false
					// A short, still touch is a tap: click.
					if time.Since(d.downAt) < 220*time.Millisecond && d.moved < 12 {
						in.send(Event{Mouse: true, X: in.x, Y: in.y, Click: true})
					}
				}
			} else if val == 1 { // touch screen / tablet: touching is clicking
				in.send(Event{Mouse: true, X: in.x, Y: in.y, Click: true})
			}
			return
		case code >= 0x100 && code < 0x160: // other buttons and tools
			return
		}
		if code == 42 || code == 54 { // shifts
			in.shift = val != 0
			return
		}
		if val == 0 { // key up
			return
		}
		if e, ok := keyEvent(code, in.shift); ok {
			in.send(e)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// US keyboard layout (Linux key codes).
var (
	plain = map[uint16]rune{2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0', 12: '-', 13: '=',
		16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p', 26: '[', 27: ']',
		30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l', 39: ';', 40: '\'', 41: '`', 43: '\\',
		44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm', 51: ',', 52: '.', 53: '/'}
	shifted = map[rune]rune{'1': '!', '2': '@', '3': '#', '4': '$', '5': '%', '6': '^', '7': '&', '8': '*', '9': '(', '0': ')',
		'-': '_', '=': '+', '[': '{', ']': '}', ';': ':', '\'': '"', '`': '~', '\\': '|', ',': '<', '.': '>', '/': '?'}
)

func keyEvent(code uint16, shift bool) (Event, bool) {
	switch code {
	case 28, 96:
		return Event{Key: KeyEnter}, true
	case 1:
		return Event{Key: KeyEsc}, true
	case 15:
		if shift {
			return Event{Key: KeyBackTab}, true
		}
		return Event{Key: KeyTab}, true
	case 14:
		return Event{Key: KeyBackspace}, true
	case 103:
		return Event{Key: KeyUp}, true
	case 108:
		return Event{Key: KeyDown}, true
	case 105:
		return Event{Key: KeyLeft}, true
	case 106:
		return Event{Key: KeyRight}, true
	case 68:
		return Event{Key: KeyF10}, true
	case 57:
		return Event{Key: KeySpace, Rune: ' '}, true
	}
	r, ok := plain[code]
	if !ok {
		return Event{}, false
	}
	if shift {
		if r >= 'a' && r <= 'z' {
			r -= 32
		} else if s, ok := shifted[r]; ok {
			r = s
		}
	}
	return Event{Rune: r}, true
}
