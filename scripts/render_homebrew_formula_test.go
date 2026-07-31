package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
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
	if err := os.WriteFile(checksums, []byte(fixtureChecksums("0.1.0")), 0o600); err != nil {
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
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("stat formula: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("rendered formula mode = %o, want 644", info.Mode().Perm())
	}
	contents := string(formula)
	if !strings.HasPrefix(contents, "# typed: strict\n# frozen_string_literal: true\n") {
		t.Errorf("rendered formula missing Homebrew Sorbet sigil:\n%s", contents)
	}
	// Production mutation: keeping the macOS-only dependency would reject the
	// formula on Linux even though the script now reads Linux checksums.
	if strings.Contains(contents, "depends_on :macos") {
		t.Errorf("rendered formula still restricts installation to macOS:\n%s", contents)
	}

	macos := formulaSection(t, contents, "on_macos do", "\n  on_linux do")
	assertArchitectureFormulaBlock(t, macos, "on_arm do", "on_intel do", "workbook_0.1.0_darwin_arm64.tar.gz", checksumA, "workbook_0.1.0_darwin_amd64.tar.gz", checksumB)
	assertArchitectureFormulaBlock(t, macos, "on_intel do", "\n  end", "workbook_0.1.0_darwin_amd64.tar.gz", checksumB, "workbook_0.1.0_darwin_arm64.tar.gz", checksumA)
	if strings.Contains(macos, "_linux_") {
		t.Errorf("macOS block serves a Linux archive:\n%s", macos)
	}

	linux := formulaSection(t, contents, "on_linux do", "\n  def install")
	assertArchitectureFormulaBlock(t, linux, "on_arm do", "on_intel do", "workbook_0.1.0_linux_arm64.tar.gz", checksumC, "workbook_0.1.0_linux_amd64.tar.gz", checksumD)
	assertArchitectureFormulaBlock(t, linux, "on_intel do", "\n  end", "workbook_0.1.0_linux_amd64.tar.gz", checksumD, "workbook_0.1.0_linux_arm64.tar.gz", checksumC)
	if strings.Contains(linux, "_darwin_") {
		t.Errorf("Linux block serves a darwin archive:\n%s", linux)
	}
}

func TestRenderHomebrewFormulaRejectsMissingOrDuplicateChecksums(t *testing.T) {
	// Production mutation: rendering from an incomplete checksums file would
	// publish a formula whose downloads were never built or verified.
	root, script := renderFormulaPaths(t)
	tests := map[string]string{
		"missing darwin": checksumLines(checksumA+"  workbook_0.1.0_darwin_arm64.tar.gz", checksumC+"  workbook_0.1.0_linux_arm64.tar.gz", checksumD+"  workbook_0.1.0_linux_amd64.tar.gz"),
		"missing linux":  checksumLines(checksumA+"  workbook_0.1.0_darwin_arm64.tar.gz", checksumB+"  workbook_0.1.0_darwin_amd64.tar.gz", checksumC+"  workbook_0.1.0_linux_arm64.tar.gz"),
		"duplicate": checksumLines(
			checksumA+"  workbook_0.1.0_darwin_arm64.tar.gz",
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  workbook_0.1.0_darwin_arm64.tar.gz",
			checksumB+"  workbook_0.1.0_darwin_amd64.tar.gz",
			checksumC+"  workbook_0.1.0_linux_arm64.tar.gz",
			checksumD+"  workbook_0.1.0_linux_amd64.tar.gz",
		),
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			checksums := filepath.Join(t.TempDir(), "checksums.txt")
			if err := os.WriteFile(checksums, []byte(contents), 0o600); err != nil {
				t.Fatalf("write checksums: %v", err)
			}
			output := filepath.Join(t.TempDir(), "workbook.rb")
			command := exec.Command(script, "0.1.0", checksums, output, "dgoings/workbook")
			command.Dir = root
			if result, err := command.CombinedOutput(); err == nil {
				t.Fatalf("render formula unexpectedly succeeded: %s", result)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("render formula wrote output for %s checksums: %v", name, err)
			}
		})
	}
}

