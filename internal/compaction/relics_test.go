package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testenv"
)

// countingIndex is a relic index that counts its refreshes and fails them
// when err is set.
type countingIndex struct {
	refreshes int
	err       error
}

func (index *countingIndex) RefreshRelics(context.Context) error {
	index.refreshes++
	return index.err
}

// relicFixture is a project with a relic service writing at the fixture's
// clock, and the index a relic run refreshes.
type relicFixture struct {
	*fixture
	relics core.RelicService
	index  *countingIndex
}

func newRelicFixture(t *testing.T) *relicFixture {
	t.Helper()
	f := &relicFixture{fixture: newFixture(t), index: &countingIndex{}}
	f.relics = core.RelicService{
		Config: f.config,
		Reader: f.repository,
		Writer: f.repository,
		Tasks:  f.repository,
		IDs:    core.CryptoULIDSource{},
		Now:    func() time.Time { return f.now },
		Actor:  "tester@example.test",
	}
	return f
}

func (f *relicFixture) relicDeps() RelicDeps {
	return RelicDeps{Repository: f.repository, Config: f.config, Relics: f.relics, Index: f.index}
}

// createRelic creates a relic with two note parts, the first with a body, and
// returns its ID and the parts' IDs.
func (f *relicFixture) createRelic(title string) (string, []string) {
	f.t.Helper()
	created, err := f.relics.Create(context.Background(), core.RelicCreateInput{
		Title: title, Type: "decision",
		Properties: map[string]json.RawMessage{"outcome": json.RawMessage(`"open"`)},
		Parts: []core.RelicPartInput{
			{Type: "note", Fields: map[string]json.RawMessage{"status": json.RawMessage(`"kept"`)},
				Body: &core.RelicBodyInput{Format: "md", Content: []byte("the text we keep\n")}},
			{Type: "note", Fields: map[string]json.RawMessage{"status": json.RawMessage(`"draft"`)}},
		},
	})
	if err != nil {
		f.t.Fatalf("Create(%q) error = %v", title, err)
	}
	parts := make([]string, 0, len(created.Parts))
	for _, part := range created.Parts {
		parts = append(parts, part.ID)
	}
	return created.Relic.ID, parts
}

// change writes one relic commit of operations that carry no content.
func (f *relicFixture) change(relicID string, operations ...core.RelicOperation) core.RelicSnapshot {
	f.t.Helper()
	ctx := context.Background()
	tip, err := f.repository.GetRelic(ctx, f.config, relicID)
	if err != nil {
		f.t.Fatalf("GetRelic(%s) error = %v", relicID, err)
	}
	for index := range operations {
		if operations[index].ID == "" {
			source := core.CryptoULIDSource{}
			if operations[index].ID, err = source.New(); err != nil {
				f.t.Fatalf("New() error = %v", err)
			}
		}
	}
	state := tip.Record.State
	pack := core.NewRelicOperationPack(f.config.ProjectID, relicID, state.History.Generation, f.relics.Actor,
		state.LogicalClock+1, f.now, operations)
	record, err := core.ApplyRelic(&tip.Record, pack)
	if err != nil {
		f.t.Fatalf("ApplyRelic(%s) error = %v", relicID, err)
	}
	written, err := f.repository.WriteRelic(ctx, f.config, &tip, pack, record, nil, "update relic "+relicID)
	if err != nil {
		f.t.Fatalf("WriteRelic(%s) error = %v", relicID, err)
	}
	f.advance(time.Minute)
	return written
}

func (f *relicFixture) retire(relicID string) {
	f.change(relicID, core.RelicOperation{Type: core.RelicOperationRetire})
}

func (f *relicFixture) deleteRelic(relicID string) {
	f.change(relicID, core.RelicOperation{Type: core.RelicOperationTombstone})
}

func (f *relicFixture) removePart(relicID, partID string) {
	f.change(relicID, core.RelicOperation{Type: core.RelicOperationPartRemove, Part: partID})
}

func (f *relicFixture) touch(relicID string) {
	f.change(relicID, core.RelicOperation{Type: core.RelicOperationPropertySet, Name: "outcome",
		Value: json.RawMessage(`"changed"`)})
}

