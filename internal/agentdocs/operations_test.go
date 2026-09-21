package agentdocs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	// A refusal names the user configuration file by path, so point the lookup
	// at a scratch directory: the assertions are then deterministic, and no
	// test can reach the developer's real configuration.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return Options{
		Root:      t.TempDir(),
		Project:   testProject(),
		User:      userconfig.Default(),
		Generator: "0.2.0",
	}
}

// userConfigPath is the file a refusal about the user-global layer must name.
func userConfigPath(t *testing.T) string {
	t.Helper()
	path, err := userconfig.Path()
	if err != nil {
		t.Fatalf("userconfig.Path() error = %v", err)
	}
	return path
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

func TestApplyPrefersAnOverriddenSkillDirectory(t *testing.T) {
	// Production mutation: reading the skill directory only from user-global
	// configuration makes one machine-wide value decide every project's layout.
	options := testOptions(t)
	options.SkillDir = filepath.Join("tools", "skills")

	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(options.Root, "tools", "skills", "workbook", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed to the overridden directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Root, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("Apply() also used the configured directory: %v", err)
	}
}

func TestApplySkipsTheSkillWhenRequested(t *testing.T) {
	// Production mutation: forcing the skill alongside the guidelines leaves no
	// way to manage documentation in a project that packages skills itself.
	options := testOptions(t)
	options.SkipSkill = true

	report, err := Apply(options)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(options.Root, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("Apply() installed a skipped skill: %v", err)
	}
	for _, artifact := range report.Artifacts {
		if strings.Contains(artifact.Path, "SKILL.md") {
			t.Fatalf("report includes a skipped skill: %#v", report.Artifacts)
		}
	}
	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); err != nil {
		t.Fatalf("Apply(skip skill) did not install guidelines: %v", err)
	}
}

func TestRemoveLeavesASkippedSkillInPlace(t *testing.T) {
	options := testOptions(t)
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	skill := filepath.Join(options.Root, ".claude", "skills", "workbook", "SKILL.md")

	options.SkipSkill = true
	if _, err := Remove(options); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("Remove(skip skill) deleted the skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); !os.IsNotExist(err) {
		t.Fatalf("Remove(skip skill) left the guidelines: %v", err)
	}
}

func TestStatusReportsTheOverriddenSkillDirectory(t *testing.T) {
	options := testOptions(t)
	options.SkillDir = filepath.Join("tools", "skills")
	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	report, err := Status(options)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if got := stateOf(t, report, "tools/skills/workbook/SKILL.md"); got != StateCurrent {
		t.Fatalf("overridden skill state = %q, want %q", got, StateCurrent)
	}
}

func TestApplyRefusesADocumentationTargetThatEscapesTheProject(t *testing.T) {
	// Production mutation: joining a configured target against the root
	// unchecked lets a copied user configuration refresh a file above the
	// repository.
	options := testOptions(t)
	options.User.DocTargets = []string{"AGENTS.md", "../../.bashrc"}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted a documentation target outside the project")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, "../../.bashrc") {
		t.Fatalf("Apply() error = %q, want it to name the offending value", got)
	}
	if !strings.Contains(got, userConfigPath(t)) {
		t.Fatalf("Apply() error = %q, want it to name the user configuration file %q", got, userConfigPath(t))
	}
}

func TestApplyRefusesAnAbsoluteDocumentationTarget(t *testing.T) {
	// Production mutation: an absolute target silently meant <root>/etc/motd,
	// so honoring it wrote somewhere other than what the configuration said.
	options := testOptions(t)
	absolute := filepath.Join(t.TempDir(), "motd")
	options.User.DocTargets = []string{absolute}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted an absolute documentation target")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, absolute) {
		t.Fatalf("Apply() error = %q, want it to name %q", got, absolute)
	}
	if !strings.Contains(got, "is an absolute path") {
		t.Fatalf("Apply() error = %q, want it to say the target is an absolute path", got)
	}
}

func TestApplyRefusesAnAbsoluteDocumentationTargetInsideTheProject(t *testing.T) {
	// Production mutation: filepath.IsLocal rejects every absolute path, so
	// reporting these as an escape tells a reader whose path plainly points
	// into their own project to hunt for a ".." they never typed.
	options := testOptions(t)
	absolute := filepath.Join(options.Root, "AGENTS.md")
	options.User.DocTargets = []string{absolute}
	writeFile(t, absolute, "# AGENTS.md\n")

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted an absolute documentation target")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, "is an absolute path") {
		t.Fatalf("Apply() error = %q, want it to say the target is an absolute path", got)
	}
	if strings.Contains(got, "escapes") {
		t.Fatalf("Apply() error = %q, want it not to claim a path inside the project escapes it", got)
	}
}

