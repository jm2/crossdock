// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package serve

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/pages"
	"github.com/jm2/crossdock/internal/publish"
)

func TestMatch(t *testing.T) {
	for p, want := range map[string]string{
		"/deb/jm2/dists/stable/InRelease":                           "metadata",
		"/deb/jm2/dists/stable/Release.gpg":                         "metadata",
		"/deb/jm2/dists/stable/main/binary-amd64/Packages.gz":       "metadata",
		"/deb/jm2/dists/stable/main/binary-amd64/by-hash/SHA256/ab": "by-hash",
		"/arch/jm2/x86_64/jm2.db":                                   "metadata",
		"/arch/jm2/x86_64/jm2.files.tar.gz.sig":                     "metadata",
		"/arch/jm2/x86_64/tributary-bin-1-1-x86_64.pkg.tar.zst":     "package",
		"/arch/jm2/x86_64/tributary-bin-1-1-x86_64.pkg.tar.zst.sig": "package",
		"/deb/jm2/pool/main/h/hello/hello_1.0_amd64.deb":            "package",
		"/rpm/jm2/x86_64/repodata/repomd.xml":                       "metadata",
		"/goo/jm2/index":                                            "metadata",
		"/goo/jm2/index.gz":                                         "metadata",
		"/goo/jm2/packages/tool.x86_64.1.2.3@4.goo":                 "package",
		"/rpm/jm2/repodata/repomd.xml.asc":                          "metadata",
		"/rpm/jm2/repodata/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-primary.xml.gz": "by-hash",
		"/rpm/jm2/Packages/a/Archer-3.4.5-6.x86_64.rpm":                                                     "package",
		"/rpm/jm2/Packages/a/repodata-tool-1-1.noarch.rpm":                                                  "package",
		"/crossdock.asc":                                  "metadata",
		"/index.html":                                     "metadata",
		"/deb/jm2/index.html":                             "metadata",
		"/rpm/jm2/SHA256SUMS":                             "metadata",
		"/arch/jm2/SHA256SUMS.asc":                        "metadata",
		"/goo/jm2/notindex.html":                          "default",
		"/munki/games/catalogs/all":                       "munki",
		"/munki/games/catalogs/index":                     "munki", // not metadata's /index
		"/munki/games/manifests/site_default":             "munki",
		"/munki/games/manifests/by-hash/x":                "munki", // not by-hash's immutable
		"/munki/games/pkgsinfo/bo3/bo3-1.27.plist":        "munki",
		"/munki/games/icons/_icon_hashes.plist":           "munki",
		"/munki/games/icons/bo3.png":                      "default",
		"/munki/games/pkgs/bo3/bo3-1.27.dmg":              "package",
		"/munki/games/index.html":                         "metadata",
		"/munki/games/SHA256SUMS":                         "metadata",
		"/deb/jm2/pool/main/m/munki/catalogs/x_1_all.deb": "package",
		"/deb/jm2/crossdock.asc":                          "default",
		"/README.txt":                                     "default",
	} {
		if got := Match(p).Name; got != want {
			t.Errorf("Match(%q) = %s, want %s", p, got, want)
		}
	}
}

func TestContentType(t *testing.T) {
	for p, want := range map[string]string{
		"/x/InRelease":                      "text/plain; charset=utf-8",
		"/x/a.deb":                          "application/vnd.debian.binary-package",
		"/x/a.pkg.tar.zst":                  "application/zstd",
		"/x/jm2.db":                         "application/gzip",
		"/x/a.pkg.tar.zst.sig":              "application/pgp-signature",
		"/x/unknown.bin":                    "application/octet-stream",
		"/x/SHA256SUMS":                     "text/plain; charset=utf-8",
		"/x/SHA256SUMS.asc":                 "text/plain; charset=utf-8",
		"/x/index.html":                     "text/html; charset=utf-8",
		"/munki/g/pkgs/a/a-1.dmg":           "application/x-apple-diskimage",
		"/munki/g/icons/a.png":              "image/png",
		"/munki/g/catalogs/production":      "application/xml",
		"/munki/g/pkgsinfo/a/a-1.plist":     "application/xml",
		"/munki/g/catalogs/evil.html":       "application/xml", // names come from uploads
		"/munki/g/manifests/groups/x.json":  "application/xml",
		"/munki/g/icons/_icon_hashes.plist": "application/xml",
	} {
		if got := ContentType(p, Match(p)); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", p, got, want)
		}
	}
}

