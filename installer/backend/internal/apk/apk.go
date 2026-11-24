// Package apk downloads and unpacks Alpine Linux packages without Alpine's
// tools, so the installer's base image builds the same way on macOS, Windows,
// Linux and in CI.
//
// Trust chain: the repository index is signed (RSA) by an Alpine key pinned in
// keys/; the index lists each package's control-segment SHA-1; the control
// segment holds the SHA-256 of the data segment. Every download is checked
// against that chain before use.
package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed keys/*.rsa.pub
var keyFS embed.FS

// Package is one entry of a repository index.
type Package struct {
	Name, Version, Repo string
	Deps                []string // raw dependency tokens
	Provides            []string // raw provide tokens
	Checksum            string   // "Q1" + base64(SHA-1 of the control segment)
	Size                int64
	Priority            int // provider_priority
}

func (p *Package) File() string { return p.Name + "-" + p.Version + ".apk" }

// Client fetches indexes and packages from one Alpine branch.
type Client struct {
	Mirror string // e.g. https://dl-cdn.alpinelinux.org/alpine
	Branch string // e.g. v3.24
	Arch   string // e.g. x86_64
	Cache  string // download cache directory
	Log    func(format string, a ...any)

	pkgs     map[string]*Package
	provides map[string][]*Package
}

func (c *Client) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log(f, a...)
	}
}

// segments splits an APK/APKINDEX file into its raw gzip members.
func segments(b []byte) ([][]byte, error) {
	var out [][]byte
	cr := &countReader{b: b}
	for cr.n < len(b) {
		start := cr.n
		z, err := gzip.NewReader(cr)
		if err != nil {
			return nil, fmt.Errorf("gzip member at %d: %w", start, err)
		}
		z.Multistream(false)
		if _, err := io.Copy(io.Discard, z); err != nil {
			return nil, err
		}
		out = append(out, b[start:cr.n])
	}
	return out, nil
}

// countReader is an io.ByteReader so gzip/flate never read past a member.
type countReader struct {
	b []byte
	n int
}

func (r *countReader) Read(p []byte) (int, error) {
	if r.n >= len(r.b) {
		return 0, io.EOF
	}
	k := copy(p, r.b[r.n:])
	r.n += k
	return k, nil
}

func (r *countReader) ReadByte() (byte, error) {
	if r.n >= len(r.b) {
		return 0, io.EOF
	}
	r.n++
	return r.b[r.n-1], nil
}

// tarFiles reads every regular file of a gzip'd tar member.
func tarFiles(seg []byte) (map[string][]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(seg))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			out[h.Name], err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
		}
	}
}

// verifySignature checks the signature member (first) over the next member.
func verifySignature(sig map[string][]byte, signed []byte) error {
	for name, s := range sig {
		hashName, key, ok := strings.Cut(strings.TrimPrefix(name, ".SIGN."), ".")
		if !ok {
			continue
		}
		pemBytes, err := keyFS.ReadFile("keys/" + key)
		if err != nil {
			return fmt.Errorf("signed by unknown key %s", key)
		}
		blk, _ := pem.Decode(pemBytes)
		if blk == nil {
			return fmt.Errorf("bad pinned key %s", key)
		}
		pk, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return err
		}
		pub, ok := pk.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("key %s is not RSA", key)
		}
		switch hashName {
		case "RSA":
			h := sha1.Sum(signed)
			return rsa.VerifyPKCS1v15(pub, crypto.SHA1, h[:], s)
		case "RSA256":
			h := sha256.Sum256(signed)
			return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], s)
		}
	}
	return errors.New("no usable signature")
}

func (c *Client) get(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// LoadIndex downloads, verifies and parses one repository ("main", "community").
func (c *Client) LoadIndex(repo string) error {
	url := fmt.Sprintf("%s/%s/%s/%s/APKINDEX.tar.gz", c.Mirror, c.Branch, repo, c.Arch)
	b, err := c.get(url)
	if err != nil {
		return err
	}
	segs, err := segments(b)
	if err != nil || len(segs) < 2 {
		return fmt.Errorf("%s: malformed index (%v)", url, err)
	}
	sig, err := tarFiles(segs[0])
	if err != nil {
		return err
	}
	if err := verifySignature(sig, bytes.Join(segs[1:], nil)); err != nil {
		return fmt.Errorf("%s: signature: %w", url, err)
	}
	files, err := tarFiles(bytes.Join(segs[1:], nil))
	if err != nil {
		return err
	}
	idx, ok := files["APKINDEX"]
	if !ok {
		return fmt.Errorf("%s: no APKINDEX", url)
	}
	if c.pkgs == nil {
		c.pkgs, c.provides = map[string]*Package{}, map[string][]*Package{}
	}
	n := 0
	for _, rec := range strings.Split(string(idx), "\n\n") {
		p := &Package{Repo: repo}
		for _, l := range strings.Split(rec, "\n") {
			if len(l) < 2 || l[1] != ':' {
				continue
			}
			v := l[2:]
			switch l[0] {
			case 'P':
				p.Name = v
			case 'V':
				p.Version = v
			case 'C':
				p.Checksum = v
			case 'S':
				p.Size, _ = strconv.ParseInt(v, 10, 64)
			case 'D':
				p.Deps = strings.Fields(v)
			case 'p':
				p.Provides = strings.Fields(v)
			case 'k':
				p.Priority, _ = strconv.Atoi(v)
			}
		}
		if p.Name == "" {
			continue
		}
		if _, dup := c.pkgs[p.Name]; dup {
			continue // first repository wins (main before community)
		}
		c.pkgs[p.Name] = p
		for _, pr := range p.Provides {
			name := depName(pr)
			c.provides[name] = append(c.provides[name], p)
		}
		n++
	}
	c.logf("index %s/%s: %d packages", c.Branch, repo, n)
	return nil
}

// depName strips a version constraint: "so:libz.so.1=1.3" -> "so:libz.so.1".
func depName(tok string) string {
	if i := strings.IndexAny(tok, "<>=~"); i > 0 {
		return tok[:i]
	}
	return tok
}

// Lookup finds a package by name or by something it provides.
func (c *Client) Lookup(name string) *Package {
	if p := c.pkgs[name]; p != nil {
		return p
	}
	cands := c.provides[name]
	if len(cands) == 0 {
		return nil
	}
	best := cands[0]
	for _, p := range cands[1:] {
		if p.Priority > best.Priority {
			best = p
		}
	}
	return best
}

// Resolve returns names plus everything they depend on, sorted by name.
// Dependencies listed in skip (names or provides) are left out on purpose.
func (c *Client) Resolve(names []string, skip map[string]bool) ([]*Package, error) {
	seen := map[string]*Package{}
	var visit func(tok, from string) error
	visit = func(tok, from string) error {
		if strings.HasPrefix(tok, "!") {
			return nil // conflict marker
		}
		name := depName(tok)
		if skip[name] {
			return nil
		}
		p := c.Lookup(name)
		if p == nil {
			return fmt.Errorf("no package provides %q (needed by %s)", name, from)
		}
		if skip[p.Name] || seen[p.Name] != nil {
			return nil
		}
		seen[p.Name] = p
		for _, d := range p.Deps {
			if err := visit(d, p.Name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range names {
		if err := visit(n, "request"); err != nil {
			return nil, err
		}
	}
	out := make([]*Package, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Fetch returns the package file, from the cache or the mirror, verified.
func (c *Client) Fetch(p *Package) ([]byte, error) {
	cache := filepath.Join(c.Cache, c.Branch, c.Arch, p.File())
	b, err := os.ReadFile(cache)
	fromCache := err == nil
	if !fromCache {
		url := fmt.Sprintf("%s/%s/%s/%s/%s", c.Mirror, c.Branch, p.Repo, c.Arch, p.File())
		c.logf("download %s (%d KB)", p.File(), p.Size/1024)
		if b, err = c.get(url); err != nil {
			return nil, err
		}
	}
	if err := verifyPackage(p, b); err != nil {
		if fromCache {
			os.Remove(cache)
			return c.Fetch(p)
		}
		return nil, err
	}
	if !fromCache && c.Cache != "" {
		os.MkdirAll(filepath.Dir(cache), 0o755)
		os.WriteFile(cache, b, 0o644)
	}
	return b, nil
}

func verifyPackage(p *Package, b []byte) error {
	segs, err := segments(b)
	if err != nil {
		return fmt.Errorf("%s: %w", p.File(), err)
	}
	ctl := 0 // the control segment follows the signature segment, if any
	if len(segs) >= 3 {
		ctl = 1
	}
	sum := sha1.Sum(segs[ctl])
	if got := "Q1" + base64.StdEncoding.EncodeToString(sum[:]); got != p.Checksum {
		return fmt.Errorf("%s: control checksum %s, index says %s", p.File(), got, p.Checksum)
	}
	files, err := tarFiles(segs[ctl])
	if err != nil {
		return err
	}
	want := ""
	for _, l := range strings.Split(string(files[".PKGINFO"]), "\n") {
		if v, ok := strings.CutPrefix(l, "datahash = "); ok {
			want = strings.TrimSpace(v)
		}
	}
	data := bytes.Join(segs[ctl+1:], nil)
	h := sha256.Sum256(data)
	if want == "" || hex.EncodeToString(h[:]) != want {
		return fmt.Errorf("%s: data hash mismatch", p.File())
	}
	return nil
}

// Entry is one item of a package's file tree.
type Entry struct {
	Path     string // no leading slash
	Mode     int64  // permission bits
	Type     byte   // tar.TypeReg, TypeDir, TypeSymlink, TypeLink
	Link     string
	Data     []byte
	UID, GID int
}

// Unpack calls fn for every entry of the package's data segment. b must have
// passed Fetch's verification.
func Unpack(b []byte, fn func(Entry) error) error {
	segs, err := segments(b)
	if err != nil {
		return err
	}
	first := 1
	if len(segs) >= 3 {
		first = 2
	}
	z, err := gzip.NewReader(bytes.NewReader(bytes.Join(segs[first:], nil)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(bufio.NewReader(z))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		e := Entry{Path: strings.TrimPrefix(strings.TrimSuffix(h.Name, "/"), "./"), Mode: h.Mode & 0o7777,
			Type: h.Typeflag, Link: h.Linkname, UID: h.Uid, GID: h.Gid}
		if e.Path == "" || strings.HasPrefix(e.Path, ".") && !strings.Contains(e.Path, "/") {
			continue // control files, if any
		}
		if h.Typeflag == tar.TypeReg {
			if e.Data, err = io.ReadAll(tr); err != nil {
				return err
			}
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}
