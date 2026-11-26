// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package munki publishes Munki repositories for macOS from disk images
// and pkginfo sidecars, laid out as Munki's makecatalogs would leave them.
//
// Each <file>.dmg in a repo's source is published under pkgs/. Its
// <file>.dmg.pkginfo sidecar (a pkginfo plist, as makepkginfo writes it)
// puts it in the catalogs, and an optional <file>.dmg.png becomes its icon.
// crossdock never looks inside a disk image: it checks the UDIF trailer,
// and takes everything else from the sidecar.
//
// Munki catalogs and manifests aren't signed, and a pkginfo can carry
// scripts Munki runs as root, so serve a Munki repo over TLS or a tailnet.
package munki

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

// Suffixes are the package files crossdock picks up.
var Suffixes = []string{".dmg"}

// Sidecar suffixes, appended to a disk image's file name.
const (
	PkginfoSuffix = ".pkginfo"
	IconSuffix    = ".png"
)

// SidecarSuffixes are the sidecar file names, for watching the source.
var SidecarSuffixes = []string{".dmg" + PkginfoSuffix, ".dmg" + IconSuffix}

// maxPlist bounds a sidecar or manifest read into memory.
const maxPlist = 16 << 20

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

type item struct {
	src  string // the disk image
	base string // its file name
	d    publish.Digest
	pi   *Pkginfo // nil: published for download only
	icon string   // the .dmg.png beside it, if any
	rel  string   // path in the repo, under pkgs/
	info map[string]any
}

