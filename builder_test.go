// builder_test.go — the pool Builder's engine against fixtures: the shelf
// parse (both lsblk boolean encodings, by-id preference, in-use reasons), the
// capacity and fault-tolerance math, validation, the exact create argv,
// candidate layouts for the debz twelve-disk shelf, an existing pool's
// topology, the spec grammar, and the CLI path through the mock lsblk/zpool.
package main

import (
	"strconv"
	"strings"
	"testing"
)

// lsblkFixture is a shelf lsblk would print on a 12 + 2 + 2 box, with the
// system disk in use, one USB stick, one zvol and one loop device.
const lsblkFixture = `{"blockdevices": [
 {"name":"nvme0n1","path":"/dev/nvme0n1","size":1600321314816,"model":"INTEL SSDPF2KX016T1","serial":"PHAX1","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-INTEL_SSDPF2KX016T1_PHAX1"},
 {"name":"nvme1n1","path":"/dev/nvme1n1","size":1600321314816,"model":"INTEL SSDPF2KX016T1","serial":"PHAX2","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-INTEL_SSDPF2KX016T1_PHAX2"},
 {"name":"nvme2n1","path":"/dev/nvme2n1","size":2000398934016,"model":"Samsung SSD 990","serial":"S1","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-Samsung_SSD_990_S1",
  "children":[{"name":"nvme2n1p1","fstype":"vfat","mountpoint":"/boot/efi"},{"name":"nvme2n1p2","fstype":"zfs_member","mountpoint":null,"label":"rpool"}]},
 {"name":"sda","path":"/dev/sda","size":1920383410176,"model":"Samsung SSD 870","serial":"E1","rota":false,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"ata-Samsung_SSD_870_E1"},
 {"name":"sdb","path":"/dev/sdb","size":1920383410176,"model":"Samsung SSD 870","serial":"E2","rota":false,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"ata-Samsung_SSD_870_E2"},
 {"name":"sdc","path":"/dev/sdc","size":20000588955648,"model":"WDC WD201KFGX","serial":"W1","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a1"},
 {"name":"sdd","path":"/dev/sdd","size":20000588955648,"model":"WDC WD201KFGX","serial":"W2","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a2"},
 {"name":"sde","path":"/dev/sde","size":20000588955648,"model":"WDC WD201KFGX","serial":"W3","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a3"},
 {"name":"sdf","path":"/dev/sdf","size":20000588955648,"model":"WDC WD201KFGX","serial":"W4","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a4"},
 {"name":"sdg","path":"/dev/sdg","size":20000588955648,"model":"WDC WD201KFGX","serial":"W5","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a5"},
 {"name":"sdh","path":"/dev/sdh","size":20000588955648,"model":"WDC WD201KFGX","serial":"W6","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a6"},
 {"name":"sdi","path":"/dev/sdi","size":20000588955648,"model":"WDC WD201KFGX","serial":"W7","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a7"},
 {"name":"sdj","path":"/dev/sdj","size":20000588955648,"model":"WDC WD201KFGX","serial":"W8","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a8"},
 {"name":"sdk","path":"/dev/sdk","size":20000588955648,"model":"WDC WD201KFGX","serial":"W9","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a9"},
 {"name":"sdl","path":"/dev/sdl","size":20000588955648,"model":"WDC WD201KFGX","serial":"W10","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2aa"},
 {"name":"sdm","path":"/dev/sdm","size":20000588955648,"model":"WDC WD201KFGX","serial":"W11","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2ab"},
 {"name":"sdn","path":"/dev/sdn","size":20000588955648,"model":"WDC WD201KFGX","serial":"W12","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2ac"},
 {"name":"sdo","path":"/dev/sdo","size":61865984000,"model":"USB Flash Drive","serial":"U1","rota":"1","tran":"usb","type":"disk","fstype":"iso9660","mountpoint":null,"label":null,"id-link":"usb-Lexar_USB_Flash_Drive_U1"},
 {"name":"zd0","path":"/dev/zd0","size":42949672960,"model":null,"serial":null,"rota":"0","tran":null,"type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":null},
 {"name":"loop0","path":"/dev/loop0","size":1073741824,"model":null,"serial":null,"rota":false,"tran":null,"type":"loop","fstype":null,"mountpoint":null,"label":null,"id-link":null},
 {"name":"sdo1","path":"/dev/sdo1","size":1,"model":null,"serial":null,"rota":true,"tran":null,"type":"part","fstype":null,"mountpoint":null,"label":null,"id-link":null}
]}`

