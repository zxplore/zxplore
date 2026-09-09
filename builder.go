// builder.go — the pool Builder's engine: what disks a host has, what a pool
// design is, what that design yields, and the exact `zpool create` it becomes.
//
// The Builder is the tab for the day a box gets new disks: see the shelf, pick
// a topology, read the usable/raw/fault-tolerance numbers BEFORE anything is
// written, then run it as a dry run and finally for real. It also draws an
// existing pool's vdev tree in the same visual language, so "what did I build
// last year" and "what am I about to build" look the same.
//
// Everything in this file is plain Go over `lsblk` and `zpool`: no GUI, so the
// math and the argv are unit-tested against fixtures, and the CLI form
// (`zxplore --builder …`) and the GUI tab share one implementation.
//
// Sources of truth:
//   - lsblk -J -b … for the shelf (Linux). FreeBSD has no lsblk; ListDisks says
//     so loudly rather than pretending the shelf is empty.
//   - `zpool status -P <pool>` for an existing pool's topology.
//   - `zpool create -n` for the dry run — ZFS's own opinion of the layout.
//
// Usable-capacity math is the textbook figure (parity subtracted, smallest
// member sets the size). RAIDZ additionally loses a few percent to padding
// and metadata, so the numbers are shown with ≈ and the dry run is the truth.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Disk is one whole block device as the Builder sees it.
type Disk struct {
	Name   string // sda, nvme0n1
	Path   string // /dev/sda
	ByID   string // /dev/disk/by-id/… — what the pool is built on when known
	Model  string
	Serial string
	Tran   string // sata, nvme, usb, sas, … ("" for virtual)
	Bytes  int64
	Rota   bool
	Kind   string // nvme | ssd | hdd | usb | virtual
	InUse  string // "" when free; otherwise why the Builder will not offer it
	pseudo bool   // zd*/loop*/… — hidden unless ZXPLORE_BUILDER_ALL_DEVICES=1
}

// Size renders Bytes the way a drive is sold (decimal), e.g. "20 TB".
func (d Disk) Size() string { return fmtBytesDec(d.Bytes) }

// fmtBytesDec formats a byte count in decimal units, one decimal below 10.
func fmtBytesDec(b int64) string {
	units := []struct {
		unit string
		div  float64
	}{{"PB", 1e15}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}}
	for _, u := range units {
		if float64(b) >= u.div {
			v := float64(b) / u.div
			if v < 10 {
				return fmt.Sprintf("%.1f %s", v, u.unit)
			}
			return fmt.Sprintf("%.0f %s", v, u.unit)
		}
	}
	return fmt.Sprintf("%d B", b)
}

// lsblkBool tolerates both encodings lsblk has used for booleans: real JSON
// true/false (util-linux ≥ 2.37) and the strings "1"/"0" before that (EL8).
type lsblkBool bool

func (b *lsblkBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = lsblkBool(s == "true" || s == "1")
	return nil
}

