// dstree_test.go — the Browser tree against a REAL dataset list: fiend's, as
// `zfs list` printed it on 2026-09-12. A default kldload install is the case
// that matters, and the numbers in the assertions below are that install's.
package main

import (
	"strings"
	"testing"
)

// fiendList is `zfs list -H -p -o name,used,refer,type,canmount,mounted,mountpoint`
// taken off fiend 2026-09-12 -- a real default kldload install, two pools.
var fiendList = strings.Join([]string{
	"rpool\t27863281664\t98304\tfilesystem\toff\tno\tnone",
	"rpool/ROOT\t15597948928\t98304\tfilesystem\toff\tno\tnone",
	"rpool/ROOT/fiend\t15597842432\t15033696256\tfilesystem\tnoauto\tyes\t/",
	"rpool/ROOT/smoketest-be-20260912-084232\t8192\t15033679872\tfilesystem\tnoauto\tno\t/",
	"rpool/home\t2232320\t98304\tfilesystem\ton\tyes\t/home",
	"rpool/home/admin\t2134016\t1970176\tfilesystem\ton\tyes\t/home/admin",
	"rpool/kldload\t2904064\t98304\tfilesystem\toff\tno\tnone",
	"rpool/kldload/playbooks\t98304\t98304\tfilesystem\ton\tyes\t/var/lib/kldload/user-playbooks",
	"rpool/kldload/secrets\t196608\t196608\tfilesystem\ton\tyes\t/var/lib/kldload/secrets",
	"rpool/kldload/state\t2510848\t2510848\tfilesystem\ton\tyes\t/var/lib/kldload",
	"rpool/opt\t276668416\t276660224\tfilesystem\ton\tyes\t/opt",
	"rpool/root\t3826475008\t1753088\tfilesystem\ton\tyes\t/root",
	"rpool/srv\t311296\t245760\tfilesystem\ton\tyes\t/srv",
	"rpool/tmp\t339968\t266240\tfilesystem\ton\tyes\t/tmp",
	"rpool/usr\t2285195264\t98304\tfilesystem\toff\tno\t/usr",
	"rpool/usr/local\t2285096960\t2259607552\tfilesystem\ton\tyes\t/usr/local",
	"rpool/var\t211591168\t98304\tfilesystem\toff\tno\t/var",
	"rpool/var/cache\t207753216\t118202368\tfilesystem\ton\tyes\t/var/cache",
	"rpool/var/lib\t98304\t98304\tfilesystem\toff\tno\t/var/lib",
	"rpool/var/log\t3272704\t2772992\tfilesystem\ton\tyes\t/var/log",
	"rpool/var/spool\t114688\t106496\tfilesystem\ton\tyes\t/var/spool",
	"rpool/var/tmp\t253952\t188416\tfilesystem\ton\tyes\t/var/tmp",
	"rpool/vms\t5645815808\t98304\tfilesystem\ton\tno\tnone",
	"rpool/vms/k8s-golden\t2493595648\t2493595648\tvolume\t-\t-\t-",
	"rpool/vms/kldload-cp\t695898112\t3066187776\tvolume\t-\t-\t-",
	"rpool/vms/kldload-cp-2\t1218977792\t3583926272\tvolume\t-\t-\t-",
	"rpool/vms/kldload-cp-3\t1237245952\t3600211968\tvolume\t-\t-\t-",
	"void\t938088\t174528\tfilesystem\ton\tyes\t/void",
}, "\n")

