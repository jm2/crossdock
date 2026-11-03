// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package deb

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// indexFields are recomputed by the repo and never copied from control.
var indexFields = []string{"Filename", "Size", "MD5sum", "SHA1", "SHA256", "SHA512", "Status"}

type entry struct {
	pkg    *Package
	pool   string
	digest publish.Digest
}

// Publish builds deb/<name>/ inside the release: a pool of packages and a
// signed dists/<codename> tree.
func Publish(repo config.Repo, env *publish.Env) error {
	files, err := publish.FindFiles(repo.Source, ".deb")
	if err != nil {
		return err
	}
	root := path.Join("deb", repo.Name)

	byKey := map[string]*entry{}
	var entries []*entry
	concrete := map[string]bool{}
	for _, f := range files {
		pkg, err := publish.Cached(env.Release, f, "deb/1", ReadPackage)
		if err != nil {
			return err
		}
		pool := pkg.PoolPath(repo.Component)
		d, err := env.Release.AddFile(f, path.Join(root, pool))
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		key := pkg.Name + "\x00" + pkg.Version + "\x00" + pkg.Arch
		if prev, ok := byKey[key]; ok {
			if prev.digest.SHA256 == d.SHA256 {
				continue
			}
			return fmt.Errorf("%s and %s are both %s %s (%s) with different contents",
				prev.pkg.Path, f, pkg.Name, pkg.Version, pkg.Arch)
		}
		e := &entry{pkg: pkg, pool: pool, digest: d}
		byKey[key] = e
		entries = append(entries, e)
		if pkg.Arch != "all" {
			concrete[pkg.Arch] = true
		}
	}

	archs := repo.Architectures
	if len(archs) == 0 {
		for a := range concrete {
			archs = append(archs, a)
		}
		if len(archs) == 0 {
			archs = []string{"amd64"}
		}
	}
	archs = append([]string(nil), archs...)
	sort.Strings(archs)
	allowed := map[string]bool{}
	for _, a := range archs {
		allowed[a] = true
	}
	for a := range concrete {
		if !allowed[a] {
			env.Logf("deb/%s: skipping %s packages (not in architectures)", repo.Name, a)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].pkg, entries[j].pkg
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Arch < b.Arch
	})

	dist := path.Join(root, "dists", repo.Codename)
	var indexes []indexFile
	for _, a := range archs {
		var buf bytes.Buffer
		n := 0
		for _, e := range entries {
			if e.pkg.Arch != a && e.pkg.Arch != "all" {
				continue
			}
			if n > 0 {
				buf.WriteString("\n")
			}
			stanza(e).Write(&buf)
			n++
		}
		rel := path.Join(repo.Component, "binary-"+a, "Packages")
		plain := buf.Bytes()
		for _, v := range []struct {
			name string
			data []byte
		}{{rel, plain}, {rel + ".gz", publish.Gzip(plain)}} {
			d, err := env.Release.WriteFile(path.Join(dist, v.name), v.data)
			if err != nil {
				return err
			}
			indexes = append(indexes, indexFile{v.name, d})
			// Acquire-By-Hash: the same bytes under their SHA-256, so a client
			// that read the previous InRelease still finds its indexes.
			if _, err := env.Release.WriteFile(path.Join(dist, byHash(v.name, d.SHA256)), v.data); err != nil {
				return err
			}
		}
		env.Logf("deb/%s: %s/binary-%s: %d packages", repo.Name, repo.Component, a, n)
	}

	if err := carryByHash(env.Release, dist); err != nil {
		return err
	}
	release, err := releaseFile(repo, archs, indexes, env)
	if err != nil {
		return err
	}
	if _, err := env.Release.WriteFile(path.Join(dist, "Release"), release); err != nil {
		return err
	}
	if env.Signer != nil {
		sig, err := env.Signer.DetachSign(release, true)
		if err != nil {
			return err
		}
		if _, err := env.Release.WriteFile(path.Join(dist, "Release.gpg"), sig); err != nil {
			return err
		}
		inrel, err := env.Signer.ClearSign(release)
		if err != nil {
			return err
		}
		if _, err := env.Release.WriteFile(path.Join(dist, "InRelease"), inrel); err != nil {
			return err
		}
	}
	// Last, so that where symlinks fall back to copies the copy is complete.
	if repo.Suite != repo.Codename {
		if err := env.Release.Symlink(repo.Codename, path.Join(root, "dists", repo.Suite)); err != nil {
			return err
		}
	}
	return nil
}

