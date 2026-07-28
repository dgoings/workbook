package gitstore

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

const writeProjectID = "01K0M6B8A4FTT8C39MXXYTW7C1"
const writeTaskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"
const writeGenerationID = "01K0M6B8A4FTT8C39MXXYTW7C3"

var writeCreatedAt = time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)

func TestWriteCreatesRootCommitAndTaskRef(t *testing.T) {
	repo, config := writeRepository(t)
	pack := writeCreatePack()
	state := writeState(t, nil, pack)

	snapshot, err := repo.Write(context.Background(), config, nil, pack, state, "workbook: create WB-01K0M6B8 Create task")
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if snapshot.Head == "" {
		t.Fatal("Write() head is empty")
	}
	if !reflect.DeepEqual(snapshot.Operation, pack) {
		t.Fatalf("Write() operation = %#v, want %#v", snapshot.Operation, pack)
	}
	if !reflect.DeepEqual(snapshot.State, state) {
		t.Fatalf("Write() state = %#v, want %#v", snapshot.State, state)
	}

	ref := taskRef(pack.TaskID)
	if got := gitOutput(t, repo, "rev-parse", ref); got != snapshot.Head {
		t.Fatalf("task ref = %q, want %q", got, snapshot.Head)
	}
	if got := gitOutput(t, repo, "rev-list", "--parents", "-n", "1", snapshot.Head); got != snapshot.Head {
		t.Fatalf("root commit parents = %q, want only %q", got, snapshot.Head)
	}
	assertTaskTree(t, repo, snapshot.Head, pack, state)
	if got, want := gitOutput(t, repo, "show", "-s", "--format=%s", snapshot.Head), "workbook: create WB-01K0M6B8 Create task"; got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
	if got, want := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", ref), "workbook: create WB-01K0M6B8 Create task"; got != want {
		t.Fatalf("reflog message = %q, want %q", got, want)
	}
}

func TestWriteAppendsCommitToCurrentHead(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	state := writeState(t, &createState, pack)
	reason := "workbook: update WB-01K0M6B8 status backlog → ready"

	snapshot, err := repo.Write(context.Background(), config, &created, pack, state, reason)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got, want := gitOutput(t, repo, "rev-list", "--parents", "-n", "1", snapshot.Head), snapshot.Head+" "+created.Head; got != want {
		t.Fatalf("update commit parents = %q, want %q", got, want)
	}
	assertTaskTree(t, repo, snapshot.Head, pack, state)
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != snapshot.Head {
		t.Fatalf("task ref = %q, want %q", got, snapshot.Head)
	}
	if got := gitOutput(t, repo, "show", "-s", "--format=%s", snapshot.Head); got != reason {
		t.Fatalf("commit subject = %q, want %q", got, reason)
	}
	if got := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", taskRef(createPack.TaskID)); got != reason {
		t.Fatalf("reflog message = %q, want %q", got, reason)
	}
}

