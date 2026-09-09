// observe.go — the Observe engine: one second of a pool's life, and what it
// means. The debugger half of zxplore: not a dashboard of counters but the
// sentences an experienced operator would say after reading them.
//
// Sources, all readable by a delegated user (no eBPF, no root on Linux;
// the kstats are world-readable):
//   - /proc/spl/kstat/zfs/{arcstats,zil,dmu_tx,zfetchstats,dbufstats} and the
//     per-pool txgs and objset-* files (FreeBSD: sysctl kstat.zfs.misc.*)
//   - `zpool iostat -Hpvly POOL 1 1` — per-vdev ops, bandwidth and the four
//     latency buckets (total, disk, sync queue, async queue), nanoseconds
//   - `zpool status -s` (slow I/O counters), `zpool get`, `zpool events -H`
//   - /proc/meminfo and /sys/module/zfs/parameters for the tunables the
//     verdicts compare against
//
// A sample is two kstat reads around one iostat interval, so every counter
// has a per-second rate. Judge() turns a sample into verdicts, each naming
// the number it read and the property or command that changes it.
//
// What this deliberately is not (yet): a per-process view. "Who is hitting
// the pool" by pid needs eBPF (Linux) or dtrace (FreeBSD); the dataset-level
// view here answers "which dataset" from objset kstats instead, which is
// most of the question most of the time.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kstatRoot is where the kstats live on Linux; tests point it at fixtures.
var kstatRoot = "/proc/spl/kstat/zfs"

// Kstat is one kstat file: numeric counters and the odd string field.
type Kstat struct {
	V map[string]int64
	S map[string]string
}

func (k Kstat) get(name string) int64 { return k.V[name] }

// parseKstat reads the Linux kstat text form: a numeric header line, a
// "name type data" line, then one field per line. Type 4/3 are counters,
// type 7 a string (objset's dataset_name).
func parseKstat(text string) Kstat {
	k := Kstat{V: map[string]int64{}, S: map[string]string{}}
	for i, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || (i == 0 && isHeaderLine(f)) || f[0] == "name" {
			continue
		}
		switch f[1] {
		case "7":
			k.S[f[0]] = strings.Join(f[2:], " ")
		default:
			if v, err := strconv.ParseInt(f[2], 10, 64); err == nil {
				k.V[f[0]] = v
			}
		}
	}
	return k
}

func isHeaderLine(f []string) bool {
	_, err := strconv.Atoi(f[0])
	return err == nil && strings.HasPrefix(f[2], "0x")
}

// parseSysctlKstat reads FreeBSD's form: kstat.zfs.misc.arcstats.hits: 123.
func parseSysctlKstat(text string) Kstat {
	k := Kstat{V: map[string]int64{}, S: map[string]string{}}
	for _, ln := range strings.Split(text, "\n") {
		i := strings.Index(ln, ": ")
		if i < 0 {
			continue
		}
		key := ln[:i]
		key = key[strings.LastIndex(key, ".")+1:]
		val := strings.TrimSpace(ln[i+2:])
		if v, err := strconv.ParseInt(val, 10, 64); err == nil {
			k.V[key] = v
		} else {
			k.S[key] = val
		}
	}
	return k
}

// readKstat fetches one kstat group ("arcstats", "rpool/txgs"): the Linux
// file locally or over ssh, else FreeBSD's sysctl for the global groups.
func readKstat(h Host, rel string) (Kstat, error) {
	var text string
	var err error
	if h.SSH == "" {
		var b []byte
		b, err = os.ReadFile(filepath.Join(kstatRoot, rel))
		text = string(b)
	} else {
		text, err = run(h.command("cat", "/proc/spl/kstat/zfs/"+rel))
	}
	if err == nil {
		return parseKstat(text), nil
	}
	if !strings.Contains(rel, "/") {
		if out, err2 := run(h.command("sysctl", "kstat.zfs.misc."+rel)); err2 == nil {
			return parseSysctlKstat(out), nil
		}
	}
	return Kstat{}, fmt.Errorf("kstat %s: %v", rel, err)
}

// Txg is one row of a pool's txgs kstat: how much was dirty and how long the
// sync took. Times in nanoseconds.
type Txg struct {
	Txg      int64
	State    string
	NDirty   int64
	NRead    int64
	NWritten int64
	Reads    int64
	Writes   int64
	OTime    int64 // open
	QTime    int64 // quiescing
	WTime    int64 // waiting for sync
	STime    int64 // syncing
}

func parseTxgs(text string) []Txg {
	var out []Txg
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 12 || f[0] == "txg" || isHeaderLine(f) {
			continue
		}
		n := func(i int) int64 { v, _ := strconv.ParseInt(f[i], 10, 64); return v }
		out = append(out, Txg{Txg: n(0), State: f[2], NDirty: n(3), NRead: n(4), NWritten: n(5), Reads: n(6), Writes: n(7), OTime: n(8), QTime: n(9), WTime: n(10), STime: n(11)})
	}
	return out
}

