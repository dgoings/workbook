package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// A project that has never configured its statuses reads the pre-ledger six and
// says so. That flag is the whole difference between "this project chose these"
// and "nobody has chosen anything yet", and a consumer that cannot tell them
// apart cannot decide whether to offer the setup.
//
// The six include `blocked`, which this build no longer mints a project with.
// Nothing removes it: an upgrade that dropped a column would move somebody's
// tasks without being asked. The listing says so instead, and prints the command
// that does the removal when its reader wants it.
func TestStatusListReadsTheBuiltInVocabularyOnALedgerlessProject(t *testing.T) {
	t.Parallel()
	repository := preLedgerRepository(t)
	cliCreateTask(t, repository, "Alpha")
	cliCreateTask(t, repository, "Beta")

	document := cliStatusList(t, repository)
	if document.Seeded || document.Head != "" {
		t.Fatalf("list = seeded %t, head %q; want an unseeded project", document.Seeded, document.Head)
	}
	if document.Default != "backlog" {
		t.Fatalf("default = %q, want backlog", document.Default)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "blocked", "in-progress", "in-review", "done",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want the six a pre-ledger project is using %v", got, want)
	}
	first := document.Statuses[0]
	if first.Status != "backlog" || first.Label != "Backlog" || first.Order != 1 ||
		first.Tasks == nil || *first.Tasks != 2 {
		t.Fatalf("first status = %#v, want backlog holding both tasks", first)
	}
	if len(document.Retired) != 0 || len(document.Unresolved) != 0 || len(document.Advisories) != 0 {
		t.Fatalf("list = %#v, want nothing retired, unresolved, or advised", document)
	}
	if len(document.Migrations) != 1 {
		t.Fatalf("migrations = %#v, want exactly the note about `blocked`", document.Migrations)
	}
	migration := document.Migrations[0]
	if migration.Status != "blocked" ||
		migration.Command != "workbook status delete blocked --into backlog" ||
		!strings.Contains(migration.Reason, "task dependencies record what a task is waiting on") {
		t.Fatalf("migration = %#v, want blocked, a reason, and the removal command", migration)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"#  STATUS       LABEL        TAGS     TASKS",
		"1  backlog      Backlog      default  2",
		"6  done         Done         done     0",
		"No status change is recorded, so these are the statuses Workbook reads for a project that has none of its own.",
		"No longer a default:",
		"remove it when this project no longer needs it: workbook status delete blocked --into backlog",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status list text = %q, want %q", stdout, want)
		}
	}
}

// A project minted by this build gets the five statuses it ships, records them
// in a genesis rather than leaning on a fallback, and is told nothing about a
// status it does not have.
func TestStatusListReportsTheMintedVocabularyOnAFreshProject(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	cliCreateTask(t, repository, "Alpha")

	document := cliStatusList(t, repository)
	if !document.Seeded || document.Head == "" {
		t.Fatalf("list = seeded %t, head %q; want a project whose genesis was written", document.Seeded, document.Head)
	}
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "in-progress", "in-review", "done",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want the five this build mints %v", got, want)
	}
	if document.Default != "backlog" {
		t.Fatalf("default = %q, want backlog", document.Default)
	}
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none for a project that never had `blocked`", document.Migrations)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, unwanted := range []string{"blocked", "No longer a default:", "No status change is recorded"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("status list text = %q, want no %q", stdout, unwanted)
		}
	}
}

// The note follows the status rather than the ledger. A project whose genesis
// was seeded from the pre-ledger vocabulary — which is what the first status
// change on an existing project writes — still defines `blocked`, and still has
// the same thing to be told about it.
func TestStatusListNotesTheDroppedDefaultOnASeededLegacyProject(t *testing.T) {
	t.Parallel()
	repository := preLedgerRepository(t)
	mustRunStatus(t, repository, "status", "label", "ready", "Up Next")

	document := cliStatusList(t, repository)
	if !document.Seeded {
		t.Fatalf("list = seeded %t, want the status change to have seeded a genesis", document.Seeded)
	}
	if got, want := len(document.Statuses), 6; got != want {
		t.Fatalf("statuses = %d, want %d; the genesis is seeded from the pre-ledger vocabulary", got, want)
	}
	if len(document.Migrations) != 1 || document.Migrations[0].Status != "blocked" {
		t.Fatalf("migrations = %#v, want the note about `blocked`", document.Migrations)
	}

	// Removing it makes the note go away.
	mustRunStatus(t, repository, "status", "delete", "blocked", "--into", "backlog")
	if got := cliStatusList(t, repository).Migrations; len(got) != 0 {
		t.Fatalf("migrations after the removal = %#v, want none", got)
	}

	// And so does deciding against it. A project that reads the note, removes
	// the column, and later wants it back has answered the question the note
	// asks; the ledger records that it did, so the listing stops asking.
	mustRunStatus(t, repository, "status", "add", "blocked")
	if got := cliStatusList(t, repository).Migrations; len(got) != 0 {
		t.Fatalf("migrations after adding it back = %#v, want none: the add is the decision", got)
	}
}

