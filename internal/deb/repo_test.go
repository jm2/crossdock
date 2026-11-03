// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// writeDeb builds a minimal .deb (ar with debian-binary, control.tar.gz and
// an empty data.tar.gz).
func writeDeb(t *testing.T, dir, name, version string) {
	t.Helper()
	tgz := func(files map[string]string) []byte {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		tw := tar.NewWriter(gz)
		for n, c := range files {
			tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c))})
			tw.Write([]byte(c))
		}
		tw.Close()
		gz.Close()
		return b.Bytes()
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nMaintainer: t <t@example.invalid>\nDescription: test\n", name, version)
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", tgz(map[string]string{"./control": control})}, {"data.tar.gz", tgz(nil)}} {
		fmt.Fprintf(&ar, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name+"_"+version+".deb"), ar.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func publishOnce(t *testing.T, out string, repo config.Repo, at int64) string {
	t.Helper()
	now := time.Unix(at, 0)
	rel, err := publish.Begin(out, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Publish(repo, &publish.Env{Release: rel, Now: now, Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	return rel.Dir
}

func byHashFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "deb/x/dists/stable/main/binary-amd64/by-hash/SHA256/*"))
	for _, f := range files {
		got[filepath.Base(f)] = true
	}
	return got
}

func TestByHashCarryOver(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	repo := config.Repo{Name: "x", Format: config.FormatDeb, Source: src, Suite: "stable", Codename: "stable",
		Component: "main", Origin: "x", Label: "x", Architectures: []string{"amd64"}}

	writeDeb(t, src, "a", "1.0")
	r1 := publishOnce(t, out, repo, 1000)
	writeDeb(t, src, "b", "1.0")
	r2 := publishOnce(t, out, repo, 2000)
	writeDeb(t, src, "c", "1.0")
	r3 := publishOnce(t, out, repo, 3000)

	g1, g2, g3 := byHashFiles(t, r1), byHashFiles(t, r2), byHashFiles(t, r3)
	if len(g1) != 2 {
		t.Fatalf("first release by-hash: %v", g1)
	}
	for h := range g1 {
		if !g2[h] {
			t.Errorf("release 2 lost release 1's %s", h)
		}
		if g3[h] {
			t.Errorf("release 3 still carries release 1's %s (only one generation is kept)", h)
		}
	}
	if len(g2) != 4 || len(g3) != 4 {
		t.Errorf("by-hash counts: %d, %d (want 4: this release + the previous)", len(g2), len(g3))
	}
}

func TestValidUntil(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	writeDeb(t, src, "a", "1.0")
	repo := config.Repo{Name: "x", Format: config.FormatDeb, Source: src, Suite: "stable", Codename: "stable",
		Component: "main", Origin: "x", Label: "x", ValidFor: "7d"}
	dir := publishOnce(t, out, repo, 1762160000)
	rel, _ := os.ReadFile(filepath.Join(dir, "deb/x/dists/stable/Release"))
	p, err := ParseParagraph(rel)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Get("Valid-Until"); got != "Mon, 10 Nov 2025 08:53:20 UTC" {
		t.Errorf("Valid-Until = %q", got)
	}
	if p.Get("Acquire-By-Hash") != "yes" {
		t.Error("no Acquire-By-Hash")
	}
	if !strings.HasPrefix(string(rel), "Origin: x\n") {
		t.Errorf("Release:\n%s", rel)
	}
}