func (f *relicFixture) relicTip(relicID string) core.RelicSnapshot {
	f.t.Helper()
	tip, err := f.repository.GetRelic(context.Background(), f.config, relicID)
	if err != nil {
		f.t.Fatalf("GetRelic(%s) error = %v", relicID, err)
	}
	return tip
}

func (f *relicFixture) prepareRelics(options RelicOptions) RelicPlan {
	f.t.Helper()
	if options.Now.IsZero() {
		options.Now = f.now
	}
	plan, err := PrepareRelics(context.Background(), f.relicDeps(), options)
	if err != nil {
		f.t.Fatalf("PrepareRelics() error = %v", err)
	}
	return plan
}

func (f *relicFixture) compactRelicsNow(options RelicOptions) RelicResult {
	f.t.Helper()
	if options.Now.IsZero() {
		options.Now = f.now
	}
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), f.prepareRelics(options), options)
	if err != nil {
		f.t.Fatalf("ExecuteRelics() error = %v", err)
	}
	return result
}

func everyRelic() RelicCriteria { return RelicCriteria{Retired: true, Deleted: true} }

func relicCandidateIDs(candidates []RelicCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.RelicID)
	}
	return ids
}

func relicSkipFor(t *testing.T, skipped []RelicSkip, relicID string) RelicSkip {
	t.Helper()
	for _, skip := range skipped {
		if skip.RelicID == relicID {
			return skip
		}
	}
	t.Fatalf("relic %s was not skipped; skipped = %+v", relicID, skipped)
	return RelicSkip{}
}

func TestPrepareRelicsSelectsAndSkips(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	live, _ := f.createRelic("Live")
	retired, retiredParts := f.createRelic("Retired")
	f.removePart(retired, retiredParts[1])
	f.retire(retired)
	deleted, _ := f.createRelic("Deleted")
	f.deleteRelic(deleted)
	both, _ := f.createRelic("Retired and deleted")
	f.retire(both)
	f.deleteRelic(both)

	for name, test := range map[string]struct {
		criteria RelicCriteria
		want     []string
	}{
		"both":    {everyRelic(), sortedIDs(retired, deleted, both)},
		"retired": {RelicCriteria{Retired: true}, []string{retired}},
		"deleted": {RelicCriteria{Deleted: true}, sortedIDs(deleted, both)},
	} {
		plan := f.prepareRelics(RelicOptions{Criteria: test.criteria})
		requireIDs(t, name, relicCandidateIDs(plan.Candidates), test.want)
		if len(plan.Skipped) != 0 {
			t.Fatalf("%s: skipped = %+v, want none", name, plan.Skipped)
		}
	}
	plan := f.prepareRelics(RelicOptions{Criteria: everyRelic()})
	if slices.Contains(relicCandidateIDs(plan.Candidates), live) {
		t.Fatalf("a live relic was selected")
	}
	for _, candidate := range plan.Candidates {
		wantBoundary := core.RelicBoundaryDeleted
		if candidate.RelicID == retired {
			wantBoundary = core.RelicBoundaryRetired
		}
		if candidate.Boundary != wantBoundary || candidate.Since.IsZero() || candidate.Commits < 2 {
			t.Fatalf("candidate %s = %+v, want boundary %s, a date and its commits", candidate.RelicID, candidate, wantBoundary)
		}
		switch candidate.RelicID {
		case retired:
			// Only the removed part is dropped.
			if candidate.DroppedParts != 1 || candidate.DroppedBytes <= 0 {
				t.Fatalf("retired candidate drops %d parts, %d bytes; want 1 part", candidate.DroppedParts, candidate.DroppedBytes)
			}
		default:
			if candidate.DroppedParts != 2 || candidate.DroppedBytes <= 0 {
				t.Fatalf("deleted candidate drops %d parts, %d bytes; want 2 parts", candidate.DroppedParts, candidate.DroppedBytes)
			}
		}
		if candidate.EstimatedSavedBytes <= 0 {
			t.Fatalf("candidate %s saves %d bytes, want some", candidate.RelicID, candidate.EstimatedSavedBytes)
		}
	}
	if plan.EstimatedSavedBytes <= 0 {
		t.Fatalf("plan saves %d bytes", plan.EstimatedSavedBytes)
	}
}

