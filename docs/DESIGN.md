# zxplor — design

A direct, keyboard-driven interface to ZFS primitives. **Not a dashboard:** every
action is a plain `zfs`/`zpool` command you could have typed yourself. The point
is to make ZFS *ownership* tangible — you see every version of your data and move
it with a keypress — without hiding or inventing anything.

## Surfaces (same primitives, different front doors)
- **Console** (`zxplor`) — an fzf-driven browser: datasets/snapshots with a
  full properties + permissions dossier, snapshot on tap, point-and-shoot
  replicate, file explore. Plus `zxplor mc` — a pinned-target commander.
- **Transaction API** (`zxplor-api` + `zxplor-txn`) — a scoped
  snapshot/rollback service so an app can bracket a risky operation with an
  instant undo: `begin → migrate → rollback|commit`. Per-caller scoping, audited.

## Portability
Core = plain `zfs`/`zpool` → runs anywhere OpenZFS does (Linux, FreeBSD). Optional
extras degrade gracefully: `fzf` (interactive console), `pv`/`mbuffer`
(replication throughput), `libvirt` (VM-zvol explore, Linux), the service
manager (`systemd` unit on Linux, `rc.d` on FreeBSD).

## Replication contract
Incremental when the target already holds a common snapshot, else a full send;
streamed through `mbuffer`/`pv`; received with `-F` into a target forced
`readonly=on`, `canmount=noauto` so a replica never drifts out of its incremental
chain. Local pool or a remote `host:pool` over SSH (your key/agent; zxplor
never manages credentials).

## Transaction API — the hard constraint
You cannot `zfs rollback` a zvol the guest is writing live. Two modes:
1. **data-zvol** — app data on a separate zvol; quiesce → unmount → rollback →
   remount → restart. No reboot.
2. **boot-environment** — snapshot the OS zvol; "rollback" = reboot onto it.

## Roadmap
Function-key sections (Filesystems · Transfer · Restore points · Pools), a
two-pane transfer view, and first-class encryption/`zfs allow`/`zfs diff`.
