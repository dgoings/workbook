package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A release tag has to sit on a branch releases are published from: reachable
// from the remote's main, and on a commit no higher stable release is already
// part of. A tag on a feature branch publishes code nobody merged, and an older
// version tagged on newer code publishes the newer code under the older number.
func TestValidateReleaseBranchAcceptsATagOnMain(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		// earlier tags go on a first commit, and tag on a second one above it.
		earlier []string
		tag     string
	}{
		{name: "first release", tag: "v0.1.0"},
		{name: "after an older release", earlier: []string{"v0.5.1"}, tag: "v0.6.0"},
		{name: "a candidate", earlier: []string{"v0.5.1"}, tag: "v0.6.0-rc1"},
		// A candidate is not a release, so one already in the history does not
		// make a patch below it an older version on newer code.
		{name: "a patch above a later candidate", earlier: []string{"v0.6.0", "v0.7.0-rc1"}, tag: "v0.6.1"},
		{name: "beside desktop tags", earlier: []string{"desktop-v0.9.0"}, tag: "v0.6.0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clone, _ := newReleaseRepository(t)
			for _, tag := range testCase.earlier {
				runCommand(t, clone, nil, "git", "tag", tag)
			}
			commitFile(t, clone, "change.txt", "a change\n")
			runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main")
			runCommand(t, clone, nil, "git", "tag", testCase.tag)

			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", testCase.tag)
			if err != nil {
				t.Fatalf("validate %s: %v\n%s", testCase.tag, err, output)
			}
			if got := lastLine(output); got != "main" {
				t.Errorf("printed %q, want the branch the tag is on", got)
			}
		})
	}
}

func TestValidateReleaseBranchRefusesATagOffASupportedBranch(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		arrange     func(t *testing.T, clone string) string
		wantMessage string
	}{
		{
			name: "a feature branch",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "switch", "--quiet", "--create", "feature")
				commitFile(t, clone, "feature.txt", "unmerged work\n")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "feature")
				runCommand(t, clone, nil, "git", "tag", "v0.6.0")
				return "v0.6.0"
			},
			wantMessage: "is not on a branch releases are published from",
		},
		{
			// Production mutation: reading the local main instead of the
			// remote's would accept a commit nobody else has.
			name: "a commit main on the remote does not have",
			arrange: func(t *testing.T, clone string) string {
				commitFile(t, clone, "local.txt", "unpushed work\n")
				runCommand(t, clone, nil, "git", "tag", "v0.6.0")
				return "v0.6.0"
			},
			wantMessage: "is not on a branch releases are published from",
		},
		{
			name: "an older version on newer code",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "tag", "v0.6.0")
				commitFile(t, clone, "patch.txt", "a patch\n")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main")
				runCommand(t, clone, nil, "git", "tag", "v0.5.2")
				return "v0.5.2"
			},
			wantMessage: "already contains v0.6.0",
		},
		{
			name: "a candidate for a version already released",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "tag", "v0.6.0")
				commitFile(t, clone, "candidate.txt", "a candidate\n")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main")
				runCommand(t, clone, nil, "git", "tag", "v0.6.0-rc1")
				return "v0.6.0-rc1"
			},
			wantMessage: "already contains v0.6.0",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clone, _ := newReleaseRepository(t)
			tag := testCase.arrange(t, clone)

			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", tag)
			if err == nil {
				t.Fatalf("validate accepted %s:\n%s", tag, output)
			}
			if got := exitCode(t, err); got != 1 {
				t.Errorf("validate exited %d, want 1\n%s", got, output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
		})
	}
}

