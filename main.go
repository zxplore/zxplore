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
// Must match the release tag — ci enforces tag == "v" + version on tag builds.
const version = "1.2.0"

// buildNum is stamped by the Makefile (-X main.buildNum=<n>) from the
// self-incrementing .buildnum counter; empty in a bare `go build`.
var buildNum = ""

// versionFull is version plus the build stamp: "1.1.0 b42".
func versionFull() string {
	if buildNum == "" || buildNum == "0" {
		return version
	}
	return version + " b" + buildNum
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-V":
			fmt.Println("zxplore " + versionFull())
			return
		case "--help", "-h":
			fmt.Print("usage: zxplore [--tui] [--containers [cmd]] [--version]\n\n" +
				"  (no flags)   native GUI (static builds start the TUI)\n" +
				"  --tui        terminal UI — headless / SSH\n" +
				"  --version    print version and exit\n\n" +
				"CONTAINERS — the estate on ZFS, from a terminal\n" +
				"  --containers                 what is running, and what it sits on\n" +
				"  --containers snapshots       list estate snapshots\n" +
				"  --containers snapshot NAME   capture every layer, the engine\n" +
				"                               database and the volumes, at once\n" +
				"  --containers rollback NAME   put all of it back (engine stopped)\n" +
				"  --containers replicate       print the send command for another host\n\n" +
				"With the zfs storage driver every image layer is a dataset, so the\n" +
				"whole container estate snapshots and replicates as one unit.\n\n" +
				"Documentation: man zxplore\n")
			return
		case "--tui":
			elevate() // safe in a terminal — root inherits the tty
			runTUI()
			return
		case "--containers":
			// Terminal path to the container estate. The Containers TAB is a
			// window, and the people this feature is for work over ssh on
			// machines with no display — a capability that exists only in a
			// GUI does not exist for them.
			//
			// elevate() because snapshot and rollback shell out to zfs. Read
			// commands work unprivileged where the engine allows it, and
			// paying the sudo cost once keeps the dispatch simple.
			elevate()
			os.Exit(runContainersCLI(os.Args[2:]))
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
