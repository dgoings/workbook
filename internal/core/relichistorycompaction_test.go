package core_test

// History and diff of a compacted relic. These tests are in an external package
// so that they can build checkpoints through the real steps of a relic
// compaction in gitstore, which imports core: CompactedRelicHistory,
// BuildRelicCheckpoint and CompactRelicRef. No checkpoint is forged.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

const compactedActor = "tester@example.test"

// compactedFixture is a real repository with a relic service whose clock the
// test moves.
type compactedFixture struct {
	t          *testing.T
	repository *gitstore.Repository
	config     core.ProjectConfig
	service    core.RelicService
	now        time.Time
}

func newCompactedFixture(t *testing.T) *compactedFixture {
	t.Helper()
	ctx := context.Background()
	repository, err := gitstore.Open(ctx, testrepo.New(t))
	if err != nil {
		t.Fatalf("gitstore.Open() error = %v", err)
	}
	config, _, err := repository.Init(ctx, "WB", core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	f := &compactedFixture{t: t, repository: repository, config: config,
		now: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)}
	f.service = core.RelicService{
		Config:        config,
		Reader:        repository,
		Writer:        repository,
		Tasks:         repository,
		HistoryReader: repository,
		IDs:           core.CryptoULIDSource{},
		Now:           func() time.Time { return f.now },
		Actor:         compactedActor,
	}
	return f
}

// advance moves the clock, which every later write records. It moves whole
// days, so that dates in messages tell the changes apart.
func (f *compactedFixture) advance() { f.now = f.now.Add(24 * time.Hour) }

// create creates a relic of two parts, the first with a body.
func (f *compactedFixture) create() (relicID, bodied, plain string) {
	f.t.Helper()
	created, err := f.service.Create(context.Background(), core.RelicCreateInput{
		Title: "Plan", Type: "plan",
		Parts: []core.RelicPartInput{
			{Type: "section", Body: &core.RelicBodyInput{Format: "md", Content: []byte("one\n")}},
			{Type: "note"},
		},
	})
	if err != nil {
		f.t.Fatalf("Create() error = %v", err)
	}
	f.advance()
	return created.Relic.ID, created.Parts[0].ID, created.Parts[1].ID
}

func (f *compactedFixture) update(relicID string, edits ...core.RelicEdit) core.RelicMutationResult {
	f.t.Helper()
	result, err := f.service.Update(context.Background(), relicID, edits)
	if err != nil {
		f.t.Fatalf("Update(%s) error = %v", relicID, err)
	}
	f.advance()
	return result
}

func (f *compactedFixture) title(relicID, title string) {
	f.t.Helper()
	f.update(relicID, core.RelicEdit{Kind: core.RelicEditTitle, Title: title})
}

// save saves a new body of the part from base and returns its version.
func (f *compactedFixture) save(relicID, partID, base, text string) string {
	f.t.Helper()
	result := f.update(relicID, core.RelicEdit{Kind: core.RelicEditPart, Part: partID, Base: base,
		Body: &core.RelicBodyInput{Format: "md", Content: []byte(text)}})
	return result.Parts[0].Body.Version
}

func (f *compactedFixture) retire(relicID string) {
	f.t.Helper()
	if _, err := f.service.Retire(context.Background(), relicID); err != nil {
		f.t.Fatalf("Retire(%s) error = %v", relicID, err)
	}
	f.advance()
}

func (f *compactedFixture) unretire(relicID string) {
	f.t.Helper()
	if _, err := f.service.Unretire(context.Background(), relicID); err != nil {
		f.t.Fatalf("Unretire(%s) error = %v", relicID, err)
	}
	f.advance()
}

