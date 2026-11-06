// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

//go:build !unix

package publish

// Lock is a no-op here: run one publish at a time.
func Lock(string, func(string, ...any)) error { return nil }