func TestRenderHomebrewFormulaRejectsUnsafeVersionsBeforeWritingOutput(t *testing.T) {
	root, script := renderFormulaPaths(t)
	checksums := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(checksums, []byte(fixtureChecksums("0.1.0")), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	for _, version := range []string{"v0.1.0", "0.1", "01.2.3", "1.2.3-alpha", "1.2.3/../../formula"} {
		t.Run(version, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "workbook.rb")
			command := exec.Command(script, version, checksums, output, "dgoings/workbook")
			command.Dir = root
			result, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("formula renderer accepted unsafe version %q; output = %q", version, result)
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("formula renderer created output for unsafe version %q: %v", version, statErr)
			}
		})
	}
}

func TestValidateReleaseTagAcceptsOnlySafeCoreSemVer(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	script := filepath.Join(root, "scripts", "validate-release-tag.sh")

	command := exec.Command(script, "v0.1.0")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("validate safe release tag: %v\n%s", err, output)
	}
	if string(output) != "0.1.0\n" {
		t.Fatalf("validated version = %q, want 0.1.0", output)
	}

	for _, tag := range []string{
		"0.1.0",
		"v0.1",
		"v01.2.3",
		"v1.02.3",
		"v1.2.03",
		"v1.2.3-alpha",
		"v1.2.3+build",
		"v1.2.3/../../release",
	} {
		t.Run(tag, func(t *testing.T) {
			command := exec.Command(script, tag)
			command.Dir = root
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("tag validator accepted %q; output = %q", tag, output)
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
		"group: release-${{ github.ref }}",
		"cancel-in-progress: false",
		"environment: release",
		"runs-on: ubuntu-24.04",
		"Validate release tag",
		"go-version: \"1.26.5\"",
		"go test ./...",
		"scripts/release.sh \"${VERSION}\" dist",
		"scripts/publish-release.sh",
		"HOMEBREW_TAP_TOKEN",
		"dgoings/homebrew-tap",
		"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5",
		"actions/setup-go@4dc6199c7b1a012772edbd06daecab0f50c9053c",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("workflow missing %q:\n%s", want, contents)
		}
	}
	for _, forbidden := range []string{"--clobber", "actions/checkout@v", "actions/setup-go@v", "ubuntu-latest"} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("workflow contains unpinned or destructive release behavior %q:\n%s", forbidden, contents)
		}
	}
	if err := validateTagOnlyPushTrigger(workflow); err != nil {
		t.Errorf("workflow trigger: %v", err)
	}
	if err := validateReleaseConcurrency(workflow); err != nil {
		t.Errorf("workflow concurrency: %v", err)
	}
	mutatedWorkflow := bytes.Replace(workflow, []byte(`tags: ["v*"]`), []byte("tags: [\"v*\"]\n    branches: [main]"), 1)
	if bytes.Equal(mutatedWorkflow, workflow) {
		t.Fatal("could not add a branches trigger to workflow fixture")
	}
	if err := validateTagOnlyPushTrigger(mutatedWorkflow); err == nil {
		t.Error("tag-only trigger validation accepted push.branches alongside push.tags")
	}
	mutatedWorkflow = bytes.Replace(workflow, []byte("cancel-in-progress: false"), []byte("cancel-in-progress: true"), 1)
	if bytes.Equal(mutatedWorkflow, workflow) {
		t.Fatal("could not enable release cancellation in workflow fixture")
	}
	if err := validateReleaseConcurrency(mutatedWorkflow); err == nil {
		t.Error("release concurrency validation accepted cancellation of an in-progress tag release")
	}
	if strings.Count(contents, "secrets.HOMEBREW_TAP_TOKEN") != 1 || !strings.Contains(contents, "repository: dgoings/homebrew-tap\n          token: ${{ secrets.HOMEBREW_TAP_TOKEN }}") {
		t.Errorf("tap credential is not scoped to the tap update:\n%s", contents)
	}
	validateIndex := strings.Index(contents, "name: Validate release tag")
	secretIndex := strings.Index(contents, "secrets.HOMEBREW_TAP_TOKEN")
	publishIndex := strings.Index(contents, "scripts/publish-release.sh")
	if validateIndex < 0 || secretIndex < 0 || publishIndex < 0 || !(validateIndex < secretIndex && validateIndex < publishIndex) {
		t.Errorf("release tag must be validated before tap credential or publication use:\n%s", contents)
	}
}

