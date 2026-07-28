# zxplore — the universal ZFS console. Clone → build → install on ANY OpenZFS
# host (Linux distros or FreeBSD). This Makefile is the clone-and-build entry
# point (the kldload ISO build calls it too).
#
#   git clone https://github.com/kldload/zxplore && cd zxplore && make && sudo make install
#
# BUILD dependencies (cgo + OpenGL, for the Fyne GUI):
#   Fedora/RHEL:   dnf install -y golang gcc pkgconf-pkg-config \
#                    mesa-libGL-devel libX11-devel libXcursor-devel \
#                    libXrandr-devel libXinerama-devel libXi-devel \
#                    libXxf86vm-devel wayland-devel libxkbcommon-devel \
#                    fontconfig-devel
#   Debian/Ubuntu: apt-get install -y golang gcc pkg-config \
#                    libgl1-mesa-dev xorg-dev libwayland-dev \
#                    libxkbcommon-dev libfontconfig1-dev
#   Arch:          pacman -S --needed go gcc pkgconf libgl libxcursor \
#                    libxrandr libxinerama libxi wayland libxkbcommon fontconfig
#   FreeBSD:       pkg install -y go pkgconf mesa-libs libX11 libxkbcommon \
#                    wayland fontconfig
#
# RUNTIME dependencies (installed system): libGL, an X11/Wayland session, and
# fontconfig. Headless/SSH: `zxplore --tui` (terminal UI, no GL needed).

PREFIX  ?= /usr/local
DESTDIR ?=
BINDIR   = $(DESTDIR)$(PREFIX)/bin
APPDIR   = $(DESTDIR)$(PREFIX)/share/applications
ICONDIR  = $(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps
DOCDIR   = $(DESTDIR)$(PREFIX)/share/doc/zxplore
UNITDIR  = $(DESTDIR)/usr/lib/systemd/system

GO      ?= go
GOFLAGS ?= -trimpath

# ── build ────────────────────────────────────────────────────────────────────
# The native GUI binary (Fyne) needs cgo. `zxplore --tui` runs the terminal UI.
build: zxplore

zxplore: $(wildcard *.go) go.mod go.sum
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -o zxplore .

# ── install ──────────────────────────────────────────────────────────────────
install: build
	install -d $(BINDIR) $(APPDIR) $(ICONDIR) $(DOCDIR)
	install -m 0755 zxplore                  $(BINDIR)/zxplore
	install -m 0644 assets/zxplore.svg       $(ICONDIR)/zxplore.svg
	install -m 0644 contrib/zxplore.desktop  $(APPDIR)/zxplore.desktop
	install -m 0644 README.md docs/DESIGN.md $(DOCDIR)
	# Optional host-side transaction API (Python) + guest CLI, when present.
	@if [ -f bin/zxplore-api ]; then \
	  install -m 0755 bin/zxplore-api bin/zxplore-txn $(BINDIR); \
	  if [ -d /run/systemd/system ]; then \
	    install -d $(UNITDIR); \
	    install -m 0644 systemd/zxplore-api.service $(UNITDIR); \
	  fi; \
	fi
	@echo "zxplore installed to $(PREFIX)/bin"

uninstall:
	rm -f $(BINDIR)/zxplore $(BINDIR)/zxplore-api $(BINDIR)/zxplore-txn
	rm -f $(APPDIR)/zxplore.desktop $(ICONDIR)/zxplore.svg
	rm -f $(UNITDIR)/zxplore-api.service
	rm -rf $(DOCDIR)

clean:
	rm -f zxplore zxplore-bin

.PHONY: build install uninstall clean
