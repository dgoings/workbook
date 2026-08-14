package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

const (
	firstAgent  = "dylan@example.com"
	secondAgent = "sam@example.com"
	hostile     = "mallory@example.com"
)

// assignmentService is syncService with a chosen acting identity, which is the
// only thing the removal rule turns on.
func assignmentService(repo *Repository, config core.ProjectConfig, actor string) core.Service {
	service := syncService(repo, config)
	service.Actor = actor
	return service
}

func assignSyncTask(t *testing.T, repo *Repository, config core.ProjectConfig, actor, taskID, to string) {
	t.Helper()
	service := assignmentService(repo, config, actor)
	if _, err := service.AssignMutation(context.Background(), taskID, core.AssignInput{To: to}); err != nil {
		t.Fatalf("AssignMutation(%s by %s) error = %v", to, actor, err)
	}
}

func taskAssignments(t *testing.T, repo *Repository, config core.ProjectConfig, taskID string) []string {
	t.Helper()
	snapshot, err := repo.Get(context.Background(), config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	values := make([]string, 0, len(snapshot.State.Task.Assignments))
	for _, assignment := range snapshot.State.Task.Assignments {
		values = append(values, assignment.Value())
	}
	return values
}

// The design's spike state, over a real bare remote.
//
// Two agents claim the same task at the same time, neither knowing about the
// other. Nothing is a conflict, nothing is resolved by a person, and nothing is
// lost: the task ends up assigned to both on both clones, which is a meaningful
// outcome — two people are spiking it — rather than a fight over one slot.
func TestConcurrentSelfAssignsFromTwoClonesConvergeToBothAssigned(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Contended task")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	assignSyncTask(t, first, config, firstAgent, task.ID, "")
	assignSyncTask(t, second, config, secondAgent, task.ID, "")
	publishTaskRefs(t, first)

	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(diverged assignments) error = %v", err)
	}
	publishTaskRefs(t, second)
	if _, err := first.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(back) error = %v", err)
	}

	want := []string{firstAgent, secondAgent}
	for name, repo := range map[string]*Repository{"first": first, "second": second} {
		if got := taskAssignments(t, repo, config, task.ID); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s clone assignments = %#v, want %#v", name, got, want)
		}
	}
}

// A fleet's assignments and their sweep survive a round trip, which is the
// orchestrator case: several agents of one principal, cleared by that principal.
func TestAFleetsAssignmentsAndItsSweepReplicate(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Fleet task")
	assignSyncTask(t, first, config, firstAgent, task.ID, firstAgent+"/impl-1")
	assignSyncTask(t, first, config, firstAgent, task.ID, firstAgent+"/impl-2")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if got, want := taskAssignments(t, second, config, task.ID),
		[]string{firstAgent + "/impl-1", firstAgent + "/impl-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fetched assignments = %#v, want %#v", got, want)
	}

	// The orchestrator sweeps both, including the one whose agent crashed.
	service := assignmentService(first, config, firstAgent)
	for _, value := range []string{firstAgent + "/impl-1", firstAgent + "/impl-2"} {
		if _, err := service.UnassignMutation(context.Background(), task.ID, core.UnassignInput{From: value}); err != nil {
			t.Fatalf("UnassignMutation(%s) error = %v", value, err)
		}
	}
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if got := taskAssignments(t, second, config, task.ID); len(got) != 0 {
		t.Fatalf("assignments after the sweep = %#v, want none", got)
	}
}

