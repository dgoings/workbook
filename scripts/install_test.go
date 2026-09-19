package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallBuildsRunnableWorkbookInDestination(t *testing.T) {
	root, script := paths(t)
	destinationRoot := t.TempDir()
	destination := filepath.Join(destinationRoot, "bin")
	physicalDestinationRoot, err := filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	installedDestination := filepath.Join(physicalDestinationRoot, "bin")

	command := exec.Command(script, destination)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	binary := filepath.Join(installedDestination, "workbook")
	if !strings.Contains(string(output), "Installed Workbook at "+binary) {
		t.Fatalf("installer output = %q, want installed path %q", output, binary)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("installed binary mode = %v, want executable", info.Mode())
	}

	run := exec.Command(binary)
	stdout, err := run.Output()
	if err != nil {
		t.Fatalf("workbook exit error = %v, want success", err)
	}
	if !strings.Contains(string(stdout), "Usage: workbook <command>") {
		t.Fatalf("workbook stdout = %q, want global help", stdout)
	}

	if _, err := os.Stat(filepath.Join(root, "workbook")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository root workbook binary exists or cannot be checked: %v", err)
	}
}

func TestInstallResolvesRelativeDestinationFromCallerDirectory(t *testing.T) {
	root, script := paths(t)
	callerDirectory := t.TempDir()
	relativeDestination := "relative-bin"
	physicalCallerDirectory, err := filepath.EvalSymlinks(callerDirectory)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(physicalCallerDirectory, relativeDestination)
	repositoryDestination := filepath.Join(root, relativeDestination)
	if _, err := os.Stat(repositoryDestination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository destination exists before test or cannot be checked: %v", err)
	}

	command := exec.Command(script, relativeDestination)
	command.Dir = callerDirectory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	binary := filepath.Join(destination, "workbook")
	if !strings.Contains(string(output), "Installed Workbook at "+binary) {
		t.Fatalf("installer output = %q, want caller-relative installed path %q", output, binary)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("stat caller-relative installed binary: %v", err)
	}
	if _, err := os.Stat(repositoryDestination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installer created repository-root destination or it cannot be checked: %v", err)
	}
}

func TestInstallReportsMissingPrerequisites(t *testing.T) {
	root, script := paths(t)

	t.Run("go", func(t *testing.T) {
		path := t.TempDir()
		command := exec.Command("/bin/sh", script, filepath.Join(t.TempDir(), "bin"))
		command.Dir = root
		command.Env = []string{"PATH=" + path}
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("install succeeded without go; output = %q", output)
		}
		if !strings.Contains(string(output), "go is required") {
			t.Fatalf("installer output = %q, want actionable missing-go message", output)
		}
	})

	t.Run("git", func(t *testing.T) {
		path := t.TempDir()
		goPath, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(goPath, filepath.Join(path, "go")); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("/bin/sh", script, filepath.Join(t.TempDir(), "bin"))
		command.Dir = root
		command.Env = []string{"PATH=" + path}
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("install succeeded without git; output = %q", output)
		}
		if !strings.Contains(string(output), "git is required") {
			t.Fatalf("installer output = %q, want actionable missing-git message", output)
		}
	})
}

func paths(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	return root, filepath.Join(root, "scripts", "install.sh")
}

func TestInstallStampsVersionAndCommit(t *testing.T) {
	// Production mutation: building without ldflags leaves every source install
	// reporting "dev (unknown)", so a developer cannot tell which build they are
	// running, and a benchmark report of it cannot name the commit it measured.
	root, script := paths(t)
	destination := filepath.Join(t.TempDir(), "bin")

	command := exec.Command(script, destination)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	stdout, err := exec.Command(filepath.Join(destination, "workbook"), "version").Output()
	if err != nil {
		t.Fatalf("workbook version: %v", err)
	}
	reported := strings.TrimSpace(string(stdout))
	if strings.Contains(reported, "dev") || strings.Contains(reported, "unknown") {
		t.Fatalf("workbook version = %q, want a stamped version and commit", reported)
	}

	expectedCommit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	if !strings.Contains(reported, strings.TrimSpace(string(expectedCommit))) {
		t.Fatalf("workbook version = %q, want commit %q", reported, expectedCommit)
	}
	// The stamped version is the describe the installer runs, restricted to the
	// CLI's own v* tags for the reason the next test pins down.
	expectedVersion := gitOutput(t, root, "describe", "--tags", "--match", "v*", "--always", "--dirty")
	if !strings.Contains(reported, expectedVersion) {
		t.Fatalf("workbook version = %q, want version %q", reported, expectedVersion)
	}
}

