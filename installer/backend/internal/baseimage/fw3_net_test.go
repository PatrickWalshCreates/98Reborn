package baseimage

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/apk"
)

// Which iwlwifi firmware names does the shipped kernel ask for?
// APK_NET=1 go test ./internal/baseimage -run IwlwifiWants -v
func TestIwlwifiWants(t *testing.T) {
	if os.Getenv("APK_NET") == "" {
		t.Skip("set APK_NET=1")
	}
	cache, _ := os.UserCacheDir()
	c := &apk.Client{Mirror: "https://dl-cdn.alpinelinux.org/alpine", Branch: "v3.24", Arch: "x86_64", Cache: filepath.Join(cache, "98reborn", "apk")}
	for _, r := range []string{"main", "community"} {
		c.LoadIndex(r)
	}
	kpkg := os.Getenv("KPKG")
	if kpkg == "" {
		kpkg = "linux-lts"
	}
	raw, _ := c.Fetch(c.Lookup(kpkg))
	re := regexp.MustCompile(`firmware=(iwlwifi-[a-z]+-[a-z0-9]+-[a-z0-9]+-[a-z0-9]+[^\x00]*)`)
	apk.Unpack(raw, func(e apk.Entry) error {
		if !strings.HasSuffix(e.Path, "/iwlwifi.ko.gz") {
			return nil
		}
		z, _ := gzip.NewReader(bytes.NewReader(e.Data))
		b, _ := io.ReadAll(z)
		for _, m := range re.FindAllSubmatch(b, -1) {
			s := string(m[1])
			if strings.Contains(s, "sc-") || strings.Contains(s, "bz-") {
				t.Log(s)
			}
		}
		return nil
	})
}
