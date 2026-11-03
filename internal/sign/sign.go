// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package sign produces the OpenPGP signatures each repo format needs.
//
// A key file (signing.key_file) is handled natively by ProtonMail/go-crypto,
// with no gpg binary needed. An existing keyring (signing.gnupg_home) goes
// through gpg instead, so gpg-agent and smartcard keys keep working.
package sign

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jm2/crossdock/internal/config"
)

// Signer creates signatures with a single key.
type Signer interface {
	// DetachSign returns a detached signature over data, ASCII-armored if
	// armor is set (apt Release.gpg, RPM repomd.xml.asc) or binary
	// otherwise (pacman .sig).
	DetachSign(data []byte, armor bool) ([]byte, error)
	// ClearSign returns a cleartext-signed copy of data (apt InRelease).
	ClearSign(data []byte) ([]byte, error)
	// PublicKey exports the signing public key.
	PublicKey(armor bool) ([]byte, error)
	// Fingerprint is the signing key's fingerprint.
	Fingerprint() string
	// Close releases resources such as a temporary keyring.
	Close() error
}

// New builds a Signer from config.
func New(c *config.Signing) (Signer, error) {
	pass, err := passphrase(c)
	if err != nil {
		return nil, err
	}
	if c.GnuPGHome != "" {
		return newGPG(c.GnuPGHome, c.KeyID, pass)
	}
	return newNative(c.KeyFile, c.KeyID, pass)
}

func passphrase(c *config.Signing) (string, error) {
	if c.PassphraseEnv != "" {
		v, ok := os.LookupEnv(c.PassphraseEnv)
		if !ok {
			return "", fmt.Errorf("passphrase env %s is not set", c.PassphraseEnv)
		}
		return v, nil
	}
	if c.PassphraseFile != "" {
		b, err := os.ReadFile(c.PassphraseFile)
		if err != nil {
			return "", fmt.Errorf("passphrase file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return "", nil
}

// GPG is a Signer backed by the gpg binary and an existing GnuPG home.
type GPG struct {
	gpg        string
	home       string
	keyID      string
	fpr        string
	passphrase string
}

func newGPG(home, keyID, passphrase string) (*GPG, error) {
	path, err := exec.LookPath("gpg")
	if err != nil {
		return nil, fmt.Errorf("signing.gnupg_home needs the gpg binary (or use signing.key_file): %w", err)
	}
	g := &GPG{gpg: path, home: home, keyID: keyID, passphrase: passphrase}
	if err := g.resolveKey(); err != nil {
		return nil, err
	}
	return g, nil
}

// resolveKey finds the fingerprint of the (first matching) secret key.
func (g *GPG) resolveKey() error {
	args := []string{"--with-colons", "--list-secret-keys"}
	if g.keyID != "" {
		args = append(args, g.keyID)
	}
	out, err := g.run(nil, args...)
	if err != nil {
		return fmt.Errorf("listing secret keys: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	inSec := false
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		switch {
		case f[0] == "sec":
			inSec = true
		case f[0] == "fpr" && inSec && len(f) > 9:
			g.fpr = f[9]
			if g.keyID == "" {
				g.keyID = g.fpr
			}
			return nil
		case f[0] == "ssb":
			inSec = false
		}
	}
	return errors.New("no secret key found")
}

func (g *GPG) run(stdin []byte, args ...string) ([]byte, error) {
	base := []string{"--homedir", g.home, "--batch", "--yes", "--no-tty", "--quiet"}
	var in []byte
	if g.passphrase != "" {
		base = append(base, "--pinentry-mode", "loopback", "--passphrase-fd", "0")
		in = append([]byte(g.passphrase+"\n"), stdin...)
	} else {
		in = stdin
	}
	cmd := exec.Command(g.gpg, append(base, args...)...)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gpg %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// signFile writes data to a temp file (stdin carries the passphrase) and
// runs a signing command over it.
func (g *GPG) signFile(data []byte, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "crossdock-sign-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return nil, err
	}
	full := append([]string{"--local-user", g.keyID, "--digest-algo", "SHA512", "--output", out}, args...)
	if _, err := g.run(nil, append(full, in)...); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}

func (g *GPG) DetachSign(data []byte, armor bool) ([]byte, error) {
	if armor {
		return g.signFile(data, "--armor", "--detach-sign")
	}
	return g.signFile(data, "--detach-sign")
}

func (g *GPG) ClearSign(data []byte) ([]byte, error) {
	return g.signFile(data, "--clearsign")
}

func (g *GPG) PublicKey(armor bool) ([]byte, error) {
	args := []string{"--export"}
	if armor {
		args = append(args, "--armor")
	}
	return g.run(nil, append(args, g.fpr)...)
}

func (g *GPG) Fingerprint() string { return g.fpr }

func (g *GPG) Close() error { return nil }

// GeneratedKey is a new signing key.
type GeneratedKey struct {
	Fingerprint string
	Secret      []byte // armored secret key
	Public      []byte // armored public key
}
