// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlistRoundTrip(t *testing.T) {
	in := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<!-- comments are skipped -->
<dict>
  <key>name</key> <string>foo</string>
  <key>Zcap</key><string>upper sorts first</string>
  <key>esc</key><string>a &amp; b &lt;c&gt; "q" 'a' é <![CDATA[<raw>]]></string>
  <key>empty</key><string></string>
  <key>n</key><integer> -5 </integer>
  <key>r</key><real>0.5</real>
  <key>r2</key><real>2</real>
  <key>t</key><true/>
  <key>f</key><false/>
  <key>d</key><date>2026-11-25T10:00:00Z</date>
  <key>data</key><data>
    SGVsbG8=
  </data>
  <key>arr</key><array><string>x</string><integer>1</integer><array/><dict/></array>
</dict>
</plist>
`
	v, err := DecodePlist([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "foo", "Zcap": "upper sorts first", "esc": `a & b <c> "q" 'a' é <raw>`, "empty": "",
		"n": int64(-5), "r": 0.5, "r2": 2.0, "t": true, "f": false,
		"d":    time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
		"data": []byte("Hello"),
		"arr":  []any{"x", int64(1), []any{}, map[string]any{}},
	}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("decoded\n%#v\nwant\n%#v", v, want)
	}
	out, err := EncodePlist(v)
	if err != nil {
		t.Fatal(err)
	}
	// The layout CoreFoundation writes: tabs, sorted keys, empty
	// containers self-closed, only & < > escaped, %.17g reals.
	wantOut := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Zcap</key>
	<string>upper sorts first</string>
	<key>arr</key>
	<array>
		<string>x</string>
		<integer>1</integer>
		<array/>
		<dict/>
	</array>
	<key>d</key>
	<date>2026-11-25T10:00:00Z</date>
	<key>data</key>
	<data>
	SGVsbG8=
	</data>
	<key>empty</key>
	<string></string>
	<key>esc</key>
	<string>a &amp; b &lt;c&gt; "q" 'a' é &lt;raw&gt;</string>
	<key>f</key>
	<false/>
	<key>n</key>
	<integer>-5</integer>
	<key>name</key>
	<string>foo</string>
	<key>r</key>
	<real>0.5</real>
	<key>r2</key>
	<real>2</real>
	<key>t</key>
	<true/>
</dict>
</plist>
`
	if string(out) != wantOut {
		t.Errorf("encoded\n%s\nwant\n%s", out, wantOut)
	}
	again, err := DecodePlist(out)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("re-decoded %#v, %v", again, err)
	}
}

func TestPlistDataWrapping(t *testing.T) {
	// CoreFoundation wraps base64 at 76 columns less 8 per tab.
	b := []byte(strings.Repeat("x", 100))
	out, err := EncodePlist(map[string]any{"d": b})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "\teHh4") && len(strings.TrimLeft(l, "\t")) > 68 {
			t.Errorf("line %q longer than 68 columns", l)
		}
	}
	v, err := DecodePlist(out)
	if err != nil || !reflect.DeepEqual(v, map[string]any{"d": b}) {
		t.Errorf("round trip: %v, %v", v, err)
	}
	if got := formatReal(0.1); got != "0.10000000000000001" {
		t.Errorf("formatReal(0.1) = %s", got)
	}
}

func TestPlistErrors(t *testing.T) {
	wrap := func(s string) string { return `<?xml version="1.0"?><plist version="1.0">` + s + `</plist>` }
	for _, c := range []struct{ in, want string }{
		{"bplist00\x00\x01", "binary property list"},
		{`<?xml version="1.0"?><dict/>`, "want one <plist>"},
		{`<?xml version="1.0"?>`, "no <plist>"},
		{wrap(``), "empty <plist>"},
		{wrap(`<string>a</string><string>b</string>`), "more than one value"},
		{wrap(`<dict><key>a</key><string/><key>a</key><string/></dict>`), `duplicate key "a"`},
		{wrap(`<dict><key>a</key></dict>`), `key "a" has no value`},
		{wrap(`<dict><string>x</string></dict>`), "where a <key> belongs"},
		{wrap(`<integer>1.5</integer>`), "integer"},
		{wrap(`<integer>99999999999999999999</integer>`), "integer"},
		{wrap(`<date>yesterday</date>`), "date"},
		{wrap(`<data>!!</data>`), "data"},
		{wrap(`<true>x</true>`), "must be empty"},
		{wrap(`<string><b/></string>`), "<b> inside <string>"},
		{wrap(`<set/>`), "unknown element <set>"},
		{wrap(`<string>&nbsp;</string>`), "entity"},
		{wrap(`<dict>`), "closed by </plist>"},
		{`<?xml version="1.0"?><plist><dict>`, "EOF"},
		{wrap(`<array>text</array>`), "unexpected text"},
		{wrap(strings.Repeat("<array>", 70) + strings.Repeat("</array>", 70)), "nested too deeply"},
		{wrap(`<string>x</string>`) + "trailing", "text outside"},
	} {
		_, err := DecodePlist([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.60q: error %v, want %q", c.in, err, c.want)
		}
	}
	for _, v := range []any{"bell\x07", "\xff", map[string]any{"k": struct{}{}}} {
		if _, err := EncodePlist(v); err == nil {
			t.Errorf("EncodePlist(%#v) succeeded", v)
		}
	}
}
