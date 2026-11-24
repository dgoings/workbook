package cli

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// createThreeRelics makes three relics through the command and returns their
// IDs in creation order.
func createThreeRelics(t *testing.T, repository string) []string {
	t.Helper()
	ids := make([]string, 0, 3)
	for _, title := range []string{"Archive plan", "Board notes", "Release checklist"} {
		ids = append(ids, mustCreateRelic(t, repository, title, "--type", "spec").Relic.ID)
	}
	return ids
}

// commandOutput is what one command printed and how it exited.
type commandOutput struct {
	code           int
	stdout, stderr string
}

func runCommand(t *testing.T, repository string, args ...string) commandOutput {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	return commandOutput{code: code, stdout: stdout, stderr: stderr}
}

// listedAllTaskIDs runs `list --all --json` and returns the IDs it lists, sorted.
func listedAllTaskIDs(t *testing.T, repository string) []string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "list", "--all", "--json")
	if code != 0 {
		t.Fatalf("list --all --json = (%d, %q, %q)", code, stdout, stderr)
	}
	var tasks []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &tasks); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(tasks))
	for index, task := range tasks {
		ids[index] = task.ID
	}
	sort.Strings(ids)
	return ids
}

// failureOf runs a command with --json that must fail and returns its exit
// code and error category.
func failureOf(t *testing.T, repository string, args ...string) (int, core.Category) {
	t.Helper()
	code, stdout, stderr := run(t, repository, append(args, "--json")...)
	if code == 0 || stdout != "" {
		t.Fatalf("%v = (%d, %q, %q), want a failure", args, code, stdout, stderr)
	}
	var document errorDocument
	if err := json.Unmarshal([]byte(stderr), &document); err != nil {
		t.Fatalf("%v error = %q, want a JSON error: %v", args, stderr, err)
	}
	return code, document.Error.Category
}

// No task command lists, picks, serves or acts on a relic, and a relic ID
// given to one is refused exactly as an ID nothing holds is.
func TestTaskCommandsNeverReturnRelics(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	ready := cliCreateTaskWithStatus(t, repository, "Ready task", "ready")
	other := cliCreateTask(t, repository, "Other task")
	relics := createThreeRelics(t, repository)

	want := []string{ready.ID, other.ID}
	sort.Strings(want)
	if got := listedAllTaskIDs(t, repository); !reflect.DeepEqual(got, want) {
		t.Fatalf("list --all --json IDs = %v, want exactly the tasks %v", got, want)
	}
	code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next --json = (%d, %q, %q)", code, stdout, stderr)
	}
	var next core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "next").Data, &next); err != nil || next.ID != ready.ID {
		t.Fatalf("next --json = %s (%v), want the ready task %s", stdout, err, ready.ID)
	}

	// A repository with relics and no task answers as it did before them.
	empty := initializedRepository(t)
	commands := [][]string{{"next", "--any", "--no-sync", "--json"}, {"list", "--all", "--json"}}
	before := map[string]commandOutput{}
	for _, command := range commands {
		before[strings.Join(command, " ")] = runCommand(t, empty, command...)
	}
	rebuildBefore := runCommand(t, empty, "rebuild", "--json")
	createThreeRelics(t, empty)
	for _, command := range commands {
		if got, want := runCommand(t, empty, command...), before[strings.Join(command, " ")]; got.stdout != want.stdout || got.code != want.code {
			t.Errorf("%v with relics = %+v, before them %+v", command, got, want)
		}
	}
	if got := runCommand(t, empty, "rebuild", "--json"); got != rebuildBefore {
		t.Errorf("rebuild --json with relics = %+v, before them %+v", got, rebuildBefore)
	}

	addr := startServeBoard(t, repository)
	body, status := boardRequest(t, http.MethodGet, "http://"+addr+"/api/tasks", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d; body = %s", status, body)
	}
	served := boardTaskIDs(t, "http://"+addr+"/api/tasks")
	sort.Strings(served)
	if !reflect.DeepEqual(served, want) {
		t.Fatalf("GET /api/tasks IDs = %v, want %v", served, want)
	}
	if strings.Contains(string(body), "-R-") {
		t.Fatalf("GET /api/tasks names a relic: %s", body)
	}

	// The same commands, given an ID no record holds, in a repository with no
	// relics, are what a relic ID must match.
	bare := initializedRepository(t)
	bareTask := cliCreateTask(t, bare, "Only task")
	unused := core.NewRelicID("WB", relicTestID(424242))
	refsBefore := refTips(t, repository)
	for name, args := range map[string]func(task, id string) []string{
		"show":   func(_, id string) []string { return []string{"show", id} },
		"update": func(_, id string) []string { return []string{"update", id, "--title", "x", "--no-sync"} },
		"delete": func(_, id string) []string { return []string{"delete", id, "--no-sync"} },
		"move":   func(task, id string) []string { return []string{"move", task, "--before", id, "--no-sync"} },
		"depend": func(task, id string) []string { return []string{"depend", task, id, "--no-sync"} },
	} {
		wantCode, wantCategory := failureOf(t, bare, args(bareTask.ID, unused)...)
		for _, relic := range relics {
			if code, category := failureOf(t, repository, args(ready.ID, relic)...); code != wantCode || category != wantCategory {
				t.Errorf("%s with relic %s = (%d, %s), want (%d, %s) as for an unused ID", name, relic, code, category, wantCode, wantCategory)
			}
		}
	}
	requireSameRefs(t, "refs after task commands given relic IDs", refTips(t, repository), refsBefore)
}

