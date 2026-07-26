package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
	contents := string(formula)
	if !strings.Contains(contents, "\n  depends_on :macos\n\n  on_macos do") {
		t.Errorf("rendered formula missing top-level macOS dependency:\n%s", contents)
	}
	assertArchitectureFormulaBlock(t, contents, "on_arm do", "on_intel do", "workbook_0.1.0_darwin_arm64.tar.gz", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "workbook_0.1.0_darwin_amd64.tar.gz", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	assertArchitectureFormulaBlock(t, contents, "on_intel do", "  end\n\n  def install", "workbook_0.1.0_darwin_amd64.tar.gz", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "workbook_0.1.0_darwin_arm64.tar.gz", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
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
	var document struct {
		On map[string]struct {
			Tags []string `yaml:"tags"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(workflow, &document); err != nil {
		t.Fatalf("parse release workflow YAML: %v", err)
	}
	if len(document.On) != 1 {
		t.Errorf("workflow triggers = %#v, want only push.tags [v*]", document.On)
	} else if push, ok := document.On["push"]; !ok || len(push.Tags) != 1 || push.Tags[0] != "v*" {
		t.Errorf("workflow triggers = %#v, want only push.tags [v*]", document.On)
	}
	if strings.Count(contents, "secrets.HOMEBREW_TAP_TOKEN") != 1 || !strings.Contains(contents, "repository: dgoings/homebrew-tap\n          token: ${{ secrets.HOMEBREW_TAP_TOKEN }}") {
		t.Errorf("tap credential is not scoped to the tap update:\n%s", contents)
	}
}

func TestTrackedFormulaDeclaresMacOSDependency(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	formula, err := os.ReadFile(filepath.Join(root, "Formula", "workbook.rb"))
	if err != nil {
		t.Fatalf("read tracked formula: %v", err)
	}
	if !strings.Contains(string(formula), "\n  depends_on :macos\n\n  on_macos do") {
		t.Errorf("tracked formula missing top-level macOS dependency:\n%s", formula)
	}
}

func assertArchitectureFormulaBlock(t *testing.T, formula, start, end, wantArchive, wantChecksum, wrongArchive, wrongChecksum string) {
	t.Helper()
	startIndex := strings.Index(formula, start)
	if startIndex < 0 {
		t.Fatalf("formula missing %q:\n%s", start, formula)
	}
	blockEnd := strings.Index(formula[startIndex:], end)
	if blockEnd < 0 {
		t.Fatalf("formula missing %q after %q:\n%s", end, start, formula)
	}
	block := formula[startIndex : startIndex+blockEnd]
	for _, want := range []string{wantArchive, `sha256 "` + wantChecksum + `"`} {
		if !strings.Contains(block, want) {
			t.Errorf("%s block missing %q:\n%s", start, want, block)
		}
	}
	for _, wrong := range []string{wrongArchive, `sha256 "` + wrongChecksum + `"`} {
		if strings.Contains(block, wrong) {
			t.Errorf("%s block contains opposite-architecture value %q:\n%s", start, wrong, block)
		}
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
