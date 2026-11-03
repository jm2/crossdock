// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package serve

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jm2/crossdock/internal/publish"
)

// ControlPrefix holds crossdock's own endpoints. Repo trees live under
// /deb, /arch, /rpm and /goo, so this cannot collide with published files.
const ControlPrefix = "/_crossdock/"

// ReleaseHeader carries the ID of the release a response came from.
const ReleaseHeader = "X-Crossdock-Release"

// Handler serves <Output>/current read-only.
type Handler struct {
	// Output is the crossdock output root (the parent of current/).
	Output string
	// Index enables minimal HTML directory listings.
	Index bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, ControlPrefix) {
		h.control(w, r)
		return
	}

	// Resolve the release once per request, so a concurrent publish swaps
	// in between requests, never within one.
	id := publish.Current(h.Output)
	if id == "" {
		http.Error(w, "no release published yet", http.StatusServiceUnavailable)
		return
	}
	root, err := os.OpenRoot(filepath.Join(h.Output, "releases", id))
	if err != nil {
		http.Error(w, "release unavailable", http.StatusServiceUnavailable)
		return
	}
	defer root.Close()
	w.Header().Set(ReleaseHeader, id)

	name, ok := cleanPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// os.Root follows symlinks only while they stay inside the release.
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}
	if fi.IsDir() {
		if !h.Index {
			http.NotFound(w, r)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		h.listing(w, r, f)
		return
	}
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	rule := Match(r.URL.Path)
	w.Header().Set("Cache-Control", rule.CacheControl)
	w.Header().Set("Content-Type", ContentType(r.URL.Path, rule))
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size()))
	// ServeContent handles HEAD, Range and If-* conditionals.
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// cleanPath turns a URL path into a root-relative file name, refusing
// dot-segments and hidden files.
func cleanPath(p string) (string, bool) {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") || strings.Contains(p, "\\") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	name := strings.TrimPrefix(path.Clean(p), "/")
	if name == "" {
		name = "."
	}
	return name, true
}

func (h *Handler) listing(w http.ResponseWriter, r *http.Request, f *os.File) {
	entries, err := f.ReadDir(-1)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheRevalidate)
	title := html.EscapeString(r.URL.Path)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><ul>\n", title, title)
	if r.URL.Path != "/" {
		fmt.Fprint(w, "<li><a href=\"../\">../</a>\n")
	}
	for _, n := range names {
		e := html.EscapeString(n)
		fmt.Fprintf(w, "<li><a href=\"%s\">%s</a>\n", e, e)
	}
	fmt.Fprint(w, "</ul>\n")
}

func (h *Handler) control(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := publish.Current(h.Output)
	switch strings.TrimPrefix(r.URL.Path, ControlPrefix) {
	case "healthz":
		if id == "" {
			http.Error(w, "no release", http.StatusServiceUnavailable)
			return
		}
		if _, err := os.Stat(filepath.Join(h.Output, "releases", id)); err != nil {
			http.Error(w, "current release missing", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	case "release":
		if id == "" {
			http.Error(w, "no release", http.StatusServiceUnavailable)
			return
		}
		var published time.Time
		if fi, err := os.Stat(filepath.Join(h.Output, "releases", id)); err == nil {
			published = fi.ModTime().UTC()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"release": id, "modified": published})
	default:
		http.NotFound(w, r)
	}
}
