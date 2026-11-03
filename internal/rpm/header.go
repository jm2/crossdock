// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

// Package rpm reads RPM package headers and writes rpm-md repositories
// (repodata/) in the format createrepo_c produces.
package rpm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Header data types.
const (
	typeNull        = 0
	typeChar        = 1
	typeInt8        = 2
	typeInt16       = 3
	typeInt32       = 4
	typeInt64       = 5
	typeString      = 6
	typeBin         = 7
	typeStringArray = 8
	typeI18NString  = 9
)

var headerMagic = []byte{0x8e, 0xad, 0xe8, 0x01}

const (
	maxIndexEntries = 1 << 20
	maxHeaderData   = 256 << 20
)

type indexEntry struct {
	typ    uint32
	offset uint32
	count  uint32
}

// header is a parsed RPM header structure (signature or main header).
type header struct {
	entries map[uint32]indexEntry
	data    []byte
	size    int64 // bytes on disk, excluding alignment padding
}

// readHeader reads one header structure from r.
func readHeader(r io.Reader) (*header, error) {
	var intro [16]byte
	if _, err := io.ReadFull(r, intro[:]); err != nil {
		return nil, fmt.Errorf("reading header intro: %w", err)
	}
	if !bytes.Equal(intro[:4], headerMagic) {
		return nil, errors.New("bad header magic")
	}
	nindex := binary.BigEndian.Uint32(intro[8:12])
	hsize := binary.BigEndian.Uint32(intro[12:16])
	if nindex > maxIndexEntries || hsize > maxHeaderData {
		return nil, fmt.Errorf("header too large (%d entries, %d bytes)", nindex, hsize)
	}
	idx := make([]byte, int(nindex)*16)
	if _, err := io.ReadFull(r, idx); err != nil {
		return nil, fmt.Errorf("reading header index: %w", err)
	}
	h := &header{entries: make(map[uint32]indexEntry, nindex), data: make([]byte, hsize)}
	if _, err := io.ReadFull(r, h.data); err != nil {
		return nil, fmt.Errorf("reading header data: %w", err)
	}
	for i := 0; i < int(nindex); i++ {
		e := idx[i*16:]
		tag := binary.BigEndian.Uint32(e[0:4])
		h.entries[tag] = indexEntry{
			typ:    binary.BigEndian.Uint32(e[4:8]),
			offset: binary.BigEndian.Uint32(e[8:12]),
			count:  binary.BigEndian.Uint32(e[12:16]),
		}
	}
	h.size = 16 + int64(nindex)*16 + int64(hsize)
	return h, nil
}

func (h *header) has(tag uint32) bool {
	_, ok := h.entries[tag]
	return ok
}

// strings returns a string or string-array tag's values.
func (h *header) strings(tag uint32) []string {
	e, ok := h.entries[tag]
	if !ok {
		return nil
	}
	switch e.typ {
	case typeString, typeStringArray, typeI18NString:
	default:
		return nil
	}
	n := int(e.count)
	if e.typ == typeString {
		n = 1
	}
	if int(e.offset) > len(h.data) {
		return nil
	}
	d := h.data[e.offset:]
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		j := bytes.IndexByte(d, 0)
		if j < 0 {
			break
		}
		out = append(out, string(d[:j]))
		d = d[j+1:]
	}
	return out
}

// str returns the first value of a string tag, or "".
func (h *header) str(tag uint32) string {
	if s := h.strings(tag); len(s) > 0 {
		return s[0]
	}
	return ""
}

// ints returns an integer tag's values.
func (h *header) ints(tag uint32) []uint64 {
	e, ok := h.entries[tag]
	if !ok {
		return nil
	}
	var size int
	switch e.typ {
	case typeChar, typeInt8:
		size = 1
	case typeInt16:
		size = 2
	case typeInt32:
		size = 4
	case typeInt64:
		size = 8
	default:
		return nil
	}
	start, n := int(e.offset), int(e.count)
	if start < 0 || n < 0 || start+n*size > len(h.data) || start+n*size < start {
		return nil
	}
	out := make([]uint64, n)
	for i := range out {
		b := h.data[start+i*size:]
		switch size {
		case 1:
			out[i] = uint64(b[0])
		case 2:
			out[i] = uint64(binary.BigEndian.Uint16(b))
		case 4:
			out[i] = uint64(binary.BigEndian.Uint32(b))
		case 8:
			out[i] = binary.BigEndian.Uint64(b)
		}
	}
	return out
}

// int returns the first value of an integer tag.
func (h *header) int(tag uint32) (uint64, bool) {
	if v := h.ints(tag); len(v) > 0 {
		return v[0], true
	}
	return 0, false
}
