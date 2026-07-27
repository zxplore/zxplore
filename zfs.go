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
	SSH     string // "" = local; else "[user@]host"
	Port    int    // 0 = default 22
	KeyPath string // "" = agent/default keys; else an explicit identity file
	Jump    string // "" = direct; else a ProxyJump chain "[user@]host[,…]"
}

// sshOpts returns the extra ssh args for this host: a non-default port, an
// explicit identity file (IdentitiesOnly so only that key is offered), and a
// ProxyJump chain (bastion / jump hosts, like an SSH client's -J).
func (h Host) sshOpts() []string {
	var o []string
	if h.Port != 0 && h.Port != 22 {
		o = append(o, "-p", strconv.Itoa(h.Port))
	}
	if h.KeyPath != "" {
		o = append(o, "-i", h.KeyPath, "-o", "IdentitiesOnly=yes")
	}
	if h.Jump != "" {
		o = append(o, "-o", "ProxyJump="+h.Jump)
	}
	return o
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
	}
	sshArgs = append(sshArgs, h.sshOpts()...)
	sshArgs = append(sshArgs, h.SSH, program)
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
// propVal is a parsed `zfs get` row: the value plus a source tag ([local], …).
type propVal struct{ value, tag string }

// propGroups turns `zfs get all`'s flat alphabetical dump into a story, ordered
// the way an operator reasons about a dataset: what it IS, how much it stores,
// how it's laid out/tuned, how it's encrypted, where it mounts/shares, how it
// snapshots. Any property not named here still prints under OTHER / CUSTOM — so
// the view stays complete (nothing hidden) while the groups make problems obvious.
var propGroups = []struct {
	title string
	props []string
}{
	{"IDENTITY", []string{"type", "creation", "guid", "createtxg", "origin", "clones", "version"}},
	{"CAPACITY & USAGE", []string{
		"used", "available", "referenced",
		"usedbydataset", "usedbysnapshots", "usedbychildren", "usedbyrefreservation",
		"logicalused", "logicalreferenced",
		"quota", "refquota", "reservation", "refreservation", "volsize",
	}},
	{"DATA LAYOUT & TUNING", []string{
		"recordsize", "volblocksize", "compression", "compressratio", "refcompressratio",
		"dedup", "checksum", "copies", "atime", "relatime",
		"sync", "logbias", "primarycache", "secondarycache",
		"redundant_metadata", "special_small_blocks",
	}},
	{"ENCRYPTION", []string{
		"encryption", "encryptionroot", "keyformat", "keylocation", "keystatus", "pbkdf2iters",
	}},
	{"MOUNT & SHARING", []string{
		"mounted", "mountpoint", "canmount", "readonly", "exec", "setuid", "devices",
		"nbmand", "overlay", "sharenfs", "sharesmb",
		"acltype", "aclmode", "aclinherit", "xattr",
		"casesensitivity", "utf8only", "normalization",
	}},
	{"SNAPSHOTS & POLICY", []string{
		"snapdir", "snapshot_limit", "snapshot_count", "defer_destroy", "userrefs",
		"com.sun:auto-snapshot",
	}},
}

// dblock is one titled "card" (a group's header + its property lines) that
// packColumns lays out side by side.
type dblock struct{ lines []string }