func TestWriteValidatedUsesFiveGitCommandsToAppendCanonicalTaskCommit(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	head, found, err := repo.InspectTaskHead(ctx, config, createPack.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("InspectTaskHead() found = false, want head %q", created.Head)
	}
	parent, err := repo.ReadTaskHead(ctx, config, head)
	if err != nil {
		t.Fatal(err)
	}
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	state := writeState(t, &createState, pack)

	var commands [][]string
	repo.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	written, err := repo.WriteValidated(ctx, config, &parent, pack, state, "update task")
	if err != nil {
		t.Fatal(err)
	}
	writeCommands := append([][]string(nil), commands...)

	if written.Head == parent.Head {
		t.Fatal("validated write did not advance the task")
	}
	if !reflect.DeepEqual(written.Operation, pack) {
		t.Fatalf("WriteValidated() operation = %#v, want %#v", written.Operation, pack)
	}
	if !reflect.DeepEqual(written.State, state) {
		t.Fatalf("WriteValidated() state = %#v, want %#v", written.State, state)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != written.Head {
		t.Fatalf("task ref = %q, want %q", got, written.Head)
	}
	if got, want := gitOutput(t, repo, "rev-list", "--parents", "-n", "1", written.Head), written.Head+" "+parent.Head; got != want {
		t.Fatalf("validated commit parents = %q, want %q", got, want)
	}
	assertTaskTree(t, repo, written.Head, pack, state)
	if got, want := gitOutput(t, repo, "show", "-s", "--format=%s", written.Head), "update task"; got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
	if got, want := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", taskRef(createPack.TaskID)), "workbook: update task"; got != want {
		t.Fatalf("reflog message = %q, want %q", got, want)
	}
	if got := len(writeCommands); got != 5 {
		t.Fatalf("Git commands = %d, want 5: %#v", got, writeCommands)
	}
	assertCommandSequence(t, writeCommands, []string{
		"hash-object -w --stdin",
		"hash-object -w --stdin",
		"mktree",
		"commit-tree",
		"update-ref",
	})
}

func TestWriteValidatedRejectsAbbreviatedObservedParentBeforeGit(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)
	created, createPack, _ := writeRoot(t, repo, config)
	head, found, err := repo.InspectTaskHead(ctx, config, createPack.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("InspectTaskHead() found = false, want head %q", created.Head)
	}
	parent, err := repo.ReadTaskHead(ctx, config, head)
	if err != nil {
		t.Fatal(err)
	}
	parent.Head = parent.Head[:len(parent.Head)-2]
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	state := writeState(t, &parent.State, pack)
	refBefore := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID))
	objectsBefore := gitOutput(t, repo, "count-objects", "-v")

	var commands [][]string
	repo.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	_, err = repo.WriteValidated(ctx, config, &parent, pack, state, "update task")
	writeCommands := append([][]string(nil), commands...)
	repo.commandObserver = nil

	if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
		t.Fatalf("WriteValidated() category = %q, want %q; error = %v", got, want, err)
	}
	if len(writeCommands) != 0 {
		t.Fatalf("WriteValidated() commands = %#v, want none for abbreviated parent", writeCommands)
	}
	if got := gitOutput(t, repo, "count-objects", "-v"); got != objectsBefore {
		t.Fatalf("WriteValidated() created objects: before %q, after %q", objectsBefore, got)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != refBefore {
		t.Fatalf("task ref after abbreviated parent = %q, want unchanged %q", got, refBefore)
	}
}

func TestWriteValidatedRejectsStaleCASWithoutReplacingConcurrentHead(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)
	_, createPack, _ := writeRoot(t, repo, config)
	head, found, err := repo.InspectTaskHead(ctx, config, createPack.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("InspectTaskHead() found = false, want current task head")
	}
	parent, err := repo.ReadTaskHead(ctx, config, head)
	if err != nil {
		t.Fatal(err)
	}

	concurrentPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	concurrentState := writeState(t, &parent.State, concurrentPack)
	concurrent, err := repo.Write(ctx, config, &parent, concurrentPack, concurrentState, "concurrent update")
	if err != nil {
		t.Fatal(err)
	}

	stalePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C6", "blocked")
	staleState := writeState(t, &parent.State, stalePack)
	_, err = repo.WriteValidated(ctx, config, &parent, stalePack, staleState, "stale update")
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("WriteValidated() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != concurrent.Head {
		t.Fatalf("task ref after stale validated write = %q, want concurrent head %q", got, concurrent.Head)
	}
}

func TestWriteRetainsLegacyReflogPrefix(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	pack.Operations = []core.Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C5", Type: core.OperationTaskTombstone}}
	state := writeState(t, &createState, pack)

	deleted, err := repo.Write(context.Background(), config, &created, pack, state, "delete task")
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got, want := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", taskRef(createPack.TaskID)), "workbook: delete task"; got != want {
		t.Fatalf("reflog message = %q, want %q", got, want)
	}
	if got, want := gitOutput(t, repo, "show", "-s", "--format=%s", deleted.Head), "delete task"; got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
}

