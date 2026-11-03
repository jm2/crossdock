// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package sign

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jm2/crossdock/internal/config"
)

func TestGenerateAndSign(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil || runtime.GOOS == "windows" {
		// Git for Windows ships an MSYS gpg that mangles native paths.
		t.Skip("needs a native gpg")
	}
	for _, pass := range []string{"", "correct horse"} {
		k, err := Generate("crossdock test <t@example.invalid>", pass)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		key := filepath.Join(dir, "signing.asc")
		os.WriteFile(key, k.Secret, 0o600)
		cfg := &config.Signing{KeyFile: key}
		if pass != "" {
			pf := filepath.Join(dir, "pass")
			os.WriteFile(pf, []byte(pass+"\n"), 0o600)
			cfg.PassphraseFile = pf
		}
		s, err := New(cfg)
		if err != nil {
			t.Fatalf("pass=%q: %v", pass, err)
		}
		if s.Fingerprint() != k.Fingerprint {
			t.Errorf("fingerprint %s, want %s", s.Fingerprint(), k.Fingerprint)
		}
		data := []byte("Origin: x\n")
		sig, err := s.DetachSign(data, true)
		if err != nil {
			t.Fatalf("pass=%q: sign: %v", pass, err)
		}
		clear, err := s.ClearSign(data)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()

		// Verify with a keyring holding only the public key.
		home, _ := os.MkdirTemp("", "cdv")
		defer os.RemoveAll(home)
		pub := filepath.Join(dir, "pub.asc")
		os.WriteFile(pub, k.Public, 0o644)
		os.WriteFile(filepath.Join(dir, "data"), data, 0o644)
		os.WriteFile(filepath.Join(dir, "data.asc"), sig, 0o644)
		os.WriteFile(filepath.Join(dir, "InRelease"), clear, 0o644)
		gpg := func(args ...string) string {
			out, err := exec.Command("gpg", append([]string{"--homedir", home, "--batch"}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("gpg %v: %v\n%s", args, err, out)
			}
			return string(out)
		}
		gpg("--import", pub)
		if out := gpg("--verify", filepath.Join(dir, "data.asc"), filepath.Join(dir, "data")); !strings.Contains(out, "Good signature") {
			t.Errorf("detached: %s", out)
		}
		if out := gpg("--verify", filepath.Join(dir, "InRelease")); !strings.Contains(out, "Good signature") {
			t.Errorf("clearsigned: %s", out)
		}
		exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()

		if pass != "" {
			// Without the passphrase, signing must fail rather than prompt.
			s2, err := New(&config.Signing{KeyFile: key})
			if err == nil {
				if _, err := s2.DetachSign(data, true); err == nil {
					t.Error("protected key signed without its passphrase")
				}
				s2.Close()
			}
		}
	}
}
