// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"strings"
	"unicode/utf8"
)

// CompareVersions orders two versions the way Munki's MunkiVersion does
// (utils/versionutils.swift): both are padded with ".0" parts to the same
// number of dot-separated parts, then compared with NSString's numeric
// compare, where runs of digits compare as numbers. Empty parts are
// dropped, so "1..2" is "1.2". It returns -1, 0 or +1.
func CompareVersions(a, b string) int {
	n := max(len(versionParts(a)), len(versionParts(b)))
	return numericCompare(padVersion(a, n), padVersion(b, n))
}

func versionParts(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' })
}

func padVersion(v string, n int) string {
	parts := versionParts(v)
	for len(parts) < n {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}

// numericCompare is NSString compare(_:options: .numeric) for the
// characters versions use: digit runs compare by value (so leading zeros
// don't count), everything else by code point.
func numericCompare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			da, ra := digitRun(a)
			db, rb := digitRun(b)
			da, db = strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if len(da) != len(db) {
				return sign(len(da) - len(db))
			}
			if c := strings.Compare(da, db); c != 0 {
				return c
			}
			a, b = ra, rb
			continue
		}
		ra, sa := firstRune(a)
		rb, sb := firstRune(b)
		if ra != rb {
			return sign(int(ra) - int(rb))
		}
		a, b = a[sa:], b[sb:]
	}
	return sign(len(a) - len(b))
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func digitRun(s string) (string, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

func firstRune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
