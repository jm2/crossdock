// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The cache remembers, for each source package file, its digest and the
// metadata each backend parsed from it, keyed by path, size and
// modification time (like make). An unchanged package is then neither
// reread, rehashed nor copied on the next publish. It lives at
// <output>/cache.json, outside any release, and only holds entries for files
// seen in the latest publish.
const (
	cacheFile    = "cache.json"
	cacheVersion = 1
)

type cacheEntry struct {
	Size    int64                      `json:"size"`
	ModTime int64                      `json:"mtime_ns"`
	Digest  *Digest                    `json:"digest,omitempty"`
	Meta    map[string]json.RawMessage `json:"meta,omitempty"`
}

type cache struct {
	Version int                    `json:"version"`
	Entries map[string]*cacheEntry `json:"entries"`
	// fresh are the entries confirmed or created during this publish.
	fresh map[string]*cacheEntry
}

func loadCache(root string) *cache {
	c := &cache{Version: cacheVersion, Entries: map[string]*cacheEntry{}, fresh: map[string]*cacheEntry{}}
	b, err := os.ReadFile(filepath.Join(root, cacheFile))
	if err != nil {
		return c
	}
	var old cache
	if json.Unmarshal(b, &old) == nil && old.Version == cacheVersion && old.Entries != nil {
		c.Entries = old.Entries
	}
	return c
}

func cacheKey(src string) (string, os.FileInfo, bool) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", nil, false
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil, false
	}
	return abs, fi, true
}

// entry returns the up-to-date entry for src, creating it (empty) if src
// is new or has changed.
func (c *cache) entry(src string) *cacheEntry {
	if c == nil {
		return nil
	}
	key, fi, ok := cacheKey(src)
	if !ok {
		return nil
	}
	if e := c.fresh[key]; e != nil && e.Size == fi.Size() && e.ModTime == fi.ModTime().UnixNano() {
		return e
	}
	e := c.Entries[key]
	if e == nil || e.Size != fi.Size() || e.ModTime != fi.ModTime().UnixNano() {
		e = &cacheEntry{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}
	}
	c.fresh[key] = e
	return e
}

func (c *cache) digest(src string) (Digest, bool) {
	if e := c.entry(src); e != nil && e.Digest != nil {
		return *e.Digest, true
	}
	return Digest{}, false
}

func (c *cache) put(src string, d Digest) {
	if e := c.entry(src); e != nil {
		e.Digest = &d
	}
}

func (c *cache) save(root string) error {
	if c == nil {
		return nil
	}
	out := cache{Version: cacheVersion, Entries: c.fresh}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, cacheFile), b)
}

// Digest returns a source file's digest, from the cache when the file is
// unchanged since it was last hashed.
func (r *Release) Digest(src string) (Digest, error) {
	if d, ok := r.cache.digest(src); ok {
		return d, nil
	}
	d, err := HashFile(src)
	if err != nil {
		return Digest{}, err
	}
	r.cache.put(src, d)
	return d, nil
}

// Cached returns parse(src), reusing the result from the last publish when
// src is unchanged. kind names the parser and its output version (e.g.
// "rpm/1"); bump the version when a parser's output changes.
func Cached[T any](r *Release, src, kind string, parse func(string) (T, error)) (T, error) {
	e := r.cache.entry(src)
	if e != nil && e.Meta != nil {
		if raw, ok := e.Meta[kind]; ok {
			var v T
			if json.Unmarshal(raw, &v) == nil {
				return v, nil
			}
		}
	}
	v, err := parse(src)
	if err != nil || e == nil {
		return v, err
	}
	if raw, merr := json.Marshal(v); merr == nil {
		if e.Meta == nil {
			e.Meta = map[string]json.RawMessage{}
		}
		e.Meta[kind] = raw
	}
	return v, nil
}
