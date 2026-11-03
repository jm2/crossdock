// Package deb reads .deb packages and writes apt repository indexes.
package deb

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/jm2/crossdock/internal/compress"
)

// Package is a parsed .deb.
type Package struct {
	Path    string
	Control Paragraph
	Name    string
	Version string
	Arch    string
	Source  string // source package name, without version
}

// ReadPackage parses a .deb file's control data.
func ReadPackage(p string) (*Package, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ctrl, err := readControl(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	pkg := &Package{
		Path:    p,
		Control: ctrl,
		Name:    ctrl.Get("Package"),
		Version: ctrl.Get("Version"),
		Arch:    ctrl.Get("Architecture"),
	}
	if pkg.Name == "" || pkg.Version == "" || pkg.Arch == "" {
		return nil, fmt.Errorf("%s: control lacks Package, Version or Architecture", p)
	}
	pkg.Source = pkg.Name
	if s := ctrl.Get("Source"); s != "" {
		pkg.Source = strings.TrimSpace(strings.SplitN(s, " ", 2)[0])
	}
	return pkg, nil
}

// readControl walks the ar archive to control.tar.* and extracts ./control.
func readControl(r io.Reader) (Paragraph, error) {
	magic := make([]byte, 8)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != "!<arch>\n" {
		return nil, errors.New("not an ar archive")
	}
	hdr := make([]byte, 60)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				return nil, errors.New("no control.tar member")
			}
			return nil, err
		}
		if string(hdr[58:60]) != "`\n" {
			return nil, errors.New("corrupt ar header")
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("bad ar member size for %q", name)
		}
		member := io.LimitReader(r, size)
		if strings.HasPrefix(name, "control.tar") {
			return controlFromTar(member)
		}
		if _, err := io.Copy(io.Discard, member); err != nil {
			return nil, err
		}
		if size%2 == 1 { // members are 2-byte aligned
			if _, err := io.CopyN(io.Discard, r, 1); err != nil {
				return nil, err
			}
		}
	}
}

func controlFromTar(r io.Reader) (Paragraph, error) {
	dr, err := compress.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer dr.Close()
	tr := tar.NewReader(dr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("control.tar has no control file")
		}
		if err != nil {
			return nil, err
		}
		if path.Clean("/"+h.Name) == "/control" && h.Typeflag == tar.TypeReg {
			var b bytes.Buffer
			if _, err := io.Copy(&b, io.LimitReader(tr, 16<<20)); err != nil {
				return nil, err
			}
			return ParseParagraph(b.Bytes())
		}
	}
}

// PoolPath returns the conventional pool location for the package within a
// component: pool/<component>/<prefix>/<source>/<name>_<version>_<arch>.deb,
// with any epoch dropped from the file name.
func (p *Package) PoolPath(component string) string {
	prefix := p.Source[:1]
	if strings.HasPrefix(p.Source, "lib") && len(p.Source) > 3 {
		prefix = p.Source[:4]
	}
	ver := p.Version
	if i := strings.IndexByte(ver, ':'); i >= 0 {
		ver = ver[i+1:]
	}
	file := fmt.Sprintf("%s_%s_%s.deb", p.Name, ver, p.Arch)
	return path.Join("pool", component, prefix, p.Source, file)
}
