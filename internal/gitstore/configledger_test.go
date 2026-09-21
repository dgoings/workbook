package gitstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// configOperations builds a batch of authored operations with fixed IDs, so a
// test can say what it means without minting ULIDs by hand.
func configOperations(operations ...core.ConfigOperation) []core.ConfigOperation {
	return operations
}

func renameOperation(from, to core.Status) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigStatusRename, From: from, To: to}
}

func addOperation(name core.Status, label, rank string, tags ...core.StatusTag) core.ConfigOperation {
	if tags == nil {
		tags = []core.StatusTag{}
	}
	return core.ConfigOperation{Type: core.ConfigStatusAdd, Name: name, Label: label, Rank: rank, Tags: tags}
}

func relabelOperation(status core.Status, label string) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigStatusRelabel, Status: status, Label: label}
}

func writeConfig(t *testing.T, repo *Repository, config core.ProjectConfig, operations ...core.ConfigOperation) ConfigWriteResult {
	t.Helper()
	result, err := repo.WriteConfigOperation(context.Background(), config, core.CryptoULIDSource{}, operations, "")
	if err != nil {
		t.Fatalf("WriteConfigOperation() error = %v", err)
	}
	return result
}

// MintConfigLedger records the statuses this build ships, which is the one place
// core.DefaultVocabulary is ever written down.
//
// It is the counterpart to the lazy seed above and differs from it in exactly
// the way that matters: the lazy seed runs for a project that already existed
// and records what that project was using, while this runs for a project being
// brought into existence and records what this release gives a new one. Reading
// the wrong accessor in either place would silently re-columnize a board.
func TestMintConfigLedgerRecordsTheDefaultVocabulary(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()

	seeded, err := repo.MintConfigLedger(ctx, config, core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}
	if !seeded {
		t.Fatal("MintConfigLedger() = false, want a genesis written for a project with no ledger")
	}

	records := configChain(t, repo, config)
	if len(records) != 1 {
		t.Fatalf("ledger holds %d commit(s), want the genesis alone", len(records))
	}
	root := records[0]
	if len(root.Operation.Operations) != 1 || root.Operation.Operations[0].Type != core.ConfigGenesis {
		t.Fatalf("root pack = %#v, want one config.genesis", root.Operation.Operations)
	}
	if got := root.Operation.Operations[0].Config.Vocabulary; !reflect.DeepEqual(got, core.DefaultVocabulary().Document()) {
		t.Fatalf("genesis vocabulary = %#v, want the vocabulary this build mints with", got)
	}
	if got := parentCount(t, repo, root.ObjectID); got != 0 {
		t.Fatalf("genesis parent count = %d, want 0", got)
	}

	vocabulary, err := repo.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if vocabulary.Has(core.StatusBlocked) {
		t.Fatal("a minted project defines `blocked`, which left the default set")
	}

	// A second call is a no-op rather than a second root, which is what a rerun
	// of `workbook setup` and a clone that fetched a ledger both look like.
	again, err := repo.MintConfigLedger(ctx, config, core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("MintConfigLedger() second call error = %v", err)
	}
	if again {
		t.Fatal("MintConfigLedger() = true on a project that already has a ledger")
	}
	if got := len(configChain(t, repo, config)); got != 1 {
		t.Fatalf("ledger holds %d commit(s) after a second mint, want 1", got)
	}
}

// A project that already has a ledger keeps recording what it holds: the lazy
// seed never runs again, and an authored change appends. The pair of tests is
// what keeps the two vocabularies from being read in each other's place.
func TestWriteConfigOperationAppendsToAMintedLedger(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	if _, err := repo.MintConfigLedger(ctx, config, core.CryptoULIDSource{}); err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}

	result := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)
	if result.Seeded {
		t.Fatal("WriteConfigOperation() reported seeding a ledger that already existed")
	}
	records := configChain(t, repo, config)
	if len(records) != 2 {
		t.Fatalf("ledger holds %d commit(s), want the mint's genesis and the author's pack", len(records))
	}
	if got := records[0].Operation.Operations[0].Config.Vocabulary; !reflect.DeepEqual(got, core.DefaultVocabulary().Document()) {
		t.Fatalf("genesis vocabulary = %#v, want the minted one preserved", got)
	}
	if result.Vocabulary().Has(core.StatusBlocked) {
		t.Fatal("appending to a minted ledger reintroduced `blocked`")
	}
}

