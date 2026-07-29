# zxplore — the universal ZFS console. Clone → build → install on ANY OpenZFS
# host (Linux distros or FreeBSD). This Makefile is the clone-and-build entry
# point (the kldload ISO build calls it too).
#
#   git clone https://github.com/zxplore/zxplore && cd zxplore && make && sudo make install
#
# Two binaries from one tree:
#   zxplore      — the full build: native GUI (Fyne) + TUI (--tui). Needs cgo+GL.
#   zxplore-tui  — STATIC terminal-only build (CGO_ENABLED=0): zero runtime
#                  deps, scp it to any ZFS box. Headless hosts can build just
#                  this (`make zxplore-tui`) with nothing but the Go toolchain.
#
# BUILD dependencies (cgo + OpenGL — for the `zxplore` GUI binary only):
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
#   FreeBSD:       pkg install -y go gmake pkgconf mesa-libs libX11 \
#                    libxkbcommon wayland fontconfig
#                  (BSD make can't parse this file — build with `gmake`;
#                   `gmake zxplore-tui` needs only go + gmake, no GL)
#
# RUNTIME dependencies (installed system): libGL, an X11/Wayland session, and
# fontconfig. Headless/SSH: `zxplore --tui` (terminal UI, no GL needed).

PREFIX  ?= /usr/local
DESTDIR ?=
BINDIR   = $(DESTDIR)$(PREFIX)/bin
APPDIR   = $(DESTDIR)$(PREFIX)/share/applications
MANDIR   = $(DESTDIR)$(PREFIX)/share/man/man1
ICONDIR  = $(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps
DOCDIR   = $(DESTDIR)$(PREFIX)/share/doc/zxplore
UNITDIR  = $(DESTDIR)/usr/lib/systemd/system

GO      ?= go
GOFLAGS ?= -trimpath

# ── version stamp ─────────────────────────────────────────────────────────────
# .buildnum is a local, gitignored counter that self-increments once per make
# run (the phony `bump` prerequisite). It's stamped into both binaries via
# -X main.buildNum and surfaces as "0.1.0 b<N>" in --version and the GUI header.
BUILDNUM_FILE = .buildnum
STAMPFLAGS    = -ldflags "-X main.buildNum=$$(cat $(BUILDNUM_FILE) 2>/dev/null || echo 0)"

bump:
	@n=$$(cat $(BUILDNUM_FILE) 2>/dev/null || echo 0); echo $$((n + 1)) > $(BUILDNUM_FILE)

# ── build ────────────────────────────────────────────────────────────────────
# zxplore (GUI+TUI, cgo) and zxplore-tui (static, terminal-only, no cgo).
build: zxplore zxplore-tui

zxplore: bump $(wildcard *.go) go.mod go.sum
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) $(STAMPFLAGS) -tags gui -o zxplore .

zxplore-tui: bump $(wildcard *.go) go.mod go.sum
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(STAMPFLAGS) -o zxplore-tui .

# ── test ─────────────────────────────────────────────────────────────────────
# Unit + mock-CLI suites for both build flavors. The mock tests fake
# zfs/zpool/pkexec/ssh on PATH — no real pool is ever touched. Feature→test
# map: docs/TESTING.md.
test:
	$(GO) vet ./...
	$(GO) vet -tags gui ./...
	$(GO) test ./...
	$(GO) test -tags gui ./...

# ── install ──────────────────────────────────────────────────────────────────
install: build
	install -d $(BINDIR) $(APPDIR) $(ICONDIR) $(DOCDIR) $(MANDIR)
	install -m 0755 zxplore                  $(BINDIR)/zxplore
	install -m 0755 zxplore-tui              $(BINDIR)/zxplore-tui
	install -m 0644 docs/zxplore.1               $(MANDIR)/zxplore.1
	install -m 0644 assets/zxplore.svg           $(ICONDIR)/zxplore.svg
	install -m 0644 assets/zxplore-tui.svg       $(ICONDIR)/zxplore-tui.svg
	install -m 0644 contrib/zxplore.desktop      $(APPDIR)/zxplore.desktop
	install -m 0644 contrib/zxplore-tui.desktop  $(APPDIR)/zxplore-tui.desktop
	# polkit: one admin auth covers a few minutes of zfs/zpool (Linux desktops)
	@if [ -d $(DESTDIR)/usr/share/polkit-1/actions ]; then \
	  install -m 0644 contrib/org.zxplore.policy $(DESTDIR)/usr/share/polkit-1/actions/; \
	fi
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
	rm -f $(BINDIR)/zxplore $(BINDIR)/zxplore-tui
	rm -f $(BINDIR)/zxplore-api $(BINDIR)/zxplore-txn
	rm -f $(APPDIR)/zxplore.desktop $(APPDIR)/zxplore-tui.desktop
	rm -f $(ICONDIR)/zxplore.svg $(ICONDIR)/zxplore-tui.svg
	rm -f $(MANDIR)/zxplore.1
	rm -f $(DESTDIR)/usr/share/polkit-1/actions/org.zxplore.policy
	rm -f $(UNITDIR)/zxplore-api.service
	rm -rf $(DOCDIR)

clean:
	rm -f zxplore zxplore-tui zxplore-bin

.PHONY: build bump test install uninstall clean
