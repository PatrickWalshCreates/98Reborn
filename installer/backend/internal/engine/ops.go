package engine

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/win9x-modern/w9xm/internal/vxd"
	"strings"

	"github.com/win9x-modern/w9xm/internal/delta"
	"github.com/win9x-modern/w9xm/internal/ini"
	"github.com/win9x-modern/w9xm/internal/lines"
	"github.com/win9x-modern/w9xm/internal/module"
	"github.com/win9x-modern/w9xm/internal/reg"
	"github.com/win9x-modern/w9xm/internal/textfile"
)

type opCtx struct {
	m     *module.Module
	vars  map[string]string
	ov    *Overlay
	rc    *regCollector
	notes *[]string
	media map[string]MediaSource
}

func (x *opCtx) render(s string) (string, error) { return module.Render(s, x.vars) }

func (x *opCtx) renderAll(ss ...*string) error {
	for _, s := range ss {
		if s == nil {
			continue
		}
		r, err := x.render(*s)
		if err != nil {
			return err
		}
		*s = r
	}
	return nil
}

func (x *opCtx) note(f string, a ...any) { *x.notes = append(*x.notes, fmt.Sprintf(f, a...)) }

// precondition reports a target that isn't in the expected state. Optional ops
// are skipped with a note; otherwise the whole plan fails before any write.
func (x *opCtx) precondition(op module.Op, f string, a ...any) error {
	msg := fmt.Sprintf(f, a...)
	if op.Optional {
		x.note("skipped optional %s: %s", op.Op, msg)
		return nil
	}
	return errors.New(msg)
}

