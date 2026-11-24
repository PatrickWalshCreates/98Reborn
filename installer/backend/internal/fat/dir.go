package fat

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

func (f *FS) rootID() uint32 {
	if f.Type == 32 {
		return f.rootCluster
	}
	return 0
}

func (f *FS) readDirRaw(dir uint32) ([]byte, error) {
	if dir == 0 {
		raw := make([]byte, f.rootEntries*dirEntrySize)
		_, err := f.dev.ReadAt(raw, f.base+f.rootStart)
		return raw, err
	}
	return f.readChain(dir, -1)
}

func (f *FS) slotOffset(dir uint32, slot int) (int64, error) {
	if dir == 0 {
		if slot >= f.rootEntries {
			return 0, errors.New("fat: root directory slot out of range")
		}
		return f.base + f.rootStart + int64(slot*dirEntrySize), nil
	}
	ch, err := f.chain(dir)
	if err != nil {
		return 0, err
	}
	off := slot * dirEntrySize
	idx := off / f.clusterSize
	if idx >= len(ch) {
		return 0, errors.New("fat: directory slot out of range")
	}
	return f.clusterOffset(ch[idx]) + int64(off%f.clusterSize), nil
}

func (f *FS) writeSlot(dir uint32, slot int, e []byte) error {
	off, err := f.slotOffset(dir, slot)
	if err != nil {
		return err
	}
	_, err = f.dev.WriteAt(e[:dirEntrySize], off)
	return err
}

func lfnChecksum(short []byte) byte {
	var s byte
	for _, c := range short[:11] {
		s = (s&1)<<7 + s>>1 + c
	}
	return s
}

func shortDisplay(e []byte) string {
	b := append([]byte(nil), e[:11]...)
	if b[0] == 0x05 {
		b[0] = 0xE5
	}
	base := strings.TrimRight(string(b[:8]), " ")
	ext := strings.TrimRight(string(b[8:11]), " ")
	if ext != "" {
		return base + "." + ext
	}
	return base
}

func lfnChars(e []byte) []uint16 {
	var u []uint16
	for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
		for o := r[0]; o < r[1]; o += 2 {
			u = append(u, binary.LittleEndian.Uint16(e[o:]))
		}
	}
	return u
}

func (f *FS) parseDir(dir uint32) ([]Entry, error) {
	raw, err := f.readDirRaw(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	var parts [][]uint16
	var chk byte
	expect, count := 0, 0
	reset := func() { parts, expect, count = nil, 0, 0 }
	for i := 0; i*dirEntrySize < len(raw); i++ {
		e := raw[i*dirEntrySize : (i+1)*dirEntrySize]
		if e[0] == 0 {
			break
		}
		if e[0] == entryDeleted {
			reset()
			continue
		}
		if e[11] == attrLFN {
			seq := int(e[0] & 0x1F)
			if e[0]&0x40 != 0 {
				parts, chk, count, expect = make([][]uint16, seq), e[13], seq, seq
			}
			if parts == nil || seq != expect || seq == 0 || e[13] != chk {
				reset()
				continue
			}
			parts[seq-1] = lfnChars(e)
			expect--
			continue
		}
		if e[11]&AttrVolume != 0 {
			reset()
			continue
		}
		name := shortDisplay(e)
		ent := Entry{
			Name: name, ShortName: name, Attr: e[11],
			Size:    int64(binary.LittleEndian.Uint32(e[28:])),
			ModTime: fromDOS(binary.LittleEndian.Uint16(e[24:]), binary.LittleEndian.Uint16(e[22:])),
			cluster: uint32(binary.LittleEndian.Uint16(e[26:])),
			dir:     dir, slot: i,
		}
		if f.Type == 32 {
			ent.cluster |= uint32(binary.LittleEndian.Uint16(e[20:])) << 16
		}
		if parts != nil && expect == 0 && lfnChecksum(e) == chk {
			var u []uint16
			for _, p := range parts {
				u = append(u, p...)
			}
			for k, c := range u {
				if c == 0 {
					u = u[:k]
					break
				}
			}
			ent.Name = string(utf16.Decode(u))
			ent.lfnSlots = count
		}
		reset()
		if name == "." || name == ".." {
			continue
		}
		out = append(out, ent)
	}
	return out, nil
}

func lookup(ents []Entry, name string) *Entry {
	for i := range ents {
		if strings.EqualFold(ents[i].Name, name) || strings.EqualFold(ents[i].ShortName, name) {
			return &ents[i]
		}
	}
	return nil
}

func (f *FS) rootEntry() Entry {
	return Entry{Attr: AttrDir, cluster: f.rootID()}
}

// walk resolves p, returning the parent directory id, the parent's entries,
// and the entry (nil if the final component doesn't exist).
func (f *FS) walk(p string) (uint32, []Entry, *Entry, error) {
	parts := splitPath(p)
	cur := f.rootEntry()
	if len(parts) == 0 {
		return 0, nil, &cur, nil
	}
	for i, part := range parts {
		if !cur.IsDir() {
			return 0, nil, nil, fmt.Errorf("%s: not a directory", strings.Join(parts[:i], "/"))
		}
		ents, err := f.parseDir(cur.cluster)
		if err != nil {
			return 0, nil, nil, err
		}
		e := lookup(ents, part)
		if i == len(parts)-1 {
			return cur.cluster, ents, e, nil
		}
		if e == nil {
			return 0, nil, nil, fmt.Errorf("%s: %w", strings.Join(parts[:i+1], "/"), errNotExist)
		}
		cur = *e
	}
	panic("unreachable")
}

func (f *FS) Stat(p string) (Entry, error) {
	_, _, e, err := f.walk(p)
	if err != nil {
		return Entry{}, err
	}
	if e == nil {
		return Entry{}, fmt.Errorf("%s: %w", p, errNotExist)
	}
	return *e, nil
}

func (f *FS) ReadDir(p string) ([]Entry, error) {
	e, err := f.Stat(p)
	if err != nil {
		return nil, err
	}
	if !e.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", p)
	}
	return f.parseDir(e.cluster)
}

