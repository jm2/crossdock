// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package config loads and validates crossdock configuration.
//
// M1 uses JSON (standard library only); YAML is planned once the module
// can take third-party dependencies. The schema is identical either way.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Format identifies a repository format.
type Format string

const (
	FormatDeb  Format = "deb"
	FormatArch Format = "arch"
	FormatRPM  Format = "rpm"
	FormatGoo  Format = "goo"
)

// Config is the top-level crossdock configuration.
type Config struct {
	// Output is the root directory releases are published under. The web
	// server should serve <Output>/current.
	Output string `json:"output"`
	// KeepReleases is how many releases (including current) to retain.
	KeepReleases int `json:"keep_releases"`
	// Signing configures the OpenPGP key. Nil means unsigned.
	Signing *Signing `json:"signing,omitempty"`
	// NoSymlinks publishes without symlinks: in-release links become
	// hardlinks or copies, and the current release is recorded in
	// <output>/current.id instead of the current symlink. crossdock serve
	// follows either; static web servers need symlinks. Windows without
	// symlink privileges falls back to this automatically.
	NoSymlinks bool `json:"no_symlinks,omitempty"`
	// Serve configures `crossdock serve`.
	Serve Serve  `json:"serve,omitempty"`
	Repos []Repo `json:"repos"`
}

// DefaultListen is serve's default address: every interface, port 8080,
// so the repo is reachable on the LAN without root or a reverse proxy.
const DefaultListen = ":8080"

// Serve configures the built-in HTTP server.
type Serve struct {
	// Listen is the TCP address to listen on (default ":8080").
	Listen string `json:"listen,omitempty"`
	// Index enables HTML directory listings.
	Index bool `json:"index,omitempty"`
}

// Signing configures repository and package signatures.
type Signing struct {
	// KeyFile is an armored private key, imported into a throwaway GnuPG
	// home for each run. Mutually exclusive with GnuPGHome.
	KeyFile string `json:"key_file,omitempty"`
	// GnuPGHome uses an existing keyring (and agent / smartcard) instead.
	GnuPGHome string `json:"gnupg_home,omitempty"`
	// KeyID selects the signing key; required with GnuPGHome.
	KeyID string `json:"key_id,omitempty"`
	// PassphraseEnv names an environment variable holding the passphrase.
	PassphraseEnv string `json:"passphrase_env,omitempty"`
	// PassphraseFile reads the passphrase from a file (trailing newline
	// trimmed), e.g. a systemd credential.
	PassphraseFile string `json:"passphrase_file,omitempty"`
}

