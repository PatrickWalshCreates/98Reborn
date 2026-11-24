// Package iso9660 reads ISO 9660 CD images, preferring Joliet long names when
// present. Read-only and pure Go, so the ISO builder runs on any host OS
// without mounting anything.
package iso9660

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const sectorSize = 2048

type Image struct {
	r        io.ReaderAt
	size     int64
	closer   io.Closer
	VolumeID string
	Joliet   bool
	root     Entry
}

type Entry struct {
	Name    string
	Path    string // slash-separated, no leading slash ("WIN98/BASE4.CAB")
	IsDir   bool
	Size    int64
	ModTime time.Time
	extent  uint32
}

// OpenFile opens an ISO file on disk. Close it when done.
func OpenFile(name string) (*Image, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	im, err := Open(f, st.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	im.closer = f
	return im, nil
}

func (im *Image) Close() error {
	if im.closer != nil {
		return im.closer.Close()
	}
	return nil
}

// Open parses the volume descriptors of an image.
func Open(r io.ReaderAt, size int64) (*Image, error) {
	im := &Image{r: r, size: size}
	var pvd, joliet []byte
	for s := int64(16); s < 64; s++ {
		vd := make([]byte, sectorSize)
		if _, err := r.ReadAt(vd, s*sectorSize); err != nil {
			return nil, fmt.Errorf("reading volume descriptor: %w", err)
		}
		if string(vd[1:6]) != "CD001" {
			return nil, errors.New("not an ISO 9660 image")
		}
		switch vd[0] {
		case 1:
			if pvd == nil {
				pvd = vd
			}
		case 2:
			if esc := string(vd[88:91]); esc == "%/@" || esc == "%/C" || esc == "%/E" {
				joliet = vd
			}
		case 255:
			s = 64
		}
	}
	if pvd == nil {
		return nil, errors.New("no primary volume descriptor")
	}
	im.VolumeID = strings.TrimSpace(string(pvd[40:72]))
	vd := pvd
	if joliet != nil {
		vd, im.Joliet = joliet, true
	}
	root, err := im.parseRecord(vd[156:190], "")
	if err != nil {
		return nil, fmt.Errorf("root directory: %w", err)
	}
	root.Name, root.Path = "", ""
	im.root = *root
	return im, nil
}

func (im *Image) parseRecord(rec []byte, parent string) (*Entry, error) {
	if len(rec) < 34 || int(rec[0]) > len(rec) {
		return nil, errors.New("directory record truncated")
	}
	nameLen := int(rec[32])
	if 33+nameLen > int(rec[0]) {
		return nil, errors.New("directory record name overflows")
	}
	raw := rec[33 : 33+nameLen]
	e := &Entry{
		extent: binary.LittleEndian.Uint32(rec[2:]),
		Size:   int64(binary.LittleEndian.Uint32(rec[10:])),
		IsDir:  rec[25]&0x02 != 0,
	}
	if rec[25]&0x80 != 0 {
		return nil, errors.New("multi-extent files are not supported")
	}
	if t := rec[18:25]; t[0] != 0 {
		e.ModTime = time.Date(1900+int(t[0]), time.Month(t[1]), int(t[2]), int(t[3]), int(t[4]), int(t[5]), 0,
			time.FixedZone("", int(int8(t[6]))*15*60))
	}
	switch {
	case nameLen == 1 && (raw[0] == 0 || raw[0] == 1):
		e.Name = "\x00"
	case im.Joliet:
		u := make([]uint16, nameLen/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(raw[2*i:])
		}
		e.Name = string(utf16.Decode(u))
	default:
		e.Name = string(raw)
	}
	if !e.IsDir {
		if i := strings.LastIndexByte(e.Name, ';'); i >= 0 {
			e.Name = e.Name[:i]
		}
		e.Name = strings.TrimSuffix(e.Name, ".")
	}
	e.Path = path.Join(parent, e.Name)
	return e, nil
}

func (im *Image) readDir(d Entry) ([]Entry, error) {
	if d.Size > 64<<20 {
		return nil, errors.New("directory too large")
	}
	buf := make([]byte, d.Size)
	if _, err := im.r.ReadAt(buf, int64(d.extent)*sectorSize); err != nil && err != io.EOF {
		return nil, err
	}
	var out []Entry
	for off := 0; off < len(buf); {
		n := int(buf[off])
		if n == 0 { // records never cross sectors: skip padding
			off = (off/sectorSize + 1) * sectorSize
			continue
		}
		if off+n > len(buf) {
			return nil, errors.New("directory record overflows directory")
		}
		e, err := im.parseRecord(buf[off:off+n], d.Path)
		if err != nil {
			return nil, err
		}
		if e.Name != "\x00" {
			out = append(out, *e)
		}
		off += n
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Stat finds an entry by path, case-insensitively ("" or "/" is the root).
func (im *Image) Stat(p string) (Entry, error) {
	p = strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/")
	cur := im.root
	if p == "" {
		return cur, nil
	}
	for _, part := range strings.Split(p, "/") {
		if !cur.IsDir {
			return Entry{}, fs.ErrNotExist
		}
		ents, err := im.readDir(cur)
		if err != nil {
			return Entry{}, err
		}
		found := false
		for _, e := range ents {
			if strings.EqualFold(e.Name, part) {
				cur, found = e, true
				break
			}
		}
		if !found {
			return Entry{}, fmt.Errorf("%s: %w", p, fs.ErrNotExist)
		}
	}
	return cur, nil
}

func (im *Image) ReadDir(p string) ([]Entry, error) {
	d, err := im.Stat(p)
	if err != nil {
		return nil, err
	}
	if !d.IsDir {
		return nil, fmt.Errorf("%s is not a directory", p)
	}
	return im.readDir(d)
}

// Open returns a reader over a file's data.
func (im *Image) Open(p string) (*io.SectionReader, error) {
	e, err := im.Stat(p)
	if err != nil {
		return nil, err
	}
	return im.OpenEntry(e)
}

func (im *Image) OpenEntry(e Entry) (*io.SectionReader, error) {
	if e.IsDir {
		return nil, fmt.Errorf("%s is a directory", e.Path)
	}
	off := int64(e.extent) * sectorSize
	if off+e.Size > im.size {
		return nil, fmt.Errorf("%s extends past the end of the image", e.Path)
	}
	return io.NewSectionReader(im.r, off, e.Size), nil
}

func (im *Image) ReadFile(p string) ([]byte, error) {
	sr, err := im.Open(p)
	if err != nil {
		return nil, err
	}
	b := make([]byte, sr.Size())
	_, err = io.ReadFull(sr, b)
	return b, err
}

// Walk visits every entry depth-first in name order.
func (im *Image) Walk(fn func(Entry) error) error {
	var walk func(Entry, int) error
	walk = func(d Entry, depth int) error {
		if depth > 32 {
			return errors.New("directory nesting too deep")
		}
		ents, err := im.readDir(d)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := fn(e); err != nil {
				return err
			}
			if e.IsDir {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(im.root, 0)
}

// BootImage is the El Torito default boot entry of a bootable CD.
type BootImage struct {
	Media   string // "floppy-1.2M", "floppy-1.44M", "floppy-2.88M", "hdd" or "none"
	LoadRBA uint32
	Sectors uint16 // 512-byte sectors (for no-emulation images)
}

// Boot returns the El Torito default boot entry, or an error if the CD isn't bootable.
func (im *Image) Boot() (*BootImage, error) {
	var catalog uint32
	for s := int64(16); s < 64; s++ {
		vd := make([]byte, sectorSize)
		if _, err := im.r.ReadAt(vd, s*sectorSize); err != nil {
			return nil, err
		}
		if vd[0] == 255 {
			break
		}
		if vd[0] == 0 && strings.HasPrefix(string(vd[7:39]), "EL TORITO SPECIFICATION") {
			catalog = binary.LittleEndian.Uint32(vd[71:])
		}
	}
	if catalog == 0 {
		return nil, errors.New("CD is not bootable (no El Torito record)")
	}
	cat := make([]byte, 64)
	if _, err := im.r.ReadAt(cat, int64(catalog)*sectorSize); err != nil {
		return nil, err
	}
	if cat[0] != 1 || cat[30] != 0x55 || cat[31] != 0xAA {
		return nil, errors.New("El Torito catalog validation entry is invalid")
	}
	e := cat[32:]
	if e[0] != 0x88 {
		return nil, errors.New("El Torito default entry is not bootable")
	}
	b := &BootImage{LoadRBA: binary.LittleEndian.Uint32(e[8:]), Sectors: binary.LittleEndian.Uint16(e[6:])}
	b.Media = map[byte]string{0: "none", 1: "floppy-1.2M", 2: "floppy-1.44M", 3: "floppy-2.88M", 4: "hdd"}[e[1]&0x0F]
	return b, nil
}

// ReadBootImage returns the bytes of a floppy-emulation boot image.
func (im *Image) ReadBootImage() ([]byte, *BootImage, error) {
	b, err := im.Boot()
	if err != nil {
		return nil, nil, err
	}
	size := map[string]int{"floppy-1.2M": 1228800, "floppy-1.44M": 1474560, "floppy-2.88M": 2949120}[b.Media]
	if size == 0 {
		return nil, b, fmt.Errorf("boot image uses %q emulation; only floppy images are supported", b.Media)
	}
	data := make([]byte, size)
	if _, err := im.r.ReadAt(data, int64(b.LoadRBA)*sectorSize); err != nil {
		return nil, b, err
	}
	return data, b, nil
}
