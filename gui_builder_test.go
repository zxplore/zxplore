//go:build gui

// gui_builder_test.go — the Builder tab built under Fyne's test driver over
// the mock lsblk/zpool: the shelf lists what lsblk said, a tick produces
// candidates and a layout, the command line the operator sees is the engine's
// argv, and an in-use disk cannot be ticked.
package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestBuilderTabUnderTestDriver(t *testing.T) {
	m := newMock(t)
	m.script("lsblk", `echo "lsblk $*" >> "$ZX_CMDLOG"
cat <<'EOF2'
{"blockdevices":[
 {"name":"sda","path":"/dev/sda","size":4000787030016,"model":"HGST HUS","serial":"A","rota":true,"tran":"sas","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0xA"},
 {"name":"sdb","path":"/dev/sdb","size":4000787030016,"model":"HGST HUS","serial":"B","rota":true,"tran":"sas","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0xB"},
 {"name":"sdc","path":"/dev/sdc","size":4000787030016,"model":"HGST HUS","serial":"C","rota":true,"tran":"sas","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0xC"},
 {"name":"nvme0n1","path":"/dev/nvme0n1","size":500107862016,"model":"SYS","serial":"S","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-SYS",
  "children":[{"name":"nvme0n1p1","fstype":"zfs_member","mountpoint":null,"label":"rpool"}]}
]}
EOF2`)
	m.script("zpool", `echo "zpool $*" >> "$ZX_CMDLOG"
case "$*" in
  "list -H -o name") echo rpool ;;
  "status -P rpool") printf 'config:\n\n\tNAME STATE READ WRITE CKSUM\n\trpool ONLINE 0 0 0\n\t  /dev/disk/by-id/nvme-SYS-part1 ONLINE 0 0 0\n\n' ;;
esac`)
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()
	created := 0
	ui := newBuilderUI(w, func(fyne.KeyName) {}, func() { created++ })
	w.SetContent(ui.page)

	if n := len(ui.st.disks); n != 4 {
		t.Fatalf("shelf: %d disks, want 4", n)
	}
	if !ui.dryRun.Disabled() {
		t.Error("nothing ticked: the verbs must be disabled")
	}
	// The shelf is in name order, not lsblk order — find rows by name.
	idx := func(name string) int {
		for i, d := range ui.st.disks {
			if d.Name == name {
				return i
			}
		}
		t.Fatalf("%s not on the shelf", name)
		return -1
	}
	// The system disk is in use and cannot be ticked.
	ui.toggle(idx("nvme0n1"))
	if len(ui.st.cands) != 0 {
		t.Error("ticking an in-use disk must do nothing")
	}
	ui.toggle(idx("sda"))
	ui.toggle(idx("sdb"))
	ui.toggle(idx("sdc"))
	if len(ui.st.cands) == 0 {
		t.Fatal("three free disks must yield candidates")
	}
	if len(ui.st.design.Vdevs) == 0 {
		t.Fatal("the first candidate loads as the layout")
	}
	cmd := ui.command.Text
	if !strings.HasPrefix(cmd, "zpool create -o ashift=12 -O compression=zstd tank ") || !strings.Contains(cmd, "/dev/disk/by-id/wwn-0xA") {
		t.Errorf("command line: %q", cmd)
	}
	if ui.dryRun.Disabled() || ui.create.Disabled() {
		t.Error("a valid design enables dry run and create")
	}
	// Un-ticking one disk reflows: two disks → a mirror, no raidz2.
	ui.toggle(idx("sdc"))
	labels := ""
	for _, c := range ui.st.cands {
		labels += c.Label + " "
	}
	if !strings.Contains(labels, "Mirror") || strings.Contains(labels, "RAIDZ2") {
		t.Errorf("two disks: %s", labels)
	}
	if created != 0 {
		t.Error("nothing was created")
	}
}

// TestCreateGate covers the gate the Create button runs. The bug it guards:
// the create path used confirmTyped, so the only field that mentioned a pool
// name demanded the DEFAULT name back, and typing the name you actually wanted
// was rejected with `name mismatch — expected "tank"` (fiend, 2026-09-12 — six
// 8TB disks picked as raidz3 with a mirrored log, and no way to name the pool).
func TestCreateGate(t *testing.T) {
	disks := shelf(t)
	// The shelf that found it: six data disks as raidz3, two NVMe as a log.
	d := Design{Name: "tank", Ashift: 12, Compression: "zstd", Vdevs: []Vdev{
		{Kind: "raidz3", Role: "data", Disks: byName(disks, "sdc", "sdd", "sde", "sdf", "sdg", "sdh")},
		{Kind: "mirror", Role: "log", Disks: byName(disks, "nvme0n1", "nvme1n1")},
	}}
	if n := len(d.Vdevs[0].Disks) + len(d.Vdevs[1].Disks); n != 8 {
		t.Fatalf("fixture: %d disks in the design, want 8", n)
	}

	for _, tc := range []struct {
		why      string
		name     string
		ack      string
		wantName string
		wantErr  string
	}{
		{why: "the reported failure: a name that is not the default is accepted",
			name: "vault", ack: "ERASE", wantName: "vault"},
		{why: "the default name still works, for anyone who leaves it alone",
			name: "tank", ack: "ERASE", wantName: "tank"},
		{why: "surrounding whitespace is trimmed off both fields",
			name: "  vault  ", ack: " ERASE ", wantName: "vault"},
		{why: "the old gate's answer is now a refusal: the name is not the ack",
			name: "vault", ack: "vault", wantErr: "type ERASE"},
		{why: "the disk count is in the refusal, so it says what is at stake",
			name: "vault", ack: "", wantErr: "8 disk(s) will be erased"},
		{why: "an empty name is caught here, not by zpool",
			name: "", ack: "ERASE", wantErr: "needs a name"},
		{why: "a zpool keyword is not a pool name",
			name: "mirror", ack: "ERASE", wantErr: "keyword"},
		{why: "a name zpool's own parser refuses",
			name: "c0data", ack: "ERASE", wantErr: "device name"},
		{why: "a name with a space in it",
			name: "my pool", ack: "ERASE", wantErr: "not a valid pool name"},
	} {
		got, err := createGate(d, tc.name, tc.ack)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: createGate(%q, %q) errored: %v", tc.why, tc.name, tc.ack, err)
		case tc.wantErr == "" && got.Name != tc.wantName:
			t.Errorf("%s: name %q, want %q", tc.why, got.Name, tc.wantName)
		case tc.wantErr != "" && err == nil:
			t.Errorf("%s: createGate(%q, %q) returned no error, want %q",
				tc.why, tc.name, tc.ack, tc.wantErr)
		case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("%s: error %q does not contain %q", tc.why, err, tc.wantErr)
		}
	}

	// The vdevs must survive the gate untouched: only the name is editable.
	got, err := createGate(d, "vault", "ERASE")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Vdevs) != 2 || got.Vdevs[0].Kind != "raidz3" || got.Vdevs[1].Role != "log" {
		t.Errorf("the gate altered the layout: %+v", got.Vdevs)
	}
	if !strings.Contains(strings.Join(got.Argv(), " "), " vault ") {
		t.Errorf("the typed name did not reach the argv: %s", strings.Join(got.Argv(), " "))
	}
}