// A project minted with the five statuses this build ships, which then adds
// `blocked` on purpose, is never told that Workbook stopped shipping it. The
// vocabulary it ends up with is the pre-ledger six minus a column, and no test
// of membership could tell the two apart — the `status.add` in its ledger is
// what does.
func TestStatusListSaysNothingAboutADeliberatelyAddedBlocked(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustRunStatus(t, repository, "status", "add", "blocked", "--label", "Blocked", "--after", "ready")

	document := cliStatusList(t, repository)
	if got, want := cliStatusNames(t, repository), []string{
		"backlog", "ready", "blocked", "in-progress", "in-review", "done",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if len(document.Migrations) != 0 {
		t.Fatalf("migrations = %#v, want none: this project chose `blocked`", document.Migrations)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, "No longer a default:") {
		t.Errorf("status list text = %q, want no note about a status this project added", stdout)
	}
}

// A project whose new tasks land in `blocked` gets the note with a different
// next step rather than no note at all.
//
// `status delete` refuses to forward a status into itself and refuses to leave a
// project with nowhere for new work to land, so the removal is not a command
// that exists for them yet — but they are the readers with the most invested in
// the column and the least reason to be told nothing. The absent `command` is
// what a caller reads the difference from, and `first` carries the step that
// makes a removal expressible at all.
func TestStatusListNamesTheFirstStepWhenBlockedHoldsTheDefaultTag(t *testing.T) {
	t.Parallel()
	repository := preLedgerRepository(t)
	mustRunStatus(t, repository, "status", "tag", "blocked", "--tag", "default")

	document := cliStatusList(t, repository)
	if document.Default != "blocked" {
		t.Fatalf("default = %q, want blocked", document.Default)
	}
	if len(document.Migrations) != 1 {
		t.Fatalf("migrations = %#v, want the note about `blocked`", document.Migrations)
	}
	migration := document.Migrations[0]
	if migration.Status != "blocked" {
		t.Fatalf("migration = %#v, want it to name blocked", migration)
	}
	if migration.Command != "" {
		t.Fatalf("migration command = %q, want none: no single command removes the default holder", migration.Command)
	}
	if migration.First != "workbook status tag <status> --tag default" {
		t.Fatalf("migration first = %q, want the tag handoff", migration.First)
	}
	if !strings.Contains(migration.Reason, "new tasks land in it") {
		t.Fatalf("migration reason = %q, want it to say why the removal needs a step first", migration.Reason)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout,
		"removing it starts by giving the default tag to another status: workbook status tag <status> --tag default") {
		t.Errorf("status list text = %q, want the first step", stdout)
	}
	if strings.Contains(stdout, "remove it when this project no longer needs it") {
		t.Errorf("status list text = %q, want no removal command it cannot run", stdout)
	}
}

// blockedProvenanceVocabulary is the shape every provenance case shares. The
// vocabulary is the same in all of them by construction — an inherited `blocked`
// and one somebody added back are indistinguishable in it, which is the whole
// reason the ledger is consulted.
func blockedProvenanceVocabulary(t *testing.T, defaultHolder core.Status) core.Vocabulary {
	t.Helper()
	definitions := []core.StatusDefinition{
		{Status: "backlog", Label: "Backlog", Rank: "1/1", Tags: []core.StatusTag{}},
		{Status: core.StatusBlocked, Label: "Blocked", Rank: "2/1", Tags: []core.StatusTag{}},
		{Status: "done", Label: "Done", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagDone}},
	}
	for index, definition := range definitions {
		if definition.Status == defaultHolder {
			definitions[index].Tags = append(definitions[index].Tags, core.StatusTagDefault)
		}
	}
	vocabulary, err := core.NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// configLedgerOf builds a read window of one operation per commit. The walk
// reads operation types and the statuses they name and nothing else, so a
// genesis here carries no configuration document: what it stands for is the
// boundary between what a project inherited and what somebody did to it.
func configLedgerOf(operations ...core.ConfigOperation) configLedgerWindow {
	commits := make([]configLedgerCommit, 0, len(operations))
	for _, operation := range operations {
		commits = append(commits, configLedgerCommit{
			Pack: core.ConfigOperationPack{Operations: []core.ConfigOperation{operation}},
		})
	}
	return configLedgerWindow{Found: len(commits) > 0, Commits: commits, Total: len(commits)}
}

// The note keys on provenance rather than on membership, so a project that
// added `blocked` back deliberately is told about it once — by the command that
// refused nothing — instead of on every listing forever.
//
// The ledger is what separates the cases, and it separates them by recording
// what somebody did: a `status.add` naming `blocked` after the genesis is a
// decision, and a `blocked` the genesis itself carried is an inheritance.
func TestDroppedDefaultStatusesKeysOnProvenance(t *testing.T) {
	t.Parallel()
	genesis := core.ConfigOperation{Type: core.ConfigGenesis}
	add := core.ConfigOperation{Type: core.ConfigStatusAdd, Name: core.StatusBlocked, Rank: "2/1"}
	remove := core.ConfigOperation{
		Type: core.ConfigStatusRemove, Status: core.StatusBlocked, Destination: "backlog",
	}

	for _, test := range []struct {
		name          string
		defaultHolder core.Status
		ledger        configLedgerWindow
		wantNote      bool
	}{
		{
			name:          "genesis seeded from the pre-ledger vocabulary carries blocked",
			defaultHolder: "backlog",
			ledger:        configLedgerOf(genesis),
			wantNote:      true,
		},
		{
			name:          "an unseeded project reading the legacy fallback has no ledger to walk",
			defaultHolder: "backlog",
			ledger:        configLedgerWindow{},
			wantNote:      true,
		},
		{
			name:          "a fresh mint that added blocked back",
			defaultHolder: "backlog",
			ledger:        configLedgerOf(genesis, add),
			wantNote:      false,
		},
		{
			name:          "a legacy-seeded project that removed blocked and later added it back",
			defaultHolder: "backlog",
			ledger:        configLedgerOf(genesis, remove, add),
			wantNote:      false,
		},
		{
			name:          "a rename that lands on the name",
			defaultHolder: "backlog",
			ledger: configLedgerOf(genesis, core.ConfigOperation{
				Type: core.ConfigStatusRename, From: "waiting", To: core.StatusBlocked,
			}),
			wantNote: false,
		},
		{
			// The add is older than the window, so the walk sees no evidence of it.
			// Absent evidence the note shows: nagging a reader who has already
			// decided costs them a sentence, and hiding the guidance from a project
			// that never migrated costs them the only place it is said.
			name:          "an add older than the bounded window",
			defaultHolder: "backlog",
			ledger: configLedgerWindow{
				Found: true,
				Total: maxDatedConfigCommits + 12,
				Commits: []configLedgerCommit{{Pack: core.ConfigOperationPack{
					Operations: []core.ConfigOperation{{
						Type: core.ConfigStatusRelabel, Status: "backlog", Label: "Inbox",
					}},
				}}},
			},
			wantNote: true,
		},
		{
			// A project that added the column and made it where new work lands is
			// the one reader with no escape — no removal to run, no window to fall
			// out of — so it is the last one to nag. Provenance is answered ahead
			// of the shape the note would take, and `status delete` explains the
			// tag handoff at the moment somebody actually tries the removal.
			name:          "blocked holding the default tag after a deliberate add",
			defaultHolder: core.StatusBlocked,
			ledger:        configLedgerOf(genesis, add),
			wantNote:      false,
		},
		{
			// The handoff note still reaches the project that inherited the column
			// and never chose anything.
			name:          "blocked holding the default tag by inheritance",
			defaultHolder: core.StatusBlocked,
			ledger:        configLedgerOf(genesis),
			wantNote:      true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			vocabulary := blockedProvenanceVocabulary(t, test.defaultHolder)

			migrations := droppedDefaultStatuses(vocabulary, test.ledger)

			if !test.wantNote {
				if len(migrations) != 0 {
					t.Fatalf("migrations = %#v, want none for a deliberate `blocked`", migrations)
				}
				return
			}
			if len(migrations) != 1 || migrations[0].Status != core.StatusBlocked {
				t.Fatalf("migrations = %#v, want the note about `blocked`", migrations)
			}
			if test.defaultHolder == core.StatusBlocked {
				if migrations[0].Command != "" || migrations[0].First == "" {
					t.Fatalf("migration = %#v, want the tag handoff rather than a removal command", migrations[0])
				}
			}
		})
	}
}

