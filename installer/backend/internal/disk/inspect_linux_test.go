//go:build linux

package disk

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Real NTFS and ext4 file systems, shrunk with the same code the installer
// uses. CI installs ntfs-3g and e2fsprogs; skipped where they're missing.
func TestShrinkRealFileSystems(t *testing.T) {
	for _, tc := range []struct{ fs, mkfs string }{{"ntfs", "mkntfs"}, {"ext4", "mkfs.ext4"}} {
		t.Run(tc.fs, func(t *testing.T) {
			if _, err := exec.LookPath(tc.mkfs); err != nil {
				t.Skip(tc.mkfs + " not installed")
			}
			img := filepath.Join(t.TempDir(), tc.fs+".img")
			f, _ := os.Create(img)
			f.Truncate(512 << 20)
			f.Close()
			args := []string{"-F", "-q", img}
			if tc.fs == "ntfs" {
				args = []string{"-F", "-f", "-q", img}
			}
			if out, err := exec.Command(tc.mkfs, args...).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v %s", tc.mkfs, err, out)
			}
			var min int64
			var why string
			if tc.fs == "ntfs" {
				min, why = ntfsMin(img)
			} else {
				min, why = ext4Min(img)
			}
			if min <= 0 || min > 300<<20 || why != "" {
				t.Fatalf("min %d, reason %q", min, why)
			}
			if err := ShrinkFS(img, tc.fs, 300<<20); err != nil {
				t.Fatal(err)
			}
			// The shrunk file system must report a size within 300 MB.
			var after int64
			if tc.fs == "ntfs" {
				out, _ := exec.Command("ntfsresize", "--info", "--no-progress-bar", img).CombinedOutput()
				if m := regexpSize.FindSubmatch(out); m != nil {
					after = atoi(string(m[1]))
				}
			} else {
				out, _ := exec.Command("dumpe2fs", "-h", img).CombinedOutput()
				bc, bs := regexpBlocks.FindSubmatch(out), regexpBS.FindSubmatch(out)
				if bc != nil && bs != nil {
					after = atoi(string(bc[1])) * atoi(string(bs[1]))
				}
			}
			if after == 0 || after > 300<<20 {
				t.Fatalf("size after shrink: %d", after)
			}
			t.Logf("%s: min %d MB, now %d MB", tc.fs, min>>20, after>>20)
		})
	}
}
