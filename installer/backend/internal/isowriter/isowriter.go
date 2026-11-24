// Package isowriter writes bootable ISO 9660 images with Joliet long names
// and El Torito boot records (BIOS floppy-emulation or no-emulation, plus an
// optional UEFI entry). Pure Go, so the 98Reborn Tool can build installer
// media on Windows, macOS and Linux.
package isowriter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const sector = 2048

// File is one file to put on the disc.
type File struct {
	Path    string // slash-separated, e.g. "98SE/BASE4.CAB"
	Size    int64
	Open    func() (io.ReadCloser, error)
	ModTime time.Time
	Hidden  bool // stored on the disc (e.g. a boot image) but not listed in any directory
}

// BootEntry describes an El Torito boot image. Path names a File (often Hidden).
type BootEntry struct {
	Path        string
	Emulation   string // "none", "floppy144"
	LoadSectors uint16 // 512-byte sectors to load for no-emulation (ISOLINUX: 4); 0 = whole image (EFI)
	InfoTable   bool   // patch an ISOLINUX boot-info table into the image
}

type Options struct {
	VolumeID string
	Files    []File
	BIOS     *BootEntry
	EFI      *BootEntry
	Now      time.Time
}

type node struct {
	name     string // original name
	iso      string // ISO 9660 identifier (with ";1" for files)
	joliet   []byte // UCS-2 BE identifier
	dir      bool
	children []*node
	parent   *node
	file     *File
	lba      uint32
	size     int64 // file size, or directory extent size per tree
	isoSize  int64
	jolSize  int64
	isoLBA   uint32
	jolLBA   uint32
	num      int // path table number (1-based), directories only
}

func newDir(name string, parent *node) *node { return &node{name: name, dir: true, parent: parent} }

var dchar = func() [256]bool {
	var t [256]bool
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	t['_'] = true
	return t
}()

func isoClean(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r < 256 && dchar[r] {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// isoName makes an ISO 9660 level-2 identifier.
func isoName(name string, dir bool) string {
	if dir {
		return isoClean(name, 31)
	}
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	ext = isoClean(ext, 8)
	base = isoClean(base, 28-len(ext))
	if base == "" {
		base = "_"
	}
	return base + "." + ext + ";1"
}

func jolietName(name string, dir bool) []byte {
	u := utf16.Encode([]rune(name))
	if len(u) > 64-2 {
		u = u[:62]
	}
	if !dir {
		u = append(u, ';', '1')
	}
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.BigEndian.PutUint16(b[2*i:], c)
	}
	return b
}

func buildTree(files []File) (*node, []*node, error) {
	root := newDir("", nil)
	var data []*node
	for i := range files {
		f := &files[i]
		if f.Hidden {
			data = append(data, &node{name: f.Path, file: f, size: f.Size})
			continue
		}
		parts := strings.Split(strings.Trim(f.Path, "/"), "/")
		cur := root
		for _, p := range parts[:len(parts)-1] {
			var next *node
			for _, c := range cur.children {
				if c.dir && strings.EqualFold(c.name, p) {
					next = c
				}
			}
			if next == nil {
				next = newDir(p, cur)
				cur.children = append(cur.children, next)
			}
			cur = next
		}
		leaf := parts[len(parts)-1]
		for _, c := range cur.children {
			if strings.EqualFold(c.name, leaf) {
				return nil, nil, fmt.Errorf("duplicate path %q", f.Path)
			}
		}
		n := &node{name: leaf, parent: cur, file: f, size: f.Size}
		cur.children = append(cur.children, n)
		data = append(data, n)
	}
	// Identifiers, made unique per directory.
	var assign func(d *node)
	assign = func(d *node) {
		seen := map[string]bool{}
		for _, c := range d.children {
			id := isoName(c.name, c.dir)
			for k := 1; seen[id]; k++ {
				suffix := fmt.Sprintf("_%d", k)
				if c.dir {
					id = isoClean(c.name, 31-len(suffix)) + suffix
				} else {
					b := isoName(c.name, false)
					stem, rest, _ := strings.Cut(b, ".")
					if len(stem) > 28-len(suffix) {
						stem = stem[:28-len(suffix)]
					}
					id = stem + suffix + "." + rest
				}
			}
			seen[id] = true
			c.iso, c.joliet = id, jolietName(c.name, c.dir)
			if c.dir {
				assign(c)
			}
		}
	}
	assign(root)
	return root, data, nil
}

func recLen(idLen int) int {
	n := 33 + idLen
	return n + n%2
}

// dirExtent computes the extent size of a directory for one naming scheme.
func dirExtent(d *node, ids func(*node) []byte) int64 {
	off := 0
	add := func(l int) {
		if off%sector+l > sector {
			off += sector - off%sector
		}
		off += l
	}
	add(34)
	add(34)
	for _, c := range d.children {
		add(recLen(len(ids(c))))
	}
	return int64((off + sector - 1) / sector * sector)
}

func sortedChildren(d *node, ids func(*node) []byte) []*node {
	cs := append([]*node(nil), d.children...)
	sort.Slice(cs, func(i, j int) bool { return bytes.Compare(ids(cs[i]), ids(cs[j])) < 0 })
	return cs
}

func both16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
	binary.BigEndian.PutUint16(b[2:], v)
}

