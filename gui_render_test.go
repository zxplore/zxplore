//go:build gui

// gui_render_test.go — headless tests for the GUI's text rendering: the
// embedded manual (overstrike stripping, section-header coloring) and the
// dossier's ━━ topic coloring. Widget behavior (lists, dialogs) is exercised
// on a real desktop; these prove the pure rendering paths that feed it.
package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestStripOverstrike(t *testing.T) {
	if got := stripOverstrike("N\x08NA\x08AM\x08ME\x08E"); got != "NAME" {
		t.Errorf("bold overstrike: %q", got)
	}
	if got := stripOverstrike("_\x08w_\x08o_\x08r_\x08d"); got != "word" {
		t.Errorf("underline overstrike: %q", got)
	}
	if got := stripOverstrike("\x08clean"); got != "clean" {
		t.Errorf("stray leading backspace: %q", got)
	}
	if got := stripOverstrike("already clean"); got != "already clean" {
		t.Errorf("clean text must pass through: %q", got)
	}
}

func TestManualSegments(t *testing.T) {
	segs := manualSegments("NAME\n     zxplore - console\nSEE ALSO\n     zfs(8)")
	headers := 0
	for _, s := range segs {
		ts, ok := s.(*widget.TextSegment)
		if !ok {
			t.Fatalf("unexpected segment type %T", s)
		}
		if ts.Style.ColorName == cnTopic && strings.TrimSpace(ts.Text) != "" {
			if !ts.Style.TextStyle.Bold || !ts.Style.TextStyle.Monospace {
				t.Errorf("header %q must be bold monospace", ts.Text)
			}
			headers++
		}
	}
	if headers != 2 {
		t.Errorf("colored headers = %d, want 2 (NAME, SEE ALSO)", headers)
	}
}

func TestRenderManual(t *testing.T) {
	text := renderManual()
	if strings.Contains(text, "\x08") {
		t.Error("rendered manual contains overstrike backspaces")
	}
	for _, section := range []string{"NAME", "SYNOPSIS", "DESCRIPTION"} {
		if !strings.Contains(text, section) {
			t.Errorf("section %q missing", section)
		}
	}
	if strings.Contains(text, "‘zfs/‘zpool’’") {
		t.Error("HISTORY quoting regressed")
	}
}

func TestDossierSegments(t *testing.T) {
	segs := dossierSegments("━━━ HEALTH ━━━\nplain line")
	var topic, plain bool
	for _, s := range segs {
		if ts, ok := s.(*widget.TextSegment); ok {
			if ts.Style.ColorName == cnTopic && strings.Contains(ts.Text, "HEALTH") {
				topic = true
			}
			if ts.Text == "plain line" {
				plain = true
			}
		}
	}
	if !topic || !plain {
		t.Errorf("topic colored=%v plain preserved=%v", topic, plain)
	}
}

// TestRefocusAfterTap covers the guard on the focus restore. The restore exists
// because Fyne unfocuses when a non-focusable object is tapped, and a list row
// is not focusable, so one click on a dataset killed every key on the list --
// including the ←/→ that fold the tree. The guard exists because a selection
// also fires while the operator is typing in the filter box.
func TestRefocusAfterTap(t *testing.T) {
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()

	entry := widget.NewEntry()
	list := newNavList(func() int { return 3 },
		func() fyne.CanvasObject { return widget.NewLabel("x") },
		func(widget.ListItemID, fyne.CanvasObject) {})
	w.SetContent(container.NewVBox(entry, list))

	// Nothing focused: the list takes focus, which is the post-tap case.
	w.Canvas().Unfocus()
	refocusAfterTap(w.Canvas(), list)
	if w.Canvas().Focused() != fyne.Focusable(list) {
		t.Errorf("after a tap the list must hold focus, got %T", w.Canvas().Focused())
	}

	// Typing in the filter box: focus must NOT be stolen.
	w.Canvas().Focus(entry)
	refocusAfterTap(w.Canvas(), list)
	if w.Canvas().Focused() != fyne.Focusable(entry) {
		t.Errorf("focus was stolen from the filter entry, now %T", w.Canvas().Focused())
	}
}
