#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Installs crossdock's files into a package staging tree. Every package
# (rpm, deb, Arch) uses this, so they all ship the same files:
#
#   <prefix>/bin/crossdock
#   <prefix>/lib/systemd/system/crossdock-serve.service
#   <prefix>/lib/systemd/system/crossdock-publish.service, crossdock-publish.path
#   <prefix>/lib/sysusers.d/crossdock.conf
#   <prefix>/lib/tmpfiles.d/crossdock.conf
#   <prefix>/lib/firewalld/services/crossdock.xml
#   /etc/crossdock/crossdock.json   (config file; never overwritten on upgrade)
#
# Nothing is enabled at install. The crossdock account and
# /var/lib/crossdock come from sysusers.d and tmpfiles.d.
#
#   build-aux/linux/install-tree.sh <destdir> <dir with crossdock> [prefix]
set -eu

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
  echo "usage: $0 <destdir> <bindir> [prefix]" >&2
  exit 2
fi
dest=$1
bindir=$2
prefix=${3:-/usr}
root=$(cd "$(dirname "$0")/../.." && pwd)
units="$root/build-aux/systemd"

install -Dm755 "$bindir/crossdock" "$dest$prefix/bin/crossdock"

for unit in crossdock-serve.service crossdock-publish.service crossdock-publish.path; do
  out="$dest$prefix/lib/systemd/system/$unit"
  mkdir -p "$(dirname "$out")"
  sed "s|@CROSSDOCK@|$prefix/bin/crossdock|" "$units/$unit" > "$out"
  chmod 0644 "$out"
done

install -Dm644 "$units/crossdock.sysusers" "$dest$prefix/lib/sysusers.d/crossdock.conf"
install -Dm644 "$units/crossdock.tmpfiles" "$dest$prefix/lib/tmpfiles.d/crossdock.conf"
install -Dm644 "$units/crossdock.firewalld.xml" "$dest$prefix/lib/firewalld/services/crossdock.xml"
install -Dm644 "$root/build-aux/etc/crossdock.json" "$dest/etc/crossdock/crossdock.json"
