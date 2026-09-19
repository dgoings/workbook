package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The desktop app stages the CLI it bundles with this script. In-tree it builds
// from the checkout it sits in, so the shell and the CLI it ships cannot drift;
// WORKBOOK_REF still lets a release build name a tag without moving anyone's
// working tree.

func desktopStagePaths(t *testing.T) (string, string) {
	t.Helper()
	root, _ := paths(t)
	return root, filepath.Join(root, "desktop", "scripts", "build-workbook.sh")
}

func TestDesktopStageBuildsFromThisCheckout(t *testing.T) {
	root, script := desktopStagePaths(t)
	output := t.TempDir()

	command := exec.Command(script, output)
	command.Dir = root
	command.Env = append(os.Environ(), "WORKBOOK_REPO=", "WORKBOOK_REF=")
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v\n%s", err, combined)
	}

	binary := filepath.Join(output, "workbook")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat staged binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("staged binary mode = %v, want executable", info.Mode())
	}
	if _, err := os.Stat(filepath.Join(output, "WORKBOOK-LICENSE")); err != nil {
		t.Fatalf("stat staged license: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "workbook-src")); !os.IsNotExist(err) {
		t.Fatalf("default stage cloned the checkout; want it to build in place (err=%v)", err)
	}

	// Stamped from this checkout, the way install.sh stamps a source build.
	described := gitOutput(t, root, "describe", "--tags", "--always", "--dirty")
	version, err := exec.Command(binary, "version").Output()
	if err != nil {
		t.Fatalf("workbook version: %v", err)
	}
	if !strings.Contains(string(version), described) {
		t.Fatalf("workbook version = %q, want it to report %q", version, described)
	}
	if !strings.Contains(string(combined), "build-workbook: staged ") {
		t.Fatalf("stage output = %q, want the staged banner", combined)
	}
}

func TestDesktopStageBuildsANamedRefWithoutMovingTheWorkingTree(t *testing.T) {
	root, script := desktopStagePaths(t)
	output := t.TempDir()

	head := gitOutput(t, root, "rev-parse", "HEAD")
	ref := gitOutput(t, root, "rev-parse", "HEAD~1")

	command := exec.Command(script, output)
	command.Dir = root
	command.Env = append(os.Environ(), "WORKBOOK_REPO=", "WORKBOOK_REF="+ref)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v\n%s", err, combined)
	}

	clone := filepath.Join(output, "workbook-src")
	if got := gitOutput(t, clone, "rev-parse", "HEAD"); got != ref {
		t.Fatalf("clone HEAD = %s, want the named ref %s", got, ref)
	}
	if got := gitOutput(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("working tree HEAD moved from %s to %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(output, "workbook")); err != nil {
		t.Fatalf("stat staged binary: %v", err)
	}
	if !strings.Contains(string(combined), "build-workbook: cloning") {
		t.Fatalf("stage output = %q, want it to report the clone", combined)
	}
}

func TestDesktopStageUsesAGivenCheckoutAsItStands(t *testing.T) {
	root, script := desktopStagePaths(t)
	output := t.TempDir()

	// A second checkout of this repository, detached at HEAD~1, is what a
	// developer pointing WORKBOOK_REPO at another clone looks like.
	other := filepath.Join(t.TempDir(), "other")
	gitOutput(t, root, "clone", "--quiet", root, other)
	older := gitOutput(t, root, "rev-parse", "HEAD~1")
	gitOutput(t, other, "checkout", "--quiet", "--detach", older)

	command := exec.Command(script, output)
	command.Dir = root
	command.Env = append(os.Environ(), "WORKBOOK_REPO="+other, "WORKBOOK_REF="+gitOutput(t, root, "rev-parse", "HEAD"))
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v\n%s", err, combined)
	}
	if got := gitOutput(t, other, "rev-parse", "HEAD"); got != older {
		t.Fatalf("WORKBOOK_REPO checkout moved from %s to %s; the script must not change a working tree", older, got)
	}
	if !strings.Contains(string(combined), "using local checkout") {
		t.Fatalf("stage output = %q, want it to say the local checkout was used", combined)
	}
}

// goEnv reads one of the Go toolchain's own settings, which is what the script
// compares GOOS/GOARCH against to decide whether the binary it just built can
// run here.
func goEnv(t *testing.T, root string, name string) string {
	t.Helper()
	command := exec.Command("go", "env", name)
	command.Dir = root
	out, err := command.Output()
	if err != nil {
		t.Fatalf("go env %s: %v", name, err)
	}
	return strings.TrimSpace(string(out))
}

// The workflow stages one CLI per target on one runner. A cross-compiled
// binary cannot run on the host, so the banner that prints its version has
// to be skipped rather than crash the stage.
func TestDesktopStageCrossCompilesAndSkipsTheBanner(t *testing.T) {
	root, script := desktopStagePaths(t)
	output := t.TempDir()
	hostArch := goEnv(t, root, "GOHOSTARCH")
	otherArch := "arm64"
	if hostArch == "arm64" {
		otherArch = "amd64"
	}

	command := exec.Command(script, output)
	command.Dir = root
	command.Env = append(os.Environ(), "WORKBOOK_REPO=", "WORKBOOK_REF=", "GOARCH="+otherArch)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v\n%s", err, combined)
	}
	if _, err := os.Stat(filepath.Join(output, "workbook")); err != nil {
		t.Fatalf("stat staged binary: %v", err)
	}
	if !strings.Contains(string(combined), "build-workbook: staged") {
		t.Fatalf("stage output = %q, want the staged banner", combined)
	}
	if strings.Contains(string(combined), "exec format error") {
		t.Fatalf("stage tried to run a cross-compiled binary:\n%s", combined)
	}
}

func TestDesktopStageNamesAWindowsBinary(t *testing.T) {
	root, script := desktopStagePaths(t)
	output := t.TempDir()
	command := exec.Command(script, output)
	command.Dir = root
	command.Env = append(os.Environ(), "WORKBOOK_REPO=", "WORKBOOK_REF=", "GOOS=windows", "GOARCH=amd64")
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("stage: %v\n%s", err, combined)
	}
	if _, err := os.Stat(filepath.Join(output, "workbook.exe")); err != nil {
		t.Fatalf("stat staged windows binary: %v", err)
	}
}
