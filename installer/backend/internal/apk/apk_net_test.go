package apk

import (
	"os"
	"testing"
)

// Talks to the real Alpine mirror; run with APK_NET=1.
func TestResolveNet(t *testing.T) {
	if os.Getenv("APK_NET") == "" {
		t.Skip("set APK_NET=1")
	}
	c := &Client{Mirror: "https://dl-cdn.alpinelinux.org/alpine", Branch: "v3.24", Arch: "x86_64", Cache: t.TempDir(), Log: t.Logf}
	for _, r := range []string{"main", "community"} {
		if err := c.LoadIndex(r); err != nil {
			t.Fatal(err)
		}
	}
	pk, err := c.Resolve([]string{"busybox", "busybox-binsh", "kmod", "qemu-system-i386", "ca-certificates-bundle"},
		map[string]bool{"initramfs-generator": true, "linux-firmware-any": true})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, p := range pk {
		total += p.Size
		t.Logf("%-28s %8d KB", p.Name, p.Size/1024)
	}
	t.Logf("%d packages, %d MB", len(pk), total>>20)
	b, err := c.Fetch(c.Lookup("busybox"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	Unpack(b, func(e Entry) error { n++; return nil })
	t.Logf("busybox: %d entries", n)
}

// Lists a package's files: APK_NET=1 APK_LIST=font-terminus go test ./internal/apk -run List -v
func TestListNet(t *testing.T) {
	name := os.Getenv("APK_LIST")
	if os.Getenv("APK_NET") == "" || name == "" {
		t.Skip("set APK_NET=1 and APK_LIST")
	}
	c := &Client{Mirror: "https://dl-cdn.alpinelinux.org/alpine", Branch: "v3.24", Arch: "x86_64", Cache: os.Getenv("APK_CACHE")}
	for _, r := range []string{"main", "community"} {
		if err := c.LoadIndex(r); err != nil {
			t.Fatal(err)
		}
	}
	b, err := c.Fetch(c.Lookup(name))
	if err != nil {
		t.Fatal(err)
	}
	Unpack(b, func(e Entry) error { t.Logf("%s %d", e.Path, len(e.Data)); return nil })
}