// TestWriteConfigOperationSeedsGenesisLazily pins the shape the whole ledger
// rests on: a project that never had a configuration grows one from the
// vocabulary it was already using, and the author's own change is the commit
// after it rather than folded into the root.
func TestWriteConfigOperationSeedsGenesisLazily(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()

	vocabulary, err := repo.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if !reflect.DeepEqual(vocabulary.Document(), core.LegacyVocabulary().Document()) {
		t.Fatalf("LoadVocabulary() without a ledger = %#v, want the legacy vocabulary", vocabulary.Document())
	}
	if refExists(t, repo, configRef) {
		t.Fatalf("%s exists before anything configured a status", configRef)
	}

	result := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)
	if !result.Seeded {
		t.Fatal("WriteConfigOperation() did not report seeding the ledger")
	}
	if got := refValue(t, repo, configRef); got != result.Head {
		t.Fatalf("%s = %q, want the written head %q", configRef, got, result.Head)
	}

	records := configChain(t, repo, config)
	if len(records) != 2 {
		t.Fatalf("ledger holds %d commit(s), want a genesis root and the author's pack", len(records))
	}
	root := records[0]
	if len(root.Operation.Operations) != 1 || root.Operation.Operations[0].Type != core.ConfigGenesis {
		t.Fatalf("root pack = %#v, want one config.genesis", root.Operation.Operations)
	}
	if got := root.Operation.Operations[0].Config.Vocabulary; !reflect.DeepEqual(got, core.LegacyVocabulary().Document()) {
		t.Fatalf("genesis vocabulary = %#v, want the legacy vocabulary this project was already using", got)
	}
	if got := parentCount(t, repo, root.ObjectID); got != 0 {
		t.Fatalf("genesis parent count = %d, want 0", got)
	}
	if got := parentCount(t, repo, records[1].ObjectID); got != 1 {
		t.Fatalf("second commit parent count = %d, want 1", got)
	}

	// The memoized vocabulary is replaced in place, so the rest of this command
	// reads what it just wrote rather than the value it opened on.
	fresh, err := repo.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if resolved, live := fresh.Resolve("ready"); !live || resolved != "todo" {
		t.Fatalf("Resolve(ready) = (%q, %t), want (todo, true)", resolved, live)
	}
}

func TestWriteConfigOperationAppendsOntoTheExistingLedger(t *testing.T) {
	repo, config := writeRepository(t)
	first := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)
	second := writeConfig(t, repo, config, configOperations(relabelOperation("todo", "To Do"))...)

	if second.Seeded {
		t.Fatal("the second write reported seeding a ledger that already existed")
	}
	if got := second.State.LogicalClock; got != 3 {
		t.Fatalf("second write logical clock = %d, want 3 (genesis, first, second)", got)
	}
	records := configChain(t, repo, config)
	if len(records) != 3 {
		t.Fatalf("ledger holds %d commit(s), want 3", len(records))
	}
	if records[1].ObjectID != first.Head {
		t.Fatalf("second commit = %q, want the first write %q", records[1].ObjectID, first.Head)
	}
	if got := second.Vocabulary().Label("todo"); got != "To Do" {
		t.Fatalf("todo label = %q, want %q", got, "To Do")
	}
}

// TestWriteConfigOperationRefusesArityTheAuthorCanStillFix is the authoring
// gate's half of the asymmetry: what a peer's pack folds silently, an author is
// refused, with a message naming the command that fixes it.
func TestWriteConfigOperationRefusesArityTheAuthorCanStillFix(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()

	_, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{},
		configOperations(core.ConfigOperation{Type: core.ConfigStatusUntag, Status: "done", Tag: core.StatusTagDone}), "")
	if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
		t.Fatalf("WriteConfigOperation() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "workbook status tag") {
		t.Fatalf("error = %q, want it to name the command that fixes the state", err)
	}
	if refExists(t, repo, configRef) {
		t.Fatalf("%s was created by a write the authoring gate refused", configRef)
	}
}