type lsblkDev struct {
	Name       string      `json:"name"`
	Path       string      `json:"path"`
	Size       json.Number `json:"size"`
	Model      *string     `json:"model"`
	Serial     *string     `json:"serial"`
	Rota       lsblkBool   `json:"rota"`
	Tran       *string     `json:"tran"`
	Type       string      `json:"type"`
	Fstype     *string     `json:"fstype"`
	Mountpoint *string     `json:"mountpoint"`
	Label      *string     `json:"label"`
	IDLink     *string     `json:"id-link"`
	Children   []lsblkDev  `json:"children"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// lsblkColumns is the column set the shelf needs. ID-LINK (the udev by-id
// name) arrived in util-linux 2.39; older hosts reject it, so ListDisks
// retries without it and resolves by-id links itself.
const lsblkColumns = "NAME,PATH,SIZE,MODEL,SERIAL,ROTA,TRAN,TYPE,FSTYPE,MOUNTPOINT,LABEL"

// pseudoPrefixes are block devices that are never a pool member in real life:
// ZFS volumes (zd), loop files, ramdisks, optical, floppy, device-mapper and
// md arrays. A lab that WANTS to build on loop devices or zvols sets
// ZXPLORE_BUILDER_ALL_DEVICES=1 — that is also how the live test works.
var pseudoPrefixes = []string{"zd", "loop", "ram", "sr", "fd", "dm-", "md", "nbd", "zram"}

func isPseudo(name string) bool {
	for _, p := range pseudoPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// diskKind classifies a device for the shelf. USB first: a USB SSD is still
// the wrong thing to put in a pool, and the badge should say why.
func diskKind(name, tran string, rota bool, model string) string {
	switch {
	case tran == "usb":
		return "usb"
	case strings.HasPrefix(name, "nvme") || tran == "nvme":
		return "nvme"
	case strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "xvd") ||
		(tran == "" && model == "") || isPseudo(name):
		return "virtual"
	case rota:
		return "hdd"
	default:
		return "ssd"
	}
}

// inUseReason says why a disk is not free, from what lsblk shows on it and
// its partitions. An empty string means free: no filesystem signature, no
// partition carrying one, nothing mounted.
func inUseReason(d lsblkDev) string {
	if fs := str(d.Fstype); fs != "" {
		if fs == "zfs_member" {
			if l := str(d.Label); l != "" {
				return "zfs member of " + l
			}
			return "zfs member"
		}
		return fs + " on the whole disk"
	}
	var reasons []string
	seen := map[string]bool{}
	for _, c := range d.Children {
		if mp := str(c.Mountpoint); mp != "" {
			reasons = append(reasons, "mounted at "+mp)
			continue
		}
		fs := str(c.Fstype)
		if fs == "" {
			fs = "partition"
		}
		if fs == "zfs_member" {
			if l := str(c.Label); l != "" {
				fs = "zfs member of " + l
			}
		}
		if !seen[fs] {
			seen[fs] = true
			reasons = append(reasons, fs)
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	return strings.Join(reasons, ", ")
}

// parseLsblk turns `lsblk -J` output into Disks (whole disks only).
func parseLsblk(out string) ([]Disk, error) {
	var top struct {
		Devices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		return nil, fmt.Errorf("lsblk output is not the JSON I asked for: %v", err)
	}
	var disks []Disk
	for _, d := range top.Devices {
		// lsblk types a loop device "loop", not "disk"; it is still a whole
		// block device a lab can build on (ZXPLORE_BUILDER_ALL_DEVICES=1).
		if d.Type != "disk" && d.Type != "loop" {
			continue
		}
		bytes, _ := d.Size.Int64()
		disk := Disk{
			Name:   d.Name,
			Path:   str(&d.Path),
			Model:  str(d.Model),
			Serial: str(d.Serial),
			Tran:   str(d.Tran),
			Bytes:  bytes,
			Rota:   bool(d.Rota),
			InUse:  inUseReason(d),
			pseudo: isPseudo(d.Name),
		}
		if disk.Path == "" {
			disk.Path = "/dev/" + d.Name
		}
		if id := str(d.IDLink); id != "" {
			disk.ByID = "/dev/disk/by-id/" + id
		}
		disk.Kind = diskKind(disk.Name, disk.Tran, disk.Rota, disk.Model)
		disks = append(disks, disk)
	}
	return disks, nil
}

// parseByIDListing maps device names to their preferred /dev/disk/by-id link
// from `ls -l /dev/disk/by-id`. Preference: wwn-, then the bus-named link
// (ata-/scsi-/nvme-<model>), never the eui/nvme-nvme forms and never a
// -partN link. The by-id path is what a pool should be built on: sdX names
// move between boots, by-id does not.
func parseByIDListing(out string) map[string]string {
	best := map[string]string{}
	rank := func(link string) int {
		switch {
		case strings.Contains(link, "-part"):
			return 0
		case strings.HasPrefix(link, "wwn-"):
			return 3
		case strings.HasPrefix(link, "nvme-eui."), strings.HasPrefix(link, "nvme-nvme."):
			return 1
		case strings.HasPrefix(link, "ata-"), strings.HasPrefix(link, "scsi-"), strings.HasPrefix(link, "nvme-"):
			return 2
		}
		return 1
	}
	for _, ln := range strings.Split(out, "\n") {
		i := strings.Index(ln, " -> ")
		if i < 0 {
			continue
		}
		fields := strings.Fields(ln[:i])
		if len(fields) == 0 {
			continue
		}
		link := fields[len(fields)-1]
		target := strings.TrimSpace(ln[i+4:])
		name := target[strings.LastIndex(target, "/")+1:]
		if r := rank(link); r > 0 {
			if cur, ok := best[name]; !ok || rank(cur) < r {
				best[name] = link
			}
		}
	}
	return best
}

// ListDisks is the shelf: every whole disk on the host, in-use ones marked.
// Pseudo devices (zvols, loops) are hidden unless ZXPLORE_BUILDER_ALL_DEVICES=1.
func ListDisks(h Host) ([]Disk, error) {
	out, err := run(h.command("lsblk", "-J", "-b", "-o", lsblkColumns+",ID-LINK"))
	if err != nil {
		// util-linux < 2.39 has no ID-LINK column; the by-id links are
		// resolved from the directory listing below instead.
		if out2, err2 := run(h.command("lsblk", "-J", "-b", "-o", lsblkColumns)); err2 == nil {
			out = out2
		} else {
			return nil, fmt.Errorf("lsblk: %v (FreeBSD hosts have no lsblk — the Builder shelf is Linux-only for now)", err2)
		}
	}
	disks, err := parseLsblk(out)
	if err != nil {
		return nil, err
	}
	needIDs := false
	for _, d := range disks {
		if d.ByID == "" && !d.pseudo {
			needIDs = true
		}
	}
	if needIDs {
		// No error handling on purpose: a host without /dev/disk/by-id (a
		// container, a minimal initramfs) builds on /dev/sdX, which zpool
		// accepts; the argv just carries the weaker name.
		if listing, err := run(h.command("ls", "-l", "/dev/disk/by-id")); err == nil {
			ids := parseByIDListing(listing)
			for i := range disks {
				if disks[i].ByID == "" {
					if id, ok := ids[disks[i].Name]; ok {
						disks[i].ByID = "/dev/disk/by-id/" + id
					}
				}
			}
		}
	}
	if os.Getenv("ZXPLORE_BUILDER_ALL_DEVICES") != "1" {
		kept := disks[:0]
		for _, d := range disks {
			if !d.pseudo {
				kept = append(kept, d)
			}
		}
		disks = kept
	}
	sort.SliceStable(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks, nil
}

// ─── the design ─────────────────────────────────────────────────────────────

// Vdev is one virtual device in a design. Kind is the zpool word (stripe means
// no word at all — bare disks). Role is where it goes in the create line:
// data vdevs first, then log / cache / spare / special / dedup sections.
type Vdev struct {
	Kind        string // stripe | mirror | raidz1 | raidz2 | raidz3 | draid1 | draid2 | draid3
	Role        string // data | log | cache | spare | special | dedup
	Disks       []Disk
	DraidData   int // draid only: data disks per redundancy group
	DraidSpares int // draid only: distributed spares
}

// Design is a pool that does not exist yet.
type Design struct {
	Name        string
	Ashift      int    // 0 = let zpool decide
	Compression string // "" = pool default
	Props       []string
	Vdevs       []Vdev
}

var vdevKinds = []string{"stripe", "mirror", "raidz1", "raidz2", "raidz3", "draid1", "draid2", "draid3"}
var vdevRoles = []string{"data", "log", "cache", "spare", "special", "dedup"}

// parity is how many members a vdev can lose and keep its data.
func (v Vdev) parity() int {
	switch v.Kind {
	case "mirror":
		if len(v.Disks) == 0 {
			return 0
		}
		return len(v.Disks) - 1
	case "raidz1", "draid1":
		return 1
	case "raidz2", "draid2":
		return 2
	case "raidz3", "draid3":
		return 3
	}
	return 0
}

func (v Vdev) minBytes() int64 {
	var m int64
	for i, d := range v.Disks {
		if i == 0 || d.Bytes < m {
			m = d.Bytes
		}
	}
	return m
}

func (v Vdev) rawBytes() int64 {
	var s int64
	for _, d := range v.Disks {
		s += d.Bytes
	}
	return s
}

// Usable is the textbook capacity of one vdev: parity subtracted, the smallest
// member setting the stripe size. dRAID: the spares are capacity held back
// for rebuilds, then the group ratio applies to what is left.
func (v Vdev) Usable() int64 {
	n := int64(len(v.Disks))
	if n == 0 {
		return 0
	}
	m := v.minBytes()
	switch v.Kind {
	case "stripe":
		return v.rawBytes()
	case "mirror":
		return m
	case "raidz1", "raidz2", "raidz3":
		p := int64(v.parity())
		if n <= p {
			return 0
		}
		return (n - p) * m
	default: // draid
		p := int64(v.parity())
		d := int64(v.DraidData)
		if d <= 0 || n-int64(v.DraidSpares) <= 0 {
			return 0
		}
		return (n - int64(v.DraidSpares)) * m * d / (d + p)
	}
}

// Label is the vdev's name in the layout: "RAIDZ2 · 6 disks · 4 data + 2 parity".
func (v Vdev) Label() string {
	n := len(v.Disks)
	switch v.Kind {
	case "stripe":
		if n == 1 {
			return "single disk · no redundancy"
		}
		return fmt.Sprintf("stripe · %d disks · no redundancy", n)
	case "mirror":
		return fmt.Sprintf("mirror · %d-way", n)
	case "raidz1", "raidz2", "raidz3":
		p := v.parity()
		return fmt.Sprintf("%s · %d disks · %d data + %d parity", strings.ToUpper(v.Kind), n, n-p, p)
	default:
		return fmt.Sprintf("dRAID%d · %d disks · %d data + %d parity per group · %d spare", v.parity(), n, v.DraidData, v.parity(), v.DraidSpares)
	}
}

// draidSpec is the zpool vdev word for a dRAID: draid2:4d:12c:1s.
func (v Vdev) draidSpec() string {
	return fmt.Sprintf("draid%d:%dd:%dc:%ds", v.parity(), v.DraidData, len(v.Disks), v.DraidSpares)
}

func (v Vdev) isDraid() bool { return strings.HasPrefix(v.Kind, "draid") }

// dataVdevs are the vdevs that hold the pool's data (role data).
func (d Design) dataVdevs() []Vdev {
	var out []Vdev
	for _, v := range d.Vdevs {
		if v.Role == "data" || v.Role == "" {
			out = append(out, v)
		}
	}
	return out
}

// Usable is the design's textbook usable capacity across data vdevs.
func (d Design) Usable() int64 {
	var s int64
	for _, v := range d.dataVdevs() {
		s += v.Usable()
	}
	return s
}

// Raw is the sum of every data disk's size.
func (d Design) Raw() int64 {
	var s int64
	for _, v := range d.dataVdevs() {
		s += v.rawBytes()
	}
	return s
}

// FaultTolerance is how many disks the pool survives losing in the WORST
// case (the weakest data vdev), with the sentence the summary shows.
func (d Design) FaultTolerance() (int, string) {
	vd := d.dataVdevs()
	if len(vd) == 0 {
		return 0, "no data vdevs yet"
	}
	worst := -1
	for _, v := range vd {
		p := v.parity()
		if worst < 0 || p < worst {
			worst = p
		}
	}
	switch {
	case worst == 0:
		return 0, "none — losing any one disk destroys the pool"
	case len(vd) == 1:
		return worst, fmt.Sprintf("any %d disk(s)", worst)
	default:
		return worst, fmt.Sprintf("%d per vdev (the weakest vdev decides)", worst)
	}
}

var poolNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)

// Validate says what stops the design from becoming a zpool create. Hard
// errors only; Warnings carries the judgement calls.
func (d Design) Validate() error {
	switch {
	case d.Name == "":
		return fmt.Errorf("the pool needs a name")
	case !poolNameRE.MatchString(d.Name):
		return fmt.Errorf("%q is not a valid pool name (letters, digits, _ . : - and it must start with a letter)", d.Name)
	case d.Name == "mirror" || d.Name == "raidz" || d.Name == "draid" || d.Name == "spare" || d.Name == "log":
		return fmt.Errorf("%q is a zpool keyword, not a pool name", d.Name)
	case regexp.MustCompile(`^c[0-9]`).MatchString(d.Name):
		return fmt.Errorf("%q looks like a device name; zpool refuses names starting with c<digit>", d.Name)
	}
	if len(d.dataVdevs()) == 0 {
		return fmt.Errorf("a pool needs at least one data vdev")
	}
	seen := map[string]int{}
	for i, v := range d.Vdevs {
		if len(v.Disks) == 0 {
			return fmt.Errorf("vdev %d has no disks", i)
		}
		if v.Role == "" {
			v.Role = "data"
		}
		min := map[string]int{"stripe": 1, "mirror": 2, "raidz1": 2, "raidz2": 3, "raidz3": 4, "draid1": 2, "draid2": 3, "draid3": 4}[v.Kind]
		if min == 0 {
			return fmt.Errorf("vdev %d: unknown kind %q", i, v.Kind)
		}
		if len(v.Disks) < min {
			return fmt.Errorf("vdev %d: %s needs at least %d disks, has %d", i, v.Kind, min, len(v.Disks))
		}
		knownRole := false
		for _, r := range vdevRoles {
			if v.Role == r {
				knownRole = true
			}
		}
		if !knownRole {
			return fmt.Errorf("vdev %d: unknown role %q", i, v.Role)
		}
		switch v.Role {
		case "log":
			if v.Kind != "stripe" && v.Kind != "mirror" {
				return fmt.Errorf("vdev %d: a log vdev is a single disk or a mirror, not %s", i, v.Kind)
			}
		case "cache", "spare":
			if v.Kind != "stripe" {
				return fmt.Errorf("vdev %d: %s devices are listed bare, not as %s", i, v.Role, v.Kind)
			}
		}
		if v.isDraid() {
			p := v.parity()
			n := len(v.Disks)
			if v.DraidData < 1 {
				return fmt.Errorf("vdev %d: dRAID needs a data-disks-per-group count", i)
			}
			if n-v.DraidSpares < v.DraidData+p {
				return fmt.Errorf("vdev %d: dRAID needs at least %d children after %d spare(s)", i, v.DraidData+p, v.DraidSpares)
			}
			if (n-v.DraidSpares)%(v.DraidData+p) != 0 {
				return fmt.Errorf("vdev %d: dRAID with %d children and %d spare(s) cannot be cut into groups of %d data + %d parity", i, n, v.DraidSpares, v.DraidData, p)
			}
		}
		for _, disk := range v.Disks {
			if disk.InUse != "" {
				return fmt.Errorf("%s is in use (%s)", disk.Name, disk.InUse)
			}
			if j, dup := seen[disk.Path]; dup {
				return fmt.Errorf("%s appears in vdev %d and vdev %d", disk.Name, j, i)
			}
			seen[disk.Path] = i
		}
	}
	return nil
}

// Warnings are the things an experienced operator would say out loud before
// hitting Create. None of them block the build.
func (d Design) Warnings() []string {
	var w []string
	for i, v := range d.Vdevs {
		n := len(v.Disks)
		if n == 0 {
			continue
		}
		minB, maxB := v.minBytes(), int64(0)
		usb, rota, kinds := 0, 0, map[string]bool{}
		for _, disk := range v.Disks {
			if disk.Bytes > maxB {
				maxB = disk.Bytes
			}
			if disk.Kind == "usb" {
				usb++
			}
			if disk.Rota {
				rota++
			}
			kinds[disk.Kind] = true
		}
		tag := fmt.Sprintf("vdev %d", i)
		if v.Kind != "stripe" && minB > 0 && maxB > minB*11/10 {
			w = append(w, fmt.Sprintf("%s mixes sizes (%s … %s): every member is treated as %s, the rest is wasted", tag, fmtBytesDec(minB), fmtBytesDec(maxB), fmtBytesDec(minB)))
		}
		if len(kinds) > 1 && v.Role != "cache" {
			w = append(w, tag+" mixes drive types; the vdev runs at the speed of its slowest member")
		}
		if usb > 0 {
			w = append(w, fmt.Sprintf("%s has %d USB disk(s): USB bridges drop, reset and lie about flushes — fine for a lab, not for data you want back", tag, usb))
		}
		switch v.Role {
		case "data", "":
			if v.Kind == "stripe" {
				w = append(w, tag+" is a stripe: one failed disk takes the whole pool with it")
			}
			if v.Kind == "raidz1" && (n >= 6 || minB >= 8e12) {
				w = append(w, tag+": RAIDZ1 this wide or with disks this large means a long resilver with no parity left — RAIDZ2 is the usual answer")
			}
			if strings.HasPrefix(v.Kind, "raidz") && n > 12 {
				w = append(w, fmt.Sprintf("%s is %d wide: past ~12 disks a RAIDZ vdev resilvers slowly and every I/O touches every disk — split it into two vdevs", tag, n))
			}
		case "log":
			if n == 1 {
				w = append(w, "a single log device: if it dies, the sync writes in flight die with it — mirror the SLOG")
			}
			if rota > 0 {
				w = append(w, "a rotational log device is slower than writing the ZIL to the pool itself")
			}
		case "cache":
			if rota > 0 {
				w = append(w, "a rotational cache device is pointless: L2ARC has to be faster than the pool")
			}
		case "special", "dedup":
			if v.Kind == "stripe" {
				w = append(w, "an unmirrored "+v.Role+" vdev: losing it loses the POOL, not just metadata — mirror it")
			}
			if rota > 0 {
				w = append(w, "a rotational "+v.Role+" vdev defeats its purpose; use SSD or NVMe")
			}
		}
	}
	return w
}

// devRef is the name a disk goes into the create line under: by-id when known
// (stable across reboots), the kernel name otherwise.
func devRef(d Disk) string {
	if d.ByID != "" {
		return d.ByID
	}
	return d.Path
}

// Argv is the exact zpool create the design becomes. Data vdevs first, then
// the role sections in the order zpool documents them. Shown to the operator
// before it runs, logged after — the same primitives the rest of zxplore uses.
func (d Design) Argv() []string {
	argv := []string{"zpool", "create"}
	if d.Ashift > 0 {
		argv = append(argv, "-o", fmt.Sprintf("ashift=%d", d.Ashift))
	}
	if d.Compression != "" {
		argv = append(argv, "-O", "compression="+d.Compression)
	}
	for _, p := range d.Props {
		argv = append(argv, "-O", p)
	}
	argv = append(argv, d.Name)
	emit := func(v Vdev) {
		switch {
		case v.isDraid():
			argv = append(argv, v.draidSpec())
		case v.Kind != "stripe":
			argv = append(argv, v.Kind)
		}
		for _, disk := range v.Disks {
			argv = append(argv, devRef(disk))
		}
	}
	for _, v := range d.dataVdevs() {
		emit(v)
	}
	for _, role := range []string{"special", "dedup", "log", "cache", "spare"} {
		first := true
		for _, v := range d.Vdevs {
			if v.Role != role {
				continue
			}
			if first {
				argv = append(argv, role)
				first = false
			}
			emit(v)
		}
	}
	return argv
}

// Command is Argv as one shell line, quoted where a path needs it.
func (d Design) Command() string {
	words := make([]string, 0, len(d.Argv()))
	for _, w := range d.Argv() {
		if strings.ContainsAny(w, " \t'\"$`\\") {
			w = shellQuote(w)
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// runOutMaybeElevated is runMaybeElevated for commands whose OUTPUT matters
// (the dry run): unprivileged first, pkexec only on a permission failure.
func runOutMaybeElevated(h Host, argv ...string) (string, error) {
	auditLog(h, argv)
	if h.SSH != "" {
		out, err := h.command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
		}
		return string(out), nil
	}
	out, err := localCmd(argv[0], argv[1:]...).CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	if !needsElevation(string(out)) {
		return string(out), fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	out2, err2 := localCmd("pkexec", argv...).CombinedOutput()
	if err2 != nil {
		return string(out2), fmt.Errorf("%s (elevated): %v: %s", strings.Join(argv, " "), err2, strings.TrimSpace(string(out2)))
	}
	return string(out2), nil
}

// DryRun asks zpool what it would build (`zpool create -n`) and returns its
// answer verbatim. This is the check that catches what the math cannot: a
// disk zpool sees as busy, a label it refuses, an ashift it will not honour.
func DryRun(h Host, d Design) (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	argv := d.Argv()
	argv = append([]string{argv[0], argv[1], "-n"}, argv[2:]...)
	return runOutMaybeElevated(h, argv...)
}

// CreatePool builds the design for real. Privileged (pkexec / delegated ssh).
func CreatePool(h Host, d Design) error {
	if err := d.Validate(); err != nil {
		return err
	}
	argv := d.Argv()
	return zpoolAdmin(h, argv[1:]...)
}

// ─── candidates ─────────────────────────────────────────────────────────────

// Candidate is one topology the Builder proposes for a set of disks, with the
// badge and the one-line reason the card shows.
type Candidate struct {
	Label  string
	Badge  string // RECOMMENDED | HIGH IOPS | LOW REDUNDANCY | MAX REDUNDANCY | FAST RESILVER | NO REDUNDANCY
	Why    string
	Design Design
}

// split cuts n disks into k vdevs as evenly as possible.
func split(disks []Disk, k int) [][]Disk {
	var out [][]Disk
	n := len(disks)
	for i := 0; i < k; i++ {
		lo, hi := i*n/k, (i+1)*n/k
		out = append(out, disks[lo:hi])
	}
	return out
}

// draidLayout finds a data-per-group count that divides the children after
// spares, preferring 8..4 data disks per group and one spare.
func draidLayout(n, parity int) (data, spares int, ok bool) {
	for _, s := range []int{1, 2, 0} {
		for d := 8; d >= 2; d-- {
			if n-s >= d+parity && (n-s)%(d+parity) == 0 {
				return d, s, true
			}
		}
	}
	return 0, 0, false
}

// Suggest proposes topologies for the chosen data disks. Fast disks (NVMe /
// SSD) among `aux` — the ones NOT chosen for data — become the log and cache
// proposals when the data disks are rotational, which is when they help.
func Suggest(name string, data []Disk, aux []Disk) []Candidate {
	data = append([]Disk(nil), data...)
	sort.SliceStable(data, func(i, j int) bool { return data[i].Bytes > data[j].Bytes })
	n := len(data)
	if n == 0 {
		return nil
	}
	rotational := false
	for _, d := range data {
		if d.Rota {
			rotational = true
		}
	}
	var fast []Disk
	for _, d := range aux {
		if d.InUse == "" && (d.Kind == "nvme" || d.Kind == "ssd") {
			fast = append(fast, d)
		}
	}
	sort.SliceStable(fast, func(i, j int) bool { return fast[i].Bytes < fast[j].Bytes })
	extras := func() []Vdev {
		if !rotational || len(fast) == 0 {
			return nil
		}
		var v []Vdev
		switch {
		case len(fast) >= 2:
			v = append(v, Vdev{Kind: "mirror", Role: "log", Disks: fast[:2]})
			if len(fast) > 2 {
				v = append(v, Vdev{Kind: "stripe", Role: "cache", Disks: fast[2:]})
			}
		default:
			v = append(v, Vdev{Kind: "stripe", Role: "log", Disks: fast[:1]})
		}
		return v
	}
	mk := func(label, badge, why string, vdevs []Vdev) Candidate {
		d := Design{Name: name, Ashift: 12, Compression: "zstd", Vdevs: append(vdevs, extras()...)}
		return Candidate{Label: label, Badge: badge, Why: why, Design: d}
	}
	var out []Candidate
	big := data[0].Bytes >= 8e12

	// Mirrors: pairs; an odd disk out becomes a hot spare.
	if n >= 2 {
		var vd []Vdev
		for i := 0; i+1 < n; i += 2 {
			vd = append(vd, Vdev{Kind: "mirror", Role: "data", Disks: data[i : i+2]})
		}
		why := fmt.Sprintf("%d mirror vdevs — random I/O scales with vdev count, resilvers are a plain copy", n/2)
		if n%2 == 1 {
			vd = append(vd, Vdev{Kind: "stripe", Role: "spare", Disks: data[n-1 : n]})
			why += "; the odd disk is a hot spare"
		}
		badge := "HIGH IOPS"
		if n <= 4 {
			badge = "RECOMMENDED"
		}
		out = append(out, mk(fmt.Sprintf("Mirror ×%d", n/2), badge, why, vd))
	}
	// RAIDZ1: 3–8 wide per vdev.
	if n >= 3 {
		k := (n + 7) / 8
		var vd []Vdev
		for _, g := range split(data, k) {
			vd = append(vd, Vdev{Kind: "raidz1", Role: "data", Disks: g})
		}
		badge, why := "RECOMMENDED", "one parity disk per vdev — the most usable space with any protection at all"
		if n >= 6 || big {
			badge, why = "LOW REDUNDANCY", "one parity disk: a second failure during a long resilver loses everything — see RAIDZ2"
		}
		out = append(out, mk(vdevTitle("RAIDZ1", k), badge, why, vd))
	}
	// RAIDZ2: 4+ disks, ≤ 12 wide per vdev.
	if n >= 4 {
		k := (n + 11) / 12
		var vd []Vdev
		for _, g := range split(data, k) {
			vd = append(vd, Vdev{Kind: "raidz2", Role: "data", Disks: g})
		}
		badge := "RECOMMENDED"
		if n < 6 {
			badge = "MAX REDUNDANCY"
		}
		out = append(out, mk(vdevTitle("RAIDZ2", k), badge, "two parity disks per vdev — survives a failure during a resilver; the default for large disks", vd))
	}
	// RAIDZ3: 6+ disks.
	if n >= 6 {
		k := (n + 14) / 15
		var vd []Vdev
		for _, g := range split(data, k) {
			vd = append(vd, Vdev{Kind: "raidz3", Role: "data", Disks: g})
		}
		out = append(out, mk(vdevTitle("RAIDZ3", k), "MAX REDUNDANCY", "three parity disks per vdev — for arrays nobody will touch for years", vd))
	}
	// dRAID2: 8+ disks, when a layout divides.
	if n >= 8 {
		if dd, sp, ok := draidLayout(n, 2); ok {
			vd := []Vdev{{Kind: "draid2", Role: "data", Disks: data, DraidData: dd, DraidSpares: sp}}
			badge := "FAST RESILVER"
			if n >= 12 {
				badge = "RECOMMENDED"
			}
			out = append(out, mk("dRAID2", badge, fmt.Sprintf("distributed RAIDZ2 with %d spare(s) built in — a rebuild uses every disk, so a big array is unprotected for minutes, not days", sp), vd))
		}
	}
	// Stripe: always possible, never a good idea for data.
	out = append(out, mk("Stripe", "NO REDUNDANCY", "every byte usable, every disk a single point of failure — scratch space only", []Vdev{{Kind: "stripe", Role: "data", Disks: data}}))

	order := map[string]int{"RECOMMENDED": 0, "HIGH IOPS": 1, "FAST RESILVER": 2, "MAX REDUNDANCY": 3, "LOW REDUNDANCY": 4, "NO REDUNDANCY": 5}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Badge] < order[out[j].Badge] })
	return out
}

