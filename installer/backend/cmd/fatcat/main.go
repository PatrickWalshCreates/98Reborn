// fatcat: print a file from a FAT disk image (read-only debug aid).
package main

import (
	"errors"
	"os"

	"github.com/win9x-modern/w9xm/internal/fat"
)

type ro struct{ f *os.File }

func (r ro) ReadAt(p []byte, o int64) (int, error) { return r.f.ReadAt(p, o) }
func (ro) WriteAt(p []byte, o int64) (int, error)  { return 0, errors.New("read-only") }

func main() {
	fh, _ := os.Open(os.Args[1])
	v, err := fat.OpenImage(ro{fh})
	if err != nil {
		panic(err)
	}
	if len(os.Args) == 2 {
		ents, _ := v.ReadDir("")
		for _, e := range ents {
			os.Stdout.WriteString(e.Name + "\n")
		}
		return
	}
	b, err := v.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	os.Stdout.Write(b)
}
