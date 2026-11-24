package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/win9x-modern/w9xm/internal/compose"
)

// cmdMakeInstaller builds the bootable 98Reborn installer ISO from the base
// image and the user's own Windows 98 SE CD.
func cmdMakeInstaller(args []string) error {
	fs := flag.NewFlagSet("make-installer", flag.ContinueOnError)
	base := fs.String("base", "", "98Reborn installer base image (98reborn-base.iso)")
	cd := fs.String("cd", "", "your Windows 98 SE CD image")
	out := fs.String("out", "98Reborn-installer.iso", "output ISO")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *cd == "" {
		return errors.New("make-installer needs --base and --cd")
	}
	err := compose.Compose(compose.Options{Base: *base, CD: *cd, Out: *out,
		Log: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }})
	if err != nil {
		return err
	}
	fi, _ := os.Stat(*out)
	fmt.Printf("wrote %s (%d MB). Write it to a USB stick with Rufus (ISO mode) and boot the target PC from it.\n", *out, fi.Size()>>20)
	return nil
}