// fixture publishes a small tree and returns the output root and release.
func fixture(t *testing.T) (string, *publish.Release) {
	t.Helper()
	out := t.TempDir()
	rel, err := publish.Begin(out, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "hello.deb")
	os.WriteFile(payload, []byte("0123456789"), 0o644)
	if _, err := rel.AddFile(payload, "deb/jm2/pool/main/h/hello/hello_1_amd64.deb"); err != nil {
		t.Fatal(err)
	}
	rel.WriteFile("deb/jm2/dists/stable/InRelease", []byte("signed"))
	rel.WriteFile("munki/mac/manifests/evil.html", []byte("<plist/>"))
	rel.WriteFile("arch/jm2/x86_64/jm2.db.tar.gz", []byte("db"))
	rel.Symlink("jm2.db.tar.gz", "arch/jm2/x86_64/jm2.db")
	rel.WriteFile(".secret", []byte("no"))
	// A symlink pointing outside the release must never be followed (where
	// the platform lets us make one).
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.Symlink(outside, filepath.Join(rel.Dir, "escape"))
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	return out, rel
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v // net/http reads Host from here, not the header map
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestHandler(t *testing.T) {
	out, rel := fixture(t)
	h := &Handler{Output: out}

	r := do(t, h, "GET", "/deb/jm2/dists/stable/InRelease", nil)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(body) != "signed" {
		t.Fatalf("InRelease: %d %q", r.StatusCode, body)
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("InRelease Cache-Control = %q", cc)
	}
	if id := r.Header.Get(ReleaseHeader); id != rel.ID {
		t.Errorf("%s = %q, want %q", ReleaseHeader, id, rel.ID)
	}

	// Rules match the path of the file opened, so repeated slashes don't
	// change the headers: an uploaded manifest named evil.html is never
	// served as HTML.
	for _, p := range []string{"/munki/mac/manifests/evil.html", "/munki//mac//manifests/evil.html", "//munki/mac/manifests/evil.html"} {
		r = do(t, h, "GET", p, nil)
		if ct, cc := r.Header.Get("Content-Type"), r.Header.Get("Cache-Control"); r.StatusCode != 200 || ct != "application/xml" || cc != "no-cache" {
			t.Errorf("GET %s: %d, Content-Type %q, Cache-Control %q", p, r.StatusCode, ct, cc)
		}
	}

	// Internal symlink is followed.
	r = do(t, h, "GET", "/arch/jm2/x86_64/jm2.db", nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/gzip" {
		t.Errorf("jm2.db: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}

	// Range and conditional requests.
	deb := "/deb/jm2/pool/main/h/hello/hello_1_amd64.deb"
	r = do(t, h, "GET", deb, map[string]string{"Range": "bytes=2-4"})
	body, _ = io.ReadAll(r.Body)
	if r.StatusCode != http.StatusPartialContent || string(body) != "234" {
		t.Errorf("range: %d %q", r.StatusCode, body)
	}
	etag := r.Header.Get("ETag")
	r = do(t, h, "GET", deb, map[string]string{"If-None-Match": etag})
	if r.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", r.StatusCode)
	}
	r = do(t, h, "HEAD", deb, nil)
	body, _ = io.ReadAll(r.Body)
	if r.StatusCode != 200 || len(body) != 0 || r.Header.Get("Content-Length") != "10" {
		t.Errorf("HEAD: %d len=%d cl=%s", r.StatusCode, len(body), r.Header.Get("Content-Length"))
	}

	for _, p := range []string{"/.secret", "/escape", "/deb/../../etc/passwd", "/deb/jm2/", "/nope", "/deb/jm2/dists/./stable/InRelease"} {
		if r := do(t, h, "GET", p, nil); r.StatusCode != 404 {
			t.Errorf("GET %s: %d, want 404", p, r.StatusCode)
		}
	}
	if r := do(t, h, "POST", "/deb/jm2/dists/stable/InRelease", nil); r.StatusCode != 405 {
		t.Errorf("POST: %d", r.StatusCode)
	}
}

func TestIndex(t *testing.T) {
	out, _ := fixture(t)
	h := &Handler{Output: out, Index: true}
	if r := do(t, h, "GET", "/arch/jm2", nil); r.StatusCode != http.StatusMovedPermanently {
		t.Errorf("dir without slash: %d", r.StatusCode)
	}
	r := do(t, h, "GET", "/arch/jm2/x86_64/", nil)
	body, _ := io.ReadAll(r.Body)
	// The db symlink shows its target's size; the escaping symlink isn't listed.
	if r.StatusCode != 200 || !bytes.Contains(body, []byte(`<a href="./jm2.db.tar.gz">jm2.db.tar.gz</a></td><td class="num" title="2 bytes">2 B</td>`)) ||
		!bytes.Contains(body, []byte(`<a href="./jm2.db">jm2.db</a>`)) || !bytes.Contains(body, []byte(`<a href="../">../</a>`)) {
		t.Errorf("listing: %d %s", r.StatusCode, body)
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("listing Cache-Control = %q", cc)
	}
	r = do(t, h, "GET", "/", nil)
	body, _ = io.ReadAll(r.Body)
	for _, bad := range []string{".secret", "escape", `href="../"`, "<script"} {
		if bytes.Contains(body, []byte(bad)) {
			t.Errorf("root listing contains %q:\n%s", bad, body)
		}
	}
	// Directories come first, then files, each sorted by name.
	arch, deb := bytes.Index(body, []byte(`href="./arch/"`)), bytes.Index(body, []byte(`href="./deb/"`))
	if arch < 0 || deb < 0 || arch > deb {
		t.Errorf("directories out of order:\n%s", body)
	}
	for _, p := range []string{"/.secret", "/.crossdock/", "/escape", "/arch/jm2/../.secret"} {
		if r := do(t, h, "GET", p, nil); r.StatusCode != 404 {
			t.Errorf("GET %s with listings on: %d, want 404", p, r.StatusCode)
		}
	}
}

// pageFixture publishes a release with pages: one carrying the base URL
// placeholder, as publish writes it without serve.public_url, and one
// with the address written in.
func pageFixture(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	rel, err := publish.Begin(out, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	rel.WriteFile("index.html", []byte("<pre>URIs: "+pages.BaseURLPlaceholder+"/deb/jm2\ngpgkey="+pages.BaseURLPlaceholder+"/crossdock.asc</pre>"))
	rel.WriteFile("deb/jm2/index.html", []byte("<pre>URIs: https://repo.example/deb/jm2</pre>"))
	rel.WriteFile("deb/jm2/SHA256SUMS", []byte("abc  x.deb\n"))
	rel.WriteFile("rpm/jm2/repodata/repomd.xml", []byte("<repomd/>"))
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPages(t *testing.T) {
	out := pageFixture(t)
	for _, index := range []bool{true, false} {
		h := &Handler{Output: out, Index: index}
		// index.html wins over the listing, and is served with listings off.
		r := do(t, h, "GET", "/", nil)
		body, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 || !bytes.Contains(body, []byte("URIs: http://example.com/deb/jm2\ngpgkey=http://example.com/crossdock.asc")) {
			t.Fatalf("index=%v: root page: %d %s", index, r.StatusCode, body)
		}
		if ct, cc := r.Header.Get("Content-Type"), r.Header.Get("Cache-Control"); ct != "text/html; charset=utf-8" || cc != "no-cache" {
			t.Errorf("root page headers: %q %q", ct, cc)
		}
		if r := do(t, h, "GET", "/deb/jm2", nil); r.StatusCode != http.StatusMovedPermanently {
			t.Errorf("index=%v: repo dir without slash: %d", index, r.StatusCode)
		}
		// Without an index.html, only a listing can answer.
		want := http.StatusNotFound
		if index {
			want = http.StatusOK
		}
		if r := do(t, h, "GET", "/rpm/jm2/", nil); r.StatusCode != want {
			t.Errorf("index=%v: dir without index.html: %d, want %d", index, r.StatusCode, want)
		}
	}
	h := &Handler{Output: out, Index: true}

	// The address comes from the request; a TLS-terminating proxy sets the scheme.
	r := do(t, h, "GET", "/index.html", map[string]string{"Host": "nas.lan:8080", "X-Forwarded-Proto": "https"})
	body, _ := io.ReadAll(r.Body)
	if !bytes.Contains(body, []byte("URIs: https://nas.lan:8080/deb/jm2")) || r.Header.Get("Vary") != "X-Forwarded-Proto" {
		t.Errorf("forwarded https: %s (Vary %q)", body, r.Header.Get("Vary"))
	}
	r = do(t, h, "GET", "/", map[string]string{"Host": "[fd00::5]:8080"})
	body, _ = io.ReadAll(r.Body)
	if !bytes.Contains(body, []byte("URIs: http://[fd00::5]:8080/deb/jm2")) {
		t.Errorf("IPv6 host: %s", body)
	}
	// Each address gets its own ETag, so a cache never mixes them up.
	e1 := do(t, h, "GET", "/", map[string]string{"Host": "a.lan"}).Header.Get("ETag")
	e2 := do(t, h, "GET", "/", map[string]string{"Host": "b.lan"}).Header.Get("ETag")
	if e1 == "" || e1 == e2 {
		t.Errorf("ETags %q and %q should differ", e1, e2)
	}
	if r := do(t, h, "GET", "/", map[string]string{"Host": "a.lan", "If-None-Match": e1}); r.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match on a page: %d", r.StatusCode)
	}
	// A Host that isn't a plain host name is never echoed: the placeholder stays.
	for _, host := range []string{`x"><script>alert(1)</script>`, "a b", "-x.lan", "x.lan:99999999", ""} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if b := rec.Body.String(); !strings.Contains(b, pages.BaseURLPlaceholder) || strings.Contains(b, "<script") {
			t.Errorf("Host %q: %s", host, b)
		}
	}
	// A page published with public_url is served byte for byte, with the
	// file's own ETag and range support.
	r = do(t, h, "GET", "/deb/jm2/", map[string]string{"Range": "bytes=0-4"})
	body, _ = io.ReadAll(r.Body)
	if r.StatusCode != http.StatusPartialContent || string(body) != "<pre>" || r.Header.Get("Vary") != "" {
		t.Errorf("public_url page: %d %q Vary=%q", r.StatusCode, body, r.Header.Get("Vary"))
	}
	r = do(t, h, "GET", "/deb/jm2/SHA256SUMS", nil)
	if ct := r.Header.Get("Content-Type"); r.StatusCode != 200 || ct != "text/plain; charset=utf-8" {
		t.Errorf("SHA256SUMS: %d %q", r.StatusCode, ct)
	}
}

func TestRequestBaseURL(t *testing.T) {
	for _, c := range []struct{ host, proto, want string }{
		{"nas", "", "http://nas"},
		{"nas.lan:8080", "", "http://nas.lan:8080"},
		{"10.0.0.5:8080", "https", "https://10.0.0.5:8080"},
		{"repo.example", "HTTPS, http", "https://repo.example"},
		{"repo.example", "gopher", "http://repo.example"},
		{"[::1]:8080", "", "http://[::1]:8080"},
		{"nas_lan", "", ""},
		{"nas/../x", "", ""},
		{"nas.lan.", "", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = c.host
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := requestBaseURL(r); got != c.want {
			t.Errorf("Host %q, proto %q: %q, want %q", c.host, c.proto, got, c.want)
		}
	}
}

func TestSwapAndControl(t *testing.T) {
	out, first := fixture(t)
	h := &Handler{Output: out}

	r := do(t, h, "GET", ControlPrefix+"healthz", nil)
	if r.StatusCode != 200 {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
	second, err := publish.Begin(out, time.Unix(2000, 0))
	if err != nil {
		t.Fatal(err)
	}
	second.WriteFile("deb/jm2/dists/stable/InRelease", []byte("v2"))
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	r = do(t, h, "GET", "/deb/jm2/dists/stable/InRelease", nil)
	body, _ := io.ReadAll(r.Body)
	if string(body) != "v2" || r.Header.Get(ReleaseHeader) != second.ID {
		t.Errorf("after swap: %q from %s (first was %s)", body, r.Header.Get(ReleaseHeader), first.ID)
	}
	r = do(t, h, "GET", ControlPrefix+"release", nil)
	var got map[string]any
	json.NewDecoder(r.Body).Decode(&got)
	if got["release"] != second.ID {
		t.Errorf("release endpoint = %v", got)
	}
}

func TestNoRelease(t *testing.T) {
	h := &Handler{Output: t.TempDir()}
	if r := do(t, h, "GET", "/x", nil); r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no release: %d", r.StatusCode)
	}
	if r := do(t, h, "GET", ControlPrefix+"healthz", nil); r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("healthz without release: %d", r.StatusCode)
	}
}

var update = flag.Bool("update", false, "rewrite testdata/nginx.golden")

// The whole generated config is pinned, so any change to it shows up in
// review. Regenerate with: go test ./internal/serve -run NginxGolden -update
func TestNginxGolden(t *testing.T) {
	var b strings.Builder
	if err := WriteNginx(&b, NginxOptions{Output: "/var/lib/crossdock/repo", ServerName: "repo.example", Index: true}); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "nginx.golden")
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("nginx config differs from %s (rerun with -update and review the diff):\n%s", golden, b.String())
	}
}

