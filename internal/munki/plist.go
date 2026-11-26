// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The XML property list values this package reads and writes:
//
//	dict     map[string]any
//	array    []any
//	string   string
//	integer  int64
//	real     float64
//	true     bool
//	date     time.Time (UTC)
//	data     []byte
//
// Encode also takes int, []string and []map[string]any, for convenience.

// maxDepth bounds nesting, so a hostile file can't exhaust the stack.
const maxDepth = 64

// DecodePlist parses an XML property list. Binary plists aren't
// supported: makepkginfo and makecatalogs write XML.
func DecodePlist(data []byte) (any, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return nil, errors.New("binary property list; convert it with plutil -convert xml1")
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	d := xml.NewDecoder(bytes.NewReader(data))
	var v any
	seen := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if seen || t.Name.Local != "plist" {
				return nil, fmt.Errorf("unexpected <%s>: want one <plist>", t.Name.Local)
			}
			seen = true
			if v, err = decodePlistBody(d); err != nil {
				return nil, err
			}
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, errors.New("text outside <plist>")
			}
		}
	}
	if !seen {
		return nil, errors.New("no <plist> element")
	}
	return v, nil
}

// decodePlistBody reads the single value inside <plist> and its end tag.
func decodePlistBody(d *xml.Decoder) (any, error) {
	var v any
	have := false
	for {
		start, end, err := nextElement(d)
		if err != nil {
			return nil, err
		}
		if end {
			if !have {
				return nil, errors.New("empty <plist>")
			}
			return v, nil
		}
		if have {
			return nil, errors.New("more than one value in <plist>")
		}
		if v, err = decodeValue(d, start, 1); err != nil {
			return nil, err
		}
		have = true
	}
}

// nextElement skips whitespace and comments to the next start element, or
// reports the parent's end element.
func nextElement(d *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return xml.StartElement{}, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, false, nil
		case xml.EndElement:
			return xml.StartElement{}, true, nil
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return xml.StartElement{}, false, fmt.Errorf("unexpected text %q", strings.TrimSpace(string(t)))
			}
		}
	}
}

// text reads character data up to the element's end tag.
func text(d *xml.Decoder, name string) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.StartElement:
			return "", fmt.Errorf("<%s> inside <%s>", t.Name.Local, name)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

func decodeValue(d *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nested too deeply")
	}
	name := start.Name.Local
	switch name {
	case "dict":
		m := map[string]any{}
		for {
			k, end, err := nextElement(d)
			if err != nil {
				return nil, err
			}
			if end {
				return m, nil
			}
			if k.Name.Local != "key" {
				return nil, fmt.Errorf("<%s> in <dict> where a <key> belongs", k.Name.Local)
			}
			key, err := text(d, "key")
			if err != nil {
				return nil, err
			}
			if _, dup := m[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			vs, end, err := nextElement(d)
			if err != nil {
				return nil, err
			}
			if end {
				return nil, fmt.Errorf("key %q has no value", key)
			}
			v, err := decodeValue(d, vs, depth+1)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			m[key] = v
		}
	case "array":
		a := []any{}
		for {
			vs, end, err := nextElement(d)
			if err != nil {
				return nil, err
			}
			if end {
				return a, nil
			}
			v, err := decodeValue(d, vs, depth+1)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", len(a), err)
			}
			a = append(a, v)
		}
	case "true", "false":
		s, err := text(d, name)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) != "" {
			return nil, fmt.Errorf("<%s> must be empty", name)
		}
		return name == "true", nil
	}
	s, err := text(d, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "string":
		return s, nil
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer %q: %w", s, errors.Unwrap(err))
		}
		return n, nil
	case "real":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("real %q: %w", s, errors.Unwrap(err))
		}
		return f, nil
	case "date":
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("date %q: %w", s, err)
		}
		return t.UTC(), nil
	case "data":
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, fmt.Errorf("data: %w", err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("unknown element <%s>", name)
}

// EncodePlist writes v as an XML property list in the layout Apple's
// PropertyListSerialization writes, which makecatalogs uses: tabs, keys
// sorted, and the same header. Sorted keys make the output reproducible.
func EncodePlist(v any) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n" +
		`<plist version="1.0">` + "\n")
	if err := encodeValue(&b, v, 0); err != nil {
		return nil, err
	}
	b.WriteString("</plist>\n")
	return b.Bytes(), nil
}

func encodeValue(b *bytes.Buffer, v any, indent int) error {
	if indent > maxDepth {
		return errors.New("nested too deeply")
	}
	tabs := strings.Repeat("\t", indent)
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 0 {
			b.WriteString(tabs + "<dict/>\n")
			return nil
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteString(tabs + "<dict>\n")
		for _, k := range keys {
			ek, err := escape(k)
			if err != nil {
				return fmt.Errorf("key %q: %w", k, err)
			}
			b.WriteString(tabs + "\t<key>" + ek + "</key>\n")
			if err := encodeValue(b, v[k], indent+1); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		b.WriteString(tabs + "</dict>\n")
	case []any:
		if len(v) == 0 {
			b.WriteString(tabs + "<array/>\n")
			return nil
		}
		b.WriteString(tabs + "<array>\n")
		for i, e := range v {
			if err := encodeValue(b, e, indent+1); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		b.WriteString(tabs + "</array>\n")
	case []string:
		a := make([]any, len(v))
		for i, s := range v {
			a[i] = s
		}
		return encodeValue(b, a, indent)
	case []map[string]any:
		a := make([]any, len(v))
		for i, m := range v {
			a[i] = m
		}
		return encodeValue(b, a, indent)
	case string:
		s, err := escape(v)
		if err != nil {
			return err
		}
		b.WriteString(tabs + "<string>" + s + "</string>\n")
	case int64:
		b.WriteString(tabs + "<integer>" + strconv.FormatInt(v, 10) + "</integer>\n")
	case int:
		b.WriteString(tabs + "<integer>" + strconv.Itoa(v) + "</integer>\n")
	case float64:
		b.WriteString(tabs + "<real>" + formatReal(v) + "</real>\n")
	case bool:
		if v {
			b.WriteString(tabs + "<true/>\n")
		} else {
			b.WriteString(tabs + "<false/>\n")
		}
	case time.Time:
		b.WriteString(tabs + "<date>" + v.UTC().Format("2006-01-02T15:04:05Z") + "</date>\n")
	case []byte:
		b.WriteString(tabs + "<data>\n")
		// CoreFoundation wraps base64 at 76 columns, counting each tab of
		// indentation (at most 8) as 8.
		width := 76 - 8*min(indent, 8)
		enc := base64.StdEncoding.EncodeToString(v)
		for len(enc) > 0 {
			n := min(width, len(enc))
			b.WriteString(tabs + enc[:n] + "\n")
			enc = enc[n:]
		}
		b.WriteString(tabs + "</data>\n")
	default:
		return fmt.Errorf("can't encode %T in a property list", v)
	}
	return nil
}

// formatReal formats like CoreFoundation: %.17g, with names for the
// special values.
func formatReal(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "+infinity"
	case math.IsInf(f, -1):
		return "-infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	return strconv.FormatFloat(f, 'g', 17, 64)
}

// escape escapes text the way CoreFoundation does (&, < and >), plus
// carriage returns, which an XML parser would read back as newlines, and
// refuses what XML 1.0 can't carry.
func escape(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("not valid UTF-8")
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return "", fmt.Errorf("character %U can't be stored in XML", r)
		}
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;").Replace(s), nil
}
