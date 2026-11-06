// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package publish manages crossdock's on-disk output: a content-addressed
// store of package payloads, immutable release directories, and the atomic
// "current" symlink swap.
//
//	<output>/current -> releases/<id>
//	<output>/releases/<id>/...
//	<output>/store/<sha256>
package publish

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	currentLink = "current"
	// currentFile records the current release where symlinks aren't
	// available (Windows without Developer Mode); see Commit.
	currentFile = "current.id"
	releasesDir = "releases"
	storeDir    = "store"
	// metaDir holds crossdock's bookkeeping inside a release. serve and the
	// generated nginx config never expose dot paths.
	metaDir      = ".crossdock"
	manifestName = "store-objects"
	// inProgressName marks a release until it's committed. A publish that
	// was killed leaves it behind, which is how Prune knows the release
	// was never live.
	inProgressName = "in-progress"
	// incomingPrefix names store files still being added. A publish that
	// was killed can leave them behind; the next successful one removes them.
	incomingPrefix = ".incoming-"
)

// symlink is os.Symlink, swappable so tests can force the fallbacks.
var symlink = os.Symlink

// removeLink is os.Remove for the old current symlink, swappable so tests
// can make the migration to current.id fail.
var removeLink = os.Remove

// lstat is os.Lstat for the old current symlink, swappable for tests.
var lstat = os.Lstat

// Digest describes a file's size and hashes.
type Digest struct {
	Size   int64
	MD5    string
	SHA1   string
	SHA256 string
}

// HashBytes digests an in-memory buffer.
func HashBytes(b []byte) Digest {
	m, s1, s256 := md5.Sum(b), sha1.Sum(b), sha256.Sum256(b)
	return Digest{
		Size:   int64(len(b)),
		MD5:    hex.EncodeToString(m[:]),
		SHA1:   hex.EncodeToString(s1[:]),
		SHA256: hex.EncodeToString(s256[:]),
	}
}

// HashFile digests a file on disk.
func HashFile(path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	return hashReader(f, io.Discard)
}

func hashReader(r io.Reader, w io.Writer) (Digest, error) {
	m, s1, s256 := md5.New(), sha1.New(), sha256.New()
	n, err := io.Copy(io.MultiWriter(w, m, s1, s256), r)
	if err != nil {
		return Digest{}, err
	}
	return Digest{
		Size:   n,
		MD5:    hex.EncodeToString(m.Sum(nil)),
		SHA1:   hex.EncodeToString(s1.Sum(nil)),
		SHA256: hex.EncodeToString(s256.Sum(nil)),
	}, nil
}

// Release is a release directory under construction.
type Release struct {
	root string // output root
	ID   string
	Dir  string // absolute path of the release directory
	done bool
	// prev is the release that was current when this one began, or "".
	prev string
	// objects are the store digests this release links to.
	objects map[string]bool
	cache   *cache
	// lastSave is when the metadata cache was last written (see checkpoint).
	lastSave time.Time
	// copyWarned is set once Logf has reported a failed hardlink.
	copyWarned bool
	// Logf, if set, reports conditions worth an operator's attention.
	Logf func(format string, args ...any)
	// noSymlinks makes the release copy instead of linking (see NoSymlinks).
	noSymlinks bool
}

// NoSymlinks makes this release use no symlinks at all: in-release links
// become hardlinks or copies, and current is recorded in current.id. Use it
// for hosts or file systems that can't make or follow symlinks.
func (r *Release) NoSymlinks() { r.noSymlinks = true }

func (r *Release) symlink(target, path string) error {
	if r.noSymlinks {
		return errors.New("symlinks disabled")
	}
	return symlink(target, path)
}

// Begin creates a new, empty release directory under root.
func Begin(root string, now time.Time) (*Release, error) {
	for _, d := range []string{root, filepath.Join(root, releasesDir), filepath.Join(root, storeDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	id := now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd[:])
	dir := filepath.Join(root, releasesDir, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, metaDir), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, metaDir, inProgressName), nil, 0o644); err != nil {
		return nil, err
	}
	return &Release{
		root: root, ID: id, Dir: dir, prev: Current(root),
		objects: map[string]bool{}, cache: loadCache(root),
		lastSave: time.Now(),
		// Windows can't atomically rename a symlink over the existing
		// current symlink ("Access is denied"), even where it can create
		// symlinks, so it always publishes with current.id.
		noSymlinks: runtime.GOOS == "windows",
	}, nil
}

