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
const version = "1.3.0"

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
			fmt.Print("usage: zxplore [--tui] [--builder cmd] [--observe [pool] [cmd]] [--containers [cmd]] [--sync-run job] [--version]\n\n" +
				"  (no flags)   native GUI; the TUI when there is no display, or in the static build\n" +
				"  --tui        terminal UI — headless / SSH\n" +
				"  --version    print version and exit\n\n" +
				"CONTAINERS — the estate on ZFS, from a terminal\n" +
				"  --containers                 what is running, and what it sits on\n" +
				"  --containers snapshots       list estate snapshots\n" +
				"  --containers snapshot NAME   capture every layer, the engine\n" +
				"                               database and the volumes, at once\n" +
				"  --containers rollback NAME   put all of it back (engine stopped)\n" +
				"  --containers replicate       print the send command for another host\n" +
				"  --builder disks              the disk shelf: kind, size, model, free or in use\n" +
				"  --builder suggest NAME DISK… candidate layouts for those disks, with the create line\n" +
				"  --builder topology POOL      an imported pool's vdev tree\n" +
				"  --builder dry-run NAME SPEC… what zpool would build (zpool create -n)\n" +
				"  --builder create NAME SPEC…  build it — SPEC is the zpool vdev grammar with disk names\n" +
				"  --observe [POOL]             one second of the pool: gauges, verdicts, vdev latency, busy datasets\n" +
				"  --observe [POOL] watch       the same, refreshed every second\n" +
				"  --observe [POOL] json        the sample and verdicts as JSON\n" +
				"  --observe [POOL] kstat GROUP a kstat group with per-second rates (arcstats, zil, POOL/txgs …)\n" +
				"  --observe [POOL] events      zpool events -v\n\n" +
				"AUTO SYNC — scheduled replication (a timer runs these; you rarely type it)\n" +
				"  --sync [list]                Auto Sync jobs, and whether each timer is enabled\n" +
				"  --sync show JOB              the exact command a job runs, and encryption warnings\n" +
				"  --sync install JOB           write + enable its timer, and verify it is enabled\n" +
				"  --sync remove JOB            stop and delete its timer\n" +
				"  --sync-run JOB               run one saved Auto Sync job now: probe both ends,\n" +
				"                               pull, then verify the snapshot actually landed\n\n" +
				"With the zfs storage driver every image layer is a dataset, so the\n" +
				"whole container estate snapshots and replicates as one unit.\n\n" +
				"Documentation: man zxplore\n")
			return
		case "--tui":
			elevate() // safe in a terminal — root inherits the tty
			runTUI()
			return
		case "--sync":
			// Auto Sync from a terminal: what is scheduled, what it would run,
			// install/remove its timer. elevate() because installing a unit and
			// reading the system job list both need root.
			elevate()
			os.Exit(syncCLI(os.Args[2:]))
		case "--sync-run":
			// What an Auto Sync timer executes. NOT interactive and not a
			// GUI path: it runs from a systemd unit at 03:00 with nobody
			// logged in. elevate() because the pull does `zfs receive` into
			// the archive; under the unit it is already root and this is a
			// no-op, but a hand-run from a terminal still works.
			elevate()
			os.Exit(syncRunCLI(os.Args[2:]))
		case "--observe":
			// One second of a pool and the verdicts, from a terminal. Not
			// elevated: the kstats are world-readable and zpool iostat/get/
			// status run unprivileged; what needs root (events on some
			// hosts) lands in the report's warnings rather than a prompt.
			os.Exit(observeCLI(os.Args[2:]))
		case "--builder":
			// Terminal path to the pool Builder — the shelf, the candidate
			// layouts, an imported pool's vdev tree, dry run and create — for
			// the same reason as --containers: the box with twelve new disks
			// is usually reached over ssh, not from a desktop.
			//
			// elevate() because lsblk without root hides serials and by-id
			// links for some transports, and dry-run/create open the devices.
			elevate()
			os.Exit(builderCLI(os.Args[2:]))
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
	// No display to open a window on: the TUI, not a GLFW panic. The gui build
	// used to go straight to runGUI() and die with "NotInitialized: The GLFW
	// library is not initialized" and a Go stack trace -- the first thing
	// anyone saw typing `zxplore` on a headless storage install, over ssh or
	// at the console (fiend, 2026-09-18). The static build already did this.
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		fmt.Fprintln(os.Stderr, "zxplore: no DISPLAY or WAYLAND_DISPLAY -- starting the TUI")
		elevate() // same as --tui: root inherits the tty
		runTUI()
		return
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