// compact compacts the relic at boundary through the real steps a compaction
// run takes, and returns the checkpoint as Git reads it back.
func (f *compactedFixture) compact(relicID, boundary string) core.RelicSnapshot {
	f.t.Helper()
	ctx := context.Background()
	tip, err := f.repository.GetRelic(ctx, f.config, relicID)
	if err != nil {
		f.t.Fatalf("GetRelic(%s) error = %v", relicID, err)
	}
	ids, commits, first, last, removedAt, since, err := f.repository.CompactedRelicHistory(ctx, f.config, tip.Head)
	if err != nil {
		f.t.Fatalf("CompactedRelicHistory(%s) error = %v", relicID, err)
	}
	newID := func() string {
		id, err := core.CryptoULIDSource{}.New()
		if err != nil {
			f.t.Fatalf("New() error = %v", err)
		}
		return id
	}
	written, err := f.repository.BuildRelicCheckpoint(ctx, f.config, gitstore.RelicCheckpointInput{
		RelicID: relicID, Tip: tip,
		RunID: newID(), Generation: newID(), OperationID: newID(),
		Actor: compactedActor, Boundary: boundary, Since: since[boundary], Now: f.now, RemovedAt: removedAt,
		Summary: core.RelicCheckpointData{
			CompactedCommits: commits, FirstWallTime: first, LastWallTime: last, CompactedOperations: ids,
		},
	})
	if err != nil {
		f.t.Fatalf("BuildRelicCheckpoint(%s) error = %v", relicID, err)
	}
	if err := f.repository.CompactRelicRef(ctx, f.config, relicID, tip.Head, written.Head); err != nil {
		f.t.Fatalf("CompactRelicRef(%s) error = %v", relicID, err)
	}
	f.advance()
	checkpoint, err := f.repository.GetRelic(ctx, f.config, relicID)
	if err != nil {
		f.t.Fatalf("GetRelic(%s) of the checkpoint error = %v", relicID, err)
	}
	return checkpoint
}

func (f *compactedFixture) history(address string, options core.RelicHistoryOptions) core.RelicHistory {
	f.t.Helper()
	history, err := f.service.History(context.Background(), address, options)
	if err != nil {
		f.t.Fatalf("History(%s) error = %v", address, err)
	}
	return history
}

func (f *compactedFixture) diff(address string, versions ...string) core.RelicDiff {
	f.t.Helper()
	diff, err := f.service.Diff(context.Background(), address, versions, core.RelicDiffOptions{Context: 3})
	if err != nil {
		f.t.Fatalf("Diff(%s, %v) error = %v", address, versions, err)
	}
	return diff
}

// refuses requires a diff to fail with the category and the whole message.
func (f *compactedFixture) refuses(address string, versions []string, category core.Category, message string) {
	f.t.Helper()
	_, err := f.service.Diff(context.Background(), address, versions, core.RelicDiffOptions{Context: 3})
	if err == nil || core.CategoryOf(err) != category || err.Error() != message {
		f.t.Errorf("Diff(%s, %v) error = %v (%s), want %s %q", address, versions, err, core.CategoryOf(err), category, message)
	}
}

// historyIDs lists every operation ID a history lists.
func historyIDs(history core.RelicHistory) []string {
	var ids []string
	for _, change := range history.Changes {
		for _, operation := range change.Operations {
			ids = append(ids, operation.ID)
		}
	}
	return ids
}

// wantCompaction is the compaction a history or diff must report for the
// checkpoint, which replaced the history before.
func wantCompaction(checkpoint core.RelicSnapshot, before core.RelicHistory) core.RelicHistoryCompaction {
	pack := checkpoint.Operation
	summary, _ := pack.Checkpoint()
	return core.RelicHistoryCompaction{
		Version: pack.Operations[0].ID, RunID: summary.RunID, At: pack.WallTime, Boundary: summary.Boundary,
		Changes: before.Total, From: before.Changes[0].WallTime, To: before.Changes[len(before.Changes)-1].WallTime,
	}
}

func assertCompaction(t *testing.T, got *core.RelicHistoryCompaction, want core.RelicHistoryCompaction) {
	t.Helper()
	if got == nil {
		t.Fatalf("compacted is nil, want %+v", want)
	}
	encodedGot, errGot := json.Marshal(*got)
	encodedWant, errWant := json.Marshal(want)
	if errGot != nil || errWant != nil || string(encodedGot) != string(encodedWant) {
		t.Fatalf("compacted = %s, want %s", encodedGot, encodedWant)
	}
}

