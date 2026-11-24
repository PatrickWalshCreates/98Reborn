package baseimage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Third-party programs shipped with 98Reborn Boot, pinned by checksum. Both
// are free software; NOTICE.txt on the medium says where their source is.
type vendorFile struct {
	Name, URL, SHA256 string
}

var vendorFiles = []vendorFile{
	// ReBIOS v0.12.0 (GPL-2.0): SeaBIOS under a minimal hypervisor.
	{"rebios.efi", "https://github.com/ReBIOS-Project/ReBIOS/releases/download/v0.12.0/rebios.efi",
		"85171815137fb5d78ef3b9dfe4da4f0ab68fc2fdebd75fbe1e9cb0409c5a4e3b"},
	// CSMWrap 3.1.2 (LGPL-2.1): SeaBIOS as a CSM, for PCs without VT-x/IOMMU.
	{"csmwrapx64.efi", "https://github.com/CSMWrap/CSMWrap/releases/download/3.1.2/csmwrapx64.efi",
		"96fdb387e177c6340287b7e07713dc09bf1f965dd404311eafd9c6eb99a02745"},
}

const bootNotice = `98Reborn Boot
=============

98reborn-boot.efi is 98Reborn's UEFI boot menu (source: installer/boot in
https://github.com/PatrickWalshCreates/98Reborn). It contains the Terminus
font by Dimitar Toshkov Zhekov, under the SIL Open Font License 1.1.

To give Windows 98 a legacy BIOS on UEFI-only PCs it starts one of:

- ReBIOS v0.12.0, GPL-2.0. Source: https://github.com/ReBIOS-Project/ReBIOS
  (release tarball rebios-0.12.0.tar.xz carries every dependency).
- CSMWrap 3.1.2, LGPL-2.1. Source: https://github.com/CSMWrap/CSMWrap
  (tag 3.1.2).

Both are unmodified release binaries, verified by SHA-256 when this image
was built.
`

// fetchVendor returns a pinned file from the cache or its release URL.
func fetchVendor(cacheDir string, v vendorFile) ([]byte, error) {
	cache := filepath.Join(cacheDir, v.SHA256[:16]+"-"+v.Name)
	if b, err := os.ReadFile(cache); err == nil && sum(b) == v.SHA256 {
		return b, nil
	}
	resp, err := http.Get(v.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", v.URL, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if got := sum(b); got != v.SHA256 {
		return nil, fmt.Errorf("%s: checksum %s, expected %s", v.Name, got, v.SHA256)
	}
	os.MkdirAll(cacheDir, 0o755)
	os.WriteFile(cache, b, 0o644)
	return b, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