// THE FOLD, over the wire. A removal nobody authorized is crafted with Git
// plumbing on one side — the way a modified build or a hand-rolled script would
// write it — pushed, and fetched by an honest reader, which folds it to
// nothing.
//
// The pack is written by hand rather than through the service on purpose: the
// service would have refused it, and a test that only exercised the boundary
// would prove nothing about the layer that has to hold when the boundary is not
// in the picture.
func TestAForeignRemovalCraftedOnOneSideFoldsToANoOpOnTheOther(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Hostile removal")
	assignSyncTask(t, first, config, firstAgent, task.ID, "")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	craftAssignRemoveCommit(t, second, config, task.ID, hostile, firstAgent)
	publishTaskRefs(t, second)

	if _, err := first.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(hostile removal) error = %v; a foreign removal must fold, not fail", err)
	}
	if got, want := taskAssignments(t, first, config, task.ID), []string{firstAgent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v; the removal had no authority", got, want)
	}
	// It is history, not a rejection: the fetched tip is the one the hostile
	// clone pushed, and the operation is in the chain.
	head := refValue(t, first, taskRefPrefix+task.ID)
	if got := gitAssignmentOperations(t, first, head); !contains(got, string(core.OperationAssignRemove)) {
		t.Fatalf("the recorded chain has no assign.remove: %#v", got)
	}
	// And the assignee can still remove their own assignment afterwards, so the
	// hostile pack left nothing wedged.
	service := assignmentService(first, config, firstAgent)
	if _, err := service.UnassignMutation(context.Background(), task.ID, core.UnassignInput{}); err != nil {
		t.Fatalf("UnassignMutation() after the hostile pack error = %v", err)
	}
	if got := taskAssignments(t, first, config, task.ID); len(got) != 0 {
		t.Fatalf("assignments = %#v, want none", got)
	}
}

// Divergence replay carries an assignment operation onto a fetched tip without
// rewriting its attribution — the creator and the creation time are the removal
// rule's evidence, and a replay that reissued them would change who may remove
// what.
func TestDivergenceReplayPreservesAssignmentAttribution(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Replayed assignment")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// The second clone tags a teammate while offline; the first clone renames
	// the task and publishes, so the local pack has to be replayed.
	assignSyncTask(t, second, config, secondAgent, task.ID, firstAgent+"/impl-1")
	before := assignmentRecord(t, second, config, task.ID, firstAgent+"/impl-1")
	updateSyncTask(t, first, config, task.ID, "Renamed upstream")
	publishTaskRefs(t, first)

	result, err := second.Fetch(context.Background(), config)
	if err != nil {
		t.Fatalf("Fetch(diverged) error = %v", err)
	}
	assertSyncOutcome(t, result, task.ID, SyncReconciled)

	after := assignmentRecord(t, second, config, task.ID, firstAgent+"/impl-1")
	if after.Creator != before.Creator {
		t.Fatalf("replayed creator = %q, want %q", after.Creator, before.Creator)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("replayed creation time = %s, want %s", after.CreatedAt, before.CreatedAt)
	}
	snapshot, err := second.Get(context.Background(), config, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.State.Task.Title, "Renamed upstream"; got != want {
		t.Fatalf("replayed title = %q, want the fetched %q", got, want)
	}
	// The teammate whose assignment this is may still remove it, and the actor
	// who recorded it may too. Both are decided from the replayed record.
	if !after.RemovableBy(firstAgent) || !after.RemovableBy(secondAgent) {
		t.Fatalf("replayed assignment %#v lost one of its removal branches", after)
	}
}

// A replayed assignment the fetched history already carries records no commit,
// because it changed nothing an operator can see. Without that, every
// synchronization of an already-published assignment would append an empty
// entry to shared history.
func TestReplayingAnAssignmentUpstreamAlreadyHasRecordsNoCommit(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Doubly assigned")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	// Both clones record the same assignment, and the first publishes.
	assignSyncTask(t, second, config, secondAgent, task.ID, firstAgent)
	assignSyncTask(t, first, config, firstAgent, task.ID, firstAgent)
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)

	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(duplicate assignment) error = %v", err)
	}
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("reconciled tip = %q, want the fetched tip %q; a redundant replay must record nothing", got, remoteTip)
	}
	if got, want := taskAssignments(t, second, config, task.ID), []string{firstAgent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}
}

// A foreign removal replayed onto a fetched tip is likewise dropped rather than
// recorded: the fold makes it a no-op, and a no-op earns no commit.
func TestReplayingAForeignRemovalRecordsNoCommit(t *testing.T) {
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Foreign replay")
	assignSyncTask(t, first, config, firstAgent, task.ID, "")
	publishTaskRefs(t, first)
	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	craftAssignRemoveCommit(t, second, config, task.ID, hostile, firstAgent)
	updateSyncTask(t, first, config, task.ID, "Renamed upstream")
	publishTaskRefs(t, first)
	remoteTip := refValue(t, first, taskRefPrefix+task.ID)

	if _, err := second.Fetch(context.Background(), config); err != nil {
		t.Fatalf("Fetch(foreign replay) error = %v", err)
	}
	if got := refValue(t, second, taskRefPrefix+task.ID); got != remoteTip {
		t.Fatalf("reconciled tip = %q, want the fetched tip %q; a no-op replay must record nothing", got, remoteTip)
	}
	if got, want := taskAssignments(t, second, config, task.ID), []string{firstAgent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, want %#v", got, want)
	}
}