func TestServicePersistsGitSafeCreateSubjectFromControlCharacters(t *testing.T) {
	repo, config := writeRepository(t)
	service := testService(repo, config)
	title := "Plan\x00phase\none"

	result, err := service.CreateMutation(context.Background(), core.CreateInput{Title: title})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	task := result.Task
	if task.Title != title {
		t.Fatalf("Create() title = %q, want canonical title unchanged %q", task.Title, title)
	}

	want := "workbook: create WB-01K0M6B8 Plan phase one"
	if got := gitOutput(t, repo, "show", "-s", "--format=%s", task.Head); got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
	if got := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", taskRef(task.ID)); got != want {
		t.Fatalf("reflog message = %q, want %q", got, want)
	}
}

func TestServicePersistsGitSafeUpdateSubjectFromControlCharacters(t *testing.T) {
	repo, config := writeRepository(t)
	service := testService(repo, config)
	createResult, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "Control labels"})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	task := createResult.Task
	labels := []string{"alpha\x00beta", "line\nbreak"}

	updateResult, err := service.UpdateMutation(context.Background(), task.ID, core.UpdateInput{Labels: &labels})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	updated := updateResult.Task
	if !reflect.DeepEqual(updated.Labels, labels) {
		t.Fatalf("Update() labels = %#v, want canonical labels unchanged %#v", updated.Labels, labels)
	}

	want := "workbook: update WB-01K0M6B8 labels +alpha beta,line break"
	if got := gitOutput(t, repo, "show", "-s", "--format=%s", updated.Head); got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
	if got := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", taskRef(task.ID)); got != want {
		t.Fatalf("reflog message = %q, want %q", got, want)
	}
}

func TestWriteRejectsStaleHeadWithoutMovingTaskRef(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	firstPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	firstState := writeState(t, &createState, firstPack)
	current, err := repo.Write(context.Background(), config, &created, firstPack, firstState, "mark ready")
	if err != nil {
		t.Fatalf("Write(first update) error = %v", err)
	}

	stalePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C6", "blocked")
	staleState := writeState(t, &createState, stalePack)
	_, err = repo.Write(context.Background(), config, &created, stalePack, staleState, "mark blocked")
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("Write(stale) category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != current.Head {
		t.Fatalf("task ref after stale write = %q, want %q", got, current.Head)
	}
}

func TestWriteNeverDereferencesSymbolicTaskRef(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	targetRef := "refs/workbook/symbolic-target"
	gitOutput(t, repo, "update-ref", targetRef, created.Head)
	gitOutput(t, repo, "update-ref", "-d", taskRef(createPack.TaskID))
	gitOutput(t, repo, "symbolic-ref", taskRef(createPack.TaskID), targetRef)

	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	state := writeState(t, &createState, pack)
	_, err := repo.Write(context.Background(), config, &created, pack, state, "mark ready")
	category := core.CategoryOf(err)
	if category != core.CategoryStaleWrite && category != core.CategoryCorruptData {
		t.Fatalf("Write() category = %q, want stale-write or corrupt-data; error = %v", category, err)
	}
	if got := gitOutput(t, repo, "rev-parse", targetRef); got != created.Head {
		t.Fatalf("symbolic target moved to %q, want unchanged %q", got, created.Head)
	}
	if got := gitOutput(t, repo, "symbolic-ref", taskRef(createPack.TaskID)); got != targetRef {
		t.Fatalf("task ref stopped pointing to %q: got %q", targetRef, got)
	}
}

