# Changelog

All notable changes to crossdock are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/jm2/crossdock/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jm2/crossdock/releases/tag/v0.1.0
