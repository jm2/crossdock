// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReleaseLifecycle(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "pkg.bin")
	os.WriteFile(src, []byte("payload"), 0o644)

	var ids []string
	for i := 0; i < 4; i++ {
		rel, err := Begin(root, time.Unix(int64(1000+i), 0))
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if _, err := rel.AddFile(src, "pool/pkg.bin"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := rel.WriteFile("index", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
		if err := rel.Commit(); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rel.ID)
		if Current(root) != rel.ID {
			t.Fatalf("current = %q, want %q", Current(root), rel.ID)
		}
	}
	b, err := os.ReadFile(filepath.Join(CurrentDir(root), "index"))
	if err != nil || b[0] != 3 {
		t.Fatalf("current/index = %v, %v", b, err)
	}

	removed, err := Prune(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Errorf("removed %v, want 3 releases", removed)
	}
	// The only release left doesn't contain the payload, so the store
	// object must be collected.
	entries, _ := os.ReadDir(filepath.Join(root, "store"))
	if len(entries) != 0 {
		t.Errorf("store not collected: %d objects left", len(entries))
	}
}

func TestPathEscape(t *testing.T) {
	rel, err := Begin(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../x", "/etc/passwd", "a/../../x"} {
		if _, err := rel.Path(p); err == nil {
			t.Errorf("Path(%q) allowed", p)
		}
	}
}

func TestAbort(t *testing.T) {
	root := t.TempDir()
	rel, _ := Begin(root, time.Now())
	rel.WriteFile("x", []byte("y"))
	if err := rel.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rel.Dir); !os.IsNotExist(err) {
		t.Error("aborted release still exists")
	}
	if Current(root) != "" {
		t.Error("abort changed current")
	}
}

func TestCache(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "pkg.bin")
	os.WriteFile(src, []byte("version-one"), 0o644)
	parses := 0
	parse := func(p string) (string, error) {
		parses++
		b, err := os.ReadFile(p)
		return string(b), err
	}

	rel, _ := Begin(root, time.Unix(1000, 0))
	d1, err := rel.AddFile(src, "a/pkg.bin")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Cached(rel, src, "test/1", parse); v != "version-one" {
		t.Fatalf("parse = %q", v)
	}
	rel.Commit()

	// Same size and mtime: the next publish trusts the cache, without
	// rehashing or reparsing.
	fi, _ := os.Stat(src)
	os.WriteFile(src, []byte("version-two"), 0o644)
	os.Chtimes(src, fi.ModTime(), fi.ModTime())
	rel2, _ := Begin(root, time.Unix(2000, 0))
	if d, _ := rel2.Digest(src); d.SHA256 != d1.SHA256 {
		t.Error("unchanged size+mtime was rehashed")
	}
	if v, _ := Cached(rel2, src, "test/1", parse); v != "version-one" || parses != 1 {
		t.Errorf("cached parse = %q after %d parses", v, parses)
	}
	if v, _ := Cached(rel2, src, "test/2", parse); v != "version-two" || parses != 2 {
		t.Errorf("a new parser version must reparse: %q, %d", v, parses)
	}
	rel2.Commit()

	// A new mtime invalidates the entry.
	os.Chtimes(src, time.Now(), time.Now().Add(time.Hour))
	rel3, _ := Begin(root, time.Unix(3000, 0))
	if d, _ := rel3.Digest(src); d.SHA256 == d1.SHA256 {
		t.Error("changed file served from cache")
	}
	if v, _ := Cached(rel3, src, "test/1", parse); v != "version-two" {
		t.Errorf("parse after change = %q", v)
	}
}

func TestManifestGC(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("aaa"), 0o644)
	os.WriteFile(b, []byte("bbb"), 0o644)
	r1, _ := Begin(root, time.Unix(1000, 0))
	da, _ := r1.AddFile(a, "a")
	r1.Commit()
	r2, _ := Begin(root, time.Unix(2000, 0))
	db, _ := r2.AddFile(b, "b")
	r2.Commit()
	if _, err := Prune(root, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "store", da.SHA256)); !os.IsNotExist(err) {
		t.Error("unreferenced object kept")
	}
	if _, err := os.Stat(filepath.Join(root, "store", db.SHA256)); err != nil {
		t.Error("referenced object removed")
	}
	// The release's own copy survives GC regardless.
	if got, _ := os.ReadFile(filepath.Join(CurrentDir(root), "b")); string(got) != "bbb" {
		t.Errorf("current/b = %q", got)
	}
}

