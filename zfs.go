// zfs.go — the ZFS command layer.
//
// Every function shells out to the portable `zfs`/`zpool` CLI — nothing
// platform-specific — so the same binary runs unchanged on any ZFS system
// (Linux distros + FreeBSD). Remote operations run the same commands over
// `ssh <host>`, so a Host is either local or `[user@]host`.
//
// Thin, honest wrappers: each returns parsed rows or an error; the TUI decides
// how to present it. No global state.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Dataset is one ZFS filesystem or volume row.
type Dataset struct {
	Name  string
	Used  string
	Refer string
	Snaps int
}

// Snapshot is one snapshot row.
type Snapshot struct {
	Name     string
	Used     string
	Creation string
}

// Host is a location the tool operates against: local, or remote over SSH.
// SSH == "" means local; otherwise it's a "[user@]host" passed to ssh.
type Host struct {
	SSH string
}

// LocalHost returns the local ZFS host.
func LocalHost() Host { return Host{} }

// Label is a short human name for the host ("local" or the ssh target).
func (h Host) Label() string {
	if h.SSH == "" {
		return "local"
	}
	return h.SSH
}

// command builds an *exec.Cmd for `program args…`, wrapping it in ssh when the
// host is remote. Batch mode + connect timeout so an unauthorised key fails
// fast instead of hanging.
func (h Host) command(program string, args ...string) *exec.Cmd {
	if h.SSH == "" {
		return exec.Command(program, args...)
	}
	sshArgs := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=8",
		h.SSH, program,
	}
	sshArgs = append(sshArgs, args...)
	return exec.Command("ssh", sshArgs...)
}

// run executes a command and returns stdout, or an error carrying stderr.
func run(cmd *exec.Cmd) (string, error) {
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s", msg)
	}
	return out.String(), nil
}

// ListDatasets lists all filesystems + volumes at a host, with snapshot counts.
func ListDatasets(h Host) ([]Dataset, error) {
	out, err := run(h.command("zfs",
		"list", "-H", "-p", "-o", "name,used,refer", "-t", "filesystem,volume"))
	if err != nil {
		return nil, err
	}
	var rows []Dataset
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		rows = append(rows, Dataset{Name: f[0], Used: human(f[1]), Refer: human(f[2])})
	}
	// Snapshot counts in one pass.
	if snapOut, err := run(h.command("zfs",
		"list", "-H", "-o", "name", "-t", "snapshot")); err == nil {
		for i := range rows {
			prefix := rows[i].Name + "@"
			for _, s := range strings.Split(snapOut, "\n") {
				if strings.HasPrefix(s, prefix) {
					rows[i].Snaps++
				}
			}
		}
	}
	return rows, nil
}

// ListSnapshots lists the snapshots of a dataset (newest last).
func ListSnapshots(h Host, dataset string) ([]Snapshot, error) {
	out, err := run(h.command("zfs",
		"list", "-H", "-o", "name,used,creation", "-s", "creation",
		"-t", "snapshot", "-r", "-d", "1", dataset))
	if err != nil {
		return nil, err
	}
	var rows []Snapshot
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		rows = append(rows, Snapshot{Name: f[0], Used: f[1], Creation: f[2]})
	}
	return rows, nil
}

// singleProp reads one property value ("?" on error).
func singleProp(h Host, dataset, prop string) string {
	out, err := run(h.command("zfs", "get", "-H", "-o", "value", prop, dataset))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(out)
}

// healthSummary surfaces PROBLEMS first: the pool's health verdict, capacity /
// fragmentation, scrub state, and data / vdev errors — each flagged so trouble
// is obvious at a glance, before the raw property dump.
func healthSummary(h Host, dataset string) string {
	pool := dataset
	if i := strings.IndexByte(pool, '/'); i >= 0 {
		pool = pool[:i]
	}
	var b strings.Builder
	var problems []string

	if li, err := run(h.command("zpool", "list", "-H", "-o",
		"name,health,capacity,fragmentation,free,size,dedupratio", pool)); err == nil {
		f := strings.Split(strings.TrimRight(li, "\n"), "\t")
		if len(f) >= 7 {
			fmt.Fprintf(&b, "  %s   health %s   cap %s   frag %s   free %s/%s   dedup %s\n",
				f[0], f[1], f[2], f[3], f[4], f[5], f[6])
			if f[1] != "ONLINE" {
				problems = append(problems, "pool "+f[1])
			}
			if n, e := strconv.Atoi(strings.TrimSuffix(f[2], "%")); e == nil && n >= 80 {
				problems = append(problems, "capacity "+f[2])
			}
		}
	}

	if st, err := run(h.command("zpool", "status", pool)); err == nil {
		for _, l := range strings.Split(st, "\n") {
			ls := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(ls, "scan:"):
				fmt.Fprintf(&b, "  %s\n", ls)
			case strings.HasPrefix(ls, "errors:"):
				fmt.Fprintf(&b, "  %s\n", ls)
				if !strings.Contains(ls, "No known data errors") {
					problems = append(problems, "data errors")
				}
			}
		}
		if vdevErrors(st) {
			problems = append(problems, "vdev read/write/cksum errors")
		}
	}

	if len(problems) == 0 {
		b.WriteString("  ✓ healthy — no problems detected\n")
	} else {
		fmt.Fprintf(&b, "  ⚠ ATTENTION: %s\n", strings.Join(problems, "; "))
	}
	return b.String()
}

