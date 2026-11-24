// 98reborn is the 98Reborn Tool: double-click it, and it opens in your web
// browser to check a PC and build the 98Reborn installer ISO from your own
// Windows 98 SE CD. It runs on Windows, macOS and Linux and only listens on
// this computer (127.0.0.1).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/win9x-modern/w9xm/internal/tool"
)

var version = "0.1.0-dev"

func main() {
	base := flag.String("base", "", "use this installer base image instead of downloading one")
	noBrowser := flag.Bool("no-browser", false, "don't open the browser; just print the link")
	flag.Parse()
	cache, _ := os.UserCacheDir()
	s := tool.New(version, &tool.BaseStore{Override: *base, Dir: filepath.Join(cache, "98reborn", "base")})
	url, err := s.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "98Reborn Tool:", err)
		os.Exit(1)
	}
	fmt.Printf("98Reborn Tool %s is running.\nIf your browser didn't open, go to:\n  %s\nClose this window (or click Quit in the page) to stop it.\n", version, url)
	if !*noBrowser {
		tool.OpenBrowser(url)
	}
	s.Wait()
}
