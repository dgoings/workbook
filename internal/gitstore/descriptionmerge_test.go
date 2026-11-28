package gitstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// sixLines is the six-line description the merge tests start from, with the
// numbered lines (1-based) changed to say who changed them.
func sixLines(changed map[int]string) string {
	var text strings.Builder
	for line := 1; line <= 6; line++ {
		if value, ok := changed[line]; ok {
			text.WriteString(value + "\n")
			continue
		}
		fmt.Fprintf(&text, "line %d\n", line)
	}
	return text.String()
}

// sharedDescribedTask creates a task in first with description, and brings it
// to second by synchronizing both.
func sharedDescribedTask(t *testing.T, first, second *Repository, config core.ProjectConfig, title, description string) core.Task {
	t.Helper()
	task := createSyncTask(t, first, config, title)
	setSyncTaskDescription(t, first, config, task.ID, description)
	syncClean(t, first, config)
	syncClean(t, second, config)
	return task
}

// assertDescriptionMergedWarnings requires exactly count description-merged
// warnings, each in Decision 14's words, and nothing else.
func assertDescriptionMergedWarnings(t *testing.T, result SyncResult, taskID string, count int) {
	t.Helper()
	want := core.Warning{
		Code: core.WarningDescriptionMerged,
		Message: "task " + taskID + ": the description was changed here and on origin; the two edits changed " +
			"different lines and were merged; `workbook show " + taskID + " --history` shows the merge",
	}
	if len(result.TaskWarnings) != count {
		t.Fatalf("TaskWarnings = %+v, want %d %s", result.TaskWarnings, count, core.WarningDescriptionMerged)
	}
	for _, warning := range result.TaskWarnings {
		if warning != want {
			t.Fatalf("warning = %+v, want %+v", warning, want)
		}
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want none", result.Conflicts)
	}
}

// mergeOperations returns a pack's description.merge operations.
func mergeOperations(pack core.OperationPack) []core.Operation {
	var merges []core.Operation
	for _, operation := range pack.Operations {
		if operation.Type == core.OperationDescriptionMerge {
			merges = append(merges, operation)
		}
	}
	return merges
}

// assertNoMergeOnHistory requires that no commit of the task's history in repo
// holds a description.merge or declares a minimum reader.
func assertNoMergeOnHistory(t *testing.T, repo *Repository, taskID string) {
	t.Helper()
	for _, commit := range strings.Fields(syncGit(t, repo.Root, "rev-list", taskRefPrefix+taskID)) {
		for _, file := range []string{"operation.json", "state.json"} {
			blob := syncGit(t, repo.Root, "cat-file", "blob", commit+":"+file)
			if strings.Contains(blob, string(core.OperationDescriptionMerge)) || strings.Contains(blob, `"minReader"`) {
				t.Fatalf("commit %s %s = %s, want no merge and no minimum reader", commit, file, blob)
			}
		}
	}
}