// Production mutation: a describe with no --match takes whichever tag git
// orders first, and for two annotated tags on one commit that is the later
// tagger date. A cascaded desktop release tags the CLI's released commit
// second, so the CLI bundled in the app would report the app's own version.
func TestInstallStampsTheCLITagWhenADesktopTagSharesTheCommit(t *testing.T) {
	root, script := paths(t)
	fixture := filepath.Join(t.TempDir(), "workbook")
	// A clone rather than the checkout itself: the tags below are the point of
	// the fixture, and this test has no business creating them in the tree it
	// is run from. The clone carries the committed installer, so the one under
	// test is copied over it; otherwise this would measure HEAD's script and
	// pass or fail for the wrong reason.
	gitOutput(t, root, "clone", "--quiet", root, fixture)
	installer, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "scripts", "install.sh"), installer, 0o700); err != nil {
		t.Fatalf("write fixture installer: %v", err)
	}
	runCommand(t, fixture, nil, "git", "config", "user.name", "Release Test")
	runCommand(t, fixture, nil, "git", "config", "user.email", "release-test@example.com")
	// Committed before the tags, so the fixture tree is clean and the stamp is
	// the bare tag rather than a "-dirty" one that would hide it. --allow-empty
	// because the copy above changes nothing whenever the installer under test
	// is the committed one, which is the ordinary case.
	runCommand(t, fixture, nil, "git", "commit", "--quiet", "--allow-empty", "--all", "--message", "installer under test")
	runCommand(t, fixture, nil, "git", "tag", "--annotate", "v1.0.0", "--message", "Workbook v1.0.0")
	// Created second, so its tagger date is later: this is the tie an unmatched
	// describe breaks the wrong way.
	runCommand(t, fixture, nil, "git", "tag", "--annotate", "desktop-v1.0.0", "--message", "Workbench desktop-v1.0.0")
	if unmatched := gitOutput(t, fixture, "describe", "--tags"); unmatched != "desktop-v1.0.0" {
		t.Fatalf("unmatched describe = %q, want desktop-v1.0.0 so the fixture reproduces the ambiguity", unmatched)
	}

	destination := filepath.Join(t.TempDir(), "bin")
	command := exec.Command(filepath.Join(fixture, "scripts", "install.sh"), destination)
	command.Dir = fixture
	if output, installErr := command.CombinedOutput(); installErr != nil {
		t.Fatalf("install: %v\n%s", installErr, output)
	}

	stdout, versionErr := exec.Command(filepath.Join(destination, "workbook"), "version").Output()
	if versionErr != nil {
		t.Fatalf("workbook version: %v", versionErr)
	}
	reported := strings.TrimSpace(string(stdout))
	fixtureCommit := gitOutput(t, fixture, "rev-parse", "HEAD")
	if want := "workbook v1.0.0 (" + fixtureCommit + ")"; reported != want {
		t.Fatalf("workbook version = %q, want %q", reported, want)
	}
}

func TestInstallAcceptsAnAlternateBinaryName(t *testing.T) {
	// Production mutation: a fixed binary name forces a source build to shadow a
	// released install that shares the destination directory.
	root, script := paths(t)
	destinationRoot := t.TempDir()
	destination := filepath.Join(destinationRoot, "bin")
	physicalDestinationRoot, err := filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(script, destination, "workbook-dev")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	binary := filepath.Join(physicalDestinationRoot, "bin", "workbook-dev")
	if !strings.Contains(string(output), "Installed Workbook at "+binary) {
		t.Fatalf("installer output = %q, want installed path %q", output, binary)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workbook")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installer also wrote the default name: %v", err)
	}
}

func TestInstallRejectsUnusableBinaryNames(t *testing.T) {
	// Production mutation: interpolating an unchecked name into the output path
	// lets an argument such as ../workbook escape the destination directory.
	root, script := paths(t)

	for name, argument := range map[string]string{
		"empty":     "",
		"path":      "nested/workbook",
		"traversal": "../workbook",
	} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "bin")
			command := exec.Command(script, destination, argument)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("installer accepted name %q: %s", argument, output)
			}
			if !strings.Contains(string(output), "binary name") {
				t.Fatalf("installer output = %q, want a binary name error", output)
			}
		})
	}
}
