# zxplore

**A direct interface to your ZFS primitives — not a dashboard.**

`zxplore` is a fast, keyboard-driven console for the ZFS you already run. Browse
datasets and snapshots with a full properties + permissions view, snapshot on
tap, point-and-shoot replicate to any pool or host over SSH, and explore the
files inside any dataset, snapshot, or VM zvol — all from one terminal.

It's a **primitives** tool, not a management UI: every action maps to a plain
`zfs`/`zpool` command, so nothing is hidden and nothing is invented. It runs on
**any OpenZFS system** — Linux or FreeBSD.

> Your data, at every point in time, on any machine you choose — with a keypress.

## Why

On most systems you *back up* your data — a separate product, a schedule, a
restore procedure. With ZFS there is nothing to back up *to*: the filesystem is
already every version of itself, everywhere you've replicated it. `zxplore` is
the console that makes that ownership tangible — you see the versions, the
replicas, the boot environments, and you move them with a keypress.

## What it does

- **Browse** datasets/zvols with a live dossier: space accounting, every
  meaningful property, and *both* permission layers (POSIX/ACL + `zfs allow`).
- **Snapshot** on tap; sanoid-aware (flags automatic snapshots, never prunes them).
- **Replicate** point-and-shoot: incremental when a common base exists, streamed
  through `mbuffer`/`pv`, into a readonly target that never drifts — a local pool
  or a remote `host:pool` over SSH.
- **Explore** the files in any dataset, snapshot, or VM zvol (read-only, cloned).
- **Transact** — a scoped snapshot/rollback API (`zxplore-api` + `zxplore-txn`)
  so an app can bracket a risky operation with an instant, ~millisecond undo:
  *snapshot → migrate → rollback-or-commit* as a function.

## Install

**Build from source** — clone → build → install on any OpenZFS host (Linux or
FreeBSD). The GUI is Go + [Fyne](https://fyne.io) (cgo + OpenGL); `--tui` is a
pure-terminal fallback that needs no GL.

```
git clone https://github.com/kldload/zxplore
cd zxplore
make               # builds ./zxplore  (CGO_ENABLED=1 go build)
sudo make install  # installs binary + icon + .desktop
```

Install the build deps for your OS first (full list at the top of the
[`Makefile`](Makefile)):

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
```

Runtime needs `zfs`/`zpool`, `libGL`, and an X11 or Wayland session. Snapshot/
replication over SSH uses the system `ssh` (`sshpass` for one-time key
authorization); privileged actions elevate with `pkexec`. Headless: `zxplore --tui`.

## Usage

```
zxplore            # the native GUI console (default)
zxplore --tui      # terminal UI, for headless / SSH
```

**Keys:** `F1`/`F2` switch Browser/Transfer · `↑↓` `PgUp`/`PgDn` `Home`/`End`
move · `Ctrl+F` (or `/`) find · **right-click a dataset** for the full lifecycle
menu (snapshot / clone / replicate / boot-env / encryption / rollback / destroy)
· `Enter` or click a snapshot to roll back / clone / hold · `Alt+Q` quit.
Toolbar: **Servers…** (saved connections — key-first auth, jump hosts),
**Pools…** (scrub / trim / clear), **Boot Envs…**.

## Portability

The core (browse / snapshot / replicate / properties) is pure `zfs`/`zpool` and
runs anywhere OpenZFS does — Linux and FreeBSD. Linux-only extras (VM-zvol
explore via libvirt, the systemd unit for the API) degrade gracefully; FreeBSD
ships an `rc.d` script for the API. Nothing is required beyond OpenZFS + a shell.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