func TestRepositoryDoesNotShipPlaceholderHomebrewFormula(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	_, err := os.Stat(filepath.Join(root, "Formula", "workbook.rb"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tracked Formula/workbook.rb exists or cannot be checked: %v", err)
	}
}

func validateTagOnlyPushTrigger(workflow []byte) error {
	var document struct {
		On map[string]yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(workflow, &document); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.On) != 1 {
		return fmt.Errorf("triggers = %#v, want only push.tags [v*]", document.On)
	}
	push, ok := document.On["push"]
	if !ok {
		return fmt.Errorf("triggers = %#v, want push.tags [v*]", document.On)
	}
	var pushFields map[string]yaml.Node
	if err := push.Decode(&pushFields); err != nil {
		return fmt.Errorf("decode push trigger: %w", err)
	}
	if len(pushFields) != 1 {
		return fmt.Errorf("push fields = %#v, want only tags [v*]", pushFields)
	}
	tags, ok := pushFields["tags"]
	if !ok {
		return fmt.Errorf("push fields = %#v, want tags [v*]", pushFields)
	}
	var patterns []string
	if err := tags.Decode(&patterns); err != nil {
		return fmt.Errorf("decode push.tags: %w", err)
	}
	if len(patterns) != 1 || patterns[0] != "v*" {
		return fmt.Errorf("push.tags = %#v, want [v*]", patterns)
	}
	return nil
}

func validateReleaseConcurrency(workflow []byte) error {
	var document struct {
		Concurrency map[string]yaml.Node `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(workflow, &document); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.Concurrency) != 2 {
		return fmt.Errorf("concurrency = %#v, want tag-scoped group with cancellation disabled", document.Concurrency)
	}
	groupNode, ok := document.Concurrency["group"]
	if !ok {
		return fmt.Errorf("concurrency = %#v, want group", document.Concurrency)
	}
	var group string
	if err := groupNode.Decode(&group); err != nil {
		return fmt.Errorf("decode concurrency group: %w", err)
	}
	if group != "release-${{ github.ref }}" {
		return fmt.Errorf("concurrency group = %q, want release-${{ github.ref }}", group)
	}
	cancelNode, ok := document.Concurrency["cancel-in-progress"]
	if !ok {
		return fmt.Errorf("concurrency = %#v, want cancel-in-progress", document.Concurrency)
	}
	var cancel bool
	if err := cancelNode.Decode(&cancel); err != nil {
		return fmt.Errorf("decode cancel-in-progress: %w", err)
	}
	if cancel {
		return fmt.Errorf("cancel-in-progress = true, want false")
	}
	return nil
}

// The four fixture checksums stand in for the archives scripts/release.sh
// builds, one per platform the formula serves.
const (
	checksumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	checksumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	checksumC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	checksumD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func fixtureChecksums(version string) string {
	return checksumLines(
		checksumA+"  workbook_"+version+"_darwin_arm64.tar.gz",
		checksumB+"  workbook_"+version+"_darwin_amd64.tar.gz",
		checksumC+"  workbook_"+version+"_linux_arm64.tar.gz",
		checksumD+"  workbook_"+version+"_linux_amd64.tar.gz",
	)
}

func checksumLines(lines ...string) string {
	return strings.Join(append(lines, ""), "\n")
}

// formulaSection returns the part of the formula between start and the first
// following end, so per-platform blocks can be asserted without matching the
// identically named architecture blocks of the other platform.
func formulaSection(t *testing.T, formula, start, end string) string {
	t.Helper()
	startIndex := strings.Index(formula, start)
	if startIndex < 0 {
		t.Fatalf("formula missing %q:\n%s", start, formula)
	}
	section := formula[startIndex:]
	endIndex := strings.Index(section, end)
	if endIndex < 0 {
		t.Fatalf("formula missing %q after %q:\n%s", end, start, formula)
	}
	return section[:endIndex]
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
