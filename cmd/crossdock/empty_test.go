// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

func TestCheckNotEmpty(t *testing.T) {
	base := t.TempDir()
	root, src := filepath.Join(base, "repo"), filepath.Join(base, "in")
	os.Mkdir(src, 0o755)
	repo := config.Repo{Name: "local", Format: config.FormatRPM, Source: src}
	logf := func(string, ...any) {}

	// A repo that has never had packages may be empty.
	rel, _ := publish.Begin(root, time.Unix(1000, 0))
	if err := checkNotEmpty(repo, &publish.Env{Release: rel, Logf: logf}, false); err != nil {
		t.Fatalf("first empty publish: %v", err)
	}
	rel.Commit()

	pkg := filepath.Join(src, "a-1-1.x86_64.rpm")
	os.WriteFile(pkg, nil, 0o644)
	rel, _ = publish.Begin(root, time.Unix(2000, 0))
	if err := checkNotEmpty(repo, &publish.Env{Release: rel, Logf: logf}, false); err != nil {
		t.Fatal(err)
	}
	rel.Commit()

	// It emptied, as an unmounted disk would make it look.
	os.Remove(pkg)
	rel, _ = publish.Begin(root, time.Unix(3000, 0))
	err := checkNotEmpty(repo, &publish.Env{Release: rel, Logf: logf}, false)
	if err == nil || !strings.Contains(err.Error(), "is the disk mounted") {
		t.Fatalf("emptying a repo must be refused: %v", err)
	}
	if err := checkNotEmpty(repo, &publish.Env{Release: rel, Logf: logf}, true); err != nil {
		t.Fatalf("-allow-empty: %v", err)
	}
	rel.Abort()

	// A missing directory is an error even with -allow-empty.
	os.Remove(src)
	rel, _ = publish.Begin(root, time.Unix(4000, 0))
	defer rel.Abort()
	if err := checkNotEmpty(repo, &publish.Env{Release: rel, Logf: logf}, true); err == nil {
		t.Fatal("missing source must be an error")
	}
}
