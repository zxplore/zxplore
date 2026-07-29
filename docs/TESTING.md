# zxplore test audit

Every feature maps to the test(s) that prove it. Run everything with
`make test` (vets and tests both build flavors). No test touches a real
pool: the mock suite plants fake `zfs`/`zpool`/`pkexec`/`ssh` executables on
`PATH` that serve canned fixture output and append each invocation to a
command log, so the engine is exercised through the exact code path
production uses — argv construction → shelling out → parsing. `HOME` and
`XDG_CONFIG_HOME` are redirected per test, so the operator's audit log,
servers, favorites, and known_hosts are never touched.

| File | What it proves |
|---|---|
| `engine_unit_test.go` | Pure functions: parsing, quoting, plans, stores |
| `engine_mockcli_test.go` | Engine end-to-end through the mock CLI |
| `gui_render_test.go` (`-tags gui`) | Manual + dossier text rendering |

## Feature → proof

| Feature | Test(s) |
|---|---|
| Dataset browser (list, sizes, deferred snapshot counts) | `TestMockListDatasets`, `TestMockSnapshotCounts` |
| Snapshot list per dataset | `TestMockListSnapshots` |
| Property dossier / editor (settable detection, sources) | `TestMockDatasetProps` |
| Pool overview + pool list | `TestMockPoolsOverview`, `TestMockListPoolsChildrenSubtree` |
| Children / delegated subtree views | `TestMockListPoolsChildrenSubtree` |
| Health analysis (vdev READ/WRITE/CKSUM counters) | `TestVdevErrors` |
| Snapshot lifecycle (create/rollback/clone/hold/release/destroy) | `TestMockMutationCommands`, `TestMockAuditLog` |
| Dataset lifecycle (create fs/zvol, rename, mount, destroy, set) | `TestMockMutationCommands` |
| Bookmarks (list/create/refuse non-snapshot) | `TestMockMutationCommands` |
| Boot environments (bootfs DERIVED across pools — zroot too) | `TestMockBootEnvs` |
| Encryption (passphrase on stdin, never argv/audit; create feeds twice) | `TestMockPassphraseStdin` |
| Privilege policy (unprivileged first; pkexec ONLY on permission errors) | `TestMockElevationFallback`, `TestMockNoElevationOnRealError` |
| Locale-proof elevation detection (`LC_ALL=C` on local commands) | `TestLocalCommandArgv`, `TestNeedsElevation` |
| Audit log (every mutation, tab format, no secrets) | `TestMockAuditLog`, `TestMockPassphraseStdin` |
| Replication pipeline (raw `-w` for encrypted, incremental base, resume token, quoted remote legs) | `TestMockReplicatePipeline`, `TestRunReplicateQuoting` |
| Replication delegation grants (pkexec local, remote sudo with password on stdin, audited without secrets) | `TestMockGrantReplicationPerms`, `TestGrantCommand` |
| Remote execution (accept-new host keys, every word shell-quoted, `sh -c` scripts survive, spacey names survive) | `TestRemoteCommandQuoted`, `TestRemoteShCScriptSurvives`, `TestMockRemoteRoundTrip` |
| SSH options (port, identity file, ProxyJump) | `TestSSHOptsAndPrefix` |
| Host-key pinning in the password bootstrap (record first contact, refuse changed key) | `TestHostKeyAcceptNew` |
| Reinstall recovery (changed key classified → ForgetHostKey → new key pins) | `TestForgetHostKeyRecovery` |
| Snapshot file explorer (ls parsing, per-file versions across snapshots, directory listing) | `TestParseLsLine`, `TestFileVersionsAndListDir` |
| Explorer pool→dataset chooser (browsable = mounted at a real path; pool roots/legacy excluded) | `TestMockListMounts` |
| Restore plans (overwrite / alongside / directory merge) | `TestRestoreArgv` |
| zfs diff (M/+/-/R rows, rename target) | `TestMockSnapshotDiff`, `TestDiffCommand` |
| Pool maintenance (scrub start/stop, trim, clear, import) | `TestMockMutationCommands` |
| Pool import scan ("no pools available" is empty, not error) | `TestMockImportablePools` |
| Platform detection (`zfs version`, header chip) | `TestMockVersionAndPlatform` |
| Empty-host guidance (no ZFS / no pools / OK) | `TestMockDiagnoseHost`, `TestWelcomeText` |
| Saved servers (upsert/delete/persist, key path sanitizing, Host mapping) | `TestServerStore` |
| One-shot setup (generate key → skip authorize when key works → demand password when it doesn't) | `TestMockSetupServer`, `TestMockSetupServerNeedsPassword` |
| Actionable auth errors (agent spray / unauthorized key → next step) | `TestFriendlySSH` |
| Favorites (parse targets, dedup, cap, persist) | `TestParseTarget`, `TestAddFavoriteDedupAndCap`, `TestFavoritesPersistence` |
| Version stamp (`0.1.0 b<N>`) | `TestVersionFull` |
| Shell quoting (hostile names inert) | `TestShellQuote`, `TestRunReplicateQuoting` |
| Human sizes | `TestHuman` |
| Embedded manual rendering (overstrike strip, headers colored, mdoc fix) | `TestStripOverstrike`, `TestManualSegments`, `TestRenderManual` |
| Dossier topic coloring | `TestDossierSegments` |

## Not covered here (verified on a real desktop / real pool)

- Fyne widget behavior (lists, dialogs, keyboard nav) — headless rendering
  of the text content is tested; interaction is eyeballed on a desktop.
- Real `zfs`/`zpool` semantics — the mocks pin OUR side of the contract
  (exact argv out, parsing in); the zxdemo scratch pool on a real host
  remains the proving ground for the binaries themselves.
- `AuthorizeKey` against a live sshd (network); its host-key policy is
  tested via `TestHostKeyAcceptNew`.
