// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 John-Michael and crossdock contributors

package pages

import "html/template"

// Style is the stylesheet the pages and serve's directory listings share.
// No JavaScript anywhere: the pages work in any browser, and in curl.
const Style = `:root{color-scheme:light dark;--fg:#1f2328;--muted:#59636e;--bg:#fff;--line:#d1d9e0;--code:#f6f8fa;--link:#0969da}
@media (prefers-color-scheme:dark){:root{--fg:#e6edf3;--muted:#9198a1;--bg:#0d1117;--line:#30363d;--code:#161b22;--link:#4493f8}}
body{font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;color:var(--fg);background:var(--bg);max-width:76rem;margin:0 auto;padding:1.5rem 1rem 3rem}
a{color:var(--link)}h1{font-size:1.6rem;margin:.2rem 0 1rem}h2{font-size:1.2rem;margin:2rem 0 .5rem}
code,pre,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
pre{background:var(--code);border:1px solid var(--line);border-radius:6px;padding:.75rem 1rem;overflow-x:auto;font-size:13px;line-height:1.45}
.wrap{overflow-x:auto}table{border-collapse:collapse;width:100%;font-size:14px}
th,td{text-align:left;padding:.4rem .6rem;border-bottom:1px solid var(--line);vertical-align:top}
th{font-weight:600;color:var(--muted);white-space:nowrap}td.num{text-align:right;white-space:nowrap}
.sha{font-size:12px;word-break:break-all;color:var(--muted)}.muted{color:var(--muted)}
tr.part td:first-child{padding-left:1.8rem}tr.install td{border-bottom:1px solid var(--line);padding-top:0}
tr.install code{font-size:12px;word-break:break-all}.nav{font-size:14px;margin-bottom:.5rem}
footer{margin-top:2.5rem;font-size:13px;color:var(--muted)}`

const head = `{{define "head"}}<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.}}</title>
{{end}}`

const keyBlock = `{{define "key"}}{{if .Signed}}<p>Signed with key <code>{{.Fingerprint}}</code>:
<a href="{{.Root}}crossdock.asc">crossdock.asc</a> (armored),
<a href="{{.Root}}crossdock.gpg">crossdock.gpg</a> (binary).</p>
{{else}}<p><strong>These repos are not signed.</strong> Clients can't tell them from a tampered copy, so only use them on a network you trust.</p>
{{end}}{{end}}`

const footer = `{{define "footer"}}<footer>Published {{.Published}}{{with .Generator}} by {{.}}{{end}}.</footer>
{{end}}`

var rootTmpl = template.Must(template.New("root").Funcs(funcs).Parse(head + keyBlock + footer + `{{template "head" "Package repositories"}}<style>{{.Style}}</style>
<h1>Package repositories</h1>
{{template "key" (withRoot .Common "")}}
{{range .Repos}}<h2><a href="{{href .Dir}}/">{{.Dir}}</a></h2>
<p class="muted">{{.Client}} · {{.Count}} {{if eq .Count 1}}package{{else}}packages{{end}} · {{.Size}}</p>
<pre>{{.Snippet}}</pre>
{{range .Notes}}<p class="muted">{{.}}</p>
{{end}}{{else}}<p>No repos are configured.</p>
{{end}}{{template "footer" .Common}}`))

var repoTmpl = template.Must(template.New("repo").Funcs(funcs).Parse(head + keyBlock + footer + `{{template "head" .Dir}}<style>{{.Style}}</style>
{{if .HasRoot}}<p class="nav"><a href="../../">All repositories</a></p>
{{end}}<h1>{{.Dir}}</h1>
<p class="muted">{{.Client}} · {{.Count}} {{if eq .Count 1}}package{{else}}packages{{end}} · {{.Size}}</p>
{{with .Browse}}<p class="nav">Browse:{{range .}} <a href="{{.Href}}">{{.Text}}</a>{{end}}</p>
{{end}}<h2>Set up {{.Client}}</h2>
<pre>{{.Snippet}}</pre>
{{range .Notes}}<p class="muted">{{.}}</p>
{{end}}{{template "key" (withRoot .Common "../../")}}
<h2>Download by hand</h2>
<p><a href="./{{.SumsName}}">{{.SumsName}}</a>{{if .Signed}} and its signature <a href="./{{.SumsName}}.asc">{{.SumsName}}.asc</a>{{end}} list every package's SHA-256. Check files you downloaded with:</p>
<pre>{{if .Signed}}curl -fsSLO '{{.Base}}/crossdock.gpg' -O '{{.Base}}/{{.Dir}}/{{.SumsName}}' -O '{{.Base}}/{{.Dir}}/{{.SumsName}}.asc'
gpgv --keyring ./crossdock.gpg {{.SumsName}}.asc {{.SumsName}}    # or: sqv --keyring crossdock.gpg {{.SumsName}}.asc {{.SumsName}}
{{else}}curl -fsSLO '{{.Base}}/{{.Dir}}/{{.SumsName}}'
{{end}}{{.SumCmd}} -c --ignore-missing {{.SumsName}}</pre>
{{range .ByHand}}<p class="muted">{{.}}</p>
{{end}}
{{if .SumsByPath}}<p class="muted">Some different files here share a name, so {{.SumsName}} lists those by their path in the repo. Keep that path when you download one, or compare its SHA-256 with the line in {{.SumsName}} by hand.</p>
{{end}}
{{if .Groups}}<div class="wrap"><table>
<thead><tr><th>Package</th><th>Version</th><th>Arch</th><th class="num">Size</th><th>File and SHA-256</th></tr></thead>
<tbody>
{{range .Groups}}{{range .Rows}}<tr{{if .Part}} class="part"{{end}}><td><strong>{{.Name}}</strong>{{with .Summary}}<br><span class="muted">{{.}}</span>{{end}}</td><td class="mono">{{.Version}}</td><td>{{.Arch}}</td><td class="num" title="{{.Bytes}} bytes">{{.Size}}</td><td><a href="{{.Href}}">{{.Text}}</a><br><span class="mono sha">{{.SHA256}}</span></td></tr>
{{end}}{{with .Install}}<tr class="install"><td colspan="5"><span class="muted">Install from downloaded files:</span> <code>{{.}}</code></td></tr>
{{end}}{{end}}</tbody>
</table></div>
{{else}}<p>This repo has no packages yet.</p>
{{end}}{{template "footer" .Common}}`))

// keyData is Common plus the relative path to the release root, for links
// to the public key.
type keyData struct {
	Common
	Root string
}

var funcs = template.FuncMap{
	"href":     href,
	"withRoot": func(c Common, root string) keyData { return keyData{c, root} },
}