func shelf(t *testing.T) []Disk {
	t.Helper()
	disks, err := parseLsblk(lsblkFixture)
	if err != nil {
		t.Fatal(err)
	}
	return disks
}

func byName(disks []Disk, names ...string) []Disk {
	var out []Disk
	for _, n := range names {
		for _, d := range disks {
			if d.Name == n {
				out = append(out, d)
			}
		}
	}
	return out
}

func TestParseLsblkShelf(t *testing.T) {
	disks := shelf(t)
	if len(disks) != 20 {
		t.Fatalf("20 whole disks expected (partitions dropped), got %d", len(disks))
	}
	want := map[string]struct{ kind, inUse string }{
		"nvme0n1": {"nvme", ""},
		"nvme2n1": {"nvme", "mounted at /boot/efi, zfs member of rpool"},
		"sda":     {"ssd", ""},
		"sdc":     {"hdd", ""},
		"sdo":     {"usb", "iso9660 on the whole disk"},
		"zd0":     {"virtual", ""},
		"loop0":   {"virtual", ""},
	}
	for _, d := range disks {
		w, ok := want[d.Name]
		if !ok {
			continue
		}
		if d.Kind != w.kind || d.InUse != w.inUse {
			t.Errorf("%s: kind %q in-use %q; want %q %q", d.Name, d.Kind, d.InUse, w.kind, w.inUse)
		}
	}
	// The string encodings of rota ("1"/"0") must land like the booleans.
	if d := byName(disks, "sdo")[0]; !d.Rota {
		t.Error(`rota "1" must read as rotational`)
	}
	if d := byName(disks, "zd0")[0]; d.Rota || !d.pseudo {
		t.Error(`zd0: rota "0" must read false, and a zvol is pseudo`)
	}
	if d := byName(disks, "sdc")[0]; d.ByID != "/dev/disk/by-id/wwn-0x5000cca2a1" || d.Size() != "20 TB" {
		t.Errorf("sdc by-id/size: %q %q", d.ByID, d.Size())
	}
}

func TestParseByIDListing(t *testing.T) {
	listing := `total 0
lrwxrwxrwx. 1 root root  9 Sep  5 10:00 ata-Samsung_SSD_870_E1 -> ../../sda
lrwxrwxrwx. 1 root root 10 Sep  5 10:00 ata-Samsung_SSD_870_E1-part1 -> ../../sda1
lrwxrwxrwx. 1 root root  9 Sep  5 10:00 wwn-0x5002538f1 -> ../../sda
lrwxrwxrwx. 1 root root 13 Sep  5 10:00 nvme-eui.0025385b21 -> ../../nvme0n1
lrwxrwxrwx. 1 root root 13 Sep  5 10:00 nvme-Samsung_SSD_990_S1 -> ../../nvme0n1
lrwxrwxrwx. 1 root root 13 Sep  5 10:00 nvme-nvme.144d-53313 -> ../../nvme0n1
`
	got := parseByIDListing(listing)
	if got["sda"] != "wwn-0x5002538f1" {
		t.Errorf("sda: wwn must win over ata: %q", got["sda"])
	}
	if got["nvme0n1"] != "nvme-Samsung_SSD_990_S1" {
		t.Errorf("nvme0n1: the model link must win over eui/nvme-nvme: %q", got["nvme0n1"])
	}
	if _, ok := got["sda1"]; ok {
		t.Error("partition links must not map anything")
	}
}

