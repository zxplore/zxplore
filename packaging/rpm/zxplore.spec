Name:           zxplore
Version:        1.1.0
Release:        1%{?dist}
Summary:        Keyboard-driven console for OpenZFS — primitives, not a dashboard
License:        BSD-3-Clause
URL:            https://github.com/zxplore/zxplore
Source0:        %{url}/archive/v%{version}/zxplore-%{version}.tar.gz

# Go build (cgo for the Fyne GUI binary; the TUI binary is pure Go).
BuildRequires:  golang >= 1.26
BuildRequires:  gcc
BuildRequires:  make
BuildRequires:  pkgconfig(gl)
BuildRequires:  pkgconfig(x11)
BuildRequires:  pkgconfig(xcursor)
BuildRequires:  pkgconfig(xrandr)
BuildRequires:  pkgconfig(xinerama)
BuildRequires:  pkgconfig(xi)
BuildRequires:  pkgconfig(xxf86vm)
BuildRequires:  pkgconfig(wayland-client)
BuildRequires:  pkgconfig(xkbcommon)
BuildRequires:  pkgconfig(fontconfig)

Requires:       zfs
Recommends:     polkit

%description
zxplore is a fast, keyboard-driven console for OpenZFS: browse datasets and
snapshots with full property dossiers, replicate over SSH (encrypted datasets
travel raw), restore files from any snapshot, and manage boot environments.
Two binaries: zxplore (native Fyne GUI, --tui for terminal) and zxplore-tui
(fully static, terminal only). Every action is a plain zfs/zpool command —
a primitives tool, not a dashboard.

%prep
%autosetup -n zxplore-%{version}

%build
make build

%install
make DESTDIR=%{buildroot} PREFIX=/usr install

%files
%license LICENSE
%doc README.md
/usr/bin/zxplore
/usr/bin/zxplore-tui
/usr/bin/zxplore-api
/usr/bin/zxplore-txn
%{_mandir}/man1/zxplore.1*
%{_datadir}/applications/zxplore.desktop
%{_datadir}/applications/zxplore-tui.desktop
%{_datadir}/icons/hicolor/scalable/apps/zxplore.svg
%{_datadir}/icons/hicolor/scalable/apps/zxplore-tui.svg
%{_docdir}/zxplore/README.md
%{_docdir}/zxplore/DESIGN.md

%changelog
* Fri Jul 31 2026 Anthony <admin@zxplore.dev> - 1.1.0-1
- Go console (Fyne GUI + static TUI); delegation grants; server setup flows;
  ssh-agent and host-key recovery fixes. Replaces the bash prototype.

* Sun Jul 26 2026 Anthony <admin@zxplore.dev> - 0.1.0-1
- Initial packaging (bash prototype, retired).