// assignmentRecord reads one stored assignment by value.
func assignmentRecord(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, value string) core.Assignment {
	t.Helper()
	snapshot, err := repo.Get(context.Background(), config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	for _, assignment := range snapshot.State.Task.Assignments {
		if assignment.Value() == value {
			return assignment
		}
	}
	t.Fatalf("task %s carries no assignment %q; it has %#v", taskID, value, snapshot.State.Task.Assignments)
	return core.Assignment{}
}

// craftAssignRemoveCommit appends an assign.remove pack authored by an actor
// the removal rule does not entitle, using Git plumbing so that nothing in
// Workbook's mutation boundary ever sees it.
//
// The checkpoint it writes is the one Apply computes, which is the point: a
// hostile writer that wanted its removal to stick would still have to publish a
// checkpoint, and the honest reader recomputes that checkpoint from the pack.
// Writing the fold's own answer here is what makes the resulting commit a valid
// history whose operation simply has no effect — the case that actually has to
// hold. A checkpoint asserting the removal took effect would instead be caught
// as a checkpoint that differs from the computed state, which is a different
// and already-covered defence.
func craftAssignRemoveCommit(t *testing.T, repo *Repository, config core.ProjectConfig, taskID, actor, value string) string {
	t.Helper()
	ctx := context.Background()
	parent, err := repo.Get(ctx, config, taskID)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", taskID, err)
	}
	pack := core.NewOperationPack(
		config.ProjectID, taskID, parent.State.History.Generation, actor,
		parent.State.LogicalClock+1, time.Now().UTC(),
		[]core.Operation{{ID: newAssignmentULID(t), Type: core.OperationAssignRemove, Value: value}},
	)
	state, err := core.Apply(&parent.State, pack, config.Key)
	if err != nil {
		t.Fatalf("Apply(crafted removal) error = %v", err)
	}
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("EncodeDocument(pack) error = %v", err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument(state) error = %v", err)
	}

	head := refValue(t, repo, taskRefPrefix+taskID)
	packBlob := hashAssignmentObject(t, repo, packBytes)
	stateBlob := hashAssignmentObject(t, repo, stateBytes)
	tree := gitAssignmentInput(t, repo,
		fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", packBlob, stateBlob),
		"mktree")
	commit := gitAssignmentInput(t, repo, "workbook: unassign "+value, "commit-tree", tree, "-p", head)
	syncGit(t, repo.Root, "update-ref", taskRefPrefix+taskID, commit, head)
	return commit
}

// newAssignmentULID mints one operation ID for a crafted pack.
func newAssignmentULID(t *testing.T) string {
	t.Helper()
	id, err := core.CryptoULIDSource{}.New()
	if err != nil {
		t.Fatalf("CryptoULIDSource.New() error = %v", err)
	}
	return id
}

func hashAssignmentObject(t *testing.T, repo *Repository, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write object: %v", err)
	}
	return strings.TrimSpace(syncGit(t, repo.Root, "hash-object", "-w", path))
}

func gitAssignmentInput(t *testing.T, repo *Repository, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo.Root}, args...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// gitAssignmentOperations lists the operation types recorded along a task's
// chain, read out of Git rather than out of any projection.
func gitAssignmentOperations(t *testing.T, repo *Repository, head string) []string {
	t.Helper()
	types := make([]string, 0, 4)
	for _, commit := range strings.Fields(syncGit(t, repo.Root, "rev-list", head)) {
		document := syncGit(t, repo.Root, "show", commit+":operation.json")
		pack, err := core.DecodeOperationPack([]byte(document + "\n"))
		if err != nil {
			t.Fatalf("DecodeOperationPack(%s) error = %v", commit, err)
		}
		for _, operation := range pack.Operations {
			types = append(types, string(operation.Type))
		}
	}
	sort.Strings(types)
	return types
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
