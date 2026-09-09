//go:build gui

// gui_observe.go — the Observe tab (F5): one second of a pool at a time, and
// the verdicts. Live mode resamples every second in the background and lands
// the result on the UI thread; the layout never rebuilds itself while a
// sample is in flight.
//
// Top to bottom: pool picker, Live toggle and a Sample-now button; the gauge
// row (ARC, ops/bandwidth, latency, txg, ZIL, throttle, capacity); the
// verdicts, one row each — level chip, title, evidence, fix; the vdev table
// and the busiest datasets as monospace text; events; and a kstat browser
// (pick a group, see the counters with per-second rates).
package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

type observeUI struct {
	page     fyne.CanvasObject
	focus    fyne.Focusable
	poolSel  *widget.Select
	live     *widget.Check
	verdicts *fyne.Container
	gauges   *fyne.Container
	vdevs    *widget.Label
	datasets *widget.Label
	events   *widget.Label
	status   *widget.Label
	last     *Sample
	render   func(Sample)
	adminEv  []string // events read with admin rights; kept across samples
}

func levelAccent(level string) accentPair {
	switch level {
	case "red":
		return acRed
	case "gold":
		return acGold
	case "green":
		return acGreen
	}
	return acTopic
}

func observeTab(w fyne.Window, switchTab func(fyne.KeyName)) (fyne.CanvasObject, fyne.Focusable) {
	ui := newObserveUI(w, switchTab)
	return ui.page, ui.focus
}

