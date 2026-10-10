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
	t.Parallel()
	// Production mutation: installing both builds under one name or one
	// directory removes the published fallback the moment the working tree
	// stops building.
	if testing.Short() {
		t.Skip("builds the published tag and the working tree; skipped in -short mode")
	}
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

// A clone that fetched desktop-latest once holds a stale copy after the next
// desktop release force-moves it. Choosing the newest release to build only
// needs the v* tags, so the stale rolling tag must not turn the refresh into a
// failure that falls back to local tags.
func TestSetupFetchesReleaseTagsPastAMovedRollingTag(t *testing.T) {
	t.Parallel()
	root, _ := setupPaths(t)
	remote := filepath.Join(t.TempDir(), "workbook.git")
	runCommand(t, "", nil, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	clone := filepath.Join(t.TempDir(), "workbook")
	runCommand(t, "", nil, "git", "clone", "--quiet", remote, clone)
	runCommand(t, clone, nil, "git", "config", "user.name", "Setup Test")
	runCommand(t, clone, nil, "git", "config", "user.email", "setup-test@example.com")
	if err := os.Mkdir(filepath.Join(clone, "scripts"), 0o755); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "scripts", "setup-dev-env.sh"))
	if err != nil {
		t.Fatalf("read setup-dev-env.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, "scripts", "setup-dev-env.sh"), contents, 0o755); err != nil {
		t.Fatalf("write setup-dev-env.sh: %v", err)
	}
	// Stands in for the real installer so the test builds nothing: the
	// "published build" just reports which release tag it came from.
	installer := "#!/bin/sh\nset -eu\n" +
		"root=$(CDPATH='' cd -- \"${0%/*}/..\" && pwd -P)\n" +
		"mkdir -p \"$1\"\n" +
		"printf '#!/bin/sh\\necho \"workbook %s\"\\n' \"$(cat \"${root}/VERSION\")\" > \"$1/workbook\"\n" +
		"chmod +x \"$1/workbook\"\n"
	if err := os.WriteFile(filepath.Join(clone, "scripts", "install.sh"), []byte(installer), 0o755); err != nil {
		t.Fatalf("write install.sh: %v", err)
	}
	release := func(version string) {
		if err := os.WriteFile(filepath.Join(clone, "VERSION"), []byte(version+"\n"), 0o600); err != nil {
			t.Fatalf("write VERSION: %v", err)
		}
		runCommand(t, clone, nil, "git", "add", ".")
		runCommand(t, clone, nil, "git", "commit", "--quiet", "-m", "release "+version)
		runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "main")
	}
	release("v0.1.0")
	for _, tag := range []string{"v0.1.0", "desktop-latest"} {
		runCommand(t, clone, nil, "git", "tag", tag)
		runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/"+tag)
	}
	// Publish v0.2.0 only on the remote and move desktop-latest past this
	// clone's copy, as the desktop publisher does.
	release("v0.2.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "HEAD:refs/tags/v0.2.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "--force", "origin", "HEAD:refs/tags/desktop-latest")
	// A clone configured to fetch every tag from origin must still leave the
	// rolling tag out; dropping --no-tags lets this setting pull it back in.
	runCommand(t, clone, nil, "git", "config", "remote.origin.tagOpt", "--tags")

	stablePrefix := filepath.Join(t.TempDir(), "stable")
	command := exec.Command(filepath.Join(clone, "scripts", "setup-dev-env.sh"),
		"--stable-only", "--stable-method", "source", "--no-profile")
	command.Dir = clone
	command.Env = append(os.Environ(), "WORKBOOK_STABLE_PREFIX="+stablePrefix)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, output)
	}

	// Production mutation: fetching every tag fails on the stale rolling tag,
	// so setup warns that it could not refresh and trusts possibly stale tags.
	if strings.Contains(string(output), "could not fetch") {
		t.Errorf("setup output = %q, want the release tags fetched without a warning", output)
	}
	if reported := reportedVersion(t, filepath.Join(stablePrefix, "bin", "workbook")); reported != "workbook v0.2.0" {
		t.Errorf("published build reports %q, want the newest release v0.2.0", reported)
	}
}

func TestSetupKeepsTheSkippedBuildOnPath(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