func TestDescriptionEditsOnDifferentLinesMerge(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		base := sixLines(nil)
		task := sharedDescribedTask(t, first, second, config, "Merged description", base)
		theirs := sixLines(map[int]string{2: "first's line 2"})
		setSyncTaskDescription(t, first, config, task.ID, theirs)
		syncClean(t, first, config)
		ours := sixLines(map[int]string{5: "second's line 5"})
		setSyncTaskDescription(t, second, config, task.ID, ours)
		local := currentSnapshot(t, second, config, task.ID)

		result := syncClean(t, second, config)
		assertSyncOutcome(t, result.Fetch, task.ID, SyncReconciled)
		assertDescriptionMergedWarnings(t, result.Fetch, task.ID, 1)
		assertSyncOutcome(t, result.Push, task.ID, SyncPublished)

		merged := sixLines(map[int]string{2: "first's line 2", 5: "second's line 5"})
		tip := currentSnapshot(t, second, config, task.ID)
		pack := tip.Operation
		if len(pack.Operations) != 2 || pack.Operations[0] != local.Operation.Operations[0] ||
			pack.Operations[1].Type != core.OperationDescriptionMerge {
			t.Fatalf("replayed pack = %+v, want the local field.set then a description.merge", pack.Operations)
		}
		merge := pack.Operations[1]
		wantMerge := core.DescriptionMerge{Other: pack.Operations[0].ID, Ancestor: base, Current: theirs,
			Method: core.DescriptionLineMergeMethod}
		if merge.Value != merged || merge.Merge == nil || *merge.Merge != wantMerge {
			t.Fatalf("merge = %q %+v, want %q %+v", merge.Value, merge.Merge, merged, wantMerge)
		}
		if want := core.DescriptionMergeOperationID(task.ID, wantMerge.Other, base, theirs, wantMerge.Method, pack.WallTime); merge.ID != want {
			t.Fatalf("merge ID = %s, want the derived %s", merge.ID, want)
		}
		if pack.MinReader != core.DescriptionMergeMinReader || tip.State.MinReader != core.DescriptionMergeMinReader {
			t.Fatalf("minReader = pack %d, state %d; want %d", pack.MinReader, tip.State.MinReader, core.DescriptionMergeMinReader)
		}
		if pack.Actor != local.Operation.Actor || !pack.WallTime.Equal(local.Operation.WallTime) {
			t.Fatalf("replayed actor and time = %v %v, want the local edit's %v %v",
				pack.Actor, pack.WallTime, local.Operation.Actor, local.Operation.WallTime)
		}
		if got := parentCount(t, second, tip.Head); got != 1 {
			t.Fatalf("merged commit has %d parents, want 1", got)
		}
		if parent := syncGit(t, second.Root, "rev-parse", tip.Head+"^"); parent != refValue(t, first, taskRefPrefix+task.ID) {
			t.Fatalf("merged commit's parent = %s, want first's tip", parent)
		}

		fetched := syncClean(t, first, config)
		assertSyncOutcome(t, fetched.Fetch, task.ID, SyncFastForwarded)
		if len(fetched.Fetch.TaskWarnings) != 0 {
			t.Fatalf("first clone's warnings = %+v, want none: it folds the stored merge", fetched.Fetch.TaskWarnings)
		}
		for _, repo := range []*Repository{first, second} {
			if got := currentSnapshot(t, repo, config, task.ID).State.Task.Description; got != merged {
				t.Fatalf("description = %q, want %q", got, merged)
			}
		}
	})
}

func TestDescriptionEditsOnTheSameLinesStillConflict(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		base := sixLines(nil)
		task := sharedDescribedTask(t, first, second, config, "Contested description", base)
		theirs := sixLines(map[int]string{3: "first's line 3"})
		setSyncTaskDescription(t, first, config, task.ID, theirs)
		syncClean(t, first, config)
		ours := sixLines(map[int]string{3: "second's line 3"})
		setSyncTaskDescription(t, second, config, task.ID, ours)
		local := refValue(t, second, taskRefPrefix+task.ID)
		origin := remoteRefValue(t, second, taskRefPrefix+task.ID)

		result, err := second.Sync(ctx, config)
		if core.CategoryOf(err) != core.CategoryConflict || core.ExitCode(err) != 8 {
			t.Fatalf("Sync() error = %v (exit %d), want a conflict, exit 8", err, core.ExitCode(err))
		}
		assertSyncOutcome(t, result.Fetch, task.ID, SyncConflicted)
		if len(result.Fetch.Conflicts) != 1 || len(result.Fetch.TaskWarnings) != 0 {
			t.Fatalf("conflicts = %+v, warnings = %+v; want one conflict and no warning",
				result.Fetch.Conflicts, result.Fetch.TaskWarnings)
		}
		conflict := result.Fetch.Conflicts[0]
		if conflict.Type != core.ConflictDescription || conflict.Description == nil {
			t.Fatalf("conflict = %+v, want a description conflict", conflict)
		}
		want := core.DescriptionConflict{Base: base, Ours: ours, Theirs: theirs}
		if *conflict.Description != want {
			t.Fatalf("conflict = %+v, want %+v", *conflict.Description, want)
		}
		encoded, err := json.Marshal(conflict.Description)
		if err != nil {
			t.Fatal(err)
		}
		golden := `{"base":"line 1\nline 2\nline 3\nline 4\nline 5\nline 6\n",` +
			`"ours":"line 1\nline 2\nsecond's line 3\nline 4\nline 5\nline 6\n",` +
			`"theirs":"line 1\nline 2\nfirst's line 3\nline 4\nline 5\nline 6\n"}`
		if string(encoded) != golden {
			t.Fatalf("conflict JSON = %s, want %s", encoded, golden)
		}
		detail := "the description changed here and on origin; reapply the wanted text"
		if core.ConflictDetail(conflict) != detail || syncOutcome(t, result.Fetch, task.ID).Detail != detail {
			t.Fatalf("detail = %q, want %q", syncOutcome(t, result.Fetch, task.ID).Detail, detail)
		}
		if got := refValue(t, second, taskRefPrefix+task.ID); got != origin {
			t.Fatalf("tip = %s, want origin's %s", got, origin)
		}
		if got := remoteRefValue(t, second, taskRefPrefix+task.ID); got != origin {
			t.Fatalf("origin moved to %s, want %s", got, origin)
		}
		if got := refValue(t, second, reconciledRefPrefix+task.ID+"/0"); got != local {
			t.Fatalf("parked = %s, want the local edit %s", got, local)
		}
		assertNoMergeOnHistory(t, second, task.ID)
	})
}

