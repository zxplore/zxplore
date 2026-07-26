// main.go — entry point for zexplore, the ZFS console.
//
// zexplore is a terminal UI ("k9s for ZFS"): browse datasets, snapshot on tap,
// and replicate anywhere — local pool or remote host over SSH. It shells out to
// the portable zfs/zpool CLI, so one static binary runs on any ZFS system
// (Linux distros + FreeBSD). On a kldload host it lights up the extra
// primitives (boot environments, etc.); elsewhere it's plain, universal ZFS.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	elevate()
	// NB: no mouse grab (WithMouseCellMotion). Grabbing the mouse would let the
	// app receive clicks, but it DISABLES the terminal's native selection,
	// right-click menu, and copy-paste — which is the "basic stuff" people
	// expect. Keyboard-first drives the app; the terminal keeps the mouse.
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "zexplore:", err)
		os.Exit(1)
	}
}

// elevate re-execs under sudo when not root — ZFS create/send/recv/mount need
// it. Matches the bash tool's behaviour. If sudo is missing we continue as-is
// (read-only `zfs list` still works where the user has delegated permission).
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
	_ = syscall.Exec(sudo, argv, os.Environ()) // replaces this process on success
}
