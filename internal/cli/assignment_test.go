package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The identity every single-clone fixture here acts as; testrepo configures it.
const assignTestActor = "workbook@example.test"

// createReadyTask makes a task `workbook next` will consider, which is the only
// kind the claim path can act on.
func createReadyTask(t *testing.T, repository, title string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--status", "ready", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create %q code = %d, want 0; stderr = %q", title, code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

// showTask lives in thread_test.go, which reads a task back the same way.

func assignmentValues(assignments []core.Assignment) []string {
	values := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		values = append(values, assignment.Value())
	}
	return values
}

func assertAssignments(t *testing.T, task core.Task, want ...string) {
	t.Helper()
	got := assignmentValues(task.Assignments)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("task %s assignments = %q, want %q", task.ID, got, want)
	}
}

func warningCodes(warnings []core.Warning) []string {
	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

// `--assign self` is the shape an agent uses, and the whole record it leaves
// has to be readable afterwards: who holds it, who recorded it, and when.
func TestUpdateAssignSelfRecordsTheActingIdentity(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Assignable")

	code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", "self", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("update --assign code = %d, want 0; stderr = %q", code, stderr)
	}
	assigned := decodeMutationTask(t, stdout, "update")
	assertAssignments(t, assigned, assignTestActor)
	if got := assigned.Assignments[0]; got.Creator != assignTestActor || got.CreatedAt.IsZero() {
		t.Fatalf("assignment = %#v, want this identity as creator and a recording time", got)
	}

	code, stdout, stderr = run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, wanted := range []string{"Assignments:\t" + assignTestActor, "assigned just now"} {
		if !strings.Contains(stdout, wanted) {
			t.Errorf("show = %q, want %q", stdout, wanted)
		}
	}
}

// A labelled self-assignment is how one agent of a fleet says which agent it is,
// and the label is the only part it spells out.
func TestUpdateAssignSelfWithALabelUsesTheRepositoryIdentity(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Assignable")

	code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", "self/impl-1", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("update --assign self/impl-1 code = %d, want 0; stderr = %q", code, stderr)
	}
	assertAssignments(t, decodeMutationTask(t, stdout, "update"), assignTestActor+"/impl-1")
}

// The claim refusal: a distinct exit code, a category a program branches on, a
// message naming who holds it, and — the promise the code carries — nothing
// recorded at all.
func TestUpdateAssignRefusesATaskSomebodyElseHolds(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Theirs")
	if code, _, stderr := run(t, repository, "update", task.ID, "--assign", "sam@example.com/review", "--no-sync"); code != 0 {
		t.Fatalf("seed assignment code = %d, want 0; stderr = %q", code, stderr)
	}
	before := showTask(t, repository, task.ID)

	code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", "self", "--no-sync", "--json")
	if code != 10 {
		t.Fatalf("update --assign code = %d, want 10; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing on a refusal", stdout)
	}
	assertJSONError(t, stderr, core.CategoryAssigned, "")
	for _, wanted := range []string{task.ID, "sam@example.com/review", "--force"} {
		if !strings.Contains(stderr, wanted) {
			t.Errorf("refusal = %q, want %q", stderr, wanted)
		}
	}

	after := showTask(t, repository, task.ID)
	assertAssignments(t, after, "sam@example.com/review")
	if after.Head != before.Head {
		t.Fatalf("head moved from %q to %q; a refused claim writes nothing", before.Head, after.Head)
	}

	// And in text mode the same refusal reads as prose rather than as JSON.
	code, _, stderr = run(t, repository, "update", task.ID, "--assign", "self", "--no-sync")
	if code != 10 {
		t.Fatalf("text-mode code = %d, want 10", code)
	}
	assertHumanError(t, stderr, "sam@example.com/review")
}

