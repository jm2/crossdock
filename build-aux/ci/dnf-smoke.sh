#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Real dnf against a signed RPM repo served by `crossdock serve`, for CI
# containers (Fedora's dnf5, EL10's dnf4). It:
#
#   1. makes a key with `crossdock keygen` and publishes the RPM test data
#   2. has dnf verify repomd.xml.asc (repo_gpgcheck=1), list and query the
#      packages, and download one
#   3. checks the repo page, and the download against the signed SHA256SUMS
#   4. checks that a repomd.xml that no longer matches its signature fails
#
#   CROSSDOCK=./crossdock build-aux/ci/dnf-smoke.sh internal/rpm/testdata
#
# Failures are printed as GitHub Actions error annotations.
set -euo pipefail

crossdock=${CROSSDOCK:-crossdock}
testdata=$(realpath "${1:?usage: dnf-smoke.sh <dir of test RPMs>}")
work=$(mktemp -d)
fail() {
  echo "::error title=dnf smoke::$*"
  exit 1
}

cat /etc/system-release
rpm --version
dnf --version | sed -n 1p  # sed, not head: no SIGPIPE under pipefail

mkdir "$work/in"
for f in "$testdata"/*.rpm; do
  case "$f" in */empty-signed-*) ;; *) cp "$f" "$work/in/" ;; esac
done
"$crossdock" keygen -uid "crossdock CI <ci@example.invalid>" -out "$work/signing.asc"
cat > "$work/crossdock.yaml" <<EOF
output: $work/out
signing:
  key_file: $work/signing.asc
repos:
  - name: t
    format: rpm
    source: $work/in
EOF
"$crossdock" publish -c "$work/crossdock.yaml"
"$crossdock" serve -q -c "$work/crossdock.yaml" -listen 127.0.0.1:8080 &
trap 'kill $! 2>/dev/null || true' EXIT
for _ in $(seq 50); do
  curl -fsS -o /dev/null http://127.0.0.1:8080/_crossdock/healthz 2>/dev/null && break
  sleep 0.1
done

# skip_if_unavailable=0: a repo that fails verification must fail the
# command, not be skipped with a warning.
dnf_repo=(--setopt=reposdir=/dev/null "--repofrompath=crossdock,http://127.0.0.1:8080/rpm/t"
  --setopt=crossdock.repo_gpgcheck=1 --setopt=crossdock.gpgcheck=0
  --setopt=crossdock.skip_if_unavailable=0
  --setopt=crossdock.gpgkey=http://127.0.0.1:8080/crossdock.asc --repo=crossdock -y)
dnf "${dnf_repo[@]}" makecache || fail "dnf rejected the signed repo"
# %{full_nevra} is dnf5-only; this format works in dnf4 and dnf5. dnf4
# ends each line itself, so drop the blank lines the \n leaves there.
dnf "${dnf_repo[@]}" repoquery --qf '%{name}-%{evr}.%{arch}\n' | grep . | sort | tee "$work/nevras"
[[ $(grep -vc '\.src$' "$work/nevras") -eq 8 ]] || fail "expected 8 binary packages"
dnf "${dnf_repo[@]}" repoquery --provides Archer | grep -qx 'barb >= 11.22.33-44' || fail "Archer provides"
dnf "${dnf_repo[@]}" repoquery --requires Archer | grep -qx 'fooa <= 2' || fail "Archer requires"
(cd "$work" && dnf "${dnf_repo[@]}" download Rimmer && ls Rimmer-*.rpm) || fail "download"

echo "== repo page and SHA256SUMS, as a person downloading by hand"
# No public_url is set, so serve fills in the address the page came from.
curl -fsS -o "$work/page.html" http://127.0.0.1:8080/rpm/t/ || fail "no repo page"
grep -qF 'baseurl=http://127.0.0.1:8080/rpm/t' "$work/page.html" || fail "repo page lacks the dnf snippet"
mkdir "$work/dl"
for f in crossdock.gpg rpm/t/SHA256SUMS rpm/t/SHA256SUMS.asc; do
  curl -fsS -o "$work/dl/${f##*/}" "http://127.0.0.1:8080/$f" || fail "not served: /$f"
done
if command -v gpgv >/dev/null; then
  gpgv --keyring "$work/dl/crossdock.gpg" "$work/dl/SHA256SUMS.asc" "$work/dl/SHA256SUMS" || fail "SHA256SUMS.asc doesn't verify"
else
  # The EL10 image has no GnuPG tools (dnf checks repomd.xml.asc itself).
  # Fedora's gpgv, Ubuntu's gpgv and Debian's sqv check SHA256SUMS.asc.
  echo "gpgv not installed; SHA256SUMS.asc not checked here"
fi
cp "$work"/Rimmer-*.rpm "$work/dl/"
(cd "$work/dl" && sha256sum -c --ignore-missing SHA256SUMS) || fail "the download doesn't match SHA256SUMS"

echo "== tampered repomd.xml"
cur=$(readlink -f "$work/out/current")
sed -i 's|<revision>|<revision>1|' "$cur/rpm/t/repodata/repomd.xml"
# --refresh: a plain `dnf clean all` doesn't reach --repofrompath caches.
if dnf "${dnf_repo[@]}" --refresh makecache > "$work/tampered" 2>&1; then
  cat "$work/tampered"
  fail "dnf accepted a repomd.xml with a bad signature"
fi
grep -i 'signature' "$work/tampered"
echo "dnf smoke: OK"
