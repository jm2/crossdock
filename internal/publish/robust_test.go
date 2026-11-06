// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(fa, fb)
}

func TestStoreHardlinksSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows always copies")
	}
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "pkg.bin")
	os.WriteFile(src, []byte("payload"), 0o644)
	rel, err := Begin(root, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	d, err := rel.AddFile(src, "a/pkg.bin")
	if err != nil {
		t.Fatal(err)
	}
	if d.SHA256 != HashBytes([]byte("payload")).SHA256 {
		t.Error("wrong digest")
	}
	if !sameFile(t, src, filepath.Join(root, storeDir, d.SHA256)) {
		t.Error("store object isn't a hardlink of the source")
	}
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(root, storeDir, ".*"))
	if len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// Same content from two sources: one store object, and both add cleanly.
func TestStoreLinksDuplicateContent(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a.bin"), filepath.Join(base, "b.bin")
	os.WriteFile(a, []byte("same"), 0o644)
	os.WriteFile(b, []byte("same"), 0o644)
	rel, _ := Begin(filepath.Join(base, "repo"), time.Unix(1000, 0))
	if _, err := rel.AddFile(a, "x/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := rel.AddFile(b, "x/b"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCopiesUnreadableSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits don't describe access on Windows")
	}
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "pkg.bin")
	os.WriteFile(src, []byte("private"), 0o600)
	rel, _ := Begin(root, time.Unix(1000, 0))
	var logged []string
	rel.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	d, err := rel.AddFile(src, "a/pkg.bin")
	if err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(root, storeDir, d.SHA256)
	if sameFile(t, src, obj) {
		t.Fatal("linked a source other accounts can't read")
	}
	if fi, _ := os.Stat(obj); fi.Mode().Perm() != 0o644 {
		t.Errorf("store object mode %o", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(src); fi.Mode().Perm() != 0o600 {
		t.Errorf("source mode changed to %o", fi.Mode().Perm())
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "copying") {
		t.Errorf("warning: %q", logged)
	}
}

func TestFindFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.pkg"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, ".b.pkg"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), nil, 0o644)
	got, err := FindFiles(dir, ".pkg")
	if err != nil || len(got) != 1 {
		t.Fatalf("FindFiles = %v, %v", got, err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.pkg"), filepath.Join(dir, "d.pkg")); err != nil {
		t.Skip(err)
	}
	if _, err := FindFiles(dir, ".pkg"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("a symlinked package must be an error: %v", err)
	}
}

// A truncated store object, as a power cut can leave, must be replaced.
func TestStoreReplacesDamagedObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{{"link", 0o644}, {"copy", 0o600}} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root, src := filepath.Join(base, "repo"), filepath.Join(base, "pkg.bin")
			os.WriteFile(src, []byte("payload"), tc.mode)
			rel, _ := Begin(root, time.Unix(1000, 0))
			want := HashBytes([]byte("payload"))
			obj := filepath.Join(root, storeDir, want.SHA256)
			os.WriteFile(obj, nil, 0o644)
			if _, err := rel.AddFile(src, "a/pkg.bin"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(rel.Dir, "a", "pkg.bin"))
			if err != nil || string(got) != "payload" {
				t.Errorf("published %q, %v", got, err)
			}
		})
	}
}

// A digest stays with the file it was computed from: replacing src while
// it's being hashed must not give the new file the old digest.
func TestCacheKeepsDigestWithItsFile(t *testing.T) {
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "pkg.bin")
	os.WriteFile(src, []byte("old-build"), 0o644)
	rel, _ := Begin(root, time.Unix(1000, 0))
	old, _ := os.Stat(src)
	d := HashBytes([]byte("old-build"))

	repl := src + ".new"
	os.WriteFile(repl, []byte("new-build!"), 0o644)
	os.Chtimes(repl, time.Now(), time.Now().Add(time.Hour))
	os.Rename(repl, src)

	rel.cache.putFor(src, old, d) // the hash of the old file finishes now
	if got, ok := rel.cache.digest(src); ok && got.SHA256 == d.SHA256 {
		t.Error("the old digest was recorded against the new file")
	}
}

