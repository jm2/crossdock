// Package sign produces the OpenPGP signatures each repo format needs.
//
// M1 drives the gpg binary, which also means hardware keys and gpg-agent
// work for free. A native backend (ProtonMail/go-crypto) slots in behind the
// same Signer interface in M2.
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

// GPG is a Signer backed by the gpg binary.
type GPG struct {
	gpg        string
	home       string
	tempHome   bool
	keyID      string
	fpr        string
	passphrase string
}

// New builds a Signer from config.
func New(c *config.Signing) (*GPG, error) {
	path, err := exec.LookPath("gpg")
	if err != nil {
		return nil, fmt.Errorf("signing needs the gpg binary until native signing lands (M2): %w", err)
	}
	g := &GPG{gpg: path, keyID: c.KeyID}
	if c.PassphraseEnv != "" {
		v, ok := os.LookupEnv(c.PassphraseEnv)
		if !ok {
			return nil, fmt.Errorf("passphrase env %s is not set", c.PassphraseEnv)
		}
		g.passphrase = v
	}
	if c.GnuPGHome != "" {
		g.home = c.GnuPGHome
	} else {
		dir, err := os.MkdirTemp("", "crossdock-gnupg-")
		if err != nil {
			return nil, err
		}
		g.home, g.tempHome = dir, true
		if _, err := g.run(nil, "--import", c.KeyFile); err != nil {
			g.Close()
			return nil, fmt.Errorf("importing %s: %w", c.KeyFile, err)
		}
	}
	if err := g.resolveKey(); err != nil {
		g.Close()
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

func (g *GPG) Close() error {
	if g.tempHome && g.home != "" {
		// Stop any agent started for the throwaway home.
		_ = exec.Command("gpgconf", "--homedir", g.home, "--kill", "all").Run()
		return os.RemoveAll(g.home)
	}
	return nil
}
