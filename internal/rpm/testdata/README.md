# Test RPMs

These packages are copied unmodified from createrepo_c's test suite
(https://github.com/rpm-software-management/createrepo_c,
`tests/testdata/packages/`), which is licensed GPL-2.0-or-later. They're
used here as GPL-3.0-or-later-compatible test data. They cover Latin-1 and
Latin-2 text, a source RPM, weak dependencies, file dependencies, and
EdDSA, RSA and RPM v6 multi-signature packages.

`validate_repodata.py` checks a published repo against createrepo_c
(`pip install createrepo_c`); `go test ./internal/rpm` runs it when the
bindings are installed.
