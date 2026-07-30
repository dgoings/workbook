package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/testrepo"
)

type resultDocument struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Command  string          `json:"command"`
	Data     json.RawMessage `json:"data"`
	Warnings []core.Warning  `json:"warnings,omitempty"`
}

type errorDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Error   struct {
		Category core.Category `json:"category"`
		Message  string        `json:"message"`
	} `json:"error"`
}

func TestWriteMutationResultRendersWarning(t *testing.T) {
	result := core.MutationResult{
		Task: core.Task{
			ID: "WB-01K0M6B8A4FTT8C39MXXYTW7D1",
			TaskData: core.TaskData{
				Title:    "Durable",
				Status:   core.StatusReady,
				Priority: core.PriorityHigh,
			},
		},
		Warnings: []core.Warning{{
			Code:    core.WarningProjectionUpdate,
			Message: "cache update failed",
		}},
	}

	t.Run("human", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		writeMutationResult(&stdout, &stderr, "create", result, false)

		if got, want := stdout.String(), "WB-01K0M6B8A4FTT8C39MXXYTW7D1\tready\thigh\tDurable\n"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		if got, want := stderr.String(), "workbook: warning: cache update failed\n"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		writeMutationResult(&stdout, &stderr, "create", result, true)

		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
		document := assertJSONResult(t, stdout.String(), "create")
		var task core.Task
		if err := json.Unmarshal(document.Data, &task); err != nil {
			t.Fatalf("decode mutation task: %v; data = %s", err, document.Data)
		}
		if !reflect.DeepEqual(task, result.Task) {
			t.Fatalf("data task = %#v, want %#v", task, result.Task)
		}
		if !reflect.DeepEqual(document.Warnings, result.Warnings) {
			t.Fatalf("warnings = %#v, want %#v", document.Warnings, result.Warnings)
		}
	})
}

func TestRunInvalidInvocationAndEarlyJSONErrors(t *testing.T) {
	repository := testrepo.New(t)

	t.Run("no command renders global help", func(t *testing.T) {
		code, stdout, stderr := run(t, repository)
		if code != 0 {
			t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr)
		}
		if !strings.Contains(stdout, "Usage: workbook <command> [arguments]") {
			t.Fatalf("Run() stdout = %q, want global help", stdout)
		}
		if stderr != "" {
			t.Fatalf("Run() stderr = %q, want empty", stderr)
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
		code, stdout, stderr := run(t, repository, "create", "Needs actor", "--json")
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
		code, stdout, stderr := run(t, repository, "create", "Needs actor")
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

	code, stdout, stderr := run(t, repository, "setup", "--json")
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
		code, stdout, stderr := run(t, repository, "setup", "--key", "--", "--json")
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
		code, stdout, stderr := run(t, repository, "setup", "--key", "--json")
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

func TestRunHooksInvocationErrorsRetainJSONIntent(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing subcommand", args: []string{"hooks", "--json"}},
		{name: "unknown subcommand", args: []string{"hooks", "unknown", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := testrepo.New(t)
			code, stdout, stderr := run(t, repository, test.args...)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2; stderr = %q", test.args, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("Run(%q) stdout = %q, want empty", test.args, stdout)
			}
			assertJSONError(t, stderr, core.CategoryInvocation, "")
			assertNoWorkbookDirectory(t, repository)
		})
	}
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

func TestRunRequiresInitializationAndSetupIsIdempotent(t *testing.T) {
	repository := testrepo.New(t)

	code, stdout, stderr := run(t, repository, "list")
	if code != 3 {
		t.Fatalf("list before setup code = %d, want 3; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("list before setup stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Workbook is not initialized") {
		t.Fatalf("list before setup stderr = %q", stderr)
	}

	code, stdout, stderr = run(t, repository, "setup", "--key", "PROJ")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
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
			t.Errorf("setup stdout = %q, want %q", stdout, wanted)
		}
	}

	// Setup is idempotent in what it durably records. Its report differs on a
	// second run because it truthfully distinguishes what it wrote from what
	// was already current.
	code, second, stderr := run(t, repository, "setup", "--key", "PROJ")
	if code != 0 {
		t.Fatalf("second setup code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, wanted := range []string{
		"Repository:\t" + canonicalRepository,
		"Key:\tPROJ",
		"Tasks:\t0",
	} {
		if !strings.Contains(second, wanted) {
			t.Errorf("second setup stdout = %q, want %q", second, wanted)
		}
	}
	if projectID(t, stdout) != projectID(t, second) {
		t.Fatalf("second setup project ID = %q, want %q", projectID(t, second), projectID(t, stdout))
	}
	if strings.Contains(second, "\twritten") {
		t.Fatalf("second setup rewrote managed documentation:\n%s", second)
	}
}

func projectID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(line, "Project ID:\t"); found {
			return value
		}
	}
	t.Fatalf("output has no project ID:\n%s", output)
	return ""
}

