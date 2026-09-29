package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every other installer test passes an explicit destination, so the two things
// the README promises a reader who just runs the script were untested: that it
// creates $HOME/.local/bin when it does not exist, and that it says how to put
// that directory on PATH when it is not already there.
func TestInstallCreatesTheDefaultDestinationAndReportsThePATHExport(t *testing.T) {
	t.Parallel()
	root, script := paths(t)
	home := t.TempDir()
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(physicalHome, ".local", "bin")
	if _, err := os.Stat(destination); err == nil {
		t.Fatalf("%s exists before the installer runs", destination)
	}

	command := exec.Command(script)
	command.Dir = root
	command.Env = buildEnvironment(t, "HOME="+home, "PATH="+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}

	binary := filepath.Join(destination, "workbook")
	if !strings.Contains(string(output), "Installed Workbook at "+binary) {
		t.Fatalf("installer output = %q, want the default destination %q", output, binary)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}

	// The hint is the whole point of the check: a destination the shell cannot
	// find leaves the reader with a working binary they cannot run.
	wantHint := "export PATH=\"" + destination + ":$PATH\""
	if !strings.Contains(string(output), wantHint) {
		t.Fatalf("installer output = %q, want the PATH hint %q", output, wantHint)
	}
}

// And it has to stay quiet when the hint would be wrong: a destination already
// on PATH that printed an export line would teach the reader to duplicate it.
func TestInstallOmitsThePATHExportWhenTheDestinationIsAlreadyOnPATH(t *testing.T) {
	t.Parallel()
	root, script := paths(t)
	destinationRoot := t.TempDir()
	physicalRoot, err := filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(physicalRoot, "bin")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(script, destination)
	command.Dir = root
	command.Env = buildEnvironment(t, "PATH="+destination+":"+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "export PATH=") {
		t.Fatalf("installer output = %q, want no PATH hint for a destination already on PATH", output)
	}
	if !strings.Contains(string(output), "Installed Workbook at "+filepath.Join(destination, "workbook")) {
		t.Fatalf("installer output = %q, want the installed path", output)
	}
}

// buildEnvironment gives the installer a Go toolchain that does not depend on
// the HOME this test replaced: with HOME pointed at a temporary directory, the
// default module, build, and download caches move with it and the build would
// either re-download the world or recompile it from scratch.
//
// The values come from goToolchainValues, which reads back what TestMain pinned
// into this process's environment before it replaced HOME. This file used to
// resolve them a second time with its own `go env` at package initialization;
// one mechanism for the invariant is enough, and TestMain's runs early enough
// for every caller.
func buildEnvironment(t *testing.T, entries ...string) []string {
	t.Helper()
	environment := append(append([]string(nil), entries...), goToolchainValues()...)
	// Built from scratch rather than os.Environ(), so the isolated git
	// configuration TestMain installs has to be threaded in explicitly or
	// install.sh's own `git describe`/`git rev-parse` would run against the
	// developer's global config instead. Appended last: neither entries nor the
	// toolchain values ever set these keys, so there is nothing to collide
	// with.
	return append(environment, isolatedGitConfigValues()...)
}
