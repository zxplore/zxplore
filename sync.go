// sync.go — scheduled replication: the "Auto Sync" engine.
//
// THE MODEL. A backup host PULLS. Each SyncJob says "fetch this dataset from
// that server into here, on this cadence". The job runs on the machine holding
// the backups, so a compromised source holds no credentials on the archive and
// cannot reach it. Push is deliberately not offered.
//
// WHAT SCHEDULES IT. Not zxplore. zxplore writes a unit and enables it; the
// system's own scheduler runs it at 3am with nobody logged in, which is the
// one property a backup needs and a GUI process can never provide. On Linux
// that is a systemd timer. Elsewhere (FreeBSD, illumos) schedulerCron is
// reported and the rendered crontab line is handed to the operator rather
// than pretending a unit was installed — zxplore runs anywhere OpenZFS does
// and must not quietly become Linux-only.
//
// WHY THESE syncoid FLAGS (every one of them cost something on 2026-09-08):
//
//	--no-sync-snap    replicate sanoid's own autosnaps, so the archive follows
//	                  the source's retention instead of minting parallel snaps.
//	--create-bookmark leave a bookmark behind. The incremental chain needs a
//	                  snapshot common to both ends; if the source's retention
//	                  prunes everything the archive holds, the next run re-seeds
//	                  the entire pool. A bookmark survives pruning and still
//	                  works as an incremental origin.
//	-o readonly=on    nothing may write to the archive. A diverged target
//	                  refuses the next increment until a -F rollback discards.
//	NO -o canmount    invalid for zvols ("does not apply to datasets of this
//	                  type"), and unnecessary: syncoid sends without -p, so
//	                  mountpoints never travel and children inherit from the
//	                  backup container.
//	timeout           a receive-side error kills the pipe while the sender's pv
//	                  waits on stdin, so a bad flag is an INDEFINITE HANG, not
//	                  a failure. Unbounded, one wedged run blocks every later
//	                  run forever.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SyncJob is one scheduled pull. Name is the identity: it becomes the unit
// name, so it must survive being put in a filename.
type SyncJob struct {
	Name string `json:"name"`
	// The connection is carried IN the job, not referenced by a registry name.
	// The GUI runs as you and reads ~/.config; the timer runs as root and
	// reads /root/.config. A job that says server:"fiend" resolves in one and
	// not the other, producing a timer that is enabled and fails every night
	// with "unknown server". Self-contained jobs cannot do that.
	Server     string `json:"server,omitempty"` // label only, for display
	Host       string `json:"host"`
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	KeyPath    string `json:"keyPath,omitempty"`
	Source     string `json:"source"` // dataset on the source host
	Target     string `json:"target"` // dataset here
	Schedule   string `json:"schedule"`
	Recursive  bool   `json:"recursive"`
	Exclude    string `json:"exclude,omitempty"`
	MaxRuntime string `json:"maxRuntime,omitempty"` // default 4h
}

// server is the connection this job pulls from.
//
// A job written before jobs carried their own connection has only Server, a
// name. Rather than fail, resolve that name against the inventories — clients
// first, since a backup job's source is a client. Without this, upgrading
// silently broke an installed timer: the job loaded, the tab drew it as
// orphaned, and the 03:00 run died with "has no host" where nobody was
// watching (2026-09-08).
func (j SyncJob) server() Server {
	if j.Host == "" && j.Server != "" {
		for _, s := range LoadClients() {
			if s.Name == j.Server {
				return s
			}
		}
		for _, s := range LoadServers() {
			if s.Name == j.Server {
				return s
			}
		}
	}
	name := j.Server
	if name == "" {
		name = j.Host
	}
	return Server{Name: name, Host: j.Host, User: j.User, Port: j.Port, KeyPath: j.KeyPath}
}

// label is what the UI calls the source.
func (j SyncJob) label() string {
	if j.Server != "" {
		return j.Server
	}
	return j.Host
}