func (f *FS) ReadFile(p string) ([]byte, error) {
	e, err := f.Stat(p)
	if err != nil {
		return nil, err
	}
	if e.IsDir() {
		return nil, fmt.Errorf("%s: is a directory", p)
	}
	return f.readChain(e.cluster, e.Size)
}

const shortChars = "!#$%&'()-@^_`{}~"

func validShortChar(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c > 127 && c < 256 || strings.ContainsRune(shortChars, c)
}

// to83 returns the 11-byte short name if name is exactly representable in 8.3 uppercase.
func to83(name string) ([]byte, bool) {
	base, ext, _ := strings.Cut(name, ".")
	if strings.Contains(ext, ".") || len(base) == 0 || len(base) > 8 || len(ext) > 3 {
		return nil, false
	}
	for _, c := range base + ext {
		if !validShortChar(c) {
			return nil, false
		}
	}
	b := []byte(fmt.Sprintf("%-8s%-3s", base, ext))
	if b[0] == 0xE5 {
		b[0] = 0x05
	}
	return b, true
}

// alias generates a unique NAME~N.EXT short name for a long name.
func alias(name string, taken map[string]bool) ([]byte, error) {
	clean := func(s string) string {
		var b strings.Builder
		for _, c := range strings.ToUpper(s) {
			switch {
			case c == ' ' || c == '.':
			case validShortChar(c):
				b.WriteRune(c)
			default:
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i+1:]
	}
	b, x := clean(base), clean(ext)
	if len(x) > 3 {
		x = x[:3]
	}
	if b == "" {
		b = "_"
	}
	for n := 1; n < 1000000; n++ {
		suffix := fmt.Sprintf("~%d", n)
		stem := b
		if len(stem)+len(suffix) > 8 {
			stem = stem[:8-len(suffix)]
		}
		cand := []byte(fmt.Sprintf("%-8s%-3s", stem+suffix, x))
		if !taken[string(cand)] {
			return cand, nil
		}
	}
	return nil, errors.New("fat: cannot generate short name")
}

func lfnEntries(name string, chk byte) [][]byte {
	u := utf16.Encode([]rune(name))
	n := (len(u) + 12) / 13
	padded := make([]uint16, n*13)
	for i := range padded {
		switch {
		case i < len(u):
			padded[i] = u[i]
		case i == len(u):
			padded[i] = 0
		default:
			padded[i] = 0xFFFF
		}
	}
	var out [][]byte
	for seq := n; seq >= 1; seq-- {
		e := make([]byte, dirEntrySize)
		e[0] = byte(seq)
		if seq == n {
			e[0] |= 0x40
		}
		e[11], e[13] = attrLFN, chk
		chars := padded[(seq-1)*13 : seq*13]
		k := 0
		for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
			for o := r[0]; o < r[1]; o += 2 {
				binary.LittleEndian.PutUint16(e[o:], chars[k])
				k++
			}
		}
		out = append(out, e)
	}
	return out
}

