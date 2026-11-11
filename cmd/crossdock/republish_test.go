// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// writeArchPkg writes a minimal pacman package named name-1-1-x86_64.
func writeArchPkg(t *testing.T, dir, name string) string {
	t.Helper()
	info := "pkgname = " + name + "\npkgbase = " + name + "\npkgver = 1-1\n" +
		"pkgdesc = test\nbuilddate = 1760000000\nsize = 1\narch = x86_64\n"
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, data string }{{".PKGINFO", info}, {".MTREE", "x"}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(f.data))})
		tw.Write([]byte(f.data))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(dir, name+"-1-1-x86_64.pkg.tar.gz")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeConfig(t *testing.T, work string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"output": "out",
		"repos": []map[string]any{{
			"name": "local", "format": "arch", "source": "incoming",
			"architectures": []string{"x86_64"},
		}},
	})
	p := filepath.Join(work, "crossdock.yaml")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func servedDB(t *testing.T, work string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(publish.CurrentDir(filepath.Join(work, "out")), "arch", "local", "x86_64", "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	return strings.Join(names, " ")
}

// An upload that lands while a publish runs must be published by that same
// run, not left for whatever changes next (issue #7).
func TestPublishRepeatsWhenSourcesChange(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	writeArchPkg(t, src, "first")
	cfg := writeConfig(t, work)

	commits := 0
	testHookBeforeCommit = func() {
		if commits++; commits == 1 {
			writeArchPkg(t, src, "late")
		}
	}
	defer func() { testHookBeforeCommit = nil }()

	if err := cmdPublish([]string{"-q", "-allow-unsigned", "-c", cfg}); err != nil {
		t.Fatal(err)
	}
	if commits != 2 {
		t.Errorf("published %d times, want 2", commits)
	}
	if db := servedDB(t, work); !strings.Contains(db, "late-1-1") || !strings.Contains(db, "first-1-1") {
		t.Errorf("served db lacks a package: %s", db)
	}
}

// Sources that never hold still stop after maxPublishRounds, still published.
func TestPublishStopsAfterMaxRounds(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	writeArchPkg(t, src, "first")
	cfg := writeConfig(t, work)

	commits := 0
	testHookBeforeCommit = func() {
		commits++
		writeArchPkg(t, src, "pkg"+strings.Repeat("x", commits))
	}
	defer func() { testHookBeforeCommit = nil }()

	if err := cmdPublish([]string{"-q", "-allow-unsigned", "-c", cfg}); err != nil {
		t.Fatal(err)
	}
	if commits != maxPublishRounds {
		t.Errorf("published %d times, want %d", commits, maxPublishRounds)
	}
}

func TestSourceStateSeesChanges(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	p := writeArchPkg(t, src, "first")
	cfgPath := writeConfig(t, work)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	state := func() string {
		t.Helper()
		s, err := sourceState(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s0 := state()
	// A staged dotfile is ignored, as publishing ignores it.
	os.WriteFile(filepath.Join(src, ".x-1-1-x86_64.pkg.tar.gz.part"), []byte("x"), 0o644)
	if state() != s0 {
		t.Error("a dotfile changed the state")
	}
	os.Rename(filepath.Join(src, ".x-1-1-x86_64.pkg.tar.gz.part"), filepath.Join(src, "x-1-1-x86_64.pkg.tar.gz"))
	s1 := state()
	if s1 == s0 {
		t.Error("a new package didn't change the state")
	}
	os.WriteFile(p, []byte("replaced"), 0o644)
	if state() == s1 {
		t.Error("a replaced package didn't change the state")
	}
}