// A publish killed part way keeps what it finished: the next one neither
// rehashes nor reparses those files, and entries it never reached survive.
func TestResumeAfterKill(t *testing.T) {
	old := checkpointEvery
	checkpointEvery = 0
	defer func() { checkpointEvery = old }()

	base := t.TempDir()
	root := filepath.Join(base, "repo")
	done, later := filepath.Join(base, "done.bin"), filepath.Join(base, "later.bin")
	os.WriteFile(later, []byte("later"), 0o644)
	parses := 0
	parse := func(p string) (string, error) { parses++; return p, nil }

	first, _ := Begin(root, time.Unix(1000, 0))
	first.AddFile(later, "a/later")
	Cached(first, later, "test/1", parse)
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(done, []byte("done"), 0o644)
	killed, _ := Begin(root, time.Unix(2000, 0))
	d, err := killed.AddFile(done, "a/done")
	if err != nil {
		t.Fatal(err)
	}
	Cached(killed, done, "test/1", parse)
	// No Commit or Abort: the process died here.

	// Prove the store object is reused: the source changes content but
	// keeps its size and mtime, so only a cache hit gives the old digest.
	fi, _ := os.Stat(done)
	os.Remove(done)
	os.WriteFile(done, []byte("DONE"), 0o644)
	os.Chtimes(done, fi.ModTime(), fi.ModTime())

	next, _ := Begin(root, time.Unix(3000, 0))
	got, err := next.AddFile(done, "a/done")
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != d.SHA256 {
		t.Error("finished file was reread after the kill")
	}
	Cached(next, done, "test/1", parse)
	Cached(next, later, "test/1", parse)
	if parses != 2 {
		t.Errorf("%d parses, want 2: the kill lost progress", parses)
	}
}

func TestAbortKeepsProgress(t *testing.T) {
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "p.bin")
	os.WriteFile(src, []byte("p"), 0o644)
	parses := 0
	parse := func(p string) (string, error) { parses++; return p, nil }
	rel, _ := Begin(root, time.Unix(1000, 0))
	Cached(rel, src, "test/1", parse)
	if err := rel.Abort(); err != nil {
		t.Fatal(err)
	}
	rel2, _ := Begin(root, time.Unix(2000, 0))
	Cached(rel2, src, "test/1", parse)
	if parses != 1 {
		t.Errorf("%d parses after a failed publish, want 1", parses)
	}
}

func TestPruneRemovesStaleIncoming(t *testing.T) {
	root := t.TempDir()
	rel, _ := Begin(root, time.Unix(1000, 0))
	stale := filepath.Join(root, storeDir, incomingPrefix+"12345")
	os.WriteFile(stale, []byte("killed"), 0o644)
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(root, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temporary file survived: %v", err)
	}
}

func TestPruneRemovesKilledReleases(t *testing.T) {
	root := t.TempDir()
	var good []string
	for i := int64(0); i < 3; i++ {
		rel, _ := Begin(root, time.Unix(1000+i, 0))
		if err := rel.Commit(); err != nil {
			t.Fatal(err)
		}
		good = append(good, rel.ID)
	}
	var killed []string
	for i := int64(0); i < 3; i++ {
		rel, _ := Begin(root, time.Unix(2000+i, 0))
		killed = append(killed, rel.ID)
	}
	rel, _ := Begin(root, time.Unix(3000, 0))
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := Prune(root, 3); err != nil {
		t.Fatal(err)
	}
	for _, id := range killed {
		if _, err := os.Stat(filepath.Join(root, releasesDir, id)); !os.IsNotExist(err) {
			t.Errorf("killed release %s survived", id)
		}
	}
	for _, id := range []string{rel.ID, good[2], good[1]} {
		if _, err := os.Stat(filepath.Join(root, releasesDir, id)); err != nil {
			t.Errorf("good release pushed out: %v", err)
		}
	}
}

func TestPreviousPackages(t *testing.T) {
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "a.pkg")
	os.WriteFile(src, []byte("p"), 0o644)
	rel, _ := Begin(root, time.Unix(1000, 0))
	if n, err := rel.PreviousPackages("arch/x", ".pkg"); n != 0 || err != nil {
		t.Errorf("first publish: %d, %v", n, err)
	}
	if _, err := rel.AddFile(src, "arch/x/x86_64/a.pkg"); err != nil {
		t.Fatal(err)
	}
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	rel2, _ := Begin(root, time.Unix(2000, 0))
	defer rel2.Abort()
	if n, err := rel2.PreviousPackages("arch/x", ".pkg"); n != 1 || err != nil {
		t.Errorf("previous = %d, %v; want 1", n, err)
	}
	if n, err := rel2.PreviousPackages("rpm/x", ".pkg"); n != 0 || err != nil {
		t.Errorf("unknown repo = %d, %v", n, err)
	}
}
