//go:build linux

package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/installui"
)

// apIface is the simulated access point's interface in test mode (hidden
// from the network list).
var apIface string

// wifiPrepare points the kernel at the Wi-Fi firmware on the medium and
// re-probes Wi-Fi cards that came up without it.
func wifiPrepare(mount string) {
	if fw := findCI(mount, "98reborn/firmware"); fw != "" {
		os.WriteFile("/sys/module/firmware_class/parameters/path", []byte(fw), 0o644)
	}
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range devs {
		cls, _ := os.ReadFile(d + "/class")
		if !strings.HasPrefix(strings.TrimSpace(string(cls)), "0x0280") {
			continue // not a Wi-Fi controller
		}
		if nets, _ := filepath.Glob(d + "/net/*"); len(nets) > 0 {
			continue // already working
		}
		addr := filepath.Base(d)
		if _, err := os.Stat(d + "/driver"); err == nil {
			os.WriteFile(d+"/driver/unbind", []byte(addr), 0o200)
		}
		os.WriteFile("/sys/bus/pci/drivers_probe", []byte(addr), 0o200)
	}
	if _, ok := cmdline["98reborn.testap"]; ok {
		startTestAP()
	}
	// Give the drivers a moment to register their interfaces.
	for i := 0; i < 20; i++ {
		if _, w := ifaces(); len(w) > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	for _, w := range wirelessIfaces() {
		exec.Command("ip", "link", "set", w, "up").Run()
	}
}

func wirelessIfaces() []string {
	_, w := ifaces()
	var out []string
	for _, n := range w {
		if n != apIface {
			out = append(out, n)
		}
	}
	return out
}

// startTestAP creates two simulated radios and runs a WPA2 access point
// ("98Reborn Test", password "retro1998") with DHCP on one of them.
func startTestAP() {
	exec.Command("modprobe", "mac80211_hwsim", "radios=2").Run()
	var w []string
	for i := 0; i < 20 && len(w) < 2; i++ {
		time.Sleep(250 * time.Millisecond)
		_, w = ifaces()
	}
	if len(w) < 2 {
		marker("TESTAP no radios")
		return
	}
	sort.Strings(w)
	apIface = w[len(w)-1]
	conf := fmt.Sprintf("interface=%s\ndriver=nl80211\nssid=98Reborn Test\nhw_mode=g\nchannel=6\nwpa=2\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\nwpa_passphrase=retro1998\n", apIface)
	os.WriteFile("/run/hostapd.conf", []byte(conf), 0o600)
	exec.Command("ip", "link", "set", apIface, "up").Run()
	exec.Command("ip", "addr", "add", "192.168.98.1/24", "dev", apIface).Run()
	if out, err := run(15*time.Second, "hostapd", "-B", "/run/hostapd.conf"); err != nil {
		marker("TESTAP hostapd failed: " + strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", " | "))
	}
	dconf := fmt.Sprintf("start 192.168.98.10\nend 192.168.98.50\ninterface %s\nopt router 192.168.98.1\nopt subnet 255.255.255.0\nlease_file /run/udhcpd.leases\n", apIface)
	os.WriteFile("/run/udhcpd.conf", []byte(dconf), 0o644)
	os.WriteFile("/run/udhcpd.leases", nil, 0o644)
	exec.Command("udhcpd", "/run/udhcpd.conf").Run()
	marker("TESTAP " + apIface)
}

// wifiScan lists networks in range, strongest first, one entry per name.
func wifiScan() ([]installui.WifiNet, error) {
	ws := wirelessIfaces()
	if len(ws) == 0 {
		return nil, errors.New("no Wi-Fi adapter is working")
	}
	var out []byte
	var err error
	for i := 0; i < 5; i++ { // "Device or resource busy" right after start-up
		out, err = run(20*time.Second, "iw", "dev", ws[0], "scan")
		if err == nil {
			break
		}
		marker(fmt.Sprintf("WIFI scan try %d: %v %s", i+1, err, firstLine(string(out))))
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("Wi-Fi scan failed: %s", strings.TrimSpace(string(out)))
	}
	nets := parseScan(string(out))
	marker(fmt.Sprintf("WIFI scan %d networks", len(nets)))
	return nets, nil
}

func parseScan(s string) []installui.WifiNet {
	best := map[string]installui.WifiNet{}
	var cur *installui.WifiNet
	flush := func() {
		if cur != nil && cur.SSID != "" {
			if b, ok := best[cur.SSID]; !ok || cur.Signal > b.Signal {
				best[cur.SSID] = *cur
			}
		}
	}
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "BSS "):
			flush()
			cur = &installui.WifiNet{Signal: -100}
		case cur == nil:
		case strings.HasPrefix(t, "signal:"):
			f := strings.Fields(strings.TrimPrefix(t, "signal:"))
			if len(f) > 0 {
				v, _ := strconv.ParseFloat(f[0], 64)
				cur.Signal = int(v)
			}
		case strings.HasPrefix(t, "SSID: "):
			cur.SSID = strings.TrimPrefix(t, "SSID: ")
		case strings.HasPrefix(t, "RSN:"), strings.HasPrefix(t, "WPA:"):
			cur.Secured = true
		case strings.Contains(t, "Privacy"):
			cur.Secured = true
		}
	}
	flush()
	var nets []installui.WifiNet
	for _, n := range best {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Signal > nets[j].Signal })
	return nets
}

