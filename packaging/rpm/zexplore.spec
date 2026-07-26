Name:           zexplore
Version:        0.1.0
Release:        1%{?dist}
Summary:        A direct interface to your ZFS primitives — not a dashboard
License:        BSD-3-Clause
URL:            https://zexplore.ca
BuildArch:      noarch
Requires:       bash
Requires:       zfs
Recommends:     fzf
Recommends:     pv
Recommends:     mbuffer
Recommends:     python3

%description
zexplore is a fast, keyboard-driven console for OpenZFS: browse datasets and
snapshots with a full properties + permissions view, snapshot on tap, point-and-
shoot replicate over SSH, explore files inside datasets/snapshots/zvols, and a
scoped snapshot/rollback transaction API. Every action is a plain zfs/zpool
command — a primitives tool, not a dashboard.

%prep
%setup -q -n zexplore-%{version}

%install
make DESTDIR=%{buildroot} PREFIX=/usr install

%files
%license LICENSE
%doc README.md
/usr/bin/zexplore
/usr/bin/zexplore-api
/usr/bin/zexplore-txn
%{_docdir}/zexplore/DESIGN.md
/usr/lib/systemd/system/zexplore-api.service

%changelog
* Sun Jul 26 2026 zexplore <hello@zexplore.ca> - 0.1.0-1
- Initial package.
