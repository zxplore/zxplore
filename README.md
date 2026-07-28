# zxplore

**A direct interface to your ZFS primitives — not a dashboard.**

`zxplore` is a fast, keyboard-driven console for the ZFS you already run.
Browse datasets and snapshots with a full properties + permissions dossier,
snapshot on tap, **restore any file from any snapshot**, diff two points in
time, and point-and-shoot replicate to any pool or host over SSH — from one
binary, on **any OpenZFS system** (Linux distros or FreeBSD).

It's a **primitives** tool, not a management UI: every action maps to a plain
`zfs`/`zpool` command. Nothing hidden, nothing invented — destructive actions
show the literal command before running, and every executed mutation lands in
an audit log.

> Your data, at every point in time, on any machine you choose — with a keypress.

## Why

On most systems you *back up* your data — a separate product, a schedule, a
restore procedure. With ZFS there is nothing to back up *to*: the filesystem is
already every version of itself, everywhere you've replicated it. `zxplore` is
the console that makes that ownership tangible — you see the versions, the
replicas, the boot environments, and you move them with a keypress.

## What it does

- **Browse** datasets/zvols with a live dossier: space accounting, every
  property with its source (`local` / `inherited from …` / `default`), and
  *both* permission layers (POSIX/ACL + `zfs allow`).
- **Snapshot explorer** — the fusion feature. Browse the files of a dataset,
  live or transparently inside any snapshot (`.zfs/snapshot`). Pick a file and
  see it across **every snapshot that contains it**, with size/mtime deltas.
  Restore any version into the live dataset — overwrite, or alongside as
  `name.from-SNAPSHOT` — no manual `cp` gymnastics. Deleted files included.
- **Diff** — `zfs diff` rendered as a colored change pane between two
  snapshots (or snapshot ↔ live), filterable by path.
- **Snapshot / rollback / clone / hold** on tap, with explicit warnings about
  exactly what a rollback destroys.
- **Replicate** point-and-shoot: incremental when a common base exists, into a
  readonly target that never drifts — local pool or remote `host:pool` over
  SSH, including remote → remote.
- **Boot environments** — derived from the pool's `bootfs` (never a hardcoded
  name): create, roll back, delete.
- **Encryption** — unlock/lock, change passphrase, create encrypted child;
  passphrases travel on stdin, never on a command line.
- **Pools** — health overview pinned at the top; scrub / trim / clear.
- **Servers** — WinSCP-style saved connections: key-first auth (generate,
  paste, or file), one-time password authorization, ProxyJump chains.
- **Transact** — a scoped snapshot/rollback API (`zxplore-api` + `zxplore-txn`)
  so an app can bracket a risky operation with an instant, ~millisecond undo:
  *snapshot → migrate → rollback-or-commit* as a function.

## Install

```
git clone https://github.com/zxplore/zxplore
cd zxplore
make               # builds ./zxplore (GUI+TUI) and ./zxplore-tui (static)
sudo make install  # binaries + man page + icon + .desktop
```

Two binaries come out of one tree:

| binary | what | needs |
|---|---|---|
| `zxplore` | native GUI (Fyne) + `--tui` | cgo, OpenGL, X11/Wayland |
| `zxplore-tui` | terminal-only, **fully static** | nothing — `scp` it anywhere |

Headless box? `make zxplore-tui` needs only the Go toolchain — no GL, no dev
headers. Or install straight from source:

```
go install github.com/zxplore/zxplore@latest     # static TUI build
```

GUI build deps per distro (full list at the top of the [`Makefile`](Makefile)):

```
# Fedora / RHEL / Rocky
sudo dnf install -y golang gcc pkgconf-pkg-config mesa-libGL-devel \
  libX11-devel libXcursor-devel libXrandr-devel libXinerama-devel \
  libXi-devel libXxf86vm-devel wayland-devel libxkbcommon-devel fontconfig-devel

# Debian / Ubuntu
sudo apt-get install -y golang gcc pkg-config libgl1-mesa-dev xorg-dev \
  libwayland-dev libxkbcommon-dev libfontconfig1-dev

# Arch
sudo pacman -S --needed go gcc pkgconf libgl libxcursor libxrandr \
  libxinerama libxi wayland libxkbcommon fontconfig

# FreeBSD
pkg install -y go pkgconf mesa-libs libX11 libxkbcommon wayland fontconfig
```

Runtime: `zfs`/`zpool` (and for the GUI, `libGL` + an X11/Wayland session).

## Usage

```
zxplore            # the native GUI console (default)
zxplore --tui      # terminal UI, for headless / SSH
zxplore --version
man zxplore        # full documentation
```

**Keys:** `F1`/`F2` switch Browser/Transfer · `↑↓` `PgUp`/`PgDn` `Home`/`End`
move · `Ctrl+F` (or `/`) find · **right-click a dataset** for the full
lifecycle menu (snapshot / explorer / diff / clone / replicate / boot-env /
encryption / rollback / destroy) · `Enter` or click a snapshot to roll back /
clone / browse its files / hold · `Esc` dismiss · `Alt+Q` quit.
Toolbar: **Servers…**, **Pools…**, **Boot Envs…**.

## Security model

- Runs **unprivileged**; elevates per command — `pkexec` locally, the
  connected (ideally [delegated](https://openzfs.github.io/openzfs-docs/man/master/8/zfs-allow.8.html))
  user remotely. `zfs allow send,snapshot,hold,diff,mount user pool/ds` is all
  a remote needs for the read/replicate paths.
- **Key-first SSH.** A password is used at most once (to authorize a key) and
  never stored. `~/.config/zxplore/servers.json` holds key *paths* only.
- **Passphrases on stdin** — encryption keys never appear in `ps` or logs.
- **Audit log** — every executed mutating command is appended to
  `~/.local/state/zxplore/audit.log` (timestamp, host, exact argv).
- **Dry-run honesty** — restore/diff/replicate dialogs show the literal
  command before you confirm it.

## Portability

The core is pure `zfs`/`zpool` + POSIX shell and runs anywhere OpenZFS does —
Linux and FreeBSD, local or over SSH (the remote end needs nothing but ZFS
itself; file listing uses POSIX `ls`, restores use `cp -a`). Linux-only extras
(the systemd unit for the API) degrade gracefully; FreeBSD ships an `rc.d`
script. The static `zxplore-tui` binary has zero runtime dependencies.

## Documentation

- [`man zxplore`](docs/zxplore.1) — the manual: views, keys, privileges, files.
- [`docs/DESIGN.md`](docs/DESIGN.md) — why it's built the way it is.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
