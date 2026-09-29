package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Tests that stay serial, and why. Go runs every one of them to completion
// before the parallel batch resumes, so each of them gets a process to itself.
//
// Five call t.Setenv, which panics if the calling test or an ancestor has
// called t.Parallel:
//
//	TestBuildRemoteFixtureIgnoresHostileGlobalSigningAndHooks — remote_fixture_test.go
//	TestMeasureRepository — scenarios_test.go
//	TestRunRepositoryGitBoundsCommandAndDescendants — scenarios_test.go
//	TestRunRepositoryGitReapsDescendantOfGitThatExits — scenarios_test.go
//	TestRunStorageGitReapsDescendantOfGitThatExits — storage_test.go
//
// The panic is the least of it. The first points GIT_CONFIG_GLOBAL at a
// deliberately hostile global configuration — signing forced on and
// core.hooksPath aimed at hooks that exit 1 — to prove that a fixture build
// survives it. Every git process in the whole test binary reads that file
// while it is set, so a concurrent fixture build would have its commits and
// pushes rejected. TestMeasureRepository sets GIT_CONFIG_COUNT and its key and
// value inside the sha256 subtest to pin init.defaultObjectFormat
// process-wide, which is why neither that subtest nor its parent may be
// parallel.
//
// TestRunRepositoryGitBoundsCommandAndDescendants is the one that could never
// run beside another test, whatever the rest of this file did. It does not
// prepend to PATH, it replaces PATH with one temporary directory holding one
// file: a `git` that runs /bin/sleep 5. For as long as it is set, no
// concurrent test could resolve the real git, and none could resolve `go`
// either — the replacement discards the $GOROOT/bin entry `go test` puts on
// the test binary's PATH, so all three go build helpers in this package would
// fail to find a compiler. The other two PATH tests prepend rather than
// replace, but what they prepend is a `git` shim that performs no git work at
// all and leaks a spinning background descendant per call, so any concurrent
// git resolved through PATH would run that instead of git.
//
// Four more stay serial for their timing rather than for process state:
//
//	TestObserveWatcherWindowMeasuresARunningDaemon — watch_scenarios_test.go
//	TestMeasureCommandTerminatesTimedOutDescendant — command_test.go
//	TestPrepareProjectionValidatesRebuildEnvelope — scenarios_test.go
//	TestMeasureCommandOutputPreservesStreamsAndCompatibilityWrapper — command_test.go
//
// The first observes a real `workbook sync --watch` daemon across two
// 1500 ms windows and requires the steady window to complete more than one
// synchronization while the idle window completes exactly one; a starved
// daemon makes those counts equal and fails the test. The second gives a
// shell 100 ms to fork a background descendant and have it record its pid
// before the reap kills the group, and fails hard if the pid never arrived.
// The last two allow a /bin/sh stub to start and print a line.
// The ruling is that they stay serial: Go finishes every serial test before
// the parallel batch resumes, so they do not contend with the fixture builds
// and the eighteen `go build`s the rest of this package runs, and a package
// whose subject is elapsed time should not make its narrowest windows compete
// with its own load. The cost of the ruling is about six and a half seconds of
// serial time, which is what the four of them take together.
//
// What being serial does not buy is a quiet machine, and an earlier version of
// this comment claimed that it did. The serial-before-parallel ordering is a
// guarantee inside one test binary; `go test ./...` runs a binary per package
// at once, so a whole-tree run starves a serial test exactly as it starves a
// parallel one. Measured: under five other packages running 18-wide,
// TestPrepareProjectionValidatesRebuildEnvelope failed on all four cases with
// `signal: killed`, its one-second stub budget never met, and re-running the
// affected tests with -parallel 1 against the same load left three of them
// still failing. So a narrow window here has to be wide enough for a loaded
// machine on its own merits; the serial list is about this package's own load
// and nothing more. The windows the four of them assert on are the subject and
// stay as they are; the budgets that are only hang guards were widened.
//
// TestResourceHelperProcess is in neither list. It skips unless the resource
// measurement tests re-execute the test binary with
// WORKBOOK_PERF_RESOURCE_HELPER=1, and in that child process it is the only
// test that runs, so it is also the only writer of resourceHelperSink.
//
// TestMain isolates the user-global and system git configuration that every
// fixture in this package would otherwise be built against, and it does so
// with os.Setenv before m.Run because the per-test alternative, t.Setenv,
// panics as soon as a test calls t.Parallel.
//
// The isolation reaches the measured processes as well as the in-process
// helpers. Every exec site in the package starts the child's environment from
// os.Environ() — MeasureCommandOutput in command.go, fixtureGitEnvironment in
// fixture.go, the warm HTTP server in scenarios.go, the watcher daemon in
// watch_scenarios.go — and the one exec that sets no Env at all, the
// fast-import in BuildFixture, inherits this process's environment directly.
// So every measured `workbook` and every fixture `git` reads the configuration
// installed here.
//
// Two things the package did not already defend. The first was the repositories
// nobody configures. fixtureGitConfig puts commit.gpgSign, tag.gpgSign,
// push.gpgSign and core.hooksPath overrides on every fixture git invocation and
// configureFixtureRepository writes them into every repository it creates, so a
// hostile global configuration cannot reach a fixture or a remote fixture's
// origin, but publishFixtureToLocalOrigin and measureLocalBareSyncAgainstNewOrigin
// in scenarios.go created their bare origins through a plain `git init --bare`
// that carried no overrides and wrote no local settings, and the measured
// `workbook` then pushed into them. Measured with the isolation removed and a
// core.hooksPath in the ambient global configuration pointing at hooks that
// exit 1: six tests failed, every one of them on a push rejected by the
// pre-receive hook of an origin created that way. That was a defect in the
// product and not only in the tests — a real benchmark run on an operator's
// machine took the operator's hooks — so both call sites now go through
// initBareFixtureOrigin, which writes the fixture's isolation settings into the
// origin and deliberately nothing that would change what the measured push
// costs; TestBareFixtureOriginsCarryTheFixtureLocalConfiguration pins both
// halves of that. The isolation installed here stays, because it covers what no
// per-repository setting can.
//
// The second is automatic garbage collection, which nothing in this package
// disables anywhere — no gc.auto, no maintenance.auto, no receive.autogc —
// while BuildFixture writes a whole synthetic history through fast-import and
// the fixtures push into bare origins, which is the loose-object volume that
// triggers a gc. A gc that outlives its test races t.TempDir's cleanup and
// fails it with "directory not empty", the failure internal/gitstore's
// sync_test.go names. Writing the three settings into the global configuration
// installed here covers every repository the package creates, in one place,
// rather than per call site. The same file also covers what neither the -c
// overrides nor the local settings mention: core.fsmonitor, whose
// per-repository daemon outlives the test, init.defaultBranch, credential
// helpers and aliases.
//
// No fixture template. BuildFixture is deterministic and its product is
// copyable, but the ten call sites outside the builder's own tests spread over
// eight distinct fixture specs, only three of which repeat, so a template
// scheme saves about five seconds of the package's serial time and nothing at
// all off the wall clock: the floor is TestBuildRemoteFixture at about fifty
// seconds, which no fixture template touches. A package whose fixture builder
// is itself under test is not the place for a second way of producing a
// fixture.
func TestMain(m *testing.M) {
	// The resource measurement tests re-execute this binary as a child that
	// touches memory and exits from inside the test with os.Exit, so no
	// cleanup here would ever run in it. That child needs none of the
	// arrangements below: it runs no git and no go, and it already inherits
	// this process's isolated environment through the measured command's Env.
	if os.Getenv(resourceHelperEnv) == "1" {
		os.Exit(m.Run())
	}

	// Recorded before HOME is replaced, for the children that shell out to
	// the Go toolchain. GOCACHE, GOMODCACHE and GOPATH all default to paths
	// under HOME, so a `go build` that inherited the isolated home would
	// resolve them inside a temporary directory this package deletes on the
	// way out: eighteen builds would each rebuild the world, and on a machine
	// with a populated real cache but no reachable module proxy they would
	// fail outright. Reading the values back with `go env` would not help,
	// because that child inherits the replaced HOME too and reports the
	// temporary paths.
	toolchainEnvironment = os.Environ()

	home, err := os.MkdirTemp("", "workbook-perf-home")
	if err != nil {
		panic("create isolated home: " + err.Error())
	}

	gitconfig := filepath.Join(home, "gitconfig")
	contents := "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[receive]\n\tautogc = false\n"
	if err := os.WriteFile(gitconfig, []byte(contents), 0o600); err != nil {
		panic("write isolated gitconfig: " + err.Error())
	}

	os.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	code := m.Run()

	// Report rather than discard: a directory under this home that a child
	// process left read-only does not come away with os.RemoveAll, and a
	// discarded error there is a leak nobody sees. scripts/main_test.go carries
	// the occurrence that made the case.
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated home %s: %v\n", home, err)
	}
	os.Exit(code)
}

// toolchainEnvironment is the environment as it stood before TestMain replaced
// the home directory, for the three helpers that run `go build`. TestMain
// explains why a child of the Go toolchain must not inherit the isolated home.
var toolchainEnvironment []string