func both32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
	binary.BigEndian.PutUint32(b[4:], v)
}

func recDate(t time.Time) []byte {
	t = t.UTC()
	return []byte{byte(t.Year() - 1900), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second()), 0}
}

func vdDate(t time.Time) []byte {
	b := []byte(t.UTC().Format("20060102150405") + "00")
	return append(b, 0)
}

func dirRecord(id []byte, lba uint32, size int64, dir bool, t time.Time) []byte {
	r := make([]byte, recLen(len(id)))
	r[0] = byte(len(r))
	both32(r[2:], lba)
	both32(r[10:], uint32(size))
	copy(r[18:25], recDate(t))
	if dir {
		r[25] = 2
	}
	both16(r[28:], 1)
	r[32] = byte(len(id))
	copy(r[33:], id)
	return r
}

type layout struct {
	dirs      []*node // BFS order (path table order)
	total     uint32
	ptISOL    uint32
	ptISOM    uint32
	ptJolL    uint32
	ptJolM    uint32
	ptISOSize int
	ptJolSize int
	catalog   uint32
	hasBoot   bool
	jolietVD  uint32
	termVD    uint32
}

func isoID(n *node) []byte    { return []byte(n.iso) }
func jolietID(n *node) []byte { return n.joliet }

func pathTable(dirs []*node, ids func(*node) []byte, lbaOf func(*node) uint32, bigEndian bool) []byte {
	var b bytes.Buffer
	for _, d := range dirs {
		id := []byte{0}
		if d.parent != nil {
			id = ids(d)
		}
		e := make([]byte, 8+len(id)+len(id)%2)
		e[0] = byte(len(id))
		parent := 1
		if d.parent != nil {
			parent = d.parent.num
		}
		if bigEndian {
			binary.BigEndian.PutUint32(e[2:], lbaOf(d))
			binary.BigEndian.PutUint16(e[6:], uint16(parent))
		} else {
			binary.LittleEndian.PutUint32(e[2:], lbaOf(d))
			binary.LittleEndian.PutUint16(e[6:], uint16(parent))
		}
		copy(e[8:], id)
		b.Write(e)
	}
	return b.Bytes()
}

func sectors(n int64) uint32 { return uint32((n + sector - 1) / sector) }