func newObserveUI(_ fyne.Window, switchTab func(fyne.KeyName)) *observeUI {
	host := LocalHost()
	ui := &observeUI{}
	ui.poolSel = widget.NewSelect(nil, nil)
	ui.poolSel.PlaceHolder = "pick a pool"
	ui.status = widget.NewLabel("")
	ui.gauges = container.NewGridWrap(fyne.NewSize(390, 34))
	ui.verdicts = container.NewVBox()
	mono := func() *widget.Label {
		l := widget.NewLabel("")
		l.TextStyle = fyne.TextStyle{Monospace: true}
		return l
	}
	ui.vdevs, ui.datasets, ui.events = mono(), mono(), mono()

	sampling := false
	sampleOnce := func() {
		pool := ui.poolSel.Selected
		if pool == "" || sampling {
			return
		}
		sampling = true
		ui.status.SetText("sampling " + pool + " …")
		go func() {
			s, err := Observe(host, pool)
			fyne.Do(func() {
				sampling = false
				if err != nil {
					ui.status.SetText("✗ " + err.Error())
					return
				}
				ui.last = &s
				ui.render(s)
			})
		}()
	}
	ui.live = widget.NewCheck("Live (every second)", nil)
	sampleBtn := widget.NewButton("Sample now", sampleOnce)
	sampleBtn.Importance = widget.HighImportance
	ui.poolSel.OnChanged = func(string) { sampleOnce() }
	// The live loop: a ticker that asks for a sample only when the previous
	// one has landed, so a slow host never queues samples behind itself.
	ui.live.OnChanged = func(on bool) {
		if !on {
			return
		}
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for range tick.C {
				stop := true
				fyne.DoAndWait(func() {
					stop = !ui.live.Checked
					if !stop && !sampling {
						sampleOnce()
					}
				})
				if stop {
					return
				}
			}
		}()
	}

	// kstat browser
	groups := []string{"arcstats", "zil", "dmu_tx", "zfetchstats", "dbufstats", "dnodestats", "abdstats", "<pool>/txgs"}
	kstatSel := widget.NewSelect(groups, nil)
	kstatSel.PlaceHolder = "kstat group"
	kstatOut := mono()
	kstatSel.OnChanged = func(g string) {
		if g == "" {
			return
		}
		if g == "<pool>/txgs" {
			g = ui.poolSel.Selected + "/txgs"
		}
		go func() {
			a, err := readKstat(host, g)
			if err != nil {
				fyne.Do(func() { kstatOut.SetText("✗ " + err.Error()) })
				return
			}
			time.Sleep(time.Second)
			b, err := readKstat(host, g)
			if err != nil {
				fyne.Do(func() { kstatOut.SetText("✗ " + err.Error()) })
				return
			}
			text := KstatTable(a, b, 1)
			if strings.HasSuffix(g, "/txgs") {
				if raw, err := readRaw(host, g); err == nil {
					text = raw
				}
			}
			fyne.Do(func() { kstatOut.SetText(text) })
		}()
	}

	ui.render = func(s Sample) {
		ui.status.SetText(s.Pool + " sampled at " + s.At.Format("15:04:05"))
		ui.gauges.Objects = nil
		for _, g := range s.gauges() {
			ui.gauges.Add(container.NewPadded(chip(g, acCyan)))
		}
		ui.gauges.Refresh()
		ui.verdicts.Objects = nil
		for _, v := range Judge(s) {
			title := widget.NewLabelWithStyle(v.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			body := container.NewVBox(title)
			if v.Evidence != "" {
				ev := widget.NewLabel(v.Evidence)
				ev.Wrapping = fyne.TextWrapWord
				body.Add(ev)
			}
			if v.Fix != "" {
				fx := smallText("fix: "+v.Fix, levelAccent(v.Level), 12, false)
				body.Add(fx)
			}
			row := container.NewBorder(nil, nil, container.NewVBox(container.NewPadded(chip(strings.ToUpper(v.Level), levelAccent(v.Level)))), nil, body)
			ui.verdicts.Add(row)
			ui.verdicts.Add(gap(6))
		}
		ui.verdicts.Refresh()
		var vb strings.Builder
		vb.WriteString("vdev                                  ops r / w     bandwidth r / w         total wait r / w     disk wait r / w      queue sync / async\n")
		for _, x := range s.Vdevs {
			vb.WriteString(vdevLine(x))
		}
		ui.vdevs.SetText(vb.String())
		var db strings.Builder
		n := 0
		for _, d := range s.Datasets {
			if d.Reads+d.Writes+d.NRead+d.NWritten+d.ZilCommits == 0 || n == 10 {
				continue
			}
			n++
			db.WriteString(datasetLine(d))
		}
		if db.Len() == 0 {
			db.WriteString("no dataset moved a byte this second")
		}
		ui.datasets.SetText(db.String())
		// An unprivileged sample usually has no events; an admin read from
		// the button outlives the samples and feeds the ereport verdict.
		if len(s.Events) == 0 && len(ui.adminEv) > 0 {
			s.Events = ui.adminEv
		}
		ev := strings.Join(s.Events, "\n")
		for _, w := range s.Warnings {
			if strings.HasPrefix(w, "zpool events") && len(ui.adminEv) > 0 {
				continue
			}
			ev += "\n! " + w
		}
		ui.events.SetText(strings.TrimSpace(ev))
	}
	readEvents := widget.NewButton("Read events as admin", func() {
		go func() {
			ev, err := EventsElevated(host, 30)
			fyne.Do(func() {
				if err != nil {
					ui.events.SetText("✗ " + err.Error())
					return
				}
				ui.adminEv = ev
				if ui.last != nil {
					ui.last.Events = ev
					ui.render(*ui.last)
				} else {
					ui.events.SetText(strings.Join(ev, "\n"))
				}
			})
		}()
	})

	refreshPools := func() {
		if names, err := ListPools(host); err == nil {
			ui.poolSel.Options = names
			ui.poolSel.Refresh()
			if ui.poolSel.Selected == "" && len(names) > 0 {
				ui.poolSel.SetSelected(names[0])
			}
		}
	}

	top := container.NewHBox(heading("OBSERVE — one second of a pool, and what it means", acElectric), gap(1), ui.poolSel, sampleBtn, ui.live, gap(1), ui.status)
	body := container.NewVBox(
		top,
		gap(10),
		ui.gauges,
		gap(14),
		heading("VERDICTS", acRed),
		gap(6),
		ui.verdicts,
		gap(14),
		widget.NewSeparator(),
		gap(8),
		heading("VDEVS", acBlue),
		gap(4),
		ui.vdevs,
		gap(14),
		heading("DATASETS — busiest this second", acGreen),
		gap(4),
		ui.datasets,
		gap(14),
		container.NewHBox(heading("EVENTS", acGold), gap(1), readEvents),
		gap(4),
		ui.events,
		gap(14),
		widget.NewSeparator(),
		gap(8),
		container.NewHBox(heading("KSTATS — a group with per-second rates", acTopic), gap(1), kstatSel),
		gap(4),
		kstatOut,
		gap(20),
	)
	// Focusable for the tab switch: the pool select accepts focus.
	ui.focus = ui.poolSel
	_ = switchTab
	ui.page = container.NewVScroll(container.NewPadded(body))
	refreshPools()
	return ui
}

func vdevLine(x VdevIO) string {
	name := x.Name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return fmt.Sprintf("%-36s %5d / %-5d   %10s / %-10s   %8s / %-8s   %8s / %-8s   %8s / %s\n", name, x.ROps, x.WOps,
		fmtRate(float64(x.RBw)), fmtRate(float64(x.WBw)), msOf(x.TotalR), msOf(x.TotalW), msOf(x.DiskR), msOf(x.DiskW),
		msOf(maxI(x.SyncqR, x.SyncqW)), msOf(maxI(x.AsyncqR, x.AsyncqW)))
}

func datasetLine(d DatasetIO) string {
	return fmt.Sprintf("%-40s r %5.0f ops %10s   w %5.0f ops %10s   sync %4.0f/s %s\n", d.Name, d.Reads, fmtRate(d.NRead),
		d.Writes, fmtRate(d.NWritten), d.ZilCommits, fmtRate(d.ZilNormalBytes+d.ZilSlogBytes))
}