// VdevIO is one line of `zpool iostat -Hpvly`: an interval's ops, bandwidth
// and latencies (ns; -1 where zpool printed "-").
type VdevIO struct {
	Name                 string
	Alloc, Free          int64
	ROps, WOps, RBw, WBw int64
	TotalR, TotalW       int64
	DiskR, DiskW         int64
	SyncqR, SyncqW       int64
	AsyncqR, AsyncqW     int64
	Scrub, Trim, Rebuild int64
}

// Leaf says whether a line is a device rather than the pool or a vdev group.
func (v VdevIO) Leaf(pool string) bool {
	n := v.Name
	if n == pool || n == "logs" || n == "cache" || n == "spares" || n == "special" || n == "dedup" {
		return false
	}
	for _, p := range []string{"mirror-", "raidz1-", "raidz2-", "raidz3-", "draid", "replacing-", "spare-"} {
		if strings.HasPrefix(n, p) {
			return false
		}
	}
	return true
}

func parseIostat(text string) []VdevIO {
	var out []VdevIO
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(ln, "\r"), "\t")
		if len(f) < 15 || f[0] == "" {
			continue
		}
		n := func(i int) int64 {
			if i >= len(f) || f[i] == "-" {
				return -1
			}
			v, err := strconv.ParseInt(f[i], 10, 64)
			if err != nil {
				return -1
			}
			return v
		}
		out = append(out, VdevIO{Name: f[0], Alloc: n(1), Free: n(2), ROps: n(3), WOps: n(4), RBw: n(5), WBw: n(6),
			TotalR: n(7), TotalW: n(8), DiskR: n(9), DiskW: n(10), SyncqR: n(11), SyncqW: n(12), AsyncqR: n(13), AsyncqW: n(14),
			Scrub: n(15), Trim: n(16), Rebuild: n(17)})
	}
	return out
}

// DatasetIO is a dataset's objset kstat, as per-second rates over the sample.
type DatasetIO struct {
	Name                   string
	Reads, Writes          float64 // ops/s
	NRead, NWritten        float64 // bytes/s
	ZilCommits             float64 // sync commits/s
	ZilNormalBytes         float64 // sync-write bytes that landed on the main pool, /s
	ZilSlogBytes           float64 // sync-write bytes that landed on the SLOG, /s
	ZilStalls, ZilSuspends int64   // lifetime
}

// splitObjsets cuts the concatenated objset-* files at their header lines.
func splitObjsets(text string) []Kstat {
	var blocks []Kstat
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, parseKstat(strings.Join(cur, "\n")))
		}
		cur = nil
	}
	for _, ln := range strings.Split(text, "\n") {
		if f := strings.Fields(ln); len(f) >= 3 && isHeaderLine(f) {
			flush()
		}
		cur = append(cur, ln)
	}
	flush()
	return blocks
}

func readObjsets(h Host, pool string) (map[string]Kstat, error) {
	var text string
	if h.SSH == "" {
		ents, err := os.ReadDir(filepath.Join(kstatRoot, pool))
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "objset-") {
				if data, err := os.ReadFile(filepath.Join(kstatRoot, pool, e.Name())); err == nil {
					b.Write(data)
					b.WriteString("\n")
				}
			}
		}
		text = b.String()
	} else {
		// One round trip for sixty files: a fixed argv, the pool as $1.
		out, err := run(h.command("sh", "-c", `cat /proc/spl/kstat/zfs/"$1"/objset-*`, "_", pool))
		if err != nil {
			return nil, err
		}
		text = out
	}
	m := map[string]Kstat{}
	for _, k := range splitObjsets(text) {
		if name := k.S["dataset_name"]; name != "" {
			m[name] = k
		}
	}
	return m, nil
}

// Sample is one second of a pool, with rates already computed.
type Sample struct {
	At       time.Time
	Pool     string
	Seconds  float64 // the interval the rates are over
	Arc      Kstat
	Zil      Kstat
	DmuTx    Kstat
	Zfetch   Kstat
	Rate     map[string]float64 // per-second deltas of the counters the verdicts use
	Txgs     []Txg              // recent committed txgs, oldest first
	Vdevs    []VdevIO
	Datasets []DatasetIO
	Props    map[string]string // capacity fragmentation dedupratio ashift health
	Scan     string            // the "scan:" line of zpool status
	SlowIOs  map[string]int64  // leaf → SLOW column of zpool status -s
	HasLog   bool              // a log vdev exists
	MemTotal int64
	MemAvail int64
	Tunables map[string]int64 // zfs_arc_max zfs_dirty_data_max zfs_txg_timeout
	Events   []string         // recent zpool events, oldest first
	Warnings []string         // sources that could not be read
}

