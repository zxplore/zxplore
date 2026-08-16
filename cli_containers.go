// =============================================================================
// cli_containers.go — the container estate, from a terminal.
//
// WHAT IT DOES:
//   Exposes the four things worth doing to a container estate on ZFS —
//   list it, snapshot it, roll it back, replicate it — without a display.
//
// WHY IT EXISTS:
//   The Containers tab put these behind a window, and the people this
//   feature is FOR do not work in one. A container developer lives in a
//   shell, usually over ssh, on a machine with no desktop at all. A
//   capability that only exists in a GUI does not exist for them.
//
//   It is also the discoverability answer. Nothing in `docker --help` will
//   ever mention that the store is a dataset, so the knowledge has to live
//   somewhere a person will actually meet it: `zxplore --containers` prints
//   the driver, says whether layers are datasets, and names the commands.
//
// Notes:
//   - Reads are unprivileged where the engine allows it. snapshot/rollback
//     shell out to zfs and need root, which the terminal path can prompt for.
//   - replicate PRINTS the command rather than running it: the far side
//     needs a host, a key and a receiving dataset, and guessing those on
//     somebody's behalf is not a thing a one-liner should do.
// =============================================================================

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// runContainersCLI handles the `--containers` family. Returns the exit status.
func runContainersCLI(args []string) int {
	e := DetectEngine(5 * time.Second)

	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "", "list", "ls":
		return containersList(e)
	case "snapshot", "snap":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "zxplore: give the snapshot a name:  --containers snapshot <name>")
			return 2
		}
		return containersSnapshot(e, args[1])
	case "rollback":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "zxplore: name the snapshot:  --containers rollback <name>")
			return 2
		}
		return containersRollback(e, args[1])
	case "snapshots":
		return containersSnapshots(e)
	case "replicate":
		return containersReplicate(e)
	default:
		fmt.Fprintf(os.Stderr, "zxplore: unknown container command %q\n", sub)
		return 2
	}
}

// containersList prints the estate and, crucially, what it is sitting on.
func containersList(e Engine) int {
	if !e.Found {
		fmt.Fprintln(os.Stderr, "zxplore: "+e.Why)
		return 1
	}
	fmt.Printf("%s · storage driver %s · %s\n", e.Name, e.Driver, e.GraphRoot)
	if e.OnZFS() {
		// The whole point, stated where somebody will read it.
		fmt.Println("Every image layer is a ZFS dataset: a pull is a clone, and the")
		fmt.Println("estate — layers, engine database and volumes — snapshots as one unit.")
	} else {
		fmt.Printf("Layers are ordinary files here, not datasets. To change that: put\n" +
			"the data root on its own dataset and set the zfs storage driver.\n")
	}
	fmt.Println()

	cs, err := e.ListContainers(10 * time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zxplore:", err)
		return 1
	}
	if len(cs) == 0 {
		fmt.Println("no containers")
	} else {
		fmt.Printf("%-22s %-28s %-10s %s\n", "NAME", "IMAGE", "STATE", "PORTS")
		for _, c := range cs {
			fmt.Printf("%-22s %-28s %-10s %s\n",
				trunc(c.Name, 22), trunc(c.Image, 28), c.State, c.Ports)
		}
	}

	if is, err := e.ListImages(10 * time.Second); err == nil && len(is) > 0 {
		fmt.Printf("\n%-40s %s\n", "IMAGE", "SIZE")
		for _, im := range is {
			fmt.Printf("%-40s %s\n", trunc(im.Ref(), 40), im.Size)
		}
	}

	if e.OnZFS() {
		if ds, ok := e.StoreDataset(10 * time.Second); ok {
			fmt.Printf("\nestate dataset: %s\n", ds)
			fmt.Println("  zxplore --containers snapshot <name>   capture all of it")
			fmt.Println("  zxplore --containers rollback <name>   put it all back")
			fmt.Println("  zxplore --containers replicate         send it to another host")
		}
	}
	return 0
}

func containersSnapshot(e Engine, name string) int {
	snap, err := e.SnapshotStore(name, 60*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zxplore:", err)
		return 1
	}
	fmt.Println(snap)
	fmt.Println("every image layer, the engine's database and the volumes are in it")
	return 0
}

func containersSnapshots(e Engine) int {
	snaps, err := e.ListStoreSnapshots(20 * time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zxplore:", err)
		return 1
	}
	if len(snaps) == 0 {
		fmt.Println("no estate snapshots yet — take one with: zxplore --containers snapshot <name>")
		return 0
	}
	for _, s := range snaps {
		fmt.Printf("%-28s %s\n", s.Name, s.Used)
	}
	return 0
}

func containersRollback(e Engine, tag string) int {
	if err := e.RollbackStore(tag, 120*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "zxplore:", err)
		return 1
	}
	fmt.Printf("the container estate is back at %s\n", tag)
	fmt.Println("start the engine again when you are ready")
	return 0
}

// containersReplicate prints the send command for the estate.
//
// Printed, not run: the destination is a host, a key and a receiving dataset
// that this command cannot know. Showing the exact invocation — with the raw
// flag already decided — is the useful half.
func containersReplicate(e Engine) int {
	ds, ok := e.StoreDataset(10 * time.Second)
	if !ok {
		fmt.Fprintf(os.Stderr, "zxplore: the container store is not on a ZFS dataset "+
			"(driver is %q), so there is nothing to replicate as a unit\n", e.Driver)
		return 1
	}
	enc := e.StoreEncrypted(10 * time.Second)
	snaps, _ := e.ListStoreSnapshots(20 * time.Second)
	if len(snaps) == 0 {
		fmt.Fprintln(os.Stderr, "zxplore: take a snapshot first: zxplore --containers snapshot <name>")
		return 1
	}
	latest := snaps[len(snaps)-1]

	fmt.Printf("estate: %s\n\n", ds)
	fmt.Println(strings.Join(ReplicateArgv(latest.Full, "", enc), " ") +
		" | ssh OTHER-HOST zfs recv -F " + ds)
	if enc {
		fmt.Println("\nraw stream (-w) because this pool is encrypted: `zfs send -R` refuses")
		fmt.Println("on an encrypted dataset. The blocks stay encrypted in transit; the far")
		fmt.Println("side needs the key to mount them, not to store them.")
	}
	if len(snaps) > 1 {
		prev := snaps[len(snaps)-2]
		fmt.Printf("\nincremental from the previous snapshot — only the delta travels:\n%s\n",
			strings.Join(ReplicateArgv(latest.Full, prev.Full, enc), " ")+
				" | ssh OTHER-HOST zfs recv -F "+ds)
	}
	return 0
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 2 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
