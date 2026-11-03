// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package sign

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Native is a pure-Go Signer for a key file. It needs no gpg binary.
type Native struct {
	entity *openpgp.Entity
	fpr    string
}

// sigConfig is used for every signature. SHA-512 matches what the gpg
// backend asks for. Generated keys are v4 Ed25519: apt (sqv and gpgv),
// rpm-sequoia, librepo and pacman (GnuPG via gpgme) all accept them,
// whereas GnuPG rejects RFC 9580 v6 keys.
func sigConfig() *packet.Config {
	return &packet.Config{DefaultHash: crypto.SHA512}
}

// newNative loads the secret key in keyFile (armored or binary). keyID, if
// set, picks one key from a file holding several; it matches the end of
// the fingerprint, so long key IDs work too.
func newNative(keyFile, keyID, passphrase string) (*Native, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	var el openpgp.EntityList
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("-----BEGIN")) {
		el, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(b))
	} else {
		el, err = openpgp.ReadKeyRing(bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", keyFile, err)
	}
	want := strings.ToUpper(strings.TrimPrefix(strings.ReplaceAll(keyID, " ", ""), "0x"))
	for _, e := range el {
		if e.PrivateKey == nil {
			continue
		}
		fpr := fmt.Sprintf("%X", e.PrimaryKey.Fingerprint)
		if want != "" && !strings.HasSuffix(fpr, want) {
			continue
		}
		if err := unlock(e, passphrase); err != nil {
			return nil, fmt.Errorf("%s: %w", keyFile, err)
		}
		if _, ok := e.SigningKey(time.Now()); !ok {
			return nil, fmt.Errorf("%s: key %s has no usable signing key (expired or revoked?)", keyFile, fpr)
		}
		return &Native{entity: e, fpr: fpr}, nil
	}
	if want != "" {
		return nil, fmt.Errorf("%s: no secret key matching %s", keyFile, keyID)
	}
	return nil, fmt.Errorf("%s: no secret key found", keyFile)
}

// unlock decrypts every protected secret key in e.
func unlock(e *openpgp.Entity, passphrase string) error {
	encrypted := e.PrivateKey.Encrypted
	for _, sk := range e.Subkeys {
		if sk.PrivateKey != nil && sk.PrivateKey.Encrypted {
			encrypted = true
		}
	}
	if !encrypted {
		return nil
	}
	if passphrase == "" {
		return errors.New("the key is passphrase-protected; set signing.passphrase_env or signing.passphrase_file")
	}
	if err := e.DecryptPrivateKeys([]byte(passphrase)); err != nil {
		return fmt.Errorf("unlocking key: %w", err)
	}
	return nil
}

func (n *Native) DetachSign(data []byte, armored bool) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	if armored {
		err = openpgp.ArmoredDetachSign(&buf, n.entity, bytes.NewReader(data), sigConfig())
	} else {
		err = openpgp.DetachSign(&buf, n.entity, bytes.NewReader(data), sigConfig())
	}
	if err != nil {
		return nil, err
	}
	if armored {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func (n *Native) ClearSign(data []byte) ([]byte, error) {
	key, ok := n.entity.SigningKey(time.Now())
	if !ok {
		return nil, errors.New("no usable signing key")
	}
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, key.PrivateKey, sigConfig())
	if err != nil {
		return nil, err
	}
	// Like gpg --clearsign, the final line break separates the text from
	// the signature block rather than being part of the signed text.
	if _, err := w.Write(bytes.TrimSuffix(data, []byte("\n"))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return withArmorChecksum(buf.Bytes())
}

// withArmorChecksum re-armors the signature block of a clearsigned message
// with a CRC-24 line. go-crypto omits it, as RFC 9580 allows, but GnuPG
// 2.4 (gpgv, and gpgme for librepo and pacman) then reports "no valid
// OpenPGP data found" and fails verification despite a good signature.
func withArmorChecksum(msg []byte) ([]byte, error) {
	const begin = "-----BEGIN PGP SIGNATURE-----"
	i := bytes.LastIndex(msg, []byte(begin))
	if i < 0 {
		return nil, errors.New("clearsign: no signature block")
	}
	blk, err := armor.Decode(bytes.NewReader(msg[i:]))
	if err != nil {
		return nil, fmt.Errorf("clearsign: %w", err)
	}
	sig, err := io.ReadAll(blk.Body)
	if err != nil {
		return nil, fmt.Errorf("clearsign: %w", err)
	}
	out := bytes.NewBuffer(append([]byte(nil), msg[:i]...))
	if err := writeArmored(out, blk.Type, func(w io.Writer) error {
		_, err := w.Write(sig)
		return err
	}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (n *Native) PublicKey(armored bool) ([]byte, error) {
	return exportPublic(n.entity, armored)
}

func (n *Native) Fingerprint() string { return n.fpr }

func (n *Native) Close() error { return nil }

func exportPublic(e *openpgp.Entity, armored bool) ([]byte, error) {
	var buf bytes.Buffer
	if !armored {
		err := e.Serialize(&buf)
		return buf.Bytes(), err
	}
	if err := writeArmored(&buf, openpgp.PublicKeyType, e.Serialize); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeArmored(buf *bytes.Buffer, blockType string, serialize func(io.Writer) error) error {
	w, err := armor.Encode(buf, blockType, nil)
	if err != nil {
		return err
	}
	if err := serialize(w); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	buf.WriteByte('\n')
	return nil
}

// Generate creates a v4 Ed25519 signing key for uid ("Name <email>"),
// protected with passphrase if it isn't empty. The key has no encryption
// subkey and does not expire.
func Generate(uid, passphrase string) (*GeneratedKey, error) {
	name, email := splitUID(uid)
	cfg := &packet.Config{
		Algorithm:   packet.PubKeyAlgoEdDSA,
		Curve:       packet.Curve25519,
		DefaultHash: crypto.SHA512,
	}
	e, err := openpgp.NewEntity(name, "", email, cfg)
	if err != nil {
		return nil, err
	}
	// NewEntity always adds an encryption subkey; a repo key only signs.
	e.Subkeys = nil
	k := &GeneratedKey{Fingerprint: fmt.Sprintf("%X", e.PrimaryKey.Fingerprint)}
	if k.Public, err = exportPublic(e, true); err != nil {
		return nil, err
	}
	if passphrase != "" {
		if err := e.EncryptPrivateKeys([]byte(passphrase), cfg); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if err := writeArmored(&buf, openpgp.PrivateKeyType, func(w io.Writer) error {
		return e.SerializePrivateWithoutSigning(w, cfg)
	}); err != nil {
		return nil, err
	}
	k.Secret = buf.Bytes()
	return k, nil
}

// splitUID splits "Name <email>" for NewEntity, which rebuilds the same
// string. A bare name or bare address is kept as given.
func splitUID(uid string) (name, email string) {
	if a, err := mail.ParseAddress(uid); err == nil {
		return a.Name, a.Address
	}
	return uid, ""
}
