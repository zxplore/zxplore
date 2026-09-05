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
