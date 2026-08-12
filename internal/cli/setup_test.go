package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func readProjectFile(t *testing.T, repository, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func writeProjectFile(t *testing.T, repository, name, contents string) {
	t.Helper()
	path := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestSetupInitializesIdentityAndInstallsDocumentation(t *testing.T) {
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "setup")

	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, want := range []string{"Repository:", "Project ID:", "Key:", "Tasks:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("setup output missing %q:\n%s", want, stdout)
		}
	}
	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, "in-progress") {
		t.Errorf("guidelines missing canonical status:\n%s", guidelines)
	}
	if _, err := os.Stat(filepath.Join(repository, ".claude", "skills", "workbook", "SKILL.md")); err != nil {
		t.Errorf("setup did not install the skill: %v", err)
	}
}

// A project this build mints gets the statuses this build ships, recorded in a
// genesis rather than left to a fallback that a later release could change under
// it. `blocked` is not among them: dependencies say what a task is waiting on.
func TestSetupMintsTheDefaultVocabularyIntoAGenesis(t *testing.T) {
	repository := testrepo.New(t)

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	document := cliStatusList(t, repository)
	if !document.Seeded || document.Head == "" {
		t.Fatalf("status list = seeded %t, head %q; want a recorded genesis", document.Seeded, document.Head)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("minted statuses = %v, want %v", got, want)
	}

	// The generated guidelines and the board agree with the ledger, because both
	// read it rather than carrying a list of their own.
	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if strings.Contains(guidelines, "`blocked`") {
		t.Errorf("minted guidelines still document `blocked`:\n%s", guidelines)
	}
	if !strings.Contains(guidelines, "| 5 | `done` | Done | `done` |") {
		t.Errorf("minted guidelines do not put done fifth:\n%s", guidelines)
	}
	code, board, stderr := run(t, repository, "board", "--narrow")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(board, "BLOCKED") {
		t.Errorf("minted board still draws a Blocked column:\n%s", board)
	}

	// A task created without --status still lands where it always did.
	if got := cliCreateTask(t, repository, "Alpha").Status; got != core.StatusBacklog {
		t.Fatalf("created task status = %q, want %q", got, core.StatusBacklog)
	}
}

// A project that already existed keeps the statuses it was using, and rerunning
// setup on it does not mint a vocabulary over the top.
//
// This is the upgrade every installed project performs, reproduced by removing
// the ledger: six columns before, six columns after, and no genesis written by a
// command whose job was to install documentation.
func TestSetupLeavesAnExistingProjectsStatusesAlone(t *testing.T) {
	repository := preLedgerRepository(t)
	task := cliCreateTask(t, repository, "Alpha")

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("second setup code = %d, want 0; stderr = %q", code, stderr)
	}

	document := cliStatusList(t, repository)
	if document.Seeded || document.Head != "" {
		t.Fatalf("status list = seeded %t, head %q; want setup to have recorded nothing",
			document.Seeded, document.Head)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "blocked", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses after the upgrade = %v, want the six it was using %v", got, want)
	}
	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if !strings.Contains(guidelines, "| 3 | `blocked` | Blocked | none |") {
		t.Errorf("the upgrade rewrote the guidelines without `blocked`:\n%s", guidelines)
	}
	code, board, stderr := run(t, repository, "board", "--narrow")
	if code != 0 || stderr != "" {
		t.Fatalf("board = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(board, "BLOCKED (0)") {
		t.Errorf("the upgraded board stopped drawing the Blocked column:\n%s", board)
	}
	// The status the project defines is still one a caller may supply.
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "blocked", "--no-sync"); code != 0 {
		t.Fatalf("update --status blocked = code %d; stderr = %q", code, stderr)
	}
}

