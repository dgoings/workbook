package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The decoded shapes are declared here rather than reused from production, for
// the reason the status suite declares its own: a change to an envelope member
// has to be made twice, once where it is produced and once where a caller reads
// it, which is the whole value of a machine interface's test.
type keyChangeDocument struct {
	Operation   string `json:"operation"`
	Key         string `json:"key"`
	From        string `json:"from"`
	Reactivated bool   `json:"reactivated"`
	State       string `json:"state"`
}

type keySetDocument struct {
	Head    string `json:"head"`
	Seeded  bool   `json:"seeded"`
	Current string `json:"current"`
	Keys    []struct {
		Key     string `json:"key"`
		State   string `json:"state"`
		Current bool   `json:"current"`
		Tasks   *int   `json:"tasks"`
	} `json:"keys"`
}

type keyMutationDocument struct {
	Change  keyChangeDocument `json:"change"`
	Keys    keySetDocument    `json:"keys"`
	Inverse inverseDocument   `json:"inverse"`
}

type keyLogDocument struct {
	Showing int `json:"showing"`
	Total   int `json:"total"`
	Entries []struct {
		Commit      string           `json:"commit"`
		OperationID string           `json:"operationId"`
		WallTime    time.Time        `json:"wallTime"`
		Actor       string           `json:"actor"`
		Operation   string           `json:"operation"`
		Summary     string           `json:"summary"`
		Collapsed   int              `json:"collapsed"`
		Inverse     *inverseDocument `json:"inverse"`
	} `json:"entries"`
	Truncated *core.HistoryTruncation `json:"truncated"`
}

func cliKeyMutation(t *testing.T, repository, command string, args ...string) keyMutationDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v = code %d, stderr %q", args, code, stderr)
	}
	var document keyMutationDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, command).Data, &document); err != nil {
		t.Fatalf("decode %s result: %v; output = %s", command, err, stdout)
	}
	return document
}

func cliKeyList(t *testing.T, repository string) keySetDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, "key", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("key list = code %d, stderr %q", code, stderr)
	}
	var document keySetDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "key list").Data, &document); err != nil {
		t.Fatalf("decode key list: %v; output = %s", err, stdout)
	}
	return document
}

func cliKeyLog(t *testing.T, repository string, args ...string) keyLogDocument {
	t.Helper()
	code, stdout, stderr := run(t, repository, append(append([]string{"key", "log"}, args...), "--json")...)
	if code != 0 || stderr != "" {
		t.Fatalf("key log = code %d, stderr %q", code, stderr)
	}
	var document keyLogDocument
	if err := json.Unmarshal(assertJSONResult(t, stdout, "key log").Data, &document); err != nil {
		t.Fatalf("decode key log: %v; output = %s", err, stdout)
	}
	return document
}

// keyStates is the project's keys in add order, each with the word `key list`
// marks it with. Most assertions here are really about this sequence: a key
// command's whole job is to change it and nothing else.
func keyStates(t *testing.T, repository string) []string {
	t.Helper()
	document := cliKeyList(t, repository)
	states := make([]string, 0, len(document.Keys))
	for _, row := range document.Keys {
		state := row.State
		if row.Current {
			state = "current"
		}
		states = append(states, row.Key+" "+state)
	}
	return states
}

// cliCreateTaskWithKey mints a task under a named key, mirroring cliCreateTask
// for the one flag that suite has no reason to pass.
func cliCreateTaskWithKey(t *testing.T, repository, title, key string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", title, "--key", key, "--no-sync", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create --key %s = code %d, stderr %q", key, code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "create").Data, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func mustRunKey(t *testing.T, repository string, args ...string) {
	t.Helper()
	if code, _, stderr := run(t, repository, args...); code != 0 {
		t.Fatalf("%v = code %d; stderr = %q", args, code, stderr)
	}
}

