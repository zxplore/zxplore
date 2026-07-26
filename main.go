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

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "zexplore:", err)
		os.Exit(1)
	}
}
