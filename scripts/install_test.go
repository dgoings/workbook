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
	destination := filepath.Join(t.TempDir(), "bin")

	command := exec.Command(script, destination)
	command.Dir = root
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "gocache"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	binary := filepath.Join(destination, "workbook")
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
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		t.Fatalf("workbook exit error = %v, want exit 2", err)
	}
	if len(stdout) != 0 {
		t.Fatalf("workbook stdout = %q, want empty", stdout)
	}
	if !strings.Contains(string(exitError.Stderr), "Usage: workbook <command>") {
		t.Fatalf("workbook stderr = %q, want usage", exitError.Stderr)
	}

	if _, err := os.Stat(filepath.Join(root, "workbook")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository root workbook binary exists or cannot be checked: %v", err)
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