// packColumns lays block cards into `cols` balanced columns (each block kept
// intact), rendered side by side — turns a very long single-column property
// dump into a compact grid, wrapping onto more rows as needed. Column-major
// greedy: each block goes to the currently-shortest column so heights stay
// even; a blank line separates stacked blocks. Rune-counted so the ━ headers
// (multi-byte) still align in a monospace pane.
func packColumns(blocks []dblock, cols int) string {
	if cols < 1 {
		cols = 1
	}
	colLines := make([][]string, cols)
	for _, bl := range blocks {
		min := 0
		for c := 1; c < cols; c++ {
			if len(colLines[c]) < len(colLines[min]) {
				min = c
			}
		}
		if len(colLines[min]) > 0 {
			colLines[min] = append(colLines[min], "") // gap between cards
		}
		colLines[min] = append(colLines[min], bl.lines...)
	}
	widths := make([]int, cols)
	maxRows := 0
	for c := 0; c < cols; c++ {
		for _, l := range colLines[c] {
			if n := len([]rune(l)); n > widths[c] {
				widths[c] = n
			}
		}
		if len(colLines[c]) > maxRows {
			maxRows = len(colLines[c])
		}
	}
	const gutter = "   "
	var b strings.Builder
	for r := 0; r < maxRows; r++ {
		for c := 0; c < cols; c++ {
			var cell string
			if r < len(colLines[c]) {
				cell = colLines[c][r]
			}
			if c < cols-1 { // pad all but the last column, then gutter
				if pad := widths[c] - len([]rune(cell)); pad > 0 {
					cell += strings.Repeat(" ", pad)
				}
				cell += gutter
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func Dossier(h Host, dataset string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s   [%s]\n", dataset, singleProp(h, dataset, "type"))

	// ── HEALTH (full width): the operator's "is anything wrong?" answer ──
	b.WriteString("\n━━━ HEALTH ━━━\n")
	b.WriteString(healthSummary(h, dataset))

	// (Snapshots are shown in the interactive list below the dossier — arrow +
	// Enter or click to roll back / clone / hold — so they're not duplicated here.)

	// ── parse every property once, keeping source tags and original order ──
	props := map[string]propVal{}
	var order []string
	if out, err := run(h.command("zfs", "get", "-H", "-o", "property,value,source", "all", dataset)); err != nil {
		fmt.Fprintf(&b, "\n  (cannot read properties of %s: %v)\n", dataset, err)
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
					tag = " [inh:" + strings.TrimPrefix(f[2], "inherited from ") + "]"
				default:
					tag = " [" + f[2] + "]" // default, local, received, temporary
				}
			}
			props[f[0]] = propVal{value: f[1], tag: tag}
			order = append(order, f[0])
		}
	}

	shown := map[string]bool{}
	cell := func(name string) string {
		p := props[name]
		shown[name] = true
		return fmt.Sprintf("  %-13s %s%s", name, p.value, p.tag)
	}

	// ── build one card per group, then pack them into 4 columns ──
	var cards []dblock
	for _, g := range propGroups {
		var lines []string
		for _, name := range g.props {
			if _, ok := props[name]; ok && !shown[name] {
				lines = append(lines, cell(name))
			}
		}
		if len(lines) > 0 {
			cards = append(cards, dblock{lines: append([]string{"━━ " + g.title + " ━━"}, lines...)})
		}
	}
	var otherLines []string
	for _, name := range order {
		if !shown[name] {
			otherLines = append(otherLines, cell(name))
		}
	}
	if len(otherLines) > 0 {
		cards = append(cards, dblock{lines: append([]string{"━━ OTHER / CUSTOM ━━"}, otherLines...)})
	}
	b.WriteString("\n")
	b.WriteString(packColumns(cards, 4))

	// ── PERMISSIONS (full width): POSIX + ACL on the mountpoint, then ZFS delegations ──
	b.WriteString("\n━━━ PERMISSIONS ━━━\n")
	mp := singleProp(h, dataset, "mountpoint")
	if strings.HasPrefix(mp, "/") {
		b.WriteString("  POSIX (mountpoint)\n")
		if o, e := run(h.command("ls", "-ldh", mp)); e == nil {
			fmt.Fprintf(&b, "    %s\n", strings.TrimRight(o, "\n"))
		}
		printedACL := false
		if o, e := run(h.command("getfacl", "-p", mp)); e == nil {
			for _, l := range strings.Split(strings.TrimRight(o, "\n"), "\n") {
				if l != "" && !strings.HasPrefix(l, "#") {
					if !printedACL {
						b.WriteString("  ACL (getfacl)\n")
						printedACL = true
					}
					fmt.Fprintf(&b, "    %s\n", l)
				}
			}
		}
	} else {
		fmt.Fprintf(&b, "  POSIX   (not mounted — mountpoint: %s)\n", mp)
	}
	if o, e := run(h.command("zfs", "allow", dataset)); e == nil && strings.TrimSpace(o) != "" {
		b.WriteString("  ZFS delegated (zfs allow)\n")
		for _, l := range strings.Split(strings.TrimRight(o, "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				fmt.Fprintf(&b, "    %s\n", strings.TrimRight(l, " "))
			}
		}
	} else {
		b.WriteString("  ZFS delegated   (none — root only)\n")
	}

	return b.String()
}

// ── property editing ────────────────────────────────────────────────────────

// PropControl tells the GUI how to edit a property: a checkbox (bool), a dropdown
// (enum, with its options), or a free-text field.
type PropControl struct {
	Kind    string // "bool" | "enum" | "text"
	Options []string
}

// propControls maps common SETTABLE ZFS properties to an edit control. Anything
// settable but unlisted falls back to a free-text field, so new/rare properties
// are still editable — just without a curated option list.
var propControls = map[string]PropControl{
	"atime":                {"bool", []string{"on", "off"}},
	"relatime":             {"bool", []string{"on", "off"}},
	"readonly":             {"bool", []string{"on", "off"}},
	"exec":                 {"bool", []string{"on", "off"}},
	"setuid":               {"bool", []string{"on", "off"}},
	"devices":              {"bool", []string{"on", "off"}},
	"nbmand":               {"bool", []string{"on", "off"}},
	"overlay":              {"bool", []string{"on", "off"}},
	"vscan":                {"bool", []string{"on", "off"}},
	"zoned":                {"bool", []string{"on", "off"}},
	"compression":          {"enum", []string{"off", "on", "lz4", "zstd", "zstd-fast", "gzip", "gzip-1", "gzip-9", "lzjb", "zle"}},
	"checksum":             {"enum", []string{"on", "off", "fletcher2", "fletcher4", "sha256", "sha512", "skein", "edonr", "blake3"}},
	"dedup":                {"enum", []string{"off", "on", "sha256", "sha512", "skein", "edonr", "blake3", "verify"}},
	"sync":                 {"enum", []string{"standard", "always", "disabled"}},
	"logbias":              {"enum", []string{"latency", "throughput"}},
	"primarycache":         {"enum", []string{"all", "none", "metadata"}},
	"secondarycache":       {"enum", []string{"all", "none", "metadata"}},
	"redundant_metadata":   {"enum", []string{"all", "most", "some", "none"}},
	"snapdir":              {"enum", []string{"hidden", "visible"}},
	"canmount":             {"enum", []string{"on", "off", "noauto"}},
	"acltype":              {"enum", []string{"off", "nfsv4", "posix"}},
	"aclmode":              {"enum", []string{"discard", "groupmask", "passthrough", "restricted"}},
	"aclinherit":           {"enum", []string{"discard", "noallow", "restricted", "passthrough", "passthrough-x"}},
	"xattr":                {"enum", []string{"on", "off", "sa", "dir"}},
	"volmode":              {"enum", []string{"default", "full", "geom", "dev", "none"}},
	"copies":               {"enum", []string{"1", "2", "3"}},
	"recordsize":           {"text", nil},
	"quota":                {"text", nil},
	"refquota":             {"text", nil},
	"reservation":          {"text", nil},
	"refreservation":       {"text", nil},
	"mountpoint":           {"text", nil},
	"sharenfs":             {"text", nil},
	"sharesmb":             {"text", nil},
	"special_small_blocks": {"text", nil},
}

// Prop is a dataset property for the editor.
type Prop struct {
	Name, Value, Source string
	Settable            bool // source != read-only ("-")
	Control             PropControl
}

// DatasetProps returns every property of a dataset (structured) for the editor,
// flagging which are user-settable and how to edit each.
func DatasetProps(h Host, dataset string) ([]Prop, error) {
	out, err := run(h.command("zfs", "get", "-H", "-o", "property,value,source", "all", dataset))
	if err != nil {
		return nil, err
	}
	var ps []Prop
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		ctrl, ok := propControls[f[0]]
		if !ok {
			ctrl = PropControl{Kind: "text"}
		}
		ps = append(ps, Prop{
			Name: f[0], Value: f[1], Source: f[2],
			Settable: f[2] != "-" && f[2] != "", // read-only props report "-"
			Control:  ctrl,
		})
	}
	return ps, nil
}

