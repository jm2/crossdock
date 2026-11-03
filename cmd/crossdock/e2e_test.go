// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jm2/crossdock/internal/serve"
)

func need(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// genKey creates a throwaway passphrase-protected signing key and returns
// the path of its armored secret export.
func genKey(t *testing.T, passphrase string) string {
	home, err := os.MkdirTemp("", "cdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()
		os.RemoveAll(home)
	})
	g := []string{"--homedir", home, "--batch", "--pinentry-mode", "loopback", "--passphrase", passphrase}
	run(t, home, "gpg", append(g, "--quick-gen-key", "crossdock test <test@example.invalid>", "ed25519", "sign", "never")...)
	key := filepath.Join(t.TempDir(), "signing.asc")
	out := run(t, home, "gpg", append(g, "--armor", "--export-secret-keys")...)
	if err := os.WriteFile(key, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

func buildDeb(t *testing.T, outDir, name, version, arch, compression string) {
	root := filepath.Join(t.TempDir(), name)
	os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755)
	os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755)
	control := "Package: " + name + "\nVersion: " + version + "\nArchitecture: " + arch +
		"\nMaintainer: Test <test@example.invalid>\nDescription: crossdock e2e package\n multi-line description.\n"
	os.WriteFile(filepath.Join(root, "DEBIAN", "control"), []byte(control), 0o644)
	os.WriteFile(filepath.Join(root, "usr", "bin", name), []byte("#!/bin/sh\necho hi\n"), 0o755)
	run(t, "", "dpkg-deb", "--root-owner-group", "-Z"+compression, "--build", root, filepath.Join(outDir, name+".deb"))
}

// TestAptEndToEnd publishes a signed repo and has a real apt-get trust it,
// update from it and download from it.
func TestAptEndToEnd(t *testing.T) {
	need(t, "gpg", "dpkg-deb", "apt-get", "xz")
	const pass = "correct horse"
	t.Setenv("CROSSDOCK_TEST_PASS", pass)
	key := genKey(t, pass)

	work := t.TempDir()
	src := filepath.Join(work, "incoming")
	os.MkdirAll(src, 0o755)
	buildDeb(t, src, "hello-crossdock", "1:1.0-1", "amd64", "xz")
	buildDeb(t, src, "crossdock-docs", "1.0", "all", "gzip")

	cfg := map[string]any{
		"output": "out",
		"signing": map[string]any{
			"key_file": key, "passphrase_env": "CROSSDOCK_TEST_PASS",
		},
		"repos": []map[string]any{{
			"name": "jm2", "format": "deb", "source": "incoming",
			"suite": "stable", "codename": "crossdock", "architectures": []string{"amd64", "arm64"},
		}},
	}
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(work, "crossdock.json")
	os.WriteFile(cfgPath, b, 0o644)

	if err := cmdPublish([]string{"-c", cfgPath}); err != nil {
		t.Fatal(err)
	}
	// Publishing twice must work and keep current valid.
	if err := cmdPublish([]string{"-q", "-c", cfgPath}); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(work, "out", "current")
	for _, f := range []string{"crossdock.asc", "deb/jm2/dists/crossdock/InRelease",
		"deb/jm2/dists/stable/Release.gpg", "deb/jm2/dists/crossdock/main/binary-arm64/Packages.gz"} {
		if _, err := os.Stat(filepath.Join(cur, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}

	fileOpts := aptRoot(t, filepath.Join(work, "apt-file"), "file://"+filepath.Join(cur, "deb", "jm2"), filepath.Join(cur, "crossdock.asc"))
	aptInstall(t, work, fileOpts)

	// The same repo through `crossdock serve`, over real HTTP.
	srv := httptest.NewServer(&serve.Handler{Output: filepath.Join(work, "out")})
	defer srv.Close()
	httpOpts := aptRoot(t, filepath.Join(work, "apt-http"), srv.URL+"/deb/jm2", filepath.Join(cur, "crossdock.asc"))
	aptInstall(t, work, httpOpts)
	opts, apt := fileOpts, filepath.Join(work, "apt-file")

	// Tampering with an index must be caught by apt's hash checks.
	pk := filepath.Join(cur, "deb/jm2/dists/crossdock/main/binary-amd64/Packages")
	os.Remove(pk + ".gz")
	os.Remove(pk)
	os.WriteFile(pk, []byte("Package: evil\n"), 0o644)
	os.RemoveAll(filepath.Join(apt, "var/lib/apt/lists"))
	os.MkdirAll(filepath.Join(apt, "var/lib/apt/lists/partial"), 0o755)
	cmd := exec.Command("apt-get", append(opts, "update")...)
	if out, err := cmd.CombinedOutput(); err == nil && !strings.Contains(string(out), "Hash Sum mismatch") &&
		!strings.Contains(string(out), "File has unexpected size") {
		t.Errorf("apt-get accepted a tampered index:\n%s", out)
	}
}

// aptRoot builds a private apt root that trusts nothing but key, with one
// source, and returns the apt options that select it.
func aptRoot(t *testing.T, apt, uri, key string) []string {
	t.Helper()
	for _, d := range []string{"etc/apt/sources.list.d", "etc/apt/trusted.gpg.d", "etc/apt/preferences.d",
		"var/lib/apt/lists/partial", "var/cache/apt/archives/partial", "var/lib/dpkg"} {
		os.MkdirAll(filepath.Join(apt, d), 0o755)
	}
	os.WriteFile(filepath.Join(apt, "var/lib/dpkg/status"), nil, 0o644)
	src := "Types: deb\nURIs: " + uri + "\nSuites: stable\nComponents: main\nSigned-By: " + key + "\n"
	os.WriteFile(filepath.Join(apt, "etc/apt/sources.list.d/crossdock.sources"), []byte(src), 0o644)
	os.WriteFile(filepath.Join(apt, "etc/apt/sources.list"), nil, 0o644)
	return []string{
		"-o", "Dir=" + apt,
		"-o", "Dir::State::status=" + filepath.Join(apt, "var/lib/dpkg/status"),
		"-o", "Debug::NoLocking=1",
		"-o", "APT::Architecture=amd64",
		"-o", "APT::Sandbox::User=root",
		"-o", "Acquire::Languages=none",
		"-o", "Acquire::http::Proxy=DIRECT",
	}
}

// aptInstall runs update, download and policy against an apt root.
func aptInstall(t *testing.T, work string, opts []string) {
	t.Helper()
	out := run(t, work, "apt-get", append(opts, "update")...)
	if strings.Contains(out, "NO_PUBKEY") || strings.Contains(out, "not signed") || strings.Contains(out, "Ign:") {
		t.Fatalf("apt-get update did not verify the repo:\n%s", out)
	}
	dl := t.TempDir()
	run(t, dl, "apt-get", append(opts, "download", "hello-crossdock", "crossdock-docs")...)
	got, _ := filepath.Glob(filepath.Join(dl, "*.deb"))
	if len(got) != 2 {
		t.Fatalf("downloaded %v, want 2 packages", got)
	}
	policy := run(t, work, "apt-cache", append(opts, "policy", "hello-crossdock")...)
	if !strings.Contains(policy, "1:1.0-1") {
		t.Errorf("apt-cache policy missing version:\n%s", policy)
	}
}
