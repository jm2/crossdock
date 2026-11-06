// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

//go:build unix

package publish

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Lock waits until this is the only publish running for root, and holds
// that until the process exits, whichever way it exits. Pruning removes
// what a killed publish left behind, which would also remove the work of
// one still running.
func Lock(root string, logf func(string, ...any)) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	// f stays open on purpose: closing it would drop the lock.
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		logf("waiting for another publish of %s to finish", root)
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	return err
}