// --force is the deliberate pairing, and it never removes anything: both
// assignments stand, and the command says so rather than reporting a clean win.
func TestUpdateAssignForceRecordsBesideTheOtherAssignment(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Shared")
	if code, _, stderr := run(t, repository, "update", task.ID, "--assign", "sam@example.com", "--no-sync"); code != 0 {
		t.Fatalf("seed assignment code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", "self", "--force", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("update --assign --force code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "update")
	if got, want := warningCodes(result.Warnings), []string{core.WarningAssignmentShared}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings = %q, want %q", got, want)
	}
	if !strings.Contains(result.Warnings[0].Message, "sam@example.com") {
		t.Fatalf("warning = %q, want it to name who else holds the task", result.Warnings[0].Message)
	}
	// Stored order is by principal, so the seeded assignment sorts first.
	assertAssignments(t, showTask(t, repository, task.ID), "sam@example.com", assignTestActor)
}

// Assigning yourself something you already hold changes nothing, says nothing
// alarming, and writes no commit.
func TestUpdateAssignIsQuietWhenYouAlreadyHoldTheTask(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Mine")
	if code, _, stderr := run(t, repository, "update", task.ID, "--assign", "self/impl-1", "--no-sync"); code != 0 {
		t.Fatalf("first assignment code = %d, want 0; stderr = %q", code, stderr)
	}
	before := showTask(t, repository, task.ID)

	code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", "self/impl-1", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("repeat assignment code = %d, want 0; stderr = %q", code, stderr)
	}
	if warnings := assertJSONResult(t, stdout, "update").Warnings; len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none for an idempotent assignment", warnings)
	}
	after := showTask(t, repository, task.ID)
	if after.Head != before.Head {
		t.Fatalf("head moved from %q to %q; re-assigning what you hold writes nothing", before.Head, after.Head)
	}
	assertAssignments(t, after, assignTestActor+"/impl-1")
}

// An assignment and a status given together are one change, which is what makes
// "take this task up" a single commit that cannot half-succeed.
func TestUpdateAssignComposesWithAStatusInOnePack(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Composed")
	before := gitOutput(t, repository, "rev-list", "--count", "refs/workbook/tasks/"+task.ID)

	code, stdout, stderr := run(t, repository,
		"update", task.ID, "--status", "in-progress", "--assign", "self", "--label", "claimed", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("composed update code = %d, want 0; stderr = %q", code, stderr)
	}
	updated := decodeMutationTask(t, stdout, "update")
	if updated.Status != core.StatusInProgress {
		t.Fatalf("status = %q, want in-progress", updated.Status)
	}
	assertAssignments(t, updated, assignTestActor)

	after := gitOutput(t, repository, "rev-list", "--count", "refs/workbook/tasks/"+task.ID)
	if before != "1" || after != "2" {
		t.Fatalf("commit count went %s → %s, want exactly one new commit for the whole change", before, after)
	}
	if subject := gitOutput(t, repository, "log", "-1", "--format=%s", "refs/workbook/tasks/"+task.ID); !strings.Contains(subject, "assign "+assignTestActor) ||
		!strings.Contains(subject, "status") {
		t.Fatalf("commit subject = %q, want it to name both changes", subject)
	}
}

