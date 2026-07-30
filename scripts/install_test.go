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
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
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
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
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
	// running, and the binary is rejected by acceptance benchmarking.
	root, script := paths(t)
	destination := filepath.Join(t.TempDir(), "bin")

	command := exec.Command(script, destination)
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
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
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
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
