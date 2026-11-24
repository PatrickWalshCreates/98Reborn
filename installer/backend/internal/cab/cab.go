// Package cab reads Microsoft Cabinet files, including multi-cabinet sets
// where folders, files and data blocks span cabinets (as on the Windows 98 SE
// and Me CDs). Supports stored, MSZIP and LZX compression. Pure Go.
package cab

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	flagPrev    = 0x0001
	flagNext    = 0x0002
	flagReserve = 0x0004

	ifoldContFromPrev = 0xFFFD
	ifoldContToNext   = 0xFFFE
	ifoldContBoth     = 0xFFFF

	compNone    = 0
	compMSZIP   = 1
	compQuantum = 2
	compLZX     = 3

	attrNameUTF = 0x80
)

var (
	ErrNotCabinet = errors.New("not a Microsoft cabinet")
	// ErrQuantum: Quantum compression is only used by a few add-on cabinets, not the OS setup files.
	ErrQuantum = errors.New("Quantum compression is not supported")
)

type Cabinet struct {
	Name    string // file name, used to resolve Next/Prev links
	Size    int64
	Flags   uint16
	SetID   uint16
	Index   uint16
	Prev    string
	Next    string
	Folders []Folder
	Files   []Entry
	dataRes int
	r       io.ReaderAt
}

type Folder struct {
	DataOffset uint32
	NumBlocks  uint16
	Comp       uint16
}

type Entry struct {
	Name   string
	Size   uint32
	Offset uint32 // in the (logical) folder's uncompressed stream
	Folder uint16
	Date   uint16
	Time   uint16
	Attrs  uint16
}

