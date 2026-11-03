#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# End-to-end check of the installed crossdock package under real systemd,
# for CI runners only (it needs root, and it changes the machine's apt
# sources). With crossdock already installed from its .deb, it:
#
#   1. checks the account and directories sysusers.d/tmpfiles.d created
#   2. installs a throwaway signing key in /etc/crossdock/signing.asc
#   3. enables crossdock-publish.path and crossdock-serve, and publishes
#   4. uploads a package (rename into incoming/deb), waits for the path
#      unit to publish it, and installs it with apt over HTTP from :8080
#   5. uploads a newer version and checks apt sees the upgrade
#   6. checks the server runs unprivileged, then removes the package
#
#   sudo build-aux/ci/system-service-smoke.sh
#
# Failures are printed as GitHub Actions error annotations.
set -euo pipefail

work=$(mktemp -d)
units=(crossdock-serve.service crossdock-publish.service crossdock-publish.path crossdock-publish.timer)

fail() {
  echo "::error title=system service smoke::$*"
  set +e
  systemctl --no-pager status "${units[@]}"
  journalctl --no-pager -n 200 -u crossdock-serve -u crossdock-publish -u crossdock-publish.path
  find /var/lib/crossdock -maxdepth 5 -printf "%M %u:%g %p\n" | head -100
  exit 1
}

