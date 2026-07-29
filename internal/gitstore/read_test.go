package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

func TestReadTaskHeadsBatchesCurrentTips(t *testing.T) {
	repository, config := writeRepository(t)
	first, _, _ := writeRoot(t, repository, config)
	second := writeIndependentRoot(
		t,
		repository,
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Second task",
	)

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	heads, err := repository.ListTaskHeads(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := repository.ReadTaskHeads(context.Background(), config, heads)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snapshots))
	}
	if !reflect.DeepEqual(snapshots, []core.Snapshot{first, second}) {
		t.Fatalf("snapshots = %#v, want first and second task snapshots", snapshots)
	}
	if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "refs/workbook/tasks/"); got != 1 {
		t.Fatalf("for-each-ref commands = %d, want 1; commands = %v", got, commands)
	}
	if got := countCommand(commands, "cat-file", "--batch"); got != 1 {
		t.Fatalf("cat-file --batch commands = %d, want 1; commands = %v", got, commands)
	}
}

func TestInspectTaskHeadRejectsSymbolicExactRef(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repository, config)
	gitOutput(t, repository, "update-ref", "refs/workbook/symbolic-target", snapshot.Head)
	gitOutput(t, repository, "update-ref", "-d", taskRef(pack.TaskID))
	gitOutput(t, repository, "symbolic-ref", taskRef(pack.TaskID), "refs/workbook/symbolic-target")

	_, _, err := repository.InspectTaskHead(context.Background(), config, pack.TaskID)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("InspectTaskHead() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestInspectTaskHeadRejectsNestedEntriesUnderExactName(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repository, config)
	gitOutput(t, repository, "update-ref", "-d", taskRef(pack.TaskID))
	gitOutput(t, repository, "update-ref", taskRef(pack.TaskID)+"/nested", snapshot.Head)

	_, _, err := repository.InspectTaskHead(context.Background(), config, pack.TaskID)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("InspectTaskHead() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestInspectTaskHeadReturnsAbsentValidTaskID(t *testing.T) {
	repository, config := writeRepository(t)
	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	head, found, err := repository.InspectTaskHead(
		context.Background(),
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7D0",
	)
	if err != nil {
		t.Fatal(err)
	}
	if found || head != (TaskHead{}) {
		t.Fatalf("InspectTaskHead() = (%#v, %t), want zero head and false", head, found)
	}
	if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "refs/workbook/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7D0"); got != 1 {
		t.Fatalf("exact for-each-ref commands = %d, want 1; commands = %v", got, commands)
	}
}

func TestInspectTaskHeadRejectsInvalidFullIDBeforeRunningGit(t *testing.T) {
	opened, config := writeRepository(t)
	repository := &Repository{
		Root:         opened.Root,
		CommonGitDir: opened.CommonGitDir,
		gitPath:      opened.gitPath,
	}
	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	_, _, err := repository.InspectTaskHead(context.Background(), config, "WB-01K0")
	if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
		t.Fatalf("InspectTaskHead() category = %q, want %q; error = %v", got, want, err)
	}
	if len(commands) != 0 {
		t.Fatalf("InspectTaskHead() commands = %v, want none for invalid full ID", commands)
	}
}

func TestInspectTaskHeadDoesNotEnumerateOrReadUnrelatedTaskObjects(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repository, config)
	unrelatedBlob := gitOutputWithInput(t, repository, []byte("not a task"), "hash-object", "-w", "--stdin")
	gitOutput(t, repository, "update-ref", "refs/workbook/tasks/WB-01K0M6B8A4FTT8C39MXXYTW7D1", unrelatedBlob)
	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	head, found, err := repository.InspectTaskHead(context.Background(), config, pack.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || head != (TaskHead{TaskID: pack.TaskID, ObjectID: snapshot.Head}) {
		t.Fatalf("InspectTaskHead() = (%#v, %t), want exact task head", head, found)
	}
	if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "refs/workbook/tasks/"+pack.TaskID); got != 1 {
		t.Fatalf("exact for-each-ref commands = %d, want 1; commands = %v", got, commands)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "cat-file" {
			t.Fatalf("InspectTaskHead() read an object with command %v", command)
		}
	}
}

func TestReadTaskHeadsSupportsRepositoryObjectFormats(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			repository, config := writeRepositoryWithObjectFormat(t, objectFormat)
			snapshot, _, _ := writeRoot(t, repository, config)

			got, err := repository.ReadTaskHeads(
				context.Background(),
				config,
				[]TaskHead{{TaskID: snapshot.Operation.TaskID, ObjectID: snapshot.Head}},
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, []core.Snapshot{snapshot}) {
				t.Fatalf("ReadTaskHeads() = %#v, want %#v", got, []core.Snapshot{snapshot})
			}
		})
	}
}