// A write that loses the ledger's compare-and-swap is refused as a stale write
// and records nothing.
//
// TestConcurrentConfigWritesConvergeOnOneLedger observes the same rule through a
// real race and therefore has to accept either outcome; this one makes the loss
// happen by moving the ref inside the losing write's own transaction, so the
// refusal is a fact rather than a probability. The category is what the CLI
// turns into "run it again", which is only sound advice because the losing
// write left the ledger exactly where it found it.
func TestWriteConfigOperationRefusesALostCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)
	seeded := writeConfig(t, repo, config, configOperations(addOperation("triage", "Triage", "1/2"))...)

	other := openSyncCloneAt(t, repo.Root)
	var once sync.Once
	var raced ConfigWriteResult
	repo.commandObserver = func(args []string) {
		if len(args) == 0 || args[0] != "update-ref" {
			return
		}
		once.Do(func() {
			raced = writeConfig(t, other, config, configOperations(addOperation("review", "Review", "5/2"))...)
		})
	}
	defer func() { repo.commandObserver = nil }()

	_, err := repo.WriteConfigOperation(ctx, config, core.CryptoULIDSource{},
		configOperations(addOperation("shipped", "Shipped", "7/2")), "")
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("WriteConfigOperation() category = %q, want %q; error = %v", got, want, err)
	}
	if got := gitOutput(t, repo, "rev-parse", configRef); got != raced.Head {
		t.Fatalf("ledger = %q, want the winning write's head %q", got, raced.Head)
	}
	fresh := openSyncCloneAt(t, repo.Root)
	vocabulary, err := fresh.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if vocabulary.Has("shipped") {
		t.Fatal("a refused configuration write left its status behind")
	}
	if !vocabulary.Has("triage") || !vocabulary.Has("review") {
		t.Fatalf("statuses = %#v, want both accepted writes; seeded head %q",
			vocabulary.Definitions(), seeded.Head)
	}
}

// A bounded read delivers the newest commits and nothing else, and still
// reports how long the whole ledger is.
//
// The bound is the point: the per-commit cost is two documents decoded and
// re-encoded to compare canonical bytes, so a ten-line log that read everything
// would cost a year of somebody's configuration history. The total stays exact
// because it comes from the commit walk, which is one rev-list whatever the
// window is.
func TestReadConfigHistoryTailDeliversOnlyTheNewestCommits(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)
	const changes = 12
	for index := range changes {
		writeConfig(t, repo, config, relabelOperation("backlog", fmt.Sprintf("Backlog %d", index)))
	}

	read := func(window int) ([]string, ConfigHistoryStart) {
		var delivered []string
		var start ConfigHistoryStart
		found, err := repo.ReadConfigHistoryTail(ctx, config, window, ConfigHistoryStream{
			Begin: func(begin ConfigHistoryStart) error {
				start = begin
				return nil
			},
			Commit: func(commit ConfigHistoryCommit) error {
				delivered = append(delivered, commit.Operation.Operations[0].Label)
				return nil
			},
			End: func(ConfigHistoryResult) error { return nil },
		})
		if err != nil || !found {
			t.Fatalf("ReadConfigHistoryTail(%d) = found %t, error %v", window, found, err)
		}
		return delivered, start
	}

	// The genesis plus one commit per change.
	whole, start := read(0)
	if len(whole) != changes+1 || start.Commits != changes+1 || start.Skipped != 0 {
		t.Fatalf("unbounded read delivered %d of %d, skipping %d; want the whole ledger",
			len(whole), start.Commits, start.Skipped)
	}

	tail, start := read(3)
	if len(tail) != 3 {
		t.Fatalf("bounded read delivered %d commits, want 3", len(tail))
	}
	if start.Commits != changes+1 {
		t.Fatalf("bounded read reported %d commits, want the ledger's whole %d", start.Commits, changes+1)
	}
	if start.Skipped != changes+1-3 {
		t.Fatalf("bounded read skipped %d, want %d", start.Skipped, changes+1-3)
	}
	if got, want := tail, whole[len(whole)-3:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("bounded read delivered %#v, want the newest %#v", got, want)
	}

	// A window longer than the ledger is not an error and delivers everything.
	everything, start := read(changes * 10)
	if len(everything) != changes+1 || start.Skipped != 0 {
		t.Fatalf("over-long window delivered %d commits skipping %d, want the whole ledger",
			len(everything), start.Skipped)
	}
}

func TestWriteConfigOperationRefusesAnAuthoredGenesis(t *testing.T) {
	repo, config := writeRepository(t)
	_, err := repo.WriteConfigOperation(context.Background(), config, core.CryptoULIDSource{},
		configOperations(core.ConfigOperation{
			Type:   core.ConfigGenesis,
			Config: &core.ConfigData{Vocabulary: core.DefaultVocabulary().Document()},
		}), "")
	if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
		t.Fatalf("WriteConfigOperation(genesis) category = %q, want %q; error = %v", got, want, err)
	}
}