wait_for() { # <seconds> <description> <command...>
  local secs=$1 what=$2
  shift 2
  for _ in $(seq "$secs"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail "timed out after ${secs}s waiting for $what"
}

build_pkg() { # <version> <dest dir>
  local root=$work/pkg-$1
  mkdir -p "$root/DEBIAN" "$root/usr/bin"
  printf 'Package: crossdock-smoke\nVersion: %s\nArchitecture: all\nMaintainer: CI <ci@example.invalid>\nDescription: crossdock smoke test package\n' "$1" > "$root/DEBIAN/control"
  printf '#!/bin/sh\necho crossdock-smoke %s\n' "$1" > "$root/usr/bin/crossdock-smoke"
  chmod 0755 "$root/usr/bin/crossdock-smoke"
  # Build to a dotfile and rename, the way rsync uploads: publish never
  # sees a half-written package.
  dpkg-deb --root-owner-group --build "$root" "$2/.crossdock-smoke_$1.deb.part" >/dev/null
  chown crossdock:crossdock "$2/.crossdock-smoke_$1.deb.part"
  mv "$2/.crossdock-smoke_$1.deb.part" "$2/crossdock-smoke_$1_all.deb"
}

echo "== account and directories"
getent passwd crossdock >/dev/null || fail "no crossdock account"
[[ $(stat -c '%U:%G %a' /var/lib/crossdock/repo) == "crossdock:crossdock 755" ]] || fail "repo dir: $(stat -c '%U:%G %a' /var/lib/crossdock/repo)"
[[ $(stat -c '%U:%G %a' /var/lib/crossdock/incoming/deb) == "crossdock:crossdock 2775" ]] || fail "incoming dir: $(stat -c '%U:%G %a' /var/lib/crossdock/incoming/deb)"
for u in "${units[@]}"; do
  [[ $(systemctl is-enabled "$u" 2>/dev/null || true) != enabled ]] || fail "$u is enabled at install"
done
systemd-analyze verify "${units[@]/#//usr/lib/systemd/system/}" || fail "systemd-analyze verify"

echo "== signing key"
gnupg=$work/gnupg
mkdir -m 0700 "$gnupg"
gpg --homedir "$gnupg" --batch --passphrase '' --quick-gen-key "crossdock CI <ci@example.invalid>" ed25519 sign never
install -m 0600 /dev/null /etc/crossdock/signing.asc
gpg --homedir "$gnupg" --batch --armor --export-secret-keys > /etc/crossdock/signing.asc
gpgconf --homedir "$gnupg" --kill all || true

echo "== enable units"
systemctl enable --now crossdock-publish.path crossdock-serve.service
# The first, empty publish by hand, the way the README's setup does it.
systemctl start crossdock-publish.service || fail "the first publish failed"
wait_for 20 "the server's health check" curl -fsS --noproxy '*' http://127.0.0.1:8080/_crossdock/healthz

echo "== upload 1.0 and wait for the path unit to publish it"
build_pkg 1.0 /var/lib/crossdock/incoming/deb
wait_for 90 "a published Packages file listing 1.0" \
  grep -q '^Version: 1.0$' /var/lib/crossdock/repo/current/deb/local/dists/stable/main/binary-amd64/Packages
[[ $(stat -c %U /var/lib/crossdock/repo/current/deb/local/dists/stable/InRelease) == crossdock ]] || fail "release not written by crossdock"

echo "== every format is published and served"
for f in deb/local/dists/stable/InRelease arch/local/x86_64/local.db arch/local/x86_64/local.db.sig \
  rpm/local/repodata/repomd.xml rpm/local/repodata/repomd.xml.asc goo/local/index.gz goo/local/index; do
  curl -fsS --noproxy '*' -o /dev/null "http://127.0.0.1:8080/$f" || fail "not served: /$f"
done

echo "== install with apt over HTTP"
install -d /etc/apt/keyrings
curl -fsS --noproxy '*' -o /etc/apt/keyrings/crossdock.asc http://127.0.0.1:8080/crossdock.asc
sources=$work/sources.list.d
mkdir -p "$sources"
cat > "$sources/crossdock.sources" <<'EOF'
Types: deb
URIs: http://127.0.0.1:8080/deb/local
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/crossdock.asc
EOF
apt_opts=(-o "Dir::Etc::sourcelist=/dev/null" -o "Dir::Etc::sourceparts=$sources" -o "Acquire::http::Proxy=DIRECT")
apt-get "${apt_opts[@]}" update 2>&1 | tee "$work/update.log"
if grep -qE 'NO_PUBKEY|not signed|^(W|E):' "$work/update.log"; then fail "apt-get update did not verify the repo"; fi
DEBIAN_FRONTEND=noninteractive apt-get "${apt_opts[@]}" install -y crossdock-smoke
[[ $(crossdock-smoke) == "crossdock-smoke 1.0" ]] || fail "installed package runs as: $(crossdock-smoke)"

echo "== upload 1.1 and upgrade"
build_pkg 1.1 /var/lib/crossdock/incoming/deb
wait_for 90 "a published Packages file listing 1.1" \
  grep -q '^Version: 1.1$' /var/lib/crossdock/repo/current/deb/local/dists/stable/main/binary-amd64/Packages
apt-get "${apt_opts[@]}" update >/dev/null
DEBIAN_FRONTEND=noninteractive apt-get "${apt_opts[@]}" install -y --only-upgrade crossdock-smoke
[[ $(crossdock-smoke) == "crossdock-smoke 1.1" ]] || fail "upgraded package runs as: $(crossdock-smoke)"

echo "== the server is unprivileged"
pid=$(systemctl show -p MainPID --value crossdock-serve)
uid=$(awk '/^Uid:/ {print $2}' "/proc/$pid/status")
[[ $uid != 0 ]] || fail "crossdock serve runs as root"
systemd-analyze security --no-pager crossdock-serve.service | tail -1
systemd-analyze security --no-pager crossdock-publish.service | tail -1

echo "== remove"
DEBIAN_FRONTEND=noninteractive apt-get "${apt_opts[@]}" remove -y crossdock-smoke >/dev/null
dpkg -r crossdock
for u in crossdock-serve.service crossdock-publish.path; do
  [[ $(systemctl is-active "$u" 2>/dev/null || true) != active ]] || fail "$u still active after removal"
done
[[ -d /var/lib/crossdock/repo/current ]] || fail "removal deleted the published repos"
echo "system service smoke test passed"
