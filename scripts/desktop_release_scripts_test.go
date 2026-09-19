package scripts_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The desktop scripts say "the answer is no" with exit 1 and "the question was
// malformed" with exit 2, and the workflow that calls them reads the two
// differently: a refusal is a release to cut by hand, a bad input is a bug in
// the caller. Every rejection below therefore pins the status, not just
// failure.
func TestValidateDesktopReleaseTagAcceptsThePrefixedGrammar(t *testing.T) {
	for _, testCase := range []struct{ tag, want string }{
		{tag: "desktop-v0.6.0", want: "0.6.0"},
		{tag: "desktop-v0.6.0-rc1", want: "0.6.0-rc1"},
	} {
		t.Run(testCase.tag, func(t *testing.T) {
			output, err := runReleaseScript(t, "", "validate-desktop-release-tag.sh", "", testCase.tag)
			if err != nil {
				t.Fatalf("validate %s: %v\n%s", testCase.tag, err, output)
			}
			if got := strings.TrimSpace(output); got != testCase.want {
				t.Errorf("printed %q, want %q", got, testCase.want)
			}
		})
	}
	// Production mutation: accepting a CLI tag here would let release.yml's
	// v* trigger and the desktop workflow publish the same tag twice.
	for _, tag := range []string{"v0.6.0", "desktop-0.6.0", "desktop-v0.6", "desktop-v0.6.0-rc", "desktop-latest", ""} {
		t.Run("rejects "+tag, func(t *testing.T) {
			output, err := runReleaseScript(t, "", "validate-desktop-release-tag.sh", "", tag)
			if err == nil {
				t.Fatalf("validate accepted %q:\n%s", tag, output)
			}
			// A malformed tag is bad input, never a refusal to act.
			if got := exitCode(t, err); got != 2 {
				t.Errorf("validate %q exited %d, want 2\n%s", tag, got, output)
			}
		})
	}
}

// The companion desktop release takes the CLI's number whenever that number
// is newer than the last desktop release; it never invents a bump.
func TestPlanDesktopReleaseTakesTheCLIVersionWhenItIsNewer(t *testing.T) {
	for _, testCase := range []struct {
		name, cli, previous, want string
		wantExit                  int
		wantMessage               string
	}{
		{name: "first ever", cli: "0.6.0-rc1", previous: "", want: "desktop-v0.6.0-rc1"},
		{name: "stable after rc", cli: "0.6.0", previous: "desktop-v0.6.0-rc1", want: "desktop-v0.6.0"},
		{name: "next minor", cli: "0.7.0", previous: "desktop-v0.6.0", want: "desktop-v0.7.0"},
		{
			name: "collides with a desktop-only release", cli: "0.6.1", previous: "desktop-v0.6.1",
			wantExit: 1, wantMessage: "cut the desktop release by hand",
		},
		{
			name: "older than a desktop-only release", cli: "0.6.1", previous: "desktop-v0.6.2",
			wantExit: 1, wantMessage: "cut the desktop release by hand",
		},
		// Production mutation: stripping a prefix that is not there would
		// compare the CLI's number against a bare number or, worse, against
		// the CLI's own sequence, and call the result a desktop ordering.
		{
			name: "previous without the desktop prefix", cli: "0.7.0", previous: "0.6.0",
			wantExit: 2, wantMessage: "is not a desktop release version tag",
		},
		{
			name: "previous from the CLI sequence", cli: "0.7.0", previous: "v0.6.0",
			wantExit: 2, wantMessage: "is not a desktop release version tag",
		},
		{
			name: "previous that is not a version", cli: "0.7.0", previous: "desktop-latest",
			wantExit: 2, wantMessage: "is not a desktop release version tag",
		},
		{
			name: "previous with the prefix and nothing after it", cli: "0.7.0", previous: "desktop-v",
			wantExit: 2, wantMessage: "is not a desktop release version tag",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runReleaseScript(t, "", "plan-desktop-release.sh", "", "--cli-version", testCase.cli, "--previous", testCase.previous)
			if testCase.want == "" {
				if err == nil {
					t.Fatalf("plan accepted %s after %s:\n%s", testCase.cli, testCase.previous, output)
				}
				if got := exitCode(t, err); got != testCase.wantExit {
					t.Errorf("plan exited %d, want %d\n%s", got, testCase.wantExit, output)
				}
				if !strings.Contains(output, testCase.wantMessage) {
					t.Errorf("output = %q, want %q", output, testCase.wantMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("plan %s after %s: %v\n%s", testCase.cli, testCase.previous, err, output)
			}
			if got := lastLine(output); got != testCase.want {
				t.Errorf("planned %q, want %q", got, testCase.want)
			}
		})
	}
}

// Discovery reads desktop-v* tags only, ignoring the CLI's v* tags that sit
// on the same commits.
func TestPlanDesktopReleaseDiscoversTheNewestDesktopTag(t *testing.T) {
	repository := newTaggedRepository(t, "v0.5.1", "v0.6.0-rc1", "desktop-v0.6.0-rc1", "desktop-v0.6.0-rc2", "v0.6.0")
	output, err := runReleaseScript(t, repository, "plan-desktop-release.sh", "", "--cli-version", "0.6.0")
	if err != nil {
		t.Fatalf("plan from repository: %v\n%s", err, output)
	}
	if got := lastLine(output); got != "desktop-v0.6.0" {
		t.Errorf("planned %q, want desktop-v0.6.0 after desktop-v0.6.0-rc2", got)
	}
	output, err = runReleaseScript(t, repository, "plan-desktop-release.sh", "", "--cli-version", "0.6.0-rc2")
	if err == nil {
		t.Fatalf("plan reused desktop-v0.6.0-rc2:\n%s", output)
	}
	// A discovered tag refuses exactly as a given one does: this is the
	// release the workflow has to hand to a human, not a caller's mistake.
	if got := exitCode(t, err); got != 1 {
		t.Errorf("plan exited %d, want 1\n%s", got, output)
	}
	if !strings.Contains(output, "cut the desktop release by hand") {
		t.Errorf("output = %q, want the manual instruction", output)
	}
}

func TestPlanDesktopReleaseRequiresACLIVersion(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "no version at all", args: []string{"--previous", "desktop-v0.6.0"}},
		{name: "a version the grammar refuses", args: []string{"--cli-version", "0.6", "--previous", ""}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runReleaseScript(t, "", "plan-desktop-release.sh", "", testCase.args...)
			if err == nil {
				t.Fatalf("plan ran with %v:\n%s", testCase.args, output)
			}
			// Both are bad input, and neither may look like the ordering
			// refusal the workflow turns into a manual instruction.
			if got := exitCode(t, err); got != 2 {
				t.Errorf("plan exited %d, want 2\n%s", got, output)
			}
		})
	}
}

// A mistyped KIND would otherwise fall through to "any", so a caller asking
// for stable tags would quietly get the pre-releases too. No script passes a
// kind from the outside, so the guard is reachable only by sourcing the
// helpers the way the scripts do.
func TestNewestReleaseTagRefusesAnUnknownKind(t *testing.T) {
	command := exec.Command("sh", "-c", `. "$1"; newest_release_tag sideways`, "sh", releaseScriptPath(t, "release-version.sh"))
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("newest_release_tag accepted an unknown kind:\n%s", output)
	}
	if got := exitCode(t, err); got != 2 {
		t.Errorf("newest_release_tag exited %d, want 2\n%s", got, output)
	}
	if !strings.Contains(string(output), "must be stable or any") {
		t.Errorf("output = %q, want the kind reported", output)
	}
}
