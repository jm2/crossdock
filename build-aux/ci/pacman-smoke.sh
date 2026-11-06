#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Real pacman against a signed repo served by `crossdock serve`, for an
# Arch CI container (it needs root and changes the pacman keyring). It:
#
#   1. builds a .pkg.tar.zst the way makepkg does (bsdtar, zstd --long)
#   2. makes a key with `crossdock keygen`, publishes with sign_packages,
#      and trusts the key with pacman-key
#   3. syncs and installs with SigLevel = Required DatabaseRequired, so
#      pacman's GnuPG checks both the database and package signatures
#   4. checks that a database that no longer matches its signature fails
#
#   CROSSDOCK=./crossdock build-aux/ci/pacman-smoke.sh
#
# Failures are printed as GitHub Actions error annotations.
set -euo pipefail

crossdock=${CROSSDOCK:-crossdock}
work=$(mktemp -d)
fail() {
  echo "::error title=pacman smoke::$*"
  exit 1
}

pacman --version | sed -n 2p

echo "== build a package"
mkdir -p "$work/in" "$work/pkg/usr/bin"
printf '#!/bin/sh\necho hello from crossdock\n' > "$work/pkg/usr/bin/hello-crossdock"
chmod 755 "$work/pkg/usr/bin/hello-crossdock"
cat > "$work/pkg/.PKGINFO" <<'EOF'
pkgname = hello-crossdock
pkgbase = hello-crossdock
pkgver = 1.0-1
pkgdesc = crossdock pacman smoke test
url = https://github.com/jm2/crossdock
builddate = 1700000000
packager = crossdock CI <ci@example.invalid>
size = 40
arch = x86_64
license = GPL-3.0-or-later
EOF
(cd "$work/pkg" && bsdtar -cf - --uid 0 --gid 0 .PKGINFO usr |
  zstd -q -19 --long=27 -T0 > "$work/in/hello-crossdock-1.0-1-x86_64.pkg.tar.zst")

echo "== publish"
"$crossdock" keygen -uid "crossdock CI <ci@example.invalid>" -out "$work/signing.asc" -pub "$work/pub.asc" | tee "$work/keygen"
fpr=$(awk '/^fingerprint/ {print $2}' "$work/keygen")
cat > "$work/crossdock.yaml" <<EOF
output: $work/out
signing:
  key_file: $work/signing.asc
repos:
  - name: t
    format: arch
    source: $work/in
    architectures: [x86_64]
    sign_packages: true
EOF
"$crossdock" publish -c "$work/crossdock.yaml"
"$crossdock" serve -q -c "$work/crossdock.yaml" -listen 127.0.0.1:8080 &
trap 'kill $! 2>/dev/null || true' EXIT
for _ in $(seq 50); do
  curl -fsS -o /dev/null http://127.0.0.1:8080/_crossdock/healthz 2>/dev/null && break
  sleep 0.1
done

echo "== trust the key and install"
pacman-key --init
pacman-key --add "$work/pub.asc"
pacman-key --lsign-key "$fpr"
cat > "$work/pacman.conf" <<'EOF'
[options]
Architecture = auto
[t]
SigLevel = Required DatabaseRequired
Server = http://127.0.0.1:8080/arch/$repo/$arch
EOF
p=(pacman --config "$work/pacman.conf" --noconfirm)
"${p[@]}" -Sy || fail "pacman rejected the signed database"
"${p[@]}" -S hello-crossdock || fail "pacman rejected the signed package"
[[ $(hello-crossdock) == "hello from crossdock" ]] || fail "installed package doesn't run"

echo "== tampered database"
cur=$(readlink -f "$work/out/current")
db=$(readlink -f "$cur/arch/t/x86_64/t.db")
printf 'x' >> "$db"
if "${p[@]}" -Syy > "$work/tampered" 2>&1; then
  cat "$work/tampered"
  fail "pacman accepted a database with a bad signature"
fi
grep -i 'signature' "$work/tampered"
echo "pacman smoke: OK"