// wifiConnect joins a network and gets an address. The pre-shared key is
// derived here (PBKDF2, as WPA2 defines), so no password ever needs quoting
// in wpa_supplicant's configuration.
func wifiConnect(ssid, pass string) error {
	ws := wirelessIfaces()
	if len(ws) == 0 {
		return errors.New("no Wi-Fi adapter is working")
	}
	w := ws[0]
	exec.Command("killall", "wpa_supplicant").Run()
	time.Sleep(300 * time.Millisecond)
	var net strings.Builder
	fmt.Fprintf(&net, "network={\n\tssid=%s\n\tscan_ssid=1\n", hex.EncodeToString([]byte(ssid)))
	switch {
	case pass == "":
		net.WriteString("\tkey_mgmt=NONE\n")
	case len(pass) < 8 || len(pass) > 63:
		return errors.New("Wi-Fi passwords are 8 to 63 characters")
	default:
		key, err := pbkdf2.Key(sha1.New, pass, []byte(ssid), 4096, 32)
		if err != nil {
			return err
		}
		fmt.Fprintf(&net, "\tkey_mgmt=WPA-PSK WPA-PSK-SHA256 SAE\n\tpsk=%s\n\tieee80211w=1\n", hex.EncodeToString(key))
		if !strings.ContainsAny(pass, "\"\\\n") {
			fmt.Fprintf(&net, "\tsae_password=\"%s\"\n", pass) // WPA3 needs the password itself
		}
	}
	net.WriteString("}\n")
	conf := "ctrl_interface=/run/wpa_supplicant\nupdate_config=0\n" + net.String()
	if err := os.WriteFile("/run/wpa.conf", []byte(conf), 0o600); err != nil {
		return err
	}
	if out, err := run(15*time.Second, "wpa_supplicant", "-B", "-i", w, "-c", "/run/wpa.conf", "-D", "nl80211"); err != nil {
		return fmt.Errorf("Couldn't start Wi-Fi: %s", strings.TrimSpace(string(out)))
	}
	ok := false
	for i := 0; i < 40; i++ {
		out, _ := run(5*time.Second, "wpa_cli", "-p", "/run/wpa_supplicant", "-i", w, "status")
		if strings.Contains(string(out), "wpa_state=COMPLETED") {
			ok = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ok {
		exec.Command("killall", "wpa_supplicant").Run()
		if pass != "" {
			return errors.New("Couldn't join the network. Check the password.")
		}
		return errors.New("Couldn't join the network.")
	}
	if _, err := run(30*time.Second, "udhcpc", "-i", w, "-f", "-q", "-n", "-t", "6", "-T", "2", "-s", "/usr/share/98reborn/udhcpc.script"); err != nil {
		return errors.New("Joined the network, but it gave this PC no address.")
	}
	marker("WIFI joined " + w)
	return nil
}

// run executes a command with a time limit and returns its combined output.
func run(limit time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s timed out", name)
	}
	return out, err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// wifiDiagnose explains why no Wi-Fi interface exists although the PC has a
// Wi-Fi controller ("" if it has none).
func wifiDiagnose() (string, []string) {
	var details []string
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	found := false
	for _, d := range devs {
		cls, _ := os.ReadFile(d + "/class")
		if !strings.HasPrefix(strings.TrimSpace(string(cls)), "0x0280") {
			continue
		}
		found = true
		rd := func(f string) string {
			b, _ := os.ReadFile(d + "/" + f)
			return strings.TrimPrefix(strings.TrimSpace(string(b)), "0x")
		}
		drv := "none"
		if l, err := os.Readlink(d + "/driver"); err == nil {
			drv = filepath.Base(l)
		}
		details = append(details, fmt.Sprintf("Wi-Fi controller %s:%s (subsystem %s:%s) at %s, driver: %s",
			rd("vendor"), rd("device"), rd("subsystem_vendor"), rd("subsystem_device"), filepath.Base(d), drv))
	}
	if !found {
		return "", nil
	}
	fw, _ := os.ReadFile("/sys/module/firmware_class/parameters/path")
	details = append(details, "Firmware path: "+strings.TrimSpace(string(fw)))
	if b, err := os.ReadFile("/proc/version"); err == nil {
		details = append(details, "Kernel: "+strings.Join(strings.Fields(string(b))[:3], " "))
	}
	// The kernel's messages about Wi-Fi and firmware, newest last.
	out, _ := run(5*time.Second, "dmesg")
	var log []string
	for _, l := range strings.Split(string(out), "\n") {
		low := strings.ToLower(l)
		for _, k := range []string{"iwlwifi", "firmware", "mt79", "mt76", "rtw8", "rtw89", "ath1", "wlan", "cfg80211"} {
			if strings.Contains(low, k) {
				log = append(log, strings.TrimSpace(l))
				break
			}
		}
	}
	if len(log) > 14 {
		log = log[len(log)-14:]
	}
	details = append(details, log...)
	return "This PC has a Wi-Fi adapter, but its driver didn't start, so there are no networks to choose. Use Ethernet (or a USB Ethernet adapter) for now.", details
}
