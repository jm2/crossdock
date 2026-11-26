// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package pages writes the browseable side of a release: an index.html and
// a SHA256SUMS (signed as SHA256SUMS.asc) in every repo, plus a landing
// page at the release root with a client snippet for each repo.
//
// They're plain files, so any web server or object store can serve them.
// The snippets need the address clients use. With serve.public_url set, it
// is written into the pages. Without it, the pages carry BaseURLPlaceholder,
// which crossdock serve and the generated nginx config replace with each
// request's own address.
package pages

import (
	"bytes"
	"cmp"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// placeholderText is what the snippets show for the base URL when
// public_url is unset and nothing fills it in.
const placeholderText = "http://<host>:8080"

// BaseURLPlaceholder is placeholderText as it appears in the HTML, the
// exact bytes a server replaces. Only page text can produce it: repo
// names, versions and file names can't contain "<".
const BaseURLPlaceholder = "http://&lt;host&gt;:8080"

// Page and checksum file names, per repo and at the release root.
const (
	IndexName  = "index.html"
	SumsName   = "SHA256SUMS"
	SumsSigExt = ".asc"
)

// Write writes every repo's index.html and SHA256SUMS (and SHA256SUMS.asc
// when signing), then the landing page if serve.index is on. Call it after
// every backend has published, so env.Packages is complete. generator
// names the crossdock version for the page footers.
func Write(cfg *config.Config, env *publish.Env, generator string) error {
	base := cfg.Serve.PublicURL
	if base == "" {
		base = placeholderText
	}
	c := Common{
		Base:      base,
		Signed:    env.Signer != nil,
		Published: env.Now.UTC().Format("2006-01-02 15:04 UTC"),
		Generator: generator,
		Style:     template.CSS(Style),
		HasRoot:   cfg.Serve.IndexEnabled(),
	}
	if env.Signer != nil {
		c.Fingerprint = groupFingerprint(env.Signer.Fingerprint())
		c.RawFingerprint = env.Signer.Fingerprint()
	}
	byRepo := map[string][]publish.PackageFile{}
	for _, f := range env.Packages {
		byRepo[f.Repo] = append(byRepo[f.Repo], f)
	}
	repos := slices.Clone(cfg.Repos)
	slices.SortFunc(repos, func(a, b config.Repo) int {
		return cmp.Or(cmp.Compare(a.Format, b.Format), cmp.Compare(a.Name, b.Name))
	})

	var summaries []repoPage
	for _, r := range repos {
		p := buildRepo(r, byRepo[string(r.Format)+"/"+r.Name], c)
		sums, byPath := checksums(p.Dir, byRepo[p.Dir])
		p.SumsByPath = byPath
		if _, err := env.Release.WriteFile(path.Join(p.Dir, SumsName), sums); err != nil {
			return err
		}
		if env.Signer != nil {
			sig, err := env.Signer.DetachSign(sums, true)
			if err != nil {
				return fmt.Errorf("%s: signing %s: %w", p.Dir, SumsName, err)
			}
			if _, err := env.Release.WriteFile(path.Join(p.Dir, SumsName+SumsSigExt), sig); err != nil {
				return err
			}
		}
		// This page stands in for the directory's listing, so link what's
		// in it.
		entries, err := os.ReadDir(filepath.Join(env.Release.Dir, filepath.FromSlash(p.Dir)))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, ".") || n == IndexName {
				continue
			}
			if e.IsDir() {
				n += "/"
			}
			p.Browse = append(p.Browse, link{Href: href(n), Text: n})
		}
		var b bytes.Buffer
		if err := repoTmpl.Execute(&b, p); err != nil {
			return fmt.Errorf("%s: %w", p.Dir, err)
		}
		if _, err := env.Release.WriteFile(path.Join(p.Dir, IndexName), b.Bytes()); err != nil {
			return err
		}
		summaries = append(summaries, p)
	}
	if !cfg.Serve.IndexEnabled() {
		return nil
	}
	var b bytes.Buffer
	if err := rootTmpl.Execute(&b, rootPage{Common: c, Repos: summaries}); err != nil {
		return err
	}
	_, err := env.Release.WriteFile(IndexName, b.Bytes())
	return err
}