// A project that never defined `blocked` is told nothing, whatever its ledger
// records. The membership test comes first, so the common listing pays for no
// walk at all.
func TestDroppedDefaultStatusesSaysNothingWithoutBlocked(t *testing.T) {
	t.Parallel()
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{
		{Status: "backlog", Label: "Backlog", Rank: "1/1", Tags: []core.StatusTag{core.StatusTagDefault}},
		{Status: "done", Label: "Done", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}

	ledger := configLedgerOf(core.ConfigOperation{Type: core.ConfigGenesis})
	if migrations := droppedDefaultStatuses(vocabulary, ledger); len(migrations) != 0 {
		t.Fatalf("migrations = %#v, want none for a project that never had `blocked`", migrations)
	}
}

// The list names the tasks nothing can place, and the command that places them.
// A stored status that resolves nowhere is invisible to every count — it is in
// no column — so the census that produces the counts is what has to find it.
func TestStatusListReportsUnresolvedStoredStatuses(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	kept := cliCreateTask(t, repository, "In a real column")
	stranded := writeTaskInAnUndefinedStatus(t, repository, "shipped", "Written by a newer clone")

	document := cliStatusList(t, repository)
	if len(document.Unresolved) != 1 {
		t.Fatalf("unresolved = %#v, want the one stranded status", document.Unresolved)
	}
	entry := document.Unresolved[0]
	if entry.Status != "shipped" || entry.Tasks != 1 || len(entry.TaskIDs) != 1 || entry.TaskIDs[0] != stranded {
		t.Fatalf("unresolved entry = %#v, want the stranded task", entry)
	}
	for _, status := range document.Statuses {
		if status.Tasks == nil {
			t.Fatalf("status %q reports no task count", status.Status)
		}
		if status.Status == "backlog" && *status.Tasks != 1 {
			t.Fatalf("backlog holds %d tasks, want only %s", *status.Tasks, kept.ID)
		}
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"\tUnresolved:\tshipped\t1 task(s)\t" + stranded,
		"correct with: workbook update <task> --status <status>",
		"workbook status add shipped",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status list text = %q, want %q", stdout, want)
		}
	}
}