func TestPrepareRelicsSkipReasons(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	ids := map[string]string{}
	for _, name := range []string{"compacted", "unpushed", "pushed", "parked", "newer", "unreadable"} {
		ids[name], _ = f.createRelic(name)
		f.touch(ids[name])
		f.retire(ids[name])
	}
	result := f.compactRelicsNow(RelicOptions{Criteria: everyRelic(), Backup: "-"})
	requireIDs(t, "first run", relicCandidateIDs(result.Compacted), slices.Sorted(maps.Values(ids)))
	// Everything is compacted; set up the others anew.
	for _, name := range []string{"unpushed", "pushed", "parked", "newer", "unreadable"} {
		ids[name], _ = f.createRelic(name + " again")
		f.touch(ids[name])
		f.retire(ids[name])
	}
	tracking := func(relicID string) string { return "refs/workbook/remotes/origin/relics/" + relicID }
	for _, name := range []string{"parked", "newer", "unreadable", "pushed"} {
		f.git("update-ref", tracking(ids[name]), f.relicTip(ids[name]).Head)
	}
	f.git("update-ref", "refs/workbook/reconciled-relics/"+ids["parked"]+"/0", f.relicTip(ids["parked"]).Head)
	writeNewerRelicTip(t, f, ids["newer"])
	f.git("update-ref", tracking(ids["newer"]), f.relicTip(ids["newer"]).Head)
	dropRelicRoot(t, f, ids["unreadable"])

	plan := f.prepareRelics(RelicOptions{Criteria: everyRelic(), RequirePushed: true})
	requireIDs(t, "candidates", relicCandidateIDs(plan.Candidates), []string{ids["pushed"]})
	for name, reason := range map[string]string{
		"unpushed":   ReasonNotPushed,
		"parked":     ReasonRelicConflictParked,
		"newer":      ReasonNewerWriter,
		"unreadable": ReasonUnreadable,
	} {
		if got := relicSkipFor(t, plan.Skipped, ids[name]).Reason; got != reason {
			t.Fatalf("%s: reason = %q, want %q", name, got, reason)
		}
	}
	for _, skip := range plan.Skipped {
		if skip.Reason == ReasonAlreadyCompacted {
			continue
		}
		if skip.Title == "" {
			t.Fatalf("skip %+v has no title", skip)
		}
	}
	if got := relicSkipFor(t, plan.Skipped, result.Compacted[0].RelicID).Reason; got != ReasonAlreadyCompacted {
		t.Fatalf("compacted relic reason = %q, want %q", got, ReasonAlreadyCompacted)
	}
	if !sortedSkips(plan.Skipped) {
		t.Fatalf("skips are not in relic ID order: %+v", plan.Skipped)
	}
	// A parked tip is never the run's to delete.
	if got := f.git("for-each-ref", "--format=%(refname)", "refs/workbook/reconciled-relics/"); !strings.Contains(got, ids["parked"]) {
		t.Fatalf("parked refs = %q", got)
	}
}

func sortedSkips(skipped []RelicSkip) bool {
	return slices.IsSortedFunc(skipped, func(a, b RelicSkip) int { return strings.Compare(a.RelicID, b.RelicID) })
}

