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
	testHookBeforeCommit = func() error {
		if commits++; commits == 1 {
			writeArchPkg(t, src, "late")
		}
		return nil
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

// Sources that never hold still stop after maxPublishRounds.
func TestPublishStopsAfterMaxRounds(t *testing.T) {
	// A fixed time puts every round in the same second.
	t.Setenv("SOURCE_DATE_EPOCH", "1760000000")
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	writeArchPkg(t, src, "first")
	cfg := writeConfig(t, work)

	commits := 0
	testHookBeforeCommit = func() error {
		commits++
		writeArchPkg(t, src, "pkg"+strings.Repeat("x", commits))
		return nil
	}
	defer func() { testHookBeforeCommit = nil }()

	if err := cmdPublish([]string{"-q", "-allow-unsigned", "-c", cfg}); err != nil {
		t.Fatal(err)
	}
	if commits != maxPublishRounds {
		t.Errorf("published %d times, want %d", commits, maxPublishRounds)
	}
	// Every round got a later release ID, so pruning keeps the newest.
	ids, _ := os.ReadDir(filepath.Join(work, "out", "releases"))
	if len(ids) == 0 || ids[len(ids)-1].Name() != filepath.Base(publish.CurrentDir(filepath.Join(work, "out"))) {
		t.Errorf("the current release doesn't sort last: %v", ids)
	}
}

// A package replaced while a publish reads the sources fails that round's
// read; publish starts over instead of failing.
func TestPublishRetriesWhenASourceVanishes(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	old := writeArchPkg(t, src, "first")
	cfg := writeConfig(t, work)

	rounds := 0
	testHookBeforeCommit = func() error {
		if rounds++; rounds == 1 {
			os.Remove(old)
			writeArchPkg(t, src, "second")
			return &os.PathError{Op: "open", Path: old, Err: os.ErrNotExist}
		}
		return nil
	}
	defer func() { testHookBeforeCommit = nil }()

	if err := cmdPublish([]string{"-q", "-allow-unsigned", "-c", cfg}); err != nil {
		t.Fatal(err)
	}
	if rounds != 2 {
		t.Errorf("ran %d rounds, want 2", rounds)
	}
	if db := servedDB(t, work); !strings.Contains(db, "second-1-1") || strings.Contains(db, "first-1-1") {
		t.Errorf("served db: %s", db)
	}
}

// A failure with unchanged sources is still an error.
func TestPublishFailsWhenSourcesHold(t *testing.T) {
	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.Mkdir(src, 0o755)
	writeArchPkg(t, src, "first")
	cfg := writeConfig(t, work)

	rounds := 0
	testHookBeforeCommit = func() error {
		rounds++
		return os.ErrInvalid
	}
	defer func() { testHookBeforeCommit = nil }()

	if err := cmdPublish([]string{"-q", "-allow-unsigned", "-c", cfg}); err == nil {
		t.Fatal("publish succeeded")
	}
	if rounds != 1 {
		t.Errorf("ran %d rounds, want 1", rounds)
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
	s2 := state()
	os.WriteFile(p+".sig", []byte("sig"), 0o644)
	if state() == s2 {
		t.Error("a new signature didn't change the state")
	}
}
