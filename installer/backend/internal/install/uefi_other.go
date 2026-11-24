//go:build !linux

package install

import "errors"

func registerBoot(disk string, part int) error {
	return errors.New("UEFI boot entries can only be added from the 98Reborn installer")
}
