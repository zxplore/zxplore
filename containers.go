// =============================================================================
// containers.go — containers, and the datasets underneath them.
//
// WHAT IT DOES, IN ORDER:
//   1. Finds the container engine on this host — docker or podman.
//   2. Lists containers and images through it.
//   3. Reports the storage driver, and whether the layers are ZFS datasets.
//   4. Runs the four verbs worth a button: start, stop, restart, remove.
//
// WHY IT LIVES IN A ZFS CONSOLE:
//   On this substrate a container image is not an opaque blob — with the zfs
//   storage driver every LAYER is a real dataset, so pulls are clone-cheap
//   and layers inherit compression. That also means the dataset tree fills up
//   with hash-named datasets: on a fresh box, 20 of 58 datasets were image
//   layers with `legacy` mountpoints and nothing to identify them. Showing
//   containers by NAME, next to the storage they occupy, is the readable half
//   of the same information.
//
// WHY ONE CODE PATH FOR BOTH ENGINES:
//   podman implements Docker's CLI surface deliberately, and every command
//   used here — ps, images, start, stop, rm, info — takes the same arguments
//   and speaks the same --format json on both. Two engines with two
//   implementations would drift; one path with a detected binary cannot.
//
// Notes:
//   - Read-only until a button is pressed. Listing never mutates.
//   - Local host only for now. zxplore drives remote pools over ssh, but a
//     remote container engine is a different trust and socket story, and
//     pretending otherwise would show an empty list on a box that has plenty.
// =============================================================================

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Engine is the container runtime found on this host.
type Engine struct {
	Bin       string // "docker" | "podman"
	Name      string // what to call it in the UI
	Driver    string // storage driver: zfs, overlay, …
	GraphRoot string // where the layers live
	Found     bool
	Why       string // when Found is false
}

// OnZFS reports whether image layers are datasets on this host.
//
// This is the fact the pane exists to surface: with the zfs driver a pull is
// a clone, and every layer can be snapshotted and rolled back like anything
// else in the tree. With overlay it is ordinary files inside one dataset.
func (e Engine) OnZFS() bool { return strings.EqualFold(e.Driver, "zfs") }

// DetectEngine finds the runtime.
//
// ORDER: docker first, then podman. On a host carrying both — which Debian
// will, since docker.io and podman do not conflict — docker is the one whose
// containers a person came looking for; podman's are usually the substrate's
// own. A host with neither gets a sentence, not an empty table.
func DetectEngine(timeout time.Duration) Engine {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	for _, bin := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		e := Engine{Bin: bin, Name: bin, Found: true}
		// `info` also proves the daemon is reachable: docker's CLI exists
		// long before dockerd is running, and a list against a dead daemon
		// is an error message, not an empty estate.
		out, err := containerRun(timeout, bin, "info", "--format",
			"{{.Driver}}\t{{.DockerRootDir}}")
		if err != nil || !plausibleDriver(out) {
			// podman spells the same fields differently.
			out2, err2 := containerRun(timeout, bin, "info", "--format",
				"{{.Store.GraphDriverName}}\t{{.Store.GraphRoot}}")
			if err2 == nil && plausibleDriver(out2) {
				out, err = out2, nil
			} else if err == nil {
				// Neither format produced a driver, and neither COMMAND
				// failed. That is docker exiting 0 while printing its error,
				// which is how "permission denied" ended up rendered as the
				// storage driver (fiend, 2026-08-16).
				err = fmt.Errorf("%s", strings.TrimSpace(out))
			}
		}
		if err != nil {
			// Distinguish "the daemon is down" from "you are not allowed to
			// talk to it". They need opposite actions, and the second one is
			// nearly universal on a freshly-configured host: the docker group
			// is applied at LOGIN, so a desktop session that started before
			// the account was added has no access and every command fails —
			// while the same command over a fresh ssh works, which makes it
			// look like the app is broken rather than the session stale
			// (fiend, 2026-08-16).
			why := fmt.Sprintf("%s is installed but not answering — is the service "+
				"running? (systemctl start %s)", bin, daemonUnit(bin))
			if isPermissionDenied(out) || isPermissionDenied(err.Error()) {
				why = fmt.Sprintf("%s is running, but this session may not talk to it: "+
					"permission denied on the socket.\n\nYou are probably in the %s "+
					"group already — group membership is applied at LOGIN, so a session "+
					"that started before it was granted still lacks it. Log out and back "+
					"in.\n\nTo check:  id -nG | tr ' ' '\\n' | grep %s",
					bin, daemonUnit(bin), daemonUnit(bin))
			}
			return Engine{Bin: bin, Name: bin, Found: false, Why: why}
		}
		fields := strings.Split(strings.TrimSpace(out), "\t")
		if len(fields) > 0 {
			e.Driver = strings.TrimSpace(fields[0])
		}
		if len(fields) > 1 {
			e.GraphRoot = strings.TrimSpace(fields[1])
		}
		return e
	}
	return Engine{Why: "no container engine on this host — install docker.io (Debian) " +
		"or podman (RHEL/Fedora)"}
}

