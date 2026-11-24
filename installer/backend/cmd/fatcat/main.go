// fatcat: a debug aid for FAT disk images (the first FAT partition, or a
// floppy image).
//
//	fatcat IMG                 list the root directory
//	fatcat IMG PATH            print a file (a directory is listed)
//	fatcat IMG put SRC PATH    copy a local file in (creates directories)
//	fatcat IMG mkfs MB [esp]   create an MBR disk image with one FAT32
//	                           partition (type 0xEF with "esp")
package main

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/win9x-modern/w9xm/internal/fat"
)

func die(err error) {
	fmt.Fprintln(os.Stderr, "fatcat:", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		die(fmt.Errorf("usage: fatcat IMG [PATH | put SRC PATH]"))
	}
	if len(os.Args) >= 4 && os.Args[2] == "mkfs" {
		mb, _ := strconv.Atoi(os.Args[3])
		size := int64(mb) << 20
		f, err := os.Create(os.Args[1])
		if err != nil {
			die(err)
		}
		defer f.Close()
		f.Truncate(size)
		typ := byte(0x0C)
		if len(os.Args) > 4 && os.Args[4] == "esp" {
			typ = 0xEF
		}
		if err := fat.WriteMBR(f, []fat.Partition{{Type: typ, StartLBA: 2048, Sectors: uint32(size/512 - 2048)}}); err != nil {
			die(err)
		}
		if err := fat.Format32(f, 2048*512, size-2048*512, "DISK"); err != nil {
			die(err)
		}
		return
	}
	put := len(os.Args) == 5 && os.Args[2] == "put"
	flag := os.O_RDONLY
	if put {
		flag = os.O_RDWR
	}
	fh, err := os.OpenFile(os.Args[1], flag, 0)
	if err != nil {
		die(err)
	}
	defer fh.Close()
	v, err := fat.OpenImage(fh)
	if err != nil {
		die(err)
	}
	if put {
		b, err := os.ReadFile(os.Args[3])
		if err != nil {
			die(err)
		}
		dst := os.Args[4]
		if d := path.Dir(dst); d != "." {
			if err := v.MkdirAll(d); err != nil {
				die(err)
			}
		}
		if err := v.WriteFile(dst, b, time.Now()); err != nil {
			die(err)
		}
		if err := v.Sync(); err != nil {
			die(err)
		}
		return
	}
	p := ""
	if len(os.Args) > 2 {
		p = os.Args[2]
	}
	if e, err := v.Stat(p); p == "" || err == nil && e.IsDir() {
		ents, err := v.ReadDir(p)
		if err != nil {
			die(err)
		}
		for _, e := range ents {
			suffix := ""
			if e.IsDir() {
				suffix = "/"
			}
			fmt.Printf("%s%s\t%d\n", e.Name, suffix, e.Size)
		}
		return
	}
	b, err := v.ReadFile(p)
	if err != nil {
		die(err)
	}
	os.Stdout.Write(b)
}