// Write builds the image into w (an *os.File works).
func Write(w io.WriterAt, o Options) error {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	root, data, err := buildTree(o.Files)
	if err != nil {
		return err
	}
	byPath := map[string]*node{}
	for _, n := range data {
		byPath[strings.ToLower(strings.Trim(n.file.Path, "/"))] = n
	}
	findBoot := func(b *BootEntry) (*node, error) {
		if b == nil {
			return nil, nil
		}
		n := byPath[strings.ToLower(strings.Trim(b.Path, "/"))]
		if n == nil {
			return nil, fmt.Errorf("boot image %q is not among the files", b.Path)
		}
		return n, nil
	}
	biosNode, err := findBoot(o.BIOS)
	if err != nil {
		return err
	}
	efiNode, err := findBoot(o.EFI)
	if err != nil {
		return err
	}
	if o.BIOS != nil && o.BIOS.Emulation == "floppy144" && biosNode.size != 1474560 {
		return errors.New("floppy-emulation boot image must be exactly 1.44 MB")
	}

	// Directories in path-table order: by level, then parent number, then identifier.
	var L layout
	L.hasBoot = o.BIOS != nil || o.EFI != nil
	queue := []*node{root}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		d.num = len(L.dirs) + 1
		L.dirs = append(L.dirs, d)
		for _, c := range sortedChildren(d, isoID) {
			if c.dir {
				queue = append(queue, c)
			}
		}
	}

	// Sector layout.
	next := uint32(16)
	next++ // PVD at 16
	if L.hasBoot {
		next++ // boot record at 17
	}
	L.jolietVD = next
	next++
	L.termVD = next
	next++
	ptISO := pathTable(L.dirs, isoID, func(*node) uint32 { return 0 }, false)
	ptJol := pathTable(L.dirs, jolietID, func(*node) uint32 { return 0 }, false)
	L.ptISOSize, L.ptJolSize = len(ptISO), len(ptJol)
	L.ptISOL = next
	next += sectors(int64(len(ptISO)))
	L.ptISOM = next
	next += sectors(int64(len(ptISO)))
	L.ptJolL = next
	next += sectors(int64(len(ptJol)))
	L.ptJolM = next
	next += sectors(int64(len(ptJol)))
	for _, d := range L.dirs {
		d.isoSize = dirExtent(d, isoID)
		d.isoLBA = next
		next += sectors(d.isoSize)
	}
	for _, d := range L.dirs {
		d.jolSize = dirExtent(d, jolietID)
		d.jolLBA = next
		next += sectors(d.jolSize)
	}
	if L.hasBoot {
		L.catalog = next
		next++
	}
	for _, n := range data {
		n.lba = next
		next += sectors(n.size)
	}
	L.total = next

	// File data first (so boot images can be patched with their final LBAs).
	for _, n := range data {
		if err := writeFile(w, n, o, biosNode, L); err != nil {
			return err
		}
	}
	// Directory extents.
	writeDirs := func(ids func(*node) []byte, lbaOf func(*node) uint32, sizeOf func(*node) int64) error {
		for _, d := range L.dirs {
			buf := make([]byte, sizeOf(d))
			off := 0
			put := func(r []byte) {
				if off%sector+len(r) > sector {
					off += sector - off%sector
				}
				copy(buf[off:], r)
				off += len(r)
			}
			parent := d
			if d.parent != nil {
				parent = d.parent
			}
			put(dirRecord([]byte{0}, lbaOf(d), sizeOf(d), true, o.Now))
			put(dirRecord([]byte{1}, lbaOf(parent), sizeOf(parent), true, o.Now))
			for _, c := range sortedChildren(d, ids) {
				if c.dir {
					put(dirRecord(ids(c), lbaOf(c), sizeOf(c), true, o.Now))
				} else {
					mt := c.file.ModTime
					if mt.IsZero() {
						mt = o.Now
					}
					put(dirRecord(ids(c), c.lba, c.size, false, mt))
				}
			}
			if _, err := w.WriteAt(buf, int64(lbaOf(d))*sector); err != nil {
				return err
			}
		}
		return nil
	}
	if err := writeDirs(isoID, func(n *node) uint32 { return n.isoLBA }, func(n *node) int64 { return n.isoSize }); err != nil {
		return err
	}
	if err := writeDirs(jolietID, func(n *node) uint32 { return n.jolLBA }, func(n *node) int64 { return n.jolSize }); err != nil {
		return err
	}
	// Path tables.
	for _, pt := range []struct {
		lba  uint32
		ids  func(*node) []byte
		lbaF func(*node) uint32
		be   bool
	}{
		{L.ptISOL, isoID, func(n *node) uint32 { return n.isoLBA }, false},
		{L.ptISOM, isoID, func(n *node) uint32 { return n.isoLBA }, true},
		{L.ptJolL, jolietID, func(n *node) uint32 { return n.jolLBA }, false},
		{L.ptJolM, jolietID, func(n *node) uint32 { return n.jolLBA }, true},
	} {
		b := pathTable(L.dirs, pt.ids, pt.lbaF, pt.be)
		if _, err := w.WriteAt(b, int64(pt.lba)*sector); err != nil {
			return err
		}
	}
	// Boot catalog.
	if L.hasBoot {
		cat, err := bootCatalog(o, biosNode, efiNode)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(cat, int64(L.catalog)*sector); err != nil {
			return err
		}
	}
	// Volume descriptors.
	if err := writeVDs(w, o, root, L); err != nil {
		return err
	}
	// System area, and pad the tail so the image has its full size without
	// touching data (the last file may end mid-sector or exactly at the end).
	if _, err := w.WriteAt(make([]byte, 16*sector), 0); err != nil {
		return err
	}
	end := int64(L.catalog+1) * sector
	if len(data) > 0 {
		last := data[len(data)-1]
		end = int64(last.lba)*sector + last.size
	}
	if gap := int64(L.total)*sector - end; gap > 0 {
		_, err = w.WriteAt(make([]byte, gap), end)
	}
	return err
}

