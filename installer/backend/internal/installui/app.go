// Package installui is the 98Reborn installer's graphical interface: a
// wizard in the spirit of Windows 98 Setup (stages listed on the left) with
// 98Reborn's look. It drives a System, which the installer implements on the
// real machine and the preview tool implements with sample data.
package installui

import (
	"fmt"
	"image"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/disk"
	"github.com/win9x-modern/w9xm/internal/gui"
	"github.com/win9x-modern/w9xm/internal/lab"
)

// Medium describes what the installer booted from.
type Medium struct {
	Device   string
	Version  string
	HasSetup bool
	Cabs     int
}

// WifiNet is a Wi-Fi network in range.
type WifiNet struct {
	SSID    string
	Signal  int // dBm
	Secured bool
}

// NetState is the result of trying to get online.
type NetState struct {
	Online   bool
	Wired    []string
	Wireless []string
}

// Request is what the user chose.
type Request struct {
	Choice       disk.Option
	Edition      string
	Answers      lab.Answers
	Key          string
	MakeDefault  bool // BIOS PCs with another system: make Windows 98 the active partition
	RegisterBoot bool // UEFI PCs: make 98Reborn Boot the first boot option
}

// System is the machine the installer runs on.
type System interface {
	Prepare(status func(string)) (*Medium, error) // drivers, find the medium
	Connect() NetState
	Online() bool // a quick internet check
	WifiScan() ([]WifiNet, error)
	WifiConnect(ssid, password string) error
	Check() *compat.Report
	Disks() []*disk.Disk
	Install(r Request, stage func(string)) error
	VMScreen() image.Image // Windows Setup's screen while it runs (nil if none yet)
	UEFI() bool
	Reboot()
	PowerOff()
	Shell()
}

type step int

const (
	stWelcome step = iota
	stNetwork
	stCheck
	stDisk
	stDetails
	stConfirm
	stInstall
	stDone
)

var stepNames = []string{"Getting ready", "Connecting", "Checking this PC", "Choosing a disk", "Your details", "Ready to install", "Installing Windows 98", "Finished"}

// App is the installer's state. Background work updates it under mu; the
// render loop reads it.
type App struct {
	Sys System
	T   *gui.Theme

	mu   sync.Mutex
	step step
	tick int

	status  []string // welcome screen progress
	medium  *Medium
	prepErr error

	netBusy bool
	net     *NetState
	offline bool

	wifiNets     []WifiNet
	wifiScanning bool
	wifiPick     int
	wifiPass     string
	wifiJoining  bool
	wifiJoined   string
	wifiErr      string
	focusPass    bool

	report  *compat.Report
	scroll  int
	disks   []*disk.Disk
	options []disk.Option
	pick    int

	edition    string
	name, org  string
	computer   string
	tz         int
	key        string
	accept     bool
	makeDef    bool
	regBoot    bool
	confirmTxt string
	backedUp   bool

	stage      string
	stages     []string
	started    time.Time
	installErr error
	quit       bool
}

// TimeZones are Windows 98 Setup's names, with friendly labels.
var TimeZones = []struct{ Label, Setup string }{
	{"US Pacific", "Pacific"}, {"US Mountain", "Mountain"}, {"US Central", "Central"}, {"US Eastern", "Eastern"},
	{"UK and Ireland", "GMT"}, {"Central Europe", "W. Europe"}, {"Eastern Europe", "E. Europe"},
	{"Australia (Eastern)", "AUS Eastern"}, {"Japan", "Tokyo"},
}

func New(sys System, t *gui.Theme) *App {
	return &App{Sys: sys, T: t, edition: "core", name: "98Reborn User", computer: "RETRO98", makeDef: true, regBoot: true, pick: -1, wifiPick: -1}
}

// Theme returns the installer theme with fonts sized for a w×h screen.
func Theme(w, h int) *gui.Theme {
	t := &gui.Theme{
		Desk1: 0x0b3b38, Desk2: 0x06231f, Panel: 0xf7f7f4, Title: 0x0f766e, TitleText: 0xffffff,
		Text: 0x16181d, Muted: 0x555b66, Border: 0xd9d9d1,
		Accent: 0x0f766e, AccentSoft: 0xccfbf1, Warn: 0x9a5b00, WarnSoft: 0xfff3d6,
		Bad: 0xb42318, BadSoft: 0xfde8e6, Ok: 0x166534, OkSoft: 0xdcfce7,
		BevelHi: 0xffffff, BevelLo: 0xa8a8a0, Field: 0xffffff,
	}
	switch {
	case h >= 1000 && w >= 1600:
		t.Small, t.Body, t.Bold, t.Big = gui.MustFont("ter-120n"), gui.MustFont("ter-124n"), gui.MustFont("ter-124b"), gui.MustFont("ter-132b")
	case h >= 720:
		t.Small, t.Body, t.Bold, t.Big = gui.MustFont("ter-116n"), gui.MustFont("ter-120n"), gui.MustFont("ter-120b"), gui.MustFont("ter-124b")
	default:
		t.Small, t.Body, t.Bold, t.Big = gui.MustFont("ter-116n"), gui.MustFont("ter-116n"), gui.MustFont("ter-120b"), gui.MustFont("ter-120b")
	}
	return t
}