// Common is what every page shows.
type Common struct {
	Base           string
	Signed         bool
	Fingerprint    string // grouped for reading
	RawFingerprint string
	Published      string
	Generator      string
	Style          template.CSS
	HasRoot        bool // the landing page exists, so link to it
}

type rootPage struct {
	Common
	Repos []repoPage
}

type repoPage struct {
	Common
	Dir      string // "<format>/<name>"
	Name     string
	Client   string // "apt", "pacman", "dnf", "GooGet"
	Snippet  string
	Notes    []string
	Count    int
	Size     string
	Groups   []group
	SumsName string
	// SumsByPath is set when SHA256SUMS names some files by their path in
	// the repo (two different files share a name).
	SumsByPath bool
	Browse     []link // the repo directory's entries
}

type link struct{ Href, Text string }

// group is a package's row, plus the rows of any packages that belong
// with it (a split deb's -dataN parts).
type group struct {
	Rows []row
	// Install installs the whole group from downloaded files.
	Install string
}

type row struct {
	Name, Version, Arch, Summary string
	Href, Text                   string
	Size                         string
	Bytes                        int64
	SHA256                       string
	Part                         bool
}

func buildRepo(r config.Repo, files []publish.PackageFile, c Common) repoPage {
	dir := string(r.Format) + "/" + r.Name
	p := repoPage{Common: c, Dir: dir, Name: r.Name, SumsName: SumsName}
	// A pacman "any" package is one package in several directories: count
	// it, and its size, once.
	pkgs, blobs := map[string]bool{}, map[string]bool{}
	var total int64
	for _, f := range files {
		pkgs[f.Name+"\x00"+f.Version+"\x00"+f.Arch] = true
		if !blobs[f.SHA256] {
			blobs[f.SHA256] = true
			total += f.Size
		}
	}
	p.Count, p.Size = len(pkgs), humanSize(total)
	p.Client, p.Snippet, p.Notes = snippet(r, c)

	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b publish.PackageFile) int {
		return cmp.Or(naturalCompare(a.Name, b.Name), naturalCompare(a.Version, b.Version),
			cmp.Compare(a.Arch, b.Arch), cmp.Compare(a.Path, b.Path))
	})
	// Link text is the file name, or the path in the repo where two files
	// share one (a pacman "any" package sits in every architecture).
	seen := map[string]int{}
	for _, f := range files {
		seen[path.Base(f.Path)]++
	}
	mkRow := func(f publish.PackageFile, part bool) row {
		rel := strings.TrimPrefix(f.Path, dir+"/")
		text := path.Base(rel)
		if seen[text] > 1 {
			text = rel
		}
		return row{Name: f.Name, Version: f.Version, Arch: f.Arch, Summary: f.Summary,
			Href: href(rel), Text: text, Size: humanSize(f.Size), Bytes: f.Size,
			SHA256: f.SHA256, Part: part}
	}

	parts := map[int][]publish.PackageFile{} // index of the main package -> its parts
	partOf := map[int]bool{}
	if r.Format == config.FormatDeb {
		parts, partOf = debParts(files)
	}
	for i, f := range files {
		if partOf[i] {
			continue
		}
		g := group{Rows: []row{mkRow(f, false)}}
		if ps := parts[i]; len(ps) > 0 {
			install := []string{"sudo apt install ./" + path.Base(f.Path)}
			for _, pf := range ps {
				g.Rows = append(g.Rows, mkRow(pf, true))
				install = append(install, "./"+path.Base(pf.Path))
			}
			g.Install = strings.Join(install, " ")
		}
		p.Groups = append(p.Groups, g)
	}
	return p
}

var dataPartRE = regexp.MustCompile(`^(.+)-data([0-9]+)$`)

