package cli

import (
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
//	TestRunReportsGitProcessFailuresAsOperationalWithoutUsage — t.Setenv
//	TestValidateCachedInvalidHeadStillExitsNonzeroWithoutHistoryBatch — t.Setenv
//
// Nothing else in the package needs to be serial. Every other test builds its
// own repository under t.TempDir and drives the CLI in process, and the one
// piece of state they share — the user-global configuration under the home
// directory TestMain replaces — is only ever read, or created with its
// defaults by `workbook setup`, which userconfig.Save publishes with a rename
// rather than a write in place. No test in this package asserts on that file's
// contents, so concurrent setups cannot disagree about it.

// TestMain isolates the user-global configuration Workbook reads from the
// environment. Without this, running the suite would read and write the
// developer's real ~/.config/workbook/config.json.
func TestMain(m *testing.M) {
	toolchainEnvironment = os.Environ()

	home, err := os.MkdirTemp("", "workbook-cli-home")
	if err != nil {
		panic("create isolated home: " + err.Error())
	}
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

	os.RemoveAll(home)
	os.Exit(code)
}