// vdevErrors reports whether any vdev row in `zpool status` output carries a
// nonzero READ/WRITE/CKSUM count.
func vdevErrors(status string) bool {
	inConfig := false
	for _, l := range strings.Split(status, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "config:") {
			inConfig = true
			continue
		}
		if strings.HasPrefix(t, "errors:") {
			inConfig = false
		}
		if !inConfig {
			continue
		}
		f := strings.Fields(t)
		if len(f) < 5 || f[0] == "NAME" {
			continue
		}
		r, e1 := strconv.Atoi(f[len(f)-3])
		w, e2 := strconv.Atoi(f[len(f)-2])
		c, e3 := strconv.Atoi(f[len(f)-1])
		if e1 == nil && e2 == nil && e3 == nil && (r|w|c) != 0 {
			return true
		}
	}
	return false
}

// Dossier renders a FULL detail block for a dataset — nothing curated:
//   - EVERY ZFS property, with its source (local / inherited / received / default)
//   - both permission layers: POSIX mode + ACLs on the mountpoint, AND the
//     kernel `zfs allow` delegations
//   - every snapshot (newest last)
//
// The UI pane is scrollable, so we show it all rather than a subset.
func Dossier(h Host, dataset string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s   [%s]\n\n", dataset, singleProp(h, dataset, "type"))

	// ── every property, with source ──
	b.WriteString("── properties  (name = value  [source]) ──\n")
	out, err := run(h.command("zfs", "get", "-H", "-o", "property,value,source", "all", dataset))
	if err != nil {
		fmt.Fprintf(&b, "  (cannot read %s: %v)\n", dataset, err)
	} else {
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			f := strings.Split(line, "\t")
			if len(f) < 2 {
				continue
			}
			tag := ""
			if len(f) >= 3 {
				switch {
				case f[2] == "" || f[2] == "-":
					tag = "" // read-only / computed — no source
				case strings.HasPrefix(f[2], "inherited from "):
					tag = "   [inherited: " + strings.TrimPrefix(f[2], "inherited from ") + "]"
				default:
					tag = "   [" + f[2] + "]" // default, local, received, temporary
				}
			}
			fmt.Fprintf(&b, "  %-26s %s%s\n", f[0], f[1], tag)
		}
	}

	// ── permissions: POSIX + ACL on the mountpoint, then ZFS delegations ──
	b.WriteString("\n── permissions ──\n")
	mp := singleProp(h, dataset, "mountpoint")
	if strings.HasPrefix(mp, "/") {
		if o, e := run(h.command("ls", "-ldh", mp)); e == nil {
			fmt.Fprintf(&b, "  POSIX   %s\n", strings.TrimRight(o, "\n"))
		}
		if o, e := run(h.command("getfacl", "-p", mp)); e == nil {
			for _, l := range strings.Split(strings.TrimRight(o, "\n"), "\n") {
				if l != "" && !strings.HasPrefix(l, "#") {
					fmt.Fprintf(&b, "  acl     %s\n", l)
				}
			}
		}
	} else {
		fmt.Fprintf(&b, "  POSIX   (mountpoint: %s)\n", mp)
	}
	if o, e := run(h.command("zfs", "allow", dataset)); e == nil && strings.TrimSpace(o) != "" {
		b.WriteString("  zfs delegated:\n")
		for _, l := range strings.Split(strings.TrimRight(o, "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				fmt.Fprintf(&b, "    %s\n", strings.TrimRight(l, " "))
			}
		}
	} else {
		b.WriteString("  zfs delegated   (none — root only)\n")
	}

	// ── snapshots ──
	b.WriteString("\n── snapshots (newest last) ──\n")
	snaps, err := ListSnapshots(h, dataset)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "  (error: %v)\n", err)
	case len(snaps) == 0:
		b.WriteString("  (none)\n")
	default:
		for _, sn := range snaps {
			short := sn.Name
			if i := strings.IndexByte(short, '@'); i >= 0 {
				short = short[i+1:]
			}
			fmt.Fprintf(&b, "  %-30s %8s  %s\n", short, sn.Used, sn.Creation)
		}
	}
	return b.String()
}

