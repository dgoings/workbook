package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// Older readers meeting a due date, played with real binaries.
//
// A pack that records a due date declares minimum reader generation 5, which
// no build before due dates folds. Two older readers are worth standing up.
// The first is this tree with the generation pinned back to 4
// (buildPatchedGenerationBinary): it proves the marker's own behavior — show
// from the checkpoint, warn, refuse to change, keep synchronizing — because the
// constant is the whole gate for that. It is still this tree, so it knows the
// due field and shows the checkpoint's date; only a genuinely older build can
// show what a reader that never heard of due dates shows. The second reader is
// the v0.6.1 release, compiled from its tag, and it is the one that proves the
// date is left out rather than misread.

// dueCompatTask is the one task in a list result with the given ID, with its
// raw members alongside, so a test can tell a missing due member from null.
func dueCompatTask(t *testing.T, stdout, command, taskID string) (core.Task, map[string]json.RawMessage, resultDocument) {
	t.Helper()
	envelope := assertJSONResult(t, stdout, command)
	var tasks []json.RawMessage
	if command == "show" {
		tasks = []json.RawMessage{envelope.Data}
	} else if err := json.Unmarshal(envelope.Data, &tasks); err != nil {
		t.Fatalf("decode %s: %v; output = %s", command, err, stdout)
	}
	for _, raw := range tasks {
		var task core.Task
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatalf("decode %s task: %v; output = %s", command, err, stdout)
		}
		if err := json.Unmarshal(raw, &members); err != nil {
			t.Fatalf("decode %s task members: %v; output = %s", command, err, stdout)
		}
		if task.ID == taskID {
			return task, members, envelope
		}
	}
	t.Fatalf("%s holds no task %s: %s", command, taskID, stdout)
	return core.Task{}, nil, envelope
}

// assertOlderDue checks the due member an older reader printed: the date for a
// reader that knows the field, and no member at all for one that does not.
func assertOlderDue(t *testing.T, members map[string]json.RawMessage, command, wantDue string) {
	t.Helper()
	due, present := members["due"]
	switch {
	case wantDue == "" && present:
		t.Fatalf("older %s printed due %s, want no due member from a build that predates due dates", command, due)
	case wantDue != "" && string(due) != `"`+wantDue+`"`:
		t.Fatalf("older %s printed due %s, want the checkpoint's %q", command, due, wantDue)
	}
}

