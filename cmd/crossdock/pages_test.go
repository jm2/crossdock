// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/pages"
	"github.com/jm2/crossdock/internal/publish"
	"github.com/jm2/crossdock/internal/serve"
)

func TestIndexSetting(t *testing.T) {
	off, on := false, true
	for _, c := range []struct {
		args []string
		cfg  *bool
		want bool
	}{
		{nil, nil, true},                       // on by default
		{nil, &off, false},                     // config turns it off
		{[]string{"-index"}, &off, true},       // an explicit flag beats the config
		{[]string{"-index=false"}, &on, false}, // either way
		{[]string{"-index=false"}, nil, false},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		index := fs.Bool("index", true, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got := indexSetting(fs, *index, config.Serve{Index: c.cfg}); got != c.want {
			t.Errorf("args %v, config %v: %v, want %v", c.args, c.cfg, got, c.want)
		}
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, data)
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// writeDeb writes a minimal .deb (ar: debian-binary, control.tar.gz,
// data.tar.gz) without dpkg-deb, so the test runs everywhere.
func writeDeb(t *testing.T, dir, name, version, arch string) {
	t.Helper()
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: t <t@example.invalid>\nDescription: page test %s\n", name, version, arch, name)
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{
		{"debian-binary", []byte("2.0\n")},
		{"control.tar.gz", tarGz(t, map[string]string{"./control": control})},
		{"data.tar.gz", tarGz(t, map[string]string{"./usr/share/doc/" + name + "/README": name})},
	} {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name+"_"+version+"_"+arch+".deb"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDMG writes a stand-in disk image (a data fork, a resource plist and
// the UDIF trailer crossdock checks) and its pkginfo sidecar.
func writeDMG(t *testing.T, dir, name, version string) {
	t.Helper()
	res := []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict/></plist>`)
	data := []byte("disk image of " + name)
	k := make([]byte, 512)
	copy(k, "koly")
	binary.BigEndian.PutUint32(k[4:], 4)
	binary.BigEndian.PutUint32(k[8:], 512)
	binary.BigEndian.PutUint64(k[32:], uint64(len(data)))
	binary.BigEndian.PutUint64(k[216:], uint64(len(data)))
	binary.BigEndian.PutUint64(k[224:], uint64(len(res)))
	img := filepath.Join(dir, name+"-"+version+".dmg")
	if err := os.WriteFile(img, slices.Concat(data, res, k), 0o644); err != nil {
		t.Fatal(err)
	}
	pkginfo := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>name</key><string>%s</string><key>version</key><string>%s</string>
<key>description</key><string>page test %s</string>
<key>installer_type</key><string>copy_from_dmg</string>
<key>items_to_copy</key><array><dict><key>source_item</key><string>%s.app</string><key>destination_path</key><string>/Applications</string></dict></array>
<key>installs</key><array><dict><key>type</key><string>application</string><key>path</key><string>/Applications/%s.app</string></dict></array>
<key>installed_size</key><integer>100</integer>
</dict></plist>
`, name, version, name, name, name)
	if err := os.WriteFile(img+".pkginfo", []byte(pkginfo), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGooPkg(t *testing.T, dir, name, version string) {
	t.Helper()
	spec := fmt.Sprintf(`{"Name":%q,"Version":%q,"Arch":"x86_64","Description":"page test %s\nmore"}`, name, version, name)
	b := tarGz(t, map[string]string{name + ".pkgspec": spec})
	if err := os.WriteFile(filepath.Join(dir, name+".x86_64."+version+".goo"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPagesEndToEnd publishes one signed repo of every format and checks
// that each repo's index.html and SHA256SUMS cover every package file in
// the release with the right digest, that SHA256SUMS.asc verifies with the
// external verifiers clients use (gpgv, Sequoia's sqv), and that serve
// fills in the address the pages were requested at.
func TestPagesEndToEnd(t *testing.T) {
	work := t.TempDir()
	in := func(f string) string {
		d := filepath.Join(work, "in", f)
		os.MkdirAll(d, 0o755)
		return d
	}
	writeDeb(t, in("deb"), "bo3", "1.27-1", "amd64")
	writeDeb(t, in("deb"), "bo3-data1", "1.27-1", "all")
	writeDeb(t, in("deb"), "bo3-data2", "1.27-1", "all")
	writeArchPkg(t, in("arch"), "quake")
	for _, f := range []string{"Archer-3.4.5-6.x86_64.rpm", "Rimmer-1.0.2-2.x86_64.rpm"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "rpm", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(in("rpm"), f), b, 0o644)
	}
	writeGooPkg(t, in("goo"), "tool", "1.2.3@4")
	writeDMG(t, in("munki"), "Game", "1.2")

	key := filepath.Join(work, "signing.asc")
	if err := cmdKeygen([]string{"-uid", "crossdock pages <test@example.invalid>", "-out", key}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(work, "crossdock.yaml")
	os.WriteFile(cfgPath, []byte(`output: out
signing:
  key_file: signing.asc
repos:
  - {name: games, format: deb, source: in/deb, architectures: [amd64]}
  - {name: games, format: arch, source: in/arch, architectures: [x86_64]}
  - {name: games, format: rpm, source: in/rpm}
  - {name: win, format: goo, source: in/goo}
  - {name: mac, format: munki, source: in/munki}
`), 0o644)
	if err := cmdPublish([]string{"-q", "-c", cfgPath}); err != nil {
		t.Fatal(err)
	}
	cur := publish.CurrentDir(filepath.Join(work, "out"))

	suffixes := []string{".deb", ".rpm", ".goo", ".pkg.tar.gz", ".dmg"}
	for _, repo := range []string{"deb/games", "arch/games", "rpm/games", "goo/win", "munki/mac"} {
		dir := filepath.Join(cur, filepath.FromSlash(repo))
		pkgs, err := publish.FindFiles(dir, suffixes...)
		if err != nil || len(pkgs) == 0 {
			t.Fatalf("%s: no packages (%v)", repo, err)
		}
		sums, _ := os.ReadFile(filepath.Join(dir, pages.SumsName))
		page, _ := os.ReadFile(filepath.Join(dir, pages.IndexName))
		for _, p := range pkgs {
			b, _ := os.ReadFile(p)
			sum := sha256.Sum256(b)
			line := hex.EncodeToString(sum[:]) + "  " + filepath.Base(p) + "\n"
			if !strings.Contains(string(sums), line) {
				t.Errorf("%s/SHA256SUMS lacks %q:\n%s", repo, line, sums)
			}
			rel, _ := filepath.Rel(dir, p)
			if !bytes.Contains(page, []byte(`href="./`+filepath.ToSlash(rel)+`"`)) || !bytes.Contains(page, []byte(hex.EncodeToString(sum[:]))) {
				t.Errorf("%s/index.html lacks %s or its digest", repo, rel)
			}
		}
		if n := strings.Count(string(sums), "\n"); n != len(pkgs) {
			t.Errorf("%s/SHA256SUMS has %d lines for %d packages", repo, n, len(pkgs))
		}
		verifySums(t, cur, dir)
	}
	// Each repo page stands in for its directory listing and links its contents.
	rpmPage, _ := os.ReadFile(filepath.Join(cur, "rpm", "games", pages.IndexName))
	if !bytes.Contains(rpmPage, []byte(`<a href="./Packages/">Packages/</a> <a href="./SHA256SUMS">SHA256SUMS</a> <a href="./SHA256SUMS.asc">SHA256SUMS.asc</a> <a href="./repodata/">repodata/</a>`)) {
		t.Errorf("rpm page lacks the Browse links:\n%s", rpmPage)
	}
	deb, _ := os.ReadFile(filepath.Join(cur, "deb", "games", pages.IndexName))
	if !bytes.Contains(deb, []byte("sudo apt install ./bo3_1.27-1_amd64.deb ./bo3-data1_1.27-1_all.deb ./bo3-data2_1.27-1_all.deb")) {
		t.Errorf("deb page lacks the install line for the split package:\n%s", deb)
	}

	// Through serve, the snippets name the address the page was fetched from.
	srv := httptest.NewServer(&serve.Handler{Output: filepath.Join(work, "out"), Index: true})
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{"URIs: " + srv.URL + "/deb/games\n", "Server = " + srv.URL + "/arch/$repo/$arch\n",
		"baseurl=" + srv.URL + "/rpm/games\n", "googet addrepo win " + srv.URL + "/goo/win\n",
		"SoftwareRepoURL " + srv.URL + "/munki/mac\n"} {
		if !bytes.Contains(root, []byte(want)) {
			t.Errorf("served landing page lacks %q", want)
		}
	}
	if bytes.Contains(root, []byte(pages.BaseURLPlaceholder)) {
		t.Error("served landing page still has the placeholder")
	}
	// The Munki repo is what a client reads: its catalog lists the image
	// with the digest the page shows.
	resp, err = http.Get(srv.URL + "/munki/mac/catalogs/all")
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/xml" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("catalogs/all served as %q, %q", ct, resp.Header.Get("Cache-Control"))
	}
	img, _ := os.ReadFile(filepath.Join(cur, "munki", "mac", "pkgs", "Game", "Game-1.2.dmg"))
	sum := sha256.Sum256(img)
	for _, want := range []string{"<string>Game/Game-1.2.dmg</string>", "<string>" + hex.EncodeToString(sum[:]) + "</string>"} {
		if !bytes.Contains(cat, []byte(want)) {
			t.Errorf("catalogs/all lacks %s:\n%s", want, cat)
		}
	}
}

// verifySums checks dir's SHA256SUMS.asc with every verifier installed.
// CI runs gpgv on Ubuntu and sqv on Debian stable.
func verifySums(t *testing.T, cur, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The runners' gpgv is Git for Windows' MSYS build, which reads a
		// --keyring with no "/" (any C:\ path) as a name in its home
		// directory. The sign tests skip external GnuPG here too.
		t.Log("external verifiers not run on Windows")
		return
	}
	sums, sig := filepath.Join(dir, pages.SumsName), filepath.Join(dir, pages.SumsName+pages.SumsSigExt)
	verifiers := map[string][]string{
		"gpgv": {"--keyring", filepath.Join(cur, "crossdock.gpg"), sig, sums},
		"sqv":  {"--keyring", filepath.Join(cur, "crossdock.asc"), sig, sums},
	}
	for bin, args := range verifiers {
		if _, err := exec.LookPath(bin); err != nil {
			t.Logf("%s not installed; not checked with it", bin)
			continue
		}
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Errorf("%s rejected %s: %v\n%s", bin, sig, err, out)
		}
		// A changed SHA256SUMS must fail.
		tampered := filepath.Join(t.TempDir(), pages.SumsName)
		b, _ := os.ReadFile(sums)
		os.WriteFile(tampered, append(b, "0000  evil.deb\n"...), 0o644)
		if out, err := exec.Command(bin, args[0], args[1], sig, tampered).CombinedOutput(); err == nil {
			t.Errorf("%s accepted a tampered SHA256SUMS:\n%s", bin, out)
		}
	}
}
