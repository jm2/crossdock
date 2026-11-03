// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package goo reads GooGet packages (.goo) and writes GooGet repository
// indexes, in the format google/googet's client and gooserve use.
//
// A .goo is a gzip-compressed tarball whose <name>.pkgspec member holds the
// package spec as JSON. A repo is an index (a JSON array of
// {Checksum, Source, PackageSpec}) served as <repo>/index.gz and
// <repo>/index. The client resolves Source against the repo URL's parent
// (url.JoinPath(repo, "..", Source)) and checks the package's SHA-256
// against Checksum. GooGet has no index signatures, so serve it over TLS
// beyond a trusted LAN.
package goo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// Suffixes are the file extensions crossdock picks up.
var Suffixes = []string{".goo"}

// validArch is googet's list (goolib.validArch).
var validArch = []string{"noarch", "x86_64", "x86_32", "arm", "arm64"}

const pkgSpecSuffix = ".pkgspec"

// Package is a parsed .goo.
type Package struct {
	Path    string
	Name    string
	Version string
	Arch    string
	// Spec is the package spec exactly as the package carries it
	// (compacted), so fields crossdock doesn't know about survive.
	Spec json.RawMessage
}

// FileName is googet's canonical package file name.
func (p *Package) FileName() string {
	return fmt.Sprintf("%s.%s.%s.goo", p.Name, p.Arch, p.Version)
}

// ReadPackage extracts and validates the package spec from a .goo.
func ReadPackage(p string) (*Package, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s: no %s file in package", p, pkgSpecSuffix)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if path.Ext(h.Name) != pkgSpecSuffix {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 16<<20))
		if err != nil {
			return nil, err
		}
		pkg, err := parseSpec(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		pkg.Path = p
		return pkg, nil
	}
}

func parseSpec(data []byte) (*Package, error) {
	var s struct {
		Name, Version, Arch string
		PkgDependencies     map[string]string
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("pkgspec: %w", err)
	}
	if s.Name == "" {
		return nil, errors.New("pkgspec: no name")
	}
	if !slices.Contains(validArch, s.Arch) {
		return nil, fmt.Errorf("pkgspec: invalid architecture %q", s.Arch)
	}
	if err := checkVersion(s.Version); err != nil {
		return nil, fmt.Errorf("pkgspec: %w", err)
	}
	for dep, v := range s.PkgDependencies {
		if err := checkVersion(v); err != nil {
			return nil, fmt.Errorf("pkgspec: dependency %q: %w", dep, err)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return &Package{Name: s.Name, Version: s.Version, Arch: s.Arch, Spec: compact.Bytes()}, nil
}

var semverRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// checkVersion applies googet's goolib.ParseVersion rules: a version,
// padded to three numeric parts, must be semver, optionally followed by
// "@<integer>".
func checkVersion(v string) error {
	if v == "" {
		return errors.New("empty version")
	}
	base, gs, hasGs := strings.Cut(v, "@")
	if hasGs {
		if _, err := strconv.ParseInt(gs, 10, 64); err != nil {
			return fmt.Errorf("version %q: bad @ suffix", v)
		}
	}
	if !semverRE.MatchString(fixVer(base)) {
		return fmt.Errorf("version %q is not semver", v)
	}
	return nil
}

// fixVer is goolib.fixVer: pad to three parts and trim leading zeros.
func fixVer(ver string) string {
	suffix := ""
	if i := strings.IndexAny(ver, "+-"); i != -1 {
		suffix, ver = ver[i:], ver[:i]
	}
	out := []string{"0", "0", "0"}
	nums := strings.SplitN(ver, ".", 3)
	offset := len(out) - len(nums)
	for i, s := range nums {
		t := strings.TrimLeft(s, "0")
		if t == "" {
			t = "0"
		}
		out[i+offset] = t
	}
	return strings.Join(out, ".") + suffix
}

// repoSpec is goolib.RepoSpec, field for field.
type repoSpec struct {
	Checksum    string
	Source      string
	PackageSpec json.RawMessage
}

// Publish builds goo/<name>/: packages/<file>.goo plus index and index.gz.
// Clients add the repo as http://host/goo/<name>.
func Publish(repo config.Repo, env *publish.Env) error {
	files, err := publish.FindFiles(repo.Source, Suffixes...)
	if err != nil {
		return err
	}
	root := path.Join("goo", repo.Name)
	allowed := map[string]bool{}
	for _, a := range repo.Architectures {
		allowed[a] = true
	}
	type entry struct {
		pkg *Package
		rs  repoSpec
	}
	byKey := map[string]*entry{}
	var entries []*entry
	for _, f := range files {
		pkg, err := ReadPackage(f)
		if err != nil {
			return err
		}
		if len(allowed) > 0 && !allowed[pkg.Arch] && pkg.Arch != "noarch" {
			env.Logf("goo/%s: skipping %s (%s not in architectures)", repo.Name, pkg.FileName(), pkg.Arch)
			continue
		}
		rel := path.Join("packages", pkg.FileName())
		d, err := publish.HashFile(f)
		if err != nil {
			return err
		}
		key := pkg.FileName()
		if prev, ok := byKey[key]; ok {
			if prev.rs.Checksum == d.SHA256 {
				continue
			}
			return fmt.Errorf("%s and %s are both %s with different contents", prev.pkg.Path, f, key)
		}
		if _, err := env.Release.AddFile(f, path.Join(root, rel)); err != nil {
			return err
		}
		e := &entry{pkg: pkg, rs: repoSpec{
			Checksum: d.SHA256,
			// Relative to the repo URL's parent, so it names the repo too.
			Source:      path.Join(repo.Name, rel),
			PackageSpec: pkg.Spec,
		}}
		byKey[key] = e
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].pkg, entries[j].pkg
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Arch != b.Arch {
			return a.Arch < b.Arch
		}
		return a.Version < b.Version
	})
	index := make([]repoSpec, len(entries))
	for i, e := range entries {
		index[i] = e.rs
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := env.Release.WriteFile(path.Join(root, "index"), data); err != nil {
		return err
	}
	if _, err := env.Release.WriteFile(path.Join(root, "index.gz"), publish.Gzip(data)); err != nil {
		return err
	}
	env.Logf("goo/%s: %d packages", repo.Name, len(entries))
	return nil
}