// ListPools lists pool names (the root datasets) at a host.
func ListPools(h Host) ([]string, error) {
	out, err := run(h.command("zpool", "list", "-H", "-o", "name"))
	if err != nil {
		return nil, err
	}
	var pools []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			pools = append(pools, l)
		}
	}
	return pools, nil
}

// ListChildren lists the immediate child datasets of a dataset (depth 1,
// excluding the dataset itself).
func ListChildren(h Host, dataset string) ([]Dataset, error) {
	out, err := run(h.command("zfs",
		"list", "-H", "-p", "-o", "name,used,refer", "-t", "filesystem,volume",
		"-r", "-d", "1", dataset))
	if err != nil {
		return nil, err
	}
	var rows []Dataset
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 || f[0] == dataset {
			continue
		}
		rows = append(rows, Dataset{Name: f[0], Used: human(f[1]), Refer: human(f[2])})
	}
	return rows, nil
}

// shellQuote single-quotes s for safe inclusion in an `sh -c` pipeline.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sshPrefix(h Host) string {
	return "ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=8 " + shellQuote(h.SSH)
}

// incrementalBase returns the newest source snapshot whose short name also
// exists on the destination — the incremental origin — or "" for a full send.
func incrementalBase(srcHost Host, srcDs string, dstHost Host, dstPath string) string {
	dstSnaps, err := ListSnapshots(dstHost, dstPath)
	if err != nil || len(dstSnaps) == 0 {
		return ""
	}
	have := map[string]bool{}
	for _, s := range dstSnaps {
		if i := strings.IndexByte(s.Name, '@'); i >= 0 {
			have[s.Name[i+1:]] = true
		}
	}
	srcSnaps, err := ListSnapshots(srcHost, srcDs) // creation order (oldest→newest)
	if err != nil {
		return ""
	}
	base := ""
	for _, s := range srcSnaps {
		if i := strings.IndexByte(s.Name, '@'); i >= 0 && have[s.Name[i+1:]] {
			base = srcDs + "@" + s.Name[i+1:]
		}
	}
	return base
}

// ReplicatePipeline builds the `sh -c` command that sends srcSnap to
// dstHost:dstPath — incremental when a common snapshot exists, else full. The
// target is received readonly + non-automounting so it can't drift out of the
// incremental chain. Works for any local/remote combination.
func ReplicatePipeline(srcHost Host, srcSnap string, dstHost Host, dstPath string) string {
	srcDs := srcSnap
	if i := strings.IndexByte(srcSnap, '@'); i >= 0 {
		srcDs = srcSnap[:i]
	}
	base := incrementalBase(srcHost, srcDs, dstHost, dstPath)

	send := "zfs send -v "
	if base != "" {
		send += "-i " + shellQuote(base) + " "
	}
	send += shellQuote(srcSnap)
	if srcHost.SSH != "" {
		send = sshPrefix(srcHost) + " " + shellQuote(send)
	}

	recv := "zfs recv -F -o readonly=on -o canmount=noauto " + shellQuote(dstPath)
	if dstHost.SSH != "" {
		recv = sshPrefix(dstHost) + " " + shellQuote(recv)
	}
	return send + " | " + recv
}

// SnapshotNow takes an ad-hoc snapshot of a dataset and returns its full name.
func SnapshotNow(h Host, dataset, name string) (string, error) {
	snap := dataset + "@" + name
	_, err := run(h.command("zfs", "snapshot", snap))
	return snap, err
}

// IsKldload reports whether we're on a kldload system, so zexplore can light up
// the extra primitives (boot environments, etc.). Universal ZFS otherwise.
func IsKldload() bool {
	for _, p := range []string{"/usr/local/bin/kbe", "/etc/kldload"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// human turns a parseable byte count (`zfs list -p`) into e.g. "4.9G".
func human(s string) string {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	units := []string{"B", "K", "M", "G", "T", "P"}
	v, i := n, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", int64(n))
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}
