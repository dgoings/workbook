// Package testrepo creates real Git repositories for integration tests.
package testrepo

import (
	"os/exec"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

// FormatSHA1 and FormatSHA256 name the Git object formats a test repository
// can be created with. Workbook must not depend on a particular object hash
// length or algorithm, so tests that exercise stored object IDs run against
// both.
const (
	FormatSHA1   = "sha1"
	FormatSHA256 = "sha256"
)

type settings struct {
	objectFormat string
}

// Option adjusts how New creates a repository.
type Option func(*settings)

// WithObjectFormat creates the repository with the named Git object format.
// SHA-256 support landed in Git 2.29, so an environment without it reports a
// missing capability rather than failing the calling test.
func WithObjectFormat(objectFormat string) Option {
	return func(s *settings) { s.objectFormat = objectFormat }
}

// New initializes a Git repository with a deterministic author identity and
// returns its working-tree path.
func New(t *testing.T, options ...Option) string {
	t.Helper()

	resolved := settings{objectFormat: FormatSHA1}
	for _, option := range options {
		option(&resolved)
	}

	dir := t.TempDir()
	args := []string{"init", "--quiet"}
	// The default path stays a plain init so a Git too old to know
	// --object-format still runs every SHA-1 test.
	if resolved.objectFormat != FormatSHA1 {
		args = append(args, "--object-format="+resolved.objectFormat)
	}
	if output, err := gitCommand(dir, args...).CombinedOutput(); err != nil {
		if resolved.objectFormat != FormatSHA1 {
			testenv.MissingCapability(t, "Git cannot create %s repositories: %v\n%s", resolved.objectFormat, err, output)
			return ""
		}
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	run(t, dir, "config", "user.name", "Workbook Test")
	run(t, dir, "config", "user.email", "workbook@example.test")
	return dir
}

// SupportsObjectFormat reports whether this Git can create repositories in the
// named object format, so a caller that has to build a remote or a bare
// repository before calling New can guard the whole fixture.
func SupportsObjectFormat(t *testing.T, objectFormat string) bool {
	t.Helper()
	if objectFormat == FormatSHA1 {
		return true
	}
	probe := t.TempDir()
	err := gitCommand(probe, "init", "--quiet", "--object-format="+objectFormat).Run()
	return err == nil
}

// RequireObjectFormat reports a missing capability when this Git cannot create
// repositories in the named object format.
func RequireObjectFormat(t *testing.T, objectFormat string) {
	t.Helper()
	if !SupportsObjectFormat(t, objectFormat) {
		testenv.MissingCapability(t, "Git cannot create %s repositories", objectFormat)
	}
}

func gitCommand(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	if output, err := gitCommand(dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