// Repo is one published repository.
type Repo struct {
	Name   string `json:"name"`
	Format Format `json:"format"`
	// Source is the directory of package files that make up this repo.
	Source string `json:"source"`
	// Architectures lists the arch directories to emit. Arch-independent
	// packages (deb "all", pacman "any") are placed in every one. If empty,
	// the concrete architectures found in Source are used.
	Architectures []string `json:"architectures,omitempty"`

	// deb
	Suite       string `json:"suite,omitempty"`
	Codename    string `json:"codename,omitempty"`
	Component   string `json:"component,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	// ValidFor adds Valid-Until to Release (e.g. "7d", "36h"), so clients
	// refuse a stale or replayed index. Publish more often than this;
	// crossdock-publish.timer republishes daily.
	ValidFor string `json:"valid_for,omitempty"`

	// arch
	// SignPackages creates detached .sig files for packages that lack one.
	SignPackages bool `json:"sign_packages,omitempty"`
}

// ValidDuration parses ValidFor: a Go duration, or a number of days with
// a "d" suffix. Zero means no Valid-Until.
func (r Repo) ValidDuration() (time.Duration, error) {
	if r.ValidFor == "" {
		return 0, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(r.ValidFor, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("valid_for %q: %w", r.ValidFor, err)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(r.ValidFor); err != nil {
			return 0, fmt.Errorf("valid_for %q: %w", r.ValidFor, err)
		}
	}
	if d < time.Hour {
		return 0, fmt.Errorf("valid_for %q: must be at least 1h", r.ValidFor)
	}
	return d, nil
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Load reads, defaults and validates a config file. Paths in the config
// may reference environment variables ("${CREDENTIALS_DIRECTORY}/key.asc"),
// and relative paths are resolved against the config file's directory.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	c.resolve(base)
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func abs(base, p string) string {
	p = os.ExpandEnv(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

func (c *Config) resolve(base string) {
	c.Output = abs(base, c.Output)
	if c.Signing != nil {
		c.Signing.KeyFile = abs(base, c.Signing.KeyFile)
		c.Signing.GnuPGHome = abs(base, c.Signing.GnuPGHome)
		c.Signing.PassphraseFile = abs(base, c.Signing.PassphraseFile)
	}
	for i := range c.Repos {
		c.Repos[i].Source = abs(base, c.Repos[i].Source)
	}
}

func (c *Config) applyDefaults() {
	if c.KeepReleases <= 0 {
		c.KeepReleases = 3
	}
	if c.Serve.Listen == "" {
		c.Serve.Listen = DefaultListen
	}
	for i := range c.Repos {
		r := &c.Repos[i]
		if r.Format == FormatDeb {
			if r.Suite == "" {
				r.Suite = "stable"
			}
			if r.Codename == "" {
				r.Codename = r.Suite
			}
			if r.Component == "" {
				r.Component = "main"
			}
			if r.Origin == "" {
				r.Origin = r.Name
			}
			if r.Label == "" {
				r.Label = r.Name
			}
		}
	}
}

// Validate checks the config for errors.
func (c *Config) Validate() error {
	if c.Output == "" {
		return fmt.Errorf("output is required")
	}
	if s := c.Signing; s != nil {
		if (s.KeyFile == "") == (s.GnuPGHome == "") {
			return fmt.Errorf("signing: set exactly one of key_file or gnupg_home")
		}
		if s.GnuPGHome != "" && s.KeyID == "" {
			return fmt.Errorf("signing: key_id is required with gnupg_home")
		}
		if s.PassphraseEnv != "" && s.PassphraseFile != "" {
			return fmt.Errorf("signing: set at most one of passphrase_env or passphrase_file")
		}
	}
	if len(c.Repos) == 0 {
		return fmt.Errorf("no repos configured")
	}
	seen := map[string]bool{}
	for i, r := range c.Repos {
		if !nameRE.MatchString(r.Name) {
			return fmt.Errorf("repos[%d]: invalid name %q", i, r.Name)
		}
		switch r.Format {
		case FormatDeb, FormatArch, FormatRPM, FormatGoo:
		default:
			return fmt.Errorf("repos[%d] %s: unknown format %q", i, r.Name, r.Format)
		}
		key := string(r.Format) + "/" + r.Name
		if seen[key] {
			return fmt.Errorf("repos[%d]: duplicate %s repo %q", i, r.Format, r.Name)
		}
		seen[key] = true
		if r.Source == "" {
			return fmt.Errorf("repos[%d] %s: source is required", i, r.Name)
		}
		for _, a := range r.Architectures {
			if !nameRE.MatchString(a) {
				return fmt.Errorf("repos[%d] %s: invalid architecture %q", i, r.Name, a)
			}
		}
		if _, err := r.ValidDuration(); err != nil {
			return fmt.Errorf("repos[%d] %s: %w", i, r.Name, err)
		}
		if r.ValidFor != "" && r.Format != FormatDeb {
			return fmt.Errorf("repos[%d] %s: valid_for only applies to deb repos", i, r.Name)
		}
		if r.Format == FormatDeb {
			for _, f := range []string{r.Suite, r.Codename, r.Component} {
				if !nameRE.MatchString(f) {
					return fmt.Errorf("repos[%d] %s: invalid suite/codename/component %q", i, r.Name, f)
				}
			}
		}
	}
	return nil
}