func TestGitStoreRejectsCallerSuppliedForeignProjectConfig(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, repo *Repository, config core.ProjectConfig) error
	}{
		{
			name: "List",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				_, err := repo.List(context.Background(), config)
				return err
			},
		},
		{
			name: "Get",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				_, err := repo.Get(context.Background(), config, writeTaskID)
				return err
			},
		},
		{
			name: "Write",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				pack := writeCreatePack()
				pack.ProjectID = config.ProjectID
				state := writeState(t, nil, pack)
				_, err := repo.Write(context.Background(), config, nil, pack, state, "foreign task")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			foreign := config
			foreign.ProjectID = "01K0M6B8A4FTT8C39MXXYTW7C9"

			err := test.call(t, repo, foreign)
			if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
				t.Fatalf("%s() category = %q, want %q; error = %v", test.name, got, want, err)
			}
			assertNoTaskRefs(t, repo)
		})
	}
}

func TestConflictingLinkedWorktreeConfigCannotAccessSharedTaskRefs(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, repo *Repository, config core.ProjectConfig) error
	}{
		{
			name: "CRUD create",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				service := testService(repo, config)
				_, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "Foreign task"})
				return err
			},
		},
		{
			name: "List",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				_, err := repo.List(context.Background(), config)
				return err
			},
		},
		{
			name: "Get",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				_, err := repo.Get(context.Background(), config, writeTaskID)
				return err
			},
		},
		{
			name: "Write",
			call: func(t *testing.T, repo *Repository, config core.ProjectConfig) error {
				pack := writeCreatePack()
				pack.ProjectID = config.ProjectID
				state := writeState(t, nil, pack)
				_, err := repo.Write(context.Background(), config, nil, pack, state, "foreign task")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guarded, linked, original, foreign := conflictingLinkedWorktrees(t)

			err := test.call(t, linked, foreign)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("%s() category = %q, want %q; error = %v", test.name, got, want, err)
			}
			assertNoTaskRefs(t, guarded)
			assertProjectConfigFile(t, filepath.Join(guarded.CommonGitDir, "workbook", projectGuard), original)

			if test.name == "CRUD create" {
				service := testService(guarded, original)
				result, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "Guarded task"})
				if err != nil {
					t.Fatalf("CreateMutation(original identity) error = %v", err)
				}
				task := result.Task
				listed, err := service.List(context.Background(), core.ListFilter{})
				if err != nil {
					t.Fatalf("List(original identity) error = %v", err)
				}
				if len(listed) != 1 || listed[0].ID != task.ID {
					t.Fatalf("List(original identity) = %#v, want task %q", listed, task.ID)
				}
				shown, err := service.Show(context.Background(), task.ID)
				if err != nil {
					t.Fatalf("Show(original identity) error = %v", err)
				}
				if shown.ID != task.ID {
					t.Fatalf("Show(original identity).ID = %q, want %q", shown.ID, task.ID)
				}
			}
		})
	}
}

func TestWriteRejectsNonCanonicalParentHeadsBeforeWritingObjectsOrMovingRefs(t *testing.T) {
	repo, config := writeRepository(t)
	created, _, createState := writeRoot(t, repo, config)
	tree := gitOutput(t, repo, "rev-parse", created.Head+"^{tree}")

	for _, test := range []struct {
		name string
		head string
	}{
		{name: "refname", head: taskRef(writeTaskID)},
		{name: "abbreviated object ID", head: created.Head[:len(created.Head)-1]},
		{name: "tree object ID", head: tree},
		{name: "tree-ish", head: created.Head + "^{tree}"},
		{name: "nonexistent object", head: "does-not-exist"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := created
			parent.Head = test.head
			pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
			state := writeState(t, &createState, pack)
			before := gitOutput(t, repo, "count-objects", "-v")

			_, err := repo.Write(context.Background(), config, &parent, pack, state, "mark ready")
			if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
				t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
			}
			if after := gitOutput(t, repo, "count-objects", "-v"); after != before {
				t.Fatalf("Write() wrote objects for parent head %q: before %q, after %q", test.head, before, after)
			}
			if got := gitOutput(t, repo, "rev-parse", taskRef(writeTaskID)); got != created.Head {
				t.Fatalf("task ref after parent head %q = %q, want %q", test.head, got, created.Head)
			}
		})
	}
}

