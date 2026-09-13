package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
)

// What the board's priority administration does, through the real wiring.
//
// These tests drive boardPriorities directly rather than over HTTP, because the
// routes do not exist yet and because the properties worth pinning here are
// about the writer rather than about a request: that every change is authored
// by the planner `workbook priority` uses, that a refusal the planner makes
// reaches the caller in the planner's own words, and that the head a change
// names is the head it lands on. The ledger is read back through the CLI, which
// is the whole point of the exercise — one project, two surfaces, one set of
// rules.

// A priority added through the board is added by `workbook priority add`'s own
// planner: it lands where the board asked for it, derives the label the verb
// derives, and the CLI reads the result out of the same ledger.
func TestBoardPriorityAddLandsThroughTheSharedLedger(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	mutation, err := board.add(ctx, boardPriorityAddition{
		priorityAddition: priorityAddition{Priority: "urgent", Before: "high"},
		ExpectedHead:     head,
	})
	if err != nil {
		t.Fatalf("add a priority through the board: %v", err)
	}
	if mutation.State.Head == head {
		t.Fatalf("head = %q, want a head past %q", mutation.State.Head, head)
	}
	if got, want := boardPriorityNames(mutation.State.Priorities), []string{"urgent", "high", "medium", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	// A change that moved no tasks still says so, so a client reads the same
	// member from every mutation.
	if mutation.Tasks.Affected != 0 {
		t.Fatalf("tasks = %#v, want zero for a change that moved nothing", mutation.Tasks)
	}

	if got, want := cliPriorityNames(t, repository), []string{"urgent", "high", "medium", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities the CLI reads = %v, want %v", got, want)
	}
	listed := cliPriorityList(t, repository)
	if listed.Head != mutation.State.Head {
		t.Fatalf("priority list head = %q, want the head the board reported %q", listed.Head, mutation.State.Head)
	}
	if listed.Priorities[0].Label != "Urgent" {
		t.Fatalf("added priority label = %q, want the derived %q", listed.Priorities[0].Label, "Urgent")
	}
	if subject := gitOutput(t, repository, "log", "-1", "--format=%s", mutation.State.Head); subject != "workbook: add priority urgent" {
		t.Fatalf("ledger commit subject = %q, want the verb's own subject", subject)
	}
}

// A rename through the board records the verb's pack — the rename, then the
// relabel the derived-label rule asks for — and the old value forwards.
func TestBoardPriorityEditRenamesAndRederivesTheLabel(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	renamed := core.Priority("urgent")
	mutation, err := board.edit(ctx, "high", boardPriorityEdit{Name: &renamed, ExpectedHead: head})
	if err != nil {
		t.Fatalf("rename a priority through the board: %v", err)
	}
	if got, want := boardPriorityNames(mutation.State.Priorities), []string{"urgent", "medium", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}

	listed := cliPriorityList(t, repository)
	if listed.Priorities[0].Priority != "urgent" || listed.Priorities[0].Label != "Urgent" {
		t.Fatalf("renamed priority = %#v, want urgent labelled the re-derived Urgent", listed.Priorities[0])
	}
	if len(listed.Retired) != 1 || listed.Retired[0].Priority != "high" ||
		listed.Retired[0].Becomes != "urgent" || listed.Retired[0].Operation != string(core.ConfigPriorityRename) {
		t.Fatalf("retired values = %#v, want high forwarding to urgent by a rename", listed.Retired)
	}

	// The pack is the verb's pack. Reconciliation classifies that shape by
	// name, so a board that invented its own would be a board whose changes a
	// teammate's clone could not fold.
	operations := boardPriorityPack(t, repository, mutation.State.Head)
	if len(operations) != 2 ||
		operations[0].Type != core.ConfigPriorityRename ||
		operations[0].PriorityFrom != "high" || operations[0].PriorityTo != "urgent" ||
		operations[1].Type != core.ConfigPriorityRelabel || operations[1].Priority != "urgent" {
		t.Fatalf("recorded operations = %#v, want the rename-then-relabel pack the verb records", operations)
	}
}

// An edit that moves only the label is a relabel, and an edit that moves
// nothing is refused before anything is authored.
func TestBoardPriorityEditRelabelsAndRefusesAnEmptyChange(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	label := "Drop everything"
	mutation, err := board.edit(ctx, "high", boardPriorityEdit{Label: &label, ExpectedHead: head})
	if err != nil {
		t.Fatalf("relabel a priority through the board: %v", err)
	}
	operations := boardPriorityPack(t, repository, mutation.State.Head)
	if len(operations) != 1 || operations[0].Type != core.ConfigPriorityRelabel ||
		operations[0].Priority != "high" || operations[0].Label != label {
		t.Fatalf("recorded operations = %#v, want one relabel of high", operations)
	}
	listed := cliPriorityList(t, repository)
	if listed.Priorities[0].Priority != "high" || listed.Priorities[0].Label != label {
		t.Fatalf("relabelled priority = %#v, want high labelled %q", listed.Priorities[0], label)
	}

	after := mutation.State.Head
	_, err = board.edit(ctx, "high", boardPriorityEdit{ExpectedHead: after})
	if err == nil {
		t.Fatal("an edit naming no member was accepted; it changes nothing and must be refused")
	}
	if got := err.Error(); got != `priority "high" was given nothing to change` {
		t.Fatalf("refusal = %q, want the empty-edit refusal", got)
	}
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("refusal category = %q, want %q", core.CategoryOf(err), core.CategoryValidation)
	}
	if head := boardPriorityHead(t, ctx, board); head != after {
		t.Fatalf("configuration head moved to %q on a refused edit", head)
	}
}

