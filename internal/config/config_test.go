// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExample(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "examples", "crossdock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) != 2 || c.Repos[1].Codename != "stable" || c.Repos[1].Origin != "jm2" {
		t.Errorf("defaults not applied: %+v", c.Repos[1])
	}
}

func TestLoadErrors(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `{"output":"o","bogus":1,"repos":[{"name":"a","format":"deb","source":"s"}]}`,
		"bad format":    `{"output":"o","repos":[{"name":"a","format":"msi","source":"s"}]}`,
		"duplicate":     `{"output":"o","repos":[{"name":"a","format":"deb","source":"s"},{"name":"a","format":"deb","source":"t"}]}`,
		"two keys":      `{"output":"o","signing":{"key_file":"k","gnupg_home":"h","key_id":"x"},"repos":[{"name":"a","format":"deb","source":"s"}]}`,
		"bad suite":     `{"output":"o","repos":[{"name":"a","format":"deb","source":"s","suite":"../x"}]}`,
		"bad valid_for": `{"output":"o","repos":[{"name":"a","format":"deb","source":"s","valid_for":"soon"}]}`,
		"short valid":   `{"output":"o","repos":[{"name":"a","format":"deb","source":"s","valid_for":"5m"}]}`,
		"valid on rpm":  `{"output":"o","repos":[{"name":"a","format":"rpm","source":"s","valid_for":"7d"}]}`,
	} {
		p := filepath.Join(t.TempDir(), "c.json")
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: Load succeeded", name)
		}
	}
}

func TestRelativePaths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	os.WriteFile(p, []byte(`{"output":"out","repos":[{"name":"a","format":"arch","source":"in"}]}`), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Output, dir) || !strings.HasPrefix(c.Repos[0].Source, dir) {
		t.Errorf("paths not resolved against config dir: %s %s", c.Output, c.Repos[0].Source)
	}
}

func TestEnvExpansionAndServeDefaults(t *testing.T) {
	creds := filepath.ToSlash(t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"output":"out",
		"signing":{"key_file":"${CREDENTIALS_DIRECTORY}/signing.asc","passphrase_file":"$CREDENTIALS_DIRECTORY/passphrase"},
		"repos":[{"name":"a","format":"deb","source":"in"}]}`), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(c.Signing.KeyFile) != filepath.Clean(creds+"/signing.asc") ||
		filepath.Clean(c.Signing.PassphraseFile) != filepath.Clean(creds+"/passphrase") {
		t.Errorf("not expanded: %+v", c.Signing)
	}
	if c.Serve.Listen != ":8080" {
		t.Errorf("serve.listen default = %q", c.Serve.Listen)
	}
}

func TestValidDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour} {
		got, err := Repo{ValidFor: in}.ValidDuration()
		if err != nil || got != want {
			t.Errorf("ValidDuration(%q) = %v, %v", in, got, err)
		}
	}
}
