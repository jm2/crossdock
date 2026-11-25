// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package munki

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jm2/crossdock/internal/config"
	"github.com/jm2/crossdock/internal/publish"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.0.0", 0}, // padded
		{"1", "1.0.0.0", 0},
		{"1..2", "1.2", 0}, // empty parts dropped
		{"1.01", "1.1", 0}, // digit runs compare by value
		{"1.9", "1.10", -1},
		{"2", "10", -1},
		{"10.0", "9.9.9", 1},
		{"1.0", "1.0.1", -1},
		{"1.0a", "1.0.1", 1}, // "1.0a.0" vs "1.0.1": 'a' > '.'
		{"1.0b1", "1.0b2", -1},
		{"1.27.16986367.2", "1.27.16986367.10", -1}, // the release bump
		{"1.27.16986367.9", "1.27.16986368.1", -1},  // a new Steam build
		{"1.27.16986367", "1.27.16986367.1", -1},
		{"B", "a", -1}, // case-sensitive, by code point
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

// fakeDMG writes a file with a valid UDIF trailer: a data fork holding
// payload, a resource plist, then the 512-byte koly block, as hdiutil
// lays an image out.
func fakeDMG(t *testing.T, p, payload string) []byte {
	t.Helper()
	res := []byte(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict/></plist>`)
	var b bytes.Buffer
	b.WriteString(payload)
	b.Write(res)
	k := make([]byte, trailerSize)
	copy(k, "koly")
	be := binary.BigEndian
	be.PutUint32(k[4:], 4)
	be.PutUint32(k[8:], trailerSize)
	be.PutUint64(k[32:], uint64(len(payload)))
	be.PutUint32(k[60:], 1)
	be.PutUint64(k[216:], uint64(len(payload)))
	be.PutUint64(k[224:], uint64(len(res)))
	be.PutUint64(k[492:], uint64(len(payload)/512))
	b.Write(k)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadTrailer(t *testing.T) {
	dir := t.TempDir()
	good := fakeDMG(t, filepath.Join(dir, "good.dmg"), strings.Repeat("d", 4096))
	tr, err := ReadTrailer(filepath.Join(dir, "good.dmg"))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Version != 4 || tr.DataForkLength != 4096 || tr.XMLOffset != 4096 || tr.SegmentCount != 1 || tr.SectorCount != 8 {
		t.Errorf("trailer %+v", tr)
	}
	set := func(off int, v uint64, n int) []byte {
		b := slices.Clone(good)
		k := b[len(b)-trailerSize:]
		if n == 4 {
			binary.BigEndian.PutUint32(k[off:], uint32(v))
		} else {
			binary.BigEndian.PutUint64(k[off:], v)
		}
		return b
	}
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"truncated":    {good[:len(good)-1], "no koly trailer"},
		"tiny":         {[]byte("koly"), "too small"},
		"not a dmg":    {bytes.Repeat([]byte{0}, 4096), "no koly trailer"},
		"version":      {set(4, 3, 4), "unsupported UDIF trailer"},
		"header size":  {set(8, 200, 4), "unsupported UDIF trailer"},
		"segmented":    {set(60, 3, 4), "segmented"},
		"data fork":    {set(32, 1<<40, 8), "data fork"},
		"overflow":     {set(24, ^uint64(0), 8), "data fork"},
		"xml past end": {set(216, uint64(len(good)), 8), "resource plist"},
		"no xml":       {set(224, 0, 8), "resource plist"},
		"xml moved":    {set(216, 10, 8), "no XML property list"},
	} {
		p := filepath.Join(dir, name+".dmg")
		os.WriteFile(p, c.data, 0o644)
		if _, err := ReadTrailer(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// pkginfo returns a sidecar for a copy_from_dmg item, with extra keys
// (raw plist XML) added.
func pkginfo(name, version, extra string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>name</key><string>%s</string>
	<key>version</key><string>%s</string>
	<key>installer_type</key><string>copy_from_dmg</string>
	<key>items_to_copy</key><array><dict>
		<key>source_item</key><string>%s</string>
		<key>destination_path</key><string>/Applications/Games</string>
	</dict></array>
	<key>installed_size</key><integer>2048</integer>
	<key>installs</key><array><dict>
		<key>type</key><string>plist</string>
		<key>path</key><string>/Applications/Games/%s/version.plist</string>
		<key>CFBundleShortVersionString</key><string>%s</string>
	</dict></array>
%s</dict>
</plist>
`, name, version, name, name, version, extra)
}