// Review Focus 1: the ancestor of each replayed commit is its own parent's
// text, so a second local edit is not compared with the shared base.
func TestTwoLocalDescriptionEditsReplayOntoAnUnrelatedChange(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		task := sharedDescribedTask(t, first, second, config, "Edited twice", "Base text")
		setSyncTaskPriority(t, first, config, task.ID, core.PriorityHigh)
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, "First draft")
		setSyncTaskDescription(t, second, config, task.ID, "Second draft")

		result, err := second.Sync(context.Background(), config)
		if err != nil {
			t.Fatalf("Sync() error = %v; conflicts = %+v", err, result.Fetch.Conflicts)
		}
		assertSyncOutcome(t, result.Fetch, task.ID, SyncReconciled)
		if len(result.Fetch.Conflicts) != 0 || len(result.Fetch.TaskWarnings) != 0 {
			t.Fatalf("conflicts = %+v, warnings = %+v; want neither", result.Fetch.Conflicts, result.Fetch.TaskWarnings)
		}
		tip := currentSnapshot(t, second, config, task.ID)
		if tip.State.Task.Description != "Second draft" || tip.State.Task.Priority != core.PriorityHigh {
			t.Fatalf("task = %q / %q, want the second draft and origin's priority",
				tip.State.Task.Description, tip.State.Task.Priority)
		}
		assertNoMergeOnHistory(t, second, task.ID)
	})
}

func TestSecondLocalEditAfterAMergeMerges(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		base := sixLines(nil)
		task := sharedDescribedTask(t, first, second, config, "Two local edits", base)
		theirs := sixLines(map[int]string{2: "first's line 2"})
		setSyncTaskDescription(t, first, config, task.ID, theirs)
		syncClean(t, first, config)
		firstDraft := sixLines(map[int]string{5: "second's line 5"})
		setSyncTaskDescription(t, second, config, task.ID, firstDraft)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "second's line 5", 6: "second's line 6"}))

		result := syncClean(t, second, config)
		assertDescriptionMergedWarnings(t, result.Fetch, task.ID, 2)
		tip := currentSnapshot(t, second, config, task.ID)
		before := currentSnapshotAt(t, second, config, task.ID, syncGit(t, second.Root, "rev-parse", tip.Head+"^"))
		onFirst := sixLines(map[int]string{2: "first's line 2", 5: "second's line 5"})
		for _, step := range []struct {
			name                      string
			snapshot                  core.Snapshot
			ancestor, current, merged string
		}{
			{name: "first local edit", snapshot: before, ancestor: base, current: theirs, merged: onFirst},
			{name: "second local edit", snapshot: tip, ancestor: firstDraft, current: onFirst,
				merged: sixLines(map[int]string{2: "first's line 2", 5: "second's line 5", 6: "second's line 6"})},
		} {
			merges := mergeOperations(step.snapshot.Operation)
			if len(merges) != 1 || merges[0].Merge.Ancestor != step.ancestor || merges[0].Merge.Current != step.current ||
				merges[0].Value != step.merged {
				t.Fatalf("%s: merges = %+v, want one from %q onto %q giving %q", step.name, merges, step.ancestor,
					step.current, step.merged)
			}
			if step.snapshot.State.Task.Description != step.merged {
				t.Fatalf("%s: description = %q, want %q", step.name, step.snapshot.State.Task.Description, step.merged)
			}
		}
		if got := syncGit(t, second.Root, "rev-parse", before.Head+"^"); got != refValue(t, first, taskRefPrefix+task.ID) {
			t.Fatalf("first replayed commit's parent = %s, want first's tip", got)
		}
	})
}