func (x *opCtx) run(op module.Op) error {
	var value *string
	if op.Value != nil {
		v := *op.Value
		value = &v
	}
	lns := append([]string(nil), op.Lines...)
	ptrs := []*string{&op.File, &op.Section, &op.Key, value, &op.Path, &op.Name, &op.Data, &op.Dest}
	for i := range lns {
		ptrs = append(ptrs, &lns[i])
	}
	if err := x.renderAll(ptrs...); err != nil {
		return err
	}

	switch op.Op {
	case "file.copy":
		data, err := x.m.ReadPayload(op.Src, op.SHA256)
		if err != nil {
			return err
		}
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if exists && bytes.Equal(cur, data) {
			return nil
		}
		if exists && !op.Overwrite {
			return x.precondition(op, "%s already exists (set overwrite to replace it)", op.File)
		}
		return x.ov.Write(op.File, data)

	case "file.delete":
		_, exists, err := x.ov.Read(op.File)
		if err != nil || !exists {
			return err
		}
		return x.ov.Delete(op.File)

	case "file.delta":
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists {
			return x.precondition(op, "%s not found", op.File)
		}
		patch, err := x.m.ReadPayload(op.Delta, "")
		if err != nil {
			return err
		}
		var out []byte
		if delta.IsW9XD(patch) {
			h, err := delta.ReadHeader(patch)
			if err != nil {
				return err
			}
			if module.SHA256(cur) == hex.EncodeToString(h.DstSHA[:]) {
				x.note("%s already patched", op.File)
				return nil
			}
			out, err = delta.Apply(cur, patch)
			if errors.Is(err, delta.ErrSourceMismatch) {
				return x.precondition(op, "%s is not the expected version (sha256 %s)", op.File, module.SHA256(cur))
			}
			if err != nil {
				return err
			}
		} else {
			sum := module.SHA256(cur)
			if sum == op.SHA256After {
				x.note("%s already patched", op.File)
				return nil
			}
			if !contains(op.SHA256Before, sum) {
				return x.precondition(op, "%s is not the expected version (sha256 %s)", op.File, sum)
			}
			if out, err = delta.BSPatch(cur, patch); err != nil {
				return err
			}
			if module.SHA256(out) != op.SHA256After {
				return fmt.Errorf("bsdiff output for %s has wrong sha256 (corrupt patch)", op.File)
			}
		}
		return x.ov.Write(op.File, out)

	case "file.bytes":
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists {
			return x.precondition(op, "%s not found", op.File)
		}
		type bp struct {
			off      int64
			old, new []byte
		}
		var ps []bp
		allNew := true
		for _, p := range op.Patches {
			o, _ := hex.DecodeString(p.Old)
			n, _ := hex.DecodeString(p.New)
			if p.Offset+int64(len(o)) > int64(len(cur)) {
				return x.precondition(op, "%s is too short for patch at offset %d", op.File, p.Offset)
			}
			if !bytes.Equal(cur[p.Offset:p.Offset+int64(len(n))], n) {
				allNew = false
			}
			ps = append(ps, bp{p.Offset, o, n})
		}
		if allNew {
			x.note("%s already patched", op.File)
			return nil
		}
		if len(op.SHA256Before) > 0 && !contains(op.SHA256Before, module.SHA256(cur)) {
			return x.precondition(op, "%s is not the expected version (sha256 %s)", op.File, module.SHA256(cur))
		}
		out := append([]byte(nil), cur...)
		for _, p := range ps {
			if got := out[p.off : p.off+int64(len(p.old))]; !bytes.Equal(got, p.old) {
				return x.precondition(op, "%s offset %d: expected %x, found %x", op.File, p.off, p.old, got)
			}
			copy(out[p.off:], p.new)
		}
		if op.SHA256After != "" && module.SHA256(out) != op.SHA256After {
			return fmt.Errorf("patched %s has wrong sha256", op.File)
		}
		return x.ov.Write(op.File, out)

	case "file.pattern":
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists {
			return x.precondition(op, "%s not found", op.File)
		}
		find, _ := module.ParsePattern(op.Find)
		repl, _ := module.ParsePattern(op.Replace)
		// The patched form: replaced bytes where Replace sets them, the
		// original's checked bytes elsewhere.
		done := make([]module.PatternByte, len(find))
		for i := range find {
			done[i] = find[i]
			if !repl[i].Any {
				done[i] = repl[i]
			}
		}
		hits := patternIndex(cur, find)
		if len(hits) == 0 {
			if len(patternIndex(cur, done)) > 0 {
				x.note("%s already patched", op.File)
				return nil
			}
			return x.precondition(op, "%s doesn't contain the code this patch fixes", op.File)
		}
		out := append([]byte(nil), cur...)
		for _, h := range hits {
			for i, r := range repl {
				if !r.Any {
					out[h+i] = r.B
				}
			}
		}
		x.note("%s: patched %d place(s)", op.File, len(hits))
		return x.ov.Write(op.File, out)

	case "vxd.extract":
		if _, exists, err := x.ov.Read(op.Dest); err != nil {
			return err
		} else if exists {
			x.note("%s already extracted", op.Dest)
			return nil
		}
		src, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists {
			return x.precondition(op, "%s not found", op.File)
		}
		c, err := vxd.Open(src)
		if err != nil {
			return x.precondition(op, "%s: %v", op.File, err)
		}
		img, err := c.Extract(op.Name)
		if err != nil {
			return x.precondition(op, "%v", err)
		}
		return x.ov.Write(op.Dest, img)

	case "ini.set", "ini.add", "ini.delete":
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists && op.Op == "ini.delete" {
			return nil
		}
		f := ini.New()
		if exists {
			f = ini.Parse(cur)
		}
		var changed bool
		switch op.Op {
		case "ini.set":
			changed = f.Set(op.Section, op.Key, *value)
		case "ini.add":
			changed = f.Add(op.Section, op.Key, *value)
		default:
			changed = f.Delete(op.Section, op.Key, value)
		}
		if !changed {
			return nil
		}
		return x.ov.Write(op.File, f.Bytes())

	case "lines.block", "lines.remove_block", "lines.edit":
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if !exists && op.Op != "lines.block" {
			return nil
		}
		d := textfile.New()
		if exists {
			d = textfile.Parse(cur)
		}
		var changed bool
		switch op.Op {
		case "lines.block":
			changed = lines.EnsureBlock(d, op.Block, lns, op.Position)
		case "lines.remove_block":
			changed = lines.RemoveBlock(d, op.Block)
		default:
			if changed, err = lines.Edit(d, op.Match, op.Action); err != nil {
				return err
			}
		}
		if !changed {
			return nil
		}
		return x.ov.Write(op.File, d.Bytes())

	case "reg.set":
		v, err := reg.NewValue(op.Path, op.Name, op.Type, op.Data)
		if err != nil {
			return err
		}
		x.rc.sets = append(x.rc.sets, v)
		return nil

	case "media.extract":
		data, from, err := x.fromMedia(op.Media, op.Source)
		if err != nil {
			return x.precondition(op, "%v", err)
		}
		if op.SHA256 != "" && module.SHA256(data) != op.SHA256 {
			return x.precondition(op, "%s on the %s CD has sha256 %s, expected %s (different release?)", op.Source, from, module.SHA256(data), op.SHA256)
		}
		cur, exists, err := x.ov.Read(op.File)
		if err != nil {
			return err
		}
		if exists && bytes.Equal(cur, data) {
			return nil
		}
		if exists && !op.Overwrite {
			return x.precondition(op, "%s already exists (set overwrite to replace it)", op.File)
		}
		x.note("%s from the %s CD", op.Source, from)
		return x.ov.Write(op.File, data)

	case "reg.delete":
		d, err := reg.NewDelete(op.Path, op.Name)
		if err != nil {
			return err
		}
		x.rc.dels = append(x.rc.dels, d)
		return nil
	}
	return fmt.Errorf("unknown op %q", op.Op)
}

func (x *opCtx) fromMedia(which, name string) ([]byte, string, error) {
	keys := []string{which}
	switch which {
	case "":
		keys = []string{"98se"}
	case "any":
		keys = []string{"98se", "98", "me"}
	}
	var tried []string
	for _, k := range keys {
		src, ok := x.media[k]
		if !ok {
			tried = append(tried, k+" (not provided)")
			continue
		}
		b, err := src.ReadFile(name)
		if err == nil {
			return b, k, nil
		}
		tried = append(tried, k+": "+err.Error())
	}
	return nil, "", fmt.Errorf("%s not available from media [%s]; pass the CD image with --media", name, strings.Join(tried, "; "))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// patternIndex returns the offsets of every non-overlapping match.
func patternIndex(b []byte, p []module.PatternByte) []int {
	var out []int
	for i := 0; i+len(p) <= len(b); i++ {
		ok := true
		for j, pb := range p {
			if !pb.Any && b[i+j] != pb.B {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
			i += len(p) - 1
		}
	}
	return out
}