// writeNewerRelicTip appends a commit to a relic ref that only a newer
// Workbook could have written: a pack and state declaring generation 11.
func writeNewerRelicTip(t *testing.T, f *relicFixture, relicID string) {
	t.Helper()
	tip := f.relicTip(relicID)
	rewrite := func(path string, edit func(map[string]any)) string {
		var document map[string]any
		if err := json.Unmarshal([]byte(f.git("cat-file", "blob", tip.Head+":"+path)), &document); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		edit(document)
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
		file := filepath.Join(t.TempDir(), "blob")
		if err := os.WriteFile(file, append(encoded, '\n'), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		return f.git("hash-object", "-w", file)
	}
	clock := float64(tip.Record.State.LogicalClock + 1)
	operation := rewrite("operation.json", func(document map[string]any) {
		document["minReader"] = 11
		document["logicalClock"] = clock
		document["operations"] = []any{map[string]any{"id": "01M9A000000000000000999999", "type": "relic.archive"}}
	})
	state := rewrite("state.json", func(document map[string]any) {
		document["minReader"] = 11
		document["logicalClock"] = clock
	})
	tree := f.git("ls-tree", tip.Head)
	var entries []string
	for _, line := range strings.Split(tree, "\n") {
		switch {
		case strings.HasSuffix(line, "\toperation.json"):
			line = "100644 blob " + operation + "\toperation.json"
		case strings.HasSuffix(line, "\tstate.json"):
			line = "100644 blob " + state + "\tstate.json"
		}
		entries = append(entries, line)
	}
	output, err := f.repository.Git(context.Background(), []byte(strings.Join(entries, "\n")+"\n"), "mktree")
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	commit := f.git("commit-tree", strings.TrimSpace(string(output)), "-p", tip.Head, "-m", "newer")
	f.git("update-ref", "refs/workbook/relics/"+relicID, commit, tip.Head)
	if snapshot := f.relicTip(relicID); !snapshot.Record.State.RequiresNewerReader() {
		t.Fatalf("forged tip does not need a newer reader")
	}
}

// dropRelicRoot removes the loose object of the relic's root commit, so its
// history can no longer be read in full while its tip still can.
func dropRelicRoot(t *testing.T, f *relicFixture, relicID string) {
	t.Helper()
	root := f.git("rev-list", "--max-parents=0", f.relicTip(relicID).Head)
	path := filepath.Join(f.repository.CommonGitDir, "objects", root[:2], root[2:])
	if err := os.Remove(path); err != nil {
		testenv.MissingCapability(t, "the relic's root commit is not a loose object here: %v", err)
	}
}

func TestExecuteRelicsCompactsAndBundles(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	retired, parts := f.createRelic("Retired")
	f.removePart(retired, parts[1])
	f.retire(retired)
	deleted, _ := f.createRelic("Deleted")
	f.deleteRelic(deleted)
	live, _ := f.createRelic("Live")
	liveTip := f.relicTip(live).Head

	options := RelicOptions{Criteria: everyRelic(), Flags: []string{"--relics"}, Now: f.now}
	plan := f.prepareRelics(options)
	tips := map[string]core.RelicSnapshot{retired: f.relicTip(retired), deleted: f.relicTip(deleted)}
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), plan, options)
	if err != nil {
		t.Fatalf("ExecuteRelics() error = %v", err)
	}
	requireIDs(t, "compacted", relicCandidateIDs(result.Compacted), sortedIDs(retired, deleted))
	if f.index.refreshes != 1 {
		t.Fatalf("index refreshed %d times, want once", f.index.refreshes)
	}
	if !filepath.IsAbs(result.BundlePath) || result.BundlePath != gitstore.DefaultArchivePath(f.repository.CommonGitDir, plan.RunID) {
		t.Fatalf("bundle path = %q", result.BundlePath)
	}
	heads := f.git("bundle", "list-heads", result.BundlePath)
	for relicID, tip := range tips {
		if !strings.Contains(heads, tip.Head+" refs/workbook/relics/"+relicID) {
			t.Fatalf("bundle heads %q lack %s at %s", heads, relicID, tip.Head)
		}
		now := f.relicTip(relicID)
		summary, ok := now.Operation.Checkpoint()
		if !ok || summary.CompactedFrom != tip.Head || summary.RunID != plan.RunID {
			t.Fatalf("relic %s tip = %+v, want a checkpoint of %s", relicID, now.Operation, tip.Head)
		}
		checked, failure, err := f.repository.ValidateRelicHistory(context.Background(), f.config,
			core.RelicHead{RelicID: relicID, ObjectID: now.Head})
		if err != nil || failure != nil || checked != 1 {
			t.Fatalf("ValidateRelicHistory(%s) = %d, %+v, %v", relicID, checked, failure, err)
		}
		if reflog := f.git("reflog", "show", "--format=%H", "refs/workbook/relics/"+relicID); strings.Contains(reflog, tip.Head) {
			t.Fatalf("reflog of %s %q still names the old tip", relicID, reflog)
		}
	}
	if got := f.relicTip(live).Head; got != liveTip {
		t.Fatalf("live relic moved")
	}
	var manifest gitstore.ArchiveManifest
	fresh := t.TempDir()
	f.git("init", "--quiet", "--bare", fresh)
	f.git("--git-dir="+fresh, "fetch", "--quiet", result.BundlePath, "refs/workbook/archive/manifest:refs/manifest")
	if err := json.Unmarshal([]byte(f.git("--git-dir="+fresh, "show", "refs/manifest:manifest.json")), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.Relics) != 2 || len(manifest.Tasks) != 0 || manifest.RunID != plan.RunID {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, entry := range manifest.Relics {
		if entry.OldTip != tips[entry.RelicID].Head || entry.NewGeneration != f.relicTip(entry.RelicID).Record.State.History.Generation {
			t.Fatalf("manifest entry %+v", entry)
		}
	}
}

