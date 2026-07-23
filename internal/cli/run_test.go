package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

type resultDocument struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data"`
}

type errorDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Error   struct {
		Category core.Category `json:"category"`
		Message  string        `json:"message"`
	} `json:"error"`
}

func TestRunInvalidInvocationAndEarlyJSONErrors(t *testing.T) {
	repository := testrepo.New(t)

	t.Run("no command", func(t *testing.T) {
		code, stdout, stderr := run(t, repository)
		if code != 2 {
			t.Fatalf("Run() code = %d, want 2; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "Usage: workbook <command>") {
			t.Fatalf("Run() stderr = %q, want usage", stderr)
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "unknown")
		if code != 2 {
			t.Fatalf("Run() code = %d, want 2; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, `unknown command "unknown"`) {
			t.Fatalf("Run() stderr = %q, want unknown-command error", stderr)
		}
	})

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "positional title after flag", args: []string{"create", "--json", "Late title"}},
		{name: "unknown flag", args: []string{"create", "Title", "--unknown", "--json"}},
		{name: "extra positional argument", args: []string{"show", "WB-123", "--json", "extra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("Run(%q) stdout = %q, want empty", test.args, stdout)
			}
			assertJSONError(t, stderr, core.CategoryInvocation, "")
			if strings.Contains(stderr, "Usage:") {
				t.Fatalf("Run(%q) JSON stderr contains human usage: %q", test.args, stderr)
			}
		})
	}
}

func TestRunReportsGitProcessFailuresAsOperationalWithoutUsage(t *testing.T) {
	repository := initializedRepository(t)
	command := exec.Command("git", "-C", repository, "config", "--unset", "user.email")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config --unset user.email: %v\n%s", err, output)
	}
	emptyGlobalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(emptyGlobalConfig, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(empty global Git config) error = %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", emptyGlobalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	t.Run("JSON", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--json")
		if code != 1 {
			t.Fatalf("Run() code = %d, want 1; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryOperational, "")
		var document errorDocument
		if err := json.Unmarshal([]byte(stderr), &document); err != nil {
			t.Fatalf("decode JSON error: %v; output = %q", err, stderr)
		}
		if !strings.Contains(document.Error.Message, "exit status 1") {
			t.Fatalf("operational JSON message = %q, want process cause", document.Error.Message)
		}
		if strings.Contains(stderr, "Usage:") {
			t.Fatalf("Run() operational JSON stderr contains usage: %q", stderr)
		}
	})

	t.Run("human", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list")
		if code != 1 {
			t.Fatalf("Run() code = %d, want 1; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("Run() stdout = %q, want empty", stdout)
		}
		assertHumanError(t, stderr, "git config --get user.email failed")
		if !strings.Contains(stderr, "exit status 1") {
			t.Fatalf("operational human stderr = %q, want process cause", stderr)
		}
		if strings.Contains(stderr, "Usage:") {
			t.Fatalf("Run() operational stderr contains usage: %q", stderr)
		}
	})
}

