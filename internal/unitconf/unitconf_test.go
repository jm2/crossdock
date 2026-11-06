// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package unitconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jm2/crossdock/internal/config"
)

func cfg(output string, sources ...string) *config.Config {
	c := &config.Config{Output: output}
	for _, s := range sources {
		c.Repos = append(c.Repos, config.Repo{Source: s})
	}
	return c
}

func find(t *testing.T, files []File, name string) string {
	t.Helper()
	for _, f := range files {
		if strings.Contains(f.Path, name) {
			return f.Content
		}
	}
	t.Fatalf("no file matching %q", name)
	return ""
}

func TestSharedParent(t *testing.T) {
	files, err := Generate(cfg("/media/r6/crossdock/repo", "/media/r6/crossdock/incoming/arch", "/media/r6/crossdock/incoming/rpm"))
	if err != nil {
		t.Fatal(err)
	}
	pub := find(t, files, "publish.service.d")
	// One mount for output and sources, or hardlinks fail with EXDEV.
	if strings.Count(pub, "ReadWritePaths=/") != 1 || !strings.Contains(pub, "ReadWritePaths=/media/r6/crossdock\n") {
		t.Errorf("ReadWritePaths:\n%s", pub)
	}
	if !strings.Contains(pub, "ReadWritePaths=\n") {
		t.Error("the packaged ReadWritePaths must be reset")
	}
	if !strings.Contains(pub, "RequiresMountsFor=/media/r6/crossdock/repo /media/r6/crossdock/incoming/arch") {
		t.Errorf("RequiresMountsFor:\n%s", pub)
	}
	path := find(t, files, "publish.path.d")
	if !strings.Contains(path, "PathChanged=\nPathChanged=/media/r6/crossdock/incoming/arch\nPathChanged=/media/r6/crossdock/incoming/rpm\n") {
		t.Errorf("path unit:\n%s", path)
	}
	tmp := find(t, files, "tmpfiles.d")
	if !strings.Contains(tmp, "d /media/r6/crossdock/incoming/rpm 2775 crossdock crossdock -") {
		t.Errorf("tmpfiles:\n%s", tmp)
	}
}

func TestUnrelatedSourceStaysReadOnly(t *testing.T) {
	files, err := Generate(cfg("/srv/crossdock/repo", "/data/incoming"))
	if err != nil {
		t.Fatal(err)
	}
	if pub := find(t, files, "publish.service.d"); strings.Count(pub, "ReadWritePaths=/") != 1 ||
		!strings.Contains(pub, "ReadWritePaths=/srv/crossdock/repo\n") {
		t.Errorf("ReadWritePaths:\n%s", pub)
	}
}

func TestShallowCommonParentNotOpened(t *testing.T) {
	files, err := Generate(cfg("/media/r6/repo", "/media/r0/incoming"))
	if err != nil {
		t.Fatal(err)
	}
	if pub := find(t, files, "publish.service.d"); !strings.Contains(pub, "ReadWritePaths=/media/r6/repo\n") {
		t.Errorf("ReadWritePaths:\n%s", pub)
	}
}

func TestRejectsAndEscapes(t *testing.T) {
	if _, err := Generate(cfg("/srv/my repo", "/srv/in")); err == nil {
		t.Error("a path with a space must be refused")
	}
	files, err := Generate(cfg("/srv/50%/repo", "/srv/50%/in"))
	if err != nil {
		t.Fatal(err)
	}
	if pub := find(t, files, "publish.service.d"); !strings.Contains(pub, "/srv/50%%") {
		t.Errorf("%% must be escaped:\n%s", pub)
	}
}

func TestWrite(t *testing.T) {
	root := t.TempDir()
	files, _ := Generate(cfg("/srv/a/repo", "/srv/a/in"))
	if err := Write(root, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/tmpfiles.d/crossdock-paths.conf")); err != nil {
		t.Error(err)
	}
}
