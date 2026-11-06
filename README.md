# crossdock

Publish one pile of package artifacts as signed, static **apt**, **pacman**,
**RPM** and **GooGet** repositories, from a single Go binary with no distro
tooling and no containers.

> Status: alpha. All four formats publish. apt is tested end to end with
> real apt-get. RPM repodata is checked field for field against
> createrepo_c, and CI runs real dnf and pacman against it. RPM package
> header signing and a live GooGet client test are still to come. See
> [PLAN.md](PLAN.md).

## Install

Release packages are on the [releases page](https://github.com/jm2/crossdock/releases):

```sh
sudo dnf install ./crossdock-x86_64.rpm            # Fedora (or: dnf copr enable jmsqrd/crossdock)
sudo apt install ./crossdock-amd64.deb             # Debian, Ubuntu
sudo pacman -U crossdock-x86_64.pkg.tar.zst        # Arch (or the AUR's crossdock-bin)
go install github.com/jm2/crossdock/cmd/crossdock@latest   # anywhere, binary only
```

The packages install:

| | |
|---|---|
| `/usr/bin/crossdock` | the binary |
| `/etc/crossdock/crossdock.yaml` | config: `deb`, `arch`, `rpm` and `goo` repos named `local`, served on `:8080` (kept on upgrade) |
| `crossdock-serve.service` | the read-only server, as a throwaway `DynamicUser`, **LAN-only** |
| `crossdock-publish.service` / `.path` | republish when files land in `/var/lib/crossdock/incoming/{deb,arch,rpm,goo}` |
| `crossdock` account (sysusers.d) | owns `/var/lib/crossdock/repo`; members of its group can upload |
| `/var/lib/crossdock` (tmpfiles.d) | `repo/` (published, world-readable) and `incoming/` (setgid, group-writable) |
| firewalld `crossdock` service | opens 8080/tcp |

Nothing is enabled at install.

## Run it as a service (no web server needed)

```sh
# 1. A signing key. Keep it root-only; systemd hands it to the publisher
#    as a credential, so the crossdock account itself can't read it.
sudo crossdock keygen -uid "My repo <repo@example.lan>" -out /etc/crossdock/signing.asc

# 2. Start serving and auto-publishing, then publish once.
sudo systemctl enable --now crossdock-serve crossdock-publish.path
sudo systemctl enable --now crossdock-publish.timer   # only if a deb repo sets valid_for
sudo systemctl start crossdock-publish
sudo firewall-cmd --permanent --add-service=crossdock && sudo firewall-cmd --reload   # Fedora

# 3. Upload. Use rsync (or copy to a dotfile and rename), so publish never
#    reads a half-written file.
sudo usermod -aG crossdock "$USER"     # log in again afterwards
rsync -av --no-g --chmod=F664 *.deb nas:/var/lib/crossdock/incoming/deb/
rsync -av --no-g --chmod=F664 *.pkg.tar.zst nas:/var/lib/crossdock/incoming/arch/
rsync -av --no-g --chmod=F664 *.rpm nas:/var/lib/crossdock/incoming/rpm/
rsync -av --no-g --chmod=F664 *.goo nas:/var/lib/crossdock/incoming/goo/
```

Clients then use `http://nas:8080/deb/local`,
`http://nas:8080/arch/$repo/$arch`, `http://nas:8080/rpm/local` and
`http://nas:8080/goo/local` (see [Clients](#clients)).

**LAN only by default.** `crossdock-serve` listens on every interface on
port 8080, but systemd's IP filtering only accepts loopback, link-local,
RFC 1918, 100.64.0.0/10 (CGNAT/Tailscale) and IPv6 ULA clients. To change
the port, set `serve.listen` in the config. To allow other networks, use a
drop-in (`sudo systemctl edit crossdock-serve`):

```ini
[Service]
IPAddressAllow=203.0.113.0/24      # or: IPAddressAllow=any
```

To serve on port 80 without a proxy, set `"listen": ":80"` and add
`AmbientCapabilities=CAP_NET_BIND_SERVICE` and
`CapabilityBoundingSet=CAP_NET_BIND_SERVICE` in the same drop-in.

There's no TLS. apt and pacman verify the repos' signatures, so plain HTTP
is fine on a LAN. For the internet, put nginx or Caddy in front (see
[Serving](#serving)).

## Run it by hand

```sh
crossdock publish -c crossdock.yaml          # see examples/crossdock.yaml
crossdock serve -c crossdock.yaml
crossdock inspect some.deb some.pkg.tar.zst  # show parsed metadata as JSON
```

Each publish builds a fresh release directory, swaps `current` atomically,
and prunes old releases (`keep_releases`, default 3). If a publish fails,
the previous release stays live. Package payloads are deduplicated in
`<output>/store` and hardlinked into releases. Paths in the config can
use environment variables, e.g. `${CREDENTIALS_DIRECTORY}/signing.asc`.
The config is YAML. Unknown or duplicate keys are errors, reported with
the line and column. JSON is valid YAML, so a JSON config also works.

Publishing is incremental. `<output>/cache.json` remembers each package's
digest and parsed metadata by path, size and mtime, so an unchanged
package isn't reread, rehashed or copied. Republishing a 400 MiB package
takes milliseconds.

### Disk use

Package payloads live in `<output>/store`, hardlinked from the source
directory where possible, so a package normally takes no extra space
however many releases keep it. A hardlink needs all of these; otherwise
the package is copied, doubling its disk use, and publish logs a warning
once:

- The source and `<output>` are on the same file system and, for a
  sandboxed service, the same mount. `crossdock systemd-config` arranges
  this when they share a parent directory.
- The file is world-readable, since `serve` runs as another account.
- The publishing account owns the file or can write to it
  (`fs.protected_hardlinks`). Uploads into the packaged `incoming`
  directories get group `crossdock`, so keep that group and make them
  group-writable: `rsync -av --no-g --chmod=F664` (`--no-g` must follow
  `-a`).

A hardlinked store object is the same file as the source. Replace sources
by rename, as rsync and `mv` do. Don't overwrite one in place (`cp` onto an
existing file): that changes what's already published. It also means anyone
who can write to `incoming` can change published packages, so only give
trusted accounts upload access. Clients that verify signatures reject a
changed package.

Run one publish at a time per `output`: a second one waits for the first.

### Large repos

The first publish reads every package, and with hundreds of GiB that takes
a while, so `crossdock-publish.service` has no start timeout. Progress is
saved every minute and when a publish fails, so a publish that's stopped or
killed picks up where it left off.

An empty source is usually a storage problem, so a repo that has packages
won't be published empty: publish fails, for every repo, and the current
release stays live. If you really mean to empty one, set `allow_empty: true`
on that repo. A repo that has never had packages may stay empty.

Packages and their `.sig` files must be regular files. A symlink fails the
publish, because it could expose any file the publisher can read.

### Storing repos elsewhere

`output` and each repo's `source` can be anywhere the sandboxed services can
see, so not under `/home`, `/root`, `/run/user`, `/tmp` or `/var/tmp`. The packaged units,
path watch and tmpfiles entry assume `/var/lib/crossdock`, so after pointing
the config somewhere else, generate matching overrides:

```sh
sudo crossdock systemd-config -root /      # omit -root to preview
sudo systemd-tmpfiles --create /etc/tmpfiles.d/crossdock-paths.conf
sudo systemctl daemon-reload
```

This sets the publisher's `ReadWritePaths` and the path unit's watched
directories, adds `RequiresMountsFor` so the services wait for the disk, and
creates the directories. Put `output` and the sources under one parent, such
as `/media/data/crossdock/{repo,incoming/arch}`, so packages can be hardlinked.
That parent can't be a shared directory such as `/srv` or `/var/lib`, which
would open too much to the publisher. Rerun it whenever those paths change.

The disk must be in `/etc/fstab`, or `RequiresMountsFor` can't tell that it's
missing. The empty-repo check can't help when `output` is on that disk too:
with no previous release to compare with, it looks like a new repo.

Config options worth knowing:

| Option | Effect |
|---|---|
| `no_symlinks: true` | In-release links become hardlinks or copies, and the current release goes in `<output>/current.id`. Use it for SMB shares, object-store syncs, or anything that won't follow links. `crossdock serve` handles either; static web servers need symlinks. Windows always publishes this way. |
| `valid_for: 7d` (deb repos) | Adds `Valid-Until` to `Release`, so clients reject stale or replayed indexes. Enable `crossdock-publish.timer` so the repo is republished before it expires. |
| `passphrase_file` (signing) | Reads the key's passphrase from a file, e.g. a systemd credential. |
| `allow_empty: true` (any repo) | Lets a repo that has packages be published empty. See [Large repos](#large-repos). |
| `sign_packages: true` (arch repos) | Creates a `.sig` for each package without one. Off by default: with a signing key and clients requiring database signatures (`DatabaseRequired`), the database pins every package's SHA-256. It holds each package in memory and re-signs them all on every publish, so don't use it with multi-GiB packages. |
| `gnupg_home` + `key_id` (signing) | Signs through `gpg` with an existing keyring instead of `key_file`, for gpg-agent and smartcard keys. This is the only feature that needs `gpg` installed. |

apt repos always set `Acquire-By-Hash: yes` and keep the previous
generation's indexes under `by-hash/`, so a client that read the old
`InRelease` just before a swap still gets matching indexes.

## Serving

The output is plain static files, so any web server or object store
works. crossdock ships two ways to serve it with the right headers:

```sh
crossdock serve -c crossdock.yaml                    # built-in, read-only, :8080
crossdock nginx-config -c crossdock.yaml -server-name repo.example > /etc/nginx/conf.d/crossdock.conf
```

Both are generated from one rules table (`internal/serve/rules.go`), so
they behave identically:

| Paths | Cache-Control |
|---|---|
| `InRelease`, `Release*`, `Packages*`, `repomd.xml*`, `*.db`/`*.files` (+`.sig`), GooGet `index`, `/crossdock.{asc,gpg}` | `no-cache` (revalidate via ETag/Last-Modified) |
| `.deb`, `.rpm`, `.goo`, `.pkg.tar.*` (+`.sig`) | `public, max-age=86400` |
| `…/by-hash/…` | `public, max-age=31536000, immutable` |
| everything else | `public, max-age=300` |

`serve` also:
- resolves `current` once per request, so a publish swaps in between
  requests and never during one. Each response carries
  `X-Crossdock-Release`.
- confines file access to the release with `os.Root`, so symlinks can't
  escape it, and refuses dotfiles and non-GET/HEAD methods.
- supports range and conditional requests, an optional `-index` listing,
  an access log (`-q` turns it off), and graceful shutdown on
  SIGINT/SIGTERM.
- exposes `/_crossdock/healthz` and `/_crossdock/release` (JSON).

On a LAN, `serve` alone is enough (see
[Run it as a service](#run-it-as-a-service-no-web-server-needed)). On the internet, terminate TLS in
nginx or Caddy and either serve the directory there with the generated
config, or reverse-proxy to `serve`.

## Clients

crossdock targets current releases only: Debian stable (13) and newer,
Fedora 44 and 45, EL10.2 and newer, and Arch. CI runs each one's real
client against a signed crossdock repo: apt on Debian stable (Sequoia's
`sqv`) and Ubuntu 24.04 (`gpgv`), dnf5 on Fedora 44 and 45, dnf4 on
AlmaLinux 10, and pacman on Arch.

Keys from `crossdock keygen` are v4 Ed25519. Every one of those clients
accepts that format. The newer RFC 9580 v6 keys aren't used because
GnuPG, which pacman and EL10's dnf verify with, rejects them.

apt (`/etc/apt/sources.list.d/jm2.sources`):

```
Types: deb
URIs: https://repo.example/deb/jm2
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/crossdock.asc
```

pacman (`/etc/pacman.conf`):

```
[jm2]
SigLevel = Required DatabaseRequired PackageOptional
Server = https://repo.example/arch/$repo/$arch
```

The trusted database signature covers every package's SHA-256, which is why
`DatabaseRequired` matters here. `PackageOptional` is needed unless packages carry their own signatures (`sign_packages`, or
`makepkg --sign`).

dnf (`/etc/yum.repos.d/jm2.repo`). One RPM repo holds every architecture,
and dnf picks what it can install:

```ini
[jm2]
name=jm2
baseurl=https://repo.example/rpm/jm2
repo_gpgcheck=1
gpgcheck=0
gpgkey=https://repo.example/crossdock.asc
```

`repo_gpgcheck=1` verifies the signed `repomd.xml`, which pins every
package's SHA-256. crossdock doesn't sign RPM headers itself yet. To use
`gpgcheck=1`, sign packages at build time with `rpmsign` (or let COPR do
it).

**Fedora 45 needs signed packages.** Its rpm 6.1 defaults to
`%_pkgverify_level all`, which requires a valid signature on every
package, so unsigned RPMs from any repo won't install there. Sign them
with `rpmsign` and use `gpgcheck=1`. Fedora 44 and EL10 (`digest`) still
install unsigned packages with `gpgcheck=0`.

GooGet (Windows):

```
googet addrepo jm2 https://repo.example/goo/jm2
googet available
```

The index pins each package's SHA-256, but GooGet has no index
signatures. Serve it over HTTPS, or keep it on a trusted LAN.

All clients get the public key from `https://repo.example/crossdock.asc`.
For pacman, add it with `pacman-key --add` and `--lsign-key`.

| Format | Layout under `current/` |
|---|---|
| apt | `deb/<repo>/dists/<suite>/…`, `deb/<repo>/pool/<component>/…` |
| pacman | `arch/<repo>/<arch>/<repo>.{db,files}` + packages |
| RPM | `rpm/<repo>/repodata/`, `rpm/<repo>/Packages/<l>/…` |
| GooGet | `goo/<repo>/index{,.gz}`, `goo/<repo>/packages/<name>.<arch>.<ver>.goo` |

## Requirements

None at run time. crossdock is one static binary. Signing, `crossdock
keygen` (via [ProtonMail/go-crypto](https://github.com/ProtonMail/go-crypto)),
xz and zstd decoding ([ulikunitz/xz](https://github.com/ulikunitz/xz),
[klauspost/compress](https://github.com/klauspost/compress)) and the
YAML config ([goccy/go-yaml](https://github.com/goccy/go-yaml)) are all
native Go. `gpg` is needed only for `gnupg_home` signing, so the packages
only suggest it.

Building needs Go 1.27.3 or newer (`go.mod`). Older `go` commands fetch it
themselves with `GOTOOLCHAIN=auto`.

## Development

```sh
make test        # vet + go test -race; the apt end-to-end test runs when dpkg-deb and apt-get exist,
                 # and the RPM tests check against createrepo_c when `pip install createrepo_c` is done
make cross       # dist/crossdock-<os>-<arch>
make deb ARCH=amd64
```

Packaging lives in `build-aux/`, and every package installs the same tree
through `build-aux/linux/install-tree.sh`. CI installs the `.deb` on a
runner with real systemd and runs `build-aux/ci/system-service-smoke.sh`.
That test does a path-triggered publish, then has apt install and upgrade
from `:8080`. `build-aux/ci/dnf-smoke.sh` and `pacman-smoke.sh` run the
real dnf and pacman against signed repos in Fedora 44/45, AlmaLinux 10 and
Arch containers, and check that a tampered index is rejected. Dependabot
keeps Go modules and Actions current, and auto-merges its grouped minor
and patch updates once the required checks pass. Major and security
updates wait for review. Tagging `vX.Y.Z` with a matching `CHANGELOG.md` section
builds the archives, `.deb`, `.rpm` (Fedora 44) and Arch package, then
creates a draft release with `SHA256SUMS.txt` and provenance attestations.
The Arch job also syncs crossdock's own repo with real pacman.

## License

crossdock is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version. See [LICENSE](LICENSE).

SPDX-License-Identifier: GPL-3.0-or-later