// Without the remote's main to compare against, the answer is unknown, and
// unknown must not pass.
func TestValidateReleaseBranchRefusesWhatItCannotCheck(t *testing.T) {
	t.Parallel()
	t.Run("no remote main", func(t *testing.T) {
		t.Parallel()
		repository := newTaggedRepository(t, "v0.6.0")
		output, err := runReleaseScript(t, repository, "validate-release-branch.sh", "", "v0.6.0")
		if err == nil {
			t.Fatalf("validate passed without a remote main:\n%s", output)
		}
		if got := exitCode(t, err); got != 2 {
			t.Errorf("validate exited %d, want 2\n%s", got, output)
		}
		if !strings.Contains(output, "refs/remotes/origin/main") {
			t.Errorf("output = %q, want the missing ref named", output)
		}
	})
	for _, testCase := range []struct{ name, tag, wantMessage string }{
		{name: "missing tag", tag: "v0.6.0", wantMessage: "is not a tag in this repository"},
		{name: "malformed tag", tag: "v0.6", wantMessage: "MAJOR.MINOR.PATCH"},
		{name: "desktop tag", tag: "desktop-v0.6.0", wantMessage: "MAJOR.MINOR.PATCH"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clone, _ := newReleaseRepository(t)
			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", testCase.tag)
			if err == nil {
				t.Fatalf("validate accepted %s:\n%s", testCase.tag, output)
			}
			if got := exitCode(t, err); got != 2 {
				t.Errorf("validate exited %d, want 2\n%s", got, output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
		})
	}
}

// newLineRepository shapes a repository the way a backport finds it: v0.5.1
// on main, v0.6.0 on main above it, and maint/v0.5 started from v0.5.1 with a
// fix on it, pushed. The clone is left on maint/v0.5.
func newLineRepository(t *testing.T) (string, string) {
	t.Helper()
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.5.1", "-m", "Workbook v0.5.1")
	commitFile(t, clone, "minor.txt", "the next minor\n")
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.6.0", "-m", "Workbook v0.6.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main", "refs/tags/v0.5.1", "refs/tags/v0.6.0")
	runCommand(t, clone, nil, "git", "switch", "--quiet", "--create", "maint/v0.5", "v0.5.1")
	commitFile(t, clone, "fix.txt", "a backported fix\n")
	runCommand(t, clone, nil, "git", "push", "--quiet", "-u", "origin", "maint/v0.5")
	return clone, remote
}

// A patch to an older line is published from that line's branch, which never
// merges back into main. Its tag is accepted there, and only for the line the
// branch names.
func TestValidateReleaseBranchAcceptsATagOnItsLineBranch(t *testing.T) {
	t.Parallel()
	clone, _ := newLineRepository(t)
	runCommand(t, clone, nil, "git", "tag", "v0.5.2")

	output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", "v0.5.2")
	if err != nil {
		t.Fatalf("validate v0.5.2 on maint/v0.5: %v\n%s", err, output)
	}
	if got := lastLine(output); got != "maint/v0.5" {
		t.Errorf("printed %q, want maint/v0.5", got)
	}
}

func TestValidateReleaseBranchRefusesATagOffItsLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		arrange func(t *testing.T, clone string) string
	}{
		{
			// Production mutation: accepting any maint branch would let a 0.6
			// number publish the 0.5 line's code.
			name: "a version from another line",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "tag", "v0.6.1")
				return "v0.6.1"
			},
		},
		{
			// A line ships stable patches; a candidate for one has no path in.
			name: "a candidate on the line",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "tag", "v0.5.2-rc1")
				return "v0.5.2-rc1"
			},
		},
		{
			// Production mutation: reading the local branch instead of the
			// remote's would accept a fix nobody reviewed.
			name: "a commit the remote line does not have",
			arrange: func(t *testing.T, clone string) string {
				commitFile(t, clone, "unpushed.txt", "unpushed work\n")
				runCommand(t, clone, nil, "git", "tag", "v0.5.2")
				return "v0.5.2"
			},
		},
		{
			name: "a line branch that was never pushed",
			arrange: func(t *testing.T, clone string) string {
				runCommand(t, clone, nil, "git", "switch", "--quiet", "--create", "maint/v0.4", "v0.5.1")
				commitFile(t, clone, "old.txt", "an older line\n")
				runCommand(t, clone, nil, "git", "tag", "v0.4.9")
				return "v0.4.9"
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			clone, _ := newLineRepository(t)
			tag := testCase.arrange(t, clone)

			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", tag)
			if err == nil {
				t.Fatalf("validate accepted %s:\n%s", tag, output)
			}
			if got := exitCode(t, err); got != 1 {
				t.Errorf("validate exited %d, want 1\n%s", got, output)
			}
			if !strings.Contains(output, "is not on a branch releases are published from") {
				t.Errorf("output = %q, want the branch refusal", output)
			}
		})
	}

	// The line's existence changes nothing for main: a v0.5.2 tagged there
	// still sits on code that already contains v0.6.0.
	t.Run("the line's version tagged on main", func(t *testing.T) {
		t.Parallel()
		clone, _ := newLineRepository(t)
		runCommand(t, clone, nil, "git", "tag", "v0.5.2", "origin/main")

		output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", "v0.5.2")
		if err == nil {
			t.Fatalf("validate accepted v0.5.2 on main:\n%s", output)
		}
		if got := exitCode(t, err); got != 1 {
			t.Errorf("validate exited %d, want 1\n%s", got, output)
		}
		if !strings.Contains(output, "already contains v0.6.0") {
			t.Errorf("output = %q, want the higher release named", output)
		}
	})
}

