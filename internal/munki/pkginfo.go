// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
)

// NameRE is the pattern for item names, catalog names and manifest path
// segments: the same as crossdock's repo names. Names become paths in the
// repo (pkgs/<name>/, icons/<name>.png, catalogs/<name>).
var NameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// Keys crossdock sets on every published pkginfo.
const (
	keyLocation = "installer_item_location"
	keyHash     = "installer_item_hash"
	keySize     = "installer_item_size"
	keyIcon     = "icon_name"
	keyCatalogs = "catalogs"
)

// Pkginfo is a validated pkginfo sidecar: the dictionary as the file holds
// it, plus the fields crossdock uses.
type Pkginfo struct {
	Info    map[string]any
	Name    string
	Version string
	// Warnings are problems that don't stop the item being published.
	Warnings []string
}

// ParsePkginfo reads and validates a pkginfo sidecar for a copy_from_dmg
// item. Keys crossdock doesn't check pass through as they are, scripts
// included: whoever can write to the source is trusted, as with RPM or
// pacman scriptlets.
func ParsePkginfo(data []byte) (*Pkginfo, error) {
	v, err := DecodePlist(data)
	if err != nil {
		return nil, err
	}
	info, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a dictionary")
	}
	p := &Pkginfo{Info: info}
	str := func(k string) (string, error) {
		s, ok := info[k].(string)
		if !ok {
			if _, present := info[k]; present {
				return "", fmt.Errorf("%s must be a string", k)
			}
			return "", fmt.Errorf("%s is required", k)
		}
		return s, nil
	}
	if p.Name, err = str("name"); err != nil {
		return nil, err
	}
	if !NameRE.MatchString(p.Name) || strings.HasSuffix(p.Name, ".dmg") {
		return nil, fmt.Errorf("name %q: use letters, digits and . _ + - (starting with a letter or digit, not ending in .dmg)", p.Name)
	}
	if p.Version, err = str("version"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Version) == "" || strings.ContainsFunc(p.Version, func(r rune) bool { return r < 0x20 }) {
		return nil, fmt.Errorf("version %q is empty or has control characters", p.Version)
	}
	if t, err := str("installer_type"); err != nil {
		return nil, err
	} else if t != "copy_from_dmg" {
		return nil, fmt.Errorf("installer_type %q: crossdock publishes copy_from_dmg items only", t)
	}
	if err := checkItemsToCopy(info["items_to_copy"]); err != nil {
		return nil, err
	}
	if n, ok := info["installed_size"].(int64); !ok || n <= 0 {
		return nil, errors.New("installed_size (KB, a positive integer) is required: Munki checks free space with it")
	}
	for _, k := range []string{"PackageURL", "PackageCompleteURL"} {
		if _, ok := info[k]; ok {
			return nil, fmt.Errorf("%s isn't allowed: it points Munki away from this repo", k)
		}
	}
	if _, ok := info["uninstaller_item_location"]; ok {
		return nil, errors.New("uninstaller_item_location isn't supported: crossdock publishes one installer item per pkginfo")
	}
	if c, ok := info[keyCatalogs]; ok {
		if _, err := Catalogs(c); err != nil {
			return nil, err
		}
	}
	for _, k := range []string{keyIcon, "display_name", "description"} {
		if _, err := optString(info, k); err != nil {
			return nil, err
		}
	}
	if _, err := optStrings(info, "supported_architectures"); err != nil {
		return nil, err
	}
	inst, present := info["installs"]
	if a, ok := inst.([]any); present && !ok {
		return nil, errors.New("installs must be an array")
	} else if len(a) == 0 {
		p.Warnings = append(p.Warnings, "no installs array: Munki will only check that the copied items exist, so it can't tell an old version from a new one")
	}
	return p, nil
}

