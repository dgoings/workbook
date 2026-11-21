package compaction

import (
	"fmt"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

const (
	selectProjectID  = "01K0M65GBZ8F5ZQX0VC1J8H3TP"
	selectTaskID     = "WB-01K0M6B8A4FTT8C39MXXYTW7C1"
	selectGeneration = "01K0M6B8A4FTT8C39MXXYTW7C2"
	selectCheckpoint = "01K0M6B8A4FTT8C39MXXYTW7C3"
	selectRunID      = "01K0M6B8A4FTT8C39MXXYTW7C4"
)

var selectOrigin = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

// at is a wall time a whole number of days after selectOrigin.
func at(days int) time.Time {
	return selectOrigin.Add(time.Duration(days) * 24 * time.Hour)
}

// historyBuilder folds packs with chosen wall times the way the stores do, so
// a selection test reads a history that replays exactly as a real one.
type historyBuilder struct {
	t          *testing.T
	history    core.TaskHistory
	state      *core.StateDocument
	generation string
	sequence   int
}

func newHistory(t *testing.T) *historyBuilder {
	return &historyBuilder{t: t, generation: selectGeneration}
}

func (b *historyBuilder) add(wall time.Time, operations ...core.Operation) *historyBuilder {
	b.t.Helper()
	for index := range operations {
		b.sequence++
		operations[index].ID = fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05X", b.sequence)
	}
	clock := uint64(1)
	parent := ""
	if b.state != nil {
		clock = b.state.LogicalClock + 1
		parent = b.history.Entries[len(b.history.Entries)-1].Commit
	}
	pack := core.NewOperationPack(selectProjectID, selectTaskID, b.generation, "tester@example.test", clock, wall, operations)
	state, err := core.Apply(b.state, pack)
	if err != nil {
		b.t.Fatalf("Apply(%v) error = %v", operations[0].Type, err)
	}
	b.state = &state
	b.history.Entries = append(b.history.Entries, core.HistoryEntry{
		Commit:    fmt.Sprintf("%040x", len(b.history.Entries)+1),
		Parent:    parent,
		Operation: pack,
	})
	return b
}

// checkpoint starts the history at a history.checkpoint root that carries
// task and the seeds a compaction recorded.
func (b *historyBuilder) checkpoint(wall time.Time, task core.TaskData, doneAt, deletedAt *time.Time) *historyBuilder {
	b.t.Helper()
	b.generation = selectCheckpoint
	b.state = nil
	clock := uint64(7)
	pack := core.NewOperationPack(selectProjectID, selectTaskID, b.generation, "compactor@example.test", clock, wall,
		[]core.Operation{{
			ID:   "01K0M6B8A4FTT8C39MXXYTW7C5",
			Type: core.OperationHistoryCheckpoint,
			Task: &task,
			Checkpoint: &core.CheckpointData{
				RunID:               selectRunID,
				CompactedFrom:       fmt.Sprintf("%040x", 99),
				PreviousGeneration:  selectGeneration,
				CompactedCommits:    6,
				FirstWallTime:       task.CreatedAt,
				LastWallTime:        wall,
				BecameDoneAt:        doneAt,
				BecameDeletedAt:     deletedAt,
				Attachments:         core.CheckpointAttachmentsDropped,
				CompactedOperations: []string{"01K0M6B8A4FTT8C39MXXYTW7C6"},
			},
		}})
	state, err := core.Apply(nil, pack)
	if err != nil {
		b.t.Fatalf("Apply(checkpoint) error = %v", err)
	}
	b.state = &state
	b.history.Entries = []core.HistoryEntry{{Commit: fmt.Sprintf("%040x", 100), Operation: pack}}
	return b
}

func (b *historyBuilder) current() core.TaskData {
	return b.state.Task
}

func createAt(status core.Status, created time.Time) core.Operation {
	task := core.TaskData{
		Title: "Selected", Status: status, Priority: core.PriorityMedium,
		Labels: []string{}, Rank: "1/1", Dependencies: []string{},
		CreatedAt: created, UpdatedAt: created,
	}
	return core.Operation{Type: core.OperationTaskCreate, Task: &task}
}

func checkpointTask(status core.Status, created, updated time.Time, deleted bool) core.TaskData {
	return core.TaskData{
		Title: "Compacted", Status: status, Priority: core.PriorityMedium,
		Labels: []string{}, Rank: "1/1", Dependencies: []string{},
		CreatedAt: created, UpdatedAt: updated, Deleted: deleted,
	}
}

func setStatus(status core.Status) core.Operation {
	return core.Operation{Type: core.OperationFieldSet, Field: "status", Value: string(status)}
}

func comment(body string) core.Operation {
	return core.Operation{Type: core.OperationCommentAdd, Body: body}
}

func tombstone() core.Operation { return core.Operation{Type: core.OperationTaskTombstone} }

func restore() core.Operation { return core.Operation{Type: core.OperationTaskRestore} }

// finishedVocabulary is the built-in workflow plus two more done-tagged
// statuses, the shape of a project whose finished column is not "done".
func finishedVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	definitions := append(core.DefaultVocabulary().Definitions(),
		core.StatusDefinition{Status: "shipped", Label: "Shipped", Rank: "6/1", Tags: []core.StatusTag{core.StatusTagDone}},
		core.StatusDefinition{Status: "cancelled", Label: "Cancelled", Rank: "7/1", Tags: []core.StatusTag{core.StatusTagDone}},
	)
	vocabulary, err := core.NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

func requireTime(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

// Moving between two done-tagged statuses and commenting afterwards both leave
// the stretch where the task first entered it.
func TestBecameAtUsesStartOfDoneStretch(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		add(at(0), createAt(core.StatusReady, at(0))).
		add(at(1), setStatus(core.StatusDone)).
		add(at(2), setStatus("cancelled")).
		add(at(3), comment("Closing note"))

	doneAt, deletedAt := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, at(1))
	requireTime(t, "deletedAt", deletedAt, time.Time{})
}

// Reopening ends the stretch, so the clock restarts when it is done again.
func TestBecameAtResetsAfterReopen(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		add(at(0), createAt(core.StatusBacklog, at(0))).
		add(at(1), setStatus(core.StatusDone)).
		add(at(2), setStatus(core.StatusReady)).
		add(at(3), setStatus(core.StatusDone))

	doneAt, _ := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, at(3))
}

