// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

//go:build !unix

package publish

// linkCount is unavailable here. It only matters for releases published
// before store manifests existed, and those keep their store objects.
func linkCount(string) (uint64, bool) { return 0, false }
