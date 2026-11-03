// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package rpm

import (
	"compress/gzip"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

func TestVercmp(t *testing.T) {
	// Cases from rpm's own test suite (tests/rpmvercmp.at).
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "2.0", -1}, {"2.0", "1.0", 1},
		{"2.0.1", "2.0.1", 0}, {"2.0", "2.0.1", -1}, {"2.0.1a", "2.0.1", 1},
		{"5.5p1", "5.5p2", -1}, {"5.5p10", "5.5p1", 1}, {"10xyz", "10.1xyz", -1},
		{"xyz10", "xyz10.1", -1}, {"xyz.4", "8", -1}, {"1b.fc17", "1.fc17", -1},
		{"1.0~rc1", "1.0", -1}, {"1.0~rc1", "1.0~rc2", -1}, {"1.0~rc1~git123", "1.0~rc1", -1},
		{"1.0^", "1.0", 1}, {"1.0^git1", "1.0", 1}, {"1.0^git1", "1.01", -1},
		{"1.0^git1~pre", "1.0^git1", -1}, {"1.0", "1.0^", -1}, {"1.0~rc1", "1.0^", -1},
		{"20101121", "20101121", 0}, {"010", "10", 0}, {"a", "a", 0}, {"a+", "a+", 0},
		{"1.0a", "1.0.a", 0}, {"1_0", "1.0", 0}, {"001", "1", 0},
	} {
		if got := Vercmp(tc.a, tc.b); got != tc.want {
			t.Errorf("Vercmp(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestReadPackage(t *testing.T) {
	p, err := ReadPackage(filepath.Join("testdata", "Archer-3.4.5-6.x86_64.rpm"))
	if err != nil {
		t.Fatal(err)
	}
	if p.NEVRA() != "Archer-2:3.4.5-6.x86_64" {
		t.Errorf("NEVRA = %s", p.NEVRA())
	}
	src, err := ReadPackage(filepath.Join("testdata", "empty-0-0.src.rpm"))
	if err != nil {
		t.Fatal(err)
	}
	if src.Arch != "src" {
		t.Errorf("source package arch = %q", src.Arch)
	}
	if _, err := ReadPackage(filepath.Join("testdata", "README.md")); err == nil {
		t.Error("README.md parsed as an RPM")
	}
}

// publishTestdata publishes the test RPMs minus the signed copies of
// empty-0-0.x86_64 (same NEVRA, different bytes), and returns the repo and
// source directories.
func publishTestdata(t *testing.T) (string, string) {
	t.Helper()
	src := t.TempDir()
	all, _ := filepath.Glob(filepath.Join("testdata", "*.rpm"))
	for _, f := range all {
		if strings.HasPrefix(filepath.Base(f), "empty-signed-") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(src, filepath.Base(f)), b, 0o644)
	}
	out := t.TempDir()
	now := time.Unix(1762160000, 0)
	rel, err := publish.Begin(out, now)
	if err != nil {
		t.Fatal(err)
	}
	env := &publish.Env{Release: rel, Now: now, Logf: t.Logf}
	if err := Publish(config.Repo{Name: "jm2", Format: config.FormatRPM, Source: src}, env); err != nil {
		t.Fatal(err)
	}
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(out, "current", "rpm", "jm2"), src
}

// TestSignedPackages parses the EdDSA-, RSA- and v6-signed copies.
func TestSignedPackages(t *testing.T) {
	for _, n := range []string{"empty-signed-eddsa", "empty-signed-rsa", "empty-signed-v6-multiple"} {
		p, err := ReadPackage(filepath.Join("testdata", n+"-0-0.x86_64.rpm"))
		if err != nil {
			t.Fatal(err)
		}
		if p.NEVRA() != "empty-0-0.x86_64" || p.HeaderStart <= 96 {
			t.Errorf("%s: %s, header at %d", n, p.NEVRA(), p.HeaderStart)
		}
	}
}

func TestPublishWellFormed(t *testing.T) {
	repo, _ := publishTestdata(t)
	files, _ := filepath.Glob(filepath.Join(repo, "repodata", "*.xml.gz"))
	if len(files) != 3 {
		t.Fatalf("repodata has %v", files)
	}
	for _, f := range append(files, filepath.Join(repo, "repodata", "repomd.xml")) {
		r, _ := os.Open(f)
		var rd io.Reader = r
		if strings.HasSuffix(f, ".gz") {
			gz, err := gzip.NewReader(r)
			if err != nil {
				t.Fatal(err)
			}
			rd = gz
		}
		dec := xml.NewDecoder(rd)
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", filepath.Base(f), err)
			}
		}
		r.Close()
	}
	if _, err := os.Stat(filepath.Join(repo, "Packages", "a", "Archer-3.4.5-6.x86_64.rpm")); err != nil {
		t.Error(err)
	}
}

// TestCreaterepoC checks the published repodata with createrepo_c itself,
// when its Python bindings are installed (pip install createrepo_c).
func TestCreaterepoC(t *testing.T) {
	if err := exec.Command("python3", "-c", "import createrepo_c").Run(); err != nil {
		t.Skip("createrepo_c Python bindings not installed")
	}
	repo, src := publishTestdata(t)
	out, err := exec.Command("python3", filepath.Join("testdata", "validate_repodata.py"), repo, src).CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateNEVRA(t *testing.T) {
	src := t.TempDir()
	b, _ := os.ReadFile(filepath.Join("testdata", "fake_bash-1.1.1-1.x86_64.rpm"))
	os.WriteFile(filepath.Join(src, "a.rpm"), b, 0o644)
	os.WriteFile(filepath.Join(src, "b.rpm"), append(b, 0), 0o644)
	rel, _ := publish.Begin(t.TempDir(), time.Now())
	err := Publish(config.Repo{Name: "x", Source: src}, &publish.Env{Release: rel, Now: time.Now(), Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("err = %v", err)
	}
}
