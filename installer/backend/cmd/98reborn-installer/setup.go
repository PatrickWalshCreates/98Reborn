//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/install"
	"github.com/win9x-modern/w9xm/internal/lab"
)

// readDisks builds the disk list from sysfs and inspects each disk read-only.
func readDisks(mediumDev string) []*disk.Disk {
	var out []*disk.Disk
	ents, _ := os.ReadDir("/sys/block")
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "zram") || strings.HasPrefix(n, "sr") {
			continue
		}
		rd := func(f string) string {
			b, _ := os.ReadFile("/sys/block/" + n + "/" + f)
			return strings.TrimSpace(string(b))
		}
		sectors, _ := strconv.ParseInt(rd("size"), 10, 64)
		if sectors == 0 {
			continue
		}
		lsec, _ := strconv.Atoi(rd("queue/logical_block_size"))
		d := &disk.Disk{Name: n, Path: "/dev/" + n, Size: sectors * 512, LogicalSec: lsec,
			Model: strings.TrimSpace(rd("device/model")), Removable: rd("removable") == "1",
			IsMedium: strings.HasPrefix(mediumDev, n)}
		link, _ := filepath.EvalSymlinks("/sys/block/" + n)
		switch {
		case strings.HasPrefix(n, "nvme"):
			d.Bus = "nvme"
		case strings.Contains(link, "/usb"):
			d.Bus = "usb"
		case strings.HasPrefix(n, "vd") || strings.Contains(link, "virtio"):
			d.Bus = "virtio"
		case strings.Contains(link, "/ata"):
			d.Bus = "sata"
		default:
			d.Bus = "disk"
		}
		if f, err := os.Open(d.Path); err == nil {
			if err := disk.Inspect(f, d); err != nil {
				d.Table = "unreadable"
			}
			f.Close()
		}
		out = append(out, d)
	}
	return out
}

var stdin *bufio.Reader

