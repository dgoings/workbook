package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Reading set-aside edits costs a fixed number of Git processes however many
// parked refs a clone holds, and a second read with nothing changed, as a
// board polling every second makes, costs none: listing thousands of loose
// parked refs alone takes a noticeable part of a second, so the listing is
// reused while no task head and no parked ref changed. The bounds are on
// processes, which do not depend on the machine; the times are logged for
// comparison and bounded only as a guard against a hang, since a run of the
// whole tree slows the first read of 2,000 parked refs from half a second to
// ten.
func TestSetAsideEditsCostDoesNotGrowWithParkedRefs(t *testing.T) {
	t.Parallel()
	const tasks = 2000
	ctx := context.Background()
	repo, config := writeRepository(t)
	ids := buildScaleTasks(t, repo, config, tasks)

	parked := 0
	for _, want := range []int{0, 300, 2000} {
		addScaleParkedEdits(t, repo, config, ids[parked:want])
		parked = want

		// A fresh handle, as a restarted server opens one.
		reopened := openSyncCloneAt(t, repo.Root)
		heads := taskHeads(t, reopened)
		if len(heads) != tasks {
			t.Fatalf("task heads = %d, want %d", len(heads), tasks)
		}
		cold, coldProcesses := measureSetAside(t, ctx, reopened, config, heads, want)
		warm, warmProcesses := measureSetAside(t, ctx, reopened, config, heads, want)
		t.Logf("%d tasks, %d parked refs: cold %v in %d Git processes, warm %v in %d", tasks, want, cold, coldProcesses, warm, warmProcesses)
		if coldProcesses > 8 {
			t.Errorf("%d parked refs: the first read ran %d Git processes, want at most 8 however many there are", want, coldProcesses)
		}
		if warmProcesses > 0 {
			t.Errorf("%d parked refs: a read with nothing changed ran %d Git processes, want none", want, warmProcesses)
		}
		if cold > time.Minute || warm > 10*time.Second {
			t.Errorf("%d parked refs: the first read took %v and a repeat %v", want, cold, warm)
		}
	}
}

// measureSetAside reads the set-aside edits once and reports how long it took
// and how many Git processes it ran. A read that runs far more processes than
// any bound is cancelled rather than waited out.
func measureSetAside(t *testing.T, ctx context.Context, repo *Repository, config core.ProjectConfig, heads map[string]string, want int) (time.Duration, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var processes atomic.Int64
	repo.commandObserver = func([]string) {
		if processes.Add(1) > 100 {
			cancel()
		}
	}
	defer func() { repo.commandObserver = nil }()
	started := time.Now()
	report, err := repo.SetAsideEdits(ctx, config, heads)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("%d parked refs: SetAsideEdits() after %d Git processes: %v", want, processes.Load(), err)
	}
	if len(report.Edits) != want || len(report.Unreadable) != 0 {
		t.Fatalf("set-aside edits on %d tasks, %d unreadable; want %d and none", len(report.Edits), len(report.Unreadable), want)
	}
	return elapsed, int(processes.Load())
}

// buildScaleTasks writes count tasks, each one task.create commit, with one
// fast-import, and returns their IDs. The first is created by the service and
// the rest are built from its create pack, so each is exactly what a create
// would have written.
func buildScaleTasks(t *testing.T, repo *Repository, config core.ProjectConfig, count int) []string {
	t.Helper()
	ctx := context.Background()
	first := createSyncTask(t, repo, config, "Scale task 0")
	template, err := repo.Get(ctx, config, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _ := strings.Cut(first.ID, "-")
	source := core.CryptoULIDSource{}
	var stream bytes.Buffer
	ids := []string{first.ID}
	for index := 1; index < count; index++ {
		body, err := source.New()
		if err != nil {
			t.Fatal(err)
		}
		operationID, err := source.New()
		if err != nil {
			t.Fatal(err)
		}
		pack := template.Operation
		pack.TaskID = key + "-" + body
		operation := pack.Operations[0]
		created := *operation.Task
		created.Title = fmt.Sprintf("Scale task %d", index)
		operation.ID, operation.Task = operationID, &created
		pack.Operations = []core.Operation{operation}
		state, err := core.Apply(nil, pack)
		if err != nil {
			t.Fatal(err)
		}
		writeScaleCommit(t, &stream, taskRefPrefix+pack.TaskID, "", pack, state)
		ids = append(ids, pack.TaskID)
	}
	runScaleImport(t, repo, &stream)
	return ids
}

// addScaleParkedEdits parks, for each task, one description edit on top of
// its tip that the task does not carry, as a conflict would have.
func addScaleParkedEdits(t *testing.T, repo *Repository, config core.ProjectConfig, taskIDs []string) {
	t.Helper()
	if len(taskIDs) == 0 {
		return
	}
	tips := taskHeads(t, repo)
	heads := make([]TaskHead, 0, len(taskIDs))
	for _, id := range taskIDs {
		heads = append(heads, TaskHead{TaskID: id, ObjectID: tips[id]})
	}
	snapshots, err := repo.ReadTaskHeads(context.Background(), config, heads)
	if err != nil {
		t.Fatal(err)
	}
	source := core.CryptoULIDSource{}
	var stream bytes.Buffer
	for _, snapshot := range snapshots {
		operationID, err := source.New()
		if err != nil {
			t.Fatal(err)
		}
		pack := snapshot.Operation
		pack.Actor = core.Actor{ID: "alice@client.example"}
		pack.LogicalClock = snapshot.State.LogicalClock + 1
		pack.HistoryGeneration = snapshot.State.History.Generation
		pack.Operations = []core.Operation{{
			ID: operationID, Type: core.OperationFieldSet, Field: "description", Value: "Set aside " + pack.TaskID,
		}}
		state, err := core.Apply(&snapshot.State, pack)
		if err != nil {
			t.Fatal(err)
		}
		writeScaleCommit(t, &stream, reconciledRefPrefix+pack.TaskID+"/0", snapshot.Head, pack, state)
	}
	runScaleImport(t, repo, &stream)
}

func writeScaleCommit(t *testing.T, stream *bytes.Buffer, ref, parent string, pack core.OperationPack, state core.StateDocument) {
	t.Helper()
	operation, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	const message = "workbook: scale fixture\n"
	fmt.Fprintf(stream, "commit %s\ncommitter Workbook <workbook@example.invalid> %d +0000\ndata %d\n%s", ref, pack.WallTime.Unix(), len(message), message)
	if parent != "" {
		fmt.Fprintf(stream, "from %s\n", parent)
	}
	fmt.Fprintf(stream, "deleteall\nM 100644 inline operation.json\ndata %d\n%s\nM 100644 inline state.json\ndata %d\n%s\n\n",
		len(operation), operation, len(checkpoint), checkpoint)
}

func runScaleImport(t *testing.T, repo *Repository, stream *bytes.Buffer) {
	t.Helper()
	command := exec.Command("git", "-C", repo.Root, "fast-import", "--quiet")
	command.Stdin = stream
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, output)
	}
}