func TestClassifyFiendLayout(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	if len(rows) != 28 {
		t.Fatalf("fixture parsed to %d rows, want 28", len(rows))
	}
	want := map[string]dsKind{
		// canmount=off or mountpoint=none: namespace only, nothing to open.
		"rpool":            dsContainer,
		"rpool/ROOT":       dsContainer,
		"rpool/usr":        dsContainer,
		"rpool/var":        dsContainer,
		"rpool/var/lib":    dsContainer, // the one that keeps the package DB in the BE
		"rpool/kldload":    dsContainer,
		"rpool/vms":        dsContainer, // canmount=on but mountpoint=none
		"rpool/ROOT/fiend": dsBootEnv,
		"rpool/ROOT/smoketest-be-20260912-084232": dsBootEnv,
		"rpool/vms/k8s-golden":                    dsVolume,
		"rpool/vms/kldload-cp":                    dsVolume,
		"rpool/home":                              dsFilesystem,
		"rpool/home/admin":                        dsFilesystem,
		"rpool/usr/local":                         dsFilesystem,
		"rpool/var/log":                           dsFilesystem,
		"void":                                    dsFilesystem,
	}
	byName := map[string]Dataset{}
	for _, d := range rows {
		byName[d.Name] = d
	}
	for name, k := range want {
		d, ok := byName[name]
		if !ok {
			t.Errorf("%s is not in the fixture", name)
			continue
		}
		if got := classify(d); got != k {
			t.Errorf("classify(%s) = %d, want %d (type=%q canmount=%q mounted=%v mp=%q)",
				name, got, k, d.Type, d.CanMount, d.Mounted, d.Mountpoint)
		}
	}

	// Only the running BE is active, and it is the one mounted at /.
	tree := BuildTree(rows, nil)
	active := 0
	for _, r := range tree {
		if r.Active {
			active++
			if r.DS.Name != "rpool/ROOT/fiend" {
				t.Errorf("active BE is %s, want rpool/ROOT/fiend", r.DS.Name)
			}
		}
	}
	if active != 1 {
		t.Errorf("%d active boot environments, want exactly 1", active)
	}

	// The claim that drove the whole change: ten of the 28 rows cannot be opened.
	closed := 0
	for _, r := range tree {
		if !r.Openable() {
			closed++
		}
	}
	if closed != 11 {
		t.Errorf("%d rows with nothing behind them, want 11 (7 containers + 4 zvols)", closed)
	}
}

func TestBuildTreeDepthAndOrder(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	tree := BuildTree(rows, nil)
	if len(tree) != len(rows) {
		t.Fatalf("expanded tree has %d rows, want all %d", len(tree), len(rows))
	}
	depth := map[string]int{
		"rpool": 0, "void": 0,
		"rpool/ROOT": 1, "rpool/home": 1, "rpool/var": 1,
		"rpool/ROOT/fiend": 2, "rpool/home/admin": 2, "rpool/var/log": 2,
	}
	got := map[string]TreeRow{}
	for _, r := range tree {
		got[r.DS.Name] = r
	}
	for name, d := range depth {
		if got[name].Depth != d {
			t.Errorf("%s at depth %d, want %d", name, got[name].Depth, d)
		}
	}
	if got["rpool"].Kids != 10 {
		t.Errorf("rpool has %d direct children, want 10", got["rpool"].Kids)
	}
	if got["rpool/ROOT"].Kids != 2 {
		t.Errorf("rpool/ROOT has %d children, want 2", got["rpool/ROOT"].Kids)
	}
	if got["rpool/home/admin"].Kids != 0 {
		t.Errorf("a leaf reports %d children", got["rpool/home/admin"].Kids)
	}
	// Every child must be drawn after its parent, or the indent is a lie.
	seen := map[string]int{}
	for i, r := range tree {
		seen[r.DS.Name] = i
		if p, ok := parentOf(r.DS.Name); ok {
			if j, isChild := seen[p]; isChild && j > i {
				t.Errorf("%s drawn at %d, before its parent at %d", r.DS.Name, i, j)
			}
		}
	}
}

func TestDefaultCollapsedFoldsOnlyScaffolding(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	collapsed := DefaultCollapsed(rows)
	wantFolded := []string{"rpool/ROOT", "rpool/kldload", "rpool/usr", "rpool/var", "rpool/vms"}
	for _, n := range wantFolded {
		if !collapsed[n] {
			t.Errorf("%s should start folded: it is a container with children", n)
		}
	}
	if len(collapsed) != len(wantFolded) {
		t.Errorf("folded %d datasets (%v), want exactly %v", len(collapsed), collapsed, wantFolded)
	}
	// A pool is never folded, and neither is anything with files in it.
	for _, n := range []string{"rpool", "void", "rpool/home", "rpool/opt"} {
		if collapsed[n] {
			t.Errorf("%s must not start folded", n)
		}
	}
	tree := BuildTree(rows, collapsed)
	if len(tree) != 13 {
		t.Errorf("the default view is %d rows, want 13 (from 28)", len(tree))
		for _, r := range tree {
			t.Logf("  %s", r.Line())
		}
	}
	// Both pools, and every mounted filesystem outside a folded container, are
	// still on screen.
	shown := map[string]bool{}
	for _, r := range tree {
		shown[r.DS.Name] = true
	}
	for _, n := range []string{"rpool", "void", "rpool/home", "rpool/home/admin", "rpool/opt",
		"rpool/root", "rpool/srv", "rpool/tmp"} {
		if !shown[n] {
			t.Errorf("%s is hidden in the default view", n)
		}
	}
}