// Removing a priority says what it costs and forwards what it moves, priced by
// the planner against the very vocabulary the change is authored against.
func TestBoardPriorityRemovalPricesAndForwardsWhatItMoves(t *testing.T) {
	repository := initializedRepository(t)
	first := createOrderingTask(t, repository, "Filed under low", "low")
	createOrderingTask(t, repository, "Also filed under low", "low")
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	mutation, err := board.remove(ctx, "low", boardPriorityRemoval{Into: "medium", ExpectedHead: head})
	if err != nil {
		t.Fatalf("remove a priority through the board: %v", err)
	}
	if mutation.Tasks.Affected != 2 {
		t.Fatalf("tasks.affected = %d, want the two tasks filed under low", mutation.Tasks.Affected)
	}
	if got, want := boardPriorityNames(mutation.State.Priorities), []string{"high", "medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	// The task still stores `low` and reads as `medium`, which is the
	// forwarding the planner authored rather than a rewrite the board invented.
	task := cliShowTask(t, repository, first.ID)
	if task.Priority != core.Priority("medium") || task.StoredPriority != core.Priority("low") {
		t.Fatalf("task priority = %q stored %q, want medium stored low", task.Priority, task.StoredPriority)
	}
}

// Where the tasks go is never guessed. The board names this project's
// priorities so the retry is one edit away, exactly as the verb's refusal does.
func TestBoardPriorityRemovalRequiresADestination(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	_, err := board.remove(ctx, "low", boardPriorityRemoval{ExpectedHead: head})
	if err == nil {
		t.Fatal("a removal with nowhere to forward to was accepted")
	}
	want := "removing a priority requires naming where its tasks belong; " +
		"this project's priorities are: high, medium, low"
	if got := err.Error(); got != want {
		t.Fatalf("refusal = %q, want %q", got, want)
	}
	if core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("refusal category = %q, want %q", core.CategoryOf(err), core.CategoryInvocation)
	}
	if after := boardPriorityHead(t, ctx, board); after != head {
		t.Fatalf("configuration head moved to %q on a refused removal", after)
	}
}

