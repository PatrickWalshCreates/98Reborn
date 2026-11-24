package gui

// Keys the widgets understand.
const (
	KeyNone = iota
	KeyEnter
	KeyEsc
	KeyTab
	KeyBackTab
	KeyBackspace
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeySpace
	KeyF10
)

// Event is one input event.
type Event struct {
	Key   int  // a Key constant, or KeyNone for a typed character / mouse
	Rune  rune // typed character
	Mouse bool // a mouse event (position in X, Y)
	X, Y  int
	Click bool // left button pressed
}

// Theme holds the installer's colors and fonts.
type Theme struct {
	Desk1, Desk2, Panel, Title, TitleText, Text, Muted, Border   Color
	Accent, AccentSoft, Warn, WarnSoft, Bad, BadSoft, Ok, OkSoft Color
	BevelHi, BevelLo, Field                                      Color
	Small, Body, Bold, Big                                       *Font
}

// UI is an immediate-mode widget layer: each frame the screen code calls
// the widget functions, which draw and report interaction. Keyboard focus
// moves through focusable widgets in the order they were drawn.
type UI struct {
	C  *Canvas
	T  *Theme
	ev []Event

	focus, nfocus  int // focused widget index, count this frame
	prevN          int
	MouseX, MouseY int
	click          bool
	clickX, clickY int
	typed          []rune
	keys           []int
	enterUsed      bool
	focusNext      bool
}

func NewUI(c *Canvas, t *Theme) *UI { return &UI{C: c, T: t, MouseX: -1, MouseY: -1} }

// Begin starts a frame with the events since the last one.
func (u *UI) Begin(ev []Event) {
	u.prevN, u.nfocus = u.nfocus, 0
	u.click, u.typed, u.keys, u.enterUsed = false, nil, nil, false
	for _, e := range ev {
		switch {
		case e.Mouse:
			u.MouseX, u.MouseY = e.X, e.Y
			if e.Click {
				u.click, u.clickX, u.clickY = true, e.X, e.Y
			}
		case e.Key == KeyTab:
			if u.prevN > 0 {
				u.focus = (max(u.focus, -1) + 1) % u.prevN
			}
		case e.Key == KeyBackTab:
			if u.prevN > 0 {
				u.focus = (u.focus + u.prevN - 1) % u.prevN
			}
		case e.Key != KeyNone:
			u.keys = append(u.keys, e.Key)
			if e.Rune != 0 { // Space: a key for buttons, a character for fields
				u.typed = append(u.typed, e.Rune)
			}
		case e.Rune != 0:
			u.typed = append(u.typed, e.Rune)
		}
	}
}

// End draws the mouse pointer (when a mouse has been used).
func (u *UI) End() {
	if u.MouseX < 0 {
		return
	}
	x, y := u.MouseX, u.MouseY
	for i := 0; i < 16; i++ { // classic arrow, white with a dark edge
		for j := 0; j <= i*2/3 && j < 11; j++ {
			col := Color(0xffffff)
			if j == 0 || j == i*2/3 || i == 15 {
				col = 0x111111
			}
			u.C.Set(x+j, y+i, col)
		}
	}
}

// Key reports (and consumes) a key pressed this frame.
func (u *UI) Key(k int) bool {
	for i, x := range u.keys {
		if x == k {
			u.keys = append(u.keys[:i], u.keys[i+1:]...)
			if k == KeySpace { // used as an action: it isn't typing too
				for j, r := range u.typed {
					if r == ' ' {
						u.typed = append(u.typed[:j], u.typed[j+1:]...)
						break
					}
				}
			}
			return true
		}
	}
	return false
}

// Focus moves focus to the n-th focusable widget (e.g. the first field);
// -1 focuses nothing, so Enter reaches the step's default (Next) button.
func (u *UI) Focus(n int) { u.focus = n }

func (u *UI) take() (idx int, focused bool) {
	idx = u.nfocus
	u.nfocus++
	if u.focusNext {
		u.focus, u.focusNext = idx, false
	}
	return idx, idx == u.focus
}

// FocusNext gives keyboard focus to the next widget drawn (e.g. a password
// field that just appeared).
func (u *UI) FocusNext() { u.focusNext = true }

func (u *UI) clicked(r Rect) bool { return u.click && r.Contains(u.clickX, u.clickY) }

// Button draws a button; primary buttons are filled with the accent color.
func (u *UI) Button(r Rect, label string, primary, enabled bool) bool {
	idx, focused := u.take()
	t := u.T
	bg, fg := Color(0xe9e9e3), t.Text
	if primary {
		bg, fg = t.Accent, 0xffffff
	}
	if !enabled {
		bg, fg = Mix(bg, t.Panel, 140), Mix(fg, t.Panel, 140)
	}
	if r.Contains(u.MouseX, u.MouseY) && enabled {
		bg = Mix(bg, 0x000000, 20)
	}
	u.C.Fill(r, bg)
	u.C.Bevel(r, Mix(bg, 0xffffff, 140), Mix(bg, 0x000000, 90), false)
	if focused && enabled {
		u.C.Frame(r.Inset(3), Mix(fg, bg, 90))
	}
	u.C.Text(t.Bold, r.X+(r.W-t.Bold.Width(label))/2, r.Y+(r.H-t.Bold.H)/2, label, fg)
	if !enabled {
		return false
	}
	if u.clicked(r) {
		u.focus = idx
		return true
	}
	if focused && !u.enterUsed && (u.Key(KeyEnter) || u.Key(KeySpace)) {
		u.enterUsed = true
		return true
	}
	return false
}

