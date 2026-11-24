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

// Relic refs travel with every push, fetch and sync, beside the task refs.
func TestRelicRefsAreSharedByFetchPushAndSync(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	originRelic := func(relicID string) string {
		t.Helper()
		fields := strings.Fields(cliGitOutput(t, clone, "ls-remote", "origin", relicRef(relicID)))
		if len(fields) != 2 {
			return ""
		}
		return fields[0]
	}

	pushed := mustCreateRelic(t, clone, "Pushed relic", "--type", "spec").Relic
	if code, stdout, stderr := run(t, clone, "push"); code != 0 {
		t.Fatalf("push = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := originRelic(pushed.ID); got != pushed.Head {
		t.Fatalf("origin's relic ref after push = %q, want %s", got, pushed.Head)
	}

	task := cliCreateTask(t, clone, "Published task")
	synced := mustCreateRelic(t, clone, "Synced relic", "--type", "spec").Relic
	if code, stdout, stderr := run(t, clone, "sync"); code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	listing := cliGitOutput(t, clone, "ls-remote", "origin", "refs/workbook/*")
	if !strings.Contains(listing, "\trefs/workbook/tasks/"+task.ID) {
		t.Fatalf("origin lacks the task ref:\n%s", listing)
	}
	if got := originRelic(synced.ID); got != synced.Head {
		t.Fatalf("origin's relic ref after sync = %q, want %s", got, synced.Head)
	}
	if head := relicHeadOf(t, clone, synced.ID); head != synced.Head {
		t.Fatalf("local relic ref = %s after sync, want %s", head, synced.Head)
	}

	// A fresh clone gets both with its first sync.
	other := cloneCode(t, project)
	if code, stdout, stderr := run(t, other, "setup"); code != 0 {
		t.Fatalf("second setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, other, "sync"); code != 0 {
		t.Fatalf("second sync = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, relic := range []struct{ ID, Head string }{{pushed.ID, pushed.Head}, {synced.ID, synced.Head}} {
		if head := relicHeadOf(t, other, relic.ID); head != relic.Head {
			t.Fatalf("the fresh clone's relic ref = %s after sync, want %s", head, relic.Head)
		}
		if got := refsUnder(t, other, "refs/workbook/remotes/"); !strings.Contains(got, "refs/workbook/remotes/origin/relics/"+relic.ID) {
			t.Fatalf("the fresh clone does not track the relic:\n%s", got)
		}
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

// handMadeRelicTip puts one hand-made commit on a relic's ref whose tree
// holds no operation.json, the way a person with Git plumbing would, and
// returns it. Every commit Workbook wrote is still reachable from it.
func handMadeRelicTip(t *testing.T, repository, relicID string) string {
	t.Helper()
	ref := relicRef(relicID)
	old := cliGitOutput(t, repository, "rev-parse", ref)
	saved := cliGitOutput(t, repository, "rev-parse", old+"^{tree}")
	tree := gitInput(t, repository, []byte("040000 tree "+saved+"\tsaved\n"), "mktree")
	forged := cliGitOutput(t, repository, "commit-tree", tree, "-p", old, "-m", "hand-made")
	cliGit(t, repository, "update-ref", ref, forged)
	return forged
}

// cloneWithAHandMadeRelicTip is a clone whose tasks are all published and
// which holds one relic of several commits with a hand-made commit on top.
func cloneWithAHandMadeRelicTip(t *testing.T) (clone, relicID, forged string) {
	t.Helper()
	project := codeProject(t, "sha1")
	clone = cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	relicID = mustCreateRelic(t, clone, "Template relic", "--type", "spec").Relic.ID
	mustUpdateRelic(t, clone, relicID, "--add-part", "--part-type", "section")
	mustUpdateRelic(t, clone, relicID, "--set", "status=draft")
	return clone, relicID, handMadeRelicTip(t, clone, relicID)
}

// A relic ref whose newest commit is hand-made and holds no operation.json is
// damaged, but its object is a commit with the relic's whole history under
// it, and nothing else holds a relic. Reset keeps it like an intact relic: it
// refuses to go ahead without a bundle, and the bundle it writes holds the
// ref at the same object, which the printed restore command fetches back.
func TestResetKeepsARelicRefWhoseNewestCommitIsHandMade(t *testing.T) {
	t.Parallel()
	clone, relicID, forged := cloneWithAHandMadeRelicTip(t)
	ref := relicRef(relicID)
	before := refTips(t, clone)

	message, refusal := resetRefusalJSON(t, clone, 5, "--no-backup")
	if refusal.Reason != gitstore.ResetUnpublished || len(refusal.Unpublished) != 1 ||
		refusal.Unpublished[0].Ref != ref || refusal.Unpublished[0].RelicID != relicID || !strings.Contains(message, relicID) {
		t.Fatalf("reset --no-backup refusal = %q, %+v, want the damaged relic named as unpublished", message, refusal)
	}
	requireSameRefs(t, "the checkout's refs after the refusal", refTips(t, clone), before)

	code, stdout, stderr := run(t, clone, "reset", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "Damaged:") || !strings.Contains(stdout, ref) ||
		strings.Contains(stdout, "nothing of it can be kept") {
		t.Fatalf("reset --dry-run = (%d, %q, %q), want the relic ref on a Damaged line that does not say nothing of it can be kept",
			code, stdout, stderr)
	}
	requireSameRefs(t, "the checkout's refs after the dry run", refTips(t, clone), before)

	summary := resetJSON(t, clone)
	var listed gitstore.ResetRef
	for _, removed := range summary.Refs {
		if removed.Ref == ref {
			listed = removed
		}
	}
	if listed.ObjectID != forged || listed.Damaged == "" || !listed.Bundleable {
		t.Fatalf("reset lists the relic ref as %+v, want it damaged at %s and bundleable", listed, forged)
	}
	if summary.Bundle == "" || len(summary.Unpublished) != 1 || summary.Unpublished[0].RelicID != relicID ||
		!strings.Contains(summary.Restore, "refs/workbook/relics/*:refs/workbook/relics/*") {
		t.Fatalf("reset = %+v, want a bundle, the relic listed, and a restore command that fetches relic refs", summary)
	}
	if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
		t.Fatalf("refs left after the reset:\n%s", got)
	}

	fresh := t.TempDir()
	cliGit(t, fresh, "init", "--quiet")
	cliGit(t, fresh, "fetch", "--quiet", summary.Bundle, "refs/workbook/*:refs/workbook/*")
	if got := cliGitOutput(t, fresh, "rev-parse", "--verify", ref); got != forged {
		t.Fatalf("the bundle restores %s at %q, want %s", ref, got, forged)
	}
	restore := exec.Command("sh", "-c", summary.Restore)
	restore.Dir = clone
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", summary.Restore, err, output)
	}
	if got := cliGitOutput(t, clone, "rev-parse", "--verify", ref); got != forged {
		t.Fatalf("%s restores %s at %q, want %s", summary.Restore, ref, got, forged)
	}
}

// With --discard-unpublished the same checkout is reset without a bundle, and
// the damaged relic ref goes with the rest.
func TestResetDiscardsADamagedRelicRefOnlyWhenTold(t *testing.T) {
	t.Parallel()
	clone, relicID, _ := cloneWithAHandMadeRelicTip(t)
	// No bundle is written, so nothing says the bundle keeps the ref.
	code, stdout, stderr := run(t, clone, "reset", "--no-backup", "--discard-unpublished", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "Damaged:") || strings.Contains(stdout, "kept") || strings.Contains(stdout, "keeps") {
		t.Fatalf("reset --no-backup --discard-unpublished --dry-run = (%d, %q, %q), want a Damaged line that promises nothing",
			code, stdout, stderr)
	}
	summary := resetJSON(t, clone, "--no-backup", "--discard-unpublished")
	if summary.Bundle != "" || summary.Restore != "" || len(summary.Unpublished) != 1 || summary.Unpublished[0].RelicID != relicID {
		t.Fatalf("reset --no-backup --discard-unpublished = %+v, want no bundle and the relic listed", summary)
	}
	if got := refsUnder(t, clone, "refs/workbook/"); got != "" {
		t.Fatalf("refs left after the reset:\n%s", got)
	}
}

// Only a relic ref whose object is missing or is not a commit cannot be
// bundled, and only such a ref is said to be removed with nothing kept.
func TestResetSaysNothingIsKeptOnlyOfARelicRefItCannotBundle(t *testing.T) {
	t.Parallel()
	clone, relicID, _ := cloneWithAHandMadeRelicTip(t)
	notACommit := relicRef(relicTestRelicID(72))
	cliGit(t, clone, "update-ref", notACommit, gitInput(t, clone, []byte("not a commit\n"), "hash-object", "-w", "--stdin"))

	code, stdout, stderr := run(t, clone, "reset", "--dry-run")
	if code != 0 {
		t.Fatalf("reset --dry-run = (%d, %q, %q)", code, stdout, stderr)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(line, "Damaged:") {
			continue
		}
		saysNothingKept := strings.Contains(line, "nothing of it can be kept")
		switch {
		case strings.Contains(line, notACommit):
			if !saysNothingKept {
				t.Fatalf("the line for the ref naming a blob = %q, want it to say nothing of it can be kept", line)
			}
		case strings.Contains(line, relicRef(relicID)):
			if saysNothingKept || !strings.Contains(line, "the bundle keeps it") {
				t.Fatalf("the line for the ref naming a commit = %q, want it to say the bundle keeps the ref", line)
			}
		default:
			t.Fatalf("unexpected Damaged line %q", line)
		}
	}
	if strings.Count(stdout, "Damaged:") != 2 {
		t.Fatalf("reset --dry-run = %q, want two Damaged lines", stdout)
	}
}

// What the refusal of reset --no-backup says to do has to be possible: sync
// publishes tasks and never a relic, so it is offered only for what is not a
// relic.
func TestResetRefusalOffersSyncOnlyForWhatSyncPublishes(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	const relicSentence = "workbook sync does not publish relics yet, so a relic is kept only by the bundle."
	mustCreateRelic(t, clone, "Only here", "--type", "spec")

	relicsOnly, _ := resetRefusalJSON(t, clone, 5, "--no-backup")
	if strings.Contains(relicsOnly, "workbook sync to publish") || !strings.Contains(relicsOnly, relicSentence) ||
		!strings.Contains(relicsOnly, "without --no-backup") || !strings.Contains(relicsOnly, "--discard-unpublished") {
		t.Fatalf("refusal with only a relic = %q, want no advice to run workbook sync", relicsOnly)
	}

	cliCreateTask(t, clone, "Created here only")
	mixed, _ := resetRefusalJSON(t, clone, 5, "--no-backup")
	syncAdvice := strings.Index(mixed, "Run workbook sync to publish everything but the relics")
	if syncAdvice < 0 || strings.Index(mixed, relicSentence) < syncAdvice {
		t.Fatalf("refusal with a task and a relic = %q, want sync offered for what is not a relic, then the sentence about relics", mixed)
	}
}

// A checkout with no relic is refused in the words it always was.
func TestResetRefusalWithoutARelicIsUnchanged(t *testing.T) {
	t.Parallel()
	project := codeProject(t, "sha1")
	clone := cloneCode(t, project)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	cliCreateTask(t, clone, "Created here only")
	message, _ := resetRefusalJSON(t, clone, 5, "--no-backup")
	if !strings.Contains(message, "Run workbook sync to publish it, or reset without --no-backup to keep it in a bundle, "+
		"or add --discard-unpublished to lose it.") || strings.Contains(message, "relic") {
		t.Fatalf("refusal with only a task = %q", message)
	}
}