func TestExecuteRelicsRequiresTheIndex(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	retired, _ := f.createRelic("Retired")
	f.retire(retired)
	tip := f.relicTip(retired).Head
	options := RelicOptions{Criteria: everyRelic(), Backup: "-", Now: f.now}
	plan := f.prepareRelics(options)
	deps := f.relicDeps()
	deps.Index = nil
	if _, err := ExecuteRelics(context.Background(), deps, plan, options); core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("ExecuteRelics() without an index error = %v, want an invocation error", err)
	}
	if got := f.relicTip(retired).Head; got != tip {
		t.Fatalf("relic moved without an index")
	}
}

func TestExecuteRelicsReportsIndexFailureAfterRefsMoved(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	retired, _ := f.createRelic("Retired")
	f.retire(retired)
	f.index.err = errors.New("index broke")
	options := RelicOptions{Criteria: everyRelic(), Backup: "-", Now: f.now}
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), f.prepareRelics(options), options)
	var projectionErr *ProjectionError
	if !errors.As(err, &projectionErr) || !strings.Contains(err.Error(), "relics were compacted") {
		t.Fatalf("ExecuteRelics() error = %v, want a *ProjectionError about relics", err)
	}
	requireIDs(t, "compacted", relicCandidateIDs(result.Compacted), []string{retired})
	if _, ok := f.relicTip(retired).Operation.Checkpoint(); !ok {
		t.Fatalf("relic was not compacted")
	}
}

func TestRelicBundleFailureMovesNoRef(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	retired, _ := f.createRelic("Retired")
	f.retire(retired)
	deleted, _ := f.createRelic("Deleted")
	f.deleteRelic(deleted)

	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if probe, err := os.CreateTemp(locked, "probe"); err == nil {
		probe.Close()
		testenv.MissingCapability(t, "a read-only directory is writable here (running as root?)")
	}
	options := RelicOptions{Criteria: everyRelic(), Backup: locked, Now: f.now}
	plan := f.prepareRelics(options)
	tips := map[string]string{retired: f.relicTip(retired).Head, deleted: f.relicTip(deleted).Head}
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), plan, options)
	if err == nil {
		t.Fatalf("ExecuteRelics() into a read-only directory succeeded")
	}
	if len(result.Compacted) != 0 || f.index.refreshes != 0 {
		t.Fatalf("result = %+v, refreshes %d; want nothing compacted", result.Compacted, f.index.refreshes)
	}
	for relicID, head := range tips {
		if got := f.relicTip(relicID).Head; got != head {
			t.Fatalf("relic %s moved to %s after a failed bundle, want %s", relicID, got, head)
		}
	}
	if entries, _ := os.ReadDir(locked); len(entries) != 0 {
		t.Fatalf("backup directory holds %d entries, want none", len(entries))
	}
}

func TestRelicThatMovesBeforeTheBundleIsSkipped(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	moved, _ := f.createRelic("Moves before the bundle")
	f.retire(moved)
	steady, _ := f.createRelic("Steady")
	f.retire(steady)
	options := RelicOptions{Criteria: everyRelic(), Now: f.now}
	plan := f.prepareRelics(options)
	steadyTip := f.relicTip(steady).Head
	f.touch(moved)
	movedTip := f.relicTip(moved).Head

	result, err := ExecuteRelics(context.Background(), f.relicDeps(), plan, options)
	if err != nil {
		t.Fatalf("ExecuteRelics() error = %v", err)
	}
	requireIDs(t, "compacted", relicCandidateIDs(result.Compacted), []string{steady})
	if reason := relicSkipFor(t, result.Skipped, moved).Reason; reason != ReasonChanged {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonChanged)
	}
	if got := f.relicTip(moved).Head; got != movedTip {
		t.Fatalf("relic changed before the bundle is at %s, want it left at %s", got, movedTip)
	}
	f.git("bundle", "verify", "--quiet", result.BundlePath)
	heads := f.git("bundle", "list-heads", result.BundlePath)
	if !strings.Contains(heads, steadyTip+" refs/workbook/relics/"+steady) || strings.Contains(heads, moved) {
		t.Fatalf("bundle heads = %q, want %s at %s and nothing of %s", heads, steady, steadyTip, moved)
	}
}