// One status can strand more work than a status table can hold, so the IDs are
// a bounded sample and the listing says so. The count beside them is never
// sampled: "how much is stranded" stays exact while "which tasks" becomes a
// place to start.
func TestStatusListBoundsTheUnresolvedTaskIDs(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	stranded := make([]string, 0, maxUnresolvedTaskIDs+2)
	for index := range maxUnresolvedTaskIDs + 2 {
		stranded = append(stranded,
			writeTaskInAnUndefinedStatus(t, repository, "shipped", "Stranded "+strconv.Itoa(index)))
	}
	sort.Strings(stranded)

	document := cliStatusList(t, repository)
	if len(document.Unresolved) != 1 {
		t.Fatalf("unresolved = %#v, want the one stranded status", document.Unresolved)
	}
	entry := document.Unresolved[0]
	if entry.Tasks != len(stranded) {
		t.Fatalf("unresolved count = %d, want the exact %d", entry.Tasks, len(stranded))
	}
	if !equalStrings(entry.TaskIDs, stranded[:maxUnresolvedTaskIDs]) {
		t.Fatalf("unresolved ids = %v, want the first %d in ID order", entry.TaskIDs, maxUnresolvedTaskIDs)
	}

	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	want := fmt.Sprintf("(first %d of %d)", maxUnresolvedTaskIDs, len(stranded))
	if !strings.Contains(stdout, want) {
		t.Fatalf("status list text = %q, want %q", stdout, want)
	}
	if strings.Contains(stdout, stranded[len(stranded)-1]) {
		t.Fatalf("status list text = %q, want the sample cut short rather than the whole set", stdout)
	}
}

