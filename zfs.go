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

// Dossier renders a human-readable detail block for the highlighted dataset:
// space, key properties, and recent snapshots. Mirrors the bash preview.
func Dossier(h Host, dataset string) string {
	props := []string{
		"type", "used", "referenced", "available", "compression", "compressratio",
		"recordsize", "mountpoint", "readonly", "encryption", "keystatus", "origin", "creation",
	}
	out, err := run(h.command("zfs", "get", "-H", "-o", "property,value",
		strings.Join(props, ","), dataset))
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", dataset, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", dataset)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[1] == "-" {
			continue
		}
		fmt.Fprintf(&b, "  %-14s %s\n", f[0], f[1])
	}
	b.WriteString("\n── snapshots (newest last) ──\n")
	snaps, err := ListSnapshots(h, dataset)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "  (error: %v)\n", err)
	case len(snaps) == 0:
		b.WriteString("  (none)\n")
	default:
		start := 0
		if len(snaps) > 12 {
			start = len(snaps) - 12
		}
		for _, sn := range snaps[start:] {
			short := sn.Name
			if i := strings.IndexByte(short, '@'); i >= 0 {
				short = short[i+1:]
			}
			fmt.Fprintf(&b, "  %-24s %8s  %s\n", short, sn.Used, sn.Creation)
		}
	}
	return b.String()
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