// Publish builds munki/<name>/: pkgs/, pkgsinfo/, catalogs/, manifests/
// and icons/. Clients set SoftwareRepoURL to http://host/munki/<name>.
func Publish(repo config.Repo, env *publish.Env) error {
	root := path.Join("munki", repo.Name)
	warn := func(format string, args ...any) {
		env.Logf("warning: %s: %s", root, fmt.Sprintf(format, args...))
	}
	files, err := publish.FindFiles(repo.Source, append(slices.Clone(Suffixes), SidecarSuffixes...)...)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	var items []*item
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, ".dmg"):
		case !have[strings.TrimSuffix(strings.TrimSuffix(f, PkginfoSuffix), IconSuffix)]:
			warn("%s has no disk image beside it; ignored", f)
			continue
		default:
			continue
		}
		it, err := readItem(f, have, env, warn)
		if err != nil {
			return err
		}
		items = append(items, it)
	}

	// Newest last within a name, so icons come from the newest version.
	slices.SortStableFunc(items, func(a, b *item) int {
		return cmp.Or(cmp.Compare(itemName(a), itemName(b)), compareItems(a, b), cmp.Compare(a.src, b.src))
	})
	byPath := map[string]*item{}
	var kept []*item
	for i, it := range items {
		if i > 0 && it.pi != nil && items[i-1].pi != nil && compareItems(items[i-1], it) == 0 &&
			items[i-1].pi.Name == it.pi.Name {
			prev := items[i-1]
			if prev.d.SHA256 != it.d.SHA256 {
				return fmt.Errorf("%s and %s are both %s %s with different contents", prev.src, it.src, it.pi.Name, it.pi.Version)
			}
			env.Logf("%s: skipping %s (the same disk image as %s)", root, it.src, prev.src)
			continue
		}
		if it.pi != nil {
			it.rel = it.pi.Name + "/" + it.base
		} else {
			it.rel = it.base
		}
		if prev, ok := byPath[it.rel]; ok {
			return fmt.Errorf("%s and %s would both be published as pkgs/%s", prev.src, it.src, it.rel)
		}
		byPath[it.rel] = it
		kept = append(kept, it)
	}
	items = kept

	// Icons: icons/<name>.png from the newest version that has one.
	icons := map[string]*item{}
	for _, it := range items {
		if it.pi != nil && it.icon != "" {
			icons[it.pi.Name] = it
		}
	}
	iconHashes := map[string]any{}
	for name, it := range icons {
		d, err := env.Release.Digest(it.icon)
		if err != nil {
			return err
		}
		if err := env.Release.AddVerified(it.icon, path.Join(root, "icons", name+IconSuffix), d); err != nil {
			return err
		}
		iconHashes[name+IconSuffix] = d.SHA256
	}

	catalogs := map[string][]any{}
	var all []any
	names := map[string]bool{}
	downloadOnly := 0
	for _, it := range items {
		dst := path.Join(root, "pkgs", it.rel)
		if err := storeDMG(env.Release, it, dst); err != nil {
			return err
		}
		pf := publish.PackageFile{Repo: root, Path: dst, Size: it.d.Size, SHA256: it.d.SHA256}
		if it.pi == nil {
			downloadOnly++
			pf.Name, pf.Summary = strings.TrimSuffix(it.base, ".dmg"), "Download only: no pkginfo"
			env.Record(pf)
			continue
		}
		icon := ""
		if it.icon != "" {
			icon = it.pi.Name + IconSuffix
		}
		it.info = it.pi.Published(it.rel, it.d.SHA256, it.d.Size, icon, repo.Catalog)
		if n, _ := it.info[keyIcon].(string); n != "" && iconHashes[n] == nil {
			warn("%s: icon_name %q isn't in this repo's icons (add a %s%s beside the disk image)", it.src, n, it.base, IconSuffix)
		}
		data, err := EncodePlist(it.info)
		if err != nil {
			return fmt.Errorf("%s: %w", it.src+PkginfoSuffix, err)
		}
		if _, err := env.Release.WriteFile(path.Join(root, "pkgsinfo", it.pi.Name, strings.TrimSuffix(it.base, ".dmg")+".plist"), data); err != nil {
			return err
		}
		entry := CatalogEntry(it.info)
		all = append(all, entry)
		cs, _ := Catalogs(it.info[keyCatalogs])
		for _, c := range cs {
			catalogs[c] = append(catalogs[c], entry)
		}
		names[it.pi.Name] = true
		pf.Name, pf.Version = it.pi.Name, it.pi.Version
		archs, _ := optStrings(it.info, "supported_architectures")
		pf.Arch = strings.Join(archs, " ")
		pf.Summary = summary(it.info)
		env.Record(pf)
	}

	// makecatalogs writes "all" always, and other catalogs when they have
	// items.
	catalogs["all"] = all
	var catalogNames []string
	lower := map[string]string{}
	for c := range catalogs {
		if c != "all" {
			catalogNames = append(catalogNames, c)
		}
		if o, ok := lower[strings.ToLower(c)]; ok {
			warn("catalogs %q and %q differ only in case, which breaks on a case-insensitive file system", o, c)
		}
		lower[strings.ToLower(c)] = c
	}
	slices.Sort(catalogNames)
	for c, entries := range catalogs {
		if err := writePlist(env, path.Join(root, "catalogs", c), entries); err != nil {
			return err
		}
	}
	if len(iconHashes) > 0 {
		if err := writePlist(env, path.Join(root, "icons", "_icon_hashes.plist"), iconHashes); err != nil {
			return err
		}
	}

	// The generated manifest offers every item, from every catalog, the
	// repo's own catalog first (Munki takes an item from the first catalog
	// that has it).
	ordered := slices.DeleteFunc(slices.Clone(catalogNames), func(c string) bool { return c == repo.Catalog })
	if catalogs[repo.Catalog] != nil {
		ordered = append([]string{repo.Catalog}, ordered...)
	}
	manifest := map[string]any{"catalogs": ordered, "optional_installs": slices.Sorted(maps.Keys(names))}
	generated := []string{repo.Name}
	if repo.SiteDefault {
		generated = append(generated, "site_default")
	}
	for _, m := range generated {
		if err := writePlist(env, path.Join(root, "manifests", m), manifest); err != nil {
			return err
		}
	}
	n, err := copyManifests(repo, env, root, generated, catalogs, names, warn)
	if err != nil {
		return err
	}
	env.Logf("%s: %d items in catalogs, %d download only, %d manifests", root, len(all), downloadOnly, len(generated)+n)
	return nil
}

