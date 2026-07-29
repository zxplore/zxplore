// engine_mockcli_test.go — integration tests through a MOCK CLI: fake
// zfs/zpool/pkexec/ssh executables on PATH serve canned fixture output and
// append every invocation to a command log. This proves the engine end to end
// (argv construction → shelling out → parsing) with zero real pools, as the
// same code path production uses. HOME and XDG_CONFIG_HOME are redirected per
// test so the audit log and stores never touch the operator's real files.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mock builds a bin dir of fake executables, prepends it to PATH, isolates
// HOME, and returns a reader for the command log every script appends to.
type mock struct {
	bin, cmdlog string
	t           *testing.T
}

func newMock(t *testing.T) *mock {
	t.Helper()
	m := &mock{bin: t.TempDir(), t: t}
	m.cmdlog = filepath.Join(t.TempDir(), "cmd.log")
	t.Setenv("ZX_CMDLOG", m.cmdlog)
	t.Setenv("PATH", m.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir()) // audit log isolation
	return m
}

func (m *mock) script(name, body string) {
	m.t.Helper()
	if err := os.WriteFile(filepath.Join(m.bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		m.t.Fatal(err)
	}
}

func (m *mock) log() string {
	b, _ := os.ReadFile(m.cmdlog)
	return string(b)
}

// zfsFixture is the standard happy-path zfs: known reads answer with canned
// rows, every other invocation (the mutations) just logs and succeeds.
const zfsFixture = `echo "zfs $*" >> "$ZX_CMDLOG"
case "$*" in
"list -H -p -o name,used,refer -t filesystem,volume")
  printf 'tank\t5368709120\t1024\ntank/data\t2147483648\t1048576\n' ;;
"list -H -o name -t snapshot")
  printf 'tank/data@a\ntank/data@b\ntank@x\nnot-a-snapshot\n' ;;
"list -H -o name,used,creation -s creation -t snapshot -r -d 1 tank/data")
  printf 'tank/data@a\t0\tMon Jul 20  9:00 2026\ntank/data@b\t8192\tTue Jul 21  9:00 2026\n' ;;
"list -H -o name,used,creation -s creation -t snapshot -r -d 1 backup/data")
  printf 'backup/data@a\t0\tMon Jul 20  9:05 2026\n' ;;
"list -H -p -o name,used,refer -t filesystem,volume -r -d 1 tank")
  printf 'tank\t5368709120\t1024\ntank/data\t2147483648\t1048576\n' ;;
"list -H -p -o name,used,refer -t filesystem,volume -r tank/data")
  printf 'tank/data\t2147483648\t1048576\ntank/data/inner\t1024\t512\n' ;;
"list -H -t bookmark -o name,creation -s creation -r -d 1 tank/data")
  printf 'tank/data#keep\tSun Jul 19  9:00 2026\n' ;;
"list -H -o name,mountpoint,mounted -r rpool")
  printf 'rpool\tnone\tno\nrpool/ROOT\tnone\tno\nrpool/home\t/home\tyes\nrpool/var\tlegacy\tyes\n' ;;
"list -H -t snapshot -o name,used,creation -s creation zroot/ROOT/default")
  printf 'zroot/ROOT/default@pre-upgrade\t1024\tMon Jul 20  9:00 2026\n' ;;
"get -H -o value encryption tank/data") echo aes-256-gcm ;;
"get -H -o value encryption tank/plain") echo off ;;
"get -H -o value receive_resume_token backup/data") echo - ;;
"get -H -o value receive_resume_token backup/rez") echo 1-mocktoken-abc ;;
"get -H -o value mountpoint tank/data") echo /tank/data ;;
"get -H -o value mounted tank/data") echo yes ;;
"get -H -o value mountpoint tank/legacy") echo legacy ;;
"get -H -o property,value,source all tank/data")
  printf 'name\ttank/data\t-\ncompression\tzstd\tlocal\nrecordsize\t128K\tdefault\n' ;;
"diff -H tank/data@a tank/data@b")
  printf 'M\t/tank/data/etc/pf.conf\n+\t/tank/data/new\nR\t/tank/data/old\t/tank/data/renamed\n' ;;
"version") printf 'zfs-2.4.3-mock\nzfs-kmod-2.4.3-mock\n' ;;
*) exit 0 ;;
esac`

const zpoolFixture = `echo "zpool $*" >> "$ZX_CMDLOG"
case "$*" in
"list -H -o name") printf 'tank\n' ;;
"list -H -o name,health,size,alloc,free,capacity,fragmentation,dedupratio")
  printf 'tank\tONLINE\t10G\t4.0G\t6.0G\t40%%\t3%%\t1.00x\n' ;;
"get -H -o value bootfs") printf -- '-\nzroot/ROOT/default\n' ;;
"import") printf '   pool: oldtank\n     id: 1234567890\n  state: ONLINE\n' ;;
"status tank") printf 'scan: scrub repaired 0B\nerrors: No known data errors\n' ;;
*) exit 0 ;;
esac`

// pkexec mock: mark elevation, log, then actually run the wrapped command.
const pkexecFixture = `echo "pkexec $*" >> "$ZX_CMDLOG"
export ZX_ELEVATED=1
exec "$@"`

// ssh mock: log the full argv, then execute the final argument the way a
// remote POSIX shell would — proving the quoted command string round-trips.
const sshFixture = `echo "ssh $*" >> "$ZX_CMDLOG"
for a; do last=$a; done
eval "$last"`

func stdMock(t *testing.T) *mock {
	m := newMock(t)
	m.script("zfs", zfsFixture)
	m.script("zpool", zpoolFixture)
	m.script("pkexec", pkexecFixture)
	m.script("ssh", sshFixture)
	return m
}

// ── browse: datasets, snapshots, properties ─────────────────────────────────

func TestMockListDatasets(t *testing.T) {
	stdMock(t)
	rows, err := ListDatasets(LocalHost())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Name != "tank" || rows[0].Used != "5.0G" || rows[0].Refer != "1.0K" || rows[0].Snaps != -1 {
		t.Errorf("row 0 parsed wrong: %+v (Snaps must be -1 = not counted yet)", rows[0])
	}
	if rows[1].Name != "tank/data" || rows[1].Used != "2.0G" || rows[1].Refer != "1.0M" {
		t.Errorf("row 1 parsed wrong: %+v", rows[1])
	}
}

func TestMockSnapshotCounts(t *testing.T) {
	stdMock(t)
	counts, err := SnapshotCounts(LocalHost())
	if err != nil {
		t.Fatal(err)
	}
	if counts["tank/data"] != 2 || counts["tank"] != 1 || len(counts) != 2 {
		t.Errorf("counts wrong: %v (junk lines must be ignored)", counts)
	}
}

func TestMockListSnapshots(t *testing.T) {
	stdMock(t)
	snaps, err := ListSnapshots(LocalHost(), "tank/data")
	if err != nil || len(snaps) != 2 {
		t.Fatalf("snaps=%v err=%v", snaps, err)
	}
	if snaps[1].Name != "tank/data@b" || snaps[1].Used != "8192" {
		t.Errorf("snapshot row parsed wrong: %+v", snaps[1])
	}
}

func TestMockDatasetProps(t *testing.T) {
	stdMock(t)
	ps, err := DatasetProps(LocalHost(), "tank/data")
	if err != nil || len(ps) != 3 {
		t.Fatalf("props=%v err=%v", ps, err)
	}
	byName := map[string]Prop{}
	for _, p := range ps {
		byName[p.Name] = p
	}
	if byName["name"].Settable {
		t.Error("read-only prop (source '-') must not be settable")
	}
	if !byName["compression"].Settable || byName["compression"].Source != "local" {
		t.Errorf("local prop wrong: %+v", byName["compression"])
	}
}

func TestMockListMounts(t *testing.T) {
	stdMock(t)
	rows, err := ListMounts(LocalHost(), "rpool")
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Browsable() {
		t.Error("pool root none/no must not be browsable")
	}
	if !rows[2].Browsable() || rows[2].Mountpoint != "/home" {
		t.Errorf("mounted child must be browsable: %+v", rows[2])
	}
	if rows[3].Browsable() {
		t.Error("legacy mountpoint must not be browsable even when mounted")
	}
}

func TestMockMountpoint(t *testing.T) {
	stdMock(t)
	mp, err := Mountpoint(LocalHost(), "tank/data")
	if err != nil || mp != "/tank/data" {
		t.Errorf("mp=%q err=%v", mp, err)
	}
	if _, err := Mountpoint(LocalHost(), "tank/legacy"); err == nil || !strings.Contains(err.Error(), "browsable") {
		t.Errorf("legacy mountpoint must error clearly, got %v", err)
	}
}

func TestMockListPoolsChildrenSubtree(t *testing.T) {
	stdMock(t)
	pools, err := ListPools(LocalHost())
	if err != nil || len(pools) != 1 || pools[0] != "tank" {
		t.Errorf("pools=%v err=%v", pools, err)
	}
	kids, err := ListChildren(LocalHost(), "tank")
	if err != nil || len(kids) != 1 || kids[0].Name != "tank/data" {
		t.Errorf("children must exclude the dataset itself: %v err=%v", kids, err)
	}
	sub, err := ListSubtree(LocalHost(), "tank/data")
	if err != nil || len(sub) != 2 {
		t.Errorf("subtree=%v err=%v", sub, err)
	}
}

// ── mutations: exact argv proven via the command log ────────────────────────

func TestMockMutationCommands(t *testing.T) {
	m := stdMock(t)
	h := LocalHost()
	steps := []struct {
		run  func() error
		want string
	}{
		{func() error { return Rollback(h, "tank/data@a") }, "zfs rollback -r tank/data@a"},
		{func() error { return Clone(h, "tank/data@a", "tank/clone") }, "zfs clone tank/data@a tank/clone"},
		{func() error { return HoldSnap(h, "tank/data@a") }, "zfs hold zxplore tank/data@a"},
		{func() error { return ReleaseSnap(h, "tank/data@a") }, "zfs release zxplore tank/data@a"},
		{func() error { return DestroySnapshot(h, "tank/data@a") }, "zfs destroy tank/data@a"},
		{func() error { return DestroyDataset(h, "tank/tmp") }, "zfs destroy -r tank/tmp"},
		{func() error { return CreateDataset(h, "tank/new", "") }, "zfs create -p tank/new"},
		{func() error { return CreateDataset(h, "tank/vol", "10G") }, "zfs create -V 10G tank/vol"},
		{func() error { return RenameDataset(h, "tank/a", "tank/b") }, "zfs rename tank/a tank/b"},
		{func() error { return SetMounted(h, "tank/data", false) }, "zfs unmount tank/data"},
		{func() error { return SetProp(h, "tank/data", "compression", "lz4") }, "zfs set compression=lz4 tank/data"},
		{func() error { return CreateBookmark(h, "tank/data@a", "keep2") }, "zfs bookmark tank/data@a tank/data#keep2"},
		{func() error { return UnloadKey(h, "tank/data") }, "zfs unload-key tank/data"},
		{func() error { return ScrubPool(h, "tank", true) }, "zpool scrub tank"},
		{func() error { return ScrubPool(h, "tank", false) }, "zpool scrub -s tank"},
		{func() error { return TrimPool(h, "tank") }, "zpool trim tank"},
		{func() error { return ClearPool(h, "tank") }, "zpool clear tank"},
		{func() error { return ImportPool(h, "oldtank") }, "zpool import oldtank"},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.want, err)
		}
	}
	logText := m.log()
	for _, s := range steps {
		if !strings.Contains(logText, s.want+"\n") {
			t.Errorf("command log missing exact %q", s.want)
		}
	}
	if err := CreateBookmark(h, "not-a-snapshot", "x"); err == nil {
		t.Error("bookmarking a non-snapshot must refuse")
	}
}

