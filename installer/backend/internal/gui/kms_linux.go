//go:build linux

package gui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// KMS output: a "dumb" buffer shown on the first connected display through
// the kernel's mode-setting interface (/dev/dri/card0). This works with the
// firmware framebuffer driver (simpledrm) and with real GPU drivers alike.

type drmCardRes struct {
	FbIDPtr, CrtcIDPtr, ConnectorIDPtr, EncoderIDPtr     uint64
	CountFbs, CountCrtcs, CountConnectors, CountEncoders uint32
	MinWidth, MaxWidth, MinHeight, MaxHeight             uint32
}

type drmModeInfo struct {
	Clock                                         uint32
	Hdisplay, HsyncStart, HsyncEnd, Htotal, Hskew uint16
	Vdisplay, VsyncStart, VsyncEnd, Vtotal, Vscan uint16
	Vrefresh, Flags, Type                         uint32
	Name                                          [32]byte
}

type drmGetConnector struct {
	EncodersPtr, ModesPtr, PropsPtr, PropValuesPtr         uint64
	CountModes, CountProps, CountEncoders                  uint32
	EncoderID, ConnectorID, ConnectorType, ConnectorTypeID uint32
	Connection, MmWidth, MmHeight, Subpixel, Pad           uint32
}

type drmGetEncoder struct {
	EncoderID, EncoderType, CrtcID, PossibleCrtcs, PossibleClones uint32
}

type drmCreateDumb struct {
	Height, Width, Bpp, Flags uint32
	Handle, Pitch             uint32
	Size                      uint64
}

type drmMapDumb struct {
	Handle, Pad uint32
	Offset      uint64
}

type drmFbCmd struct {
	FbID, Width, Height, Pitch, Bpp, Depth, Handle uint32
}

type drmCrtc struct {
	SetConnectorsPtr uint64
	CountConnectors  uint32
	CrtcID, FbID     uint32
	X, Y             uint32
	GammaSize        uint32
	ModeValid        uint32
	Mode             drmModeInfo
}

type drmDirty struct {
	FbID, Flags, Color, NumClips uint32
	ClipsPtr                     uint64
}

const (
	drmSetMaster     = 0x641e
	drmGetResources  = 0xC04064A0
	drmGetConnectorI = 0xC05064A7
	drmGetEncoderI   = 0xC01464A6
	drmCreateDumbI   = 0xC02064B2
	drmMapDumbI      = 0xC01064B3
	drmAddFB         = 0xC01C64AE
	drmSetCrtcI      = 0xC06864A2
	drmDirtyFB       = 0xC01864B1
)

type kms struct {
	f   *os.File
	mem []byte
	fb  uint32
}

