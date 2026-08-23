package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// boardActorEmail is the identity testrepo configures, which is what a board
// bound to that checkout records an assignment against.
const boardActorEmail = "workbook@example.test"

// The assignment routes through the real serve wiring.
//
// A handler test cannot show any of this, for the reason the thread routes'
// integration test records: the webui package holds its own fakes, so the two
// function literals `runServe` hands the board could be missing, or closed over
// a service with no identity, and the whole package would stay green. The
// identity is the half worth proving here — an assignment made from this board
// has to be recorded against the same `user.email` a commit from this worktree
// carries, or it is an assignment nobody can be held to and nobody may withdraw.
func TestRunServeAssignsAndWithdrawsThroughWebRoutes(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "create", "Assigned through the board", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create = (%d, %q, %q)", code, stdout, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	addr := startServeBoard(t, repository)
	assignments := "http://" + addr + "/api/tasks/" + task.ID + "/assignments"

	// An empty value is the board's own identity, resolved by core out of the
	// repository this server is bound to.
	body, status := boardRequest(t, http.MethodPost, assignments,
		`{"to":"","expectedHead":"`+task.Head+`"}`)
	assigned, views := decodeServeAssignment(t, body, status)
	if len(assigned.Assignments) != 1 {
		t.Fatalf("assign returned %#v, want one assignment", assigned.Assignments)
	}
	self := assigned.Assignments[0]
	if self.Principal != boardActorEmail || self.Creator != boardActorEmail || self.Label != "" {
		t.Fatalf("the board assigned %#v, want this repository's own identity %q", self, boardActorEmail)
	}
	if assigned.Head == task.Head {
		t.Fatal("an assignment did not advance the task ref")
	}
	if head := gitOutput(t, repository, "rev-parse", "--verify", "refs/workbook/tasks/"+task.ID); head != assigned.Head {
		t.Fatalf("task ref = %s, want the head the answer reported %s", head, assigned.Head)
	}
	// The section the page draws travels with the answer, so the panel redraws
	// from the server's derivation rather than composing one.
	if len(views) != 1 || views[0].Ago == "" || !views[0].Removable {
		t.Fatalf("the answer's assignment views = %#v, want one this board may withdraw", views)
	}

	// A second agent of the same identity, and somebody else beside it: assigning
	// from a board is additive, never the claim gate, which is the difference
	// between this surface and `workbook update --assign` without --force.
	body, status = boardRequest(t, http.MethodPost, assignments,
		`{"to":"`+boardActorEmail+`/impl-1","expectedHead":"`+assigned.Head+`"}`)
	paired, _ := decodeServeAssignment(t, body, status)
	body, status = boardRequest(t, http.MethodPost, assignments,
		`{"to":"sam@example.com","expectedHead":"`+paired.Head+`"}`)
	shared, sharedViews := decodeServeAssignment(t, body, status)
	if len(shared.Assignments) != 3 {
		t.Fatalf("the task carries %#v, want three assignments", shared.Assignments)
	}
	// Somebody else's, recorded by this board: removable, because the actor who
	// recorded a tag may undo it. That is core's rule, and the view is what
	// carries it to the page.
	for _, view := range sharedViews {
		if !view.Removable {
			t.Fatalf("this board may not withdraw an assignment it recorded: %#v", view)
		}
	}

	// A head the caller has moved past is refused here as everywhere.
	body, status = boardRequest(t, http.MethodDelete, assignments,
		`{"from":"`+boardActorEmail+`","expectedHead":"`+task.Head+`"}`)
	if status != http.StatusConflict {
		t.Fatalf("stale withdrawal = %d, want %d; body = %s", status, http.StatusConflict, body)
	}

	// A principal that is not an address is the mutation boundary's refusal, in
	// core's own words rather than in words this package invented.
	body, status = boardRequest(t, http.MethodPost, assignments, `{"to":"dylan"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "must be an email address") {
		t.Fatalf("assigning a bare name = %d; body = %s", status, body)
	}

	body, status = boardRequest(t, http.MethodDelete, assignments,
		`{"from":"`+boardActorEmail+`/impl-1","expectedHead":"`+shared.Head+`"}`)
	withdrawn, _ := decodeServeAssignment(t, body, status)
	if len(withdrawn.Assignments) != 2 {
		t.Fatalf("withdrawal left %#v, want two assignments", withdrawn.Assignments)
	}
	for _, assignment := range withdrawn.Assignments {
		if assignment.Label == "impl-1" {
			t.Fatalf("the withdrawn assignment is still there: %#v", withdrawn.Assignments)
		}
	}

	// And the command line reads exactly what the board wrote, which is the whole
	// claim: one history, two surfaces.
	code, stdout, stderr = run(t, repository, "show", task.ID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("show = (%d, %q, %q)", code, stdout, stderr)
	}
	var shown struct {
		Data core.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if len(shown.Data.Assignments) != 2 {
		t.Fatalf("the CLI reads %#v, want the two the board left", shown.Data.Assignments)
	}
}

// servedAssignment is one row of the section an assignment route answers with,
// decoded here rather than imported so that this test reads the wire the page
// reads rather than the struct the server encodes from.
type servedAssignment struct {
	Principal string `json:"principal"`
	Label     string `json:"label"`
	Ago       string `json:"ago"`
	Removable bool   `json:"removable"`
}

// decodeServeAssignment reads the task and the assignment section one of the two
// routes answers with.
func decodeServeAssignment(t *testing.T, body []byte, status int) (core.Task, []servedAssignment) {
	t.Helper()
	task := decodeServeMutation(t, body, status)
	var document struct {
		Assignments []servedAssignment `json:"assignments"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the assignment section: %v; body = %s", err, body)
	}
	return task, document.Assignments
}