func TestHistoryAfterCompactionStartsAtCheckpoint(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, _ := f.create()
	second := f.save(relicID, bodied, bodied, "two\n")
	f.retire(relicID)
	before := f.history(relicID, core.RelicHistoryOptions{All: true})
	if before.Compacted != nil || before.Total != 3 {
		t.Fatalf("before = total %d, compacted %+v; want 3 and none", before.Total, before.Compacted)
	}
	checkpoint := f.compact(relicID, core.RelicBoundaryRetired)

	right := f.history(relicID, core.RelicHistoryOptions{})
	if right.Total != 0 || right.Showing != 0 || right.Changes == nil || len(right.Changes) != 0 {
		t.Fatalf("right after = total %d, showing %d, changes %#v; want none", right.Total, right.Showing, right.Changes)
	}
	assertCompaction(t, right.Compacted, wantCompaction(checkpoint, before))
	encoded, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &members)
	var names []string
	for name := range members["compacted"] {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"at", "boundary", "changes", "from", "runId", "to", "version"}; !slices.Equal(names, want) {
		t.Fatalf("compacted members = %v, want %v", names, want)
	}

	f.title(relicID, "Plan, revised")
	third := f.save(relicID, bodied, second, "three\n")
	history := f.history(relicID, core.RelicHistoryOptions{})
	if history.Total != 2 || history.Showing != 2 || len(history.Changes) != 2 {
		t.Fatalf("total %d, showing %d; want only the 2 edits above the checkpoint", history.Total, history.Showing)
	}
	assertCompaction(t, history.Compacted, wantCompaction(checkpoint, before))
	if slices.Contains(historyIDs(history), checkpoint.Operation.Operations[0].ID) {
		t.Fatalf("history lists the checkpoint as a change: %v", historyIDs(history))
	}
	// The save from the body the checkpoint kept took.
	saved := history.Changes[1].Operations[0]
	if saved.ID != third || saved.Applied == nil || !*saved.Applied {
		t.Fatalf("save from the kept body = %+v, want it applied", saved)
	}
	limited := f.history(relicID, core.RelicHistoryOptions{Limit: 1})
	if limited.Total != 2 || limited.Showing != 1 || limited.Compacted == nil {
		t.Fatalf("limited = total %d, showing %d, compacted %+v", limited.Total, limited.Showing, limited.Compacted)
	}

	// A second compaction reports the whole replaced history, the first
	// checkpoint left out of the count of changes it replaced.
	again := f.compact(relicID, core.RelicBoundaryRetired)
	compacted := f.history(relicID, core.RelicHistoryOptions{}).Compacted
	if compacted == nil || compacted.Version != again.Operation.Operations[0].ID ||
		compacted.Changes != before.Total+2 || !compacted.From.Equal(before.Changes[0].WallTime) ||
		!compacted.To.Equal(history.Changes[1].WallTime) {
		t.Fatalf("second compaction = %+v", compacted)
	}
}