func TestApplyRefusesAConfiguredSkillDirectoryThatEscapesTheProject(t *testing.T) {
	// Production mutation: a relative skillDir is joined against the root, so
	// an unchecked one installs a skill tree outside the repository.
	options := testOptions(t)
	options.User.SkillDir = filepath.Join("..", "..", "evil")

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted a skill directory outside the project")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, "../../evil") {
		t.Fatalf("Apply() error = %q, want it to name the offending value", got)
	}
	if !strings.Contains(got, userConfigPath(t)) {
		t.Fatalf("Apply() error = %q, want it to name the user configuration file %q", got, userConfigPath(t))
	}
}

func TestApplyRefusesAnOverriddenSkillDirectoryThatEscapesTheProject(t *testing.T) {
	// Production mutation: naming the wrong layer sends the reader to edit a
	// file that does not hold the offending value.
	options := testOptions(t)
	options.SkillDir = filepath.Join("..", "..", "evil")

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted an overridden skill directory outside the project")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, "--skill-dir") || !strings.Contains(got, "../../evil") {
		t.Fatalf("Apply() error = %q, want it to name --skill-dir and the value", got)
	}
	// The flag layer has no file, so the message must send the reader to the
	// flag and nowhere else — naming the configuration file here would point
	// at a value that is not in it.
	if strings.Contains(got, "configuration") {
		t.Fatalf("Apply() error = %q, want it not to blame the user configuration", got)
	}
	if strings.Contains(got, userConfigPath(t)) {
		t.Fatalf("Apply() error = %q, want it not to name the user configuration file", got)
	}
}

func TestStatusRefusesADocumentationTargetThatEscapesTheProject(t *testing.T) {
	// Status is the one operation that writes nothing even when it succeeds,
	// so its refusal is the proof the check sits in plan() and is therefore
	// shared by Apply, Status and Remove alike rather than guarding a write.
	options := testOptions(t)
	options.User.DocTargets = []string{"../../.bashrc"}

	_, err := Status(options)

	if err == nil {
		t.Fatal("Status() accepted a documentation target outside the project")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Status() category = %q, want %q", got, core.CategoryValidation)
	}
}

func TestApplyWritesNothingWhenADocumentationTargetEscapesTheProject(t *testing.T) {
	// Production mutation: a refusal that lands after the first write is not a
	// refusal, it is a half-finished install plus an error message.
	options := testOptions(t)
	options.User.DocTargets = []string{"AGENTS.md", "../../.bashrc"}
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n\nMy own rules.\n")

	if _, err := Apply(options); err == nil {
		t.Fatal("Apply() accepted a documentation target outside the project")
	}

	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); !os.IsNotExist(err) {
		t.Fatalf("Apply() wrote the guidelines before refusing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Root, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("Apply() installed the skill before refusing: %v", err)
	}
	if got, want := readFile(t, filepath.Join(options.Root, "AGENTS.md")), "# AGENTS.md\n\nMy own rules.\n"; got != want {
		t.Fatalf("AGENTS.md after refusal = %q, want %q", got, want)
	}
}

func TestApplyRefusesADocumentationTargetThatNamesTheProjectDirectory(t *testing.T) {
	// Production mutation: filepath.IsLocal accepts these, and every one of
	// them resolves to the project root, so letting them through means
	// write() reports the operating system's "is a directory" instead of the
	// configuration mistake that caused it.
	for name, value := range map[string]string{
		"the current directory": ".",
		"a climb back to it":    "docs/..",
		"a trailing separator":  "./",
		"the empty string":      "",
	} {
		t.Run(name, func(t *testing.T) {
			options := testOptions(t)
			options.User.DocTargets = []string{value}

			_, err := Apply(options)

			if err == nil {
				t.Fatalf("Apply() accepted the documentation target %q", value)
			}
			if got := core.CategoryOf(err); got != core.CategoryValidation {
				t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
			}
			got := err.Error()
			if !strings.Contains(got, "names the project directory itself") {
				t.Fatalf("Apply() error = %q, want it to say the target names the project directory itself", got)
			}
			if !strings.Contains(got, fmt.Sprintf("%q", value)) {
				t.Fatalf("Apply() error = %q, want it to quote %q as configured", got, value)
			}
			if !strings.Contains(got, userConfigPath(t)) {
				t.Fatalf("Apply() error = %q, want it to name the user configuration file", got)
			}
			if strings.Contains(got, "escapes") {
				t.Fatalf("Apply() error = %q, want it not to claim the target escapes the project", got)
			}
		})
	}
}

func TestApplyHonorsASkillDirectoryNamingTheProjectRoot(t *testing.T) {
	// A skillDir of "." is not the same mistake as a doc target of ".": it
	// installs the skill at <root>/workbook/SKILL.md, which is inside the
	// project and works, so the doc-target refusal must not spread to it.
	options := testOptions(t)
	options.User.SkillDir = "."

	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(options.Root, "workbook", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed at the project root: %v", err)
	}
}

