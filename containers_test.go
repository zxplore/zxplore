package main

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// TestDecodePSHandlesBothEngines is the one that matters. docker emits ONE
// JSON OBJECT PER LINE; podman emits a single JSON ARRAY. Parsing only one
// shape works perfectly on the machine you tested and shows an empty table on
// the other — a container pane that silently reports "nothing here" on half
// the fleet.
func TestDecodePSHandlesBothEngines(t *testing.T) {
	// docker: newline-delimited objects, Names and Ports as strings
	docker := `{"ID":"a1b2c3d4e5f67890","Names":"web","Image":"nginx:1.27","State":"running","Status":"Up 3 hours","Ports":"0.0.0.0:8080->80/tcp"}
{"ID":"ff00112233445566","Names":"db","Image":"postgres:16","State":"exited","Status":"Exited (0) 2 days ago","Ports":""}`

	// podman: one array, Names and Ports as arrays
	podman := `[{"ID":"a1b2c3d4e5f67890","Names":["web"],"Image":"nginx:1.27","State":"running","Status":"Up 3 hours","Ports":["0.0.0.0:8080->80/tcp"]},
{"ID":"ff00112233445566","Names":["db"],"Image":"postgres:16","State":"exited","Status":"Exited (0) 2 days ago","Ports":[]}]`

	for name, out := range map[string]string{"docker": docker, "podman": podman} {
		rows, err := decodePS(out)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(rows) != 2 {
			t.Errorf("%s: got %d rows, want 2", name, len(rows))
			continue
		}
		if got := firstString(rows[0].Names); got != "web" {
			t.Errorf("%s: first name = %q, want web", name, got)
		}
		if rows[0].Image != "nginx:1.27" {
			t.Errorf("%s: image = %q", name, rows[0].Image)
		}
		if got := firstString(rows[0].Ports); !strings.Contains(got, "8080") {
			t.Errorf("%s: ports = %q, want the published port", name, got)
		}
	}
}

// TestDecodePSEmptyIsNotAnError — no containers is a normal state, and a pane
// that errors on it reads as broken.
func TestDecodePSEmptyIsNotAnError(t *testing.T) {
	for _, out := range []string{"", "   ", "[]", "\n\n"} {
		rows, err := decodePS(out)
		if err != nil {
			t.Errorf("decodePS(%q) errored: %v", out, err)
		}
		if len(rows) != 0 {
			t.Errorf("decodePS(%q) returned %d rows", out, len(rows))
		}
	}
}

// TestDecodePSSurvivesOneBadRow — one unreadable line must not blank the
// whole table; the other containers are still real.
func TestDecodePSSurvivesOneBadRow(t *testing.T) {
	out := `{"ID":"aaa","Names":"good","Image":"a:1","State":"running"}
this line is not json
{"ID":"bbb","Names":"also-good","Image":"b:2","State":"exited"}`
	rows, err := decodePS(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the 2 readable ones", len(rows))
	}
}

// TestVerbRefusesAnythingButTheFour is a security property, not tidiness:
// the action string reaches an argv, so a caller able to pass "exec" or
// "run" would turn a lifecycle button into arbitrary execution inside a
// container.
func TestVerbRefusesAnythingButTheFour(t *testing.T) {
	e := Engine{Bin: "docker", Found: true}
	for _, bad := range []string{"exec", "run", "cp", "commit", "", "start;rm", "--help"} {
		if err := e.Verb(bad, "abc123", time.Second); err == nil {
			t.Errorf("action %q was accepted", bad)
		} else if !strings.Contains(err.Error(), "refusing") && bad != "" {
			// An empty action can fail either check; the rest must be
			// rejected by the allowlist specifically.
			t.Errorf("action %q was rejected for the wrong reason: %v", bad, err)
		}
	}
}

// TestVerbNeedsAContainer — a verb with no id would apply to whatever the
// engine picked, which is nothing good.
func TestVerbNeedsAContainer(t *testing.T) {
	e := Engine{Bin: "docker", Found: true}
	if err := e.Verb("stop", "   ", time.Second); err == nil {
		t.Error("stop with no container was accepted")
	}
}

// TestEngineNotFoundExplainsItself — an absent engine has to say what to
// install, because the pane is otherwise an empty table with no cause.
func TestEngineNotFoundExplainsItself(t *testing.T) {
	e := Engine{}
	if _, err := e.ListContainers(time.Second); err == nil {
		t.Fatal("listing against no engine succeeded")
	}
	if e.OnZFS() {
		t.Error("an absent engine claimed ZFS storage")
	}
}

func TestOnZFSDetectsTheDriver(t *testing.T) {
	if !(Engine{Driver: "zfs", Found: true}).OnZFS() {
		t.Error("zfs driver not detected")
	}
	if (Engine{Driver: "overlay", Found: true}).OnZFS() {
		t.Error("overlay reported as zfs")
	}
}

