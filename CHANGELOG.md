# Changelog

All notable changes to zxplore. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.3.0] — unreleased

### Added
- **Auto Sync (F7, kldload only)** — scheduled replication, and whether it is
  actually running. A job *pulls*: the backup host fetches from the source, so
  a compromised source holds no credentials on the archive. Each row answers
  the three questions a timer cannot answer by existing — is it scheduled,
  when does it run next, and what is the newest snapshot that actually
  arrived. Clients and servers are two inventories: a **client** is a node
  this host backs up, a **server** is a box it syncs against. The saved-session
  manager is parameterised over the two rather than forked, so the key-first
  auth flow exists once. Terminal equivalent: `zxplore --sync
  {list,show,install,remove}` and `--sync-run JOB`.
- **Restore mode** in Transfer (F3) — the same transfer pointed the other way,
  to revive a host from its archive. `send -p` so mountpoints come back and
  `recv -x readonly` so the restored root is writable; a root filesystem that
  is readonly does not boot. The archive it restores *from* is never modified.
  Off by default: a backup must land readonly so nothing can write to it and
  diverge from the source.

### Fixed
- Restore of an encrypted archive sends *decrypted* when the key is loaded on
  the archive host. A backup pool is usually encrypted when the source is not
  — fiend's root is `encryption off`, its copy on onyx inherited onyx's
  `aes-256-gcm` — and a raw restore preserved that wrapping key, so the
  restored dataset became its own encryption root with `keystatus
  unavailable`: a rebuilt machine unable to mount its own filesystem without
  the backup server's passphrase. Sending decrypted lets the target apply its
  own policy. Verified by restoring fiend's root from the archive: writable,
  key available, kernel and `/etc/hostname` intact.
- A job's last run is recorded by the job, not only by systemd, so a
  successful `Run now` no longer leaves the tab showing the previous
  scheduled failure.
- Raw send is decided from a measured matrix instead of a single probe. `zfs
  send -w` on an *unencrypted* source is `-Lec`, and its embedded-data feature
  is refused by an encrypted receive — exactly the unencrypted-source into
  encrypted-archive case. It does not fail cleanly either: the receive dies
  while the sender's `pv` waits on stdin, so it presents as an indefinite
  hang. The old code also skipped `-w` when the encryption probe returned its
  *error* value, sending plaintext to the archive in the one case raw sending
  exists to prevent. Now: raw unless the source is positively unencrypted and
  the target inherits encryption, with the target's nearest existing ancestor
  probed because the target does not exist on a first run.
- **Builder (F2)** — design a pool from the disks the box has, and see what
  it yields before anything is written. The shelf marks in-use disks and
  why; ticking disks for data proposes mirrors, RAIDZ1/2/3 in sane vdev
  widths, dRAID2 and a stripe, each with usable / raw / what-it-survives, a
  badge and the reason; free NVMe/SSD become SLOG and cache proposals. The
  layout is editable vdev by vdev, the exact `zpool create` line is shown
  (by-id names, explicit ashift and compression) with the warnings an
  operator would raise, then **Dry run** (`zpool create -n`) and **Create**
  (typed confirmation). **See a pool** draws an imported pool's vdev tree in
  the same rows.
- Builder from a terminal: `zxplore --builder {disks,suggest,topology,dry-run,create}`,
  with zpool's own vdev grammar and disk names as the spec.
- **Observe (F5)** — one second of a pool as verdicts, not counters. Two
  kstat reads around a `zpool iostat -l` interval give every counter a rate;
  Judge turns the sample into sentences that name the number read and the
  knob that changes it: slow I/Os, a vdev many times slower than its
  siblings, pool latency, the write throttle against `zfs_dirty_data_max`,
  txg sync time against `zfs_txg_timeout`, ARC hit rate and a cap pinned
  below the host's RAM, memory-pressure throttling, sync writes landing on a
  pool with no SLOG (with the datasets doing it, from the objset kstats), ZIL
  stalls, capacity, fragmentation, dedup for nothing, prefetch, the busiest
  datasets, error reports. Live mode, the vdev latency table, busiest
  datasets, events and a kstat browser with rates. Unprivileged.
- Observe from a terminal: `zxplore --observe [POOL] [watch|json|kstat GROUP|events]`.

### Changed
- **Tab order and keys.** Builder is F2 and Observe F5; Transfer moved to
  F3, Explorer to F4, Containers to F6. The Builder sits second on purpose —
  build the pool, then browse what you built. The manual and the in-app
  hints say so.

## [1.2.0] — 2026-08-19

### Added
- **Containers (F4)** — the container estate as the storage it actually is.
  Detects docker or podman and drives both through one code path; lists
  containers by name and every image with its real on-disk size; `start`,
  `stop`, `restart` and `remove` as buttons, coloured by what they do.
- Containers: **estate snapshot, rollback and replicate** — one ZFS snapshot
  across the container root, restored or sent like any other dataset.
- Containers: the same estate verbs from a terminal —
  `zxplore --containers {list,snapshot,snapshots,rollback,replicate}`.
- The manual now documents the container commands, and the in-app `?` view
  renders it wherever `man` was never installed.

### Fixed
- Containers: the storage driver is stated plainly, including when layers are
  ordinary files rather than datasets. An `overlay` driver on a ZFS box is a
  real configuration, and implying otherwise misrepresents what a snapshot
  would capture.
- Containers: a failed engine call reported itself as a working engine with an
  empty list; a permission error looked identical to "no containers".
- Containers: the list was blank because the row index was wrong.
- Servers: the public key could be read but not copied.
- The selected dataset now reads blue against the light theme.

### Changed
- The icon joins the family look — borderless mark, no tile.
- Screenshots in the README are generated by `docs/annotate.py` from raw
  captures, so they can be re-rendered when the UI moves instead of freezing
  at the version they were drawn for.

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