// daemonUnit maps an engine to the unit that has to be running.
func daemonUnit(bin string) string {
	if bin == "docker" {
		return "docker"
	}
	return "podman.socket"
}

// Container is one container, in the terms the pane shows.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string // running | exited | created | paused
	Status string // the human phrase: "Up 3 hours"
	Ports  string
}

// Running reports whether the verbs that need it are applicable.
func (c Container) Running() bool {
	return strings.EqualFold(c.State, "running")
}

// psJSON is the subset of `ps --format json` both engines emit.
//
// The two disagree on shape — docker gives a string for Names and Ports,
// podman gives arrays — so both forms are accepted and normalised. A single
// struct with json.RawMessage would push that mess into the UI instead.
type psJSON struct {
	ID      string          `json:"ID"`
	Names   json.RawMessage `json:"Names"`
	Image   string          `json:"Image"`
	State   string          `json:"State"`
	Status  string          `json:"Status"`
	Ports   json.RawMessage `json:"Ports"`
	Command json.RawMessage `json:"Command"`
}

// firstString reads a field that is either a string or an array of strings.
func firstString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var a []string
	if err := json.Unmarshal(raw, &a); err == nil && len(a) > 0 {
		return strings.Join(a, ", ")
	}
	return ""
}

// ListContainers returns every container, running or not.
//
// `-a` on purpose: a stopped container still owns its writable layer — which
// on this substrate is a dataset holding real space — so hiding it hides the
// thing somebody came to reclaim.
func (e Engine) ListContainers(timeout time.Duration) ([]Container, error) {
	if !e.Found {
		return nil, fmt.Errorf("%s", e.Why)
	}
	out, err := containerRun(timeout, e.Bin, "ps", "-a", "--format", "json")
	if err != nil {
		// The engine's own message, not "exit status 1". containerRun already
		// merged stderr in, so the useful sentence is sitting in `out` while
		// the error carries only the status — reporting the status alone told
		// an operator nothing at all (fiend, 2026-08-16).
		if msg := strings.TrimSpace(out); msg != "" {
			if isPermissionDenied(msg) {
				return nil, fmt.Errorf("permission denied on the %s socket.\n\n"+
					"You are probably in the %s group already — membership is applied at "+
					"LOGIN, so a desktop session that started before it was granted still "+
					"lacks it. Log out and back in.", e.Bin, daemonUnit(e.Bin))
			}
			return nil, fmt.Errorf("%s ps: %s", e.Bin, msg)
		}
		return nil, fmt.Errorf("%s ps: %w", e.Bin, err)
	}
	rows, err := decodePS(out)
	if err != nil {
		return nil, err
	}
	res := make([]Container, 0, len(rows))
	for _, r := range rows {
		c := Container{
			ID:     shortID(r.ID),
			Name:   strings.TrimPrefix(firstString(r.Names), "/"),
			Image:  r.Image,
			State:  r.State,
			Status: r.Status,
			Ports:  firstString(r.Ports),
		}
		if c.State == "" {
			// docker's older format omits State and only carries Status.
			c.State = strings.ToLower(strings.Fields(c.Status + " ")[0])
			if strings.HasPrefix(c.State, "up") {
				c.State = "running"
			}
		}
		res = append(res, c)
	}
	return res, nil
}

// decodePS handles both shapes of `--format json`.
//
// docker emits ONE OBJECT PER LINE; podman emits a single JSON ARRAY. Parsing
// only one of them works perfectly on the machine you tested and returns
// nothing on the other.
func decodePS(out string) ([]psJSON, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []psJSON
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, fmt.Errorf("could not read the container list: %w", err)
		}
		return arr, nil
	}
	var rows []psJSON
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r psJSON
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // one unreadable row must not blank the table
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// Image is one image in the local store.
type Image struct {
	Repo string
	Tag  string
	ID   string
	Size string
}

