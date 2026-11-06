// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

//go:build !windows

package publish

// transient is always false here: renames and removals don't fail because
// another process has the file open.
func transient(error) bool { return false }
