//go:build linux

// 98reborn-installer runs as /init on the 98Reborn installer medium. This first
// stage brings the machine up (modules, boot medium, network), grades it with
// the same rules as the 98Reborn Tool and shows the report. Partitioning,
// unattended Setup and patching follow in the next stage.
//
// Kernel command line options:
//
//	98reborn.auto   no prompts; power off when done (automated tests)
//	98reborn.shell  drop to a shell before anything else
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/scan"
)

// out writes to every console: the kernel console (serial, for tests) and the
// screen. Lines starting "98REBORN:" are markers for the test harness.
var out io.Writer = os.Stdout

var cmdline = map[string]string{}

func main() {
	if os.Getpid() == 1 {
		setupSystem()
	}
	b, _ := os.ReadFile("/proc/cmdline")
	for _, f := range strings.Fields(string(b)) {
		k, v, _ := strings.Cut(f, "=")
		cmdline[k] = v
	}
	_, auto := cmdline["98reborn.auto"]
	openConsoles()
	marker("STAGE1")
	if _, ok := cmdline["98reborn.shell"]; ok {
		shell()
	}

	banner()
	step("Starting hardware")
	coldplug()
	ok("Hardware drivers loaded")

	step("Looking for the 98Reborn medium")
	med, err := findMedium(30 * time.Second)
	if err != nil {
		fail(err.Error())
		marker("MEDIUM none")
		finish(auto, 1)
	}
	ok(fmt.Sprintf("Found %s on %s (installer %s)", med.Label(), med.Dev, med.Base.Version))
	marker("MEDIUM " + med.Dev)
	if med.Setup == "" {
		warn("No Windows 98 SE setup files (98SE folder) on this medium. Build it again with the 98Reborn Tool.")
		marker("98SE missing")
	} else {
		ok(fmt.Sprintf("Windows 98 SE setup files: %d cabinets", med.Cabs))
		marker("98SE ok")
	}

	online := network(auto)

	step("Checking this PC")
	s, err := scan.Run()
	if err != nil {
		fail("Scan failed: " + err.Error())
		finish(auto, 1)
	}
	rep := compat.Evaluate(s)
	fmt.Fprintln(out)
	fmt.Fprint(out, rep.Text())
	fmt.Fprintln(out)
	if !online {
		warn("Offline: only drivers and fixes on this medium can be used, so the result above may be optimistic.")
	}
	marker("GRADE " + rep.Tier.String())
	if js, err := json.Marshal(rep); err == nil {
		os.WriteFile("/run/98reborn-report.json", js, 0o644)
	}

	disks := listDisks(med.Dev)
	step("Disks in this PC")
	if len(disks) == 0 {
		warn("No internal disk found. If the disk is NVMe or behind RAID/RST, see the report above.")
	}
	for _, d := range disks {
		fmt.Fprintf(out, "      %-10s %8s  %s\n", d.Name, human(d.Size), d.Model)
	}
	marker(fmt.Sprintf("DISKS %d", len(disks)))
	fmt.Fprintln(out)
	info("Installing from this screen comes in the next installer build.")
	marker("DONE")
	finish(auto, 0)
}

// ---------- system setup (PID 1) ----------

func setupSystem() {
	mount := func(src, dst, fs string, flags uintptr, data string) {
		os.MkdirAll(dst, 0o755)
		syscall.Mount(src, dst, fs, flags, data)
	}
	mount("proc", "/proc", "proc", 0, "")
	mount("sysfs", "/sys", "sysfs", 0, "")
	mount("devtmpfs", "/dev", "devtmpfs", 0, "mode=0755")
	mount("devpts", "/dev/pts", "devpts", 0, "")
	mount("tmpfs", "/run", "tmpfs", 0, "mode=0755")
	mount("tmpfs", "/tmp", "tmpfs", 0, "mode=1777")
	exec.Command("/bin/busybox", "--install", "-s").Run() // applet symlinks
	os.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin")
	os.Setenv("HOME", "/root")
	os.Setenv("TERM", "linux")
	exec.Command("hostname", "98reborn").Run()
	exec.Command("ip", "link", "set", "lo", "up").Run()
}

type multi []io.Writer

func (m multi) Write(p []byte) (int, error) {
	for _, w := range m {
		w.Write(p)
	}
	return len(p), nil
}

// openConsoles writes to the kernel console and, if that isn't the screen,
// to tty1 as well.
func openConsoles() {
	ws := multi{os.Stdout}
	b, _ := os.ReadFile("/sys/class/tty/console/active")
	act := strings.Fields(string(b))
	if len(act) > 0 && act[len(act)-1] != "tty1" && act[len(act)-1] != "tty0" {
		if f, err := os.OpenFile("/dev/tty1", os.O_WRONLY, 0); err == nil {
			ws = append(ws, f)
		}
	}
	out = ws
}

