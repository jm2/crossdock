// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package serve

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"/crossdock.asc":                                            "metadata",
		"/deb/jm2/crossdock.asc":                                    "default",
		"/README.txt":                                               "default",
	} {
		if got := Match(p).Name; got != want {
			t.Errorf("Match(%q) = %s, want %s", p, got, want)
		}
	}
}

func TestContentType(t *testing.T) {
	for p, want := range map[string]string{
		"/x/InRelease":         "text/plain; charset=utf-8",
		"/x/a.deb":             "application/vnd.debian.binary-package",
		"/x/a.pkg.tar.zst":     "application/zstd",
		"/x/jm2.db":            "application/gzip",
		"/x/a.pkg.tar.zst.sig": "application/pgp-signature",
		"/x/unknown.bin":       "application/octet-stream",
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
	rel.WriteFile("arch/jm2/x86_64/jm2.db.tar.gz", []byte("db"))
	rel.Symlink("jm2.db.tar.gz", "arch/jm2/x86_64/jm2.db")
	rel.WriteFile(".secret", []byte("no"))
	// A symlink pointing outside the release must never be followed.
	os.Symlink("/etc/passwd", filepath.Join(rel.Dir, "escape"))
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	return out, rel
}

func do(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
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
	if r.StatusCode != 200 || !bytes.Contains(body, []byte("jm2.db.tar.gz")) {
		t.Errorf("listing: %d %s", r.StatusCode, body)
	}
	r = do(t, h, "GET", "/", nil)
	body, _ = io.ReadAll(r.Body)
	if bytes.Contains(body, []byte(".secret")) {
		t.Error("listing shows hidden files")
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
}
