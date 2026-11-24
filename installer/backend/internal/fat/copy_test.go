package fat

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"
)

// CopyUsed must produce a volume with identical contents at another offset,
// writing nothing over unallocated space.
func TestCopyUsed(t *testing.T) {
	src, v := newDisk(t, 80<<20)
	big := make([]byte, 3<<20)
	rand.Read(big)
	files := map[string][]byte{"IO.SYS": []byte("io"), "WINDOWS/SYSTEM/BIG.DAT": big, "Program Files/Long Name.txt": []byte("lfn")}
	for p, b := range files {
		if err := v.MkdirAll(dirOf(p)); err != nil {
			t.Fatal(err)
		}
		if err := v.WriteFile(p, b, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	v.Remove("IO.SYS") // a freed cluster must not be copied
	if err := v.Sync(); err != nil {
		t.Fatal(err)
	}
	const off = 7 << 20
	dst := &memDev{b: bytes.Repeat([]byte{0xA5}, len(src.b)+off)}
	if err := v.CopyUsed(dst, off, nil); err != nil {
		t.Fatal(err)
	}
	w, err := Open(dst, off)
	if err != nil {
		t.Fatal(err)
	}
	for p, b := range files {
		if p == "IO.SYS" {
			if _, err := w.Stat(p); err == nil {
				t.Fatal("removed file came back")
			}
			continue
		}
		got, err := w.ReadFile(p)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if dst.b[0] != 0xA5 || dst.b[len(dst.b)-1] != 0xA5 {
		t.Fatal("wrote outside the volume")
	}
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