func TestApplyRefusesADocumentationTargetThatIsADirectory(t *testing.T) {
	// Production mutation: the Stat result was tested for existence and then
	// discarded, so an existing directory became a write target and surfaced
	// as the operating system's "is a directory" — and only after the
	// guidelines, the skill and the valid target ahead of it had been written.
	options := testOptions(t)
	options.User.DocTargets = []string{"AGENTS.md", "docs"}
	writeFile(t, filepath.Join(options.Root, "AGENTS.md"), "# AGENTS.md\n\nMy own rules.\n")
	if err := os.MkdirAll(filepath.Join(options.Root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs directory: %v", err)
	}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted a directory as a documentation target")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, `"docs"`) || !strings.Contains(got, "is a directory, not a documentation file") {
		t.Fatalf("Apply() error = %q, want it to say the entry names a directory", got)
	}
	if !strings.Contains(got, userConfigPath(t)) {
		t.Fatalf("Apply() error = %q, want it to name the user configuration file", got)
	}
	// The refusal has to land before the first write, or it is a half-finished
	// install carrying an error message rather than a refusal.
	if _, err := os.Stat(filepath.Join(options.Root, GuidelinesPath)); !os.IsNotExist(err) {
		t.Fatalf("Apply() wrote the guidelines before refusing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Root, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("Apply() installed the skill before refusing: %v", err)
	}
	if got, want := readFile(t, filepath.Join(options.Root, "AGENTS.md")), "# AGENTS.md\n\nMy own rules.\n"; got != want {
		t.Fatalf("AGENTS.md after refusal = %q, want %q", got, want)
	}
}

func TestStatusRefusesADocumentationTargetThatIsADirectory(t *testing.T) {
	// The reader whose configuration names a directory needs `docs status` to
	// keep working well enough to say so, since that is the command they reach
	// for to find out what is wrong.
	options := testOptions(t)
	options.User.DocTargets = []string{"docs"}
	if err := os.MkdirAll(filepath.Join(options.Root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs directory: %v", err)
	}

	_, err := Status(options)

	if err == nil {
		t.Fatal("Status() accepted a directory as a documentation target")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Status() category = %q, want %q", got, core.CategoryValidation)
	}
}

func TestApplyRefreshesADocumentationTargetInsideADirectory(t *testing.T) {
	// Guard on the directory refusal: it must judge the target itself, not the
	// directories on the way to it, or a perfectly good nested target breaks.
	options := testOptions(t)
	options.User.DocTargets = []string{"docs/AGENTS.md"}
	nested := filepath.Join(options.Root, "docs", "AGENTS.md")
	writeFile(t, nested, "# AGENTS.md\n\nMy own rules.\n")

	if _, err := Apply(options); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	contents := readFile(t, nested)
	if !strings.HasPrefix(contents, "# AGENTS.md\n\nMy own rules.\n") {
		t.Fatalf("nested target lost user content:\n%s", contents)
	}
	if !strings.Contains(contents, GuidelinesPath) {
		t.Fatalf("nested target missing the managed reference:\n%s", contents)
	}
}

func TestApplyRefusesACreatedDocumentationTargetThatNamesTheProjectDirectory(t *testing.T) {
	// --create skips the Stat entirely, so the lexical check is the only thing
	// standing between "." and a write against the project root. This is why
	// that check cannot be replaced by the IsDir refusal.
	options := testOptions(t)
	options.User.DocTargets = []string{"."}
	options.Create = []string{"."}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted a created documentation target naming the project directory")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	if got := err.Error(); !strings.Contains(got, "names the project directory itself") {
		t.Fatalf("Apply() error = %q, want the project-directory refusal", got)
	}
}

func TestApplyStillRefusesWhenTheUserConfigurationPathIsUnknown(t *testing.T) {
	// Being unable to name the file is no reason to turn a validation refusal
	// into an operational failure about the home directory: that would bury
	// the configuration mistake that is the actual problem.
	options := testOptions(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := userconfig.Path(); err == nil {
		t.Skip("this platform resolves a configuration path without XDG_CONFIG_HOME or HOME")
	}
	options.User.DocTargets = []string{"../../.bashrc"}

	_, err := Apply(options)

	if err == nil {
		t.Fatal("Apply() accepted a documentation target outside the project")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("Apply() category = %q, want %q", got, core.CategoryValidation)
	}
	got := err.Error()
	if !strings.Contains(got, "../../.bashrc") {
		t.Fatalf("Apply() error = %q, want it to name the offending value", got)
	}
	if !strings.Contains(got, "in the user configuration file") {
		t.Fatalf("Apply() error = %q, want it to fall back to naming the file generically", got)
	}
}
