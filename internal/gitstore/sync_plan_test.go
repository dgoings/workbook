package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestClassifyTaskHeadsUsesOneGraphForUnequalPairs(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			repository, config := writeRepositoryWithObjectFormat(t, objectFormat)
			equalRoot, _, _ := writeRoot(t, repository, config)
			remoteRoot := writeIndependentRoot(
				t, repository, config,
				"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
				"01K0M6B8A4FTT8C39MXXYTW7C7",
				"01K0M6B8A4FTT8C39MXXYTW7C8",
				"Remote child",
			)
			remotePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
			remotePack.TaskID = remoteRoot.Operation.TaskID
			remotePack.HistoryGeneration = remoteRoot.Operation.HistoryGeneration
			remoteState := writeState(t, &remoteRoot.State, remotePack)
			remoteChild, err := repository.Write(context.Background(), config, &remoteRoot, remotePack, remoteState, "remote child")
			if err != nil {
				t.Fatal(err)
			}

			localRoot := writeIndependentRoot(
				t, repository, config,
				"WB-01K0M6B8A4FTT8C39MXXYTW7D0",
				"01K0M6B8A4FTT8C39MXXYTW7E1",
				"01K0M6B8A4FTT8C39MXXYTW7E2",
				"Local child",
			)
			localPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C9", string(core.StatusReady))
			localPack.TaskID = localRoot.Operation.TaskID
			localPack.HistoryGeneration = localRoot.Operation.HistoryGeneration
			localState := writeState(t, &localRoot.State, localPack)
			localChild, err := repository.Write(context.Background(), config, &localRoot, localPack, localState, "local child")
			if err != nil {
				t.Fatal(err)
			}

			divergentRoot := writeIndependentRoot(
				t, repository, config,
				"WB-01K0M6B8A4FTT8C39MXXYTW7F3",
				"01K0M6B8A4FTT8C39MXXYTW7F4",
				"01K0M6B8A4FTT8C39MXXYTW7F5",
				"Divergent",
			)
			divergentPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7E3", string(core.StatusReady))
			divergentPack.TaskID = divergentRoot.Operation.TaskID
			divergentPack.HistoryGeneration = divergentRoot.Operation.HistoryGeneration
			divergentState := writeState(t, &divergentRoot.State, divergentPack)
			firstChild, err := repository.Write(context.Background(), config, &divergentRoot, divergentPack, divergentState, "first child")
			if err != nil {
				t.Fatal(err)
			}
			sibling := gitOutput(t, repository, "commit-tree", gitOutput(t, repository, "rev-parse", firstChild.Head+"^{tree}"), "-p", divergentRoot.Head, "-m", "sibling child")
			siblingChild := firstChild
			siblingChild.Head = sibling

			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			results, err := repository.classifyTaskHeadRelationships(context.Background(), config, []taskHeadPair{
				{TaskID: equalRoot.Operation.TaskID, Local: equalRoot, Remote: equalRoot},
				{TaskID: remoteRoot.Operation.TaskID, Local: remoteRoot, Remote: remoteChild},
				{TaskID: localRoot.Operation.TaskID, Local: localChild, Remote: localRoot},
				{TaskID: divergentRoot.Operation.TaskID, Local: firstChild, Remote: siblingChild},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []taskHeadRelationshipResult{
				{TaskID: equalRoot.Operation.TaskID, Relationship: taskHeadsEqual},
				{TaskID: remoteRoot.Operation.TaskID, Relationship: taskHeadsRemoteAhead},
				{TaskID: localRoot.Operation.TaskID, Relationship: taskHeadsLocalAhead},
				{TaskID: divergentRoot.Operation.TaskID, Relationship: taskHeadsDiverged},
			}
			if !reflect.DeepEqual(results, want) {
				t.Fatalf("relationships = %#v, want %#v", results, want)
			}
			if got := countCommand(commands, "rev-list", "--parents", "--stdin"); got != 1 {
				t.Fatalf("rev-list commands = %d, want one for unequal heads; commands = %v", got, commands)
			}
			if got := countCommand(commands, "merge-base", "--is-ancestor"); got != 0 {
				t.Fatalf("merge-base commands = %d, want none; commands = %v", got, commands)
			}
		})
	}
}