// rateKeys are the counters whose per-second delta the verdicts read.
var rateKeys = map[string][]string{
	"arc":    {"hits", "misses", "demand_data_hits", "demand_data_misses", "demand_metadata_hits", "demand_metadata_misses", "memory_throttle_count", "l2_hits", "l2_misses", "prefetch_data_hits", "prefetch_data_misses"},
	"zil":    {"zil_commit_count", "zil_itx_metaslab_normal_bytes", "zil_itx_metaslab_slog_bytes", "zil_itx_indirect_bytes"},
	"dmu_tx": {"dmu_tx_dirty_throttle", "dmu_tx_dirty_delay", "dmu_tx_memory_reclaim", "dmu_tx_assigned"},
	"zfetch": {"hits", "misses"},
}

type kstatSet struct {
	arc, zil, dmu, zfetch Kstat
	objsets               map[string]Kstat
	at                    time.Time
}

func readSet(h Host, pool string, warn *[]string) kstatSet {
	s := kstatSet{at: time.Now()}
	var err error
	if s.arc, err = readKstat(h, "arcstats"); err != nil {
		*warn = append(*warn, err.Error())
	}
	if s.zil, err = readKstat(h, "zil"); err != nil {
		*warn = append(*warn, err.Error())
	}
	if s.dmu, err = readKstat(h, "dmu_tx"); err != nil {
		*warn = append(*warn, err.Error())
	}
	if s.zfetch, err = readKstat(h, "zfetchstats"); err != nil {
		*warn = append(*warn, err.Error())
	}
	if s.objsets, err = readObjsets(h, pool); err != nil {
		*warn = append(*warn, "per-dataset counters: "+err.Error())
	}
	return s
}

// Observe takes one sample of pool: kstats, one iostat interval, kstats
// again, then the slow-moving context (status, props, memory, tunables,
// events). About 1.3 s. Sources that fail land in Warnings, never silently.
func Observe(h Host, pool string) (Sample, error) {
	if pool == "" {
		return Sample{}, fmt.Errorf("observe needs a pool name")
	}
	smp := Sample{Pool: pool, Rate: map[string]float64{}, Props: map[string]string{}, SlowIOs: map[string]int64{}, Tunables: map[string]int64{}}
	a := readSet(h, pool, &smp.Warnings)
	io, err := run(h.command("zpool", "iostat", "-Hpvly", pool, "1", "1"))
	if err != nil {
		return smp, fmt.Errorf("zpool iostat: %v", err)
	}
	smp.Vdevs = parseIostat(io)
	b := readSet(h, pool, &smp.Warnings)
	smp.At = b.at
	smp.Seconds = b.at.Sub(a.at).Seconds()
	if smp.Seconds <= 0 {
		smp.Seconds = 1
	}
	smp.Arc, smp.Zil, smp.DmuTx, smp.Zfetch = b.arc, b.zil, b.dmu, b.zfetch
	pairs := map[string][2]Kstat{"arc": {a.arc, b.arc}, "zil": {a.zil, b.zil}, "dmu_tx": {a.dmu, b.dmu}, "zfetch": {a.zfetch, b.zfetch}}
	for group, keys := range rateKeys {
		for _, k := range keys {
			smp.Rate[group+"."+k] = float64(pairs[group][1].get(k)-pairs[group][0].get(k)) / smp.Seconds
		}
	}
	for name, kb := range b.objsets {
		ka, ok := a.objsets[name]
		if !ok {
			continue
		}
		d := func(k string) float64 { return float64(kb.get(k)-ka.get(k)) / smp.Seconds }
		smp.Datasets = append(smp.Datasets, DatasetIO{Name: name, Reads: d("reads"), Writes: d("writes"), NRead: d("nread"), NWritten: d("nwritten"),
			ZilCommits: d("zil_commit_count"), ZilNormalBytes: d("zil_itx_metaslab_normal_bytes"), ZilSlogBytes: d("zil_itx_metaslab_slog_bytes"),
			ZilStalls: kb.get("zil_commit_stall_count"), ZilSuspends: kb.get("zil_commit_suspend_count")})
	}
	sort.Slice(smp.Datasets, func(i, j int) bool {
		return smp.Datasets[i].NRead+smp.Datasets[i].NWritten > smp.Datasets[j].NRead+smp.Datasets[j].NWritten
	})
	if txt, err := readRaw(h, pool+"/txgs"); err == nil {
		all := parseTxgs(txt)
		var done []Txg
		for _, t := range all {
			if t.State == "C" {
				done = append(done, t)
			}
		}
		if len(done) > 20 {
			done = done[len(done)-20:]
		}
		smp.Txgs = done
	} else {
		smp.Warnings = append(smp.Warnings, "txg history: "+err.Error())
	}
	if st, err := run(h.command("zpool", "status", "-s", "-P", pool)); err == nil {
		smp.Scan, smp.SlowIOs, smp.HasLog = parseStatusExtras(st)
	} else {
		smp.Warnings = append(smp.Warnings, "zpool status: "+err.Error())
	}
	if pr, err := run(h.command("zpool", "get", "-Hp", "-o", "property,value", "capacity,fragmentation,dedupratio,ashift,health,size,allocated", pool)); err == nil {
		for _, ln := range strings.Split(pr, "\n") {
			if f := strings.Fields(ln); len(f) == 2 {
				smp.Props[f[0]] = f[1]
			}
		}
	}
	if h.SSH == "" {
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			smp.MemTotal, smp.MemAvail = parseMeminfo(string(b))
		}
		for _, t := range []string{"zfs_arc_max", "zfs_dirty_data_max", "zfs_txg_timeout"} {
			if b, err := os.ReadFile("/sys/module/zfs/parameters/" + t); err == nil {
				v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
				smp.Tunables[t] = v
			}
		}
	} else {
		if out, err := run(h.command("sh", "-c", `cat /proc/meminfo; for t in zfs_arc_max zfs_dirty_data_max zfs_txg_timeout; do printf '%s %s\n' "$t" "$(cat /sys/module/zfs/parameters/$t 2>/dev/null)"; done`)); err == nil {
			smp.MemTotal, smp.MemAvail = parseMeminfo(out)
			for _, ln := range strings.Split(out, "\n") {
				if f := strings.Fields(ln); len(f) == 2 && strings.HasPrefix(f[0], "zfs_") {
					v, _ := strconv.ParseInt(f[1], 10, 64)
					smp.Tunables[f[0]] = v
				}
			}
		}
	}
	ev, err := run(h.command("zpool", "events", "-H"))
	if err != nil {
		smp.Warnings = append(smp.Warnings, "zpool events: "+strings.TrimSpace(err.Error())+" (usually needs root; the rest of the sample does not)")
	} else {
		lines := strings.Split(strings.TrimSpace(ev), "\n")
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		for _, ln := range lines {
			if strings.TrimSpace(ln) != "" {
				smp.Events = append(smp.Events, strings.Join(strings.Fields(ln), " "))
			}
		}
	}
	return smp, nil
}