// ── elevation: unprivileged first, pkexec only on permission failures ───────

func TestMockElevationFallback(t *testing.T) {
	m := newMock(t)
	m.script("pkexec", pkexecFixture)
	m.script("zfs", `echo "zfs $*" >> "$ZX_CMDLOG"
if [ "$ZX_ELEVATED" = "1" ]; then exit 0; fi
echo "cannot destroy 'tank/x': permission denied" >&2
exit 1`)
	if err := DestroySnapshot(LocalHost(), "tank/x@s"); err != nil {
		t.Fatalf("permission failure must retry elevated and succeed: %v", err)
	}
	if !strings.Contains(m.log(), "pkexec zfs destroy tank/x@s") {
		t.Error("pkexec retry not recorded")
	}
}

func TestMockNoElevationOnRealError(t *testing.T) {
	m := newMock(t)
	m.script("pkexec", pkexecFixture)
	m.script("zfs", `echo "zfs $*" >> "$ZX_CMDLOG"
echo "cannot open 'tank/x': dataset does not exist" >&2
exit 1`)
	err := DestroySnapshot(LocalHost(), "tank/x@s")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("real error must surface verbatim, got %v", err)
	}
	if strings.Contains(m.log(), "pkexec") {
		t.Error("a NON-permission failure must never trigger pkexec")
	}
}