// ---------- background work ----------

func (a *App) startPrepare() {
	go func() {
		m, err := a.Sys.Prepare(func(s string) {
			a.mu.Lock()
			a.status = append(a.status, s)
			a.mu.Unlock()
		})
		a.mu.Lock()
		a.medium, a.prepErr = m, err
		a.mu.Unlock()
	}()
}

func (a *App) startConnect() {
	a.netBusy, a.net = true, nil
	go func() {
		n := a.Sys.Connect()
		a.mu.Lock()
		a.net, a.netBusy = &n, false
		a.mu.Unlock()
	}()
}

func (a *App) startCheck() {
	go func() {
		r := a.Sys.Check()
		d := a.Sys.Disks()
		a.mu.Lock()
		a.report, a.disks = r, d
		a.options = disk.Plan(d, disk.Requirements{})
		a.pick = -1
		for i, o := range a.options {
			if o.Recommended {
				a.pick = i
			}
		}
		a.mu.Unlock()
	}()
}

func (a *App) startInstall() {
	r := Request{Choice: a.options[a.pick], Edition: a.edition, Key: a.key, MakeDefault: a.makeDef, RegisterBoot: a.regBoot && a.Sys.UEFI(),
		Answers: lab.Answers{Name: a.name, Org: a.org, Computer: a.computer, Workgroup: "WORKGROUP", TimeZone: TimeZones[a.tz].Setup}}
	a.started = time.Now()
	go func() {
		err := a.Sys.Install(r, func(s string) {
			a.mu.Lock()
			if a.stage != "" {
				a.stages = append(a.stages, a.stage)
			}
			a.stage = s
			a.mu.Unlock()
		})
		a.mu.Lock()
		a.installErr = err
		if err == nil {
			a.step = stDone
		}
		a.mu.Unlock()
	}()
}

// Start begins the first step's background work.
func (a *App) Start() { a.startPrepare() }

// Quit reports whether the user chose to leave the installer (shell).
func (a *App) Quit() bool { return a.quit }

// ---------- frame ----------

// Frame draws one frame with the input since the last one.
func (a *App) Frame(u *gui.UI, ev []gui.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tick++
	u.Begin(ev)
	c, t := u.C, a.T
	c.VGradient(gui.R(0, 0, c.W, c.H), t.Desk1, t.Desk2)

	ww, wh := min(c.W-32, 72*t.Body.W+260), min(c.H-32, 31*t.Body.H+120)
	win := gui.R((c.W-ww)/2, (c.H-wh)/2, ww, wh)
	c.Fill(gui.R(win.X+6, win.Y+6, win.W, win.H), 0x041a18) // shadow
	c.Fill(win, t.Panel)
	c.Bevel(gui.R(win.X-2, win.Y-2, win.W+4, win.H+4), t.BevelHi, t.BevelLo, false)
	th := t.Big.H + 20
	c.Fill(gui.R(win.X, win.Y, win.W, th), t.Title)
	ls := th - 10
	c.Logo(win.X+10, win.Y+5, ls, t.AccentSoft, 0x042f2e, 0x5eead4)
	c.Text(t.Big, win.X+ls+20, win.Y+(th-t.Big.H)/2, "98Reborn Installer", t.TitleText)

	// Stage pane (as in Windows 98 Setup).
	side := gui.R(win.X, win.Y+th, 22*t.Body.W+40, win.H-th)
	c.Fill(side, 0xe9eee9)
	c.Fill(gui.R(side.X+side.W-1, side.Y, 1, side.H), t.Border)
	y := side.Y + 24
	for i, n := range stepNames {
		col, f := t.Muted, t.Body
		if step(i) == a.step {
			col, f = t.Text, t.Bold
			c.Fill(gui.R(side.X, y-6, side.W-1, f.H+12), t.AccentSoft)
			c.Fill(gui.R(side.X, y-6, 4, f.H+12), t.Accent)
		}
		if step(i) < a.step {
			c.Check(side.X+16, y+2, f.H-4, t.Accent)
		} else {
			c.Disc(side.X+22, y+f.H/2, 3, col)
		}
		c.Text(f, side.X+40, y, n, col)
		y += f.H + 18
	}
	if a.medium != nil && a.medium.Version != "" {
		c.Text(t.Small, side.X+16, side.Y+side.H-t.Small.H-12, "Installer "+a.medium.Version, t.Muted)
	}

	body := gui.R(side.X+side.W+28, win.Y+th+22, win.W-side.W-56, win.H-th-44)
	btnH := t.Bold.H + 16
	buttons := gui.R(body.X, body.Y+body.H-btnH, body.W, btnH)
	content := gui.R(body.X, body.Y, body.W, body.H-btnH-16)

	switch a.step {
	case stWelcome:
		a.welcome(u, content, buttons)
	case stNetwork:
		a.network(u, content, buttons)
	case stCheck:
		a.check(u, content, buttons)
	case stDisk:
		a.diskStep(u, content, buttons)
	case stDetails:
		a.details(u, content, buttons)
	case stConfirm:
		a.confirm(u, content, buttons)
	case stInstall:
		a.installing(u, content, buttons)
	case stDone:
		a.done(u, content, buttons)
	}
	u.End()
}