// EventsElevated reads `zpool events -H` the privileged way — unprivileged
// first, pkexec (or the delegated ssh user) on a permission failure — and
// returns the last n lines. The sample itself never elevates: a polkit
// prompt every second in Live mode would be unusable, so the tab asks once,
// on a button, and keeps the answer across samples.
func EventsElevated(h Host, n int) ([]string, error) {
	out, err := runOutMaybeElevated(h, "zpool", "events", "-H")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var ev []string
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			ev = append(ev, strings.Join(strings.Fields(ln), " "))
		}
	}
	return ev, nil
}

// readRaw returns a kstat file's text (txgs is a table, not name/type/data).
func readRaw(h Host, rel string) (string, error) {
	if h.SSH == "" {
		b, err := os.ReadFile(filepath.Join(kstatRoot, rel))
		return string(b), err
	}
	return run(h.command("cat", "/proc/spl/kstat/zfs/"+rel))
}

func parseMeminfo(text string) (total, avail int64) {
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return
}

// parseStatusExtras pulls the scan line, the per-leaf SLOW counters and
// whether a log section exists out of `zpool status -s -P`.
func parseStatusExtras(st string) (scan string, slow map[string]int64, hasLog bool) {
	slow = map[string]int64{}
	inConfig := false
	rows := 0
	for _, ln := range strings.Split(st, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "scan:"):
			scan = strings.TrimSpace(strings.TrimPrefix(t, "scan:"))
		case strings.HasPrefix(t, "config:"):
			inConfig = true
		case inConfig && t == "logs":
			hasLog = true
			rows++
		case inConfig && strings.HasPrefix(t, "/"):
			rows++
			f := strings.Fields(t)
			if len(f) >= 6 {
				if v, err := strconv.ParseInt(f[5], 10, 64); err == nil {
					slow[f[0]] = v
				}
			}
		case inConfig && t == "":
			// zpool prints a blank line right after "config:" and another
			// after the table; only the second one ends the block.
			if rows > 0 {
				inConfig = false
			}
		default:
			if inConfig && t != "" && !strings.HasPrefix(t, "NAME") {
				rows++
			}
		}
	}
	return
}

// ─── verdicts ───────────────────────────────────────────────────────────────

// Verdict is one sentence about the pool, with the number behind it and the
// knob that changes it. Level: red (acting on it now), gold (worth knowing),
// green (fine, and why), info (context).
type Verdict struct {
	Level    string
	Title    string
	Evidence string
	Fix      string
}

func pct(a, b float64) float64 {
	if a+b == 0 {
		return 100
	}
	return 100 * a / (a + b)
}

func fmtRate(bps float64) string { return fmtBytesDec(int64(bps)) + "/s" }

func msOf(ns int64) string {
	switch {
	case ns < 0:
		return "-"
	case ns < 1e6:
		// "us", not "µs": the monospace font's fallback shaping crashed
		// go-text on the micro sign under the software renderer (2026-09-05).
		return fmt.Sprintf("%.0f us", float64(ns)/1e3)
	}
	return fmt.Sprintf("%.1f ms", float64(ns)/1e6)
}