// The two families that ride `update` compose with each other, not merely each
// with the fields.
//
// A status, a comment and an assignment in one invocation are one pack: the
// intents are members of one UpdateInput, so either all three landed or none
// did. This is what both stories promised separately, and it is only true
// jointly if neither of them reached for a write of its own.
func TestUpdateComposesAStatusACommentAndAnAssignmentInOnePack(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Composed three ways")
	before := gitOutput(t, repository, "rev-list", "--count", "refs/workbook/tasks/"+task.ID)

	code, stdout, stderr := run(t, repository,
		"update", task.ID,
		"--status", "in-progress",
		"--comment", "picking this up",
		"--assign", "self",
		"--no-sync", "--json")
	if code != 0 {
		t.Fatalf("composed update code = %d, want 0; stderr = %q", code, stderr)
	}
	updated := decodeMutationTask(t, stdout, "update")
	if updated.Status != core.StatusInProgress {
		t.Fatalf("status = %q, want in-progress", updated.Status)
	}
	if len(updated.Comments) != 1 || updated.Comments[0].Body != "picking this up" {
		t.Fatalf("comments = %#v, want the one this update carried", updated.Comments)
	}
	assertAssignments(t, updated, assignTestActor)

	after := gitOutput(t, repository, "rev-list", "--count", "refs/workbook/tasks/"+task.ID)
	if before != "1" || after != "2" {
		t.Fatalf("commit count went %s → %s, want exactly one new commit for all three intents", before, after)
	}
	// One pack, and one commit subject naming every intent in it.
	subject := gitOutput(t, repository, "log", "-1", "--format=%s", "refs/workbook/tasks/"+task.ID)
	for _, wanted := range []string{"status", "comment added", "assign " + assignTestActor} {
		if !strings.Contains(subject, wanted) {
			t.Errorf("commit subject = %q, want it to name %q", subject, wanted)
		}
	}
	// And the change log records them as one entry, which is the same claim read
	// from the other end.
	code, stdout, stderr = run(t, repository, "show", task.ID, "--history", "--json")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	var detail core.TaskDetail
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &detail); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if detail.History == nil || detail.History.Total != 2 {
		t.Fatalf("history = %#v, want the create and one update", detail.History)
	}
	fields := map[string]bool{}
	for _, change := range detail.History.Changes[len(detail.History.Changes)-1].Fields {
		fields[change.Field] = true
	}
	for _, wanted := range []string{"status", "comment", "assignments"} {
		if !fields[wanted] {
			t.Errorf("last change fields = %v, want %q among them", fields, wanted)
		}
	}
}

// The removal rule as somebody meets it: a foreign withdrawal is refused before
// anything is written, and the refusal says who to ask.
func TestUpdateUnassignSurfacesTheRemovalAuthorityRefusal(t *testing.T) {
	first, second := cliSyncRepositories(t)
	cliGit(t, first, "config", "user.email", "sam@example.com")
	cliGit(t, second, "config", "user.email", "dylan@example.com")

	task := cliCreateTask(t, first, "Sam's task")
	if code, _, stderr := run(t, first, "update", task.ID, "--assign", "self/impl-1"); code != 0 {
		t.Fatalf("assign code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch code = %d, want 0; stderr = %q", code, stderr)
	}

	code, _, stderr := run(t, second, "update", task.ID, "--unassign", "sam@example.com/impl-1", "--json")
	if code != 5 {
		t.Fatalf("foreign unassign code = %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	for _, wanted := range []string{"sam@example.com/impl-1", task.ID, "may be removed only by"} {
		if !strings.Contains(stderr, wanted) {
			t.Errorf("refusal = %q, want %q", stderr, wanted)
		}
	}
	assertAssignments(t, showTask(t, second, task.ID), "sam@example.com/impl-1")

	// The principal itself may withdraw it, which is the other half of the rule.
	if code, _, stderr := run(t, first, "update", task.ID, "--unassign", "self/impl-1"); code != 0 {
		t.Fatalf("own unassign code = %d, want 0; stderr = %q", code, stderr)
	}
	assertAssignments(t, showTask(t, first, task.ID))
}

// The flag combinations that have no single meaning are refused before anything
// is opened, in the invocation category every other argument error uses.
func TestUpdateAssignmentFlagCombinationsAreRefused(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Guarded")

	for name, test := range map[string]struct {
		args    []string
		message string
	}{
		"assign twice":   {args: []string{"--assign", "self", "--assign", "sam@example.com"}, message: "accepts --assign once"},
		"unassign twice": {args: []string{"--unassign", "self", "--unassign", "sam@example.com"}, message: "accepts --unassign once"},
		"assign and unassign": {args: []string{"--assign", "self", "--unassign", "sam@example.com"},
			message: "--assign or --unassign, not both"},
		"force without assign": {args: []string{"--force", "--status", "ready"}, message: "--force requires --assign"},
		"blank assignee":       {args: []string{"--assign", ""}, message: "assignment must not be blank"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"update", task.ID}, test.args...)
			code, stdout, stderr := run(t, repository, append(args, "--no-sync")...)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stderr = %q", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing", stdout)
			}
			assertHumanError(t, stderr, test.message)
		})
	}
}

