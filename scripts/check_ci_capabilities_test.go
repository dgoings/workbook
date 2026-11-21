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

// Production mutation: a preflight that passes on an incomplete environment is
// worse than none, because CI then reports success for a suite that skipped
// the web client and SHA-256 coverage entirely.
func TestCheckCICapabilitiesAcceptsAFullyProvisionedEnvironment(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		testenv.MissingCapability(t, "node is required to prove the capability preflight accepts a full environment")
	}
	if !chromeAvailable() {
		testenv.MissingCapability(t, "Chrome or Chromium is required to prove the capability preflight accepts a full environment")
	}
	_, script := checkCapabilitiesPaths(t)

	output, err := exec.Command(script).CombinedOutput()
	if err != nil {
		t.Fatalf("capability preflight rejected this environment: %v\n%s", err, output)
	}
	for _, want := range []string{"node ", "chrome ", "--object-format=sha256", "every optional test capability is present"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("preflight output %q does not report %q", output, want)
		}
	}
}

// Production mutation: passing without node lets the 36 embedded client
// behavior tests skip while the job stays green.
func TestCheckCICapabilitiesFailsWhenNodeIsMissing(t *testing.T) {
	t.Parallel()
	_, script := checkCapabilitiesPaths(t)
	path := capabilityProbePATH(t, nil)

	output, err := runWithPATH(script, path)
	if err == nil {
		t.Fatalf("preflight accepted an environment without node:\n%s", output)
	}
	for _, want := range []string{"missing capability", "node is not on PATH", "embedded web client behavior tests"} {
		if !strings.Contains(output, want) {
			t.Errorf("preflight output %q does not report %q", output, want)
		}
	}
}

// Production mutation: passing without Chrome lets every real-browser layout
// test in internal/webui skip while the job stays green. A configured path is
// used here because it is the probe's first source, and the only one a test
// can withdraw on a Mac that has Chrome installed where the probe looks.
func TestCheckCICapabilitiesFailsWhenChromeIsMissing(t *testing.T) {
	t.Parallel()
	_, script := checkCapabilitiesPaths(t)
	path := capabilityProbePATH(t, map[string]string{"node": "#!/bin/sh\necho v22.0.0\n"})
	missing := filepath.Join(t.TempDir(), "no-chrome-here")

	output, err := runWithPATH(script, path, "WORKBOOK_TEST_CHROME="+missing)
	if err == nil {
		t.Fatalf("preflight accepted an environment without Chrome:\n%s", output)
	}
	for _, want := range []string{"missing capability", "WORKBOOK_TEST_CHROME names " + missing, "browser layout tests"} {
		if !strings.Contains(output, want) {
			t.Errorf("preflight output %q does not report %q", output, want)
		}
	}
	if strings.Contains(output, "node is not on PATH") {
		t.Errorf("preflight reported node missing, so this does not isolate Chrome:\n%s", output)
	}
}

// chromeAvailable reports whether the preflight would find a Chrome, looking
// where it looks.
func chromeAvailable() bool {
	if configured := os.Getenv("WORKBOOK_TEST_CHROME"); configured != "" {
		_, err := os.Stat(configured)
		return err == nil
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	if runtime.GOOS == "darwin" {
		for _, bundle := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
			if _, err := os.Stat(bundle); err == nil {
				return true
			}
		}
	}
	return false
}

// Production mutation: passing with a Git that cannot create SHA-256
// repositories lets every cross-object-format test skip unnoticed.
func TestCheckCICapabilitiesFailsWhenGitCannotCreateSHA256Repositories(t *testing.T) {
	t.Parallel()
	_, script := checkCapabilitiesPaths(t)
	stub := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"	*--object-format=sha256*) exit 128 ;;\n" +
		"esac\n" +
		"case \"$1\" in\n" +
		"	--version) echo 'git version 2.20.0 (stub)' ;;\n" +
		"esac\n"
	path := capabilityProbePATH(t, map[string]string{"git": stub})

	output, err := runWithPATH(script, path)
	if err == nil {
		t.Fatalf("preflight accepted a Git without SHA-256 support:\n%s", output)
	}
	for _, want := range []string{"missing capability", "SHA-256 repositories", "git version 2.20.0 (stub)"} {
		if !strings.Contains(output, want) {
			t.Errorf("preflight output %q does not report %q", output, want)
		}
	}
}

// capabilityProbePATH builds a directory holding only the tools the preflight
// needs, so a capability can be removed from the environment without touching
// the machine running the test. Entries in stubs replace the real tool with the
// given shell script; every other tool is linked from the real PATH when it
// exists, and is simply absent when it does not. A machine missing one of the
// tools it links reports a missing capability rather than a bare skip, so the
// preflight's own tests are covered by the guard they exercise.
func capabilityProbePATH(t *testing.T, stubs map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	for _, tool := range []string{"git", "mktemp", "rm", "uname", "node"} {
		target := filepath.Join(directory, tool)
		if stub, ok := stubs[tool]; ok {
			testenv.WriteExecutable(t, target, []byte(stub))
			continue
		}
		if tool == "node" {
			// Deliberately absent unless a stub asks for it: these probes exist
			// to watch the preflight react to a missing capability.
			continue
		}
		resolved, err := exec.LookPath(tool)
		if err != nil {
			testenv.MissingCapability(t, "%s is not available to build a capability probe PATH: %v", tool, err)
		}
		if err := os.Symlink(resolved, target); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	return directory
}

func runWithPATH(script, path string, environment ...string) (string, error) {
	command := exec.Command(script)
	// Built from scratch rather than os.Environ(), so the isolated git
	// configuration TestMain installs has to be threaded in explicitly or the
	// preflight's real `git init --object-format=sha256` probe would run
	// against the developer's global config instead.
	command.Env = append(append([]string{"PATH=" + path}, isolatedGitConfigValues()...), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func checkCapabilitiesPaths(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine capability preflight test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	return root, filepath.Join(root, "scripts", "check-ci-capabilities.sh")
}
