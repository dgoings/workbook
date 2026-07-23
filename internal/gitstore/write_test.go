package gitstore

import (
	"bytes"
	"context"
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

	snapshot, err := repo.Write(context.Background(), config, nil, pack, state, "create task")
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
	if got := gitOutput(t, repo, "reflog", "show", "--format=%gs", "-n", "1", ref); !strings.HasPrefix(got, "workbook:") {
		t.Fatalf("reflog message = %q, want workbook prefix", got)
	}
}

func TestWriteAppendsCommitToCurrentHead(t *testing.T) {
	repo, config := writeRepository(t)
	created, createPack, createState := writeRoot(t, repo, config)
	pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", "ready")
	state := writeState(t, &createState, pack)

	snapshot, err := repo.Write(context.Background(), config, &created, pack, state, "mark ready")
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

func writeRepository(t *testing.T) (*Repository, core.ProjectConfig) {
	t.Helper()
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return repo, core.ProjectConfig{Format: "workbook.project", Version: 1, ProjectID: writeProjectID, Key: "WB"}
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
