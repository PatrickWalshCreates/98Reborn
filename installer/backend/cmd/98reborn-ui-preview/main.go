// 98reborn-ui-preview renders every installer screen with sample data to
// PNG files, so the graphical installer can be designed on any computer.
//
//	go run ./cmd/98reborn-ui-preview --out ../../lab/ui
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/gui"
	"github.com/win9x-modern/w9xm/internal/installui"
)

const GiB = int64(1<<30) / 512

type mock struct{ uefi bool }

func (m mock) Prepare(status func(string)) (*installui.Medium, error) {
	status("Hardware drivers loaded")
	status("Found the 98Reborn USB stick")
	status("Windows 98 SE setup files: 77 cabinets")
	return &installui.Medium{Device: "sdb1", Version: "0.1.0-dev", HasSetup: true, Cabs: 77}, nil
}
func (mock) Connect() installui.NetState {
	return installui.NetState{Wired: []string{"eth0"}, Wireless: []string{"wlan0"}}
}
func (mock) Online() bool { return true }
func (mock) WifiScan() ([]installui.WifiNet, error) {
	return []installui.WifiNet{{SSID: "HomeNetwork", Signal: -48, Secured: true}, {SSID: "Neighbors 5G", Signal: -67, Secured: true},
		{SSID: "Coffee Shop Guest", Signal: -78}}, nil
}
func (mock) WifiConnect(string, string) error { return nil }
func (mock) Check() *compat.Report {
	return compat.Evaluate(&compat.Scan{OS: "98Reborn Installer", Arch: "x86_64", CPU: "Intel Core i5-4590 @ 3.30GHz", CPUMHz: 3300, RAMMB: 8192,
		Firmware: "uefi", SecureBoot: "off", Virtualization: "on", IOMMU: "on",
		PCI: []compat.Device{{ID: "8086:0412", Class: "030000"}, {ID: "8086:8c02", Class: "010601"}, {ID: "10ec:8168", Class: "020000"},
			{ID: "8086:8c20", Class: "040300"}, {ID: "8086:8c31", Class: "0c0330"}, {ID: "8086:8c26", Class: "0c0320"}},
		Disks: []compat.Disk{{Name: "sda", Bus: "sata", SizeGB: 500}, {Name: "sdc", Bus: "sata", SizeGB: 120}}})
}
func (mock) Disks() []*disk.Disk {
	win := &disk.Part{Num: 1, Start: 2048, Sectors: 100 * GiB, FS: "ntfs", OS: "Windows 10/11", Active: true, MinBytes: 45 << 30}
	return []*disk.Disk{
		{Name: "sda", Bus: "sata", Model: "WDC WD5000AAKX", Size: 500 * GiB * 512, LogicalSec: 512, Table: disk.TableMBR, Parts: []*disk.Part{win}},
		{Name: "sdc", Bus: "sata", Model: "Samsung SSD 850", Size: 120 * GiB * 512, LogicalSec: 512, Table: disk.TableNone},
		{Name: "nvme0n1", Bus: "nvme", Model: "Samsung 970 EVO", Size: 1000 * GiB * 512, LogicalSec: 512, Table: disk.TableGPT},
	}
}
func (mock) Install(_ installui.Request, stage func(string)) error {
	stage("setup")
	select {}
}
func (mock) VMScreen() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			c := color.RGBA{0, 0, 168, 255}
			if x > 120 && x < 520 && y > 100 && y < 380 {
				c = color.RGBA{192, 192, 192, 255}
			}
			if x > 120 && x < 520 && y > 100 && y < 120 {
				c = color.RGBA{0, 0, 128, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}
func (m mock) UEFI() bool { return m.uefi }
func (mock) Reboot()      {}
func (mock) PowerOff()    {}
func (mock) Shell()       {}

func main() {
	out := flag.String("out", "ui-preview", "output directory")
	flag.Parse()
	os.MkdirAll(*out, 0o755)
	for _, sz := range [][2]int{{1280, 800}, {1920, 1080}, {1024, 768}} {
		c := gui.NewCanvas(sz[0], sz[1])
		t := installui.Theme(sz[0], sz[1])
		u := gui.NewUI(c, t)
		a := installui.New(mock{uefi: true}, t)
		a.Start()
		time.Sleep(50 * time.Millisecond)
		shot := func(name string, ev ...gui.Event) {
			a.Frame(u, ev)
			time.Sleep(60 * time.Millisecond) // let background steps finish
			a.Frame(u, nil)
			p := filepath.Join(*out, fmt.Sprintf("%dx%d-%s.png", sz[0], sz[1], name))
			if err := c.SavePNG(p); err != nil {
				panic(err)
			}
		}
		enter := gui.Event{Key: gui.KeyEnter}
		installui.Splash(c, t, 3, "Starting the installer")
		c.SavePNG(filepath.Join(*out, fmt.Sprintf("%dx%d-0-splash.png", sz[0], sz[1])))
		shot("1-welcome")
		shot("2-network", enter)
		a.Frame(u, nil)
		// Pick the first Wi-Fi network with the mouse and type a password.
		time.Sleep(80 * time.Millisecond)
		a.Frame(u, nil)
		shot("2b-wifi-list")
		shot("3-check", gui.Event{Key: gui.KeyTab}, gui.Event{Key: gui.KeyTab}, enter) // "Continue without internet"
		shot("4-disk", enter)
		shot("5-details", enter)
		var typed []gui.Event
		for _, r := range "b4a8x2m3k9q7p1z6t5r8w3y2c" {
			typed = append(typed, gui.Event{Rune: r})
		}
		a.Frame(u, nil)
		for i := 0; i < 4; i++ { // name -> product key, one key press per frame like a person
			a.Frame(u, []gui.Event{{Key: gui.KeyTab}})
		}
		a.Frame(u, typed)
		a.Frame(u, []gui.Event{{Key: gui.KeyTab}})
		shot("5b-details-filled", gui.Event{Key: gui.KeySpace})
		shot("6-confirm", enter)
		shot("7-install", enter)
	}
	fmt.Println("wrote", *out)
}
