//go:build gui

// gui_observe_shot_test.go — renders the Observe tab with a crafted sample
// when ZX_SHOT names a file; the eye's check on spacing, like the Builder's.
package main

import (
	"image/png"
	"os"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

func TestObserveScreenshot(t *testing.T) {
	out := os.Getenv("ZX_SHOT")
	if out == "" {
		t.Skip("set ZX_SHOT=/path/to.png to render the Observe tab")
	}
	m := newMock(t)
	// No pools from the mock: the picker's auto-select would start a real
	// sample in a goroutine, and its fyne.Do render racing the software
	// renderer's text shaping is what crashed harfbuzz here — in the app
	// fyne.Do serialises onto the UI thread and no such race exists.
	m.script("zpool", `exit 0`)
	dedupLister = func(Sample) (string, error) { return "tank\toff\n", nil }
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()
	ui := newObserveUI(w, func(fyne.KeyName) {})
	ui.poolSel.Options = []string{"tank"}
	w.SetContent(ui.page)
	w.Resize(fyne.NewSize(1500, 1000))
	s := baseSample()
	s.At = time.Now()
	s.HasLog = false
	s.Rate["zil.zil_itx_metaslab_normal_bytes"] = 2.1e7
	s.Rate["zil.zil_commit_count"] = 56
	s.Vdevs[2].DiskR, s.Vdevs[2].DiskW = 90e6, 90e6
	s.Props["capacity"] = "91"
	s.Datasets = []DatasetIO{{Name: "tank/vm/db0", Reads: 32, NRead: 1.3e5, Writes: 62, NWritten: 3.7e6, ZilCommits: 56, ZilNormalBytes: 2.1e7}, {Name: "tank/home", Reads: 97, NRead: 1.8e5, Writes: 3, NWritten: 1.1e4}}
	s.Events = []string{"Sep 5 2026 11:12:38 sysevent.fs.zfs.history_event", "Sep 5 2026 11:27:38 ereport.fs.zfs.io"}
	s.Warnings = []string{"zpool events: permission denied (usually needs root; the rest of the sample does not)"}
	ui.render(s)
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