func vdevTitle(kind string, k int) string {
	if k == 1 {
		return kind
	}
	return fmt.Sprintf("%s ×%d", kind, k)
}

// ─── an existing pool, drawn the same way ───────────────────────────────────

// TopoNode is one line of `zpool status` config: the pool, a vdev, a device.
type TopoNode struct {
	Name     string
	State    string
	Read     string
	Write    string
	Cksum    string
	Note     string // trailing text zpool adds (e.g. "(resilvering)")
	Children []*TopoNode
}

// Kind classifies a node by its zpool name: "mirror", "raidz2", "draid2",
// "logs", "cache", "spares", "special", "dedup", "pool" or "disk".
func (n *TopoNode) Kind() string {
	switch {
	case strings.HasPrefix(n.Name, "mirror-"):
		return "mirror"
	case strings.HasPrefix(n.Name, "raidz1-"):
		return "raidz1"
	case strings.HasPrefix(n.Name, "raidz2-"):
		return "raidz2"
	case strings.HasPrefix(n.Name, "raidz3-"):
		return "raidz3"
	case strings.HasPrefix(n.Name, "draid"):
		return strings.SplitN(n.Name, ":", 2)[0]
	case n.Name == "logs" || n.Name == "cache" || n.Name == "spares" || n.Name == "special" || n.Name == "dedup":
		return n.Name
	case n.Name == "replacing" || strings.HasPrefix(n.Name, "replacing-") || strings.HasPrefix(n.Name, "spare-"):
		return "replacing"
	case strings.HasPrefix(n.Name, "/") || strings.Contains(n.Name, "ONLINE"):
		return "disk"
	}
	return "disk"
}