func validLongName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.TrimRight(name, " .") != name {
		return fmt.Errorf("invalid file name %q", name)
	}
	if strings.ContainsAny(name, `"*/:<>?\|`) {
		return fmt.Errorf("invalid character in file name %q", name)
	}
	for _, c := range name {
		if c < 32 {
			return fmt.Errorf("invalid character in file name %q", name)
		}
	}
	return nil
}

// createEntry adds a directory entry (with LFN entries when needed).
func (f *FS) createEntry(dir uint32, ents []Entry, name string, attr byte, cluster uint32, size uint32, mt time.Time) error {
	if err := validLongName(name); err != nil {
		return err
	}
	short, ok := to83(name)
	var lfn [][]byte
	if !ok {
		taken := map[string]bool{}
		for _, e := range ents {
			b, _ := to83(e.ShortName)
			taken[string(b)] = true
		}
		var err error
		if short, err = alias(name, taken); err != nil {
			return err
		}
		lfn = lfnEntries(name, lfnChecksum(short))
	}
	need := len(lfn) + 1
	slot, err := f.freeSlots(dir, need)
	if err != nil {
		return err
	}
	for i, e := range lfn {
		if err := f.writeSlot(dir, slot+i, e); err != nil {
			return err
		}
	}
	e := make([]byte, dirEntrySize)
	copy(e, short)
	e[11] = attr
	d, t := dosDateTime(mt)
	binary.LittleEndian.PutUint16(e[14:], t)
	binary.LittleEndian.PutUint16(e[16:], d)
	binary.LittleEndian.PutUint16(e[18:], d)
	binary.LittleEndian.PutUint16(e[20:], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[22:], t)
	binary.LittleEndian.PutUint16(e[24:], d)
	binary.LittleEndian.PutUint16(e[26:], uint16(cluster))
	binary.LittleEndian.PutUint32(e[28:], size)
	return f.writeSlot(dir, slot+len(lfn), e)
}

// freeSlots finds n consecutive free entries, growing the directory if needed.
func (f *FS) freeSlots(dir uint32, n int) (int, error) {
	raw, err := f.readDirRaw(dir)
	if err != nil {
		return 0, err
	}
	total := len(raw) / dirEntrySize
	run := 0
	for i := 0; i < total; i++ {
		b := raw[i*dirEntrySize]
		if b == 0 {
			if total-i >= n-run {
				return i - run, nil
			}
			run += total - i
			break
		}
		if b == entryDeleted {
			run++
			if run == n {
				return i - n + 1, nil
			}
		} else {
			run = 0
		}
	}
	if dir == 0 {
		return 0, errors.New("fat: root directory is full")
	}
	ch, err := f.chain(dir)
	if err != nil {
		return 0, err
	}
	extra := (n - run + f.clusterSize/dirEntrySize - 1) / (f.clusterSize / dirEntrySize)
	nc, err := f.alloc(extra)
	if err != nil {
		return 0, err
	}
	f.set(ch[len(ch)-1], nc[0])
	if err := f.writeClusters(nc, nil); err != nil {
		return 0, err
	}
	return total - run, nil
}

func (f *FS) updateEntry(e *Entry, mut func(raw []byte)) error {
	off, err := f.slotOffset(e.dir, e.slot)
	if err != nil {
		return err
	}
	raw := make([]byte, dirEntrySize)
	if _, err := f.dev.ReadAt(raw, off); err != nil {
		return err
	}
	mut(raw)
	_, err = f.dev.WriteAt(raw, off)
	return err
}

