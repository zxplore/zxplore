// main.go — entry point for zxplore, the ZFS console.
//
// One repo, two surfaces, one engine (zfs.go → the portable zfs/zpool CLI):
//
//	zxplore            → native GUI (Fyne) in the full build; the static
//	                     TUI-only build (no `gui` tag) starts the TUI instead
//	zxplore --tui      → the terminal UI (bubbletea) — headless / SSH / power use
//	zxplore --version  → version and exit
//
// Runs on any ZFS system (Linux distros + FreeBSD); on a kldload host it
// lights up the extra primitives (boot environments, etc.).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// version is the zxplore release (shown by --version and in the GUI header).
const version = "0.1.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-V":
			fmt.Println("zxplore " + version)
			return
		case "--help", "-h":
			fmt.Print("usage: zxplore [--tui] [--version]\n\n" +
				"  (no flags)   native GUI (static builds start the TUI)\n" +
				"  --tui        terminal UI — headless / SSH\n" +
				"  --version    print version and exit\n\n" +
				"Documentation: man zxplore\n")
			return
		case "--tui":
			elevate() // safe in a terminal — root inherits the tty
			runTUI()
			return
		}
	}
	// GUI: do NOT sudo-reexec — root can't reach the user's Wayland/X display.
	// Privileged ZFS ops elevate per-command (pkexec local, delegated ssh remote).
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