// A folded configuration may sit over a size ceiling without anybody having
// been refused anything: two clones adding statuses concurrently is enough. The
// list says so where a person is already looking at what they would remove.
func TestStatusListReportsTheOverCeilingAdvisory(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)

	// One clone fills the project to the ceiling and publishes it.
	additions := make([]core.ConfigOperation, 0, core.MaxStatusCount-6)
	for index := range core.MaxStatusCount - 6 {
		name := core.Status(fmt.Sprintf("extra-%02d", index))
		additions = append(additions, core.ConfigOperation{
			Type:  core.ConfigStatusAdd,
			Name:  name,
			Label: core.DerivedStatusLabel(name),
			Rank:  fmt.Sprintf("%d/1", 100+index),
		})
	}
	writeConfigOperations(t, first, additions)
	if code, _, stderr := run(t, first, "push"); code != 0 {
		t.Fatalf("push = code %d; stderr = %q", code, stderr)
	}

	// The other clone adds one status without having seen any of that, which is
	// a change neither author could have been refused.
	mustRunStatus(t, second, "status", "add", "triage", "--no-sync")
	if code, _, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = code %d; stderr = %q", code, stderr)
	}

	document := cliStatusList(t, second)
	if got, want := len(document.Statuses), core.MaxStatusCount+1; got != want {
		t.Fatalf("statuses = %d, want %d after the reconcile", got, want)
	}
	if len(document.Advisories) != 1 || document.Advisories[0].Code != "status-ceiling-exceeded" {
		t.Fatalf("advisories = %#v, want the ceiling advisory", document.Advisories)
	}
	if !strings.Contains(document.Advisories[0].Message, "removing one brings it back under") {
		t.Fatalf("advisory = %q, want the way back named", document.Advisories[0].Message)
	}

	code, stdout, stderr := run(t, second, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tAdvisory:\tstatus-ceiling-exceeded\t") {
		t.Fatalf("status list text = %q, want the advisory line", stdout)
	}

	// Growth is refused while over the ceiling, and the refusal names the verb
	// that brings it back under.
	code, _, stderr = run(t, second, "status", "add", "another", "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("status add over the ceiling = code %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation,
		fmt.Sprintf("the project would define %d statuses and must not exceed %d; "+
			"remove one first: workbook status delete <status> --into <status>",
			core.MaxStatusCount+2, core.MaxStatusCount))
}

// The list filter refuses a value this project does not have — on both
// surfaces — and follows one it can still resolve, saying what it did. An empty
// table is indistinguishable from an empty column, so the refusal is what
// reports the one fact worth reporting: this checkout has never heard of that
// name, which means it is out of sync rather than looking at an empty column.
// unknownFilterStatusRefusal is what this fixture's project says about a status
// it does not define: the statuses it does have, and the fix. A bare `invalid
// task status "typoo"` was the same sentence for a typo, for a display label,
// and for a status a teammate added that this checkout has not fetched — the
// last of which is the case this refusal exists to report at all.
const unknownFilterStatusRefusal = `no status "typoo" in this project; ` +
	`the statuses are: backlog, queued, in-progress, in-review, done; fetch if a teammate added it`

func TestListStatusFilterRefusesAnUnknownStatusAndFollowsAResolvableOne(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Alpha")
	mustRunStatus(t, repository, "update", task.ID, "--status", "ready", "--no-sync")
	mustRunStatus(t, repository, "status", "rename", "ready", "queued")

	t.Run("unknown status in JSON", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "typoo", "--json")
		if code != 5 {
			t.Fatalf("list --status typoo = code %d, want 5; stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("list --status typoo stdout = %q, want nothing listed", stdout)
		}
		assertJSONError(t, stderr, core.CategoryValidation, unknownFilterStatusRefusal)
	})

	t.Run("unknown status in text", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "typoo")
		if code != 5 {
			t.Fatalf("list --status typoo = code %d, want 5; stderr = %q", code, stderr)
		}
		if strings.Contains(stdout, task.ID) {
			t.Fatalf("list --status typoo listed %q", stdout)
		}
		if stderr != "workbook: "+unknownFilterStatusRefusal+"\n" {
			t.Fatalf("stderr = %q, want one refusal line naming the status", stderr)
		}
	})

	t.Run("resolvable status is followed", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "ready", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("list --status ready = code %d, stderr %q", code, stderr)
		}
		result := assertJSONResult(t, stdout, "list")
		var tasks []core.Task
		if err := json.Unmarshal(result.Data, &tasks); err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 || tasks[0].ID != task.ID {
			t.Fatalf("list --status ready returned %#v, want the renamed status's task", tasks)
		}
		if len(result.Warnings) != 1 ||
			result.Warnings[0].Message != `no status "ready" in this project's vocabulary; it was renamed to "queued", and "queued" is what was listed` {
			t.Fatalf("warnings = %#v, want the rename explained", result.Warnings)
		}
	})

	t.Run("a live status warns about nothing", func(t *testing.T) {
		code, stdout, stderr := run(t, repository, "list", "--status", "queued", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("list --status queued = code %d, stderr %q", code, stderr)
		}
		if len(assertJSONResult(t, stdout, "list").Warnings) != 0 {
			t.Fatalf("list --status queued warned about a live status: %q", stdout)
		}
	})
}