// heading draws the step's title and returns the y below it.
func (a *App) heading(c *gui.Canvas, r gui.Rect, title, sub string) int {
	t := a.T
	c.Text(t.Big, r.X, r.Y, title, t.Text)
	y := r.Y + t.Big.H + 10
	if sub != "" {
		y += c.TextBox(t.Body, gui.R(r.X, y, r.W, 0), sub, t.Muted, 4) + 8
	}
	return y + 6
}

// note draws a colored message box and returns its height.
func (a *App) note(c *gui.Canvas, r gui.Rect, text string, fg, bg gui.Color) int {
	t := a.T
	lines := t.Body.Wrap(text, r.W-28)
	h := len(lines)*(t.Body.H+4) + 20
	c.Fill(gui.R(r.X, r.Y, r.W, h), bg)
	c.Fill(gui.R(r.X, r.Y, 4, h), fg)
	y := r.Y + 10
	for _, l := range lines {
		c.Text(t.Body, r.X+16, y, l, fg)
		y += t.Body.H + 4
	}
	return h
}

func (a *App) spinner(c *gui.Canvas, x, y, size int) {
	t := a.T
	for i := 0; i < 8; i++ {
		col := gui.Mix(t.Accent, t.Panel, ((i+a.tick)%8)*28)
		dx := []int{0, 1, 1, 1, 0, -1, -1, -1}[i]
		dy := []int{-1, -1, 0, 1, 1, 1, 0, -1}[i]
		c.Disc(x+size/2+dx*size/3, y+size/2+dy*size/3, max(2, size/9), col)
	}
}

func (a *App) nav(u *gui.UI, r gui.Rect, back bool, next string, nextOK bool) (goBack, goNext bool) {
	t := a.T
	bw := max(t.Bold.Width(next), t.Bold.Width("< Back")) + 48
	nr := gui.R(r.X+r.W-bw, r.Y, bw, r.H)
	br := gui.R(nr.X-bw-12, r.Y, bw, r.H)
	if back {
		goBack = u.Button(br, "< Back", false, true)
	}
	if next != "" {
		goNext = u.Button(nr, next, true, nextOK)
	}
	if !goNext && nextOK && next != "" && u.Key(gui.KeyEnter) {
		goNext = true
	}
	return
}

func (a *App) welcome(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Welcome to 98Reborn",
		"This installer puts Windows 98 Second Edition on this PC: on a disk of its own, or next to the systems already on it. Nothing on your disks changes until you confirm, near the end.")
	for i, s := range a.status {
		done := i < len(a.status)-1 || a.medium != nil || a.prepErr != nil
		if done {
			c.Check(r.X, y+2, t.Body.H-4, t.Ok)
		} else {
			a.spinner(c, r.X-2, y, t.Body.H)
		}
		c.Text(t.Body, r.X+t.Body.H+10, y, s, t.Text)
		y += t.Body.H + 12
	}
	y += 8
	switch {
	case a.prepErr != nil:
		a.note(c, gui.R(r.X, y, r.W, 0), a.prepErr.Error(), t.Bad, t.BadSoft)
	case a.medium != nil && !a.medium.HasSetup:
		a.note(c, gui.R(r.X, y, r.W, 0), "This USB stick has no Windows 98 SE setup files, so it can only check PCs. Build it again with the 98Reborn Tool and your CD.", t.Warn, t.WarnSoft)
	}
	ready := a.medium != nil && a.prepErr == nil
	if _, next := a.nav(u, btn, false, "Next >", ready); next {
		a.step = stNetwork
		a.startConnect()
		u.Focus(-1)
	}
}

func (a *App) network(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Connect to the internet",
		"With internet, the installer can download drivers and fixes for this exact PC. Plug in Ethernet, or choose a Wi-Fi network.")
	online := a.net != nil && a.net.Online
	switch {
	case a.netBusy:
		a.spinner(c, r.X-2, y, t.Body.H)
		c.Text(t.Body, r.X+t.Body.H+10, y, "Connecting…", t.Text)
	case online:
		msg := "Connected to the internet."
		if a.wifiJoined != "" {
			msg = "Connected to the internet through Wi-Fi (" + a.wifiJoined + ")."
		}
		a.note(c, gui.R(r.X, y, r.W, 0), msg, t.Ok, t.OkSoft)
	case a.net != nil:
		if len(a.net.Wireless) > 0 {
			y = a.wifiPicker(u, gui.R(r.X, y, r.W, r.Y+r.H-y), btn.H)
		} else {
			msg := "No internet connection. Check the Ethernet cable and try again."
			if len(a.net.Wired) == 0 {
				msg = "No network adapter was found."
			}
			y += a.note(c, gui.R(r.X, y, r.W, 0), msg, t.Warn, t.WarnSoft) + 14
		}
		y += 6
		y += a.note(c, gui.R(r.X, y, r.W, 0), "You can continue without internet, but compatibility will be minimal: no driver downloads and no fixes newer than this installer. Windows 98 may not work properly on this PC.", t.Bad, t.BadSoft) + 14
		bw := t.Bold.Width("Try again") + 48
		if u.Button(gui.R(r.X, y, bw, btn.H), "Try again", false, true) {
			a.startConnect()
		}
		ow := t.Bold.Width("Continue without internet") + 48
		if u.Button(gui.R(r.X+bw+12, y, ow, btn.H), "Continue without internet", false, true) {
			a.offline = true
			a.step = stCheck
			a.startCheck()
			u.Focus(-1)
		}
	}
	back, next := a.nav(u, btn, true, "Next >", online)
	if back {
		a.step = stWelcome
	}
	if next {
		a.step = stCheck
		a.startCheck()
		u.Focus(-1)
	}
}