// ── encryption: passphrases travel on stdin, never argv, never the audit log ─

func TestMockPassphraseStdin(t *testing.T) {
	m := newMock(t)
	secrets := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("ZX_SECRETS", secrets)
	m.script("zfs", `echo "zfs $*" >> "$ZX_CMDLOG"
case "$1" in load-key|change-key|create) cat > "$ZX_SECRETS" ;; esac
exit 0`)
	if err := LoadKey(LocalHost(), "tank/sec", "hunter2"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(secrets)
	if string(got) != "hunter2\n" {
		t.Errorf("passphrase must arrive on stdin, got %q", got)
	}
	if strings.Contains(m.log(), "hunter2") {
		t.Error("passphrase leaked into a command line")
	}
	audit, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local/state/zxplore/audit.log"))
	if !strings.Contains(string(audit), "zfs load-key tank/sec") {
		t.Error("load-key not audited")
	}
	if strings.Contains(string(audit), "hunter2") {
		t.Error("passphrase leaked into the audit log")
	}
	if err := CreateEncrypted(LocalHost(), "tank/enc", "pw"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(secrets); string(got) != "pw\npw\n" {
		t.Errorf("create-encrypted must feed the passphrase twice, got %q", got)
	}
}

// ── audit log ───────────────────────────────────────────────────────────────

func TestMockAuditLog(t *testing.T) {
	stdMock(t)
	if _, err := SnapshotNow(LocalHost(), "tank/data", "t1"); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local/state/zxplore/audit.log"))
	if err != nil {
		t.Fatal("audit log not written:", err)
	}
	if !strings.Contains(string(audit), "\tlocal\tzfs snapshot tank/data@t1") {
		t.Errorf("audit entry wrong: %q", audit)
	}
}

// ── boot environments (derived bootfs, no pool-name hardcode) ───────────────

func TestMockBootEnvs(t *testing.T) {
	stdMock(t)
	if bd := bootDataset(LocalHost()); bd != "zroot/ROOT/default" {
		t.Fatalf("bootDataset must skip '-' pools and find zroot: %q", bd)
	}
	bes, bd, err := ListBootEnvs(LocalHost())
	if err != nil || bd != "zroot/ROOT/default" || len(bes) != 1 {
		t.Fatalf("bes=%v bd=%q err=%v", bes, bd, err)
	}
	if bes[0].Snapshot != "zroot/ROOT/default@pre-upgrade" {
		t.Errorf("BE row wrong: %+v", bes[0])
	}
}

// ── platform detection & host diagnosis ─────────────────────────────────────

func TestMockVersionAndPlatform(t *testing.T) {
	stdMock(t)
	if v := ZFSVersion(LocalHost()); v != "2.4.3-mock" {
		t.Errorf("ZFSVersion = %q", v)
	}
	if p := HostPlatform(LocalHost()); !strings.HasPrefix(p, "OpenZFS 2.4.3-mock") {
		t.Errorf("HostPlatform = %q", p)
	}
}

func TestMockDiagnoseHost(t *testing.T) {
	stdMock(t)
	if d := DiagnoseHost(LocalHost()); d != HostOK {
		t.Errorf("with pools: %v, want HostOK", d)
	}
	m2 := newMock(t) // fresh PATH: zfs exists, zpool reports no pools
	m2.script("zfs", `exit 0`)
	m2.script("zpool", `case "$*" in "list -H -o name") : ;; *) exit 0 ;; esac`)
	if d := DiagnoseHost(LocalHost()); d != HostNoPools {
		t.Errorf("no pools: %v, want HostNoPools", d)
	}
	t.Setenv("PATH", t.TempDir()) // no zfs anywhere
	if d := DiagnoseHost(LocalHost()); d != HostNoZFS {
		t.Errorf("no CLI: %v, want HostNoZFS", d)
	}
}

// ── pools overview & import scan ────────────────────────────────────────────

func TestMockPoolsOverview(t *testing.T) {
	stdMock(t)
	out := PoolsOverview(LocalHost())
	for _, want := range []string{"tank", "ONLINE"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview missing %q:\n%s", want, out)
		}
	}
}

func TestMockImportablePools(t *testing.T) {
	stdMock(t)
	names, err := ImportablePools(LocalHost())
	if err != nil || len(names) != 1 || names[0] != "oldtank" {
		t.Errorf("names=%v err=%v", names, err)
	}
	m2 := newMock(t)
	m2.script("pkexec", pkexecFixture)
	m2.script("zpool", `echo "no pools available to import" >&2; exit 1`)
	names, err = ImportablePools(LocalHost())
	if err != nil || names != nil {
		t.Errorf("'no pools available' must be empty result, not error: %v %v", names, err)
	}
}

// ── zfs diff ────────────────────────────────────────────────────────────────

func TestMockSnapshotDiff(t *testing.T) {
	stdMock(t)
	rows, err := SnapshotDiff(LocalHost(), "tank/data@a", "tank/data@b")
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Change != "M" || rows[0].Path != "/tank/data/etc/pf.conf" {
		t.Errorf("modified row wrong: %+v", rows[0])
	}
	if rows[2].Change != "R" || rows[2].Extra != "/tank/data/renamed" {
		t.Errorf("rename row must carry the new path: %+v", rows[2])
	}
}

// ── snapshot file explorer against a real filesystem ────────────────────────

func TestFileVersionsAndListDir(t *testing.T) {
	stdMock(t)
	mp := filepath.Join(t.TempDir(), "mp")
	for _, snap := range []string{"daily-1", "daily-2"} {
		dir := filepath.Join(mp, ".zfs/snapshot", snap, "etc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pf.conf"), []byte(snap), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := FileVersions(LocalHost(), mp, "etc/pf.conf")
	if err != nil || len(vs) != 2 {
		t.Fatalf("vs=%v err=%v", vs, err)
	}
	if vs[0].Snapshot != "daily-1" || vs[1].Snapshot != "daily-2" {
		t.Errorf("snapshot names wrong: %+v", vs)
	}
	entries, err := ListDir(LocalHost(), filepath.Join(mp, ".zfs/snapshot/daily-1/etc"))
	if err != nil || len(entries) != 1 || entries[0].Name != "pf.conf" || entries[0].Dir {
		t.Errorf("entries=%v err=%v", entries, err)
	}
}

// ── replication delegation (zfs allow grants) ───────────────────────────────

func TestMockGrantReplicationPerms(t *testing.T) {
	m := newMock(t)
	secrets := filepath.Join(t.TempDir(), "sudo-stdin")
	t.Setenv("ZX_SECRETS", secrets)
	m.script("zfs", `echo "zfs $*" >> "$ZX_CMDLOG"; exit 0`)
	m.script("pkexec", pkexecFixture)
	m.script("ssh", sshFixture)
	m.script("sudo", `echo "sudo $*" >> "$ZX_CMDLOG"; cat > "$ZX_SECRETS"; exit 0`)

	// Local: elevates via pkexec, no password involved.
	if err := GrantReplicationPerms(LocalHost(), "zexp", ReplRecvPerms, "tank/backups", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.log(), "pkexec zfs allow -u zexp "+ReplRecvPerms+" tank/backups") {
		t.Errorf("local grant not elevated via pkexec:\n%s", m.log())
	}

	// Remote: sudo -S with the password on stdin, never on the command line.
	h := Host{SSH: "admin@fiend"}
	if err := GrantReplicationPerms(h, "admin", ReplSendPerms, "rpool/home/admin", "Passw0rd"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.log(), "sudo -S -p  -- zfs allow -u admin "+ReplSendPerms+" rpool/home/admin") {
		t.Errorf("remote grant argv wrong:\n%s", m.log())
	}
	if got, _ := os.ReadFile(secrets); string(got) != "Passw0rd\n" {
		t.Errorf("sudo password must arrive on stdin, got %q", got)
	}
	if strings.Contains(m.log(), "Passw0rd") {
		t.Error("sudo password leaked into a command line")
	}
	audit, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".local/state/zxplore/audit.log"))
	if !strings.Contains(string(audit), "zfs allow -u admin") {
		t.Error("grant not audited")
	}
	if strings.Contains(string(audit), "Passw0rd") {
		t.Error("sudo password leaked into the audit log")
	}
}

