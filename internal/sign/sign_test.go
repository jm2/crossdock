// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package sign

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/jm2/crossdock/internal/config"
)

const testUID = "crossdock test <t@example.invalid>"

// writeKey generates a key and writes the secret (and passphrase file).
func writeKey(t *testing.T, pass string) (*GeneratedKey, *config.Signing) {
	t.Helper()
	k, err := Generate(testUID, pass)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &config.Signing{KeyFile: filepath.Join(dir, "signing.asc")}
	os.WriteFile(cfg.KeyFile, k.Secret, 0o600)
	if pass != "" {
		cfg.PassphraseFile = filepath.Join(dir, "pass")
		os.WriteFile(cfg.PassphraseFile, []byte(pass+"\n"), 0o600)
	}
	return k, cfg
}

func TestGeneratedKeyShape(t *testing.T) {
	k, _ := writeKey(t, "")
	el, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(k.Public))
	if err != nil || len(el) != 1 {
		t.Fatalf("public key: %v (%d entities)", err, len(el))
	}
	e := el[0]
	pk := e.PrimaryKey
	// v4 Ed25519 is what every target verifier (GnuPG for pacman and
	// librepo, sqv for apt, rpm-sequoia) accepts.
	if pk.Version != 4 || pk.PubKeyAlgo != packet.PubKeyAlgoEdDSA {
		t.Errorf("key is v%d algo %d, want v4 EdDSA", pk.Version, pk.PubKeyAlgo)
	}
	if len(e.Subkeys) != 0 {
		t.Errorf("signing-only key has %d subkeys", len(e.Subkeys))
	}
	if _, ok := e.Identities[testUID]; !ok {
		t.Errorf("identities %v, want %q", e.Identities, testUID)
	}
	if strings.Contains(string(k.Secret), "PUBLIC KEY") || !strings.Contains(string(k.Secret), "PRIVATE KEY") {
		t.Errorf("secret export is not a private key block")
	}
}

func TestNativeSign(t *testing.T) {
	for _, pass := range []string{"", "correct horse"} {
		k, cfg := writeKey(t, pass)
		s, err := New(cfg)
		if err != nil {
			t.Fatalf("pass=%q: %v", pass, err)
		}
		if _, ok := s.(*Native); !ok {
			t.Fatalf("key_file gave %T, want *Native", s)
		}
		if s.Fingerprint() != k.Fingerprint {
			t.Errorf("fingerprint %s, want %s", s.Fingerprint(), k.Fingerprint)
		}
		data := []byte("Origin: x\nLabel: y\n")
		keyring, _ := openpgp.ReadArmoredKeyRing(bytes.NewReader(k.Public))

		for _, armored := range []bool{true, false} {
			sig, err := s.DetachSign(data, armored)
			if err != nil {
				t.Fatal(err)
			}
			check := openpgp.CheckDetachedSignature
			if armored {
				check = openpgp.CheckArmoredDetachedSignature
			}
			if _, err := check(keyring, bytes.NewReader(data), bytes.NewReader(sig), nil); err != nil {
				t.Errorf("armored=%v: %v", armored, err)
			}
			if _, err := check(keyring, bytes.NewReader(append(data, 'x')), bytes.NewReader(sig), nil); err == nil {
				t.Errorf("armored=%v: tampered data verified", armored)
			}
		}

		clear, err := s.ClearSign(data)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := clearsign.Decode(clear)
		// The signed text drops the final line break, as with gpg.
		if b == nil || !bytes.Equal(b.Plaintext, bytes.TrimSuffix(data, []byte("\n"))) ||
			!strings.Contains(string(clear), "Hash: SHA512") {
			t.Fatalf("clearsigned: %s", clear)
		}
		if _, err := b.VerifySignature(keyring, nil); err != nil {
			t.Errorf("clearsigned: %v", err)
		}

		pub, _ := s.PublicKey(false)
		if el, err := openpgp.ReadKeyRing(bytes.NewReader(pub)); err != nil || len(el) != 1 {
			t.Errorf("binary public key: %v", err)
		}
		s.Close()
	}
}

