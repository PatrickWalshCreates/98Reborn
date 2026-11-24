package baseimage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/win9x-modern/w9xm/internal/apk"
)

// APK_NET=1 go test ./internal/baseimage -run FirmwareSizes -v
func TestFirmwareSizes(t *testing.T) {
	if os.Getenv("APK_NET") == "" {
		t.Skip("set APK_NET=1")
	}
	cache, _ := os.UserCacheDir()
	c := &apk.Client{Mirror: "https://dl-cdn.alpinelinux.org/alpine", Branch: "v3.24", Arch: "x86_64", Cache: filepath.Join(cache, "98reborn", "apk")}
	for _, r := range []string{"main", "community"} {
		if err := c.LoadIndex(r); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := wifiFirmware(c)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	total := 0
	for p, b := range fw {
		parts := strings.SplitN(p, "/", 3)
		k := parts[0]
		if len(parts) > 2 {
			k += "/" + parts[1]
		}
		if strings.HasPrefix(p, "iwlwifi-") {
			k = "iwlwifi (top)"
		}
		by[k] += len(b)
		total += len(b)
	}
	var ks []string
	for k := range by {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return by[ks[i]] > by[ks[j]] })
	for _, k := range ks[:min(15, len(ks))] {
		t.Logf("%-30s %4d MB", k, by[k]>>20)
	}
	t.Logf("total %d files, %d MB", len(fw), total>>20)
}
