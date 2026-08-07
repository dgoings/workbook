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
	if _, err := exec.LookPath("node"); err != nil {
		testenv.MissingCapability(t, "node is required to prove the capability preflight accepts a full environment")
	}
	_, script := checkCapabilitiesPaths(t)

	output, err := exec.Command(script).CombinedOutput()
	if err != nil {
		t.Fatalf("capability preflight rejected this environment: %v\n%s", err, output)
	}
	for _, want := range []string{"node ", "--object-format=sha256", "every optional test capability is present"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("preflight output %q does not report %q", output, want)
		}
	}
}

// Production mutation: passing without node lets the 36 embedded client
// behavior tests skip while the job stays green.
func TestCheckCICapabilitiesFailsWhenNodeIsMissing(t *testing.T) {
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

// Production mutation: passing with a Git that cannot create SHA-256
// repositories lets every cross-object-format test skip unnoticed.
func TestCheckCICapabilitiesFailsWhenGitCannotCreateSHA256Repositories(t *testing.T) {
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
// exists, and is simply absent when it does not.
func capabilityProbePATH(t *testing.T, stubs map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	for _, tool := range []string{"git", "mktemp", "rm", "node"} {
		target := filepath.Join(directory, tool)
		if stub, ok := stubs[tool]; ok {
			if err := os.WriteFile(target, []byte(stub), 0o755); err != nil {
				t.Fatalf("write %s stub: %v", tool, err)
			}
			continue
		}
		if tool == "node" {
			// Deliberately absent unless a stub asks for it: these probes exist
			// to watch the preflight react to a missing capability.
			continue
		}
		resolved, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not available to build a capability probe PATH: %v", tool, err)
		}
		if err := os.Symlink(resolved, target); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	return directory
}

func runWithPATH(script, path string) (string, error) {
	command := exec.Command(script)
	command.Env = []string{"PATH=" + path}
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
