# Changelog

All notable changes to zxplore. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] — 2026-07-31

### Added
- Transfer: **delegation grants** — replicate as an unprivileged user via
  `zfs allow`, no root on either end.
- Servers: one-click **Set up & save** flow.
- Servers: reinstall recovery for changed host keys.
- Release engineering: prebuilt static `zxplore-tui` binaries for
  linux (amd64/arm64), FreeBSD (amd64/arm64), OpenBSD, NetBSD, illumos and
  Solaris, with `SHA256SUMS`; rpm/deb/Arch packages built by CI (nfpm);
  CI now enforces that a release tag matches the binary's `--version`.

### Fixed
- Transfer: the direction arrow is the button — the affordance matches the
  action.
- GUI: mouse travel no longer steals locked selections.
- Desktop: `StartupWMClass=ca.zxplore` matches the Fyne app_id, so window
  icons group correctly.
- Servers: an explicit ssh user is required before any auth flow starts.
- Servers: password prompts render reliably.
- SSH: a loaded ssh-agent no longer breaks every connection.

### Changed
- README: leads with the turnkey wins; documents the kldload extras.
- Repository history rewritten to a single canonical author identity
  (`Anthony <admin@zxplore.dev>`); packaging metadata now matches.

## [1.0.0] — 2026-07-28

Initial release. Browser (F1) / Transfer (F2) / Explorer (F3); native GUI
(Fyne) and terminal UI from one codebase; mock-CLI test suite proving every
feature without touching a real pool; embedded man page; read-only by default
with explicit unlock; audit log; encrypted datasets replicate raw.
