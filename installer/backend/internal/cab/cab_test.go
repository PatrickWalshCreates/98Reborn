package cab

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// Test cabinets are synthesised here (no Microsoft content in the repo).

type tFile struct {
	name   string
	size   int
	offset int
	folder uint16
}

type tBlock struct {
	data    []byte
	uncomp  int
	corrupt bool
}

// writeCab builds a single-folder cabinet with the given data blocks and file entries.
func writeCab(name, prev, next string, comp uint16, blocks []tBlock, files []tFile) []byte {
	le := binary.LittleEndian
	var hdr bytes.Buffer
	flags := uint16(0)
	if prev != "" {
		flags |= flagPrev
	}
	if next != "" {
		flags |= flagNext
	}
	strs := []byte{}
	if prev != "" {
		strs = append(strs, append([]byte(prev), 0, 'd', 0)...)
	}
	if next != "" {
		strs = append(strs, append([]byte(next), 0, 'd', 0)...)
	}
	var ft bytes.Buffer
	for _, f := range files {
		var e [16]byte
		le.PutUint32(e[0:], uint32(f.size))
		le.PutUint32(e[4:], uint32(f.offset))
		le.PutUint16(e[8:], f.folder)
		le.PutUint16(e[10:], 0x2B21) // 2001-09-01
		ft.Write(e[:])
		ft.WriteString(f.name)
		ft.WriteByte(0)
	}
	folderOff := 36 + len(strs)
	filesOff := folderOff + 8
	dataOff := filesOff + ft.Len()
	var data bytes.Buffer
	for _, b := range blocks {
		var h [8]byte
		le.PutUint16(h[4:], uint16(len(b.data)))
		le.PutUint16(h[6:], uint16(b.uncomp))
		le.PutUint32(h[0:], checksum(h[4:8], checksum(b.data, 0)))
		d := append([]byte(nil), b.data...)
		if b.corrupt {
			d[0] ^= 1
		}
		data.Write(h[:])
		data.Write(d)
	}
	total := dataOff + data.Len()
	var h [36]byte
	copy(h[:], "MSCF")
	le.PutUint32(h[8:], uint32(total))
	le.PutUint32(h[16:], uint32(filesOff))
	h[24], h[25] = 3, 1
	le.PutUint16(h[26:], 1)
	le.PutUint16(h[28:], uint16(len(files)))
	le.PutUint16(h[30:], flags)
	hdr.Write(h[:])
	hdr.Write(strs)
	var fo [8]byte
	le.PutUint32(fo[0:], uint32(dataOff))
	le.PutUint16(fo[4:], uint16(len(blocks)))
	le.PutUint16(fo[6:], comp)
	hdr.Write(fo[:])
	hdr.Write(ft.Bytes())
	hdr.Write(data.Bytes())
	return hdr.Bytes()
}

