//go:build !gui

// nogui.go — the static build's stand-in for the GUI.
//
// Built WITHOUT the `gui` tag (CGO_ENABLED=0), zxplore is a single static
// TUI-only binary — no cgo, no OpenGL, no X/Wayland — that you can scp to any
// ZFS box or `go install` directly. Asking that build for the GUI lands here.
package main

import (
	"fmt"
	"os"
)

func runGUI() {
	fmt.Fprintln(os.Stderr,
		"zxplore: this is the static terminal build (no GUI compiled in) — starting the TUI.\n"+
			"         For the native GUI, build with:  make zxplore-gui   (or: go build -tags gui)")
	elevate()
	runTUI()
}