func TestParsePkginfo(t *testing.T) {
	p, err := ParsePkginfo([]byte(pkginfo("bo3", "1.27.16986367.2", "")))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "bo3" || p.Version != "1.27.16986367.2" || len(p.Warnings) != 0 {
		t.Errorf("%+v", p)
	}
	without := func(key string) string {
		s := pkginfo("x", "1", "")
		i := strings.Index(s, "<key>"+key+"</key>")
		rest := s[i:]
		end := strings.Index(rest[1:], "<key>") + 1
		if key == "items_to_copy" || key == "installs" {
			end = strings.Index(rest, "</array>") + len("</array>")
		}
		return s[:i] + rest[end:]
	}
	bad := map[string]struct{ in, want string }{
		"not a dict":        {`<plist><array/></plist>`, "not a dictionary"},
		"no name":           {without("name"), "name is required"},
		"name type":         {strings.Replace(pkginfo("x", "1", ""), "<string>x</string>", "<integer>1</integer>", 1), "name must be a string"},
		"bad name":          {pkginfo("../x", "1", ""), "name"},
		"name .dmg":         {pkginfo("x.dmg", "1", ""), "not ending in .dmg"},
		"no version":        {without("version"), "version is required"},
		"blank version":     {pkginfo("x", " ", ""), "version"},
		"installer type":    {strings.Replace(pkginfo("x", "1", ""), "copy_from_dmg", "nopkg", 1), "copy_from_dmg items only"},
		"no items":          {without("items_to_copy"), "items_to_copy"},
		"absolute source":   {strings.Replace(pkginfo("x", "1", ""), "<string>x</string>\n\t\t<key>dest", "<string>/x</string>\n\t\t<key>dest", 1), "source_item"},
		"dotdot source":     {strings.Replace(pkginfo("x", "1", ""), "<string>x</string>\n\t\t<key>dest", "<string>a/../../x</string>\n\t\t<key>dest", 1), "source_item"},
		"relative dest":     {strings.Replace(pkginfo("x", "1", ""), "<string>/Applications/Games</string>", "<string>Games</string>", 1), "destination_path"},
		"dotdot dest":       {strings.Replace(pkginfo("x", "1", ""), "<string>/Applications/Games</string>", "<string>/Applications/../etc</string>", 1), "destination_path"},
		"no installed_size": {without("installed_size"), "installed_size"},
		"zero size":         {strings.Replace(pkginfo("x", "1", ""), "<integer>2048</integer>", "<integer>0</integer>", 1), "installed_size"},
		"PackageURL":        {pkginfo("x", "1", "<key>PackageURL</key><string>http://evil/</string>"), "PackageURL isn't allowed"},
		"complete URL":      {pkginfo("x", "1", "<key>PackageCompleteURL</key><string>http://evil/x.dmg</string>"), "PackageCompleteURL isn't allowed"},
		"uninstaller":       {pkginfo("x", "1", "<key>uninstaller_item_location</key><string>u.pkg</string>"), "uninstaller_item_location"},
		"empty catalogs":    {pkginfo("x", "1", "<key>catalogs</key><array/>"), "non-empty array"},
		"catalog all":       {pkginfo("x", "1", "<key>catalogs</key><array><string>all</string></array>"), "invalid catalog name"},
		"catalog slash":     {pkginfo("x", "1", "<key>catalogs</key><array><string>a/b</string></array>"), "invalid catalog name"},
		"installs type":     {strings.Replace(without("installs"), "</dict>\n</plist>", "<key>installs</key><string>no</string></dict>\n</plist>", 1), "installs must be an array"},
		"icon type":         {pkginfo("x", "1", "<key>icon_name</key><true/>"), "icon_name must be a string"},
		"archs type":        {pkginfo("x", "1", "<key>supported_architectures</key><string>arm64</string>"), "supported_architectures"},
	}
	for name, c := range bad {
		if _, err := ParsePkginfo([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	// Munki falls back to destination_item as the full destination.
	alt := strings.Replace(pkginfo("x", "1", ""), "<key>destination_path</key>", "<key>destination_item</key>", 1)
	if _, err := ParsePkginfo([]byte(alt)); err != nil {
		t.Errorf("destination_item only: %v", err)
	}
	p, err = ParsePkginfo([]byte(without("installs")))
	if err != nil || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "no installs array") {
		t.Errorf("no installs: %v, %v", err, p)
	}
}

func TestStaleAndPublished(t *testing.T) {
	p, err := ParsePkginfo([]byte(pkginfo("x", "1", `<key>installer_item_hash</key><string>ABC</string>
<key>installer_item_size</key><integer>1</integer><key>notes</key><string>n</string><key>_x</key><true/>`)))
	if err != nil {
		t.Fatal(err)
	}
	if why := p.Stale("abc", 1500); why != "" {
		t.Errorf("matching sidecar called stale: %s", why)
	}
	if why := p.Stale("abd", 1500); !strings.Contains(why, "installer_item_hash") {
		t.Errorf("hash mismatch: %q", why)
	}
	if why := p.Stale("abc", 5000); !strings.Contains(why, "installer_item_size") {
		t.Errorf("size mismatch: %q", why)
	}
	info := p.Published("x/x-1.dmg", "abc", 1025, "x.png", "production")
	if info["installer_item_size"] != int64(2) || info["installer_item_hash"] != "abc" ||
		info["installer_item_location"] != "x/x-1.dmg" || info["icon_name"] != "x.png" ||
		!reflect.DeepEqual(info["catalogs"], []any{"production"}) {
		t.Errorf("published %v", info)
	}
	if _, ok := p.Info["installer_item_location"]; ok {
		t.Error("Published changed the sidecar's dictionary")
	}
	e := CatalogEntry(info)
	if _, ok := e["notes"]; ok {
		t.Error("catalog entry kept notes")
	}
	if _, ok := e["_x"]; ok {
		t.Error("catalog entry kept a key starting with _")
	}
	if SizeKB(0) != 0 || SizeKB(1) != 1 || SizeKB(1024) != 1 || SizeKB(1025) != 2 {
		t.Error("SizeKB doesn't round up")
	}
}

var pngData = "\x89PNG\r\n\x1a\nicon"

// fixture lays out a source with every case: versions, catalogs, icons, a
// download-only image, a stale sidecar, an orphan sidecar and a duplicate.
func fixture(t *testing.T, src string) {
	t.Helper()
	w := func(name, data string) {
		p := filepath.Join(src, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeDMG(t, filepath.Join(src, "game-1.0.dmg"), "game one")
	w("game-1.0.dmg.pkginfo", pkginfo("game", "1.0", "<key>description</key><string>A game\nwith more</string>"))
	w("game-1.0.dmg.png", pngData+"1")
	// In a subdirectory, newer, in two catalogs, with makepkginfo's keys.
	b := fakeDMG(t, filepath.Join(src, "new", "game-1.10.dmg"), "game ten")
	sum := sha256.Sum256(b)
	w("new/game-1.10.dmg.pkginfo", pkginfo("game", "1.10", fmt.Sprintf(`<key>catalogs</key><array><string>testing</string><string>production</string></array>
	<key>installer_item_hash</key><string>%x</string><key>installer_item_size</key><integer>%d</integer>
	<key>installer_item_location</key><string>game-1.10.dmg</string>
	<key>supported_architectures</key><array><string>arm64</string></array>`, sum, len(b)/1024)))
	w("new/game-1.10.dmg.png", pngData+"10")
	fakeDMG(t, filepath.Join(src, "tool-2.dmg"), "tool")
	w("tool-2.dmg.pkginfo", pkginfo("tool", "2", `<key>catalogs</key><array><string>testing</string></array>
	<key>display_name</key><string>Tool</string><key>notes</key><string>admin only</string><key>_meta</key><string>x</string>
	<key>preinstall_script</key><string>#!/bin/sh
echo "&lt;scripts pass through&gt;"</string><key>icon_name</key><string>missing.png</string>`))
	// The same image twice is published once.
	fakeDMG(t, filepath.Join(src, "zz", "tool-2.dmg"), "tool")
	w("zz/tool-2.dmg.pkginfo", pkginfo("tool", "2.0", ""))
	fakeDMG(t, filepath.Join(src, "loose.dmg"), "loose")
	fakeDMG(t, filepath.Join(src, "stale.dmg"), "stale")
	w("stale.dmg.pkginfo", pkginfo("stale", "1", `<key>installer_item_hash</key><string>00</string>`))
	w("orphan.dmg.pkginfo", pkginfo("orphan", "1", ""))
	w("notes.txt", "ignored")
}

func publishFixture(t *testing.T, repo config.Repo) (*publish.Release, *publish.Env, []string) {
	t.Helper()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	rel, err := publish.Begin(t.TempDir(), now)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	env := &publish.Env{Release: rel, Now: now, Logf: func(f string, a ...any) {
		logs = append(logs, fmt.Sprintf(f, a...))
		t.Logf(f, a...)
	}}
	if err := Publish(repo, env); err != nil {
		t.Fatal(err)
	}
	return rel, env, logs
}

func readOut(t *testing.T, rel *publish.Release, p string) []byte {
	t.Helper()
	abs, err := rel.Path(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeOut(t *testing.T, rel *publish.Release, p string) any {
	t.Helper()
	v, err := DecodePlist(readOut(t, rel, p))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return v
}

func TestPublish(t *testing.T) {
	src, manifests := t.TempDir(), t.TempDir()
	fixture(t, src)
	os.MkdirAll(filepath.Join(manifests, "groups"), 0o755)
	os.WriteFile(filepath.Join(manifests, "testers"), []byte(`<?xml version="1.0"?><plist version="1.0"><dict>
<key>catalogs</key><array><string>testing</string><string>production</string><string>beta</string></array>
<key>included_manifests</key><array><string>games</string><string>groups/staff</string><string>nope</string></array>
<key>managed_installs</key><array><string>tool</string><string>game-1.0</string><string>ghost</string></array>
<key>conditional_items</key><array><dict><key>condition</key><string>arch == "arm64"</string>
<key>optional_installs</key><array><string>phantom</string></array></dict></array>
</dict></plist>`), 0o644)
	os.WriteFile(filepath.Join(manifests, "groups", "staff"), []byte(`<plist><dict><key>optional_installs</key><array><string>game--1.10</string></array></dict></plist>`), 0o644)
	repo := config.Repo{Name: "games", Format: config.FormatMunki, Source: src, Catalog: "production",
		SiteDefault: true, ManifestsSource: manifests}
	rel, env, logs := publishFixture(t, repo)

	// Package files and what the pages see.
	var got []string
	for _, f := range env.Packages {
		got = append(got, fmt.Sprintf("%s %s %s %q %q", f.Path, f.Name, f.Version, f.Arch, f.Summary))
	}
	want := []string{
		`munki/games/pkgs/loose.dmg loose  "" "Download only: no pkginfo"`,
		`munki/games/pkgs/stale.dmg stale  "" "Download only: no pkginfo"`,
		`munki/games/pkgs/game/game-1.0.dmg game 1.0 "" "A game"`,
		`munki/games/pkgs/game/game-1.10.dmg game 1.10 "arm64" ""`,
		`munki/games/pkgs/tool/tool-2.dmg tool 2 "" "Tool"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("recorded\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, w := range []string{
		"orphan.dmg.pkginfo has no disk image beside it",
		"loose.dmg has no .pkginfo sidecar: published for download only",
		"stale.dmg.pkginfo: its installer_item_hash 00 isn't the disk image's SHA-256",
		`icon_name "missing.png" isn't in this repo's icons`,
		"skipping " + filepath.Join(src, "zz", "tool-2.dmg") + " (the same disk image as",
		`catalog "beta" isn't published`,
		`included manifest "nope" isn't published`,
		`managed_installs: no item named "ghost"`,
		`optional_installs: no item named "phantom"`,
		"munki/games: 3 items in catalogs, 2 download only, 4 manifests",
	} {
		if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, w) }) {
			t.Errorf("no log line with %q", w)
		}
	}
	for _, l := range logs {
		for _, quiet := range []string{`"game-1.0"`, `"game--1.10"`, `"tool"`, `"games"`, `"groups/staff"`, `"testing"`} {
			if strings.Contains(l, "no item named "+quiet) || strings.Contains(l, "manifest "+quiet) || strings.Contains(l, "catalog "+quiet) {
				t.Errorf("unexpected warning: %s", l)
			}
		}
	}

	// The published pkginfo: sidecar keys, plus crossdock's, notes kept.
	tool := decodeOut(t, rel, "munki/games/pkgsinfo/tool/tool-2.plist").(map[string]any)
	if tool["notes"] != "admin only" || tool["installer_item_location"] != "tool/tool-2.dmg" ||
		!strings.Contains(tool["preinstall_script"].(string), "<scripts pass through>") {
		t.Errorf("tool pkginfo %v", tool)
	}
	game := decodeOut(t, rel, "munki/games/pkgsinfo/game/game-1.0.plist").(map[string]any)
	dmg := readOut(t, rel, "munki/games/pkgs/game/game-1.0.dmg")
	sum := sha256.Sum256(dmg)
	if game["installer_item_hash"] != hex.EncodeToString(sum[:]) || game["installer_item_size"] != SizeKB(int64(len(dmg))) ||
		game["icon_name"] != "game.png" || !reflect.DeepEqual(game["catalogs"], []any{"production"}) {
		t.Errorf("game pkginfo %v", game)
	}
	// The icon comes from the newest version.
	if string(readOut(t, rel, "munki/games/icons/game.png")) != pngData+"10" {
		t.Error("icons/game.png isn't the newest version's icon")
	}
	iconSum := sha256.Sum256([]byte(pngData + "10"))
	if h := decodeOut(t, rel, "munki/games/icons/_icon_hashes.plist"); !reflect.DeepEqual(h, map[string]any{"game.png": hex.EncodeToString(iconSum[:])}) {
		t.Errorf("_icon_hashes.plist %v", h)
	}

	catalog := func(name string) []string {
		var out []string
		for _, e := range decodeOut(t, rel, "munki/games/catalogs/"+name).([]any) {
			m := e.(map[string]any)
			if _, ok := m["notes"]; ok {
				t.Errorf("catalogs/%s: notes kept", name)
			}
			if _, ok := m["_meta"]; ok {
				t.Errorf("catalogs/%s: _meta kept", name)
			}
			out = append(out, m["name"].(string)+"-"+m["version"].(string))
		}
		return out
	}
	for name, want := range map[string][]string{
		"all":        {"game-1.0", "game-1.10", "tool-2"},
		"production": {"game-1.0", "game-1.10"},
		"testing":    {"game-1.10", "tool-2"},
	} {
		if got := catalog(name); !slices.Equal(got, want) {
			t.Errorf("catalogs/%s = %v, want %v", name, got, want)
		}
	}

	wantManifest := map[string]any{"catalogs": []any{"production", "testing"}, "optional_installs": []any{"game", "tool"}}
	for _, m := range []string{"games", "site_default"} {
		if got := decodeOut(t, rel, "munki/games/manifests/"+m); !reflect.DeepEqual(got, wantManifest) {
			t.Errorf("manifests/%s = %v", m, got)
		}
	}
	if b := readOut(t, rel, "munki/games/manifests/groups/staff"); !bytes.HasPrefix(b, []byte("<plist><dict>")) {
		t.Errorf("hand-written manifest not copied as is: %s", b)
	}

	if *update {
		for _, f := range []string{"catalogs/all", "manifests/games", "pkgsinfo/tool/tool-2.plist"} {
			os.MkdirAll(filepath.Join("testdata", filepath.Dir(f)), 0o755)
			os.WriteFile(filepath.Join("testdata", f), readOut(t, rel, "munki/games/"+f), 0o644)
		}
	}
	for _, f := range []string{"catalogs/all", "manifests/games", "pkgsinfo/tool/tool-2.plist"} {
		want, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatalf("%v (run go test -update to create it)", err)
		}
		if got := readOut(t, rel, "munki/games/"+f); !bytes.Equal(got, want) {
			t.Errorf("%s differs from testdata (go test -update rewrites it):\n%s", f, got)
		}
	}
}

func TestPublishErrors(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(t *testing.T, src, manifests string)
		want  string
	}{
		"same version, different image": {func(t *testing.T, src, _ string) {
			fakeDMG(t, filepath.Join(src, "a.dmg"), "a")
			os.WriteFile(filepath.Join(src, "a.dmg.pkginfo"), []byte(pkginfo("x", "1.0", "")), 0o644)
			fakeDMG(t, filepath.Join(src, "b.dmg"), "b")
			os.WriteFile(filepath.Join(src, "b.dmg.pkginfo"), []byte(pkginfo("x", "1", "")), 0o644)
		}, "are both x 1 with different contents"},
		"same path": {func(t *testing.T, src, _ string) {
			fakeDMG(t, filepath.Join(src, "a", "x.dmg"), "a")
			fakeDMG(t, filepath.Join(src, "b", "x.dmg"), "b")
		}, "would both be published as pkgs/x.dmg"},
		"truncated image": {func(t *testing.T, src, _ string) {
			b := fakeDMG(t, filepath.Join(src, "a.dmg"), "a")
			os.WriteFile(filepath.Join(src, "a.dmg"), b[:len(b)-10], 0o644)
		}, "no koly trailer"},
		"bad sidecar": {func(t *testing.T, src, _ string) {
			fakeDMG(t, filepath.Join(src, "a.dmg"), "a")
			os.WriteFile(filepath.Join(src, "a.dmg.pkginfo"), []byte(pkginfo("x", "1", "<key>PackageURL</key><string>u</string>")), 0o644)
		}, "a.dmg.pkginfo: PackageURL isn't allowed"},
		"bad icon": {func(t *testing.T, src, _ string) {
			fakeDMG(t, filepath.Join(src, "a.dmg"), "a")
			os.WriteFile(filepath.Join(src, "a.dmg.png"), []byte("GIF89a"), 0o644)
		}, "not a PNG"},
		"symlinked sidecar": {func(t *testing.T, src, _ string) {
			fakeDMG(t, filepath.Join(src, "a.dmg"), "a")
			if err := os.Symlink("/etc/passwd", filepath.Join(src, "a.dmg.pkginfo")); err != nil {
				t.Skip(err)
			}
		}, "is a symlink"},
		"control character": {func(t *testing.T, src, _ string) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows file names can't hold a tab")
			}
			fakeDMG(t, filepath.Join(src, "a\tb.dmg"), "a")
		}, "control characters"},
		"generated manifest name": {func(t *testing.T, src, m string) {
			os.WriteFile(filepath.Join(m, "site_default"), []byte(`<plist><dict/></plist>`), 0o644)
		}, "crossdock generates manifests/site_default"},
		"manifest type": {func(t *testing.T, src, m string) {
			os.WriteFile(filepath.Join(m, "x"), []byte(`<plist><dict><key>managed_installs</key><string>x</string></dict></plist>`), 0o644)
		}, "managed_installs must be an array"},
		"manifest condition": {func(t *testing.T, src, m string) {
			os.WriteFile(filepath.Join(m, "x"), []byte(`<plist><dict><key>conditional_items</key><array><dict/></array></dict></plist>`), 0o644)
		}, "condition (a string) is required"},
		"manifest name": {func(t *testing.T, src, m string) {
			os.WriteFile(filepath.Join(m, "a b"), []byte(`<plist><dict/></plist>`), 0o644)
		}, "manifest names"},
		"manifest not a plist": {func(t *testing.T, src, m string) {
			os.WriteFile(filepath.Join(m, "x"), []byte(`{"json": true}`), 0o644)
		}, "x:"},
	} {
		t.Run(name, func(t *testing.T) {
			src, m := t.TempDir(), t.TempDir()
			c.setup(t, src, m)
			rel, _ := publish.Begin(t.TempDir(), time.Now())
			err := Publish(config.Repo{Name: "r", Format: config.FormatMunki, Source: src, Catalog: "production",
				SiteDefault: true, ManifestsSource: m}, &publish.Env{Release: rel, Now: time.Now(), Logf: t.Logf})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%v, want %q", err, c.want)
			}
		})
	}
}

