// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package rpm

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// Suffixes are the file extensions crossdock picks up.
var Suffixes = []string{".rpm"}

type entry struct {
	pkg    *Package
	href   string // relative to the repo root
	digest publish.Digest
	mtime  int64
}

// Publish builds rpm/<name>/: Packages/<l>/<file>.rpm plus repodata/ with
// primary, filelists and other metadata, repomd.xml and repomd.xml.asc.
// One repo holds every architecture; dnf picks the ones it can install.
func Publish(repo config.Repo, env *publish.Env) error {
	files, err := publish.FindFiles(repo.Source, Suffixes...)
	if err != nil {
		return err
	}
	root := path.Join("rpm", repo.Name)
	allowed := map[string]bool{}
	for _, a := range repo.Architectures {
		allowed[a] = true
	}

	byNEVRA := map[string]*entry{}
	byFile := map[string]*entry{}
	var entries []*entry
	for _, f := range files {
		pkg, err := publish.Cached(env.Release, f, "rpm/1", ReadPackage)
		if err != nil {
			return err
		}
		if len(allowed) > 0 && !allowed[pkg.Arch] && pkg.Arch != "noarch" {
			env.Logf("rpm/%s: skipping %s (%s not in architectures)", repo.Name, pkg.NEVRA(), pkg.Arch)
			continue
		}
		fi, err := os.Stat(f)
		if err != nil {
			return err
		}
		d, err := env.Release.Digest(f)
		if err != nil {
			return err
		}
		if prev, ok := byNEVRA[pkg.NEVRA()]; ok {
			if prev.digest.SHA256 == d.SHA256 {
				continue
			}
			return fmt.Errorf("%s and %s are both %s with different contents", prev.pkg.Path, f, pkg.NEVRA())
		}
		name := pkg.FileName()
		if prev, ok := byFile[name]; ok {
			return fmt.Errorf("%s and %s would both be published as %s (they differ only in epoch)", prev.pkg.Path, f, name)
		}
		e := &entry{
			pkg:   pkg,
			href:  path.Join("Packages", strings.ToLower(name[:1]), name),
			mtime: fi.ModTime().Unix(),
		}
		if _, err := env.Release.AddFile(f, path.Join(root, e.href)); err != nil {
			return err
		}
		e.digest = d
		byNEVRA[pkg.NEVRA()] = e
		byFile[name] = e
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].pkg, entries[j].pkg
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if c := Vercmp(a.Version, b.Version); c != 0 {
			return c < 0
		}
		if c := Vercmp(a.Release, b.Release); c != 0 {
			return c < 0
		}
		return a.Arch < b.Arch
	})

	type md struct {
		kind string
		data []byte
	}
	mds := []md{
		{"primary", primaryXML(entries)},
		{"filelists", filelistsXML(entries)},
		{"other", otherXML(entries)},
	}
	var records []record
	for _, m := range mds {
		gz := publish.Gzip(m.data)
		gzd := publish.HashBytes(gz)
		href := "repodata/" + gzd.SHA256 + "-" + m.kind + ".xml.gz"
		if _, err := env.Release.WriteFile(path.Join(root, href), gz); err != nil {
			return err
		}
		records = append(records, record{kind: m.kind, href: href, gz: gzd, open: publish.HashBytes(m.data)})
	}
	repomd := repomdXML(records, env.Now.Unix())
	if _, err := env.Release.WriteFile(path.Join(root, "repodata", "repomd.xml"), repomd); err != nil {
		return err
	}
	if env.Signer != nil {
		sig, err := env.Signer.DetachSign(repomd, true)
		if err != nil {
			return err
		}
		if _, err := env.Release.WriteFile(path.Join(root, "repodata", "repomd.xml.asc"), sig); err != nil {
			return err
		}
	}
	env.Logf("rpm/%s: %d packages", repo.Name, len(entries))
	return nil
}

// x is a small XML writer that escapes text and attributes.
type x struct{ bytes.Buffer }

func (b *x) text(s string) {
	xml.EscapeText(&b.Buffer, []byte(s))
}

func (b *x) attr(name, val string) {
	b.WriteString(" " + name + `="`)
	var e bytes.Buffer
	xml.EscapeText(&e, []byte(val))
	// EscapeText leaves quotes alone in some Go versions' output; attributes
	// need them escaped.
	b.WriteString(strings.ReplaceAll(e.String(), `"`, "&quot;"))
	b.WriteString(`"`)
}

func (b *x) elem(indent, name, content string) {
	b.WriteString(indent + "<" + name + ">")
	b.text(content)
	b.WriteString("</" + name + ">\n")
}

