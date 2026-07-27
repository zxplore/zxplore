# zxplor

**A direct interface to your ZFS primitives — not a dashboard.**

`zxplor` is a fast, keyboard-driven console for the ZFS you already run. Browse
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
already every version of itself, everywhere you've replicated it. `zxplor` is
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
- **Transact** — a scoped snapshot/rollback API (`zxplor-api` + `zxplor-txn`)
  so an app can bracket a risky operation with an instant, ~millisecond undo:
  *snapshot → migrate → rollback-or-commit* as a function.

## Install

**From a package** (once published):

```
sudo apt install zxplor      # Debian / Ubuntu
sudo dnf install zxplor      # Fedora / RHEL / Rocky
sudo pacman -S zxplor        # Arch    (or the AUR)
sudo pkg install zxplor      # FreeBSD
```

**From source** (any OpenZFS host):

```
git clone https://github.com/<org>/zxplor
cd zxplor && sudo make install
```

Requires `zfs`/`zpool` + `bash`; **recommended**: `fzf` (the interactive
console), `pv`, `mbuffer` (replication throughput). `python3` for the
transaction API. See [`packaging/`](packaging/) for the per-platform recipes.

## Usage

```
zxplor                 # the console — browse; ? for keys, Esc to quit
zxplor mc <target>     # pinned-target commander (WinSCP-for-ZFS)
zxplor replicate <dataset@snap> [pool | host:pool]
zxplor snap <dataset> [name]
```

Keys: `↵` snapshots · `^s` snap · `^r` replicate · `^o` explore files ·
`^p` properties · `^d` destroy · `?` help · `Esc` back.

## Portability

The core (browse / snapshot / replicate / properties) is pure `zfs`/`zpool` and
runs anywhere OpenZFS does — Linux and FreeBSD. Linux-only extras (VM-zvol
explore via libvirt, the systemd unit for the API) degrade gracefully; FreeBSD
ships an `rc.d` script for the API. Nothing is required beyond OpenZFS + a shell.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
