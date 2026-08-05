package gitstore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadTaskOperationsReturnsChainOrderedFromTheRoot(t *testing.T) {
	// Mutation caught: returning commits newest first, or losing a pack's
	// parent so a caller cannot tell where a chain joins.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3100, 4)

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: history[3].Operation.TaskID, ObjectID: history[3].Head}},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	result := results[0]
	if result.BoundaryReached {
		t.Fatal("BoundaryReached = true, want false for a request with no boundary")
	}
	if len(result.Commits) != 4 {
		t.Fatalf("commits = %d, want the whole four-commit chain", len(result.Commits))
	}
	for index, commit := range result.Commits {
		if commit.ObjectID != history[index].Head {
			t.Fatalf("commit %d = %q, want %q", index, commit.ObjectID, history[index].Head)
		}
		if commit.Operation.LogicalClock != uint64(index+1) {
			t.Fatalf("commit %d clock = %d, want %d", index, commit.Operation.LogicalClock, index+1)
		}
		wantParent := ""
		if index > 0 {
			wantParent = history[index-1].Head
		}
		if commit.Parent != wantParent {
			t.Fatalf("commit %d parent = %q, want %q", index, commit.Parent, wantParent)
		}
	}
}

func TestReadTaskOperationsStopsAtTheProjectedBoundary(t *testing.T) {
	// Mutation caught: rereading a whole history on every refresh, or including
	// the boundary commit a caller already projected.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3200, 5)

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: history[4].Operation.TaskID, ObjectID: history[4].Head},
			StopAt: history[2].Head,
		},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if !results[0].BoundaryReached {
		t.Fatal("BoundaryReached = false, want true for a reachable boundary")
	}
	got := commitIDsOf(results[0])
	want := []string{history[3].Head, history[4].Head}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("commits = %#v, want only the unprojected descendants %#v", got, want)
	}
}

func TestReadTaskOperationsRestartsAtTheRootWhenTheBoundaryIsUnreachable(t *testing.T) {
	// Mutation caught: reporting a reconciled task as an ordinary advance, which
	// would append onto rows the replay may have orphaned.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3300, 3)
	unrelated := writeHistoryForTask(t, repository, config, 3400, 2)

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: history[2].Operation.TaskID, ObjectID: history[2].Head},
			StopAt: unrelated[1].Head,
		},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if results[0].BoundaryReached {
		t.Fatal("BoundaryReached = true, want false so the caller reprojects from the root")
	}
	if len(results[0].Commits) != 3 {
		t.Fatalf("commits = %d, want the whole chain restarted at the root", len(results[0].Commits))
	}
}

func TestReadTaskOperationsReportsAnAbsentCommitAsNotFound(t *testing.T) {
	// Mutation caught: treating a retired pre-replay tip as repository
	// corruption instead of an ordinary not-found for that argument.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3500, 2)
	absent := strings.Repeat("0", len(history[0].Head))

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: history[0].Operation.TaskID, ObjectID: absent}},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if len(results[0].Commits) != 0 {
		t.Fatalf("commits = %d, want none for an absent commit", len(results[0].Commits))
	}
	if results[0].Truncated == nil {
		t.Fatal("Truncated = nil, want the absent commit named")
	}
	if got, want := core.CategoryOf(results[0].Truncated.Err), core.CategoryNotFound; got != want {
		t.Fatalf("absent commit category = %q, want %q", got, want)
	}
}

func TestReadTaskOperationsReadsAParkedPreReplayTip(t *testing.T) {
	// Mutation caught: gating the read on the canonical task ref, which would
	// hide exactly the commits a comparison against replaced work needs.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3600, 3)
	taskID := history[2].Operation.TaskID
	parked := reconciledRefPrefix + taskID + "/0"
	if _, err := repository.Git(context.Background(), nil, "update-ref", parked, history[2].Head); err != nil {
		t.Fatalf("park pre-replay tip: %v", err)
	}
	if _, err := repository.Git(context.Background(), nil, "update-ref", taskRef(taskID), history[0].Head); err != nil {
		t.Fatalf("roll canonical ref back: %v", err)
	}

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: taskID, ObjectID: history[2].Head}},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if len(results[0].Commits) != 3 || results[0].Truncated != nil {
		t.Fatalf("parked chain = %d commits, truncation %#v; want the whole chain", len(results[0].Commits), results[0].Truncated)
	}
}

func TestReadTaskOperationsTruncatesSoftlyAtAnUnreadableCommit(t *testing.T) {
	// Mutation caught: failing a whole read because one commit is unreadable,
	// when the valid prefix plus a named boundary is what a reader needs.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3700, 3)
	taskID := history[2].Operation.TaskID

	malformedBlob := gitOutputWithInput(t, repository, []byte("{not canonical}\n"), "hash-object", "-w", "--stdin")
	stateBlob := gitOutput(t, repository, "rev-parse", history[1].Head+":state.json")
	malformedTree := gitOutputWithInput(
		t,
		repository,
		[]byte(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", malformedBlob, stateBlob)),
		"mktree",
	)
	malformedCommit := gitOutput(t, repository, "commit-tree", malformedTree, "-p", history[0].Head, "-m", "malformed")
	rewrittenTip := gitOutput(
		t,
		repository,
		"commit-tree", gitOutput(t, repository, "rev-parse", history[2].Head+"^{tree}"),
		"-p", malformedCommit, "-m", "tip",
	)
	if _, err := repository.Git(context.Background(), nil, "update-ref", taskRef(taskID), rewrittenTip); err != nil {
		t.Fatalf("update task ref: %v", err)
	}

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: taskID, ObjectID: rewrittenTip}},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if got := commitIDsOf(results[0]); len(got) != 1 || got[0] != history[0].Head {
		t.Fatalf("commits = %#v, want the valid root-only prefix", got)
	}
	if results[0].Truncated == nil || results[0].Truncated.Commit != malformedCommit {
		t.Fatalf("truncation = %#v, want the malformed commit named", results[0].Truncated)
	}
}

