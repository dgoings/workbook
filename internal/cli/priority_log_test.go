package cli

import (
	"encoding/json"
	"fmt"
	"reflect"
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

// writeLegacyPriorityLessLedger replaces a project's configuration ledger with
// the one shape that reaches gitstore's built-in backfill: a ledger whose
// genesis carries a status vocabulary and no priorities section at all.
//
// No path in this build writes such a root. `setup` mints one carrying
// core.BuiltInPriorityVocabulary(), and so does the lazy seed a project with no
// ledger takes — which is why deleting the configuration ref, the way
// preLedgerRepository does, reaches the seed rather than the backfill. Only a
// project configured by a build that predates the priorities section has this
// root, and a genesis is immutable, so the first priority.* operation authored
// against it is recorded together with the built-in three. Writing that root by
// hand is the only way to drive the command line over one.
//
// The objects are the two blobs, one tree and one commit every configuration
// commit is made of; see gitstore's writeConfigObjects for the shape being
// reproduced.
func writeLegacyPriorityLessLedger(t *testing.T, repository string) {
	t.Helper()
	root := gitOutput(t, repository, "rev-list", "--max-parents=0", configLedgerRefName)
	minted := gitOutput(t, repository, "cat-file", "blob", root+":operation.json")
	var existing core.ConfigOperationPack
	if err := json.Unmarshal([]byte(minted), &existing); err != nil {
		t.Fatalf("decode the minted genesis: %v", err)
	}

	ids := core.CryptoULIDSource{}
	generation, err := ids.New()
	if err != nil {
		t.Fatalf("generation ID: %v", err)
	}
	genesisID, err := ids.New()
	if err != nil {
		t.Fatalf("genesis operation ID: %v", err)
	}
	pack, err := core.NewConfigOperationPack(
		existing.ProjectID, generation, existing.Actor.ID, 1, existing.WallTime,
		[]core.ConfigOperation{{
			ID:     genesisID,
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: core.LegacyVocabulary().Document()},
		}})
	if err != nil {
		t.Fatalf("NewConfigOperationPack() error = %v", err)
	}
	state, err := core.ApplyConfig(nil, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if state.Config.Priorities != nil {
		t.Fatalf("legacy genesis state carries a priorities section: %#v", state.Config.Priorities)
	}
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("encode the legacy genesis pack: %v", err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("encode the legacy genesis state: %v", err)
	}

	operationBlob := gitWithInput(t, repository, string(packBytes), "hash-object", "-w", "-t", "blob", "--stdin")
	stateBlob := gitWithInput(t, repository, string(stateBytes), "hash-object", "-w", "-t", "blob", "--stdin")
	tree := gitWithInput(t, repository, fmt.Sprintf(
		"100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n", operationBlob, stateBlob), "mktree")
	commit := gitOutput(t, repository, "commit-tree", tree, "-m", "Record the Workbook configuration genesis")
	gitOutput(t, repository, "update-ref", configLedgerRefName, commit)
}

// The log names the change somebody authored, not the backfill recorded beside
// it, and the inverse it prints undoes that change and nothing else.
//
// A project whose ledger predates the priorities section records its first
// priority change together with the built-in three, as three priority.add
// operations ahead of the authored one. Those adds are priority operations, so
// a rule that answers about the first priority operation in the pack answers
// about `high` — and prints `priority delete high --into medium` as the undo
// for a command that added `urgent`. An inverse is printed so somebody can
// paste it, so such an entry destroys a priority the project still uses and
// forwards its tasks away from it.
func TestPriorityLogDescribesTheAuthoredChangeRatherThanTheBackfill(t *testing.T) {
	repository := initializedRepository(t)
	writeLegacyPriorityLessLedger(t, repository)
	urgentWork := cliCreateTaskAtPriority(t, repository, "Ship the fix", "high")
	ordinaryWork := cliCreateTask(t, repository, "Write the notes")

	mustRunStatus(t, repository, "priority", "add", "urgent", "--before", "high", "--no-sync")

	document := cliPriorityLog(t, repository)
	if len(document.Entries) != 1 {
		t.Fatalf("log = %#v, want the one change this project made", document)
	}
	entry := document.Entries[0]
	if entry.Summary != "added priority urgent" {
		t.Errorf("summary = %q, want the authored add named with no backfill counted", entry.Summary)
	}
	if entry.Collapsed != 0 {
		t.Errorf("collapsed = %d, want 0: the backfill changed none of this project's priorities", entry.Collapsed)
	}
	if entry.Inverse == nil {
		t.Fatal("inverse = nil, want the command that removes the added priority")
	}
	if entry.Inverse.Command != "workbook priority delete urgent --into medium" {
		t.Fatalf("inverse = %q, want the undo for the add somebody ran", entry.Inverse.Command)
	}

	// An inverse is printed to be pasted, so paste it: the project is back to
	// the built-in three and every task is still filed where it was.
	mustRunStatus(t, repository, "priority", "delete", "urgent", "--into", "medium", "--no-sync")
	if got, want := cliPriorityNames(t, repository), []string{"high", "medium", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities after the inverse = %v, want %v", got, want)
	}
	list := cliPriorityList(t, repository)
	if list.Default != "medium" {
		t.Errorf("default after the inverse = %q, want medium", list.Default)
	}
	if len(list.Unresolved) != 0 {
		t.Errorf("unresolved after the inverse = %#v, want every task still resolving", list.Unresolved)
	}
	if got := showTask(t, repository, urgentWork.ID).Priority; got != core.PriorityHigh {
		t.Errorf("the task filed under high reads back at %q, want high", got)
	}
	if got := showTask(t, repository, ordinaryWork.ID).Priority; got != core.PriorityMedium {
		t.Errorf("the task filed under medium reads back at %q, want medium", got)
	}
}