// Judge reads a sample and says what it means. Order: red first.
func Judge(s Sample) []Verdict {
	var v []Verdict
	add := func(level, title, evidence, fix string) { v = append(v, Verdict{level, title, evidence, fix}) }

	// health, scan, slow I/O
	if h := s.Props["health"]; h != "" && h != "ONLINE" {
		add("red", "pool is "+h, "zpool get health", "zpool status -v "+s.Pool+" names the device; replace or clear it")
	}
	if strings.Contains(s.Scan, "in progress") {
		add("info", "a scrub or resilver is running", s.Scan, "expect reads to be slower until it finishes; zpool scrub -p pauses a scrub")
	}
	var slowTotal int64
	for _, n := range s.SlowIOs {
		slowTotal += n
	}
	if slowTotal > 0 {
		var names []string
		for n, c := range s.SlowIOs {
			if c > 0 {
				names = append(names, fmt.Sprintf("%s (%d)", filepath.Base(n), c))
			}
		}
		sort.Strings(names)
		add("red", "slow I/Os recorded on "+strings.Join(names, ", "), "zpool status -s: SLOW column (I/Os over zio_slow_io_ms, default 30 s)", "a device that answers in seconds is dying or its cable/controller is; zpool events -v shows each one")
	}

	// vdev latency: an outlier among siblings, or a slow pool overall
	var leaves []VdevIO
	var pool VdevIO
	for _, x := range s.Vdevs {
		if x.Name == s.Pool {
			pool = x
		} else if x.Leaf(s.Pool) && (x.ROps > 0 || x.WOps > 0) {
			leaves = append(leaves, x)
		}
	}
	lat := func(x VdevIO) int64 {
		var n, sum int64
		for _, w := range []int64{x.DiskR, x.DiskW} {
			if w >= 0 {
				sum += w
				n++
			}
		}
		if n == 0 {
			return -1
		}
		return sum / n
	}
	if len(leaves) >= 3 {
		vals := make([]int64, 0, len(leaves))
		for _, x := range leaves {
			if l := lat(x); l >= 0 {
				vals = append(vals, l)
			}
		}
		if len(vals) >= 3 {
			sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
			median := vals[len(vals)/2]
			for _, x := range leaves {
				if l := lat(x); median > 0 && l > 5*median && l > 20e6 {
					add("red", fmt.Sprintf("%s answers %.0f× slower than its siblings", filepath.Base(x.Name), float64(l)/float64(median)),
						fmt.Sprintf("disk_wait %s vs a median of %s this second (zpool iostat -l)", msOf(l), msOf(median)),
						"one slow member holds every RAIDZ/mirror I/O back; check smartctl -a and zpool events, then zpool offline/replace it")
				}
			}
		}
	}
	if pool.TotalW > 100e6 && pool.WOps > 0 {
		add("gold", "write latency is high", fmt.Sprintf("total_wait write %s over %d ops this second", msOf(pool.TotalW), pool.WOps),
			"see whether the wait is disk_wait (the devices) or asyncq_wait (the pool queuing behind a slow txg) in the vdev table")
	}
	if pool.TotalR > 50e6 && pool.ROps > 0 {
		add("gold", "read latency is high", fmt.Sprintf("total_wait read %s over %d ops this second", msOf(pool.TotalR), pool.ROps),
			"reads that miss the ARC pay the disk; check the ARC verdict and whether a scrub is running")
	}

	// write throttle
	if r := s.Rate["dmu_tx.dmu_tx_dirty_throttle"]; r > 0 {
		add("red", "writes are being throttled hard", fmt.Sprintf("dmu_tx_dirty_throttle +%.0f/s: dirty data hit zfs_dirty_data_max (%s)", r, fmtBytesDec(s.Tunables["zfs_dirty_data_max"])),
			"the pool cannot sync as fast as writes arrive — look at txg sync time and vdev disk_wait below; raising zfs_dirty_data_max only buys a bigger buffer")
	} else if r := s.Rate["dmu_tx.dmu_tx_dirty_delay"]; r > 0 {
		add("gold", "writes are being delayed", fmt.Sprintf("dmu_tx_dirty_delay +%.0f/s: dirty data past zfs_delay_min_dirty_percent of %s", r, fmtBytesDec(s.Tunables["zfs_dirty_data_max"])),
			"ZFS is pacing writers so the txg can keep up; sustained, this is a write-bound pool")
	}
	if r := s.Rate["dmu_tx.dmu_tx_memory_reclaim"]; r > 0 {
		add("red", "writes are waiting on memory reclaim", fmt.Sprintf("dmu_tx_memory_reclaim +%.0f/s", r), "the host is short of RAM; the ARC verdict below says whether ZFS is the one holding it")
	}

	// txg sync
	if n := len(s.Txgs); n > 0 {
		var sum, worst, dirty int64
		for _, t := range s.Txgs {
			sum += t.STime
			dirty += t.NDirty
			if t.STime > worst {
				worst = t.STime
			}
		}
		avg := sum / int64(n)
		timeout := s.Tunables["zfs_txg_timeout"]
		if timeout == 0 {
			timeout = 5
		}
		switch {
		case worst > timeout*1e9:
			add("red", "a txg took longer to sync than the txg timeout", fmt.Sprintf("worst sync %s, average %s over the last %d txgs; timeout %d s (txgs kstat)", msOf(worst), msOf(avg), n, timeout),
				"while one txg syncs the next one fills — writers stall when it fills to zfs_dirty_data_max; the devices are not keeping up with the write rate")
		case avg > 2e9:
			add("gold", "txg syncs are slow", fmt.Sprintf("average sync %s over the last %d txgs, %s dirty per txg", msOf(avg), n, fmtBytesDec(dirty/int64(n))),
				"fine for a bulk load; for latency-sensitive writers this is the number to bring down (faster vdevs, or fewer sync writes)")
		}
	}

	// ARC
	hits := s.Rate["arc.demand_data_hits"] + s.Rate["arc.demand_metadata_hits"]
	misses := s.Rate["arc.demand_data_misses"] + s.Rate["arc.demand_metadata_misses"]
	lifeHits := float64(s.Arc.get("demand_data_hits") + s.Arc.get("demand_metadata_hits"))
	lifeMiss := float64(s.Arc.get("demand_data_misses") + s.Arc.get("demand_metadata_misses"))
	size, cmax := s.Arc.get("size"), s.Arc.get("c_max")
	if s.Rate["arc.memory_throttle_count"] > 0 {
		add("red", "the ARC is being throttled by memory pressure", fmt.Sprintf("memory_throttle_count +%.0f/s; ARC %s of a %s cap, host has %s of %s available", s.Rate["arc.memory_throttle_count"], fmtBytesDec(size), fmtBytesDec(cmax), fmtBytesDec(s.MemAvail), fmtBytesDec(s.MemTotal)),
			"something else wants the RAM; lower zfs_arc_max or give the host memory")
	}
	if hits+misses >= 50 && pct(hits, misses) < 80 {
		ev := fmt.Sprintf("demand hit rate %.0f%% this second (%.0f hits, %.0f misses); lifetime %.0f%%", pct(hits, misses), hits, misses, pct(lifeHits, lifeMiss))
		fix := "the working set is larger than the ARC"
		if cmax > 0 && size >= cmax*95/100 && s.MemTotal > 0 && cmax < s.MemTotal/2 {
			fix += fmt.Sprintf("; the ARC is at its %s cap on a %s host — raise zfs_arc_max", fmtBytesDec(cmax), fmtBytesDec(s.MemTotal))
		} else {
			fix += "; more RAM, an L2ARC on flash, or accept the disk reads"
		}
		add("gold", "the ARC is missing", ev, fix)
	} else if hits+misses >= 50 {
		add("green", "the ARC is serving reads", fmt.Sprintf("demand hit rate %.0f%% this second, ARC %s of %s", pct(hits, misses), fmtBytesDec(size), fmtBytesDec(cmax)), "")
	}
	if s.Arc.get("arc_no_grow") != 0 {
		add("info", "the ARC is not allowed to grow right now", "arc_no_grow = 1 (memory pressure seen recently)", "")
	}

	// ZIL / sync writes
	normal := s.Rate["zil.zil_itx_metaslab_normal_bytes"]
	slog := s.Rate["zil.zil_itx_metaslab_slog_bytes"]
	commits := s.Rate["zil.zil_commit_count"]
	if normal > 0 && !s.HasLog {
		var who []string
		for _, d := range s.Datasets {
			if d.ZilNormalBytes > 0 {
				who = append(who, fmt.Sprintf("%s %s", d.Name, fmtRate(d.ZilNormalBytes)))
			}
			if len(who) == 3 {
				break
			}
		}
		add("gold", "sync writes are landing on the main pool", fmt.Sprintf("%s of ZIL blocks/s, %.0f commits/s, no log vdev; from: %s", fmtRate(normal), commits, strings.Join(who, ", ")),
			"a mirrored SLOG (Builder, role log) absorbs them; or sync=disabled on a dataset whose last seconds of writes are expendable")
	} else if slog > 0 {
		add("green", "sync writes are going to the SLOG", fmt.Sprintf("%s/s to the log vdev, %.0f commits/s", fmtBytesDec(int64(slog)), commits), "")
	}
	for _, d := range s.Datasets {
		if d.ZilStalls > 0 || d.ZilSuspends > 0 {
			add("gold", d.Name+" has had ZIL commit stalls", fmt.Sprintf("zil_commit_stall_count %d, suspend %d (lifetime)", d.ZilStalls, d.ZilSuspends), "a stall is a commit waiting on a txg — the write-throttle verdicts above are the cause when they fire")
		}
	}

	// capacity, fragmentation, dedup
	if c, err := strconv.Atoi(s.Props["capacity"]); err == nil {
		switch {
		case c >= 90:
			add("red", fmt.Sprintf("the pool is %d%% full", c), "zpool get capacity", "above ~90% the allocator works hard for every block and fragmentation climbs; free space or add a vdev")
		case c >= 80:
			add("gold", fmt.Sprintf("the pool is %d%% full", c), "zpool get capacity", "plan the next vdev now; writes slow down past 90%")
		}
	}
	if f, err := strconv.Atoi(s.Props["fragmentation"]); err == nil && f >= 50 {
		add("gold", fmt.Sprintf("free space is %d%% fragmented", f), "zpool get fragmentation", "sequential writes become many small ones; the cure is free space (or a rebuild via send/recv)")
	}
	if d := s.Props["dedupratio"]; d == "1.00" || d == "1.00x" {
		if ds, err := datasetsWithDedup(s); err == nil && len(ds) > 0 {
			add("gold", "dedup is on and deduplicating nothing", "dedupratio 1.00 with dedup enabled on "+strings.Join(ds, ", "), "every write still pays the DDT lookup; zfs set dedup=off on those datasets (existing blocks stay as they are)")
		}
	}

	// prefetch
	zh, zm := s.Rate["zfetch.hits"], s.Rate["zfetch.misses"]
	if zh+zm > 100 && zm > zh*3 {
		add("info", "prefetch is not helping this workload", fmt.Sprintf("zfetch %.0f hits vs %.0f misses/s — random reads", zh, zm), "")
	}

	// busiest datasets
	var busy []string
	for i, d := range s.Datasets {
		if i == 3 || d.NRead+d.NWritten < 1e5 {
			break
		}
		busy = append(busy, fmt.Sprintf("%s r %s w %s", d.Name, fmtRate(d.NRead), fmtRate(d.NWritten)))
	}
	if len(busy) > 0 {
		add("info", "busiest datasets this second", strings.Join(busy, " · "), "")
	}

	// events
	ereports := 0
	last := ""
	for _, e := range s.Events {
		if strings.Contains(e, "ereport.fs.zfs.") {
			ereports++
			last = e
		}
	}
	if ereports > 0 {
		add("gold", fmt.Sprintf("%d error reports among the last %d events", ereports, len(s.Events)), last, "zpool events -v prints each with the vdev and the errno")
	}

	if len(v) == 0 {
		add("green", "nothing to report", "no latency, throttle, ARC, ZIL, capacity or error signal in this second", "")
	}
	order := map[string]int{"red": 0, "gold": 1, "green": 2, "info": 3}
	sort.SliceStable(v, func(i, j int) bool { return order[v[i].Level] < order[v[j].Level] })
	return v
}

