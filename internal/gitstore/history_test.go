package gitstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadTaskHistoriesReturnsOnlyUnseenDescendantsInRequestOrder(t *testing.T) {
	// Mutation caught: sorting by task ID or including the boundary commit.
	repository, config := writeRepository(t)
	first := writeHistoryForTask(t, repository, config, 100, 3)
	second := writeHistoryForTask(t, repository, config, 200, 3)

	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: second[2].Operation.TaskID, ObjectID: second[2].Head},
			StopAt: second[0].Head,
		},
		{
			Head:   TaskHead{TaskID: first[2].Operation.TaskID, ObjectID: first[2].Head},
			StopAt: first[1].Head,
		},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	assertHistoryResult(
		t,
		results[0],
		second[2].Operation.TaskID,
		second[2].Head,
		true,
		2,
		[]string{second[1].Head, second[2].Head},
	)
	assertHistoryResult(
		t,
		results[1],
		first[2].Operation.TaskID,
		first[2].Head,
		true,
		1,
		[]string{first[2].Head},
	)
	if got, want := results[0].Commits[0].Parents, []string{second[0].Head}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second boundary descendant parents = %#v, want %#v", got, want)
	}
	if got, want := results[1].Commits[0].Parents, []string{first[1].Head}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first boundary descendant parents = %#v, want %#v", got, want)
	}
}

func TestReadTaskHistoriesRestartsAtRootWhenBoundaryIsUnreachable(t *testing.T) {
	// Mutation caught: treating any supplied boundary as reached without proving ancestry.
	repository, config := writeRepository(t)
	first := writeHistoryForTask(t, repository, config, 300, 3)
	second := writeHistoryForTask(t, repository, config, 400, 2)

	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: first[2].Operation.TaskID, ObjectID: first[2].Head},
			StopAt: second[0].Head,
		},
		{
			Head:   TaskHead{TaskID: second[1].Operation.TaskID, ObjectID: second[1].Head},
			StopAt: first[1].Head,
		},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	assertHistoryResult(
		t,
		results[0],
		first[2].Operation.TaskID,
		first[2].Head,
		false,
		3,
		[]string{first[0].Head, first[1].Head, first[2].Head},
	)
	assertHistoryResult(
		t,
		results[1],
		second[1].Operation.TaskID,
		second[1].Head,
		false,
		2,
		[]string{second[0].Head, second[1].Head},
	)
}

func TestReadTaskHistoriesAttributesMalformedCheckpointAndContinuesOtherTasks(t *testing.T) {
	// Mutation caught: returning one buried document failure as a shared batch error.
	repository, config := writeRepository(t)
	malformedHistory := writeHistoryForTask(t, repository, config, 500, 3)
	validHistory := writeHistoryForTask(t, repository, config, 600, 2)

	operationBlob := gitOutput(t, repository, "rev-parse", malformedHistory[1].Head+":operation.json")
	malformedStateBlob := gitOutputWithInput(
		t,
		repository,
		[]byte("{not canonical JSON}\n"),
		"hash-object", "-w", "--stdin",
	)
	malformedTree := gitOutputWithInput(
		t,
		repository,
		[]byte(fmt.Sprintf(
			"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
			operationBlob,
			malformedStateBlob,
		)),
		"mktree",
	)
	malformedCommit := gitOutput(
		t,
		repository,
		"commit-tree", malformedTree,
		"-p", malformedHistory[0].Head,
		"-m", "malformed checkpoint",
	)
	headTree := gitOutput(t, repository, "rev-parse", malformedHistory[2].Head+"^{tree}")
	head := gitOutput(
		t,
		repository,
		"commit-tree", headTree,
		"-p", malformedCommit,
		"-m", "valid descendant",
	)
	gitOutput(
		t,
		repository,
		"update-ref",
		taskRef(malformedHistory[0].Operation.TaskID),
		head,
		malformedHistory[2].Head,
	)

	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: malformedHistory[0].Operation.TaskID, ObjectID: head}},
		{Head: TaskHead{TaskID: validHistory[1].Operation.TaskID, ObjectID: validHistory[1].Head}},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	assertHistoryResult(
		t,
		results[0],
		malformedHistory[0].Operation.TaskID,
		head,
		false,
		2,
		[]string{malformedHistory[0].Head},
	)
	if results[0].Failure == nil {
		t.Fatal("malformed result failure = nil, want attributed checkpoint failure")
	}
	if got, want := results[0].Failure.TaskID, malformedHistory[0].Operation.TaskID; got != want {
		t.Fatalf("failure task ID = %q, want %q", got, want)
	}
	if got, want := results[0].Failure.Commit, malformedCommit; got != want {
		t.Fatalf("failure commit = %q, want %q", got, want)
	}
	if got, want := core.CategoryOf(results[0].Failure.Err), core.CategoryCorruptData; got != want {
		t.Fatalf("failure category = %q, want %q; error = %v", got, want, results[0].Failure.Err)
	}
	assertHistoryResult(
		t,
		results[1],
		validHistory[1].Operation.TaskID,
		validHistory[1].Head,
		false,
		2,
		[]string{validHistory[0].Head, validHistory[1].Head},
	)
	if got := gitOutput(t, repository, "rev-parse", taskRef(malformedHistory[0].Operation.TaskID)); got != head {
		t.Fatalf("task ref = %q, want validation to leave it at %q", got, head)
	}
}