func TestWriteClassifiesNamespaceCollisionAsOperationalNotStale(t *testing.T) {
	repo, config := writeRepository(t)
	pack := writeCreatePack()
	state := writeState(t, nil, pack)
	contents, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	object, err := repo.Git(context.Background(), contents, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("Git(hash-object) error = %v", err)
	}
	if _, err := repo.Git(context.Background(), nil, "update-ref", taskRef(pack.TaskID)+"/extra", strings.TrimSpace(string(object))); err != nil {
		t.Fatalf("Git(update-ref) error = %v", err)
	}

	_, err = repo.Write(context.Background(), config, nil, pack, state, "create task")
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("Write() category = %q, want original Git category %q; error = %v", got, want, err)
	}
	if _, err := repo.Git(context.Background(), nil, "rev-parse", "--verify", taskRef(pack.TaskID)); err == nil {
		t.Fatal("Write() created task ref despite namespace collision")
	}
}

func TestWriteRejectsInvalidCheckpointBeforeWritingObjectsOrRefs(t *testing.T) {
	repo, config := writeRepository(t)
	pack := writeCreatePack()
	state := writeState(t, nil, pack)
	state.Task.Title = "different"

	before := gitOutput(t, repo, "count-objects", "-v")
	_, err := repo.Write(context.Background(), config, nil, pack, state, "invalid checkpoint")
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
	}
	if after := gitOutput(t, repo, "count-objects", "-v"); after != before {
		t.Fatalf("Write() wrote objects on invalid checkpoint: before %q, after %q", before, after)
	}
	if _, err := repo.Git(context.Background(), nil, "rev-parse", "--verify", taskRef(pack.TaskID)); err == nil {
		t.Fatal("Write() created a task ref for an invalid checkpoint")
	}
}

func TestWriteRejectsMalformedAndDuplicateOperationIDsBeforePublishing(t *testing.T) {
	t.Run("malformed operation ID", func(t *testing.T) {
		repo, config := writeRepository(t)
		pack := writeCreatePack()
		pack.Operations[0].ID = "not-a-ulid"
		state := core.StateDocument{
			Format: "workbook.task-state", Version: 1, ProjectID: pack.ProjectID, TaskID: pack.TaskID,
			History: core.History{Generation: pack.HistoryGeneration}, LogicalClock: 1,
			Task: *pack.Operations[0].Task,
		}
		before := gitOutput(t, repo, "count-objects", "-v")

		_, err := repo.Write(context.Background(), config, nil, pack, state, "invalid operation")
		if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
			t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
		}
		if after := gitOutput(t, repo, "count-objects", "-v"); after != before {
			t.Fatalf("Write() wrote objects: before %q, after %q", before, after)
		}
		if _, err := repo.Git(context.Background(), nil, "rev-parse", "--verify", taskRef(pack.TaskID)); err == nil {
			t.Fatal("Write() created a task ref for malformed operation ID")
		}
	})

	t.Run("duplicate operation ID", func(t *testing.T) {
		repo, config := writeRepository(t)
		created, _, parentState := writeRoot(t, repo, config)
		pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
		pack.Operations = append(pack.Operations, core.Operation{
			ID: "01K0M6B8A4FTT8C39MXXYTW7C5", Type: core.OperationFieldSet, Field: "priority", Value: "high",
		})
		state := parentState
		state.LogicalClock = pack.LogicalClock
		state.Task.Status = core.StatusReady
		state.Task.Priority = core.PriorityHigh
		state.Task.UpdatedAt = pack.WallTime
		before := gitOutput(t, repo, "count-objects", "-v")

		_, err := repo.Write(context.Background(), config, &created, pack, state, "duplicate operation")
		if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
			t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
		}
		if after := gitOutput(t, repo, "count-objects", "-v"); after != before {
			t.Fatalf("Write() wrote objects: before %q, after %q", before, after)
		}
		if got := gitOutput(t, repo, "rev-parse", taskRef(pack.TaskID)); got != created.Head {
			t.Fatalf("task ref moved to %q, want %q", got, created.Head)
		}
	})
}