func (j SyncJob) runtimeCap() string {
	if strings.TrimSpace(j.MaxRuntime) == "" {
		return "4h"
	}
	return j.MaxRuntime
}

// unitName is the systemd unit stem. Job names come from an operator, so they
// are sanitised rather than trusted: a name reaching a filesystem path gets
// checked before use, not after.
func (j SyncJob) unitName() string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, j.Name)
	return "zxplore-sync-" + strings.Trim(safe, "-")
}

// ── persistence — same shape as servers.json, next to it ────────────────────

// syncJobsPath is SYSTEM-wide, not per-user, and that is deliberate.
//
// A scheduled job is system state: installing its timer needs root, and the
// timer runs as root. Keeping the job list under the configuring user's
// ~/.config would mean the timer cannot read the job it is supposed to run —
// a unit that exists, is enabled, and does nothing. That failure is silent by
// construction, which is the exact shape of defect this whole feature is
// meant to prevent.
//
// Servers stay in the per-user file: browsing is not scheduling. Mode 0644,
// not 0600 — this holds hosts, usernames, dataset names and the PATH of a key,
// never a key itself, and a root-only file made the Auto Sync tab show "no
// jobs" while a timer was enabled and running (onyx, 2026-09-08).
func syncJobsPath() string { return "/etc/zxplore/syncjobs.json" }

func LoadSyncJobs() []SyncJob {
	data, err := os.ReadFile(syncJobsPath())
	if err != nil {
		return nil
	}
	var jobs []SyncJob
	if json.Unmarshal(data, &jobs) != nil {
		return nil
	}
	return jobs
}