// dedupLister is the one zfs call the verdicts make; tests replace it.
var dedupLister = func(s Sample) (string, error) {
	return run(LocalHost().command("zfs", "get", "-H", "-r", "-t", "filesystem,volume", "-o", "name,value", "dedup", s.Pool))
}

func datasetsWithDedup(s Sample) ([]string, error) {
	out, err := dedupLister(s)
	if err != nil {
		return nil, err
	}
	var ds []string
	for _, ln := range strings.Split(out, "\n") {
		if f := strings.Fields(ln); len(f) == 2 && f[1] != "off" && f[1] != "-" {
			ds = append(ds, f[0])
		}
	}
	return ds, nil
}

// ─── text renderings (CLI and TUI) ─────────────────────────────────────────

func (s Sample) gauges() []string {
	hits := s.Rate["arc.demand_data_hits"] + s.Rate["arc.demand_metadata_hits"]
	misses := s.Rate["arc.demand_data_misses"] + s.Rate["arc.demand_metadata_misses"]
	var pool VdevIO
	for _, x := range s.Vdevs {
		if x.Name == s.Pool {
			pool = x
		}
	}
	var syncAvg int64
	if n := len(s.Txgs); n > 0 {
		var sum int64
		for _, t := range s.Txgs {
			sum += t.STime
		}
		syncAvg = sum / int64(n)
	}
	return []string{
		fmt.Sprintf("ARC %.0f%% hit · %s of %s", pct(hits, misses), fmtBytesDec(s.Arc.get("size")), fmtBytesDec(s.Arc.get("c_max"))),
		fmt.Sprintf("ops %d r / %d w · %s r / %s w", pool.ROps, pool.WOps, fmtRate(float64(pool.RBw)), fmtRate(float64(pool.WBw))),
		fmt.Sprintf("latency r %s / w %s", msOf(pool.TotalR), msOf(pool.TotalW)),
		fmt.Sprintf("txg sync avg %s", msOf(syncAvg)),
		fmt.Sprintf("ZIL %s pool / %s slog · %.0f commits/s", fmtRate(s.Rate["zil.zil_itx_metaslab_normal_bytes"]), fmtRate(s.Rate["zil.zil_itx_metaslab_slog_bytes"]), s.Rate["zil.zil_commit_count"]),
		fmt.Sprintf("throttle delay %.0f/s · hard %.0f/s", s.Rate["dmu_tx.dmu_tx_dirty_delay"], s.Rate["dmu_tx.dmu_tx_dirty_throttle"]),
		fmt.Sprintf("%s%% full · frag %s%% · %s", s.Props["capacity"], s.Props["fragmentation"], s.Props["health"]),
	}
}