// debParts finds split debs: Architecture: all <name>-dataN packages that
// carry the bulk of <name>'s files at exactly its version, because a single
// .deb can't hold more than about 9.3 GiB. Each part is listed under every
// architecture of its main package, which depends on it. A part of a part
// (foo-data1-data1) isn't nested: only packages listed in their own right
// take parts, so nothing ends up hidden.
func debParts(files []publish.PackageFile) (map[int][]publish.PackageFile, map[int]bool) {
	mains := map[string][]int{}
	for i, f := range files {
		mains[f.Name+"\x00"+f.Version] = append(mains[f.Name+"\x00"+f.Version], i)
	}
	// owners lists, for each candidate part, the packages it belongs to.
	owners := map[int][]int{}
	nums := map[int]int{}
	for i, f := range files {
		m := dataPartRE.FindStringSubmatch(f.Name)
		// A part for one architecture can't go in every architecture's
		// install line.
		if m == nil || f.Arch != "all" {
			continue
		}
		if own := mains[m[1]+"\x00"+f.Version]; len(own) > 0 {
			owners[i] = own
			nums[i], _ = strconv.Atoi(m[2])
		}
	}
	type part struct {
		n int
		f publish.PackageFile
	}
	byMain := map[int][]part{}
	partOf := map[int]bool{}
	for i, own := range owners {
		for _, o := range own {
			if _, isPart := owners[o]; isPart {
				continue
			}
			byMain[o] = append(byMain[o], part{nums[i], files[i]})
			partOf[i] = true
		}
	}
	out := map[int][]publish.PackageFile{}
	for o, ps := range byMain {
		slices.SortFunc(ps, func(a, b part) int { return cmp.Compare(a.n, b.n) })
		for _, p := range ps {
			out[o] = append(out[o], p.f)
		}
	}
	return out, partOf
}

// checksums renders SHA256SUMS: one "<sha256>  <file name>" line per file,
// in the format `sha256sum -c` reads, so a file downloaded on its own checks
// with `sha256sum -c --ignore-missing`. Copies of one file in several
// directories share a line. Should two different files ever share a name,
// both are listed by their path in the repo instead, and byPath is set.
// A name with a backslash, newline or carriage return is escaped as GNU
// sha256sum writes it.
func checksums(dir string, files []publish.PackageFile) (sums []byte, byPath bool) {
	digests := map[string]map[string]bool{}
	for _, f := range files {
		n := path.Base(f.Path)
		if digests[n] == nil {
			digests[n] = map[string]bool{}
		}
		digests[n][f.SHA256] = true
	}
	type entry struct{ name, line string }
	seen := map[entry]bool{}
	var entries []entry
	for _, f := range files {
		n := path.Base(f.Path)
		if len(digests[n]) > 1 {
			n = strings.TrimPrefix(f.Path, dir+"/")
			byPath = true
		}
		e := entry{n, sumLine(f.SHA256, n)}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	// By file name, as people scan it.
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.line, b.line)) })
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.line)
	}
	return []byte(b.String()), byPath
}