func (b *x) version(indent string, p *Package) {
	b.WriteString(indent + "<version")
	b.attr("epoch", p.Epoch)
	b.attr("ver", p.Version)
	b.attr("rel", p.Release)
	b.WriteString("/>\n")
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func primaryXML(entries []*entry) []byte {
	var b x
	b.WriteString(xmlHeader)
	fmt.Fprintf(&b, `<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="%d">`+"\n", len(entries))
	for _, e := range entries {
		p := e.pkg
		b.WriteString(`<package type="rpm">` + "\n")
		b.elem("  ", "name", p.Name)
		b.elem("  ", "arch", p.Arch)
		b.version("  ", p)
		b.WriteString(`  <checksum type="sha256" pkgid="YES">` + e.digest.SHA256 + "</checksum>\n")
		b.elem("  ", "summary", p.Summary)
		b.elem("  ", "description", p.Description)
		b.elem("  ", "packager", p.Packager)
		b.elem("  ", "url", p.URL)
		fmt.Fprintf(&b, `  <time file="%d" build="%d"/>`+"\n", e.mtime, p.BuildTime)
		fmt.Fprintf(&b, `  <size package="%d" installed="%d" archive="%d"/>`+"\n", e.digest.Size, p.InstalledSize, p.ArchiveSize)
		b.WriteString("  <location")
		b.attr("href", e.href)
		b.WriteString("/>\n")
		b.WriteString("  <format>\n")
		b.elem("    ", "rpm:license", p.License)
		b.elem("    ", "rpm:vendor", p.Vendor)
		b.elem("    ", "rpm:group", p.Group)
		b.elem("    ", "rpm:buildhost", p.BuildHost)
		b.elem("    ", "rpm:sourcerpm", p.SourceRPM)
		fmt.Fprintf(&b, `    <rpm:header-range start="%d" end="%d"/>`+"\n", p.HeaderStart, p.HeaderEnd)
		for _, l := range []struct {
			name string
			deps []Dep
			pre  bool
		}{
			{"rpm:provides", p.Provides, false},
			{"rpm:requires", p.Requires, true},
			{"rpm:conflicts", p.Conflicts, false},
			{"rpm:obsoletes", p.Obsoletes, false},
			{"rpm:suggests", p.Suggests, false},
			{"rpm:enhances", p.Enhances, false},
			{"rpm:recommends", p.Recommends, false},
			{"rpm:supplements", p.Supplement, false},
		} {
			b.deps(l.name, l.deps, l.pre)
		}
		for _, f := range p.Files {
			if isPrimary(f.Path) {
				b.file("    ", f)
			}
		}
		b.WriteString("  </format>\n")
		b.WriteString("</package>\n")
	}
	b.WriteString("</metadata>\n")
	return b.Bytes()
}

func (b *x) deps(name string, deps []Dep, withPre bool) {
	var n int
	for _, d := range deps {
		if d.Name != "" {
			n++
		}
	}
	if n == 0 {
		return
	}
	b.WriteString("    <" + name + ">\n")
	for _, d := range deps {
		if d.Name == "" {
			continue
		}
		b.WriteString("      <rpm:entry")
		b.attr("name", d.Name)
		if d.Flags != "" {
			b.attr("flags", d.Flags)
			if d.Epoch != "" {
				b.attr("epoch", d.Epoch)
			}
			if d.Version != "" {
				b.attr("ver", d.Version)
			}
			if d.Release != "" {
				b.attr("rel", d.Release)
			}
		}
		if withPre && d.Pre {
			b.attr("pre", "1")
		}
		b.WriteString("/>\n")
	}
	b.WriteString("    </" + name + ">\n")
}

func (b *x) file(indent string, f File) {
	b.WriteString(indent + "<file")
	if f.Type != "" {
		b.attr("type", f.Type)
	}
	b.WriteString(">")
	b.text(f.Path)
	b.WriteString("</file>\n")
}

func (b *x) pkgOpen(e *entry) {
	b.WriteString("<package")
	b.attr("pkgid", e.digest.SHA256)
	b.attr("name", e.pkg.Name)
	b.attr("arch", e.pkg.Arch)
	b.WriteString(">\n")
	b.version("  ", e.pkg)
}

func filelistsXML(entries []*entry) []byte {
	var b x
	b.WriteString(xmlHeader)
	fmt.Fprintf(&b, `<filelists xmlns="http://linux.duke.edu/metadata/filelists" packages="%d">`+"\n", len(entries))
	for _, e := range entries {
		b.pkgOpen(e)
		for _, f := range e.pkg.Files {
			b.file("  ", f)
		}
		b.WriteString("</package>\n")
	}
	b.WriteString("</filelists>\n")
	return b.Bytes()
}

func otherXML(entries []*entry) []byte {
	var b x
	b.WriteString(xmlHeader)
	fmt.Fprintf(&b, `<otherdata xmlns="http://linux.duke.edu/metadata/other" packages="%d">`+"\n", len(entries))
	for _, e := range entries {
		b.pkgOpen(e)
		for _, c := range e.pkg.Changelogs {
			b.WriteString("  <changelog")
			b.attr("author", c.Author)
			b.attr("date", strconv.FormatInt(c.Date, 10))
			b.WriteString(">")
			b.text(c.Text)
			b.WriteString("</changelog>\n")
		}
		b.WriteString("</package>\n")
	}
	b.WriteString("</otherdata>\n")
	return b.Bytes()
}

type record struct {
	kind     string
	href     string
	gz, open publish.Digest
}

func repomdXML(records []record, now int64) []byte {
	var b x
	b.WriteString(xmlHeader)
	b.WriteString(`<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">` + "\n")
	fmt.Fprintf(&b, "  <revision>%d</revision>\n", now)
	for _, r := range records {
		fmt.Fprintf(&b, "  <data type=%q>\n", r.kind)
		fmt.Fprintf(&b, "    <checksum type=\"sha256\">%s</checksum>\n", r.gz.SHA256)
		fmt.Fprintf(&b, "    <open-checksum type=\"sha256\">%s</open-checksum>\n", r.open.SHA256)
		b.WriteString("    <location")
		b.attr("href", r.href)
		b.WriteString("/>\n")
		fmt.Fprintf(&b, "    <timestamp>%d</timestamp>\n", now)
		fmt.Fprintf(&b, "    <size>%d</size>\n", r.gz.Size)
		fmt.Fprintf(&b, "    <open-size>%d</open-size>\n", r.open.Size)
		b.WriteString("  </data>\n")
	}
	b.WriteString("</repomd>\n")
	return b.Bytes()
}
