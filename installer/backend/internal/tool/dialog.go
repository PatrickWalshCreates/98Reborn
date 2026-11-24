package tool

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Native file dialogs, so the page gets real paths (a browser file picker
// only gives the file's contents).

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", nil // cancelled
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func asQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func openDialog(prompt string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return run("osascript", "-e", "POSIX path of (choose file with prompt "+asQuote(prompt)+")")
	case "windows":
		return run("powershell", "-NoProfile", "-STA", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.OpenFileDialog; "+
				"$d.Title="+psQuote(prompt)+"; $d.Filter='CD images (*.iso)|*.iso|All files (*.*)|*.*'; "+
				"if($d.ShowDialog() -eq 'OK'){$d.FileName}")
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			return run(p, "--file-selection", "--title="+prompt, "--file-filter=CD images | *.iso *.ISO", "--file-filter=All files | *")
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			return run(p, "--getopenfilename", ".", "*.iso *.ISO", "--title", prompt)
		}
		return "", errors.New("no file dialog available: type the path instead")
	}
}

func saveDialog(prompt, name, dir string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		script := "POSIX path of (choose file name with prompt " + asQuote(prompt) + " default name " + asQuote(name)
		if dir != "" {
			script += " default location (POSIX file " + asQuote(dir) + ")"
		}
		return run("osascript", "-e", script+")")
	case "windows":
		return run("powershell", "-NoProfile", "-STA", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.SaveFileDialog; "+
				"$d.Title="+psQuote(prompt)+"; $d.FileName="+psQuote(name)+"; $d.InitialDirectory="+psQuote(dir)+"; "+
				"$d.Filter='ISO image (*.iso)|*.iso'; if($d.ShowDialog() -eq 'OK'){$d.FileName}")
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			return run(p, "--file-selection", "--save", "--confirm-overwrite", "--title="+prompt, "--filename="+filepath.Join(dir, name))
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			return run(p, "--getsavefilename", filepath.Join(dir, name), "*.iso", "--title", prompt)
		}
		return "", errors.New("no file dialog available: type the path instead")
	}
}

func revealFile(p string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", p).Run()
	case "windows":
		return exec.Command("explorer", "/select,", p).Run()
	default:
		return exec.Command("xdg-open", filepath.Dir(p)).Run()
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
