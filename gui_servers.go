//go:build gui

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
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// showServerManager lists saved servers; Connect calls onConnect with the chosen
// one. Two panes in the app's card style: the saved-server list (two-line rows)
// on the left, a live detail panel + Test/Connect on the right — so the screen
// reads as a real manager, not a bare list. Reloads after edits.
func showServerManager(w fyne.Window, onConnect func(Server)) {
	servers := LoadServers()
	sel := -1

	// ── right: detail panel for the selected server ──
	detName := dialogHeading("no server selected", acCyan)
	detBody := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	detHint := widget.NewLabel("Pick a server on the left — or ＋ New to add your first ZFS box:\nname + host + user, then generate a key and authorize it\nwith the password ONCE (never stored).")
	detHint.Wrapping = fyne.TextWrapWord
	testResult := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	testResult.Wrapping = fyne.TextWrapWord

	showDetail := func() {
		testResult.SetText("")
		if sel < 0 || sel >= len(servers) {
			detName.Text = "no server selected"
			detName.Refresh()
			detBody.SetText("")
			detHint.Show()
			return
		}
		s := servers[sel]
		detHint.Hide()
		detName.Text = s.Name
		detName.Refresh()
		key := "(none — generate or add one, then Authorize)"
		if s.KeyPath != "" {
			key = s.KeyPath
		}
		jump, path := s.Jump, s.Path
		if jump == "" {
			jump = "(direct)"
		}
		if path == "" {
			path = "(whole machine)"
		}
		port := s.Port
		if port == 0 {
			port = 22
		}
		detBody.SetText(fmt.Sprintf(
			"host      %s\nuser      %s\nport      %d\ndataset   %s\njump      %s\nkey       %s",
			s.Host, s.User, port, path, jump, key))
	}

	// ── left: two-line rows (bold name / mono target) ──
	list := widget.NewList(
		func() int { return len(servers) },
		func() fyne.CanvasObject {
			return container.NewVBox(
				widget.NewLabelWithStyle("name", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				widget.NewLabelWithStyle("sub", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			s := servers[i]
			box := o.(*fyne.Container)
			key := "key ✓"
			if s.KeyPath == "" {
				key = "no key"
			}
			port := s.Port
			if port == 0 {
				port = 22
			}
			box.Objects[0].(*widget.Label).SetText(s.Name)
			box.Objects[1].(*widget.Label).SetText(
				fmt.Sprintf("  %s : %d   ·   %s", s.toHost().SSH, port, key))
		},
	)
	list.OnSelected = func(i widget.ListItemID) { sel = int(i); showDetail() }

	empty := widget.NewLabelWithStyle(
		"No saved servers yet.\n\n＋ New adds a ZFS box (any Linux distro or FreeBSD\nrunning OpenZFS — key-first, passwords never stored).",
		fyne.TextAlignCenter, fyne.TextStyle{})
	syncEmpty := func() {
		if len(servers) == 0 {
			empty.Show()
		} else {
			empty.Hide()
		}
	}
	syncEmpty()

	reload := func() {
		servers = LoadServers()
		if sel >= len(servers) {
			sel = -1
		}
		list.UnselectAll()
		list.Refresh()
		syncEmpty()
		showDetail()
	}

	newBtn := widget.NewButtonWithIcon("New", theme.ContentAddIcon(), func() {
		serverEditDialog(w, Server{Port: 22}, true, reload)
	})
	editBtn := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() {
		if sel >= 0 && sel < len(servers) {
			serverEditDialog(w, servers[sel], false, reload)
		}
	})
	delBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
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

	testBtn := widget.NewButtonWithIcon("Test", theme.ConfirmIcon(), func() {
		if sel < 0 || sel >= len(servers) {
			return
		}
		s := servers[sel]
		testResult.SetText("… connecting to " + s.sshTarget())
		go func() {
			err := TestServer(s)
			fyne.Do(func() {
				if err != nil {
					testResult.SetText("✗ " + err.Error())
				} else {
					testResult.SetText("✓ connected — ZFS visible on " + s.sshTarget())
				}
			})
		}()
	})

	var dlg dialog.Dialog
	connectBtn := widget.NewButtonWithIcon("Connect", theme.LoginIcon(), func() {
		if sel >= 0 && sel < len(servers) {
			s := servers[sel]
			dlg.Hide()
			onConnect(s)
		}
	})
	connectBtn.Importance = widget.HighImportance

	leftPane := paneCard(container.NewBorder(
		container.NewVBox(dialogHeading("SAVED SERVERS", acBlue), widget.NewSeparator()),
		container.NewHBox(newBtn, editBtn, delBtn),
		nil, nil,
		container.NewStack(list, container.NewCenter(empty))))
	rightPane := paneCard(container.NewBorder(
		container.NewVBox(detName, widget.NewSeparator()),
		container.NewVBox(testResult, container.NewHBox(testBtn, layout.NewSpacer(), connectBtn)),
		nil, nil,
		container.NewVBox(detBody, detHint)))
	split := container.NewHSplit(leftPane, rightPane)
	split.SetOffset(0.5)

	dlg = dialog.NewCustom("Servers", "Close", split, w)
	dlg.Resize(fyne.NewSize(940, 560))
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
	// userOK gates the auth flows: an empty User silently means "connect as
	// your LOCAL username", which on a remote box is almost never an account —
	// every test and authorize then fails with a baffling permission-denied.
	userOK := func() bool {
		if !nameOK() {
			return false
		}
		if srv.User == "" {
			dialog.ShowInformation("SSH user required",
				"Set the User field to the account on the server (e.g. admin or zexp).\n\nLeft empty, ssh would connect as your local username — which usually\ndoesn't exist on the remote box and fails with 'permission denied'.", w)
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

	authBtn := widget.NewButton("Authorize (password)…", func() {
		if !userOK() {
			return
		}
		if srv.KeyPath == "" {
			dialog.ShowInformation("No key", "Set a key first (generate/paste/file).", w)
			return
		}
		pw := widget.NewPasswordEntry()
		pw.SetPlaceHolder("account password on the server")
		var d dialog.Dialog
		var authAttempt func(offerForget bool)
		authAttempt = func(offerForget bool) {
			go func() {
				err := AuthorizeKey(srv, pw.Text)
				fyne.Do(func() {
					if err != nil && offerForget && HostKeyChangedErr(err) {
						offerHostKeyReset(w, srv, func() { authAttempt(false) })
						return
					}
					if err != nil {
						dialog.ShowError(err, w)
					} else {
						dialog.ShowInformation("Authorized", "Public key installed on "+srv.sshTarget()+" ✓\nKey login should work now — try Test.", w)
					}
				})
			}()
		}
		runAuth := func() { authAttempt(true) }
		pw.OnSubmitted = func(string) { d.Hide(); runAuth() }
		body := container.NewVBox(
			widget.NewLabel("Password for "+srv.sshTarget()+" — used once to install the key, never stored:"),
			pw,
		)
		d = dialog.NewCustomConfirm("Authorize key on "+srv.sshTarget(), "Authorize", "Cancel", body,
			func(ok bool) {
				if ok {
					runAuth()
				}
			}, w)
		d.Resize(fyne.NewSize(540, 180))
		d.Show()
		w.Canvas().Focus(pw)
	})
	testBtn := widget.NewButton("Test", func() {
		if !userOK() {
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

	// ⚡ the turnkey path: generate key (if needed) + authorize + test + save,
	// one password prompt, one click. The buttons above remain for piecemeal
	// setups (own key, manual authorize on password-less servers).
	setupBtn := widget.NewButtonWithIcon("⚡ Set up & save", theme.MediaPlayIcon(), func() {
		if !userOK() {
			return
		}
		pw := widget.NewPasswordEntry()
		pw.SetPlaceHolder("leave empty if the key is already authorized")
		var d dialog.Dialog
		var runSetup func()
		pw.OnSubmitted = func(string) { d.Hide(); runSetup() }
		body := container.NewVBox(
			widget.NewLabel("Password for "+srv.sshTarget()+" — used once to install the key, never stored:"),
			pw,
		)
		d = dialog.NewCustomConfirm("Set up "+srv.sshTarget(), "Set up", "Cancel", body,
			func(ok bool) {
				if !ok {
					return
				}
				runSetup()
			}, w)
		// attempt runs the one-shot setup; on the pinned-key-changed refusal it
		// offers ONE forget-and-retry (expected after a reinstall) — with the
		// operator's explicit go-ahead, never silently.
		var attempt func(offerForget bool)
		attempt = func(offerForget bool) {
			go func() {
				s2, err := SetupServer(srv, pw.Text)
				fyne.Do(func() {
					if err != nil && offerForget && HostKeyChangedErr(err) {
						offerHostKeyReset(w, srv, func() { attempt(false) })
						return
					}
					if err != nil {
						dialog.ShowError(err, w)
						return
					}
					srv = s2
					refreshKey()
					if err := SaveServers(UpsertServer(LoadServers(), srv)); err != nil {
						dialog.ShowError(err, w)
						return
					}
					onSaved()
					dialog.ShowInformation("Ready",
						"✓ key in place\n✓ authorized on "+srv.sshTarget()+"\n✓ ZFS visible\n\nServer saved — Connect away.", w)
				})
			}()
		}
		runSetup = func() { attempt(true) }
		d.Resize(fyne.NewSize(540, 180))
		d.Show()
		w.Canvas().Focus(pw) // the field is ready to type into immediately
	})
	setupBtn.Importance = widget.HighImportance

	keyRow := container.NewHBox(genBtn, pasteBtn, fileBtn, showKeyBtn)
	actionRow := container.NewHBox(setupBtn, authBtn, testBtn)
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
	d.Resize(fyne.NewSize(660, 560))
	d.Show()
}

// offerHostKeyReset explains a pinned-host-key mismatch and, with explicit
// consent, forgets the stale entry and retries — the reinstall recovery path.
// Refusing silently would strand every reinstalled box; forgetting silently
// would gut the pinning. So: explain, ask, then act.
func offerHostKeyReset(w fyne.Window, srv Server, retry func()) {
	dialog.ShowConfirm("Host key changed",
		"The host key of "+srv.sshTarget()+" no longer matches the one pinned in known_hosts.\n\n"+
			"EXPECTED if this server was reinstalled — a fresh OS generates a fresh key.\n"+
			"If you did NOT reinstall it, cancel and investigate before typing any password.\n\n"+
			"Forget the old key and continue?",
		func(ok bool) {
			if !ok {
				return
			}
			if err := ForgetHostKey(srv); err != nil {
				dialog.ShowError(err, w)
				return
			}
			retry()
		}, w)
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