func TestWriteRejectsUnsupportedCompactionMetadataBeforePublishing(t *testing.T) {
	repo, config := writeRepository(t)
	pack := writeCreatePack()
	state := writeState(t, nil, pack)
	compactedFrom := "0123456789abcdef"
	state.History.CompactedFrom = &compactedFrom
	before := gitOutput(t, repo, "count-objects", "-v")

	_, err := repo.Write(context.Background(), config, nil, pack, state, "compacted state")
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
	}
	if after := gitOutput(t, repo, "count-objects", "-v"); after != before {
		t.Fatalf("Write() wrote objects: before %q, after %q", before, after)
	}
	if _, err := repo.Git(context.Background(), nil, "rev-parse", "--verify", taskRef(pack.TaskID)); err == nil {
		t.Fatal("Write() created a task ref for unsupported compaction metadata")
	}
}

func TestWriteValidatesAgainstStateStoredAtParentHead(t *testing.T) {
	repo, config := writeRepository(t)
	created, _, createState := writeRoot(t, repo, config)
	forged := created
	forged.State.Task.Labels = []string{"forged"}
	pack := writeAddLabelPack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "real")
	state := writeState(t, &forged.State, pack)

	_, err := repo.Write(context.Background(), config, &forged, pack, state, "mark ready")
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(pack.TaskID)); got != created.Head {
		t.Fatalf("task ref after forged parent = %q, want %q", got, created.Head)
	}
	if got, want := createState.Task.Status, core.StatusBacklog; got != want {
		t.Fatalf("root state fixture mutated to %q, want %q", got, want)
	}
}

func TestWriteRejectsDivergentRootParentEvenWhenCallerMatchesStoredState(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, _ := writeRoot(t, repo, config)
	tamperedState := created.State
	tamperedState.Task.Title = "Different state title"
	replaceTaskTree(
		t,
		repo,
		created,
		gitOutput(t, repo, "rev-parse", created.Head+":operation.json"),
		writeDocumentBlob(t, repo, tamperedState),
	)
	tamperedHead := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID))
	parent := core.Snapshot{Head: tamperedHead, Operation: createPack, State: tamperedState}
	pack := writeAddLabelPack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "direct-write")
	state := writeState(t, &parent.State, pack)

	_, err := repo.Write(context.Background(), config, &parent, pack, state, "extend malformed root")
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != tamperedHead {
		t.Fatalf("Write() advanced malformed root from %q to %q", tamperedHead, got)
	}
}

func TestWriteRejectsRootParentWhoseLogicalClockStartsAtTwo(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, _ := writeRoot(t, repo, config)
	tamperedPack := created.Operation
	tamperedPack.LogicalClock = 2
	tamperedState := created.State
	tamperedState.LogicalClock = 2
	replaceTaskTree(
		t,
		repo,
		created,
		writeDocumentBlob(t, repo, tamperedPack),
		writeDocumentBlob(t, repo, tamperedState),
	)
	tamperedHead := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID))
	parent := core.Snapshot{Head: tamperedHead, Operation: tamperedPack, State: tamperedState}
	pack := writeAddLabelPack(3, "01K0M6B8A4FTT8C39MXXYTW7C5", "direct-write")
	state := writeState(t, &parent.State, pack)

	_, err := repo.Write(context.Background(), config, &parent, pack, state, "extend malformed root")
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Write() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != tamperedHead {
		t.Fatalf("Write() advanced malformed root from %q to %q", tamperedHead, got)
	}
}

