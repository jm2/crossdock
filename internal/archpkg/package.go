// Package archpkg reads pacman packages and writes pacman repo databases.
package archpkg

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jm2/crossdock/internal/compress"
)

// Suffixes are the package file extensions crossdock picks up.
var Suffixes = []string{".pkg.tar.zst", ".pkg.tar.xz", ".pkg.tar.gz", ".pkg.tar.bz2", ".pkg.tar"}

// Package is a parsed pacman package.
type Package struct {
	Path string
	// Info holds .PKGINFO keys; every key may repeat.
	Info map[string][]string
	// Files lists the package's paths (directories end in "/"), sorted.
	Files []string
}

func (p *Package) one(k string) string {
	if v := p.Info[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (p *Package) Name() string    { return p.one("pkgname") }
func (p *Package) Version() string { return p.one("pkgver") }
func (p *Package) Arch() string    { return p.one("arch") }

// Base is pkgbase, defaulting to pkgname.
func (p *Package) Base() string {
	if b := p.one("pkgbase"); b != "" {
		return b
	}
	return p.Name()
}

// ReadPackage parses .PKGINFO and the file list of a package.
func ReadPackage(path string) (*Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dr, err := compress.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pkg := &Package{Path: path}
	tr := tar.NewReader(dr)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			dr.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name == ".PKGINFO" {
			var b bytes.Buffer
			if _, err := io.Copy(&b, io.LimitReader(tr, 16<<20)); err != nil {
				dr.Close()
				return nil, err
			}
			if pkg.Info, err = parseInfo(b.Bytes()); err != nil {
				dr.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			continue
		}
		// Like repo-add: skip top-level metadata (.BUILDINFO, .MTREE, ...).
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if h.Typeflag == tar.TypeDir && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		if !seen[name] {
			seen[name] = true
			pkg.Files = append(pkg.Files, name)
		}
	}
	if err := dr.Close(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if pkg.Info == nil {
		return nil, fmt.Errorf("%s: no .PKGINFO", path)
	}
	if pkg.Name() == "" || pkg.Version() == "" || pkg.Arch() == "" {
		return nil, fmt.Errorf("%s: .PKGINFO lacks pkgname, pkgver or arch", path)
	}
	sort.Strings(pkg.Files)
	return pkg, nil
}

func parseInfo(b []byte) (map[string][]string, error) {
	info := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, " = ")
		if !ok {
			// "key =" with an empty value
			if strings.HasSuffix(t, " =") {
				k, v = strings.TrimSuffix(t, " ="), ""
			} else {
				return nil, fmt.Errorf(".PKGINFO: malformed line %q", t)
			}
		}
		info[k] = append(info[k], v)
	}
	if len(info) == 0 {
		return nil, errors.New(".PKGINFO is empty")
	}
	return info, sc.Err()
}