func openKMS() (*Screen, error) {
	f, err := os.OpenFile("/dev/dri/card0", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Screen, error) { f.Close(); return nil, e }
	fd := f.Fd()
	syscall.Syscall(syscall.SYS_IOCTL, fd, drmSetMaster, 0)

	var res drmCardRes
	if err := ioctl(fd, drmGetResources, unsafe.Pointer(&res)); err != nil {
		return fail(fmt.Errorf("GETRESOURCES: %w", err))
	}
	crtcs := make([]uint32, max(res.CountCrtcs, 1))
	conns := make([]uint32, max(res.CountConnectors, 1))
	encs := make([]uint32, max(res.CountEncoders, 1))
	fbs := make([]uint32, max(res.CountFbs, 1))
	res.CrtcIDPtr = uint64(uintptr(unsafe.Pointer(&crtcs[0])))
	res.ConnectorIDPtr = uint64(uintptr(unsafe.Pointer(&conns[0])))
	res.EncoderIDPtr = uint64(uintptr(unsafe.Pointer(&encs[0])))
	res.FbIDPtr = uint64(uintptr(unsafe.Pointer(&fbs[0])))
	if err := ioctl(fd, drmGetResources, unsafe.Pointer(&res)); err != nil {
		return fail(err)
	}
	for _, cid := range conns[:res.CountConnectors] {
		var c drmGetConnector
		c.ConnectorID = cid
		if ioctl(fd, drmGetConnectorI, unsafe.Pointer(&c)) != nil || c.Connection != 1 || c.CountModes == 0 {
			continue // not connected
		}
		modes := make([]drmModeInfo, c.CountModes)
		cencs := make([]uint32, max(c.CountEncoders, 1))
		c.ModesPtr = uint64(uintptr(unsafe.Pointer(&modes[0])))
		c.EncodersPtr = uint64(uintptr(unsafe.Pointer(&cencs[0])))
		c.CountProps, c.PropsPtr, c.PropValuesPtr = 0, 0, 0
		if ioctl(fd, drmGetConnectorI, unsafe.Pointer(&c)) != nil {
			continue
		}
		mode := modes[0]
		for _, m := range modes[:c.CountModes] {
			if m.Type&(1<<3) != 0 { // DRM_MODE_TYPE_PREFERRED
				mode = m
				break
			}
		}
		// The CRTC driving this connector, or any it can use.
		crtc := uint32(0)
		if c.EncoderID != 0 {
			e := drmGetEncoder{EncoderID: c.EncoderID}
			if ioctl(fd, drmGetEncoderI, unsafe.Pointer(&e)) == nil {
				crtc = e.CrtcID
			}
		}
		if crtc == 0 {
			for _, eid := range cencs[:c.CountEncoders] {
				e := drmGetEncoder{EncoderID: eid}
				if ioctl(fd, drmGetEncoderI, unsafe.Pointer(&e)) != nil {
					continue
				}
				for i, cr := range crtcs[:res.CountCrtcs] {
					if e.PossibleCrtcs&(1<<i) != 0 {
						crtc = cr
						break
					}
				}
				if crtc != 0 {
					break
				}
			}
		}
		if crtc == 0 {
			continue
		}
		w, h := uint32(mode.Hdisplay), uint32(mode.Vdisplay)
		cd := drmCreateDumb{Width: w, Height: h, Bpp: 32}
		if err := ioctl(fd, drmCreateDumbI, unsafe.Pointer(&cd)); err != nil {
			return fail(fmt.Errorf("CREATE_DUMB: %w", err))
		}
		fbc := drmFbCmd{Width: w, Height: h, Pitch: cd.Pitch, Bpp: 32, Depth: 24, Handle: cd.Handle}
		if err := ioctl(fd, drmAddFB, unsafe.Pointer(&fbc)); err != nil {
			return fail(fmt.Errorf("ADDFB: %w", err))
		}
		md := drmMapDumb{Handle: cd.Handle}
		if err := ioctl(fd, drmMapDumbI, unsafe.Pointer(&md)); err != nil {
			return fail(fmt.Errorf("MAP_DUMB: %w", err))
		}
		mem, err := syscall.Mmap(int(fd), int64(md.Offset), int(cd.Size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			return fail(fmt.Errorf("mmap: %w", err))
		}
		cr := drmCrtc{CountConnectors: 1, CrtcID: crtc, FbID: fbc.FbID, ModeValid: 1, Mode: mode}
		connID := cid
		cr.SetConnectorsPtr = uint64(uintptr(unsafe.Pointer(&connID)))
		if err := ioctl(fd, drmSetCrtcI, unsafe.Pointer(&cr)); err != nil {
			syscall.Munmap(mem)
			return fail(fmt.Errorf("SETCRTC: %w", err))
		}
		s := &Screen{W: int(w), H: int(h), stride: int(cd.Pitch), bpp: 32, rOff: 16, gOff: 8, bOff: 0,
			kms: &kms{f: f, mem: mem, fb: fbc.FbID}}
		s.Info = fmt.Sprintf("%dx%d KMS (%s)", w, h, cString(mode.Name[:]))
		return s, nil
	}
	return fail(errors.New("no connected display"))
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func (k *kms) show(c *Canvas, w, h, stride int) error {
	for y := 0; y < h; y++ {
		row := k.mem[y*stride:]
		src := c.Pix[y*c.W:]
		for x := 0; x < w; x++ {
			binary.LittleEndian.PutUint32(row[x*4:], uint32(src[x])) // XRGB8888
		}
	}
	d := drmDirty{FbID: k.fb}
	if err := ioctl(k.f.Fd(), drmDirtyFB, unsafe.Pointer(&d)); err != nil && !errors.Is(err, syscall.ENOSYS) {
		return err
	}
	return nil
}

func (k *kms) close() {
	syscall.Munmap(k.mem)
	k.f.Close()
}