// The listing dates a retirement from the newest commits and says nothing for
// one older than that, rather than reading a history that only grows to answer
// a courtesy.
func TestStatusListDatesRecentRetirementsAndDegradesBeyondItsBound(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	// One early retirement, then enough commits to push it past the bound.
	mustRunStatus(t, repository, "status", "add", "ancient", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "ancient", "--into", "backlog")
	packs := make([][]core.ConfigOperation, 0, maxDatedConfigCommits)
	for index := range maxDatedConfigCommits {
		packs = append(packs, []core.ConfigOperation{{
			Type:   core.ConfigStatusRelabel,
			Status: "backlog",
			Label:  fmt.Sprintf("Backlog %d", index),
		}})
	}
	writeConfigCommits(t, repository, packs)
	mustRunStatus(t, repository, "status", "add", "recent", "--after", "backlog")
	mustRunStatus(t, repository, "status", "delete", "recent", "--into", "backlog")

	document := cliStatusList(t, repository)
	dates := make(map[string]string, len(document.Retired))
	for _, retired := range document.Retired {
		dates[retired.Status] = retired.At
	}
	if len(dates) != 2 {
		t.Fatalf("retired = %#v, want both removals reported", document.Retired)
	}
	if dates["recent"] == "" {
		t.Fatal("the recent removal carries no date, want one from inside the bound")
	}
	if dates["ancient"] != "" {
		t.Fatalf("the ancient removal carries the date %q, want none from beyond the bound", dates["ancient"])
	}

	// Both are still reported, with the undated one simply missing its clause.
	code, stdout, stderr := run(t, repository, "status", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("status list = code %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "\tRetired:\tancient → backlog\tremoved\n") {
		t.Fatalf("status list text = %q, want the undated retirement with no clause", stdout)
	}
	if !strings.Contains(stdout, "\tRetired:\trecent → backlog\tremoved on ") {
		t.Fatalf("status list text = %q, want the dated retirement", stdout)
	}
}

// writeTaskInAnUndefinedStatus records a task whose stored status this project
// cannot resolve.
//
// It goes through the core service against a vocabulary of its own rather than
// through a command, because no command can produce this state: the mutation
// boundary refuses a status the project does not define, and every rename and
// removal leaves a forwarding pointer behind. What it reproduces is a task
// written by a clone whose configuration this one has never seen — the state
// the unresolved report exists for.
func writeTaskInAnUndefinedStatus(t *testing.T, repository, status, title string) string {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	store, releaseProjection, err := openProjection(ctx, repo, config)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProjection()
	actor, err := repo.Actor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{{
		Status: core.Status(status),
		Label:  core.DerivedStatusLabel(core.Status(status)),
		Rank:   "1/1",
		Tags:   []core.StatusTag{core.StatusTagDefault, core.StatusTagNext, core.StatusTagDone},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := core.Service{
		Config:     config,
		Vocabulary: vocabulary,
		Reader:     store,
		Writer:     repo,
		Projection: store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
		Actor:      actor,
	}
	result, err := service.CreateMutation(ctx, core.CreateInput{Title: title, Status: core.Status(status)})
	if err != nil {
		t.Fatalf("CreateMutation() error = %v", err)
	}
	return result.Task.ID
}

// writeConfigOperations records a batch of configuration operations directly,
// for a state no single command produces.
func writeConfigOperations(t *testing.T, repository string, operations []core.ConfigOperation) {
	t.Helper()
	writeConfigCommits(t, repository, [][]core.ConfigOperation{operations})
}

// writeConfigCommits records one commit per batch through one opened
// repository, for a test that needs a ledger longer than it needs commands.
func writeConfigCommits(t *testing.T, repository string, packs [][]core.ConfigOperation) {
	t.Helper()
	ctx := context.Background()
	repo, err := gitstore.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	config, err := repo.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, operations := range packs {
		if _, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{}, operations, ""); err != nil {
			t.Fatalf("WriteConfigOperation() error = %v", err)
		}
	}
}
