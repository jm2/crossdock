#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Validates a crossdock rpm-md repository with createrepo_c, the library
# that writes Fedora's repodata (`pip install createrepo_c`):
#
#   validate_repodata.py <repo dir> <dir of the source .rpm files>
#
# 1. repomd.xml parses, and every <data> record's size and checksums match
#    the file it points to.
# 2. primary, filelists and other XML parse with createrepo_c's parser and
#    no warnings.
# 3. Every package is identical, field for field, to the one createrepo_c
#    builds from the .rpm itself. Both sides go through createrepo_c's XML
#    writer and parser, so text encodings compare the same way.
import glob
import gzip
import hashlib
import os
import sys
import tempfile

import createrepo_c as cr

repo, src = sys.argv[1], sys.argv[2]
failures = []


def fail(msg):
    failures.append(msg)
    print("FAIL:", msg)


def warn(kind, msg):
    fail(f"parser warning ({kind}): {msg}")


# 1. repomd.xml
repomd = cr.Repomd(os.path.join(repo, "repodata", "repomd.xml"))
records = {r.type: r for r in repomd.records}
for kind in ("primary", "filelists", "other"):
    r = records.get(kind)
    if r is None:
        fail(f"repomd.xml has no {kind} record")
        continue
    path = os.path.join(repo, r.location_href)
    raw = open(path, "rb").read()
    opened = gzip.decompress(raw)
    if r.size != len(raw) or r.size_open != len(opened):
        fail(f"{kind}: size {r.size}/{r.size_open}, file is {len(raw)}/{len(opened)}")
    if r.checksum != hashlib.sha256(raw).hexdigest() or r.checksum_type != "sha256":
        fail(f"{kind}: checksum does not match {r.location_href}")
    if r.checksum_open != hashlib.sha256(opened).hexdigest():
        fail(f"{kind}: open-checksum does not match")
    if not os.path.basename(path).startswith(r.checksum + "-"):
        fail(f"{kind}: file name is not prefixed with its checksum")


# 2. parse the metadata with createrepo_c
pkgs = {}


def newpkg(pkgId, name, arch):
    return pkgs.get(pkgId)


def pkgcb(pkg):
    pkgs[pkg.pkgId] = pkg
    return True


cr.xml_parse_primary(os.path.join(repo, records["primary"].location_href),
                     pkgcb=pkgcb, warningcb=warn, do_files=True)
cr.xml_parse_filelists(os.path.join(repo, records["filelists"].location_href),
                       newpkgcb=newpkg, warningcb=warn)
cr.xml_parse_other(os.path.join(repo, records["other"].location_href),
                   newpkgcb=newpkg, warningcb=warn)


# 3. compare with createrepo_c's own reading of each .rpm, round-tripped
# through its XML writer and parser.
def reference(path, href):
    p = cr.package_from_rpm(path, changelog_limit=10, location_href=href)
    out = {}
    with tempfile.TemporaryDirectory() as d:
        for kind, dump, parse in (
            ("primary", cr.xml_dump_primary, cr.xml_parse_primary),
            ("filelists", cr.xml_dump_filelists, cr.xml_parse_filelists),
            ("other", cr.xml_dump_other, cr.xml_parse_other),
        ):
            ns = {
                "primary": 'xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm"',
                "filelists": 'xmlns="http://linux.duke.edu/metadata/filelists"',
                "other": 'xmlns="http://linux.duke.edu/metadata/other"',
            }[kind]
            root = {"primary": "metadata", "filelists": "filelists", "other": "otherdata"}[kind]
            f = os.path.join(d, kind + ".xml")
            with open(f, "w", encoding="utf-8") as fh:
                fh.write(f'<?xml version="1.0" encoding="UTF-8"?>\n<{root} {ns} packages="1">\n')
                fh.write(dump(p))
                fh.write(f"</{root}>\n")
            if kind == "primary":
                parse(f, pkgcb=lambda q: out.setdefault("pkg", q) and True, warningcb=warn, do_files=True)
            else:
                parse(f, newpkgcb=lambda i, n, a: out["pkg"], warningcb=warn)
    return out["pkg"]


FIELDS = ["name", "arch", "epoch", "version", "release", "checksum_type", "summary", "description",
          "url", "time_file", "time_build", "rpm_license", "rpm_vendor", "rpm_group", "rpm_buildhost",
          "rpm_sourcerpm", "rpm_header_start", "rpm_header_end", "rpm_packager", "size_package",
          "size_installed", "size_archive", "location_href", "provides", "requires", "conflicts",
          "obsoletes", "suggests", "enhances", "recommends", "supplements", "files", "changelogs"]

by_name = {}
for p in pkgs.values():
    by_name[(p.name, p.epoch, p.version, p.release, p.arch)] = p

rpms = sorted(glob.glob(os.path.join(src, "*.rpm")))
seen = set()
for path in rpms:
    ref0 = cr.package_from_rpm(path)
    key = (ref0.name, ref0.epoch, ref0.version, ref0.release, ref0.arch)
    if key in seen:
        continue
    seen.add(key)
    ours = by_name.get(key)
    if ours is None:
        fail(f"{os.path.basename(path)} is missing from the repo")
        continue
    if ours.pkgId != hashlib.sha256(open(path, "rb").read()).hexdigest():
        fail(f"{os.path.basename(path)}: pkgid is not the file's sha256")
    pub = os.path.join(repo, ours.location_href)
    if open(pub, "rb").read() != open(path, "rb").read():
        fail(f"{ours.location_href} differs from {path}")
    ref = reference(path, ours.location_href)
    ref.time_file = ours.time_file  # the publish copy's mtime may differ
    for field in FIELDS:
        a, b = getattr(ref, field), getattr(ours, field)
        if field == "files":
            a = sorted(tuple(x or "" for x in f) for f in a)
            b = sorted(tuple(x or "" for x in f) for f in b)
        if a != b:
            fail(f"{os.path.basename(path)} {field}:\n  createrepo_c: {a!r}\n  crossdock:    {b!r}")
    print(f"ok {ours.location_href}: {len(FIELDS)} fields match")

if len(pkgs) != len(seen):
    fail(f"repo has {len(pkgs)} packages, expected {len(seen)}")
print(f"{len(seen)} packages, {len(failures)} failures")
sys.exit(1 if failures else 0)
