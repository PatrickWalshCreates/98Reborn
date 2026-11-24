//go:build linux

package install

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// registerBoot adds a "98Reborn Boot" UEFI boot entry for \EFI\BOOT\BOOTX64.EFI
// on partition part of disk, first in the boot order. Entries of other
// systems are left as they are.
func registerBoot(disk string, part int) error {
	if _, err := os.Stat("/sys/firmware/efi/efivars"); err != nil {
		return fmt.Errorf("not started in UEFI mode")
	}
	syscall.Mount("efivarfs", "/sys/firmware/efi/efivars", "efivarfs", 0, "")
	// Replace an older 98Reborn entry instead of piling them up.
	out, _ := exec.Command("efibootmgr").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "Boot") && strings.Contains(l, "98Reborn Boot") && len(l) > 8 {
			exec.Command("efibootmgr", "-b", l[4:8], "-B").Run()
		}
	}
	cmd := exec.Command("efibootmgr", "-c", "-d", disk, "-p", strconv.Itoa(part), "-L", "98Reborn Boot", "-l", `\EFI\BOOT\BOOTX64.EFI`)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}
