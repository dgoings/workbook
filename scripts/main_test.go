package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests that stay serial, and why. There are none: this package has no
// t.Setenv, no os.Setenv, no os.Chdir, no t.Chdir, and no package-level
// mutable state (`grep -rn 't\.Setenv|os\.Setenv|os\.Unsetenv|os\.Chdir|t\.Chdir|os\.Getwd' scripts/*_test.go`
// finds nothing). Every test that needs a different PATH, HOME, GOCACHE,
// WORKBOOK_REPO, or FAKE_GH_ROOT sets it on its own exec.Cmd.Env, which is
// unaffected by another test running at the same time.
//
// One subtest group stays in order for a different reason:
//
//	TestDeleteReleaseTagRejectsUnsafeVersions — delete_release_tag_test.go
//
// Its four subtests share the parent's single clone and the parent asserts on
// that clone's tags after the loop. Parallel subtests would let that
// assertion run before they finish, which would make the whole test vacuous
// rather than racy, so it is not a t.Parallel() candidate at all.
//
// TestMain isolates the user-global and system git configuration that every
// fixture repository in this package would otherwise read. Two reasons.
//
// First, the fixtures in this package commit, tag, and push through `git
// commit`, `git tag`, and `git push` — real plumbing that reads the
// developer's real ~/.gitconfig and any system gitconfig. Nothing in this
// package intends to depend on that config, but core.hooksPath could redirect
// a hook one of the fixtures installs, commit.gpgsign could try to sign a
// fixture commit with a key the machine does not have, and core.fsmonitor
// could start a daemon that outlives the test and races t.TempDir's cleanup.
// A per-test t.Setenv would normally paper over this, but it panics once a
// test calls t.Parallel, so the environment has to be replaced once, in
// TestMain, before m.Run.
//
// Second, background auto-gc or git maintenance spawned by receive-pack on a
// bare remote can outlive its test and race t.TempDir's cleanup, which fails
// with "directory not empty" if a stray process still has the directory
// open. newTapRepository (publish_release_test.go) already guards its own
// bare remote against this with three git config lines. newReleaseRepository
// (cut_release_test.go), newTagDeletionRepository
// (delete_release_tag_test.go), and ciWorkflowGateRepository
// (ci_workflow_test.go) push to or commit into repositories with no such
// guard. Writing gc.auto=0, maintenance.auto=false, and receive.autogc=false
// into the global config this TestMain installs covers every repository any
// test creates, not just the one that was guarded per-fixture.
// newTapRepository's own three lines stay in place; they are redundant with
// the global file but harmless.
//
// Replacing HOME costs the Go build cache unless the cache is pinned first,
// which is what pinGoToolchainPaths is for.
func TestMain(m *testing.M) {
	pinGoToolchainPaths()

	home, err := os.MkdirTemp("", "workbook-scripts-home")
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

	// Report rather than discard. This removal can fail: the Go module cache
	// creates its directories read-only, which is the whole reason
	// `go clean -modcache` exists, so a home that ever held one does not come
	// away with os.RemoveAll. That happened on this package before
	// pinGoToolchainPaths landed, and because the error went nowhere the suite
	// reported success and left a quarter of a gigabyte behind each run — 4.2 GB
	// in total before anybody noticed. One line on stderr is what turns the next
	// occurrence into something a reader sees.
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated home %s: %v\n", home, err)
	}
	os.Exit(code)
}

// pinGoToolchainPaths puts the Go toolchain's real cache paths into the
// environment before TestMain replaces HOME.
//
// GOCACHE, GOMODCACHE, GOPATH and GOENV are all derived from HOME when they are
// not set explicitly, and the shell scripts these tests run build the product
// six ways. With HOME replaced and nothing else done, every one of those builds
// resolves its cache inside the temporary home TestMain deletes on the way out,
// so each one compiles the world from nothing; on a machine with a populated
// real cache but no reachable module proxy they would fail outright. GOENV is
// the fourth for a different reason: it names the file `go env -w` writes, so
// leaving it HOME-derived silently drops whatever the developer configured there
// — GOFLAGS, GOPROXY, GOPRIVATE, or a GOTOOLCHAIN that decides which toolchain
// builds at all.
//
// Setting these on an exec.Cmd.Env, the way internal/perf's tests do for their
// own `go build` helpers, cannot work here: the builds happen several processes
// down, inside shell scripts that inherit whatever environment they are given,
// so the values have to be in this process's environment. One `go env` resolves
// all four while HOME is still the real one — read back afterwards it would
// report the temporary paths.
func pinGoToolchainPaths() {
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	output, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		panic("resolve the Go toolchain paths: " + err.Error())
	}
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	if len(lines) != len(names) {
		panic(fmt.Sprintf("go env returned %d lines, want %d", len(lines), len(names)))
	}
	for index, name := range names {
		value := strings.TrimSpace(lines[index])
		if value == "" {
			panic("go env reported no value for " + name)
		}
		os.Setenv(name, value)
	}
}

// goToolchainValues names the environment entries that point the Go toolchain at
// the developer's real caches and configuration rather than at the isolated home.
// pinGoToolchainPaths has already put them in this process's environment, so a
// child that inherits it needs nothing; this is for the call sites that build an
// environment from scratch and would otherwise hand a build a HOME-derived
// cache. Reading them back here rather than resolving them again is the point:
// `go env` run after the swap reports the temporary paths.
func goToolchainValues() []string {
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name+"="+os.Getenv(name))
	}
	return values
}

// isolatedGitConfigValues names the two environment entries that make a git
// invocation honor the isolated configuration TestMain installs above. Every
// exec.Cmd in this package either leaves Env nil or builds it from
// os.Environ(), so it inherits GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM
// automatically. Four call sites build an environment from scratch instead.
// Two of them append this slice, so the scripts they run still see the
// isolated git configuration rather than the developer's real one: runWithPATH
// in check_ci_capabilities_test.go and buildEnvironment in
// install_default_test.go.
//
// The other two are a deliberate exception, and a new site should not copy
// them: both subtests of TestInstallReportsMissingPrerequisites
// (install_test.go) hand the installer nothing but a PATH, because what they
// prove is that it refuses when go or git is missing from PATH. Threading
// anything else in would not defeat them, but the emptiness is the fixture, so
// they are left alone.
func isolatedGitConfigValues() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.Getenv("GIT_CONFIG_GLOBAL"),
		"GIT_CONFIG_NOSYSTEM=" + os.Getenv("GIT_CONFIG_NOSYSTEM"),
	}
}
