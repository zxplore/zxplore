//go:build gui

// gui_builder_shot_test.go — renders the Builder tab to a PNG with the
// software painter when ZX_SHOT names a file. A look, not an assertion:
// spacing and alignment are judged by eye, and this is how to get the eye
// on it without a display.
package main

import (
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

func TestBuilderScreenshot(t *testing.T) {
	out := os.Getenv("ZX_SHOT")
	if out == "" {
		t.Skip("set ZX_SHOT=/path/to.png to render the Builder tab")
	}
	m := newMock(t)
	m.script("lsblk", `cat <<'EOF2'
{"blockdevices":[
 {"name":"nvme0n1","path":"/dev/nvme0n1","size":1600321314816,"model":"INTEL SSDPF2KX016T1","serial":"P1","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-INTEL_P1"},
 {"name":"nvme1n1","path":"/dev/nvme1n1","size":1600321314816,"model":"INTEL SSDPF2KX016T1","serial":"P2","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-INTEL_P2"},
 {"name":"nvme2n1","path":"/dev/nvme2n1","size":2000398934016,"model":"Samsung SSD 990","serial":"S1","rota":false,"tran":"nvme","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"nvme-Samsung_S1","children":[{"name":"nvme2n1p1","fstype":"vfat","mountpoint":"/boot/efi"},{"name":"nvme2n1p2","fstype":"zfs_member","mountpoint":null,"label":"rpool"}]},
 {"name":"sda","path":"/dev/sda","size":1920383410176,"model":"Samsung SSD 870","serial":"E1","rota":false,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"ata-Samsung_870_E1"},
 {"name":"sdb","path":"/dev/sdb","size":1920383410176,"model":"Samsung SSD 870","serial":"E2","rota":false,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"ata-Samsung_870_E2"},
 {"name":"sdc","path":"/dev/sdc","size":20000588955648,"model":"WDC WD201KFGX","serial":"W1","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a1"},
 {"name":"sdd","path":"/dev/sdd","size":20000588955648,"model":"WDC WD201KFGX","serial":"W2","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a2"},
 {"name":"sde","path":"/dev/sde","size":20000588955648,"model":"WDC WD201KFGX","serial":"W3","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a3"},
 {"name":"sdf","path":"/dev/sdf","size":20000588955648,"model":"WDC WD201KFGX","serial":"W4","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a4"},
 {"name":"sdg","path":"/dev/sdg","size":20000588955648,"model":"WDC WD201KFGX","serial":"W5","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a5"},
 {"name":"sdh","path":"/dev/sdh","size":20000588955648,"model":"WDC WD201KFGX","serial":"W6","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a6"},
 {"name":"sdi","path":"/dev/sdi","size":20000588955648,"model":"WDC WD201KFGX","serial":"W7","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a7"},
 {"name":"sdj","path":"/dev/sdj","size":20000588955648,"model":"WDC WD201KFGX","serial":"W8","rota":true,"tran":"sata","type":"disk","fstype":null,"mountpoint":null,"label":null,"id-link":"wwn-0x5000cca2a8"}
]}
EOF2`)
	m.script("zpool", `case "$*" in "list -H -o name") echo rpool ;; esac`)
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()
	ui := newBuilderUI(w, func(fyne.KeyName) {}, func() {})
	w.SetContent(ui.page)
	w.Resize(fyne.NewSize(1500, 950))
	for i, d := range ui.st.disks {
		if d.Kind == "hdd" {
			ui.toggle(i)
		}
	}
	img := software.RenderCanvas(w.Canvas(), theme.DefaultTheme())
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
