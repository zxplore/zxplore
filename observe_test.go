// observe_test.go — the Observe engine against fixtures: the kstat parsers
// (Linux and FreeBSD forms), txgs, the iostat latency columns, objset
// splitting, the status extras, every verdict rule in both directions, the
// report rendering, and a whole sample through fixture kstats + a mock zpool.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const arcFixture = `24 1 0x01 148 40256 9215373141 534920636523815
name                            type data
hits                            4    25779124146
misses                          4    22019068
demand_data_hits                4    537136779
demand_data_misses              4    6906891
demand_metadata_hits            4    25235760960
demand_metadata_misses          4    1084279
size                            4    16606685160
c_max                           4    16768380928
memory_throttle_count           4    0
arc_no_grow                     4    0
`

const objsetFixture = `129 1 0x01 28 7872 17198118646 534936893545945
name                            type data
dataset_name                    7    rpool/home
writes                          4    10
nwritten                        4    1000
reads                           4    0
nread                           4    0
zil_commit_count                4    28
zil_commit_stall_count          4    0
zil_commit_suspend_count        4    0
zil_itx_metaslab_normal_bytes   4    4096
zil_itx_metaslab_slog_bytes     4    0
130 1 0x01 28 7872 17198118646 534936893545945
name                            type data
dataset_name                    7    rpool/var/log
writes                          4    5
nwritten                        4    500
reads                           4    1
nread                           4    100
zil_commit_count                4    0
zil_commit_stall_count          4    2
zil_commit_suspend_count        4    0
zil_itx_metaslab_normal_bytes   4    0
zil_itx_metaslab_slog_bytes     4    0
`

const txgsFixture = `txg      birth            state ndirty       nread        nwritten     reads    writes   otime        qtime        wtime        stime
130127   534426220200134  C     6297088      0            3649536      0        115      5119927415   4060         6600         15270872
130128   534431340127549  C     6790144      0            4276224      0        183      5119935915   4420         8340         8611263
130226   534918635174328  O     0            0            0            0        0        0            0            0            0
`

const iostatFixture = "rpool\t236931067904\t1755933757440\t0\t123\t0\t3283565\t-\t2088167\t-\t129321\t-\t1152\t-\t1984556\t-\t-\t-\n" +
	"nvme0n1p2\t236931067904\t1755933757440\t0\t123\t0\t3283490\t-\t2088167\t-\t129321\t-\t1152\t-\t1984556\t-\t-\t-\n"

func TestParseKstatForms(t *testing.T) {
	k := parseKstat(arcFixture)
	if k.get("hits") != 25779124146 || k.get("c_max") != 16768380928 {
		t.Errorf("linux kstat: hits %d c_max %d", k.get("hits"), k.get("c_max"))
	}
	if _, ok := k.V["name"]; ok {
		t.Error("the 'name type data' line is not a counter")
	}
	f := parseSysctlKstat("kstat.zfs.misc.arcstats.hits: 42\nkstat.zfs.misc.arcstats.c_max: 1000\nkstat.zfs.misc.arcstats.something: text\n")
	if f.get("hits") != 42 || f.get("c_max") != 1000 || f.S["something"] != "text" {
		t.Errorf("sysctl kstat: %+v", f)
	}
	blocks := splitObjsets(objsetFixture)
	if len(blocks) != 2 || blocks[0].S["dataset_name"] != "rpool/home" || blocks[1].get("zil_commit_stall_count") != 2 {
		t.Errorf("objsets: %d blocks, %+v", len(blocks), blocks)
	}
	tx := parseTxgs(txgsFixture)
	if len(tx) != 3 || tx[0].STime != 15270872 || tx[2].State != "O" || tx[1].NDirty != 6790144 {
		t.Errorf("txgs: %+v", tx)
	}
	io := parseIostat(iostatFixture)
	if len(io) != 2 || io[0].Name != "rpool" || io[0].WOps != 123 || io[0].TotalW != 2088167 || io[0].TotalR != -1 || io[0].AsyncqW != 1984556 || io[0].Rebuild != -1 {
		t.Errorf("iostat: %+v", io[0])
	}
	if io[0].Leaf("rpool") || !io[1].Leaf("rpool") || (VdevIO{Name: "raidz2-0"}).Leaf("tank") || (VdevIO{Name: "logs"}).Leaf("tank") {
		t.Error("leaf classification")
	}
	tot, av := parseMeminfo("MemTotal:       32750744 kB\nMemAvailable:    7286588 kB\n")
	if tot != 32750744*1024 || av != 7286588*1024 {
		t.Errorf("meminfo: %d %d", tot, av)
	}
	scan, slow, hasLog := parseStatusExtras(`  pool: tank
 state: ONLINE
  scan: scrub in progress since Sat Sep  5 11:00:00 2026