func input(prompt, def string) string {
	if stdin == nil {
		tty, err := os.OpenFile("/dev/tty1", os.O_RDWR, 0)
		if err != nil {
			return def
		}
		stdin = bufio.NewReader(tty)
	}
	if def != "" {
		fmt.Fprintf(out, "    %s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(out, "    %s: ", prompt)
	}
	l, err := stdin.ReadString('\n')
	if err != nil {
		return def
	}
	if l = strings.TrimSpace(l); l == "" {
		return def
	}
	return l
}

var timeZones = []struct{ label, setup string }{
	{"US Pacific", "Pacific"}, {"US Mountain", "Mountain"}, {"US Central", "Central"}, {"US Eastern", "Eastern"},
	{"UK / Ireland (GMT)", "GMT"}, {"Central Europe", "W. Europe"}, {"Eastern Europe", "E. Europe"},
	{"Australia Eastern", "AUS Eastern"}, {"Japan", "Tokyo"},
}

var keyFormat = regexp.MustCompile(`^[A-Z0-9]{5}(-[A-Z0-9]{5}){4}$`)

// installFlow runs the disk, edition and personal-settings screens, then the install.
func installFlow(med *medium, rep *compat.Report, auto bool) {
	step("Where should Windows 98 go?")
	disks := readDisks(med.Dev)
	for _, d := range disks {
		if d.IsMedium {
			continue
		}
		fmt.Fprintf(out, "\n    %s%s%s (%s, %s)\n", bold, d.Name, reset, strings.ToUpper(d.Bus), d.Model)
		fmt.Fprint(out, indent(disk.Layout(d, nil), "    "))
	}
	_, allowNVMe := cmdline["98reborn.allow-nvme"]
	opts := disk.Plan(disks, disk.Requirements{AllowNVMe: allowNVMe})
	fmt.Fprintln(out)
	var avail []int
	for i, o := range opts {
		if !o.Available {
			fmt.Fprintf(out, "      -  %s\n         Not possible: %s.\n", o.Title, o.Reason)
			continue
		}
		avail = append(avail, i)
		tag := ""
		if o.Recommended {
			tag = green + "  (recommended)" + reset
		}
		fmt.Fprintf(out, "     %2d. %s%s\n         %s\n", len(avail), o.Title, tag, o.Detail)
	}
	marker(fmt.Sprintf("PLAN %d", len(avail)))
	if len(avail) == 0 {
		warn("There's nowhere Windows 98 can be installed on this PC yet (see the reasons above).")
		return
	}
	if auto {
		return
	}
	if rep.Tier == compat.Dev || rep.Tier == compat.None {
		warn("This PC's grade is " + rep.Tier.String() + ". Windows 98 will probably not start on it yet.")
		if ask("Install anyway? [y/N] ", "ny") != 'y' {
			return
		}
	}
	n, _ := strconv.Atoi(input("Choose an option number (or 0 to cancel)", "0"))
	if n < 1 || n > len(avail) {
		return
	}
	choice := opts[avail[n-1]]

	// Edition.
	step("Edition")
	fmt.Fprintln(out, "      1. Core: for the software you still depend on")
	fmt.Fprintln(out, "      2. Gaming: for the games you grew up with")
	edition := "core"
	if input("Choose 1 or 2", "1") == "2" {
		edition = "gaming"
	}

	// Personal settings.
	step("About you")
	ans := lab.Answers{Workgroup: "WORKGROUP"}
	ans.Name = input("Your name", "98Reborn User")
	ans.Org = input("Organization (optional)", "")
	ans.Computer = strings.ToUpper(input("Computer name", "RETRO98"))
	for i, z := range timeZones {
		fmt.Fprintf(out, "      %d. %s\n", i+1, z.label)
	}
	tz, _ := strconv.Atoi(input("Time zone", "1"))
	ans.TimeZone = timeZones[max(0, min(len(timeZones)-1, tz-1))].setup

	var key string
	for {
		key = strings.ToUpper(strings.ReplaceAll(input("Windows 98 SE product key (XXXXX-XXXXX-XXXXX-XXXXX-XXXXX)", ""), " ", ""))
		if keyFormat.MatchString(key) {
			break
		}
		warn("That doesn't look like a Windows 98 product key: 25 letters and digits in five groups.")
	}
	fmt.Fprintln(out)
	info("Windows 98 SE is Microsoft software, licensed under the agreement that came with your copy.")
	if strings.ToLower(input("Type ACCEPT to accept that license agreement", "")) != "accept" {
		return
	}

	makeDefault := true
	others := false
	for _, p := range choice.Disk.Parts {
		if p.OS != "" && choice.Kind != disk.KindWholeDisk {
			others = true
		}
	}
	if others {
		info("This disk already starts another system. A boot menu for both comes with 98Reborn Boot;")
		info("until then, the system that starts is the one marked active.")
		makeDefault = ask("Start Windows 98 by default after installing? [y/N] ", "ny") == 'y'
	}

	// Confirm.
	step("Ready to install")
	fmt.Fprintf(out, "    Disk %s now:\n%s", choice.Disk.Name, indent(disk.Layout(choice.Disk, nil), "    "))
	fmt.Fprintf(out, "    After installing:\n%s", indent(disk.Layout(choice.Disk, &choice), "    "))
	if choice.Kind == disk.KindWholeDisk && len(choice.Erases) > 0 {
		warn(red + "Everything on " + choice.Disk.Name + " will be erased." + reset)
	}
	if input("Type the disk name ("+choice.Disk.Name+") to start installing", "") != choice.Disk.Name {
		info("Cancelled. Nothing was changed.")
		return
	}

	work := "/tmp/98reborn"
	f, err := os.OpenFile(choice.Disk.Path, os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		fail("Can't open the disk: " + err.Error())
		return
	}
	defer f.Close()
	stageNames := map[string]string{
		"setup":  "Running Windows 98 Setup (about 20-40 minutes; your disk isn't touched yet)",
		"patch":  "Applying the " + edition + " edition",
		"shrink": "Shrinking the existing partition",
		"copy":   "Writing Windows 98 to the disk",
		"finish": "Writing the partition table",
	}
	logf, _ := os.Create("/run/98reborn-install.log")
	start := time.Now()
	err = install.Run(install.Options{
		Device: f, Disk: choice.Disk, Choice: choice, MakeDefault: makeDefault,
		Source: lab.DirSource{Dir: med.Setup, Floppy: findCI(med.Mount, "98reborn/cdboot.img")},
		Key:    key, Answers: ans, AcceptEULA: true,
		Edition: edition, ModulesDir: findCI(med.Mount, "98reborn/modules"), EditionsDir: findCI(med.Mount, "98reborn/editions"),
		Facts: rep.Scan.Facts(), WorkDir: work, Log: logf,
		Stage: func(s string) {
			step(stageNames[s])
			marker("INSTALL " + s)
		},
	})
	logf.Close()
	if err != nil {
		fail("The install stopped: " + err.Error())
		info("Details: /run/98reborn-install.log (open a shell from the menu to read it).")
		marker("INSTALL failed")
		return
	}
	ok(fmt.Sprintf("Windows 98 is installed (%s).", time.Since(start).Round(time.Minute)))
	if js, err := json.MarshalIndent(map[string]any{"disk": choice.Disk.Name, "start": choice.Start, "sectors": choice.Sectors, "edition": edition}, "", "  "); err == nil {
		os.WriteFile("/run/98reborn-installed.json", js, 0o644)
	}
	if rep.Scan.Firmware == "uefi" {
		warn("This PC started the installer in UEFI mode. To start Windows 98, choose the disk's Legacy/CSM entry in the boot menu (98Reborn Boot will do this for you later).")
	}
	info("Remove the USB stick, then restart.")
	marker("INSTALL done")
}

func indent(s, pre string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(pre + l + "\n")
	}
	return b.String()
}
