package projection

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// TestOpenAndCloseLeaveNoConnectionOpenerGoroutine pins what Close is for.
//
// database/sql starts one connectionOpener goroutine per *sql.DB inside
// sql.OpenDB and only db.Close stops it, so a Store that is opened and dropped
// leaks that goroutine for the life of the process. A one-shot command is
// unharmed; `workbook serve`, the sync watcher and a test binary that runs
// hundreds of commands are not.
//
// This test is serial — see the note in main_test.go — because a goroutine
// census is a reading of the whole process.
func TestOpenAndCloseLeaveNoConnectionOpenerGoroutine(t *testing.T) {
	ctx := context.Background()
	config := testConfig()
	source := &countingHeadSource{}

	before := connectionOpenerGoroutines(t)
	store, err := openStore(ctx, source, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	opened := newGoroutines(before, connectionOpenerGoroutines(t))
	if len(opened) == 0 {
		t.Fatal("opening a store started no connection-opener goroutine, so this test would pass without Close")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if surviving := awaitGoroutineExit(t, opened); len(surviving) > 0 {
		t.Fatalf("goroutine(s) %v outlived the store Close returned from", surviving)
	}
}

// TestClosedStoreRefusesUse pins that a released handle answers rather than
// panics, and that closing twice is not a failure: `serve` closes its store
// after a shutdown that may already have closed it, and a command's deferred
// close runs on every path out.
func TestClosedStoreRefusesUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := testConfig()
	store, err := openStore(ctx, &countingHeadSource{}, config, filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}

	for _, use := range []struct {
		name string
		call func() error
	}{
		{"List", func() error { _, err := store.List(ctx, config); return err }},
		{"Get", func() error { _, err := store.Get(ctx, config, "WB-01K0M6B8A4FTT8C39MXXYTW7D1"); return err }},
		{"Refresh", func() error { return store.Refresh(ctx) }},
		{"Rebuild", func() error { _, err := store.Rebuild(ctx); return err }},
	} {
		err := use.call()
		if err == nil {
			t.Fatalf("%s() on a closed store succeeded, want an error", use.name)
		}
		// The category matters as much as the refusal: a closed cache is an
		// operational fault in the caller's process, not a corrupt projection
		// the reader should be told to rebuild.
		if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
			t.Fatalf("%s() category = %v, want %v (error = %v)", use.name, got, want, err)
		}
		if !strings.Contains(err.Error(), "closed") {
			t.Fatalf("%s() error = %v, want it to say the cache is closed", use.name, err)
		}
	}
}

// connectionOpenerGoroutines reports the IDs of the live goroutines running
// database/sql's connection opener.
//
// Identities rather than a count: the count includes every other store this
// process has open, and the question is only whether the goroutines this test
// started are still there.
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
		header := strings.TrimPrefix(block, "goroutine ")
		id, _, ok := strings.Cut(header, " ")
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

// awaitGoroutineExit waits for every goroutine in wanted to leave the process,
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