// TestImageRefFallsBackSensibly — a dangling image has <none> for both repo
// and tag, and "<none>:<none>" is not a name anybody can act on.
func TestImageRefFallsBackSensibly(t *testing.T) {
	cases := []struct {
		in   Image
		want string
	}{
		{Image{Repo: "nginx", Tag: "1.27", ID: "abc"}, "nginx:1.27"},
		{Image{Repo: "<none>", Tag: "<none>", ID: "abc123"}, "abc123"},
		{Image{Repo: "localhost/thing", Tag: "", ID: "def"}, "localhost/thing"},
	}
	for _, c := range cases {
		if got := c.in.Ref(); got != c.want {
			t.Errorf("Ref() = %q, want %q", got, c.want)
		}
	}
}

func TestShortIDTrimsTheSha256Prefix(t *testing.T) {
	if got := shortID("sha256:a1b2c3d4e5f6a7b8c9"); got != "a1b2c3d4e5f6" {
		t.Errorf("shortID = %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("short input was altered: %q", got)
	}
}

// TestHasContainerEngineMatchesDetection — the tab is shown by one and filled
// by the other, so a host where they disagree gets an empty tab or a missing
// one. They may differ in only one direction: the binary can exist while the
// daemon is down, which is exactly the case worth showing.
func TestHasContainerEngineMatchesDetection(t *testing.T) {
	has := HasContainerEngine()
	e := DetectEngine(3 * time.Second)
	if e.Found && !has {
		t.Error("an engine was detected but the tab would be hidden")
	}
	if !has && e.Found {
		t.Error("no engine binary, yet detection claims one")
	}
}

// TestReplicateArgvUsesRawOnEncrypted is the one that would otherwise be
// found in the field: kldload pools are encrypted by default and `zfs send
// -R` refuses on an encrypted dataset without -w.
func TestReplicateArgvUsesRawOnEncrypted(t *testing.T) {
	full := strings.Join(ReplicateArgv("rpool/store@s1", "", true), " ")
	if !strings.Contains(full, " -w") {
		t.Errorf("encrypted send has no raw flag: %q", full)
	}
	if !strings.Contains(full, " -R") {
		t.Errorf("send is not recursive, so layers and volumes stay behind: %q", full)
	}
	plain := strings.Join(ReplicateArgv("rpool/store@s1", "", false), " ")
	if strings.Contains(plain, " -w") {
		t.Errorf("unencrypted send asked for raw: %q", plain)
	}
}

// TestReplicateArgvIncremental — after the first full send, only the delta
// should travel.
func TestReplicateArgvIncremental(t *testing.T) {
	got := ReplicateArgv("rpool/store@tue", "rpool/store@mon", true)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-I rpool/store@mon") {
		t.Errorf("no incremental base: %q", joined)
	}
	if got[len(got)-1] != "rpool/store@tue" {
		t.Errorf("the target snapshot must come last: %q", joined)
	}
}

// TestListRowShapeMatchesTheUpdater guards the indexing that blanked the
// Containers tab.
//
// container.NewBorder appends the CENTER object first, then top/bottom/left/
// right in that order — so a Border built with no center has Objects
// [left, right], and code indexing it as [center, left] type-asserts a Label
// into a *Container and panics inside the list renderer. Fyne swallows that
// into an empty list: `docker ps` showed four containers and the tab showed
// none (2026-08-16).
//
// This asserts the shape directly rather than through the widget, so it fails
// at `go test` instead of at a blank pane.
func TestListRowShapeMatchesTheUpdater(t *testing.T) {
	name := widget.NewLabel("name")
	state := widget.NewLabel("●")
	detail := widget.NewLabel("detail")
	row := container.NewBorder(nil, nil, container.NewHBox(state, name), nil, detail)

	if len(row.Objects) < 2 {
		t.Fatalf("row has %d objects, want at least 2", len(row.Objects))
	}
	if _, ok := row.Objects[0].(*widget.Label); !ok {
		t.Errorf("Objects[0] is %T, want the CENTER label", row.Objects[0])
	}
	left, ok := row.Objects[1].(*fyne.Container)
	if !ok {
		t.Fatalf("Objects[1] is %T, want the left HBox", row.Objects[1])
	}
	if len(left.Objects) != 2 {
		t.Errorf("left box has %d objects, want state + name", len(left.Objects))
	}
}

// TestPlausibleDriverRejectsProse is the guard for a failure that reported
// itself as success: `docker info` prints its error and still exits 0, and
// containerRun merges stderr in, so the header rendered "storage driver
// permission denied while trying to connect to the Docker daemon socket..."
// and the pane concluded the layers were ordinary files (fiend, 2026-08-16).
func TestPlausibleDriverRejectsProse(t *testing.T) {
	good := []string{"zfs\t/var/lib/docker", "overlay2\t/var/lib/docker", "btrfs", "vfs\t/x"}
	for _, g := range good {
		if !plausibleDriver(g) {
			t.Errorf("rejected a real driver line: %q", g)
		}
	}
	bad := []string{
		"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
		"", "   ",
		"error during connect: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.45/info\"",
	}
	for _, b := range bad {
		if plausibleDriver(b) {
			t.Errorf("accepted prose as a driver: %q", b)
		}
	}
}