// Review Focus 2: a merged commit still unpublished is replayed again onto a
// newer edit, and its pack gains a second merge naming the first.
func TestUnpublishedMergeMergesAgainOntoANewerEdit(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		task := sharedDescribedTask(t, first, second, config, "Three editors", sixLines(nil))
		third := openSyncClone(t, syncGit(t, first.Root, "remote", "get-url", "origin"))
		if _, err := third.LoadConfig(); err != nil {
			t.Fatal(err)
		}
		syncClean(t, third, config)

		alice := sixLines(map[int]string{2: "alice's line 2"})
		setSyncTaskDescription(t, first, config, task.ID, alice)
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "bob's line 5"}))
		// Bob's fetch merges, and his push is not made.
		fetched, err := second.Fetch(ctx, config)
		if err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		assertDescriptionMergedWarnings(t, fetched, task.ID, 1)
		firstMerge := mergeOperations(currentSnapshot(t, second, config, task.ID).Operation)[0]

		syncClean(t, third, config)
		carol := sixLines(map[int]string{2: "alice's line 2", 3: "carol's line 3"})
		setSyncTaskDescription(t, third, config, task.ID, carol)
		syncClean(t, third, config)

		result := syncClean(t, second, config)
		assertSyncOutcome(t, result.Fetch, task.ID, SyncReconciled)
		assertDescriptionMergedWarnings(t, result.Fetch, task.ID, 1)
		all := sixLines(map[int]string{2: "alice's line 2", 3: "carol's line 3", 5: "bob's line 5"})
		tip := currentSnapshot(t, second, config, task.ID)
		ops := tip.Operation.Operations
		if len(ops) != 3 || ops[0].Type != core.OperationFieldSet || ops[1].ID != firstMerge.ID ||
			ops[2].Type != core.OperationDescriptionMerge {
			t.Fatalf("re-replayed pack = %+v, want [field.set, merge %s, merge]", ops, firstMerge.ID)
		}
		want := core.DescriptionMerge{Other: firstMerge.ID, Ancestor: alice, Current: carol, Method: core.DescriptionLineMergeMethod}
		if *ops[2].Merge != want || ops[2].Value != all {
			t.Fatalf("second merge = %q %+v, want %q %+v", ops[2].Value, *ops[2].Merge, all, want)
		}
		syncClean(t, first, config)
		syncClean(t, third, config)
		for _, repo := range []*Repository{first, second, third} {
			if got := currentSnapshot(t, repo, config, task.ID).State.Task.Description; got != all {
				t.Fatalf("description = %q, want every edit: %q", got, all)
			}
		}
	})
}

// Review Focus 3: no generation-10 commit is written for nothing.
func TestIdenticalDescriptionEditsRecordNoMerge(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		for _, row := range []struct {
			name          string
			first, second string
		}{
			{name: "identical edits", first: sixLines(map[int]string{3: "both"}), second: sixLines(map[int]string{3: "both"})},
			{
				name:   "an edit origin already holds",
				first:  sixLines(map[int]string{2: "first's line 2", 5: "agreed line 5"}),
				second: sixLines(map[int]string{5: "agreed line 5"}),
			},
		} {
			task := sharedDescribedTask(t, first, second, config, row.name, sixLines(nil))
			setSyncTaskDescription(t, first, config, task.ID, row.first)
			syncClean(t, first, config)
			setSyncTaskDescription(t, second, config, task.ID, row.second)

			result := syncClean(t, second, config)
			assertSyncOutcome(t, result.Fetch, task.ID, SyncReconciled)
			if len(result.Fetch.TaskWarnings) != 0 || len(result.Fetch.Conflicts) != 0 {
				t.Fatalf("%s: warnings = %+v, conflicts = %+v; want neither", row.name,
					result.Fetch.TaskWarnings, result.Fetch.Conflicts)
			}
			if got, want := refValue(t, second, taskRefPrefix+task.ID), refValue(t, first, taskRefPrefix+task.ID); got != want {
				t.Fatalf("%s: tip = %s, want origin's %s with no replayed commit", row.name, got, want)
			}
			if got := currentSnapshot(t, second, config, task.ID).State.Task.Description; got != row.first {
				t.Fatalf("%s: description = %q, want %q", row.name, got, row.first)
			}
			assertNoMergeOnHistory(t, second, task.ID)
		}
	})
}