func TestNativeKeySelection(t *testing.T) {
	k, cfg := writeKey(t, "secret")
	cfg.PassphraseFile = ""
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("protected key without passphrase: %v", err)
	}
	t.Setenv("CD_PASS", "wrong")
	cfg.PassphraseEnv = "CD_PASS"
	if _, err := New(cfg); err == nil {
		t.Error("wrong passphrase accepted")
	}
	t.Setenv("CD_PASS", "secret")
	cfg.KeyID = k.Fingerprint[len(k.Fingerprint)-16:]
	if _, err := New(cfg); err != nil {
		t.Errorf("long key ID: %v", err)
	}
	cfg.KeyID = "0123456789ABCDEF"
	if _, err := New(cfg); err == nil {
		t.Error("non-matching key_id accepted")
	}
	// A public key alone can't sign.
	os.WriteFile(cfg.KeyFile, k.Public, 0o600)
	cfg.KeyID = ""
	if _, err := New(cfg); err == nil {
		t.Error("public key accepted as signing key")
	}
}

func needGPG(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil || runtime.GOOS == "windows" {
		// Git for Windows ships an MSYS gpg that mangles native paths.
		t.Skip("needs a native gpg")
	}
}

func gpgHome(t *testing.T) (string, func(...string) string) {
	t.Helper()
	home, err := os.MkdirTemp("", "cdg") // short: gpg-agent socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()
		os.RemoveAll(home)
	})
	return home, func(args ...string) string {
		t.Helper()
		out, err := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--pinentry-mode", "loopback"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("gpg %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
}

// Real GnuPG (what pacman and librepo use) and sqv (what apt uses on
// Debian trixie) must accept native signatures.
func TestNativeSignaturesVerifyExternally(t *testing.T) {
	needGPG(t)
	k, cfg := writeKey(t, "")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data := []byte("Origin: x\n")
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	pub := write("pub.asc", k.Public)
	in := write("data", data)
	asc, _ := s.DetachSign(data, true)
	bin, _ := s.DetachSign(data, false)
	clear, _ := s.ClearSign(data)
	ascP, binP, clearP := write("data.asc", asc), write("data.sig", bin), write("InRelease", clear)

	_, gpg := gpgHome(t)
	gpg("--import", pub)
	for _, args := range [][]string{{ascP, in}, {binP, in}, {clearP}} {
		if out := gpg(append([]string{"--verify"}, args...)...); !strings.Contains(out, "Good signature") || strings.Contains(out, "no valid OpenPGP") {
			t.Errorf("gpg --verify %v: %s", args, out)
		}
	}

	if sqv, err := exec.LookPath("sqv"); err == nil {
		for _, args := range [][]string{{ascP, in}, {binP, in}} {
			out, err := exec.Command(sqv, append([]string{"--keyring", pub}, args...)...).CombinedOutput()
			if err != nil || !strings.Contains(string(out), k.Fingerprint) {
				t.Errorf("sqv %v: %v\n%s", args, err, out)
			}
		}
	}
}

// gnupg_home goes through gpg, for agent and smartcard keys. gpg must
// also be able to import keys from keygen, protected or not.
func TestGPGBackend(t *testing.T) {
	needGPG(t)
	for _, pass := range []string{"", "correct horse"} {
		k, cfg := writeKey(t, pass)
		home, gpg := gpgHome(t)
		gpg("--passphrase", pass, "--import", cfg.KeyFile)
		s, err := New(&config.Signing{GnuPGHome: home, KeyID: k.Fingerprint, PassphraseFile: cfg.PassphraseFile})
		if err != nil {
			t.Fatalf("pass=%q: %v", pass, err)
		}
		if _, ok := s.(*GPG); !ok || s.Fingerprint() != k.Fingerprint {
			t.Fatalf("got %T %s", s, s.Fingerprint())
		}
		data := []byte("Origin: x\n")
		sig, err := s.DetachSign(data, true)
		if err != nil {
			t.Fatalf("pass=%q: %v", pass, err)
		}
		keyring, _ := openpgp.ReadArmoredKeyRing(bytes.NewReader(k.Public))
		if _, err := openpgp.CheckArmoredDetachedSignature(keyring, bytes.NewReader(data), bytes.NewReader(sig), nil); err != nil {
			t.Errorf("pass=%q: gpg signature: %v", pass, err)
		}
		if _, err := s.ClearSign(data); err != nil {
			t.Error(err)
		}
	}
}