func writeFile(w io.WriterAt, n *node, o Options, biosNode *node, L layout) error {
	rc, err := n.file.Open()
	if err != nil {
		return fmt.Errorf("%s: %w", n.file.Path, err)
	}
	defer rc.Close()
	if n == biosNode && o.BIOS.InfoTable {
		b, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if int64(len(b)) != n.size || len(b) < 64 {
			return fmt.Errorf("%s: unexpected size", n.file.Path)
		}
		// ISOLINUX boot-info table at offset 8: PVD LBA, file LBA, length, checksum of bytes 64..end.
		var sum uint32
		for i := 64; i+4 <= len(b); i += 4 {
			sum += binary.LittleEndian.Uint32(b[i:])
		}
		binary.LittleEndian.PutUint32(b[8:], 16)
		binary.LittleEndian.PutUint32(b[12:], n.lba)
		binary.LittleEndian.PutUint32(b[16:], uint32(len(b)))
		binary.LittleEndian.PutUint32(b[20:], sum)
		_, err = w.WriteAt(b, int64(n.lba)*sector)
		return err
	}
	buf := make([]byte, 1<<20)
	off := int64(n.lba) * sector
	var written int64
	for {
		k, err := rc.Read(buf)
		if k > 0 {
			if _, werr := w.WriteAt(buf[:k], off+written); werr != nil {
				return werr
			}
			written += int64(k)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", n.file.Path, err)
		}
	}
	if written != n.size {
		return fmt.Errorf("%s: read %d bytes, expected %d", n.file.Path, written, n.size)
	}
	return nil
}

func bootCatalog(o Options, bios, efi *node) ([]byte, error) {
	cat := make([]byte, sector)
	entry := func(e []byte, b *BootEntry, n *node) error {
		e[0] = 0x88
		switch b.Emulation {
		case "none", "":
			e[1] = 0
			cnt := b.LoadSectors
			if cnt == 0 {
				s := (n.size + 511) / 512
				if s > 0xFFFF {
					s = 0xFFFF
				}
				cnt = uint16(s)
			}
			binary.LittleEndian.PutUint16(e[6:], cnt)
		case "floppy144":
			e[1] = 2
			binary.LittleEndian.PutUint16(e[6:], 1)
		default:
			return fmt.Errorf("unknown emulation %q", b.Emulation)
		}
		binary.LittleEndian.PutUint32(e[8:], n.lba)
		return nil
	}
	// Validation entry.
	v := cat[0:32]
	v[0] = 1
	if bios == nil && efi != nil {
		v[1] = 0xEF
	}
	copy(v[4:28], "98Reborn")
	v[30], v[31] = 0x55, 0xAA
	var sum uint16
	for i := 0; i < 32; i += 2 {
		sum += binary.LittleEndian.Uint16(v[i:])
	}
	binary.LittleEndian.PutUint16(v[28:], -sum)
	off := 32
	if bios != nil {
		if err := entry(cat[off:off+32], o.BIOS, bios); err != nil {
			return nil, err
		}
		off += 32
	}
	if efi != nil {
		if bios != nil {
			h := cat[off : off+32]
			h[0] = 0x91 // final section header
			h[1] = 0xEF // UEFI
			binary.LittleEndian.PutUint16(h[2:], 1)
			off += 32
		}
		if err := entry(cat[off:off+32], o.EFI, efi); err != nil {
			return nil, err
		}
	}
	return cat, nil
}