// A relic that moves after it was selected, with no bundle to find it first,
// is skipped by its compare-and-swap, and the others are still compacted.
func TestRelicThatMovesBeforeItsRefMoveIsSkipped(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	moved, _ := f.createRelic("Moves under the run")
	f.retire(moved)
	steady, _ := f.createRelic("Steady")
	f.retire(steady)
	options := RelicOptions{Criteria: everyRelic(), Backup: "-", Now: f.now}
	plan := f.prepareRelics(options)
	f.touch(moved)
	movedTip := f.relicTip(moved).Head
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), plan, options)
	if err != nil {
		t.Fatalf("ExecuteRelics() error = %v", err)
	}
	requireIDs(t, "compacted", relicCandidateIDs(result.Compacted), []string{steady})
	if reason := relicSkipFor(t, result.Skipped, moved).Reason; reason != ReasonChanged {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonChanged)
	}
	if got := f.relicTip(moved).Head; got != movedTip {
		t.Fatalf("relic is at %s, want %s", got, movedTip)
	}
}

// Not parallel: it sets beforeRelicBundleRetryHook.
func TestRelicRunFailsWhenARelicMovesDuringTheBundleRetry(t *testing.T) {
	f := newRelicFixture(t)
	first, _ := f.createRelic("Moves before the bundle")
	f.retire(first)
	second, _ := f.createRelic("Moves before the retry")
	f.retire(second)
	options := RelicOptions{Criteria: everyRelic(), Now: f.now}
	plan := f.prepareRelics(options)
	f.touch(first)
	tips := map[string]string{first: f.relicTip(first).Head}
	beforeRelicBundleRetryHook = func() {
		f.touch(second)
		tips[second] = f.relicTip(second).Head
	}
	t.Cleanup(func() { beforeRelicBundleRetryHook = nil })
	result, err := ExecuteRelics(context.Background(), f.relicDeps(), plan, options)
	beforeRelicBundleRetryHook = nil
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("ExecuteRelics() error = %v, want a stale write", err)
	}
	if len(result.Compacted) != 0 || result.BundlePath != "" {
		t.Fatalf("result = %+v, want nothing compacted and no bundle", result)
	}
	for relicID, head := range tips {
		if got := f.relicTip(relicID).Head; got != head {
			t.Fatalf("relic %s moved to %s, want %s", relicID, got, head)
		}
	}
}

// A run that stops after its first relic leaves that relic compacted and the
// rest untouched; the rerun reports it already compacted, expires the reflog a
// crash could have left, and finishes the rest.
//
// Not parallel: it sets afterRelicHook, which every ExecuteRelics reads.
func TestInterruptedRelicCompactionFinishesOnRerun(t *testing.T) {
	ctx := context.Background()
	f := newRelicFixture(t)
	var ids []string
	for _, title := range []string{"First", "Second", "Third"} {
		id, _ := f.createRelic(title)
		f.retire(id)
		ids = append(ids, id)
	}
	options := RelicOptions{Criteria: everyRelic(), Backup: "-", Now: f.now}
	plan := f.prepareRelics(options)
	tips := map[string]string{}
	for _, id := range ids {
		tips[id] = f.relicTip(id).Head
	}
	interruption := errors.New("interrupted")
	var interrupted string
	afterRelicHook = func(relicID string) error {
		interrupted = relicID
		checkpoint := f.relicTip(relicID).Head
		ref := "refs/workbook/relics/" + relicID
		f.git("update-ref", "--create-reflog", "-m", "test: back", ref, tips[relicID], checkpoint)
		f.git("update-ref", "--create-reflog", "-m", "test: forward", ref, checkpoint, tips[relicID])
		return interruption
	}
	t.Cleanup(func() { afterRelicHook = nil })
	result, err := ExecuteRelics(ctx, f.relicDeps(), plan, options)
	afterRelicHook = nil
	if !errors.Is(err, interruption) {
		t.Fatalf("ExecuteRelics() error = %v, want the interruption", err)
	}
	requireIDs(t, "compacted before the interruption", relicCandidateIDs(result.Compacted), []string{interrupted})
	if f.index.refreshes != 1 {
		t.Fatalf("an interrupted run refreshed the index %d times, want once for the relic it moved", f.index.refreshes)
	}
	for _, id := range ids {
		if id != interrupted && f.relicTip(id).Head != tips[id] {
			t.Fatalf("relic %s moved before the run reached it", id)
		}
	}
	ref := "refs/workbook/relics/" + interrupted
	if reflog := f.git("reflog", "show", "--format=%H", ref); !strings.Contains(reflog, tips[interrupted]) {
		t.Fatalf("fixture reflog %q does not name the old tip", reflog)
	}

	plan = f.prepareRelics(options)
	if reason := relicSkipFor(t, plan.Skipped, interrupted).Reason; reason != ReasonAlreadyCompacted {
		t.Fatalf("rerun skip reason = %q, want %q", reason, ReasonAlreadyCompacted)
	}
	result, err = ExecuteRelics(ctx, f.relicDeps(), plan, options)
	if err != nil {
		t.Fatalf("rerun ExecuteRelics() error = %v", err)
	}
	var rest []string
	for _, id := range ids {
		if id != interrupted {
			rest = append(rest, id)
		}
	}
	requireIDs(t, "compacted by the rerun", relicCandidateIDs(result.Compacted), sortedIDs(rest...))
	if reflog := f.git("reflog", "show", "--format=%H", ref); strings.Contains(reflog, tips[interrupted]) {
		t.Fatalf("rerun left the reflog naming the old tip: %q", reflog)
	}
}