// checkItemsToCopy checks what Munki's copy_from_dmg needs (see
// installer/dmg.swift): a source item inside the image, and an absolute
// destination.
func checkItemsToCopy(v any) error {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return errors.New("items_to_copy (a non-empty array) is required")
	}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return fmt.Errorf("items_to_copy[%d] must be a dictionary", i)
		}
		src, _ := m["source_item"].(string)
		if src == "" || strings.HasPrefix(src, "/") || hasDotDot(src) {
			return fmt.Errorf("items_to_copy[%d]: source_item %q must be a relative path inside the disk image, without ..", i, src)
		}
		for _, k := range []string{"destination_path", "destination_item", "user", "group", "mode"} {
			if _, err := optString(m, k); err != nil {
				return fmt.Errorf("items_to_copy[%d]: %w", i, err)
			}
		}
		dst, _ := m["destination_path"].(string)
		if dst == "" {
			// Munki falls back to destination_item as a full path.
			dst, _ = m["destination_item"].(string)
		}
		if !strings.HasPrefix(dst, "/") || hasDotDot(dst) {
			return fmt.Errorf("items_to_copy[%d]: destination_path %q must be an absolute path, without ..", i, dst)
		}
	}
	return nil
}

func hasDotDot(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return true
		}
	}
	return false
}

// Catalogs checks a catalogs value: a non-empty array of catalog names.
func Catalogs(v any) ([]string, error) {
	a, ok := v.([]any)
	if !ok || len(a) == 0 {
		return nil, errors.New("catalogs must be a non-empty array of names (leave it out to use the repo's catalog)")
	}
	out := make([]string, len(a))
	for i, e := range a {
		s, _ := e.(string)
		if !NameRE.MatchString(s) || s == "all" || s == "index.html" {
			return nil, fmt.Errorf("catalogs[%d]: invalid catalog name %q", i, s)
		}
		out[i] = s
	}
	return out, nil
}

func optString(m map[string]any, k string) (string, error) {
	v, ok := m[k]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", k)
	}
	return s, nil
}

func optStrings(m map[string]any, k string) ([]string, error) {
	v, ok := m[k]
	if !ok {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", k)
	}
	out := make([]string, len(a))
	for i, e := range a {
		if out[i], ok = e.(string); !ok {
			return nil, fmt.Errorf("%s must be an array of strings", k)
		}
	}
	return out, nil
}

// SizeKB is a size in Munki's units, KB, rounded up so free-space checks
// never come up short.
func SizeKB(bytes int64) int64 { return (bytes + 1023) / 1024 }

// Stale reports why a sidecar describes a different file than the one
// beside it, if its own installer_item_hash or installer_item_size (as
// makepkginfo writes them) disagree. That happens mid-upload: rsync sorts
// X.dmg ahead of X.dmg.pkginfo, so a new image lands next to the old
// sidecar.
func (p *Pkginfo) Stale(sha256 string, size int64) string {
	if h, ok := p.Info[keyHash].(string); ok && !strings.EqualFold(h, sha256) {
		return fmt.Sprintf("its installer_item_hash %s isn't the disk image's SHA-256 %s", h, sha256)
	}
	// makepkginfo rounds down; crossdock rounds up.
	if n, ok := p.Info[keySize].(int64); ok && n != size/1024 && n != SizeKB(size) {
		return fmt.Sprintf("its installer_item_size %d KB doesn't match the disk image's %d bytes", n, size)
	}
	return ""
}

// Published returns the pkginfo as published: the sidecar's keys plus the
// ones crossdock sets from the file it published. An empty icon, or
// catalog, leaves that key as the sidecar has it.
func (p *Pkginfo) Published(location, sha256 string, size int64, icon, catalog string) map[string]any {
	out := maps.Clone(p.Info)
	out[keyLocation] = location
	out[keyHash] = sha256
	out[keySize] = SizeKB(size)
	if icon != "" {
		out[keyIcon] = icon
	}
	if _, ok := out[keyCatalogs]; !ok && catalog != "" {
		out[keyCatalogs] = []any{catalog}
	}
	return out
}

// CatalogEntry is a pkginfo as makecatalogs puts it in a catalog: without
// the admin's notes or any key starting with "_".
func CatalogEntry(info map[string]any) map[string]any {
	out := maps.Clone(info)
	delete(out, "notes")
	for k := range out {
		if strings.HasPrefix(k, "_") {
			delete(out, k)
		}
	}
	return out
}
