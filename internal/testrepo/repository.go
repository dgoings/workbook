// Package testrepo creates real Git repositories for integration tests.
package testrepo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	name         string
}

// Option adjusts how New creates a repository.
type Option func(*settings)

// WithName creates the repository in a directory of this name inside the
// test's temporary directory, for a test whose subject is the directory name
// itself. The default is the temporary directory, whose name means nothing.
func WithName(name string) Option {
	return func(s *settings) { s.name = name }
}

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
	if resolved.name != "" {
		dir = filepath.Join(dir, resolved.name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create repository directory %s: %v", dir, err)
		}
	}
	// Both formats go through InitAtWithObjectFormat, so that a repository
	// made inside a test and one made outside are initialized by the same
	// code rather than by two copies of it: an init flag added to one path
	// would otherwise miss the other, which is a difference nothing would
	// report.
	if err := InitAtWithObjectFormat(dir, resolved.objectFormat); err != nil {
		if resolved.objectFormat != FormatSHA1 {
			// SHA-256 support landed in Git 2.29, so an environment without
			// it reports a missing capability rather than failing the test.
			testenv.MissingCapability(t, "Git cannot create %s repositories: %v", resolved.objectFormat, err)
			return ""
		}
		t.Fatalf("%v", err)
	}
	return dir
}

// identity is the deterministic author every test repository commits as. It is
// defined once because a setting added to only one of the paths that initialize
// a repository is a difference nothing would report.
var identity = [][]string{
	{"config", "user.name", "Workbook Test"},
	{"config", "user.email", "workbook@example.test"},
}

// InitAt initializes a Git repository in dir, which must already exist: the
// same `git init` and the same author identity New gives a test its repository
// with. New is this plus a temporary directory to hold it.
//
// A caller that mints a repository outside a test — a fixture template built
// once for a whole package, where there is no *testing.T to fail — uses this,
// so that what an initialized test repository is configured with has one
// definition rather than two that drift apart.
func InitAt(dir string) error {
	return InitAtWithObjectFormat(dir, FormatSHA1)
}

// InitAtWithObjectFormat is InitAt in the named Git object format. It is the
// one place a test repository is initialized: New calls it, and so does a
// caller outside a test that has to build a fixture in a format other than the
// default.
//
// SHA-1 stays a plain `git init`, so a Git too old to know --object-format
// still builds every SHA-1 fixture. Any other format is a capability, and this
// only reports that the init failed — a caller with a *testing.T turns that
// into a skip, and one without has to have checked with SupportsObjectFormat.
func InitAtWithObjectFormat(dir, objectFormat string) error {
	init := []string{"init", "--quiet"}
	if objectFormat != FormatSHA1 {
		init = append(init, "--object-format="+objectFormat)
	}
	if output, err := gitCommand(dir, init...).CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(init, " "), err, output)
	}
	return ConfigureIdentity(dir)
}

// ConfigureIdentity writes the deterministic author identity onto a repository
// that already exists but that InitAt did not create — a clone, or a bare
// repository — so that a fixture assembled from several repositories commits as
// one author without a second copy of who that author is.
func ConfigureIdentity(dir string) error {
	for _, args := range identity {
		if output, err := gitCommand(dir, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, output)
		}
	}
	return nil
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
