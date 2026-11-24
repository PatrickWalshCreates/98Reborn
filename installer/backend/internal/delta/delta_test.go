package delta

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	src := make([]byte, 200_000)
	r.Read(src)
	dst := append([]byte(nil), src...)
	for i := 0; i < 50; i++ { // scattered byte edits, like a VxD patch
		dst[r.Intn(len(dst))] ^= 0xFF
	}
	dst = append(dst[:1000], append([]byte("INSERTED BLOCK"), dst[1000:]...)...)
	dst = dst[:len(dst)-5000]

	p, err := Make(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > 5000 {
		t.Errorf("delta too large: %d bytes", len(p))
	}
	out, err := Apply(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, dst) {
		t.Fatal("round trip mismatch")
	}
}

func TestEdgeCases(t *testing.T) {
	for _, c := range []struct{ src, dst string }{
		{"", ""}, {"", "new"}, {"old", ""}, {"short", "short"}, {"abc", "xyz"},
	} {
		p, err := Make([]byte(c.src), []byte(c.dst))
		if err != nil {
			t.Fatal(err)
		}
		out, err := Apply([]byte(c.src), p)
		if err != nil || string(out) != c.dst {
			t.Errorf("%q->%q: got %q, %v", c.src, c.dst, out, err)
		}
	}
}

func TestWrongSource(t *testing.T) {
	p, _ := Make([]byte("original contents here"), []byte("patched contents here"))
	if _, err := Apply([]byte("something else entirely"), p); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("want ErrSourceMismatch, got %v", err)
	}
}

func TestCorruptBody(t *testing.T) {
	src := bytes.Repeat([]byte("0123456789"), 100)
	p, _ := Make(src, append(src, 'x'))
	p[len(p)-3] ^= 0xFF
	if _, err := Apply(src, p); err == nil {
		t.Fatal("corrupt delta applied without error")
	}
}

// Fixture produced with Python + bz2 and verified with the system bspatch.
func TestBSPatchFixture(t *testing.T) {
	old, _ := os.ReadFile("testdata/old.bin")
	want, _ := os.ReadFile("testdata/new.bin")
	patch, _ := os.ReadFile("testdata/patch.bsdiff")
	got, err := BSPatch(old, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("bspatch output mismatch")
	}
}
