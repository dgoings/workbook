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

func commitFile(t *testing.T, repository, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repository, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runCommand(t, repository, nil, "git", "add", name)
	runCommand(t, repository, nil, "git", "commit", "--quiet", "-m", "change "+name)
}
