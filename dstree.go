// dstree.go — the Browser's left panel as a TREE instead of a flat list: what
// each dataset IS, which of them are structure rather than storage, and the
// rows that come out of that.
//
// Why this exists: a kldload install lays out 28 datasets on a box with two
// pools, and the flat list gave rpool/var/cache the same visual weight as the
// pool itself. Ten of those 28 rows cannot be opened at all — six containers
// that are never mounted and exist only so children inherit properties, and
// four zvols that have no files — and nothing in the view said so. The
// operator's words were "is there a way to categorize it better or more easily
// identify pools and explore into them" (2026-09-12).
//
// Everything here classifies from what ZFS reports (type, canmount, mounted,
// mountpoint), never from the NAME. Name-sniffing would have called
// rpool/kldload "system" and a user's pool called "var" a container, and it
// would have been wrong on the first box that did not look like fiend.
package main

import (
	"fmt"
	"strings"
)

// dsKind is what a row is, for the purpose of deciding how to draw it and
// whether there is anything behind it to open.
type dsKind int

const (
	// dsContainer has no files of its own: canmount=off, or mountpoint=none.
	// rpool, rpool/ROOT, rpool/usr, rpool/var, rpool/var/lib and
	// rpool/kldload are all this on a default install — pure namespace.
	dsContainer  dsKind = iota
	dsFilesystem        // mounted, has files
	dsUnmounted         // canmount=on and NOT mounted: usually a problem
	dsBootEnv           // <pool>/ROOT/<name>
	dsVolume            // a zvol: a block device, no files to browse
)

// isBEPath reports whether name is <pool>/ROOT/<one segment> — the layout
// ZFSBootMenu and every root-on-ZFS guide use for boot environments. Depth is
// part of the test: rpool/ROOT/fiend is a BE, rpool/ROOT/fiend/var is not.
func isBEPath(name string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 3 && parts[1] == "ROOT"
}

// classify reads the four facts ZFS gives us. Order matters: a zvol has no
// canmount at all, and a BE carries canmount=noauto, which would otherwise
// read as "not mounted" for every BE that is not the running one.
func classify(d Dataset) dsKind {
	if d.Type == "volume" {
		return dsVolume
	}
	if isBEPath(d.Name) {
		return dsBootEnv
	}
	if d.CanMount == "off" || d.Mountpoint == "none" || d.Mountpoint == "-" {
		return dsContainer
	}
	if d.Mounted {
		return dsFilesystem
	}
	// An unmounted filesystem with a real mountpoint. Worth saying out loud:
	// on a kldload box this is how a dataset that failed to mount looks, and
	// the flat list rendered it identically to a healthy one.
	return dsUnmounted
}

// TreeRow is one drawn line. It carries the dataset plus where it sits, so the
// GUI layer does no walking of its own.
type TreeRow struct {
	DS       Dataset
	Depth    int // 0 = a pool root
	Leaf     string
	Kind     dsKind
	Active   bool // the BE currently mounted at /
	Kids     int  // direct children in the list
	Expanded bool
	Flat     bool // a filter result: full name, no indent, no marker
}

func parentOf(name string) (string, bool) {
	i := strings.LastIndexByte(name, '/')
	if i <= 0 {
		return "", false
	}
	return name[:i], true
}