// A project nobody has given a second key reports exactly the key it was
// created with, current, and says that nothing is recorded. That flag is the
// whole difference between "this project chose these keys" and "nobody has
// chosen anything yet", and a consumer that cannot tell them apart cannot
// decide whether to offer the setup.
func TestKeyListReportsTheFoundingKeyAsCurrent(t *testing.T) {
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Alpha")
	cliCreateTask(t, repository, "Beta")

	document := cliKeyList(t, repository)
	if document.Current != "WB" {
		t.Fatalf("current = %q, want WB", document.Current)
	}
	if document.Seeded {
		t.Fatalf("seeded = true, want false for a project that has recorded no key change")
	}
	if len(document.Keys) != 1 {
		t.Fatalf("keys = %#v, want the founding key alone", document.Keys)
	}
	row := document.Keys[0]
	if row.Key != "WB" || row.State != string(core.KeyStateActive) || !row.Current {
		t.Fatalf("key = %#v, want WB active and current", row)
	}
	if row.Tasks == nil || *row.Tasks != 2 {
		t.Fatalf("tasks = %v, want both tasks counted under WB", row.Tasks)
	}

	code, stdout, stderr := run(t, repository, "key", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("key list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"KEY  STATE    TASKS",
		"WB   current  2",
		"No key change is recorded, so this is the key this project was created with.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("key list text = %q, want %q", stdout, want)
		}
	}
}

