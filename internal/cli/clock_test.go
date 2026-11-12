package cli

import (
	"encoding/json"
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