// ── one-shot server setup ───────────────────────────────────────────────────

func TestMockSetupServer(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	stdMock(t) // mock ssh succeeds → key counts as already authorized
	s, err := SetupServer(Server{Name: "unit box", Host: "mock", User: "zexp"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyPath == "" {
		t.Fatal("SetupServer must generate and record a key")
	}
	if _, err := os.Stat(s.KeyPath); err != nil {
		t.Errorf("generated key missing on disk: %v", err)
	}
	if pub, err := PublicKey(s.KeyPath); err != nil || !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("public key not derivable: %q %v", pub, err)
	}
}

func TestMockSetupServerNeedsPassword(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	m := newMock(t)
	m.script("ssh", `echo "u@mock: Permission denied (publickey,password)." >&2; exit 255`)
	_, err := SetupServer(Server{Name: "unit box2", Host: "mock", User: "u"}, "")
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("unauthorized key with no password must ask for one, got %v", err)
	}
}

// ── remote: quoting round-trips through a simulated remote shell ────────────

func TestMockRemoteRoundTrip(t *testing.T) {
	m := stdMock(t)
	h := Host{SSH: "zexp@mockbox"}
	rows, err := ListDatasets(h)
	if err != nil || len(rows) != 2 {
		t.Fatalf("remote list failed: %v %v", rows, err)
	}
	logText := m.log()
	if !strings.Contains(logText, "StrictHostKeyChecking=accept-new") {
		t.Error("remote leg missing accept-new")
	}
	if !strings.Contains(logText, "'zfs' 'list'") {
		t.Error("remote command words not quoted")
	}

	// A spacey path through remote sh -c — shredded to nothing before the
	// quoting fix, exact listing after it.
	spacey := filepath.Join(t.TempDir(), "my data")
	if err := os.MkdirAll(spacey, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spacey, "file one"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ListDir(h, spacey)
	if err != nil || len(entries) != 1 || entries[0].Name != "file one" {
		t.Fatalf("remote spacey ListDir: entries=%v err=%v", entries, err)
	}
}

// ── replication pipeline: the exact command an operator could paste ─────────

func TestMockReplicatePipeline(t *testing.T) {
	stdMock(t)
	// Encrypted source with a common snapshot on the target: raw incremental.
	p := ReplicatePipeline(LocalHost(), "tank/data@b", LocalHost(), "backup/data")
	want := "zfs send -v -w -i 'tank/data@a' 'tank/data@b' | zfs recv -s -F -o readonly=on -o canmount=noauto 'backup/data'"
	if p != want {
		t.Errorf("pipeline =\n  %s\nwant\n  %s", p, want)
	}
	// A resume token on the target wins over everything else.
	p = ReplicatePipeline(LocalHost(), "tank/data@b", LocalHost(), "backup/rez")
	if !strings.HasPrefix(p, "zfs send -v -t '1-mocktoken-abc'") {
		t.Errorf("resume pipeline wrong: %s", p)
	}
	// A remote destination wraps the recv leg in quoted ssh with accept-new.
	p = ReplicatePipeline(LocalHost(), "tank/data@b", Host{SSH: "zexp@offsite"}, "backup/data")
	if !strings.Contains(p, "| ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new") ||
		!strings.Contains(p, "'zexp@offsite'") {
		t.Errorf("remote recv leg wrong: %s", p)
	}
}