// SaveSyncJobs writes the system job list. Needs root; the GUI elevates for
// this the same way it does for any other admin action.
func SaveSyncJobs(jobs []SyncJob) error {
	if err := os.MkdirAll(filepath.Dir(syncJobsPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(syncJobsPath(), data, 0o644)
}

func UpsertSyncJob(list []SyncJob, j SyncJob) []SyncJob {
	for i := range list {
		if list[i].Name == j.Name {
			list[i] = j
			return list
		}
	}
	return append(list, j)
}

func DeleteSyncJob(list []SyncJob, name string) []SyncJob {
	out := list[:0]
	for _, j := range list {
		if j.Name != name {
			out = append(out, j)
		}
	}
	return out
}

// ── the command ──────────────────────────────────────────────────────────────

// syncoidPath finds syncoid. Distros disagree: the sanoid package puts it in
// /usr/sbin on Debian, the GitHub install lands in /usr/local/sbin, and onyx
// carries it in /usr/local/bin. A probe path is part of the measurement — get
// it wrong and you have invented a missing dependency (kldload, 2026-08-22).
func syncoidPath() string {
	for _, p := range []string{
		"/usr/local/bin/syncoid", "/usr/local/sbin/syncoid",
		"/usr/sbin/syncoid", "/usr/bin/syncoid",
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("syncoid"); err == nil {
		return p
	}
	return ""
}

// SyncCommand renders the exact argv a job runs. Exported because the UI shows
// it before anything is installed: an operator should be able to read the
// command they are about to schedule.
func SyncCommand(j SyncJob, s Server) []string {
	argv := []string{"timeout", j.runtimeCap(), syncoidPath(),
		"--no-sync-snap", "--create-bookmark"}
	if j.Recursive {
		argv = append(argv, "--recursive", "--skip-parent")
	}
	if s.KeyPath != "" {
		argv = append(argv, "--sshkey", s.KeyPath)
	}
	if strings.TrimSpace(j.Exclude) != "" {
		argv = append(argv, "--exclude="+j.Exclude)
	}
	// Raw or not is the SAME decision the interactive path makes, and it has
	// exactly one illegal cell: a positively-unencrypted source into a target
	// that inherits encryption is refused for its embedded-data feature.
	if rawForJob(s.toHost(), j.Source, LocalHost(), j.Target) {
		argv = append(argv, "--sendoptions=w")
	}
	argv = append(argv, "--recvoptions=u o readonly=on",
		s.sshTarget()+":"+j.Source, j.Target)
	return argv
}

// rawForJob mirrors the interactive rule in replicatePipeline. Kept as one
// sentence in one place so the scheduled path can never drift from the
// interactive one.
func rawForJob(srcHost Host, srcDs string, dstHost Host, dstPath string) bool {
	return !(singleProp(srcHost, srcDs, "encryption") == "off" &&
		isEncrypted(inheritedEncryption(dstHost, dstPath)))
}

// MixedEncryption reports datasets under root whose encryption differs from
// root's. syncoid takes ONE global send mode per run, so a mixed subtree
// cannot be replicated correctly by a single job — the caller must warn and
// split. Found the hard way: fiend's rpool is unencrypted except
// rpool/kldload/secrets (2026-09-08).
func MixedEncryption(h Host, root string) []string {
	rootEnc := isEncrypted(singleProp(h, root, "encryption"))
	// -t filesystem,volume: `zfs get -r` includes SNAPSHOTS, which inherit
	// their dataset's encryption and are not separately replicable. Without
	// this the warning reported 5 "datasets" where there was 1 (2026-09-08).
	out, err := run(h.command("zfs", "get", "-H", "-o", "name,value",
		"-t", "filesystem,volume", "-r", "encryption", root))
	if err != nil {
		return nil
	}
	var odd []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[0] == root {
			continue
		}
		if isEncrypted(f[1]) != rootEnc {
			odd = append(odd, f[0])
		}
	}
	return odd
}

// ── the scheduler ────────────────────────────────────────────────────────────

type schedulerKind int

const (
	schedulerNone schedulerKind = iota
	schedulerSystemd
	schedulerCron
)

// detectScheduler asks what actually runs jobs on THIS host, rather than
// assuming Linux. A wrong answer here silently produces a job that never runs.
func detectScheduler() schedulerKind {
	if runtime.GOOS == "linux" {
		if fi, err := os.Stat("/run/systemd/system"); err == nil && fi.IsDir() {
			return schedulerSystemd
		}
	}
	if _, err := exec.LookPath("crontab"); err == nil {
		return schedulerCron
	}
	return schedulerNone
}

// CronLine renders the crontab entry for hosts without systemd. Returned for
// the operator to install; zxplore does not edit crontabs behind their back.
func CronLine(j SyncJob, s Server) string {
	return fmt.Sprintf("%s root %s", j.Schedule, strings.Join(SyncCommand(j, s), " "))
}

func unitDir() string { return "/etc/systemd/system" }

func renderService(j SyncJob, s Server) string {
	return fmt.Sprintf(`# %s.service — zxplore Auto Sync: pull %s from %s.
#
# Written by zxplore. Edit the job in the Auto Sync tab, not this file: the
# next install overwrites it.
#
# Type=oneshot because this is a task that finishes, not a daemon; the timer
# owns the schedule. ExecStart calls zxplore back rather than a frozen syncoid
# command line: whether the send must be raw depends on probing BOTH ends, and
# a probe run at install time can fail and bake the wrong answer into this file
# permanently. Deciding at run time also lets the wrapper verify the OUTCOME
# instead of trusting syncoid's exit code, which lied on 2026-09-08.
#
# The timeout lives inside that wrapper, not here: a wedged zfs receive hangs
# forever instead of failing, and an unbounded hang blocks every later run.
[Unit]
Description=zxplore Auto Sync — %s
After=network-online.target zfs.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s --sync-run %s
# The pull needs root to zfs receive into the archive.
User=root

[Install]
WantedBy=multi-user.target
`, j.unitName(), j.Source, s.Name, j.Name, zxploreExe(), j.Name)
}

func renderTimer(j SyncJob) string {
	return fmt.Sprintf(`# %s.timer — when the pull runs.
#
# Persistent=true so a run missed while this host was off happens at the next
# boot rather than being skipped silently. A backup that quietly skips is the
# failure mode this whole feature exists to prevent.
[Unit]
Description=zxplore Auto Sync schedule — %s

[Timer]
OnCalendar=%s
Persistent=true
Unit=%s.service

[Install]
WantedBy=timers.target
`, j.unitName(), j.Name, j.Schedule, j.unitName())
}

// zxploreExe is the absolute path of this binary, for a unit's ExecStart. A
// unit with a relative path silently never runs.
func zxploreExe() string {
	if p, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return "/usr/local/bin/zxplore"
}

// syncRunCLI is what the timer actually executes: `zxplore --sync-run <name>`.
//
// It exists so the raw/plain decision is made HERE, against the live hosts,
// and so success is judged by what landed rather than by an exit code.
//
// Exit: 0 replicated and verified · 1 replication or verification failed
//
//	2 misconfiguration (unknown job/server, no syncoid) · 124 timed out.
func syncRunCLI(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: zxplore --sync-run <job-name>")
		return 2
	}
	name := args[0]
	for _, j := range LoadSyncJobs() {
		if j.Name == name {
			out, rc := runSyncJob(j)
			fmt.Fprint(os.Stderr, out)
			return rc
		}
	}
	fmt.Fprintf(os.Stderr, "no such sync job: %s\n", name)
	return 2
}

// runSyncJob does the whole pull and judges it by what landed.
//
// Returns the operator-readable transcript and an exit code:
//
//	0 replicated and verified · 1 replication or verification failed
//	2 misconfiguration · 124 timed out (a wedged receive, not slowness).
//
// syncStatePath is where a run records its own outcome. systemd only knows
// about runs IT started, so after a successful `Run now` the tab kept showing
// the 03:00 failure — a working backup reported as broken, which erodes trust
// in the indicator exactly as much as the reverse (2026-09-09). Every run
// writes here, so the record is of the JOB, not of one way of starting it.
// Mode 0644: it holds a timestamp and an exit code, and the GUI reads it
// without elevation.
func syncStatePath(j SyncJob) string {
	return filepath.Join("/var/lib/zxplore", j.unitName()+".state")
}

type syncRunRecord struct {
	When string `json:"when"`
	RC   int    `json:"rc"`
	Note string `json:"note,omitempty"`
}

func recordSyncRun(j SyncJob, rc int, note string) {
	rec := syncRunRecord{When: time.Now().Format(time.RFC1123), RC: rc, Note: note}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(syncStatePath(j)), 0o755) == nil {
		// Best effort: a run that worked must not be reported as failed
		// because its bookkeeping could not be written.
		_ = os.WriteFile(syncStatePath(j), data, 0o644)
	}
}

