package agentdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Root:      t.TempDir(),
		Project:   testProject(),
		User:      userconfig.Default(),
		Generator: "0.2.0",
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func stateOf(t *testing.T, report Report, path string) State {
	t.Helper()
	for _, artifact := range report.Artifacts {
		if artifact.Path == path {
			return artifact.State
		}
	}
	t.Fatalf("report has no artifact %q; got %#v", path, report.Artifacts)
	return ""
}

func TestApplyInstallsGuidelinesAndSkill(t *testing.T) {
	options := testOptions(t)

	report, err := Apply(options)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if got := stateOf(t, report, GuidelinesPath); got != StateAbsent {
		t.Fatalf("guidelines state = %q, want %q", got, StateAbsent)
	}
	guidelines := readFile(t, filepath.Join(options.Root, GuidelinesPath))
	if !strings.Contains(guidelines, "in-progress") {
		t.Fatalf("guidelines missing canonical status:\n%s", guidelines)
	}
	skill := readFile(t, filepath.Join(options.Root, ".claude", "skills", "workbook", "SKILL.md"))
	if !strings.HasPrefix(skill, "---\n") {
		t.Fatalf("installed skill does not begin with frontmatter:\n%s", skill)
	}
}

func TestApplyOnlyRefreshesDocumentationFilesThatExist(t *testing.T) {
	// Production mutation: creating AGENTS.md and CLAUDE.md unprompted would
	// add files to repositories that never asked for them.
	options := testOptions(t)
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n\nMy own rules.\n")

	report, err := Apply(options)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	agents := readFile(t, filepath.Join(options.Root, "AGENTS.md"))
	if !strings.HasPrefix(agents, "# AGENTS.md\n\nMy own rules.\n") {
		t.Fatalf("AGENTS.md lost user content:\n%s", agents)
	}
	if !strings.Contains(agents, GuidelinesPath) {
		t.Fatalf("AGENTS.md missing the managed reference:\n%s", agents)
	}
	if _, err := os.Stat(filepath.Join(options.Root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("Apply() created CLAUDE.md: %v", err)
	}
	for _, artifact := range report.Artifacts {
		if artifact.Path == "CLAUDE.md" {
			t.Fatalf("report includes absent target CLAUDE.md: %#v", report.Artifacts)
		}
	}
}

func TestApplyCreatesRequestedDocumentationFiles(t *testing.T) {
	options := testOptions(t)
	options.Create = []string{"CLAUDE.md"}

	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	claude := readFile(t, filepath.Join(options.Root, "CLAUDE.md"))
	if !strings.Contains(claude, GuidelinesPath) {
		t.Fatalf("CLAUDE.md missing the managed reference:\n%s", claude)
	}
}

func TestApplyRejectsACreateTargetOutsideTheConfiguredTargets(t *testing.T) {
	options := testOptions(t)
	options.Create = []string{"NOTES.md"}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted an unconfigured create target")
	}
	if got := core.CategoryOf(err); got != core.CategoryInvocation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryInvocation)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	options := testOptions(t)
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n")
	if _, err := Apply(options); err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	before := readFile(t, filepath.Join(options.Root, "AGENTS.md"))

	report, err := Apply(options)
	if err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}

	for _, artifact := range report.Artifacts {
		if artifact.State != StateCurrent {
			t.Errorf("artifact %q state = %q, want %q", artifact.Path, artifact.State, StateCurrent)
		}
		if artifact.Written {
			t.Errorf("artifact %q was rewritten while current", artifact.Path)
		}
	}
	if after := readFile(t, filepath.Join(options.Root, "AGENTS.md")); after != before {
		t.Fatalf("second Apply() changed AGENTS.md:\n%s", after)
	}
}

func TestApplyRefusesToOverwriteAModifiedArtifact(t *testing.T) {
	// Production mutation: overwriting a hand-edited managed block would
	// silently destroy the user's work.
	options := testOptions(t)
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	path := filepath.Join(options.Root, GuidelinesPath)
	edited := strings.Replace(readFile(t, path), "# Workbook guidelines", "# My guidelines", 1)
	writeFile(t, path, edited)

	report, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() overwrote a modified artifact without --force")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	if got := stateOf(t, report, GuidelinesPath); got != StateModified {
		t.Fatalf("guidelines state = %q, want %q", got, StateModified)
	}
	if got := readFile(t, path); got != edited {
		t.Fatalf("Apply() rewrote the modified artifact:\n%s", got)
	}
}

func TestApplyWithForceOverwritesAModifiedArtifact(t *testing.T) {
	options := testOptions(t)
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	path := filepath.Join(options.Root, GuidelinesPath)
	writeFile(t, path, strings.Replace(readFile(t, path), "# Workbook guidelines", "# My guidelines", 1))

	options.Force = true
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply(force) error = %v", err)
	}

	if got := readFile(t, path); !strings.Contains(got, "# Workbook guidelines") {
		t.Fatalf("Apply(force) did not restore generated content:\n%s", got)
	}
}

func TestApplyStillInstallsOtherArtifactsWhenOneIsModified(t *testing.T) {
	options := testOptions(t)
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n")
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	guidelines := filepath.Join(options.Root, GuidelinesPath)
	writeFile(t, guidelines, strings.Replace(readFile(t, guidelines), "# Workbook guidelines", "# Mine", 1))
	agents := filepath.Join(options.Root, "AGENTS.md")
	if err := os.Remove(agents); err != nil {
		t.Fatalf("remove AGENTS.md: %v", err)
	}
	writeFile(t, agents, "# AGENTS.md\n")

	if _, err := Apply(options); err == nil {
		t.Fatal("Apply() succeeded with a modified artifact")
	}

	if got := readFile(t, agents); !strings.Contains(got, GuidelinesPath) {
		t.Fatalf("Apply() skipped an installable artifact because another was modified:\n%s", got)
	}
}

func TestStatusReportsWithoutWriting(t *testing.T) {
	// Production mutation: a status check that writes would make an
	// inspection command mutate the repository.
	options := testOptions(t)

	report, err := Status(options)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if got := stateOf(t, report, GuidelinesPath); got != StateAbsent {
		t.Fatalf("guidelines state = %q, want %q", got, StateAbsent)
	}
	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); !os.IsNotExist(err) {
		t.Fatalf("Status() created the guidelines file: %v", err)
	}
}

func TestRemoveStripsManagedContentAndPreservesUserContent(t *testing.T) {
	options := testOptions(t)
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n\nMy own rules.\n")
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := Remove(options); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if got, want := readFile(t, filepath.Join(options.Root, "AGENTS.md")), "# AGENTS.md\n\nMy own rules.\n"; got != want {
		t.Fatalf("AGENTS.md after Remove() = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); !os.IsNotExist(err) {
		t.Fatalf("Remove() left the guidelines file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Root, ".claude", "skills", "workbook", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("Remove() left the skill: %v", err)
	}
}

func TestRemoveRefusesToDiscardAModifiedArtifact(t *testing.T) {
	options := testOptions(t)
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	path := filepath.Join(options.Root, GuidelinesPath)
	writeFile(t, path, strings.Replace(readFile(t, path), "# Workbook guidelines", "# Mine", 1))

	if _, err := Remove(options); err == nil {
		t.Fatal("Remove() discarded a modified artifact without --force")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Remove() deleted a modified artifact: %v", err)
	}
}

func TestApplyHonoursAnAbsoluteSkillDirectory(t *testing.T) {
	options := testOptions(t)
	personal := t.TempDir()
	options.User.SkillDir = personal

	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(personal, "workbook", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed to the absolute directory: %v", err)
	}
}