func TestReadTaskHistoriesUsesConstantBatchedGitCommands(t *testing.T) {
	// Mutation caught: inspecting commits or task histories with per-object Git processes.
	for _, operationCount := range []int{4, 7} {
		t.Run(fmt.Sprintf("%d_operations", operationCount), func(t *testing.T) {
			repository, config := writeRepository(t)
			requests := make([]TaskHistoryRequest, 0, 10)
			for task := 0; task < 10; task++ {
				history := writeHistoryForTask(t, repository, config, 1000+task*20, operationCount)
				head := history[len(history)-1]
				requests = append(requests, TaskHistoryRequest{
					Head: TaskHead{TaskID: head.Operation.TaskID, ObjectID: head.Head},
				})
			}

			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			results, err := repository.ReadTaskHistories(context.Background(), config, requests)
			if err != nil {
				t.Fatalf("ReadTaskHistories() error = %v", err)
			}
			if len(results) != 10 {
				t.Fatalf("results = %d, want 10", len(results))
			}
			for i, result := range results {
				if result.Failure != nil {
					t.Fatalf("results[%d] failure = %v", i, result.Failure.Err)
				}
				if got := len(result.Commits); got != operationCount {
					t.Fatalf("results[%d] commits = %d, want %d", i, got, operationCount)
				}
			}

			if got := countCommand(commands, "cat-file", "--batch"); got != 2 {
				t.Fatalf("cat-file --batch commands = %d, want 2; commands = %v", got, commands)
			}
			wantGraphCommand := []string{"rev-list", "--reverse", "--topo-order", "--parents", "--stdin"}
			if got := countCommand(commands, wantGraphCommand...); got != 1 {
				t.Fatalf("history graph commands = %d, want 1; commands = %v", got, commands)
			}
			for _, command := range commands {
				switch {
				case commandHasPrefix(command, "cat-file", "-t"):
					t.Fatalf("ReadTaskHistories() used cat-file -t: %v", command)
				case len(command) > 0 && command[0] == "show":
					t.Fatalf("ReadTaskHistories() used show: %v", command)
				case len(command) > 0 && command[0] == "ls-tree":
					t.Fatalf("ReadTaskHistories() used ls-tree: %v", command)
				case len(command) > 0 && command[0] == "rev-list" &&
					!reflect.DeepEqual(command, wantGraphCommand):
					t.Fatalf("ReadTaskHistories() used an extra or per-task rev-list: %v", command)
				case len(command) > 0 && command[0] == "update-ref":
					t.Fatalf("ReadTaskHistories() mutated refs: %v", command)
				}
			}
		})
	}
}