func lastSyncRun(j SyncJob) (syncRunRecord, bool) {
	data, err := os.ReadFile(syncStatePath(j))
	if err != nil {
		return syncRunRecord{}, false
	}
	var rec syncRunRecord
	if json.Unmarshal(data, &rec) != nil {
		return syncRunRecord{}, false
	}
	return rec, true
}

func runSyncJob(j SyncJob) (string, int) {
	var b strings.Builder
	say := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	// Resolve FIRST: a legacy job carries only a server name, and testing the
	// raw field here is what made an installed timer fail at 03:00.
	srv := j.server()
	if strings.TrimSpace(srv.Host) == "" {
		say("job %s has no host, and %q is in neither inventory\n", j.Name, j.Server)
		recordSyncRun(j, 2, "no host")
		return b.String(), 2
	}
	if syncoidPath() == "" {
		say("syncoid not found on PATH or the usual prefixes\n")
		return b.String(), 2
	}

	// What must be here when this finishes, recorded BEFORE, so the check
	// afterwards is against a specific thing rather than a vague "it worked".
	// Reachability FIRST, so an unreachable host does not get reported as a
	// host with no snapshots. Those need opposite responses — power the box on
	// versus configure sanoid — and conflating them sends the operator to the
	// wrong place. fiend was simply switched off when this was found
	// (2026-09-08).
	if out, err := run(srv.toHost().command("true")); err != nil {
		say("cannot reach %s: %s\n", srv.sshTarget(), strings.TrimSpace(firstLine(out+err.Error())))
		recordSyncRun(j, 2, "unreachable")
		return b.String(), 2
	}
	newest := newestSnapshot(srv.toHost(), j.Source)
	if newest == "" {
		say("%s is reachable but has no snapshots under %s — is sanoid running there, "+
			"and does its config cover that dataset?\n", srv.sshTarget(), j.Source)
		return b.String(), 2
	}
	if odd := MixedEncryption(srv.toHost(), j.Source); len(odd) > 0 {
		// Loud, not fatal: the rest still replicates, and the operator needs
		// to know which datasets this job cannot carry.
		say("WARNING: %d dataset(s) under %s differ in encryption from its root; one syncoid run "+
			"has ONE send mode, so these need their own job: %s\n",
			len(odd), j.Source, strings.Join(odd, " "))
	}

	argv := SyncCommand(j, srv)
	say("running: %s\n", strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	raw, err := cmd.CombinedOutput()
	b.Write(raw)
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			rc = 1
		}
	}
	if rc == 124 {
		say("FATAL: exceeded %s and was killed — a wedged receive, not slowness.\n", j.runtimeCap())
		recordSyncRun(j, 124, "timed out")
		return b.String(), 124
	}
	if rc != 0 {
		say("WARNING: syncoid exited %d; verifying what landed anyway.\n", rc)
	}

	tail := newest
	if i := strings.IndexByte(tail, '@'); i >= 0 {
		tail = tail[i+1:]
	}
	if !snapshotPresent(LocalHost(), j.Target, tail) {
		say("FATAL: %s is NOT under %s. Do not trust this backup.\n", tail, j.Target)
		recordSyncRun(j, 1, "snapshot did not land")
		return b.String(), 1
	}
	// A count is not a result — but the absence of one is. Verifying a single
	// snapshot says nothing about the other 79 datasets: a run with syncoid
	// exiting 2 and four datasets skipped still found its snapshot
	// (2026-09-08).
	srcN := datasetCount(srv.toHost(), j.Source, j.Exclude)
	dstN := datasetCount(LocalHost(), j.Target, j.Exclude)
	if srcN > 0 && dstN < srcN {
		say("FATAL: %d of %d datasets present under %s — partial replica, %d missing.\n",
			dstN, srcN, j.Target, srcN-dstN)
		recordSyncRun(j, 1, "partial replica")
		return b.String(), 1
	}
	say("OK: %s present, %d/%d datasets under %s.\n", tail, dstN, srcN, j.Target)
	recordSyncRun(j, 0, fmt.Sprintf("%d/%d datasets", dstN, srcN))
	return b.String(), 0
}