func cliCreateTaskWithStatus(t *testing.T, repository, title, status string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--status", status, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create --status %s = (%d, %q, %q)", status, code, stdout, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

// What task commands print is byte for byte the same once relics exist, and
// validate adds only its one line about them.
func TestTaskOutputIsUnchangedByRelics(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTaskWithStatus(t, repository, "Ready task", "ready")
	cliCreateTask(t, repository, "Other task")
	commands := [][]string{
		{"list", "--json"},
		{"list"},
		{"show", task.ID, "--json"},
		{"next", "--any", "--no-sync", "--json"},
		{"status", "list", "--json"},
	}
	before := map[string]commandOutput{}
	for _, command := range commands {
		before[strings.Join(command, " ")] = runCommand(t, repository, command...)
	}
	// The first validate fills its cache, which changes its counts; the one
	// compared is the run after it.
	runCommand(t, repository, "validate")
	validateBefore := runCommand(t, repository, "validate")
	if validateBefore.code != 0 {
		t.Fatalf("validate before relics = %+v", validateBefore)
	}

	relics := createThreeRelics(t, repository)
	mustUpdateRelic(t, repository, relics[0], "--title", "Archive plan, revised")

	for _, command := range commands {
		key := strings.Join(command, " ")
		if got := runCommand(t, repository, command...); got != before[key] {
			t.Errorf("%s with relics = %+v\nbefore them %+v", key, got, before[key])
		}
	}
	validateAfter := runCommand(t, repository, "validate")
	if validateAfter.code != 0 || validateAfter.stderr != validateBefore.stderr {
		t.Fatalf("validate with relics = %+v", validateAfter)
	}
	beforeLines := strings.Split(strings.TrimSuffix(validateBefore.stdout, "\n"), "\n")
	afterLines := strings.Split(strings.TrimSuffix(validateAfter.stdout, "\n"), "\n")
	var added []string
	for index, cursor := 0, 0; index < len(afterLines); index++ {
		if cursor < len(beforeLines) && afterLines[index] == beforeLines[cursor] {
			cursor++
			continue
		}
		added = append(added, afterLines[index])
	}
	if len(afterLines) != len(beforeLines)+1 || len(added) != 1 || !strings.Contains(strings.ToLower(added[0]), "relic") {
		t.Fatalf("validate with relics added %q, want one relic line:\nbefore\n%s\nafter\n%s", added, validateBefore.stdout, validateAfter.stdout)
	}
}

// A sync publishes the task and leaves the relic where it is: origin holds no
// relic ref, and the local one has not moved.
func TestNoRelicRefIsPushedOrFetched(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	task := cliCreateTask(t, clone, "Published task")
	relic := mustCreateRelic(t, clone, "Local relic", "--type", "spec").Relic
	if code, stdout, stderr := run(t, clone, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	listing := cliGitOutput(t, clone, "ls-remote", "origin", "refs/workbook/*")
	if !strings.Contains(listing, "\trefs/workbook/tasks/"+task.ID) {
		t.Fatalf("origin lacks the task ref:\n%s", listing)
	}
	if strings.Contains(listing, "refs/workbook/relics/") || strings.Contains(listing, "-R-") {
		t.Fatalf("origin holds a relic ref:\n%s", listing)
	}
	if head := relicHeadOf(t, clone, relic.ID); head != relic.Head {
		t.Fatalf("local relic ref = %s after sync, want %s", head, relic.Head)
	}
	if got := refsUnder(t, clone, "refs/workbook/remotes/"); strings.Contains(got, "relics/") {
		t.Fatalf("this checkout tracks a relic ref:\n%s", got)
	}

	// A relic someone wrote to origin by hand is not fetched by a sync.
	cliGit(t, clone, "push", "--quiet", "origin", relicRef(relic.ID)+":"+relicRef(relic.ID))
	other := cloneCode(t, project)
	if code, stdout, stderr := run(t, other, "setup"); code != 0 {
		t.Fatalf("second setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, other, "sync"); code != 0 {
		t.Fatalf("second sync = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := refsUnder(t, other, "refs/workbook/"); strings.Contains(got, "relics/") {
		t.Fatalf("a fresh clone fetched a relic ref:\n%s", got)
	}
}

// Every relic command needs only the refs a tasks-only folder holds.
func TestRelicCommandsWorkInATasksOnlyFolder(t *testing.T) {
	t.Parallel()
	origin, _ := tasksOnlyOriginWithoutCode(t)
	folder := t.TempDir()
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", origin); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")

	created := mustCreateRelic(t, folder, "Relic on the board server", "--type", "spec")
	updated := mustUpdateRelic(t, folder, created.Relic.ID, "--add-part", "--part-type", "section")
	if len(updated.Parts) != 1 {
		t.Fatalf("relic update --add-part = %+v, want one part", updated)
	}
	if shown := shownRelic(t, folder, created.Relic.ID); len(shown.Parts) != 1 {
		t.Fatalf("relic show = %+v, want the added part", shown)
	}
	if code, stdout, stderr := run(t, folder, "validate"); code != 0 {
		t.Fatalf("validate = (%d, %q, %q)", code, stdout, stderr)
	}
}

// A relic is never on the tasks remote, so a checkout holding one holds work
// that exists only here: reset without a bundle is refused for it, and the
// bundle reset writes brings it back at the same commit.
func TestResetBundlesRelics(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	relic := mustCreateRelic(t, clone, "Only here", "--type", "spec").Relic
	ref := relicRef(relic.ID)
	before := refTips(t, clone)

	message, refusal := resetRefusalJSON(t, clone, 5, "--no-backup")
	if refusal.Reason != gitstore.ResetUnpublished || len(refusal.Unpublished) != 1 ||
		refusal.Unpublished[0].Ref != ref || refusal.Unpublished[0].RelicID != relic.ID || !refusal.Unpublished[0].New ||
		!strings.Contains(message, relic.ID) || !strings.Contains(message, "Only here") {
		t.Fatalf("reset --no-backup refusal = %q, %+v, want the relic named as unpublished", message, refusal)
	}
	requireSameRefs(t, "the checkout's refs after the refusal", refTips(t, clone), before)

	summary := resetJSON(t, clone)
	if summary.Bundle == "" || len(summary.Unpublished) != 1 || summary.Unpublished[0].RelicID != relic.ID {
		t.Fatalf("reset = %+v, want a bundle and the relic listed", summary)
	}
	if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
		t.Fatalf("refs left after the reset:\n%s", got)
	}

	fresh := t.TempDir()
	cliGit(t, fresh, "init", "--quiet")
	cliGit(t, fresh, "fetch", "--quiet", summary.Bundle, "refs/workbook/*:refs/workbook/*")
	if got := cliGitOutput(t, fresh, "rev-parse", "--verify", ref); got != relic.Head {
		t.Fatalf("the bundle restores %s at %q, want %s", ref, got, relic.Head)
	}

	// The restore command reset prints brings the relic back too.
	restore := exec.Command("sh", "-c", summary.Restore)
	restore.Dir = clone
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", summary.Restore, err, output)
	}
	if got := cliGitOutput(t, clone, "rev-parse", "--verify", ref); got != relic.Head {
		t.Fatalf("%s restores %s at %q, want %s", summary.Restore, ref, got, relic.Head)
	}
}

// A reset of the remote touches only what the remote holds: this checkout's
// relic, which the remote never held, stays where it is.
func TestResetRemoteLeavesALocalRelic(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	relic := mustCreateRelic(t, clone, "Only here", "--type", "spec").Relic
	summary := resetJSON(t, clone, "--remote", "origin", "--confirm", "origin")
	for _, ref := range summary.Refs {
		if strings.Contains(ref.Ref, "relics/") {
			t.Fatalf("the remote reset removed %s", ref.Ref)
		}
	}
	if got := refsUnder(t, project.code, "refs/workbook/"); got != "" {
		t.Fatalf("origin still holds:\n%s", got)
	}
	if head := relicHeadOf(t, clone, relic.ID); head != relic.Head {
		t.Fatalf("local relic ref = %s after the remote reset, want %s", head, relic.Head)
	}
}