func TestWriteAcceptsValidatedRootAndLinearParents(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	firstPack := writeAddLabelPack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "first")
	firstState := writeState(t, &createState, firstPack)
	first, err := repo.Write(context.Background(), config, &created, firstPack, firstState, "add first label")
	if err != nil {
		t.Fatalf("Write(root parent) error = %v", err)
	}

	secondPack := writeAddLabelPack(3, "01K0M6B8A4FTT8C39MXXYTW7C6", "second")
	secondState := writeState(t, &firstState, secondPack)
	second, err := repo.Write(context.Background(), config, &first, secondPack, secondState, "add second label")
	if err != nil {
		t.Fatalf("Write(linear parent) error = %v", err)
	}
	if got, want := gitOutput(t, repo, "rev-list", "--parents", "-n", "1", second.Head), second.Head+" "+first.Head; got != want {
		t.Fatalf("second update commit parents = %q, want %q", got, want)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(createPack.TaskID)); got != second.Head {
		t.Fatalf("task ref = %q, want %q", got, second.Head)
	}
}

func writeRepository(t *testing.T) (*Repository, core.ProjectConfig) {
	t.Helper()
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	config, _, err := repo.Init(context.Background(), "WB", idsFor(writeProjectID))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return repo, config
}

func writeRoot(t *testing.T, repo *Repository, config core.ProjectConfig) (core.Snapshot, core.OperationPack, core.StateDocument) {
	t.Helper()
	pack := writeCreatePack()
	state := writeState(t, nil, pack)
	snapshot, err := repo.Write(context.Background(), config, nil, pack, state, "create task")
	if err != nil {
		t.Fatalf("Write(root) error = %v", err)
	}
	return snapshot, pack, state
}

func writeCreatePack() core.OperationPack {
	task := core.TaskData{
		Title: "Persist task operations", Status: core.StatusBacklog, Priority: core.PriorityMedium,
		Labels: []string{}, Rank: "1/1", Dependencies: []string{}, CreatedAt: writeCreatedAt, UpdatedAt: writeCreatedAt,
	}
	return core.OperationPack{
		Format: "workbook.operation-pack", Version: 1, ProjectID: writeProjectID, TaskID: writeTaskID,
		HistoryGeneration: writeGenerationID, Actor: core.Actor{ID: "writer@example.test"}, LogicalClock: 1,
		WallTime: writeCreatedAt, Operations: []core.Operation{{ID: "01K0M6B8A4FTT8C39MXXYTW7C4", Type: core.OperationTaskCreate, Task: &task}},
	}
}

func writeUpdatePack(clock uint64, operationID, status string) core.OperationPack {
	return core.OperationPack{
		Format: "workbook.operation-pack", Version: 1, ProjectID: writeProjectID, TaskID: writeTaskID,
		HistoryGeneration: writeGenerationID, Actor: core.Actor{ID: "writer@example.test"}, LogicalClock: clock,
		WallTime:   writeCreatedAt.Add(time.Duration(clock-1) * time.Minute),
		Operations: []core.Operation{{ID: operationID, Type: core.OperationFieldSet, Field: "status", Value: status}},
	}
}

func writeAddLabelPack(clock uint64, operationID, label string) core.OperationPack {
	return core.OperationPack{
		Format: "workbook.operation-pack", Version: 1, ProjectID: writeProjectID, TaskID: writeTaskID,
		HistoryGeneration: writeGenerationID, Actor: core.Actor{ID: "writer@example.test"}, LogicalClock: clock,
		WallTime:   writeCreatedAt.Add(time.Duration(clock-1) * time.Minute),
		Operations: []core.Operation{{ID: operationID, Type: core.OperationSetAdd, Field: "labels", Value: label}},
	}
}