// newestSnapshot returns the most recently created snapshot anywhere under
// dataset, or "" if there are none (or it cannot be read).
func newestSnapshot(h Host, dataset string) string {
	out, err := run(h.command("zfs", "list", "-H", "-t", "snapshot", "-o", "name",
		"-s", "creation", "-r", dataset))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// snapshotPresent reports whether any dataset under root carries @suffix.
func snapshotPresent(h Host, root, suffix string) bool {
	out, err := run(h.command("zfs", "list", "-H", "-t", "snapshot", "-o", "name", "-r", root))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), "@"+suffix) {
			return true
		}
	}
	return false
}

// InstallSyncJob writes the unit + timer, enables it, and VERIFIES it is
// enabled. Installing and enabling are ONE operation: a unit that exists on
// disk and is not enabled is the classic silent failure — the file is there,
// the package query says installed, nothing errors, and it surfaces weeks
// later as "why is this empty" (kldload found five such services in one
// codebase, 2026-08-22).
//
// Returns an error describing what did not happen, never a bare exit code.
func InstallSyncJob(j SyncJob, s Server) error {
	if detectScheduler() != schedulerSystemd {
		return fmt.Errorf("this host does not run systemd; install the crontab line yourself:\n%s",
			CronLine(j, s))
	}
	if syncoidPath() == "" {
		return fmt.Errorf("syncoid not found — install sanoid before scheduling a pull")
	}
	unit := j.unitName()
	svc := filepath.Join(unitDir(), unit+".service")
	tmr := filepath.Join(unitDir(), unit+".timer")
	if err := os.WriteFile(svc, []byte(renderService(j, s)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", svc, err)
	}
	if err := os.WriteFile(tmr, []byte(renderTimer(j)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmr, err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", unit+".timer").CombinedOutput(); err != nil {
		return fmt.Errorf("enable %s.timer: %v: %s", unit, err, strings.TrimSpace(string(out)))
	}
	// The outcome, not the exit code: `enable` returning 0 is not proof the
	// symlink is there.
	if out, err := exec.Command("systemctl", "is-enabled", unit+".timer").CombinedOutput(); err != nil ||
		!strings.HasPrefix(strings.TrimSpace(string(out)), "enabled") {
		return fmt.Errorf("%s.timer was written but is NOT enabled — it will not run", unit)
	}
	return nil
}

// RemoveSyncJob stops and deletes the units. Missing units are not an error:
// removing something already gone is the normal case when an operator retries.
func RemoveSyncJob(j SyncJob) error {
	unit := j.unitName()
	_ = exec.Command("systemctl", "disable", "--now", unit+".timer").Run()
	var firstErr error
	for _, p := range []string{
		filepath.Join(unitDir(), unit+".service"),
		filepath.Join(unitDir(), unit+".timer"),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	return firstErr
}

// SyncJobState is what the UI shows for a job: is it scheduled, when did it
// last run, and did that run actually work.
type SyncJobState struct {
	Enabled    bool
	NextRun    string
	LastRun    string
	LastOK     bool
	LastStatus string // systemd ExecMainStatus, "" if it has never run
	Result     string // systemd Result: "success", "exit-code", "timeout" …
	NewestHere string // newest snapshot under the target, "" if none
}

// Summary is the one line that says whether this job is working. A job whose
// last run FAILED must not read the same as one that succeeded: on 2026-09-09
// a run died at 03:00:04 with "no route to host" and the listing showed only
// "enabled", a next run and a snapshot from the night before. Everything
// looked fine. That is the failure this whole feature exists to prevent, so
// the outcome of the last run is now the first thing reported.
func (s SyncJobState) Summary() string {
	switch {
	case s.LastRun == "" || s.LastStatus == "":
		if s.Enabled {
			return "scheduled, has not run yet"
		}
		return "NOT SCHEDULED"
	case s.LastOK:
		return "last run OK, " + s.LastRun
	default:
		why := s.Result
		if why == "" {
			why = "exit " + s.LastStatus
		}
		return "LAST RUN FAILED (" + why + "), " + s.LastRun
	}
}

// SyncStatus reads the live state of one job.
func SyncStatus(j SyncJob) SyncJobState {
	st := SyncJobState{}
	unit := j.unitName()
	if out, err := exec.Command("systemctl", "is-enabled", unit+".timer").Output(); err == nil {
		st.Enabled = strings.HasPrefix(strings.TrimSpace(string(out)), "enabled")
	}
	if out, err := exec.Command("systemctl", "show", unit+".timer",
		"-p", "NextElapseUSecRealtime", "--value").Output(); err == nil {
		st.NextRun = strings.TrimSpace(string(out))
	}
	// One property per call: `systemctl show` with several -p and --value
	// returns them in ITS order, not the order asked for, which is how a
	// failed run first read as status 0 here.
	if out, err := exec.Command("systemctl", "show", unit+".service",
		"-p", "ExecMainStatus", "--value").Output(); err == nil {
		st.LastStatus = strings.TrimSpace(string(out))
		st.LastOK = st.LastStatus == "0"
	}
	if out, err := exec.Command("systemctl", "show", unit+".service",
		"-p", "Result", "--value").Output(); err == nil {
		st.Result = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("systemctl", "show", unit+".service",
		"-p", "ExecMainExitTimestamp", "--value").Output(); err == nil {
		st.LastRun = strings.TrimSpace(string(out))
	}
	// The job's own record beats systemd's, which only covers runs it started.
	if rec, ok := lastSyncRun(j); ok {
		st.LastRun = rec.When
		st.LastStatus = strconv.Itoa(rec.RC)
		st.LastOK = rec.RC == 0
		st.Result = rec.Note
		if rec.RC == 0 {
			st.Result = ""
		}
	}
	if snap := newestSnapshot(LocalHost(), j.Target); snap != "" {
		st.NewestHere = snap
	}
	return st
}

// syncCLI is the operator-facing side of Auto Sync: list what is scheduled,
// install or remove a job's timer, and see whether it is actually running.
//
// Exit: 0 ok · 1 the action failed · 2 usage / unknown job.
func syncCLI(args []string) int {
	jobs := LoadSyncJobs()
	find := func(name string) (SyncJob, Server, bool) {
		for _, j := range jobs {
			if j.Name == name {
				return j, j.server(), true
			}
		}
		fmt.Fprintf(os.Stderr, "no such sync job: %s\n", name)
		return SyncJob{}, Server{}, false
	}

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		if len(jobs) == 0 {
			fmt.Printf("no Auto Sync jobs. They live in %s\n", syncJobsPath())
			return 0
		}
		for _, j := range jobs {
			st := SyncStatus(j)
			mark := "not scheduled"
			if st.Enabled {
				mark = "enabled"
			}
			fmt.Printf("%-20s %s:%s -> %s\n", j.Name, j.label(), j.Source, j.Target)
			fmt.Printf("  %s\n", st.Summary())
			fmt.Printf("  %-18s %s   schedule %s\n", mark, j.unitName()+".timer", j.Schedule)
			if st.NextRun != "" && st.NextRun != "0" {
				fmt.Printf("  next               %s\n", st.NextRun)
			}
			if st.NewestHere != "" {
				fmt.Printf("  newest here        %s\n", st.NewestHere)
			}
		}
		return 0
	case "install":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --sync install <job-name>")
			return 2
		}
		j, s, ok := find(args[1])
		if !ok {
			return 2
		}
		if err := InstallSyncJob(j, s); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
			return 1
		}
		fmt.Printf("%s.timer installed and enabled — schedule %s\n", j.unitName(), j.Schedule)
		return 0
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --sync remove <job-name>")
			return 2
		}
		for _, j := range jobs {
			if j.Name == args[1] {
				if err := RemoveSyncJob(j); err != nil {
					fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
					return 1
				}
				fmt.Printf("%s removed\n", j.unitName())
				return 0
			}
		}
		fmt.Fprintf(os.Stderr, "no such sync job: %s\n", args[1])
		return 2
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --sync show <job-name>")
			return 2
		}
		j, s, ok := find(args[1])
		if !ok {
			return 2
		}
		fmt.Printf("command: %s\n", strings.Join(SyncCommand(j, s), " "))
		if odd := MixedEncryption(s.toHost(), j.Source); len(odd) > 0 {
			fmt.Printf("mixed encryption — these need their own job: %s\n", strings.Join(odd, " "))
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: zxplore --sync [list|show JOB|install JOB|remove JOB]")
		return 2
	}
}

// datasetCount counts datasets under root, skipping any whose name contains
// the exclude pattern, so source and target are compared on the same basis.
func datasetCount(h Host, root, exclude string) int {
	out, err := run(h.command("zfs", "list", "-H", "-o", "name", "-r", root))
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if exclude != "" && strings.Contains(l, exclude) {
			continue
		}
		n++
	}
	return n
}

// firstLine keeps a multi-line ssh complaint to the part that names the cause.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