// A project whose configuration ledger was never written or was lost is
// repaired by the next setup, provided nothing has been tracked under it.
//
// The gate that produced this branch first was "did this run mint the identity",
// which cannot see either failure: a genesis write that failed after Init minted
// leaves the project reverted to the pre-ledger six with nothing able to put it
// back, and so does a ref lost afterwards. Emptiness is the honest test — a
// task-less, unconfigured project has no board to re-columnize and no recorded
// decision to overrule, whoever created it and whenever.
func TestSetupSeedsAProjectThatHasNoLedgerAndNoTasks(t *testing.T) {
	repository := preLedgerRepository(t)
	if document := cliStatusList(t, repository); document.Seeded {
		t.Fatal("the fixture still has a configuration ledger")
	}

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	document := cliStatusList(t, repository)
	if !document.Seeded || document.Head == "" {
		t.Fatalf("status list = seeded %t, head %q; want the ledger repaired", document.Seeded, document.Head)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("repaired statuses = %v, want %v", got, want)
	}
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none once the ledger records five statuses", document.Migrations)
	}
}

// The repair stops at the first task. A project with work in it is an existing
// project whatever its ledger says, and seeding one under it would drop a column
// its board is drawing — so it stays on the conservative fallback and says so.
func TestSetupLeavesAProjectWithTasksOnTheFallback(t *testing.T) {
	repository := preLedgerRepository(t)
	cliCreateTask(t, repository, "Alpha")

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	if document := cliStatusList(t, repository); document.Seeded {
		t.Fatal("setup seeded a vocabulary over a project that already holds tasks")
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "blocked", "in-progress", "in-review", "done",
	}; !equalStrings(got, want) {
		t.Fatalf("statuses = %v, want the six it was using %v", got, want)
	}
	// The line a project in this state reads has to be true of it. It did not
	// necessarily start here — it may have lost a ledger that recorded
	// something — so the report says what is recorded now and nothing more.
	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "this project started with") {
		t.Errorf("status list claims the project started with these statuses:\n%s", stdout)
	}
	if !strings.Contains(stdout, "No status change is recorded") {
		t.Errorf("status list does not say that nothing is recorded:\n%s", stdout)
	}
}

func TestSetupReportsSkippedSyncWithoutAnOrigin(t *testing.T) {
	// Production mutation: failing when no remote is configured would break the
	// solo local workflow, which needs no remote at all.
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "setup")

	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Sync:") || !strings.Contains(stdout, "skipped") {
		t.Errorf("setup did not report a skipped sync:\n%s", stdout)
	}
}

func TestSetupPreservesUserContentInAgentDocumentation(t *testing.T) {
	repository := testrepo.New(t)
	writeProjectFile(t, repository, "AGENTS.md", "# AGENTS.md\n\nMy own rules.\n")

	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	agents := readProjectFile(t, repository, "AGENTS.md")
	if !strings.HasPrefix(agents, "# AGENTS.md\n\nMy own rules.\n") {
		t.Fatalf("setup discarded user content:\n%s", agents)
	}
	if !strings.Contains(agents, agentdocs.GuidelinesPath) {
		t.Fatalf("setup did not add the managed reference:\n%s", agents)
	}
	if _, err := os.Stat(filepath.Join(repository, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("setup created CLAUDE.md: %v", err)
	}
}

func TestSetupIsIdempotent(t *testing.T) {
	repository := testrepo.New(t)
	writeProjectFile(t, repository, "AGENTS.md", "# AGENTS.md\n")
	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("first setup code = %d, want 0; stderr = %q", code, stderr)
	}
	before := readProjectFile(t, repository, "AGENTS.md")

	code, _, stderr := run(t, repository, "setup")

	if code != 0 {
		t.Fatalf("second setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if after := readProjectFile(t, repository, "AGENTS.md"); after != before {
		t.Fatalf("second setup changed AGENTS.md:\n%s", after)
	}
}

func TestSetupWithNoDocsSkipsDocumentation(t *testing.T) {
	repository := testrepo.New(t)

	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(agentdocs.GuidelinesPath))); !os.IsNotExist(err) {
		t.Fatalf("setup --no-docs installed guidelines: %v", err)
	}
	if _, _, stderr := run(t, repository, "list"); stderr != "" {
		t.Fatalf("setup --no-docs did not initialize the project; list stderr = %q", stderr)
	}
}

