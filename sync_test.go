// sync_test.go — guards for the scheduled-replication engine.
//
// Every case here is a defect that actually happened on 2026-09-08, in the
// order it bit. None of them were visible to a linter and two of them
// presented as an indefinite hang rather than an error.
package main

import (
	"strings"
	"testing"
)

func testJob() (SyncJob, Server) {
	j := SyncJob{
		Name: "fiend nightly", Server: "fiend", Host: "10.100.10.146", User: "root",
		KeyPath: "/root/.ssh/id_ed25519", Source: "rpool", Target: "rpool/backup/fiend",
		Schedule: "*-*-* 03:00:00", Recursive: true, Exclude: "containers/storage",
	}
	return j, j.server()
}

// A job name is operator input that becomes a filesystem path.
func TestUnitNameIsPathSafe(t *testing.T) {
	for _, in := range []string{"fiend nightly", "../../etc/passwd", "a/b", "-lead", "ok_name-1"} {
		got := SyncJob{Name: in}.unitName()
		if strings.ContainsAny(got, " /.") {
			t.Errorf("unitName(%q) = %q — must not contain space, slash or dot", in, got)
		}
		if !strings.HasPrefix(got, "zxplore-sync-") {
			t.Errorf("unitName(%q) = %q — lost its prefix", in, got)
		}
	}
}

// mockZFS answers the encryption probes SyncCommand makes, so these tests do
// not reach for the network. Without it each one waited on a real SSH connect.
func mockZFS(t *testing.T) {
	m := newMock(t)
	m.script("zfs", `case "$*" in
"get -H -o value encryption rpool") echo off ;;
*) exit 0 ;;
esac`)
	// The SOURCE probe goes over ssh, so faking zfs alone still opened a real
	// connection and made each of these tests wait on a network timeout.
	m.script("ssh", sshFixture)
}

func TestSyncCommandFlags(t *testing.T) {
	mockZFS(t)
	j, s := testJob()
	got := strings.Join(SyncCommand(j, s), " ")

	// -o canmount is INVALID for zvols: "property 'canmount' does not apply to
	// datasets of this type". It skipped every VM volume, as a hang.
	if strings.Contains(got, "canmount") {
		t.Error("canmount must never be passed to zfs receive — invalid for zvols")
	}
	// Without a bound, a wedged receive blocks every later run forever.
	if !strings.HasPrefix(got, "timeout ") {
		t.Errorf("the run must be bounded by timeout(1); got %q", got)
	}
	// Without a bookmark, source-side pruning forces a full re-seed.
	for _, must := range []string{
		"--no-sync-snap", "--create-bookmark", "--recursive", "--skip-parent",
		"--exclude=containers/storage", "--recvoptions=u o readonly=on",
		"root@10.100.10.146:rpool", "rpool/backup/fiend",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("missing %q in: %s", must, got)
		}
	}
}

func TestNonRecursiveOmitsSkipParent(t *testing.T) {
	mockZFS(t)
	j, s := testJob()
	j.Recursive = false
	got := strings.Join(SyncCommand(j, s), " ")
	if strings.Contains(got, "--skip-parent") {
		t.Error("--skip-parent is meaningless without --recursive and must not appear")
	}
}

func TestRenderedUnitsCarryTheirGuards(t *testing.T) {
	j, s := testJob()
	svc, tmr := renderService(j, s), renderTimer(j)

	// The unit must call zxplore back, not a frozen syncoid line: the raw/plain
	// decision needs both ends probed and can only be made at RUN time.
	exec := ""
	for _, l := range strings.Split(svc, "\n") {
		if strings.HasPrefix(l, "ExecStart=") {
			exec = l
		}
	}
	if !strings.Contains(exec, "--sync-run") {
		t.Errorf("ExecStart must call `zxplore --sync-run`; got %q", exec)
	}
	// The command must be decided at run time, so it cannot be in the unit.
	// (Checked on the ExecStart line alone — the comments above it explain
	// exactly this and legitimately say "syncoid".)
	if strings.Contains(exec, "syncoid") {
		t.Errorf("the syncoid command must NOT be frozen into ExecStart; got %q", exec)
	}
	if !strings.Contains(svc, "Type=oneshot") {
		t.Error("a task that finishes is oneshot, not a daemon")
	}
	// A run missed while the host was off must happen, not be skipped silently.
	if !strings.Contains(tmr, "Persistent=true") {
		t.Error("timer must be Persistent=true or a missed run vanishes")
	}
	if !strings.Contains(tmr, "OnCalendar="+j.Schedule) {
		t.Error("timer lost its schedule")
	}
}

