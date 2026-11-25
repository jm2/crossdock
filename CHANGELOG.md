# Changelog

All notable changes to crossdock are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Munki repositories** for macOS. `format: munki` publishes `munki/<repo>/` from disk
  images (`<file>.dmg`) and pkginfo sidecars (`<file>.dmg.pkginfo`, as `makepkginfo` writes
  them) for Munki's `copy_from_dmg` installs, with optional icons (`<file>.dmg.png`). It writes
  `catalogs/` as Munki's `makecatalogs` does (CI compares them with Munki 7.4's Linux build),
  `pkgsinfo/`, `icons/_icon_hashes.plist`, a manifest offering every item (`manifests/<repo>`,
  and `manifests/site_default` with `site_default: true`), and hand-written manifests from
  `manifests_source`. crossdock checks each image's UDIF trailer and never reads inside it;
  it sets `installer_item_location`, `installer_item_hash` and `installer_item_size`, refuses
  `PackageURL`, and passes everything else through. A disk image without a sidecar is
  published for download only. CI installs, upgrades and removes an item with the real Munki
  client on macOS, including a resumed download and a disk image refused on its hash.
- `crossdock inspect` reads `.dmg` files: the UDIF trailer and the pkginfo publish would write.

- **Browseable repos.** Every publish writes a landing page (`index.html` at the release root)
  with the signing key's fingerprint and a ready-to-paste setup snippet for each repo (apt,
  pacman, dnf, GooGet), and an `index.html` per repo listing each package's version,
  architecture, size and SHA-256 with a download link. A deb split into `<name>-dataN` parts is
  listed under its package with the one `apt install` line that needs them all.
- Each repo gets `SHA256SUMS`, in `sha256sum -c` format, and `SHA256SUMS.asc`, a detached
  signature by the repo key, so packages downloaded by hand can be checked with `gpgv` or `sqv`.
- `serve.public_url` sets the address the snippets use. Without it, `serve` and the generated
  nginx config fill in the address each page was requested at.
- `serve`'s directory listings show directories first, sizes and UTC times. A directory's
  `index.html` is served in place of the listing.

### Changed

- Directory listings are on by default in `serve` and `nginx-config`, and the landing page is
  published by default. Existing configs have no `index` key, so this applies to them on
  upgrade. On a host the internet can reach, set `serve.index: false`: that turns listings off
  and leaves out the landing page, the only page that names every repo. Each repo's indexes
  already list its packages to anyone who can reach them, and so do its page and `SHA256SUMS`.
  `-index=false` turns listings off for one `serve` or `nginx-config` run.
- `nginx-config` needs `ngx_http_sub_module` (`sub_filter`), which the Debian, Fedora, EL,
  Arch and nginx.org packages include, and serves `index.html` for directories.
- The packages create and watch `/var/lib/crossdock/incoming/munki`, and the packaged config
  has a commented-out munki repo.

## [0.2.3] — 2026-11-12

### Added

- Windows on ARM: releases include `crossdock-<version>-windows-arm64.zip`.

### Security

- Builds use Go 1.27.4, which fixes GO-2026-6859 in `net/http`.

## [0.2.2] — 2026-11-12

### Fixed

- Publish no longer fails when a package is replaced or removed while it reads the sources
  (an upload swapping one version for the next). If the sources changed, the failed round is
  dropped and publish runs again, up to the same limit of rounds as for other changes.

## [0.2.1] — 2026-11-11

### Changed

- Arch: when a source holds several versions of one package (same name and architecture),
  the publish keeps the newest, compared as pacman's `vercmp` does, and logs the older files as
  skipped. Before, it failed, and that stopped every repo from publishing. Two different files
  with the same version are still an error.

### Fixed

