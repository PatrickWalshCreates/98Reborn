// 98reborn-macapp packages the 98Reborn Tool as a macOS app bundle:
// "98Reborn Tool.app" with a universal (Apple Silicon + Intel) binary, an
// icon and an Info.plist. Signing and zipping are left to the caller
// (codesign and ditto, on macOS):
//
//	98reborn-macapp --arm64 dist/98reborn-tool-darwin-arm64 --amd64 dist/98reborn-tool-darwin-amd64 \
//	    --version 0.1.0 --out dist/macapp
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/win9x-modern/w9xm/internal/gui"
)

const exeName = "98reborn-tool"

func main() {
	arm := flag.String("arm64", "", "darwin/arm64 binary")
	amd := flag.String("amd64", "", "darwin/amd64 binary")
	version := flag.String("version", "0.1.0-dev", "version")
	out := flag.String("out", "dist/macapp", "output directory")
	flag.Parse()
	if *arm == "" || *amd == "" {
		flag.Usage()
		os.Exit(2)
	}
	app := filepath.Join(*out, "98Reborn Tool.app", "Contents")
	for _, d := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(app, d), 0o755); err != nil {
			die(err)
		}
	}
	a, err := os.ReadFile(*arm)
	if err != nil {
		die(err)
	}
	b, err := os.ReadFile(*amd)
	if err != nil {
		die(err)
	}
	if err := os.WriteFile(filepath.Join(app, "MacOS", exeName), universal(a, b), 0o755); err != nil {
		die(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Resources", "AppIcon.icns"), icns(), 0o644); err != nil {
		die(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Info.plist"), []byte(fmt.Sprintf(plist, *version, *version)), 0o644); err != nil {
		die(err)
	}
	fmt.Println("wrote", filepath.Join(*out, "98Reborn Tool.app"))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "98reborn-macapp:", err)
	os.Exit(1)
}

// LSUIElement: no Dock icon or menu bar; the Tool's window is the browser
// page, and it quits by itself when the page closes.
const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>98Reborn Tool</string>
	<key>CFBundleDisplayName</key><string>98Reborn Tool</string>
	<key>CFBundleIdentifier</key><string>org.98reborn.tool</string>
	<key>CFBundleExecutable</key><string>` + exeName + `</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>%s</string>
	<key>CFBundleVersion</key><string>%s</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHumanReadableCopyright</key><string>98Reborn contributors. Open source.</string>
</dict>
</plist>
`

// universal joins an arm64 and an x86-64 Mach-O into one fat binary.
func universal(arm, amd []byte) []byte {
	const align = 1 << 14
	type slice struct {
		cpu, sub uint32
		data     []byte
	}
	slices := []slice{{0x01000007, 3, amd}, {0x0100000C, 0, arm}} // CPU_TYPE_X86_64, CPU_TYPE_ARM64
	var hdr bytes.Buffer
	be := binary.BigEndian
	binary.Write(&hdr, be, uint32(0xCAFEBABE))
	binary.Write(&hdr, be, uint32(len(slices)))
	off := uint32(align)
	var offs []uint32
	for _, s := range slices {
		offs = append(offs, off)
		for _, v := range []uint32{s.cpu, s.sub, off, uint32(len(s.data)), 14} {
			binary.Write(&hdr, be, v)
		}
		off += (uint32(len(s.data)) + align - 1) &^ (align - 1)
	}
	// No padding after the last slice: codesign's strict check rejects
	// trailing bytes.
	last := len(slices) - 1
	out := make([]byte, offs[last]+uint32(len(slices[last].data)))
	copy(out, hdr.Bytes())
	for i, s := range slices {
		copy(out[offs[i]:], s.data)
	}
	return out
}

// icon renders the app icon: the 98Reborn monitor on a rounded teal tile.
func icon(size int) []byte {
	c := gui.NewCanvas(size, size)
	m := size / 10 // macOS icon grid: the tile sits inside a margin
	tile := size - 2*m
	r := tile * 22 / 100
	for y := 0; y < tile; y++ {
		col := gui.Mix(0x0f766e, 0x0b3b38, y*255/max(1, tile-1))
		for x := 0; x < tile; x++ {
			dx, dy := 0, 0
			switch {
			case x < r:
				dx = r - x
			case x >= tile-r:
				dx = x - (tile - r - 1)
			}
			switch {
			case y < r:
				dy = r - y
			case y >= tile-r:
				dy = y - (tile - r - 1)
			}
			if dx*dx+dy*dy <= r*r {
				c.Set(m+x, m+y, col)
			}
		}
	}
	ls := tile * 3 / 4
	c.Logo(m+(tile-ls)/2, m+(tile-ls)/2, ls, 0xccfbf1, 0x042f2e, 0x5eead4)
	img := c.Image()
	// Transparent outside the tile.
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if c.Pix[y*size+x] == 0 {
				img.Pix[(y*size+x)*4+3] = 0
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// icns builds an Apple icon file from PNG renditions.
func icns() []byte {
	types := []struct {
		tag  string
		size int
	}{{"icp4", 16}, {"icp5", 32}, {"icp6", 64}, {"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}}
	var body bytes.Buffer
	for _, t := range types {
		p := icon(t.size)
		body.WriteString(t.tag)
		binary.Write(&body, binary.BigEndian, uint32(8+len(p)))
		body.Write(p)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