func TestNginxConfig(t *testing.T) {
	var b strings.Builder
	if err := WriteNginx(&b, NginxOptions{Output: "/srv/cd", ServerName: "repo.example"}); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	for _, want := range []string{"root /srv/cd/current;", "server_name repo.example;", "open_file_cache off;",
		`add_header Cache-Control "no-cache" always;`, "application/vnd.debian.binary-package    deb;"} {
		if !strings.Contains(s, want) {
			t.Errorf("nginx config missing %q", want)
		}
	}
	// Every non-default rule must appear verbatim, so the two servers agree.
	for _, r := range Rules {
		if r.Name != "default" && !strings.Contains(s, `"`+r.Pattern+`"`) {
			t.Errorf("rule %s missing from nginx config", r.Name)
		}
	}
	if strings.Count(s, "{") != strings.Count(s, "}") {
		t.Error("unbalanced braces")
	}
	// Listings are opt-out, pages and the URL filling are always there.
	for _, want := range []string{"autoindex off;", "index index.html;", "sub_filter_once off;",
		`sub_filter "` + pages.BaseURLPlaceholder + `" $crossdock_base_url;`, `"~` + hostRE.String() + `"`} {
		if !strings.Contains(s, want) {
			t.Errorf("nginx config missing %q", want)
		}
	}
}
