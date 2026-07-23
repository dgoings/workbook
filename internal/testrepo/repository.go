// Package testrepo creates real Git repositories for integration tests.
package testrepo

import (
	"os/exec"
	"testing"
)

// New initializes a Git repository with a deterministic author identity and
// returns its working-tree path.
func New(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	run(t, dir, "init", "--quiet")
	run(t, dir, "config", "user.name", "Workbook Test")
	run(t, dir, "config", "user.email", "workbook@example.test")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
