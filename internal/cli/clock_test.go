package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/dgoings/workbook/internal/testrepo"
)

// WORKBOOK_NOW exists so a project's past can be recorded after the fact, and
// a reader of that history sees time in two places: the timestamps the
// operations carry and the time encoded in every identifier minted with them.
// Each step below runs at its own time, so a path still reading the real clock,
// or reading the override for one of the two and not the other, shows up as a
// mismatch on that step alone. It drives the built binary rather than Run
// because the override is read from the process environment, and setting that
// in process would make the test serial.
func TestWorkbookNowDatesWhatEachCommandRecords(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := testrepo.New(t)

	const (
		setupAt     = "2024-03-04T09:00:00Z"
		statusAt    = "2024-03-04T09:30:00Z"
		createdAt   = "2024-03-05T14:00:00-05:00"
		commentedAt = "2024-03-06T10:15:00Z"
	)

	mustRunBinaryAt(t, binary, repository, setupAt, "setup", "--key", "WB", "--no-sync")
	mustRunBinaryAt(t, binary, repository, statusAt, "status", "add", "triage", "--after", "backlog", "--no-sync")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinaryAt(t, binary, repository, createdAt, "create", "Imported task", "--no-sync", "--json"), &created)
	taskID := created.Data.ID
	mustRunBinaryAt(t, binary, repository, commentedAt, "update", taskID, "--comment", "Landed in the first release", "--no-sync")

	var statusLog struct {
		Data struct {
			Entries []struct {
				OperationID string `json:"operationId"`
				WallTime    string `json:"wallTime"`
				Operation   string `json:"operation"`
			} `json:"entries"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinary(t, binary, repository, "status", "log", "--all", "--json"), &statusLog)
	recorded := map[string]string{}
	for _, entry := range statusLog.Data.Entries {
		recorded[entry.Operation] = entry.WallTime
		if entry.Operation == "status.add" {
			assertULIDTime(t, "status.add operation ID", entry.OperationID, statusAt)
		}
	}
	assertSameInstant(t, "config.genesis wall time", recorded["config.genesis"], setupAt)
	assertSameInstant(t, "status.add wall time", recorded["status.add"], statusAt)

	var shown struct {
		Data struct {
			ID                string `json:"id"`
			ProjectID         string `json:"projectId"`
			CreatedAt         string `json:"createdAt"`
			UpdatedAt         string `json:"updatedAt"`
			HistoryGeneration string `json:"historyGeneration"`
			Comments          []struct {
				ID        string `json:"id"`
				CreatedAt string `json:"createdAt"`
			} `json:"comments"`
			History struct {
				Changes []struct {
					WallTime string `json:"wallTime"`
				} `json:"changes"`
			} `json:"history"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinary(t, binary, repository, "show", taskID, "--history", "--json"), &shown)
	task := shown.Data

	assertULIDTime(t, "project ID", task.ProjectID, setupAt)
	assertULIDTime(t, "task ID", strings.TrimPrefix(task.ID, "WB-"), createdAt)
	assertULIDTime(t, "history generation", task.HistoryGeneration, createdAt)
	assertSameInstant(t, "task createdAt", task.CreatedAt, createdAt)
	assertSameInstant(t, "task updatedAt", task.UpdatedAt, commentedAt)
	if len(task.Comments) != 1 {
		t.Fatalf("task has %d comments, want 1", len(task.Comments))
	}
	assertULIDTime(t, "comment ID", task.Comments[0].ID, commentedAt)
	assertSameInstant(t, "comment createdAt", task.Comments[0].CreatedAt, commentedAt)
	if len(task.History.Changes) != 2 {
		t.Fatalf("task history has %d changes, want 2", len(task.History.Changes))
	}
	assertSameInstant(t, "create wall time", task.History.Changes[0].WallTime, createdAt)
	assertSameInstant(t, "comment wall time", task.History.Changes[1].WallTime, commentedAt)
}

// A value that does not parse must stop the command before it records
// anything, rather than fall back to the real clock: a migration that silently
// dated one step today would be indistinguishable from a correct one until
// someone read the history.
func TestWorkbookNowRefusesATimeItCannotRead(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)

	code, _, stderr := runBinaryAt(t, binary, repository, "last tuesday", "create", "Never recorded", "--no-sync")
	if code == 0 {
		t.Fatal("create with an unreadable WORKBOOK_NOW succeeded, want a refusal")
	}
	if !strings.Contains(stderr, "WORKBOOK_NOW") {
		t.Fatalf("stderr = %q, want it to name WORKBOOK_NOW", stderr)
	}
	if got := taskRefCount(t, repository); got != 0 {
		t.Fatalf("%d task refs exist after a refused create, want 0", got)
	}
}