// newNewestLineRepository is the line for the newest minor: v0.6.0 on main,
// work toward 0.7 on main after it, and maint/v0.6 started from v0.6.0, all
// pushed. The clone is left on main.
func newNewestLineRepository(t *testing.T) string {
	t.Helper()
	clone, _ := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "tag", "--annotate", "v0.6.0", "-m", "Workbook v0.6.0")
	commitFile(t, clone, "next.txt", "work toward 0.7\n")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main", "refs/tags/v0.6.0", "v0.6.0^{commit}:refs/heads/maint/v0.6")
	runCommand(t, clone, nil, "git", "fetch", "--quiet", "origin")
	return clone
}

// Once a line has its own branch, main has moved past it and the line cuts its
// patches. A patch tagged on main as well would interleave with the line's, and
// the line's next one would become the newest release without what main's
// shipped. Production mutation: dropping the check accepts v0.6.1 on main.
func TestValidateReleaseBranchRefusesMainReleasingALineWithItsOwnBranch(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"v0.6.1", "v0.6.1-rc1"} {
		t.Run(tag, func(t *testing.T) {
			t.Parallel()
			clone := newNewestLineRepository(t)
			runCommand(t, clone, nil, "git", "tag", tag, "origin/main")

			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", tag)
			if err == nil {
				t.Fatalf("validate accepted %s on main beside maint/v0.6:\n%s", tag, output)
			}
			if got := exitCode(t, err); got != 1 {
				t.Errorf("validate exited %d, want 1\n%s", got, output)
			}
			if !strings.Contains(output, "main has moved past 0.6") {
				t.Errorf("output = %q, want the line named as moved past", output)
			}
		})
	}

	// What the line started from is still a release from main, so a re-run
	// of its workflow is not refused; the line's own patch and main's next
	// minor are accepted as before.
	for _, testCase := range []struct {
		tag, want string
		arrange   func(t *testing.T, clone string)
	}{
		{tag: "v0.6.0", want: "main"},
		{
			tag:  "v0.6.1",
			want: "maint/v0.6",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "switch", "--quiet", "--create", "maint/v0.6", "origin/maint/v0.6")
				commitFile(t, clone, "fix.txt", "a backported fix\n")
				runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "maint/v0.6")
				runCommand(t, clone, nil, "git", "fetch", "--quiet", "origin")
				runCommand(t, clone, nil, "git", "tag", "v0.6.1")
			},
		},
		{
			tag:  "v0.7.0",
			want: "main",
			arrange: func(t *testing.T, clone string) {
				runCommand(t, clone, nil, "git", "tag", "v0.7.0", "origin/main")
			},
		},
	} {
		t.Run("accepts "+testCase.tag, func(t *testing.T) {
			t.Parallel()
			clone := newNewestLineRepository(t)
			if testCase.arrange != nil {
				testCase.arrange(t, clone)
			}

			output, err := runReleaseScript(t, clone, "validate-release-branch.sh", "", testCase.tag)
			if err != nil {
				t.Fatalf("validate %s: %v\n%s", testCase.tag, err, output)
			}
			if got := lastLine(output); got != testCase.want {
				t.Errorf("printed %q, want %s", got, testCase.want)
			}
		})
	}
}

func commitFile(t *testing.T, repository, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repository, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runCommand(t, repository, nil, "git", "add", name)
	runCommand(t, repository, nil, "git", "commit", "--quiet", "-m", "change "+name)
}
