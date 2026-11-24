// Command w9xm is the patch-module engine CLI: plan/apply/rollback modules on a
// mounted Windows 98 SE system drive, validate modules, and make deltas.
// It builds for Windows, macOS and Linux with no external dependencies.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/win9x-modern/w9xm/internal/delta"
	"github.com/win9x-modern/w9xm/internal/engine"
	"github.com/win9x-modern/w9xm/internal/media"
	"github.com/win9x-modern/w9xm/internal/module"
)

var version = "0.1.0-dev"

const usage = `w9xm %s: patch-module engine for Windows 98 SE on modern hardware

Usage:
  w9xm plan     --target DIR (--edition NAME|PATH | --module ID ...) [options]
  w9xm apply    --target DIR (--edition NAME|PATH | --module ID ...) [options]
  w9xm rollback --target DIR [--tx ID] [--force]
  w9xm status   --target DIR
  w9xm modules  list|validate [--modules DIR] [--editions DIR]
  w9xm media    info ISO
  w9xm media    ls ISO [GLOB...] [--all]
  w9xm media    extract ISO --out DIR [GLOB...] [--all] [--by-cab]
  w9xm iso      ls ISO [PATH]
  w9xm iso      get ISO PATH OUT
  w9xm cab      ls CAB...
  w9xm cab      extract --out DIR CAB... [--match GLOB]
  w9xm lab      install --iso 98SE.ISO [--accept-eula] [--key-file F] [--out DIR] [--show]
  w9xm lab      install --resume --iso 98SE.ISO   (continue an interrupted install)
  w9xm lab      finalize --iso 98SE.ISO [--out DIR]   (recover an interrupted install)
  w9xm lab      test (--edition E | --module ID ...) [--out DIR] [--show]
  w9xm delta    make ORIGINAL PATCHED OUT.w9xd
  w9xm delta    apply ORIGINAL PATCH OUT
  w9xm delta    info PATCH

Plan/apply options:
  --modules DIR    module directory (default: modules)
  --editions DIR   edition directory, used when --edition is a name (default: editions)
  --facts FILE     hardware facts JSON from the installer's scan
  --set MOD.PARAM=VALUE  override a parameter (repeatable)
  --windir DIR     Windows directory on the target (default: WINDOWS)
  --media ISO      your original 98 SE / Me CD image, for modules that pull
                   files from it (repeatable)
  --reapply        re-run modules already applied at the same version
  --quiet          (plan) omit per-file diffs

--target is where the Win98 C: drive is: a mounted folder (/Volumes/WIN98,
/mnt/c98, E:\) or a raw disk image file (first FAT partition is used).

lab: installs 98 SE unattended in QEMU from your CD (key read from
product-key.txt next to the ISO), then boots patched copies and checks the
result inside Windows. Needs qemu-system-i386.

media: GLOBs match file names or DIR/CABINET/NAME paths, case-insensitively
(e.g. "*.vxd", "win98/*/usb*"). Without --all only the OS setup cabinets
(WIN98\ or WIN9X\) are searched. Flags may appear anywhere.
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "plan":
		err = cmdPlan(args, false)
	case "apply":
		err = cmdPlan(args, true)
	case "rollback":
		err = cmdRollback(args)
	case "status":
		err = cmdStatus(args)
	case "modules":
		err = cmdModules(args)
	case "delta":
		err = cmdDelta(args)
	case "media":
		err = cmdMedia(args)
	case "iso":
		err = cmdISO(args)
	case "cab":
		err = cmdCab(args)
	case "lab":
		err = cmdLab(args)
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
	default:
		err = fmt.Errorf("unknown command %q (see w9xm help)", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdPlan(args []string, apply bool) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	target := fs.String("target", "", "")
	modDir := fs.String("modules", "modules", "")
	edDir := fs.String("editions", "editions", "")
	edition := fs.String("edition", "", "")
	factsPath := fs.String("facts", "", "")
	winDir := fs.String("windir", "WINDOWS", "")
	reapply := fs.Bool("reapply", false, "")
	quiet := fs.Bool("quiet", false, "")
	var mods, sets, mediaPaths multi
	fs.Var(&mods, "module", "")
	fs.Var(&sets, "set", "")
	fs.Var(&mediaPaths, "media", "")
	parseAll(fs, args)
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	if *edition == "" && len(mods) == 0 {
		return fmt.Errorf("give --edition or at least one --module")
	}

	all, err := module.LoadDir(*modDir)
	if err != nil {
		return err
	}
	params := map[string]map[string]string{}
	wanted := []string(mods)
	if *edition != "" {
		path := *edition
		if !strings.ContainsAny(path, `/\`) && !strings.HasSuffix(path, ".json") {
			path = *edDir + "/" + path
		}
		ed, err := module.LoadEdition(path)
		if err != nil {
			return err
		}
		if params, err = ed.ParamOverrides(); err != nil {
			return err
		}
		wanted = append(ed.Modules, wanted...)
		fmt.Printf("Edition: %s (%s)\n", ed.Name, ed.ID)
	}
	for _, s := range sets {
		kv, val, ok := strings.Cut(s, "=")
		mod, p, ok2 := strings.Cut(kv, ".")
		if !ok || !ok2 {
			return fmt.Errorf("--set %q: want MODULE.PARAM=VALUE", s)
		}
		if params[mod] == nil {
			params[mod] = map[string]string{}
		}
		params[mod][p] = val
	}
	ordered, err := module.Resolve(all, wanted)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, m := range ordered {
		selected[m.ID] = true
	}
	for id := range params {
		if !selected[id] {
			return fmt.Errorf("parameter set for module %s, which is not selected", id)
		}
	}

	var facts *module.Facts
	if *factsPath != "" {
		if facts, err = module.LoadFacts(*factsPath); err != nil {
			return err
		}
		fmt.Printf("Hardware: %d MB RAM, %d MHz CPU, %d PCI devices\n", facts.RAMMB, facts.CPUMHz, len(facts.PCI))
	} else {
		fmt.Println("Hardware: unknown (no --facts); hardware-specific modules follow their if_unknown rule")
	}

	srcs := map[string]engine.MediaSource{}
	for _, mp := range mediaPaths {
		md, err := media.Open(mp)
		if err != nil {
			return err
		}
		defer md.Close()
		if md.Key() == "" {
			return fmt.Errorf("%s: not a recognised Windows 98/Me CD", mp)
		}
		srcs[md.Key()] = md
		fmt.Printf("Media: %s (%s)\n", md.Product, mp)
	}

	tgt, err := engine.OpenTarget(*target)
	if err != nil {
		return err
	}
	defer tgt.Close()
	fmt.Printf("Target: %s\n", tgt)

	plan, err := engine.NewPlan(engine.Options{
		Target: tgt, WinDir: *winDir, Facts: facts, Params: params,
		Reapply: *reapply, Tool: "w9xm " + version, Media: srcs,
	}, ordered)
	if err != nil {
		return err
	}
	printPlan(plan, !*quiet && !apply)
	if !apply {
		fmt.Println("\nDry run: nothing was written. Run `w9xm apply` with the same arguments to commit.")
		return nil
	}
	j, err := plan.Apply()
	if err != nil {
		return err
	}
	if j == nil {
		fmt.Println("\nNothing to apply.")
		return nil
	}
	fmt.Printf("\nApplied as transaction %s (%d files). Undo with: w9xm rollback --target %s --tx %s\n",
		j.ID, len(j.Files), *target, j.ID)
	return nil
}

func printPlan(p *engine.Plan, diffs bool) {
	fmt.Println("\nModules to apply:")
	if len(p.Modules) == 0 {
		fmt.Println("  (none)")
	}
	for _, m := range p.Modules {
		var ps []string
		for k, v := range m.Params {
			ps = append(ps, k+"="+v)
		}
		sort.Strings(ps)
		extra := ""
		if len(ps) > 0 {
			extra = "  [" + strings.Join(ps, " ") + "]"
		}
		fmt.Printf("  + %s %s: %s%s\n", m.Module.ID, m.Module.Version, m.Module.Name, extra)
		for _, n := range m.Notes {
			fmt.Printf("      note: %s\n", n)
		}
	}
	if len(p.Skipped) > 0 {
		fmt.Println("\nSkipped:")
		for _, s := range p.Skipped {
			fmt.Printf("  - %s: %s\n", s.ID, s.Reason)
		}
	}
	fmt.Printf("\nFile changes (%d):\n", len(p.Changes))
	for _, c := range p.Changes {
		fmt.Printf("  %s %s\n", map[string]string{"add": "A", "modify": "M", "delete": "D"}[c.Kind], c.Path)
		if diffs {
			for _, l := range strings.Split(strings.TrimRight(c.Describe(), "\n"), "\n") {
				fmt.Printf("      %s\n", l)
			}
		}
	}
}

func cmdRollback(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	target := fs.String("target", "", "")
	tx := fs.String("tx", "", "")
	force := fs.Bool("force", false, "")
	parseAll(fs, args)
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	tgt, err := engine.OpenTarget(*target)
	if err != nil {
		return err
	}
	defer tgt.Close()
	j, err := engine.Rollback(tgt, *tx, *force)
	if err != nil {
		return err
	}
	fmt.Printf("Rolled back transaction %s (%d files restored).\n", j.ID, len(j.Files))
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	target := fs.String("target", "", "")
	parseAll(fs, args)
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	tgt, err := engine.OpenTarget(*target)
	if err != nil {
		return err
	}
	defer tgt.Close()
	h, err := engine.LoadHistory(tgt)
	if err != nil {
		return err
	}
	if len(h.Journals) == 0 {
		fmt.Println("No transactions recorded on this target.")
		return nil
	}
	fmt.Println("Transactions:")
	for _, j := range h.Journals {
		var ids []string
		for _, m := range j.Modules {
			ids = append(ids, m.ID)
		}
		fmt.Printf("  %s  %-11s %2d files  %s\n", j.ID, j.Status, len(j.Files), strings.Join(ids, ", "))
	}
	fmt.Println("\nApplied modules:")
	applied := h.Applied()
	var ids []string
	for id := range applied {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %s %s (tx %s)\n", id, applied[id].Version, applied[id].Tx)
	}
	return nil
}

func cmdModules(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("modules: want list or validate")
	}
	fs := flag.NewFlagSet("modules", flag.ExitOnError)
	modDir := fs.String("modules", "modules", "")
	edDir := fs.String("editions", "editions", "")
	parseAll(fs, args[1:])
	all, err := module.LoadDir(*modDir)
	switch args[0] {
	case "list":
		if err != nil {
			return err
		}
		var ids []string
		for id := range all {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			m := all[id]
			fmt.Printf("%-24s %-8s %-12s %s\n", id, m.Version, m.Category, m.Name)
		}
		return nil
	case "validate":
		if err != nil {
			return fmt.Errorf("module errors:\n%v", err)
		}
		fmt.Printf("%d modules OK\n", len(all))
		ents, _ := os.ReadDir(*edDir)
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			ed, err := module.LoadEdition(*edDir + "/" + e.Name())
			if err != nil {
				return err
			}
			if err := ed.Check(all); err != nil {
				return err
			}
			fmt.Printf("edition %s OK (%d modules)\n", ed.ID, len(ed.Modules))
		}
		return nil
	}
	return fmt.Errorf("modules: unknown subcommand %q", args[0])
}

func cmdDelta(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("delta: want make, apply or info")
	}
	switch {
	case args[0] == "make" && len(args) == 4:
		src, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		dst, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		p, err := delta.Make(src, dst)
		if err != nil {
			return err
		}
		if err := os.WriteFile(args[3], p, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n  sha256 before: %s\n  sha256 after:  %s\n",
			args[3], len(p), module.SHA256(src), module.SHA256(dst))
		return nil
	case args[0] == "apply" && len(args) == 4:
		src, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		p, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		var out []byte
		if delta.IsBSDiff(p) {
			out, err = delta.BSPatch(src, p)
		} else {
			out, err = delta.Apply(src, p)
		}
		if err != nil {
			return err
		}
		return os.WriteFile(args[3], out, 0o644)
	case args[0] == "info" && len(args) == 2:
		p, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		if delta.IsBSDiff(p) {
			fmt.Println("format: BSDIFF40 (no embedded checksums)")
			return nil
		}
		h, err := delta.ReadHeader(p)
		if err != nil {
			return err
		}
		fmt.Printf("format: W9XD v1\nsource: %d bytes, sha256 %x\nresult: %d bytes, sha256 %x\n",
			h.SrcSize, h.SrcSHA, h.DstSize, h.DstSHA)
		return nil
	}
	return fmt.Errorf("delta: bad arguments (see w9xm help)")
}