func TestTipReadAcceptsInternallyValidNonRootCheckpointMismatch(t *testing.T) {
	repository, config := writeRepository(t)
	created, pack, state := writeRoot(t, repository, config)
	update := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	updatedState := writeState(t, &state, update)
	updated, err := repository.Write(context.Background(), config, &created, update, updatedState, "mark ready")
	if err != nil {
		t.Fatal(err)
	}

	checkpoint := updated.State
	checkpoint.Task.Title = "Structurally valid, semantically mismatched"
	tree := gitOutputWithInput(t, repository, []byte(fmt.Sprintf(
		"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		gitOutput(t, repository, "rev-parse", updated.Head+":operation.json"),
		writeDocumentBlob(t, repository, checkpoint),
	)), "mktree")
	tip := gitOutput(t, repository, "commit-tree", tree, "-p", created.Head, "-m", "mismatched checkpoint")

	got, err := repository.ReadTaskHeads(context.Background(), config, []TaskHead{{TaskID: pack.TaskID, ObjectID: tip}})
	if err != nil {
		t.Fatalf("ReadTaskHeads() error = %v", err)
	}
	if len(got) != 1 || got[0].Head != tip || !reflect.DeepEqual(got[0].State, checkpoint) {
		t.Fatalf("ReadTaskHeads() = %#v, want the unchecked non-root checkpoint", got)
	}
}

