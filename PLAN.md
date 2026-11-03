# crossdock — plan

crossdock takes one set of built package artifacts and publishes them as
signed, static package repositories for apt (Debian/Ubuntu), pacman (Arch),
dnf/yum (RPM), and GooGet (Windows). It runs anywhere as a single static Go
binary, with no distro tooling (`reprepro`, `createrepo_c`, `repo-add`,
`rpmsign`) required on the host.

## Goals

- **One binary, any host.** It runs on a NAS, in CI, on macOS or on Windows,
  with no containers, no `dpkg`, no `pacman`, no `rpm`, and no `gpg` binary
  required.
- **Declarative.** A repo's contents are the package files in a source directory.
  Every publish rebuilds all indexes from that set, so publishing is
  idempotent and the output is reproducible from the inputs.
- **Atomic.** Clients never see a half-written index. Each publish builds a new
  release directory, then swaps a `current` symlink with a single rename.
- **Signed by default.** One OpenPGP key covers everything; each format gets
  the signing scheme its client expects.
- **Static output.** Any web server that can serve files and follow symlinks
  (nginx, Caddy, `python -m http.server`) is enough.

## Supported targets

Only current releases: Debian stable (13) and newer, Fedora 44 and 45,
EL10.2 and newer, Arch, and Windows for GooGet. No compatibility code for
older clients. Dependencies and the Go toolchain track the latest releases
(`go 1.27.3`). The one deliberate exception is the key format.
Signing keys stay v4 Ed25519 with SHA-512 signatures, because GnuPG
(pacman, and gpgme in EL10's librepo) rejects RFC 9580 v6 keys; apt's sqv
and rpm-sequoia would accept them.

## Non-goals

- Building packages from source. Use `makepkg`, `dpkg-buildpackage` and
  `rpmbuild` in CI, or nFPM for repackaging prebuilt binaries. crossdock only
  ingests what those tools produce.
- Being a package server with an API, auth or uploads. Uploading means dropping
  files into the source dir; a systemd path unit or a CI step runs `publish`.
- Replacing aptly's mirroring and snapshot features.

## Output layout

```
<output>/
  current -> releases/20261102T210000Z-ab12cd   # web root points here
  releases/<id>/                               # immutable once swapped in
    crossdock.asc, crossdock.gpg               # public key (armored / binary)
    deb/<repo>/dists/<suite>/{Release,Release.gpg,InRelease}
    deb/<repo>/dists/<suite>/<component>/binary-<arch>/Packages{,.gz}
    deb/<repo>/pool/<component>/<p>/<source>/<file>.deb
    arch/<repo>/<arch>/{<repo>.db,<repo>.files}[.tar.gz][.sig] + packages
    rpm/<repo>/<arch>/{repodata/,*.rpm}        # milestone 3
    goo/<repo>/{index,packages/*.goo}          # milestone 4
  store/<sha256>                               # content store, hardlinked into releases
```

Package payloads are copied into `store/` once and hardlinked into each
release, so keeping N releases costs only their metadata. After a publish,
releases older than `keep_releases` are pruned, along with any store objects
no release still links.

Client config examples:

```
# apt
deb [signed-by=/etc/apt/keyrings/crossdock.asc] https://repo.example/deb/jm2 stable main
# pacman
[jm2]
Server = https://repo.example/arch/$repo/$arch
```

## Configuration

`crossdock.yaml` (goccy/go-yaml, with unknown and duplicate keys
rejected). JSON configs still load, since JSON is valid YAML.

```yaml
output: /srv/crossdock
keep_releases: 3
signing:
  key_file: /etc/crossdock/signing.asc   # armored private key
  passphrase_env: CROSSDOCK_PASSPHRASE   # optional
repos:
  - name: jm2
    format: arch
    source: /srv/incoming/arch
    architectures: [x86_64, aarch64]     # "any" packages land in each
    sign_packages: true                  # create missing .sig files
  - name: jm2
    format: deb
    source: /srv/incoming/deb
    suite: stable
    component: main
    origin: jm2
    label: jm2
    architectures: [amd64, arm64]        # "all" packages land in each
```

## Architecture

```
cmd/crossdock          CLI: publish, serve, nginx-config, inspect, version
internal/config        YAML load + validation + defaults
internal/publish       release dir, content store, symlink swap, pruning
internal/sign          OpenPGP: detached (binary/armored), clearsign, pubkey export
internal/deb           .deb parsing (ar + control.tar.{gz,xz,zst}), apt index writer
internal/archpkg       .pkg.tar.{zst,xz,gz} parsing (.PKGINFO, file list), pacman db writer
internal/rpm           RPM header parsing, rpm-md repodata (createrepo_c-compatible)
internal/goo           .goo parsing, GooGet index
internal/compress      shared decompressor selection by magic bytes
internal/serve         HTTP handler, shared cache/type rules, nginx config generator
```

Each format backend exposes the same entry point:

```go
// Publish reads packages from repo.Source and writes a complete, signed
// repo tree into env.Release, adding payloads through the content store.
func Publish(repo config.Repo, env *publish.Env) error
```

## Milestones

1. **M1: core + apt + pacman** (done). Config, atomic publish, content
   store, signing, Debian and Arch backends, `inspect`, and an end-to-end
   test in which a real `apt-get` verifies, updates from and downloads from
   a signed repo, and rejects a tampered index.
   M1 was written with **standard library only** (the bootstrap environment
   couldn't reach the Go module proxy). The temporary shortcuts:
   - JSON config instead of YAML.
   - Signing shells out to `gpg` (behind `sign.Signer`).
   - xz and zstd decompression shell out to `xz` / `zstd`, which matters
     for `.pkg.tar.zst` and for zstd/xz-compressed `control.tar` in debs.
     gzip and bzip2 are native.
   **Serving** (done, after M1): `crossdock serve`, a read-only stdlib HTTP
   server, and `crossdock nginx-config`. Both derive caching and content
   types from one rules table. The output stays plain static files, so
   any host (nginx, Caddy, S3/R2) keeps working; `serve` is a convenience,
   not a dependency. TLS stays with a front proxy.
   **Packaging** (done): `.deb`, `.rpm` (COPR via Packit), Arch PKGBUILD
   plus AUR `-bin`, systemd units (`crossdock-serve` as a LAN-only
   DynamicUser on :8080, `crossdock-publish.service` + `.path`),
   sysusers.d/tmpfiles.d, and a firewalld service. The release workflow
   mirrors jm2/switchworks.
2. **M2: hardening.** Done:
   - Incremental publishing: `<output>/cache.json` keeps digests and
     parsed metadata by path+size+mtime, and unchanged packages are
     hardlinked from the store without being reread.
   - Store GC from per-release manifests (`.crossdock/store-objects`)
     instead of hardlink counts, so it works on every platform.
   - apt `Acquire-By-Hash` with one generation carried over, and
     `valid_for` (`Valid-Until`) with `crossdock-publish.timer`.
   - Symlink-free publishing (`no_symlinks`, automatic on Windows), with
     `current.id` and copies or hardlinks for in-release links. The whole
     suite, real apt included, passes with symlinks forced off.
   - `crossdock keygen`.
   - The **go-native swap**. `key_file` signing and `keygen` use
     ProtonMail/go-crypto, which makes v4 Ed25519 keys and SHA-512
     signatures. Clearsigned output is re-armored with a CRC-24 line,
     because GnuPG 2.4 fails `InRelease` without one. gpg remains only
     for `gnupg_home` (agent and smartcard keys). xz and zstd decoding use
     ulikunitz/xz and klauspost/compress/zstd, and the config is YAML
     (goccy/go-yaml). The packages no longer depend on gnupg, xz or zstd.
   - Client CI on every target: apt on Debian stable (sqv) and Ubuntu
     (gpgv), dnf on Fedora 44/45 and AlmaLinux 10, and pacman on Arch.
     Each one rejects a tampered index.
   - Windows always publishes without symlinks. It can't rename a symlink
     over `current`, even when it can create symlinks.
3. **M3: RPM** (done, except header signing). A stdlib RPM header parser
   and `primary`/`filelists`/`other.xml.gz` plus a signed `repomd.xml`,
   following createrepo_c's rules: primary files, `rpmlib()` and
   self-provided requires dropped, `pre`, libc.so.6 version pruning,
   Latin-1 fallback, changelog limit and timestamp bumping. Tests compare
   every field with createrepo_c on its own test RPMs (Latin-1/2,
   source, weak deps, EdDSA/RSA/v6-signed). CI runs real dnf5 with
   `repo_gpgcheck=1`. Remaining: sign package headers (an RSA/EdDSA
   header signature in the signature header) so `gpgcheck=1` works
   without `rpmsign`. This needs real rpm in the loop to verify. It now
   matters more: Fedora 45's rpm 6.1 defaults to `%_pkgverify_level all`
   and won't install unsigned packages at all (Fedora 44 and EL10 use
   `digest`).
4. **M4: GooGet** (done, except a live client test). Reads the
   `<name>.pkgspec` from `.goo` (gzip tarball) with googet's validation
   (arch list, `fixVer` + semver + `@N`). The pkgspec is preserved
   verbatim. `index` and `index.gz` contain `{Checksum, Source,
   PackageSpec}`, with `Source` relative to the repo URL's parent, as
   `download.FromRepo` resolves it. Tests decode the index with googet's
   own struct definitions. Remaining: a Windows CI job running the real
   googet client; optional Authenticode checks on payloads.
5. **M5: packaging glue (optional).** Build packages from one nFPM spec
   (deb/rpm/arch), plus a `.goo` writer, so `-bin` style packages come from one
   definition.

## Open questions

- Should pacman dbs use gzip (`repo-add`'s default, widest compatibility) or
  zstd? Plan: gzip by default, zstd as an option.
- Should signatures be required for every published package, with a hard
  failure if no key is configured? Plan: unsigned only with `--allow-unsigned`.
- Should GooGet use one index per repo or one per arch?