// zfsAdmin runs a privileged `zfs <args>`: locally via pkexec (a polkit prompt),
// remotely over ssh as the connected (delegated) user. Returns ZFS's own error
// text on failure so the GUI can show exactly what was rejected.
func zfsAdmin(h Host, args ...string) error {
	var cmd *exec.Cmd
	if h.SSH == "" {
		cmd = exec.Command("pkexec", append([]string{"zfs"}, args...)...)
	} else {
		cmd = h.command("zfs", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zfs %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetProp applies `zfs set prop=value dataset` (privileged; see zfsAdmin).
func SetProp(h Host, dataset, prop, value string) error {
	return zfsAdmin(h, "set", prop+"="+value, dataset)
}

// zfsAdminStdin is zfsAdmin with data fed on stdin — for ops that read a
// passphrase (load-key / change-key / create with keyformat=passphrase). The
// passphrase never touches the command line or disk.
func zfsAdminStdin(h Host, stdin string, args ...string) error {
	var cmd *exec.Cmd
	if h.SSH == "" {
		cmd = exec.Command("pkexec", append([]string{"zfs"}, args...)...)
	} else {
		cmd = h.command("zfs", args...)
	}
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zfs %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── encryption ───────────────────────────────────────────────────────────────

// LoadKey unlocks an encrypted dataset (zfs load-key), feeding the passphrase on
// stdin. UnloadKey re-locks it. ChangeKey rotates the passphrase. CreateEncrypted
// makes a new passphrase-encrypted dataset. All privileged.
func LoadKey(h Host, dataset, passphrase string) error {
	return zfsAdminStdin(h, passphrase+"\n", "load-key", dataset)
}
func UnloadKey(h Host, dataset string) error { return zfsAdmin(h, "unload-key", dataset) }
func ChangeKey(h Host, dataset, newPassphrase string) error {
	return zfsAdminStdin(h, newPassphrase+"\n"+newPassphrase+"\n", "change-key", dataset)
}
func CreateEncrypted(h Host, name, passphrase string) error {
	return zfsAdminStdin(h, passphrase+"\n"+passphrase+"\n",
		"create", "-o", "encryption=on", "-o", "keyformat=passphrase", "-o", "keylocation=prompt", name)
}

// ── pool operations ──────────────────────────────────────────────────────────

// zpoolAdmin runs a privileged `zpool <args>` (pkexec local / ssh remote).
func zpoolAdmin(h Host, args ...string) error {
	var cmd *exec.Cmd
	if h.SSH == "" {
		cmd = exec.Command("pkexec", append([]string{"zpool"}, args...)...)
	} else {
		cmd = h.command("zpool", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zpool %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ScrubPool starts (or with start=false, stops) a scrub. TrimPool trims free
// space; ClearPool clears device errors. PoolStatusText returns `zpool status`.
func ScrubPool(h Host, pool string, start bool) error {
	if start {
		return zpoolAdmin(h, "scrub", pool)
	}
	return zpoolAdmin(h, "scrub", "-s", pool)
}
func TrimPool(h Host, pool string) error  { return zpoolAdmin(h, "trim", pool) }
func ClearPool(h Host, pool string) error { return zpoolAdmin(h, "clear", pool) }
func PoolStatusText(h Host, pool string) (string, error) {
	return run(h.command("zpool", "status", pool))
}

// adminExec runs an arbitrary privileged command (not just zfs) — pkexec locally,
// ssh (delegated) remotely. Used for host tools like kldload's `kbe`.
func adminExec(h Host, argv ...string) error {
	var cmd *exec.Cmd
	if h.SSH == "" {
		cmd = exec.Command("pkexec", argv...)
	} else {
		cmd = h.command(argv[0], argv[1:]...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DestroyDataset destroys a dataset recursively. The caller MUST confirm first.
func DestroyDataset(h Host, dataset string) error { return zfsAdmin(h, "destroy", "-r", dataset) }

// CreateDataset creates a filesystem, or a zvol when volsize != "" (e.g. "10G").
func CreateDataset(h Host, name, volsize string) error {
	if volsize != "" {
		return zfsAdmin(h, "create", "-V", volsize, name)
	}
	return zfsAdmin(h, "create", "-p", name)
}

// RenameDataset renames a dataset (zfs rename).
func RenameDataset(h Host, oldName, newName string) error {
	return zfsAdmin(h, "rename", oldName, newName)
}

// SetMounted mounts or unmounts a dataset.
func SetMounted(h Host, dataset string, mount bool) error {
	if mount {
		return zfsAdmin(h, "mount", dataset)
	}
	return zfsAdmin(h, "unmount", dataset)
}

// BootEnv is one boot-environment restore point — a snapshot of the active boot
// dataset (the pool's bootfs).
type BootEnv struct {
	Snapshot string
	Created  string
	Used     string
}

// bootDataset returns the active boot filesystem from the pool's bootfs (e.g.
// rpool/ROOT/onyx). We DERIVE it rather than hardcode rpool/ROOT/default — the
// BE dataset name varies per install (that hardcode is the kbe/krecovery bug).
// Empty when no bootfs is set (not a boot-environment system).
func bootDataset(h Host) string {
	out, err := run(h.command("zpool", "get", "-H", "-o", "value", "bootfs", "rpool"))
	if err != nil {
		return ""
	}
	if v := strings.TrimSpace(out); v != "" && v != "-" {
		return v
	}
	return ""
}

// ListBootEnvs lists boot environments — snapshots of the active boot dataset —
// newest last, and returns the boot dataset itself. Errors clearly on a system
// with no bootfs (non-kldload).
func ListBootEnvs(h Host) ([]BootEnv, string, error) {
	bd := bootDataset(h)
	if bd == "" {
		return nil, "", fmt.Errorf("no bootfs set on rpool — not a boot-environment system")
	}
	out, err := run(h.command("zfs", "list", "-H", "-t", "snapshot",
		"-o", "name,used,creation", "-s", "creation", bd))
	if err != nil {
		return nil, bd, err
	}
	var bes []BootEnv
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		bes = append(bes, BootEnv{Snapshot: f[0], Used: f[1], Created: f[2]})
	}
	return bes, bd, nil
}

// Boot-environment actions (privileged) — direct zfs against the DERIVED boot
// dataset, so they're correct regardless of the BE name. CreateBootEnv snapshots
// the running boot dataset; delete/rollback take a full boot-dataset@name snap.
// (Rollback of the LIVE boot dataset takes effect on reboot; warn the operator.)
func CreateBootEnv(h Host, name string) error {
	bd := bootDataset(h)
	if bd == "" {
		return fmt.Errorf("no bootfs set — not a boot-environment system")
	}
	return zfsAdmin(h, "snapshot", bd+"@"+name)
}
func DeleteBootEnv(h Host, snap string) error   { return zfsAdmin(h, "destroy", snap) }
func RollbackBootEnv(h Host, snap string) error { return zfsAdmin(h, "rollback", "-r", snap) }

// RunReplicate executes a send|recv pipeline (from ReplicatePipeline). The local
// leg needs root to read/write the pool, so the pipe runs under pkexec; any
// remote leg carries its own ssh (root can still read the user's key file).
func RunReplicate(pipeline string) error {
	out, err := exec.Command("pkexec", "sh", "-c", pipeline).CombinedOutput()
	if err != nil {
		return fmt.Errorf("replicate: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Snapshot actions (all privileged). Rollback uses -r so rolling back past newer
// snapshots works — the GUI WARNS first, because -r DESTROYS those newer
// snapshots (and any clones/bookmarks of them). hold/release use a fixed tag.
func Rollback(h Host, snapshot string) error        { return zfsAdmin(h, "rollback", "-r", snapshot) }
func Clone(h Host, snapshot, target string) error   { return zfsAdmin(h, "clone", snapshot, target) }
func HoldSnap(h Host, snapshot string) error        { return zfsAdmin(h, "hold", "zxplore", snapshot) }
func ReleaseSnap(h Host, snapshot string) error     { return zfsAdmin(h, "release", "zxplore", snapshot) }
func DestroySnapshot(h Host, snapshot string) error { return zfsAdmin(h, "destroy", snapshot) }

// PoolsOverview renders a compact machine overview: every imported pool with its
// vitals (health, alloc/free/size, capacity, fragmentation, dedup) and a scrub +
// errors line. Pinned at the top of the GUI so the whole box reads at a glance;
// multiple pools stack. Best-effort — a failing zpool call degrades to a one-line
// note rather than blanking the header.
func PoolsOverview(h Host) string {
	out, err := run(h.command("zpool", "list", "-H", "-o",
		"name,health,size,alloc,free,capacity,fragmentation,dedupratio"))
	if err != nil {
		return "  (cannot list pools: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "  (no pools imported)"
	}
	var b strings.Builder
	for _, line := range lines {
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			continue
		}
		mark := "✓"
		if f[1] != "ONLINE" {
			mark = "⚠"
		}
		fmt.Fprintf(&b, "%s  %-16s %-9s  %6s used · %6s free of %-6s  ·  %4s cap · %3s frag · %s dedup\n",
			mark, f[0], f[1], f[3], f[4], f[2], f[5], f[6], f[7])
		if st, e := run(h.command("zpool", "status", f[0])); e == nil {
			for _, sl := range strings.Split(st, "\n") {
				t := strings.TrimSpace(sl)
				switch {
				case strings.HasPrefix(t, "scan:"):
					fmt.Fprintf(&b, "     scan: %s\n", strings.TrimSpace(strings.TrimPrefix(t, "scan:")))
				case strings.HasPrefix(t, "errors:"):
					fmt.Fprintf(&b, "     %s\n", t)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
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

// ListSubtree lists a dataset and all its descendants — used for a scoped
// target pane so a DELEGATED user (who can't `zfs list` the whole pool, only
// their allowed dataset) still sees the target. Snapshot counts are skipped
// (a delegated user often can't enumerate all snapshots).
func ListSubtree(h Host, path string) ([]Dataset, error) {
	out, err := run(h.command("zfs", "list", "-H", "-p", "-o", "name,used,refer",
		"-t", "filesystem,volume", "-r", path))
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
	return rows, nil
}

// shellQuote single-quotes s for safe inclusion in an `sh -c` pipeline.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sshPrefix(h Host) string {
	s := "ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=8"
	for _, o := range h.sshOpts() {
		s += " " + shellQuote(o)
	}
	return s + " " + shellQuote(h.SSH)
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

// IsKldload reports whether we're on a kldload system, so zxplore can light up
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