func TestReadTaskOperationsDoesNotRevalidateStoredCheckpoints(t *testing.T) {
	// Mutation caught: revalidating every commit on an ordinary read, which
	// makes a read scale with history depth and buys nothing the write path,
	// setup, sync, and fetch have not already checked.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 3800, 3)
	taskID := history[2].Operation.TaskID

	operationBlob := gitOutput(t, repository, "rev-parse", history[1].Head+":operation.json")
	wrongStateBlob := gitOutput(t, repository, "rev-parse", history[0].Head+":state.json")
	mismatchedTree := gitOutputWithInput(
		t,
		repository,
		[]byte(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, wrongStateBlob)),
		"mktree",
	)
	mismatched := gitOutput(t, repository, "commit-tree", mismatchedTree, "-p", history[0].Head, "-m", "stale checkpoint")
	if _, err := repository.Git(context.Background(), nil, "update-ref", taskRef(taskID), mismatched); err != nil {
		t.Fatalf("update task ref: %v", err)
	}

	results, err := repository.ReadTaskOperations(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: taskID, ObjectID: mismatched}},
	})
	if err != nil {
		t.Fatalf("ReadTaskOperations() error = %v", err)
	}
	if results[0].Truncated != nil {
		t.Fatalf("truncation = %#v, want a stored checkpoint left to workbook validate", results[0].Truncated)
	}
	if got := commitIDsOf(results[0]); len(got) != 2 || got[1] != mismatched {
		t.Fatalf("commits = %#v, want the operation chain regardless of stored state", got)
	}
}

func TestReadTaskOperationsUsesConstantBatchedGitCommands(t *testing.T) {
	// Mutation caught: reading one commit or one task per Git process.
	repository, config := writeRepository(t)
	for _, taskCount := range []int{1, 4} {
		t.Run(fmt.Sprintf("%d tasks", taskCount), func(t *testing.T) {
			requests := make([]TaskHistoryRequest, 0, taskCount)
			for task := range taskCount {
				history := writeHistoryForTask(t, repository, config, 4000+taskCount*100+task*10, 3)
				requests = append(requests, TaskHistoryRequest{
					Head: TaskHead{TaskID: history[2].Operation.TaskID, ObjectID: history[2].Head},
				})
			}

			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			defer func() { repository.commandObserver = nil }()
			if _, err := repository.ReadTaskOperations(context.Background(), config, requests); err != nil {
				t.Fatalf("ReadTaskOperations() error = %v", err)
			}
			if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
				t.Fatalf("cat-file --batch commands = %d, want 1; commands = %v", got, commands)
			}
			if got := countCommand(commands, "cat-file", "--batch-check"); got != 1 {
				t.Fatalf("cat-file --batch-check commands = %d, want 1; commands = %v", got, commands)
			}
			if got := countCommand(commands, "rev-list", "--reverse", "--topo-order", "--parents", "--stdin"); got != 1 {
				t.Fatalf("rev-list commands = %d, want 1; commands = %v", got, commands)
			}
			for _, command := range commands {
				if len(command) > 0 && (command[0] == "show" || command[0] == "ls-tree" || command[0] == "update-ref") {
					t.Fatalf("ReadTaskOperations() used %v", command)
				}
			}
		})
	}
}

func TestReadTaskOperationsRejectsInvalidRequestsBeforeTransport(t *testing.T) {
	// Mutation caught: reporting a caller's mistyped commit as corrupt data, or
	// transporting an abbreviated ID into a shared Git process.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 4900, 1)
	valid := TaskHistoryRequest{Head: TaskHead{TaskID: history[0].Operation.TaskID, ObjectID: history[0].Head}}
	tests := []struct {
		name     string
		requests []TaskHistoryRequest
		category core.Category
	}{
		{name: "duplicate task ID", requests: []TaskHistoryRequest{valid, valid}, category: core.CategoryCorruptData},
		{name: "abbreviated head", category: core.CategoryValidation, requests: []TaskHistoryRequest{{
			Head: TaskHead{TaskID: valid.Head.TaskID, ObjectID: valid.Head.ObjectID[:7]},
		}}},
		{name: "abbreviated boundary", category: core.CategoryValidation, requests: []TaskHistoryRequest{{
			Head:   valid.Head,
			StopAt: valid.Head.ObjectID[:7],
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			defer func() { repository.commandObserver = nil }()
			_, err := repository.ReadTaskOperations(context.Background(), config, test.requests)
			if got := core.CategoryOf(err); got != test.category {
				t.Fatalf("category = %q, want %q; error = %v", got, test.category, err)
			}
			for _, command := range commands {
				if len(command) > 0 && (command[0] == "rev-list" || strings.HasPrefix(command[len(command)-1], "--batch")) {
					t.Fatalf("transported an invalid request with %v", command)
				}
			}
		})
	}
}

func commitIDsOf(result TaskOperationsResult) []string {
	ids := make([]string, len(result.Commits))
	for index, commit := range result.Commits {
		ids[index] = commit.ObjectID
	}
	return ids
}