func TestPublishEmpty(t *testing.T) {
	rel, _, _ := publishFixture(t, config.Repo{Name: "r", Format: config.FormatMunki, Source: t.TempDir(), Catalog: "production"})
	if v := decodeOut(t, rel, "munki/r/catalogs/all"); !reflect.DeepEqual(v, []any{}) {
		t.Errorf("catalogs/all = %v", v)
	}
	if v := decodeOut(t, rel, "munki/r/manifests/r"); !reflect.DeepEqual(v, map[string]any{"catalogs": []any{}, "optional_installs": []any{}}) {
		t.Errorf("manifests/r = %v", v)
	}
}

// TestMakecatalogs runs Munki's own makecatalogs (Munki 7.4+ builds it for
// Linux) over the published pkgsinfo/, pkgs/ and icons/, and checks that
// its catalogs and icon hashes match crossdock's. CI sets MAKECATALOGS.
func TestMakecatalogs(t *testing.T) {
	bin := os.Getenv("MAKECATALOGS")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("makecatalogs"); err != nil {
			t.Skip("makecatalogs not installed (set MAKECATALOGS to Munki's makecatalogs)")
		}
	}
	src := t.TempDir()
	fixture(t, src)
	rel, _, _ := publishFixture(t, config.Repo{Name: "games", Format: config.FormatMunki, Source: src, Catalog: "production"})
	pub, err := rel.Path("munki/games")
	if err != nil {
		t.Fatal(err)
	}
	ref := t.TempDir()
	for _, dir := range []string{"pkgsinfo", "pkgs", "icons"} {
		copyTree(t, filepath.Join(pub, dir), filepath.Join(ref, dir))
	}
	os.Remove(filepath.Join(ref, "icons", "_icon_hashes.plist"))
	out, err := exec.Command(bin, ref).CombinedOutput()
	t.Logf("makecatalogs:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	names := func(dir string) []string {
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, e := range es {
			n = append(n, e.Name())
		}
		return n
	}
	if a, b := names(filepath.Join(pub, "catalogs")), names(filepath.Join(ref, "catalogs")); !slices.Equal(a, b) {
		t.Fatalf("catalogs: crossdock wrote %v, makecatalogs %v", a, b)
	}
	read := func(p string) any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		v, err := DecodePlist(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return v
	}
	// makecatalogs lists pkginfo files in directory order, so compare
	// catalogs as sets.
	sorted := func(v any) []any {
		a := slices.Clone(v.([]any))
		slices.SortFunc(a, func(x, y any) int {
			return cmp.Compare(x.(map[string]any)["installer_item_location"].(string), y.(map[string]any)["installer_item_location"].(string))
		})
		return a
	}
	for _, c := range names(filepath.Join(pub, "catalogs")) {
		ours, theirs := sorted(read(filepath.Join(pub, "catalogs", c))), sorted(read(filepath.Join(ref, "catalogs", c)))
		if !reflect.DeepEqual(ours, theirs) {
			t.Errorf("catalogs/%s differs:\ncrossdock    %v\nmakecatalogs %v", c, ours, theirs)
		}
	}
	if a, b := read(filepath.Join(pub, "icons", "_icon_hashes.plist")), read(filepath.Join(ref, "icons", "_icon_hashes.plist")); !reflect.DeepEqual(a, b) {
		t.Errorf("_icon_hashes.plist: crossdock %v, makecatalogs %v", a, b)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p) // follows the release's links
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
