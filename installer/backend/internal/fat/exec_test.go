package fat

import "os/exec"

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
