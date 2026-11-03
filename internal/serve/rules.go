// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package serve exposes a published crossdock tree over HTTP, and renders
// equivalent configuration for external web servers.
//
// The caching and content-type policy is defined once, in Rules and Types,
// and both the built-in server and the generated nginx config are derived
// from it, so the two cannot drift apart.
package serve

import (
	"path"
	"regexp"
	"strings"
)

// Rule maps request paths to caching behaviour. The first matching rule
// wins. Patterns use the subset of regexp syntax that RE2 (Go) and PCRE
// (nginx) agree on.
type Rule struct {
	Name         string
	Pattern      string
	CacheControl string
	// DefaultType is the Content-Type for files whose extension is not in
	// Types (e.g. InRelease, Packages). Empty means application/octet-stream.
	DefaultType string
	re          *regexp.Regexp
}

const (
	cacheRevalidate = "no-cache"
	cachePackage    = "public, max-age=86400"
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheDefault    = "public, max-age=300"
)

// Rules is the shared serving policy.
var Rules = []*Rule{
	{
		// Content-addressed paths never change.
		Name:         "by-hash",
		Pattern:      `/by-hash/`,
		CacheControl: cacheImmutable,
	},
	{
		// Indexes that change on every publish: clients must revalidate
		// (cheap, thanks to Last-Modified / ETag) so they never pair a new
		// signature with a stale index or vice versa.
		Name: "metadata",
		Pattern: `(/(InRelease|Release|Release\.gpg|Packages|Packages\.(gz|xz|bz2|zst)|Sources|Sources\.(gz|xz)|repomd\.xml|repomd\.xml\.asc|index)` +
			`|\.(db|files)|\.(db|files)\.tar\.(gz|xz|zst|bz2)|\.(db|files)\.sig|\.(db|files)\.tar\.(gz|xz|zst|bz2)\.sig` +
			`|^/crossdock\.(asc|gpg))$`,
		CacheControl: cacheRevalidate,
		DefaultType:  "text/plain; charset=utf-8",
	},
	{
		// Package payloads. Names embed name+version+arch, but a rebuild
		// that reuses a version would change content, so cache for a day
		// rather than forever.
		Name:         "package",
		Pattern:      `\.(deb|rpm|goo|pkg\.tar|pkg\.tar\.(zst|xz|gz|bz2))(\.sig)?$`,
		CacheControl: cachePackage,
	},
	{
		Name:         "default",
		Pattern:      `.`,
		CacheControl: cacheDefault,
	},
}

// Types maps a file's final extension to its Content-Type.
var Types = map[string]string{
	"deb":   "application/vnd.debian.binary-package",
	"rpm":   "application/x-rpm",
	"goo":   "application/zip",
	"zst":   "application/zstd",
	"xz":    "application/x-xz",
	"gz":    "application/gzip",
	"bz2":   "application/x-bzip2",
	"tar":   "application/x-tar",
	"db":    "application/gzip", // pacman <repo>.db symlink -> .db.tar.gz
	"files": "application/gzip",
	"sig":   "application/pgp-signature",
	"asc":   "text/plain; charset=utf-8",
	"xml":   "application/xml",
	"json":  "application/json",
	"html":  "text/html; charset=utf-8",
	"txt":   "text/plain; charset=utf-8",
}

func init() {
	for _, r := range Rules {
		r.re = regexp.MustCompile(r.Pattern)
	}
}

// Match returns the first rule matching an absolute request path.
func Match(urlPath string) *Rule {
	for _, r := range Rules {
		if r.re.MatchString(urlPath) {
			return r
		}
	}
	return Rules[len(Rules)-1]
}

// ContentType picks the Content-Type for a request path.
func ContentType(urlPath string, r *Rule) string {
	base := path.Base(urlPath)
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		if t, ok := Types[base[i+1:]]; ok {
			return t
		}
	}
	if r.DefaultType != "" {
		return r.DefaultType
	}
	return "application/octet-stream"
}
