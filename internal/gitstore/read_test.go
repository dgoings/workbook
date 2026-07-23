package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestGetReadsCanonicalTipWithoutReplayingParents(t *testing.T) {
	repo, config := writeRepository(t)
	created, _, _ := writeRoot(t, repo, config)
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	state := writeState(t, &created.State, pack)
	updated, err := repo.Write(context.Background(), config, &created, pack, state, "mark ready")
	if err != nil {
		t.Fatalf("Write(update) error = %v", err)
	}

	// Replace the valid parent with an invalid commit while retaining the valid
	// tip documents. Direct reads must validate only the current snapshot.
	invalidTree := gitOutput(t, repo, "mktree")
	invalidParent := gitOutput(t, repo, "commit-tree", invalidTree, "-m", "invalid parent")
	tipTree := gitOutput(t, repo, "rev-parse", updated.Head+"^{tree}")
	tip := gitOutput(t, repo, "commit-tree", tipTree, "-p", invalidParent, "-m", "valid snapshot")
	gitOutput(t, repo, "update-ref", taskRef(pack.TaskID), tip)

	got, err := repo.Get(context.Background(), config, pack.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Head != tip {
		t.Fatalf("Get().Head = %q, want %q", got.Head, tip)
	}
	if !reflect.DeepEqual(got.Operation, pack) {
		t.Fatalf("Get().Operation = %#v, want %#v", got.Operation, pack)
	}
	if !reflect.DeepEqual(got.State, state) {
		t.Fatalf("Get().State = %#v, want %#v", got.State, state)
	}
}

func TestGetRejectsAnnotatedTagTarget(t *testing.T) {
	repo, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repo, config)
	gitOutput(t, repo, "tag", "-a", "workbook-task-tip", "-m", "tagged task tip", snapshot.Head)
	tagObject := gitOutput(t, repo, "rev-parse", "workbook-task-tip")
	gitOutput(t, repo, "update-ref", taskRef(pack.TaskID), tagObject)

	_, err := repo.Get(context.Background(), config, pack.TaskID)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Get() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestGetAndListRejectSymbolicTaskRefs(t *testing.T) {
	reads := []struct {
		name string
		read func(context.Context, *Repository, core.ProjectConfig, string) error
	}{
		{
			name: "Get",
			read: func(ctx context.Context, repo *Repository, config core.ProjectConfig, taskID string) error {
				_, err := repo.Get(ctx, config, taskID)
				return err
			},
		},
		{
			name: "List",
			read: func(ctx context.Context, repo *Repository, config core.ProjectConfig, _ string) error {
				_, err := repo.List(ctx, config)
				return err
			},
		},
	}

	for _, read := range reads {
		t.Run(read.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			snapshot, pack, _ := writeRoot(t, repo, config)
			gitOutput(t, repo, "update-ref", "refs/workbook/symbolic-target", snapshot.Head)
			gitOutput(t, repo, "update-ref", "-d", taskRef(pack.TaskID))
			gitOutput(t, repo, "symbolic-ref", taskRef(pack.TaskID), "refs/workbook/symbolic-target")

			err := read.read(context.Background(), repo, config, pack.TaskID)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("%s() category = %q, want %q; error = %v", read.name, got, want, err)
			}
		})
	}
}

func TestListFindsCanonicalTasksAfterPackingRefs(t *testing.T) {
	repo, config := writeRepository(t)
	first, firstPack, _ := writeRoot(t, repo, config)
	secondPack := writeCreatePack()
	secondPack.TaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C6"
	secondPack.Operations[0].ID = "01K0M6B8A4FTT8C39MXXYTW7C7"
	secondState := writeState(t, nil, secondPack)
	second, err := repo.Write(context.Background(), config, nil, secondPack, secondState, "create second task")
	if err != nil {
		t.Fatalf("Write(second root) error = %v", err)
	}
	gitOutput(t, repo, "update-ref", "refs/workbook/remotes/origin/"+firstPack.TaskID, first.Head)

	assertListedSnapshots(t, repo, config, []core.Snapshot{first, second})
	gitOutput(t, repo, "pack-refs", "--all")
	assertListedSnapshots(t, repo, config, []core.Snapshot{first, second})
}

