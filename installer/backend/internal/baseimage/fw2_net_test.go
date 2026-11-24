package baseimage

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/apk"
)

// APK_NET=1 go test ./internal/baseimage -run IntelLayout -v
func TestIntelLayout(t *testing.T) {
	if os.Getenv("APK_NET") == "" {
		t.Skip("set APK_NET=1")
	}
	cache, _ := os.UserCacheDir()
	c := &apk.Client{Mirror: "https://dl-cdn.alpinelinux.org/alpine", Branch: "v3.24", Arch: "x86_64", Cache: filepath.Join(cache, "98reborn", "apk")}
	for _, r := range []string{"main", "community"} {
		c.LoadIndex(r)
	}
	raw, _ := c.Fetch(c.Lookup("linux-firmware-intel"))
	top, sub := map[string]bool{}, map[string]bool{}
	apk.Unpack(raw, func(e apk.Entry) error {
		rel, ok := strings.CutPrefix(e.Path, "lib/firmware/")
		if !ok {
			return nil
		}
		switch {
		case strings.HasPrefix(rel, "iwlwifi-"):
			top[rel] = true
		case strings.HasPrefix(rel, "intel/iwlwifi/"):
			sub[path.Base(rel)] = true
		}
		return nil
	})
	var missing []string
	for b := range sub {
		if !top[b] {
			missing = append(missing, b)
		}
	}
	sort.Strings(missing)
	t.Logf("top-level %d, intel/iwlwifi %d, without top-level twin %d", len(top), len(sub), len(missing))
	for _, m := range missing {
		if strings.Contains(m, "sc-") || strings.Contains(m, "pnvm") {
			t.Log("  ", m)
		}
	}
}