// sumEscaper escapes a file name for a checksum line, as GNU coreutils does.
var sumEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`)

// sumLine is one SHA256SUMS line. A name that needs escaping starts the line
// with a backslash, which tells sha256sum -c to unescape it.
func sumLine(sha256, name string) string {
	if strings.ContainsAny(name, "\\\n\r") {
		return `\` + sha256 + "  " + sumEscaper.Replace(name) + "\n"
	}
	return sha256 + "  " + name + "\n"
}

// snippet returns the client's name, a shell snippet that sets up the repo,
// and notes to show beside it.
func snippet(r config.Repo, c Common) (string, string, []string) {
	base, repoURL := c.Base, c.Base+"/"+string(r.Format)+"/"+r.Name
	var b strings.Builder
	l := func(s ...string) { b.WriteString(strings.Join(s, "") + "\n") }
	switch r.Format {
	case config.FormatDeb:
		if c.Signed {
			l("sudo curl -fsSLo /etc/apt/keyrings/crossdock.asc ", base, "/crossdock.asc")
		}
		l("sudo tee /etc/apt/sources.list.d/", r.Name, ".sources >/dev/null <<'EOF'")
		l("Types: deb")
		l("URIs: ", repoURL)
		l("Suites: ", r.Suite)
		l("Components: ", r.Component)
		if c.Signed {
			l("Signed-By: /etc/apt/keyrings/crossdock.asc")
		} else {
			l("Trusted: yes")
		}
		l("EOF")
		l("sudo apt update")
		return "apt", b.String(), nil
	case config.FormatArch:
		if c.Signed {
			l("curl -fsSLO ", base, "/crossdock.asc")
			l("sudo pacman-key --add crossdock.asc")
			l("sudo pacman-key --lsign-key ", c.RawFingerprint)
		}
		l("sudo tee -a /etc/pacman.conf >/dev/null <<'EOF'")
		l("")
		l("[", r.Name, "]")
		if c.Signed {
			l("SigLevel = Required DatabaseRequired PackageOptional")
		} else {
			l("SigLevel = Optional TrustAll")
		}
		l("Server = ", base, "/arch/$repo/$arch")
		l("EOF")
		l("sudo pacman -Syu")
		var notes []string
		if c.Signed {
			notes = append(notes, "The signed database pins every package's SHA-256, which is why DatabaseRequired matters. PackageOptional allows packages without their own signature.")
		}
		return "pacman", b.String(), notes
	case config.FormatRPM:
		l("sudo tee /etc/yum.repos.d/", r.Name, ".repo >/dev/null <<'EOF'")
		l("[", r.Name, "]")
		l("name=", r.Name)
		l("baseurl=", repoURL)
		if c.Signed {
			l("repo_gpgcheck=1")
			l("gpgcheck=0")
			l("gpgkey=", base, "/crossdock.asc")
		} else {
			l("repo_gpgcheck=0")
			l("gpgcheck=0")
		}
		l("EOF")
		l("sudo dnf makecache")
		return "dnf", b.String(), []string{"repo_gpgcheck=1 checks the signed repodata, which pins every package's SHA-256. Fedora 45 also requires a signature on each package (rpmsign); set gpgcheck=1 for signed packages."}
	case config.FormatGoo:
		l("googet addrepo ", r.Name, " ", repoURL)
		l("googet available")
		return "GooGet", b.String(), []string{
			"GooGet has no index signatures: serve it over HTTPS, or keep it on a trusted LAN. It skips http:// repos unless googet.conf sets allowunsafeurl: true.",
		}
	}
	return string(r.Format), "", nil
}

// href turns a slash-separated relative path into a safe relative URL.
// The "./" keeps a first segment with a colon from reading as a scheme.
func href(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "./" + strings.Join(segs, "/")
}

// Href is href for other packages' listings.
func Href(rel string) string { return href(rel) }

// humanSize formats bytes in IEC units with one decimal.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanSize is humanSize for other packages' listings.
func HumanSize(n int64) string { return humanSize(n) }

// groupFingerprint spaces a hex fingerprint in groups of four.
func groupFingerprint(fpr string) string {
	var parts []string
	for len(fpr) > 4 {
		parts = append(parts, fpr[:4])
		fpr = fpr[4:]
	}
	return strings.Join(append(parts, fpr), " ")
}

// naturalCompare orders strings with digit runs compared as numbers, so
// version 1.10 sorts after 1.9.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := isDigit(a[0]), isDigit(b[0])
		if da && db {
			na, ra := splitDigits(a)
			nb, rb := splitDigits(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if c := cmp.Or(cmp.Compare(len(ta), len(tb)), cmp.Compare(ta, tb)); c != 0 {
				return c
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return cmp.Compare(a[0], b[0])
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func splitDigits(s string) (string, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
