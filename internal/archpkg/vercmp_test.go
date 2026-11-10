// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package archpkg

import (
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// Cases from pacman's test/util/vercmptest.sh.
var vercmpCases = []struct {
	a, b string
	want int
}{
	{"1.5.0", "1.5.0", 0},
	{"1.5.1", "1.5.0", 1},
	{"1.5.1", "1.5", 1},
	{"1.5.0-1", "1.5.0-1", 0},
	{"1.5.0-1", "1.5.0-2", -1},
	{"1.5.0-1", "1.5.1-1", -1},
	{"1.5.0-2", "1.5.1-1", -1},
	{"1.5-1", "1.5", 0},
	{"1.1-1", "1.1", 0},
	{"1.0-1", "1.1", -1},
	{"1.1-1", "1.0", 1},
	{"1.5b-1", "1.5-1", -1},
	{"1.5b", "1.5", -1},
	{"1.5b-1", "1.5", -1},
	{"1.5b", "1.5.1", -1},
	{"1.0a", "1.0alpha", -1},
	{"1.0alpha", "1.0b", -1},
	{"1.0b", "1.0beta", -1},
	{"1.0beta", "1.0rc", -1},
	{"1.0rc", "1.0", -1},
	{"1.5.a", "1.5", 1},
	{"1.5.b", "1.5.a", 1},
	{"1.5.1", "1.5.b", 1},
	{"1.5.b-1", "1.5.b", 0},
	{"1.5-1", "1.5.b", -1},
	{"2.0", "2_0", 0},
	{"2.0_a", "2_0.a", 0},
	{"2.0a", "2.0.a", -1},
	{"2___a", "2_a", 1},
	{"0:1.0", "0:1.0", 0},
	{"0:1.0", "0:1.1", -1},
	{"1:1.0", "0:1.0", 1},
	{"1:1.0", "0:1.1", 1},
	{"1:1.0", "2:1.1", -1},
	{"0:1.0", "1.0", 0},
	{"0:1.0", "1.1", -1},
	{"0:1.1", "1.0", 1},
	{"1:1.0", "1.0", 1},
	{"1:1.0", "1.1", 1},
	{"1:1.1", "1.1", 1},
	{"1.2.3-9", "1.2.3-10", -1},
}

func TestVercmp(t *testing.T) {
	for _, c := range vercmpCases {
		if got := Vercmp(c.a, c.b); got != c.want {
			t.Errorf("Vercmp(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Vercmp(c.b, c.a); got != -c.want {
			t.Errorf("Vercmp(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

// TestVercmpMatchesPacman compares random versions against pacman's own
// vercmp tool when it is installed.
func TestVercmpMatchesPacman(t *testing.T) {
	bin, err := exec.LookPath("vercmp")
	if err != nil {
		t.Skip("pacman's vercmp is not installed")
	}
	r := rand.New(rand.NewSource(1))
	parts := []string{"0", "1", "2", "10", "01", "a", "b", "rc", "alpha", "pre", ".", "..", "_", "+", "-", ":", "~"}
	gen := func() string {
		var b strings.Builder
		if r.Intn(4) == 0 {
			b.WriteString(strconv.Itoa(r.Intn(3)) + ":")
		}
		for n := 1 + r.Intn(6); n > 0; n-- {
			b.WriteString(parts[r.Intn(len(parts))])
		}
		return b.String()
	}
	n := 5000
	if testing.Short() {
		n = 50
	}
	for i := 0; i < n; i++ {
		a, b := gen(), gen()
		out, err := exec.Command(bin, a, b).Output()
		if err != nil {
			t.Fatalf("vercmp %q %q: %v", a, b, err)
		}
		want, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if got := Vercmp(a, b); got != want {
			t.Errorf("Vercmp(%q, %q) = %d, pacman says %d", a, b, got, want)
		}
	}
}