// copyWorld copies origin and the second clone to a new directory, so one
// divergence can be settled in two orders.
func copyWorld(t *testing.T, first, second *Repository) *Repository {
	t.Helper()
	bare := syncGit(t, first.Root, "remote", "get-url", "origin")
	root := t.TempDir()
	copiedBare, copiedSecond := filepath.Join(root, "origin.git"), filepath.Join(root, "second")
	for _, pair := range [][2]string{{bare, copiedBare}, {second.Root, copiedSecond}} {
		if err := os.MkdirAll(pair[1], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := testrepo.CopyTree(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	syncGit(t, copiedSecond, "config", "remote.origin.url", copiedBare)
	repo := openSyncCloneAt(t, copiedSecond)
	if _, err := repo.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestDescriptionMergeIsTheSameWhicheverCloneFetchesFirst(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		task := sharedDescribedTask(t, first, second, config, "Fetch order", sixLines(nil))
		setSyncTaskDescription(t, first, config, task.ID, sixLines(map[int]string{2: "first's line 2"}))
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "second's line 5"}))

		type outcome struct {
			description, state, operation string
			ids                           []string
		}
		results := map[string]outcome{}
		worlds := map[string]*Repository{"third fetches first": second, "merge first": copyWorld(t, first, second)}
		for _, order := range []string{"third fetches first", "merge first"} {
			merging := worlds[order]
			bare := syncGit(t, merging.Root, "remote", "get-url", "origin")
			var third *Repository
			openThird := func() {
				third = openSyncClone(t, bare)
				if _, err := third.LoadConfig(); err != nil {
					t.Fatal(err)
				}
			}
			if order == "third fetches first" {
				openThird()
				syncClean(t, third, config)
			}
			syncClean(t, merging, config)
			if third == nil {
				openThird()
			}
			syncClean(t, third, config)
			head := refValue(t, third, taskRefPrefix+task.ID)
			if got := refValue(t, merging, taskRefPrefix+task.ID); got != head {
				t.Fatalf("%s: third holds %s, the merging clone %s", order, head, got)
			}
			snapshot := currentSnapshot(t, third, config, task.ID)
			results[order] = outcome{
				description: snapshot.State.Task.Description,
				state:       syncGit(t, third.Root, "cat-file", "blob", head+":state.json"),
				operation:   syncGit(t, third.Root, "cat-file", "blob", head+":operation.json"),
				ids:         operationIDs(snapshot),
			}
		}
		one, other := results["third fetches first"], results["merge first"]
		if one.description != other.description || one.state != other.state || one.operation != other.operation ||
			!slices.Equal(one.ids, other.ids) {
			t.Fatalf("the two orders differ:\n%+v\n%+v", one, other)
		}
		if one.description != sixLines(map[int]string{2: "first's line 2", 5: "second's line 5"}) {
			t.Fatalf("description = %q, want both edits", one.description)
		}
	})
}