config:

	NAME                                  STATE     READ WRITE CKSUM  SLOW
	tank                                  ONLINE       0     0     0     -
	  mirror-0                            ONLINE       0     0     0     -
	    /dev/disk/by-id/wwn-1             ONLINE       0     0     0     0
	    /dev/disk/by-id/wwn-2             ONLINE       0     0     0     7
	logs
	  /dev/disk/by-id/nvme-1              ONLINE       0     0     0     0

errors: No known data errors
`)
	if !strings.HasPrefix(scan, "scrub in progress") || slow["/dev/disk/by-id/wwn-2"] != 7 || !hasLog {
		t.Errorf("status extras: %q %v %v", scan, slow, hasLog)
	}
}

// baseSample is a quiet, healthy pool; each rule test perturbs one thing.
func baseSample() Sample {
	s := Sample{Pool: "tank", Seconds: 1, Rate: map[string]float64{}, Props: map[string]string{"capacity": "40", "fragmentation": "5", "dedupratio": "1.00", "health": "ONLINE"},
		SlowIOs: map[string]int64{}, Tunables: map[string]int64{"zfs_dirty_data_max": 4e9, "zfs_txg_timeout": 5, "zfs_arc_max": 16e9}, MemTotal: 64e9, MemAvail: 20e9, HasLog: true}
	s.Arc = Kstat{V: map[string]int64{"size": 15.6e9, "c_max": 16e9, "demand_data_hits": 1000, "demand_data_misses": 10}}
	s.Rate["arc.demand_data_hits"] = 900
	s.Rate["arc.demand_data_misses"] = 10
	s.Vdevs = []VdevIO{
		{Name: "tank", ROps: 100, WOps: 100, TotalR: 2e6, TotalW: 3e6, DiskR: 1e6, DiskW: 1e6},
		{Name: "/dev/disk/by-id/a", ROps: 30, WOps: 30, DiskR: 1e6, DiskW: 1e6},
		{Name: "/dev/disk/by-id/b", ROps: 30, WOps: 30, DiskR: 1e6, DiskW: 1e6},
		{Name: "/dev/disk/by-id/c", ROps: 30, WOps: 30, DiskR: 1e6, DiskW: 1e6},
		{Name: "/dev/disk/by-id/d", ROps: 30, WOps: 30, DiskR: 1e6, DiskW: 1e6},
	}
	for i := 0; i < 5; i++ {
		s.Txgs = append(s.Txgs, Txg{State: "C", STime: 20e6, NDirty: 1e6})
	}
	return s
}

func levelsOf(v []Verdict) string {
	var out []string
	for _, x := range v {
		out = append(out, x.Level+":"+x.Title)
	}
	return strings.Join(out, "\n")
}

func TestJudgeRules(t *testing.T) {
	dedupLister = func(Sample) (string, error) { return "tank\toff\ntank/dd\ton\n", nil }
	quiet := Judge(baseSample())
	if !strings.Contains(levelsOf(quiet), "green:the ARC is serving reads") || strings.Contains(levelsOf(quiet), "red:") {
		t.Errorf("a healthy pool must read green:\n%s", levelsOf(quiet))
	}
	cases := []struct {
		name  string
		mut   func(*Sample)
		want  string
		level string
	}{
		{"degraded", func(s *Sample) { s.Props["health"] = "DEGRADED" }, "pool is DEGRADED", "red"},
		{"scrub", func(s *Sample) { s.Scan = "scrub in progress since today" }, "scrub or resilver is running", "info"},
		{"slow io", func(s *Sample) { s.SlowIOs["/dev/disk/by-id/b"] = 3 }, "slow I/Os recorded on b (3)", "red"},
		{"outlier vdev", func(s *Sample) { s.Vdevs[2].DiskR, s.Vdevs[2].DiskW = 200e6, 200e6 }, "b answers 200× slower than its siblings", "red"},
		{"write latency", func(s *Sample) { s.Vdevs[0].TotalW = 150e6 }, "write latency is high", "gold"},
		{"read latency", func(s *Sample) { s.Vdevs[0].TotalR = 80e6 }, "read latency is high", "gold"},
		{"throttle", func(s *Sample) { s.Rate["dmu_tx.dmu_tx_dirty_throttle"] = 3 }, "throttled hard", "red"},
		{"delay", func(s *Sample) { s.Rate["dmu_tx.dmu_tx_dirty_delay"] = 30 }, "being delayed", "gold"},
		{"txg over timeout", func(s *Sample) { s.Txgs[3].STime = 6e9 }, "longer to sync than the txg timeout", "red"},
		{"txg slow", func(s *Sample) {
			for i := range s.Txgs {
				s.Txgs[i].STime = 3e9
			}
		}, "txg syncs are slow", "gold"},
		{"arc miss at cap", func(s *Sample) { s.Rate["arc.demand_data_hits"], s.Rate["arc.demand_data_misses"] = 100, 100 }, "the ARC is missing", "gold"},
		{"arc memory throttle", func(s *Sample) { s.Rate["arc.memory_throttle_count"] = 2 }, "throttled by memory pressure", "red"},
		{"zil no slog", func(s *Sample) {
			s.HasLog = false
			s.Rate["zil.zil_itx_metaslab_normal_bytes"] = 5e6
			s.Datasets = []DatasetIO{{Name: "tank/db", ZilNormalBytes: 5e6}}
		}, "sync writes are landing on the main pool", "gold"},
		{"zil slog", func(s *Sample) { s.Rate["zil.zil_itx_metaslab_slog_bytes"] = 5e6 }, "going to the SLOG", "green"},
		{"zil stalls", func(s *Sample) { s.Datasets = []DatasetIO{{Name: "tank/db", ZilStalls: 4}} }, "tank/db has had ZIL commit stalls", "gold"},
		{"90% full", func(s *Sample) { s.Props["capacity"] = "91" }, "pool is 91% full", "red"},
		{"80% full", func(s *Sample) { s.Props["capacity"] = "83" }, "pool is 83% full", "gold"},
		{"fragmented", func(s *Sample) { s.Props["fragmentation"] = "61" }, "61% fragmented", "gold"},
		{"dedup for nothing", func(s *Sample) { s.Props["dedupratio"] = "1.00" }, "dedup is on and deduplicating nothing", "gold"},
		{"prefetch", func(s *Sample) { s.Rate["zfetch.hits"], s.Rate["zfetch.misses"] = 10, 500 }, "prefetch is not helping", "info"},
		{"busy datasets", func(s *Sample) { s.Datasets = []DatasetIO{{Name: "tank/vm", NRead: 50e6, NWritten: 20e6}} }, "busiest datasets", "info"},
		{"ereports", func(s *Sample) { s.Events = []string{"x ereport.fs.zfs.io", "y sysevent.fs.zfs.history_event"} }, "1 error reports among the last 2 events", "gold"},
	}
	for _, c := range cases {
		s := baseSample()
		c.mut(&s)
		vs := Judge(s)
		hit := false
		for _, v := range vs {
			if v.Level == c.level && strings.Contains(v.Title, c.want) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("%s: want %s:%s in\n%s", c.name, c.level, c.want, levelsOf(vs))
		}
	}
	// The ARC verdict names the cap when the ARC is pinned below half of RAM.
	s := baseSample()
	s.Rate["arc.demand_data_hits"], s.Rate["arc.demand_data_misses"] = 100, 100
	for _, v := range Judge(s) {
		if v.Title == "the ARC is missing" && !strings.Contains(v.Fix, "raise zfs_arc_max") {
			t.Errorf("ARC at a 16 GB cap on a 64 GB host must point at zfs_arc_max: %q", v.Fix)
		}
	}
	// Red sorts before gold before green before info.
	s = baseSample()
	s.Props["capacity"] = "95"
	s.Scan = "scrub in progress"
	lv := Judge(s)
	if lv[0].Level != "red" || lv[len(lv)-1].Level != "info" {
		t.Errorf("order: %s", levelsOf(lv))
	}
	// Dedup with nothing enabled says nothing.
	dedupLister = func(Sample) (string, error) { return "tank\toff\n", nil }
	if strings.Contains(levelsOf(Judge(baseSample())), "dedup") {
		t.Error("dedup off everywhere must not warn")
	}
}

func TestReportAndKstatTable(t *testing.T) {
	s := baseSample()
	s.At = time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
	s.Datasets = []DatasetIO{{Name: "tank/vm", Reads: 3, NRead: 2e6, Writes: 1, NWritten: 5e5}}
	s.Events = []string{"Sep 5 2026 10:00:00 sysevent.fs.zfs.history_event"}
	s.Warnings = []string{"zpool events: permission denied"}
	r := s.Report()
	for _, want := range []string{"tank — 11:00:00", "ARC 99% hit", "VERDICTS", "[GREEN]", "VDEVS", "  a  ", "tank/vm", "2.0 MB/s", "EVENTS", "! zpool events"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	tbl := KstatTable(Kstat{V: map[string]int64{"hits": 100, "misses": 5}}, Kstat{V: map[string]int64{"hits": 160, "misses": 5}}, 2)
	if !strings.Contains(tbl, "hits") || !strings.Contains(tbl, "+30") || strings.Contains(tbl, "+0") {
		t.Errorf("kstat table:\n%s", tbl)
	}
	if msOf(-1) != "-" || msOf(500e3) != "500 us" || msOf(15e6) != "15.0 ms" {
		t.Error("msOf")
	}
}

// TestObserveMock runs a whole sample over fixture kstats and a mock zpool.
func TestObserveMock(t *testing.T) {
	m := newMock(t)
	root := t.TempDir()
	old := kstatRoot
	kstatRoot = root
	defer func() { kstatRoot = old }()
	must := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "arcstats"), arcFixture)
	must(filepath.Join(root, "zil"), "1 1 0x01 3 0 0 0\nname type data\nzil_commit_count 4 10\nzil_itx_metaslab_normal_bytes 4 1000\nzil_itx_metaslab_slog_bytes 4 0\n")
	must(filepath.Join(root, "dmu_tx"), "1 1 0x01 3 0 0 0\nname type data\ndmu_tx_dirty_throttle 4 0\ndmu_tx_dirty_delay 4 0\ndmu_tx_assigned 4 5\n")
	must(filepath.Join(root, "zfetchstats"), "1 1 0x01 2 0 0 0\nname type data\nhits 4 5\nmisses 4 5\n")
	must(filepath.Join(root, "rpool", "txgs"), txgsFixture)
	must(filepath.Join(root, "rpool", "objset-0x109"), strings.SplitN(objsetFixture, "130 1", 2)[0])
	m.script("zpool", `echo "zpool $*" >> "$ZX_CMDLOG"
