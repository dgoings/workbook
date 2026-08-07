package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
)

func TestSetupInstallsPublishedAndWorkingTreeBuildsSideBySide(t *testing.T) {
	// Production mutation: installing both builds under one name or one
	// directory removes the published fallback the moment the working tree
	// stops building.
	root, script := setupPaths(t)
	version := latestReleaseTag(t, root)
	stablePrefix := filepath.Join(t.TempDir(), "stable")
	devPrefix := filepath.Join(t.TempDir(), "dev")
	profile := filepath.Join(t.TempDir(), "profile")

	command := exec.Command(script, "--stable-method", "source", "--stable-version", version)
	command.Dir = root
	command.Env = append(os.Environ(),
		"WORKBOOK_STABLE_PREFIX="+stablePrefix,
		"WORKBOOK_DEV_PREFIX="+devPrefix,
		"WORKBOOK_SETUP_PROFILE="+profile,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	stable := filepath.Join(stablePrefix, "bin", "workbook")
	development := filepath.Join(devPrefix, "bin", "workbook-dev")
	if reported := reportedVersion(t, stable); !strings.Contains(reported, version) {
		t.Fatalf("published build version = %q, want release tag %q", reported, version)
	}
	if reported := reportedVersion(t, development); !strings.Contains(reported, headCommit(t, root)) {
		t.Fatalf("working-tree build version = %q, want the checked-out commit", reported)
	}
	// Neither install may write the other's name, or the two shadow each other
	// wherever both directories are on PATH.
	for _, unexpected := range []string{
		filepath.Join(stablePrefix, "bin", "workbook-dev"),
		filepath.Join(devPrefix, "bin", "workbook"),
	} {
		if _, err := os.Stat(unexpected); err == nil {
			t.Fatalf("setup also installed %q", unexpected)
		}
	}

	contents := readFile(t, profile)
	for _, directory := range []string{filepath.Join(stablePrefix, "bin"), filepath.Join(devPrefix, "bin")} {
		if !strings.Contains(contents, directory) {
			t.Fatalf("profile = %q, want PATH entry for %q", contents, directory)
		}
	}
}

func TestSetupKeepsTheSkippedBuildOnPath(t *testing.T) {
	// Production mutation: rewriting the profile from only the current run drops
	// the published build from PATH whenever the working tree is rebuilt alone.
	root, script := setupPaths(t)
	stablePrefix := filepath.Join(t.TempDir(), "stable")
	devPrefix := filepath.Join(t.TempDir(), "dev")
	profile := filepath.Join(t.TempDir(), "profile")
	if err := os.MkdirAll(filepath.Join(stablePrefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(script, "--dev-only")
	command.Dir = root
	command.Env = append(os.Environ(),
		"WORKBOOK_STABLE_PREFIX="+stablePrefix,
		"WORKBOOK_DEV_PREFIX="+devPrefix,
		"WORKBOOK_SETUP_PROFILE="+profile,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	contents := readFile(t, profile)
	if !strings.Contains(contents, filepath.Join(stablePrefix, "bin")) {
		t.Fatalf("profile = %q, want the retained published directory", contents)
	}
	if !strings.Contains(contents, filepath.Join(devPrefix, "bin")) {
		t.Fatalf("profile = %q, want the working-tree directory", contents)
	}
}

func TestSetupInstallsThePublishedBuildThroughHomebrew(t *testing.T) {
	// Production mutation: building the published side from source everywhere
	// ignores the tap that macOS users actually install and upgrade from.
	root, script := setupPaths(t)
	brewRoot := t.TempDir()
	profile := filepath.Join(t.TempDir(), "profile")

	command := exec.Command(script, "--stable-only", "--stable-method", "brew")
	command.Dir = root
	command.Env = append(os.Environ(),
		"PATH="+fakeBrewDirectory(t, brewRoot)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"WORKBOOK_SETUP_PROFILE="+profile,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	invocations := readFile(t, filepath.Join(brewRoot, "invocations"))
	if !strings.Contains(invocations, "install dgoings/tap/workbook") {
		t.Fatalf("brew invocations = %q, want an install of the tap formula", invocations)
	}
	installed := filepath.Join(brewRoot, "prefix", "bin", "workbook")
	if !strings.Contains(string(output), installed) {
		t.Fatalf("setup output = %q, want the Homebrew binary %q", output, installed)
	}
	if contents := readFile(t, profile); !strings.Contains(contents, filepath.Join(brewRoot, "prefix", "bin")) {
		t.Fatalf("profile = %q, want the Homebrew binary directory", contents)
	}
}

func TestSetupUpgradesAnAlreadyInstalledFormula(t *testing.T) {
	// Production mutation: unconditionally installing fails on a machine that
	// already has the formula, so the fallback is never refreshed.
	root, script := setupPaths(t)
	brewRoot := t.TempDir()
	brewDirectory := fakeBrewDirectory(t, brewRoot)
	if err := os.WriteFile(filepath.Join(brewRoot, "installed"), []byte("0.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(script, "--stable-only", "--stable-method", "brew", "--no-profile")
	command.Dir = root
	command.Env = append(os.Environ(),
		"PATH="+brewDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	invocations := readFile(t, filepath.Join(brewRoot, "invocations"))
	if !strings.Contains(invocations, "upgrade dgoings/tap/workbook") {
		t.Fatalf("brew invocations = %q, want an upgrade of the tap formula", invocations)
	}
	if strings.Contains(invocations, "install dgoings/tap/workbook") {
		t.Fatalf("brew invocations = %q, want no reinstall of an installed formula", invocations)
	}
}

func TestSetupReplacesTheProfileBlockOnRepeatedRuns(t *testing.T) {
	// Production mutation: appending a new block every run grows the profile and
	// stacks duplicate PATH entries.
	root, script := setupPaths(t)
	brewRoot := t.TempDir()
	brewDirectory := fakeBrewDirectory(t, brewRoot)
	profile := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(profile, []byte("# existing profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for run := range 2 {
		command := exec.Command(script, "--stable-only", "--stable-method", "brew")
		command.Dir = root
		command.Env = append(os.Environ(),
			"PATH="+brewDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
			"WORKBOOK_SETUP_PROFILE="+profile,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("setup run %d: %v\n%s", run+1, err, output)
		}
	}

	contents := readFile(t, profile)
	if blocks := strings.Count(contents, ">>> workbook development environment >>>"); blocks != 1 {
		t.Fatalf("profile = %q, want exactly one managed block, got %d", contents, blocks)
	}
	if !strings.Contains(contents, "# existing profile") {
		t.Fatalf("profile = %q, want the unrelated existing content preserved", contents)
	}
	if entries := strings.Count(contents, filepath.Join(brewRoot, "prefix", "bin")+":${PATH}"); entries != 1 {
		t.Fatalf("profile = %q, want one PATH entry per directory, got %d", contents, entries)
	}

	info, err := os.Stat(profile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v, want the original 0600", info.Mode().Perm())
	}
}

func TestSetupLeavesTheProfileAloneWhenAsked(t *testing.T) {
	root, script := setupPaths(t)
	brewRoot := t.TempDir()
	profile := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(profile, []byte("# existing profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(script, "--stable-only", "--stable-method", "brew", "--no-profile")
	command.Dir = root
	command.Env = append(os.Environ(),
		"PATH="+fakeBrewDirectory(t, brewRoot)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"WORKBOOK_SETUP_PROFILE="+profile,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	if contents := readFile(t, profile); contents != "# existing profile\n" {
		t.Fatalf("profile = %q, want it unchanged", contents)
	}
}

func TestSetupRejectsUnusableOptions(t *testing.T) {
	root, script := setupPaths(t)

	for name, testCase := range map[string]struct {
		arguments []string
		message   string
	}{
		"unknown option":     {arguments: []string{"--publish"}, message: "unknown option"},
		"missing value":      {arguments: []string{"--stable-method"}, message: "requires a value"},
		"unknown method":     {arguments: []string{"--stable-method", "curl"}, message: "must be auto, brew, or source"},
		"nothing to install": {arguments: []string{"--stable-only", "--dev-only"}, message: "cannot be combined"},
		"pinned brew build":  {arguments: []string{"--stable-method", "brew", "--stable-version", "v0.2.0"}, message: "cannot be used with"},
	} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command(script, testCase.arguments...)
			command.Dir = root
			command.Env = append(os.Environ(),
				"WORKBOOK_STABLE_PREFIX="+filepath.Join(t.TempDir(), "stable"),
				"WORKBOOK_DEV_PREFIX="+filepath.Join(t.TempDir(), "dev"),
				"WORKBOOK_SETUP_PROFILE="+filepath.Join(t.TempDir(), "profile"),
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("setup accepted %v: %s", testCase.arguments, output)
			}
			if !strings.Contains(string(output), testCase.message) {
				t.Fatalf("setup output = %q, want %q", output, testCase.message)
			}
		})
	}
}

// fakeBrewDirectory writes a brew stand-in that records its invocations and
// installs a runnable stub, so Homebrew behaviour can be exercised on any
// platform. It returns the directory to place ahead of PATH.
func fakeBrewDirectory(t *testing.T, brewRoot string) string {
	t.Helper()
	directory := filepath.Join(brewRoot, "bin")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	brew := "#!/bin/sh\n" +
		"set -eu\n" +
		"echo \"$*\" >> \"" + brewRoot + "/invocations\"\n" +
		"case $1 in\n" +
		"	list) test -f \"" + brewRoot + "/installed\" ;;\n" +
		"	install | upgrade)\n" +
		"		mkdir -p \"" + brewRoot + "/prefix/bin\"\n" +
		"		printf '#!/bin/sh\\necho \"workbook 0.2.0 (released)\"\\n' > \"" + brewRoot + "/prefix/bin/workbook\"\n" +
		"		chmod 0755 \"" + brewRoot + "/prefix/bin/workbook\"\n" +
		"		touch \"" + brewRoot + "/installed\"\n" +
		"		;;\n" +
		"	--prefix) echo \"" + brewRoot + "/prefix\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(directory, "brew"), []byte(brew), 0o755); err != nil {
		t.Fatal(err)
	}
	return directory
}

func latestReleaseTag(t *testing.T, root string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", root, "tag", "--list", "v[0-9]*", "--sort=-v:refname").Output()
	if err != nil {
		t.Fatalf("list release tags: %v", err)
	}
	tags := strings.Fields(string(output))
	if len(tags) == 0 {
		testenv.MissingCapability(t, "no release tag is available in this clone")
	}
	return tags[0]
}

func headCommit(t *testing.T, root string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func reportedVersion(t *testing.T, binary string) string {
	t.Helper()
	output, err := exec.Command(binary, "version").Output()
	if err != nil {
		t.Fatalf("%s version: %v", binary, err)
	}
	return strings.TrimSpace(string(output))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func setupPaths(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	return root, filepath.Join(root, "scripts", "setup-dev-env.sh")
}