func TestSetupEmitsAJSONEnvelope(t *testing.T) {
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "setup", "--json")

	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "setup")
	var payload struct {
		Repository string `json:"repository"`
		ProjectID  string `json:"projectId"`
		Key        string `json:"key"`
		Docs       struct {
			Artifacts []agentdocs.Artifact `json:"artifacts"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode setup result: %v", err)
	}
	if payload.ProjectID == "" || payload.Key != "WB" {
		t.Fatalf("setup result = %#v, want a project ID and key WB", payload)
	}
	if len(payload.Docs.Artifacts) == 0 {
		t.Fatal("setup result reported no documentation artifacts")
	}
}

func TestSetupRefusesToOverwriteModifiedDocumentation(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	edited := strings.Replace(
		readProjectFile(t, repository, agentdocs.GuidelinesPath),
		"# Workbook guidelines", "# My guidelines", 1)
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, edited)

	code, _, stderr := run(t, repository, "setup")

	if code != core.ExitCode(core.Errorf(core.CategoryValidation, "x")) {
		t.Fatalf("setup code = %d, want %d; stderr = %q", code, core.ExitCode(core.Errorf(core.CategoryValidation, "x")), stderr)
	}
	assertHumanError(t, stderr, "--force")
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != edited {
		t.Fatalf("setup overwrote modified guidelines:\n%s", got)
	}
}

func TestInitIsReplacedBySetup(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "init")

	if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "x")) {
		t.Fatalf("init code = %d, want %d; stderr = %q", code, core.ExitCode(core.Errorf(core.CategoryInvocation, "x")), stderr)
	}
	assertHumanError(t, stderr, "unknown command")
}

func TestDocsStatusReportsEachManagedArtifact(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "docs", "status", "--json")

	if code != 0 {
		t.Fatalf("docs status code = %d, want 0; stderr = %q", code, stderr)
	}
	var report agentdocs.Report
	if err := json.Unmarshal(assertJSONResult(t, stdout, "docs status").Data, &report); err != nil {
		t.Fatalf("decode docs status: %v", err)
	}
	if len(report.Artifacts) == 0 {
		t.Fatal("docs status reported no artifacts")
	}
	for _, artifact := range report.Artifacts {
		if artifact.State != agentdocs.StateCurrent {
			t.Errorf("artifact %q state = %q, want %q after setup", artifact.Path, artifact.State, agentdocs.StateCurrent)
		}
	}
}

func TestDocsStatusReportsAbsentBeforeInstall(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "docs", "status")

	if code != 0 {
		t.Fatalf("docs status code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, string(agentdocs.StateAbsent)) {
		t.Fatalf("docs status did not report an absent artifact:\n%s", stdout)
	}
}

func TestDocsUpdateRefusesModifiedContentWithoutForce(t *testing.T) {
	repository := initializedRepository(t)
	edited := strings.Replace(
		readProjectFile(t, repository, agentdocs.GuidelinesPath),
		"# Workbook guidelines", "# My guidelines", 1)
	writeProjectFile(t, repository, agentdocs.GuidelinesPath, edited)

	code, _, stderr := run(t, repository, "docs", "update")

	if code != core.ExitCode(core.Errorf(core.CategoryValidation, "x")) {
		t.Fatalf("docs update code = %d, want validation exit code; stderr = %q", code, stderr)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); got != edited {
		t.Fatalf("docs update overwrote modified content:\n%s", got)
	}

	if code, _, stderr := run(t, repository, "docs", "update", "--force"); code != 0 {
		t.Fatalf("docs update --force code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := readProjectFile(t, repository, agentdocs.GuidelinesPath); !strings.Contains(got, "# Workbook guidelines") {
		t.Fatalf("docs update --force did not restore generated content:\n%s", got)
	}
}

func TestDocsInstallCreatesARequestedTarget(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "docs", "install", "--create", "CLAUDE.md")

	if code != 0 {
		t.Fatalf("docs install code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := readProjectFile(t, repository, "CLAUDE.md"); !strings.Contains(got, agentdocs.GuidelinesPath) {
		t.Fatalf("CLAUDE.md missing the managed reference:\n%s", got)
	}
}

func TestDocsRemoveStripsManagedContent(t *testing.T) {
	repository := testrepo.New(t)
	writeProjectFile(t, repository, "AGENTS.md", "# AGENTS.md\n\nMy own rules.\n")
	if code, _, stderr := run(t, repository, "setup"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, _, stderr := run(t, repository, "docs", "remove")

	if code != 0 {
		t.Fatalf("docs remove code = %d, want 0; stderr = %q", code, stderr)
	}
	if got, want := readProjectFile(t, repository, "AGENTS.md"), "# AGENTS.md\n\nMy own rules.\n"; got != want {
		t.Fatalf("AGENTS.md after docs remove = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(agentdocs.GuidelinesPath))); !os.IsNotExist(err) {
		t.Fatalf("docs remove left the guidelines file: %v", err)
	}
}

func TestDocsRejectsAnUnknownSubcommand(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "docs", "publish")

	if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "x")) {
		t.Fatalf("docs publish code = %d, want invocation exit code; stderr = %q", code, stderr)
	}
	assertHumanError(t, stderr, "unknown docs command")
}

func TestDocsSubcommandsExposeHelp(t *testing.T) {
	// Production mutation: leaving the subcommand help list hardcoded to hooks
	// would hide every docs subcommand from help output.
	repository := initializedRepository(t)

	for _, subcommand := range []string{"install", "update", "status", "remove"} {
		t.Run(subcommand, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, "docs", subcommand, "--help")
			if code != 0 {
				t.Fatalf("docs %s --help code = %d, want 0; stderr = %q", subcommand, code, stderr)
			}
			if !strings.Contains(stdout, "workbook docs "+subcommand) {
				t.Fatalf("docs %s help missing synopsis:\n%s", subcommand, stdout)
			}

			code, stdout, stderr = run(t, repository, "help", "docs", subcommand)
			if code != 0 {
				t.Fatalf("help docs %s code = %d, want 0; stderr = %q", subcommand, code, stderr)
			}
			if !strings.Contains(stdout, "workbook docs "+subcommand) {
				t.Fatalf("help docs %s missing synopsis:\n%s", subcommand, stdout)
			}
		})
	}
}

func TestDocsHelpListsEverySubcommand(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "docs", "--help")

	if code != 0 {
		t.Fatalf("docs --help code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, subcommand := range []string{"install", "update", "status", "remove"} {
		if !strings.Contains(stdout, subcommand) {
			t.Errorf("docs help missing subcommand %q:\n%s", subcommand, stdout)
		}
	}
}

func TestSetupInstallsTheSkillIntoAnOverriddenDirectory(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "setup", "--skill-dir", "tools/skills")

	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(repository, "tools", "skills", "workbook", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed to the overridden directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("setup also used the configured directory: %v", err)
	}
}

func TestSetupWithNoSkillStillInstallsGuidelines(t *testing.T) {
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "setup", "--no-skill")

	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "SKILL.md") {
		t.Fatalf("setup --no-skill reported a skill:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(repository, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("setup --no-skill installed the skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(agentdocs.GuidelinesPath))); err != nil {
		t.Fatalf("setup --no-skill skipped the guidelines: %v", err)
	}
}

func TestSkillDirectoryAndNoSkillAreMutuallyExclusive(t *testing.T) {
	// Production mutation: silently ignoring one of two contradictory flags
	// leaves the user guessing which one took effect.
	repository := testrepo.New(t)

	for _, args := range [][]string{
		{"setup", "--no-skill", "--skill-dir", "tools/skills"},
		{"docs", "install", "--no-skill", "--skill-dir", "tools/skills"},
	} {
		t.Run(strings.Join(args[:len(args)-2], " "), func(t *testing.T) {
			code, _, stderr := run(t, repository, args...)
			if code != core.ExitCode(core.Errorf(core.CategoryInvocation, "x")) {
				t.Fatalf("code = %d, want invocation exit code; stderr = %q", code, stderr)
			}
			assertHumanError(t, stderr, "cannot use --skill-dir with --no-skill")
		})
	}
}

func TestDocsSubcommandsHonourTheSkillDirectoryOverride(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--skill-dir", "tools/skills"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "docs", "status", "--skill-dir", "tools/skills")

	if code != 0 {
		t.Fatalf("docs status code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "tools/skills/workbook/SKILL.md") {
		t.Fatalf("docs status did not report the overridden skill:\n%s", stdout)
	}
	if strings.Contains(stdout, string(agentdocs.StateAbsent)) {
		t.Fatalf("docs status reported an absent artifact:\n%s", stdout)
	}

	if code, _, stderr := run(t, repository, "docs", "remove", "--no-skill"); code != 0 {
		t.Fatalf("docs remove code = %d, want 0; stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(repository, "tools", "skills", "workbook", "SKILL.md")); err != nil {
		t.Fatalf("docs remove --no-skill deleted the skill: %v", err)
	}
}

// Setup bootstraps a clone by synchronizing, so it reaches the same replay the
// other fetching commands do and must hand back the same contract.
func TestSetupReportsConflictsAndFinishesBootstrapping(t *testing.T) {
	first, second := cliSyncRepositories(t)
	code, stdout, stderr := run(t, first, "create", "Described", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if code, _, stderr := run(t, first, "update", task.ID, "--description", "Base text", "--json"); code != 0 {
		t.Fatalf("base description = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch", "--json"); code != 0 {
		t.Fatalf("fetch = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "update", task.ID, "--description", "Their text", "--json"); code != 0 {
		t.Fatalf("remote description = code %d, stderr %q", code, stderr)
	}
	if code, _, _ := run(t, first, "update", task.ID, "--description", "Our text", "--no-sync", "--json"); code != 0 {
		t.Fatalf("seeding local divergence = code %d", code)
	}

	code, stdout, stderr = run(t, first, "setup", "--json")
	if code != 8 {
		t.Fatalf("setup = code %d, want 8; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	document := assertJSONResult(t, stdout, "setup")
	if len(document.Conflict) != 1 || document.Conflict[0].TaskID != task.ID {
		t.Fatalf("conflict list = %#v, want one entry for %s", document.Conflict, task.ID)
	}
	var payload struct {
		TaskCount int `json:"taskCount"`
		Sync      struct {
			Status string `json:"status"`
		} `json:"sync"`
	}
	if err := json.Unmarshal(document.Data, &payload); err != nil {
		t.Fatalf("decode setup result: %v", err)
	}
	if payload.Sync.Status != "conflicted" {
		t.Fatalf("setup sync status = %q, want conflicted", payload.Sync.Status)
	}
	if payload.TaskCount == 0 {
		t.Fatal("setup did not finish bootstrapping after reporting a conflict")
	}
	assertJSONError(t, stderr, core.CategoryConflict, "")
}

// setup is the one bootstrap command a fresh clone runs, so it is where a
// person first meets an origin holding a ref this build cannot read. It
// synchronized, reported "completed", and named nothing, while `setup --json`
// carried the report all along.
func TestSetupNamesTheRefsItsSynchronizationIgnored(t *testing.T) {
	const foreignRef = "refs/workbook/tasks/OPS-01K0M6B8A4FTT8C39MXXYTW7D9"
	first, second := cliSyncRepositories(t)
	cliCreateTask(t, first, "Shared task")
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("initial push code = %d; stderr = %q", code, stderr)
	}
	cliGit(t, first, "push", "origin", "HEAD:refs/workbook/tasks/EVIL")
	cliGit(t, first, "push", "origin", "HEAD:"+foreignRef)

	code, stdout, stderr := run(t, second, "setup")
	if code != 0 || stderr != "" {
		t.Fatalf("poisoned setup code = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for _, want := range []string{
		"Sync:\tcompleted",
		"Ignored:\trefs/workbook/tasks/EVIL\t" + ignoredRefRemovable + "\t",
		"Ignored:\t" + foreignRef + "\t" + ignoredRefPlausible + "\t",
		removalAdvice,
		keepWarning,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("poisoned setup stdout = %q, want it to contain %q", stdout, want)
		}
	}
	// One line per skipped ref and one report: setup renders what its
	// synchronization returned once, rather than a block per phase or a block
	// per place the run's result is read.
	if got := strings.Count(stdout, "Ignored:\t"); got != 2 {
		t.Fatalf("setup wrote %d ignored-ref lines, want one for each of the two refs origin holds:\n%s",
			got, stdout)
	}
}