func writeState(t *testing.T, parent *core.StateDocument, pack core.OperationPack) core.StateDocument {
	t.Helper()
	state, err := core.Apply(parent, pack, "WB")
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return state
}

func assertTaskTree(t *testing.T, repo *Repository, head string, pack core.OperationPack, state core.StateDocument) {
	t.Helper()
	if got, want := gitOutput(t, repo, "ls-tree", head), "100644 blob "+gitOutput(t, repo, "rev-parse", head+":operation.json")+"\toperation.json\n100644 blob "+gitOutput(t, repo, "rev-parse", head+":state.json")+"\tstate.json"; got != want {
		t.Fatalf("task tree = %q, want exactly two regular blobs %q", got, want)
	}
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(pack) error = %v", err)
	}
	if got := gitOutput(t, repo, "show", head+":operation.json"); !bytes.Equal([]byte(got+"\n"), packBytes) {
		t.Fatalf("operation blob = %q, want %q", got+"\n", packBytes)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(state) error = %v", err)
	}
	if got := gitOutput(t, repo, "show", head+":state.json"); !bytes.Equal([]byte(got+"\n"), stateBytes) {
		t.Fatalf("state blob = %q, want %q", got+"\n", stateBytes)
	}
}

func taskRef(taskID string) string { return "refs/workbook/tasks/" + taskID }

func gitOutput(t *testing.T, repo *Repository, args ...string) string {
	t.Helper()
	output, err := repo.Git(context.Background(), nil, args...)
	if err != nil {
		t.Fatalf("Git(%v) error = %v", args, err)
	}
	return strings.TrimSuffix(string(output), "\n")
}

func assertCommandSequence(t *testing.T, commands [][]string, want []string) {
	t.Helper()
	got := make([]string, len(commands))
	for i, command := range commands {
		if len(command) == 0 {
			got[i] = ""
			continue
		}
		switch command[0] {
		case "hash-object":
			got[i] = strings.Join(command, " ")
		default:
			got[i] = command[0]
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Git command sequence = %#v, want %#v; commands = %#v", got, want, commands)
	}
}

func assertNoTaskRefs(t *testing.T, repo *Repository) {
	t.Helper()
	output, err := repo.Git(context.Background(), nil, "for-each-ref", "--format=%(refname)", taskRefPrefix)
	if err != nil {
		t.Fatalf("Git(for-each-ref) error = %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("task refs = %q, want none", output)
	}
}

func conflictingLinkedWorktrees(t *testing.T) (*Repository, *Repository, core.ProjectConfig, core.ProjectConfig) {
	t.Helper()
	repositories := linkedWorktreeRepositories(t)
	guarded := repositories[0]
	linked := repositories[1]
	original, _, err := guarded.Init(context.Background(), "WB", idsFor(writeProjectID))
	if err != nil {
		t.Fatalf("Init(guarded worktree) error = %v", err)
	}
	foreign := original
	foreign.ProjectID = "01K0M6B8A4FTT8C39MXXYTW7C9"
	writeProjectConfigFile(t, filepath.Join(linked.Root, configPath), foreign)
	assertNoTaskRefs(t, guarded)
	return guarded, linked, original, foreign
}

func testService(repo *Repository, config core.ProjectConfig) core.Service {
	ids := []string{
		"01K0M6B8A4FTT8C39MXXYTW7D1",
		"01K0M6B8A4FTT8C39MXXYTW7D2",
		"01K0M6B8A4FTT8C39MXXYTW7D3",
		"01K0M6B8A4FTT8C39MXXYTW7D4",
		"01K0M6B8A4FTT8C39MXXYTW7D5",
	}
	index := 0
	return core.Service{
		Config: config,
		Reader: repo,
		Writer: repo,
		IDs: core.IDSourceFunc(func() (string, error) {
			id := ids[index]
			index++
			return id, nil
		}),
		Now:   func() time.Time { return writeCreatedAt },
		Actor: "writer@example.test",
	}
}