// Report is the terminal rendering of a sample and its verdicts.
func (s Sample) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s (%.1f s sample)\n", s.Pool, s.At.Format("15:04:05"), s.Seconds)
	for _, g := range s.gauges() {
		b.WriteString("  " + g + "\n")
	}
	b.WriteString("\nVERDICTS\n")
	for _, v := range Judge(s) {
		fmt.Fprintf(&b, "  [%s] %s\n", strings.ToUpper(v.Level), v.Title)
		if v.Evidence != "" {
			b.WriteString("        " + v.Evidence + "\n")
		}
		if v.Fix != "" {
			b.WriteString("        fix: " + v.Fix + "\n")
		}
	}
	b.WriteString("\nVDEVS  (ops r / w · bandwidth r / w · total wait r / w · disk wait r / w · queue sync / async)\n")
	for _, x := range s.Vdevs {
		fmt.Fprintf(&b, "  %-40s %5d / %-5d  %9s / %-9s  %8s / %-8s  %8s / %-8s  %8s / %-8s\n", filepath.Base(x.Name), x.ROps, x.WOps, fmtRate(float64(x.RBw)), fmtRate(float64(x.WBw)),
			msOf(x.TotalR), msOf(x.TotalW), msOf(x.DiskR), msOf(x.DiskW), msOf(maxI(x.SyncqR, x.SyncqW)), msOf(maxI(x.AsyncqR, x.AsyncqW)))
	}
	if len(s.Datasets) > 0 {
		b.WriteString("\nDATASETS  (this second; top 8 by bytes)\n")
		for i, d := range s.Datasets {
			if i == 8 {
				break
			}
			if d.Reads+d.Writes+d.NRead+d.NWritten+d.ZilCommits == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %-40s r %6.0f ops %10s   w %6.0f ops %10s   sync %4.0f/s %s\n", d.Name, d.Reads, fmtRate(d.NRead), d.Writes, fmtRate(d.NWritten), d.ZilCommits, fmtRate(d.ZilNormalBytes+d.ZilSlogBytes))
		}
	}
	if len(s.Events) > 0 {
		b.WriteString("\nEVENTS  (last)\n")
		for _, e := range s.Events {
			b.WriteString("  " + e + "\n")
		}
	}
	for _, w := range s.Warnings {
		b.WriteString("\n! " + w + "\n")
	}
	return b.String()
}

