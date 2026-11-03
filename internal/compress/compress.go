// Package compress opens compressed streams by sniffing magic bytes.
//
// gzip and bzip2 are decoded natively. xz and zstd currently shell out to
// the xz / zstd binaries; M2 replaces those with pure-Go decoders
// (ulikunitz/xz, klauspost/compress/zstd) so crossdock has no host
// dependencies.
package compress

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Kind is a detected compression format.
type Kind string

const (
	None  Kind = "none"
	Gzip  Kind = "gzip"
	Bzip2 Kind = "bzip2"
	XZ    Kind = "xz"
	Zstd  Kind = "zstd"
)

var magics = []struct {
	kind  Kind
	magic []byte
}{
	{Gzip, []byte{0x1f, 0x8b}},
	{Bzip2, []byte("BZh")},
	{XZ, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
	{Zstd, []byte{0x28, 0xb5, 0x2f, 0xfd}},
}

// Detect returns the compression kind of a stream's leading bytes.
func Detect(head []byte) Kind {
	for _, m := range magics {
		if bytes.HasPrefix(head, m.magic) {
			return m.kind
		}
	}
	return None
}

// NewReader returns a decompressing reader for r. The caller must Close it.
func NewReader(r io.Reader) (io.ReadCloser, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(6)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return nil, err
	}
	switch k := Detect(head); k {
	case None:
		return io.NopCloser(br), nil
	case Gzip:
		return gzip.NewReader(br)
	case Bzip2:
		return io.NopCloser(bzip2.NewReader(br)), nil
	case XZ:
		return execReader(br, "xz", "-dc")
	case Zstd:
		return execReader(br, "zstd", "-dc", "-q")
	default:
		return nil, fmt.Errorf("unsupported compression %s", k)
	}
}

type cmdReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *strings.Builder
}

func (c *cmdReader) Close() error {
	// Drain so the child can exit even if the caller stopped early.
	_, _ = io.Copy(io.Discard, c.ReadCloser)
	_ = c.ReadCloser.Close()
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w: %s", c.cmd.Path, err, strings.TrimSpace(c.stderr.String()))
	}
	return nil
}

func execReader(r io.Reader, name string, args ...string) (io.ReadCloser, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s-compressed input needs the %q binary until native decoding lands (M2): %w", name, name, err)
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = r
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdReader{ReadCloser: out, cmd: cmd, stderr: &stderr}, nil
}