func TestRunRebuildProducesVersionedResult(t *testing.T) {
	repository := initializedRepository(t)
	code, _, stderr := run(t, repository, "create", "Projected")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q)", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "rebuild", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("rebuild = (%d, %q, %q)", code, stdout, stderr)
	}
	result := assertJSONResult(t, stdout, "rebuild")
	var data struct {
		TaskCount int    `json:"taskCount"`
		CachePath string `json:"cachePath"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("decode rebuild result: %v; data = %s", err, result.Data)
	}
	if data.TaskCount != 1 || data.CachePath == "" {
		t.Fatalf("rebuild data = %#v, want one task and a cache path", data)
	}
}

func TestOpenServiceUsesSplitMutationStores(t *testing.T) {
	repository := initializedRepository(t)

	service, err := openService(context.Background(), repository)
	if err != nil {
		t.Fatalf("openService() error = %v", err)
	}
	reader, ok := service.Reader.(*projection.Store)
	if !ok {
		t.Fatalf("Reader = %T, want *projection.Store", service.Reader)
	}
	if _, ok := service.Writer.(*gitstore.Repository); !ok {
		t.Fatalf("Writer = %T, want *gitstore.Repository", service.Writer)
	}
	projectionStore, ok := service.Projection.(*projection.Store)
	if !ok {
		t.Fatalf("Projection = %T, want *projection.Store", service.Projection)
	}
	if projectionStore != reader {
		t.Fatalf("Projection = %T, want the opened reader instance", service.Projection)
	}
}

func TestRunExactMutationPathAdvancesCanonicalRefOnce(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "create", "Created", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q), want success", code, stdout, stderr)
	}
	created := decodeMutationTask(t, stdout, "create")
	createHead := assertCanonicalMutationHead(t, repository, created, "")

	code, stdout, stderr = run(t, repository, "update", created.ID, "--title", "Updated", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = (%d, %q, %q), want success", code, stdout, stderr)
	}
	updated := decodeMutationTask(t, stdout, "update")
	updateHead := assertCanonicalMutationHead(t, repository, updated, createHead)

	code, stdout, stderr = run(t, repository, "delete", created.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("delete = (%d, %q, %q), want success", code, stdout, stderr)
	}
	deleted := decodeMutationTask(t, stdout, "delete")
	deleteHead := assertCanonicalMutationHead(t, repository, deleted, updateHead)

	code, stdout, stderr = run(t, repository, "restore", created.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("restore = (%d, %q, %q), want success", code, stdout, stderr)
	}
	restored := decodeMutationTask(t, stdout, "restore")
	assertCanonicalMutationHead(t, repository, restored, deleteHead)
}

func TestReadCommandsRefreshCachedProjectionAfterGitTipAdvances(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Before advance", "--status", "ready", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	var created core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	code, stdout, stderr = run(t, repository, "rebuild", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("rebuild = (%d, %q, %q)", code, stdout, stderr)
	}
	readService, err := openReadService(context.Background(), repository)
	if err != nil {
		t.Fatalf("openReadService() error = %v", err)
	}
	if _, ok := readService.Reader.(*projection.Store); !ok {
		t.Fatalf("openReadService() reader = %T, want *projection.Store", readService.Reader)
	}

	code, stdout, stderr = run(t, repository, "update", created.ID, "--title", "After advance", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
	}

	for _, command := range [][]string{
		{"list", "--json"},
		{"show", created.ID, "--json"},
		{"board", "--json"},
		{"next", "--json"},
	} {
		code, stdout, stderr = run(t, repository, command...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s = (%d, %q, %q)", strings.Join(command, " "), code, stdout, stderr)
		}
		result := assertJSONResult(t, stdout, command[0])
		if !strings.Contains(string(result.Data), "After advance") {
			t.Fatalf("%s data = %s, want advanced title", strings.Join(command, " "), result.Data)
		}
	}
}

func TestRunCRUDLifecycleAndOutputContracts(t *testing.T) {
	repository := testrepo.New(t)

	code, _, stderr := run(t, repository, "setup", "--key", "PROJ")
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
	if strings.Contains(stdout, "\t") || !strings.HasPrefix(stdout, "ID") {
		t.Fatalf("list stdout = %q, want responsive table", stdout)
	}
	if !strings.Contains(stdout, created.ID) || !strings.Contains(stdout, "A full task title th...") || !strings.Contains(stdout, "agent,backend") {
		t.Fatalf("list stdout = %q, want task ID and deterministic terminal preview", stdout)
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

	code, stdout, stderr = run(t, repository, "restore", prefix, "--json")
	if code != 0 {
		t.Fatalf("restore code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "restore")
	var restored core.Task
	if err := json.Unmarshal(result.Data, &restored); err != nil {
		t.Fatalf("decode restored task: %v", err)
	}
	if restored.Deleted {
		t.Fatal("restored task Deleted = true, want false")
	}

	code, stdout, stderr = run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list after restore code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	if err := json.Unmarshal(result.Data, &active); err != nil {
		t.Fatalf("decode restored active list: %v", err)
	}
	if len(active) != 1 || active[0].ID != restored.ID {
		t.Fatalf("active list after restore = %#v, want restored task", active)
	}
}

func TestCLIInReviewStatus(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "create", "Review from creation", "--status", "in-review", "--json")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "create")
	var created core.Task
	if err := json.Unmarshal(result.Data, &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	code, stdout, stderr = run(t, repository, "create", "Review from update", "--json")
	if code != 0 {
		t.Fatalf("second create code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "create")
	var updated core.Task
	if err := json.Unmarshal(result.Data, &updated); err != nil {
		t.Fatalf("decode second created task: %v", err)
	}

	code, _, stderr = run(t, repository, "update", updated.ID, "--status", "in-review", "--json")
	if code != 0 {
		t.Fatalf("update code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr = run(t, repository, "list", "--status", "in-review", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	result = assertJSONResult(t, stdout, "list")
	var tasks []core.Task
	if err := json.Unmarshal(result.Data, &tasks); err != nil {
		t.Fatalf("decode in-review tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("in-review task count = %d, want 2", len(tasks))
	}
	if got, want := tasks[0].ID, created.ID; got != want {
		t.Fatalf("first in-review task ID = %q, want %q", got, want)
	}
	if got, want := tasks[1].ID, updated.ID; got != want {
		t.Fatalf("second in-review task ID = %q, want %q", got, want)
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
	commandList := firstFencedCodeBlock(t, implemented)
	var got []string
	for _, line := range strings.Split(commandList, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "workbook ") {
			got = append(got, line)
		}
	}
	want := []string{
		"workbook setup [--key <key>] [--no-docs] [--no-sync] [--skill-dir <dir>] [--no-skill] [--force] [--json]",
		"workbook create",
		"workbook list",
		"workbook board [--wide | --narrow] [--json]",
		"workbook show",
		"workbook update",
		"workbook delete",
		"workbook restore",
		"workbook move <task> (--before <task> | --after <task>) [--json]",
		"workbook depend <task> <dependency> [--json]",
		"workbook free <task> <dependency> [--json]",
		"workbook next [--json]",
		"workbook rebuild [--json]",
		"workbook validate [--full] [--json]",
		"workbook version [--json]",
		"workbook fetch [--json]",
		"workbook push [--json]",
		"workbook sync [--json]",
		"workbook docs install [--create <file>] [--skill-dir <dir>] [--no-skill] [--force] [--json]",
		"workbook docs update [--skill-dir <dir>] [--no-skill] [--force] [--json]",
		"workbook docs status [--skill-dir <dir>] [--no-skill] [--json]",
		"workbook docs remove [--skill-dir <dir>] [--no-skill] [--force] [--json]",
		"workbook hooks install [--json]",
		"workbook serve [--addr 127.0.0.1:7331]",
		"workbook help [command]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("implemented commands = %q, want exactly %q", got, want)
	}

	for _, command := range []string{"workbook claim"} {
		if !strings.Contains(proposed, command) {
			t.Errorf("proposed commands missing %q", command)
		}
	}

	readme := string(contents)
	assertREADMECommandPolicy(t, readme)
	if !strings.Contains(readme, "### Small-team workflow") {
		t.Error("README is missing the implemented small-team workflow")
	}
	for _, required := range []string{
		"drag-and-drop status changes",
		"PATCH /api/tasks/<id>/status",
		"client-rendered form",
		"shared new-task and detail form",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README web board documentation is missing %q", required)
		}
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

func TestREADMEDocumentsInstallationPaths(t *testing.T) {
	readmePath := filepath.Join("..", "..", "README.md")
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	readme := string(contents)

	for _, required := range []string{
		"## Installation",
		"brew install dgoings/tap/workbook",
		"workbook setup",
		"### Building from source",
		"Go 1.26",
		"Git",
		"./scripts/install.sh",
		"$HOME/.local/bin",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README installation section is missing %q", required)
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
	const claim = "## Current workflow\n\nRun `workbook claim` to acquire work.\n"
	violations := readmeCommandPolicyViolations(claim)
	if len(violations) != 1 || !strings.Contains(violations[0], `"claim"`) {
		t.Fatalf("violations = %q, want one for workbook claim", violations)
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
		"setup": true, "docs": true, "create": true, "list": true, "board": true,
		"show": true, "update": true, "delete": true, "restore": true, "fetch": true,
		"push": true, "sync": true, "hooks": true, "serve": true, "move": true,
		"depend": true, "free": true, "next": true, "rebuild": true, "validate": true, "version": true,
		"help": true,
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

func firstFencedCodeBlock(t *testing.T, section string) string {
	t.Helper()
	const fence = "```"
	start := strings.Index(section, fence)
	if start < 0 {
		t.Fatal("README implemented-command section has no code block")
	}
	section = section[start+len(fence):]
	end := strings.Index(section, fence)
	if end < 0 {
		t.Fatal("README implemented-command code block is unterminated")
	}
	return section[:end]
}

