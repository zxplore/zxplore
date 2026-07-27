# zxplore — install/uninstall on any OpenZFS host (Linux or FreeBSD).
PREFIX ?= /usr/local
BINDIR  = $(DESTDIR)$(PREFIX)/bin
DOCDIR  = $(DESTDIR)$(PREFIX)/share/doc/zxplore
UNITDIR = $(DESTDIR)/usr/lib/systemd/system
TOOLS   = zxplore zxplore-api zxplore-txn

install:
	install -d $(BINDIR) $(DOCDIR)
	install -m 0755 $(addprefix bin/,$(TOOLS)) $(BINDIR)
	install -m 0644 README.md docs/DESIGN.md $(DOCDIR)
	@if [ -d /run/systemd/system ]; then \
	  install -d $(UNITDIR); \
	  install -m 0644 systemd/zxplore-api.service $(UNITDIR); \
	  echo "systemd unit installed — enable with: systemctl enable --now zxplore-api"; \
	fi
	@command -v fzf >/dev/null 2>&1 || echo "note: install fzf for the interactive console"
	@echo "zxplore installed to $(PREFIX)/bin"

uninstall:
	rm -f $(addprefix $(BINDIR)/,$(TOOLS))
	rm -rf $(DOCDIR)
	rm -f $(UNITDIR)/zxplore-api.service

.PHONY: install uninstall