// A task that is not done now has no done time, whatever its history says.
func TestBecameAtNotDoneIsZero(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		add(at(0), createAt(core.StatusBacklog, at(0))).
		add(at(1), setStatus(core.StatusDone)).
		add(at(2), setStatus(core.StatusReady))

	doneAt, deletedAt := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, time.Time{})
	requireTime(t, "deletedAt", deletedAt, time.Time{})
}

// "shipped" was not done-tagged when this task was compacted, so its checkpoint
// carries no done time, and the vocabulary has since retagged it: no operation
// the history holds entered the stretch, and the last status change stands in.
func TestBecameAtFallsBackToLastStatusChange(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		checkpoint(at(4), checkpointTask("shipped", at(0), at(3), false), nil, nil).
		add(at(5), setStatus("cancelled")).
		add(at(6), comment("Still finished"))

	doneAt, _ := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, at(5))
}

// With no stretch start and no status change at all, the task's creation time
// is all that is left.
func TestBecameAtFallsBackToCreatedAt(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		checkpoint(at(4), checkpointTask("shipped", at(1), at(3), false), nil, nil).
		add(at(5), comment("Still finished"))

	doneAt, _ := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, at(1))
}

// A checkpoint's recorded times stand in for the history it replaced.
func TestBecameAtSeedsFromCheckpoint(t *testing.T) {
	t.Parallel()
	t.Run("done", func(t *testing.T) {
		t.Parallel()
		doneSeed := at(2)
		history := newHistory(t).
			checkpoint(at(4), checkpointTask(core.StatusDone, at(0), at(3), false), &doneSeed, nil).
			add(at(5), setStatus("cancelled")).
			add(at(6), comment("Moved within the finished columns"))

		doneAt, _ := BecameAt(history.history, finishedVocabulary(t), history.current())
		requireTime(t, "doneAt", doneAt, doneSeed)
	})
	t.Run("deleted", func(t *testing.T) {
		t.Parallel()
		doneSeed, deletedSeed := at(1), at(3)
		history := newHistory(t).
			checkpoint(at(4), checkpointTask(core.StatusDone, at(0), at(3), true), &doneSeed, &deletedSeed)

		doneAt, deletedAt := BecameAt(history.history, finishedVocabulary(t), history.current())
		requireTime(t, "doneAt", doneAt, doneSeed)
		requireTime(t, "deletedAt", deletedAt, deletedSeed)
	})
	t.Run("reopened", func(t *testing.T) {
		t.Parallel()
		doneSeed := at(2)
		history := newHistory(t).
			checkpoint(at(4), checkpointTask(core.StatusDone, at(0), at(3), false), &doneSeed, nil).
			add(at(5), setStatus(core.StatusReady)).
			add(at(6), setStatus(core.StatusDone))

		doneAt, _ := BecameAt(history.history, finishedVocabulary(t), history.current())
		requireTime(t, "doneAt", doneAt, at(6))
	})
}

// A delete, a restore and a second delete: the clock starts at the second.
func TestBecameAtDeleted(t *testing.T) {
	t.Parallel()
	history := newHistory(t).
		add(at(0), createAt(core.StatusBacklog, at(0))).
		add(at(1), tombstone()).
		add(at(2), restore()).
		add(at(3), setStatus(core.StatusReady)).
		add(at(4), tombstone())

	doneAt, deletedAt := BecameAt(history.history, finishedVocabulary(t), history.current())
	requireTime(t, "doneAt", doneAt, time.Time{})
	requireTime(t, "deletedAt", deletedAt, at(4))

	restored := newHistory(t).
		add(at(0), createAt(core.StatusBacklog, at(0))).
		add(at(1), tombstone()).
		add(at(2), restore())
	_, deletedAt = BecameAt(restored.history, finishedVocabulary(t), restored.current())
	requireTime(t, "deletedAt after restore", deletedAt, time.Time{})
}

// A status a stored token forwards to is the one the done tag is asked of.
func TestBecameAtResolvesForwardedStatus(t *testing.T) {
	t.Parallel()
	definitions := append(core.DefaultVocabulary().Definitions(),
		core.StatusDefinition{Status: "shipped", Label: "Shipped", Rank: "6/1", Tags: []core.StatusTag{core.StatusTagDone}},
	)
	vocabulary, err := core.NewVocabulary(definitions, []core.StatusAlias{{From: "released", To: "shipped"}}, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	history := newHistory(t).
		add(at(0), createAt(core.StatusBacklog, at(0))).
		add(at(2), setStatus("released")).
		add(at(3), comment("Out the door"))

	doneAt, _ := BecameAt(history.history, vocabulary, history.current())
	requireTime(t, "doneAt", doneAt, at(2))
}
