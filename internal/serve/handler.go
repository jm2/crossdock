// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jm2/crossdock/internal/pages"
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
	// Index enables HTML directory listings. A directory's index.html is
	// served either way.
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
		h.dir(w, r, root, name, f)
		return
	}
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	h.file(w, r, "/"+name, f, fi)
}

// file serves one regular file with the headers its rule sets. urlPath is
// the cleaned path of the file opened, not the request's, so that
// /munki/x//manifests/m gets the munki rule, as /munki/x/manifests/m does.
func (h *Handler) file(w http.ResponseWriter, r *http.Request, urlPath string, f *os.File, fi os.FileInfo) {
	rule := Match(urlPath)
	w.Header().Set("Cache-Control", rule.CacheControl)
	w.Header().Set("Content-Type", ContentType(urlPath, rule))
	etag := fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size())
	if path.Base(urlPath) == pages.IndexName && fi.Size() <= maxPage {
		if page, ok := fillBaseURL(f, r); ok {
			// The body depends on the address the request came to.
			w.Header().Add("Vary", "X-Forwarded-Proto")
			w.Header().Set("ETag", fmt.Sprintf(`"%x-%x-%x"`, fi.ModTime().UnixNano(), fi.Size(), crc32.ChecksumIEEE(page)))
			http.ServeContent(w, r, fi.Name(), fi.ModTime(), bytes.NewReader(page))
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("ETag", etag)
	// ServeContent handles HEAD, Range and If-* conditionals.
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// maxPage bounds how much of a page fillBaseURL reads into memory. A
// larger one is served as published, placeholder and all.
const maxPage = 64 << 20

// fillBaseURL reads a page and replaces pages.BaseURLPlaceholder with the
// address r came to. ok is false if there was nothing to replace (the page
// was published with serve.public_url) or the address isn't usable.
func fillBaseURL(f *os.File, r *http.Request) ([]byte, bool) {
	base := requestBaseURL(r)
	if base == "" {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxPage+1))
	if err != nil || len(b) > maxPage || !bytes.Contains(b, []byte(pages.BaseURLPlaceholder)) {
		return nil, false
	}
	return bytes.ReplaceAll(b, []byte(pages.BaseURLPlaceholder), []byte(html.EscapeString(base))), true
}

// hostRE accepts a DNS name, IPv4 address or bracketed IPv6 address, with
// an optional port: nothing that needs escaping in a page, or in a shell
// word in single quotes (the brackets would glob unquoted).
var hostRE = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?)(:[0-9]{1,5})?$`)

// requestBaseURL is the scheme and host a client used to reach this
// server, or "" if the Host header isn't a plain host name. A reverse
// proxy that terminates TLS (Caddy, nginx) says so in X-Forwarded-Proto;
// it only changes the scheme shown on the page.
func requestBaseURL(r *http.Request) string {
	if !hostRE.MatchString(r.Host) {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "https":
		scheme = "https"
	case "http":
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// dir serves a directory: its index.html if it has one, else a listing if
// listings are on, else 404.
func (h *Handler) dir(w http.ResponseWriter, r *http.Request, root *os.Root, name string, d *os.File) {
	idx, err := root.Open(path.Join(name, pages.IndexName))
	var ifi os.FileInfo
	if err == nil {
		defer idx.Close()
		if ifi, err = idx.Stat(); err != nil || !ifi.Mode().IsRegular() {
			ifi = nil
		}
	}
	if ifi == nil && !h.Index {
		http.NotFound(w, r)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}
	if ifi != nil {
		h.file(w, r, "/"+path.Join(name, pages.IndexName), idx, ifi)
		return
	}
	h.listing(w, r, root, name, d)
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

// listing renders a directory as an HTML table: directories first, then
// files, each with its size and modification time in UTC. Entries are
// statted through root, so a symlink shows what it points to, and one that
// leads out of the release isn't listed at all.
func (h *Handler) listing(w http.ResponseWriter, r *http.Request, root *os.Root, name string, f *os.File) {
	entries, err := f.ReadDir(-1)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	type item struct {
		name string
		fi   os.FileInfo
	}
	var items []item
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := root.Stat(path.Join(name, e.Name()))
		if err != nil || !(fi.IsDir() || fi.Mode().IsRegular()) {
			continue
		}
		items = append(items, item{e.Name(), fi})
	}
	sort.Slice(items, func(i, j int) bool {
		if a, b := items[i].fi.IsDir(), items[j].fi.IsDir(); a != b {
			return a
		}
		return items[i].name < items[j].name
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheRevalidate)
	title := html.EscapeString(r.URL.Path)
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html>\n<html lang=\"en\">\n<meta charset=\"utf-8\">\n"+
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n<title>%s</title>\n<style>%s</style>\n"+
		"<h1>Index of %s</h1>\n<div class=\"wrap\"><table>\n"+
		"<thead><tr><th>Name</th><th class=\"num\">Size</th><th>Modified (UTC)</th></tr></thead>\n<tbody>\n",
		title, pages.Style, title)
	if r.URL.Path != "/" {
		b.WriteString("<tr><td><a href=\"../\">../</a></td><td></td><td></td></tr>\n")
	}
	for _, it := range items {
		mod := it.fi.ModTime().UTC().Format("2006-01-02 15:04")
		if it.fi.IsDir() {
			fmt.Fprintf(&b, "<tr><td><a href=\"%s/\">%s/</a></td><td class=\"num\">-</td><td>%s</td></tr>\n",
				html.EscapeString(pages.Href(it.name)), html.EscapeString(it.name), mod)
			continue
		}
		fmt.Fprintf(&b, "<tr><td><a href=\"%s\">%s</a></td><td class=\"num\" title=\"%d bytes\">%s</td><td>%s</td></tr>\n",
			html.EscapeString(pages.Href(it.name)), html.EscapeString(it.name), it.fi.Size(), pages.HumanSize(it.fi.Size()), mod)
	}
	b.WriteString("</tbody>\n</table></div>\n")
	io.WriteString(w, b.String())
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
