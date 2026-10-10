package scripts_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newReleasedRepository is the shape a line is started from: v0.5.0 and v0.5.1
// on main, then v0.6.0 above them, then work on main toward the next minor.
// Everything is pushed.
func newReleasedRepository(t *testing.T) (string, string) {
	t.Helper()
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.5.0", "-m", "Workbook v0.5.0")
	commitFile(t, clone, "patch.txt", "a patch\n")
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.5.1", "-m", "Workbook v0.5.1")
	commitFile(t, clone, "minor.txt", "the minor\n")
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.6.0", "-m", "Workbook v0.6.0")
	commitFile(t, clone, "next.txt", "work toward the next minor\n")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main", "refs/tags/v0.5.0", "refs/tags/v0.5.1", "refs/tags/v0.6.0")
	return clone, remote
}

// A line is started from the newest release of it, and only on request does it
// leave this clone. Production mutation: starting from v0.5.0, or from main,
// would put already-released fixes, or the next minor, on the line.
func TestStartReleaseLineBranchesFromTheLinesNewestRelease(t *testing.T) {
	t.Parallel()
	clone, remote := newReleasedRepository(t)
	tagsBefore := gitOutput(t, remote, "tag", "--list")
	released := gitOutput(t, clone, "rev-parse", "v0.5.1^{commit}")

	output, err := runStartReleaseLine(clone, "0.5")
	if err != nil {
		t.Fatalf("start the 0.5 line: %v\n%s", err, output)
	}
	if got := gitOutput(t, clone, "rev-parse", "refs/heads/maint/v0.5"); got != released {
		t.Errorf("maint/v0.5 = %s, want v0.5.1's commit %s", got, released)
	}
	if got := gitOutput(t, remote, "branch", "--list", "maint/v0.5"); got != "" {
		t.Errorf("remote carries %q without --push", got)
	}
	if !strings.Contains(output, "git push origin refs/heads/maint/v0.5") {
		t.Errorf("output = %q, want the push named for when it is wanted", output)
	}
	// v0.5.1 carries the line tooling, so there is nothing to bootstrap.
	if strings.Contains(output, "predates release lines") {
		t.Errorf("output = %q, want no bootstrap step for a release with the tooling", output)
	}
	// It creates a branch and nothing else: a tag is a release.
	if got := gitOutput(t, remote, "tag", "--list"); got != tagsBefore {
		t.Errorf("remote tags changed from %q to %q", tagsBefore, got)
	}
}

func TestStartReleaseLinePushesTheBranchWhenAsked(t *testing.T) {
	t.Parallel()
	clone, remote := newReleasedRepository(t)
	tagsBefore := gitOutput(t, clone, "tag", "--list")
	released := gitOutput(t, clone, "rev-parse", "v0.5.1^{commit}")

	output, err := runStartReleaseLine(clone, "0.5", "--push")
	if err != nil {
		t.Fatalf("start and push the 0.5 line: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/maint/v0.5"); got != released {
		t.Errorf("remote maint/v0.5 = %s, want v0.5.1's commit %s", got, released)
	}
	if got := gitOutput(t, clone, "tag", "--list"); got != tagsBefore {
		t.Errorf("local tags changed from %q to %q", tagsBefore, got)
	}
}

// main moving past the newest release is what makes a line worth having, even
// before the next minor ships: v0.6.1 cut from maint/v0.6 is then the newest
// release, published like any other.
func TestStartReleaseLineStartsTheNewestLineOnceMainHasMovedOn(t *testing.T) {
	t.Parallel()
	clone, _ := newReleasedRepository(t)
	released := gitOutput(t, clone, "rev-parse", "v0.6.0^{commit}")

	output, err := runStartReleaseLine(clone, "0.6")
	if err != nil {
		t.Fatalf("start the 0.6 line: %v\n%s", err, output)
	}
	if got := gitOutput(t, clone, "rev-parse", "refs/heads/maint/v0.6"); got != released {
		t.Errorf("maint/v0.6 = %s, want v0.6.0's commit %s", got, released)
	}
}

// newPreLineRepository is newReleasedRepository's shape with v0.5.0 tagged
// before the line tooling existed: its tree carries no start-release-line.sh,
// as every tag cut before release lines does not.
func newPreLineRepository(t *testing.T) (string, string) {
	t.Helper()
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "rm", "--quiet", "--cached", "scripts/start-release-line.sh")
	runCommand(t, clone, nil, "git", "commit", "--quiet", "-m", "before release lines")
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.5.0", "-m", "Workbook v0.5.0")
	runCommand(t, clone, nil, "git", "add", "scripts/start-release-line.sh")
	runCommand(t, clone, nil, "git", "commit", "--quiet", "-m", "release lines")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main", "refs/tags/v0.5.0")
	return clone, remote
}