// Ref is the name a person uses.
func (i Image) Ref() string {
	if i.Repo == "" || i.Repo == "<none>" {
		return i.ID
	}
	if i.Tag == "" || i.Tag == "<none>" {
		return i.Repo
	}
	return i.Repo + ":" + i.Tag
}

// ListImages returns the local image store.
func (e Engine) ListImages(timeout time.Duration) ([]Image, error) {
	if !e.Found {
		return nil, fmt.Errorf("%s", e.Why)
	}
	// Tab-separated rather than json: the two engines disagree far more about
	// image JSON than about ps, and these four fields are all the pane wants.
	out, err := containerRun(timeout, e.Bin, "images",
		"--format", "{{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}")
	if err != nil {
		if msg := strings.TrimSpace(out); msg != "" {
			return nil, fmt.Errorf("%s images: %s", e.Bin, msg)
		}
		return nil, fmt.Errorf("%s images: %w", e.Bin, err)
	}
	var res []Image
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		for len(f) < 4 {
			f = append(f, "")
		}
		res = append(res, Image{
			Repo: strings.TrimSpace(f[0]), Tag: strings.TrimSpace(f[1]),
			ID: shortID(strings.TrimSpace(f[2])), Size: strings.TrimSpace(f[3]),
		})
	}
	return res, nil
}

// Verb runs one lifecycle action against a container.
//
// Args:    action, one of start|stop|restart|rm; id, the container.
// Returns: the engine's own message on failure, which is more useful than
// anything this layer could invent.
//
// The allowlist is the safety property: `action` reaches an argv, and a
// caller that could pass "exec" or "run" would turn a button into arbitrary
// execution inside somebody's container.
func (e Engine) Verb(action, id string, timeout time.Duration) error {
	switch action {
	case "start", "stop", "restart", "rm":
	default:
		return fmt.Errorf("refusing unknown container action %q", action)
	}
	if !e.Found {
		return fmt.Errorf("%s", e.Why)
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("no container selected")
	}
	args := []string{action}
	if action == "rm" {
		// A stopped container is the normal case for rm; -f covers a running
		// one, which the UI has already confirmed.
		args = append(args, "-f")
	}
	args = append(args, id)
	if out, err := containerRun(timeout, e.Bin, args...); err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", e.Bin, action, msg)
	}
	return nil
}

// Logs returns the tail of a container's log.
func (e Engine) Logs(id string, lines int, timeout time.Duration) (string, error) {
	if !e.Found {
		return "", fmt.Errorf("%s", e.Why)
	}
	if lines <= 0 {
		lines = 200
	}
	return containerRun(timeout, e.Bin, "logs", "--tail", fmt.Sprint(lines), id)
}

// shortID trims an ID to the 12 characters every engine displays.
func shortID(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// containerRun executes an engine command with a deadline, returning stdout
// and stderr together.
//
// Both streams: engines put the useful part of a failure on stderr, and a
// button that reports "exit status 1" is a button nobody can act on.
func containerRun(timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cmd := exec.Command(name, args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), fmt.Errorf("%s timed out after %s", name, timeout)
	}
}

// HasContainerEngine reports whether this host has a container runtime at all.
//
// WHY THE BINARY AND NOT THE DAEMON: an installed docker with a stopped
// dockerd is worth a tab — it can say "start the service", which is
// actionable. A host with neither binary has nothing to manage, and a
// permanently empty tab is worse than no tab. The BSD posture and any
// unix-like without an engine therefore never see it.
//
// This is a CAPABILITY probe, deliberately not a distro test: gating on
// "is this Debian" would hide the tab on a RHEL box running podman, which
// has containers to manage right now.
func HasContainerEngine() bool {
	for _, bin := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// StoreDataset resolves the engine's graphroot to the ZFS dataset holding it.
//
// Returns: the dataset name, or ok=false when the store is not on ZFS.
//
// WHY THIS ONE DATASET MATTERS: with the zfs driver it carries the whole
// container estate — the layer datasets as children, the engine's database,
// the layer graph, AND the volumes. One recursive snapshot captures all of
// it; one `zfs send -R` moves all of it to another machine, incrementally
// after the first. That is the capability this substrate has and a plain
// container host does not.
func (e Engine) StoreDataset(timeout time.Duration) (string, bool) {
	if !e.Found || e.GraphRoot == "" {
		return "", false
	}
	out, err := containerRun(timeout, "zfs", "list", "-H", "-o", "name,mountpoint")
	if err != nil {
		return "", false
	}
	// Longest matching mountpoint wins: /var/lib/containers/storage is a
	// dataset in its own right, and so is its parent — replicating the
	// parent would drag in unrelated state.
	best, bestLen := "", -1
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] == "legacy" || f[1] == "none" {
			continue
		}
		if f[1] == e.GraphRoot && len(f[1]) > bestLen {
			best, bestLen = f[0], len(f[1])
		}
	}
	return best, best != ""
}