case "$1" in
  iostat) printf '`+strings.ReplaceAll(iostatFixture, "\n", `\n`)+`' ;;
  status) printf 'config:\n\n\tNAME STATE READ WRITE CKSUM SLOW\n\trpool ONLINE 0 0 0 -\n\t  /dev/nvme0n1p2 ONLINE 0 0 0 0\n\n' ;;
  get) printf 'capacity\t11\nfragmentation\t8\ndedupratio\t1.00\nashift\t12\nhealth\tONLINE\n' ;;
  events) echo "permission denied" >&2; exit 1 ;;
esac`)
	dedupLister = func(Sample) (string, error) { return "rpool\toff\n", nil }
	s, err := Observe(LocalHost(), "rpool")
	if err != nil {
		t.Fatal(err)
	}
	if s.Pool != "rpool" || len(s.Vdevs) != 2 || len(s.Txgs) != 2 || len(s.Datasets) != 1 || s.Datasets[0].Name != "rpool/home" {
		t.Errorf("sample: vdevs %d txgs %d datasets %+v", len(s.Vdevs), len(s.Txgs), s.Datasets)
	}
	if s.Props["health"] != "ONLINE" || s.Arc.get("c_max") != 16768380928 {
		t.Errorf("props/arc: %v %d", s.Props, s.Arc.get("c_max"))
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "zpool events") {
		t.Errorf("events refusal must be a warning, not silence: %v", s.Warnings)
	}
	if !strings.Contains(m.log(), "zpool iostat -Hpvly rpool 1 1\n") {
		t.Errorf("iostat argv:\n%s", m.log())
	}
	if r := s.Report(); !strings.Contains(r, "VERDICTS") {
		t.Error("report renders")
	}
	if _, err := Observe(LocalHost(), ""); err == nil {
		t.Error("no pool must be an error")
	}
}
