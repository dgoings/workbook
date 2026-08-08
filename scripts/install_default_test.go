package scripts_test

import (
	"fmt"
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

// toolchainDirectories resolves GOPATH, GOMODCACHE, and GOCACHE at package
// initialization, before any TestMain could replace HOME process-wide. All
// three default to paths under HOME, and internal/cli/main_test.go documents
// how `go env` run after such a swap reports the temporary paths instead of
// the developer's real toolchain. This package has no TestMain today; the
// snapshot keeps these tests correct on the day one appears.
var toolchainDirectories, toolchainDirectoriesErr = resolveToolchainDirectories()

func resolveToolchainDirectories() ([]string, error) {
	names := []string{"GOPATH", "GOMODCACHE", "GOCACHE"}
	output, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		return nil, err
	}
	values := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
	if len(values) != len(names) {
		return nil, fmt.Errorf("go env %s reported %d values", strings.Join(names, " "), len(values))
	}
	directories := make([]string, 0, len(names))
	for index, name := range names {
		directories = append(directories, name+"="+values[index])
	}
	return directories, nil
}

// buildEnvironment gives the installer a Go toolchain that does not depend on
// the HOME this test replaced: with HOME pointed at a temporary directory, the
// default module, build, and download caches move with it and the build would
// either re-download the world or recompile it from scratch.
func buildEnvironment(t *testing.T, entries ...string) []string {
	t.Helper()
	if toolchainDirectoriesErr != nil {
		t.Fatalf("resolve toolchain directories: %v", toolchainDirectoriesErr)
	}
	return append(append([]string(nil), entries...), toolchainDirectories...)
}
