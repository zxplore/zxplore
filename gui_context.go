// gui_context.go — the right-click / context menu on a dataset.
//
// One gesture for the whole ZFS lifecycle: snapshot, clone/duplicate, replicate
// or back up to a server, create a boot environment, roll back, edit properties,
// destroy. Every mutation runs privileged (pkexec / delegated ssh); destructive
// ones confirm first. This is what makes zxplore feel like a real console — the
// filesystem as a right-click-managed solution.
package main

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// latestOrNewSnapshot returns the newest snapshot of a dataset, taking one if it
// has none (so clone/replicate always have a source).
func latestOrNewSnapshot(h Host, dataset string) (string, error) {
	if snaps, _ := ListSnapshots(h, dataset); len(snaps) > 0 {
		return snaps[len(snaps)-1].Name, nil
	}
	return SnapshotNow(h, dataset, "zx-"+time.Now().Format("20060102-150405"))
}

func datasetLeaf(dataset string) string {
	if i := strings.LastIndexByte(dataset, '/'); i >= 0 {
		return dataset[i+1:]
	}
	return dataset
}

// datasetContextMenu builds the right-click menu for a dataset. refresh reloads
// the views after any change; onEdit flips the detail pane into edit mode.
func datasetContextMenu(h Host, dataset string, w fyne.Window, refresh, onEdit func()) *fyne.Menu {
	// runOp runs a privileged op off the UI thread, reports the result, refreshes.
	runOp := func(verb string, fn func() error) {
		go func() {
			err := fn()
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, w)
				} else {
					dialog.ShowInformation(verb, verb+" ✓", w)
				}
				refresh()
			})
		}()
	}
	promptName := func(title, label, def string, do func(string)) {
		e := widget.NewEntry()
		e.SetText(def)
		dialog.ShowForm(title, "OK", "Cancel", []*widget.FormItem{widget.NewFormItem(label, e)},
			func(ok bool) {
				if ok && strings.TrimSpace(e.Text) != "" {
					do(strings.TrimSpace(e.Text))
				}
			}, w)
	}

	snapNow := fyne.NewMenuItem("Snapshot now…", func() {
		promptName("Snapshot "+dataset, "Name", "snap-"+time.Now().Format("20060102-150405"), func(n string) {
			runOp("snapshot", func() error { _, e := SnapshotNow(h, dataset, n); return e })
		})
	})
	clone := fyne.NewMenuItem("Clone / duplicate…", func() {
		promptName("Clone "+dataset, "New dataset", dataset+"-copy", func(target string) {
			runOp("clone", func() error {
				snap, err := latestOrNewSnapshot(h, dataset)
				if err != nil {
					return err
				}
				return Clone(h, snap, target)
			})
		})
	})
	replicate := fyne.NewMenuItem("Replicate / back up to…", func() {
		showServerManager(w, func(s Server) {
			dstPath := s.Path + "/" + datasetLeaf(dataset)
			dialog.ShowConfirm("Replicate",
				fmt.Sprintf("Send  %s\nto  %s:%s ?", dataset, s.sshTarget(), dstPath), func(ok bool) {
					if !ok {
						return
					}
					runOp("replicate", func() error {
						snap, err := latestOrNewSnapshot(h, dataset)
						if err != nil {
							return err
						}
						return RunReplicate(ReplicatePipeline(h, snap, s.toHost(), dstPath))
					})
				}, w)
		})
	})
	be := fyne.NewMenuItem("Create boot environment…", func() {
		promptName("Create boot environment", "BE name", "", func(n string) {
			runOp("boot environment", func() error { return CreateBootEnv(h, n) })
		})
	})
	rollback := fyne.NewMenuItem("Roll back to latest snapshot", func() {
		snaps, _ := ListSnapshots(h, dataset)
		if len(snaps) == 0 {
			dialog.ShowInformation("Rollback", "No snapshots to roll back to.", w)
			return
		}
		snap := snaps[len(snaps)-1].Name
		dialog.ShowConfirm("Roll back",
			"Roll back "+dataset+" to\n  "+snap+"\n\n⚠ destroys newer snapshots. Continue?", func(ok bool) {
				if ok {
					runOp("rollback", func() error { return Rollback(h, snap) })
				}
			}, w)
	})
	edit := fyne.NewMenuItem("Edit properties", func() {
		if onEdit != nil {
			onEdit()
		}
	})
	destroy := fyne.NewMenuItem("Destroy…", func() {
		dialog.ShowConfirm("Destroy",
			"Permanently destroy  "+dataset+"  (recursively)?", func(ok bool) {
				if ok {
					runOp("destroy", func() error { return DestroyDataset(h, dataset) })
				}
			}, w)
	})

	newChild := fyne.NewMenuItem("Create child dataset…", func() {
		promptName("New dataset under "+dataset, "Name", "", func(n string) {
			runOp("create dataset", func() error { return CreateDataset(h, dataset+"/"+n, "") })
		})
	})
	newVol := fyne.NewMenuItem("Create volume (zvol)…", func() {
		nameE := widget.NewEntry()
		nameE.SetPlaceHolder("name")
		sizeE := widget.NewEntry()
		sizeE.SetPlaceHolder("size e.g. 10G")
		dialog.ShowForm("New volume under "+dataset, "Create", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Name", nameE), widget.NewFormItem("Size", sizeE)},
			func(ok bool) {
				if ok && strings.TrimSpace(nameE.Text) != "" && strings.TrimSpace(sizeE.Text) != "" {
					runOp("create volume", func() error {
						return CreateDataset(h, dataset+"/"+strings.TrimSpace(nameE.Text), strings.TrimSpace(sizeE.Text))
					})
				}
			}, w)
	})
	renameItem := fyne.NewMenuItem("Rename…", func() {
		promptName("Rename "+dataset, "New full name", dataset, func(n string) {
			runOp("rename", func() error { return RenameDataset(h, dataset, n) })
		})
	})
	mountItem := fyne.NewMenuItem("Mount", func() {
		runOp("mount", func() error { return SetMounted(h, dataset, true) })
	})
	unmountItem := fyne.NewMenuItem("Unmount", func() {
		runOp("unmount", func() error { return SetMounted(h, dataset, false) })
	})

	unlock := fyne.NewMenuItem("Unlock (load key)…", func() {
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("Unlock "+dataset, "Unlock", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Passphrase", pw)}, func(ok bool) {
				if ok {
					runOp("unlock", func() error { return LoadKey(h, dataset, pw.Text) })
				}
			}, w)
	})
	lock := fyne.NewMenuItem("Lock (unload key)", func() {
		runOp("lock", func() error { return UnloadKey(h, dataset) })
	})
	changeKey := fyne.NewMenuItem("Change passphrase…", func() {
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("Change passphrase for "+dataset, "Change", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("New passphrase", pw)}, func(ok bool) {
				if ok && pw.Text != "" {
					runOp("change passphrase", func() error { return ChangeKey(h, dataset, pw.Text) })
				}
			}, w)
	})
	newEnc := fyne.NewMenuItem("Create encrypted child…", func() {
		nameE := widget.NewEntry()
		nameE.SetPlaceHolder("name")
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("New encrypted dataset under "+dataset, "Create", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Name", nameE), widget.NewFormItem("Passphrase", pw)}, func(ok bool) {
				if ok && strings.TrimSpace(nameE.Text) != "" && pw.Text != "" {
					runOp("create encrypted", func() error {
						return CreateEncrypted(h, dataset+"/"+strings.TrimSpace(nameE.Text), pw.Text)
					})
				}
			}, w)
	})
	enc := fyne.NewMenuItem("Encryption", nil)
	enc.ChildMenu = fyne.NewMenu("", unlock, lock, changeKey, newEnc)

	return fyne.NewMenu("",
		newChild, newVol, renameItem, mountItem, unmountItem, enc,
		fyne.NewMenuItemSeparator(),
		snapNow, clone, replicate, be,
		fyne.NewMenuItemSeparator(), rollback, edit,
		fyne.NewMenuItemSeparator(), destroy)
}
