// 98reborn-ui-preview renders every installer screen with sample data to
// PNG files, so the graphical installer can be designed on any computer.
//
//	go run ./cmd/98reborn-ui-preview --out ../../lab/ui
package main

import (
	"errors"
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

type mock struct{ uefi, noWifi bool }

func (m mock) Prepare(status func(string)) (*installui.Medium, error) {
	status("Hardware drivers loaded")
	status("Found the 98Reborn USB stick")
	status("Windows 98 SE setup files: 77 cabinets")
	return &installui.Medium{Device: "sdb1", Version: "0.1.0-dev", HasSetup: true, Cabs: 77}, nil
}
func (m mock) Connect() installui.NetState {
	if m.noWifi {
		return installui.NetState{Wired: []string{"eth0"}}
	}
	return installui.NetState{Wired: []string{"eth0"}, Wireless: []string{"wlan0"}}
}
func (mock) InternetCheck() error {
	return errors.New("Name lookup (DNS) failed: lookup connectivitycheck.gstatic.com: i/o timeout. Address wlan0 192.168.43.20/24; gateway: default via 192.168.43.1 dev wlan0; DNS: 192.168.43.1.")
}
func (mock) WifiScan() ([]installui.WifiNet, error) {
	var nets []installui.WifiNet
	for i, n := range []string{"HomeNetwork", "Neighbors 5G", "Coffee Shop Guest", "xfinitywifi", "Office", "Office Guest", "Printer-42", "Apartment 3B", "Library", "Phone Hotspot", "Lab"} {
		nets = append(nets, installui.WifiNet{SSID: n, Signal: -45 - i*4, Secured: i != 2 && i != 3})
	}
	return nets, nil
}
func (mock) WifiConnect(string, string) error { return nil }

var _ = errors.New

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
		{Name: "sdc", Bus: "sata", Model: "Micron_1100_MTFDDAV256TBN", Size: 120 * GiB * 512, LogicalSec: 512, Table: disk.TableNone},
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
		// Pick the first network, type a password, connect: the "joined, no internet" case.
		a.Frame(u, []gui.Event{{Key: gui.KeyTab}})
		a.Frame(u, []gui.Event{{Key: gui.KeyTab}})
		a.Frame(u, []gui.Event{{Key: gui.KeySpace, Rune: ' '}})
		var pw []gui.Event
		for _, r := range "password1" {
			pw = append(pw, gui.Event{Rune: r})
		}
		a.Frame(u, pw)
		a.Frame(u, []gui.Event{{Key: gui.KeyEnter}})
		time.Sleep(80 * time.Millisecond)
		shot("2c-wifi-no-internet")
		// The rest of the flow on a PC without Wi-Fi, where the network
		// screen has just "Try again" and "Continue without internet".
		a = installui.New(mock{uefi: true, noWifi: true}, t)
		a.Start()
		time.Sleep(50 * time.Millisecond)
		a.Frame(u, nil)
		a.Frame(u, []gui.Event{enter})
		time.Sleep(80 * time.Millisecond)
		a.Frame(u, nil)
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