func TestReadTaskHistoriesUsesFixedTransportWhenAllTipsFail(t *testing.T) {
	// Mutation caught: returning before the empty graph and candidate batch when every tip is invalid.
	repository, config := writeRepository(t)
	first := writeHistoryForTask(t, repository, config, 1800, 1)
	second := writeHistoryForTask(t, repository, config, 1900, 1)
	if _, err := repository.ListTaskHeads(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	blob := gitOutputWithInput(t, repository, []byte("not a commit"), "hash-object", "-w", "--stdin")

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{Head: TaskHead{TaskID: first[0].Operation.TaskID, ObjectID: blob}},
		{Head: TaskHead{TaskID: second[0].Operation.TaskID, ObjectID: blob}},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for i, result := range results {
		if result.Failure == nil || result.CheckedCommits != 1 {
			t.Fatalf("results[%d] = %#v, want one checked commit and an attributed failure", i, result)
		}
	}
	assertFixedHistoryTransportCommands(t, commands)
}

func TestReadTaskHistoriesUsesFixedTransportWhenBoundariesEqualHeads(t *testing.T) {
	// Mutation caught: skipping the empty candidate batch when every boundary equals its head.
	repository, config := writeRepository(t)
	first := writeHistoryForTask(t, repository, config, 2400, 2)
	second := writeHistoryForTask(t, repository, config, 2500, 2)

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: first[1].Operation.TaskID, ObjectID: first[1].Head},
			StopAt: first[1].Head,
		},
		{
			Head:   TaskHead{TaskID: second[1].Operation.TaskID, ObjectID: second[1].Head},
			StopAt: second[1].Head,
		},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for i, result := range results {
		if !result.BoundaryReached || result.CheckedCommits != 0 ||
			len(result.Commits) != 0 || result.Failure != nil {
			t.Fatalf("results[%d] = %#v, want reached head boundary with no candidates", i, result)
		}
	}
	assertFixedHistoryTransportCommands(t, commands)
}

func TestReadTaskHistoriesSupportsSHA256ObjectIDs(t *testing.T) {
	// Mutation caught: assuming every full Git object ID is exactly 40 hexadecimal characters.
	repository, config := writeRepositoryWithObjectFormat(t, "sha256")
	first := writeHistoryForTask(t, repository, config, 2000, 3)
	second := writeHistoryForTask(t, repository, config, 2100, 2)

	results, err := repository.ReadTaskHistories(context.Background(), config, []TaskHistoryRequest{
		{
			Head:   TaskHead{TaskID: second[1].Operation.TaskID, ObjectID: second[1].Head},
			StopAt: second[0].Head,
		},
		{Head: TaskHead{TaskID: first[2].Operation.TaskID, ObjectID: first[2].Head}},
	})
	if err != nil {
		t.Fatalf("ReadTaskHistories() error = %v", err)
	}
	if len(first[0].Head) != 64 || len(second[0].Head) != 64 {
		t.Fatalf("SHA-256 object ID lengths = %d and %d, want 64", len(first[0].Head), len(second[0].Head))
	}
	assertHistoryResult(
		t,
		results[0],
		second[1].Operation.TaskID,
		second[1].Head,
		true,
		1,
		[]string{second[1].Head},
	)
	assertHistoryResult(
		t,
		results[1],
		first[2].Operation.TaskID,
		first[2].Head,
		false,
		3,
		[]string{first[0].Head, first[1].Head, first[2].Head},
	)
}

func TestReadTaskHistoriesRejectsInvalidRequestsBeforeTransport(t *testing.T) {
	// Mutation caught: allowing duplicate tasks or abbreviated IDs into shared Git transport.
	repository, config := writeRepository(t)
	history := writeHistoryForTask(t, repository, config, 2200, 1)
	valid := TaskHistoryRequest{
		Head: TaskHead{TaskID: history[0].Operation.TaskID, ObjectID: history[0].Head},
	}
	tests := []struct {
		name     string
		requests []TaskHistoryRequest
	}{
		{name: "duplicate task ID", requests: []TaskHistoryRequest{valid, valid}},
		{name: "noncanonical task ID", requests: []TaskHistoryRequest{{
			Head: TaskHead{TaskID: strings.ToLower(valid.Head.TaskID), ObjectID: valid.Head.ObjectID},
		}}},
		{name: "abbreviated head", requests: []TaskHistoryRequest{{
			Head: TaskHead{TaskID: valid.Head.TaskID, ObjectID: valid.Head.ObjectID[:len(valid.Head.ObjectID)-2]},
		}}},
		{name: "abbreviated boundary", requests: []TaskHistoryRequest{{
			Head:   valid.Head,
			StopAt: valid.Head.ObjectID[:len(valid.Head.ObjectID)-2],
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			_, err := repository.ReadTaskHistories(context.Background(), config, test.requests)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("ReadTaskHistories() category = %q, want %q; error = %v", got, want, err)
			}
			if got := countCommand(commands, "cat-file", "--batch"); got != 0 {
				t.Fatalf("ReadTaskHistories() cat-file batches = %d, want none for invalid requests; commands = %v", got, commands)
			}
			for _, command := range commands {
				if len(command) > 0 && command[0] == "rev-list" {
					t.Fatalf("ReadTaskHistories() transported an invalid request with %v", command)
				}
			}
		})
	}
}

