// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package deb

import (
	"bytes"
	"fmt"
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
		pkg, err := ReadPackage(f)
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
		}
		env.Logf("deb/%s: %s/binary-%s: %d packages", repo.Name, repo.Component, a, n)
	}

	release := releaseFile(repo, archs, indexes, env)
	if _, err := env.Release.WriteFile(path.Join(dist, "Release"), release); err != nil {
		return err
	}
	if repo.Suite != repo.Codename {
		if err := env.Release.Symlink(repo.Codename, path.Join(root, "dists", repo.Suite)); err != nil {
			return err
		}
	}
	if env.Signer == nil {
		return nil
	}
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
	_, err = env.Release.WriteFile(path.Join(dist, "InRelease"), inrel)
	return err
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

func releaseFile(repo config.Repo, archs []string, files []indexFile, env *publish.Env) []byte {
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	p := Paragraph{
		{Name: "Origin", Value: repo.Origin},
		{Name: "Label", Value: repo.Label},
		{Name: "Suite", Value: repo.Suite},
		{Name: "Codename", Value: repo.Codename},
		{Name: "Date", Value: env.Now.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC")},
		{Name: "Architectures", Value: strings.Join(archs, " ")},
		{Name: "Components", Value: repo.Component},
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
	return b.Bytes()
}