// A move is the verb's move: one rerank, from the planner that knows how two
// clones inserting between the same pair still order the same way.
func TestBoardPriorityMoveReordersThroughThePlanner(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	mutation, err := board.move(ctx, "low", boardPriorityMove{Before: "high", ExpectedHead: head})
	if err != nil {
		t.Fatalf("move a priority through the board: %v", err)
	}
	if got, want := boardPriorityNames(mutation.State.Priorities), []string{"low", "high", "medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want %v", got, want)
	}
	operations := boardPriorityPack(t, repository, mutation.State.Head)
	if len(operations) != 1 || operations[0].Type != core.ConfigPriorityReorder || operations[0].Priority != "low" {
		t.Fatalf("recorded operations = %#v, want one rerank of low", operations)
	}

	after := mutation.State.Head
	for _, test := range []struct {
		name string
		move boardPriorityMove
		want string
	}{
		{
			name: "both",
			move: boardPriorityMove{Before: "high", After: "medium", ExpectedHead: after},
			want: "a priority moves before or after another priority, not both",
		},
		{
			name: "neither",
			move: boardPriorityMove{ExpectedHead: after},
			want: "moving a priority requires naming the priority it goes before or after",
		},
	} {
		_, err := board.move(ctx, "low", test.move)
		if err == nil {
			t.Fatalf("a move naming %s anchor was accepted", test.name)
		}
		if got := err.Error(); got != test.want {
			t.Fatalf("refusal for %s = %q, want %q", test.name, got, test.want)
		}
	}
	if head := boardPriorityHead(t, ctx, board); head != after {
		t.Fatalf("configuration head moved to %q on a refused move", head)
	}
}

// A priority has exactly one role, and the fold transfers it: naming a new
// default records one operation rather than a reconciliation of a tag set.
func TestBoardPriorityDefaultTransfersInOneOperation(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	mutation, err := board.setDefault(ctx, "high", boardPriorityDefault{ExpectedHead: head})
	if err != nil {
		t.Fatalf("tag a default priority through the board: %v", err)
	}
	if got := mutation.State.Priorities.Default(); got != "high" {
		t.Fatalf("default priority = %q, want high", got)
	}
	operations := boardPriorityPack(t, repository, mutation.State.Head)
	if len(operations) != 1 || operations[0].Type != core.ConfigPriorityTag ||
		operations[0].Priority != "high" || operations[0].PriorityTag != core.PriorityTagDefault {
		t.Fatalf("recorded operations = %#v, want the single tag operation the fold transfers", operations)
	}
	for _, definition := range mutation.State.Priorities.EffectiveDocument().Priorities {
		if definition.Priority == "medium" && definition.HasTag(core.PriorityTagDefault) {
			t.Fatal("medium still carries the default tag; exactly one priority may hold it")
		}
	}

	// Naming the priority that already holds it is the planner's refusal, in
	// the planner's words.
	after := mutation.State.Head
	_, err = board.setDefault(ctx, "high", boardPriorityDefault{ExpectedHead: after})
	if err == nil {
		t.Fatal("tagging the priority that already holds the default was accepted")
	}
	if got := err.Error(); got != `priority "high" already carries the "default" tag` {
		t.Fatalf("refusal = %q, want the planner's own refusal", got)
	}
}

// A recolor records the one operation a recolor needs, and the color it records
// is the canonical reading of what the board sent.
func TestBoardPriorityRecolorRecordsTheColor(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	mutation, err := board.recolor(ctx, "high", boardPriorityRecolor{Color: "  #FF0000  ", ExpectedHead: head})
	if err != nil {
		t.Fatalf("recolor a priority through the board: %v", err)
	}
	canonical, err := core.ValidateThemeColor("#FF0000")
	if err != nil {
		t.Fatal(err)
	}
	operations := boardPriorityPack(t, repository, mutation.State.Head)
	if len(operations) != 1 || operations[0].Type != core.ConfigPriorityRecolor ||
		operations[0].Priority != "high" || operations[0].Value != canonical {
		t.Fatalf("recorded operations = %#v, want one recolor of high to %q", operations, canonical)
	}
	if listed := cliPriorityList(t, repository); listed.Priorities[0].Color != canonical {
		t.Fatalf("stored color = %q, want %q", listed.Priorities[0].Color, canonical)
	}

	// A color nothing renders is refused before anything is written, by the
	// same validation the verb runs.
	_, err = board.recolor(ctx, "high", boardPriorityRecolor{Color: "not-a-color", ExpectedHead: mutation.State.Head})
	if err == nil {
		t.Fatal("an unreadable color was accepted")
	}
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("refusal category = %q, want %q", core.CategoryOf(err), core.CategoryValidation)
	}
}

