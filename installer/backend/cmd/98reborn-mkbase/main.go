// 98reborn-mkbase builds the 98Reborn installer base image (no Microsoft
// files). It runs on macOS, Windows and Linux; CI runs it for releases.
//
//	98reborn-mkbase --installer dist/98reborn-installer-linux-amd64 --out 98reborn-base.iso
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/win9x-modern/w9xm/internal/apk"
	"github.com/win9x-modern/w9xm/internal/baseimage"
)

func main() {
	installer := flag.String("installer", "", "98reborn-installer binary for linux/amd64 (required)")
	out := flag.String("out", "98reborn-base.iso", "output ISO")
	version := flag.String("version", "0.1.0-dev", "98Reborn version")
	mirror := flag.String("mirror", "https://dl-cdn.alpinelinux.org/alpine", "Alpine mirror")
	branch := flag.String("alpine", "v3.24", "Alpine branch")
	cache := flag.String("cache", "", "package cache (default: user cache dir)")
	modules := flag.String("modules", "../../modules", "modules directory")
	editions := flag.String("editions", "../../editions", "editions directory")
	bootEFI := flag.String("boot-efi", "../boot/out/98reborn-boot.efi", "98Reborn Boot (build with installer/boot/build.sh); \"\" to leave it out")
	flag.Parse()
	if *installer == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *cache == "" {
		d, _ := os.UserCacheDir()
		*cache = filepath.Join(d, "98reborn", "apk")
	}
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	now := time.Now().UTC()
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" { // reproducible builds
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			now = time.Unix(n, 0).UTC()
		}
	}
	err := baseimage.Build(baseimage.Options{
		Client:    &apk.Client{Mirror: *mirror, Branch: *branch, Arch: "x86_64", Cache: *cache, Log: logf},
		Installer: *installer, Modules: *modules, Editions: *editions, BootEFI: *bootEFI, Out: *out, Version: *version, Now: now, Log: logf,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "98reborn-mkbase:", err)
		os.Exit(1)
	}
	fi, _ := os.Stat(*out)
	logf("wrote %s (%d MB)", *out, fi.Size()>>20)
}
