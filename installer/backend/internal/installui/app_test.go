package installui

import (
	"testing"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/gui"
	"image"
)

type fake struct{}

func (fake) Prepare(s func(string)) (*Medium, error) { s("x"); return &Medium{HasSetup: true}, nil }
func (fake) Connect() NetState                       { return NetState{Online: true} }
func (fake) InternetCheck() error                    { return nil }
func (fake) WifiScan() ([]WifiNet, error)            { return nil, nil }
func (fake) WifiConnect(string, string) error        { return nil }
func (fake) Check() *compat.Report                   { return compat.Evaluate(&compat.Scan{Arch: "x86_64"}) }
func (fake) Disks() []*disk.Disk {
	return []*disk.Disk{{Name: "sda", Bus: "sata", Size: 100 << 30, LogicalSec: 512, Table: disk.TableNone}}
}
func (fake) Install(Request, func(string)) error { return nil }
func (fake) VMScreen() image.Image               { return nil }
func (fake) UEFI() bool                          { return false }
func (fake) Reboot()                             {}
func (fake) PowerOff()                           {}
func (fake) Shell()                              {}

// Walks the wizard with the keyboard alone: Enter on each step.
func TestKeyboardWalk(t *testing.T) {
	c := gui.NewCanvas(1280, 800)
	th := Theme(1280, 800)
	u := gui.NewUI(c, th)
	a := New(fake{}, th)
	a.Start()
	time.Sleep(20 * time.Millisecond)
	frame := func(ev ...gui.Event) {
		a.Frame(u, ev)
		time.Sleep(20 * time.Millisecond)
		a.Frame(u, nil)
	}
	enter := gui.Event{Key: gui.KeyEnter}
	frame()
	for _, want := range []step{stNetwork, stCheck, stDisk, stDetails} { // Enter = Next
		frame(enter)
		if a.step != want {
			t.Fatalf("after Enter: step %d, want %d (medium %v, err %v)", a.step, want, a.medium, a.prepErr)
		}
	}
}
