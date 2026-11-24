package compose

import (
	"io"
	"os"
	"path"
	"testing"
	"time"

	"github.com/win9x-modern/w9xm/internal/fat"
	"github.com/win9x-modern/w9xm/internal/iso9660"
)

// TestRufusStyleUSB copies an installer ISO's files onto a FAT32 USB image the
// way Rufus does in ISO mode, for booting in QEMU as a USB stick (UEFI).
// USB_ISO=installer.iso USB_OUT=stick.img go test ./internal/compose -run Rufus
func TestRufusStyleUSB(t *testing.T) {
	src, out := os.Getenv("USB_ISO"), os.Getenv("USB_OUT")
	if src == "" || out == "" {
		t.Skip("set USB_ISO and USB_OUT")
	}
	im, err := iso9660.OpenFile(src)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	const size = 1 << 30
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Truncate(size)
	const start = 2048
	if err := fat.WriteMBR(f, []fat.Partition{{Bootable: true, Type: 0x0C, StartLBA: start, Sectors: size/512 - start}}); err != nil {
		t.Fatal(err)
	}
	if err := fat.Format32(f, start*512, size-start*512, "98REBORN"); err != nil {
		t.Fatal(err)
	}
	fs, err := fat.Open(f, start*512)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	err = im.Walk(func(e iso9660.Entry) error {
		if e.IsDir {
			return nil
		}
		if d := path.Dir(e.Path); d != "." {
			if err := fs.MkdirAll(d); err != nil {
				return err
			}
		}
		sr, err := im.OpenEntry(e)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(sr)
		if err != nil {
			return err
		}
		n++
		return fs.WriteFile(e.Path, b, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("copied %d files", n)
}