// TestConfigPackBudgetRefusalNamesItsBoundAndIsOperational is the transport
// free half of the resource-bound contract: a pack this clone declines to fold
// is an operational refusal that names the bound, never a verdict on the data.
func TestConfigPackBudgetRefusalNamesItsBoundAndIsOperational(t *testing.T) {
	operations := make([]core.ConfigOperation, core.MaxConfigOperationsPerPack+1)
	for index := range operations {
		operations[index] = core.ConfigOperation{
			ID:     fmt.Sprintf("01K0M6B8A4FTT8C39MXXYTW%03d", index),
			Type:   core.ConfigStatusRelabel,
			Status: "backlog",
			Label:  fmt.Sprintf("Backlog %d", index),
		}
	}
	pack := core.ConfigOperationPack{Operations: operations}
	err := validateConfigPackBudget(pack)
	if err == nil {
		t.Fatal("validateConfigPackBudget() error = nil, want a refusal")
	}
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("category = %q, want %q — a refusal is never a claim that the checkpoint is invalid; error = %v", got, want, err)
	}
	if got := core.CategoryOf(err); got == core.CategoryCorruptData {
		t.Fatalf("category = %q, which would strand a project append-only storage cannot repair", got)
	}
	if !strings.Contains(err.Error(), "MaxConfigOperationsPerPack") {
		t.Fatalf("error = %q, want it to name the bound so it can be raised", err)
	}
	if err := validateConfigPackBudget(core.ConfigOperationPack{Operations: operations[:core.MaxConfigOperationsPerPack]}); err != nil {
		t.Fatalf("a pack exactly at the bound was refused: %v", err)
	}
}

// TestOverBudgetLedgerIsRefusedWithoutTouchingTheCheckpoint drives the same
// refusal through a real ledger: the ref stays exactly where it was, so raising
// the bound is the only thing standing between this clone and the history.
func TestOverBudgetLedgerIsRefusedWithoutTouchingTheCheckpoint(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	seeded := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)

	operations := make([]core.ConfigOperation, core.MaxConfigOperationsPerPack+1)
	for index := range operations {
		operations[index] = core.ConfigOperation{
			ID:     mustConfigOperationID(t, index),
			Type:   core.ConfigStatusRelabel,
			Status: "todo",
			Label:  fmt.Sprintf("To Do %d", index),
		}
	}
	pack, err := core.NewConfigOperationPack(
		config.ProjectID,
		seeded.State.History.Generation,
		"peer@example.test",
		seeded.State.LogicalClock+1,
		time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC),
		operations,
	)
	if err != nil {
		t.Fatalf("NewConfigOperationPack() error = %v", err)
	}
	state, err := core.ApplyConfig(&seeded.State, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v; the pack itself is perfectly foldable, which is the point", err)
	}
	hostile, err := repo.writeConfigObjects(ctx, seeded.Head, pack, state, "workbook: a very large configuration change")
	if err != nil {
		t.Fatalf("writeConfigObjects() error = %v", err)
	}
	syncGit(t, repo.Root, "update-ref", configRef, hostile, seeded.Head)

	fresh, err := Open(ctx, repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := fresh.LoadVocabulary(ctx)
	if got, want := core.CategoryOf(readErr), core.CategoryOperational; got != want {
		t.Fatalf("LoadVocabulary() category = %q, want %q; error = %v", got, want, readErr)
	}
	if !strings.Contains(readErr.Error(), "MaxConfigOperationsPerPack") {
		t.Fatalf("error = %q, want it to name the bound", readErr)
	}
	if got := refValue(t, repo, configRef); got != hostile {
		t.Fatalf("%s = %q, want the refusal to have left it at %q", configRef, got, hostile)
	}
}

// TestParkedConfigRefsAreInvisibleToTheTaskParkedSweep is why the parking
// namespace is not under refs/workbook/reconciled/: that lister name-splits on
// task IDs and would skip every configuration entry forever.
func TestParkedConfigRefsAreInvisibleToTheTaskParkedSweep(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	seeded := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)

	for index := 0; index < maxParkedConfigRefs+2; index++ {
		syncGit(t, repo.Root, "update-ref", fmt.Sprintf("%s%d", parkedConfigRefPrefix, index), seeded.Head)
	}
	// The task sweep must neither see them nor fail on them.
	pruned, err := repo.PruneParkedRefs(ctx, config)
	if err != nil {
		t.Fatalf("PruneParkedRefs() error = %v", err)
	}
	if pruned != 0 {
		t.Fatalf("PruneParkedRefs() deleted %d ref(s), want none of the configuration parks", pruned)
	}
	parked, err := repo.parkedTaskHeads(ctx, config)
	if err != nil {
		t.Fatalf("parkedTaskHeads() error = %v", err)
	}
	if len(parked) != 0 {
		t.Fatalf("parkedTaskHeads() = %#v, want the configuration parks invisible to it", parked)
	}

	count, err := repo.PruneParkedConfigRefs(ctx)
	if err != nil {
		t.Fatalf("PruneParkedConfigRefs() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("PruneParkedConfigRefs() deleted %d ref(s), want the 2 past the retention bound", count)
	}
	for index := 0; index < 2; index++ {
		if refExists(t, repo, fmt.Sprintf("%s%d", parkedConfigRefPrefix, index)) {
			t.Fatalf("parked configuration ref %d survived its own sweep", index)
		}
	}
	for index := 2; index < maxParkedConfigRefs+2; index++ {
		if !refExists(t, repo, fmt.Sprintf("%s%d", parkedConfigRefPrefix, index)) {
			t.Fatalf("parked configuration ref %d was pruned inside the retention bound", index)
		}
	}
}