func TestOwnedRefsValidateCanonicalAndTrackingNamespaces(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repository, config)
	validRecord := func(prefix string) []byte {
		return []byte(prefix + pack.TaskID + "\x00" + snapshot.Head + "\x00\n")
	}

	for _, prefix := range []string{taskRefPrefix, trackingTaskRefPrefix} {
		t.Run(prefix, func(t *testing.T) {
			refs, err := repository.parseOwnedRefRecords(config, prefix, validRecord(prefix), "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(refs, []taskRefRecord{{taskID: pack.TaskID, objectID: snapshot.Head}}) {
				t.Fatalf("refs = %#v, want one validated record", refs)
			}
		})
	}

	for _, test := range []struct {
		name     string
		contents []byte
	}{
		{name: "symbolic", contents: []byte(taskRefPrefix + pack.TaskID + "\x00" + snapshot.Head + "\x00refs/heads/main\n")},
		{name: "nested", contents: []byte(taskRefPrefix + pack.TaskID + "/nested\x00" + snapshot.Head + "\x00\n")},
		{name: "duplicate", contents: append(validRecord(taskRefPrefix), validRecord(taskRefPrefix)...)},
		{name: "wrong prefix", contents: []byte("refs/heads/main\x00" + snapshot.Head + "\x00\n")},
		{name: "invalid task ID", contents: []byte(taskRefPrefix + "not-a-task\x00" + snapshot.Head + "\x00\n")},
		{name: "abbreviated object ID", contents: []byte(taskRefPrefix + pack.TaskID + "\x00" + snapshot.Head[:len(snapshot.Head)-1] + "\x00\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.parseOwnedRefRecords(config, taskRefPrefix, test.contents, "")
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("parseOwnedRefRecords() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestOwnedRefsUseOneEnumerationForCanonicalAndTrackingRefs(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repository, config)
	gitOutput(t, repository, "update-ref", trackingTaskRefPrefix+pack.TaskID, snapshot.Head)
	gitOutput(t, repository, "pack-refs", "--all")

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	for _, prefix := range []string{taskRefPrefix, trackingTaskRefPrefix} {
		refs, err := repository.listOwnedTaskRefs(context.Background(), config, prefix)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(refs, []taskRefRecord{{taskID: pack.TaskID, objectID: snapshot.Head}}) {
			t.Fatalf("%s refs = %#v, want one task ref", prefix, refs)
		}
		if got := countCommand(commands, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", prefix); got != 1 {
			t.Fatalf("%s for-each-ref commands = %d, want 1; commands = %v", prefix, got, commands)
		}
	}
}

func TestValidateTaskHeadAdvancesBatchesIndependentHistories(t *testing.T) {
	repository, config := writeRepository(t)
	firstPrevious, _, firstState := writeRoot(t, repository, config)
	secondPrevious := writeIndependentRoot(
		t,
		repository,
		config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Second task",
	)

	firstPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7D1", string(core.StatusReady))
	firstCurrent, err := repository.Write(
		context.Background(),
		config,
		&firstPrevious,
		firstPack,
		writeState(t, &firstState, firstPack),
		"advance first",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7D2", string(core.StatusReady))
	secondPack.TaskID = secondPrevious.Operation.TaskID
	secondPack.HistoryGeneration = secondPrevious.Operation.HistoryGeneration
	secondCurrent, err := repository.Write(
		context.Background(),
		config,
		&secondPrevious,
		secondPack,
		writeState(t, &secondPrevious.State, secondPack),
		"advance second",
	)
	if err != nil {
		t.Fatal(err)
	}

	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	err = repository.ValidateTaskHeadAdvances(context.Background(), config, []HeadAdvance{
		{Previous: firstPrevious, Current: TaskHead{TaskID: firstCurrent.Operation.TaskID, ObjectID: firstCurrent.Head}},
		{Previous: secondPrevious, Current: TaskHead{TaskID: secondCurrent.Operation.TaskID, ObjectID: secondCurrent.Head}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := countCommand(commands, "rev-list", "--parents", "--stdin"); got != 1 {
		t.Fatalf("rev-list --parents --stdin commands = %d, want 1; commands = %v", got, commands)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "merge-base" {
			t.Fatalf("ValidateTaskHeadAdvances() ran per-task merge-base command %v", command)
		}
	}
}

func TestValidateTaskHeadAdvancesRejectsBackwardAndSidewaysMovement(t *testing.T) {
	tests := []struct {
		name    string
		current func(t *testing.T, repository *Repository, previous, root core.Snapshot) TaskHead
	}{
		{
			name: "backward",
			current: func(_ *testing.T, _ *Repository, _, root core.Snapshot) TaskHead {
				return TaskHead{TaskID: root.Operation.TaskID, ObjectID: root.Head}
			},
		},
		{
			name: "sideways",
			current: func(t *testing.T, repository *Repository, previous, root core.Snapshot) TaskHead {
				tree := gitOutput(t, repository, "rev-parse", previous.Head+"^{tree}")
				sibling := gitOutput(t, repository, "commit-tree", tree, "-p", root.Head, "-m", "sideways update")
				return TaskHead{TaskID: root.Operation.TaskID, ObjectID: sibling}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, config := writeRepository(t)
			root, _, rootState := writeRoot(t, repository, config)
			pack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7D1", string(core.StatusReady))
			previous, err := repository.Write(
				context.Background(),
				config,
				&root,
				pack,
				writeState(t, &rootState, pack),
				"advance",
			)
			if err != nil {
				t.Fatal(err)
			}

			err = repository.ValidateTaskHeadAdvances(context.Background(), config, []HeadAdvance{{
				Previous: previous,
				Current:  test.current(t, repository, previous, root),
			}})
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("ValidateTaskHeadAdvances() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestValidateTaskHeadAdvancesRejectsInvalidPairsBeforeWalkingHistory(t *testing.T) {
	repository, config := writeRepository(t)
	previous, _, _ := writeRoot(t, repository, config)
	valid := HeadAdvance{
		Previous: previous,
		Current:  TaskHead{TaskID: previous.Operation.TaskID, ObjectID: previous.Head},
	}
	tests := []struct {
		name     string
		advances []HeadAdvance
	}{
		{
			name:     "duplicate task ID",
			advances: []HeadAdvance{valid, valid},
		},
		{
			name: "mismatched task ID",
			advances: []HeadAdvance{{
				Previous: previous,
				Current: TaskHead{
					TaskID:   "WB-01K0M6B8A4FTT8C39MXXYTW7D1",
					ObjectID: previous.Head,
				},
			}},
		},
		{
			name: "missing current head",
			advances: []HeadAdvance{{
				Previous: previous,
				Current:  TaskHead{TaskID: previous.Operation.TaskID},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := repository.ValidateTaskHeadAdvances(context.Background(), config, test.advances)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("ValidateTaskHeadAdvances() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestListTaskHeadsAndReadTaskHead(t *testing.T) {
	repository, config := writeRepository(t)
	ids := []string{
		"01K0M6B8A4FTT8C39MXXYTW7D1", "01K0M6B8A4FTT8C39MXXYTW7D2", "01K0M6B8A4FTT8C39MXXYTW7D3",
		"01K0M6B8A4FTT8C39MXXYTW7D4", "01K0M6B8A4FTT8C39MXXYTW7D5", "01K0M6B8A4FTT8C39MXXYTW7D6",
	}
	index := 0
	service := core.Service{
		Config: config,
		Reader: repository,
		Writer: repository,
		IDs: core.IDSourceFunc(func() (string, error) {
			if index == len(ids) {
				return "", fmt.Errorf("test ID source exhausted")
			}
			id := ids[index]
			index++
			return id, nil
		}),
		Now:   func() time.Time { return writeCreatedAt },
		Actor: "writer@example.test",
	}
	firstResult, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "First"})
	if err != nil {
		t.Fatalf("CreateMutation(first) error = %v", err)
	}
	first := firstResult.Task
	secondResult, err := service.CreateMutation(context.Background(), core.CreateInput{Title: "Second"})
	if err != nil {
		t.Fatalf("CreateMutation(second) error = %v", err)
	}
	second := secondResult.Task

	heads, err := repository.ListTaskHeads(context.Background(), config)
	if err != nil {
		t.Fatalf("ListTaskHeads() error = %v", err)
	}
	got := []string{heads[0].TaskID, heads[1].TaskID}
	want := []string{first.ID, second.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("heads = %v, want %v", got, want)
	}

	snapshot, err := repository.ReadTaskHead(context.Background(), config, heads[0])
	if err != nil {
		t.Fatalf("ReadTaskHead() error = %v", err)
	}
	if snapshot.Head != heads[0].ObjectID || snapshot.State.TaskID != heads[0].TaskID {
		t.Fatalf("snapshot = %#v, want head %q for %q", snapshot, heads[0].ObjectID, heads[0].TaskID)
	}
}

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

func TestGetAcceptsRootAndLinearTipTopology(t *testing.T) {
	repo, config := writeRepository(t)
	created, pack, state := writeRoot(t, repo, config)
	if _, err := repo.Get(context.Background(), config, pack.TaskID); err != nil {
		t.Fatalf("Get(root) error = %v", err)
	}

	updatePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	updateState := writeState(t, &state, updatePack)
	if _, err := repo.Write(context.Background(), config, &created, updatePack, updateState, "mark ready"); err != nil {
		t.Fatalf("Write(update) error = %v", err)
	}
	if _, err := repo.Get(context.Background(), config, pack.TaskID); err != nil {
		t.Fatalf("Get(linear) error = %v", err)
	}
}

func TestGetValidatesRootOperationAndStateCheckpointEquivalence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, repo *Repository, snapshot core.Snapshot)
	}{
		{name: "valid root"},
		{
			name: "operation and state titles differ",
			mutate: func(t *testing.T, repo *Repository, snapshot core.Snapshot) {
				state := snapshot.State
				state.Task.Title = "Different state title"
				replaceTaskTree(
					t,
					repo,
					snapshot,
					gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json"),
					writeDocumentBlob(t, repo, state),
				)
			},
		},
		{
			name: "root logical clock is two",
			mutate: func(t *testing.T, repo *Repository, snapshot core.Snapshot) {
				pack := snapshot.Operation
				pack.LogicalClock = 2
				state := snapshot.State
				state.LogicalClock = 2
				replaceTaskTree(t, repo, snapshot, writeDocumentBlob(t, repo, pack), writeDocumentBlob(t, repo, state))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			snapshot, pack, _ := writeRoot(t, repo, config)
			if test.mutate != nil {
				test.mutate(t, repo, snapshot)
			}

			_, err := repo.Get(context.Background(), config, pack.TaskID)
			if test.mutate == nil {
				if err != nil {
					t.Fatalf("Get(valid root) error = %v", err)
				}
				return
			}
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("Get() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestCRUDCannotExtendMalformedRootCheckpoint(t *testing.T) {
	repo, config := writeRepository(t)
	snapshot, pack, _ := writeRoot(t, repo, config)
	state := snapshot.State
	state.Task.Title = "Different state title"
	replaceTaskTree(
		t,
		repo,
		snapshot,
		gitOutput(t, repo, "rev-parse", snapshot.Head+":operation.json"),
		writeDocumentBlob(t, repo, state),
	)
	tamperedHead := gitOutput(t, repo, "rev-parse", taskRef(pack.TaskID))

	title := "Attempted extension"
	service := testService(repo, config)
	_, err := service.UpdateMutation(context.Background(), pack.TaskID, core.UpdateInput{Title: &title})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Update() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", taskRef(pack.TaskID)); got != tamperedHead {
		t.Fatalf("Update() advanced malformed root from %q to %q", tamperedHead, got)
	}
}

func TestGetRejectsUnsupportedTipTopology(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, repo *Repository, snapshot core.Snapshot) string
	}{
		{
			name: "parented create",
			mutate: func(t *testing.T, repo *Repository, snapshot core.Snapshot) string {
				tree := gitOutput(t, repo, "rev-parse", snapshot.Head+"^{tree}")
				parent := gitOutput(t, repo, "commit-tree", gitOutput(t, repo, "mktree"), "-m", "parent")
				return gitOutput(t, repo, "commit-tree", tree, "-p", parent, "-m", "parented create")
			},
		},
		{
			name: "parentless update",
			mutate: func(t *testing.T, repo *Repository, snapshot core.Snapshot) string {
				tree := gitOutput(t, repo, "rev-parse", snapshot.Head+"^{tree}")
				return gitOutput(t, repo, "commit-tree", tree, "-m", "parentless update")
			},
		},
		{
			name: "multi-parent update",
			mutate: func(t *testing.T, repo *Repository, snapshot core.Snapshot) string {
				tree := gitOutput(t, repo, "rev-parse", snapshot.Head+"^{tree}")
				secondParent := gitOutput(t, repo, "commit-tree", gitOutput(t, repo, "mktree"), "-m", "second parent")
				parentLine := gitOutput(t, repo, "rev-list", "--parents", "-n", "1", snapshot.Head)
				firstParent := strings.Fields(parentLine)[1]
				return gitOutput(t, repo, "commit-tree", tree, "-p", firstParent, "-p", secondParent, "-m", "merged update")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			created, pack, state := writeRoot(t, repo, config)
			tip := created
			if test.name != "parented create" {
				updatePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
				updateState := writeState(t, &state, updatePack)
				var err error
				tip, err = repo.Write(context.Background(), config, &created, updatePack, updateState, "mark ready")
				if err != nil {
					t.Fatalf("Write(update) error = %v", err)
				}
			}
			invalidTip := test.mutate(t, repo, tip)
			gitOutput(t, repo, "update-ref", taskRef(pack.TaskID), invalidTip)

			_, err := repo.Get(context.Background(), config, pack.TaskID)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("Get() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestGetIgnoresGitReplaceObjects(t *testing.T) {
	repo, config := writeRepository(t)
	original, _, _ := writeRoot(t, repo, config)

	replacementPack := writeCreatePack()
	replacementPack.Operations[0].ID = "01K0M6B8A4FTT8C39MXXYTW7C9"
	replacementPack.Operations[0].Task.Title = "Replacement title"
	replacementState := writeState(t, nil, replacementPack)
	operationBlob := writeDocumentBlob(t, repo, replacementPack)
	stateBlob := writeDocumentBlob(t, repo, replacementState)
	tree := gitOutputWithInput(t, repo, []byte(fmt.Sprintf(
		"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		operationBlob,
		stateBlob,
	)), "mktree")
	replacementHead := gitOutput(t, repo, "commit-tree", tree, "-m", "replacement task")
	gitOutput(t, repo, "replace", original.Head, replacementHead)

	got, err := repo.Get(context.Background(), config, original.Operation.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("Get() followed replace ref:\n got %#v\nwant %#v", got, original)
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

func writeIndependentRoot(
	t *testing.T,
	repository *Repository,
	config core.ProjectConfig,
	taskID string,
	generationID string,
	operationID string,
	title string,
) core.Snapshot {
	t.Helper()
	pack := writeCreatePack()
	pack.TaskID = taskID
	pack.HistoryGeneration = generationID
	pack.Operations[0].ID = operationID
	pack.Operations[0].Task.Title = title
	state := writeState(t, nil, pack)
	snapshot, err := repository.Write(context.Background(), config, nil, pack, state, "create "+title)
	if err != nil {
		t.Fatalf("Write(%s) error = %v", taskID, err)
	}
	return snapshot
}

func writeRepositoryWithObjectFormat(t *testing.T, objectFormat string) (*Repository, core.ProjectConfig) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	if _, err := runGit(
		context.Background(),
		gitPath,
		repositoryRoot,
		nil,
		"init",
		"--quiet",
		"--object-format="+objectFormat,
	); err != nil {
		if objectFormat == "sha256" {
			t.Skipf("Git does not support SHA-256 repositories: %v", err)
		}
		t.Fatalf("git init --object-format=%s: %v", objectFormat, err)
	}
	if _, err := runGit(context.Background(), gitPath, repositoryRoot, nil, "config", "user.name", "Workbook Test"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(context.Background(), gitPath, repositoryRoot, nil, "config", "user.email", "workbook@example.test"); err != nil {
		t.Fatal(err)
	}
	repository, err := Open(context.Background(), repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := repository.Init(context.Background(), "WB", idsFor(writeProjectID))
	if err != nil {
		t.Fatal(err)
	}
	return repository, config
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
