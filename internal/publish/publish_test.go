// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"os"
	"path/filepath"
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
	b, err := os.ReadFile(filepath.Join(root, "current", "index"))
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