// version and help record nothing, so a value they would never read does not
// stop them: someone whose shell holds a bad WORKBOOK_NOW can still ask which
// build they are running.
func TestWorkbookNowDoesNotStopVersionOrHelp(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)

	for _, args := range [][]string{{"version"}, {"help"}, {"create", "--help"}} {
		if code, _, stderr := runBinaryAt(t, binary, repository, "last tuesday", args...); code != 0 {
			t.Errorf("%v with an unreadable WORKBOOK_NOW exited %d, want 0; stderr = %q", args, code, stderr)
		}
	}
}

// The priority, key and display verbs write to the same configuration ledger
// the status verbs do, each through a call of its own, so each is dated here
// at its own time: one left on the real clock shows up as that row alone.
func TestWorkbookNowDatesEveryConfigurationVerb(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := testrepo.New(t)

	steps := []struct {
		operation string
		at        string
		args      []string
	}{
		{"config.genesis", "2024-03-04T09:00:00Z", []string{"setup", "--key", "WB", "--no-sync"}},
		{"priority.add", "2024-03-05T09:00:00Z", []string{"priority", "add", "urgent", "--no-sync"}},
		{"key.add", "2024-03-06T09:00:00+02:00", []string{"key", "add", "NEW", "--no-sync"}},
		{"display.set", "2024-03-07T09:00:00Z", []string{"config", "set", "project-name", "Atlas", "--no-sync"}},
	}
	for _, step := range steps {
		mustRunBinaryAt(t, binary, repository, step.at, step.args...)
	}

	entries := configLogEntries(t, mustRunBinary(t, binary, repository, "status", "log", "--all", "--json"))
	for _, step := range steps {
		entry, found := entries[step.operation]
		if !found {
			t.Errorf("the configuration log has no %s row", step.operation)
			continue
		}
		assertSameInstant(t, step.operation+" wall time", entry.WallTime, step.at)
		assertULIDTime(t, step.operation+" operation ID", entry.OperationID, step.at)
		// A configuration pack is stamped in UTC whatever offset was given.
		if !strings.HasSuffix(entry.WallTime, "Z") {
			t.Errorf("%s wall time = %s, want it recorded in UTC", step.operation, entry.WallTime)
		}
	}
}

// A compact run measures its cutoff from the recorded time and dates itself
// with it. Dated a day after the task was finished, a thirty-day cutoff finds
// nothing; dated two months after, it compacts the task, and the checkpoint's
// time and the run ID's both say when.
func TestWorkbookNowDatesACompaction(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := testrepo.New(t)

	const (
		setupAt     = "2024-03-04T09:00:00Z"
		doneAt      = "2024-03-10T09:00:00Z"
		tooEarlyAt  = "2024-03-11T09:00:00Z"
		compactedAt = "2024-05-10T09:00:00Z"
	)
	mustRunBinaryAt(t, binary, repository, setupAt, "setup", "--key", "WB", "--no-sync")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinaryAt(t, binary, repository, setupAt, "create", "Imported and finished", "--no-sync", "--json"), &created)
	mustRunBinaryAt(t, binary, repository, doneAt, "update", created.Data.ID, "--status", "done", "--no-sync")

	var compacted struct {
		Data struct {
			RunID string `json:"runId"`
			Tasks []struct {
				ID string `json:"id"`
			} `json:"tasks"`
		} `json:"data"`
	}
	compact := []string{"compact", "--done-only", "30d", "--no-backup", "--no-sync", "--json"}
	decodeClockJSON(t, mustRunBinaryAt(t, binary, repository, tooEarlyAt, compact...), &compacted)
	if len(compacted.Data.Tasks) != 0 {
		t.Fatalf("a run dated a day after the task was done compacted %d task(s), want none inside the cutoff",
			len(compacted.Data.Tasks))
	}
	decodeClockJSON(t, mustRunBinaryAt(t, binary, repository, compactedAt, compact...), &compacted)
	if len(compacted.Data.Tasks) != 1 || compacted.Data.Tasks[0].ID != created.Data.ID {
		t.Fatalf("a run dated two months on compacted %v, want the finished task", compacted.Data.Tasks)
	}
	assertULIDTime(t, "compaction run ID", compacted.Data.RunID, compactedAt)

	var shown struct {
		Data struct {
			HistoryGeneration string `json:"historyGeneration"`
			History           struct {
				Compacted struct {
					On string `json:"on"`
				} `json:"compacted"`
			} `json:"history"`
		} `json:"data"`
	}
	decodeClockJSON(t, mustRunBinary(t, binary, repository, "show", created.Data.ID, "--history", "--json"), &shown)
	assertSameInstant(t, "checkpoint time", shown.Data.History.Compacted.On, compactedAt)
	assertULIDTime(t, "checkpoint history generation", shown.Data.HistoryGeneration, compactedAt)
}