// TestConfigRefRejectsChildrenLocallyAndToleratesThemOnOrigin pins the two
// verdicts the singleton earns in its two namespaces.
func TestConfigRefRejectsChildrenLocallyAndToleratesThemOnOrigin(t *testing.T) {
	repo, config := writeRepository(t)
	seeded := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)

	listing, err := parseConfigRefRecords(
		[]string{configRef, remoteConfigRef},
		[]byte(remoteConfigRef+"/notes\x00"+seeded.Head+"\x00\n"),
	)
	if err != nil {
		t.Fatalf("a stray ref under origin's mirror must be skipped, not fatal: %v", err)
	}
	if len(listing.Ignored) != 1 || listing.Ignored[0] != configRef+"/notes" {
		t.Fatalf("ignored = %#v, want it restated under the name origin holds it at", listing.Ignored)
	}

	_, err = parseConfigRefRecords(
		[]string{configRef, remoteConfigRef},
		[]byte(configRef+"/notes\x00"+seeded.Head+"\x00\n"),
	)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("local child category = %q, want %q; error = %v", got, want, err)
	}
}

// TestUnreadableLedgerNamesTheRefAndTheCommandThatDiagnosesIt.
//
// Refusing to run is the right answer — a clone that quietly fell back to the
// built-in statuses would draw every board in columns the project does not have
// — but this failure reaches a person through list, show, next and create
// alike, and what those said before was whatever the decoder said, naming
// nothing. Every one of them has to say which ref stopped it and what reads it
// in detail.
func TestUnreadableLedgerNamesTheRefAndTheCommandThatDiagnosesIt(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)

	// A task commit has the same tree shape and decodes as nothing this ref can
	// hold, which is the shape of a ledger somebody rewrote by hand.
	task, _, _ := writeRoot(t, repo, config)
	syncGit(t, repo.Root, "update-ref", configRef, task.Head)

	fresh, err := Open(ctx, repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := fresh.LoadVocabulary(ctx)
	if readErr == nil {
		t.Fatal("LoadVocabulary() error = nil, want an unreadable ledger to stop the command")
	}
	if !strings.Contains(readErr.Error(), configRef) {
		t.Fatalf("error = %q, want it to name %s", readErr, configRef)
	}
	if !strings.Contains(readErr.Error(), "workbook validate") {
		t.Fatalf("error = %q, want it to name the command that diagnoses it", readErr)
	}
	// The decoder's own account survives; the wrapper adds to it rather than
	// replacing it.
	if !strings.Contains(readErr.Error(), "decode") {
		t.Fatalf("error = %q, want the underlying failure preserved", readErr)
	}
}

