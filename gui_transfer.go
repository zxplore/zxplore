// gui_transfer.go — the GUI transfer view: two dataset panes side by side.
//
// Each pane lists a host's datasets and can be re-pointed at a remote
// ([user@]host:pool). Pick a source in one pane, a destination in the other,
// and "Replicate →" sends the source's latest snapshot into the destination
// (incremental when a common snapshot exists). ZFS send/recv needs root, and
// the GUI runs as the user, so the transfer is elevated via pkexec (a polkit
// prompt), keeping the GUI itself unprivileged (and able to reach the display).
package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type xferPane struct {
	host     Host
	datasets []Dataset
	sel      int
	list     *widget.List
	title    *widget.Label
}

func newXferPane() *xferPane {
	p := &xferPane{host: LocalHost(), sel: -1}
	p.title = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.list = widget.NewList(
		func() int { return len(p.datasets) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			d := p.datasets[i]
			o.(*widget.Label).SetText(fmt.Sprintf("%s   %s/%s  ×%d", d.Name, d.Used, d.Refer, d.Snaps))
		},
	)
	p.list.OnSelected = func(i widget.ListItemID) { p.sel = int(i) }
	p.reload()
	return p
}

func (p *xferPane) reload() {
	ds, err := ListDatasets(p.host)
	p.datasets = ds
	if p.sel >= len(p.datasets) {
		p.sel = -1
	}
	p.list.Refresh()
	if err != nil {
		p.title.SetText("● " + p.host.Label() + "   (" + err.Error() + ")")
	} else {
		p.title.SetText("● " + p.host.Label())
	}
}

func (p *xferPane) selectedName() string {
	if p.sel >= 0 && p.sel < len(p.datasets) {
		return p.datasets[p.sel].Name
	}
	return ""
}

func (p *xferPane) view(onConnect func()) fyne.CanvasObject {
	connectBtn := widget.NewButton("Connect…", onConnect)
	refreshBtn := widget.NewButton("Refresh", func() { p.reload() })
	top := container.NewBorder(nil, nil, p.title, container.NewHBox(refreshBtn, connectBtn))
	return container.NewBorder(top, nil, nil, nil, p.list)
}

func transferTab(w fyne.Window) fyne.CanvasObject {
	left := newXferPane()
	right := newXferPane()

	connect := func(p *xferPane) {
		e := widget.NewEntry()
		e.SetPlaceHolder("user@host:pool   (blank = local)")
		dialog.ShowForm("Connect this pane", "Connect", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Target", e)},
			func(ok bool) {
				if !ok {
					return
				}
				v := strings.TrimSpace(e.Text)
				if v == "" {
					p.host = LocalHost()
				} else {
					p.host = ParseTarget(v).Host()
				}
				p.sel = -1
				p.reload()
			}, w)
	}

	replicate := func(src, dst *xferPane) {
		s := src.selectedName()
		if s == "" {
			dialog.ShowInformation("Replicate", "Select a SOURCE dataset (highlight it in the source pane).", w)
			return
		}
		d := dst.selectedName()
		if d == "" {
			dialog.ShowInformation("Replicate", "Select a DESTINATION dataset/pool in the other pane (the source lands under it).", w)
			return
		}
		// Resolve the source snapshot (newest, or take one).
		snap := ""
		if snaps, _ := ListSnapshots(src.host, s); len(snaps) > 0 {
			snap = snaps[len(snaps)-1].Name
		} else if sn, err := SnapshotNow(src.host, s, "zx-"+time.Now().Format("20060102-150405")); err == nil {
			snap = sn
		} else {
			dialog.ShowError(fmt.Errorf("snapshot failed: %v", err), w)
			return
		}
		leaf := s
		if i := strings.LastIndexByte(s, '/'); i >= 0 {
			leaf = s[i+1:]
		}
		dstPath := d + "/" + leaf
		pipeline := ReplicatePipeline(src.host, snap, dst.host, dstPath)

		dialog.ShowConfirm("Replicate",
			fmt.Sprintf("Send\n  %s\nto\n  %s:%s\n\n(runs as root via pkexec)", snap, dst.host.Label(), dstPath),
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					out, err := exec.Command("pkexec", "sh", "-c", pipeline).CombinedOutput()
					fyne.Do(func() {
						if err != nil {
							dialog.ShowError(fmt.Errorf("replicate failed: %v\n%s", err, string(out)), w)
							return
						}
						dst.reload()
						dialog.ShowInformation("Replicate", "✓ replicated → "+dst.host.Label()+":"+dstPath, w)
					})
				}()
			}, w)
	}

	btnLR := widget.NewButton("Replicate  →", func() { replicate(left, right) })
	btnRL := widget.NewButton("←  Replicate", func() { replicate(right, left) })
	bar := container.NewCenter(container.NewHBox(btnLR, widget.NewLabel("        "), btnRL))

	split := container.NewHSplit(left.view(func() { connect(left) }), right.view(func() { connect(right) }))
	split.SetOffset(0.5)
	return container.NewBorder(nil, bar, nil, nil, split)
}