- `publish` publishes again when the sources changed while it ran, until they hold still (at
  most five rounds). Uploads that landed during a long publish used to wait for the next change,
  because `crossdock-publish.path` doesn't start the service again while it's running. Later
  rounds are quick, as unchanged packages are cached. ([#7](https://github.com/jm2/crossdock/issues/7))
- GooGet: a pkgspec dependency with an empty version is accepted as "any version", as googet
  itself does, instead of failing the publish with `empty version`.

## [0.2.0] — 2026-11-06

### Added

- **RPM repositories** — `format: rpm` publishes `rpm/<repo>/` with `Packages/` and `repodata/`
  (`primary`, `filelists`, `other`, and a signed `repomd.xml`), following createrepo_c's rules,
  for dnf with `repo_gpgcheck=1`. Package headers aren't re-signed; sign packages with `rpmsign`
  for `gpgcheck=1`.
- **GooGet repositories** — `format: goo` publishes `goo/<repo>/index` and `index.gz` for
  `googet addrepo`, keeping each package's pkgspec verbatim.
- `crossdock inspect` reads `.rpm` and `.goo` files.
- The packaged config, `/var/lib/crossdock/incoming` and `crossdock-publish.path` now cover all
  four formats.
- `serve` and `nginx-config` cache content-addressed `repodata/<sha256>-*.xml.gz` files as
  immutable and serve `.goo` as `application/gzip`.
- **Incremental publishing** — `<output>/cache.json` remembers each package's digest and parsed
  metadata (by path, size and mtime), so unchanged packages aren't reread, rehashed or copied.
- **apt `Acquire-By-Hash`**, keeping the previous generation's indexes reachable through a swap,
  and **`valid_for`** for `Valid-Until`, with a new (disabled) `crossdock-publish.timer` that
  republishes daily.
- **`no_symlinks`** — publish without symlinks (in-release links become hardlinks or copies;
  `current.id` records the current release). It's used automatically where symlinks can't be
  created, and always on Windows.
- **`crossdock keygen`** creates an Ed25519 signing key, optionally passphrase-protected, and
  `signing.passphrase_file` reads a passphrase from a file such as a systemd credential.
- `signing.key_id` can pick one key from a `key_file` that holds several.
- CI runs the real client for every supported target against a signed repo, and checks that a
  tampered index is rejected: apt on Debian stable (sqv) and Ubuntu (gpgv), dnf on Fedora 44
  and 45 and AlmaLinux 10, and pacman on Arch.
- Dependabot auto-merges its grouped minor and patch updates to Go modules and Actions once CI
  passes. Major and security updates (which skip the cooldown) wait for review.

- `crossdock systemd-config` generates the drop-ins and tmpfiles entry for a config whose paths
  aren't under `/var/lib/crossdock`: `ReadWritePaths`, the watched directories and
  `RequiresMountsFor`.
- `allow_empty: true` on a repo lets it be published empty after it has had packages.

### Changed

- **Packages are hardlinked into the store** when the source shares a file system with `output`,
  so they no longer take twice the space. Publish warns if it has to copy. Replace sources by
  rename, not by writing into them. A truncated store object is now replaced, not reused.
- **A publish that's interrupted resumes.** Progress is saved every minute and on failure, and
  `crossdock-publish.service` no longer has a 30-minute start timeout. Releases left by a killed
  publish no longer count towards `keep_releases`.
- **A repo that has packages won't be published empty**, as an unmounted disk would make it look,
  unless it sets `allow_empty`. Symlinked packages, once skipped silently, now fail the publish.
- `sign_packages` is off in the packaged config and example: it's unsuitable for large packages,
  and a signed database covers package contents when clients require its signature. pacman clients
  need
  `SigLevel = Required DatabaseRequired PackageOptional`.
- `crossdock-publish.service` can write all of `/var/lib/crossdock`, not just `repo/`, because
  hardlinks can't cross the separate mounts `ReadWritePaths` makes. Both services now require
  that path's mount.
- **No host tools needed.** Signing, `keygen`, and xz and zstd decoding are native Go
  (ProtonMail/go-crypto, ulikunitz/xz, klauspost/compress). The packages no longer depend on
  gnupg, xz or zstd. `gpg` is only used, and only suggested, for `signing.gnupg_home`
  (agent and smartcard keys).
- **The config is YAML**: `crossdock.yaml`, and `/etc/crossdock/crossdock.yaml` in the
  packages. Unknown and duplicate keys are errors with line and column. JSON configs still load.
- Keys are v4 Ed25519 with SHA-512 signatures, which every supported client accepts. GnuPG
  (pacman, EL10's dnf) rejects RFC 9580 v6 keys.
- Supported targets are current releases only: Debian 13+, Fedora 44/45, EL10.2+ and Arch.
  Building needs Go 1.27.3 (`GOTOOLCHAIN=auto` fetches it).
- The store is garbage-collected from per-release manifests rather than hardlink counts.

### Fixed

- Windows always publishes without symlinks. With symlink rights, the `current` swap failed
  with "Access is denied".
- Release paths are checked with `filepath.IsLocal`, which also rejects rooted paths and
  reserved names on Windows.

## [0.1.0] — 2026-11-03

### Added

- **Atomic, declarative publishing** — `crossdock publish` rebuilds every configured repo from its
  source directory into a new release directory and swaps `current` with a single rename, so
  clients never see a half-written index. Package payloads are stored once by SHA-256 and
  hardlinked into releases; old releases and unreferenced payloads are pruned (`keep_releases`).
- **apt repositories** — `Packages`/`Packages.gz`, `Release`, `Release.gpg` and `InRelease` in the
  standard pool layout. `Architecture: all` packages are listed under every architecture.
- **pacman repositories** — signed `<repo>.db` and `<repo>.files` built the way `repo-add` builds
  them. `any` packages land in every architecture directory, and missing package `.sig` files can
  be created (`sign_packages`).
- **`crossdock serve`** — a read-only HTTP server for the published tree, listening on `:8080` by
  default. It resolves the release once per request, confines file access to the release, and
  derives caching and content types from the same rules as **`crossdock nginx-config`**.
- **Packages for Fedora, Debian/Ubuntu and Arch** — `.rpm` (also built by COPR), `.deb` and
  `.pkg.tar.zst`. They ship the `crossdock` account (sysusers.d) and `/var/lib/crossdock`
  (tmpfiles.d); `crossdock-serve.service`, LAN-only by default; `crossdock-publish.path`, which
  republishes when packages are uploaded; and a firewalld service. Nothing is enabled at install.
- `crossdock inspect` prints the metadata crossdock reads from `.deb` and pacman packages.

[Unreleased]: https://github.com/jm2/crossdock/compare/v0.2.3...HEAD
[0.2.3]: https://github.com/jm2/crossdock/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/jm2/crossdock/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/jm2/crossdock/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/jm2/crossdock/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jm2/crossdock/releases/tag/v0.1.0