func (a *App) startWifiScan() {
	a.wifiScanning, a.wifiErr = true, ""
	go func() {
		nets, err := a.Sys.WifiScan()
		a.mu.Lock()
		if nets == nil {
			nets = []WifiNet{} // scanned: found none (nil means "not scanned yet")
		}
		a.wifiNets, a.wifiScanning = nets, false
		if err != nil {
			a.wifiErr = err.Error()
		}
		if a.wifiPick >= len(nets) {
			a.wifiPick = -1
		}
		a.mu.Unlock()
	}()
}

func (a *App) startWifiJoin(n WifiNet, pass string) {
	a.wifiJoining, a.wifiErr = true, ""
	go func() {
		err := a.Sys.WifiConnect(n.SSID, pass)
		online := err == nil && a.Sys.Online()
		a.mu.Lock()
		a.wifiJoining = false
		switch {
		case err != nil:
			a.wifiErr = err.Error()
		case !online:
			a.wifiErr = "Joined " + n.SSID + ", but it has no internet access."
		default:
			a.wifiJoined = n.SSID
			a.net.Online = true
		}
		a.mu.Unlock()
	}()
}

// signalBars draws 0-4 bars for a signal strength in dBm.
func (a *App) signalBars(c *gui.Canvas, x, y, h, dbm int) {
	n := 1
	switch {
	case dbm >= -55:
		n = 4
	case dbm >= -65:
		n = 3
	case dbm >= -75:
		n = 2
	}
	for i := 0; i < 4; i++ {
		bh := h * (i + 1) / 4
		col := a.T.Border
		if i < n {
			col = a.T.Accent
		}
		c.Fill(gui.R(x+i*6, y+h-bh, 4, bh), col)
	}
}

// wifiPicker lists Wi-Fi networks; choosing one asks for its password.
func (a *App) wifiPicker(u *gui.UI, r gui.Rect, btnH int) int {
	c, t := u.C, a.T
	y := r.Y
	if a.wifiNets == nil && !a.wifiScanning && a.wifiErr == "" {
		a.startWifiScan()
	}
	c.Text(t.Bold, r.X, y, "Wi-Fi networks", t.Text)
	rw := t.Bold.Width("Refresh") + 32
	if u.Button(gui.R(r.X+r.W-rw, y-6, rw, t.Bold.H+12), "Refresh", false, !a.wifiScanning && !a.wifiJoining) {
		a.startWifiScan()
	}
	y += t.Bold.H + 14
	if a.wifiScanning {
		a.spinner(c, r.X-2, y, t.Body.H)
		c.Text(t.Body, r.X+t.Body.H+10, y, "Looking for networks…", t.Text)
		return y + t.Body.H + 16
	}
	rowH := t.Body.H + 14
	maxRows := max(3, (r.H-200)/(rowH+2))
	if len(a.wifiNets) == 0 && a.wifiErr == "" {
		c.Text(t.Body, r.X, y, "No networks found. Move closer to the router, then Refresh.", t.Muted)
		y += t.Body.H + 14
	}
	for i, n := range a.wifiNets {
		if i >= maxRows {
			break
		}
		row := gui.R(r.X, y, r.W, rowH)
		if u.Row(row, a.wifiPick == i, !a.wifiJoining) && a.wifiPick != i {
			a.wifiPick, a.wifiPass, a.focusPass = i, "", true
		}
		a.signalBars(c, row.X+14, row.Y+(rowH-t.Body.H)/2+2, t.Body.H-4, n.Signal)
		c.Text(t.Body, row.X+50, row.Y+(rowH-t.Body.H)/2, n.SSID, t.Text)
		if n.Secured {
			c.Text(t.Small, row.X+row.W-t.Small.Width("secured")-12, row.Y+(rowH-t.Small.H)/2, "secured", t.Muted)
		}
		y += rowH + 2
	}
	if a.wifiPick >= 0 && a.wifiPick < len(a.wifiNets) {
		n := a.wifiNets[a.wifiPick]
		y += 8
		cw := t.Bold.Width("Connect") + 48
		fw := r.W - cw - 12
		if n.Secured {
			c.Text(t.Small, r.X, y, "Password for "+n.SSID, t.Muted)
			y += t.Small.H + 4
			masked := strings.Repeat("*", len([]rune(a.wifiPass)))
			shown := masked
			if a.focusPass {
				u.FocusNext()
				a.focusPass = false
			}
			if u.Field(gui.R(r.X, y, fw, btnH), &shown, "", 63, nil) {
				// Field edits the masked copy: apply the change to the real password.
				rs := []rune(a.wifiPass)
				switch {
				case len([]rune(shown)) < len(rs):
					a.wifiPass = string(rs[:len([]rune(shown))])
				default:
					a.wifiPass += string([]rune(shown)[len([]rune(masked)):])
				}
			}
		}
		ready := !a.wifiJoining && (!n.Secured || len(a.wifiPass) >= 8)
		if u.Button(gui.R(r.X+r.W-cw, y, cw, btnH), "Connect", true, ready) || ready && u.Key(gui.KeyEnter) {
			a.startWifiJoin(n, a.wifiPass)
		}
		y += btnH + 10
	}
	switch {
	case a.wifiJoining:
		a.spinner(c, r.X-2, y, t.Body.H)
		c.Text(t.Body, r.X+t.Body.H+10, y, "Joining the network…", t.Text)
		y += t.Body.H + 12
	case a.wifiErr != "":
		y += a.note(c, gui.R(r.X, y, r.W, 0), a.wifiErr, t.Warn, t.WarnSoft) + 8
	}
	return y
}

