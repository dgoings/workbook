package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

type priorityLogDocument struct {
	Showing int `json:"showing"`
	Total   int `json:"total"`
	Entries []struct {
		Commit      string `json:"commit"`
		OperationID string `json:"operationId"`
		Actor       string `json:"actor"`
		Operation   string `json:"operation"`
		Summary     string `json:"summary"`
		Collapsed   int    `json:"collapsed"`
		Inverse     *struct {
			Command string `json:"command"`
			Exact   bool   `json:"exact"`
			Note    string `json:"note"`
		} `json:"inverse"`
	} `json:"entries"`
}

func cliPriorityLog(t *testing.T, repository string, args ...string) priorityLogDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append(append([]string{"priority", "log"}, args...), "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("priority log = code %d, stderr %q", code, stderr)
	}
	var document priorityLogDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority log").Data, &document); err != nil {
		t.Fatalf("decode priority log: %v; output = %s", err, stdout)
	}
	return document
}

// A project with no ledger at all has recorded no priority change, and says so
// rather than failing. The ledger is seeded lazily, so its absence is the
// ordinary state of most projects.
func TestPriorityLogOnALedgerlessProjectReportsNothingRecorded(t *testing.T) {
	repository := preLedgerRepository(t)

	document := cliPriorityLog(t, repository)
	if document.Total != 0 || document.Showing != 0 || len(document.Entries) != 0 {
		t.Fatalf("log = %#v, want an empty log", document)
	}

	code, stdout, stderr := run(t, repository, "priority", "log")
	if code != 0 || stderr != "" {
		t.Fatalf("priority log = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "No priority change is recorded; this project has not configured its priorities.") {
		t.Errorf("priority log text = %q, want the nothing-recorded line", stdout)
	}
}

// A project whose ledger records status changes and no priority change prints
// nothing rather than listing somebody else's history. The two sections share a
// ledger and do not share a log.
func TestPriorityLogSkipsCommitsThatChangedNoPriority(t *testing.T) {
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "triage", "--after", "backlog", "--no-sync")

	document := cliPriorityLog(t, repository)
	if document.Total != 0 || document.Showing != 0 || len(document.Entries) != 0 {
		t.Fatalf("log = %#v, want no entries for a project that changed only statuses", document)
	}

	code, stdout, stderr := run(t, repository, "priority", "log")
	if code != 0 || stderr != "" {
		t.Fatalf("priority log = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "triage") {
		t.Errorf("priority log text = %q, want nothing about a status change", stdout)
	}
	if !strings.Contains(stdout, "Showing all 0 change(s).") {
		t.Errorf("priority log text = %q, want the empty-window line", stdout)
	}
}

// --limit and --all describe the same window and cannot both decide it.
func TestPriorityLogRefusesLimitWithAll(t *testing.T) {
	repository := initializedRepository(t)
	code, _, stderr := run(t, repository, "priority", "log", "--limit", "2", "--all")
	if code != 2 {
		t.Fatalf("priority log = code %d, want 2", code)
	}
	if !strings.Contains(stderr, "cannot use --limit with --all") {
		t.Errorf("priority log stderr = %q, want the conflict refusal", stderr)
	}
}

func TestPriorityLogRefusesANonPositiveLimit(t *testing.T) {
	repository := initializedRepository(t)
	code, _, stderr := run(t, repository, "priority", "log", "--limit", "0")
	if code != 2 {
		t.Fatalf("priority log = code %d, want 2", code)
	}
	if !strings.Contains(stderr, "priority log --limit must be a positive whole number") {
		t.Errorf("priority log stderr = %q, want the limit refusal", stderr)
	}
}

// A peer's priority.untag is described, but its inverse is offered only for a
// role this build can name.
//
// This build cannot author a priority.untag at all — the verb was dropped
// because a priority has one role and one priority must carry it, so every
// call refused. The operation still arrives from a peer on a build with a wider
// set of roles, and `priority log` still has to describe it. What it must not
// do is print `priority tag <p> --tag next` as the undo: `--tag` refuses every
// word but default, so that command exits 5 the moment somebody pastes it, and
// an inverse nobody can run is worse than no inverse at all.
func TestPriorityLogOffersNoInverseForARoleThisBuildCannotName(t *testing.T) {
	before := configBefore{priorities: core.BuiltInPriorityVocabulary()}

	foreign := []core.ConfigOperation{{
		ID:          "01M2FOREIGNUNTAGOPERATION1",
		Type:        core.ConfigPriorityUntag,
		Priority:    core.PriorityMedium,
		PriorityTag: core.PriorityTag("next"),
	}}
	if inverse := priorityPackInverse(before, foreign); inverse != nil {
		t.Fatalf("inverse for a foreign role = %#v, want none: %q cannot be pasted",
			inverse, inverse.Command)
	}

	// The role this build does know still gets its exact inverse, because
	// giving the default back is a command that runs.
	known := []core.ConfigOperation{{
		ID:          "01M2KNOWNUNTAGOPERATION123",
		Type:        core.ConfigPriorityUntag,
		Priority:    core.PriorityMedium,
		PriorityTag: core.PriorityTagDefault,
	}}
	inverse := priorityPackInverse(before, known)
	if inverse == nil {
		t.Fatal("inverse for the default role = nil, want the command that gives it back")
	}
	if !inverse.Exact || inverse.Command != "workbook priority tag medium --tag default" {
		t.Fatalf("inverse = %#v, want an exact `priority tag medium --tag default`", inverse)
	}
}