// ---------- screen output ----------

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	teal   = "\x1b[36m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
)

func banner() {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	fmt.Fprintln(out, teal+bold+"  ┌──────────────────────────────────────────────────────┐")
	fmt.Fprintln(out, "  │   98Reborn Installer                                 │")
	fmt.Fprintln(out, "  │   Windows 98 SE for modern PCs                       │")
	fmt.Fprintln(out, "  └──────────────────────────────────────────────────────┘"+reset)
	fmt.Fprintln(out)
}

func marker(s string) { fmt.Fprintln(os.Stdout, "98REBORN:"+s) }
func step(s string)   { fmt.Fprintln(out, bold+"  ▸ "+s+reset) }
func ok(s string)     { fmt.Fprintln(out, green+"    ✓ "+reset+s) }
func warn(s string)   { fmt.Fprintln(out, yellow+"    ! "+reset+s) }
func fail(s string)   { fmt.Fprintln(out, red+"    ✗ "+reset+s) }
func info(s string)   { fmt.Fprintln(out, "    "+s) }

func human(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.0f GB", float64(n)/(1<<30))
	default:
		return fmt.Sprintf("%d MB", n>>20)
	}
}

// ---------- modules ----------

// coldplug loads drivers for every device the kernel has found, repeating
// while new devices keep appearing (USB hubs, disks behind controllers).
func coldplug() {
	always := []string{"isofs", "vfat", "exfat", "nls_cp437", "nls_iso8859_1", "nls_utf8",
		"sd_mod", "sr_mod", "usb_storage", "uas", "hid_generic", "usbhid", "evdev", "af_packet"}
	exec.Command("modprobe", append([]string{"-q", "-a"}, always...)...).Run()
	seen := map[string]bool{}
	deadline := time.Now().Add(12 * time.Second)
	quiet := 0
	for time.Now().Before(deadline) && quiet < 3 {
		var fresh []string
		filepath.Walk("/sys/devices", func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.Name() == "modalias" && fi.Mode().IsRegular() {
				b, _ := os.ReadFile(p)
				a := strings.TrimSpace(string(b))
				if a != "" && !seen[a] {
					seen[a] = true
					fresh = append(fresh, a)
				}
			}
			return nil
		})
		if len(fresh) == 0 {
			quiet++
			time.Sleep(500 * time.Millisecond)
			continue
		}
		quiet = 0
		for i := 0; i < len(fresh); i += 64 {
			j := min(i+64, len(fresh))
			exec.Command("modprobe", append([]string{"-q", "-b", "-a"}, fresh[i:j]...)...).Run()
		}
		time.Sleep(300 * time.Millisecond)
	}
	exec.Command("mdev", "-s").Run() // device nodes for anything devtmpfs missed
}

// ---------- boot medium ----------

type baseInfo struct {
	Product string `json:"product"`
	Version string `json:"version"`
	Built   string `json:"built"`
}

type medium struct {
	Dev, Mount string
	Base       baseInfo
	Setup      string // path of the 98SE folder, "" if missing
	Cabs       int
}

func (m *medium) Label() string { return "98Reborn medium" }

func blockDevices() []string {
	ents, _ := os.ReadDir("/sys/class/block")
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "zram") {
			continue
		}
		out = append(out, n)
	}
	// Optical drives and partitions first: that's where media live.
	sort.SliceStable(out, func(i, j int) bool { return mediaRank(out[i]) < mediaRank(out[j]) })
	return out
}

func mediaRank(n string) int {
	if strings.HasPrefix(n, "sr") {
		return 0
	}
	if _, err := os.Stat("/sys/class/block/" + n + "/partition"); err == nil {
		return 1
	}
	return 2
}

func findMedium(timeout time.Duration) (*medium, error) {
	tried := map[string]bool{}
	deadline := time.Now().Add(timeout)
	for {
		for _, dev := range blockDevices() {
			if tried[dev] {
				continue
			}
			dir := "/run/media/" + dev
			os.MkdirAll(dir, 0o755)
			mounted := false
			for _, fs := range []string{"iso9660", "vfat", "exfat", "udf"} {
				if syscall.Mount("/dev/"+dev, dir, fs, syscall.MS_RDONLY, "") == nil {
					mounted = true
					break
				}
			}
			if !mounted {
				os.Remove(dir)
				continue // no media yet (empty drive) or unknown fs; maybe later
			}
			tried[dev] = true
			b, err := os.ReadFile(findCI(dir, "98reborn/base.json"))
			if err != nil {
				syscall.Unmount(dir, 0)
				continue
			}
			m := &medium{Dev: dev, Mount: dir}
			json.Unmarshal(b, &m.Base)
			if se := findCI(dir, "98SE"); se != "" {
				if setup := findCI(se, "SETUP.EXE"); setup != "" {
					m.Setup = se
					ents, _ := os.ReadDir(se)
					for _, e := range ents {
						if strings.EqualFold(filepath.Ext(e.Name()), ".cab") {
							m.Cabs++
						}
					}
				}
			}
			return m, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("couldn't find the 98Reborn medium. Is the USB stick in a USB 2/3 port?")
		}
		time.Sleep(time.Second)
	}
}

