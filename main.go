// main.go — entry point for zxplore, the ZFS console.
//
// Default: a native GUI (Fyne) — real window, keyboard + mouse, own icon.
//
//	zxplore            → GUI
//	zxplore --tui      → the terminal UI (bubbletea), for headless / SSH / power use
//
// Both share the zfs.go engine. It shells out to the portable zfs/zpool CLI, so
// it runs on any ZFS system (Linux distros + FreeBSD); on a kldload host it
// lights up the extra primitives (boot environments, etc.).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--tui" {
		elevate() // safe in a terminal — root inherits the tty
		runTUI()
		return
	}
	// GUI: do NOT sudo-reexec — root can't reach the user's Wayland/X display.
	// Privileged ZFS ops are elevated per-command (pkexec/sudo-askpass) — TODO.
	runGUI()
}

func runTUI() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "zxplore:", err)
		os.Exit(1)
	}
}

// elevate re-execs under sudo when not root — ZFS create/send/recv/mount need
// it. Used only by the TUI path (a terminal); the GUI stays as the user.
func elevate() {
	if os.Geteuid() == 0 {
		return
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	argv := append([]string{"sudo", exe}, os.Args[1:]...)
	_ = syscall.Exec(sudo, argv, os.Environ())
}
