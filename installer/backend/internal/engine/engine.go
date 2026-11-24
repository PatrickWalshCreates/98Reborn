// Package engine plans and applies patch modules to an installed Windows 98 SE
// tree (the mounted C: drive), with dry-run diffs, backups and rollback.
package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/lines"
	"github.com/win9x-modern/w9xm/internal/module"
	"github.com/win9x-modern/w9xm/internal/reg"
	"github.com/win9x-modern/w9xm/internal/textfile"
)

type Options struct {
	Target  Target                       // the Win98 system drive (C:): mounted folder or disk image
	WinDir  string                       // Windows directory relative to the drive root (default WINDOWS)
	Facts   *module.Facts                // hardware facts; nil = unknown
	Params  map[string]map[string]string // module -> param -> value (edition presets + user overrides)
	Reapply bool                         // re-run modules already applied at the same version
	Tool    string                       // tool version string recorded in the journal
	// Media are the user's original CDs, keyed "98se", "98" or "me", for media.extract ops.
	Media map[string]MediaSource
}

// MediaSource reads a file from an original CD's setup cabinets.
type MediaSource interface {
	ReadFile(name string) ([]byte, error)
}

type PlannedModule struct {
	Module *module.Module
	Params map[string]string
	Notes  []string
}

type Skip struct{ ID, Reason string }

type Plan struct {
	Opts     Options
	Modules  []PlannedModule
	Skipped  []Skip
	Changes  []Change
	registry []reg.Value
	regDels  []reg.Delete
}

// Registry returns the registry values the plan queues for import at next boot.
func (p *Plan) Registry() []reg.Value { return p.registry }

// RegistryDeletes returns the registry keys/values the plan deletes at first boot.
func (p *Plan) RegistryDeletes() []reg.Delete { return p.regDels }

// NewPlan runs mods (already dependency-ordered) against an overlay of the target.
func NewPlan(opts Options, mods []*module.Module) (*Plan, error) {
	if opts.WinDir == "" {
		opts.WinDir = "WINDOWS"
	}
	if opts.Target == nil {
		return nil, fmt.Errorf("no target")
	}
	hist, err := LoadHistory(opts.Target)
	if err != nil {
		return nil, err
	}
	applied := hist.Applied()

	p := &Plan{Opts: opts}
	ov := NewOverlay(opts.Target)
	rc := &regCollector{}
	excluded := map[string]bool{} // skipped for a reason that blocks dependents

	for _, m := range mods {
		if r := blockedBy(m, excluded); r != "" {
			p.Skipped = append(p.Skipped, Skip{m.ID, "requires skipped module " + r})
			excluded[m.ID] = true
			continue
		}
		if ok, why := m.When.Eval(opts.Facts); !ok {
			p.Skipped = append(p.Skipped, Skip{m.ID, why})
			excluded[m.ID] = true
			continue
		}
		if rec, ok := applied[m.ID]; ok && rec.Version == m.Version && !opts.Reapply {
			p.Skipped = append(p.Skipped, Skip{m.ID, fmt.Sprintf("already applied (v%s, tx %s)", rec.Version, rec.Tx)})
			continue
		}
		params, err := m.ResolveParams(opts.Params[m.ID])
		if err != nil {
			return nil, err
		}
		vars := opts.Facts.Vars()
		vars["windir"] = strings.ReplaceAll(opts.WinDir, "/", `\`)
		for k, v := range params {
			vars[k] = v
		}
		pm := PlannedModule{Module: m, Params: params}
		x := &opCtx{m: m, vars: vars, ov: ov, rc: rc, notes: &pm.Notes, media: opts.Media}
		for i, op := range m.Ops {
			if err := x.run(op); err != nil {
				return nil, fmt.Errorf("module %s, op %d (%s): %w", m.ID, i+1, op.Op, err)
			}
		}
		p.Modules = append(p.Modules, pm)
	}
	if err := rc.finalize(ov, opts.WinDir); err != nil {
		return nil, err
	}
	p.Changes = ov.Changes()
	p.registry = rc.sets
	p.regDels = rc.dels
	return p, nil
}

func blockedBy(m *module.Module, excluded map[string]bool) string {
	for _, r := range m.Requires {
		if excluded[r] {
			return r
		}
	}
	return ""
}

// regCollector gathers registry ops across modules into one pending import.
type regCollector struct {
	sets []reg.Value
	dels []reg.Delete
}

const (
	pendingREG = "W9XM/PENDING.REG"
	autoexec   = "AUTOEXEC.BAT"
	runOnceKey = `HKLM\Software\Microsoft\Windows\CurrentVersion\RunOnce`
)

func (rc *regCollector) finalize(ov *Overlay, winDir string) error {
	if len(rc.sets) == 0 && len(rc.dels) == 0 {
		return nil
	}
	sets := rc.sets
	if len(rc.dels) > 0 {
		// One INF per plan, 8.3-safe name, never overwriting a pending one.
		var name string
		for n := time.Now().Unix() & 0xFFFFFFF; ; n++ {
			name = fmt.Sprintf("D%07X.INF", n&0xFFFFFFF)
			if _, exists, err := ov.Read("W9XM/" + name); err != nil {
				return err
			} else if !exists {
				break
			}
		}
		if err := ov.Write("W9XM/"+name, reg.RenderINF(rc.dels)); err != nil {
			return err
		}
		cmd := `rundll.exe setupx.dll,InstallHinfSection DefaultInstall 132 C:\W9XM\` + name
		v, err := reg.NewValue(runOnceKey, "W9XM_"+strings.TrimSuffix(name, ".INF"), "sz", cmd)
		if err != nil {
			return err
		}
		sets = append(sets, v)
	}
	body := reg.RenderREG(sets)
	cur, exists, err := ov.Read(pendingREG)
	if err != nil {
		return err
	}
	if exists { // an earlier change hasn't been imported yet: append to it
		body = append(append([]byte(nil), cur...), body[len(reg.Header):]...)
	}
	if err := ov.Write(pendingREG, body); err != nil {
		return err
	}

	win := `C:\` + strings.ReplaceAll(winDir, "/", `\`)
	block := []string{
		`IF EXIST C:\W9XM\APPLIED.REG DEL C:\W9XM\APPLIED.REG`,
		`IF EXIST C:\W9XM\PENDING.REG ` + win + `\REGEDIT.EXE /L:` + win + `\SYSTEM.DAT /R:` + win + `\USER.DAT C:\W9XM\PENDING.REG`,
		`IF EXIST C:\W9XM\PENDING.REG REN C:\W9XM\PENDING.REG APPLIED.REG`,
	}
	ab, exists, err := ov.Read(autoexec)
	if err != nil {
		return err
	}
	doc := textfile.New()
	if exists {
		doc = textfile.Parse(ab)
	}
	if lines.EnsureBlock(doc, "registry", block, "start") {
		return ov.Write(autoexec, doc.Bytes())
	}
	return nil
}
