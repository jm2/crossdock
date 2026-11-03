# crossdock

Publish one pile of package artifacts as signed, static **apt**, **pacman**,
**RPM** and **GooGet** repositories, from a single Go binary with no distro
tooling and no containers.

> Status: M1. apt and pacman work; RPM and GooGet are planned. See
> [PLAN.md](PLAN.md).

## Quick start

```sh
go install github.com/jm2/crossdock/cmd/crossdock@latest

cp examples/crossdock.json /etc/crossdock/crossdock.json   # edit paths
crossdock publish -c /etc/crossdock/crossdock.json
```

Point your web server at `<output>/current`. Each publish builds a fresh
release directory and swaps `current` atomically, then prunes old releases
(`keep_releases`, default 3). Package payloads are deduplicated in
`<output>/store` and hardlinked into releases.

```sh
crossdock inspect some.deb some.pkg.tar.zst   # show parsed metadata as JSON
```

## Serving

The output is plain static files, so any web server or object store
works. crossdock ships two ways to serve it with the right headers:

```sh
crossdock serve -c crossdock.json -listen :8080      # built-in, read-only
crossdock nginx-config -c crossdock.json -server-name repo.example > /etc/nginx/conf.d/crossdock.conf
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
`examples/crossdock-serve.service`). On the internet, terminate TLS in
nginx or Caddy and either serve the directory there with the generated
config, or reverse-proxy to `serve`.

## Clients

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
Server = https://repo.example/arch/$repo/$arch
```

Both clients get the public key from `https://repo.example/crossdock.asc`.
For pacman, add it with `pacman-key --add` and `--lsign-key`.

## Requirements (M1)

- `gpg` for signing (any key gpg can use, including smartcards via
  `gnupg_home` + `key_id`).
- `xz` / `zstd` binaries when reading xz- or zstd-compressed packages.

Both requirements go away in M2.

## Development

```sh
go test ./...   # the apt end-to-end test runs when gpg, dpkg-deb and apt-get exist
```

## License

crossdock is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version. See [LICENSE](LICENSE).

SPDX-License-Identifier: GPL-3.0-or-later

