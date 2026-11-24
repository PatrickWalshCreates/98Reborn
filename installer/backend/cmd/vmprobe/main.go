// vmprobe: boot a disk/floppy in QEMU and take screenshots at fixed times (debug aid).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/win9x-modern/w9xm/internal/lab"
)

func main() {
	disk, floppy, out := os.Args[1], os.Args[2], os.Args[3]
	os.MkdirAll(out, 0o755)
	q, _ := lab.FindQEMU("")
	vm, err := lab.StartVM(lab.VMConfig{QEMU: q, Disk: disk, Floppy: floppy, BootFloppy: floppy != "", LogDir: out})
	if err != nil {
		panic(err)
	}
	start := time.Now()
	for _, a := range os.Args[4:] {
		s, _ := strconv.Atoi(a)
		time.Sleep(time.Until(start.Add(time.Duration(s) * time.Second)))
		p := filepath.Join(out, fmt.Sprintf("t%03d.png", s))
		vm.Screenshot(p)
		fmt.Println("shot", p)
	}
	fmt.Printf("serial: %q\n", vm.Serial())
	vm.Kill()
}
