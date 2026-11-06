// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

//go:build unix

package publish

import (
	"testing"
	"time"
)

func TestLockWaits(t *testing.T) {
	root := t.TempDir()
	if err := Lock(root, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 1)
	go Lock(root, func(string, ...any) { waiting <- struct{}{} }) // never gets it
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("a second publish didn't wait for the first")
	}
}
