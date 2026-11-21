package compaction

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Tests that stay serial, and why. Go runs these before the parallel batch,
// so each gets a quiet process environment.
//
//	TestExecuteInterruptedThenRerun — sets the package-level afterTaskHook,
//	which every Execute reads
//
// TestMain isolates the user-global and system git configuration every test
// repository here would otherwise read, as the other packages that build real
// repositories do: a developer's ~/.gitconfig is not something these tests
// mean to depend on, and background auto-gc spawned by a test repository can
// outlive its test and race t.TempDir's cleanup. A per-test t.Setenv would
// panic once a test calls t.Parallel, so the environment is replaced once
// here, before m.Run.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "workbook-compaction-home")
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

	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated home %s: %v\n", home, err)
	}
	os.Exit(code)
}