func itemName(it *item) string {
	if it.pi != nil {
		return it.pi.Name
	}
	return ""
}

func compareItems(a, b *item) int {
	if a.pi == nil || b.pi == nil {
		return 0
	}
	return CompareVersions(a.pi.Version, b.pi.Version)
}

// storeDMG adds a disk image to the release at dst, then checks the stored
// copy's trailer: readItem checked the source's, but the source could have
// been replaced by a truncated upload before it was hashed, and the stored
// copy is what the release serves.
func storeDMG(rel *publish.Release, it *item, dst string) error {
	if err := rel.AddVerified(it.src, dst, it.d); err != nil {
		return err
	}
	if _, err := ReadTrailer(filepath.Join(rel.Dir, filepath.FromSlash(dst))); err != nil {
		return fmt.Errorf("%s changed during the publish: %w", it.src, err)
	}
	return nil
}

// readItem checks one disk image and reads its sidecars.
func readItem(f string, have map[string]bool, env *publish.Env, warn func(string, ...any)) (*item, error) {
	if strings.ContainsFunc(filepath.Base(f), func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }) {
		return nil, fmt.Errorf("%q: disk image names can't contain control characters or backslashes", f)
	}
	if _, err := ReadTrailer(f); err != nil {
		return nil, err
	}
	d, err := env.Release.Digest(f)
	if err != nil {
		return nil, err
	}
	it := &item{src: f, base: filepath.Base(f), d: d}
	if have[f+IconSuffix] {
		if err := checkPNG(f + IconSuffix); err != nil {
			return nil, err
		}
		it.icon = f + IconSuffix
	}
	side := f + PkginfoSuffix
	if !have[side] {
		warn("%s has no %s sidecar: published for download only, not in any catalog", f, PkginfoSuffix)
		return it, nil
	}
	data, err := readLimited(side)
	if err != nil {
		return nil, err
	}
	pi, err := ParsePkginfo(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", side, err)
	}
	if why := pi.Stale(d.SHA256, d.Size); why != "" {
		warn("%s: %s, so it belongs to another build; published for download only until its sidecar is replaced", side, why)
		return it, nil
	}
	for _, w := range pi.Warnings {
		warn("%s: %s", side, w)
	}
	it.pi = pi
	return it, nil
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPlist+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPlist {
		return nil, fmt.Errorf("%s: larger than %d MiB", p, maxPlist>>20)
	}
	return data, nil
}

func checkPNG(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(pngMagic))
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, pngMagic) {
		return fmt.Errorf("%s: not a PNG image", p)
	}
	return nil
}

func summary(info map[string]any) string {
	if s, _ := info["description"].(string); strings.TrimSpace(s) != "" {
		first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
		return strings.TrimSpace(first)
	}
	s, _ := info["display_name"].(string)
	return s
}

func writePlist(env *publish.Env, rel string, v any) error {
	data, err := EncodePlist(v)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	_, err = env.Release.WriteFile(rel, data)
	return err
}

// Manifest keys that hold item names, checked in hand-written manifests.
var itemListKeys = []string{"managed_installs", "managed_uninstalls", "managed_updates",
	"optional_installs", "optional_uninstalls", "featured_items", "default_installs"}