func TestTreeLineLayout(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	tree := BuildTree(rows, DefaultCollapsed(rows))
	for _, r := range tree {
		line := r.Line()
		if strings.Contains(line, "\t") {
			t.Errorf("a tab in %q would break the monospace columns", line)
		}
		// The badge column must start at the same offset on every row, or the
		// eye cannot scan it.
		if len([]rune(line)) < treeNameWidth {
			t.Errorf("row %q is shorter than the name column", line)
		}
	}
	byName := map[string]TreeRow{}
	for _, r := range tree {
		byName[r.DS.Name] = r
	}
	if b := byName["rpool"].Badge(); b != "POOL" {
		t.Errorf("rpool badge %q, want POOL", b)
	}
	if b := byName["void"].Badge(); b != "POOL" {
		t.Errorf("void badge %q, want POOL (it is a pool root, even though it is mounted)", b)
	}
	if b := byName["rpool/ROOT"].Badge(); b != "container" {
		t.Errorf("rpool/ROOT badge %q, want container", b)
	}
	// Every folded row must say how many children it hides: with the subtree
	// gone, that count is the only thing telling an operator to look inside.
	for n, kids := range map[string]string{
		"rpool/var": "(5)", "rpool/vms": "(4)", "rpool/ROOT": "(2)", "rpool/kldload": "(3)",
	} {
		if !strings.Contains(byName[n].Line(), kids) {
			t.Errorf("%s hides its children without saying so: %q", n, byName[n].Line())
		}
		if m := byName[n].marker(); m != "▸ " {
			t.Errorf("%s folded marker %q", n, m)
		}
	}
	if m := byName["rpool/home"].marker(); m != "▾ " {
		t.Errorf("expanded marker %q", m)
	}
	// And the goldens still classify as block devices once unfolded.
	full := map[string]TreeRow{}
	for _, r := range BuildTree(rows, nil) {
		full[r.DS.Name] = r
	}
	if b := full["rpool/vms/k8s-golden"].Badge(); b != "ZVOL" {
		t.Errorf("k8s-golden badge %q, want ZVOL", b)
	}
	if m := byName["rpool/opt"].marker(); m != "  " {
		t.Errorf("a leaf must have no marker, got %q", m)
	}
}

func TestFlatRowsForFilter(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	flat := FlatRows(rows)
	if len(flat) != len(rows) {
		t.Fatalf("flat view dropped rows: %d of %d", len(flat), len(rows))
	}
	for _, r := range flat {
		if r.Depth != 0 {
			t.Errorf("%s is indented in the flat view", r.DS.Name)
		}
		if m := r.marker(); m != "" {
			t.Errorf("%s wears a tree marker %q in the flat view", r.DS.Name, m)
		}
		// A name that fits must appear whole; a long one keeps its tail.
		if len([]rune(r.DS.Name)) <= treeFlatWidth && !strings.Contains(r.Line(), r.DS.Name) {
			t.Errorf("the flat view must show the FULL name: %q", r.Line())
		}
		if !strings.Contains(r.Line(), leafOf(r.DS.Name)) {
			t.Errorf("the flat view dropped the identifying leaf: %q", r.Line())
		}
	}
}

// TestTreeLooksRight prints the default view. Not an assertion -- this is how I
// get an eye on the spacing without a display, the same trick the Builder's
// screenshot test uses. `go test -tags gui -run TestTreeLooksRight -v`
func TestTreeLooksRight(t *testing.T) {
	rows := parseDatasetList(fiendList, "")
	for i := range rows {
		rows[i].Snaps = 0
	}
	rows[2].Snaps = 3 // the active BE, as snapshot counts arrive
	t.Log("default view:")
	for _, r := range BuildTree(rows, DefaultCollapsed(rows)) {
		t.Logf("|%s|", r.Line())
	}
	t.Log("fully expanded:")
	for _, r := range BuildTree(rows, nil) {
		t.Logf("|%s|", r.Line())
	}
}