// Not parallel: it replaces buildRelicCheckpoint, which every ExecuteRelics
// reads.
func TestRelicCheckpointTooLarge(t *testing.T) {
	f := newRelicFixture(t)
	large, _ := f.createRelic("Too many changes")
	f.retire(large)
	small, _ := f.createRelic("Fits")
	f.retire(small)
	largeTip := f.relicTip(large).Head
	build := buildRelicCheckpoint
	buildRelicCheckpoint = func(repository *gitstore.Repository, ctx context.Context, config core.ProjectConfig,
		input gitstore.RelicCheckpointInput) (core.RelicSnapshot, error) {
		if input.RelicID == large {
			return core.RelicSnapshot{}, core.Wrap(core.CategoryValidation, "too large", gitstore.ErrRelicCheckpointTooLarge)
		}
		return build(repository, ctx, config, input)
	}
	t.Cleanup(func() { buildRelicCheckpoint = build })
	result := f.compactRelicsNow(RelicOptions{Criteria: everyRelic(), Backup: "-"})
	buildRelicCheckpoint = build
	requireIDs(t, "compacted", relicCandidateIDs(result.Compacted), []string{small})
	if reason := relicSkipFor(t, result.Skipped, large).Reason; reason != "lists too many changes for one checkpoint" {
		t.Fatalf("skip reason = %q, want %q", reason, ReasonTooManyChanges)
	}
	if got := f.relicTip(large).Head; got != largeTip {
		t.Fatalf("relic too large to compact moved")
	}
}

// A task compaction with relics present moves no relic ref.
func TestTaskCompactionLeavesRelicsAlone(t *testing.T) {
	t.Parallel()
	f := newRelicFixture(t)
	task := f.create("Done task")
	f.status(task, core.StatusDone)
	retired, _ := f.createRelic("Retired")
	f.retire(retired)
	deleted, _ := f.createRelic("Deleted")
	f.deleteRelic(deleted)
	before := f.git("for-each-ref", "refs/workbook/relics/")

	result := f.compactNow(everything())
	requireIDs(t, "compacted tasks", compactedIDs(result), []string{task})
	if after := f.git("for-each-ref", "refs/workbook/relics/"); after != before {
		t.Fatalf("relic refs changed from %q to %q", before, after)
	}
	for _, skip := range result.Skipped {
		if skip.TaskID == retired || skip.TaskID == deleted {
			t.Fatalf("a task run reported relic %s", skip.TaskID)
		}
	}
}

func TestRelicSkipMarshalsWithJSONNames(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(RelicSkip{RelicID: "WB-R-1", Title: "T", Reason: ReasonRelicConflictParked})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if want := `{"relicId":"WB-R-1","title":"T","reason":"has a relic conflict set aside"}`; string(encoded) != want {
		t.Fatalf("Marshal() = %s, want %s", encoded, want)
	}
}
