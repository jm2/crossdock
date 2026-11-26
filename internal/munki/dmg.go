// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Trailer is the parts of a UDIF disk image's 512-byte "koly" trailer that
// crossdock checks. hdiutil writes one at the end of every .dmg it makes,
// whatever the format (UDRO, UDZO, ULFO, ...).
type Trailer struct {
	Version        uint32 `json:"version"`
	Flags          uint32 `json:"flags"`
	DataForkOffset uint64 `json:"data_fork_offset"`
	DataForkLength uint64 `json:"data_fork_length"`
	SegmentNumber  uint32 `json:"segment_number"`
	SegmentCount   uint32 `json:"segment_count"`
	XMLOffset      uint64 `json:"xml_offset"`
	XMLLength      uint64 `json:"xml_length"`
	ImageVariant   uint32 `json:"image_variant"`
	SectorCount    uint64 `json:"sector_count"`
}

const trailerSize = 512

// ReadTrailer reads and checks a .dmg's UDIF trailer. It reads a few
// hundred bytes whatever the image's size, and catches truncated uploads
// (the trailer is the last thing written) and files that aren't disk
// images.
func ReadTrailer(p string) (*Trailer, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size < trailerSize {
		return nil, fmt.Errorf("%s: %d bytes is too small for a disk image", p, size)
	}
	var k [trailerSize]byte
	if _, err := f.ReadAt(k[:], size-trailerSize); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	t, err := parseTrailer(k[:], uint64(size))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	// The resource plist (the image's block map) is XML.
	var head [5]byte
	if _, err := f.ReadAt(head[:], int64(t.XMLOffset)); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if !bytes.Equal(head[:], []byte("<?xml")) {
		return nil, fmt.Errorf("%s: no XML property list where the disk image trailer points (truncated or corrupt?)", p)
	}
	return t, nil
}

func parseTrailer(k []byte, size uint64) (*Trailer, error) {
	be := binary.BigEndian
	if string(k[:4]) != "koly" {
		return nil, errors.New("not a UDIF disk image (no koly trailer); truncated, encrypted, or not made by hdiutil?")
	}
	t := &Trailer{
		Version:        be.Uint32(k[4:]),
		Flags:          be.Uint32(k[12:]),
		DataForkOffset: be.Uint64(k[24:]),
		DataForkLength: be.Uint64(k[32:]),
		SegmentNumber:  be.Uint32(k[56:]),
		SegmentCount:   be.Uint32(k[60:]),
		XMLOffset:      be.Uint64(k[216:]),
		XMLLength:      be.Uint64(k[224:]),
		ImageVariant:   be.Uint32(k[488:]),
		SectorCount:    be.Uint64(k[492:]),
	}
	if hs := be.Uint32(k[8:]); t.Version != 4 || hs != trailerSize {
		return nil, fmt.Errorf("unsupported UDIF trailer (version %d, header size %d)", t.Version, hs)
	}
	if t.SegmentCount > 1 {
		return nil, fmt.Errorf("segmented disk image (%d segments); Munki needs a single file", t.SegmentCount)
	}
	body := size - trailerSize
	inside := func(off, n uint64) bool { return off <= body && n <= body-off }
	if !inside(t.DataForkOffset, t.DataForkLength) {
		return nil, fmt.Errorf("data fork (offset %d, length %d) runs past the end of the file: truncated?", t.DataForkOffset, t.DataForkLength)
	}
	if t.XMLLength == 0 || !inside(t.XMLOffset, t.XMLLength) {
		return nil, fmt.Errorf("resource plist (offset %d, length %d) isn't inside the file: truncated?", t.XMLOffset, t.XMLLength)
	}
	return t, nil
}