// olderReaderMeetsAPublishedDueDate has one clone set a due date and publish
// it, then plays the older reader against it: it lists and shows the task from
// its checkpoint with the newer-writer advisory, refuses to change it with exit
// 9, leaves origin's tip of that task exactly as it found it when it
// synchronizes, and still edits and publishes a task that never had a date.
func olderReaderMeetsAPublishedDueDate(t *testing.T, binary, wantDue string) {
	t.Helper()
	writer, older := cliSyncRepositories(t)
	dated := cliCreateTask(t, writer, "Dated by a newer clone")
	plain := cliCreateTask(t, writer, "Never dated")
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone initial sync code = %d; stderr = %q", code, stderr)
	}

	if code, _, stderr := run(t, writer, "update", dated.ID, "--due", "2030-01-15", "--no-sync"); code != 0 {
		t.Fatalf("writer update --due code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push of the date code = %d; stderr = %q", code, stderr)
	}
	tip := remoteTaskRefValue(t, writer, dated.ID)
	if tip == "" || tip != taskRefs(t, writer)[dated.ID] {
		t.Fatalf("origin holds %q for the dated task, want the writer's tip %s", tip, taskRefs(t, writer)[dated.ID])
	}

	if code, stdout, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone sync code = %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if got := taskRefs(t, older)[dated.ID]; got != tip {
		t.Fatalf("older clone holds %s for the dated task, want origin's tip %s", got, tip)
	}
	if got := remoteTaskRefValue(t, older, dated.ID); got != tip {
		t.Fatalf("origin holds %s after the older sync, want the dated tip %s unchanged", got, tip)
	}

	code, stdout, stderr := runBinary(t, binary, older, "list", "--json")
	if code != 0 {
		t.Fatalf("older list code = %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	listed, members, envelope := dueCompatTask(t, stdout, "list", dated.ID)
	if !listed.NewerWriter {
		t.Fatalf("older list shows %s without newerWriter: %s", dated.ID, stdout)
	}
	assertNewerWriterWarning(t, envelope.Warnings, dated.ID)
	assertOlderDue(t, members, "list", wantDue)
	if other, _, _ := dueCompatTask(t, stdout, "list", plain.ID); other.NewerWriter {
		t.Fatalf("older list marks the undated task %s newerWriter: %s", plain.ID, stdout)
	}

	code, stdout, stderr = runBinary(t, binary, older, "show", dated.ID, "--json")
	if code != 0 {
		t.Fatalf("older show code = %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	shown, members, envelope := dueCompatTask(t, stdout, "show", dated.ID)
	if !shown.NewerWriter || shown.Title != dated.Title {
		t.Fatalf("older show = %+v, want the checkpoint's title %q marked newerWriter", shown, dated.Title)
	}
	assertNewerWriterWarning(t, envelope.Warnings, dated.ID)
	assertOlderDue(t, members, "show", wantDue)
	code, stdout, stderr = runBinary(t, binary, older, "show", dated.ID)
	if code != 0 || !strings.Contains(stderr, "written by a newer workbook") {
		t.Fatalf("older show text = (%d, %q, %q), want the newer-writer warning", code, stdout, stderr)
	}
	if wantDue == "" && strings.Contains(stdout, "2030-01-15") {
		t.Fatalf("older show text prints a date it cannot know: %q", stdout)
	}

	code, stdout, stderr = runBinary(t, binary, older, "update", dated.ID, "--title", "Renamed by an older build")
	if code != 9 || !strings.Contains(stderr, "was written by a newer workbook; upgrade workbook") {
		t.Fatalf("older update = (%d, %q, %q), want exit 9 with the newer-writer refusal", code, stdout, stderr)
	}
	if got := taskRefs(t, older)[dated.ID]; got != tip {
		t.Fatalf("older clone holds %s after the refused update, want %s", got, tip)
	}

	if code, stdout, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone second sync code = %d, want 0; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if got := remoteTaskRefValue(t, older, dated.ID); got != tip {
		t.Fatalf("origin holds %s after the older clone synchronized, want the dated tip %s byte-unchanged", got, tip)
	}

	before := remoteTaskRefValue(t, older, plain.ID)
	if code, stdout, stderr := runBinary(t, binary, older, "update", plain.ID, "--title", "Edited by an older build"); code != 0 {
		t.Fatalf("older update of the undated task = (%d, %q, %q), want 0", code, stdout, stderr)
	}
	published := remoteTaskRefValue(t, older, plain.ID)
	if published == before || published != taskRefs(t, older)[plain.ID] {
		t.Fatalf("origin holds %s for the undated task (was %s), want the older clone's edit %s published",
			published, before, taskRefs(t, older)[plain.ID])
	}
	if code, _, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stderr = %q", code, stderr)
	}
	if got := cliShowTask(t, writer, plain.ID); got.Title != "Edited by an older build" {
		t.Fatalf("writer reads the undated task as %q, want the older clone's edit", got.Title)
	}
}

// olderReaderPublishesBeforeADueDate is the other order: a newer clone sets a
// date without publishing, the older clone publishes an edit to the same task
// first, and the newer clone's sync replays its date on top of that edit, so
// origin ends with both and nothing of either clone is lost.
func olderReaderPublishesBeforeADueDate(t *testing.T, binary string) {
	t.Helper()
	writer, older := cliSyncRepositories(t)
	task := cliCreateTask(t, writer, "Edited by both clones")
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone initial sync code = %d; stderr = %q", code, stderr)
	}

	if code, _, stderr := run(t, writer, "update", task.ID, "--due", "2030-03-03", "--no-sync"); code != 0 {
		t.Fatalf("writer update --due --no-sync code = %d; stderr = %q", code, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "update", task.ID, "--title", "Renamed by the older clone"); code != 0 {
		t.Fatalf("older update = (%d, %q, %q), want 0", code, stdout, stderr)
	}
	olderEdit := remoteTaskRefValue(t, older, task.ID)
	if olderEdit != taskRefs(t, older)[task.ID] {
		t.Fatalf("origin holds %s, want the older clone's edit %s published", olderEdit, taskRefs(t, older)[task.ID])
	}

	if code, stdout, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	tip := taskRefs(t, writer)[task.ID]
	if got := remoteTaskRefValue(t, writer, task.ID); got != tip {
		t.Fatalf("origin holds %s, want the writer's replayed tip %s", got, tip)
	}
	if err := exec.Command("git", "-C", writer, "merge-base", "--is-ancestor", olderEdit, tip).Run(); err != nil {
		t.Fatalf("the older clone's edit %s is not an ancestor of origin's tip %s: %v", olderEdit, tip, err)
	}
	shown := cliShowTask(t, writer, task.ID)
	if shown.Title != "Renamed by the older clone" || reportedDue(shown) != "2030-03-03" {
		t.Fatalf("writer reads (%q, %q), want the older clone's title and the replayed date", shown.Title, reportedDue(shown))
	}
	values, readers := dueOperations(t, writer, task.ID)
	if strings.Join(values, ",") != "2030-03-03" || len(readers) != 1 || readers[0] != core.SupportedFormatGeneration {
		t.Fatalf("due operations = %q at readers %v, want the one replayed date, marked %d",
			values, readers, core.SupportedFormatGeneration)
	}

	// The older clone now meets the date the way it meets any other.
	if code, stdout, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone sync code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	code, stdout, stderr := runBinary(t, binary, older, "show", task.ID, "--json")
	if code != 0 {
		t.Fatalf("older show code = %d; stderr = %q", code, stderr)
	}
	if shown, _, _ := dueCompatTask(t, stdout, "show", task.ID); !shown.NewerWriter || shown.Title != "Renamed by the older clone" {
		t.Fatalf("older show = %+v, want its own title, marked newerWriter", shown)
	}
	if got := remoteTaskRefValue(t, older, task.ID); got != tip {
		t.Fatalf("origin holds %s after the older sync, want %s unchanged", got, tip)
	}
}

func TestAGenerationFourReaderMeetsADueDate(t *testing.T) {
	t.Parallel()
	binary := buildPatchedGenerationBinary(t, 4)
	t.Run("published date", func(t *testing.T) {
		t.Parallel()
		olderReaderMeetsAPublishedDueDate(t, binary, "2030-01-15")
	})
	t.Run("older edit published first", func(t *testing.T) {
		t.Parallel()
		olderReaderPublishesBeforeADueDate(t, binary)
	})
}

func TestReleasedV061MeetsADueDate(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the v0.6.1 release; skipped in -short mode")
	}
	t.Parallel()
	binary := v0061Binary(t)
	t.Run("published date", func(t *testing.T) {
		t.Parallel()
		olderReaderMeetsAPublishedDueDate(t, binary, "")
	})
	t.Run("older edit published first", func(t *testing.T) {
		t.Parallel()
		olderReaderPublishesBeforeADueDate(t, binary)
	})
}
