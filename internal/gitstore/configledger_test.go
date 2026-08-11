package gitstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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