// Previous returns the absolute path of rel in the release that was current
// when this one began, and whether that release exists.
func (r *Release) Previous(rel string) (string, bool) {
	if r.prev == "" {
		return "", false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	// IsLocal also rejects rooted paths ("\\etc") and reserved names
	// ("NUL") on Windows, which IsAbs alone lets through.
	if !filepath.IsLocal(clean) {
		return "", false
	}
	return filepath.Join(r.root, releasesDir, r.prev, clean), true
}

// LinkPrevious hardlinks (or copies) a file from the previous release into
// the same path in this one. A missing source is not an error; it reports
// whether the file was carried over.
func (r *Release) LinkPrevious(rel string) (bool, error) {
	src, ok := r.Previous(rel)
	if !ok {
		return false, nil
	}
	fi, err := os.Stat(src)
	if err != nil || !fi.Mode().IsRegular() {
		return false, nil
	}
	dst, err := r.Path(rel)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := os.Link(src, dst); err != nil {
		if err := copyFile(src, dst); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Path returns the absolute path of rel inside the release, rejecting
// paths that escape it.
func (r *Release) Path(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	// IsLocal also rejects rooted paths ("\\etc") and reserved names
	// ("NUL") on Windows, which IsAbs alone lets through.
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("path %q escapes release", rel)
	}
	return filepath.Join(r.Dir, clean), nil
}

// WriteFile writes generated metadata into the release.
func (r *Release) WriteFile(rel string, data []byte) (Digest, error) {
	p, err := r.Path(rel)
	if err != nil {
		return Digest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Digest{}, err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return Digest{}, err
	}
	return HashBytes(data), nil
}

// Symlink creates a relative symlink at rel pointing to target (a path
// relative to rel's directory). Where symlinks can't be created, it
// hardlinks or copies the target instead, so clients see the same content.
// Call it after the target exists.
func (r *Release) Symlink(target, rel string) error {
	p, err := r.Path(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := r.symlink(target, p); err == nil {
		return nil
	}
	src := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
	if rel, err := filepath.Rel(r.Dir, src); err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("symlink target %q escapes release", target)
	}
	return copyTree(src, p)
}

// copyTree hardlinks (or copies) a file or directory tree.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if err := os.Link(path, out); err != nil {
			return copyFile(path, out)
		}
		return nil
	})
}

// AddFile places a package payload at rel inside the release. The payload
// is copied into the content store once (keyed by SHA-256) and hardlinked
// into the release, falling back to a copy where hardlinks are unsupported.
// Adding the same path twice with the same content is a no-op.
func (r *Release) AddFile(src, rel string) (Digest, error) {
	dst, err := r.Path(rel)
	if err != nil {
		return Digest{}, err
	}
	d, obj, err := r.store(src)
	if err != nil {
		return Digest{}, err
	}
	r.objects[d.SHA256] = true
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Digest{}, err
	}
	if fi, err := os.Lstat(dst); err == nil {
		if existing, herr := HashFile(dst); herr == nil && existing.SHA256 == d.SHA256 && fi.Mode().IsRegular() {
			return d, nil
		}
		return Digest{}, fmt.Errorf("%s: already exists with different content", rel)
	}
	if err := os.Link(obj, dst); err != nil {
		if err := copyFile(obj, dst); err != nil {
			return Digest{}, err
		}
	}
	return d, nil
}

func (r *Release) store(src string) (Digest, string, error) {
	// Unchanged since the last publish and already stored: no read at all.
	if d, ok := r.cache.digest(src); ok {
		if obj := filepath.Join(r.root, storeDir, d.SHA256); intact(obj, d) {
			return d, obj, nil
		}
	}
	d, obj, linked, err := r.linkIn(src)
	if err != nil {
		return Digest{}, "", err
	}
	if linked {
		return d, obj, nil
	}
	f, err := os.Open(src)
	if err != nil {
		return Digest{}, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Digest{}, "", err
	}
	tmp, err := os.CreateTemp(filepath.Join(r.root, storeDir), incomingPrefix+"*")
	if err != nil {
		return Digest{}, "", err
	}
	defer os.Remove(tmp.Name())
	d, err = hashReader(f, tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Digest{}, "", err
	}
	r.cache.putFor(src, fi, d)
	r.checkpoint()
	obj = filepath.Join(r.root, storeDir, d.SHA256)
	if intact(obj, d) {
		return d, obj, nil
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return Digest{}, "", err
	}
	if err := os.Rename(tmp.Name(), obj); err != nil {
		return Digest{}, "", err
	}
	return d, obj, nil
}