// The recolor that records nothing is refused, and the board says so in the
// CLI's own words rather than reporting a no-op as a success.
//
// The cost this refusal exists to avoid is not a wasted commit. A priority
// write on a project that has configured none backfills the built-in three and
// stamps the priority section's generation marker, which parks every teammate
// below that generation on an older build for the life of the project. A board
// that swallowed the refusal and answered "saved" would spend that on a change
// nobody asked for.
func TestBoardPriorityRecolorRefusesAChangeThatRecordsNothing(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	// Clearing a color a priority does not have. This project has never
	// configured its priorities, so the head must not move: the refusal is what
	// keeps the marker unstamped.
	_, err := board.recolor(ctx, "low", boardPriorityRecolor{ExpectedHead: head})
	if err == nil {
		t.Fatal("clearing a color that is not stored was accepted")
	}
	if got := err.Error(); got != `priority "low" has no color to clear` {
		t.Fatalf("refusal = %q, want the planner's own refusal", got)
	}
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("refusal category = %q, want %q", core.CategoryOf(err), core.CategoryValidation)
	}
	if after := boardPriorityHead(t, ctx, board); after != head {
		t.Fatalf("configuration head moved to %q on a refused recolor", after)
	}

	// And setting the color a priority already has.
	mutation, err := board.recolor(ctx, "high", boardPriorityRecolor{Color: "#ff0000", ExpectedHead: head})
	if err != nil {
		t.Fatalf("recolor a priority through the board: %v", err)
	}
	stored := mutation.State.Head
	_, err = board.recolor(ctx, "high", boardPriorityRecolor{Color: "#ff0000", ExpectedHead: stored})
	if err == nil {
		t.Fatal("recording the color a priority already has was accepted")
	}
	if got := err.Error(); got != `priority "high" already has that color` {
		t.Fatalf("refusal = %q, want the planner's own refusal", got)
	}
	if after := boardPriorityHead(t, ctx, board); after != stored {
		t.Fatalf("configuration head moved to %q on a refused recolor", after)
	}
}

// The head a change names is the head it lands on, whether the ledger moved
// before the change was composed or while it was being composed.
//
// The window this closes is the one a board actually has: the panel's change is
// read, authored and written over several Git processes, and a teammate's
// `workbook priority` fits between any two of them.
func TestBoardPriorityRefusesAChangeTheLedgerMovedUnderneath(t *testing.T) {
	repository := initializedRepository(t)
	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	head := boardPriorityHead(t, ctx, board)

	// The window: somebody else records a priority change while this one is
	// being composed. It goes through the CLI, in another process's shape, so
	// nothing about this depends on the two sharing a handle.
	interloped := false
	_, err := board.apply(ctx, head, func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
		if !interloped {
			interloped = true
			if code, _, stderr := run(t, repository, "priority", "add", "icebox", "--no-sync"); code != 0 {
				t.Fatalf("interloping priority add = code %d; stderr = %q", code, stderr)
			}
		}
		return planPriorityRelabel(ctx, scope, vocabulary, "high", "Highest")
	})
	if err == nil {
		t.Fatal("a change authored across a teammate's change was accepted")
	}
	if core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("refusal category = %q, want %q", core.CategoryOf(err), core.CategoryStaleWrite)
	}
	if got := err.Error(); got != "this project's priorities have changed since "+head+"; reload and try again" {
		t.Fatalf("refusal = %q, want the reload the client can act on", got)
	}
	if !interloped {
		t.Fatal("the plan builder never ran, so this test refused nothing")
	}

	// And the cheap check: a change composed against a head the ledger is
	// already past never reaches the planners at all.
	built := false
	_, err = board.apply(ctx, head, func(priorityScope, core.PriorityVocabulary) (priorityPlan, error) {
		built = true
		return priorityPlan{}, nil
	})
	if err == nil || core.CategoryOf(err) != core.CategoryStaleWrite {
		t.Fatalf("a change composed against a stale head = %v, want a stale write", err)
	}
	if built {
		t.Fatal("a change nobody could land authored a plan first")
	}

	// The relabel never happened, and the teammate's addition is what stands.
	if got, want := cliPriorityNames(t, repository), []string{"high", "medium", "low", "icebox"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priorities = %v, want the teammate's change and nothing else", got)
	}
	if listed := cliPriorityList(t, repository); listed.Priorities[0].Label != "High" {
		t.Fatalf("high's label = %q, want the refused relabel not to have landed", listed.Priorities[0].Label)
	}
}