var tierColors = map[string][2]gui.Color{
	"Gold": {0x92400e, 0xfef3c7}, "Silver": {0x374151, 0xe5e7eb}, "Bronze": {0x8a3b12, 0xfde2cc},
	"In development": {0x0b5c56, 0xccfbf1}, "Not supported": {0xb42318, 0xfde8e6},
}

func (a *App) check(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	if a.report == nil {
		y := a.heading(c, r, "Checking this PC", "")
		a.spinner(c, r.X-2, y, t.Body.H)
		c.Text(t.Body, r.X+t.Body.H+10, y, "Looking at the hardware…", t.Text)
		a.nav(u, btn, false, "Next >", false)
		return
	}
	rep := a.report
	y := a.heading(c, r, "How well this PC runs Windows 98", "")
	tier := rep.Tier.String()
	col := tierColors[tier]
	pw := t.Bold.Width(strings.ToUpper(tier)) + 32
	c.Fill(gui.R(r.X, y, pw, t.Bold.H+12), col[1])
	c.Text(t.Bold, r.X+16, y+6, strings.ToUpper(tier), col[0])
	y += t.Bold.H + 20
	y += c.TextBox(t.Body, gui.R(r.X, y, r.W, 0), rep.Meaning, t.Text, 2) + 12
	if a.offline {
		y += a.note(c, gui.R(r.X, y, r.W, 0), "Offline: only the drivers and fixes on this USB stick can be used.", t.Warn, t.WarnSoft) + 10
	}
	// Findings, most important first, in a scrolling list.
	list := gui.R(r.X, y, r.W, r.Y+r.H-y)
	order := map[string]int{"block": 0, "warn": 1, "info": 2, "ok": 3}
	fs := append([]compat.Finding{}, rep.Findings...)
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && order[fs[j].Level] < order[fs[j-1].Level]; j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
	if u.Key(gui.KeyDown) {
		a.scroll = min(a.scroll+1, max(0, len(fs)-1))
	}
	if u.Key(gui.KeyUp) {
		a.scroll = max(0, a.scroll-1)
	}
	c.Clip(list)
	fy := list.Y
	shown := 0
	for i := a.scroll; i < len(fs); i++ {
		f := fs[i]
		icon, ic := "i", t.Muted
		switch f.Level {
		case "block":
			icon, ic = "x", t.Bad
		case "warn":
			icon, ic = "!", t.Warn
		case "ok":
			icon, ic = "", t.Ok
		}
		if icon == "" {
			c.Check(list.X, fy+2, t.Body.H-6, ic)
		} else {
			c.Text(t.Bold, list.X+4, fy, icon, ic)
		}
		h := c.TextBox(t.Body, gui.R(list.X+28, fy, list.W-28, 0), f.Area+": "+f.Reason, t.Text, 2)
		if f.Fix != "" && f.Level != "ok" {
			h += c.TextBox(t.Small, gui.R(list.X+28, fy+h+2, list.W-28, 0), f.Fix, t.Accent, 2) + 2
		}
		fy += h + 12
		if fy < list.Y+list.H {
			shown++
		}
	}
	c.Unclip()
	if a.scroll+shown < len(fs) || a.scroll > 0 {
		c.Text(t.Small, btn.X, btn.Y+(btn.H-t.Small.H)/2, fmt.Sprintf("Up/Down arrows to scroll (%d of %d)", min(a.scroll+shown, len(fs)), len(fs)), t.Muted)
	}
	label := "Next >"
	if rep.Tier == compat.Dev || rep.Tier == compat.None {
		label = "Continue anyway >"
	}
	back, next := a.nav(u, btn, true, label, true)
	if back {
		a.step = stNetwork
	}
	if next {
		a.step = stDisk
		u.Focus(-1)
	}
}