// readTaskTip reads the snapshot at an object ID.
func readTaskTip(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, objectID string) core.Snapshot {
	t.Helper()
	snapshot, err := repo.ReadTaskHead(context.Background(), config, TaskHead{TaskID: taskID, ObjectID: objectID})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRetriedReplayWritesTheSameMerge(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		task := sharedDescribedTask(t, first, second, config, "Retried", sixLines(nil))
		setSyncTaskDescription(t, first, config, task.ID, sixLines(map[int]string{2: "first's line 2"}))
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "second's line 5"}))
		syncGit(t, second.Root, "fetch", "--quiet", "origin", "+"+taskRefPrefix+task.ID+":refs/test/remote")

		local := readTaskTip(t, second, config, task.ID, refValue(t, second, taskRefPrefix+task.ID))
		remote := readTaskTip(t, second, config, task.ID, refValue(t, second, "refs/test/remote"))
		_, graph, err := second.classifyTaskHeadRelationships(ctx, config,
			[]taskHeadPair{{TaskID: task.ID, Local: local, Remote: remote}})
		if err != nil {
			t.Fatal(err)
		}
		request := []reconcileRequest{{TaskID: task.ID, Local: local, Remote: remote}}
		var heads, trees []string
		var merges [][]core.Operation
		for attempt := 0; attempt < 2; attempt++ {
			outcomes, err := second.reconcileDivergentTasks(ctx, config, graph, map[string][]string{}, request)
			if err != nil || outcomes[0].Err != nil || outcomes[0].Conflict != nil {
				t.Fatalf("attempt %d: error = %v, outcome = %+v", attempt, err, outcomes)
			}
			heads = append(heads, outcomes[0].Head)
			trees = append(trees, syncGit(t, second.Root, "rev-parse", outcomes[0].Head+"^{tree}"))
			merges = append(merges, outcomes[0].Merges)
		}
		if trees[0] != trees[1] {
			t.Fatalf("trees = %s and %s, want the same operation and state documents", trees[0], trees[1])
		}
		for _, file := range []string{"operation.json", "state.json"} {
			if one, two := syncGit(t, second.Root, "cat-file", "blob", heads[0]+":"+file),
				syncGit(t, second.Root, "cat-file", "blob", heads[1]+":"+file); one != two {
				t.Fatalf("%s differs between attempts:\n%s\n%s", file, one, two)
			}
		}
		if len(merges[0]) != 1 || len(merges[1]) != 1 || merges[0][0].ID != merges[1][0].ID {
			t.Fatalf("merges = %+v, want the same single merge twice", merges)
		}
	})
}

func TestDescriptionMergeKeepsOtherFieldsOfThePack(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		task := sharedDescribedTask(t, first, second, config, "Several fields", sixLines(nil))
		setSyncTaskDescription(t, first, config, task.ID, sixLines(map[int]string{2: "first's line 2"}))
		syncClean(t, first, config)
		title, description := "Retitled here", sixLines(map[int]string{5: "second's line 5"})
		if _, err := syncService(second, config).UpdateMutation(context.Background(), task.ID,
			core.UpdateInput{Title: &title, Description: &description}); err != nil {
			t.Fatal(err)
		}
		local := currentSnapshot(t, second, config, task.ID)

		result := syncClean(t, second, config)
		assertDescriptionMergedWarnings(t, result.Fetch, task.ID, 1)
		tip := currentSnapshot(t, second, config, task.ID)
		if got, want := tip.Operation.Operations[:len(tip.Operation.Operations)-1], local.Operation.Operations; !slices.Equal(got, want) {
			t.Fatalf("replayed operations = %+v, want the local pack's %+v first", got, want)
		}
		if tip.State.Task.Title != title ||
			tip.State.Task.Description != sixLines(map[int]string{2: "first's line 2", 5: "second's line 5"}) {
			t.Fatalf("task = %q / %q, want the title and the merged description", tip.State.Task.Title, tip.State.Task.Description)
		}
	})
}

func TestDescriptionMergeTooLargeOrTooManyChangesConflicts(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		long := strings.Repeat("x", core.MaxDescriptionBytes/2)
		var many, rewritten strings.Builder
		for line := 0; line < 3000; line++ {
			fmt.Fprintf(&many, "l%d\n", line)
			fmt.Fprintf(&rewritten, "L%d\n", line)
		}
		for _, row := range []struct {
			name               string
			base, theirs, ours string
		}{
			{name: "too large", base: "a\nb\nc\n", theirs: long + "\nb\nc\n", ours: "a\nb\n" + long + "\n"},
			{name: "too many changes", base: many.String(), theirs: rewritten.String(), ours: many.String() + "end\n"},
		} {
			task := sharedDescribedTask(t, first, second, config, row.name, row.base)
			setSyncTaskDescription(t, first, config, task.ID, row.theirs)
			syncClean(t, first, config)
			setSyncTaskDescription(t, second, config, task.ID, row.ours)

			result, err := second.Sync(context.Background(), config)
			if core.CategoryOf(err) != core.CategoryConflict {
				t.Fatalf("%s: Sync() error = %v, want a conflict", row.name, err)
			}
			if len(result.Fetch.Conflicts) != 1 || len(result.Fetch.TaskWarnings) != 0 {
				t.Fatalf("%s: conflicts = %d, warnings = %+v; want one conflict, no warning", row.name,
					len(result.Fetch.Conflicts), result.Fetch.TaskWarnings)
			}
			conflict := result.Fetch.Conflicts[0]
			want := core.DescriptionConflict{Base: row.base, Ours: row.ours, Theirs: row.theirs}
			if conflict.Type != core.ConflictDescription || conflict.Description == nil || *conflict.Description != want {
				t.Fatalf("%s: conflict is not today's description conflict", row.name)
			}
			if got := syncOutcome(t, result.Fetch, task.ID).Detail; got != core.ConflictDetail(conflict) {
				t.Fatalf("%s: detail = %q", row.name, got)
			}
			assertNoMergeOnHistory(t, second, task.ID)
		}
	})
}

