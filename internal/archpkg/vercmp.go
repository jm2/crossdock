// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package archpkg

import "strings"

// Vercmp compares two full pacman versions ([epoch:]pkgver[-pkgrel]) the
// way libalpm's alpm_pkg_vercmp does, returning -1, 0 or 1. It differs from
// rpm's ordering: a trailing letter segment is older than none (1.0a < 1.0),
// and a missing epoch is 0.
func Vercmp(a, b string) int {
	if a == b {
		return 0
	}
	e1, v1, r1, ok1 := parseEVR(a)
	e2, v2, r2, ok2 := parseEVR(b)
	if c := segcmp(e1, e2); c != 0 {
		return c
	}
	if c := segcmp(v1, v2); c != 0 {
		return c
	}
	// A missing pkgrel (no "-" at all) matches any pkgrel.
	if ok1 && ok2 {
		return segcmp(r1, r2)
	}
	return 0
}

// parseEVR splits [epoch:]version[-release] like libalpm's parseEVR;
// hasRelease reports whether there was a "-", even with nothing after it.
func parseEVR(evr string) (epoch, version, release string, hasRelease bool) {
	s := 0
	for s < len(evr) && isDigit(evr[s]) {
		s++
	}
	rest := evr[s:]
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		release, hasRelease = rest[i+1:], true
		evr = evr[:s+i]
	}
	epoch, version = "0", evr
	if s < len(evr) && evr[s] == ':' {
		epoch, version = evr[:s], evr[s+1:]
		if epoch == "" {
			epoch = "0"
		}
	}
	return epoch, version, release, hasRelease
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }

// segcmp is libalpm's rpmvercmp: alternating digit and letter segments,
// split on other characters, where a longer separator run is newer.
func segcmp(a, b string) int {
	if a == b {
		return 0
	}
	one, two := 0, 0   // start of the current segment
	end1, end2 := 0, 0 // end of the previous segment
	for one < len(a) && two < len(b) {
		for one < len(a) && !isAlnum(a[one]) {
			one++
		}
		for two < len(b) && !isAlnum(b[two]) {
			two++
		}
		if one >= len(a) || two >= len(b) {
			break
		}
		if sep1, sep2 := one-end1, two-end2; sep1 != sep2 {
			if sep1 < sep2 {
				return -1
			}
			return 1
		}
		end1, end2 = one, two
		numeric := isDigit(a[one])
		if numeric {
			for end1 < len(a) && isDigit(a[end1]) {
				end1++
			}
			for end2 < len(b) && isDigit(b[end2]) {
				end2++
			}
		} else {
			for end1 < len(a) && isAlpha(a[end1]) {
				end1++
			}
			for end2 < len(b) && isAlpha(b[end2]) {
				end2++
			}
		}
		x, y := a[one:end1], b[two:end2]
		if y == "" {
			// Different segment types: a number is newer than letters.
			if numeric {
				return 1
			}
			return -1
		}
		if numeric {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				if len(x) > len(y) {
					return 1
				}
				return -1
			}
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
		one, two = end1, end2
	}
	restA, restB := a[one:], b[two:]
	if restA == "" && restB == "" {
		return 0
	}
	// A remaining letter segment never beats an empty string.
	if restA == "" && (restB == "" || !isAlpha(restB[0])) || restA != "" && isAlpha(restA[0]) {
		return -1
	}
	return 1
}
