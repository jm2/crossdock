package deb

import (
	"bytes"
	"testing"
)

func TestParagraphRoundTrip(t *testing.T) {
	in := "Package: hello\nVersion: 1:2.0-1\nArchitecture: amd64\nDescription: greeter\n Longer text.\n .\n More.\n"
	p, err := ParseParagraph([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Get("version"); got != "1:2.0-1" {
		t.Errorf("Version = %q", got)
	}
	var b bytes.Buffer
	p.Write(&b)
	if b.String() != in {
		t.Errorf("round trip:\n got %q\nwant %q", b.String(), in)
	}
}

func TestParagraphErrors(t *testing.T) {
	for _, in := range []string{"", " leading continuation\n", "no colon here\n", "A: 1\n\nB: 2\n"} {
		if _, err := ParseParagraph([]byte(in)); err == nil {
			t.Errorf("ParseParagraph(%q) succeeded, want error", in)
		}
	}
}

func TestPoolPath(t *testing.T) {
	for _, tc := range []struct{ name, src, ver, arch, want string }{
		{"hello", "hello", "1:2.0-1", "amd64", "pool/main/h/hello/hello_2.0-1_amd64.deb"},
		{"libfoo1", "libfoo", "1.0", "arm64", "pool/main/libf/libfoo/libfoo1_1.0_arm64.deb"},
	} {
		p := &Package{Name: tc.name, Source: tc.src, Version: tc.ver, Arch: tc.arch}
		if got := p.PoolPath("main"); got != tc.want {
			t.Errorf("PoolPath = %q, want %q", got, tc.want)
		}
	}
}
