//go:build linux

package main

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/gui"
	"github.com/win9x-modern/w9xm/internal/install"
	"github.com/win9x-modern/w9xm/internal/installui"
	"github.com/win9x-modern/w9xm/internal/lab"
	"github.com/win9x-modern/w9xm/internal/scan"
)

const workDir = "/tmp/98reborn"

// linuxSystem is the real machine behind the graphical installer.
type linuxSystem struct {
	med    *medium
	report *compat.Report
	disks  []*disk.Disk
}

func (s *linuxSystem) Prepare(status func(string)) (*installui.Medium, error) {
	coldplug() // already done before the screen opened; this picks up late devices
	status("Hardware drivers loaded")
	med, err := findMedium(30 * time.Second)
	if err != nil {
		marker("MEDIUM none")
		return nil, err
	}
	s.med = med
	marker("MEDIUM " + med.Dev)
	what := "USB stick"
	if strings.HasPrefix(med.Dev, "sr") {
		what = "disc"
	}
	status("Found the 98Reborn " + what + " (" + med.Dev + ")")
	m := &installui.Medium{Device: med.Dev, Version: med.Base.Version, HasSetup: med.Setup != "", Cabs: med.Cabs}
	if m.HasSetup {
		status(fmt.Sprintf("Windows 98 SE setup files: %d cabinets", med.Cabs))
		marker("98SE ok")
	} else {
		marker("98SE missing")
	}
	return m, nil
}

func (s *linuxSystem) Connect() installui.NetState {
	wired, wireless := ifaces()
	for _, n := range wired {
		exec.Command("ip", "link", "set", n, "up").Run()
		exec.Command("udhcpc", "-i", n, "-f", "-q", "-n", "-t", "4", "-T", "2", "-s", "/usr/share/98reborn/udhcpc.script").Run()
	}
	st := installui.NetState{Online: internetUp(), Wired: wired, Wireless: wireless}
	marker(map[bool]string{true: "NET online", false: "NET offline"}[st.Online])
	return st
}

func (s *linuxSystem) Check() *compat.Report {
	sc, err := scan.Run()
	if err != nil {
		sc = &compat.Scan{Arch: "x86_64"}
	}
	s.report = compat.Evaluate(sc)
	marker("GRADE " + s.report.Tier.String())
	return s.report
}

func (s *linuxSystem) Disks() []*disk.Disk {
	dev := ""
	if s.med != nil {
		dev = s.med.Dev
	}
	s.disks = readDisks(dev)
	return s.disks
}

func (s *linuxSystem) Install(r installui.Request, stage func(string)) error {
	if s.med == nil || s.med.Setup == "" {
		return errors.New("no Windows 98 SE setup files on this medium")
	}
	f, err := os.OpenFile(r.Choice.Disk.Path, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	logf, _ := os.Create("/run/98reborn-install.log")
	defer logf.Close()
	var facts = s.report.Scan.Facts()
	marker("INSTALL start")
	err = install.Run(install.Options{
		Device: f, Disk: r.Choice.Disk, Choice: r.Choice, MakeDefault: r.MakeDefault,
		Source: lab.DirSource{Dir: s.med.Setup, Floppy: findCI(s.med.Mount, "98reborn/cdboot.img")},
		Key:    r.Key, Answers: r.Answers, AcceptEULA: true,
		Edition: r.Edition, ModulesDir: findCI(s.med.Mount, "98reborn/modules"), EditionsDir: findCI(s.med.Mount, "98reborn/editions"),
		BootDir: findCI(s.med.Mount, "98reborn/boot"), RegisterBoot: r.RegisterBoot,
		ACPI:  s.UEFI(), // UEFI PCs start Windows 98 through ReBIOS, which gives it ACPI
		Facts: facts, WorkDir: workDir, Log: logf,
		Stage: func(st string) { marker("INSTALL " + st); stage(st) },
	})
	if err != nil {
		marker("INSTALL failed")
		return err
	}
	marker("INSTALL done")
	return nil
}

// VMScreen returns the newest screenshot the Setup VM harness saved.
func (s *linuxSystem) VMScreen() image.Image {
	var newest string
	var t time.Time
	for _, p := range []string{workDir + "/install/screen.png", workDir + "/install/.probe.png"} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			newest, t = p, fi.ModTime()
		}
	}
	if newest == "" {
		return nil
	}
	f, err := os.Open(newest)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

func (s *linuxSystem) UEFI() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

func (s *linuxSystem) Reboot() {
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
}

func (s *linuxSystem) PowerOff() {
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}

func (s *linuxSystem) Shell() {}

// runGUI runs the graphical installer until the user leaves it for a shell.
// It returns false if there is no usable framebuffer.
func openScreen() *gui.Screen {
	for i := 0; i < 50; i++ { // a display driver may be swapping framebuffers
		if scr, err := gui.OpenScreen(); err == nil {
			if scr.W >= 800 && scr.H >= 600 {
				return scr
			}
			scr.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func runGUI() bool {
	// Load drivers first: a display driver taking over from the firmware
	// framebuffer replaces /dev/fb0.
	coldplug()
	scr := openScreen()
	if scr == nil {
		return false
	}
	marker("GUI " + scr.Info)
	c := gui.NewCanvas(scr.W, scr.H)
	t := installui.Theme(scr.W, scr.H)
	u := gui.NewUI(c, t)
	app := installui.New(&linuxSystem{}, t)
	app.Start()
	in := gui.OpenInput(scr.W, scr.H)
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	var ev []gui.Event
	for !app.Quit() {
		app.Frame(u, ev)
		scr.Show(c)
		if scr.Err != nil { // the framebuffer went away: reopen it
			scr.Close()
			if scr = openScreen(); scr == nil {
				return false
			}
			marker("GUI reopened " + scr.Info)
			if scr.W != c.W || scr.H != c.H {
				c = gui.NewCanvas(scr.W, scr.H)
				t = installui.Theme(scr.W, scr.H)
				u, app.T = gui.NewUI(c, t), t
			}
			continue
		}
		ev = ev[:0]
		select {
		case e := <-in.C:
			ev = append(ev, e)
		drain:
			for {
				select {
				case e := <-in.C:
					ev = append(ev, e)
				default:
					break drain
				}
			}
		case <-tick.C:
		}
	}
	scr.Close()
	return true
}