func parse(t *testing.T, name string, b []byte) *Cabinet {
	t.Helper()
	c, err := Parse(bytes.NewReader(b), int64(len(b)), name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func extractAll(t *testing.T, sets []*Set) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	var all []*File
	for _, s := range sets {
		all = append(all, s.Files...)
	}
	if err := Extract(all, func(f *File, d []byte) error { out[f.Name] = append([]byte(nil), d...); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// A folder spanning two cabinets, with a data block split across the boundary
// and a file continued from one cabinet into the next.
func TestSpanningStored(t *testing.T) {
	a := []byte(strings.Repeat("A", 25))
	b := []byte(strings.Repeat("B", 30))
	c := []byte(strings.Repeat("C", 15))
	stream := append(append(append([]byte{}, a...), b...), c...) // 70 bytes, blocks of 20
	blk := func(i, j int) []byte { return stream[i:j] }
	cab1 := writeCab("ONE.CAB", "", "TWO.CAB", compNone,
		[]tBlock{{blk(0, 20), 20, false}, {blk(20, 40), 20, false}, {blk(40, 50), 0, false}}, // last block split
		[]tFile{{"a.txt", 25, 0, 0}, {"b.txt", 30, 25, ifoldContToNext}})
	cab2 := writeCab("TWO.CAB", "ONE.CAB", "", compNone,
		[]tBlock{{blk(50, 60), 20, false}, {blk(60, 70), 10, false}},
		[]tFile{{"b.txt", 30, 25, ifoldContFromPrev}, {"c.txt", 15, 55, 0}})
	// pass them out of order: chains are built from Next links
	sets, err := BuildSets([]*Cabinet{parse(t, "TWO.CAB", cab2), parse(t, "ONE.CAB", cab1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || len(sets[0].Files) != 3 {
		t.Fatalf("sets=%d files=%d", len(sets), len(sets[0].Files))
	}
	got := extractAll(t, sets)
	for name, want := range map[string][]byte{"a.txt": a, "b.txt": b, "c.txt": c} {
		if !bytes.Equal(got[name], want) {
			t.Errorf("%s: got %q", name, got[name])
		}
	}
}

func TestChecksumDetectsCorruption(t *testing.T) {
	data := []byte("hello cabinet world")
	cb := writeCab("X.CAB", "", "", compNone, []tBlock{{data, len(data), true}}, []tFile{{"x", len(data), 0, 0}})
	sets, _ := BuildSets([]*Cabinet{parse(t, "X.CAB", cb)})
	err := Extract(sets[0].Files, func(*File, []byte) error { return nil })
	var xe *ExtractError
	if !errors.As(err, &xe) || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
}

func TestMissingContinuation(t *testing.T) {
	cb := writeCab("TWO.CAB", "ONE.CAB", "", compNone, []tBlock{{[]byte("abc"), 3, false}},
		[]tFile{{"b", 3, 0, ifoldContFromPrev}})
	if _, err := BuildSets([]*Cabinet{parse(t, "TWO.CAB", cb)}); err == nil {
		t.Fatal("expected error for missing previous cabinet")
	}
	cb = writeCab("ONE.CAB", "", "TWO.CAB", compNone, []tBlock{{[]byte("abc"), 0, false}},
		[]tFile{{"b", 6, 0, ifoldContToNext}})
	sets, _ := BuildSets([]*Cabinet{parse(t, "ONE.CAB", cb)})
	if err := Extract(sets[0].Files, func(*File, []byte) error { return nil }); err == nil {
		t.Fatal("expected error for missing next cabinet")
	}
}

// MSZIP: each block is "CK" + deflate, with the previous 32 KB as dictionary.
func TestMSZIP(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	words := []string{"windows ", "ninety ", "eight ", "second ", "edition ", "vxd ", "driver "}
	var stream []byte
	for len(stream) < 100_000 {
		stream = append(stream, words[r.Intn(len(words))]...)
	}
	var blocks []tBlock
	for i := 0; i < len(stream); i += 32768 {
		end := min(i+32768, len(stream))
		var buf bytes.Buffer
		buf.WriteString("CK")
		dict := stream[max(0, i-32768):i]
		w, _ := flate.NewWriterDict(&buf, flate.BestCompression, dict)
		w.Write(stream[i:end])
		w.Close()
		blocks = append(blocks, tBlock{buf.Bytes(), end - i, false})
	}
	files := []tFile{{"first", 40000, 0, 0}, {"second", len(stream) - 40000, 40000, 0}}
	sets, err := BuildSets([]*Cabinet{parse(t, "Z.CAB", writeCab("Z.CAB", "", "", compMSZIP, blocks, files))})
	if err != nil {
		t.Fatal(err)
	}
	got := extractAll(t, sets)
	if !bytes.Equal(got["first"], stream[:40000]) || !bytes.Equal(got["second"], stream[40000:]) {
		t.Fatal("mszip output mismatch")
	}
}

// e8Encode is the encoder-side transform; decode(encode(x)) must equal x.
func e8Encode(data []byte, curpos, filesize int32) {
	for i := 0; i < len(data)-10; {
		if data[i] != 0xE8 {
			i++
			curpos++
			continue
		}
		rel := int32(binary.LittleEndian.Uint32(data[i+1:]))
		if rel >= -curpos && rel < filesize {
			var abs int32
			if rel < filesize-curpos {
				abs = rel + curpos
			} else {
				abs = rel - filesize
			}
			binary.LittleEndian.PutUint32(data[i+1:], uint32(abs))
		}
		i += 5
		curpos += 5
	}
}

func TestE8RoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	orig := make([]byte, 32768)
	r.Read(orig)
	for i := 0; i < 2000; i++ {
		orig[r.Intn(len(orig)-20)] = 0xE8
	}
	for _, pos := range []int32{0, 32768, 1 << 20} {
		d := append([]byte(nil), orig...)
		e8Encode(d, pos, 12_000_000)
		e8Translate(d, pos, 12_000_000)
		if !bytes.Equal(d, orig) {
			t.Fatalf("E8 round trip failed at frame offset %d", pos)
		}
	}
}

func TestHuffmanCanonical(t *testing.T) {
	// lengths a:1 b:2 c:3 d:3 -> codes 0, 10, 110, 111
	var h huffman
	if err := h.build([]byte{1, 2, 3, 3}, 2); err != nil { // tableBits 2 forces the slow path for c, d
		t.Fatal(err)
	}
	// bitstream: 0 10 110 111 0 ... packed MSB-first into 16-bit LE words
	bits := "0101101110"
	var w uint16
	for i, c := range bits {
		if c == '1' {
			w |= 1 << (15 - i)
		}
	}
	br := &bitReader{in: []byte{byte(w), byte(w >> 8)}}
	for _, want := range []int{0, 1, 2, 3, 0} {
		got, err := h.decode(br)
		if err != nil || got != want {
			t.Fatalf("got %d (%v), want %d", got, err, want)
		}
	}
	if err := h.build([]byte{1, 1, 1}, 4); err == nil {
		t.Fatal("oversubscribed table accepted")
	}
}