func TestClassifyTaskHeadsSkipsGraphWhenAllPairsAreEqual(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)
	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}

	results, err := repository.classifyTaskHeadRelationships(context.Background(), config, []taskHeadPair{{
		TaskID: snapshot.Operation.TaskID,
		Local:  snapshot,
		Remote: snapshot,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []taskHeadRelationshipResult{{TaskID: snapshot.Operation.TaskID, Relationship: taskHeadsEqual}}; !reflect.DeepEqual(results, want) {
		t.Fatalf("relationships = %#v, want %#v", results, want)
	}
	if got := countCommand(commands, "rev-list", "--parents", "--stdin"); got != 0 {
		t.Fatalf("rev-list commands = %d, want none for equal heads; commands = %v", got, commands)
	}
}

func TestClassifyTaskHeadsRejectsInvalidPairsBeforeGraph(t *testing.T) {
	repository, config := writeRepository(t)
	snapshot, _, _ := writeRoot(t, repository, config)

	for _, test := range []struct {
		name  string
		pairs []taskHeadPair
	}{
		{
			name: "duplicate task ID",
			pairs: []taskHeadPair{
				{TaskID: snapshot.Operation.TaskID, Local: snapshot, Remote: snapshot},
				{TaskID: snapshot.Operation.TaskID, Local: snapshot, Remote: snapshot},
			},
		},
		{name: "malformed task ID", pairs: []taskHeadPair{{TaskID: "not-a-task", Local: snapshot, Remote: snapshot}}},
		{name: "abbreviated local ID", pairs: []taskHeadPair{{
			TaskID: snapshot.Operation.TaskID,
			Local:  core.Snapshot{Head: snapshot.Head[:len(snapshot.Head)-2], Operation: snapshot.Operation, State: snapshot.State},
			Remote: snapshot,
		}}},
		{name: "generation mismatch", pairs: []taskHeadPair{{
			TaskID: snapshot.Operation.TaskID,
			Local:  snapshot,
			Remote: func() core.Snapshot {
				remote := snapshot
				remote.Head = strings.Repeat("a", len(snapshot.Head))
				remote.Operation.HistoryGeneration = "01K0M6B8A4FTT8C39MXXYTW7D9"
				remote.State.History.Generation = "01K0M6B8A4FTT8C39MXXYTW7D9"
				return remote
			}(),
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var commands [][]string
			repository.commandObserver = func(args []string) {
				commands = append(commands, append([]string(nil), args...))
			}
			_, err := repository.classifyTaskHeadRelationships(context.Background(), config, test.pairs)
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("classifyTaskHeadRelationships() category = %q, want %q; error = %v", got, want, err)
			}
			if got := countCommand(commands, "rev-list", "--parents", "--stdin"); got != 0 {
				t.Fatalf("rev-list commands = %d, want none for invalid pairs; commands = %v", got, commands)
			}
		})
	}
}

func TestCompleteParentGraphRejectsMissingReferencedCommit(t *testing.T) {
	head := strings.Repeat("a", 40)
	missingParent := strings.Repeat("b", 40)
	err := validateCompleteParentGraph(map[string][]string{
		head: {missingParent},
	})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("validateCompleteParentGraph() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestUpdateCanonicalRefsUsesOneCompareAndSwapTransaction(t *testing.T) {
	repository, config := writeRepository(t)
	current, currentPack, currentState := writeRoot(t, repository, config)
	updatePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	updated, err := repository.Write(context.Background(), config, &current, updatePack, writeState(t, &currentState, updatePack), "advance")
	if err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repository, "update-ref", taskRef(currentPack.TaskID), current.Head)

	created, createdPack, _ := writeRootForTask(
		t, repository, config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Created in transaction",
	)
	gitOutput(t, repository, "update-ref", "-d", taskRef(createdPack.TaskID))

	capturedInput := captureNextGitStdin(t, repository)
	var commands [][]string
	repository.commandObserver = func(args []string) {
		commands = append(commands, append([]string(nil), args...))
	}
	err = repository.updateCanonicalRefs(context.Background(), config, []canonicalRefUpdate{
		{TaskID: currentPack.TaskID, Next: updated.Head, Expected: current.Head},
		{TaskID: createdPack.TaskID, Next: created.Head},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := countCommand(commands, "update-ref", "--no-deref", "--create-reflog", "-m", "workbook: fetch origin", "--stdin"); got != 1 {
		t.Fatalf("update-ref commands = %d, want one; commands = %v", got, commands)
	}
	if got, want := string(mustReadFile(t, capturedInput)), "start\noption no-deref\nupdate "+taskRef(currentPack.TaskID)+" "+updated.Head+" "+current.Head+"\ncreate "+taskRef(createdPack.TaskID)+" "+created.Head+"\nprepare\ncommit\n"; got != want {
		t.Fatalf("update-ref stdin = %q, want %q", got, want)
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == "update-ref" {
			if strings.Contains(strings.Join(command, " "), "--force") || strings.Contains(strings.Join(command, " "), " delete") {
				t.Fatalf("update-ref command = %v, must not force or delete", command)
			}
		}
	}
	if got := gitOutput(t, repository, "rev-parse", taskRef(currentPack.TaskID)); got != updated.Head {
		t.Fatalf("updated ref = %q, want %q", got, updated.Head)
	}
	if got := gitOutput(t, repository, "rev-parse", taskRef(createdPack.TaskID)); got != created.Head {
		t.Fatalf("created ref = %q, want %q", got, created.Head)
	}
}

func TestUpdateCanonicalRefsAbortsEntireTransactionOnStaleRef(t *testing.T) {
	repository, config := writeRepository(t)
	current, currentPack, currentState := writeRoot(t, repository, config)
	advancedPack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	advanced, err := repository.Write(context.Background(), config, &current, advancedPack, writeState(t, &currentState, advancedPack), "advance")
	if err != nil {
		t.Fatal(err)
	}
	created, createdPack, _ := writeRootForTask(
		t, repository, config,
		"WB-01K0M6B8A4FTT8C39MXXYTW7C6",
		"01K0M6B8A4FTT8C39MXXYTW7C7",
		"01K0M6B8A4FTT8C39MXXYTW7C8",
		"Must not be created",
	)
	gitOutput(t, repository, "update-ref", "-d", taskRef(createdPack.TaskID))
	syncGit(t, repository.Root, "update-ref", taskRef(currentPack.TaskID), current.Head, advanced.Head)

	raced := false
	repository.commandObserver = func(args []string) {
		if raced || len(args) == 0 || args[0] != "update-ref" || args[len(args)-1] != "--stdin" {
			return
		}
		raced = true
		syncGit(t, repository.Root, "update-ref", taskRef(currentPack.TaskID), advanced.Head, current.Head)
	}
	err = repository.updateCanonicalRefs(context.Background(), config, []canonicalRefUpdate{
		{TaskID: currentPack.TaskID, Next: advanced.Head, Expected: current.Head},
		{TaskID: createdPack.TaskID, Next: created.Head},
	})
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("updateCanonicalRefs() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repository, "rev-parse", taskRef(currentPack.TaskID)); got != advanced.Head {
		t.Fatalf("stale ref = %q, want unchanged %q", got, advanced.Head)
	}
	if _, err := repository.Git(context.Background(), nil, "rev-parse", "--verify", taskRef(createdPack.TaskID)); err == nil {
		t.Fatalf("create ref %q exists after aborted transaction", taskRef(createdPack.TaskID))
	}
}

func TestUpdateCanonicalRefsRejectsExistingSymbolicCanonicalRef(t *testing.T) {
	repository, config := writeRepository(t)
	current, pack, state := writeRoot(t, repository, config)
	updatePack := writeUpdatePack(2, "01K0M6B8A4FTT8C39MXXYTW7C5", string(core.StatusReady))
	updated, err := repository.Write(
		context.Background(),
		config,
		&current,
		updatePack,
		writeState(t, &state, updatePack),
		"advance",
	)
	if err != nil {
		t.Fatal(err)
	}
	codeRef := "refs/heads/symbolic-target"
	syncGit(t, repository.Root, "update-ref", codeRef, current.Head)
	syncGit(t, repository.Root, "symbolic-ref", taskRef(pack.TaskID), codeRef)

	err = repository.updateCanonicalRefs(context.Background(), config, []canonicalRefUpdate{{
		TaskID:   pack.TaskID,
		Next:     updated.Head,
		Expected: current.Head,
	}})
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("updateCanonicalRefs() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repository, "rev-parse", codeRef); got != current.Head {
		t.Fatalf("code ref = %q, want unchanged %q", got, current.Head)
	}
	if got := gitOutput(t, repository, "symbolic-ref", taskRef(pack.TaskID)); got != codeRef {
		t.Fatalf("canonical symref target = %q, want unchanged %q", got, codeRef)
	}
}

func captureNextGitStdin(t *testing.T, repository *Repository) string {
	t.Helper()
	capturePath := filepath.Join(t.TempDir(), "update-ref.stdin")
	fakeGit := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fakeGit, []byte(fmt.Sprintf("#!/bin/sh\ncat > %q\nexec %q \"$@\" < %q\n", capturePath, repository.gitPath, capturePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	repository.gitPath = fakeGit
	return capturePath
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func writeRootForTask(
	t *testing.T,
	repository *Repository,
	config core.ProjectConfig,
	taskID string,
	generationID string,
	operationID string,
	title string,
) (core.Snapshot, core.OperationPack, core.StateDocument) {
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
	return snapshot, pack, state
}
