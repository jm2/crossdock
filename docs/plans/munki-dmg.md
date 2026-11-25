# Munki + DMG and browseable repos: plan

Nov 25, 2026. Written against `jm2/crossdock` at `f48cf70` and
`jm2/game-packages` at `8504a75`.

> **Status.** Phase 1 (browseable repos) is implemented; see
> [Phase 1 as built](#phase-1-as-built) for where it differs from the plan
> below. Phase 0 needs a Mac and real game data. The decisions under
> [Decisions](#decisions) are still open, except the landing-page URLs.

## Verdict: mostly agree, with caveats on size

Munki 7 with `copy_from_dmg` DMGs is the right managed client for the Macs.
The DMG is also the right hybrid artifact: the file Munki installs is the
same one a person mounts and drags by hand. But Munki tolerates 100 GB+
items rather than handling them well. Every update downloads the whole DMG
again, and an upgrade briefly needs about three times the installed size
free.

**Where I agree**

- Munki is FOSS (Apache-2.0) and its repo is plain static files over HTTP.
  That maps directly onto crossdock: atomic release swap, hardlinked store,
  `serve` with Range requests.
- DMG beats a flat `.pkg`. A pkg with any file of 8 GiB or more needs
  `pkgbuild --large-payload`, which needs macOS 12+ on the client and
  Apple's pkgbuild to build. A DMG needs neither, and people can browse and
  drag it.
- Homebrew casks are out. Homebrew has dropped its quarantine bypass, and
  since 5.0 is disabling official casks that fail Gatekeeper. Unsigned game
  wrappers would trip Gatekeeper.
- Munki brings self-service optional installs, `requires`/`update_for`,
  uninstall and resumable downloads with no extra work.

**Where I disagree, or add caveats**

- Size: no delta updates, about 2x the installed size free to install and
  about 3x for a moment during an upgrade (next section). pacman, dnf, apt
  and GooGet re-download whole packages today too, so it's no regression,
  but it isn't a fix.
- Storage: each DMG is a fifth full copy of a game on a NAS that's already
  tight, about 125 GiB more for BO3. Build DMGs only for titles that
  actually run on the Macs.
- Integrity: Munki catalogs aren't signed, and a pkginfo can carry scripts
  that Munki runs as root. Over plain HTTP on a shared LAN, that's a path to
  root on the Macs. It needs TLS or a tailnet-only URL (Phase 4).
- Manual installs: Munki strips the quarantine flag; a browser download
  keeps it. On macOS 15+, an unsigned app copied from a downloaded DMG
  needs the System Settings "Open Anyway" step before it runs.

**Explore alongside, not instead:** a content-defined chunk store (the
casync format, via the Go tool desync) for the largest titles. Whether it's
worth building depends on how much of a game a typical Steam update
actually changes, which Phase 0 measures.

## What Munki does with a 125 GiB DMG

An upgrade of a 125 GiB game peaks at about 375 GiB of free space, while
Munki's pre-flight check asks for about 250 GiB. Everything below was read
in Munki's Swift source on `main` (CLI version 7.4.2, commit of
2026-11-18), under `code/cli/munki/shared/`.

Upgrade sequence, with the space Munki holds at each step (BO3 at about
125 GiB, DMG roughly the same size since game data barely compresses):

1. Check free space: `installer_item_size` minus what's already downloaded,
   plus `installed_size`, plus 100 MB. About 250 GiB.
   (`updatecheck/download.swift`)
2. Download the DMG into `/Library/Managed Installs/Cache`, resuming with
   `Range: bytes=N-` when it stored an ETag or Last-Modified for the partial
   file. Holds 125 GiB. (`network/gurl.swift`)
3. Check the SHA-256 against `installer_item_hash` (default
   `PackageVerificationMode` is `hash`), then
   `hdiutil attach -nobrowse -noverify`, so there's no second full read at
   mount. (`network/fetch.swift`, `utils/dmgutils.swift`)
4. `ditto` each item into a temp directory, strip quarantine xattrs, set
   owner and mode. The old install is still there: about 375 GiB in use.
   (`installer/dmg.swift`)
5. Delete the old install and move the new copy into place. Missing parent
   directories are created. About 250 GiB.
6. Unmount, then delete the cached DMG once nothing else needs it. 125 GiB.
   (`installer/installer.swift`)

Other behaviour the crossdock side has to match:

| Behaviour | What it means for crossdock |
| --- | --- |
| Resume keys on the stored ETag / Last-Modified | `serve` already derives the ETag from mtime and size; the hardlinked store object keeps both stable across releases. Test it anyway. |
| `makecatalogs` writes `catalogs/all` plus one file per catalog name, drops `notes` and keys starting with `_`, and writes `icons/_icon_hashes.plist` | Reproduce exactly; diff against Munki's own makecatalogs in CI. |
| Versions are padded to the same number of dot-separated parts, then compared with a numeric string compare | Port it (about 20 lines) for newest-version and duplicate checks, as was done for pacman's `vercmp` and RPM. |
| Sizes are integers in KB | Compute from the DMG's byte size; `installed_size` comes from the producer. |
| No catalog or manifest signatures | Same gap as GooGet; covered in Phase 4. |
| Munki 7.4 adds a Linux build of `makecatalogs` | Usable as a reference implementation in crossdock's Linux CI. |

## Phase 0: spikes before any code

Four short experiments settle the DMG format, prove Munki at full size, and
decide whether chunked delivery is worth building. None needs crossdock
changes.

- [ ] **Build one real 100 GB+ DMG on the mbp16.**
  `hdiutil create -srcfolder <staged tree> -fs APFS -format UDRO` and again
  with `-format ULFO`. Record build time, file size, attach time and `ditto`
  throughput to the internal SSD. Pick the default format (I expect UDRO,
  since game data barely compresses).
- [ ] **Run Munki end to end on that DMG.** Hand-write the pkginfo, run
  Munki's `makecatalogs`, and serve the repo from the NAS with any static
  server. Time a fresh install and an upgrade, wired and on Wi-Fi. Kill one
  download halfway to confirm it resumes. Watch free space during the
  upgrade to confirm the 3x peak.
- [ ] **Measure what a Steam update changes.** Keep two consecutive builds
  of BO3 and one other large title in `PT_CACHE`. Chunk both trees with
  `desync make` and count shared chunks. If a typical update changes well
  under a fifth of the bytes, Phase 5 earns its place.
- [ ] **Decide what a Mac game DMG contains.** Game files, a launcher app,
  and which Wine/D3DMetal runtime. Homebrew's Wine casks were deprecated for
  failing Gatekeeper, so the runtime likely becomes its own Munki item that
  games `require`. This is game-packages design work, but Phase 3 depends
  on it.

Optional: try Mozilla's libdmg-hfsplus fork to build a 10+ GiB DMG on
Linux. Its own README calls it highly experimental, and Firefox's images are
tiny by comparison, so only chase this if building on the Mac becomes the
bottleneck.

## Phase 1: browseable repos by default

Ship this first, as its own release: directory listings on by default, plus
static index pages and signed checksums per repo. It makes manual download
work for every format today, and DMGs inherit it in Phase 2.

| File | Change |
| --- | --- |
| `internal/config/config.go` | `Serve.Index` becomes `*bool`; `applyDefaults` sets it to true when unset; add `IndexEnabled()`. |
| `cmd/crossdock/main.go` | `cmdServe` can only switch listings on today (`if *index { sc.Index = true }`). Use `fs.Visit` so an explicit `-index=false` beats the config. `cmdNginx` defaults `-index` to true. |
| `internal/serve/handler.go` | A directory request serves its `index.html` if present, else the listing, else 404. Listing gains directories first, size, UTC mtime and a parent link; still no JavaScript, dotfiles still refused, still confined by `os.Root`. |
| `internal/serve/nginx.go` | `autoindex on` by default, with `autoindex_exact_size off`, `autoindex_localtime off` (UTC, matching `serve`) and `index index.html`. |
| `internal/serve/rules.go` | Add `/index\.html` and `/SHA256SUMS(\.asc)?` to the `metadata` rule, so they revalidate. |
| `internal/publish/env.go` | A collector on `Env`: backends report name, version, arch, path, size and SHA-256 for each file they publish. All five call it next to their existing `AddVerified`/`AddFile`. |
| `build-aux/etc/crossdock.yaml` | `index: true` under `serve:`, with a comment on turning it off. |

**Publish-time pages.** These are plain files in the release, so nginx,
Caddy or an object store show them too:

- `index.html` at the release root: each repo, the signing key's
  fingerprint, and a ready-to-paste client snippet per repo (apt, pacman,
  dnf, googet, Munki).
- `<format>/<repo>/index.html`: one row per package with version, arch,
  size, SHA-256 and a link. A deb's `-dataN` parts are grouped under their
  package with the single `apt install` line that needs them all.
- `<format>/<repo>/SHA256SUMS`, plus `SHA256SUMS.asc` signed with the repo
  key when signing is configured. That gives hand-downloaded files, DMGs
  included, a signature check that Munki and GooGet lack.
- Snippets need an absolute base URL, so add an optional
  `serve.public_url`. Without it the page shows a `http://<host>:8080`
  placeholder.
- No clashes: GooGet's index is `goo/<repo>/index` with no extension, and no
  format publishes `index.html` or `SHA256SUMS`.

**Why on by default is safe.** Every format's index already lists every
package to anyone who can reach it: `Packages`, `repodata`, the pacman
`.db`, GooGet's `index`, and soon Munki catalogs. A listing adds only the
repo names at the root. This changes behaviour for existing installs on
upgrade (their kept config has no `index` key), so it goes under Changed in
`CHANGELOG.md`, with `index: false` documented for internet-facing hosts.

**Tests.** `serve_test.go`: on by default, `-index=false` and
`index: false` both win, `index.html` takes precedence, dotfiles still 404.
A golden test for the nginx output. An e2e check that each repo's
`index.html` and `SHA256SUMS` list every published file with the right
digest, and that `SHA256SUMS.asc` verifies with the `gpgv` and `sqv` the
client CI already runs.

### Phase 1 as built

Where the implementation differs from the plan above, and why:

- **Landing-page URLs: static pages, URL filled in per request.** Without
  `serve.public_url`, the pages carry the placeholder
  (`pages.BaseURLPlaceholder`). `crossdock serve` replaces it with the
  request's scheme and `Host`, taking the scheme from `X-Forwarded-Proto`
  behind a TLS proxy. The generated nginx config does the same with
  `sub_filter` and a `map` on `$http_host`. Both only echo a `Host` that is
  a plain host name, IP address or bracketed IPv6 address, with an optional
  port. Object stores and other servers show the placeholder, so set
  `public_url` there. This kept the pages identical through `serve` and
  nginx, which is the rules table's design goal.
- **`index: false` also leaves out the landing page**, the only page that
  names every repo. Without that, turning listings off would still list the
  repos at `/index.html`. Per-repo pages and `SHA256SUMS` are always
  published.
- **`SHA256SUMS.asc` is a detached signature, not clearsigned.** With a
  detached signature, `SHA256SUMS` stays plain, so
  `sha256sum -c --ignore-missing SHA256SUMS` works on it directly.
  `gpgv sig file` and `sqv --keyring key sig file` verify it as is.
- **`SHA256SUMS` names files, not paths in the repo.** A file downloaded on
  its own then checks in the current directory. Copies of one file (a
  pacman `any` package in every architecture) share a line. Two different
  files with one name would fall back to repo paths, which crossdock's
  per-format duplicate checks already prevent.
- **Four backends, not five.** Munki is Phase 2; it calls `Env.Record`
  like the others. Its client snippet arrives with it.
- **Tests as planned**, plus: `crossdock serve` filling in the URL, and
  refusing hostile `Host` headers; `TestPagesEndToEnd` across all four
  formats, verified with `gpgv` on Ubuntu and `sqv` 1.3 in the Debian
  stable CI job; the dnf smoke test (Fedora 44/45, EL10) and the packaged
  systemd smoke test fetching the pages and `SHA256SUMS` over HTTP. The
  generated nginx config was also checked by hand against nginx 1.30 on
  Fedora 45: it loads, fills in the address, and serves autoindex and the
  right headers.

## Phase 2: `format: munki` in crossdock

A new `internal/munki` backend publishes `munki/<repo>/` as a real Munki
repo from DMGs plus a pkginfo sidecar. crossdock never parses the
filesystem inside a DMG, which keeps it pure Go and fast on 125 GiB files.

**Input contract** (files in the repo's `source`):

- `<file>.dmg`: the installer item.
- `<file>.dmg.pkginfo`: a Munki pkginfo plist, as `makepkginfo` writes it.
  Without one, the DMG is still published under `pkgs/` and listed for
  manual download, but left out of catalogs. That also covers rsync, which
  sorts `X.dmg` ahead of `X.dmg.pkginfo` and so lands the DMG first.
- `<file>.dmg.png`: optional icon, published as `icons/<name>.png`.

**Pkginfo rules**

- crossdock sets `installer_item_location` (`<name>/<file>.dmg`),
  `installer_item_hash` (the SHA-256 the store already computes),
  `installer_item_size` (KB, rounded up), `icon_name` when a PNG is given,
  and `catalogs` when the sidecar has none.
- Required from the sidecar: `name` (same pattern as repo names), `version`,
  `installer_type: copy_from_dmg`, `items_to_copy` (relative `source_item`
  with no `..`, absolute destination) and `installed_size`. A missing
  `installs` array is a warning: without it Munki can't tell an old folder
  from a new one.
- `PackageURL` and `PackageCompleteURL` are refused, since they point Munki
  away from the repo.
- Everything else passes through verbatim, scripts included, like a GooGet
  pkgspec. That's the same trust model as RPM or pacman scriptlets: whoever
  can write to `incoming` is trusted.

**DMG check.** Read the 512-byte UDIF trailer: `koly` magic, version 4,
header size 512, data-fork and XML-plist ranges inside the file. It cheaply
catches truncated uploads and files that aren't DMGs. `crossdock inspect`
prints the trailer and the merged pkginfo as JSON.

**Versions.** Port `MunkiVersion`. One name and version with two different
digests stops the publish, as in every other format. Several versions are
all published, and Munki installs the newest.

**Output**

```
munki/<repo>/
  catalogs/all, catalogs/<name>      as makecatalogs writes them
  manifests/<repo>                   every item under optional_installs
  manifests/site_default             when site_default: true
  manifests/<hand-written>           copied from manifests_source, validated
  pkgs/<name>/<file>.dmg             hardlinked from the store
  pkgsinfo/<name>/<file>.plist       final pkginfo, for makecatalogs and admin tools
  icons/<name>.png, icons/_icon_hashes.plist
  index.html, SHA256SUMS[.asc]       from Phase 1
```

**Config**

```yaml
- name: game-packages
  format: munki
  source: /var/lib/crossdock/incoming/munki
  catalog: production            # for pkginfo without a catalogs key
  site_default: true             # also write manifests/site_default
  manifests_source: /var/lib/crossdock/incoming/munki-manifests   # optional
```

A Munki client reads one `SoftwareRepoURL`, so plan on one Munki repo for
all the Macs and split it with catalogs and manifests, not more repos.

**Where it touches existing code**

| File | Change |
| --- | --- |
| `internal/config/config.go` | `FormatMunki`; `catalog`, `site_default`, `manifests_source`, validated as munki-only. |
| `cmd/crossdock/main.go` | `suffixes()` returns `.dmg`, so the empty-repo check counts DMGs only; dispatch in `build()`. `sourceState()`, which reruns publish when uploads land mid-run, must also cover `.dmg.pkginfo` and `.dmg.png`, the way it already adds `.sig` for Arch. Otherwise a sidecar that arrives late never gets published. |
| `internal/serve/rules.go` | `dmg` in the `package` rule; a new no-cache rule, with an XML type, for everything under `/munki/<repo>/catalogs/` and `/munki/<repo>/manifests/` plus `icons/_icon_hashes.plist`; types for `dmg` (`application/x-apple-diskimage`), `plist` and `png`. |
| `internal/munki/plist.go` | A small XML plist codec for the types Munki uses (dict, array, string, integer, real, bool, date, data), with sorted keys so output is reproducible. No new dependency. |
| `build-aux/systemd/crossdock.tmpfiles`, `crossdock-publish.path`, `build-aux/etc/crossdock.yaml` | `incoming/munki`, its `PathChanged=` line, and a commented-out munki repo. |
| `internal/pages` | A Munki client snippet (`defaults write … SoftwareRepoURL`). |

Hashing a 125 GiB DMG is the only expensive step, and the existing cache
already skips it for an unchanged path, size and mtime. Sidecars are small
and re-read on every publish.

**Tests**

- Unit: sidecar validation cases, truncated and non-DMG trailers, Munki
  version ordering, golden catalogs and manifests.
- Reference check on Linux CI: run Munki 7.4's Linux `makecatalogs` over the
  published `pkgsinfo/` and `pkgs/` and diff its catalogs against
  crossdock's. Same pattern as the `createrepo_c` comparison.
- Real client on a GitHub-hosted macOS arm64 runner: build two tiny DMGs
  with `hdiutil`, publish and serve on the runner, install munkitools, and
  drive `managedsoftwareupdate` through a `managed_installs` manifest. Cover
  install, upgrade, removal via `managed_uninstalls`, a tampered DMG refused
  on hash, and a killed download that resumes (a 206 in `serve`'s access
  log).

## Phase 3: producing DMGs in game-packages

game-packages gains a `munki` format that builds each DMG on a Mac and
generates its pkginfo from `package.toml`, so Mac packages stay as
declarative as the others.

- **Build host.** Build on the mbp16 (or any Mac) with `hdiutil`, reading
  `PT_CACHE` from the NAS over NFS or SMB.
  `tools/build-all --formats arch,fedora,goo,munki` hands the Mac step to
  xmat. Linux-side DMG creation stays a Phase 0 option.
- **Staging.** Reuse the goo `[stage]` output (the same Windows game files),
  plus the Mac launcher and a version marker file.
- **`tools/dmg-build <pkgdir> [out]`.** Stage, then
  `hdiutil create -srcfolder <stage> -fs APFS -format <Phase 0 pick> -volname <name>`,
  writing `<name>-<version>.dmg`.
- **`tools/munkipack.py`** (beside `goopack.py`) writes `<file>.dmg.pkginfo`:
  - `name`, `display_name`, `description` (from `summary`),
    `category: Games`.
  - `version`: one mapping shared with GooGet, `goo_version`'s
    normalisation with the release appended as the last part (Steam
    `1.27.b16986367` release 2 becomes `1.27.16986367.2`). Unit tests run it
    through the ported Munki comparator so every Steam build and release
    bump sorts higher.
  - `items_to_copy`: the game folder to `/Applications/Games` (Munki 7
    creates the directory), owned by root, not user-writable. That mirrors
    `C:\Program Files\Games\<dir>`; saves and settings live in each user's
    Wine prefix.
  - `installs`: a `plist` entry pointing at the marker file inside the game
    folder, whose `CFBundleShortVersionString` is the package version.
    That's how Munki tells versions of a plain folder apart.
  - `installed_size` (KB, summed from the staged tree),
    `supported_architectures: [arm64]`, `requires` the runtime item,
    `uninstall_method: remove_copied_items`, `uninstallable: true`.
  - Large items (say over 20 GiB) get a `preinstall_script` that deletes the
    old game folder once the download is done. That brings the upgrade peak
    from about 3x to 2x. The cost: if the copy fails, the game is missing
    until Munki's next run.
- **`package.toml`.** A `[munki]` table like `[goo]`: `disabled`, extra
  `deps`, `destination` override, `min_os`.
- **Icon.** The PNG the `[icon] exe` extraction already produces, written as
  `<file>.dmg.png`.
- **`tools/promote`.** A DMG's name and version come from its sidecar, since
  DMG file names can't be split reliably. Copy the icon, then the sidecar,
  then the DMG last, each as a dotfile renamed into place; remove older
  versions DMG-first.
- **`docs/MUNKI.md`.** Client setup, the free-space rule of thumb (2x the
  game to install, 3x without the preinstall step), and the Gatekeeper step
  for hand-installed DMGs.

## Phase 4: client setup and transport security

Plain HTTP is fine for apt, dnf and pacman because their indexes are
signed. It isn't fine for Munki, so the Macs should reach the Munki repo
over TLS or a tailnet.

**Client setup** (munkitools 7.x from Munki's GitHub releases):

```sh
sudo installer -pkg munkitools-7.x.pkg -target /
sudo defaults write /Library/Preferences/ManagedInstalls SoftwareRepoURL "https://nas.lan/munki/game-packages"
sudo defaults write /Library/Preferences/ManagedInstalls ClientIdentifier game-packages
```

Games then appear as optional installs in Managed Software Center.
Utilities you always want can go under `managed_installs` in a hand-written
manifest.

**Transport choices**

| Option | What it takes | Trade-off |
| --- | --- | --- |
| Caddy in front with `tls internal`; `SoftwareRepoCACertificate` set on each Mac | Caddy reverse-proxying `crossdock serve`; the CA certificate pushed to the Macs | Real TLS on any LAN. One more service. GooGet gains too: it needs `allowunsafeurl` for plain HTTP today. |
| Munki URL on the tailnet or WireGuard address only | `SoftwareRepoURL` set to the NAS's tailnet address; `serve` already allows 100.64.0.0/10 | No certificates. Macs must be on the tailnet; devices on the local LAN can't tamper with catalogs. |
| Plain HTTP on a trusted LAN | Nothing | Anyone on the LAN can hand the Macs a catalog with root scripts. Not for LAN parties. |

crossdock itself needs no TLS for this: it stays in a front proxy, as the
README already says. Two small additions: a "Munki" entry under Clients in
the README, and a check in the macOS CI job that Range and ETag survive the
proxy, so resume still works. (Phase 1's pages already take the scheme from
`X-Forwarded-Proto`, so a Caddy front shows `https://` snippets.)

**Optional:** publish `munki/<repo>/ManagedInstalls.mobileconfig` carrying
`SoftwareRepoURL` and `ClientIdentifier` (from `serve.public_url`). A
double-click sets up a Mac, though macOS still asks for approval in System
Settings.

## Phase 5 (explore): chunked delivery for the largest titles

If Phase 0 shows a typical Steam update changes a small share of a game, a
content-addressed chunk store would replace a 125 GiB re-download with just
the changed chunks. It could also serve every platform from one
deduplicated copy, instead of five full copies per game.

- **Tool.** desync is a Go reimplementation of casync (BSD-3-Clause): it
  chunks blobs or directory trees into a store of content-addressed chunks,
  plus small index files (`.caibx` for a blob, `.caidx` for a tree). It can
  rebuild a blob using existing files as seeds, and unpack a tree through a
  local chunk cache. Which of those suits a 125 GiB folder is part of the
  spike.
- **Chunk size.** casync's defaults (64 KiB average) would mean roughly two
  million chunk files per large game. Game data needs averages in the MiB
  range, which desync lets you set.
- **In crossdock.** A new format whose artifacts are index files, with
  chunks in a store beside `store/`, served under the existing immutable
  cache rule, since chunk names are their hashes.
- **On the Mac.** A Munki `nopkg` item whose install step runs desync
  against the store, with the same version marker as Phase 3 for
  `installs`. On Linux and Windows the same store needs a small wrapper
  you'd write and own.
- **Gate.** Build a one-title prototype only if the Phase 0 measurement
  shows big savings and large titles update often. Otherwise stop at
  Munki + DMG: it already matches what the other formats do.

## Decisions

Seven choices shape the work; the plan's pick is given for each.

- [x] **Landing-page URLs:** decided 2026-11-25: static pages, with the base
  URL filled in per request by `serve` and the generated nginx config, and
  `serve.public_url` to override it. See [Phase 1 as built](#phase-1-as-built).
- [ ] **Munki transport:** Caddy with an internal CA, tailnet-only, or plain
  HTTP on a trusted LAN. Pick: tailnet if the Macs are already on it, Caddy
  otherwise.
- [ ] **Upgrade peak:** a preinstall step that deletes the old copy (2x,
  game briefly missing if the copy fails), a doubled `installed_size`
  (honest check, misleading size in the UI), or accept 3x. Pick: the
  preinstall step above 20 GiB.
- [ ] **DMG format:** UDRO or ULFO, APFS or HFS+, after the Phase 0 numbers.
  Guess: UDRO on APFS.
- [ ] **Which titles get DMGs:** every game, or only those that run on the
  Macs. Pick: only those, given NAS space.
- [ ] **Mac runtime:** which Wine/D3DMetal build the games use, packaged as
  its own Munki item.
- [ ] **DMGs without a sidecar:** publish them as download-only, or refuse
  them. Pick: download-only, which also handles rsync's upload order.

## Sources

- [munki/munki](https://github.com/munki/munki), `main` at 2026-11-18 (CLI
  7.4.2): `shared/network/gurl.swift`, `network/fetch.swift`,
  `updatecheck/download.swift`, `installer/dmg.swift`,
  `installer/installer.swift`, `utils/dmgutils.swift`,
  `utils/versionutils.swift`, `admin/makecatalogslib.swift`
- [Munki wiki: Supported Pkginfo Keys](https://github.com/munki/munki/wiki/Supported-Pkginfo-Keys)
- [munki-dev: Munki 7.4 developer preview](https://groups.google.com/g/munki-dev/c/rO5g5zDf-Rg)
  and [makecatalogs on Linux](https://groups.google.com/g/munki-dev/c/wRvjPpOZJxw)
- [Using Munki with SoftwareRepoCACertificate and client certificates (AFP548)](https://www.afp548.com/?p=386995)
- [pkgbuild(1)](https://keith.github.io/xcode-man-pages/pkgbuild.1.html),
  [productbuild(1)](https://keith.github.io/xcode-man-pages/productbuild.1.html)
  and [Scripting OS X on `--large-payload`](https://scriptingosx.com/2021/10/save-up-to-25-pkg-file-size-with-this-weird-monterey-trick/)
- [Homebrew 5.0.0](https://brew.sh/2025/11/12/homebrew-5.0.0) and
  [Homebrew 6.0.0](https://brew.sh/2026/06/11/homebrew-6.0.0/)
- [WineHQ bug 58946: Homebrew Wine packages deprecated](https://list.winehq.org/mailman3/hyperkitty/list/wine-bugs@winehq.org/thread/YCF5B2H2GCDUR6MSYIA2VFVQIJIOY7PM)
- [Apple: Updates to runtime protection in macOS Sequoia](https://developer.apple.com/news/?id=saqachfa)
- [libdmg-hfsplus README](https://github.com/symbioquine/libdmg-hfsplus) and
  [Firefox's build script for it](https://sources.debian.org/src/firefox/147.0.4-1/taskcluster/scripts/misc/build-dmg-hfsplus.sh)
- [desync on pkg.go.dev](https://pkg.go.dev/github.com/folbricht/desync)
- [google/googet](https://github.com/google/googet): `repo/repo.go`
  (`validateRepoURL`) and `settings/settings.go` for `allowunsafeurl`
- [jm2/crossdock](https://github.com/jm2/crossdock) at `f48cf70` and
  [jm2/game-packages](https://github.com/jm2/game-packages) at `8504a75`
