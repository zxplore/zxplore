// engine_unit_test.go — pure-function tests for the engine and stores: no ZFS,
// no exec, no network. Everything here proves parsing, quoting, and command
// CONSTRUCTION (argv inspected, never run). The mock-CLI integration tests
// live in engine_mockcli_test.go; the feature→test audit is docs/TESTING.md.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// ── quoting & remote command construction ───────────────────────────────────

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":        "'plain'",
		"has space":    "'has space'",
		"a'b":          `'a'\''b'`,
		"$(rm -rf /);": `'$(rm -rf /);'`,
		"":             "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLocalCommandArgv(t *testing.T) {
	cmd := LocalHost().command("zfs", "list", "-H")
	if want := []string{"zfs", "list", "-H"}; strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Errorf("local argv = %v, want %v", cmd.Args, want)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "LC_ALL=C" {
			found = true
		}
	}
	if !found {
		t.Error("local command missing LC_ALL=C (needsElevation would be locale-fragile)")
	}
}

func TestRemoteCommandQuoted(t *testing.T) {
	h := Host{SSH: "zexp@box"}
	cmd := h.command("zfs", "destroy", "tank/my data@old snap")
	if cmd.Args[0] != "ssh" {
		t.Fatalf("remote command must run through ssh, got %v", cmd.Args[0])
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "StrictHostKeyChecking=accept-new") {
		t.Error("remote ssh missing accept-new host-key policy")
	}
	if strings.Contains(joined, "StrictHostKeyChecking=no") {
		t.Error("remote ssh still trusts any host key (=no)")
	}
	// The remote command must be ONE pre-quoted string so the remote shell
	// cannot split the spacey dataset name into words.
	last := cmd.Args[len(cmd.Args)-1]
	if want := "'zfs' 'destroy' 'tank/my data@old snap'"; last != want {
		t.Errorf("remote command string = %s, want %s", last, want)
	}
}

func TestRemoteShCScriptSurvives(t *testing.T) {
	// sh -c scripts used to shred into words over ssh; quoted they survive.
	h := Host{SSH: "zexp@box"}
	script := "LC_ALL=C ls -lAn -- '/pool/my data'"
	cmd := h.command("sh", "-c", script)
	last := cmd.Args[len(cmd.Args)-1]
	if want := "'sh' '-c' 'LC_ALL=C ls -lAn -- '\\''/pool/my data'\\'''"; last != want {
		t.Errorf("remote sh -c = %s, want %s", last, want)
	}
}

func TestSSHOptsAndPrefix(t *testing.T) {
	h := Host{SSH: "root@nyc", Port: 2222, KeyPath: "/k/id", Jump: "bastion"}
	got := strings.Join(h.sshOpts(), " ")
	for _, want := range []string{"-p 2222", "-i /k/id", "IdentitiesOnly=yes", "ProxyJump=bastion"} {
		if !strings.Contains(got, want) {
			t.Errorf("sshOpts missing %q in %q", want, got)
		}
	}
	pfx := sshPrefix(h)
	if !strings.Contains(pfx, "accept-new") || strings.Contains(pfx, "StrictHostKeyChecking=no") {
		t.Errorf("sshPrefix host-key policy wrong: %s", pfx)
	}
	if !strings.Contains(pfx, "'root@nyc'") {
		t.Errorf("sshPrefix target unquoted: %s", pfx)
	}
	// IdentitiesOnly must be present even with NO key configured — a loaded
	// ssh-agent otherwise spends MaxAuthTries on unrelated keys and every
	// connection dies with "Too many authentication failures".
	if got := strings.Join((Host{SSH: "u@h"}).sshOpts(), " "); !strings.Contains(got, "IdentitiesOnly=yes") {
		t.Errorf("keyless host missing IdentitiesOnly: %q", got)
	}
}

