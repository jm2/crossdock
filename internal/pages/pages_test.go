// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package pages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
	"github.com/jm2/crossdock/internal/sign"
)

func pf(repo, rel, name, version, arch string, size int64, sha byte) publish.PackageFile {
	return publish.PackageFile{Repo: repo, Path: repo + "/" + rel, Name: name, Version: version, Arch: arch,
		Size: size, SHA256: strings.Repeat(string(sha), 64)}
}

// env starts a release to write pages into, with the given packages.
func env(t *testing.T, signer sign.Signer, files ...publish.PackageFile) *publish.Env {
	t.Helper()
	rel, err := publish.Begin(t.TempDir(), time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rel.Abort() })
	return &publish.Env{Release: rel, Signer: signer, Now: time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC),
		Logf: t.Logf, Packages: files}
}

func read(t *testing.T, e *publish.Env, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.Release.Dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(e *publish.Env, rel string) bool {
	_, err := os.Stat(filepath.Join(e.Release.Dir, filepath.FromSlash(rel)))
	return err == nil
}

func testKey(t *testing.T) sign.Signer {
	t.Helper()
	k, err := sign.Generate("pages test <test@example.invalid>", "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "k.asc")
	os.WriteFile(p, k.Secret, 0o600)
	s, err := sign.New(&config.Signing{KeyFile: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func cfg(publicURL string, index *bool, repos ...config.Repo) *config.Config {
	return &config.Config{Serve: config.Serve{PublicURL: publicURL, Index: index}, Repos: repos}
}

var (
	debRepo  = config.Repo{Name: "games", Format: config.FormatDeb, Suite: "stable", Component: "main"}
	archRepo = config.Repo{Name: "games", Format: config.FormatArch}
	rpmRepo  = config.Repo{Name: "games", Format: config.FormatRPM}
	gooRepo  = config.Repo{Name: "win", Format: config.FormatGoo}
)

func TestWriteSigned(t *testing.T) {
	s := testKey(t)
	e := env(t, s,
		pf("deb/games", "pool/main/b/bo3/bo3_1.27_amd64.deb", "bo3", "1.27", "amd64", 4<<20, 'a'),
		pf("arch/games", "x86_64/tool-1-1-any.pkg.tar.zst", "tool", "1-1", "any", 10, 'b'),
		pf("arch/games", "aarch64/tool-1-1-any.pkg.tar.zst", "tool", "1-1", "any", 10, 'b'),
		pf("rpm/games", "Packages/q/quake-2:1.0-1.x86_64.rpm", "quake", "2:1.0-1", "x86_64", 2048, 'c'),
	)
	e.Packages[0].Summary = `Black Ops <III> & "friends"`
	if err := Write(cfg("", nil, debRepo, archRepo, rpmRepo, gooRepo), e, "crossdock v9"); err != nil {
		t.Fatal(err)
	}
	root := read(t, e, "index.html")
	for _, want := range []string{
		// The placeholder, exactly as servers replace it, in every snippet.
		"URIs: " + BaseURLPlaceholder + "/deb/games\n",
		"Server = " + BaseURLPlaceholder + "/arch/$repo/$arch\n",
		"baseurl=" + BaseURLPlaceholder + "/rpm/games\n",
		"googet addrepo win " + BaseURLPlaceholder + "/goo/win\n",
		"sudo pacman-key --lsign-key " + s.Fingerprint() + "\n",
		// URLs given to the shell are quoted; apt's keyring is named for the key.
		"sudo curl -fsSLo /etc/apt/keyrings/crossdock-" + s.Fingerprint() + ".asc &#39;" + BaseURLPlaceholder + "/crossdock.asc&#39;\n",
		"Signed-By: /etc/apt/keyrings/crossdock-" + s.Fingerprint() + ".asc\n",
		"curl -fsSLO &#39;" + BaseURLPlaceholder + "/crossdock.asc&#39;\n",
		groupFingerprint(s.Fingerprint()),
		`<a href="./deb/games/">deb/games</a>`, `<a href="./goo/win/">goo/win</a>`,
		"apt · 1 package · 4.0 MiB", "pacman · 1 package · 10 B", "GooGet · 0 packages · 0 B",
		`<a href="crossdock.asc">`, "Published 2026-11-25 09:30 UTC by crossdock v9.",
	} {
		if !strings.Contains(root, want) {
			t.Errorf("root page lacks %q", want)
		}
	}
	// The GooGet snippet installs googet first where it's missing, from
	// Google's package pinned by SHA-256.
	gooSetup := []string{
		"if (-not (Get-Command googet -ErrorAction SilentlyContinue)) {\n",
		"curl.exe -fsSLo $goo &#39;" + googetGooURL + "&#39;\n",
		"if ((Get-FileHash $goo).Hash -ne &#39;" + googetGooSHA256 + "&#39;) { throw",
		"-noconfirm install -sources &#39;" + googetRepo + "&#39; googet\n",
		"$env:GooGetRoot = &#34;$env:ProgramData\\GooGet&#34;\n",
		// Prepended, so the session's own PATH entries stay.
		"$env:Path = &#34;$env:GooGetRoot;$env:Path&#34;\n}\n" + HTTPOnlyStart + "$conf = ",
		// allowunsafeurl, marked as only for plain HTTP.
		"$lines = @(if (Test-Path $conf) { Get-Content $conf })\n",
		"Set-Content $conf (@($lines -notmatch &#39;^allowunsafeurl:&#39;) + &#39;allowunsafeurl: true&#39;)\n}\n" +
			HTTPOnlyEnd + "googet addrepo win " + BaseURLPlaceholder + "/goo/win\n",
	}
	for _, want := range gooSetup {
		if !strings.Contains(root, want) {
			t.Errorf("root page's GooGet snippet lacks %q", want)
		}
	}
	// Repos are in format, then name order.
	if strings.Index(root, "arch/games") > strings.Index(root, "deb/games") || strings.Index(root, "goo/win") > strings.Index(root, "rpm/games") {
		t.Error("repos out of order")
	}

	deb := read(t, e, "deb/games/index.html")
	for _, want := range []string{
		`<a href="../../">All repositories</a>`, `<a href="../../crossdock.gpg">`,
		`<a href="./pool/main/b/bo3/bo3_1.27_amd64.deb">bo3_1.27_amd64.deb</a>`,
		"Black Ops &lt;III&gt; &amp; &#34;friends&#34;",
		"gpgv --keyring ./crossdock.gpg SHA256SUMS.asc SHA256SUMS",
		"curl -fsSLO '" + BaseURLPlaceholder + "/crossdock.gpg' -O '" + BaseURLPlaceholder + "/deb/games/SHA256SUMS'",
		"sha256sum -c --ignore-missing SHA256SUMS",
	} {
		if !strings.Contains(deb, want) {
			t.Errorf("deb page lacks %q", want)
		}
	}
	// The page replaces the directory listing, so it links what's there.
	if !strings.Contains(deb, `Browse: <a href="./SHA256SUMS">SHA256SUMS</a> <a href="./SHA256SUMS.asc">SHA256SUMS.asc</a>`) {
		t.Errorf("deb page lacks the Browse line")
	}
	if strings.Contains(deb, "<III>") {
		t.Error("summary not escaped")
	}
	// A pacman "any" package is in both architectures: link text says which.
	arch := read(t, e, "arch/games/index.html")
	for _, want := range []string{`>aarch64/tool-1-1-any.pkg.tar.zst</a>`, `>x86_64/tool-1-1-any.pkg.tar.zst</a>`} {
		if !strings.Contains(arch, want) {
			t.Errorf("arch page lacks %q", want)
		}
	}
	// ... but the checksum file names it once, so `sha256sum -c` finds a
	// download in the current directory.
	if got, want := read(t, e, "arch/games/SHA256SUMS"), strings.Repeat("b", 64)+"  tool-1-1-any.pkg.tar.zst\n"; got != want {
		t.Errorf("arch SHA256SUMS = %q, want %q", got, want)
	}
	// The colon in the file name stays inside a relative link.
	if rpm := read(t, e, "rpm/games/index.html"); !strings.Contains(rpm, `href="./Packages/q/quake-2:1.0-1.x86_64.rpm"`) {
		t.Errorf("rpm link: %s", rpm)
	}
	if goo := read(t, e, "goo/win/index.html"); !strings.Contains(goo, "This repo has no packages yet.") || read(t, e, "goo/win/SHA256SUMS") != "" {
		t.Error("an empty repo still gets a page and an empty SHA256SUMS")
	}
	for _, d := range []string{"deb/games", "arch/games", "rpm/games", "goo/win"} {
		sig := read(t, e, d+"/SHA256SUMS.asc")
		if !strings.HasPrefix(sig, "-----BEGIN PGP SIGNATURE-----") {
			t.Errorf("%s/SHA256SUMS.asc is not an armored detached signature: %q", d, sig)
		}
	}
}

// GooGet's allowunsafeurl lines are marked for the server to drop over
// HTTPS when the address is the placeholder, and written or left out by
// public_url's scheme otherwise.
func TestGooHTTPOnly(t *testing.T) {
	const line = "Set-Content $conf ("
	for _, c := range []struct {
		publicURL     string
		lines, marked bool
	}{
		{"", true, true},
		{"http://repo.lan:8080", true, false},
		{"https://repo.example", false, false},
	} {
		e := env(t, nil)
		if err := Write(cfg(c.publicURL, nil, gooRepo), e, ""); err != nil {
			t.Fatal(err)
		}
		page := read(t, e, "goo/win/index.html")
		if got := strings.Contains(page, line); got != c.lines {
			t.Errorf("public_url %q: allowunsafeurl lines = %v, want %v", c.publicURL, got, c.lines)
		}
		if got := strings.Contains(page, HTTPOnlyStart) && strings.Contains(page, HTTPOnlyEnd); got != c.marked {
			t.Errorf("public_url %q: markers = %v, want %v", c.publicURL, got, c.marked)
		}
		if !strings.Contains(page, "googet addrepo win ") {
			t.Errorf("public_url %q: no addrepo line", c.publicURL)
		}
	}
}

func TestWriteUnsignedWithPublicURL(t *testing.T) {
	off := false
	e := env(t, nil, pf("deb/games", "pool/main/h/hello/hello_1_all.deb", "hello", "1", "all", 1, 'a'))
	if err := Write(cfg("https://repo.example/crossdock", &off, debRepo, archRepo, rpmRepo), e, ""); err != nil {
		t.Fatal(err)
	}
	// index: false leaves out the one page that names every repo.
	if exists(e, "index.html") {
		t.Error("index: false must not write the landing page")
	}
	deb := read(t, e, "deb/games/index.html")
	for _, want := range []string{"URIs: https://repo.example/crossdock/deb/games\n", "Trusted: yes\n", "These repos are not signed",
		"curl -fsSLO 'https://repo.example/crossdock/deb/games/SHA256SUMS'\n"} {
		if !strings.Contains(deb, want) {
			t.Errorf("deb page lacks %q", want)
		}
	}
	for _, bad := range []string{BaseURLPlaceholder, "All repositories", "Signed-By", "crossdock.asc", "gpgv"} {
		if strings.Contains(deb, bad) {
			t.Errorf("deb page has %q", bad)
		}
	}
	if arch := read(t, e, "arch/games/index.html"); !strings.Contains(arch, "SigLevel = Optional TrustAll") || strings.Contains(arch, "pacman-key") {
		t.Errorf("unsigned pacman snippet: %s", arch)
	}
	if rpm := read(t, e, "rpm/games/index.html"); !strings.Contains(rpm, "repo_gpgcheck=0") || strings.Contains(rpm, "gpgkey") {
		t.Errorf("unsigned dnf snippet: %s", rpm)
	}
	if exists(e, "deb/games/SHA256SUMS.asc") {
		t.Error("unsigned publish wrote a signature")
	}
}

func TestMunkiPage(t *testing.T) {
	munkiRepo := config.Repo{Name: "mac", Format: config.FormatMunki, SiteDefault: true}
	e := env(t, testKey(t),
		pf("munki/mac", "pkgs/bo3/bo3-1.27.dmg", "bo3", "1.27", "arm64", 3<<30, 'a'),
		pf("munki/mac", "pkgs/loose.dmg", "loose", "", "", 10, 'b'))
	if err := Write(cfg("", nil, munkiRepo), e, ""); err != nil {
		t.Fatal(err)
	}
	page := read(t, e, "munki/mac/index.html")
	for _, want := range []string{
		"Munki · 2 packages · 3.0 GiB",
		// munkitools first where it's missing, pinned by SHA-256.
		"if [ ! -x /usr/local/munki/managedsoftwareupdate ]; then\n",
		"curl -fsSLO &#39;" + munkitoolsURL + "&#39; &amp;&amp;\n",
		"echo &#39;" + munkitoolsSHA256 + "  " + munkitoolsPkg + "&#39; | shasum -a 256 -c &amp;&amp;\n",
		"sudo installer -pkg " + munkitoolsPkg + " -target /)\n",
		"fi\nsudo defaults write /Library/Preferences/ManagedInstalls SoftwareRepoURL &#39;" + BaseURLPlaceholder + "/munki/mac&#39;\n",
		"ClientIdentifier mac\n",
		"from manifests/site_default",
		"over HTTPS or a tailnet",
		"shasum -a 256 -c --ignore-missing SHA256SUMS",
		"gpgv comes with GnuPG",
		"Open Anyway",
		`<a href="./pkgs/bo3/bo3-1.27.dmg">bo3-1.27.dmg</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("munki page lacks %q", want)
		}
	}
	if strings.Contains(page, "sha256sum -c") {
		t.Error("munki page tells Mac users to run sha256sum")
	}
	if got := read(t, e, "munki/mac/SHA256SUMS"); !strings.Contains(got, "  bo3-1.27.dmg\n") || !strings.Contains(got, "  loose.dmg\n") {
		t.Errorf("SHA256SUMS = %q", got)
	}
	if root := read(t, e, "index.html"); !strings.Contains(root, "SoftwareRepoURL &#39;"+BaseURLPlaceholder+"/munki/mac&#39;") {
		t.Error("landing page lacks the Munki snippet")
	}
}

// A deb too big for one file is split into <name>-dataN parts at the same
// version; the page lists them under their package with one install line.
func TestDebParts(t *testing.T) {
	e := env(t, nil,
		pf("deb/games", "pool/main/b/bo3/bo3-data2_1.27_all.deb", "bo3-data2", "1.27", "all", 9, 'c'),
		pf("deb/games", "pool/main/b/bo3/bo3-data10_1.27_all.deb", "bo3-data10", "1.27", "all", 9, 'd'),
		pf("deb/games", "pool/main/b/bo3/bo3_1.27_amd64.deb", "bo3", "1.27", "amd64", 1, 'a'),
		pf("deb/games", "pool/main/b/bo3/bo3-data1_1.27_all.deb", "bo3-data1", "1.27", "all", 9, 'b'),
		pf("deb/games", "pool/main/b/bo3/bo3_1.27_arm64.deb", "bo3", "1.27", "arm64", 1, 'e'),
		// A part whose package isn't there, or is at another version, stands alone.
		pf("deb/games", "pool/main/o/orphan/orphan-data1_2_all.deb", "orphan-data1", "2", "all", 1, 'f'),
		pf("deb/games", "pool/main/b/bo3/bo3-data1_1.26_all.deb", "bo3-data1", "1.26", "all", 1, '0'),
		// So does one for a single architecture: it can't join arm64's install line.
		pf("deb/games", "pool/main/q/q3/q3_1_amd64.deb", "q3", "1", "amd64", 1, '1'),
		pf("deb/games", "pool/main/q/q3/q3_1_arm64.deb", "q3", "1", "arm64", 1, '2'),
		pf("deb/games", "pool/main/q/q3/q3-data1_1_amd64.deb", "q3-data1", "1", "amd64", 1, '3'),
		// A part of a part isn't nested, and isn't hidden either.
		pf("deb/games", "pool/main/f/foo/foo_1_all.deb", "foo", "1", "all", 1, '4'),
		pf("deb/games", "pool/main/f/foo/foo-data1_1_all.deb", "foo-data1", "1", "all", 1, '5'),
		pf("deb/games", "pool/main/f/foo/foo-data1-data1_1_all.deb", "foo-data1-data1", "1", "all", 1, '6'),
	)
	p := buildRepo(debRepo, e.Packages, Common{Base: placeholderText})
	var got []string
	for _, g := range p.Groups {
		var names []string
		for _, r := range g.Rows {
			names = append(names, r.Name+"/"+r.Arch)
		}
		got = append(got, strings.Join(names, " ")+" | "+g.Install)
	}
	want := []string{
		"bo3/amd64 bo3-data1/all bo3-data2/all bo3-data10/all | sudo apt install ./bo3_1.27_amd64.deb ./bo3-data1_1.27_all.deb ./bo3-data2_1.27_all.deb ./bo3-data10_1.27_all.deb",
		"bo3/arm64 bo3-data1/all bo3-data2/all bo3-data10/all | sudo apt install ./bo3_1.27_arm64.deb ./bo3-data1_1.27_all.deb ./bo3-data2_1.27_all.deb ./bo3-data10_1.27_all.deb",
		"bo3-data1/all | ",
		"foo/all foo-data1/all | sudo apt install ./foo_1_all.deb ./foo-data1_1_all.deb",
		"foo-data1-data1/all | ",
		"orphan-data1/all | ",
		"q3/amd64 | ",
		"q3/arm64 | ",
		"q3-data1/amd64 | ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Other formats never group.
	if p := buildRepo(rpmRepo, []publish.PackageFile{
		pf("rpm/games", "Packages/b/bo3-1-1.x86_64.rpm", "bo3", "1-1", "x86_64", 1, 'a'),
		pf("rpm/games", "Packages/b/bo3-data1-1-1.noarch.rpm", "bo3-data1", "1-1", "noarch", 1, 'b'),
	}, Common{}); len(p.Groups) != 2 {
		t.Errorf("rpm groups: %+v", p.Groups)
	}
}

func TestChecksumsNameClash(t *testing.T) {
	// Should two different files share a name, both are listed by path.
	b, byPath := checksums("arch/x", []publish.PackageFile{
		pf("arch/x", "x86_64/a-1-1-any.pkg.tar.zst", "a", "1-1", "any", 1, '1'),
		pf("arch/x", "aarch64/a-1-1-any.pkg.tar.zst", "a", "1-1", "any", 1, '2'),
		pf("arch/x", "x86_64/b-1-1-x86_64.pkg.tar.zst", "b", "1-1", "x86_64", 1, '3'),
	})
	got := string(b)
	want := strings.Repeat("2", 64) + "  aarch64/a-1-1-any.pkg.tar.zst\n" +
		strings.Repeat("3", 64) + "  b-1-1-x86_64.pkg.tar.zst\n" +
		strings.Repeat("1", 64) + "  x86_64/a-1-1-any.pkg.tar.zst\n"
	if got != want || !byPath {
		t.Errorf("checksums (byPath %v):\n%s\nwant:\n%s", byPath, got, want)
	}
	// The page then says how to check those files.
	e := env(t, nil,
		pf("arch/games", "x86_64/a-1-1-any.pkg.tar.zst", "a", "1-1", "any", 1, '1'),
		pf("arch/games", "aarch64/a-1-1-any.pkg.tar.zst", "a", "1-1", "any", 1, '2'))
	if err := Write(cfg("", nil, archRepo), e, ""); err != nil {
		t.Fatal(err)
	}
	if page := read(t, e, "arch/games/index.html"); !strings.Contains(page, "lists those by their path in the repo") {
		t.Error("the page doesn't explain path entries in SHA256SUMS")
	}
	if _, byPath := checksums("arch/x", e.Packages[:1]); byPath {
		t.Error("byPath set without a clash")
	}
}

func TestChecksumsEscape(t *testing.T) {
	// Names sha256sum would escape are written as it writes them, sorted
	// by the name itself.
	b, _ := checksums("arch/x", []publish.PackageFile{
		pf("arch/x", "x86_64/b\nc-1-1-any.pkg.tar.zst", "b", "1-1", "any", 1, '1'),
		pf("arch/x", `x86_64/a\b-1-1-any.pkg.tar.zst`, "a", "1-1", "any", 1, '2'),
		pf("arch/x", "x86_64/b-1-1-any.pkg.tar.zst", "b", "1-1", "any", 1, '3'),
		pf("arch/x", "x86_64/c\r-1-1-any.pkg.tar.zst", "c", "1-1", "any", 1, '4'),
	})
	want := `\` + strings.Repeat("2", 64) + `  a\\b-1-1-any.pkg.tar.zst` + "\n" +
		`\` + strings.Repeat("1", 64) + `  b\nc-1-1-any.pkg.tar.zst` + "\n" +
		strings.Repeat("3", 64) + "  b-1-1-any.pkg.tar.zst\n" +
		`\` + strings.Repeat("4", 64) + `  c\r-1-1-any.pkg.tar.zst` + "\n"
	if string(b) != want {
		t.Errorf("checksums:\n%s\nwant:\n%s", b, want)
	}
}

func TestNaturalCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.9", "1.10", -1}, {"1.10", "1.9", 1}, {"1.27.b16986367", "1.27.b9", 1},
		{"a", "a", 0}, {"a", "ab", -1}, {"007", "7", 0}, {"2:1.0", "10:0.1", -1}, {"x1y", "x01z", -1},
	} {
		if got := naturalCompare(c.a, c.b); got != c.want {
			t.Errorf("naturalCompare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB",
		125 << 30: "125.0 GiB", 3 << 40: "3.0 TiB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHref(t *testing.T) {
	for in, want := range map[string]string{
		"pool/main/h/hello/hello_1_amd64.deb": "./pool/main/h/hello/hello_1_amd64.deb",
		"packages/tool.x86_64.1.2.3@4.goo":    "./packages/tool.x86_64.1.2.3@4.goo",
		"a b/c?d#e":                           "./a%20b/c%3Fd%23e",
		"x:y":                                 "./x:y",
	} {
		if got := href(in); got != want {
			t.Errorf("href(%q) = %q, want %q", in, got, want)
		}
	}
}
