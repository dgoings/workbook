package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderHomebrewFormulaReadsExactChecksums(t *testing.T) {
	root, script := renderFormulaPaths(t)
	checksums := filepath.Join(t.TempDir(), "checksums.txt")
	output := filepath.Join(t.TempDir(), "workbook.rb")
	if err := os.WriteFile(checksums, []byte(strings.Join([]string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  workbook_0.1.0_darwin_arm64.tar.gz",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  workbook_0.1.0_darwin_amd64.tar.gz",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	command := exec.Command(script, "0.1.0", checksums, output, "dgoings/workbook")
	command.Dir = root
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render formula: %v\n%s", err, result)
	}
	formula, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read formula: %v", err)
	}
	if !strings.Contains(string(formula), "sha256 \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"") ||
		!strings.Contains(string(formula), "sha256 \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"") {
		t.Fatalf("formula checksums = %q, want both checksum-file values", formula)
	}
}

func TestRenderHomebrewFormulaRejectsMissingOrDuplicateChecksums(t *testing.T) {
	root, script := renderFormulaPaths(t)
	tests := map[string]string{
		"missing": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  workbook_0.1.0_darwin_arm64.tar.gz\n",
		"duplicate": strings.Join([]string{
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  workbook_0.1.0_darwin_arm64.tar.gz",
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  workbook_0.1.0_darwin_arm64.tar.gz",
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  workbook_0.1.0_darwin_amd64.tar.gz",
			"",
		}, "\n"),
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			checksums := filepath.Join(t.TempDir(), "checksums.txt")
			if err := os.WriteFile(checksums, []byte(contents), 0o600); err != nil {
				t.Fatalf("write checksums: %v", err)
			}
			command := exec.Command(script, "0.1.0", checksums, filepath.Join(t.TempDir(), "workbook.rb"), "dgoings/workbook")
			command.Dir = root
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("render formula unexpectedly succeeded: %s", output)
			}
		})
	}
}

func TestReleaseWorkflowIsTagOnlyAndPublishesFormula(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	contents := string(workflow)
	for _, want := range []string{
		"push:",
		"tags: [\"v*\"]",
		"go test ./...",
		"workbook_${VERSION}_darwin_arm64.tar.gz",
		"workbook_${VERSION}_darwin_amd64.tar.gz",
		"checksums.txt",
		"gh release create",
		"HOMEBREW_TAP_TOKEN",
		"dgoings/homebrew-tap",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("workflow missing %q:\n%s", want, contents)
		}
	}
	if strings.Contains(contents, "pull_request:") || strings.Contains(contents, "workflow_dispatch:") {
		t.Errorf("workflow has a non-tag trigger:\n%s", contents)
	}
	if strings.Count(contents, "secrets.HOMEBREW_TAP_TOKEN") != 1 || !strings.Contains(contents, "repository: dgoings/homebrew-tap\n          token: ${{ secrets.HOMEBREW_TAP_TOKEN }}") {
		t.Errorf("tap credential is not scoped to the tap update:\n%s", contents)
	}
}

func renderFormulaPaths(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine render-formula test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	return root, filepath.Join(root, "scripts", "render-homebrew-formula.sh")
}