func TestRunReportsConfigurationFilesystemFailureAsOperational(t *testing.T) {
	repository := testrepo.New(t)
	blockingPath := filepath.Join(repository, ".workbook")
	if err := os.WriteFile(blockingPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile(.workbook) error = %v", err)
	}

	code, stdout, stderr := run(t, repository, "init", "--json")
	if code != 1 {
		t.Fatalf("Run() code = %d, want 1; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("Run() stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryOperational, "")
	var document errorDocument
	if err := json.Unmarshal([]byte(stderr), &document); err != nil {
		t.Fatalf("decode JSON error: %v; output = %q", err, stderr)
	}
	if !strings.Contains(document.Error.Message, filepath.Join(".workbook", "config.json")) {
		t.Fatalf("operational JSON message = %q, want failing configuration path", document.Error.Message)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Fatalf("Run() operational stderr contains usage: %q", stderr)
	}
}

func TestRunJSONIntentAccountsForStringFlagValuesAndParserStops(t *testing.T) {
	t.Run("init string value consumes terminator before JSON flag", func(t *testing.T) {
		repository := testrepo.New(t)
		code, stdout, stderr := run(t, repository, "init", "--key", "--", "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "")
	})

	t.Run("init string value consumes JSON-looking token", func(t *testing.T) {
		repository := testrepo.New(t)
		code, stdout, stderr := run(t, repository, "init", "--key", "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertHumanError(t, stderr, "")
	})

	t.Run("create string value consumes terminator before JSON flag", func(t *testing.T) {
		repository := initializedRepository(t)
		code, stdout, stderr := run(t, repository, "create", "Title", "--status", "--", "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "")
	})

	t.Run("create string value consumes JSON-looking token", func(t *testing.T) {
		repository := initializedRepository(t)
		code, stdout, stderr := run(t, repository, "create", "Title", "--status", "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertHumanError(t, stderr, "")
	})

	t.Run("unconsumed positional stops JSON recognition", func(t *testing.T) {
		repository := initializedRepository(t)
		code, stdout, stderr := run(t, repository, "create", "Title", "extra", "--json")
		if code != 2 {
			t.Fatalf("code = %d, want 2; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertHumanError(t, stderr, "")
	})
}

func TestRunJSONIntentMatchesGoBooleanFlagSyntax(t *testing.T) {
	repository := initializedRepository(t)

	for _, spelling := range []string{
		"-json",
		"--json",
		"--json=1",
		"-json=t",
		"--json=T",
		"-json=TRUE",
		"--json=true",
		"-json=True",
	} {
		t.Run("true "+spelling, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, "create", "", spelling)
			if code != 5 {
				t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			assertJSONError(t, stderr, core.CategoryValidation, "title is required")
		})
	}

	for _, spelling := range []string{
		"--json=0",
		"-json=f",
		"--json=F",
		"-json=FALSE",
		"--json=false",
		"-json=False",
	} {
		t.Run("false "+spelling, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, "create", "", spelling)
			if code != 5 {
				t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			assertHumanError(t, stderr, "title is required")
		})
	}

	for _, test := range []struct {
		name string
		args []string
		json bool
	}{
		{
			name: "true before parse error",
			args: []string{"create", "Title", "-json=TRUE", "--unknown"},
			json: true,
		},
		{
			name: "true after parse error",
			args: []string{"create", "Title", "--unknown", "-json"},
			json: true,
		},
		{
			name: "invalid value is JSON intent",
			args: []string{"create", "Title", "--json=not-a-bool"},
			json: true,
		},
		{
			name: "last repeated value is false",
			args: []string{"create", "", "--json=1", "-json=FALSE"},
			json: false,
		},
		{
			name: "last repeated value is true",
			args: []string{"create", "", "--json=0", "-json=TRUE"},
			json: true,
		},
		{
			name: "invalid repeated value stops parsing",
			args: []string{"create", "Title", "--json=invalid", "--json=false"},
			json: true,
		},
		{
			name: "literal terminator stops recognition",
			args: []string{"create", "", "--", "--json=TRUE"},
			json: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 && code != 5 {
				t.Fatalf("code = %d, want invocation or validation failure; stderr = %q", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if test.json {
				category := core.CategoryInvocation
				if code == 5 {
					category = core.CategoryValidation
				}
				assertJSONError(t, stderr, category, "")
			} else {
				assertHumanError(t, stderr, "")
			}
		})
	}
}

func TestRunRequiresInitializationAndInitIsIdempotent(t *testing.T) {
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "list")
	if code != 3 {
		t.Fatalf("list before init code = %d, want 3; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("list before init stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Workbook is not initialized") {
		t.Fatalf("list before init stderr = %q", stderr)
	}

	code, stdout, stderr = run(t, repository, "init", "--key", "PROJ")
	if code != 0 {
		t.Fatalf("init code = %d, want 0; stderr = %q", code, stderr)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{
		"Repository:\t" + canonicalRepository,
		"Project ID:\t",
		"Key:\tPROJ",
		"Tasks:\t0",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Errorf("init stdout = %q, want %q", stdout, wanted)
		}
	}

	code, second, stderr := run(t, repository, "init", "--key", "PROJ")
	if code != 0 {
		t.Fatalf("second init code = %d, want 0; stderr = %q", code, stderr)
	}
	if second != stdout {
		t.Fatalf("second init stdout differs:\nfirst:  %q\nsecond: %q", stdout, second)
	}
}

func TestRunCRUDLifecycleAndOutputContracts(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "init", "--key", "PROJ")
	if code != 0 {
		t.Fatalf("init code = %d, want 0; stderr = %q", code, stderr)
	}

	title := "A full task title that must never be truncated"
	description := "A long description that must be preserved in full."
	code, stdout, stderr := run(t, repository,
		"create", title,
		"--description", description,
		"--status", "ready",
		"--priority", "high",
		"--label", "backend",
		"--label", "agent",
		"--json",
	)
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("create stderr = %q, want empty", stderr)
	}
	result := assertJSONResult(t, stdout, "create")
	var created core.Task
	if err := json.Unmarshal(result.Data, &created); err != nil {
		t.Fatalf("decode created task: %v; data = %s", err, result.Data)
	}
	if created.Title != title || created.Description != description {
		t.Fatalf("created task = %#v, want full title and description", created)
	}
	if got, want := strings.Join(created.Labels, ","), "agent,backend"; got != want {
		t.Fatalf("created labels = %q, want %q", got, want)
	}

	code, stdout, stderr = run(t, repository, "list")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "ID\tTITLE\tSTATUS\tPRIORITY\tLABELS\n") {
		t.Fatalf("list stdout = %q, want header", stdout)
	}
	if !strings.Contains(stdout, created.ID+"\t"+title+"\tready\thigh\tagent,backend\n") {
		t.Fatalf("list stdout = %q, want complete task row", stdout)
	}

	prefix := created.ID[:12]
	code, stdout, stderr = run(t, repository, "show", prefix)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, wanted := range []string{
		"ID:\t" + created.ID,
		"Title:\t" + title,
		"Description:\t" + description,
		"Status:\tready",
		"Priority:\thigh",
		"Labels:\tagent,backend",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Errorf("show stdout = %q, want %q", stdout, wanted)
		}
	}

	code, stdout, stderr = run(t, repository,
		"update", prefix,
		"--title", "Updated title",
		"--description", "Updated description",
		"--status", "in-progress",
		"--priority", "low",
		"--label", "cli",
		"--label", "poc",
		"--json",
	)
	if code != 0 {
		t.Fatalf("update code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "update")
	var updated core.Task
	if err := json.Unmarshal(result.Data, &updated); err != nil {
		t.Fatalf("decode updated task: %v", err)
	}
	if got, want := strings.Join(updated.Labels, ","), "cli,poc"; got != want {
		t.Fatalf("updated labels = %q, want complete replacement %q", got, want)
	}

	code, stdout, stderr = run(t, repository, "update", prefix, "--clear-labels", "--json")
	if code != 0 {
		t.Fatalf("clear labels code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "update")
	if err := json.Unmarshal(result.Data, &updated); err != nil {
		t.Fatalf("decode clear-labels task: %v", err)
	}
	if len(updated.Labels) != 0 {
		t.Fatalf("cleared labels = %q, want empty", updated.Labels)
	}

	code, stdout, stderr = run(t, repository, "update", prefix, "--label", "x", "--clear-labels", "--json")
	if code != 2 {
		t.Fatalf("label conflict code = %d, want 2; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("label conflict stdout = %q, want empty", stdout)
	}
	assertJSONError(t, stderr, core.CategoryInvocation, "cannot use --label with --clear-labels")

	code, stdout, stderr = run(t, repository, "delete", prefix, "--json")
	if code != 0 {
		t.Fatalf("delete code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "delete")
	var deleted core.Task
	if err := json.Unmarshal(result.Data, &deleted); err != nil {
		t.Fatalf("decode deleted task: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("deleted task Deleted = false, want true")
	}

	code, stdout, stderr = run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list after delete code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	var active []core.Task
	if err := json.Unmarshal(result.Data, &active); err != nil {
		t.Fatalf("decode active list: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active list = %#v, want empty", active)
	}

	code, stdout, stderr = run(t, repository, "list", "--all", "--json")
	if code != 0 {
		t.Fatalf("list --all code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	var all []core.Task
	if err := json.Unmarshal(result.Data, &all); err != nil {
		t.Fatalf("decode all list: %v", err)
	}
	if len(all) != 1 || !all[0].Deleted {
		t.Fatalf("all list = %#v, want one tombstoned task", all)
	}
}

func TestRunJSONFailureIsCompactAndUsesStableExitCodes(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		repository := initializedRepository(t)
		code, stdout, stderr := run(t, repository, "create", "", "--json")
		if code != 5 {
			t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation, "title is required")
	})

	t.Run("not found", func(t *testing.T) {
		repository := initializedRepository(t)
		code, stdout, stderr := run(t, repository, "show", "WB-NOPE", "--json")
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryNotFound, "")
	})

	t.Run("stale write", func(t *testing.T) {
		err := core.Errorf(core.CategoryStaleWrite, "task ref changed concurrently")
		code := core.ExitCode(err)
		if code != 6 {
			t.Fatalf("code = %d, want 6", code)
		}
		var stderr bytes.Buffer
		writeError(&stderr, err, true)
		assertJSONError(t, stderr.String(), core.CategoryStaleWrite, "")
	})

	t.Run("corrupt data", func(t *testing.T) {
		repository := testrepo.New(t)
		configPath := filepath.Join(repository, ".workbook", "config.json")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte("{not-json}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := run(t, repository, "list", "--json")
		if code != 7 {
			t.Fatalf("code = %d, want 7; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		assertJSONError(t, stderr, core.CategoryCorruptData, "")
	})
}

func TestREADMEImplementedCommands(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}

	implemented, proposed := readmeCommandSections(t, string(contents))
	var got []string
	for _, line := range strings.Split(implemented, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "workbook ") {
			got = append(got, line)
		}
	}
	want := []string{
		"workbook init",
		"workbook create",
		"workbook list",
		"workbook show",
		"workbook update",
		"workbook delete",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("implemented commands = %q, want exactly %q", got, want)
	}

	for _, command := range []string{"workbook claim", "workbook sync"} {
		if !strings.Contains(proposed, command) {
			t.Errorf("proposed commands missing %q", command)
		}
	}

	readme := string(contents)
	assertREADMECommandPolicy(t, readme)
	if !strings.Contains(readme, "### Proposed small-team workflow") {
		t.Error("README small-team workflow is not labeled proposed")
	}
	for _, stale := range []string{
		"Workbook synchronizes only its own refs",
		"automatically reconciles concurrent edits",
	} {
		if strings.Contains(readme, stale) {
			t.Errorf("README contains stale present-tense claim %q", stale)
		}
	}
}

func TestREADMEDocumentsSourceInstallationPrerequisites(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	readme := string(contents)

	for _, required := range []string{
		"## Source installation prerequisites",
		"Go 1.26",
		"Git",
		"./scripts/install.sh",
		"$HOME/.local/bin",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README source installation section is missing %q", required)
		}
	}
}

func TestREADMEDocumentsCommonProjectIdentityGuard(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	readme := strings.Join(strings.Fields(string(contents)), " ")

	for _, required := range []string{
		"`<git-common-dir>/workbook/project.json`",
		"one Workbook project per common Git repository",
		"linked worktrees",
		"tracked and common identities do not match",
		"atomically backfills",
		"portable tracked configuration",
		"private coordination metadata",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README project identity section is missing %q", required)
		}
	}
}

func TestREADMECommandPolicyRejectsUnimplementedCommandOutsideProposedSection(t *testing.T) {
	const claim = "## Current workflow\n\nRun `workbook serve` to start the board.\n"
	violations := readmeCommandPolicyViolations(claim)
	if len(violations) != 1 || !strings.Contains(violations[0], `"serve"`) {
		t.Fatalf("violations = %q, want one for workbook serve", violations)
	}

	const proposal = "## Proposed web workflow\n\nA future release may run `workbook serve`.\n"
	if violations := readmeCommandPolicyViolations(proposal); len(violations) != 0 {
		t.Fatalf("proposed command violations = %q, want none", violations)
	}
}

func assertREADMECommandPolicy(t *testing.T, readme string) {
	t.Helper()
	if violations := readmeCommandPolicyViolations(readme); len(violations) != 0 {
		t.Fatalf("README presents unimplemented commands outside proposed sections:\n%s", strings.Join(violations, "\n"))
	}
}

func readmeCommandPolicyViolations(readme string) []string {
	implemented := map[string]bool{
		"init": true, "create": true, "list": true,
		"show": true, "update": true, "delete": true,
	}
	commandPattern := regexp.MustCompile(`\bworkbook ([a-z][a-z0-9-]*)\b`)
	var h2, h3 string
	var violations []string
	for index, line := range strings.Split(readme, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			h2 = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			h3 = ""
		case strings.HasPrefix(line, "### "):
			h3 = strings.TrimSpace(strings.TrimPrefix(line, "### "))
		}

		headingPath := strings.TrimSpace(strings.Join([]string{h2, h3}, " / "))
		isProposed := strings.Contains(strings.ToLower(headingPath), "proposed")
		for _, match := range commandPattern.FindAllStringSubmatch(line, -1) {
			if !implemented[match[1]] && !isProposed {
				violations = append(violations,
					fmt.Sprintf("line %d under %q uses %q", index+1, headingPath, match[1]))
			}
		}
	}
	return violations
}

func readmeCommandSections(t *testing.T, readme string) (string, string) {
	t.Helper()
	const (
		implementedHeading = "## Implemented POC commands"
		proposedHeading    = "## Proposed post-POC commands"
	)
	implementedStart := strings.Index(readme, implementedHeading)
	if implementedStart < 0 {
		t.Fatalf("README missing %q section", implementedHeading)
	}
	proposedStart := strings.Index(readme, proposedHeading)
	if proposedStart < 0 {
		t.Fatalf("README missing %q section", proposedHeading)
	}
	if proposedStart <= implementedStart {
		t.Fatalf("%q must follow %q", proposedHeading, implementedHeading)
	}

	implemented := readme[implementedStart+len(implementedHeading) : proposedStart]
	proposed := readme[proposedStart+len(proposedHeading):]
	if nextHeading := strings.Index(proposed, "\n## "); nextHeading >= 0 {
		proposed = proposed[:nextHeading]
	}
	return implemented, proposed
}

func initializedRepository(t *testing.T) string {
	t.Helper()
	repository := testrepo.New(t)
	code, _, stderr := run(t, repository, "init")
	if code != 0 {
		t.Fatalf("init code = %d, want 0; stderr = %q", code, stderr)
	}
	return repository
}

func run(t *testing.T, cwd string, args ...string) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), args, cwd, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func assertJSONResult(t *testing.T, output, command string) resultDocument {
	t.Helper()
	if strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, "\n") {
		t.Fatalf("JSON result is not one compact line: %q", output)
	}
	var result resultDocument
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode JSON result: %v; output = %q", err, output)
	}
	if result.Format != "workbook.result" || result.Version != 1 || result.Command != command {
		t.Fatalf("result envelope = %#v, want format workbook.result, version 1, command %q", result, command)
	}
	return result
}

func assertJSONError(t *testing.T, output string, category core.Category, message string) {
	t.Helper()
	if strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, "\n") {
		t.Fatalf("JSON error is not one compact line: %q", output)
	}
	var document errorDocument
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("decode JSON error: %v; output = %q", err, output)
	}
	if document.Format != "workbook.error" || document.Version != 1 {
		t.Fatalf("error envelope = %#v, want format workbook.error, version 1", document)
	}
	if document.Error.Category != category {
		t.Fatalf("error category = %q, want %q; output = %q", document.Error.Category, category, output)
	}
	if message != "" && document.Error.Message != message {
		t.Fatalf("error message = %q, want %q", document.Error.Message, message)
	}
}

func assertHumanError(t *testing.T, output, message string) {
	t.Helper()
	if strings.HasPrefix(output, "{") {
		t.Fatalf("error unexpectedly uses JSON: %q", output)
	}
	if !strings.HasPrefix(output, "workbook: ") {
		t.Fatalf("human error = %q, want workbook prefix", output)
	}
	if message != "" && !strings.Contains(output, message) {
		t.Fatalf("human error = %q, want message %q", output, message)
	}
}
