package scripts_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveReleaseVersionAppliesEachBumpKind(t *testing.T) {
	for _, testCase := range []struct {
		bump string
		want string
	}{
		{bump: "patch", want: "0.4.2"},
		{bump: "minor", want: "0.5.0"},
		{bump: "major", want: "1.0.0"},
	} {
		t.Run(testCase.bump, func(t *testing.T) {
			output, err := runResolveReleaseVersion(t, "", "--bump", testCase.bump, "--previous", "v0.4.1")
			if err != nil {
				t.Fatalf("resolve %s: %v\n%s", testCase.bump, err, output)
			}
			if got := strings.TrimSpace(output); got != testCase.want {
				t.Errorf("resolved %q, want %q", got, testCase.want)
			}
		})
	}
}

// Callers accept a bump and an optional exact version from the same form and
// pass both through, so the precedence between them has to be decided here
// rather than repeated in each workflow's YAML.
func TestResolveReleaseVersionLetsAnExplicitVersionOverrideTheBump(t *testing.T) {
	output, err := runResolveReleaseVersion(t, "", "--bump", "patch", "--version", "1.0.0", "--previous", "v0.4.1")
	if err != nil {
		t.Fatalf("resolve explicit version: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(output); got != "1.0.0" {
		t.Errorf("resolved %q, want the explicit 1.0.0 rather than the patch bump", got)
	}
}

// A workflow_dispatch form submits an empty string for a version the releaser
// left blank. Treating that as a request for version "" would reject every
// bump-only release.
func TestResolveReleaseVersionTreatsAnEmptyVersionAsAbsent(t *testing.T) {
	output, err := runResolveReleaseVersion(t, "", "--bump", "minor", "--version", "", "--previous", "v0.4.1")
	if err != nil {
		t.Fatalf("resolve empty version: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(output); got != "0.5.0" {
		t.Errorf("resolved %q, want the minor bump 0.5.0", got)
	}
}

func TestResolveReleaseVersionBumpsFromZeroWithoutAPreviousRelease(t *testing.T) {
	for _, testCase := range []struct {
		bump string
		want string
	}{
		{bump: "patch", want: "0.0.1"},
		{bump: "minor", want: "0.1.0"},
		{bump: "major", want: "1.0.0"},
	} {
		t.Run(testCase.bump, func(t *testing.T) {
			output, err := runResolveReleaseVersion(t, "", "--bump", testCase.bump, "--previous", "")
			if err != nil {
				t.Fatalf("resolve %s: %v\n%s", testCase.bump, err, output)
			}
			if got := strings.TrimSpace(output); got != testCase.want {
				t.Errorf("resolved %q, want %q", got, testCase.want)
			}
		})
	}
}

// Production mutation: resolving a version at or below the newest tag would
// have the cut workflows publish a release that orders below one already
// shipped, which Homebrew would then serve as an upgrade.
func TestResolveReleaseVersionRejectsAVersionThatDoesNotMoveForward(t *testing.T) {
	for _, version := range []string{"0.4.1", "0.4.0", "0.3.0"} {
		t.Run(version, func(t *testing.T) {
			output, err := runResolveReleaseVersion(t, "", "--version", version, "--previous", "v0.4.1")
			if err == nil {
				t.Fatalf("resolve accepted %q:\n%s", version, output)
			}
			if !strings.Contains(output, "does not come after") {
				t.Errorf("output = %q, want an ordering error", output)
			}
		})
	}
}

// The same rule the release workflow and the formula renderer apply, applied
// before a tag exists rather than after one has been pushed.
func TestResolveReleaseVersionRejectsUnsafeVersions(t *testing.T) {
	for _, version := range []string{"0.5", "01.2.3", "1.2.03", "1.2.3-alpha", "v1.2.3", "1.2.3/../etc"} {
		t.Run(version, func(t *testing.T) {
			output, err := runResolveReleaseVersion(t, "", "--version", version, "--previous", "v0.4.1")
			if err == nil {
				t.Fatalf("resolve accepted %q:\n%s", version, output)
			}
		})
	}
}

func TestResolveReleaseVersionRequiresABumpOrAVersion(t *testing.T) {
	output, err := runResolveReleaseVersion(t, "", "--previous", "v0.4.1")
	if err == nil {
		t.Fatalf("resolve accepted neither a bump nor a version:\n%s", output)
	}
	if !strings.Contains(output, "either --bump or --version is required") {
		t.Errorf("output = %q, want a missing-argument error", output)
	}
}

func TestResolveReleaseVersionRejectsAnUnknownBumpKind(t *testing.T) {
	output, err := runResolveReleaseVersion(t, "", "--bump", "sideways", "--previous", "v0.4.1")
	if err == nil {
		t.Fatalf("resolve accepted an unknown bump kind:\n%s", output)
	}
	if !strings.Contains(output, "patch, minor, or major") {
		t.Errorf("output = %q, want a bump-kind error", output)
	}
}

// A tag that is not a release version cannot be bumped, and silently treating
// it as absent would restart numbering from 0.0.0.
func TestResolveReleaseVersionRejectsAnUnparsablePreviousTag(t *testing.T) {
	output, err := runResolveReleaseVersion(t, "", "--bump", "patch", "--previous", "v2026-08-08")
	if err == nil {
		t.Fatalf("resolve accepted an unparsable previous tag:\n%s", output)
	}
	if !strings.Contains(output, "MAJOR.MINOR.PATCH tag") {
		t.Errorf("output = %q, want a previous-tag error", output)
	}
}

// Interactive use passes no --previous, so discovery has to find the newest
// release rather than the most recently created tag.
func TestResolveReleaseVersionDiscoversTheNewestReleaseTag(t *testing.T) {
	repository := t.TempDir()
	runCommand(t, repository, nil, "git", "init", "--quiet", "--initial-branch=main")
	runCommand(t, repository, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, repository, nil, "git", "config", "user.email", "release-test@example.com")
	runCommand(t, repository, nil, "git", "commit", "--quiet", "--allow-empty", "-m", "init")
	// v0.10.0 is created last but orders below v0.9.0 lexically, so a lexical
	// sort would resolve the next patch as 0.9.1 rather than 0.10.1.
	for _, tag := range []string{"v0.9.0", "v0.10.0"} {
		runCommand(t, repository, nil, "git", "tag", tag)
	}

	output, err := runResolveReleaseVersionIn(t, repository, "--bump", "patch")
	if err != nil {
		t.Fatalf("resolve from repository: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(output); got != "0.10.1" {
		t.Errorf("resolved %q, want 0.10.1 from the newest release v0.10.0", got)
	}
}

func runResolveReleaseVersion(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	return runReleaseScript(t, "", "resolve-release-version.sh", stdin, args...)
}

func runResolveReleaseVersionIn(t *testing.T, directory string, args ...string) (string, error) {
	t.Helper()
	return runReleaseScript(t, directory, "resolve-release-version.sh", "", args...)
}

// runReleaseScript runs one of this repository's scripts by absolute path, so
// the script resolves its own siblings while git commands inside it run against
// directory.
func runReleaseScript(t *testing.T, directory, name, stdin string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command(releaseScriptPath(t, name), args...)
	command.Dir = directory
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func releaseScriptPath(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine release script path")
	}
	return filepath.Join(filepath.Dir(filename), name)
}