func stanza(e *entry) Paragraph {
	p := e.pkg.Control.Without(indexFields...)
	// Package first, as tools conventionally expect.
	out := Paragraph{{Name: "Package", Value: e.pkg.Name}}
	for _, f := range p {
		if !strings.EqualFold(f.Name, "Package") {
			out = append(out, f)
		}
	}
	return append(out,
		Field{Name: "Filename", Value: e.pool},
		Field{Name: "Size", Value: strconv.FormatInt(e.digest.Size, 10)},
		Field{Name: "MD5sum", Value: e.digest.MD5},
		Field{Name: "SHA1", Value: e.digest.SHA1},
		Field{Name: "SHA256", Value: e.digest.SHA256},
	)
}

type indexFile struct {
	name   string
	digest publish.Digest
}

// byHash is an index file's Acquire-By-Hash path.
func byHash(name, sha256 string) string {
	return path.Join(path.Dir(name), "by-hash", "SHA256", sha256)
}

// carryByHash keeps the previous release's indexes reachable by hash for
// one more generation: a client that fetched the old InRelease just before
// the swap can still download what it lists.
func carryByHash(rel *publish.Release, dist string) error {
	prev, ok := rel.Previous(path.Join(dist, "Release"))
	if !ok {
		return nil
	}
	b, err := os.ReadFile(prev)
	if err != nil {
		return nil // the previous release had no such suite
	}
	p, err := ParseParagraph(b)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(p.Get("SHA256"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || strings.Contains(f[2], "by-hash") {
			continue
		}
		if _, err := rel.LinkPrevious(path.Join(dist, byHash(f[2], f[0]))); err != nil {
			return err
		}
	}
	return nil
}

func releaseFile(repo config.Repo, archs []string, files []indexFile, env *publish.Env) ([]byte, error) {
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	p := Paragraph{
		{Name: "Origin", Value: repo.Origin},
		{Name: "Label", Value: repo.Label},
		{Name: "Suite", Value: repo.Suite},
		{Name: "Codename", Value: repo.Codename},
		{Name: "Date", Value: env.Now.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC")},
		{Name: "Architectures", Value: strings.Join(archs, " ")},
		{Name: "Components", Value: repo.Component},
		{Name: "Acquire-By-Hash", Value: "yes"},
	}
	valid, err := repo.ValidDuration()
	if err != nil {
		return nil, err
	}
	if valid > 0 {
		p = append(p[:5], append(Paragraph{{Name: "Valid-Until", Value: env.Now.Add(valid).UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC")}}, p[5:]...)...)
	}
	if repo.Description != "" {
		p = append(p, Field{Name: "Description", Value: repo.Description})
	}
	sums := []struct {
		name string
		get  func(publish.Digest) string
	}{
		{"MD5Sum", func(d publish.Digest) string { return d.MD5 }},
		{"SHA1", func(d publish.Digest) string { return d.SHA1 }},
		{"SHA256", func(d publish.Digest) string { return d.SHA256 }},
	}
	for _, s := range sums {
		var v strings.Builder
		for _, f := range files {
			fmt.Fprintf(&v, "\n %s %16d %s", s.get(f.digest), f.digest.Size, f.name)
		}
		p = append(p, Field{Name: s.name, Value: v.String()})
	}
	var b bytes.Buffer
	p.Write(&b)
	return b.Bytes(), nil
}
