// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package publish

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jm2/crossdock/internal/sign"
)

// Env is what a format backend gets to build one repo.
type Env struct {
	Release *Release
	// Signer is nil for unsigned publishes.
	Signer sign.Signer
	// Now is the publish timestamp (honours SOURCE_DATE_EPOCH).
	Now  time.Time
	Logf func(format string, args ...any)
}

// FindFiles returns files, and symlinks to files, under dir (recursively)
// whose names end with any of the given suffixes, sorted.
func FindFiles(dir string, suffixes ...string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		for _, s := range suffixes {
			if !strings.HasSuffix(d.Name(), s) {
				continue
			}
			// Symlinked packages count; a dangling link is an error, not
			// a package that silently disappears from the repo.
			if d.Type()&fs.ModeSymlink != 0 {
				fi, err := os.Stat(p)
				if err != nil {
					return err
				}
				if fi.Mode().IsRegular() {
					out = append(out, p)
				}
			} else if d.Type().IsRegular() {
				out = append(out, p)
			}
			break
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Gzip compresses deterministically (no name or mtime in the header).
func Gzip(data []byte) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	w.Write(data)
	w.Close()
	return b.Bytes()
}