func TestResolveAcceptsFullIDsAndUnambiguousCaseInsensitivePrefixes(t *testing.T) {
	repo, config := writeRepository(t)
	_, firstPack, _ := writeRoot(t, repo, config)
	secondPack := writeCreatePack()
	secondPack.TaskID = "WB-11K0M6B8A4FTT8C39MXXYTW7C6"
	secondPack.Operations[0].ID = "01K0M6B8A4FTT8C39MXXYTW7C7"
	secondState := writeState(t, nil, secondPack)
	if _, err := repo.Write(context.Background(), config, nil, secondPack, secondState, "create second task"); err != nil {
		t.Fatalf("Write(second root) error = %v", err)
	}

	for _, input := range []string{firstPack.TaskID, strings.ToLower(firstPack.TaskID), "wb-01k0"} {
		got, err := repo.Resolve(context.Background(), config, input)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", input, err)
		}
		if got != firstPack.TaskID {
			t.Fatalf("Resolve(%q) = %q, want %q", input, got, firstPack.TaskID)
		}
	}
}

func TestResolveRejectsUnknownAndAmbiguousPrefixes(t *testing.T) {
	repo, config := writeRepository(t)
	_, _, _ = writeRoot(t, repo, config)
	secondPack := writeCreatePack()
	secondPack.TaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C6"
	secondPack.Operations[0].ID = "01K0M6B8A4FTT8C39MXXYTW7C7"
	secondState := writeState(t, nil, secondPack)
	if _, err := repo.Write(context.Background(), config, nil, secondPack, secondState, "create second task"); err != nil {
		t.Fatalf("Write(second root) error = %v", err)
	}

	for _, test := range []struct {
		input    string
		category core.Category
	}{
		{input: "WB-NOT-FOUND", category: core.CategoryNotFound},
		{input: "wb-01", category: core.CategoryValidation},
	} {
		t.Run(test.input, func(t *testing.T) {
			_, err := repo.Resolve(context.Background(), config, test.input)
			if got := core.CategoryOf(err); got != test.category {
				t.Fatalf("Resolve(%q) category = %q, want %q; error = %v", test.input, got, test.category, err)
			}
		})
	}
}