// Parse reads a cabinet's header, folder and file tables.
func Parse(r io.ReaderAt, size int64, name string) (*Cabinet, error) {
	hdr := make([]byte, min(size, 65536+600))
	if _, err := r.ReadAt(hdr, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if len(hdr) < 36 || string(hdr[:4]) != "MSCF" {
		return nil, ErrNotCabinet
	}
	le := binary.LittleEndian
	c := &Cabinet{Name: name, Size: size, r: r}
	cbCabinet := int64(le.Uint32(hdr[8:]))
	coffFiles := int64(le.Uint32(hdr[16:]))
	if hdr[25] != 1 {
		return nil, fmt.Errorf("%s: unsupported cabinet version %d.%d", name, hdr[25], hdr[24])
	}
	nFolders := int(le.Uint16(hdr[26:]))
	nFiles := int(le.Uint16(hdr[28:]))
	c.Flags = le.Uint16(hdr[30:])
	c.SetID = le.Uint16(hdr[32:])
	c.Index = le.Uint16(hdr[34:])
	if cbCabinet > size {
		return nil, fmt.Errorf("%s: truncated (header says %d bytes, file is %d)", name, cbCabinet, size)
	}
	off := 36
	folderRes := 0
	if c.Flags&flagReserve != 0 {
		if len(hdr) < 40 {
			return nil, ErrNotCabinet
		}
		off = 40 + int(le.Uint16(hdr[36:]))
		folderRes = int(hdr[38])
		c.dataRes = int(hdr[39])
	}
	cstr := func() (string, error) {
		i := bytes.IndexByte(hdr[min(off, len(hdr)):], 0)
		if i < 0 || i > 255 {
			return "", fmt.Errorf("%s: bad header string", name)
		}
		s := string(hdr[off : off+i])
		off += i + 1
		return s, nil
	}
	var err error
	if c.Flags&flagPrev != 0 {
		if c.Prev, err = cstr(); err != nil {
			return nil, err
		}
		if _, err = cstr(); err != nil { // disk name
			return nil, err
		}
	}
	if c.Flags&flagNext != 0 {
		if c.Next, err = cstr(); err != nil {
			return nil, err
		}
		if _, err = cstr(); err != nil {
			return nil, err
		}
	}
	for i := 0; i < nFolders; i++ {
		if off+8 > len(hdr) {
			return nil, fmt.Errorf("%s: folder table truncated", name)
		}
		c.Folders = append(c.Folders, Folder{le.Uint32(hdr[off:]), le.Uint16(hdr[off+4:]), le.Uint16(hdr[off+6:])})
		off += 8 + folderRes
	}

	ft := make([]byte, min(size-coffFiles, int64(nFiles)*(16+256)))
	if coffFiles <= 0 || coffFiles >= size {
		return nil, fmt.Errorf("%s: bad file table offset", name)
	}
	if _, err := r.ReadAt(ft, coffFiles); err != nil && err != io.EOF {
		return nil, err
	}
	p := 0
	for i := 0; i < nFiles; i++ {
		if p+16 > len(ft) {
			return nil, fmt.Errorf("%s: file table truncated", name)
		}
		e := Entry{
			Size: le.Uint32(ft[p:]), Offset: le.Uint32(ft[p+4:]), Folder: le.Uint16(ft[p+8:]),
			Date: le.Uint16(ft[p+10:]), Time: le.Uint16(ft[p+12:]), Attrs: le.Uint16(ft[p+14:]),
		}
		j := bytes.IndexByte(ft[p+16:], 0)
		if j < 0 {
			return nil, fmt.Errorf("%s: file name truncated", name)
		}
		raw := ft[p+16 : p+16+j]
		if e.Attrs&attrNameUTF != 0 {
			e.Name = string(raw)
		} else {
			rs := make([]rune, len(raw)) // treat as Latin-1; Win9x CABs use ASCII
			for k, b := range raw {
				rs[k] = rune(b)
			}
			e.Name = string(rs)
		}
		e.Name = strings.ReplaceAll(e.Name, `\`, "/")
		p += 16 + j + 1
		if e.Folder < ifoldContFromPrev && int(e.Folder) >= nFolders {
			return nil, fmt.Errorf("%s: %s refers to missing folder %d", name, e.Name, e.Folder)
		}
		c.Files = append(c.Files, e)
	}
	return c, nil
}

// File is one extractable file in a Set.
type File struct {
	Name     string
	Size     uint32
	Modified time.Time
	Attrs    uint16
	Cabinet  string // cabinet where the file starts
	folder   *logicalFolder
	offset   uint32
}

type folderPart struct {
	cab *Cabinet
	idx int
}

type logicalFolder struct {
	parts      []folderPart
	comp       uint16
	incomplete bool // continues into a cabinet we don't have
}

// Set is a chain of linked cabinets with folders merged across boundaries.
type Set struct {
	Cabinets []*Cabinet
	Files    []*File
}

// BuildSets links cabinets by their Next names (case-insensitive) into chains
// and returns one Set per chain.
func BuildSets(cabs []*Cabinet) ([]*Set, error) {
	byName := map[string]*Cabinet{}
	for _, c := range cabs {
		byName[strings.ToUpper(c.Name)] = c
	}
	isNext := map[*Cabinet]bool{}
	for _, c := range cabs {
		if n, ok := byName[strings.ToUpper(c.Next)]; ok && c.Next != "" {
			isNext[n] = true
		}
	}
	sorted := append([]*Cabinet(nil), cabs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var sets []*Set
	var errs []string
	seen := map[*Cabinet]bool{}
	build := func(head *Cabinet) {
		var chain []*Cabinet
		for c := head; c != nil && !seen[c]; {
			seen[c] = true
			chain = append(chain, c)
			if c.Next == "" {
				break
			}
			c = byName[strings.ToUpper(c.Next)]
		}
		s, err := newSet(chain)
		if err != nil {
			errs = append(errs, err.Error())
		}
		if s != nil {
			sets = append(sets, s)
		}
	}
	for _, c := range sorted {
		if !isNext[c] {
			build(c)
		}
	}
	for _, c := range sorted { // cycles: anything not yet visited
		if !seen[c] {
			build(c)
		}
	}
	if len(errs) > 0 {
		return sets, errors.New(strings.Join(errs, "; "))
	}
	return sets, nil
}

func newSet(chain []*Cabinet) (*Set, error) {
	s := &Set{Cabinets: chain}
	var carry *logicalFolder
	for ci, c := range chain {
		contPrev, contNext := false, false
		for _, f := range c.Files {
			switch f.Folder {
			case ifoldContFromPrev:
				contPrev = true
			case ifoldContToNext:
				contNext = true
			case ifoldContBoth:
				contPrev, contNext = true, true
			}
		}
		if contPrev && carry == nil {
			return s, fmt.Errorf("%s continues a folder from %q, which is missing or not linked", c.Name, c.Prev)
		}
		if len(c.Folders) == 0 {
			return s, fmt.Errorf("%s has no folders", c.Name)
		}
		lfs := make([]*logicalFolder, len(c.Folders))
		for fi, fo := range c.Folders {
			if fi == 0 && carry != nil && contPrev {
				if carry.comp != fo.Comp {
					return s, fmt.Errorf("%s: continued folder changes compression", c.Name)
				}
				carry.parts = append(carry.parts, folderPart{c, 0})
				lfs[0] = carry
				continue
			}
			lfs[fi] = &logicalFolder{parts: []folderPart{{c, fi}}, comp: fo.Comp}
		}
		carry = nil
		for _, e := range c.Files {
			var lf *logicalFolder
			switch e.Folder {
			case ifoldContFromPrev, ifoldContBoth:
				continue // duplicate of the entry in the cabinet where the file starts
			case ifoldContToNext:
				lf = lfs[len(lfs)-1]
			default:
				lf = lfs[e.Folder]
			}
			s.Files = append(s.Files, &File{
				Name: e.Name, Size: e.Size, Modified: dosTime(e.Date, e.Time), Attrs: e.Attrs,
				Cabinet: c.Name, folder: lf, offset: e.Offset,
			})
		}
		if contNext {
			carry = lfs[len(lfs)-1]
			if ci == len(chain)-1 {
				carry.incomplete = true
			}
		}
	}
	return s, nil
}

func dosTime(d, t uint16) time.Time {
	if d == 0 {
		return time.Time{}
	}
	return time.Date(int(d>>9)+1980, time.Month(d>>5&15), int(d&31), int(t>>11), int(t>>5&63), int(t&31)*2, 0, time.UTC)
}

type block struct {
	data []byte
	size int
}

// checksum implements the cabinet CFDATA checksum.
func checksum(b []byte, seed uint32) uint32 {
	cs := seed
	n := len(b) / 4
	for i := 0; i < n; i++ {
		cs ^= binary.LittleEndian.Uint32(b[i*4:])
	}
	var ul uint32
	rest := b[n*4:]
	switch len(rest) {
	case 3:
		ul = uint32(rest[0])<<16 | uint32(rest[1])<<8 | uint32(rest[2])
	case 2:
		ul = uint32(rest[0])<<8 | uint32(rest[1])
	case 1:
		ul = uint32(rest[0])
	}
	return cs ^ ul
}

func (lf *logicalFolder) blocks() ([]block, error) {
	if lf.incomplete {
		return nil, errors.New("folder continues into a cabinet that is not present")
	}
	var out []block
	var pending []byte
	for _, p := range lf.parts {
		fo := p.cab.Folders[p.idx]
		off := int64(fo.DataOffset)
		hlen := 8 + p.cab.dataRes
		for i := 0; i < int(fo.NumBlocks); i++ {
			h := make([]byte, hlen)
			if _, err := p.cab.r.ReadAt(h, off); err != nil {
				return nil, fmt.Errorf("%s: data block header: %w", p.cab.Name, err)
			}
			csum := binary.LittleEndian.Uint32(h)
			cbData := int(binary.LittleEndian.Uint16(h[4:]))
			cbUncomp := int(binary.LittleEndian.Uint16(h[6:]))
			data := make([]byte, cbData)
			if _, err := p.cab.r.ReadAt(data, off+int64(hlen)); err != nil {
				return nil, fmt.Errorf("%s: data block: %w", p.cab.Name, err)
			}
			if csum != 0 && checksum(h[4:], checksum(data, 0)) != csum {
				return nil, fmt.Errorf("%s: data block %d checksum mismatch (corrupt image?)", p.cab.Name, i)
			}
			off += int64(hlen + cbData)
			pending = append(pending, data...)
			if cbUncomp == 0 {
				continue // block is split; the rest is in the next cabinet
			}
			out = append(out, block{pending, cbUncomp})
			pending = nil
		}
	}
	if pending != nil {
		return nil, errors.New("folder ends with a split data block")
	}
	return out, nil
}

// decompress returns at least the first need bytes of the folder's stream.
func (lf *logicalFolder) decompress(need int64) ([]byte, error) {
	blocks, err := lf.blocks()
	if err != nil {
		return nil, err
	}
	var out []byte
	switch lf.comp & 0xF {
	case compNone:
		for _, b := range blocks {
			if len(b.data) != b.size {
				return nil, errors.New("stored block size mismatch")
			}
			out = append(out, b.data...)
			if int64(len(out)) >= need {
				break
			}
		}
	case compMSZIP:
		for _, b := range blocks {
			if len(b.data) < 2 || b.data[0] != 'C' || b.data[1] != 'K' {
				return nil, errors.New("mszip block missing CK signature")
			}
			dict := out
			if len(dict) > 32768 {
				dict = dict[len(dict)-32768:]
			}
			fr := flate.NewReaderDict(bytes.NewReader(b.data[2:]), dict)
			buf := make([]byte, b.size)
			if _, err := io.ReadFull(fr, buf); err != nil {
				return nil, fmt.Errorf("mszip: %w", err)
			}
			out = append(out, buf...)
			if int64(len(out)) >= need {
				break
			}
		}
	case compLZX:
		var all []byte
		for _, b := range blocks {
			all = append(all, b.data...)
		}
		d, err := newLZX(uint(lf.comp>>8&0x1F), all)
		if err != nil {
			return nil, err
		}
		for i, b := range blocks {
			frame := make([]byte, b.size)
			if err := d.decodeFrame(frame); err != nil {
				return nil, fmt.Errorf("lzx frame %d: %w", i, err)
			}
			out = append(out, frame...)
			if int64(len(out)) >= need {
				break
			}
		}
	case compQuantum:
		return nil, ErrQuantum
	default:
		return nil, fmt.Errorf("unsupported compression type 0x%04x", lf.comp)
	}
	if int64(len(out)) < need {
		return nil, fmt.Errorf("folder has %d bytes, need %d", len(out), need)
	}
	return out, nil
}

// Extract decompresses the given files, calling fn for each. Each folder is
// decompressed once, so pass all wanted files together.
func Extract(files []*File, fn func(f *File, data []byte) error) error {
	byFolder := map[*logicalFolder][]*File{}
	var order []*logicalFolder
	for _, f := range files {
		if _, ok := byFolder[f.folder]; !ok {
			order = append(order, f.folder)
		}
		byFolder[f.folder] = append(byFolder[f.folder], f)
	}
	var failed []*FolderError
	for _, lf := range order {
		var need int64
		for _, f := range byFolder[lf] {
			need = max(need, int64(f.offset)+int64(f.Size))
		}
		data, err := lf.decompress(need)
		if err != nil { // keep going: report every unreadable folder at the end
			failed = append(failed, &FolderError{Files: byFolder[lf], Err: err})
			continue
		}
		for _, f := range byFolder[lf] {
			if err := fn(f, data[f.offset:int64(f.offset)+int64(f.Size)]); err != nil {
				return err
			}
		}
	}
	if len(failed) > 0 {
		return &ExtractError{Folders: failed}
	}
	return nil
}

// FolderError reports files that could not be extracted because their folder failed.
type FolderError struct {
	Files []*File
	Err   error
}

// ExtractError lists every folder that failed; all other files were extracted.
type ExtractError struct{ Folders []*FolderError }

func (e *ExtractError) Error() string {
	n := 0
	var parts []string
	for _, f := range e.Folders {
		n += len(f.Files)
		parts = append(parts, fmt.Sprintf("%s: %v", f.Files[0].Cabinet, f.Err))
	}
	if len(parts) > 5 {
		parts = append(parts[:5], fmt.Sprintf("and %d more", len(parts)-5))
	}
	return fmt.Sprintf("%d files in %d folders could not be extracted (%s)", n, len(e.Folders), strings.Join(parts, "; "))
}

// Match reports whether name matches a case-insensitive glob (path.Match syntax).
func Match(pattern, name string) bool {
	ok, _ := path.Match(strings.ToUpper(pattern), strings.ToUpper(name))
	return ok
}