func TestRunServeRejectsInvalidArguments(t *testing.T) {
	repository := initializedRepository(t)

	for _, args := range [][]string{
		{"unexpected"},
		{"--json"},
		{"--addr", "127.0.0.1:0", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		err := runServe(context.Background(), args, repository, &stdout, &stderr)
		if core.CategoryOf(err) != core.CategoryInvocation {
			t.Errorf("runServe(%q) category = %q, want %q", args, core.CategoryOf(err), core.CategoryInvocation)
		}
		if stdout.Len() != 0 {
			t.Errorf("runServe(%q) stdout = %q, want empty", args, stdout.String())
		}
	}
}

func TestRunServeUpdatesTaskStatusThroughWebRoute(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Serve mutation task", "--status", "ready", "--json")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &task); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	waitForHTTP(t, "http://"+addr+"/healthz")

	request, err := http.NewRequest(http.MethodPatch, "http://"+addr+"/api/tasks/"+task.ID+"/status", strings.NewReader(`{"status":"in-progress"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH status: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runServe() error = %v; stderr = %q", err, serveStderr.String())
	}

	code, stdout, stderr = run(t, repository, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	var updated core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &updated); err != nil {
		t.Fatalf("decode shown task: %v", err)
	}
	if updated.Status != core.StatusInProgress {
		t.Fatalf("shown task status = %q, want %q", updated.Status, core.StatusInProgress)
	}
	if serveStdout.Len() != 0 {
		t.Fatalf("serve stdout = %q, want empty", serveStdout.String())
	}
}

func TestRunServePositionsTaskThroughWebRoute(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Moved through web position", "--status", "ready", "--priority", "medium", "--json")
	if code != 0 {
		t.Fatalf("create moved task code = %d, want 0; stderr = %q", code, stderr)
	}
	moved := decodeMutationTask(t, stdout, "create")

	code, stdout, stderr = run(t, repository, "create", "Position anchor", "--status", "in-progress", "--priority", "medium", "--json")
	if code != 0 {
		t.Fatalf("create anchor task code = %d, want 0; stderr = %q", code, stderr)
	}
	anchor := decodeMutationTask(t, stdout, "create")

	movedHeadBefore := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+moved.ID)
	anchorHeadBefore := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+anchor.ID)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	waitForHTTP(t, "http://"+addr+"/healthz")

	request, err := http.NewRequest(
		http.MethodPatch,
		"http://"+addr+"/api/tasks/"+moved.ID+"/position",
		strings.NewReader(`{"status":"in-progress","before":"`+anchor.ID+`"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH position: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PATCH position = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
	}
	var mutation struct {
		Task core.Task `json:"task"`
	}
	if err := json.Unmarshal(body, &mutation); err != nil {
		t.Fatalf("decode placement mutation task: %v; body = %s", err, body)
	}
	if mutation.Task.Status != core.StatusInProgress {
		t.Fatalf("placement mutation status = %q, want %q", mutation.Task.Status, core.StatusInProgress)
	}
	movedRank, ok := new(big.Rat).SetString(mutation.Task.Rank)
	if !ok {
		t.Fatalf("parse moved rank %q", mutation.Task.Rank)
	}
	anchorRank, ok := new(big.Rat).SetString(anchor.Rank)
	if !ok {
		t.Fatalf("parse anchor rank %q", anchor.Rank)
	}
	if movedRank.Cmp(anchorRank) >= 0 {
		t.Fatalf("placement mutation rank = %q, want before anchor rank %q", mutation.Task.Rank, anchor.Rank)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runServe() error = %v; stderr = %q", err, serveStderr.String())
	}

	movedHeadAfter := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+moved.ID)
	anchorHeadAfter := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+anchor.ID)
	if movedHeadAfter == movedHeadBefore {
		t.Fatal("position did not advance the moved task ref")
	}
	if anchorHeadAfter != anchorHeadBefore {
		t.Fatal("position advanced the anchor task ref")
	}
	parents := strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", movedHeadAfter))
	if len(parents) != 2 || parents[1] != movedHeadBefore {
		t.Fatalf("position commit parents = %#v, want sole parent %q", parents, movedHeadBefore)
	}
	var pack core.OperationPack
	if err := json.Unmarshal([]byte(gitOutput(t, repository, "show", movedHeadAfter+":operation.json")), &pack); err != nil {
		t.Fatalf("decode placement operation pack: %v", err)
	}
	if len(pack.Operations) != 2 ||
		pack.Operations[0].Field != "status" || pack.Operations[0].Value != "in-progress" ||
		pack.Operations[1].Field != "rank" {
		t.Fatalf("placement operations = %#v", pack.Operations)
	}
	if serveStdout.Len() != 0 {
		t.Fatalf("serve stdout = %q, want empty", serveStdout.String())
	}
}

func TestRunServeListsGitTipAdvancedAfterStarting(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Before web advance", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	var created core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	waitForHTTP(t, "http://"+addr+"/healthz")

	code, stdout, stderr = run(t, repository, "update", created.ID, "--title", "After web advance", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("update = (%d, %q, %q)", code, stdout, stderr)
	}
	response, err := http.Get("http://" + addr + "/api/tasks")
	if err != nil {
		t.Fatalf("GET tasks: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "After web advance") {
		t.Fatalf("GET /api/tasks = (%d, %s), want advanced task", response.StatusCode, body)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runServe() error = %v; stderr = %q", err, serveStderr.String())
	}
}

func TestRunServeCreatesTaskThroughWebRoute(t *testing.T) {
	repository := initializedRepository(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	waitForHTTP(t, "http://"+addr+"/healthz")

	response, err := http.Post("http://"+addr+"/api/tasks", "application/json", strings.NewReader(`{"title":"Persisted from web","description":"The listener must use the Git-backed service.","status":"in-review","priority":"high","labels":["web","persistence"]}`))
	if err != nil {
		t.Fatalf("POST task: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST task = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
	}
	var mutation struct {
		Task core.Task `json:"task"`
	}
	if err := json.Unmarshal(body, &mutation); err != nil {
		t.Fatalf("decode created task: %v; body = %s", err, body)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runServe() error = %v; stderr = %q", err, serveStderr.String())
	}

	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	var tasks []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &tasks); err != nil {
		t.Fatalf("decode listed tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID == mutation.Task.ID {
			if task.Title != "Persisted from web" || task.Description != "The listener must use the Git-backed service." || task.Status != core.StatusInReview || task.Priority != core.PriorityHigh || !reflect.DeepEqual(task.Labels, []string{"persistence", "web"}) {
				t.Fatalf("persisted task = %#v, want complete web create fields", task)
			}
			return
		}
	}
	t.Fatalf("list did not include created task %q: %#v", mutation.Task.ID, tasks)
}

func TestRunServeUpdatesAllTaskFieldsThroughWebRoute(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Before web update", "--json")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	var created core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	var serveStdout, serveStderr bytes.Buffer
	go func() {
		result <- runServe(ctx, []string{"--addr", addr}, repository, &serveStdout, &serveStderr)
	}()
	waitForHTTP(t, "http://"+addr+"/healthz")

	request, err := http.NewRequest(http.MethodPatch, "http://"+addr+"/api/tasks/"+created.ID, strings.NewReader(`{"title":"After web update","description":"All editable fields reached the core service.","status":"blocked","priority":"low","labels":["updated","web"]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH task: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PATCH task = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
	}

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runServe() error = %v; stderr = %q", err, serveStderr.String())
	}

	code, stdout, stderr = run(t, repository, "show", created.ID, "--json")
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	var updated core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &updated); err != nil {
		t.Fatalf("decode shown task: %v", err)
	}
	if updated.Title != "After web update" || updated.Description != "All editable fields reached the core service." || updated.Status != core.StatusBlocked || updated.Priority != core.PriorityLow || !reflect.DeepEqual(updated.Labels, []string{"updated", "web"}) {
		t.Fatalf("persisted task = %#v, want complete web update fields", updated)
	}
}

func TestRunServeReportsListenerFailureAsOperational(t *testing.T) {
	repository := initializedRepository(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var stdout, stderr bytes.Buffer
	err = runServe(context.Background(), []string{"--addr", listener.Addr().String()}, repository, &stdout, &stderr)
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("runServe() category = %q, want %q; error = %v", core.CategoryOf(err), core.CategoryOperational, err)
	}
	if !strings.Contains(err.Error(), "listen tcp") {
		t.Fatalf("runServe() error = %q, want listener cause", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("runServe() output = stdout %q stderr %q, want empty", stdout.String(), stderr.String())
	}
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", response.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %v", url, lastErr)
}

func initializedRepository(t *testing.T) string {
	t.Helper()
	repository := testrepo.New(t)
	code, _, stderr := run(t, repository, "setup")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
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

func decodeMutationTask(t *testing.T, output, command string) core.Task {
	t.Helper()
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, output, command).Data, &task); err != nil {
		t.Fatalf("decode %s task: %v", command, err)
	}
	return task
}

func assertCanonicalMutationHead(t *testing.T, repository string, task core.Task, previousHead string) string {
	t.Helper()
	ref := "refs/workbook/tasks/" + task.ID
	head := gitOutput(t, repository, "rev-parse", "--verify", ref)
	if task.Head != head {
		t.Fatalf("%s head = %q, Git ref = %q", task.ID, task.Head, head)
	}
	if head == previousHead {
		t.Fatalf("%s head did not advance from %q", task.ID, previousHead)
	}

	fields := strings.Fields(gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", head))
	if previousHead == "" {
		if len(fields) != 1 || fields[0] != head {
			t.Fatalf("create commit topology = %q, want parentless head %q", fields, head)
		}
		return head
	}
	if len(fields) != 2 || fields[0] != head || fields[1] != previousHead {
		t.Fatalf("mutation commit topology = %q, want head %q with sole parent %q", fields, head, previousHead)
	}
	return head
}

func gitOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
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