func maxI(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// KstatTable renders one group with per-second rates from two reads — the
// kstat browser.
func KstatTable(prev, cur Kstat, seconds float64) string {
	keys := make([]string, 0, len(cur.V))
	for k := range cur.V {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%-40s %20s %14s\n", "counter", "value", "per second")
	for _, k := range keys {
		rate := float64(cur.V[k]-prev.V[k]) / seconds
		r := ""
		if rate != 0 {
			r = fmt.Sprintf("%+.0f", rate)
		}
		fmt.Fprintf(&b, "%-40s %20d %14s\n", k, cur.V[k], r)
	}
	return b.String()
}

var poolNameArgRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)

// observeCLI is `zxplore --observe [POOL] [json|kstat GROUP|events|watch]`.
func observeCLI(args []string) int {
	h := LocalHost()
	pool := ""
	if len(args) > 0 && poolNameArgRE.MatchString(args[0]) && args[0] != "json" && args[0] != "kstat" && args[0] != "events" && args[0] != "watch" {
		pool, args = args[0], args[1:]
	}
	if pool == "" {
		pools, err := ListPools(h)
		if err != nil || len(pools) == 0 {
			fmt.Fprintln(os.Stderr, "zxplore: no imported pool to observe (name one: zxplore --observe POOL)")
			return 1
		}
		pool = pools[0]
	}
	mode := "report"
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "report", "json", "watch":
		for {
			s, err := Observe(h, pool)
			if err != nil {
				fmt.Fprintln(os.Stderr, "zxplore:", err)
				return 1
			}
			if mode == "json" {
				out := struct {
					Sample   Sample
					Verdicts []Verdict
				}{s, Judge(s)}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					fmt.Fprintln(os.Stderr, "zxplore:", err)
					return 1
				}
				return 0
			}
			if mode == "watch" {
				fmt.Print("\033[H\033[2J")
			}
			fmt.Print(s.Report())
			if mode != "watch" {
				return 0
			}
		}
	case "kstat":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: zxplore --observe [POOL] kstat GROUP   (arcstats, zil, dmu_tx, zfetchstats, dbufstats, POOL/txgs …)")
			return 2
		}
		a, err := readKstat(h, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		time.Sleep(time.Second)
		b, err := readKstat(h, args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		fmt.Print(KstatTable(a, b, 1))
		return 0
	case "events":
		// zpool events needs root on most hosts; a terminal has sudo.
		elevate()
		out, err := run(h.command("zpool", "events", "-v"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "zxplore:", err)
			return 1
		}
		fmt.Print(out)
		return 0
	}
	fmt.Fprintf(os.Stderr, "zxplore: unknown observe command %q (report, json, watch, kstat GROUP, events)\n", mode)
	return 2
}
