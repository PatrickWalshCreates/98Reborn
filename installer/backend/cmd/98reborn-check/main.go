// 98reborn-check scans the PC it runs on (under Windows 10/11 or Linux) and
// grades how well it can run 98Reborn: Gold, Silver, Bronze, In development or
// Not supported, with the reasons why. It changes nothing and needs no
// internet connection or admin rights.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/win9x-modern/w9xm/internal/compat"
	"github.com/win9x-modern/w9xm/internal/scan"
)

var version = "0.1.0-dev"

func main() {
	asJSON := flag.Bool("json", false, "print the full report as JSON")
	from := flag.String("from", "", "grade a saved scan (98reborn-scan.json) instead of this PC")
	save := flag.String("save", "98reborn-scan.json", "where to save the scan and report (\"\" to skip)")
	noPause := flag.Bool("no-pause", false, "don't wait for Enter before exiting (Windows)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	code := run(*asJSON, *from, *save)
	// Double-clicked on Windows: keep the window open so the result can be read.
	if runtime.GOOS == "windows" && !*noPause && !*asJSON && len(os.Args) == 1 {
		fmt.Print("\nPress Enter to close.")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	os.Exit(code)
}

func run(asJSON bool, from, save string) int {
	var s *compat.Scan
	if from != "" {
		b, err := os.ReadFile(from)
		if err == nil {
			var saved struct {
				Scan *compat.Scan `json:"scan"`
			}
			if err = json.Unmarshal(b, &saved); err == nil && saved.Scan == nil {
				err = fmt.Errorf("%s has no scan", from)
			}
			s = saved.Scan
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	} else {
		if !asJSON {
			fmt.Println("98Reborn compatibility check", version)
			fmt.Println("Scanning this PC (read-only, nothing is changed or sent anywhere)...")
			fmt.Println()
		}
		var err error
		if s, err = scan.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	r := compat.Evaluate(s)
	out, _ := json.MarshalIndent(struct {
		Tool string `json:"tool"`
		*compat.Report
		Facts any `json:"facts"`
	}{"98reborn-check " + version, r, s.Facts()}, "", "  ")
	if asJSON {
		fmt.Println(string(out))
	} else {
		fmt.Print(r.Text())
	}
	if save != "" && from == "" {
		if err := os.WriteFile(save, out, 0o644); err == nil && !asJSON {
			fmt.Printf("\nSaved the full results to %s (attach it to bug reports; the installer can use it too).\n", save)
		}
	}
	return 0
}
