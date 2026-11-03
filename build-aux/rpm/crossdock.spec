# Builds crossdock from the release source. Used by COPR (through Packit,
# see .packit.yaml) and by the release workflow's .rpm job.
#
# crossdock uses only the Go standard library. When the builder's Go is
# older than go.mod asks for, the go command downloads that toolchain from
# proxy.golang.org (checked against sum.golang.org), so COPR builds need
# network access (enable_net).

# Static, stripped Go binary: there is no debug information to package.
%global debug_package %{nil}
# The version stamped into the binary: the release tag. The release
# workflow passes it for pre-releases, whose RPM version uses "~".
%{!?crossdock_tag:%global crossdock_tag v%{version}}

Name:           crossdock
# Packit replaces Version with the release tag minus its "v" (see .packit.yaml).
Version:        0.1.0
Release:        1%{?dist}
Summary:        Publish packages as signed apt, pacman, RPM and GooGet repositories

License:        GPL-3.0-or-later
URL:            https://github.com/jm2/crossdock
Source0:        https://github.com/jm2/crossdock/archive/refs/tags/v%{version}.tar.gz

ExclusiveArch:  x86_64 aarch64

BuildRequires:  golang >= 1.21
BuildRequires:  make
BuildRequires:  systemd-rpm-macros
%{?systemd_requires}
Requires(pre):  systemd
# Signing, and reading xz- and zstd-compressed packages (dpkg-deb on Ubuntu
# and makepkg default to zstd), until native support lands.
Requires:       gnupg2
Requires:       xz
Requires:       zstd

%description
crossdock takes one directory of built packages per format and publishes
them as signed, static package repositories. Each publish swaps in
atomically. It includes a read-only HTTP server, so no web server is
required.

This package installs crossdock, systemd units for the server
(crossdock-serve, LAN-only on port 8080) and for automatic publishing
(crossdock-publish.path), the crossdock account, /var/lib/crossdock, and
a firewalld service definition. Nothing is enabled at install.

%prep
%autosetup -p1 -n %{name}-%{version}

%build
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.org}"
export GOFLAGS="${GOFLAGS:-} -mod=readonly"
make build VERSION=%{crossdock_tag}

%install
mkdir -p bin-stage
cp crossdock bin-stage/
build-aux/linux/install-tree.sh %{buildroot} bin-stage %{_prefix}

%check
./crossdock version | grep -F "crossdock %{crossdock_tag} "

# %%pre creates the crossdock account from the sysusers.d line this package
# ships (what %%sysusers_create_compat expands to; newer rpm also creates it
# from the file itself). Presets leave the units off; the server restarts on
# upgrade if it runs, and everything stops and is disabled on removal.
%pre
systemd-sysusers --replace=%{_sysusersdir}/crossdock.conf - <<'EOF' || :
u crossdock - "crossdock repository publisher" /var/lib/crossdock
EOF

%post
%tmpfiles_create crossdock.conf
%systemd_post crossdock-serve.service crossdock-publish.service crossdock-publish.path crossdock-publish.timer

%preun
%systemd_preun crossdock-serve.service crossdock-publish.service crossdock-publish.path crossdock-publish.timer

%postun
%systemd_postun_with_restart crossdock-serve.service
%systemd_postun crossdock-publish.service crossdock-publish.path crossdock-publish.timer

%files
%license LICENSE
%doc README.md
%{_bindir}/crossdock
%{_unitdir}/crossdock-serve.service
%{_unitdir}/crossdock-publish.service
%{_unitdir}/crossdock-publish.path
%{_unitdir}/crossdock-publish.timer
%{_sysusersdir}/crossdock.conf
%{_tmpfilesdir}/crossdock.conf
%dir %{_prefix}/lib/firewalld
%dir %{_prefix}/lib/firewalld/services
%{_prefix}/lib/firewalld/services/crossdock.xml
%dir %{_sysconfdir}/crossdock
%config(noreplace) %{_sysconfdir}/crossdock/crossdock.json

%changelog
* Tue Nov 03 2026 crossdock contributors - 0.1.0-1
- Initial package: crossdock, systemd units for serving and automatic
  publishing, the crossdock account, and a firewalld service.