func assertFixedHistoryTransportCommands(t *testing.T, commands [][]string) {
	t.Helper()
	if got := countCommand(commands, "cat-file", "--batch"); got != 2 {
		t.Fatalf("cat-file --batch commands = %d, want 2; commands = %v", got, commands)
	}
	wantGraphCommand := []string{"rev-list", "--reverse", "--topo-order", "--parents", "--stdin"}
	if got := countCommand(commands, wantGraphCommand...); got != 1 {
		t.Fatalf("history graph commands = %d, want 1; commands = %v", got, commands)
	}
}

func writeHistoryForTask(
	t *testing.T,
	repository *Repository,
	config core.ProjectConfig,
	idBase int,
	operationCount int,
) []core.Snapshot {
	t.Helper()
	if operationCount < 1 {
		t.Fatalf("operation count = %d, want at least 1", operationCount)
	}
	taskID := config.Key + "-" + historyTestULID(idBase)
	generationID := historyTestULID(idBase + 1)
	root, _, state := writeRootForTask(
		t,
		repository,
		config,
		taskID,
		generationID,
		historyTestULID(idBase+2),
		fmt.Sprintf("History task %d", idBase),
	)
	history := []core.Snapshot{root}
	for operation := 1; operation < operationCount; operation++ {
		pack := writeAddLabelPack(
			uint64(operation+1),
			historyTestULID(idBase+2+operation),
			fmt.Sprintf("history-%d-%d", idBase, operation),
		)
		pack.TaskID = taskID
		pack.HistoryGeneration = generationID
		nextState := writeState(t, &state, pack)
		parent := history[len(history)-1]
		next, err := repository.Write(
			context.Background(),
			config,
			&parent,
			pack,
			nextState,
			fmt.Sprintf("advance history %d", operation),
		)
		if err != nil {
			t.Fatalf("Write(history operation %d) error = %v", operation, err)
		}
		history = append(history, next)
		state = nextState
	}
	return history
}

func historyTestULID(sequence int) string {
	return fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05X", sequence)
}

func assertHistoryResult(
	t *testing.T,
	result TaskHistoryResult,
	wantTaskID string,
	wantHead string,
	wantBoundary bool,
	wantChecked int,
	wantCommitIDs []string,
) {
	t.Helper()
	if result.TaskID != wantTaskID ||
		result.Head != wantHead ||
		result.BoundaryReached != wantBoundary ||
		result.CheckedCommits != wantChecked {
		t.Fatalf(
			"result metadata = (%q, %q, %t, %d), want (%q, %q, %t, %d)",
			result.TaskID,
			result.Head,
			result.BoundaryReached,
			result.CheckedCommits,
			wantTaskID,
			wantHead,
			wantBoundary,
			wantChecked,
		)
	}
	gotCommitIDs := make([]string, len(result.Commits))
	for i, commit := range result.Commits {
		gotCommitIDs[i] = commit.ObjectID
	}
	if !reflect.DeepEqual(gotCommitIDs, wantCommitIDs) {
		t.Fatalf("commit IDs = %#v, want literal root-to-head sequence %#v", gotCommitIDs, wantCommitIDs)
	}
	if result.Failure == nil && !wantBoundary && len(result.Commits) > 0 {
		for i, commit := range result.Commits {
			wantParents := []string{}
			if i > 0 {
				wantParents = []string{result.Commits[i-1].ObjectID}
			}
			if !reflect.DeepEqual(commit.Parents, wantParents) {
				t.Fatalf("commit %q parents = %#v, want %#v", commit.ObjectID, commit.Parents, wantParents)
			}
		}
	}
}