// findCI resolves a relative path case-insensitively below dir ("" if absent).
func findCI(dir, rel string) string {
	cur := dir
	for _, part := range strings.Split(rel, "/") {
		ents, err := os.ReadDir(cur)
		if err != nil {
			return ""
		}
		found := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return ""
		}
		cur = filepath.Join(cur, found)
	}
	return cur
}

// ---------- network ----------

func ifaces() (wired, wireless []string) {
	ents, _ := os.ReadDir("/sys/class/net")
	for _, e := range ents {
		n := e.Name()
		t, _ := os.ReadFile("/sys/class/net/" + n + "/type")
		if n == "lo" || strings.TrimSpace(string(t)) != "1" {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + n + "/wireless"); err == nil {
			wireless = append(wireless, n)
		} else {
			wired = append(wired, n)
		}
	}
	return
}

func internetUp() bool {
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Head("https://github.com/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// network gets the PC online over Ethernet. Offline is allowed, with a warning.
func network(auto bool) bool {
	for {
		step("Connecting to the internet")
		wired, wireless := ifaces()
		for _, n := range wired {
			exec.Command("ip", "link", "set", n, "up").Run()
			c := exec.Command("udhcpc", "-i", n, "-f", "-q", "-n", "-t", "4", "-T", "2",
				"-s", "/usr/share/98reborn/udhcpc.script")
			c.Run()
		}
		if internetUp() {
			ok("Online")
			marker("NET online")
			return true
		}
		switch {
		case len(wired) == 0 && len(wireless) > 0:
			warn("Only Wi-Fi found. Wi-Fi setup comes in a later installer build; use Ethernet (or a USB Ethernet adapter).")
		case len(wired) == 0:
			warn("No network adapter found.")
		default:
			warn("No internet on " + strings.Join(wired, ", ") + ". Check the cable.")
		}
		warn("Without internet, compatibility is limited: no driver downloads and no fixes newer than this medium.")
		warn("Windows 98 may not work properly on this PC.")
		if auto {
			marker("NET offline")
			return false
		}
		switch ask("[R]etry, [C]ontinue without internet, or open a [S]hell? ", "rcs") {
		case 'c':
			marker("NET offline")
			return false
		case 's':
			shell()
		}
	}
}

// ---------- disks ----------

type disk struct {
	Name, Model string
	Size        int64
}

func listDisks(skip string) []disk {
	var out []disk
	ents, _ := os.ReadDir("/sys/block")
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "sr") ||
			strings.HasPrefix(n, "zram") || strings.HasPrefix(skip, n) {
			continue
		}
		sz, _ := os.ReadFile("/sys/block/" + n + "/size")
		sectors, _ := strconv.ParseInt(strings.TrimSpace(string(sz)), 10, 64)
		if sectors == 0 {
			continue
		}
		model, _ := os.ReadFile("/sys/block/" + n + "/device/model")
		out = append(out, disk{Name: n, Model: strings.TrimSpace(string(model)), Size: sectors * 512})
	}
	return out
}

// ---------- interaction ----------

func ask(prompt, keys string) byte {
	tty, err := os.OpenFile("/dev/tty1", os.O_RDWR, 0)
	if err != nil {
		return keys[0]
	}
	defer tty.Close()
	r := bufio.NewReader(tty)
	for {
		fmt.Fprint(out, "    "+prompt)
		l, err := r.ReadString('\n')
		if err != nil {
			return keys[0]
		}
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && strings.IndexByte(keys, l[0]) >= 0 {
			return l[0]
		}
	}
}

func shell() {
	fmt.Fprintln(out, "    Shell: type 'exit' to return.")
	c := exec.Command("/bin/sh")
	if tty, err := os.OpenFile("/dev/tty1", os.O_RDWR, 0); err == nil {
		c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
		defer tty.Close()
	} else {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	c.Run()
}

func finish(auto bool, code int) {
	if os.Getpid() != 1 {
		os.Exit(code)
	}
	if auto {
		syscall.Sync()
		syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	}
	for {
		switch ask("[S]hell, [R]eboot or [P]ower off? ", "srp") {
		case 's':
			shell()
		case 'r':
			syscall.Sync()
			syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
		case 'p':
			syscall.Sync()
			syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
		}
	}
}
