package cli

import (
	"context"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/gitstore"
)

// TestCommandLeavesNoProjectionGoroutineBehind pins that a command gives its
// projection handle back.
//
// Every command opens the repository's SQLite projection, and database/sql
// starts one connectionOpener goroutine per *sql.DB that only db.Close stops.
// Nothing closed one until openProjection existed, so each command left a
// goroutine behind: harmless in a process that exits straight afterwards,
// cumulative in the ones that do not — `workbook serve`, `workbook sync
// --watch`, the desktop app's watcher, and this test binary, where it was
// measured at roughly 1300 surviving goroutines over one run of this package.
//
// `list` stands in for the ordinary command. It is the plainest path that
// opens a projection and nothing else, so a failure here is about the handle
// rather than about the command.
//
// This test is serial — see the note in main_test.go — because it attributes
// every connection opener that appears while it runs to the command it ran.
func TestCommandLeavesNoProjectionGoroutineBehind(t *testing.T) {
	ctx := context.Background()
	repository := initializedRepository(t)

	// The control, first. A census can only stand outside the command, and a
	// command that closes its handle opens and closes it in between two
	// readings — so the reading after `list` is expected to be empty, and would
	// be just as empty if this census could not see a projection at all. One
	// handle opened by hand says it can: it appears while it is held and is
	// gone once it is released.
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	before := connectionOpenerGoroutines(t)
	_, releaseProjection, err := openProjection(ctx, repo, config)
	if err != nil {
		t.Fatalf("openProjection() error = %v", err)
	}
	held := newGoroutines(before, connectionOpenerGoroutines(t))
	if len(held) == 0 {
		t.Fatal("an open projection shows no connection-opener goroutine, so this census proves nothing")
	}
	releaseProjection()
	if surviving := awaitGoroutineExit(t, held); len(surviving) > 0 {
		t.Fatalf("goroutine(s) %v outlived the projection the release closed", surviving)
	}

	// And now the command. Nothing it opened may still be running once it has
	// returned.
	before = connectionOpenerGoroutines(t)
	code, stdout, stderr := run(t, repository, "list")
	if code != 0 {
		t.Fatalf("list = (%d, %q, %q), want code 0", code, stdout, stderr)
	}
	left := newGoroutines(before, connectionOpenerGoroutines(t))
	if surviving := awaitGoroutineExit(t, left); len(surviving) > 0 {
		t.Fatalf("goroutine(s) %v outlived `workbook list`; a command is not closing its projection\n%s",
			surviving, goroutineDump(t))
	}
}

// connectionOpenerGoroutines reports the IDs of the live goroutines running
// database/sql's connection opener.
//
// Identities rather than a count: a count would also carry every store the
// rest of this binary has open, and the question is only whether the
// goroutines one command started are still there.
func connectionOpenerGoroutines(t *testing.T) map[int]struct{} {
	t.Helper()
	return parseConnectionOpeners(goroutineDump(t))
}

// openerCreator is how a connection opener is recognized in a goroutine dump:
// by who started it, not by the frame it is sitting in.
//
// sql.OpenDB starts exactly one goroutine, the connection opener, so its
// parentage names it exactly. The frame does not: a `go` statement over a
// method value compiles to a wrapper, so an opener the scheduler has not run
// yet reads as `database/sql.OpenDB.gowrap1()` and only becomes
// `database/sql.(*DB).connectionOpener(...)` once it is running. Matching the
// frame made this test pass or fail on whether the new goroutine had been
// scheduled by the time the dump was taken.
const openerCreator = "created by database/sql.OpenDB"

func parseConnectionOpeners(dump string) map[int]struct{} {
	openers := make(map[int]struct{})
	for _, block := range strings.Split(dump, "\n\ngoroutine ") {
		if !strings.Contains(block, openerCreator) {
			continue
		}
		id, _, ok := strings.Cut(strings.TrimPrefix(block, "goroutine "), " ")
		if !ok {
			continue
		}
		parsed, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		openers[parsed] = struct{}{}
	}
	return openers
}

func goroutineDump(t *testing.T) string {
	t.Helper()
	// Doubling until the dump fits: runtime.Stack truncates silently, and a
	// truncated dump would read as goroutines that have exited.
	for size := 1 << 16; ; size *= 2 {
		buf := make([]byte, size)
		written := runtime.Stack(buf, true)
		if written < size {
			return string(buf[:written])
		}
		if size >= 1<<26 {
			t.Fatalf("goroutine dump exceeds %d bytes", size)
		}
	}
}

// newGoroutines reports the IDs in after that were not already in before.
func newGoroutines(before, after map[int]struct{}) []int {
	var added []int
	for id := range after {
		if _, existing := before[id]; !existing {
			added = append(added, id)
		}
	}
	sort.Ints(added)
	return added
}

// awaitGoroutineExit waits for every goroutine in wanted to leave the process
// and reports those that did not. A goroutine the runtime has not yet
// scheduled through its return is not a leak, so this polls rather than reading
// the dump once.
func awaitGoroutineExit(t *testing.T, wanted []int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		live := connectionOpenerGoroutines(t)
		var surviving []int
		for _, id := range wanted {
			if _, running := live[id]; running {
				surviving = append(surviving, id)
			}
		}
		if len(surviving) == 0 || time.Now().After(deadline) {
			return surviving
		}
		time.Sleep(10 * time.Millisecond)
	}
}