// A line takes its workflows and release scripts from the release it starts
// at. One that predates release lines cannot verify, cut or safely publish a
// patch, and a tag pushed onto it would run a release workflow that moves
// Latest and the Homebrew tap backward. Production mutation: starting it
// silently leaves that hazard for the first backport to find.
func TestStartReleaseLineRefusesAReleaseThatPredatesLines(t *testing.T) {
	t.Parallel()
	clone, remote := newPreLineRepository(t)

	output, err := runStartReleaseLine(clone, "0.5", "--push")
	if err == nil {
		t.Fatalf("start-release-line started a line from a release without the tooling:\n%s", output)
	}
	for _, want := range []string{"v0.5.0 predates release lines", "--bootstrap"} {
		if !strings.Contains(output, want) {
			t.Errorf("output = %q, want %q", output, want)
		}
	}
	if got := gitOutput(t, clone, "branch", "--list", "maint/v0.5"); got != "" {
		t.Errorf("clone carries %q after a refusal", got)
	}
	if got := gitOutput(t, remote, "branch", "--list", "maint/v0.5"); got != "" {
		t.Errorf("remote carries %q after a refusal", got)
	}
}

// --bootstrap starts it anyway, and says that the line's first pull request
// has to bring main's tooling onto it before anything is released there.
func TestStartReleaseLineBootstrapsAReleaseThatPredatesLines(t *testing.T) {
	t.Parallel()
	clone, remote := newPreLineRepository(t)
	released := gitOutput(t, clone, "rev-parse", "v0.5.0^{commit}")

	output, err := runStartReleaseLine(clone, "0.5", "--bootstrap", "--push")
	if err != nil {
		t.Fatalf("bootstrap the 0.5 line: %v\n%s", err, output)
	}
	if got := gitOutput(t, remote, "rev-parse", "refs/heads/maint/v0.5"); got != released {
		t.Errorf("remote maint/v0.5 = %s, want v0.5.0's commit %s", got, released)
	}
	for _, want := range []string{
		"first pull request into maint/v0.5",
		"main's .github/workflows/ and scripts/",
		"Do not push a tag on maint/v0.5",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output = %q, want %q", output, want)
		}
	}
	// The ordinary next step would release from tooling that cannot do it.
	if strings.Contains(output, "merge that pull request to cut the release") {
		t.Errorf("output = %q, want the bootstrap step instead of the release step", output)
	}
}

func TestStartReleaseLineRefuses(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		arrange     func(t *testing.T, clone string)
		args        []string
		wantMessage string
	}{
		{
			name: "a branch that already exists here",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "branch", "maint/v0.5", "v0.5.0")
			},
			args:        []string{"0.5"},
			wantMessage: "maint/v0.5 already exists",
		},
		{
			name: "a branch that already exists on the remote",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "v0.5.0^{commit}:refs/heads/maint/v0.5")
			},
			args:        []string{"0.5"},
			wantMessage: "maint/v0.5 already exists on origin",
		},
		{name: "a line with no release", args: []string{"0.4"}, wantMessage: "no v0.4.* release"},
		// A candidate is not a release, so it cannot start a line either.
		{
			name: "a line with only a candidate",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "tag", "v0.7.0-rc1")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.7.0-rc1")
			},
			args:        []string{"0.7"},
			wantMessage: "no v0.7.* release",
		},
		{
			// main still is the 0.6 line when nothing has landed since v0.6.0, so
			// a patch is cut from main and a line would add nothing.
			name: "the line main is on",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.6.1", "-m", "Workbook v0.6.1")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/v0.6.1")
			},
			args:        []string{"0.6"},
			wantMessage: "nothing to branch",
		},
		{name: "a version rather than a line", args: []string{"0.5.1"}, wantMessage: "MAJOR.MINOR"},
		{name: "a prefixed line", args: []string{"v0.5"}, wantMessage: "MAJOR.MINOR"},
		{name: "a leading zero", args: []string{"0.05"}, wantMessage: "MAJOR.MINOR"},
		{name: "no line", args: nil, wantMessage: "usage"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clone, remote := newReleasedRepository(t)
			if testCase.arrange != nil {
				testCase.arrange(t, clone)
			}
			remoteBranches := gitOutput(t, remote, "branch", "--list")
			localBranches := gitOutput(t, clone, "branch", "--list")

			output, err := runStartReleaseLine(clone, append(testCase.args, "--push")...)
			if err == nil {
				t.Fatalf("start-release-line accepted %v:\n%s", testCase.args, output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
			if got := gitOutput(t, remote, "branch", "--list"); got != remoteBranches {
				t.Errorf("remote branches changed from %q to %q", remoteBranches, got)
			}
			if got := gitOutput(t, clone, "branch", "--list"); got != localBranches {
				t.Errorf("local branches changed from %q to %q", localBranches, got)
			}
		})
	}
}

func runStartReleaseLine(clone string, args ...string) (string, error) {
	command := exec.Command(filepath.Join(clone, "scripts", "start-release-line.sh"), args...)
	command.Dir = clone
	output, err := command.CombinedOutput()
	return string(output), err
}