// TestPushRefusalReasonKeepsTheReasonAndDropsTheAdvice pins the one line a
// carve-out warning is allowed to be. Git's advice after a refused push is
// about pulling and merging a branch, which is not what a Workbook ref is, and
// flattening it buried the reason under a paragraph aimed at the wrong workflow.
func TestPushRefusalReasonKeepsTheReasonAndDropsTheAdvice(t *testing.T) {
	refused := gitCommandResult{stderr: []byte(
		"To /tmp/origin.git\n" +
			" ! [remote rejected] refs/workbook/config -> refs/workbook/config (pre-receive hook declined)\n" +
			"error: failed to push some refs to '/tmp/origin.git'\n" +
			"hint: Updates were rejected because the tip of your current branch is behind\n" +
			"hint: its remote counterpart. If you want to integrate the remote changes,\n" +
			"hint: use 'git pull' before pushing again.\n" +
			"hint: See the 'Note about fast-forwards' in 'git push --help' for details.\n"),
		err: errors.New("exit status 1"),
	}
	got := pushRefusalReason(refused)
	if strings.Contains(got, "hint:") || strings.Contains(got, "git pull") {
		t.Fatalf("reason = %q, want the advice dropped", got)
	}
	if !strings.Contains(got, "pre-receive hook declined") {
		t.Fatalf("reason = %q, want the refusal reason kept", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("reason = %q, want one line", got)
	}

	// A refusal Git explained only through its exit status still says something.
	if got := pushRefusalReason(gitCommandResult{err: errors.New("exit status 128")}); got != "exit status 128" {
		t.Fatalf("reason = %q, want the exit status when there is no stderr", got)
	}
}

// configChain reads the whole ledger oldest first through the streaming reader.
func configChain(t *testing.T, repo *Repository, config core.ProjectConfig) []ConfigHistoryCommit {
	t.Helper()
	var records []ConfigHistoryCommit
	found, err := repo.ReadConfigHistoryStream(context.Background(), config, ConfigHistoryStream{
		Begin:  func(ConfigHistoryStart) error { return nil },
		Commit: func(commit ConfigHistoryCommit) error { records = append(records, commit); return nil },
		End: func(result ConfigHistoryResult) error {
			if result.Failure != nil {
				t.Fatalf("configuration history failure at %s: %v", result.Failure.Commit, result.Failure.Err)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ReadConfigHistoryStream() error = %v", err)
	}
	if !found {
		t.Fatal("ReadConfigHistoryStream() found no ledger")
	}
	return records
}

// mustConfigOperationID builds a canonical ULID that differs per index, so a
// test pack can carry many distinct operations without a random source.
func mustConfigOperationID(t *testing.T, index int) string {
	t.Helper()
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	id := []byte("01K0M6B8A4FTT8C39MXXYTW000")
	id[len(id)-1] = alphabet[index%len(alphabet)]
	id[len(id)-2] = alphabet[(index/len(alphabet))%len(alphabet)]
	return string(id)
}

// LoadVocabularyState memoizes the checkpoint it decoded, keyed on the tip it
// decoded it from, and a tip that moved misses.
//
// The memo exists because `workbook serve` calls this on every request — that
// is how an open board notices a teammate's status change — and an object read
// a second for a configuration that changes once a month was most of the poll
// route's latency. Keying on the head is what makes it safe: a commit's
// contents cannot change, and the ref enumeration that would notice a move
// still happens every call. This test states the part that could regress: the
// answer after a write is the new one, not the remembered one.
func TestLoadVocabularyStateFollowsTheLedgerPastItsMemo(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()

	unseeded, err := repo.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if unseeded.Seeded || unseeded.Head != "" {
		t.Fatalf("LoadVocabularyState() without a ledger = %#v, want an unseeded state with no head", unseeded)
	}

	written := writeConfig(t, repo, config, addOperation("icebox", "Icebox", "7/1"))
	first, err := repo.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if !first.Seeded || first.Head == "" || !first.Vocabulary.Has("icebox") {
		t.Fatalf("LoadVocabularyState() after a write = %#v, want the seeded ledger with icebox", first)
	}
	if got := written.Vocabulary().Document(); !reflect.DeepEqual(first.Vocabulary.Document(), got) {
		t.Fatalf("LoadVocabularyState() vocabulary = %#v, want the written %#v", first.Vocabulary.Document(), got)
	}

	// Reading the same tip again answers identically, which is the memo hit.
	repeated, err := repo.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if !reflect.DeepEqual(repeated, first) {
		t.Fatalf("LoadVocabularyState() repeated = %#v, want %#v", repeated, first)
	}

	// Moving the ledger moves the answer. This is the assertion the memo could
	// break, and it fails loudly if the head ever stops being the key.
	writeConfig(t, repo, config, relabelOperation("icebox", "Deep Freeze"))
	moved, err := repo.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if moved.Head == first.Head {
		t.Fatalf("LoadVocabularyState() head = %q after a second write, want a new tip", moved.Head)
	}
	if got := moved.Vocabulary.Label("icebox"); got != "Deep Freeze" {
		t.Fatalf("LoadVocabularyState() label = %q, want the relabelled Deep Freeze", got)
	}
}

// A project that has recorded nothing about keys still has one. Its founding
// key lives in the identity ref, every task ID it has ever minted carries that
// key, and the ledger deliberately does not keep a second copy — so the state
// this read reports is the founding key alone rather than the zero set, which
// would classify every one of that project's own refs as somebody else's.
func TestLoadVocabularyStateReportsTheFoundingKeyWithoutALedger(t *testing.T) {
	repo, config := writeRepository(t)
	state, err := repo.LoadVocabularyState(context.Background(), config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if got := state.Keys.Current(); got != config.Key {
		t.Fatalf("Keys.Current() = %q, want the founding key %q", got, config.Key)
	}
	if got := state.Keys.Keys(); len(got) != 1 {
		t.Fatalf("Keys() = %#v, want the founding key alone", got)
	}
}

// The hazard this whole backfill exists to close: the moment a pack writes the
// key section, the fold stops substituting the founding key and every later
// reader reads only what has been folded. A pack recording `key.add NEW` alone
// would therefore tell the next reader that NEW is this project's only key, and
// every task already minted under the founding key would become a foreign ref.
func TestFirstKeyAddRecordsTheFoundingKeyBesideIt(t *testing.T) {
	repo, config := writeRepository(t)
	written := writeConfig(t, repo, config, core.ConfigOperation{Type: core.ConfigKeyAdd, Key: "NEW"})
	keys := written.KeySet(config.Key)
	if got := keys.Keys(); len(got) != 2 || got[0].Key != config.Key || got[1].Key != "NEW" {
		t.Fatalf("Keys() = %#v, want %q then NEW", got, config.Key)
	}
	if got := keys.Current(); got != config.Key {
		t.Fatalf("Current() = %q, want the founding key %q until key.current moves it", got, config.Key)
	}
	// The pack records the founding key, so a clone folding this commit alone
	// reaches the same set without consulting the identity ref.
	if got := len(written.State.Config.Keys.Keys); got != 2 {
		t.Fatalf("stored keys = %d, want 2", got)
	}
}

// The same hazard, driven end to end through a real repository and read back by
// a handle that did not do the write: `Init` with WB, a key pack adding NEW
// through the store's own write path, and then the question every boundary will
// ask — does this project still own the task IDs it has already minted.
func TestARecordedKeySectionStillOwnsTheFoundingKeysTaskIDs(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()
	writeConfig(t, repo, config, core.ConfigOperation{Type: core.ConfigKeyAdd, Key: "NEW"})

	// A second handle holds none of the writer's memos, so what it reports is
	// what a clone folding this ledger cold would report.
	reader, err := Open(ctx, repo.Root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	state, err := reader.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	definitions := state.Keys.Keys()
	if len(definitions) != 2 {
		t.Fatalf("Keys() = %#v, want the founding key and NEW", definitions)
	}
	if definitions[0].Key != config.Key || definitions[0].Retired {
		t.Fatalf("Keys()[0] = %#v, want %q active and first in add order", definitions[0], config.Key)
	}
	if definitions[1].Key != "NEW" || definitions[1].Retired {
		t.Fatalf("Keys()[1] = %#v, want NEW active", definitions[1])
	}
	if got := state.Keys.Current(); got != config.Key {
		t.Fatalf("Current() = %q, want the founding key %q: key.add does not move it", got, config.Key)
	}
	if !state.Keys.Owns(writeTaskID) {
		t.Fatalf("Owns(%q) = false, want true: the project's own task IDs must survive its first key change", writeTaskID)
	}
	if state.Keys.Owns("OTHER-01K0M6B8A4FTT8C39MXXYTW7C2") {
		t.Fatal("Owns(OTHER-…) = true, want false: a key this project never had is another project's ref")
	}
}

// A key change is the only thing that may write the key section. A status
// change against a project that has recorded no keys must come out with no key
// section at all, exactly as it must come out with no priorities section.
func TestAStatusChangeLeavesTheKeySectionAbsent(t *testing.T) {
	repo, config := writeRepository(t)
	written := writeConfig(t, repo, config, relabelOperation("todo", "To Do"))
	if written.State.Config.Keys != nil {
		t.Fatalf("keys = %#v, want nil: only a key operation may write the section", written.State.Config.Keys)
	}
}

// `key.current` and `key.retire` are first key changes too. Neither can name a
// key the section does not yet hold, so each on its own folds to nothing — but
// the backfill runs on any key operation, which is what keeps the rule one
// sentence long instead of three: a project's first key change records the key
// its existing task IDs carry.
func TestAFirstKeyChangeWithoutAnAddStillRecordsTheFoundingKey(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		operation core.ConfigOperation
	}{
		{"key.current alone", core.ConfigOperation{Type: core.ConfigKeyCurrent, Key: "WB"}},
		{"key.retire alone", core.ConfigOperation{Type: core.ConfigKeyRetire, Key: "WB"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo, config := writeRepository(t)
			written := writeConfig(t, repo, config, testCase.operation)
			keys := written.KeySet(config.Key)
			if got := keys.Keys(); len(got) != 1 || got[0].Key != config.Key || got[0].Retired {
				t.Fatalf("Keys() = %#v, want the founding key %q alone and active", got, config.Key)
			}
			if got := keys.Current(); got != config.Key {
				t.Fatalf("Current() = %q, want the founding key %q", got, config.Key)
			}
			if written.State.Config.Keys == nil {
				t.Fatal("keys = nil, want the founding key recorded: the section is written the moment a key " +
					"operation lands, and it must not land empty")
			}
		})
	}
}

// The write result and the next read agree. A key change reports the set its
// own write produced, and the state read afterwards reports the same one.
func TestKeySetRefreshesAfterAConfigurationWrite(t *testing.T) {
	repo, config := writeRepository(t)
	writeConfig(t, repo, config, core.ConfigOperation{Type: core.ConfigKeyAdd, Key: "NEW"},
		core.ConfigOperation{Type: core.ConfigKeyCurrent, Key: "NEW"})
	state, err := repo.LoadVocabularyState(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Keys.Current(); got != "NEW" {
		t.Fatalf("Keys.Current() = %q, want NEW", got)
	}
}

// The memo the boundaries read through: resolved once per opened repository,
// and dropped exactly where this process moves the ledger. Both halves matter
// — a memo that never held would put a Git process on every ref listing, and a
// memo that outlived a write would classify refs against a configuration the
// same command had already superseded.
func TestKeySetMemoHoldsUntilThisProcessMovesTheLedger(t *testing.T) {
	repo, config := writeRepository(t)
	ctx := context.Background()

	first, err := repo.keySet(ctx, config)
	if err != nil {
		t.Fatalf("keySet() error = %v", err)
	}
	if got := first.Keys(); len(got) != 1 || got[0].Key != config.Key {
		t.Fatalf("keySet() = %#v, want the founding key alone", got)
	}

	// Another handle moves the ledger. This one did not, so it answers from its
	// memo rather than paying an enumeration per ref it classifies.
	writer, err := Open(ctx, repo.Root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	writeConfig(t, writer, config, core.ConfigOperation{Type: core.ConfigKeyAdd, Key: "NEW"})
	memoized, err := repo.keySet(ctx, config)
	if err != nil {
		t.Fatalf("keySet() error = %v", err)
	}
	if got := memoized.Keys(); len(got) != 1 {
		t.Fatalf("keySet() = %#v, want the memoized founding key alone: the memo is not per call", got)
	}

	repo.forgetKeySet()
	dropped, err := repo.keySet(ctx, config)
	if err != nil {
		t.Fatalf("keySet() error = %v", err)
	}
	if got := dropped.Keys(); len(got) != 2 {
		t.Fatalf("keySet() after forgetKeySet() = %#v, want both keys", got)
	}

	// A write through this handle drops it too, so later work in the same
	// command reads what that command just wrote.
	writeConfig(t, repo, config, core.ConfigOperation{Type: core.ConfigKeyCurrent, Key: "NEW"})
	refreshed, err := repo.keySet(ctx, config)
	if err != nil {
		t.Fatalf("keySet() error = %v", err)
	}
	if got := refreshed.Current(); got != "NEW" {
		t.Fatalf("keySet().Current() = %q after a write through this handle, want NEW", got)
	}
}

// The backfill adds to the pack, so the ceiling has to hold against what is
// written. It is the same unrepairable failure the priority backfill's ceiling
// guards: a ledger is append-only, and a pack written past the reader's budget
// is a configuration no clone can ever fold again, the writer's included.
func TestAFirstKeyChangeRefusedWhenTheBackfillWouldPushItOverTheCeiling(t *testing.T) {
	repo, config := writeRepository(t)
	operations := make([]core.ConfigOperation, 0, core.MaxConfigOperationsPerPack)
	for i := 0; i < core.MaxConfigOperationsPerPack; i++ {
		operations = append(operations, core.ConfigOperation{Type: core.ConfigKeyCurrent, Key: config.Key})
	}

	_, err := repo.WriteConfigOperation(context.Background(), config, core.CryptoULIDSource{}, operations, "")
	if err == nil {
		t.Fatal("WriteConfigOperation() error = nil, want a refusal: the ceiling of authored operations plus the " +
			"backfilled founding key is one over the pack ceiling")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("error category = %v, want %v; error = %v", got, core.CategoryValidation, err)
	}
	if !strings.Contains(err.Error(), "first key change") {
		t.Fatalf("error = %q, want it to name the backfill that pushed the pack over", err)
	}
	if _, found, readErr := repo.readConfigRef(context.Background(), config, configRef); readErr != nil {
		t.Fatalf("readConfigRef() error = %v", readErr)
	} else if found {
		t.Fatal("the refused write seeded a ledger; a refusal must leave the project exactly as it was")
	}
}