func TestCapacityAndFaultTolerance(t *testing.T) {
	disks := shelf(t)
	hdd := byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh")
	tb := int64(20000588955648)
	cases := []struct {
		name   string
		v      Vdev
		usable int64
		parity int
	}{
		{"stripe", Vdev{Kind: "stripe", Disks: hdd}, 6 * tb, 0},
		{"mirror", Vdev{Kind: "mirror", Disks: hdd[:2]}, tb, 1},
		{"3-way mirror", Vdev{Kind: "mirror", Disks: hdd[:3]}, tb, 2},
		{"raidz1", Vdev{Kind: "raidz1", Disks: hdd}, 5 * tb, 1},
		{"raidz2", Vdev{Kind: "raidz2", Disks: hdd}, 4 * tb, 2},
		{"raidz3", Vdev{Kind: "raidz3", Disks: hdd}, 3 * tb, 3},
		{"draid2 4d:12c:2s", Vdev{Kind: "draid2", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh", "sdi", "sdj", "sdk", "sdl", "sdm", "sdn"), DraidData: 4, DraidSpares: 2}, 10 * tb * 4 / 6, 2},
	}
	for _, c := range cases {
		if got := c.v.Usable(); got != c.usable {
			t.Errorf("%s usable = %d, want %d", c.name, got, c.usable)
		}
		if got := c.v.parity(); got != c.parity {
			t.Errorf("%s parity = %d, want %d", c.name, got, c.parity)
		}
	}
	// The smallest member sets the size.
	mixed := Vdev{Kind: "mirror", Disks: byName(disks, "sdc", "sda")}
	if mixed.Usable() != 1920383410176 {
		t.Errorf("mixed mirror must be the small disk: %d", mixed.Usable())
	}
	d := Design{Name: "tank", Vdevs: []Vdev{
		{Kind: "raidz2", Role: "data", Disks: hdd},
		{Kind: "mirror", Role: "data", Disks: byName(disks, "sdi", "sdj")},
		{Kind: "mirror", Role: "log", Disks: byName(disks, "nvme0n1", "nvme1n1")},
	}}
	if d.Usable() != 5*tb || d.Raw() != 8*tb {
		t.Errorf("design usable/raw = %d/%d", d.Usable(), d.Raw())
	}
	if n, s := d.FaultTolerance(); n != 1 || !strings.Contains(s, "weakest") {
		t.Errorf("fault tolerance = %d %q; the mirror is the weakest vdev", n, s)
	}
	if n, s := (Design{Vdevs: []Vdev{{Kind: "stripe", Role: "data", Disks: hdd}}}).FaultTolerance(); n != 0 || !strings.Contains(s, "destroys") {
		t.Errorf("stripe fault tolerance = %d %q", n, s)
	}
}

func TestValidate(t *testing.T) {
	disks := shelf(t)
	hdd := byName(disks, "sdc", "sdd", "sde", "sdf")
	ok := Design{Name: "tank", Vdevs: []Vdev{{Kind: "raidz1", Role: "data", Disks: hdd}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a plain raidz1 must validate: %v", err)
	}
	bad := []struct {
		name string
		d    Design
		want string
	}{
		{"no name", Design{Vdevs: ok.Vdevs}, "needs a name"},
		{"keyword", Design{Name: "mirror", Vdevs: ok.Vdevs}, "keyword"},
		{"c-digit", Design{Name: "c0t0d0", Vdevs: ok.Vdevs}, "device name"},
		{"bad char", Design{Name: "my pool", Vdevs: ok.Vdevs}, "not a valid pool name"},
		{"no data", Design{Name: "t", Vdevs: []Vdev{{Kind: "stripe", Role: "cache", Disks: hdd[:1]}}}, "at least one data vdev"},
		{"raidz2 too narrow", Design{Name: "t", Vdevs: []Vdev{{Kind: "raidz2", Role: "data", Disks: hdd[:2]}}}, "at least 3"},
		{"raidz log", Design{Name: "t", Vdevs: []Vdev{ok.Vdevs[0], {Kind: "raidz1", Role: "log", Disks: byName(disks, "nvme0n1", "nvme1n1", "sda")}}}, "log vdev"},
		{"mirror cache", Design{Name: "t", Vdevs: []Vdev{ok.Vdevs[0], {Kind: "mirror", Role: "cache", Disks: byName(disks, "sda", "sdb")}}}, "listed bare"},
		{"in use", Design{Name: "t", Vdevs: []Vdev{{Kind: "stripe", Role: "data", Disks: byName(disks, "nvme2n1")}}}, "in use"},
		{"duplicate", Design{Name: "t", Vdevs: []Vdev{ok.Vdevs[0], {Kind: "stripe", Role: "spare", Disks: hdd[:1]}}}, "appears in vdev"},
		{"draid too few", Design{Name: "t", Vdevs: []Vdev{{Kind: "draid2", Role: "data", Disks: hdd, DraidData: 3, DraidSpares: 1}}}, "at least 5 children"},
		{"draid no groups", Design{Name: "t", Vdevs: []Vdev{{Kind: "draid2", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh"), DraidData: 3, DraidSpares: 0}}}, "cannot be cut"},
	}
	for _, c := range bad {
		err := c.d.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestArgvAndCommand(t *testing.T) {
	disks := shelf(t)
	d := Design{Name: "tank", Ashift: 12, Compression: "zstd", Props: []string{"atime=off"}, Vdevs: []Vdev{
		{Kind: "raidz2", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh")},
		{Kind: "raidz2", Role: "data", Disks: byName(disks, "sdi", "sdj", "sdk", "sdl", "sdm", "sdn")},
		{Kind: "stripe", Role: "spare", Disks: byName(disks, "sdb")},
		{Kind: "mirror", Role: "log", Disks: byName(disks, "nvme0n1", "nvme1n1")},
		{Kind: "stripe", Role: "cache", Disks: byName(disks, "sda")},
	}}
	got := strings.Join(d.Argv(), " ")
	want := "zpool create -o ashift=12 -O compression=zstd -O atime=off tank " +
		"raidz2 /dev/disk/by-id/wwn-0x5000cca2a1 /dev/disk/by-id/wwn-0x5000cca2a2 /dev/disk/by-id/wwn-0x5000cca2a3 /dev/disk/by-id/wwn-0x5000cca2a4 /dev/disk/by-id/wwn-0x5000cca2a5 /dev/disk/by-id/wwn-0x5000cca2a6 " +
		"raidz2 /dev/disk/by-id/wwn-0x5000cca2a7 /dev/disk/by-id/wwn-0x5000cca2a8 /dev/disk/by-id/wwn-0x5000cca2a9 /dev/disk/by-id/wwn-0x5000cca2aa /dev/disk/by-id/wwn-0x5000cca2ab /dev/disk/by-id/wwn-0x5000cca2ac " +
		"log mirror /dev/disk/by-id/nvme-INTEL_SSDPF2KX016T1_PHAX1 /dev/disk/by-id/nvme-INTEL_SSDPF2KX016T1_PHAX2 " +
		"cache /dev/disk/by-id/ata-Samsung_SSD_870_E1 " +
		"spare /dev/disk/by-id/ata-Samsung_SSD_870_E2"
	if got != want {
		t.Errorf("argv:\n got %s\nwant %s", got, want)
	}
	if d.Command() != got {
		t.Errorf("Command must equal the joined argv when nothing needs quoting: %q", d.Command())
	}
	dr := Design{Name: "big", Vdevs: []Vdev{{Kind: "draid2", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh", "sdi", "sdj", "sdk", "sdl", "sdm", "sdn"), DraidData: 4, DraidSpares: 2}}}
	if a := dr.Argv(); a[3] != "draid2:4d:12c:2s" {
		t.Errorf("draid word: %q", a[3])
	}
	// A disk with no by-id link goes in by kernel path.
	bare := Design{Name: "lab", Vdevs: []Vdev{{Kind: "stripe", Role: "data", Disks: byName(disks, "loop0")}}}
	if a := bare.Argv(); a[len(a)-1] != "/dev/loop0" {
		t.Errorf("no by-id → /dev path: %v", a)
	}
}

func TestWarnings(t *testing.T) {
	disks := shelf(t)
	d := Design{Name: "t", Vdevs: []Vdev{
		{Kind: "raidz1", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh", "sdi")},
		{Kind: "stripe", Role: "log", Disks: byName(disks, "nvme0n1")},
		{Kind: "stripe", Role: "cache", Disks: byName(disks, "sdj")},
		{Kind: "mirror", Role: "data", Disks: byName(disks, "sdk", "sdo")},
	}}
	w := strings.Join(d.Warnings(), "\n")
	for _, want := range []string{"RAIDZ2 is the usual answer", "mirror the SLOG", "rotational cache", "USB", "mixes sizes"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings must mention %q:\n%s", want, w)
		}
	}
	clean := Design{Name: "t", Vdevs: []Vdev{{Kind: "raidz2", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh")}}}
	if len(clean.Warnings()) != 0 {
		t.Errorf("a plain raidz2 of six matched disks has nothing to warn about: %v", clean.Warnings())
	}
}

func TestSuggestDebzShelf(t *testing.T) {
	disks := shelf(t)
	hdd := byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh", "sdi", "sdj", "sdk", "sdl", "sdm", "sdn")
	var aux []Disk
	for _, d := range disks {
		if d.Kind != "hdd" {
			aux = append(aux, d)
		}
	}
	cands := Suggest("tank", hdd, aux)
	labels := map[string]Candidate{}
	for _, c := range cands {
		labels[c.Label] = c
		if err := c.Design.Validate(); err != nil {
			t.Errorf("%s: a candidate must validate: %v", c.Label, err)
		}
	}
	for _, want := range []string{"Mirror ×6", "RAIDZ1 ×2", "RAIDZ2", "RAIDZ3", "dRAID2", "Stripe"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("candidate %q missing; have %v", want, keysOf(labels))
		}
	}
	if cands[0].Badge != "RECOMMENDED" {
		t.Errorf("recommended candidates sort first, got %s/%s", cands[0].Label, cands[0].Badge)
	}
	tb := int64(20000588955648)
	if u := labels["RAIDZ2"].Design.Usable(); u != 10*tb {
		t.Errorf("RAIDZ2 of 12: usable %d, want %d", u, 10*tb)
	}
	if u := labels["Mirror ×6"].Design.Usable(); u != 6*tb {
		t.Errorf("6 mirrors: usable %d", u)
	}
	// Rotational data + free NVMe/SSD: the two NVMe become a mirrored log,
	// the two SSDs the cache. The in-use nvme2n1 is never offered.
	rz := labels["RAIDZ2"].Design
	var log, cache []string
	for _, v := range rz.Vdevs {
		for _, d := range v.Disks {
			switch v.Role {
			case "log":
				log = append(log, d.Name)
			case "cache":
				cache = append(cache, d.Name)
			}
		}
	}
	if strings.Join(log, ",") != "nvme0n1,nvme1n1" || strings.Join(cache, ",") != "sda,sdb" {
		t.Errorf("log %v cache %v", log, cache)
	}
	if strings.Contains(strings.Join(rz.Argv(), " "), "nvme2n1") {
		t.Error("an in-use disk must never appear in a candidate")
	}
	// dRAID: 12 children, 2 parity → 8 data + 1 spare does not divide, 4 data + 2 spares... the layout must divide.
	dr := labels["dRAID2"].Design.Vdevs[0]
	if (12-dr.DraidSpares)%(dr.DraidData+2) != 0 {
		t.Errorf("draid layout does not divide: %dd %ds", dr.DraidData, dr.DraidSpares)
	}
	// Three disks: mirror + spare, raidz1, stripe — and nothing wider.
	small := Suggest("s", hdd[:3], nil)
	names := keysOf(candMap(small))
	if strings.Contains(strings.Join(names, " "), "RAIDZ2") || !strings.Contains(strings.Join(names, " "), "RAIDZ1") {
		t.Errorf("three disks: %v", names)
	}
	if Suggest("none", nil, nil) != nil {
		t.Error("no disks, no candidates")
	}
}

func candMap(c []Candidate) map[string]Candidate {
	m := map[string]Candidate{}
	for _, x := range c {
		m[x.Label] = x
	}
	return m
}

func keysOf(m map[string]Candidate) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	return k
}

const statusFixture = `  pool: tank
 state: ONLINE
  scan: scrub repaired 0B in 00:30:39 with 0 errors on Mon Jul 27 22:56:39 2026
config:

	NAME                                            STATE     READ WRITE CKSUM
	tank                                            ONLINE       0     0     0
	  raidz2-0                                      ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a1            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a2            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a3            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a4            ONLINE       0     0     0
	  mirror-1                                      DEGRADED     0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a5            ONLINE       0     0     0
	    /dev/disk/by-id/wwn-0x5000cca2a6            FAULTED      3    12     0  too many errors
	logs
	  mirror-2                                      ONLINE       0     0     0
	    /dev/disk/by-id/nvme-INTEL_PHAX1            ONLINE       0     0     0
	    /dev/disk/by-id/nvme-INTEL_PHAX2            ONLINE       0     0     0
	cache
	  /dev/disk/by-id/ata-Samsung_SSD_870_E1        ONLINE       0     0     0
	spares
	  /dev/disk/by-id/wwn-0x5000cca2a7              AVAIL

errors: No known data errors
`

func TestParsePoolStatus(t *testing.T) {
	root, err := ParsePoolStatus(statusFixture)
	if err != nil {
		t.Fatal(err)
	}
	if root.Name != "tank" || root.State != "ONLINE" || len(root.Children) != 5 {
		t.Fatalf("root %s %s with %d children", root.Name, root.State, len(root.Children))
	}
	kinds := []string{}
	for _, c := range root.Children {
		kinds = append(kinds, c.Kind())
	}
	if got := strings.Join(kinds, ","); got != "raidz2,mirror,logs,cache,spares" {
		t.Errorf("top-level kinds: %s", got)
	}
	m := root.Children[1]
	if m.State != "DEGRADED" || len(m.Children) != 2 {
		t.Errorf("mirror-1: %s with %d children", m.State, len(m.Children))
	}
	bad := m.Children[1]
	if bad.State != "FAULTED" || bad.Read != "3" || bad.Write != "12" || bad.Note != "too many errors" {
		t.Errorf("faulted leaf: %+v", *bad)
	}
	if root.Children[2].Children[0].Kind() != "mirror" || len(root.Children[2].Children[0].Children) != 2 {
		t.Error("logs must hold a mirror of two")
	}
	if sp := root.Children[4].Children[0]; sp.State != "AVAIL" || sp.Read != "" {
		t.Errorf("spare: %+v", *sp)
	}
	flat := root.Flatten()
	if !strings.Contains(flat, "    /dev/disk/by-id/wwn-0x5000cca2a6") || !strings.Contains(flat, "r/w/c 3/12/0") {
		t.Errorf("flatten:\n%s", flat)
	}
	if _, err := ParsePoolStatus("no pools available\n"); err == nil {
		t.Error("no config block must be an error")
	}
}

func TestParseSpec(t *testing.T) {
	disks := shelf(t)
	d, err := ParseSpec("tank", strings.Fields("raidz2 sdc sdd sde sdf mirror sdg sdh log mirror nvme0n1 nvme1n1 cache sda sdb spare sdi"), disks)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, v := range d.Vdevs {
		roles = append(roles, v.Role+":"+v.Kind+":"+itoa(len(v.Disks)))
	}
	if got := strings.Join(roles, " "); got != "data:raidz2:4 data:mirror:2 log:mirror:2 cache:stripe:2 spare:stripe:1" {
		t.Errorf("spec vdevs: %s", got)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("the spec must validate: %v", err)
	}
	if _, err := ParseSpec("t", []string{"mirror", "sdc", "sdzz"}, disks); err == nil || !strings.Contains(err.Error(), "sdzz") {
		t.Errorf("unknown disk must name itself: %v", err)
	}
	dr, err := ParseSpec("t", strings.Fields("draid2 sdc sdd sde sdf sdg sdh sdi sdj sdk sdl sdm sdn"), disks)
	if err != nil {
		t.Fatal(err)
	}
	if v := dr.Vdevs[0]; v.Kind != "draid2" || v.DraidData == 0 || dr.Validate() != nil {
		t.Errorf("bare draid2 gets an automatic layout: %+v %v", v, dr.Validate())
	}
	ex, _ := ParseSpec("t", strings.Fields("draid1:3d:6c:0s sdc sdd sde sdf sdg sdh"), disks)
	if v := ex.Vdevs[0]; v.Kind != "draid1" || v.DraidData != 3 || v.DraidSpares != 0 || v.draidSpec() != "draid1:3d:6c:0s" {
		t.Errorf("explicit draid spec: %+v → %s", v, v.draidSpec())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestBuilderMockCLI drives ListDisks and DryRun through fake lsblk/zpool on
// PATH — the production code path, no real disks.
func TestBuilderMockCLI(t *testing.T) {
	m := newMock(t)
	m.script("lsblk", `echo "lsblk $*" >> "$ZX_CMDLOG"
case "$*" in
  *ID-LINK*) echo "lsblk: unknown column: ID-LINK" >&2; exit 1 ;;
  *) cat <<'EOF'
{"blockdevices":[
 {"name":"sda","path":"/dev/sda","size":4000787030016,"model":"HGST HUS","serial":"A","rota":true,"tran":"sas","type":"disk","fstype":null,"mountpoint":null,"label":null},
 {"name":"sdb","path":"/dev/sdb","size":4000787030016,"model":"HGST HUS","serial":"B","rota":true,"tran":"sas","type":"disk","fstype":null,"mountpoint":null,"label":null},
 {"name":"loop0","path":"/dev/loop0","size":1073741824,"model":null,"serial":null,"rota":false,"tran":null,"type":"loop","fstype":null,"mountpoint":null,"label":null}
]}
EOF
  ;;
esac`)
	m.script("ls", `echo "ls $*" >> "$ZX_CMDLOG"
printf 'lrwxrwxrwx 1 root root 9 Sep 5 10:00 wwn-0xA -> ../../sda\nlrwxrwxrwx 1 root root 9 Sep 5 10:00 scsi-B -> ../../sdb\n'`)
	m.script("zpool", `echo "zpool $*" >> "$ZX_CMDLOG"
case "$1 $2" in
  "create -n") echo "would create 'lab' with the following layout:"; echo; echo "	lab"; echo "	  mirror"; echo "	    wwn-0xA"; echo "	    scsi-B" ;;
esac`)
	t.Setenv("ZXPLORE_BUILDER_ALL_DEVICES", "")
	disks, err := ListDisks(LocalHost())
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 {
		t.Fatalf("loop0 is pseudo and hidden: got %d disks", len(disks))
	}
	if disks[0].ByID != "/dev/disk/by-id/wwn-0xA" || disks[1].ByID != "/dev/disk/by-id/scsi-B" {
		t.Errorf("by-id fallback via ls: %q %q", disks[0].ByID, disks[1].ByID)
	}
	if !strings.Contains(m.log(), "lsblk -J -b -o "+lsblkColumns+"\n") {
		t.Errorf("the ID-LINK refusal must be retried without it:\n%s", m.log())
	}
	t.Setenv("ZXPLORE_BUILDER_ALL_DEVICES", "1")
	all, _ := ListDisks(LocalHost())
	if len(all) != 3 {
		t.Errorf("ALL_DEVICES=1 shows the loop device: %d", len(all))
	}
	d := Design{Name: "lab", Ashift: 12, Vdevs: []Vdev{{Kind: "mirror", Role: "data", Disks: disks}}}
	out, err := DryRun(LocalHost(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would create 'lab'") {
		t.Errorf("dry-run output: %q", out)
	}
	if !strings.Contains(m.log(), "zpool create -n -o ashift=12 lab mirror /dev/disk/by-id/wwn-0xA /dev/disk/by-id/scsi-B\n") {
		t.Errorf("dry-run argv:\n%s", m.log())
	}
	if err := CreatePool(LocalHost(), d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.log(), "zpool create -o ashift=12 lab mirror /dev/disk/by-id/wwn-0xA /dev/disk/by-id/scsi-B\n") {
		t.Errorf("create argv:\n%s", m.log())
	}
	if err := CreatePool(LocalHost(), Design{Name: "x"}); err == nil {
		t.Error("an invalid design must be refused before zpool is touched")
	}
}