// TestNoSymlinks runs a publish the way it happens on Windows without
// symlink privileges.
func TestNoSymlinks(t *testing.T) {
	old := symlink
	symlink = func(string, string) error { return errors.New("symlinks unavailable") }
	defer func() { symlink = old }()

	root := t.TempDir()
	r, _ := Begin(root, time.Unix(1000, 0))
	r.WriteFile("arch/x/x.db.tar.gz", []byte("db"))
	if err := r.Symlink("x.db.tar.gz", "arch/x/x.db"); err != nil {
		t.Fatal(err)
	}
	r.WriteFile("deb/d/dists/codename/Release", []byte("rel"))
	if err := r.Symlink("codename", "deb/d/dists/stable"); err != nil {
		t.Fatal(err)
	}
	if err := r.Symlink("../../../../etc", "deb/d/escape"); err == nil {
		t.Error("escaping link target accepted")
	}
	if err := r.Commit(); err != nil {
		t.Fatal(err)
	}
	if Current(root) != r.ID {
		t.Fatalf("Current = %q, want %q", Current(root), r.ID)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Error("current symlink exists")
	}
	dir := filepath.Join(root, "releases", r.ID)
	for p, want := range map[string]string{"arch/x/x.db": "db", "deb/d/dists/stable/Release": "rel"} {
		fi, err := os.Lstat(filepath.Join(dir, p))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s: %v %v", p, fi, err)
			continue
		}
		if got, _ := os.ReadFile(filepath.Join(dir, p)); string(got) != want {
			t.Errorf("%s = %q", p, got)
		}
	}
	// A second release swaps current.id and pruning still finds current.
	r2, _ := Begin(root, time.Unix(2000, 0))
	r2.Commit()
	if Current(root) != r2.ID {
		t.Error("current.id not replaced")
	}
	if removed, _ := Prune(root, 1); len(removed) != 1 || removed[0] != r.ID {
		t.Errorf("pruned %v", removed)
	}
}

// Moving from the current symlink to current.id, as Windows does for a
// tree published with symlinks: the symlink must go, and if it can't, the
// publish fails and the release that was live stays live.
func TestMigrateToCurrentID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a symlink-based first release")
	}
	root := t.TempDir()
	r1, _ := Begin(root, time.Unix(1000, 0))
	if err := r1.Commit(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "current")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("first release didn't publish a symlink: %v", err)
	}

	oldRemove := removeLink
	removeLink = func(string) error { return errors.New("in use") }
	r2, _ := Begin(root, time.Unix(2000, 0))
	r2.NoSymlinks()
	err := r2.Commit()
	removeLink = oldRemove
	if err == nil {
		t.Fatal("Commit succeeded although the old symlink stayed")
	}
	r2.Abort()
	if Current(root) != r1.ID {
		t.Errorf("Current = %q, want the still-live %q", Current(root), r1.ID)
	}
	if _, err := os.Stat(filepath.Join(root, "current.id")); !os.IsNotExist(err) {
		t.Errorf("current.id left behind naming the aborted release: %v", err)
	}

	// Not being able to inspect the old link must fail the same way.
	oldLstat := lstat
	lstat = func(string) (os.FileInfo, error) { return nil, errors.New("cannot inspect") }
	rx, _ := Begin(root, time.Unix(2500, 0))
	rx.NoSymlinks()
	err = rx.Commit()
	lstat = oldLstat
	if err == nil {
		t.Fatal("Commit succeeded although the old link couldn't be inspected")
	}
	rx.Abort()
	if _, err := os.Stat(filepath.Join(root, "current.id")); Current(root) != r1.ID || !os.IsNotExist(err) {
		t.Errorf("after failed inspect: Current = %q, current.id: %v", Current(root), err)
	}

	r3, _ := Begin(root, time.Unix(3000, 0))
	r3.NoSymlinks()
	if err := r3.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Error("old current symlink kept")
	}
	if Current(root) != r3.ID {
		t.Errorf("Current = %q, want %q", Current(root), r3.ID)
	}
}