// SnapshotStore takes a recursive snapshot of the whole container estate.
//
// Args:    tag, the snapshot suffix; the dataset is resolved from the engine.
// Returns: the full snapshot name, or an error.
//
// Recursive (-r) because the layers are CHILD datasets: a non-recursive
// snapshot would capture the engine's database and none of the images it
// describes, which restores to a store that references layers that are not
// there — worse than no snapshot.
func (e Engine) SnapshotStore(tag string, timeout time.Duration) (string, error) {
	ds, ok := e.StoreDataset(timeout)
	if !ok {
		return "", fmt.Errorf("the container store is not on a ZFS dataset "+
			"(driver is %q) — there is nothing to snapshot as a unit", e.Driver)
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", fmt.Errorf("give the snapshot a name")
	}
	// The same allowlist ZFS itself enforces, applied early so a bad name is
	// a dialog rather than a shell surprise.
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == ':' || r == '.':
		default:
			return "", fmt.Errorf("snapshot name %q may only contain letters, "+
				"digits, and _ - : .", tag)
		}
	}
	snap := ds + "@" + tag
	if out, err := containerRun(timeout, "zfs", "snapshot", "-r", snap); err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("zfs snapshot: %s", msg)
	}
	return snap, nil
}

// StoreSnapshot is one recorded state of the whole container estate.
type StoreSnapshot struct {
	Name string // the tag after @
	Full string // dataset@tag
	Used string // space held by this snapshot
}

// ListStoreSnapshots returns the store's snapshots, newest last.
func (e Engine) ListStoreSnapshots(timeout time.Duration) ([]StoreSnapshot, error) {
	ds, ok := e.StoreDataset(timeout)
	if !ok {
		return nil, fmt.Errorf("the container store is not on a ZFS dataset")
	}
	// -d 1 so this lists the STORE's own snapshots and not the hundreds the
	// layer children carry under the same tag.
	out, err := containerRun(timeout, "zfs", "list", "-H", "-t", "snapshot",
		"-o", "name,used", "-s", "creation", "-d", "1", ds)
	if err != nil {
		return nil, fmt.Errorf("zfs list: %s", strings.TrimSpace(out))
	}
	var res []StoreSnapshot
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		_, tag, ok := strings.Cut(f[0], "@")
		if !ok {
			continue
		}
		res = append(res, StoreSnapshot{Name: tag, Full: f[0], Used: f[1]})
	}
	return res, nil
}