// A project that predates the configuration ledger has no ledger at all, which
// is the state most existing installations are in. Its keys read as the founding
// key, and its first key change seeds the ledger and records that key beside the
// new one in the same commit — so the tasks it already has stay its own.
func TestKeyChangeOnAProjectWithNoLedgerRecordsTheFoundingKey(t *testing.T) {
	repository := preLedgerRepository(t)
	existing := cliCreateTask(t, repository, "Minted before any ledger")

	document := cliKeyList(t, repository)
	if document.Current != "WB" || document.Seeded || document.Head != "" {
		t.Fatalf("list = %#v, want the founding key alone and no ledger", document)
	}

	mustRunKey(t, repository, "key", "add", "NEW", "--current", "--no-sync")
	if got, want := keyStates(t, repository), []string{"WB active", "NEW current"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	// The whole point of the backfill: WB is in the recorded set, so the task
	// minted under it is still this project's.
	if code, _, stderr := run(t, repository, "show", existing.ID, "--json"); code != 0 {
		t.Fatalf("show %s = code %d; stderr = %q", existing.ID, code, stderr)
	}
}

// `key add` without --current adds a key nothing is minted under yet, and the
// reverse is the single command that takes it back out. The current key is
// untouched, which is what makes a project able to prepare a key before it
// switches to it.
func TestKeyAddPrintsTheReverseCommandAndMintsUnderTheCurrentKey(t *testing.T) {
	repository := initializedRepository(t)

	document := cliKeyMutation(t, repository, "key add", "key", "add", "NEW", "--no-sync", "--json")
	if document.Change.Operation != "add" || document.Change.Key != "NEW" ||
		document.Change.State != string(core.KeyStateActive) {
		t.Fatalf("change = %#v, want an add of NEW leaving it active", document.Change)
	}
	if document.Change.From != "" || document.Change.Reactivated {
		t.Fatalf("change = %#v, want nothing moved and nothing reactivated", document.Change)
	}
	if document.Keys.Current != "WB" {
		t.Fatalf("current = %q, want WB: an add that was not asked to move it must not", document.Keys.Current)
	}
	if !document.Keys.Seeded || document.Keys.Head == "" {
		t.Fatalf("keys = %#v, want a recorded set naming the ledger tip", document.Keys)
	}
	if got, want := document.Inverse.Command, "workbook key retire NEW"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if !document.Inverse.Exact || document.Inverse.Note != "" {
		t.Fatalf("inverse = %#v, want an exact reversal with nothing left over", document.Inverse)
	}
	if got, want := keyStates(t, repository), []string{"WB current", "NEW active"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}

	// The founding key is still where new tasks are minted, which is the half
	// of `key add` a caller is most likely to get wrong.
	task := cliCreateTask(t, repository, "Still WB")
	if !strings.HasPrefix(task.ID, "WB-") {
		t.Fatalf("task ID = %q, want a WB- ID", task.ID)
	}
}

// `key add NEW --current` is one commit, and its reverse is one command: give
// the role back. Retiring NEW is deliberately not joined onto it — a reverse
// command is something a person pastes, and `workbook key retire NEW` is
// refused while NEW is current, so the note says what the paste leaves behind
// rather than printing a line that would fail halfway.
func TestKeyAddCurrentMovesTheCurrentKeyAndReversesInOneLine(t *testing.T) {
	repository := initializedRepository(t)
	before := cliCreateTask(t, repository, "Minted under WB")
	if !strings.HasPrefix(before.ID, "WB-") {
		t.Fatalf("task ID = %q, want a WB- ID", before.ID)
	}

	document := cliKeyMutation(t, repository, "key add", "key", "add", "NEW", "--current", "--no-sync", "--json")
	if document.Change.Operation != "add" || document.Change.Key != "NEW" || document.Change.From != "WB" {
		t.Fatalf("change = %#v, want an add of NEW taking the role from WB", document.Change)
	}
	if got := document.Keys.Current; got != "NEW" {
		t.Fatalf("current = %q, want NEW", got)
	}
	if got, want := document.Inverse.Command, "workbook key current WB"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if document.Inverse.Exact {
		t.Fatalf("inverse = %#v, want exact false: giving the role back leaves NEW active", document.Inverse)
	}
	if want := "workbook key retire NEW"; !strings.Contains(document.Inverse.Note, want) {
		t.Fatalf("note = %q, want it to name %q", document.Inverse.Note, want)
	}
	if got, want := keyStates(t, repository), []string{"WB active", "NEW current"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}

	// The controller's requirement, and the reason Service.Keys is wired in
	// this package: `create` mints under the key the ledger says is current,
	// not under the founding key in the project identity.
	after := cliCreateTask(t, repository, "Minted under NEW")
	if !strings.HasPrefix(after.ID, "NEW-") {
		t.Fatalf("task ID = %q, want a NEW- ID", after.ID)
	}
	// And the task minted under the old key is still this project's.
	code, _, stderr := run(t, repository, "show", before.ID, "--json")
	if code != 0 {
		t.Fatalf("show %s = code %d; stderr = %q", before.ID, code, stderr)
	}

	document = cliKeyMutation(t, repository, "key current", "key", "current", "WB", "--no-sync", "--json")
	if document.Change.Operation != "current" || document.Change.Key != "WB" || document.Change.From != "NEW" {
		t.Fatalf("change = %#v, want the role moving from NEW back to WB", document.Change)
	}
	if got, want := document.Inverse.Command, "workbook key current NEW"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if !document.Inverse.Exact {
		t.Fatalf("inverse = %#v, want exact true: moving the role back restores the set", document.Inverse)
	}
}

// The fold converges on a key that is already active by doing nothing. A person
// who typed it is told so instead, and told what to type if what they meant was
// to mint under it.
func TestKeyAddRefusesAKeyThisProjectAlreadyHas(t *testing.T) {
	repository := initializedRepository(t)

	code, _, stderr := run(t, repository, "key", "add", "WB", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `project key "WB" is already active and already current`) {
		t.Fatalf("stderr = %q, want the sentence that says the key is already the current one", stderr)
	}

	mustRunKey(t, repository, "key", "add", "NEW", "--no-sync")
	code, _, stderr = run(t, repository, "key", "add", "NEW", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		`project key "NEW" is already active`,
		"workbook key current NEW",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
}

// Adding a retired key back is the reactivation, and it keeps the key's place in
// the order: the order is add order, and a key that has minted tasks was added
// when it was added.
func TestKeyAddReactivatesARetiredKey(t *testing.T) {
	repository := initializedRepository(t)
	mustRunKey(t, repository, "key", "add", "OLD", "--no-sync")
	mustRunKey(t, repository, "key", "add", "NEW", "--no-sync")
	mustRunKey(t, repository, "key", "retire", "OLD", "--no-sync")
	if got, want := keyStates(t, repository),
		[]string{"WB current", "OLD retired", "NEW active"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}

	document := cliKeyMutation(t, repository, "key add", "key", "add", "OLD", "--no-sync", "--json")
	if !document.Change.Reactivated {
		t.Fatalf("change = %#v, want it to report a reactivation", document.Change)
	}
	if document.Change.State != string(core.KeyStateActive) {
		t.Fatalf("state = %q, want active", document.Change.State)
	}
	if got, want := document.Inverse.Command, "workbook key retire OLD"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if got, want := keyStates(t, repository),
		[]string{"WB current", "OLD active", "NEW active"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want OLD back in its own place %v", got, want)
	}

	code, stdout, stderr := run(t, repository, "key", "add", "OLD2", "--no-sync")
	if code != 0 || stderr != "" {
		t.Fatalf("key add = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "Key:\tadd\tOLD2") {
		t.Errorf("key add text = %q, want the change heading", stdout)
	}
}

// A retired key cannot become current, and the refusal names the keys that can:
// naming what exists is what turns a dead end into the command somebody wanted.
// An unknown key names every key instead, including the retired ones, because a
// typo against a retired key is explained by seeing it listed.
func TestKeyCurrentRefusesARetiredKeyAndNamesTheActiveOnes(t *testing.T) {
	repository := initializedRepository(t)
	mustRunKey(t, repository, "key", "add", "OLD", "--no-sync")
	mustRunKey(t, repository, "key", "retire", "OLD", "--no-sync")

	code, _, stderr := run(t, repository, "key", "current", "OLD", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{`project key "OLD" is retired`, "the active keys are: WB"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}

	code, _, stderr = run(t, repository, "key", "current", "ZZ", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{`no project key "ZZ"`, "its keys are: WB, OLD (retired)"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}

	code, _, stderr = run(t, repository, "key", "current", "WB", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `project key "WB" is already this project's current key`) {
		t.Fatalf("stderr = %q, want the no-op sentence", stderr)
	}
}

// The two retirements that would leave a project unable to mint are refused in
// words, each naming what to do instead. The fold refuses them silently, because
// by the time a pack reaches it the change has already happened somewhere; the
// author is the one who can still act.
//
// Which of the two answers a project gets is decided by the order planKeyRetire
// asks in, and the order is load-bearing: the current key is always active, so a
// project down to one active key would otherwise be told to make another key
// current when it has no other key to name.
func TestKeyRetireRefusesTheCurrentKeyAndTheLastActiveKey(t *testing.T) {
	repository := initializedRepository(t)

	// One key, so it is both the current one and the last active one, and the
	// answer is the one a person can act on.
	code, _, stderr := run(t, repository, "key", "retire", "WB", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		`project key "WB" is this project's only active key`,
		"workbook key add <key>",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "make another key current first") {
		t.Errorf("stderr = %q, names a command with no argument this project could give it", stderr)
	}

	// A second active key makes the current one the only thing in the way, and
	// the refusal becomes the other one.
	mustRunKey(t, repository, "key", "add", "NEW", "--no-sync")
	code, _, stderr = run(t, repository, "key", "retire", "WB", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "make another key current first") {
		t.Fatalf("stderr = %q, want the sentence that says what to do", stderr)
	}

	// And the brief's case: the same refusal for a key `key add --current` has
	// just made current.
	mustRunKey(t, repository, "key", "current", "NEW", "--no-sync")
	code, _, stderr = run(t, repository, "key", "retire", "NEW", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "make another key current first") {
		t.Fatalf("stderr = %q, want the current-key refusal for NEW", stderr)
	}

	document := cliKeyMutation(t, repository, "key retire", "key", "retire", "WB", "--no-sync", "--json")
	if document.Change.Operation != "retire" || document.Change.State != string(core.KeyStateRetired) {
		t.Fatalf("change = %#v, want a retirement of WB", document.Change)
	}
	if got, want := document.Inverse.Command, "workbook key add WB"; got != want {
		t.Fatalf("inverse = %q, want %q", got, want)
	}
	if !document.Inverse.Exact {
		t.Fatalf("inverse = %#v, want exact true: adding the key back restores the set", document.Inverse)
	}

	code, _, stderr = run(t, repository, "key", "retire", "WB", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, `project key "WB" is already retired`) {
		t.Fatalf("stderr = %q, want the already-retired refusal", stderr)
	}
}

// The ceiling is enforced in core at the authoring boundary, where a fold can
// never fail on a count. The planner asks first so the refusal is a sentence
// naming the verb that makes room, rather than the boundary's report of a
// configuration that was never written.
func TestKeyAddRefusesPastTheKeyCeiling(t *testing.T) {
	repository := initializedRepository(t)
	// The founding key counts, and the store back-fills it into the first key
	// change, so filling the ceiling takes one fewer add than the ceiling.
	for index := 1; index < core.MaxProjectKeys; index++ {
		mustRunKey(t, repository, "key", "add", fmt.Sprintf("K%d", index), "--no-sync")
	}
	if got := len(cliKeyList(t, repository).Keys); got != core.MaxProjectKeys {
		t.Fatalf("keys = %d, want the ceiling %d", got, core.MaxProjectKeys)
	}

	code, _, stderr := run(t, repository, "key", "add", "LAST", "--no-sync")
	if code != 5 {
		t.Fatalf("code = %d, want 5; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		fmt.Sprintf("at most %d", core.MaxProjectKeys),
		"workbook key retire",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}

	// A reactivation does not grow the set, so the ceiling does not refuse it.
	mustRunKey(t, repository, "key", "retire", "K1", "--no-sync")
	mustRunKey(t, repository, "key", "add", "K1", "--no-sync")
}

// `key log` mirrors `status log`: one entry per commit that changed a key,
// oldest first, each with the command that reverses it. The founding key the
// store records ahead of a project's first key change is bookkeeping rather than
// news, so the entry is about what somebody ran.
func TestKeyLogListsKeyOperationsWithTheirInverses(t *testing.T) {
	repository := initializedRepository(t)
	if empty := cliKeyLog(t, repository); empty.Total != 0 || len(empty.Entries) != 0 {
		t.Fatalf("log = %#v, want nothing before any key change", empty)
	}
	// A status change proves the log reports its own section: this commit is in
	// the same ledger and is not a key change.
	mustRunKey(t, repository, "status", "add", "triage", "--no-sync", "--no-docs")

	mustRunKey(t, repository, "key", "add", "NEW", "--current", "--no-sync")
	mustRunKey(t, repository, "key", "add", "OLD", "--no-sync")
	mustRunKey(t, repository, "key", "retire", "OLD", "--no-sync")

	document := cliKeyLog(t, repository)
	if document.Total != 3 || len(document.Entries) != 3 {
		t.Fatalf("log = %#v, want the three key commits", document)
	}
	first := document.Entries[0]
	if first.Operation != string(core.ConfigKeyAdd) {
		t.Fatalf("first operation = %q, want %q; the back-filled founding key is not the news",
			first.Operation, core.ConfigKeyAdd)
	}
	if !strings.Contains(first.Summary, "added project key NEW") {
		t.Fatalf("first summary = %q, want it to name NEW", first.Summary)
	}
	if first.Collapsed != 1 {
		t.Fatalf("first collapsed = %d, want 1: the same commit made NEW current", first.Collapsed)
	}
	if first.Inverse == nil || first.Inverse.Command != "workbook key current WB" {
		t.Fatalf("first inverse = %#v, want the role going back to WB", first.Inverse)
	}
	if first.Actor == "" || first.OperationID == "" || first.Commit == "" {
		t.Fatalf("first entry = %#v, want the ledger's own attribution", first)
	}
	if last := document.Entries[2]; last.Inverse == nil ||
		last.Inverse.Command != "workbook key add OLD" || !last.Inverse.Exact {
		t.Fatalf("last inverse = %#v, want an exact `key add OLD`", document.Entries[2].Inverse)
	}

	if limited := cliKeyLog(t, repository, "--limit", "1"); limited.Showing != 1 || limited.Total != 3 {
		t.Fatalf("limited log = %#v, want 1 of 3", limited)
	}

	code, stdout, stderr := run(t, repository, "key", "log")
	if code != 0 || stderr != "" {
		t.Fatalf("key log = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"Showing all 3 change(s).",
		"inverse:\tworkbook key current WB",
		"inverse:\tworkbook key add OLD",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("key log text = %q, want %q", stdout, want)
		}
	}
}

// Every refusal in this family is a validation error, which is exit 5 and the
// voice the status and priority refusals use. A malformed key never reaches the
// ledger, and a missing subcommand names the ones that exist.
func TestKeyVerbsExitFiveOnAValidationError(t *testing.T) {
	repository := initializedRepository(t)
	for _, verb := range []string{"add", "current", "retire"} {
		code, _, stderr := run(t, repository, "key", verb, "lower", "--no-sync")
		if code != 5 {
			t.Errorf("key %s lower = code %d, want 5; stderr = %q", verb, code, stderr)
		}
		if !strings.Contains(stderr, "project key") {
			t.Errorf("key %s lower stderr = %q, want the key grammar refusal", verb, stderr)
		}
	}

	code, _, stderr := run(t, repository, "key")
	if code != 2 {
		t.Fatalf("bare key = code %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "the subcommands are list, add, current, retire, log") {
		t.Fatalf("stderr = %q, want the subcommand list", stderr)
	}

	code, _, stderr = run(t, repository, "key", "WB-01K0M6B8A4FTT8C39MXXYTW7C2")
	if code != 2 {
		t.Fatalf("key <task> = code %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "workbook show WB-01K0M6B8A4FTT8C39MXXYTW7C2") {
		t.Fatalf("stderr = %q, want the command the caller wanted", stderr)
	}
}

// `create --key` mints under the key the flag names rather than the current
// one, which is the whole point of offering the choice: a caller preparing a
// second subproject's first task should not have to make that key current
// first just to mint under it.
func TestCreateMintsUnderTheKeyTheFlagNames(t *testing.T) {
	repository := initializedRepository(t)
	mustRunKey(t, repository, "key", "add", "NEW", "--no-sync")
	if got, want := keyStates(t, repository), []string{"WB current", "NEW active"}; !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}

	task := cliCreateTaskWithKey(t, repository, "Under NEW", "NEW")
	if !strings.HasPrefix(task.ID, "NEW-") {
		t.Fatalf("task ID = %q, want a NEW- ID", task.ID)
	}

	// Without the flag, the current key still wins.
	other := cliCreateTask(t, repository, "Under WB")
	if !strings.HasPrefix(other.ID, "WB-") {
		t.Fatalf("task ID = %q, want a WB- ID", other.ID)
	}
}

// A retired key mints nothing new: its tasks are still this project's, but
// `create --key` on it is refused, naming the keys that are still active —
// the retired one is not among them, which is the whole difference between
// retiring a key and deleting it.
func TestCreateRefusesARetiredKeyAndNamesTheActiveOnes(t *testing.T) {
	repository := initializedRepository(t)
	mustRunKey(t, repository, "key", "add", "NEW", "--current", "--no-sync")
	mustRunKey(t, repository, "key", "retire", "WB", "--no-sync")

	code, stdout, stderr := run(t, repository, "create", "Under WB", "--key", "WB", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("create --key WB = code %d, want 5; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("create --key WB stdout = %q, want nothing minted", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`project key "WB" is retired, so no new task is minted under it; the active keys are: NEW`)
}

// A key this project has never heard of is refused the same way, naming every
// key it does have so the caller can tell a typo from a teammate's key this
// checkout has not fetched yet.
func TestCreateRefusesAnUnknownKey(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "create", "Under ZZ", "--key", "ZZ", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("create --key ZZ = code %d, want 5; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("create --key ZZ stdout = %q, want nothing minted", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`no project key "ZZ" in this project; its active keys are: WB`)
}

// `list --key` on a retired key still answers: its tasks did not stop
// existing when the key stopped minting new ones, and a filter that refused a
// retired key would make a project's own history unreadable by the key it was
// recorded under.
func TestListFiltersByKeyIncludingARetiredOne(t *testing.T) {
	repository := initializedRepository(t)
	underWB := cliCreateTask(t, repository, "Under WB")
	mustRunKey(t, repository, "key", "add", "NEW", "--current", "--no-sync")
	underNew := cliCreateTask(t, repository, "Under NEW")
	mustRunKey(t, repository, "key", "retire", "WB", "--no-sync")

	code, stdout, stderr := run(t, repository, "list", "--key", "WB", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list --key WB = code %d, stderr %q", code, stderr)
	}
	var listed []core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != underWB.ID {
		t.Fatalf("list --key WB = %#v, want the one task minted under the now-retired WB", listed)
	}

	code, stdout, stderr = run(t, repository, "list", "--key", "NEW", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("list --key NEW = code %d, stderr %q", code, stderr)
	}
	listed = nil
	if err := json.Unmarshal(assertJSONResult(t, stdout, "list").Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != underNew.ID {
		t.Fatalf("list --key NEW = %#v, want the one task minted under NEW", listed)
	}
}

// An unknown key filter is refused rather than answered with an empty list,
// the same way an unknown status filter is: an empty table cannot be told
// apart from "no such key" any other way.
func TestListRefusesAnUnknownKeyNamingTheKnownOnes(t *testing.T) {
	repository := initializedRepository(t)
	mustRunKey(t, repository, "key", "add", "NEW", "--no-sync")

	code, stdout, stderr := run(t, repository, "list", "--key", "ZZ", "--json")
	if code != 5 {
		t.Fatalf("list --key ZZ = code %d, want 5; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("list --key ZZ stdout = %q, want nothing listed", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		`no project key "ZZ" in this project; its keys are: WB, NEW; fetch if a teammate added it`)
}

func TestKeyHelpDocumentsTheFamily(t *testing.T) {
	repository := initializedRepository(t)
	code, stdout, stderr := run(t, repository, "help", "key")
	if code != 0 || stderr != "" {
		t.Fatalf("help key = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"workbook key <command> [options]",
		"list", "add", "current", "retire", "log",
		"A retired key is never deleted",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help key = %q, want %q", stdout, want)
		}
	}
}
