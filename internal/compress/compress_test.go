// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package compress

import (
	"bytes"
	"compress/gzip"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func payload() []byte {
	return bytes.Repeat([]byte("Package: crossdock\nVersion: 1.0\n\n"), 4096)
}

func decode(t *testing.T, b []byte) []byte {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	want := payload()
	enc := map[Kind]func(io.Writer) io.WriteCloser{
		Gzip: func(w io.Writer) io.WriteCloser { return gzip.NewWriter(w) },
		XZ: func(w io.Writer) io.WriteCloser {
			x, _ := xz.NewWriter(w)
			return x
		},
		Zstd: func(w io.Writer) io.WriteCloser {
			z, _ := zstd.NewWriter(w)
			return z
		},
	}
	for kind, f := range enc {
		var buf bytes.Buffer
		w := f(&buf)
		w.Write(want)
		w.Close()
		if got := Detect(buf.Bytes()); got != kind {
			t.Errorf("Detect = %s, want %s", got, kind)
		}
		if got := decode(t, buf.Bytes()); !bytes.Equal(got, want) {
			t.Errorf("%s: round trip mismatch (%d bytes)", kind, len(got))
		}
	}
	if got := decode(t, []byte("plain")); string(got) != "plain" {
		t.Errorf("uncompressed: %q", got)
	}
}

// Packages come from the real tools: makepkg uses zstd with long windows
// and dpkg-deb/xz use multi-threaded xz. Check those streams decode.
func TestCLIStreams(t *testing.T) {
	want := payload()
	for _, c := range [][]string{
		{"xz", "-c", "-T2", "-9e"},
		{"zstd", "-c", "-q", "-19", "--long=27", "-T0"},
		{"zstd", "-c", "-q", "--ultra", "-20"},
	} {
		if _, err := exec.LookPath(c[0]); err != nil {
			t.Logf("skipping %s: not installed", c[0])
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = bytes.NewReader(want)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if got := decode(t, out); !bytes.Equal(got, want) {
			t.Errorf("%v: mismatch", c)
		}
	}
}

func TestCorrupt(t *testing.T) {
	var xb, zb bytes.Buffer
	x, _ := xz.NewWriter(&xb)
	x.Write(payload())
	x.Close()
	z, _ := zstd.NewWriter(&zb)
	z.Write(payload())
	z.Close()
	for name, b := range map[string][]byte{"xz": xb.Bytes(), "zstd": zb.Bytes()} {
		bad := append([]byte(nil), b...)
		bad = bad[:len(bad)/2] // truncated
		r, err := NewReader(bytes.NewReader(bad))
		if err == nil {
			_, err = io.ReadAll(r)
			if cerr := r.Close(); err == nil {
				err = cerr
			}
		}
		if err == nil {
			t.Errorf("%s: truncated stream decoded without error", name)
		} else if strings.Contains(err.Error(), "binary") {
			t.Errorf("%s: still shelling out: %v", name, err)
		}
	}
}
