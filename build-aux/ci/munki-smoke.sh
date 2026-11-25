#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# The real Munki client against a munki repo served by `crossdock serve`,
# on a macOS CI runner (it installs munkitools and writes to /Applications,
# so not on your own Mac). It:
#
#   1. builds small copy_from_dmg disk images with hdiutil, writes their
#      pkginfo sidecars, and publishes them with a hand-written manifest
#   2. installs munkitools and points it at the repo
#   3. has managedsoftwareupdate install version 1.0, finishing a download
#      that was cut off halfway (a 206 in serve's access log)
#   4. upgrades to 1.1
#   5. checks that a disk image that doesn't match its catalog hash is
#      refused
#   6. removes the item through managed_uninstalls
#   7. checks a download by hand with the repo page's steps for macOS
#
#   MUNKITOOLS=munkitools.pkg CROSSDOCK=./crossdock build-aux/ci/munki-smoke.sh
#
# Failures are printed as GitHub Actions error annotations.
set -euo pipefail

crossdock=${CROSSDOCK:-crossdock}
munkitools=${MUNKITOOLS:?set MUNKITOOLS to a munkitools .pkg}
work=$(mktemp -d)
msu=/usr/local/munki/managedsoftwareupdate
cache="/Library/Managed Installs/Cache"
dest=/Applications/Games/CrossdockSmoke
base=http://127.0.0.1:8088
repo=$base/munki/smoke
fail() {
  echo "::error title=munki smoke::$*"
  exit 1
}

sw_vers
mkdir "$work/in" "$work/manifests"

# image <version> writes crossdock-smoke-<version>.dmg, holding
# CrossdockSmoke/ with the version marker Munki's installs check reads, and
# its pkginfo sidecar. The sidecar goes first and the image is renamed into
# place last, as an upload should.
image() {
  local v=$1 stage=$work/stage-$1 name=crossdock-smoke-$1.dmg
  mkdir -p "$stage/CrossdockSmoke"
  cat > "$stage/CrossdockSmoke/version.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleShortVersionString</key><string>$v</string></dict></plist>
EOF
  # Random data doesn't compress, so the resume has something to skip.
  head -c 4000000 /dev/urandom > "$stage/CrossdockSmoke/data"
  hdiutil create -quiet -srcfolder "$stage" -volname CrossdockSmoke -fs APFS -format UDRO "$work/build-$v.dmg"
  cat > "$work/in/$name.pkginfo" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>name</key><string>crossdock-smoke</string>
  <key>display_name</key><string>crossdock smoke test</string>
  <key>version</key><string>$v</string>
  <key>installer_type</key><string>copy_from_dmg</string>
  <key>items_to_copy</key>
  <array><dict>
    <key>source_item</key><string>CrossdockSmoke</string>
    <key>destination_path</key><string>/Applications/Games</string>
  </dict></array>
  <key>installs</key>
  <array><dict>
    <key>type</key><string>plist</string>
    <key>path</key><string>$dest/version.plist</string>
    <key>CFBundleShortVersionString</key><string>$v</string>
  </dict></array>
  <key>installed_size</key><integer>$(du -sk "$stage" | cut -f1)</integer>
  <key>uninstallable</key><true/>
  <key>uninstall_method</key><string>remove_copied_items</string>
</dict>
</plist>
EOF
  mv "$work/build-$v.dmg" "$work/in/$name"
}

# manifest <key>: the client's manifest, with the item under that key.
manifest() {
  cat > "$work/manifests/smoke-client" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>catalogs</key><array><string>production</string></array>
  <key>$1</key><array><string>crossdock-smoke</string></array>
</dict>
</plist>
EOF
}

cat > "$work/crossdock.yaml" <<EOF
output: $work/out
signing:
  key_file: $work/signing.asc
repos:
  - name: smoke
    format: munki
    source: $work/in
    manifests_source: $work/manifests
EOF
"$crossdock" keygen -uid "crossdock CI <ci@example.invalid>" -out "$work/signing.asc"
publish() { "$crossdock" publish -q -c "$work/crossdock.yaml"; }

image 1.0
manifest managed_installs
publish
"$crossdock" serve -c "$work/crossdock.yaml" -listen 127.0.0.1:8088 > "$work/access.log" &
serve_pid=$!
trap 'kill $serve_pid 2>/dev/null || true' EXIT
for _ in $(seq 50); do
  curl -fsS -o /dev/null "$base/_crossdock/healthz" 2>/dev/null && break
  sleep 0.1
done

sudo installer -pkg "$munkitools" -target /
"$msu" --version
prefs=/Library/Preferences/ManagedInstalls
sudo defaults write "$prefs" SoftwareRepoURL "$repo"
sudo defaults write "$prefs" ClientIdentifier smoke-client
sudo defaults write "$prefs" SuppressUserNotification -bool true
sudo defaults write "$prefs" InstallAppleSoftwareUpdates -bool false
sudo defaults write "$prefs" LoggingLevel -int 2