func TestListRejectsCorruptTaskRefsAndTipDocuments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, repo *Repository, config core.ProjectConfig, snapshot core.Snapshot)
	}{
		{
			name: "nested ref",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				gitOutput(t, repo, "update-ref", "-d", taskRef(writeTaskID))
				gitOutput(t, repo, "update-ref", taskRef(writeTaskID)+"/nested", snapshot.Head)
			},
		},
		{
			name: "invalid task id",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				gitOutput(t, repo, "update-ref", taskRef("not-a-task"), snapshot.Head)
			},
		},
		{
			name: "non commit target",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, _ core.Snapshot) {
				blob := gitOutput(t, repo, "hash-object", "-w", "--stdin")
				gitOutput(t, repo, "update-ref", taskRef(writeTaskID), blob)
			},
		},
		{
			name: "missing tree entry",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				operation := gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json")
				tree := gitOutputWithInput(t, repo, []byte("100644 blob "+operation+"\toperation.json\n"), "mktree")
				gitOutput(t, repo, "update-ref", taskRef(writeTaskID), gitOutput(t, repo, "commit-tree", tree, "-m", "missing state"))
			},
		},
		{
			name: "extra tree entry",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				operation := gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json")
				state := gitOutput(t, repo, "rev-parse", snapshot.Head+":state.json")
				extra := gitOutputWithInput(t, repo, []byte("extra"), "hash-object", "-w", "--stdin")
				tree := gitOutputWithInput(t, repo, []byte(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n100644 blob %s\textra\n", operation, state, extra)), "mktree")
				gitOutput(t, repo, "update-ref", taskRef(writeTaskID), gitOutput(t, repo, "commit-tree", tree, "-m", "extra entry"))
			},
		},
		{
			name: "non canonical operation bytes",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				operation := gitOutputWithInput(t, repo, []byte("{}\n"), "hash-object", "-w", "--stdin")
				replaceTaskTree(t, repo, snapshot, operation, gitOutput(t, repo, "rev-parse", snapshot.Head+":state.json"))
			},
		},
		{
			name: "operation whitespace is corrupt",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				operation := gitOutput(t, repo, "show", snapshot.Head+":operation.json")
				operationBlob := gitOutputWithInput(t, repo, []byte(" "+operation+"\n"), "hash-object", "-w", "--stdin")
				replaceTaskTree(t, repo, snapshot, operationBlob, gitOutput(t, repo, "rev-parse", snapshot.Head+":state.json"))
			},
		},
		{
			name: "state identity mismatch",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				state := snapshot.State
				state.TaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C6"
				stateBlob := writeDocumentBlob(t, repo, state)
				replaceTaskTree(t, repo, snapshot, gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json"), stateBlob)
			},
		},
		{
			name: "foreign project",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				pack := snapshot.Operation
				pack.ProjectID = "01K0M6B8A4FTT8C39MXXYTW7C2"
				replaceTaskTree(t, repo, snapshot, writeDocumentBlob(t, repo, pack), gitOutput(t, repo, "rev-parse", snapshot.Head+":state.json"))
			},
		},
		{
			name: "wrong project key prefix",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				gitOutput(t, repo, "update-ref", "refs/workbook/tasks/OTHER-01K0M6B8A4FTT8C39MXXYTW7C3", snapshot.Head)
			},
		},
		{
			name: "history generation mismatch",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				state := snapshot.State
				state.History.Generation = "01K0M6B8A4FTT8C39MXXYTW7C2"
				replaceTaskTree(t, repo, snapshot, gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json"), writeDocumentBlob(t, repo, state))
			},
		},
		{
			name: "logical clock mismatch",
			mutate: func(t *testing.T, repo *Repository, _ core.ProjectConfig, snapshot core.Snapshot) {
				state := snapshot.State
				state.LogicalClock++
				replaceTaskTree(t, repo, snapshot, gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json"), writeDocumentBlob(t, repo, state))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			snapshot, _, _ := writeRoot(t, repo, config)
			test.mutate(t, repo, config, snapshot)

			_, err := repo.List(context.Background(), config)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("List() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func assertListedSnapshots(t *testing.T, repo *Repository, config core.ProjectConfig, want []core.Snapshot) {
	t.Helper()
	got, err := repo.List(context.Background(), config)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d snapshots, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Head != want[i].Head || got[i].Operation.TaskID != want[i].Operation.TaskID || !reflect.DeepEqual(got[i].State, want[i].State) {
			t.Fatalf("List()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func replaceTaskTree(t *testing.T, repo *Repository, snapshot core.Snapshot, operationBlob, stateBlob string) {
	t.Helper()
	tree := gitOutputWithInput(t, repo, []byte(fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob)), "mktree")
	head := gitOutput(t, repo, "commit-tree", tree, "-m", "corrupt snapshot")
	gitOutput(t, repo, "update-ref", taskRef(snapshot.Operation.TaskID), head)
}

func writeDocumentBlob(t *testing.T, repo *Repository, document any) string {
	t.Helper()
	contents, err := core.EncodeDocument(document)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	return gitOutputWithInput(t, repo, contents, "hash-object", "-w", "--stdin")
}

func gitOutputWithInput(t *testing.T, repo *Repository, input []byte, args ...string) string {
	t.Helper()
	output, err := repo.Git(context.Background(), input, args...)
	if err != nil {
		t.Fatalf("Git(%v) error = %v", args, err)
	}
	return strings.TrimSuffix(string(output), "\n")
}

func TestGetRejectsNonCanonicalStateBytes(t *testing.T) {
	repo, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repo, config)
	operation := gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json")
	state := gitOutput(t, repo, "show", snapshot.Head+":state.json")
	stateBlob := gitOutputWithInput(t, repo, bytes.Replace([]byte(state+"\n"), []byte("\"format\""), []byte("\n\"format\""), 1), "hash-object", "-w", "--stdin")
	replaceTaskTree(t, repo, snapshot, operation, stateBlob)

	_, err := repo.Get(context.Background(), config, writeTaskID)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Get() category = %q, want %q; error = %v", got, want, err)
	}
}
