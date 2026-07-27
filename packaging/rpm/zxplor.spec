Name:           zxplor
Version:        0.1.0
Release:        1%{?dist}
Summary:        A direct interface to your ZFS primitives — not a dashboard
License:        BSD-3-Clause
URL:            https://zxplor.ca
BuildArch:      noarch
Requires:       bash
Requires:       zfs
Recommends:     fzf
Recommends:     pv
Recommends:     mbuffer
Recommends:     python3

%description
zxplor is a fast, keyboard-driven console for OpenZFS: browse datasets and
snapshots with a full properties + permissions view, snapshot on tap, point-and-
shoot replicate over SSH, explore files inside datasets/snapshots/zvols, and a
scoped snapshot/rollback transaction API. Every action is a plain zfs/zpool
command — a primitives tool, not a dashboard.

%prep
%setup -q -n zxplor-%{version}

%install
make DESTDIR=%{buildroot} PREFIX=/usr install

%files
%license LICENSE
%doc README.md
/usr/bin/zxplor
/usr/bin/zxplor-api
/usr/bin/zxplor-txn
%{_docdir}/zxplor/DESIGN.md
/usr/lib/systemd/system/zxplor-api.service

%changelog
* Sun Jul 26 2026 zxplor <hello@zxplor.ca> - 0.1.0-1
- Initial package.