func leafOf(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// BuildTree turns a flat dataset list into drawn rows, depth-first, skipping
// the children of anything in collapsed.
//
// A row whose parent is NOT in the list becomes a root. That is the delegated
// case, not an error: ListSubtree hands back rpool/vms and its children with no
// rpool row, and a user with `zfs allow` on one dataset sees exactly that.
// Input order is preserved per level rather than re-sorted, because zfs list
// already sorts and its order is the one the operator sees everywhere else.
func BuildTree(rows []Dataset, collapsed map[string]bool) []TreeRow {
	present := make(map[string]bool, len(rows))
	for _, d := range rows {
		present[d.Name] = true
	}
	kids := map[string][]int{}
	var roots []int
	for i, d := range rows {
		if p, ok := parentOf(d.Name); ok && present[p] {
			kids[p] = append(kids[p], i)
		} else {
			roots = append(roots, i)
		}
	}
	out := make([]TreeRow, 0, len(rows))
	var walk func(idx []int, depth int)
	walk = func(idx []int, depth int) {
		for _, i := range idx {
			d := rows[i]
			k := classify(d)
			r := TreeRow{
				DS: d, Depth: depth, Leaf: leafOf(d.Name), Kind: k,
				Active:   k == dsBootEnv && d.Mounted && d.Mountpoint == "/",
				Kids:     len(kids[d.Name]),
				Expanded: !collapsed[d.Name],
			}
			out = append(out, r)
			if r.Kids > 0 && r.Expanded {
				walk(kids[d.Name], depth+1)
			}
		}
	}
	walk(roots, 0)
	return out
}

// DefaultCollapsed folds EVERYTHING that has children, pool roots included, so
// the Browser opens on one line per pool and nothing else: "ideally collapsed
// to just the root pools[,] by default on onyx you should see 2 line[s] zroot
// and tank" (2026-09-12). On onyx that is rpool and tank out of 60-odd rows.
//
// This rule went out, came back, and then went further in one afternoon, and
// the round trip is the lesson. Folded first; the operator could not see the
// goldens, because rpool/vms holds them and the only way to unfold was a key
// binding that died the moment a mouse touched the list — so folded meant
// GONE. I unfolded, which hid the problem instead of fixing it. Once a
// double-click folds and focus survives a click, folding all the way down is
// not just safe, it is the better start.
//
// The rule a default view has to obey: only hide what the operator can get
// back in one gesture.
func DefaultCollapsed(rows []Dataset) map[string]bool {
	hasKids := map[string]bool{}
	present := map[string]bool{}
	for _, d := range rows {
		present[d.Name] = true
	}
	for _, d := range rows {
		if p, ok := parentOf(d.Name); ok && present[p] {
			hasKids[p] = true
		}
	}
	out := map[string]bool{}
	for _, d := range rows {
		if hasKids[d.Name] {
			out[d.Name] = true
		}
	}
	return out
}

// FlatRows is the filtered view: one row per match, full name, no indent. A
// filtered tree is mostly scaffolding holding up two hits, which is why the
// filter flattens instead.
func FlatRows(rows []Dataset) []TreeRow {
	out := make([]TreeRow, 0, len(rows))
	for _, d := range rows {
		out = append(out, TreeRow{DS: d, Leaf: d.Name, Kind: classify(d), Depth: 0, Flat: true,
			Active: classify(d) == dsBootEnv && d.Mounted && d.Mountpoint == "/"})
	}
	return out
}

// Openable reports whether selecting this row can show files. Containers and
// zvols cannot, which is the question the flat list never answered.
func (r TreeRow) Openable() bool { return r.Kind == dsFilesystem || r.Kind == dsBootEnv }

// marker is the expand/collapse glyph, or spaces to keep the columns straight.
func (r TreeRow) marker() string {
	switch {
	case r.Flat:
		return ""
	case r.Kids == 0 && r.Depth == 0:
		// An empty pool with a blank marker reads as a CHILD of the pool above
		// it -- void looked like it lived inside rpool. A pool keeps its own
		// glyph so the eye sees a root.
		return "· "
	case r.Kids == 0:
		return "  "
	case r.Expanded:
		return "▾ "
	default:
		return "▸ "
	}
}

// Badge names the row's kind in the one column an operator scans. A pool root
// says POOL regardless of its canmount, because that is the fact that matters
// at depth 0.
func (r TreeRow) Badge() string {
	if r.Depth == 0 && !strings.Contains(r.DS.Name, "/") {
		return "POOL"
	}
	switch r.Kind {
	case dsVolume:
		return "ZVOL"
	case dsBootEnv:
		if r.Active {
			return "BE ●"
		}
		return "BE"
	case dsContainer:
		return "container"
	case dsUnmounted:
		return "NOT MOUNTED"
	default:
		// A filesystem mounted where its name says it should be needs no
		// badge: blank means ordinary, and the pane is 480px wide at 1920 and
		// half that on a laptop, so a column of "/var/log"-shaped noise costs
		// more than it tells. A SURPRISING mountpoint is worth the space --
		// rpool/kldload/playbooks lands on /var/lib/kldload/user-playbooks,
		// and that is exactly the thing you want to see without selecting it.
		if r.DS.Mountpoint == conventionalMount(r.DS.Name) {
			return ""
		}
		return "→" + tailFit(r.DS.Mountpoint, treeBadgeWidth-1)
	}
}

// conventionalMount is where a dataset named pool/a/b mounts if nobody has
// overridden it: /a/b. Comparing against it is how Badge decides whether a
// mountpoint is news.
func conventionalMount(name string) string {
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return "/" + name
	}
	return name[i:]
}

// tailFit keeps the END of a path, because that is the part that identifies it.
func tailFit(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return "…" + string(r[len(r)-width+1:])
}

// Column widths. The browser list is 25% of the window -- 480px at 1920, ~340
// on a 1366 laptop, which is about 40 monospace characters. So the row has to
// fit in ~43, and that budget is why there is one size column and not two: the
// dossier on the right has refer, quota, compressratio and the rest.
const (
	treeNameWidth  = 22
	treeBadgeWidth = 12
	treeFlatWidth  = 32 // the filter view: no indent and no badge, so a name fits
)

// Line is the whole row as drawn. Kept here rather than in the GUI so the
// layout can be tested without a window.
func (r TreeRow) Line() string {
	if r.Flat {
		// A filter result is about the NAME, so it gets the whole width and the
		// badge column goes away. Truncation keeps the TAIL: the filter matched
		// something, and "…be-20260912-084232" identifies a row where
		// "rpool/ROOT/smoketest…" does not.
		snaps := ""
		if r.DS.Snaps > 0 {
			snaps = fmt.Sprintf("  ×%d", r.DS.Snaps)
		}
		return fmt.Sprintf("%-*s %7s%s", treeFlatWidth,
			tailFit(r.DS.Name, treeFlatWidth), r.DS.Used, snaps)
	}
	left := strings.Repeat("  ", r.Depth) + r.marker() + r.Leaf
	// The hidden-child count belongs next to the NAME, not in the badge: as a
	// badge suffix it pushed "container (2)" past a 12-wide column and the
	// truncation ate the front of the word ("…ntainer (2)").
	if r.Kids > 0 && !r.Expanded {
		left += fmt.Sprintf(" (%d)", r.Kids)
	}
	if n := []rune(left); len(n) > treeNameWidth {
		left = string(n[:treeNameWidth-1]) + "…"
	}
	badge := r.Badge()
	snaps := ""
	if r.DS.Snaps > 0 {
		snaps = fmt.Sprintf("  ×%d", r.DS.Snaps)
	}
	return fmt.Sprintf("%-*s %-*s %7s%s",
		treeNameWidth, left, treeBadgeWidth, badge, r.DS.Used, snaps)
}
