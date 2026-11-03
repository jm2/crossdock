// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package archpkg

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

type entry struct {
	pkg    *Package
	file   string // base file name in the repo
	digest publish.Digest
	sig    []byte // binary detached signature, if any
}

// Publish builds arch/<name>/<arch>/ for each architecture: packages,
// signatures, and <name>.db / <name>.files (gzip tarballs, plus symlinks).
func Publish(repo config.Repo, env *publish.Env) error {
	files, err := publish.FindFiles(repo.Source, Suffixes...)
	if err != nil {
		return err
	}
	byKey := map[string]*entry{}
	var entries []*entry
	concrete := map[string]bool{}
	for _, f := range files {
		pkg, err := ReadPackage(f)
		if err != nil {
			return err
		}
		d, err := publish.HashFile(f)
		if err != nil {
			return err
		}
		key := pkg.Name() + "\x00" + pkg.Arch()
		if prev, ok := byKey[key]; ok {
			if prev.digest.SHA256 == d.SHA256 {
				continue
			}
			// pacman repos hold one version per name+arch.
			return fmt.Errorf("%s and %s both provide %s (%s); keep one version per package",
				prev.pkg.Path, f, pkg.Name(), pkg.Arch())
		}
		e := &entry{pkg: pkg, file: filepath.Base(f), digest: d}
		if sig, err := os.ReadFile(f + ".sig"); err == nil {
			e.sig = sig
		} else if !os.IsNotExist(err) {
			return err
		}
		if e.sig == nil && repo.SignPackages && env.Signer != nil {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if e.sig, err = env.Signer.DetachSign(b, false); err != nil {
				return fmt.Errorf("signing %s: %w", f, err)
			}
		}
		byKey[key] = e
		entries = append(entries, e)
		if pkg.Arch() != "any" {
			concrete[pkg.Arch()] = true
		}
	}

	archs := repo.Architectures
	if len(archs) == 0 {
		for a := range concrete {
			archs = append(archs, a)
		}
		if len(archs) == 0 {
			archs = []string{"x86_64"}
		}
	}
	archs = append([]string(nil), archs...)
	sort.Strings(archs)
	sort.Slice(entries, func(i, j int) bool { return entries[i].pkg.Name() < entries[j].pkg.Name() })

	for _, a := range archs {
		dir := path.Join("arch", repo.Name, a)
		var sel []*entry
		for _, e := range entries {
			if e.pkg.Arch() == a || e.pkg.Arch() == "any" {
				sel = append(sel, e)
			}
		}
		for _, e := range sel {
			if _, err := env.Release.AddFile(e.pkg.Path, path.Join(dir, e.file)); err != nil {
				return err
			}
			if e.sig != nil {
				if _, err := env.Release.WriteFile(path.Join(dir, e.file+".sig"), e.sig); err != nil {
					return err
				}
			}
		}
		for _, kind := range []string{"db", "files"} {
			data, err := buildDB(sel, kind == "files", env.Now)
			if err != nil {
				return err
			}
			name := repo.Name + "." + kind
			if _, err := env.Release.WriteFile(path.Join(dir, name+".tar.gz"), data); err != nil {
				return err
			}
			if err := env.Release.Symlink(name+".tar.gz", path.Join(dir, name)); err != nil {
				return err
			}
			if env.Signer != nil {
				sig, err := env.Signer.DetachSign(data, false)
				if err != nil {
					return err
				}
				if _, err := env.Release.WriteFile(path.Join(dir, name+".tar.gz.sig"), sig); err != nil {
					return err
				}
				if err := env.Release.Symlink(name+".tar.gz.sig", path.Join(dir, name+".sig")); err != nil {
					return err
				}
			}
		}
		env.Logf("arch/%s/%s: %d packages", repo.Name, a, len(sel))
	}
	return nil
}

// buildDB writes a pacman sync database: one <name>-<version>/ directory
// per package holding desc (and files, for the .files database).
func buildDB(entries []*entry, withFiles bool, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	mt := now.UTC().Truncate(time.Second)
	add := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: mt,
			Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	for _, e := range entries {
		dir := e.pkg.Name() + "-" + e.pkg.Version() + "/"
		if err := tw.WriteHeader(&tar.Header{
			Name: dir, Mode: 0o755, ModTime: mt, Typeflag: tar.TypeDir, Format: tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if err := add(dir+"desc", desc(e)); err != nil {
			return nil, err
		}
		if withFiles {
			var f bytes.Buffer
			f.WriteString("%FILES%\n")
			for _, p := range e.pkg.Files {
				f.WriteString(p + "\n")
			}
			f.WriteString("\n")
			if err := add(dir+"files", f.Bytes()); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return publish.Gzip(b.Bytes()), nil
}

// desc renders the desc entry the way repo-add does.
func desc(e *entry) []byte {
	p := e.pkg
	var b strings.Builder
	section := func(name string, vals ...string) {
		var nonEmpty []string
		for _, v := range vals {
			if v != "" {
				nonEmpty = append(nonEmpty, v)
			}
		}
		if len(nonEmpty) == 0 {
			return
		}
		b.WriteString("%" + name + "%\n")
		for _, v := range nonEmpty {
			b.WriteString(v + "\n")
		}
		b.WriteString("\n")
	}
	section("FILENAME", e.file)
	section("NAME", p.Name())
	section("BASE", p.Base())
	section("VERSION", p.Version())
	section("DESC", p.one("pkgdesc"))
	section("GROUPS", p.Info["group"]...)
	section("CSIZE", strconv.FormatInt(e.digest.Size, 10))
	section("ISIZE", p.one("size"))
	section("MD5SUM", e.digest.MD5)
	section("SHA256SUM", e.digest.SHA256)
	if e.sig != nil {
		section("PGPSIG", base64.StdEncoding.EncodeToString(e.sig))
	}
	section("URL", p.one("url"))
	section("LICENSE", p.Info["license"]...)
	section("ARCH", p.Arch())
	section("BUILDDATE", p.one("builddate"))
	section("PACKAGER", p.one("packager"))
	section("REPLACES", p.Info["replaces"]...)
	section("CONFLICTS", p.Info["conflict"]...)
	section("PROVIDES", p.Info["provides"]...)
	section("DEPENDS", p.Info["depend"]...)
	section("OPTDEPENDS", p.Info["optdepend"]...)
	section("MAKEDEPENDS", p.Info["makedepend"]...)
	section("CHECKDEPENDS", p.Info["checkdepend"]...)
	return []byte(b.String())
}