// linkIn adds src to the store as a hardlink, so a package costs no extra
// disk when the source directory and the store share a file system. The
// store object then shares src's inode: replace sources by rename (rsync,
// mv), never by writing into them. linked is false if the caller must copy
// instead: the file systems differ, the kernel refuses (with
// fs.protected_hardlinks, the publishing account needs write access to src
// or must own it), or src isn't world-readable, which can't be changed here
// without changing the source file too. Windows always copies: a link
// would keep the source's ACL, which its mode bits don't describe.
func (r *Release) linkIn(src string) (d Digest, obj string, linked bool, err error) {
	if runtime.GOOS == "windows" {
		return Digest{}, "", false, nil
	}
	if fi, err := os.Lstat(src); err != nil {
		return Digest{}, "", false, err
	} else if !fi.Mode().IsRegular() {
		return Digest{}, "", false, nil
	}
	tmp := filepath.Join(r.root, storeDir, incomingPrefix+r.ID+"-"+strconv.Itoa(len(r.objects)))
	if err := os.Link(src, tmp); err != nil {
		r.warnCopy(err)
		return Digest{}, "", false, nil
	}
	defer os.Remove(tmp)
	// From here on, tmp is the inode being stored even if src is replaced.
	fi, err := os.Stat(tmp)
	if err != nil {
		return Digest{}, "", false, err
	}
	if fi.Mode().Perm()&0o444 != 0o444 {
		r.warnCopy(fmt.Errorf("%s is not world-readable", src))
		return Digest{}, "", false, nil
	}
	d, ok := r.cache.digestFor(src, fi)
	if !ok {
		if d, err = HashFile(tmp); err != nil {
			return Digest{}, "", false, err
		}
		r.cache.putFor(src, fi, d)
		r.checkpoint()
	}
	obj = filepath.Join(r.root, storeDir, d.SHA256)
	if intact(obj, d) {
		return d, obj, true, nil
	}
	if err := os.Rename(tmp, obj); err != nil {
		return Digest{}, "", false, err
	}
	return d, obj, true, nil
}

// intact reports whether store object obj exists with d's size. A power cut
// can leave one truncated; a damaged object must be replaced, not reused.
func intact(obj string, d Digest) bool {
	fi, err := os.Stat(obj)
	return err == nil && fi.Size() == d.Size
}

// warnCopy reports, once per publish, that packages are being copied into
// the store, which doubles the disk they use.
func (r *Release) warnCopy(err error) {
	if r.copyWarned || r.Logf == nil {
		return
	}
	r.copyWarned = true
	r.Logf("warning: copying packages into the store, not hardlinking (%v); see README, Disk use", err)
}

// PreviousPackages counts the files named with suffixes under rel in the
// release that was current when this one began. It looks at the release
// itself, so it works for releases from any version of crossdock.
func (r *Release) PreviousPackages(rel string, suffixes ...string) (int, error) {
	dir, ok := r.Previous(rel)
	if !ok {
		return 0, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, nil
	}
	files, err := FindFiles(dir, suffixes...)
	return len(files), err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Abort removes an uncommitted release.
func (r *Release) Abort() error {
	if r.done {
		return nil
	}
	r.done = true
	// Keep what this attempt read: a retry needn't repeat it.
	_ = r.cache.saveProgress(r.root)
	return os.RemoveAll(r.Dir)
}

// Commit records the release's store manifest and the metadata cache,
// then atomically points <root>/current at this release. Where symlinks
// can't be created, it atomically replaces <root>/current.id instead;
// crossdock serve follows either, but static web servers need the symlink.
func (r *Release) Commit() error {
	if r.done {
		return errors.New("release already finished")
	}
	if err := r.writeManifest(); err != nil {
		return err
	}
	if err := r.cache.save(r.root); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(r.Dir, metaDir, inProgressName)); err != nil {
		return err
	}
	tmp := filepath.Join(r.root, "."+currentLink+".tmp")
	_ = os.Remove(tmp)
	if err := r.symlink(filepath.Join(releasesDir, r.ID), tmp); err == nil {
		if err := os.Rename(tmp, filepath.Join(r.root, currentLink)); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		_ = os.Remove(filepath.Join(r.root, currentFile))
	} else if err := r.commitID(); err != nil {
		return err
	}
	r.done = true
	return nil
}

