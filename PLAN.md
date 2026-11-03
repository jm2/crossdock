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

M1 reads `crossdock.json`. The schema below is shown as YAML, which takes
over once third-party dependencies are pulled in (M2); the field names are
the same.

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
2. **M2: hardening + go native.** Swap in `ProtonMail/go-crypto` (native
   signing, while keeping the gpg backend for smartcards), `klauspost/compress/zstd`,
   `ulikunitz/xz`, and YAML config, which drops the gpg/xz/zstd package
   dependencies. Windows: publish without symlink privileges (copy
   fallback for `current` and in-release links), then add Windows to the
   CI test matrix. Metadata cache keyed by sha256, so large repos don't
   re-read every package; `Acquire-By-Hash` for apt; `Valid-Until` as an
   option; validating pacman db output against real `pacman`; a systemd
   path unit and an nginx example; release tooling (GoReleaser, with
   crossdock packaging itself).
3. **M3: RPM** (done, except header signing). A stdlib RPM header parser
   and `primary`/`filelists`/`other.xml.gz` plus a signed `repomd.xml`,
   following createrepo_c's rules: primary files, `rpmlib()` and
   self-provided requires dropped, `pre`, libc.so.6 version pruning,
   Latin-1 fallback, changelog limit and timestamp bumping. Tests compare
   every field with createrepo_c on its own test RPMs (Latin-1/2,
   source, weak deps, EdDSA/RSA/v6-signed). CI runs real dnf5 with
   `repo_gpgcheck=1`. Remaining: sign package headers (an RSA/EdDSA
   header signature in the signature header) so `gpgcheck=1` works
   without `rpmsign`. This needs real rpm in the loop to verify.
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
