package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// priorityColorMutationDocument decodes a `priority color` envelope's data
// member. Declared here rather than reused from production for the reason
// priorityListDocument's comment gives in priority_list_test.go: a change to
// what the envelope carries has to be made twice, once where it is produced
// and once where a caller reads it.
type priorityColorMutationDocument struct {
	Change struct {
		Operation string `json:"operation"`
		Priority  string `json:"priority"`
		Color     *struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"color"`
	} `json:"change"`
	Vocabulary priorityVocabularyDocument `json:"vocabulary"`
}

func cliPriorityColorMutation(t *testing.T, repository string, args ...string) priorityColorMutationDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v = code %d, stderr %q", args, code, stderr)
	}
	var document priorityColorMutationDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "priority color").Data, &document); err != nil {
		t.Fatalf("decode priority color result: %v; output = %s", err, stdout)
	}
	return document
}

// priorityColorOf reads the stored color for one priority from `priority
// list`, so a refusal can be checked against the ledger rather than against
// the refused command's own say-so.
func priorityColorOf(t *testing.T, repository string, priority string) string {
	t.Helper()
	for _, entry := range cliPriorityList(t, repository).Priorities {
		if entry.Priority == priority {
			return entry.Color
		}
	}
	t.Fatalf("no priority %q in %v", priority, cliPriorityList(t, repository))
	return ""
}

func TestPriorityColorSetsAndClearsTheStoredInk(t *testing.T) {
	repository := initializedRepository(t)

	set := cliPriorityColorMutation(t, repository, "priority", "color", "high", "#b42318", "--json")
	if set.Change.Operation != "color" || set.Change.Priority != "high" {
		t.Fatalf("color change = %#v", set.Change)
	}
	if set.Change.Color == nil || set.Change.Color.From != "" || set.Change.Color.To != "#b42318" {
		t.Fatalf("color change.color = %#v, want empty from, #b42318 to", set.Change.Color)
	}
	if got := priorityColorOf(t, repository, "high"); got != "#b42318" {
		t.Fatalf("stored color = %q, want #b42318", got)
	}

	cleared := cliPriorityColorMutation(t, repository, "priority", "color", "high", "--json")
	if cleared.Change.Color == nil || cleared.Change.Color.From != "#b42318" || cleared.Change.Color.To != "" {
		t.Fatalf("cleared change.color = %#v, want #b42318 -> empty", cleared.Change.Color)
	}
	if got := priorityColorOf(t, repository, "high"); got != "" {
		t.Fatalf("stored color after clear = %q, want empty (derived)", got)
	}

	// The text surface names the field, the way status.go's mutations do.
	code, stdout, stderr := run(t, repository, "priority", "color", "high", "#0f62fe")
	if code != 0 || stderr != "" {
		t.Fatalf("priority color = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tcolor:\t#0f62fe\n") {
		t.Fatalf("color text = %q, want the color line", stdout)
	}
}

// An uppercase value is normalized to lowercase before it is stored, not
// refused. Confirmed against core.ValidateThemeColor in internal/core/limits.go
// (it lowercases the value it accepts) and against the canonical-storage check
// in internal/core/priority.go's normalizePriorityDocument (definition.Color
// must already equal ValidateThemeColor's return, or the checkpoint is corrupt
// data) and the identical check on priority.recolor's own Value in
// internal/core/configop.go.
func TestPriorityColorNormalizesAnUppercaseValue(t *testing.T) {
	repository := initializedRepository(t)

	mutation := cliPriorityColorMutation(t, repository, "priority", "color", "medium", "#B42318", "--json")
	if mutation.Change.Color == nil || mutation.Change.Color.To != "#b42318" {
		t.Fatalf("normalized color = %#v, want #b42318", mutation.Change.Color)
	}
	if got := priorityColorOf(t, repository, "medium"); got != "#b42318" {
		t.Fatalf("stored color = %q, want lowercase #b42318", got)
	}
}

// A malformed value is refused before anything is written: the ledger has to
// still read as though the command never ran.
func TestPriorityColorRefusesAMalformedValueBeforeWritingAnything(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository)

	code, stdout, stderr := run(t, repository, "priority", "color", "high", "crimson")
	if code == 0 {
		t.Fatalf("priority color crimson = code 0, want a refusal")
	}
	if stdout != "" {
		t.Fatalf("priority color crimson stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "hexadecimal") {
		t.Fatalf("priority color crimson stderr = %q, want it to name the color rule", stderr)
	}

	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused color", before.Head, after.Head)
	}
	if got := priorityColorOf(t, repository, "high"); got != "" {
		t.Fatalf("stored color after refusal = %q, want untouched", got)
	}
}

// The same refusal applies to the JSON surface, and still writes nothing.
func TestPriorityColorRefusesAMalformedValueInJSONMode(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository)

	code, stdout, stderr := run(t, repository, "priority", "color", "high", "#zzzzzz", "--json")
	if code == 0 {
		t.Fatalf("priority color #zzzzzz --json = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	if stdout != "" {
		t.Fatalf("priority color #zzzzzz --json stdout = %q, want nothing written", stdout)
	}

	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused color", before.Head, after.Head)
	}
}

// A recolor that would record nothing is refused before anything is
// authored, the same shape `priority label` uses for the label it already
// has (internal/cli/priority_label.go's planPriorityRelabel). Setting the
// color to the value already stored is one of the two directions that
// counts as nothing to record; the case-only spelling is folded to the same
// canonical value first (TestPriorityColorNormalizesAnUppercaseValue), so it
// refuses too.
func TestPriorityColorRefusesSettingTheColorItAlreadyHas(t *testing.T) {
	repository := initializedRepository(t)
	cliPriorityColorMutation(t, repository, "priority", "color", "high", "#0f62fe", "--json")

	before := cliPriorityList(t, repository)
	code, stdout, stderr := run(t, repository, "priority", "color", "high", "#0f62fe", "--json")
	if code == 0 {
		t.Fatalf("priority color with the color it already has = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "high" already has that color`)
	if stdout != "" {
		t.Fatalf("priority color already-has stdout = %q, want nothing written", stdout)
	}
	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused no-op color", before.Head, after.Head)
	}
	if got := priorityColorOf(t, repository, "high"); got != "#0f62fe" {
		t.Fatalf("stored color after the refusal = %q, want the untouched #0f62fe", got)
	}

	// The same value spelled with a different case folds to the same
	// canonical color, so it is refused for the identical reason.
	code, stdout, stderr = run(t, repository, "priority", "color", "high", "#0F62FE", "--json")
	if code == 0 {
		t.Fatalf("priority color with the same color uppercased = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "high" already has that color`)
	if stdout != "" {
		t.Fatalf("priority color uppercase-already-has stdout = %q, want nothing written", stdout)
	}
}

// The other direction that counts as nothing to record: clearing a priority
// that has no color stored already.
func TestPriorityColorRefusesClearingAPriorityWithNoColor(t *testing.T) {
	repository := initializedRepository(t)
	before := cliPriorityList(t, repository)

	code, stdout, stderr := run(t, repository, "priority", "color", "medium", "--json")
	if code == 0 {
		t.Fatalf("priority color clear on an uncolored priority = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "medium" has no color to clear`)
	if stdout != "" {
		t.Fatalf("priority color clear-nothing stdout = %q, want nothing written", stdout)
	}

	after := cliPriorityList(t, repository)
	if after.Head != before.Head {
		t.Fatalf("ledger head moved from %q to %q on a refused clear", before.Head, after.Head)
	}
	if got := priorityColorOf(t, repository, "medium"); got != "" {
		t.Fatalf("stored color after the refused clear = %q, want it to stay empty", got)
	}
}

// legacyDisplayConfiguredRepository builds the real shape a v0.5 project
// reaches when it recorded a display setting before this build's priority
// work shipped: a configuration ledger whose immutable genesis carries no
// priorities section at all, and whose only later commit is a display change
// that already spent this project's one generation-two marker
// (core.ConfigDisplaySet, per configOperationMinReader in
// internal/core/configop.go).
//
// No path through this build's own write functions can reach that shape any
// more: every genesis this build writes packs core.BuiltInPriorityVocabulary
// in with it, deliberately (see writeConfigGenesis's comment in
// internal/gitstore/configledger.go), so seeding a project the ordinary way
// and then running `config set` always starts from minReader 3, not 2. The
// only way to test the case that actually costs a real team something is to
// forge the ledger a pre-priorities build would have left, the same way
// internal/cli/newerwriter_test.go forges a project ahead of this build (its
// writeFutureConfigCommit) — real git objects, built from the same core
// encoding functions gitstore itself calls, pointed at a project behind this
// build instead of ahead of it.
func legacyDisplayConfiguredRepository(t *testing.T) string {
	t.Helper()
	repository := preLedgerRepository(t)

	task := cliCreateTask(t, repository, "Predates the priorities section")
	taskHead := cliGitOutput(t, repository, "rev-parse", "refs/workbook/tasks/"+task.ID)
	taskState, err := core.DecodeStateDocument(
		[]byte(cliGitOutput(t, repository, "show", taskHead+":state.json") + "\n"))
	if err != nil {
		t.Fatalf("decode task state: %v", err)
	}

	const actor = "legacy@example.test"
	ids := core.CryptoULIDSource{}
	generation, err := ids.New()
	if err != nil {
		t.Fatalf("history generation id: %v", err)
	}
	genesisID, err := ids.New()
	if err != nil {
		t.Fatalf("genesis operation id: %v", err)
	}

	genesisPack, err := core.NewConfigOperationPack(taskState.ProjectID, generation, actor, 1, time.Now().UTC(),
		[]core.ConfigOperation{{
			ID:     genesisID,
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: core.LegacyVocabulary().Document()},
		}})
	if err != nil {
		t.Fatalf("legacy genesis pack: %v", err)
	}
	genesisState, err := core.ApplyConfig(nil, genesisPack)
	if err != nil {
		t.Fatalf("apply legacy genesis: %v", err)
	}
	genesisCommit := writeForgedConfigCommit(t, repository, "", genesisPack, genesisState, "workbook: legacy genesis")

	displayID, err := ids.New()
	if err != nil {
		t.Fatalf("display operation id: %v", err)
	}
	displayPack, err := core.NewConfigOperationPack(taskState.ProjectID, generation, actor, 2, time.Now().UTC(),
		[]core.ConfigOperation{{
			ID:      displayID,
			Type:    core.ConfigDisplaySet,
			Setting: core.DisplayProjectName,
			Value:   "Atlas",
		}})
	if err != nil {
		t.Fatalf("display pack: %v", err)
	}
	displayState, err := core.ApplyConfig(&genesisState, displayPack)
	if err != nil {
		t.Fatalf("apply display change: %v", err)
	}
	displayCommit := writeForgedConfigCommit(
		t, repository, genesisCommit, displayPack, displayState, "workbook: set project-name to Atlas")

	cliGit(t, repository, "update-ref", "refs/workbook/config", displayCommit)
	return repository
}

// writeForgedConfigCommit writes one configuration ledger commit with raw git
// plumbing, the same technique internal/cli/newerwriter_test.go's
// writeFutureConfigCommit uses, generalized to a parent that may be empty (a
// genesis has none) and to a pack and state this build produced normally
// rather than forged into a shape it does not recognize.
func writeForgedConfigCommit(
	t *testing.T,
	repository, parent string,
	pack core.ConfigOperationPack,
	state core.ConfigStateDocument,
	subject string,
) string {
	t.Helper()
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		t.Fatalf("encode configuration operation pack: %v", err)
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		t.Fatalf("encode configuration state document: %v", err)
	}
	operationBlob := hashObject(t, repository, string(packBytes))
	stateBlob := hashObject(t, repository, string(stateBytes))
	tree := gitWithInput(t, repository, fmt.Sprintf("100644 blob %s\toperation.json\n100644 blob %s\tstate.json\n",
		operationBlob, stateBlob), "mktree")
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return gitWithInput(t, repository, subject, args...)
}

// The test this fix exists for: a project that behaves exactly like a real
// v0.5 project sitting at minReader 2 — configured once, through `config
// set`, and never through a priority verb. Clearing a color on a priority
// that never had one is nothing to record, and must cost this project
// nothing: not a commit, not the permanent generation-three marker a real
// priority write would backfill in. Asserting only the exit code would not
// catch a regression that refused correctly on the surface but still wrote —
// the ledger head and the stored minReader are what a corrupted compatibility
// promise would actually move.
func TestPriorityColorNoOpRefusalDoesNotBumpAnUnconfiguredProjectsLedger(t *testing.T) {
	repository := legacyDisplayConfiguredRepository(t)

	before := cliGitOutput(t, repository, "rev-parse", "refs/workbook/config")
	beforeState := cliGitOutput(t, repository, "show", before+":state.json")
	if !strings.Contains(beforeState, `"minReader":2`) {
		t.Fatalf("forged ledger state.json = %s, want minReader 2 before the refusal", beforeState)
	}
	if strings.Contains(beforeState, "priorities") {
		t.Fatalf("forged ledger state.json = %s, want no priorities section before the refusal", beforeState)
	}

	code, stdout, stderr := run(t, repository, "priority", "color", "low", "--json")
	if code == 0 {
		t.Fatalf("priority color clear on an unconfigured project = code 0, want a refusal")
	}
	assertJSONError(t, stderr, core.CategoryValidation, `priority "low" has no color to clear`)
	if stdout != "" {
		t.Fatalf("priority color clear stdout = %q, want nothing written", stdout)
	}

	after := cliGitOutput(t, repository, "rev-parse", "refs/workbook/config")
	if after != before {
		t.Fatalf("configuration ledger head moved from %q to %q on a refused no-op recolor", before, after)
	}
	afterState := cliGitOutput(t, repository, "show", after+":state.json")
	if !strings.Contains(afterState, `"minReader":2`) {
		t.Fatalf("ledger state.json after the refusal = %s, want minReader still 2, not bumped to 3", afterState)
	}
	if strings.Contains(afterState, "priorities") {
		t.Fatalf("ledger state.json after the refusal = %s, want no priorities section backfilled in", afterState)
	}
}
