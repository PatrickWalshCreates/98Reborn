//go:build !windows && !linux

package scan

import (
	"errors"

	"github.com/win9x-modern/w9xm/internal/compat"
)

func run() (*compat.Scan, error) {
	return nil, errors.New("run the scanner on the PC you want to install on, under Windows or Linux (or use --from with a saved scan)")
}