// copyManifests publishes the hand-written manifests in manifests_source
// as they are, after checking them.
func copyManifests(repo config.Repo, env *publish.Env, root string, generated []string,
	catalogs map[string][]any, names map[string]bool, warn func(string, ...any)) (int, error) {
	if repo.ManifestsSource == "" {
		return 0, nil
	}
	files, err := publish.FindFiles(repo.ManifestsSource, "")
	if err != nil {
		return 0, err
	}
	rels := map[string]bool{}
	for _, g := range generated {
		rels[g] = true
	}
	type manifest struct {
		src, rel string
		m        map[string]any
	}
	var ms []manifest
	for _, f := range files {
		r, err := filepath.Rel(repo.ManifestsSource, f)
		if err != nil {
			return 0, err
		}
		rel := filepath.ToSlash(r)
		for _, seg := range strings.Split(rel, "/") {
			if !NameRE.MatchString(seg) || seg == "index.html" {
				return 0, fmt.Errorf("%s: manifest names use letters, digits and . _ + - (and aren't index.html)", f)
			}
		}
		if rels[rel] {
			return 0, fmt.Errorf("%s: crossdock generates manifests/%s for this repo; rename the file", f, rel)
		}
		data, err := readLimited(f)
		if err != nil {
			return 0, err
		}
		v, err := DecodePlist(data)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("%s: a manifest must be a dictionary", f)
		}
		if err := checkManifest(m); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		if _, err := env.Release.WriteFile(path.Join(root, "manifests", rel), data); err != nil {
			return 0, err
		}
		rels[rel] = true
		ms = append(ms, manifest{f, rel, m})
	}
	// Warn about what a client would fail to find, now that every manifest
	// is known.
	for _, mf := range ms {
		for _, w := range manifestWarnings(mf.m, rels, catalogs, names) {
			warn("%s: %s", mf.src, w)
		}
	}
	return len(ms), nil
}

// checkManifest checks the types of the keys Munki reads.
func checkManifest(m map[string]any) error {
	for _, k := range append([]string{"catalogs", "included_manifests"}, itemListKeys...) {
		if _, err := optStrings(m, k); err != nil {
			return err
		}
	}
	if v, ok := m["conditional_items"]; ok {
		a, ok := v.([]any)
		if !ok {
			return errors.New("conditional_items must be an array of dictionaries")
		}
		for i, e := range a {
			c, ok := e.(map[string]any)
			if !ok {
				return fmt.Errorf("conditional_items[%d] must be a dictionary", i)
			}
			if _, ok := c["condition"].(string); !ok {
				return fmt.Errorf("conditional_items[%d]: condition (a string) is required", i)
			}
			if err := checkManifest(c); err != nil {
				return fmt.Errorf("conditional_items[%d]: %w", i, err)
			}
		}
	}
	return nil
}

func manifestWarnings(m map[string]any, manifests map[string]bool, catalogs map[string][]any, names map[string]bool) []string {
	var out []string
	cs, _ := optStrings(m, "catalogs")
	for _, c := range cs {
		if catalogs[c] == nil {
			out = append(out, fmt.Sprintf("catalog %q isn't published (no item lists it)", c))
		}
	}
	inc, _ := optStrings(m, "included_manifests")
	for _, i := range inc {
		if !manifests[i] {
			out = append(out, fmt.Sprintf("included manifest %q isn't published", i))
		}
	}
	for _, k := range itemListKeys {
		items, _ := optStrings(m, k)
		for _, it := range items {
			if !knownItem(it, names) {
				out = append(out, fmt.Sprintf("%s: no item named %q in this repo's catalogs", k, it))
			}
		}
	}
	if a, ok := m["conditional_items"].([]any); ok {
		for _, e := range a {
			if c, ok := e.(map[string]any); ok {
				out = append(out, manifestWarnings(c, manifests, catalogs, names)...)
			}
		}
	}
	return out
}

// knownItem matches an item reference as Munki's nameAndVersion splits it:
// "name", "name-1.2" or "name--1.2".
func knownItem(s string, names map[string]bool) bool {
	if names[s] {
		return true
	}
	if n, _, ok := strings.Cut(s, "--"); ok && names[n] {
		return true
	}
	if i := strings.LastIndexByte(s, '-'); i > 0 && i+1 < len(s) && isDigit(s[i+1]) {
		return names[s[:i]]
	}
	return false
}
