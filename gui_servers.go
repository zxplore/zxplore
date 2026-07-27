// gui_servers.go — the server manager (WinSCP-style saved sessions).
//
// showServerManager lists saved servers and lets you connect one; serverEditDialog
// adds/edits one with key-first auth: generate a key, paste one, or point at a
// file, then Authorize it on the server with a one-time password (never stored),
// and Test the connection with a clear result. Backed by servers.go.
package main

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// showServerManager lists saved servers; Connect calls onConnect with the chosen
// one. New/Edit open the editor; Delete removes. Reloads the list after edits.
func showServerManager(w fyne.Window, onConnect func(Server)) {
	servers := LoadServers()
	sel := -1

	var list *widget.List
	list = widget.NewList(
		func() int { return len(servers) },
		func() fyne.CanvasObject { return widget.NewLabel("t") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			s := servers[i]
			key := "no key"
			if s.KeyPath != "" {
				key = "key set"
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%s   —   %s   (%s)", s.Name, s.toHost().SSH, key))
		},
	)
	list.OnSelected = func(i widget.ListItemID) { sel = int(i) }

	reload := func() {
		servers = LoadServers()
		if sel >= len(servers) {
			sel = -1
		}
		list.UnselectAll()
		list.Refresh()
	}

	newBtn := widget.NewButton("＋ New server", func() {
		serverEditDialog(w, Server{Port: 22}, true, reload)
	})
	editBtn := widget.NewButton("Edit", func() {
		if sel >= 0 && sel < len(servers) {
			serverEditDialog(w, servers[sel], false, reload)
		}
	})
	delBtn := widget.NewButton("Delete", func() {
		if sel < 0 || sel >= len(servers) {
			return
		}
		name := servers[sel].Name
		dialog.ShowConfirm("Delete server", "Remove saved server \""+name+"\"?", func(ok bool) {
			if ok {
				_ = SaveServers(DeleteServer(servers, name))
				reload()
			}
		}, w)
	})
	delBtn.Importance = widget.DangerImportance

	var dlg dialog.Dialog
	connectBtn := widget.NewButton("Connect →", func() {
		if sel >= 0 && sel < len(servers) {
			s := servers[sel]
			dlg.Hide()
			onConnect(s)
		}
	})
	connectBtn.Importance = widget.HighImportance

	buttons := container.NewHBox(newBtn, editBtn, delBtn, widget.NewLabel("   "), connectBtn)
	body := container.NewBorder(
		widget.NewLabelWithStyle("Saved servers — select one, then Connect (or New to add)",
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buttons, nil, nil, list)
	dlg = dialog.NewCustom("Servers", "Close", body, w)
	dlg.Resize(fyne.NewSize(680, 460))
	dlg.Show()
}

// serverEditDialog edits one server. onSaved runs after a successful Save.
func serverEditDialog(w fyne.Window, srv Server, isNew bool, onSaved func()) {
	name := widget.NewEntry()
	name.SetText(srv.Name)
	host := widget.NewEntry()
	host.SetText(srv.Host)
	host.SetPlaceHolder("host or IP")
	port := widget.NewEntry()
	if srv.Port == 0 {
		srv.Port = 22
	}
	port.SetText(strconv.Itoa(srv.Port))
	user := widget.NewEntry()
	user.SetText(srv.User)
	user.SetPlaceHolder("ssh user (e.g. zexp)")
	path := widget.NewEntry()
	path.SetText(srv.Path)
	path.SetPlaceHolder("pool or pool/dataset")
	jump := widget.NewEntry()
	jump.SetText(srv.Jump)
	jump.SetPlaceHolder("optional bastion: user@jumphost[,user@jump2]")

	keyStatus := widget.NewLabel("")
	refreshKey := func() {
		if srv.KeyPath == "" {
			keyStatus.SetText("key: (none yet — generate, paste, or pick a file)")
		} else {
			keyStatus.SetText("key: " + srv.KeyPath)
		}
	}
	refreshKey()

	// keep srv in sync with the entries before an action runs
	sync := func() {
		srv.Name = strings.TrimSpace(name.Text)
		srv.Host = strings.TrimSpace(host.Text)
		srv.Port, _ = strconv.Atoi(strings.TrimSpace(port.Text))
		srv.User = strings.TrimSpace(user.Text)
		srv.Path = strings.TrimSpace(path.Text)
		srv.Jump = strings.TrimSpace(jump.Text)
	}
	nameOK := func() bool {
		sync()
		if srv.Name == "" || srv.Host == "" {
			dialog.ShowInformation("Missing fields", "Name and Host are required first.", w)
			return false
		}
		return true
	}

	genBtn := widget.NewButton("Generate key", func() {
		if !nameOK() {
			return
		}
		kp, err := GenerateKey(srv.Name)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		srv.KeyPath = kp
		refreshKey()
		if pub, e := PublicKey(kp); e == nil {
			showPublicKey(w, pub)
		}
	})
	pasteBtn := widget.NewButton("Paste key…", func() {
		if !nameOK() {
			return
		}
		e := widget.NewMultiLineEntry()
		e.SetPlaceHolder("-----BEGIN OPENSSH PRIVATE KEY-----\n…")
		d := dialog.NewCustomConfirm("Paste private key", "Save", "Cancel", e, func(ok bool) {
			if !ok || strings.TrimSpace(e.Text) == "" {
				return
			}
			kp, err := SavePastedKey(srv.Name, e.Text)
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			srv.KeyPath = kp
			refreshKey()
		}, w)
		d.Resize(fyne.NewSize(560, 360))
		d.Show()
	})
	fileBtn := widget.NewButton("Use key file…", func() {
		e := widget.NewEntry()
		e.SetText(srv.KeyPath)
		e.SetPlaceHolder("/home/you/.ssh/id_ed25519")
		dialog.ShowForm("Key file path", "Use", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Path", e)}, func(ok bool) {
				if ok && strings.TrimSpace(e.Text) != "" {
					srv.KeyPath = strings.TrimSpace(e.Text)
					refreshKey()
				}
			}, w)
	})
	showKeyBtn := widget.NewButton("Show public key", func() {
		if srv.KeyPath == "" {
			dialog.ShowInformation("No key", "Generate, paste, or pick a key first.", w)
			return
		}
		pub, err := PublicKey(srv.KeyPath)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		showPublicKey(w, pub)
	})

	authBtn := widget.NewButton("Authorize on server (password)…", func() {
		if !nameOK() {
			return
		}
		if srv.KeyPath == "" {
			dialog.ShowInformation("No key", "Set a key first (generate/paste/file).", w)
			return
		}
		pw := widget.NewPasswordEntry()
		dialog.ShowForm("Authorize key on "+srv.sshTarget(), "Authorize", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("Password (used once, not stored)", pw)},
			func(ok bool) {
				if !ok {
					return
				}
				go func() {
					err := AuthorizeKey(srv, pw.Text)
					fyne.Do(func() {
						if err != nil {
							dialog.ShowError(err, w)
						} else {
							dialog.ShowInformation("Authorized", "Public key installed on "+srv.sshTarget()+" ✓\nKey login should work now — try Test.", w)
						}
					})
				}()
			}, w)
	})
	testBtn := widget.NewButton("Test connection", func() {
		if !nameOK() {
			return
		}
		go func() {
			err := TestServer(srv)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(fmt.Errorf("connection failed:\n%v", err), w)
				} else {
					dialog.ShowInformation("Connection OK", "Connected to "+srv.sshTarget()+" and listed ZFS ✓", w)
				}
			})
		}()
	})

	keyRow := container.NewHBox(genBtn, pasteBtn, fileBtn, showKeyBtn)
	actionRow := container.NewHBox(authBtn, testBtn)
	form := widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Host", host),
		widget.NewFormItem("Port", port),
		widget.NewFormItem("User", user),
		widget.NewFormItem("Path", path),
		widget.NewFormItem("Jump host", jump),
	)
	content := container.NewVBox(form, keyStatus, keyRow, widget.NewSeparator(), actionRow)

	title := "Edit server"
	if isNew {
		title = "New server"
	}
	d := dialog.NewCustomConfirm(title, "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		if !nameOK() {
			return
		}
		if err := SaveServers(UpsertServer(LoadServers(), srv)); err != nil {
			dialog.ShowError(err, w)
			return
		}
		onSaved()
	}, w)
	d.Resize(fyne.NewSize(620, 520))
	d.Show()
}

// showPublicKey displays a public key in a read-only field for copy/paste (so it
// can be authorized manually where a password login isn't wanted).
func showPublicKey(w fyne.Window, pub string) {
	e := widget.NewMultiLineEntry()
	e.SetText(pub)
	e.Wrapping = fyne.TextWrapBreak
	d := dialog.NewCustom("Public key — copy to the server's authorized_keys", "Close", e, w)
	d.Resize(fyne.NewSize(560, 200))
	d.Show()
}