# run_msu checks and installs, as a scheduled run would, and keeps the
# output and the access log lines of this run. Its exit status isn't the
# test: what ends up installed is.
run_msu() {
  local seen
  seen=$(wc -l < "$work/access.log")
  sudo "$msu" --checkonly 2>&1 | tee "$work/msu.out" || echo "managedsoftwareupdate --checkonly failed"
  sudo "$msu" --installonly 2>&1 | tee -a "$work/msu.out" || echo "managedsoftwareupdate --installonly failed"
  tail -n +$((seen + 1)) "$work/access.log" > "$work/run.log"
}
installed() { /usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$dest/version.plist" 2>/dev/null || echo none; }

# 3. Install, resuming a download cut off halfway: the partial file and
# the headers Munki keeps in an xattr beside it, as a killed run leaves them.
dmg=crossdock-smoke-1.0.dmg
url=$repo/pkgs/crossdock-smoke/$dmg
header() { curl -fsSI "$url" | tr -d '\r' | awk -v h="$1" 'tolower($1) == h ":" { sub(/^[^:]*: /, ""); print }'; }
etag=$(header etag)
modified=$(header last-modified)
size=$(header content-length)
[[ -n $etag && -n $modified && $size -gt 1000000 ]] || fail "HEAD $url: etag '$etag', last-modified '$modified', length '$size'"
sudo mkdir -p "$cache"
curl -fsS -r "0-$((size / 2 - 1))" "$url" | sudo tee "$cache/$dmg.download" > /dev/null
sudo xattr -w com.googlecode.munki.downloadData "<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<plist version=\"1.0\"><dict>
<key>etag</key><string>$etag</string>
<key>last-modified</key><string>$modified</string>
<key>expected-length</key><string>$size</string>
</dict></plist>" "$cache/$dmg.download"
run_msu
[[ $(installed) == 1.0 ]] || fail "after the first run, installed version is $(installed), want 1.0"
grep -E "GET \"/munki/smoke/pkgs/crossdock-smoke/$dmg\" 206 $((size - size / 2)) " "$work/run.log" ||
  fail "Munki didn't resume the download with a range request: $(grep "$dmg" "$work/run.log")"
for f in catalogs/production manifests/smoke-client; do
  grep -q "GET \"/munki/smoke/$f\" 200 " "$work/run.log" || fail "Munki didn't fetch $f"
done
echo "installed 1.0, resuming the download"

# 4. Upgrade.
image 1.1
publish
run_msu
[[ $(installed) == 1.1 ]] || fail "after the upgrade, installed version is $(installed), want 1.1"
echo "upgraded to 1.1"

# 5. A disk image that isn't the one the catalog describes is refused.
image 1.2
publish
served=$work/out/current/munki/smoke/pkgs/crossdock-smoke/crossdock-smoke-1.2.dmg
cp "$served" "$work/tampered.dmg"
printf 'X' | dd of="$work/tampered.dmg" bs=1 seek=1000000 conv=notrunc 2> /dev/null
mv "$work/tampered.dmg" "$served" # a new file: the store's copy stays intact
run_msu
[[ $(installed) == 1.1 ]] || fail "a tampered 1.2 was installed: installed version is $(installed)"
grep -q "integrity check for crossdock-smoke-1.2.dmg failed" "$work/msu.out" ||
  sudo grep -q "integrity check for crossdock-smoke-1.2.dmg failed" "/Library/Managed Installs/Logs/ManagedSoftwareUpdate.log" ||
  fail "Munki didn't report the hash mismatch"
echo "refused a tampered disk image"

# 6. Removal.
manifest managed_uninstalls
publish
run_msu
[[ ! -e $dest ]] || fail "$dest is still there after managed_uninstalls"
echo "removed"

# 7. A download by hand, checked as the repo page says for macOS.
curl -fsS "$repo/" > "$work/page.html"
for want in "SoftwareRepoURL $repo" "ClientIdentifier smoke" "shasum -a 256 -c --ignore-missing SHA256SUMS" "crossdock-smoke-1.2.dmg"; do
  grep -qF "$want" "$work/page.html" || fail "repo page lacks: $want"
done
mkdir "$work/byhand"
(
  cd "$work/byhand"
  curl -fsSO "$repo/SHA256SUMS" -O "$repo/pkgs/crossdock-smoke/crossdock-smoke-1.2.dmg"
  shasum -a 256 -c --ignore-missing SHA256SUMS
) || fail "shasum rejected a disk image downloaded by hand"
echo "checked a download by hand"