func isTopoSection(name string) bool {
	switch name {
	case "logs", "cache", "spares", "special", "dedup":
		return true
	}
	return false
}

// ParsePoolStatus reads the config block of `zpool status` into a tree. The
// tree is indentation: zpool prints the pool at one tab, each level two
// spaces deeper. Anything before "config:" and after the blank line that ends
// the block is ignored.
func ParsePoolStatus(status string) (*TopoNode, error) {
	lines := strings.Split(status, "\n")
	in := false
	var root *TopoNode
	stack := []*TopoNode{}
	depths := []int{}
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "config:") {
			in = true
			continue
		}
		if !in {
			continue
		}
		t := strings.TrimLeft(ln, "\t")
		if strings.TrimSpace(t) == "" {
			if root != nil {
				break
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(t), "NAME") {
			continue
		}
		depth := len(t) - len(strings.TrimLeft(t, " "))
		f := strings.Fields(t)
		if len(f) == 0 {
			continue
		}
		n := &TopoNode{Name: f[0]}
		if len(f) > 1 {
			n.State = f[1]
		}
		if len(f) > 4 {
			n.Read, n.Write, n.Cksum = f[2], f[3], f[4]
		}
		if len(f) > 5 {
			n.Note = strings.Join(f[5:], " ")
		}
		// The role sections — logs, cache, spares, special, dedup — print at
		// the POOL's indentation, not under it; they are still the pool's
		// children, so they hang off the root rather than starting a new tree.
		if root != nil && depth == 0 && isTopoSection(n.Name) {
			stack, depths = stack[:1], depths[:1]
			root.Children = append(root.Children, n)
			stack = append(stack, n)
			depths = append(depths, depth)
			continue
		}
		for len(stack) > 0 && depths[len(depths)-1] >= depth {
			stack = stack[:len(stack)-1]
			depths = depths[:len(depths)-1]
		}
		if len(stack) == 0 {
			if root != nil {
				break // a second pool's block — one pool per call
			}
			root = n
		} else {
			p := stack[len(stack)-1]
			p.Children = append(p.Children, n)
		}
		stack = append(stack, n)
		depths = append(depths, depth)
	}
	if root == nil {
		return nil, fmt.Errorf("no config block in zpool status output")
	}
	return root, nil
}

