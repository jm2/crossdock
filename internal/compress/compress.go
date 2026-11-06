// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package compress opens compressed streams by sniffing magic bytes.
//
// Every format is decoded in pure Go, so crossdock needs no xz or zstd
// binaries on the host.
package compress

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
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
		r, err := xz.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("xz: %w", err)
		}
		return io.NopCloser(r), nil
	case Zstd:
		d, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		return zstdCloser{d}, nil
	default:
		return nil, fmt.Errorf("unsupported compression %s", k)
	}
}

// zstdCloser adapts *zstd.Decoder, whose Close returns nothing.
type zstdCloser struct{ *zstd.Decoder }

func (z zstdCloser) Close() error {
	z.Decoder.Close()
	return nil
}