// commitID points current.id at this release, then removes any current
// symlink left by an earlier publish, which would otherwise win over
// current.id. If that removal fails, current.id is put back, so it never
// names a release the caller is about to abort, and the symlink keeps
// serving the release that was live.
func (r *Release) commitID() error {
	idPath, link := filepath.Join(r.root, currentFile), filepath.Join(r.root, currentLink)
	old, readErr := os.ReadFile(idPath)
	if err := writeAtomic(idPath, []byte(r.ID+"\n")); err != nil {
		return err
	}
	rollback := func(err error) error {
		if readErr == nil {
			_ = writeAtomic(idPath, old)
		} else {
			_ = os.Remove(idPath)
		}
		return fmt.Errorf("removing the old %s symlink: %w", currentLink, err)
	}
	fi, err := lstat(link)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		// It may still be a symlink that would win over current.id.
		return rollback(err)
	case fi.Mode()&os.ModeSymlink == 0:
		return nil
	}
	if err := retry(func() error { return removeLink(link) }); err != nil && !os.IsNotExist(err) {
		return rollback(err)
	}
	return nil
}

// retry runs op until it succeeds, fails with a non-transient error, or
// about two seconds of transient failures have passed (see transient).
func retry(op func() error) error {
	var err error
	for i := 0; i < 20; i++ {
		if err = op(); err == nil || !transient(err) {
			return err
		}
		time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
	}
	return err
}

// writeAtomic replaces path with data via a temp file and rename.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return retry(func() error { return os.Rename(tmp.Name(), path) })
}

func (r *Release) writeManifest() error {
	ids := make([]string, 0, len(r.objects))
	for id := range r.objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	dir := filepath.Join(r.Dir, metaDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), []byte(strings.Join(ids, "\n")+"\n"), 0o644)
}

// CurrentDir returns the current release's directory, or "".
func CurrentDir(root string) string {
	if id := Current(root); id != "" {
		return filepath.Join(root, releasesDir, id)
	}
	return ""
}

// Current returns the ID of the current release, or "".
func Current(root string) string {
	if t, err := os.Readlink(filepath.Join(root, currentLink)); err == nil {
		return filepath.Base(t)
	}
	if b, err := os.ReadFile(filepath.Join(root, currentFile)); err == nil {
		id := strings.TrimSpace(string(b))
		if id != "" && id == filepath.Base(id) && !strings.HasPrefix(id, ".") {
			return id
		}
	}
	return ""
}

// Prune removes all but the newest keep releases (never the current one),
// then deletes store objects no remaining release links to. It returns the
// removed release IDs.
func Prune(root string, keep int) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, releasesDir))
	if err != nil {
		return nil, err
	}
	cur := Current(root)
	var ids, removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, releasesDir, e.Name())
		// A killed publish never went live: it doesn't count as a release,
		// so it can't push a good one out.
		if _, err := os.Stat(filepath.Join(dir, metaDir, inProgressName)); err == nil && e.Name() != cur {
			if err := os.RemoveAll(dir); err != nil {
				return removed, err
			}
			removed = append(removed, e.Name())
			continue
		}
		ids = append(ids, e.Name())
	}
	// IDs start with a UTC timestamp, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	kept := 0
	for _, id := range ids {
		if id == cur || kept < keep {
			kept++
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, releasesDir, id)); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}
	return removed, gcStore(root)
}

// gcStore deletes store objects that no remaining release's manifest
// lists. Releases from before manifests existed keep everything that's
// still hardlinked somewhere. Deleting an object never breaks a release:
// releases hold their own hardlinks or copies, so the store only saves
// space and copying.
func gcStore(root string) error {
	releases, err := os.ReadDir(filepath.Join(root, releasesDir))
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	legacy := false
	for _, rel := range releases {
		if !rel.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, releasesDir, rel.Name(), metaDir, manifestName))
		if os.IsNotExist(err) {
			legacy = true
			continue
		}
		if err != nil {
			return err
		}
		for _, id := range strings.Fields(string(b)) {
			keep[id] = true
		}
	}
	dir := filepath.Join(root, storeDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), incomingPrefix) {
			// Left by a publish that was killed.
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || keep[e.Name()] {
			continue
		}
		if legacy {
			if n, ok := linkCount(p); !ok || n > 1 {
				continue
			}
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}