// The board writes through wiring of its own: one ID source and one ledger
// call per route family. This drives each family once with the override set
// and reads back that everything the project holds carries that time.
//
// Not parallel: the board runs in this process, so the override is set in this
// process's environment.
func TestWorkbookNowDatesWhatTheBoardRecords(t *testing.T) {
	const at = "2024-03-04T09:00:00Z"
	t.Setenv(recordedTimeVariable, at)
	repository := initializedRepository(t)
	addr := startServeBoard(t, repository)
	base := "http://" + addr

	head := boardVocabularyDocument(t, addr).Head
	status := boardVocabularyMutation(t, http.MethodPost, base+"/api/vocabulary/statuses",
		`{"status":"triage","expectedHead":`+quoteJSON(head)+`}`)
	key := boardKeyChange(t, http.MethodPost, base+"/api/vocabulary/keys",
		`{"key":"NEW","expectedHead":`+quoteJSON(status.Vocabulary.Head)+`}`)
	priority := boardPriorityChange(t, http.MethodPost, base+"/api/vocabulary/priorities",
		`{"priority":"blocker","expectedHead":`+quoteJSON(key.Vocabulary.Head)+`}`)
	boardDisplaySave(t, addr,
		`{"name":"Atlas","primaryColor":"","textColor":"","expectedHead":`+quoteJSON(priority.Vocabulary.Head)+`}`)
	body, code := boardRequest(t, http.MethodPost, base+"/api/tasks", `{"title":"Filed on the board"}`)
	task := decodeServeMutation(t, body, code)

	_, suffix, found := strings.Cut(task.ID, "-")
	if !found {
		t.Fatalf("task ID %q has no key prefix", task.ID)
	}
	assertULIDTime(t, "board task ID", suffix, at)
	assertSameInstant(t, "board task createdAt", task.CreatedAt.Format(time.RFC3339Nano), at)

	exit, stdout, stderr := run(t, repository, "status", "log", "--all", "--json")
	if exit != 0 {
		t.Fatalf("status log exited %d; stderr = %q", exit, stderr)
	}
	entries := configLogEntries(t, stdout)
	for _, operation := range []string{"status.add", "key.add", "priority.add", "display.set"} {
		entry, found := entries[operation]
		if !found {
			t.Errorf("the configuration log has no %s row", operation)
			continue
		}
		assertSameInstant(t, operation+" wall time", entry.WallTime, at)
		assertULIDTime(t, operation+" operation ID", entry.OperationID, at)
	}
}

type configLogEntry struct {
	OperationID string `json:"operationId"`
	WallTime    string `json:"wallTime"`
	Operation   string `json:"operation"`
}

// configLogEntries indexes `status log --all --json` by operation type. Each
// test above records every type once, so the newest row of a type is the only
// one.
func configLogEntries(t *testing.T, output string) map[string]configLogEntry {
	t.Helper()
	var log struct {
		Data struct {
			Entries []configLogEntry `json:"entries"`
		} `json:"data"`
	}
	decodeClockJSON(t, output, &log)
	entries := map[string]configLogEntry{}
	for _, entry := range log.Data.Entries {
		entries[entry.Operation] = entry
	}
	return entries
}

func runBinaryAt(t *testing.T, binary, repository, at string, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "WORKBOOK_NOW="+at)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case asExitError(err, &exit):
		return exit.ExitCode(), stdout.String(), stderr.String()
	default:
		t.Fatalf("run %v: %v", args, err)
		return -1, "", ""
	}
}

func mustRunBinaryAt(t *testing.T, binary, repository, at string, args ...string) string {
	t.Helper()
	code, stdout, stderr := runBinaryAt(t, binary, repository, at, args...)
	if code != 0 {
		t.Fatalf("WORKBOOK_NOW=%s %v exited %d; stdout = %q, stderr = %q", at, args, code, stdout, stderr)
	}
	return stdout
}

func decodeClockJSON(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
}

func assertSameInstant(t *testing.T, what, got, want string) {
	t.Helper()
	gotTime, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("%s = %q, not an RFC 3339 time: %v", what, got, err)
	}
	wantTime, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatalf("expected %s %q does not parse: %v", what, want, err)
	}
	if !gotTime.Equal(wantTime) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func assertULIDTime(t *testing.T, what, id, want string) {
	t.Helper()
	parsed, err := ulid.ParseStrict(id)
	if err != nil {
		t.Fatalf("%s %q is not a ULID: %v", what, id, err)
	}
	wantTime, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatalf("expected %s time %q does not parse: %v", what, want, err)
	}
	if got := ulid.Time(parsed.Time()); !got.Equal(wantTime) {
		t.Errorf("%s %s encodes %s, want %s", what, id, got.UTC().Format(time.RFC3339Nano), want)
	}
}
