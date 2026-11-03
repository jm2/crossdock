// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package rpm

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Header tags crossdock reads (rpm/rpmtag.h).
const (
	tagSigLongArchiveSize = 271  // signature header
	tagSigPayloadSize     = 1007 // signature header

	tagName           = 1000
	tagVersion        = 1001
	tagRelease        = 1002
	tagEpoch          = 1003
	tagSummary        = 1004
	tagDescription    = 1005
	tagBuildTime      = 1006
	tagBuildHost      = 1007
	tagSize           = 1009
	tagVendor         = 1011
	tagLicense        = 1014
	tagPackager       = 1015
	tagGroup          = 1016
	tagURL            = 1020
	tagArch           = 1022
	tagOldFilenames   = 1027
	tagFileModes      = 1030
	tagFileFlags      = 1037
	tagSourceRPM      = 1044
	tagArchiveSize    = 1046
	tagProvideName    = 1047
	tagRequireFlags   = 1048
	tagRequireName    = 1049
	tagRequireVersion = 1050
	tagConflictFlags  = 1053
	tagConflictName   = 1054
	tagConflictVer    = 1055
	tagChangelogTime  = 1080
	tagChangelogName  = 1081
	tagChangelogText  = 1082
	tagObsoleteName   = 1090
	tagSourcePackage  = 1106
	tagProvideFlags   = 1112
	tagProvideVersion = 1113
	tagObsoleteFlags  = 1114
	tagObsoleteVer    = 1115
	tagDirIndexes     = 1116
	tagBaseNames      = 1117
	tagDirNames       = 1118
	tagLongArchive    = 271
	tagLongSize       = 5009
	tagRecommendName  = 5046
	tagRecommendVer   = 5047
	tagRecommendFlags = 5048
	tagSuggestName    = 5049
	tagSuggestVer     = 5050
	tagSuggestFlags   = 5051
	tagSupplementName = 5052
	tagSupplementVer  = 5053
	tagSupplementFlg  = 5054
	tagEnhanceName    = 5055
	tagEnhanceVer     = 5056
	tagEnhanceFlags   = 5057
	tagPayloadSizeAlt = 5113
)

// Dependency sense flags.
const (
	senseLess        = 1 << 1
	senseGreater     = 1 << 2
	senseEqual       = 1 << 3
	sensePrereq      = 1 << 6
	senseScriptPre   = 1 << 9
	senseScriptPost  = 1 << 10
	sensePretrans    = 1 << 7
	sensePosttrans   = 1 << 5
	fileGhost        = 1 << 6
	modeTypeMask     = 0o170000
	modeDir          = 0o040000
	changelogDefault = 10
)

var leadMagic = []byte{0xed, 0xab, 0xee, 0xdb}

// Dep is one provides/requires/... entry.
type Dep struct {
	Name, Flags, Epoch, Version, Release string
	Pre                                  bool
}

// File is one packaged path.
type File struct {
	Path string
	Type string // "", "dir" or "ghost"
}

// Changelog is one changelog entry.
type Changelog struct {
	Author string
	Date   int64
	Text   string
}

// Package is the repo-relevant metadata of an RPM.
type Package struct {
	Path string

	Name, Arch, Epoch, Version, Release string
	Summary, Description, URL           string
	License, Vendor, Group              string
	BuildHost, SourceRPM, Packager      string
	BuildTime                           int64
	InstalledSize, ArchiveSize          uint64
	HeaderStart, HeaderEnd              int64

	Provides, Requires, Conflicts, Obsoletes   []Dep
	Suggests, Enhances, Recommends, Supplement []Dep
	Files                                      []File
	Changelogs                                 []Changelog // oldest first
}

// NEVRA is name-[epoch:]version-release.arch.
func (p *Package) NEVRA() string {
	evr := p.Version + "-" + p.Release
	if p.Epoch != "0" && p.Epoch != "" {
		evr = p.Epoch + ":" + evr
	}
	return p.Name + "-" + evr + "." + p.Arch
}