// PoolTopology is the vdev tree of an imported pool, device paths as given
// (-P: full paths, so the by-id names the pool was built on show up).
func PoolTopology(h Host, pool string) (*TopoNode, error) {
	out, err := run(h.command("zpool", "status", "-P", pool))
	if err != nil {
		return nil, err
	}
	return ParsePoolStatus(out)
}

// Flatten renders a topology as indented text — the CLI and TUI form.
func (n *TopoNode) Flatten() string {
	var b strings.Builder
	var walk func(x *TopoNode, depth int)
	walk = func(x *TopoNode, depth int) {
		fmt.Fprintf(&b, "%s%-40s %s", strings.Repeat("  ", depth), x.Name, x.State)
		if x.Read != "" {
			fmt.Fprintf(&b, "  r/w/c %s/%s/%s", x.Read, x.Write, x.Cksum)
		}
		if x.Note != "" {
			b.WriteString("  " + x.Note)
		}
		b.WriteString("\n")
		for _, c := range x.Children {
			walk(c, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

// ─── the terminal form ──────────────────────────────────────────────────────

// builderCLI is `zxplore --builder <cmd>` — the same engine from a shell:
//
//	disks                    the shelf, one line per disk
//	suggest NAME DISK…       candidates for those disks, each with its create line
//	topology POOL            an imported pool's vdev tree
//	dry-run NAME SPEC        zpool create -n for a spec (see below)
//	create NAME SPEC         build it
//
// SPEC is the zpool vdev grammar with disk names: `raidz2 sda sdb sdc sdd log
// mirror nvme0n1 nvme1n1 cache sde` — what the GUI's preview line shows, minus
// the "zpool create" prefix and with bare names allowed.
func builderCLI(args []string) int {
	h := LocalHost()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: zxplore --builder {disks|suggest NAME DISK…|topology POOL|dry-run NAME SPEC…|create NAME SPEC…}")
		return 2
	}
	switch args[0] {
	case "disks":
		disks, err := ListDisks(h)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		for _, d := range disks {
			use := "free"
			if d.InUse != "" {
				use = "in use: " + d.InUse
			}
			fmt.Printf("%-12s %-8s %8s  %-28s %s\n", d.Name, d.Kind, d.Size(), d.Model, use)
		}
		return 0
	case "suggest":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --builder suggest NAME DISK…")
			return 2
		}
		disks, err := ListDisks(h)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		data, aux, err := pickDisks(disks, args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		for _, c := range Suggest(args[1], data, aux) {
			_, ft := c.Design.FaultTolerance()
			fmt.Printf("%-14s %-15s usable ≈ %-8s raw %-8s survives %s\n  %s\n  %s\n", c.Label, c.Badge, fmtBytesDec(c.Design.Usable()), fmtBytesDec(c.Design.Raw()), ft, c.Why, c.Design.Command())
			for _, w := range c.Design.Warnings() {
				fmt.Println("  ! " + w)
			}
		}
		return 0
	case "topology":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --builder topology POOL")
			return 2
		}
		t, err := PoolTopology(h, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		fmt.Print(t.Flatten())
		return 0
	case "dry-run", "create":
		if len(args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: zxplore --builder %s NAME SPEC…\n", args[0])
			return 2
		}
		disks, err := ListDisks(h)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		d, err := ParseSpec(args[1], args[2:], disks)
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		for _, w := range d.Warnings() {
			fmt.Fprintln(os.Stderr, "warning: "+w)
		}
		if args[0] == "dry-run" {
			out, err := DryRun(h, d)
			fmt.Print(out)
			if err != nil {
				fmt.Fprintln(os.Stderr, "zxplore:", err)
				return 1
			}
			return 0
		}
		fmt.Println(d.Command())
		if err := CreatePool(h, d); err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		fmt.Printf("pool %s created\n", d.Name)
		return 0
	}
	fmt.Fprintf(os.Stderr, "zxplore: unknown builder command %q\n", args[0])
	return 2
}

// pickDisks resolves names (sda, /dev/sda, a by-id path) against the shelf,
// returning the chosen disks and the rest (the aux set for Suggest).
func pickDisks(shelf []Disk, names []string) (chosen, rest []Disk, err error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	found := map[string]bool{}
	for _, d := range shelf {
		hit := false
		for _, key := range []string{d.Name, d.Path, d.ByID} {
			if key != "" && want[key] {
				hit, found[key] = true, true
			}
		}
		if hit {
			chosen = append(chosen, d)
		} else {
			rest = append(rest, d)
		}
	}
	for _, n := range names {
		if !found[n] {
			return nil, nil, fmt.Errorf("no such disk on the shelf: %s (see `zxplore --builder disks`)", n)
		}
	}
	return chosen, rest, nil
}

// ParseSpec reads the zpool vdev grammar into a Design: kinds start a vdev,
// role words start a section, everything else is a disk from the shelf.
// dRAID takes the zpool form draid2:4d:12c:1s or bare draid2 (auto layout).
func ParseSpec(name string, words []string, shelf []Disk) (Design, error) {
	d := Design{Name: name, Ashift: 12, Compression: "zstd"}
	role := "data"
	var cur *Vdev
	flush := func() {
		if cur != nil && len(cur.Disks) > 0 {
			d.Vdevs = append(d.Vdevs, *cur)
		}
		cur = nil
	}
	isKind := func(w string) bool {
		for _, k := range vdevKinds {
			if w == k || strings.HasPrefix(w, "draid") {
				return true
			}
		}
		return false
	}
	for _, w := range words {
		switch {
		case w == "log" || w == "cache" || w == "spare" || w == "special" || w == "dedup":
			flush()
			role = w
		case isKind(w):
			flush()
			cur = &Vdev{Kind: w, Role: role}
			if strings.HasPrefix(w, "draid") {
				spec := strings.Split(w, ":")
				cur.Kind = spec[0]
				if cur.Kind == "draid" {
					cur.Kind = "draid1"
				}
				for _, part := range spec[1:] {
					v, _ := strconv.Atoi(strings.TrimRight(part, "dcs"))
					switch part[len(part)-1] {
					case 'd':
						cur.DraidData = v
					case 's':
						cur.DraidSpares = v
					}
				}
			}
		default:
			chosen, _, err := pickDisks(shelf, []string{w})
			if err != nil {
				return d, err
			}
			if cur == nil {
				cur = &Vdev{Kind: "stripe", Role: role}
			}
			cur.Disks = append(cur.Disks, chosen[0])
			// A bare-disk section (cache, spare, a stripe of data) keeps
			// collecting; a mirror/raidz keeps collecting too — zpool's
			// grammar is the same: the next keyword ends the vdev.
		}
	}
	flush()
	for i := range d.Vdevs {
		v := &d.Vdevs[i]
		if v.isDraid() && v.DraidData == 0 {
			if dd, sp, ok := draidLayout(len(v.Disks), v.parity()); ok {
				v.DraidData, v.DraidSpares = dd, sp
			}
		}
	}
	return d, nil
}