// Checkbox toggles *v.
func (u *UI) Checkbox(r Rect, label string, v *bool) bool {
	_, focused := u.take()
	t := u.T
	box := Rect{r.X, r.Y + (r.H-18)/2, 18, 18}
	u.C.Fill(box, t.Field)
	u.C.Bevel(box, t.BevelHi, t.BevelLo, true)
	if *v {
		u.C.Check(box.X+3, box.Y+2, 13, t.Accent)
	}
	lx := box.X + 28
	lines := t.Body.Wrap(label, r.W-28)
	ty := r.Y + (r.H-len(lines)*(t.Body.H+2))/2
	for _, l := range lines {
		u.C.Text(t.Body, lx, ty, l, t.Text)
		ty += t.Body.H + 2
	}
	if focused {
		u.C.Frame(Rect{r.X - 4, r.Y, r.W + 4, r.H}, t.Accent)
	}
	changed := false
	if u.clicked(r) || focused && u.Key(KeySpace) {
		*v = !*v
		changed = true
	}
	return changed
}

// Field is a one-line text input. filter can reject or transform runes.
func (u *UI) Field(r Rect, v *string, placeholder string, max int, filter func(rune) rune) bool {
	idx, focused := u.take()
	t := u.T
	u.C.Fill(r, t.Field)
	u.C.Bevel(r, t.BevelHi, t.BevelLo, true)
	if focused {
		u.C.Frame(r.Inset(1), t.Accent)
	}
	if u.clicked(r) {
		u.focus, focused = idx, true
	}
	changed := false
	if focused {
		for _, ch := range u.typed {
			if filter != nil {
				ch = filter(ch)
			}
			if ch != 0 && len([]rune(*v)) < max {
				*v += string(ch)
				changed = true
			}
		}
		u.typed = nil
		if u.Key(KeyBackspace) && *v != "" {
			rs := []rune(*v)
			*v = string(rs[:len(rs)-1])
			changed = true
		}
	}
	ty := r.Y + (r.H-t.Body.H)/2
	u.C.Clip(r.Inset(2))
	if *v == "" && !focused {
		u.C.Text(t.Body, r.X+8, ty, placeholder, t.Muted)
	} else {
		x := u.C.Text(t.Body, r.X+8, ty, *v, t.Text)
		if focused {
			u.C.Fill(Rect{x + 1, ty, 2, t.Body.H}, t.Text)
		}
	}
	u.C.Unclip()
	return changed
}

// Row is a selectable row (list item / radio choice). It returns true when
// chosen by click, or by Space/Enter while focused. Arrow keys move focus
// between consecutive rows.
func (u *UI) Row(r Rect, selected, enabled bool) bool {
	idx, focused := u.take()
	t := u.T
	switch {
	case selected:
		u.C.Fill(r, t.AccentSoft)
		u.C.Fill(Rect{r.X, r.Y, 4, r.H}, t.Accent)
	case r.Contains(u.MouseX, u.MouseY) && enabled:
		u.C.Fill(r, Mix(t.Panel, t.AccentSoft, 120))
	}
	if focused {
		u.C.Frame(r, Mix(t.Accent, t.Panel, 60))
		if u.Key(KeyDown) {
			u.focus = idx + 1
		} else if u.Key(KeyUp) && idx > 0 {
			u.focus = idx - 1
		}
	}
	if !enabled {
		return false
	}
	if u.clicked(r) {
		u.focus = idx
		return true
	}
	return focused && (u.Key(KeySpace) || u.Key(KeyEnter))
}

// Progress draws a progress bar; frac < 0 shows a moving stripe.
func (u *UI) Progress(r Rect, frac float64, tick int) {
	t := u.T
	u.C.Fill(r, t.Field)
	u.C.Bevel(r, t.BevelHi, t.BevelLo, true)
	in := r.Inset(3)
	if frac >= 0 {
		// 98-style blocks.
		w := int(float64(in.W) * min(frac, 1))
		bw := max(8, in.H*2/3)
		for x := 0; x < w; x += bw + 2 {
			u.C.Fill(Rect{in.X + x, in.Y, min(bw, w-x), in.H}, t.Accent)
		}
		return
	}
	bw := max(8, in.H*2/3)
	span := in.W / 4
	start := (tick * 12) % (in.W + span)
	for x := start - span; x < start; x += bw + 2 {
		if x >= 0 && x < in.W {
			u.C.Fill(Rect{in.X + x, in.Y, min(bw, in.W-x), in.H}, t.Accent)
		}
	}
}
