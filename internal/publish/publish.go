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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	currentLink = "current"
	releasesDir = "releases"
	storeDir    = "store"
)

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
	return &Release{root: root, ID: id, Dir: dir}, nil
}

// Path returns the absolute path of rel inside the release, rejecting
// paths that escape it.
func (r *Release) Path(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
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

// Symlink creates a relative symlink at rel pointing to target (a sibling
// name or relative path).
func (r *Release) Symlink(target, rel string) error {
	p, err := r.Path(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, p)
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
	f, err := os.Open(src)
	if err != nil {
		return Digest{}, "", err
	}
	defer f.Close()
	tmp, err := os.CreateTemp(filepath.Join(r.root, storeDir), ".incoming-*")
	if err != nil {
		return Digest{}, "", err
	}
	defer os.Remove(tmp.Name())
	d, err := hashReader(f, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Digest{}, "", err
	}
	obj := filepath.Join(r.root, storeDir, d.SHA256)
	if _, err := os.Stat(obj); err == nil {
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
	return os.RemoveAll(r.Dir)
}

// Commit atomically points <root>/current at this release.
func (r *Release) Commit() error {
	if r.done {
		return errors.New("release already finished")
	}
	tmp := filepath.Join(r.root, "."+currentLink+".tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join(releasesDir, r.ID), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(r.root, currentLink)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	r.done = true
	return nil
}

// Current returns the ID of the release <root>/current points to, or "".
func Current(root string) string {
	t, err := os.Readlink(filepath.Join(root, currentLink))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// Prune removes all but the newest keep releases (never the current one),
// then deletes store objects no remaining release links to. It returns the
// removed release IDs.
func Prune(root string, keep int) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, releasesDir))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	// IDs start with a UTC timestamp, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	cur := Current(root)
	var removed []string
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

func gcStore(root string) error {
	dir := filepath.Join(root, storeDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), ".incoming-") {
			continue
		}
		n, ok := linkCount(p)
		if ok && n <= 1 {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}