func (a *App) diskStep(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Where should Windows 98 go?", "Your other systems and files stay where they are unless you choose to erase a disk.")
	rowH := t.Body.H + t.Small.H*2 + 22
	for i, o := range a.options {
		if y+rowH > r.Y+r.H-110 {
			break
		}
		row := gui.R(r.X, y, r.W, rowH)
		if !o.Available {
			c.Text(t.Body, row.X+16, row.Y+8, o.Title, t.Muted)
			c.TextBox(t.Small, gui.R(row.X+16, row.Y+10+t.Body.H, row.W-24, 0), "Not possible: "+o.Reason+".", t.Muted, 2)
			y += rowH + 4
			continue
		}
		if u.Row(row, a.pick == i, true) {
			a.pick = i
		}
		title := o.Title
		detail := o.Detail
		if o.Recommended {
			detail = "Recommended. " + detail
		}
		c.Disc(row.X+18, row.Y+8+t.Body.H/2, 7, t.BevelLo)
		c.Disc(row.X+18, row.Y+8+t.Body.H/2, 5, 0xffffff)
		if a.pick == i {
			c.Disc(row.X+18, row.Y+8+t.Body.H/2, 3, t.Accent)
		}
		c.Text(t.Bold, row.X+36, row.Y+8, title, t.Text)
		col := t.Muted
		if len(o.Erases) > 0 {
			col = t.Bad
		}
		c.TextBox(t.Small, gui.R(row.X+36, row.Y+12+t.Body.H, row.W-48, 0), detail, col, 2)
		y += rowH + 4
	}
	if a.pick >= 0 {
		o := &a.options[a.pick]
		by := r.Y + r.H - 96
		c.Text(t.Small, r.X, by, "Now", t.Muted)
		a.bar(c, gui.R(r.X+50, by-4, r.W-50, 34), o.Disk, nil)
		c.Text(t.Small, r.X, by+48, "After", t.Muted)
		a.bar(c, gui.R(r.X+50, by+44, r.W-50, 34), o.Disk, o)
	}
	back, next := a.nav(u, btn, true, "Next >", a.pick >= 0)
	if back {
		a.step = stCheck
	}
	if next {
		a.step = stDetails
		u.Focus(2)
	}
}

// bar draws a disk as proportional segments, before or after an option.
func (a *App) bar(c *gui.Canvas, r gui.Rect, d *disk.Disk, o *disk.Option) {
	t := a.T
	c.Fill(r, 0xe4e4dc)
	c.Bevel(r, t.BevelLo, t.BevelHi, false)
	total := float64(max(d.Sectors(), 1))
	seg := func(start, n int64, col gui.Color, label string) {
		x := r.X + int(float64(start)/total*float64(r.W))
		w := max(3, int(float64(n)/total*float64(r.W)))
		s := gui.R(x, r.Y+2, min(w, r.X+r.W-x-1), r.H-4)
		c.Fill(s, col)
		c.Fill(gui.R(s.X, s.Y, 1, s.H), 0xffffff)
		c.Clip(s)
		c.Text(t.Small, s.X+6, s.Y+(s.H-t.Small.H)/2, label, 0xffffff)
		c.Unclip()
	}
	whole := o != nil && o.Kind == disk.KindWholeDisk
	if o == nil && len(d.Parts) == 0 {
		c.Text(t.Small, r.X+10, r.Y+(r.H-t.Small.H)/2, "Empty ("+strings.TrimSpace(fmt.Sprintf("%d GB", d.Size>>30))+")", t.Muted)
	}
	for _, p := range d.Parts {
		if p.Num > 4 || whole {
			continue
		}
		n := p.Sectors
		if o != nil && o.Shrink == p {
			n = o.ShrinkTo
		}
		col, label := gui.Color(0x6b7280), p.Describe()
		switch {
		case strings.Contains(p.OS, "Windows 9"):
			col = 0x0f766e
		case strings.Contains(p.OS, "Windows"):
			col = 0x2563eb
		case p.OS != "":
			col = 0xc2410c
		}
		seg(p.Start, n, col, label)
	}
	if o != nil {
		seg(o.Start, o.Sectors, 0x0f766e, "Windows 98 (new)")
	}
}

func digitsAndLetters(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z':
		return r - 32
	case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return r
	}
	return 0
}

var keyRe = regexp.MustCompile(`^[A-Z0-9]{5}(-[A-Z0-9]{5}){4}$`)

