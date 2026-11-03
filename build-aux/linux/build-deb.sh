#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Builds a .deb from a prebuilt binary with dpkg-deb (no debhelper). Nothing
# is enabled or started at install. The maintainer scripts create the
# crossdock account and its directories, and restart the server on upgrade
# if it's running, as dh_installsysusers, dh_installtmpfiles and
# dh_installsystemd --no-enable would.
#
#   build-aux/linux/build-deb.sh <version> <amd64|arm64> <dir with crossdock> <output .deb>
#
# DEB_MAINTAINER sets the Maintainer field.
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <version> <amd64|arm64> <bindir> <output.deb>" >&2
  exit 2
fi
version=${1#v}
arch=$2
bindir=$3
out=$4
root=$(cd "$(dirname "$0")/../.." && pwd)
maintainer=${DEB_MAINTAINER:-crossdock contributors <crossdock@example.invalid>}

case $arch in
  amd64 | arm64) ;;
  *) echo "unsupported architecture $arch" >&2; exit 2 ;;
esac
# Debian sorts "~" before anything, so 1.0.0~rc.1 precedes 1.0.0.
deb_version=${version//-/\~}
if [[ ! $deb_version =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
  echo "version $version is not a valid Debian version" >&2
  exit 2
fi

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
chmod 0755 "$stage"
"$root/build-aux/linux/install-tree.sh" "$stage" "$bindir" /usr

doc="$stage/usr/share/doc/crossdock"
install -Dm644 "$root/README.md" "$doc/README.md"
cat > "$doc/copyright" <<'EOF'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: crossdock
Source: https://github.com/jm2/crossdock

Files: *
Copyright: 2026 John-Michael and crossdock contributors
License: GPL-3.0-or-later
 On Debian systems, the full text of the GNU General Public License
 version 3 can be found in /usr/share/common-licenses/GPL-3.
EOF
chmod 0644 "$doc/copyright"
{
  printf 'crossdock (%s) unstable; urgency=medium\n\n' "$deb_version"
  printf '  * Release %s. See https://github.com/jm2/crossdock/releases\n\n' "$version"
  printf ' -- %s  %s\n' "$maintainer" "$(date -R -u -d "@${SOURCE_DATE_EPOCH:-$(date +%s)}")"
} | gzip -9n > "$doc/changelog.gz"
chmod 0644 "$doc/changelog.gz"
# Go binaries are statically linked by design (CGO_ENABLED=0).
install -Dm644 /dev/stdin "$stage/usr/share/lintian/overrides/crossdock" <<'EOF'
crossdock: statically-linked-binary [usr/bin/crossdock]
crossdock: no-manual-page [usr/bin/crossdock]
EOF

mkdir -p "$stage/DEBIAN"
installed_size=$(du -sk --exclude=DEBIAN "$stage" | cut -f1)
cat > "$stage/DEBIAN/control" <<EOF
Package: crossdock
Version: $deb_version
Architecture: $arch
Maintainer: $maintainer
Installed-Size: $installed_size
Section: admin
Priority: optional
Homepage: https://github.com/jm2/crossdock
Depends: gpg, gpg-agent, xz-utils, zstd, systemd | systemd-sysusers | systemd-standalone-sysusers
Description: publish packages as signed apt, pacman, RPM and GooGet repos
 crossdock takes one directory of built packages per format and publishes
 them as signed, static package repositories. Each publish swaps in
 atomically. It includes a read-only HTTP server, so no web server is
 required.
 .
 This package installs crossdock, systemd units for the server
 (crossdock-serve, LAN-only on port 8080) and for automatic publishing
 (crossdock-publish.path), the crossdock account, and /var/lib/crossdock.
 Nothing is enabled at install.
EOF
echo /etc/crossdock/crossdock.json > "$stage/DEBIAN/conffiles"
cat > "$stage/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = configure ]; then
	systemd-sysusers ${DPKG_ROOT:+--root="$DPKG_ROOT"} crossdock.conf
	if [ -z "${DPKG_ROOT:-}" ] && command -v systemd-tmpfiles >/dev/null; then
		systemd-tmpfiles --create crossdock.conf || true
	fi
fi
# Nothing is enabled, but deb-systemd-helper records the links enabling
# would make, so that prerm can remove them however they were made. A
# server that is running restarts on the new binary.
if [ "$1" = configure ] || [ "$1" = abort-upgrade ] || [ "$1" = abort-deconfigure ] || [ "$1" = abort-remove ]; then
	if command -v deb-systemd-helper >/dev/null; then
		deb-systemd-helper update-state crossdock-serve.service >/dev/null || true
		deb-systemd-helper update-state crossdock-publish.path >/dev/null || true
	fi
	if [ -z "${DPKG_ROOT:-}" ] && [ -d /run/systemd/system ]; then
		systemctl --system daemon-reload >/dev/null || true
		if [ -n "$2" ]; then
			deb-systemd-invoke try-restart crossdock-serve.service >/dev/null || true
		fi
	fi
fi
exit 0
EOF
cat > "$stage/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
# Stop and disable the units when the package goes, so a later install
# doesn't start them by itself. The account and /var/lib/crossdock (the
# published repos and incoming packages) are kept.
if [ "$1" = remove ]; then
	if [ -z "${DPKG_ROOT:-}" ] && [ -d /run/systemd/system ]; then
		deb-systemd-invoke stop crossdock-publish.path crossdock-publish.service crossdock-serve.service >/dev/null || true
	fi
	if command -v deb-systemd-helper >/dev/null; then
		deb-systemd-helper disable crossdock-serve.service >/dev/null || true
		deb-systemd-helper disable crossdock-publish.path >/dev/null || true
	fi
fi
exit 0
EOF
cat > "$stage/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = remove ] && [ -z "${DPKG_ROOT:-}" ] && [ -d /run/systemd/system ]; then
	systemctl --system daemon-reload >/dev/null || true
fi
exit 0
EOF
chmod 0755 "$stage/DEBIAN/postinst" "$stage/DEBIAN/prerm" "$stage/DEBIAN/postrm"
(cd "$stage" && find usr etc -type f -print0 | LC_ALL=C sort -z | xargs -0 md5sum) > "$stage/DEBIAN/md5sums"
chmod 0644 "$stage/DEBIAN/control" "$stage/DEBIAN/md5sums" "$stage/DEBIAN/conffiles"

mkdir -p "$(dirname "$out")"
dpkg-deb --root-owner-group -Zxz --build "$stage" "$out"
dpkg-deb --info "$out"