func TestFriendlySSH(t *testing.T) {
	s := Server{Name: "nyc", Host: "h", User: "zexp"}
	if friendlySSH(nil, s) != nil {
		t.Error("nil must pass through")
	}
	err := friendlySSH(errString("Received disconnect: Too many authentication failures"), s)
	if err == nil || !strings.Contains(err.Error(), "Authorize on server") {
		t.Errorf("agent-spray error not actionable: %v", err)
	}
	err = friendlySSH(errString("zexp@h: Permission denied (publickey,password)"), s)
	if err == nil || !strings.Contains(err.Error(), "used once") {
		t.Errorf("permission-denied error not actionable: %v", err)
	}
	plain := errString("cannot open 'tank': dataset does not exist")
	if friendlySSH(plain, s) != plain {
		t.Error("non-auth errors must pass through untouched")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestHostLabel(t *testing.T) {
	if LocalHost().Label() != "local" {
		t.Error("LocalHost label")
	}
	if (Host{SSH: "u@h"}).Label() != "u@h" {
		t.Error("remote label")
	}
}

// ── elevation classification ────────────────────────────────────────────────

func TestNeedsElevation(t *testing.T) {
	yes := []string{
		"cannot destroy 'tank/x': permission denied",
		"Insufficient privileges to snapshot",
		"internal error: must be root",
		"cannot mount: Not privileged",
	}
	no := []string{
		"cannot open 'tank/x': dataset does not exist",
		"cannot create 'tank/x': out of space",
		"",
	}
	for _, s := range yes {
		if !needsElevation(s) {
			t.Errorf("needsElevation(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if needsElevation(s) {
			t.Errorf("needsElevation(%q) = true, want false", s)
		}
	}
}

// ── name & size helpers ─────────────────────────────────────────────────────

func TestSnapShortAndRelJoin(t *testing.T) {
	if snapShort("tank/data@daily") != "daily" || snapShort("plain") != "plain" {
		t.Error("snapShort")
	}
	if relJoin("", "etc") != "etc" || relJoin("etc", "pf.conf") != "etc/pf.conf" {
		t.Error("relJoin")
	}
}

func TestHuman(t *testing.T) {
	cases := map[string]string{
		"512":        "512B",
		"1024":       "1.0K",
		"5259264":    "5.0M",
		"5368709120": "5.0G",
		"not-a-num":  "not-a-num", // non -p output passes through untouched
	}
	for in, want := range cases {
		if got := human(in); got != want {
			t.Errorf("human(%q) = %q, want %q", in, got, want)
		}
	}
	if humanBytes(2048) != "2.0K" {
		t.Error("humanBytes")
	}
}

// ── ls parsing (snapshot file explorer) ─────────────────────────────────────

func TestParseLsLine(t *testing.T) {
	e, ok := parseLsLine("-rw-r--r--  1 0 0 4096 Jul 20 09:00 pf.conf")
	if !ok || e.Name != "pf.conf" || e.Dir || e.Size != 4096 || e.MTime != "Jul 20 09:00" {
		t.Errorf("file row parsed wrong: %+v ok=%v", e, ok)
	}
	e, ok = parseLsLine("drwxr-xr-x  3 0 0 4096 Jul 20 09:00 name with spaces")
	if !ok || !e.Dir || e.Name != "name with spaces" {
		t.Errorf("dir-with-spaces parsed wrong: %+v", e)
	}
	e, ok = parseLsLine("lrwxrwxrwx  1 0 0 9 Jul 20 09:00 link -> /target x")
	if !ok || e.Name != "link" {
		t.Errorf("symlink target not stripped: %+v", e)
	}
	e, ok = parseLsLine("brw-rw----  1 0 6 259, 1 Jul 20 09:00 nvme0n1")
	if !ok || e.Name != "nvme0n1" || e.Size != 0 {
		t.Errorf("device node parsed wrong: %+v", e)
	}
	if _, ok := parseLsLine("total 12"); ok {
		t.Error("ls header line must not parse")
	}
	if _, ok := parseLsLine("drwx------ 2 0 0 4096 Jul 20 09:00 ."); ok {
		t.Error("dot entry must not parse")
	}
}

// ── zpool status analysis ───────────────────────────────────────────────────

const statusClean = `  pool: tank
 state: ONLINE
config:

	NAME        STATE     READ WRITE CKSUM
	tank        ONLINE       0     0     0
	  sda       ONLINE       0     0     0

errors: No known data errors
`

func TestVdevErrors(t *testing.T) {
	if vdevErrors(statusClean) {
		t.Error("clean status flagged as vdev errors")
	}
	dirty := strings.Replace(statusClean, "sda       ONLINE       0     0     0",
		"sda       ONLINE       3     0     0", 1)
	if !vdevErrors(dirty) {
		t.Error("nonzero READ count not flagged")
	}
}

// ── restore & diff plans ────────────────────────────────────────────────────

func TestRestoreArgv(t *testing.T) {
	argv, dst := RestoreArgv("/tank/data", "daily", "etc/pf.conf", false, true)
	if strings.Join(argv, " ") != "cp -a /tank/data/.zfs/snapshot/daily/etc/pf.conf /tank/data/etc/pf.conf" || dst != "/tank/data/etc/pf.conf" {
		t.Errorf("file overwrite plan wrong: %v → %s", argv, dst)
	}
	argv, dst = RestoreArgv("/tank/data", "daily", "etc/pf.conf", false, false)
	if dst != "/tank/data/etc/pf.conf.from-daily" || argv[len(argv)-1] != dst {
		t.Errorf("alongside plan wrong: %v → %s", argv, dst)
	}
	argv, _ = RestoreArgv("/tank/data", "daily", "etc", true, true)
	if argv[1] != "-a" || !strings.HasSuffix(argv[2], "/etc/.") {
		t.Errorf("dir overwrite must merge with src/. : %v", argv)
	}
}

func TestDiffCommand(t *testing.T) {
	if DiffCommand("t@a", "") != "zfs diff -H t@a" || DiffCommand("t@a", "t@b") != "zfs diff -H t@a t@b" {
		t.Error("DiffCommand")
	}
}

// ── replication pipeline (construction only; data flow proven in mock tests) ─

func TestRunReplicateQuoting(t *testing.T) {
	// The pipeline string is built exclusively from shellQuote'd parts —
	// prove a hostile snapshot name stays inert inside it.
	q := shellQuote("tank/x@$(reboot)")
	if q != `'tank/x@$(reboot)'` {
		t.Errorf("hostile snapshot name not neutralized: %s", q)
	}
}

// ── welcome / diagnosis text ────────────────────────────────────────────────

func TestWelcomeText(t *testing.T) {
	if !strings.Contains(WelcomeText(HostNoZFS), "OPENZFS NOT FOUND") {
		t.Error("HostNoZFS text")
	}
	if !strings.Contains(WelcomeText(HostNoPools), "NO POOLS IMPORTED") {
		t.Error("HostNoPools text")
	}
	if WelcomeText(HostOK) != "" {
		t.Error("HostOK must render nothing")
	}
}

// ── version stamp ───────────────────────────────────────────────────────────

func TestVersionFull(t *testing.T) {
	old := buildNum
	defer func() { buildNum = old }()
	buildNum = ""
	if versionFull() != version {
		t.Error("bare build must show plain version")
	}
	buildNum = "42"
	if versionFull() != version+" b42" {
		t.Error("stamped build must show version b42")
	}
}

// ── favorites store ─────────────────────────────────────────────────────────

func TestParseTarget(t *testing.T) {
	f := ParseTarget("zexp@nyc:tank/backups")
	if f.SSH != "zexp@nyc" || f.Path != "tank/backups" {
		t.Errorf("remote target parsed wrong: %+v", f)
	}
	f = ParseTarget("tank/data")
	if f.SSH != "" || f.Path != "tank/data" {
		t.Errorf("local target parsed wrong: %+v", f)
	}
	// a ':' AFTER a '/' is part of the path, not a host separator
	f = ParseTarget("tank/od:d")
	if f.SSH != "" || f.Path != "tank/od:d" {
		t.Errorf("colon-after-slash parsed wrong: %+v", f)
	}
	if (Favorite{SSH: "u@h", Path: "t/d"}).Target() != "u@h:t/d" {
		t.Error("Target round-trip")
	}
}

func TestAddFavoriteDedupAndCap(t *testing.T) {
	favs := []Favorite{{Name: "a"}, {Name: "b"}}
	favs = AddFavorite(favs, Favorite{Name: "b"})
	if len(favs) != 2 || favs[0].Name != "b" {
		t.Errorf("dedup/most-recent-first wrong: %+v", favs)
	}
	for i := 0; i < 60; i++ {
		favs = AddFavorite(favs, Favorite{Name: strings.Repeat("x", i+1)})
	}
	if len(favs) != 50 {
		t.Errorf("cap = %d, want 50", len(favs))
	}
}

func TestFavoritesPersistence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := []Favorite{{Name: "nyc", SSH: "zexp@nyc", Path: "tank"}}
	if err := SaveFavorites(want); err != nil {
		t.Fatal(err)
	}
	got := LoadFavorites()
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

// ── server store ────────────────────────────────────────────────────────────

func TestServerStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	list := UpsertServer(nil, Server{Name: "nyc", Host: "10.0.0.1", User: "zexp"})
	list = UpsertServer(list, Server{Name: "ams", Host: "10.0.0.2"})
	list = UpsertServer(list, Server{Name: "nyc", Host: "10.9.9.9", User: "zexp"}) // replace
	if len(list) != 2 || list[0].Host != "10.9.9.9" {
		t.Errorf("upsert wrong: %+v", list)
	}
	if err := SaveServers(list); err != nil {
		t.Fatal(err)
	}
	if got := LoadServers(); len(got) != 2 || got[0].Name != "nyc" {
		t.Errorf("persist wrong: %+v", got)
	}
	if got := DeleteServer(list, "nyc"); len(got) != 1 || got[0].Name != "ams" {
		t.Errorf("delete wrong: %+v", got)
	}
	s := Server{Name: "n", Host: "h", User: "u", Port: 2222, KeyPath: "/k", Jump: "j"}
	if s.sshTarget() != "u@h" {
		t.Error("sshTarget")
	}
	if h := s.toHost(); h.SSH != "u@h" || h.Port != 2222 || h.KeyPath != "/k" || h.Jump != "j" {
		t.Errorf("toHost wrong: %+v", h)
	}
	if p := keyPathFor("evil/../ name"); strings.Contains(filepath.Base(p), "/") || strings.Contains(filepath.Base(p), " ") {
		t.Errorf("keyPathFor not sanitized: %s", p)
	}
}

// ── host-key pinning (accept-new) ───────────────────────────────────────────

func testSSHKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestHostKeyAcceptNew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addr := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 22}
	k1, k2 := testSSHKey(t), testSSHKey(t)

	cb := hostKeyAcceptNew()
	if err := cb("box:22", addr, k1); err != nil {
		t.Fatalf("first contact must record, got %v", err)
	}
	kh, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"))
	if err != nil || !strings.Contains(string(kh), "box") {
		t.Fatalf("host key not recorded: %v %q", err, kh)
	}
	if err := cb("box:22", addr, k1); err != nil {
		t.Errorf("same key must verify, got %v", err)
	}
	if err := cb("box:22", addr, k2); err == nil {
		t.Error("CHANGED host key must be refused (MITM)")
	} else if !strings.Contains(err.Error(), "CHANGED") {
		t.Errorf("changed-key error unclear: %v", err)
	}
}