// The board writes the ledger and never the working tree, and says what the
// generated file now describes instead.
func TestBoardPriorityChangeLeavesTheGuidelinesAloneAndSaysSo(t *testing.T) {
	repository := initializedRepository(t)
	guidelines := filepath.Join(repository, agentdocs.GuidelinesPath)
	generated, err := os.ReadFile(guidelines)
	if err != nil {
		t.Fatalf("read the generated guidelines: %v", err)
	}
	if !strings.Contains(string(generated), "high") {
		t.Fatalf("the fixture's guidelines do not name the priority this test renames:\n%s", generated)
	}

	ctx := context.Background()
	board := openBoardPriorities(t, ctx, repository)
	renamed := core.Priority("urgent")
	mutation, err := board.edit(ctx, "high", boardPriorityEdit{
		Name:         &renamed,
		ExpectedHead: boardPriorityHead(t, ctx, board),
	})
	if err != nil {
		t.Fatalf("rename a priority through the board: %v", err)
	}

	after, err := os.ReadFile(guidelines)
	if err != nil {
		t.Fatalf("read the generated guidelines: %v", err)
	}
	if string(after) != string(generated) {
		t.Fatal("the board rewrote the generated guidelines from a request")
	}
	warned := false
	for _, warning := range mutation.Warnings {
		if warning.Code == core.WarningDocsRefresh && strings.Contains(warning.Message, agentdocs.GuidelinesPath) {
			warned = true
			if !strings.Contains(warning.Message, "workbook docs update") {
				t.Fatalf("docs warning = %q, want the command that refreshes the file", warning.Message)
			}
			if !strings.Contains(warning.Message, "priorities") {
				t.Fatalf("docs warning = %q, want it to name the section that moved", warning.Message)
			}
		}
	}
	if !warned {
		t.Fatalf("warnings = %#v, want one naming the guidelines the board did not rewrite", mutation.Warnings)
	}
}

// openBoardPriorities builds the board's priority administration over a real
// repository, the way runServe does.
func openBoardPriorities(t *testing.T, ctx context.Context, repository string) *boardPriorities {
	t.Helper()
	service, store, err := openBoardServiceParts(ctx, repository)
	if err != nil {
		t.Fatalf("open the board's service: %v", err)
	}
	return &boardPriorities{
		repository: store,
		config:     service.Config,
		publisher:  &boardPublisher{repository: store, config: service.Config},
		service: func(priorities core.PriorityVocabulary) core.Service {
			reader := service
			reader.Priorities = priorities
			return reader
		},
	}
}

func boardPriorityHead(t *testing.T, ctx context.Context, board *boardPriorities) string {
	t.Helper()
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		t.Fatalf("read the configuration ledger: %v", err)
	}
	return state.Head
}

// boardPriorityNames is a mutation's priorities in board order, most urgent
// first, which is what most assertions here are really about.
func boardPriorityNames(priorities core.PriorityVocabulary) []string {
	names := make([]string, 0, 4)
	for _, definition := range priorities.EffectiveDocument().Priorities {
		names = append(names, string(definition.Priority))
	}
	return names
}

// boardPriorityPack is the operations one board change recorded, read out of
// the ledger commit the mutation reported.
func boardPriorityPack(t *testing.T, repository, head string) []core.ConfigOperation {
	t.Helper()
	operations := configPackOperations(t, repository, head)
	// A ledger whose genesis predates the priority section is backfilled with
	// the built-in three ahead of its first priority change, in the same
	// commit. Those operations are the ledger catching up rather than part of
	// the change, so what this asserts about is whatever follows them.
	builtIn := core.BuiltInPriorityVocabulary().Definitions()
	if len(operations) <= len(builtIn) {
		return operations
	}
	for index, definition := range builtIn {
		if operations[index].Type != core.ConfigPriorityAdd ||
			operations[index].PriorityName != definition.Priority {
			return operations
		}
	}
	return operations[len(builtIn):]
}