func formatKey(s string) string {
	raw := strings.ReplaceAll(s, "-", "")
	var b strings.Builder
	for i, r := range raw {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (a *App) details(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Your details", "")
	// Edition cards.
	cw := (r.W - 16) / 2
	ch := t.Bold.H + t.Small.H*2 + 30
	for i, ed := range []struct{ id, name, desc string }{
		{"core", "Core", "For the software you still depend on."},
		{"gaming", "Gaming", "For the games you grew up with."},
	} {
		card := gui.R(r.X+i*(cw+16), y, cw, ch)
		c.Bevel(card, t.Border, t.Border, false)
		if u.Row(card, a.edition == ed.id, true) {
			a.edition = ed.id
		}
		c.Text(t.Bold, card.X+16, card.Y+12, ed.name, t.Text)
		c.TextBox(t.Small, gui.R(card.X+16, card.Y+18+t.Bold.H, card.W-32, 0), ed.desc, t.Muted, 2)
	}
	y += ch + 22
	lw := 17 * t.Body.W
	fh := t.Body.H + 14
	field := func(label string, v *string, ph string, maxLen int, f func(rune) rune) {
		c.Text(t.Body, r.X, y+(fh-t.Body.H)/2, label, t.Text)
		u.Field(gui.R(r.X+lw, y, r.W-lw, fh), v, ph, maxLen, f)
		y += fh + 10
	}
	field("Your name", &a.name, "", 48, nil)
	field("Organization", &a.org, "(optional)", 48, nil)
	field("Computer name", &a.computer, "", 15, func(r rune) rune {
		if d := digitsAndLetters(r); d != 0 || r == '-' {
			if d != 0 {
				return d
			}
			return r
		}
		return 0
	})
	// Time zone selector.
	c.Text(t.Body, r.X, y+(fh-t.Body.H)/2, "Time zone", t.Text)
	tzr := gui.R(r.X+lw, y, r.W-lw, fh)
	if u.Row(tzr, false, true) {
		a.tz = (a.tz + 1) % len(TimeZones)
	}
	if u.Key(gui.KeyRight) {
		a.tz = (a.tz + 1) % len(TimeZones)
	}
	if u.Key(gui.KeyLeft) {
		a.tz = (a.tz + len(TimeZones) - 1) % len(TimeZones)
	}
	c.Bevel(tzr, t.BevelLo, t.BevelHi, false)
	c.Text(t.Body, tzr.X+10, tzr.Y+(fh-t.Body.H)/2, "<  "+TimeZones[a.tz].Label+"  >", t.Text)
	y += fh + 10
	field("Product key", &a.key, "XXXXX-XXXXX-XXXXX-XXXXX-XXXXX", 29, digitsAndLetters)
	a.key = formatKey(a.key)
	y += 4
	u.Checkbox(gui.R(r.X, y, r.W, t.Body.H*2+8), "I accept the license agreement that came with my copy of Windows 98 Second Edition.", &a.accept)
	y += t.Body.H*2 + 16
	if a.Sys.UEFI() {
		u.Checkbox(gui.R(r.X, y, r.W, t.Body.H+8), "Make 98Reborn Boot the first boot option (its menu lists every system).", &a.regBoot)
	}
	ok := keyRe.MatchString(a.key) && a.accept && a.computer != "" && a.name != ""
	if !ok && !keyRe.MatchString(a.key) && len(a.key) > 0 {
		c.Text(t.Small, btn.X, btn.Y+(btn.H-t.Small.H)/2, "The key has 25 letters and digits.", t.Warn)
	}
	back, next := a.nav(u, btn, true, "Next >", ok)
	if back {
		a.step = stDisk
	}
	if next {
		a.step = stConfirm
		a.confirmTxt, a.backedUp = "", false
		u.Focus(-1)
	}
}

func (a *App) confirm(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	o := &a.options[a.pick]
	y := a.heading(c, r, "Ready to install", "")
	kv := func(k, v string) {
		c.Text(t.Body, r.X, y, k, t.Muted)
		c.TextBox(t.Body, gui.R(r.X+15*t.Body.W, y, r.W-15*t.Body.W, 0), v, t.Text, 2)
		y += t.Body.H + 10
	}
	kv("Where", o.Title)
	kv("Edition", map[string]string{"core": "Core", "gaming": "Gaming"}[a.edition])
	kv("Name", a.name+"  ("+a.computer+")")
	kv("Time zone", TimeZones[a.tz].Label)
	y += 8
	c.Text(t.Small, r.X, y+8, "Now", t.Muted)
	a.bar(c, gui.R(r.X+50, y, r.W-50, 34), o.Disk, nil)
	y += 46
	c.Text(t.Small, r.X, y+8, "After", t.Muted)
	a.bar(c, gui.R(r.X+50, y, r.W-50, 34), o.Disk, o)
	y += 52
	ok := true
	switch {
	case len(o.Erases) > 0:
		y += a.note(c, gui.R(r.X, y, r.W, 0), "Everything on "+o.Disk.Name+" will be erased. Type the disk's name to confirm.", t.Bad, t.BadSoft) + 12
		u.Field(gui.R(r.X, y, 20*t.Body.W, t.Body.H+14), &a.confirmTxt, o.Disk.Name, 16, nil)
		ok = a.confirmTxt == o.Disk.Name
	case o.Shrink != nil:
		y += a.note(c, gui.R(r.X, y, r.W, 0), "The existing partition will be made smaller. Its files are kept, but a backup first is always wise.", t.Warn, t.WarnSoft) + 12
		u.Checkbox(gui.R(r.X, y, r.W, t.Body.H+8), "My important files are backed up.", &a.backedUp)
		ok = a.backedUp
	default:
		a.note(c, gui.R(r.X, y, r.W, 0), "Windows 98 Setup runs first in a sealed-off virtual PC; your disk is only written at the very end.", t.Ok, t.OkSoft)
	}
	back, next := a.nav(u, btn, true, "Install", ok)
	if back {
		a.step = stDetails
	}
	if next {
		a.step = stInstall
		a.startInstall()
	}
}

var stageText = map[string]string{
	"setup":  "Windows 98 Setup (in a virtual PC)",
	"patch":  "Applying the edition and fixes",
	"shrink": "Making room on the disk",
	"copy":   "Writing Windows 98 to the disk",
	"finish": "Finishing the partition table",
}

func (a *App) installing(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Installing Windows 98", "")
	for _, s := range a.stages {
		c.Check(r.X, y+2, t.Body.H-4, t.Ok)
		c.Text(t.Body, r.X+t.Body.H+10, y, stageText[s], t.Muted)
		y += t.Body.H + 10
	}
	if a.installErr == nil && a.stage != "" {
		a.spinner(c, r.X-2, y, t.Body.H)
		c.Text(t.Bold, r.X+t.Body.H+10, y, stageText[a.stage], t.Text)
		y += t.Body.H + 10
	}
	el := time.Since(a.started).Round(time.Second)
	c.Text(t.Small, r.X, y+4, fmt.Sprintf("%d:%02d elapsed", int(el.Minutes()), int(el.Seconds())%60), t.Muted)
	y += t.Small.H + 14
	if a.installErr != nil {
		a.note(c, gui.R(r.X, y, r.W, 0), "The install stopped: "+a.installErr.Error(), t.Bad, t.BadSoft)
		bw := t.Bold.Width("Open a shell") + 48
		if u.Button(gui.R(btn.X, btn.Y, bw, btn.H), "Open a shell", false, true) {
			a.quit = true
		}
		if _, next := a.nav(u, btn, false, "Restart", true); next {
			go a.Sys.Reboot()
		}
		return
	}
	u.Progress(gui.R(r.X, y, r.W, t.Body.H+8), -1, a.tick)
	y += t.Body.H + 24
	// Windows Setup's own screen, in a little monitor.
	if a.stage == "setup" {
		if img := a.Sys.VMScreen(); img != nil {
			mh := min(r.Y+r.H-y-10, (r.W/2)*3/4)
			mw := mh * 4 / 3
			mon := gui.R(r.X, y, mw+20, mh+20)
			c.Fill(mon, 0x2b2f33)
			c.Blit(img, mon.Inset(10))
			c.TextBox(t.Small, gui.R(mon.X+mon.W+18, y, r.W-mon.W-18, 0),
				"Windows 98 Setup is running in a sealed-off virtual PC. Your disk isn't touched until it has finished. This takes 20-40 minutes.", t.Muted, 4)
		}
	}
}

func (a *App) done(u *gui.UI, r, btn gui.Rect) {
	c, t := u.C, a.T
	y := a.heading(c, r, "Windows 98 is installed", "")
	msg := "Remove the USB stick and restart. Windows 98 finishes setting up your hardware on its first start."
	if a.Sys.UEFI() && a.regBoot {
		msg += " 98Reborn Boot starts first: it lists Windows 98 and your other systems."
	}
	y += a.note(c, gui.R(r.X, y, r.W, 0), msg, t.Ok, t.OkSoft) + 16
	bw := t.Bold.Width("Turn off") + 48
	if u.Button(gui.R(btn.X, btn.Y, bw, btn.H), "Turn off", false, true) {
		go a.Sys.PowerOff()
	}
	if _, next := a.nav(u, btn, false, "Restart", true); next {
		go a.Sys.Reboot()
	}
}

// Splash draws the start-up screen shown while drivers load.
func Splash(c *gui.Canvas, t *gui.Theme, tick int, status string) {
	c.VGradient(gui.R(0, 0, c.W, c.H), t.Desk1, t.Desk2)
	size := min(c.W, c.H) / 4
	x, y := (c.W-size)/2, c.H/2-size*3/4
	c.Logo(x, y, size, t.AccentSoft, 0x042f2e, 0x5eead4)
	title := "98Reborn"
	ty := y + size + t.Big.H/2
	c.Text(t.Big, (c.W-t.Big.Width(title))/2, ty, title, 0xffffff)
	sy := ty + t.Big.H + 18
	c.Text(t.Body, (c.W-t.Body.Width(status))/2, sy, status, 0x9fd8d0)
	// A row of dots, one lit at a time, in the spirit of Windows 98's boot bar.
	n, dw := 9, 10
	bx := (c.W - n*(dw+8)) / 2
	for i := 0; i < n; i++ {
		col := gui.Color(0x1b5c56)
		if i == tick%n {
			col = 0x5eead4
		}
		c.Fill(gui.R(bx+i*(dw+8), sy+t.Body.H+20, dw, dw), col)
	}
}