// The boundary judges an identity somebody typed, which the fold deliberately
// does not, and a value that would break the grammars it has to survive is
// refused rather than stored.
func TestUpdateAssignRefusesValuesTheBoundaryWillNotAuthor(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Guarded")

	for name, value := range map[string]string{
		"no domain":                "dylan",
		"no dot":                   "dylan@localhost",
		"empty label":              "dylan@example.com/",
		"embedded space":           "dylan@example.com/impl 1",
		"embedded tab":             "dylan@example.com/impl\t1",
		"embedded newline":         "dylan@example.com/impl\n1",
		"no principal":             "/impl-1",
		"self with no slash label": "self/",
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, "update", task.ID, "--assign", value, "--no-sync", "--json")
			if code != 5 {
				t.Fatalf("assign %q code = %d, want 5; stderr = %q", value, code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing", stdout)
			}
			assertJSONError(t, stderr, core.CategoryValidation, "")
		})
	}
	assertAssignments(t, showTask(t, repository, task.ID))
}

// A hostile label cannot reshape the output it is printed into: whatever
// whitespace it carries, one assignment is one line, and no line it produces can
// be read as a field of its own.
func TestShowRendersAHostileAssignmentOnOneLine(t *testing.T) {
	repository := initializedRepository(t)
	task := createReadyTask(t, repository, "Hostile")
	// A no-break space and a right-to-left override both pass the fold, which
	// deliberately excludes only the characters that would break a commit
	// subject, a shell word or an HTML attribute. What keeps them harmless here
	// is the renderer.
	const label = "impl Status:‮done"
	if code, _, stderr := run(t, repository, "update", task.ID, "--assign", "eve@example.com/"+label, "--no-sync"); code != 0 {
		t.Fatalf("assign code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	var assignmentLines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "eve@example.com") {
			assignmentLines = append(assignmentLines, line)
		}
	}
	if len(assignmentLines) != 1 {
		t.Fatalf("show = %q, want the assignment on exactly one line", stdout)
	}
	if !strings.HasPrefix(assignmentLines[0], "Assignments:\t") {
		t.Fatalf("assignment line = %q, want it under the Assignments field", assignmentLines[0])
	}
	if strings.Contains(stdout, " ") {
		t.Fatalf("show = %q, want the no-break space collapsed rather than printed", stdout)
	}
	// The forged field text is still visible — it is somebody's label — but it
	// is inside the assignment line rather than at the start of a line, which is
	// what would let it read as a field.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Status:") && !strings.HasSuffix(line, "ready") {
			t.Fatalf("show = %q, want no forged status field", stdout)
		}
	}
}