func TestDeletedCompactionHistoryHasNoBodyVersions(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, _ := f.create()
	if _, err := f.service.Delete(context.Background(), relicID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	f.advance()
	f.compact(relicID, core.RelicBoundaryDeleted)
	history := f.history(relicID+"#"+bodied, core.RelicHistoryOptions{})
	if history.Total != 0 || history.BodyVersions != nil || history.Compacted == nil ||
		history.Compacted.Boundary != core.RelicBoundaryDeleted {
		t.Fatalf("history of a stub = %+v", history)
	}
}

func TestDiffOfDroppedVersionIsRefused(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, plain := f.create()
	second := f.save(relicID, bodied, bodied, "two\n")
	f.retire(relicID)
	before := f.history(relicID, core.RelicHistoryOptions{All: true})
	first := f.compact(relicID, core.RelicBoundaryRetired)
	at := first.Operation.WallTime.UTC().Format("2006-01-02")
	point := first.Operation.Operations[0].ID
	dropped := func(version, checkpoint, day string) string {
		return fmt.Sprintf("change %s of relic %s was dropped when it was compacted on %s; history and diffs reach back only to change %s",
			version, relicID, day, checkpoint)
	}

	old := historyIDs(before)
	if !slices.Contains(old, second) || !slices.Contains(old, plain) {
		t.Fatalf("history before compaction = %v", old)
	}
	for _, id := range old {
		message := dropped(id, point, at)
		f.refuses(relicID, []string{id}, core.CategoryNotFound, message)
		f.refuses(relicID, []string{strings.ToLower(id)}, core.CategoryNotFound, message)
		f.refuses(relicID, []string{point, id}, core.CategoryNotFound, message)
		f.refuses(relicID+"#"+bodied, []string{id, point}, core.CategoryNotFound, message)
	}
	// A prefix of one dropped change names it in full; one of several names
	// what was typed.
	unique := second[:len(second)-1]
	f.refuses(relicID, []string{unique}, core.CategoryNotFound, dropped(second, point, at))
	shared := second[:4]
	f.refuses(relicID, []string{shared}, core.CategoryNotFound, dropped(shared, point, at))
	// A version that no change ever had stays the ordinary refusal.
	f.refuses(relicID, []string{"7ZZZ"}, core.CategoryNotFound, fmt.Sprintf(
		"relic %s has no change %q in its history; a version is a change ID from \"workbook relic show %s --history\", not a Git commit",
		relicID, "7ZZZ", relicID))
	if core.ExitCode(core.Errorf(core.CategoryNotFound, "x")) != 4 {
		t.Fatal("not found no longer exits 4")
	}

	// A second compaction drops the first checkpoint too.
	f.title(relicID, "Plan, revised")
	again := f.compact(relicID, core.RelicBoundaryRetired)
	latest := again.Operation.Operations[0].ID
	day := again.Operation.WallTime.UTC().Format("2006-01-02")
	for _, id := range []string{point, second} {
		f.refuses(relicID, []string{id}, core.CategoryNotFound, dropped(id, latest, day))
	}
}

func TestDiffWithNoVersionRightAfterCompaction(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, _ := f.create()
	f.save(relicID, bodied, bodied, "two\n")
	f.retire(relicID)
	before := f.history(relicID, core.RelicHistoryOptions{All: true})
	checkpoint := f.compact(relicID, core.RelicBoundaryRetired)
	point := checkpoint.Operation.Operations[0].ID
	for _, versions := range [][]string{nil, {point}, {point, point}} {
		diff := f.diff(relicID, versions...)
		if diff.From == nil || diff.From.Version != point || diff.From.Commit != checkpoint.Head ||
			diff.To.Version != point || diff.To.Commit != checkpoint.Head {
			t.Fatalf("%v: points = %+v → %+v, want the checkpoint on both sides", versions, diff.From, diff.To)
		}
		if diff.Changes != nil || diff.Properties != nil || diff.Parts != nil || diff.Links != nil {
			t.Fatalf("%v: diff = %+v, want no changes", versions, diff)
		}
		assertCompaction(t, diff.Compacted, wantCompaction(checkpoint, before))
	}
}

func TestDiffAcrossCheckpointUsesCheckpointSide(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, _ := f.create()
	second := f.save(relicID, bodied, bodied, "two\n")
	f.retire(relicID)
	if diff := f.diff(relicID); diff.Compacted != nil {
		t.Fatalf("a relic never compacted reports compacted %+v", diff.Compacted)
	}
	checkpoint := f.compact(relicID, core.RelicBoundaryRetired)
	point := checkpoint.Operation.Operations[0].ID
	third := f.save(relicID, bodied, second, "three\n")

	// The newest change is the first above the checkpoint, so the checkpoint
	// is its earlier side, for the relic and for the part.
	for _, address := range []string{relicID, relicID + "#" + bodied} {
		for _, versions := range [][]string{nil, {point}, {point, third}} {
			diff := f.diff(address, versions...)
			if diff.From == nil || diff.From.Version != point || diff.To.Version != third || diff.Compacted == nil {
				t.Fatalf("%s %v: diff = %+v", address, versions, diff)
			}
			if len(diff.Parts) != 1 || diff.Parts[0].ID != bodied || diff.Parts[0].Kind != core.RelicPartChanged ||
				diff.Parts[0].Body == nil || diff.Parts[0].Body.From.Version != second || diff.Parts[0].Body.To.Version != third {
				t.Fatalf("%s %v: parts = %+v", address, versions, diff.Parts)
			}
		}
	}
}

func TestPartWithNoChangeSinceCompaction(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, plain := f.create()
	f.retire(relicID)
	checkpoint := f.compact(relicID, core.RelicBoundaryRetired)
	message := func(part string) string {
		return fmt.Sprintf("relic %s has no change to part %s since it was compacted on %s",
			relicID, part, checkpoint.Operation.WallTime.UTC().Format("2006-01-02"))
	}
	f.refuses(relicID+"#"+bodied, nil, core.CategoryNotFound, message(bodied))
	f.title(relicID, "Plan, revised")
	f.save(relicID, bodied, bodied, "two\n")
	f.refuses(relicID+"#"+plain, nil, core.CategoryNotFound, message(plain))
	if history := f.history(relicID+"#"+plain, core.RelicHistoryOptions{}); history.Total != 0 || history.Compacted == nil {
		t.Fatalf("history of an unchanged part = %+v", history)
	}
}

func TestRetireShowsInHistoryAndDiff(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, _, _ := f.create()
	f.retire(relicID)
	f.unretire(relicID)
	history := f.history(relicID, core.RelicHistoryOptions{})
	var summaries []string
	for _, change := range history.Changes {
		summaries = append(summaries, change.Summary)
	}
	if want := []string{"relic.create, part.add", "retired", "unretired"}; !slices.Equal(summaries, want) {
		t.Fatalf("summaries = %q, want %q", summaries, want)
	}
	// The operations keep their stored types.
	if got := history.Changes[1].Operations[0].Type; got != core.RelicOperationRetire {
		t.Fatalf("operation type = %s", got)
	}
	retired := history.Changes[1].Operations[0].ID
	diff := f.diff(relicID, history.Changes[0].Operations[0].ID, retired)
	if len(diff.Changes) != 1 || diff.Changes[0].Name != "retired" ||
		string(diff.Changes[0].From) != "false" || string(diff.Changes[0].To) != "true" {
		t.Fatalf("retire diff = %+v", diff.Changes)
	}
	diff = f.diff(relicID)
	if len(diff.Changes) != 1 || diff.Changes[0].Name != "retired" ||
		string(diff.Changes[0].From) != "true" || string(diff.Changes[0].To) != "false" {
		t.Fatalf("unretire diff = %+v", diff.Changes)
	}
}

func TestKeptBodyVersionListedAsCompacted(t *testing.T) {
	t.Parallel()
	f := newCompactedFixture(t)
	relicID, bodied, _ := f.create()
	second := f.save(relicID, bodied, bodied, "second\n")
	f.retire(relicID)
	checkpoint := f.compact(relicID, core.RelicBoundaryRetired)
	pack := checkpoint.Operation
	kept := core.RelicHistoryBodyVersion{
		Version: second, Format: "md", Bytes: int64(len("second\n")), Actor: pack.Actor.ID,
		WallTime: pack.WallTime, Commit: checkpoint.Head, Current: true, Compacted: true,
	}
	history := f.history(relicID+"#"+bodied, core.RelicHistoryOptions{})
	assertBodyVersions(t, history.BodyVersions, kept)

	third := f.save(relicID, bodied, second, "third\n")
	// A save from a body the compaction dropped is listed and not applied.
	history = f.history(relicID+"#"+bodied, core.RelicHistoryOptions{Limit: 1})
	kept.Current = false
	edit := history.Changes[len(history.Changes)-1]
	assertBodyVersions(t, history.BodyVersions, kept, core.RelicHistoryBodyVersion{
		Version: third, Base: second, Format: "md", Bytes: int64(len("third\n")), Actor: compactedActor,
		WallTime: edit.WallTime, Commit: edit.Commit, Current: true,
	})
	encoded, err := json.Marshal(history.BodyVersions)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(encoded), `"compacted":true`); got != 1 || strings.Contains(string(encoded), `"compacted":false`) {
		t.Fatalf("bodyVersions JSON = %s, want compacted on the kept version alone", encoded)
	}
	if whole := f.history(relicID, core.RelicHistoryOptions{}); whole.BodyVersions != nil {
		t.Fatalf("a whole relic's history lists body versions: %+v", whole.BodyVersions)
	}
}

func assertBodyVersions(t *testing.T, got []core.RelicHistoryBodyVersion, want ...core.RelicHistoryBodyVersion) {
	t.Helper()
	encodedGot, errGot := json.Marshal(got)
	encodedWant, errWant := json.Marshal(want)
	if errGot != nil || errWant != nil || string(encodedGot) != string(encodedWant) {
		t.Fatalf("body versions:\n got %s\nwant %s", encodedGot, encodedWant)
	}
}