func TestEditOnADeletedTaskStillTombstoneConflict(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		task := sharedDescribedTask(t, first, second, config, "Deleted there", sixLines(nil))
		deleteSyncTask(t, first, config, task.ID)
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "second's line 5"}))
		local := currentSnapshot(t, second, config, task.ID)

		result, err := second.Sync(context.Background(), config)
		if core.CategoryOf(err) != core.CategoryConflict {
			t.Fatalf("Sync() error = %v, want a conflict", err)
		}
		if len(result.Fetch.Conflicts) != 1 || len(result.Fetch.TaskWarnings) != 0 {
			t.Fatalf("conflicts = %+v, warnings = %+v; want one conflict", result.Fetch.Conflicts, result.Fetch.TaskWarnings)
		}
		conflict := result.Fetch.Conflicts[0]
		blocked := local.Operation.Operations[0]
		if conflict.Type != core.ConflictTombstone || conflict.Tombstone == nil ||
			conflict.Tombstone.OperationID != blocked.ID || conflict.Tombstone.Value != blocked.Value {
			t.Fatalf("conflict = %+v, want a tombstone conflict naming %s", conflict, blocked.ID)
		}
		assertNoMergeOnHistory(t, second, task.ID)
	})
}

func TestDescriptionMergesWhenReplayingOntoOriginsCheckpoint(t *testing.T) {
	t.Parallel()
	forBothSyncFormats(t, func(t *testing.T, first, second *Repository, config core.ProjectConfig) {
		base := sixLines(nil)
		task := sharedDescribedTask(t, first, second, config, "Adopted and merged", base)
		theirs := sixLines(map[int]string{2: "first's line 2"})
		setSyncTaskDescription(t, first, config, task.ID, theirs)
		syncClean(t, first, config)
		setSyncTaskDescription(t, second, config, task.ID, sixLines(map[int]string{5: "second's line 5"}))
		compacted := compactLocally(t, first, config, task.ID)
		publishCheckpoint(t, first, config, compacted)

		result := syncClean(t, second, config)
		if item := syncOutcome(t, result.Fetch, task.ID); item.Status != SyncAdoptedReplayed {
			t.Fatalf("fetch outcome = %+v, want %q", item, SyncAdoptedReplayed)
		}
		assertDescriptionMergedWarnings(t, result.Fetch, task.ID, 1)
		adopted := currentSnapshot(t, second, config, task.ID)
		if parent := syncGit(t, second.Root, "rev-parse", adopted.Head+"^"); parent != compacted.checkpoint.Head {
			t.Fatalf("replayed commit's parent = %s, want origin's checkpoint %s", parent, compacted.checkpoint.Head)
		}
		merges := mergeOperations(adopted.Operation)
		if len(merges) != 1 || merges[0].Merge.Ancestor != base || merges[0].Merge.Current != theirs {
			t.Fatalf("merges = %+v, want one from the base onto origin's text", merges)
		}
		if adopted.Operation.HistoryGeneration != compacted.checkpoint.Operation.HistoryGeneration ||
			adopted.Operation.MinReader != core.DescriptionMergeMinReader {
			t.Fatalf("replayed pack generation %s, minReader %d", adopted.Operation.HistoryGeneration, adopted.Operation.MinReader)
		}
		want := sixLines(map[int]string{2: "first's line 2", 5: "second's line 5"})
		if adopted.State.Task.Description != want {
			t.Fatalf("description = %q, want %q", adopted.State.Task.Description, want)
		}
		syncClean(t, first, config)
		if got := currentSnapshot(t, first, config, task.ID).State.Task.Description; got != want {
			t.Fatalf("first clone's description = %q, want %q", got, want)
		}
	})
}
