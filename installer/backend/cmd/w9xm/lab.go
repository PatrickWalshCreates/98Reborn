package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/engine"
	"github.com/win9x-modern/w9xm/internal/lab"
	"github.com/win9x-modern/w9xm/internal/media"
	"github.com/win9x-modern/w9xm/internal/module"
)

func cmdLab(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("lab: want install or test")
	}
	fs := flag.NewFlagSet("lab", flag.ExitOnError)
	iso := fs.String("iso", "", "")
	keyFile := fs.String("key-file", "", "")
	out := fs.String("out", "lab", "")
	qemu := fs.String("qemu", "", "")
	show := fs.Bool("show", false, "")
	diskMB := fs.Int("disk-mb", 2048, "")
	timeout := fs.Duration("timeout", 0, "")
	acceptEULA := fs.Bool("accept-eula", false, "")
	resume := fs.Bool("resume", false, "")
	modDir := fs.String("modules", "modules", "")
	edDir := fs.String("editions", "editions", "")
	edition := fs.String("edition", "", "")
	factsPath := fs.String("facts", "", "")
	var mods, sets, mediaPaths multi
	fs.Var(&mods, "module", "")
	fs.Var(&sets, "set", "")
	fs.Var(&mediaPaths, "media", "")
	parseAll(fs, args[1:])

	switch args[0] {
	case "install":
		if *iso == "" {
			return fmt.Errorf("lab install: --iso is required (your Windows 98 SE CD image)")
		}
		if *keyFile == "" {
			*keyFile = filepath.Join(filepath.Dir(*iso), "product-key.txt")
			for _, alt := range []string{"product-key.txt", "Win98Key.rtf", "Win98Key.txt", "Win98Key"} {
				if _, err := os.Stat(filepath.Join(filepath.Dir(*iso), alt)); err == nil {
					*keyFile = filepath.Join(filepath.Dir(*iso), alt)
					break
				}
			}
		}
		key, err := lab.ReadProductKey(*keyFile)
		if err != nil {
			return err
		}
		start := time.Now()
		if err := lab.Install(lab.InstallOptions{ISO: *iso, ProductKey: key, Out: *out, QEMU: *qemu,
			DiskMB: *diskMB, Show: *show, Timeout: *timeout, AcceptEULA: *acceptEULA, Resume: *resume}, os.Stdout); err != nil {
			return err
		}
		fmt.Printf("Golden image ready: %s (took %v)\n", filepath.Join(*out, "golden.img"), time.Since(start).Round(time.Second))
		return nil

	case "finalize":
		if *iso == "" {
			return fmt.Errorf("lab finalize: --iso is required")
		}
		m, err := media.Open(*iso)
		if err != nil {
			return err
		}
		defer m.Close()
		if err := lab.Finalize(*out, m); err != nil {
			return err
		}
		fmt.Printf("Golden image ready: %s\n", filepath.Join(*out, "golden.img"))
		return nil

	case "test":
		golden := filepath.Join(*out, "golden.img")
		if _, err := os.Stat(golden); err != nil {
			return lab.ErrNoGolden
		}
		all, err := module.LoadDir(*modDir)
		if err != nil {
			return err
		}
		params := map[string]map[string]string{}
		wanted := []string(mods)
		if *edition != "" {
			p := *edition
			if !strings.ContainsAny(p, `/\`) {
				p = filepath.Join(*edDir, p)
			}
			ed, err := module.LoadEdition(p)
			if err != nil {
				return err
			}
			if params, err = ed.ParamOverrides(); err != nil {
				return err
			}
			wanted = append(ed.Modules, wanted...)
		}
		if len(wanted) == 0 {
			return fmt.Errorf("lab test: give --edition or --module")
		}
		for _, s := range sets {
			kv, val, _ := strings.Cut(s, "=")
			m, p, _ := strings.Cut(kv, ".")
			if params[m] == nil {
				params[m] = map[string]string{}
			}
			params[m][p] = val
		}
		ordered, err := module.Resolve(all, wanted)
		if err != nil {
			return err
		}
		var facts *module.Facts
		if *factsPath != "" {
			if facts, err = module.LoadFacts(*factsPath); err != nil {
				return err
			}
		}
		srcs := map[string]engine.MediaSource{}
		for _, mp := range mediaPaths {
			md, err := media.Open(mp)
			if err != nil {
				return err
			}
			defer md.Close()
			srcs[md.Key()] = md
		}
		res, err := lab.Test(lab.TestOptions{Golden: golden, Out: *out, QEMU: *qemu, Modules: ordered,
			Params: params, Facts: facts, Media: srcs, Show: *show, Timeout: *timeout}, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Println("\nResults:")
		for _, c := range res.Checks {
			mark := "PASS"
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Printf("  %s  %s", mark, c.What)
			if c.Info != "" {
				fmt.Printf("  (%s)", c.Info)
			}
			fmt.Println()
		}
		fmt.Printf("\nRun directory: %s\n", res.RunDir)
		if !res.Passed() {
			return fmt.Errorf("lab test failed")
		}
		return nil
	}
	return fmt.Errorf("lab: unknown subcommand %q", args[0])
}