// FileName is the canonical file name, name-version-release.arch.rpm.
func (p *Package) FileName() string {
	return p.Name + "-" + p.Version + "-" + p.Release + "." + p.Arch + ".rpm"
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

// ReadPackage parses an RPM's lead, signature header and main header.
func ReadPackage(path string) (*Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p, err := read(&countingReader{r: bufio.NewReader(f)})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	return p, nil
}

func read(cr *countingReader) (*Package, error) {
	lead := make([]byte, 96)
	if _, err := io.ReadFull(cr, lead); err != nil {
		return nil, fmt.Errorf("reading lead: %w", err)
	}
	if !bytes.HasPrefix(lead, leadMagic) {
		return nil, errors.New("not an RPM (bad lead magic)")
	}
	sig, err := readHeader(cr)
	if err != nil {
		return nil, fmt.Errorf("signature header: %w", err)
	}
	// The signature header is padded to an 8-byte boundary.
	if pad := (8 - cr.n%8) % 8; pad > 0 {
		if _, err := io.CopyN(io.Discard, cr, pad); err != nil {
			return nil, err
		}
	}
	start := cr.n
	h, err := readHeader(cr)
	if err != nil {
		return nil, fmt.Errorf("main header: %w", err)
	}
	p := &Package{HeaderStart: start, HeaderEnd: cr.n}
	p.fill(h, sig)
	// rpm marks source packages from the lead's type field (1 = source)
	// when it reads them, as well as from the SOURCEPACKAGE tag.
	if lead[6] == 0 && lead[7] == 1 {
		p.Arch = "src"
	}
	if p.Name == "" || p.Version == "" || p.Arch == "" {
		return nil, errors.New("header lacks name, version or arch")
	}
	return p, nil
}

func (p *Package) fill(h, sig *header) {
	p.Name = h.str(tagName)
	p.Arch = h.str(tagArch)
	if h.has(tagSourcePackage) {
		p.Arch = "src"
	}
	p.Version = h.str(tagVersion)
	p.Release = h.str(tagRelease)
	epoch, _ := h.int(tagEpoch)
	p.Epoch = strconv.FormatUint(epoch, 10)
	p.Summary = text(h.str(tagSummary))
	p.Description = text(h.str(tagDescription))
	p.URL = text(h.str(tagURL))
	if t, ok := h.int(tagBuildTime); ok {
		p.BuildTime = int64(t)
	}
	p.License = text(h.str(tagLicense))
	p.Vendor = text(h.str(tagVendor))
	p.Group = text(h.str(tagGroup))
	p.BuildHost = text(h.str(tagBuildHost))
	p.SourceRPM = xmlString(h.str(tagSourceRPM))
	p.Packager = text(h.str(tagPackager))
	if v, ok := h.int(tagLongSize); ok {
		p.InstalledSize = v
	} else if v, ok := h.int(tagSize); ok {
		p.InstalledSize = v
	}
	// rpm merges the signature header's payload sizes into the main header
	// when it reads a package; v6 packages use PAYLOADSIZEALT instead.
	for _, src := range []struct {
		h   *header
		tag uint32
	}{{h, tagPayloadSizeAlt}, {h, tagLongArchive}, {sig, tagSigLongArchiveSize}, {h, tagArchiveSize}, {sig, tagSigPayloadSize}} {
		if v, ok := src.h.int(src.tag); ok {
			p.ArchiveSize = v
			break
		}
	}

	p.Files = files(h)
	fileSet := make(map[string]bool, len(p.Files))
	for _, f := range p.Files {
		fileSet[f.Path] = true
	}
	p.Provides = deps(h, tagProvideName, tagProvideFlags, tagProvideVersion)
	provided := map[string]bool{}
	for _, d := range rawDeps(h, tagProvideName, tagProvideFlags, tagProvideVersion) {
		provided[d.key()] = true
	}
	p.Requires = requires(h, fileSet, provided)
	p.Conflicts = deps(h, tagConflictName, tagConflictFlags, tagConflictVer)
	p.Obsoletes = deps(h, tagObsoleteName, tagObsoleteFlags, tagObsoleteVer)
	p.Suggests = deps(h, tagSuggestName, tagSuggestFlags, tagSuggestVer)
	p.Enhances = deps(h, tagEnhanceName, tagEnhanceFlags, tagEnhanceVer)
	p.Recommends = deps(h, tagRecommendName, tagRecommendFlags, tagRecommendVer)
	p.Supplement = deps(h, tagSupplementName, tagSupplementFlg, tagSupplementVer)
	p.Changelogs = changelogs(h, changelogDefault)
}

func files(h *header) []File {
	var paths []string
	if base := h.strings(tagBaseNames); base != nil {
		dirs := h.strings(tagDirNames)
		idx := h.ints(tagDirIndexes)
		for i, b := range base {
			dir := ""
			if i < len(idx) && idx[i] < uint64(len(dirs)) {
				dir = dirs[idx[i]]
			}
			paths = append(paths, dir+b)
		}
	} else {
		paths = h.strings(tagOldFilenames)
	}
	modes, flags := h.ints(tagFileModes), h.ints(tagFileFlags)
	out := make([]File, 0, len(paths))
	for i, p := range paths {
		f := File{Path: p}
		if i < len(modes) && modes[i]&modeTypeMask == modeDir {
			f.Type = "dir"
		} else if i < len(flags) && flags[i]&fileGhost != 0 {
			f.Type = "ghost"
		}
		out = append(out, f)
	}
	return out
}

type rawDep struct {
	name, flags, version string
	sense                uint64
}

func (d rawDep) key() string { return d.name + d.flags + d.version }

func rawDeps(h *header, nameTag, flagTag, verTag uint32) []rawDep {
	names, flags, vers := h.strings(nameTag), h.ints(flagTag), h.strings(verTag)
	n := min(len(names), len(flags), len(vers))
	out := make([]rawDep, n)
	for i := 0; i < n; i++ {
		// createrepo_c converts non-UTF-8 dependency strings from Latin-1
		// when it writes XML; do it at read time so filtering sees the same.
		out[i] = rawDep{name: xmlString(names[i]), flags: flagString(flags[i]), version: xmlString(vers[i]), sense: flags[i]}
	}
	return out
}

// flagString maps comparison bits the way createrepo_c does.
func flagString(f uint64) string {
	switch f & 0xf {
	case senseLess:
		return "LT"
	case senseGreater:
		return "GT"
	case senseEqual:
		return "EQ"
	case senseLess | senseEqual:
		return "LE"
	case senseGreater | senseEqual:
		return "GE"
	}
	return ""
}

// parseEVR splits "E:V-R". ok is false for a non-numeric epoch.
func parseEVR(s string) (epoch, version, release string, ok bool) {
	if s == "" {
		return "", "", "", true
	}
	rest := s
	if i := strings.IndexByte(s, ':'); i >= 0 {
		e := s[:i]
		if _, err := strconv.ParseInt(e, 10, 64); e != "" && err != nil {
			return "", "", "", false
		}
		epoch, rest = e, s[i+1:]
	}
	if epoch == "" {
		epoch = "0"
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		return epoch, rest[:i], rest[i+1:], true
	}
	return epoch, rest, "", true
}

func toDep(r rawDep) (Dep, bool) {
	e, v, rel, ok := parseEVR(r.version)
	if !ok {
		return Dep{}, false
	}
	return Dep{Name: r.name, Flags: r.flags, Epoch: e, Version: v, Release: rel}, true
}

func deps(h *header, nameTag, flagTag, verTag uint32) []Dep {
	var out []Dep
	for _, r := range rawDeps(h, nameTag, flagTag, verTag) {
		if d, ok := toDep(r); ok {
			out = append(out, d)
		}
	}
	return out
}

// requires applies createrepo_c's filtering: no rpmlib() deps, no primary
// files the package itself ships, nothing it provides, no repeats, and
// only the highest libc.so.6 symbol version.
func requires(h *header, files, provided map[string]bool) []Dep {
	type seenVal struct {
		flags, version string
		pre            bool
	}
	seen := map[string]seenVal{}
	var out []Dep
	var libc *Dep
	for _, r := range rawDeps(h, tagRequireName, tagRequireFlags, tagRequireVersion) {
		if strings.HasPrefix(r.name, "rpmlib(") {
			continue
		}
		if strings.HasPrefix(r.name, "/") && files[r.name] && isPrimary(r.name) {
			continue
		}
		if provided[r.key()] {
			continue
		}
		pre := r.sense&(sensePrereq|senseScriptPre|sensePosttrans|sensePretrans|senseScriptPost) != 0
		if s, ok := seen[r.name]; ok && s.flags == r.flags && s.version == r.version && s.pre == pre {
			continue
		}
		d, ok := toDep(r)
		if !ok {
			continue
		}
		d.Pre = pre
		if strings.HasPrefix(d.Name, "libc.so.6") {
			if libc == nil || compareLibc(libc.Name, d.Name) == 2 {
				dd := d
				libc = &dd
			}
			continue
		}
		out = append(out, d)
		seen[d.Name] = seenVal{r.flags, r.version, pre}
	}
	if libc != nil {
		out = append([]Dep{*libc}, out...)
	}
	return out
}

// compareLibc mirrors createrepo_c's cr_compare_dependency: 0 same,
// 1 first bigger, 2 second bigger.
func compareLibc(a, b string) int {
	if a == b {
		return 0
	}
	ia, ib := strings.IndexByte(a, '('), strings.IndexByte(b, '(')
	switch {
	case ia < 0 && ib < 0:
		return 0
	case ia < 0:
		return 2
	case ib < 0:
		return 1
	}
	ea, eb := strings.IndexByte(a[ia:], ')'), strings.IndexByte(b[ib:], ')')
	switch {
	case ea < 0 && eb < 0:
		return -1
	case ea < 0:
		return 2
	case eb < 0:
		return 1
	}
	va, vb := a[ia+1:ia+ea], b[ib+1:ib+eb]
	switch {
	case va == "" && vb == "":
		return 0
	case va == "":
		return 2
	case vb == "":
		return 1
	}
	da := strings.IndexAny(va, "0123456789")
	db := strings.IndexAny(vb, "0123456789")
	switch {
	case da < 0 && db < 0:
		return 0
	case da < 0:
		return 2
	case db < 0:
		return 1
	}
	switch Vercmp(va[da:], vb[db:]) {
	case 1:
		return 1
	case -1:
		return 2
	}
	return 0
}

func changelogs(h *header, limit int) []Changelog {
	times, names, texts := h.ints(tagChangelogTime), h.strings(tagChangelogName), h.strings(tagChangelogText)
	n := min(len(times), len(names), len(texts), limit)
	// The header lists newest first; repodata lists oldest first.
	out := make([]Changelog, n)
	for i := 0; i < n; i++ {
		out[n-1-i] = Changelog{
			Author: strings.TrimRight(text(names[i]), " "),
			Date:   int64(times[i]),
			Text:   text(texts[i]),
		}
	}
	// createrepo_c bumps entries that share a timestamp so each is unique:
	// walking from newest, a repeat of the previous time pushes the run of
	// equal, newer-listed entries up by one second each.
	var last int64
	for i := n - 1; i >= 0; i-- {
		t := int64(times[n-1-i])
		if i < n-1 && last == t {
			tmp := t
			for j := i + 1; j < n && out[j].Date == tmp; j++ {
				out[j].Date++
				tmp++
			}
		} else {
			last = t
		}
	}
	return out
}

// text normalizes a free-text header field the way createrepo_c does:
// forbidden control characters become U+FFFD, then the string goes through
// xmlString.
func text(s string) string {
	if strings.IndexFunc(s, forbiddenControl) >= 0 {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if forbiddenControl(rune(s[i])) {
				b.WriteString("\uFFFD")
			} else {
				b.WriteByte(s[i])
			}
		}
		s = b.String()
	}
	return xmlString(s)
}

func forbiddenControl(r rune) bool {
	return r < 0x20 && r != '\t' && r != '\n' && r != '\r'
}

// xmlString makes a header string safe for XML as createrepo_c's writer
// does: valid UTF-8 passes through; anything else is read as Latin-1,
// dropping control characters and 0x80-0xBF (most likely cp1252, not
// Latin-1).
func xmlString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < 0x80:
			if !forbiddenControl(rune(c)) {
				b.WriteByte(c)
			}
		case c < 0xc0:
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// isPrimary reports whether a path is listed in primary.xml as well as
// filelists.xml.
func isPrimary(p string) bool {
	return strings.HasPrefix(p, "/etc/") || p == "/usr/lib/sendmail" || strings.Contains(p, "bin/")
}