// RollbackStore returns the whole container estate to a snapshot.
//
// Args:    tag, the snapshot name taken by SnapshotStore.
// Returns: an error, or nil once every dataset is back.
//
// WHY IT ROLLS BACK EACH DATASET INDIVIDUALLY: `zfs rollback -r` does NOT
// recurse into children — the -r means "destroy snapshots newer than this
// one on THIS dataset". The layers are child datasets, so rolling back only
// the parent restores the engine's database to a state describing layers
// that were never rolled back with it. That is a store which references
// images it no longer has: worse than not rolling back at all.
//
// THE ENGINE MUST BE STOPPED. Rolling storage out from under a running
// daemon that holds those files open is how a container store gets
// corrupted; the caller is responsible for stopping it, and this refuses
// when it can tell the daemon is still up.
func (e Engine) RollbackStore(tag string, timeout time.Duration) error {
	ds, ok := e.StoreDataset(timeout)
	if !ok {
		return fmt.Errorf("the container store is not on a ZFS dataset")
	}
	if strings.TrimSpace(tag) == "" {
		return fmt.Errorf("pick a snapshot to roll back to")
	}
	// Refuse while the daemon is answering: it holds the store open.
	if probe := DetectEngine(timeout); probe.Found {
		return fmt.Errorf("%s is still running — stop it first "+
			"(systemctl stop %s), then roll back", e.Bin, daemonUnit(e.Bin))
	}

	// Parent first, then every child layer, all to the same tag.
	targets := []string{ds}
	out, err := containerRun(timeout, "zfs", "list", "-H", "-r", "-o", "name", ds)
	if err != nil {
		return fmt.Errorf("could not enumerate the store's datasets: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && name != ds {
			targets = append(targets, name)
		}
	}
	var failed []string
	for _, t := range targets {
		if o, err := containerRun(timeout, "zfs", "rollback", "-r", t+"@"+tag); err != nil {
			// A child created AFTER the snapshot has no such snapshot; that
			// is expected and not a failure of the rollback.
			if strings.Contains(o, "could not find any snapshots to destroy") ||
				strings.Contains(o, "does not exist") {
				continue
			}
			failed = append(failed, t)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("rolled back the store but %d dataset(s) refused: %s",
			len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// ReplicateArgv builds the send command for the whole container estate.
//
// Args:    snap, the store snapshot to send; since, an earlier snapshot tag
//
//	for an incremental stream ("" for a full one); encrypted, whether
//	the dataset carries encryption.
//
// Returns: the argv, ready to pipe into `zfs recv` on the far side.
//
// WHY -w IS NOT OPTIONAL HERE: kldload installs an encrypted pool by
// default, and `zfs send -R` refuses outright on an encrypted dataset —
//
//	"encrypted dataset may not be sent with properties without the raw flag"
//
// so the obvious recursive send fails on every kldload host (measured on
// fiend, 2026-08-15). The raw flag sends the encrypted blocks as they sit,
// which also means the stream never exposes plaintext in transit — the
// receiving side needs the key to mount it, not to store it.
//
// -R carries the CHILDREN, which is the whole point: the layer datasets, the
// engine's database and the volumes all travel together, and a stream of the
// parent alone restores a store describing images it does not have.
func ReplicateArgv(snap, since string, encrypted bool) []string {
	argv := []string{"zfs", "send", "-R"}
	if encrypted {
		argv = append(argv, "-w")
	}
	if strings.TrimSpace(since) != "" {
		// Incremental: everything between the two snapshots. After the first
		// full send this is what makes replicating a container estate cheap.
		argv = append(argv, "-I", since)
	}
	return append(argv, snap)
}

// StoreEncrypted reports whether the container store is encrypted, which
// decides whether a send needs the raw flag.
func (e Engine) StoreEncrypted(timeout time.Duration) bool {
	ds, ok := e.StoreDataset(timeout)
	if !ok {
		return false
	}
	out, err := containerRun(timeout, "zfs", "get", "-H", "-o", "value", "encryption", ds)
	if err != nil {
		// Assume encrypted: a raw send of an unencrypted dataset still works,
		// while a non-raw send of an encrypted one fails outright. Guessing
		// wrong in this direction costs nothing.
		return true
	}
	v := strings.TrimSpace(out)
	return v != "" && v != "off" && v != "-"
}

// isPermissionDenied recognises the engine refusing an unprivileged caller.
//
// Matched on the message because both engines exit 1 for everything, so the
// status code cannot tell "daemon down" from "not your socket" — and those
// two need opposite actions from whoever is reading.
func isPermissionDenied(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "permission denied") ||
		strings.Contains(l, "dial unix /var/run/docker.sock") ||
		strings.Contains(l, "connect: permission denied")
}

// plausibleDriver reports whether an `info --format` result looks like a
// storage driver rather than an error message.
//
// WHY THIS EXISTS: containerRun merges stderr into stdout so failures are
// legible, and `docker info` prints its error and STILL EXITS 0. The result
// was a permission-denied message rendered as the storage driver, with the
// engine reported as working — the header read "storage driver permission
// denied while trying to connect to the Docker daemon socket..." and the
// pane concluded layers were ordinary files (fiend, 2026-08-16).
//
// A driver name is one short token with no spaces. Anything else is prose,
// and prose here means something went wrong.
func plausibleDriver(out string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	first = strings.TrimSpace(first)
	if first == "" || len(first) > 24 || strings.ContainsAny(first, " :/\"") {
		return false
	}
	return true
}
