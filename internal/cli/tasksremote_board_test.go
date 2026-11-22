package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

type boardMutationWarnings struct {
	Task     core.Task      `json:"task"`
	Warnings []core.Warning `json:"warnings"`
}

func boardSave(t *testing.T, method, url, payload string) boardMutationWarnings {
	t.Helper()
	body, status := boardRequest(t, method, url, payload)
	return decodeBoardMutationWarnings(t, body, status)
}

func decodeBoardMutationWarnings(t *testing.T, body []byte, status int) boardMutationWarnings {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("board mutation = %d, want %d; body = %s", status, http.StatusOK, body)
	}
	var document boardMutationWarnings
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode board mutation: %v; body = %s", err, body)
	}
	return document
}

// A board already running when the tasks move away from its origin keeps
// recording every save, and publishes none of them to the marked place: each
// answer carries the moved refusal as a warning, in both publication modes,
// and the board's own background synchronization says so once in its log.
// The same handle that published before the move is the one asked after it,
// which is the case a remembered identity agreement used to get wrong.
func TestTasksRemoteBoardRunningAcrossAMovePublishesNothingToTheOldPlace(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	stale := cloneCode(t, project)
	if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup = (%d, %q, %q)", code, stdout, stderr)
	}
	address, output, stop := startServeCapturing(t, stale)
	defer stop()
	base := "http://" + address

	before := boardSave(t, http.MethodPost, base+"/api/tasks", `{"title":"Saved before the move"}`)
	deadline := time.Now().Add(30 * time.Second)
	for cliGitOutputAllowingMissing(t, project.code, "refs/workbook/tasks/"+before.Task.ID) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("origin never received %s from the board before the move", before.Task.ID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	project.ids = append(project.ids, before.Task.ID)
	project.moveTasks(t)
	afterMove := repositoryState(t, project.code)

	for _, mode := range []string{"deferred", "inline"} {
		if body, status := boardRequest(t, http.MethodPut, base+"/api/sync", `{"mode":"`+mode+`"}`); status != http.StatusOK {
			t.Fatalf("PUT /api/sync %s = %d; body = %s", mode, status, body)
		}
		saved := boardSave(t, http.MethodPost, base+"/api/tasks", `{"title":"Saved after the move, `+mode+`"}`)
		if len(saved.Warnings) != 1 || saved.Warnings[0].Code != core.WarningTasksMoved ||
			!strings.Contains(saved.Warnings[0].Message, project.tasks) || !strings.Contains(saved.Warnings[0].Message, "workbook setup") {
			t.Fatalf("%s save warnings = %+v, want the moved refusal naming %s", mode, saved.Warnings, project.tasks)
		}
		if got := localTaskRef(t, stale, saved.Task.ID); got == "" {
			t.Fatalf("%s save %s was not recorded", mode, saved.Task.ID)
		}
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("the board published to the marked old place:\nbefore %s\nafter  %s", afterMove, after)
	}

	// The background synchronization reports the refusal once, however many
	// intervals pass.
	deadline = time.Now().Add(30 * time.Second)
	for !strings.Contains(output.String(), project.tasks) {
		if time.Now().After(deadline) {
			t.Fatalf("the board's synchronization never reported the move; it wrote:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(12 * time.Second)
	if count := strings.Count(output.String(), "tasks moved from origin to "+project.tasks); count != 1 {
		t.Fatalf("the board logged the move %d times, want once:\n%s", count, output.String())
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("the board's synchronization published to the marked old place:\nbefore %s\nafter  %s", afterMove, after)
	}
}

// A hosted board serving a tasks-only folder on the old place says the same
// to every person who saves, in its own words and without the server's
// paths.
func TestTasksRemoteHostedBoardAcrossAMoveSaysSoWithoutServerDetail(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.code); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")
	board := startHostedServe(t, folder, nil, "--public-url", proxyOrigin, "--actor-header", proxyHeader)
	project.moveTasks(t)
	afterMove := repositoryState(t, project.code)

	body, status, _ := board.do(t, proxied{method: http.MethodPost, path: "/api/tasks", body: `{"title":"Saved after the move"}`, person: "sam@example.com"})
	saved := decodeBoardMutationWarnings(t, body, status)
	if len(saved.Warnings) != 1 || saved.Warnings[0].Code != core.WarningTasksMoved {
		t.Fatalf("hosted save warnings = %+v, want the moved warning", saved.Warnings)
	}
	if message := saved.Warnings[0].Message; strings.Contains(message, project.tasks) || strings.Contains(message, folder) ||
		!strings.Contains(message, "moved to another repository") {
		t.Fatalf("hosted warning = %q, want the board's own sentence and no server path", message)
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("the hosted board published to the marked old place:\nbefore %s\nafter  %s", afterMove, after)
	}
}

// A board handing saves to a watcher reads this checkout's copy of the
// remote's identity ref before each hand-off. The watcher here last
// synchronized before the move and waits an hour, so it still counts as
// trustworthy; the marker reaches the copy through another command's fetch.
// Without the read, the save would be handed over and answered with no
// warning, and the watcher would be refused in its own log an hour later.
func TestTasksRemoteBoardHandOffReadsTheMarkerAnotherCommandFetched(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	stale := cloneCode(t, project)
	if code, stdout, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup = (%d, %q, %q)", code, stdout, stderr)
	}
	startCLIWatcher(t, stale, "1h")
	address, output, stop := startServeCapturing(t, stale)
	defer stop()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(output.String(), "external sync watcher") {
		if time.Now().After(deadline) {
			t.Fatalf("board never deferred to the external watcher; wrote %q", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	base := "http://" + address
	if body, status := boardRequest(t, http.MethodPut, base+"/api/sync", `{"mode":"deferred"}`); status != http.StatusOK {
		t.Fatalf("PUT /api/sync deferred = %d; body = %s", status, body)
	}

	project.moveTasks(t)
	afterMove := repositoryState(t, project.code)
	if code, _, stderr := run(t, stale, "fetch", "--json"); code != 3 {
		t.Fatalf("stale fetch after the move = %d, want the moved refusal; stderr = %q", code, stderr)
	}

	saved := boardSave(t, http.MethodPost, base+"/api/tasks", `{"title":"Saved after the move"}`)
	if len(saved.Warnings) != 1 || saved.Warnings[0].Code != core.WarningTasksMoved ||
		!strings.Contains(saved.Warnings[0].Message, project.tasks) {
		t.Fatalf("save warnings = %+v, want the moved refusal naming %s", saved.Warnings, project.tasks)
	}
	if got := localTaskRef(t, stale, saved.Task.ID); got == "" {
		t.Fatalf("save %s was not recorded", saved.Task.ID)
	}
	if after := repositoryState(t, project.code); after != afterMove {
		t.Fatalf("the board published to the marked old place:\nbefore %s\nafter  %s", afterMove, after)
	}
}
