package scripts_test

import (
	"strings"
	"testing"
)

// Publishing an older release must leave everything that points at "the
// current release" where it is: GitHub's Latest badge, the Homebrew tap, the
// desktop cascade and desktop-latest. One script answers whether a tag is the
// newest, and every one of those callers reads its exit status, so each case
// pins the status as well as what it prints.
func TestReleaseIsNewestOrdersATagAgainstItsOwnSequence(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		tags     []string
		tag      string
		wantExit int
		// wantNewer is the tag printed when a newer one exists.
		wantNewer string
	}{
		{name: "the only release", tags: []string{"v0.6.0"}, tag: "v0.6.0"},
		{name: "after an older stable release", tags: []string{"v0.5.1", "v0.6.0"}, tag: "v0.6.0"},
		{name: "a patch to an older line", tags: []string{"v0.5.1", "v0.6.0", "v0.5.2"}, tag: "v0.5.2", wantExit: 1, wantNewer: "v0.6.0"},
		{name: "a rerun of a superseded release", tags: []string{"v0.6.0", "v0.6.1"}, tag: "v0.6.0", wantExit: 1, wantNewer: "v0.6.1"},
		// A candidate is not a release, so one that is out does not make the
		// newest stable release an older one: brew and Latest still want it.
		{name: "stable while a later candidate is out", tags: []string{"v0.6.0", "v0.7.0-rc1", "v0.6.1"}, tag: "v0.6.1"},
		// A candidate answers against every tag, the rule plan-desktop-release.sh
		// already applies, so a rerun of rc1 after rc2 never counts as newest.
		{name: "the next candidate", tags: []string{"v0.6.0", "v0.7.0-rc1", "v0.7.0-rc2"}, tag: "v0.7.0-rc2"},
		{name: "a superseded candidate", tags: []string{"v0.6.0", "v0.7.0-rc1", "v0.7.0-rc2"}, tag: "v0.7.0-rc1", wantExit: 1, wantNewer: "v0.7.0-rc2"},
		{name: "a candidate for a version already released", tags: []string{"v0.6.0", "v0.6.0-rc1"}, tag: "v0.6.0-rc1", wantExit: 1, wantNewer: "v0.6.0"},
		// Production mutation: comparing across sequences would let the CLI's
		// v0.6.0 hold a desktop-only fix off desktop-latest, or the reverse.
		{name: "desktop ignores CLI tags", tags: []string{"v0.7.0", "desktop-v0.6.0", "desktop-v0.6.1"}, tag: "desktop-v0.6.1"},
		{name: "CLI ignores desktop tags", tags: []string{"desktop-v0.7.0", "v0.6.0"}, tag: "v0.6.0"},
		{name: "an older desktop tag", tags: []string{"desktop-v0.6.0", "desktop-v0.5.2"}, tag: "desktop-v0.5.2", wantExit: 1, wantNewer: "desktop-v0.6.0"},
		// The rolling tag and a date-named tag are not versions and never order
		// against one.
		{name: "non-version tags", tags: []string{"desktop-latest", "v2026-08-08", "desktop-v0.6.0"}, tag: "desktop-v0.6.0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repository := newTaggedRepository(t, testCase.tags...)
			output, err := runReleaseScript(t, repository, "release-is-newest.sh", "", testCase.tag)
			if testCase.wantExit == 0 {
				if err != nil {
					t.Fatalf("release-is-newest %s: %v\n%s", testCase.tag, err, output)
				}
				if strings.TrimSpace(output) != "" {
					t.Errorf("output = %q, want nothing for the newest release", output)
				}
				return
			}
			if err == nil {
				t.Fatalf("release-is-newest called %s the newest among %v:\n%s", testCase.tag, testCase.tags, output)
			}
			if got := exitCode(t, err); got != testCase.wantExit {
				t.Errorf("release-is-newest exited %d, want %d\n%s", got, testCase.wantExit, output)
			}
			if got := strings.TrimSpace(output); got != testCase.wantNewer {
				t.Errorf("printed %q, want the newer tag %q", got, testCase.wantNewer)
			}
		})
	}
}

// A question it cannot answer must never read as "newest": that is the answer
// that moves Latest, the tap and desktop-latest. A checkout without the tag in
// it is the shape a shallow or tagless checkout takes, where no newer tag would
// be visible either.
func TestReleaseIsNewestRefusesWhatItCannotOrder(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		tags        []string
		args        []string
		wantMessage string
	}{
		{name: "no tag", args: nil, wantMessage: "usage"},
		{name: "malformed tag", tags: []string{"v0.6.0"}, args: []string{"v0.6"}, wantMessage: "MAJOR.MINOR.PATCH"},
		{name: "malformed desktop tag", tags: []string{"desktop-v0.6.0"}, args: []string{"desktop-latest"}, wantMessage: "desktop-vMAJOR.MINOR.PATCH"},
		{name: "a tag the checkout does not carry", tags: []string{"v0.5.1"}, args: []string{"v0.6.0"}, wantMessage: "is not a tag in this repository"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repository := newTaggedRepository(t, testCase.tags...)
			output, err := runReleaseScript(t, repository, "release-is-newest.sh", "", testCase.args...)
			if err == nil {
				t.Fatalf("release-is-newest answered %v:\n%s", testCase.args, output)
			}
			if got := exitCode(t, err); got != 2 {
				t.Errorf("release-is-newest exited %d, want 2\n%s", got, output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
		})
	}

	t.Run("outside a repository", func(t *testing.T) {
		t.Parallel()
		output, err := runReleaseScript(t, t.TempDir(), "release-is-newest.sh", "", "v0.6.0")
		if err == nil {
			t.Fatalf("release-is-newest answered outside a repository:\n%s", output)
		}
		if got := exitCode(t, err); got != 2 {
			t.Errorf("release-is-newest exited %d, want 2\n%s", got, output)
		}
	})
}
