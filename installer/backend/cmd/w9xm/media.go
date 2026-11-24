package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/win9x-modern/w9xm/internal/cab"
	"github.com/win9x-modern/w9xm/internal/iso9660"
	"github.com/win9x-modern/w9xm/internal/media"
)

// parseAll parses flags appearing anywhere in args and returns the positionals.
func parseAll(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdMedia(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("media: want info, ls or extract")
	}
	fs := flag.NewFlagSet("media", flag.ExitOnError)
	all := fs.Bool("all", false, "")
	byCab := fs.Bool("by-cab", false, "")
	out := fs.String("out", "", "")
	pos := parseAll(fs, args[1:])
	if len(pos) == 0 {
		return fmt.Errorf("media %s: ISO path required", args[0])
	}
	m, err := media.Open(pos[0])
	if err != nil {
		return err
	}
	defer m.Close()
	globs := pos[1:]

	switch args[0] {
	case "info":
		fmt.Printf("Product:    %s\nVolume:     %s (Joliet: %v)\nSetup dir:  %s\n", m.Product, m.ISO.VolumeID, m.ISO.Joliet, m.SetupDir)
		if m.SetupSHA256 != "" {
			fmt.Printf("Release id: sha256(SETUP.EXE) %s\n", m.SetupSHA256)
		}
		fmt.Println("\nCabinets:")
		for _, l := range m.Summary() {
			fmt.Println("  " + l)
		}
		if len(m.NotCabinets) > 0 {
			fmt.Printf("\nIgnored %d .CAB files that are not Microsoft cabinets.\n", len(m.NotCabinets))
		}
		for _, w := range m.Warnings {
			fmt.Println("warning:", w)
		}
		return nil

	case "ls":
		files := m.Find(globs, !*all)
		for _, f := range files {
			fmt.Printf("%10d  %s  %s\n", f.Size, f.Modified.Format("2006-01-02 15:04"), f.Path())
		}
		fmt.Fprintf(os.Stderr, "%d files\n", len(files))
		return nil

	case "extract":
		if *out == "" {
			return fmt.Errorf("media extract: --out DIR is required")
		}
		files := m.Find(globs, !*all)
		if len(files) == 0 {
			return fmt.Errorf("no files match")
		}
		written, dupes := 0, 0
		seen := map[string]bool{}
		start := time.Now()
		err := media.Extract(files, func(f *media.File, data []byte) error {
			rel := f.Name
			if *byCab {
				rel = path.Join(f.Dir, f.Cabinet, f.Name)
			}
			key := strings.ToUpper(rel)
			if seen[key] {
				dupes++
				return nil
			}
			seen[key] = true
			if err := writeOut(*out, rel, data, f.Modified); err != nil {
				return err
			}
			written++
			return nil
		})
		fmt.Printf("Extracted %d files to %s in %v.\n", written, *out, time.Since(start).Round(time.Millisecond))
		if dupes > 0 {
			fmt.Printf("Skipped %d files whose name was already extracted from an earlier cabinet (use --by-cab to keep every copy).\n", dupes)
		}
		var xe *cab.ExtractError
		if errors.As(err, &xe) {
			fmt.Println("warning:", err)
			return nil
		}
		return err
	}
	return fmt.Errorf("media: unknown subcommand %q", args[0])
}

// writeOut writes rel (slash path from a CD/cabinet) under dir, refusing traversal.
func writeOut(dir, rel string, data []byte, mod time.Time) error {
	rel = strings.ReplaceAll(rel, `\`, "/")
	for _, part := range strings.Split(rel, "/") {
		if part == ".." || part == "" {
			return fmt.Errorf("refusing unsafe path %q", rel)
		}
	}
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return err
	}
	if !mod.IsZero() {
		os.Chtimes(p, mod, mod)
	}
	return nil
}

func cmdISO(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("iso: want ls ISO [PATH] or get ISO PATH OUT")
	}
	im, err := iso9660.OpenFile(args[1])
	if err != nil {
		return err
	}
	defer im.Close()
	switch {
	case args[0] == "ls":
		p := ""
		if len(args) > 2 {
			p = args[2]
		}
		ents, err := im.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range ents {
			kind := "     "
			if e.IsDir {
				kind = "<DIR>"
			}
			fmt.Printf("%s %10d  %s  %s\n", kind, e.Size, e.ModTime.Format("2006-01-02 15:04"), e.Name)
		}
		return nil
	case args[0] == "get" && len(args) == 4:
		e, err := im.Stat(args[2])
		if err != nil {
			return err
		}
		b, err := im.ReadFile(e.Path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(args[3], b, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%d bytes)\n", args[3], len(b))
		return nil
	}
	return fmt.Errorf("iso: bad arguments (see w9xm help)")
}

func cmdCab(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cab: want ls or extract")
	}
	fs := flag.NewFlagSet("cab", flag.ExitOnError)
	out := fs.String("out", "", "")
	match := fs.String("match", "", "")
	pos := parseAll(fs, args[1:])
	if len(pos) == 0 {
		return fmt.Errorf("cab: give one or more cabinet files (all parts of a multi-cabinet set)")
	}
	var cabs []*cab.Cabinet
	for _, p := range pos {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		st, _ := f.Stat()
		c, err := cab.Parse(f, st.Size(), filepath.Base(p))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		cabs = append(cabs, c)
	}
	sets, err := cab.BuildSets(cabs)
	if err != nil {
		fmt.Println("warning:", err)
	}
	var files []*cab.File
	for _, s := range sets {
		for _, f := range s.Files {
			if *match == "" || cab.Match(*match, f.Name) {
				files = append(files, f)
			}
		}
	}
	switch args[0] {
	case "ls":
		for _, f := range files {
			fmt.Printf("%10d  %s  %s  (%s)\n", f.Size, f.Modified.Format("2006-01-02 15:04"), f.Name, f.Cabinet)
		}
		return nil
	case "extract":
		if *out == "" {
			return fmt.Errorf("cab extract: --out DIR is required")
		}
		n := 0
		err := cab.Extract(files, func(f *cab.File, data []byte) error {
			n++
			return writeOut(*out, f.Name, data, f.Modified)
		})
		fmt.Printf("Extracted %d files to %s.\n", n, *out)
		return err
	}
	return fmt.Errorf("cab: unknown subcommand %q", args[0])
}
