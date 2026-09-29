package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// toolchainEnvironment is the environment as it stood before TestMain replaced
// the home directory, kept for tests that shell out to the Go toolchain.
//
// GOMODCACHE, GOCACHE, and GOPATH all default to paths under HOME, so a child
// `go build` that inherited the isolated home would resolve them inside a
// temporary directory this package deletes when it exits: it would re-download
// the whole module graph and rebuild from nothing on every run, and on a
// machine with a populated real cache but no reachable proxy it would fail
// outright where every other test in the package passes. Reading them back with
// `go env` would not help, because that child inherits the replaced HOME too
// and reports the temporary paths. Only a snapshot taken before the swap is the
// developer's real toolchain, and `go build` has no business reading the
// Workbook configuration this isolation exists to protect.
var toolchainEnvironment []string

// templateRoot holds the one project this package mints, which every test
// asking for an initialized repository copies. It lives under the isolated
// home so that removing the home removes it, and so that the template is
// minted with the same user-global configuration every test runs against.
var templateRoot string

// Tests that stay serial, and why. Go runs these before the parallel batch,
// so each gets a quiet process environment.
//
//	TestValidateCachedInvalidHeadStillExitsNonzeroWithoutHistoryBatch — t.Setenv
//
// It puts a logging Git wrapper on PATH and then counts the Git commands the
// whole test binary runs through it, so a concurrently running test's Git would
// land in the same log.
//
// Nothing else in the package needs to be serial. Every other test builds its
// own repository under t.TempDir and drives the CLI in process, and the one
// piece of state they share — the user-global configuration under the home
// directory TestMain replaces — is only ever read, or created with its
// defaults by `workbook setup`, which userconfig.Save publishes with a rename
// rather than a write in place. No test in this package asserts on that file's
// contents, so concurrent setups cannot disagree about it.

// TestMain isolates the user-global Workbook configuration and the user-global
// and system Git configuration that this package's repositories would otherwise
// read. Without it, running the suite would read and write the developer's real
// ~/.config/workbook/config.json, and every repository it creates would carry
// whatever the developer's ~/.gitconfig and the machine's /etc/gitconfig say.
//
// Three reasons for the Git half, the same three the other packages with a
// TestMain give.
//
// First, this is the most Git-heavy package on the branch:
// initializedRepository and cliSyncRepositories build well over three hundred
// repositories per run, and each of them would otherwise inherit the ambient
// credential helper, aliases, init.defaultBranch and push.autosetupremote.
// Replacing HOME neutralizes the global file but not the system one, so
// GIT_CONFIG_NOSYSTEM is what keeps a machine's /etc/gitconfig — Xcode's, for
// one — out of the fixtures.
//
// Second, background auto-gc or Git maintenance spawned by any of those
// repositories can outlive its test and race t.TempDir's cleanup, which fails
// with "directory not empty" while a stray process still holds the directory
// open. Writing gc.auto=0, maintenance.auto=false and receive.autogc=false into
// the global config installed here covers every repository the tests create,
// instead of the three bare origins that guard themselves by hand in
// sync_test.go and bootstrap_test.go. Those three lines stay where they are:
// redundant with this file, and harmless.
//
// Third, a per-test t.Setenv would be the ordinary way to get any of this, and
// it panics once a test calls t.Parallel, so the environment has to be replaced
// once, here, before m.Run. That is what lets
// TestRunReportsGitProcessFailuresAsOperationalWithoutUsage — which needs a
// global config that names no user.email — run in the parallel batch: the
// isolated file below names none.
//
// The toolchain snapshot is taken before any of this, for the reason recorded on
// toolchainEnvironment.
func TestMain(m *testing.M) {
	toolchainEnvironment = os.Environ()

	home, err := os.MkdirTemp("", "workbook-cli-home")
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

	templateRoot = filepath.Join(home, "template")
	if err := os.MkdirAll(templateRoot, 0o755); err != nil {
		panic("create template root: " + err.Error())
	}
	// Mint the template here, not on whichever test asks for it first: the
	// mint then runs with the environment this function has just arranged
	// rather than with whatever a parallel test has done to its own, and a
	// mint that cannot succeed says so once instead of failing every test
	// that copies it.
	if _, err := templateProject(); err != nil {
		panic("mint template project: " + err.Error())
	}

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
