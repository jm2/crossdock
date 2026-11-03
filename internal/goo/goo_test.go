// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package goo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// writeGoo builds a .goo the way goopack does: payload files first, then
// <name>.pkgspec, in a gzip-compressed tarball.
func writeGoo(t *testing.T, dir, file, spec string) string {
	t.Helper()
	var b bytes.Buffer
	gw := gzip.NewWriter(&b)
	tw := tar.NewWriter(gw)
	add := func(name, data string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
		io.WriteString(tw, data)
	}
	add("bin/tool.exe", "MZ")
	add("install.ps1", "Copy-Item ...")
	var s struct{ Name string }
	json.Unmarshal([]byte(spec), &s)
	add(s.Name+".pkgspec", spec)
	tw.Close()
	gw.Close()
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const toolSpec = `{
  "Name": "tool",
  "Version": "1.2.3@4",
  "Arch": "x86_64",
  "ReleaseNotes": ["1.2.3 - first"],
  "Description": "A tool",
  "Owners": "jm2",
  "PkgDependencies": {"googet": "2.18.0"},
  "Install": {"Path": "install.ps1", "ExitCodes": [0, 3010]},
  "Files": {"bin/tool.exe": "<ProgramFiles>/tool/tool.exe"},
  "FutureField": {"kept": true}
}`

func TestReadPackage(t *testing.T) {
	p, err := ReadPackage(writeGoo(t, t.TempDir(), "x.goo", toolSpec))
	if err != nil {
		t.Fatal(err)
	}
	if p.FileName() != "tool.x86_64.1.2.3@4.goo" {
		t.Errorf("FileName = %s", p.FileName())
	}
	if !bytes.Contains(p.Spec, []byte(`"FutureField":{"kept":true}`)) {
		t.Errorf("unknown fields dropped: %s", p.Spec)
	}
}

func TestVersions(t *testing.T) {
	for _, ok := range []string{"1", "1.2", "1.2.3", "01.002.3", "1.2.3@10", "1.0.0-rc.1", "2.0.0+build.5", "1.2.3-beta@2"} {
		if err := checkVersion(ok); err != nil {
			t.Errorf("checkVersion(%q): %v", ok, err)
		}
	}
	// googet itself accepts "1..2" (fixVer makes it 1.0.2), so crossdock does too.
	for _, bad := range []string{"", "1.2.3.4", "abc", "1.2.3@x", "1.2.3-"} {
		if err := checkVersion(bad); err == nil {
			t.Errorf("checkVersion(%q) accepted", bad)
		}
	}
}

func TestInvalidSpecs(t *testing.T) {
	dir := t.TempDir()
	for name, spec := range map[string]string{
		"noname": `{"Version":"1.0.0","Arch":"x86_64"}`,
		"arch":   `{"Name":"a","Version":"1.0.0","Arch":"amd64"}`,
		"ver":    `{"Name":"a","Version":"one","Arch":"noarch"}`,
		"dep":    `{"Name":"a","Version":"1.0.0","Arch":"noarch","PkgDependencies":{"b":"x"}}`,
	} {
		if _, err := ReadPackage(writeGoo(t, dir, name+".goo", spec)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// googetPkgSpec mirrors goolib.PkgSpec (google/googet), so the index is
// decoded here exactly as the googet client decodes it.
type googetPkgSpec struct {
	Name                string
	Version             string
	Arch                string
	LockArch            bool              `json:",omitempty"`
	ReleaseNotes        []string          `json:",omitempty"`
	Description         string            `json:",omitempty"`
	License             string            `json:",omitempty"`
	Authors             string            `json:",omitempty"`
	Owners              string            `json:",omitempty"`
	Source              string            `json:",omitempty"`
	Tags                map[string][]byte `json:",omitempty"`
	PkgDependencies     map[string]string `json:",omitempty"`
	Replaces            []string
	Conflicts           []string
	Provides            []string
	Install             googetExec
	Uninstall           googetExec
	Verify              googetExec
	Files               map[string]string `json:",omitempty"`
	ExternalProgramName string
	ExecutableNames     []string `json:",omitempty"`
	InstallPath         string   `json:",omitempty"`
}

type googetExec struct {
	Path      string   `json:",omitempty"`
	Args      []string `json:",omitempty"`
	ExitCodes []int    `json:",omitempty"`
}

type googetRepoSpec struct {
	Checksum, Source string
	PackageSpec      *googetPkgSpec
}

func TestPublishGoogetClientView(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	writeGoo(t, src, "tool.goo", toolSpec)
	writeGoo(t, src, "tool-arm.goo", strings.Replace(toolSpec, `"x86_64"`, `"arm64"`, 1))
	writeGoo(t, src, "dup.goo", toolSpec) // same package, same bytes: deduplicated
	writeGoo(t, src, "lib.goo", `{"Name":"lib","Version":"0.1.0","Arch":"noarch"}`)

	now := time.Unix(1762160000, 0)
	rel, err := publish.Begin(out, now)
	if err != nil {
		t.Fatal(err)
	}
	env := &publish.Env{Release: rel, Now: now, Logf: t.Logf}
	if err := Publish(config.Repo{Name: "jm2", Format: config.FormatGoo, Source: src}, env); err != nil {
		t.Fatal(err)
	}
	if err := rel.Commit(); err != nil {
		t.Fatal(err)
	}
	cur := publish.CurrentDir(out)

	// googet fetches <repo>/index.gz and decodes a JSON array of RepoSpec.
	f, err := os.Open(filepath.Join(cur, "goo", "jm2", "index.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var m []googetRepoSpec
	dec := json.NewDecoder(gr)
	for dec.More() {
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
	}
	if len(m) != 3 {
		t.Fatalf("index has %d packages, want 3", len(m))
	}
	repoURL := "http://nas.lan:8080/goo/jm2"
	for _, rs := range m {
		// download.FromRepo: url.JoinPath(repo, "..", rs.Source)
		u, err := url.JoinPath(repoURL, "..", rs.Source)
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		local := filepath.Join(cur, filepath.FromSlash(strings.TrimPrefix(pu.Path, "/")))
		b, err := os.ReadFile(local)
		if err != nil {
			t.Fatalf("%s resolves to %s: %v", rs.Source, u, err)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != rs.Checksum {
			t.Errorf("%s: checksum mismatch", u)
		}
		if rs.PackageSpec == nil || rs.PackageSpec.Name == "" {
			t.Errorf("%s: empty spec", u)
		}
	}
	tool := m[1]
	if tool.PackageSpec.Name != "tool" || tool.PackageSpec.Arch != "arm64" ||
		tool.PackageSpec.Install.ExitCodes[1] != 3010 || tool.PackageSpec.Files["bin/tool.exe"] == "" {
		t.Errorf("spec not preserved: %+v", tool.PackageSpec)
	}
	plain, _ := os.ReadFile(filepath.Join(cur, "goo", "jm2", "index"))
	if !json.Valid(plain) {
		t.Error("plain index is not JSON")
	}
}

func TestPublishRejectsConflicts(t *testing.T) {
	src := t.TempDir()
	writeGoo(t, src, "a.goo", toolSpec)
	writeGoo(t, src, "b.goo", strings.Replace(toolSpec, "A tool", "Another tool", 1))
	rel, _ := publish.Begin(t.TempDir(), time.Now())
	err := Publish(config.Repo{Name: "x", Source: src}, &publish.Env{Release: rel, Now: time.Now(), Logf: t.Logf})
	if err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("err = %v", err)
	}
}