// The default skip: work another principal is responsible for is work that is
// being done, and offering it again is how a fleet duplicates one task.
func TestNextSkipsTasksAssignedToOtherPrincipals(t *testing.T) {
	repository := initializedRepository(t)
	theirs := createReadyTask(t, repository, "Theirs")
	mine := createReadyTask(t, repository, "Mine")
	if code, _, stderr := run(t, repository, "update", theirs.ID, "--assign", "sam@example.com", "--no-sync"); code != 0 {
		t.Fatalf("assign code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != mine.ID {
		t.Fatalf("next = %#v, want the unheld task %s", got, mine.ID)
	}

	code, stdout, stderr = run(t, repository, "next", "--any", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next --any code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := decodeNextTask(t, stdout); got == nil || got.ID != theirs.ID {
		t.Fatalf("next --any = %#v, want the whole eligible set back, starting at %s", got, theirs.ID)
	}
}

// "There is no work" and "the work is being done" are different answers, and a
// caller that cannot tell them apart concludes the board is empty.
func TestNextSaysWhenEveryEligibleTaskIsHeldBySomebodyElse(t *testing.T) {
	repository := initializedRepository(t)
	theirs := createReadyTask(t, repository, "Theirs")
	if code, _, stderr := run(t, repository, "update", theirs.ID, "--assign", "sam@example.com", "--no-sync"); code != 0 {
		t.Fatalf("assign code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "next", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "next")
	if string(result.Data) != "null" {
		t.Fatalf("next data = %s, want null", result.Data)
	}
	if got, want := warningCodes(result.Warnings), []string{core.WarningNextHeldByOthers}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings = %q, want %q", got, want)
	}

	code, stdout, stderr = run(t, repository, "next", "--no-sync")
	if code != 0 {
		t.Fatalf("text next code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "No eligible task.") {
		t.Fatalf("stdout = %q, want the empty answer", stdout)
	}
	if !strings.Contains(stderr, "assigned to somebody else") || !strings.Contains(stderr, "--any") {
		t.Fatalf("stderr = %q, want the explanation and the flag that lifts the skip", stderr)
	}
}

// A board with nothing eligible answers the same way whether or not a claim was
// asked for: nothing to do is not a failure.
func TestNextClaimReportsAnEmptyBoardWithoutFailing(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "next", "--claim", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next --claim code = %d, want 0; stderr = %q", code, stderr)
	}
	if string(assertJSONResult(t, stdout, "next").Data) != "null" {
		t.Fatalf("next --claim data = %s, want null", stdout)
	}
}

// The claim itself: one command picks the task next would pick and records the
// assignment, so two agents asking at once cannot both walk away with it.
func TestNextClaimAssignsTheTaskItPicked(t *testing.T) {
	repository := initializedRepository(t)
	first := createReadyTask(t, repository, "First")
	createReadyTask(t, repository, "Second")

	code, stdout, stderr := run(t, repository, "next", "--claim", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("next --claim code = %d, want 0; stderr = %q", code, stderr)
	}
	claimed := decodeMutationTask(t, stdout, "next")
	if claimed.ID != first.ID {
		t.Fatalf("claimed %s, want the task next would have picked, %s", claimed.ID, first.ID)
	}
	assertAssignments(t, claimed, assignTestActor)
	assertAssignments(t, showTask(t, repository, first.ID), assignTestActor)

	// Asking again offers the same task, and claiming it again writes nothing.
	// The skip is about work somebody else is responsible for; a task this
	// identity holds and has not moved on is still what it should be working on,
	// and a claim that re-selected around it would send an agent off its own
	// unfinished work.
	head := showTask(t, repository, first.ID).Head
	code, stdout, stderr = run(t, repository, "next", "--claim", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("second next --claim code = %d, want 0; stderr = %q", code, stderr)
	}
	if again := decodeMutationTask(t, stdout, "next"); again.ID != first.ID {
		t.Fatalf("second claim = %s, want the same task %s", again.ID, first.ID)
	}
	if now := showTask(t, repository, first.ID).Head; now != head {
		t.Fatalf("head moved from %q to %q; re-claiming what you hold writes nothing", head, now)
	}

	// Another identity is offered the other task instead, which is the skip
	// doing its work.
	cliGit(t, repository, "config", "user.email", "other@example.com")
	code, stdout, stderr = run(t, repository, "next", "--claim", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("other identity's claim code = %d, want 0; stderr = %q", code, stderr)
	}
	if other := decodeMutationTask(t, stdout, "next"); other.ID == first.ID {
		t.Fatalf("other identity claimed %s, want the task nobody holds", other.ID)
	}
}

func TestNextRefusesToClaimAndOfferEverythingAtOnce(t *testing.T) {
	repository := initializedRepository(t)
	code, _, stderr := run(t, repository, "next", "--claim", "--any", "--no-sync")
	if code != 2 {
		t.Fatalf("code = %d, want 2; stderr = %q", code, stderr)
	}
	assertHumanError(t, stderr, "--any or --claim, not both")
}

// The two-clone race the design is built around, played out in full: one agent
// claims, the other meets the refusal that names them, records nothing, and can
// still choose to pair by forcing — at which point both assignments stand.
func TestTwoClonesClaimingOneTask(t *testing.T) {
	first, second := cliSyncRepositories(t)
	cliGit(t, first, "config", "user.email", "one@example.com")
	cliGit(t, second, "config", "user.email", "two@example.com")

	task := cliCreateTask(t, first, "Contested")
	if code, _, stderr := run(t, first, "update", task.ID, "--status", "ready"); code != 0 {
		t.Fatalf("make ready code = %d, want 0; stderr = %q", code, stderr)
	}

	code, stdout, stderr := run(t, first, "next", "--claim", "--json")
	if code != 0 {
		t.Fatalf("first claim code = %d, want 0; stderr = %q", code, stderr)
	}
	if claimed := decodeMutationTask(t, stdout, "next"); claimed.ID != task.ID {
		t.Fatalf("first claim = %s, want %s", claimed.ID, task.ID)
	}

	// The second agent's own `next --claim` will not even offer the task, which
	// is the skip doing its work before any refusal is needed.
	code, stdout, stderr = run(t, second, "next", "--claim", "--json")
	if code != 0 {
		t.Fatalf("second claim code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "next")
	if string(result.Data) != "null" {
		t.Fatalf("second claim data = %s, want null; the only eligible task is held", result.Data)
	}
	if got, want := warningCodes(result.Warnings), []string{core.WarningNextHeldByOthers}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("second claim warnings = %q, want %q", got, want)
	}

	// Naming the task directly is where the refusal lives, and it names the
	// agent that got there first.
	code, _, stderr = run(t, second, "update", task.ID, "--assign", "self", "--json")
	if code != 10 {
		t.Fatalf("second assign code = %d, want 10; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryAssigned, "")
	if !strings.Contains(stderr, "one@example.com") {
		t.Fatalf("refusal = %q, want it to name the agent holding the task", stderr)
	}
	assertAssignments(t, showTask(t, second, task.ID), "one@example.com")

	// Forcing is the spike: both assignments stand, neither removes the other,
	// and the command says whose company it is in.
	code, stdout, stderr = run(t, second, "update", task.ID, "--assign", "self", "--force", "--json")
	if code != 0 {
		t.Fatalf("forced assign code = %d, want 0; stderr = %q", code, stderr)
	}
	forced := assertJSONResult(t, stdout, "update")
	if got, want := warningCodes(forced.Warnings), []string{core.WarningAssignmentShared}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("forced warnings = %q, want %q", got, want)
	}
	assertAssignments(t, showTask(t, second, task.ID), "one@example.com", "two@example.com")

	// And the first agent sees the same both-assigned state once it fetches,
	// which is the outcome the design calls a spike rather than a conflict.
	if code, _, stderr := run(t, first, "fetch"); code != 0 {
		t.Fatalf("first fetch code = %d, want 0; stderr = %q", code, stderr)
	}
	assertAssignments(t, showTask(t, first, task.ID), "one@example.com", "two@example.com")

	// The spike does not make the work disappear from either agent's `next`.
	// Both hold the task, so both are still offered it; a skip that asked only
	// whether somebody else holds it would answer "nothing to do" to the two
	// agents actually doing the work.
	for name, clone := range map[string]string{"first": first, "second": second} {
		code, stdout, stderr := run(t, clone, "next", "--json")
		if code != 0 {
			t.Fatalf("%s next code = %d, want 0; stderr = %q", name, code, stderr)
		}
		if got := decodeNextTask(t, stdout); got == nil || got.ID != task.ID {
			t.Fatalf("%s next = %#v, want the shared task %s", name, got, task.ID)
		}
	}

	// And claiming it again writes nothing: the identity already holds it.
	head := showTask(t, first, task.ID).Head
	code, stdout, stderr = run(t, first, "next", "--claim", "--json")
	if code != 0 {
		t.Fatalf("post-spike claim code = %d, want 0; stderr = %q", code, stderr)
	}
	if claimed := decodeMutationTask(t, stdout, "next"); claimed.ID != task.ID {
		t.Fatalf("post-spike claim = %s, want the shared task %s", claimed.ID, task.ID)
	}
	if now := showTask(t, first, task.ID).Head; now != head {
		t.Fatalf("head moved from %q to %q; re-claiming a task you already hold writes nothing", head, now)
	}
	assertAssignments(t, showTask(t, first, task.ID), "one@example.com", "two@example.com")
}

// The push race the design names, played out exactly: B claims while A's claim
// is still unpublished, B's push loses, and the synchronization that replays B's
// operation onto A's tip is where B learns it shares the task.
//
// Nothing in B's claim could have said so — when it ran, A's assignment did not
// exist in this clone — which is why the contract is that the command that
// claimed *or* the synchronization that reconciled tells the claimant, and why
// this repro drives the second one.
func TestAClaimThatLosesThePushRaceIsReportedBySyncing(t *testing.T) {
	_, second, task := stagedClaimRace(t)
	assertAssignments(t, showTask(t, second, task.ID), "two@example.com")

	code, stdout, stderr := run(t, second, "sync", "--json")
	if code != 0 {
		t.Fatalf("second sync code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "sync")
	if got, want := warningCodes(result.Warnings), []string{core.WarningAssignmentShared}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sync warnings = %q, want %q; a claimant that ends up sharing has to be told", got, want)
	}
	if !strings.Contains(result.Warnings[0].Message, "one@example.com") || !strings.Contains(result.Warnings[0].Message, task.ID) {
		t.Fatalf("warning = %q, want it to name the task and the other holder", result.Warnings[0].Message)
	}
	// Both claims survive, which is the spike the removal rule guarantees.
	assertAssignments(t, showTask(t, second, task.ID), "one@example.com", "two@example.com")

	// Said once, about a fetch that moved the task. A synchronization that
	// changes nothing repeats nothing.
	code, stdout, stderr = run(t, second, "sync", "--json")
	if code != 0 {
		t.Fatalf("repeat sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if warnings := assertJSONResult(t, stdout, "sync").Warnings; len(warnings) != 0 {
		t.Fatalf("repeat sync warnings = %#v, want none for a synchronization that moved nothing", warnings)
	}
}

// The same contract through the other door: whichever command reconciles is the
// one that tells the claimant, so an agent that simply asks for its next task
// hears it too.
func TestAReconciledClaimIsReportedByTheNextCommandThatFetches(t *testing.T) {
	_, second, task := stagedClaimRace(t)

	code, stdout, stderr := run(t, second, "next", "--json")
	if code != 0 {
		t.Fatalf("second next code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "next")
	var shared bool
	for _, warning := range result.Warnings {
		if warning.Code == core.WarningAssignmentShared && strings.Contains(warning.Message, "one@example.com") {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("next warnings = %#v, want the shared claim reported by the command that reconciled it", result.Warnings)
	}
	// And the task it shares is still the task it is offered, because it holds
	// it too.
	if got := decodeNextTask(t, stdout); got == nil || got.ID != task.ID {
		t.Fatalf("next = %#v, want the shared task %s offered back to a holder", got, task.ID)
	}
}

// The command that reconciles is very often an ordinary one that has nothing to
// do with assignment — `update <id> --status in-progress` is literally the step
// the README, the skill and the generated guidelines tell an agent to run right
// after claiming — and it must report the sharing just the same.
//
// This is the case that was silently lost: the mutation's own result reports
// sharing only when the update carried an assignment, so a status change had
// nothing to say, while the reconcile pass skipped the task because the result
// had "already reported" it. Both channels went quiet on the one task that
// needed either of them, and no later command could pick it up, because by then
// there was nothing left to reconcile.
func TestANonAssignmentMutationThatReconcilesAClaimReportsTheSharing(t *testing.T) {
	_, second, task := stagedClaimRace(t)

	// The take-it-up step, run on the very task whose claim lost the race.
	code, stdout, stderr := run(t, second, "update", task.ID, "--status", "in-progress", "--json")
	if code != 0 {
		t.Fatalf("status update code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "update")
	if got, want := warningCodes(result.Warnings), []string{core.WarningAssignmentShared}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings = %q, want exactly %q from the command that reconciled the claim", got, want)
	}
	if !strings.Contains(result.Warnings[0].Message, "one@example.com") || !strings.Contains(result.Warnings[0].Message, task.ID) {
		t.Fatalf("warning = %q, want it to name the task and the other holder", result.Warnings[0].Message)
	}
	assertAssignments(t, showTask(t, second, task.ID), "one@example.com", "two@example.com")
}

// The other half of the same rule: an update that did carry an assignment says
// it once, not once per channel. The result's own report and the reconcile pass
// would otherwise both name the same task.
func TestAnAssignmentThatReconcilesItsOwnClaimReportsTheSharingOnce(t *testing.T) {
	_, second, task := stagedClaimRace(t)

	// Re-claiming is a no-op on the assignment, so everything this reports comes
	// from the reconcile the same command performed.
	code, stdout, stderr := run(t, second, "update", task.ID, "--assign", "self", "--json")
	if code != 0 {
		t.Fatalf("re-claim code = %d, want 0; stderr = %q", code, stderr)
	}
	result := assertJSONResult(t, stdout, "update")
	if got, want := warningCodes(result.Warnings), []string{core.WarningAssignmentShared}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("warnings = %q, want exactly one %q rather than one per channel", got, want)
	}
	assertAssignments(t, showTask(t, second, task.ID), "one@example.com", "two@example.com")
}

// stagedClaimRace leaves the two clones exactly where the push race the design
// names leaves them: `second` holds an unpublished claim, `first` has published
// one, and nothing has reconciled yet. What each test then varies is only which
// command does the reconciling.
func stagedClaimRace(t *testing.T) (first, second string, task core.Task) {
	t.Helper()
	first, second = cliSyncRepositories(t)
	cliGit(t, first, "config", "user.email", "one@example.com")
	cliGit(t, second, "config", "user.email", "two@example.com")

	task = cliCreateTask(t, first, "Contested")
	if code, _, stderr := run(t, first, "update", task.ID, "--status", "ready"); code != 0 {
		t.Fatalf("make ready code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("second fetch code = %d, want 0; stderr = %q", code, stderr)
	}
	// The loser claims locally without publishing, which is the state a claim is
	// in between its write and its push.
	if code, _, stderr := run(t, second, "update", task.ID, "--assign", "self", "--no-sync"); code != 0 {
		t.Fatalf("second claim code = %d, want 0; stderr = %q", code, stderr)
	}
	// The winner claims and publishes, so the unpublished claim has lost.
	if code, _, stderr := run(t, first, "update", task.ID, "--assign", "self"); code != 0 {
		t.Fatalf("first claim code = %d, want 0; stderr = %q", code, stderr)
	}
	return first, second, task
}

func decodeNextTask(t *testing.T, output string) *core.Task {
	t.Helper()
	data := assertJSONResult(t, output, "next").Data
	if string(data) == "null" {
		return nil
	}
	var task core.Task
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatalf("decode next task: %v", err)
	}
	return &task
}