// WriteFile creates or replaces a file. Existing attributes are kept.
func (f *FS) WriteFile(p string, data []byte, mt time.Time) error {
	if int64(len(data)) > 0xFFFFFFFF {
		return errors.New("fat: file too large")
	}
	dir, ents, e, err := f.walk(p)
	if err != nil {
		return err
	}
	if e != nil && e.IsDir() {
		return fmt.Errorf("%s: is a directory", p)
	}
	if mt.IsZero() {
		mt = time.Now()
	}
	n := (len(data) + f.clusterSize - 1) / f.clusterSize
	if e != nil {
		if err := f.freeChain(e.cluster); err != nil {
			return err
		}
	}
	cl, err := f.alloc(n)
	if err != nil {
		return err
	}
	if err := f.writeClusters(cl, data); err != nil {
		return err
	}
	var first uint32
	if len(cl) > 0 {
		first = cl[0]
	}
	if e == nil {
		parts := splitPath(p)
		return f.createEntry(dir, ents, parts[len(parts)-1], AttrArchive, first, uint32(len(data)), mt)
	}
	d, t := dosDateTime(mt)
	return f.updateEntry(e, func(raw []byte) {
		raw[11] |= AttrArchive
		binary.LittleEndian.PutUint16(raw[20:], uint16(first>>16))
		binary.LittleEndian.PutUint16(raw[22:], t)
		binary.LittleEndian.PutUint16(raw[24:], d)
		binary.LittleEndian.PutUint16(raw[18:], d)
		binary.LittleEndian.PutUint16(raw[26:], uint16(first))
		binary.LittleEndian.PutUint32(raw[28:], uint32(len(data)))
	})
}

func (f *FS) Mkdir(p string) error {
	dir, ents, e, err := f.walk(p)
	if err != nil {
		return err
	}
	if e != nil {
		return fmt.Errorf("%s: already exists", p)
	}
	cl, err := f.alloc(1)
	if err != nil {
		return err
	}
	if err := f.writeClusters(cl, nil); err != nil {
		return err
	}
	parent := dir
	if parent == f.rootCluster && f.Type == 32 {
		parent = 0 // ".." of a first-level directory points to cluster 0
	}
	now := time.Now()
	d, t := dosDateTime(now)
	for i, nm := range []string{".", ".."} {
		raw := make([]byte, dirEntrySize)
		copy(raw, fmt.Sprintf("%-11s", nm))
		raw[11] = AttrDir
		c := cl[0]
		if i == 1 {
			c = parent
		}
		binary.LittleEndian.PutUint16(raw[20:], uint16(c>>16))
		binary.LittleEndian.PutUint16(raw[22:], t)
		binary.LittleEndian.PutUint16(raw[24:], d)
		binary.LittleEndian.PutUint16(raw[26:], uint16(c))
		if err := f.writeSlot(cl[0], i, raw); err != nil {
			return err
		}
	}
	parts := splitPath(p)
	return f.createEntry(dir, ents, parts[len(parts)-1], AttrDir, cl[0], 0, now)
}

func (f *FS) MkdirAll(p string) error {
	parts := splitPath(p)
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		e, err := f.Stat(sub)
		if err == nil {
			if !e.IsDir() {
				return fmt.Errorf("%s: not a directory", sub)
			}
			continue
		}
		if err := f.Mkdir(sub); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a file or an empty directory.
func (f *FS) Remove(p string) error {
	_, _, e, err := f.walk(p)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("%s: %w", p, errNotExist)
	}
	if len(splitPath(p)) == 0 {
		return errors.New("fat: cannot remove root")
	}
	if e.IsDir() {
		ents, err := f.parseDir(e.cluster)
		if err != nil {
			return err
		}
		if len(ents) > 0 {
			return fmt.Errorf("%s: directory not empty", p)
		}
	}
	if err := f.freeChain(e.cluster); err != nil {
		return err
	}
	for s := e.slot - e.lfnSlots; s <= e.slot; s++ {
		off, err := f.slotOffset(e.dir, s)
		if err != nil {
			return err
		}
		if _, err := f.dev.WriteAt([]byte{entryDeleted}, off); err != nil {
			return err
		}
	}
	return nil
}

// SetAttr sets the read-only/hidden/system/archive bits (directory bit unchanged).
func (f *FS) SetAttr(p string, attr byte) error {
	e, err := f.Stat(p)
	if err != nil {
		return err
	}
	if len(splitPath(p)) == 0 {
		return errors.New("fat: cannot set attributes on root")
	}
	return f.updateEntry(&e, func(raw []byte) {
		raw[11] = raw[11]&(AttrDir|AttrVolume) | attr&^(AttrDir|AttrVolume)
	})
}