func TestDatasetCountHonoursExclude(t *testing.T) {
	m := newMock(t)
	m.script("zfs", `echo "zfs $*" >> "$ZX_CMDLOG"
case "$*" in
"list -H -o name -r tank")
  printf 'tank\ntank/a\ntank/var/lib/containers/storage/x\ntank/b\n' ;;
*) exit 0 ;;
esac`)
	if got := datasetCount(Host{}, "tank", ""); got != 4 {
		t.Errorf("no exclude: got %d, want 4", got)
	}
	if got := datasetCount(Host{}, "tank", "containers/storage"); got != 3 {
		t.Errorf("with exclude: got %d, want 3", got)
	}
}

// The source and target must be counted on the SAME basis, or the partial-
// replica check fires on a difference the exclude created.
func TestCountsCompareLikeForLike(t *testing.T) {
	m := newMock(t)
	m.script("zfs", `case "$*" in
"list -H -o name -r tank")   printf 'tank\ntank/keep\ntank/containers/storage/l\n' ;;
"list -H -o name -r backup") printf 'backup\nbackup/keep\n' ;;
*) exit 0 ;;
esac`)
	src := datasetCount(Host{}, "tank", "containers/storage")
	dst := datasetCount(Host{}, "backup", "containers/storage")
	if src != dst {
		t.Errorf("excluded datasets must not read as a partial replica: src=%d dst=%d", src, dst)
	}
}

func TestCronLineCarriesTheSameCommand(t *testing.T) {
	mockZFS(t)
	j, s := testJob()
	line := CronLine(j, s)
	if !strings.Contains(line, j.Schedule) || !strings.Contains(line, "--create-bookmark") {
		t.Errorf("the crontab fallback must carry the same guards: %s", line)
	}
}

// A failed run must not read like a healthy one. On 2026-09-09 a job died at
// 03:00:04 with "no route to host" and the listing showed only "enabled", a
// next run, and a snapshot from the night before — indistinguishable from
// working. SyncStatus computed the outcome and nothing displayed it.
func TestSummaryDistinguishesFailureFromSuccess(t *testing.T) {
	cases := []struct {
		name  string
		st    SyncJobState
		want  string
		avoid string
	}{
		{"failed run says so first",
			SyncJobState{Enabled: true, LastRun: "Wed 2026-09-09 03:00:04 PDT", LastStatus: "2", Result: "exit-code"},
			"LAST RUN FAILED", "OK"},
		{"timeout is named, not just an exit code",
			SyncJobState{Enabled: true, LastRun: "t", LastStatus: "124", Result: "timeout"},
			"timeout", ""},
		{"successful run",
			SyncJobState{Enabled: true, LastRun: "t", LastStatus: "0", LastOK: true},
			"last run OK", "FAILED"},
		{"never run is not the same as failed",
			SyncJobState{Enabled: true},
			"has not run yet", "FAILED"},
		{"not scheduled at all",
			SyncJobState{},
			"NOT SCHEDULED", ""},
	}
	for _, c := range cases {
		got := c.st.Summary()
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q does not contain %q", c.name, got, c.want)
		}
		if c.avoid != "" && strings.Contains(got, c.avoid) {
			t.Errorf("%s: %q must not contain %q", c.name, got, c.avoid)
		}
	}
}
