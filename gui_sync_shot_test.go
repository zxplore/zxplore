//go:build gui

// gui_sync_shot_test.go — renders the Auto Sync tab when ZX_SHOT names a file.
// The eye's check on spacing and wording, like the Builder's and Observe's.
//
// It points the inventories at a temp dir so the shot is reproducible and
// never reads or writes the host's real /etc/zxplore.
package main

import (
	"image/color"
	"image/png"
	"os"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

func TestSyncScreenshot(t *testing.T) {
	out := os.Getenv("ZX_SHOT")
	if out == "" {
		t.Skip("set ZX_SHOT=/path/to.png to render the Auto Sync tab")
	}
	m := newMock(t)
	// systemctl and zfs are faked so the rows show a plausible enabled job
	// without touching the host's units or pools.
	m.script("systemctl", `case "$*" in
*"is-enabled"*) echo enabled ;;
*"NextElapseUSecRealtime"*) echo "Wed 2026-09-09 03:00:00 PDT" ;;
*"ExecMainStatus"*) echo 0 ;;
*) exit 0 ;;
esac`)
	m.script("zfs", `case "$*" in
"list -H -t snapshot -o name -s creation -r rpool/backup/fiend")
  printf 'rpool/backup/fiend/srv@autosnap_2026-09-08_22:39:02_daily\n' ;;
*) exit 0 ;;
esac`)
	m.script("ssh", sshFixture)

	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()
	w.SetContent(syncTab(w))
	w.Resize(fyne.NewSize(1400, 820))

	// cardColor() asks the APP for its variant, while RenderCanvas is handed a
	// theme directly. Left to themselves the two disagree: the test app reports
	// light, the cards paint their light colour, and a dark render then puts
	// light text on them. The result is an unreadable shot that is an artifact
	// of the harness, not a defect in the tab. Render in the variant the app
	// actually reports so they agree.
	v := fyne.ThemeVariant(theme.VariantLight)
	if variantDark() {
		v = theme.VariantDark
	}
	img := software.RenderCanvas(w.Canvas(), shotTheme{theme.DefaultTheme(), v})
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// shotTheme pins a variant so a rendered screenshot is internally consistent.
type shotTheme struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (t shotTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.Theme.Color(n, t.v)
}