func padA(dst []byte, s string) {
	for i := range dst {
		dst[i] = ' '
	}
	copy(dst, strings.ToUpper(s))
}

func padUCS2(dst []byte, s string) {
	for i := 0; i+1 < len(dst); i += 2 {
		dst[i], dst[i+1] = 0, ' '
	}
	u := utf16.Encode([]rune(s))
	for i, c := range u {
		if 2*i+1 >= len(dst) {
			break
		}
		binary.BigEndian.PutUint16(dst[2*i:], c)
	}
}

func writeVDs(w io.WriterAt, o Options, root *node, L layout) error {
	vol := o.VolumeID
	if vol == "" {
		vol = "98REBORN"
	}
	mk := func(typ byte, joliet bool) []byte {
		vd := make([]byte, sector)
		vd[0] = typ
		copy(vd[1:6], "CD001")
		vd[6] = 1
		if joliet {
			padUCS2(vd[8:40], "")
			padUCS2(vd[40:72], vol)
			copy(vd[88:91], "%/E")
		} else {
			padA(vd[8:40], "")
			padA(vd[40:72], isoClean(vol, 32))
		}
		both32(vd[80:], L.total)
		both16(vd[120:], 1)
		both16(vd[124:], 1)
		both16(vd[128:], sector)
		ptSize, ptL, ptM := L.ptISOSize, L.ptISOL, L.ptISOM
		lba, size := root.isoLBA, root.isoSize
		if joliet {
			ptSize, ptL, ptM = L.ptJolSize, L.ptJolL, L.ptJolM
			lba, size = root.jolLBA, root.jolSize
		}
		both32(vd[132:], uint32(ptSize))
		binary.LittleEndian.PutUint32(vd[140:], ptL)
		binary.BigEndian.PutUint32(vd[148:], ptM)
		copy(vd[156:190], dirRecord([]byte{0}, lba, size, true, o.Now))
		for _, f := range [][2]int{{190, 318}, {318, 446}, {446, 574}, {574, 702}, {702, 739}, {739, 776}, {776, 813}} {
			if joliet {
				padUCS2(vd[f[0]:f[1]], "")
			} else {
				padA(vd[f[0]:f[1]], "")
			}
		}
		if joliet {
			padUCS2(vd[574:702], "98REBORN TOOL")
		} else {
			padA(vd[574:702], "98REBORN TOOL")
		}
		copy(vd[813:830], vdDate(o.Now))
		copy(vd[830:847], vdDate(o.Now))
		copy(vd[847:864], []byte("0000000000000000\x00"))
		copy(vd[864:881], []byte("0000000000000000\x00"))
		vd[881] = 1
		return vd
	}
	if _, err := w.WriteAt(mk(1, false), 16*sector); err != nil {
		return err
	}
	if L.hasBoot {
		br := make([]byte, sector)
		copy(br[1:6], "CD001")
		br[6] = 1
		copy(br[7:39], "EL TORITO SPECIFICATION")
		binary.LittleEndian.PutUint32(br[71:], L.catalog)
		if _, err := w.WriteAt(br, 17*sector); err != nil {
			return err
		}
	}
	if _, err := w.WriteAt(mk(2, true), int64(L.jolietVD)*sector); err != nil {
		return err
	}
	term := make([]byte, sector)
	term[0] = 255
	copy(term[1:6], "CD001")
	term[6] = 1
	_, err := w.WriteAt(term, int64(L.termVD)*sector)
	return err
}
