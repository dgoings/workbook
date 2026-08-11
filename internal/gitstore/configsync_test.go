package gitstore

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func publishConfigRef(t *testing.T, repo *Repository) {
	t.Helper()
	syncGit(t, repo.Root, "push", "origin", configRef+":"+configRef)
}

// TestConfigLedgerLifecycleAcrossTwoClones walks the whole singleton lifecycle
// in one test, because the states are only meaningful in sequence: what
// "diverged" means depends on what "fast-forwarded" left behind.
func TestConfigLedgerLifecycleAcrossTwoClones(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// created: the second clone gains a ledger it did not have.
	seeded := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	publishConfigRef(t, first)
	result, err := second.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(created) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigCreated)
	if got := refValue(t, second, configRef); got != seeded.Head {
		t.Fatalf("created ledger = %q, want origin's %q", got, seeded.Head)
	}
	vocabulary, err := second.LoadVocabulary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, live := vocabulary.Resolve("ready"); !live || resolved != "todo" {
		t.Fatalf("Resolve(ready) = (%q, %t), want (todo, true)", resolved, live)
	}

	// unchanged: nothing moved, so nothing is reported at all.
	fresh := openSyncCloneAt(t, second.Root)
	result, err = fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(unchanged) error = %v; result = %#v", err, result)
	}
	if result.Config != nil {
		t.Fatalf("Fetch(unchanged) config = %#v, want nothing reported", result.Config)
	}

	// fast-forwarded: origin's ledger contains this clone's.
	advanced := writeConfig(t, first, config, configOperations(relabelOperation("todo", "To Do"))...)
	publishConfigRef(t, first)
	fresh = openSyncCloneAt(t, second.Root)
	result, err = fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(fast-forward) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigFastForwarded)
	if got := refValue(t, second, configRef); got != advanced.Head {
		t.Fatalf("fast-forwarded ledger = %q, want origin's %q", got, advanced.Head)
	}

	// local-ahead: this clone holds configuration origin does not.
	local := writeConfig(t, second, config, configOperations(addOperation("triage", "Triage", "1/2"))...)
	fresh = openSyncCloneAt(t, second.Root)
	result, err = fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(local-ahead) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigLocalAhead)
	if got := refValue(t, second, configRef); got != local.Head {
		t.Fatalf("local-ahead fetch moved the ledger from %q to %q", local.Head, got)
	}

	// reconciled: both sides moved, and the local operation replays cleanly.
	writeConfig(t, first, config, configOperations(relabelOperation("doing", "Doing"))...)
	publishConfigRef(t, first)
	fresh = openSyncCloneAt(t, second.Root)
	result, err = fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(reconciled) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigReconciled)
	reconciled := refValue(t, second, configRef)
	if reconciled == local.Head {
		t.Fatal("reconciled ledger did not move off the orphaned local tip")
	}
	if !mergeBaseIsAncestor(t, second.Root, remoteRefValue(t, first, configRef), reconciled) {
		t.Fatal("reconciled ledger is not a descendant of the fetched tip")
	}
	if !refExists(t, second, parkedConfigRefPrefix+"0") {
		t.Fatalf("reconciliation did not park the orphaned local tip at %s0", parkedConfigRefPrefix)
	}
	if got := refValue(t, second, parkedConfigRefPrefix+"0"); got != local.Head {
		t.Fatalf("parked ref = %q, want the orphaned local tip %q", got, local.Head)
	}
	settled, err := openSyncCloneAt(t, second.Root).LoadVocabulary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Has("triage") {
		t.Fatalf("statuses = %#v, want the replayed local add to have survived", settled.Definitions())
	}

	// conflicted: two clones rename the same status to different tokens, which
	// converges to something but discards an intent, so a person decides.
	publishConfigRef(t, second)
	if _, err := first.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch(before conflict) error = %v", err)
	}
	writeConfig(t, first, config, configOperations(renameOperation("todo", "queued"))...)
	publishConfigRef(t, first)
	writeConfig(t, second, config, configOperations(renameOperation("todo", "next-up"))...)
	conflictedLocal := refValue(t, second, configRef)

	fresh = openSyncCloneAt(t, second.Root)
	result, err = fresh.Fetch(ctx, config)
	if got, want := core.CategoryOf(err), core.CategoryConflict; got != want {
		t.Fatalf("Fetch(conflict) category = %q, want %q; error = %v; result = %#v", got, want, err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigConflicted)
	if len(result.ConfigConflicts) != 1 {
		t.Fatalf("configuration conflicts = %#v, want exactly one", result.ConfigConflicts)
	}
	conflict := result.ConfigConflicts[0]
	if conflict.Type != core.ConfigConflictStatusRename || conflict.Status != "todo" {
		t.Fatalf("conflict = %#v, want a status-rename conflict on todo", conflict)
	}
	if conflict.Ours != "next-up" || conflict.Theirs != "queued" {
		t.Fatalf("conflict values = (%q, %q), want both intents reported", conflict.Ours, conflict.Theirs)
	}
	if !refExists(t, second, parkedConfigRefPrefix+"1") {
		t.Fatalf("a conflicted replay did not park the local tip at %s1", parkedConfigRefPrefix)
	}
	if got := refValue(t, second, parkedConfigRefPrefix+"1"); got != conflictedLocal {
		t.Fatalf("parked ref = %q, want the orphaned local tip %q", got, conflictedLocal)
	}
}

// TestConfigLedgerLazyGenesisConvergesAfterAFetch is the common case the design
// relies on: fetching before mutating means the second clone sees the first's
// root and appends to it rather than minting a competing one.
func TestConfigLedgerLazyGenesisConvergesAfterAFetch(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// Both clones start with no ledger and both observe that.
	if _, err := first.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	seeded := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	publishConfigRef(t, first)

	// The second clone's first configuration change happens after it fetches
	// again, so it appends rather than seeding.
	fresh := openSyncCloneAt(t, second.Root)
	result, err := fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch() error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigCreated)

	appended := writeConfig(t, openSyncCloneAt(t, second.Root), config,
		configOperations(addOperation("triage", "Triage", "1/2"))...)
	if appended.Seeded {
		t.Fatal("the second clone seeded a competing root after fetching origin's")
	}
	if !mergeBaseIsAncestor(t, second.Root, seeded.Head, appended.Head) {
		t.Fatal("the appended commit is not a descendant of origin's ledger")
	}
	if got := appended.Vocabulary(); !got.Has("triage") || !got.Has("todo") {
		t.Fatalf("statuses = %#v, want both clones' intents", got.Definitions())
	}
}

// TestConfigLedgerAdoptsOriginsRootWhenBothClonesSeeded is the case fetching
// cannot settle: two clones each seeded a root while neither could see the
// other, so the two ledgers are unrelated histories rather than a divergence
// inside one.
func TestConfigLedgerAdoptsOriginsRootWhenBothClonesSeeded(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// Neither clone synchronizes before writing, which is exactly what --no-sync
	// produces, so each mints its own genesis.
	published := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	local := writeConfig(t, second, config, configOperations(addOperation("triage", "Triage", "1/2"))...)
	publishConfigRef(t, first)
	if configChain(t, first, config)[0].ObjectID == configChain(t, second, config)[0].ObjectID {
		t.Fatal("the fixture did not produce two different genesis roots")
	}

	fresh := openSyncCloneAt(t, second.Root)
	result, err := fresh.Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(unrelated roots) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigReconciled)
	if !strings.Contains(result.Config.Detail, "adopted origin's configuration root") {
		t.Fatalf("detail = %q, want it to say the root was adopted", result.Config.Detail)
	}

	adopted := refValue(t, second, configRef)
	if !mergeBaseIsAncestor(t, second.Root, published.Head, adopted) {
		t.Fatal("the reconciled ledger did not adopt origin's root")
	}
	settled, err := openSyncCloneAt(t, second.Root).LoadVocabulary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Origin won the publication race, so its root is the project's root. The
	// local operation is intent and survives.
	if !settled.Has("triage") {
		t.Fatalf("statuses = %#v, want the local intent replayed onto origin's root", settled.Definitions())
	}
	if resolved, live := settled.Resolve("ready"); !live || resolved != "todo" {
		t.Fatalf("Resolve(ready) = (%q, %t), want origin's rename to hold", resolved, live)
	}
	if got := refValue(t, second, parkedConfigRefPrefix+"0"); got != local.Head {
		t.Fatalf("parked ref = %q, want the orphaned local root %q", got, local.Head)
	}
}

// TestSyncReportsRefsItCannotReadUnderOriginsConfigName is the identity ref's
// tolerance rule, restated for the second singleton: refs under origin's
// configuration name are origin's business, and a clone reads past them and
// says what it skipped rather than refusing to run.
func TestSyncReportsRefsItCannotReadUnderOriginsConfigName(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	seeded := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	syncGit(t, first.Root, "push", "origin", seeded.Head+":"+configRef+"/notes")

	run, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, run)
	}
	if run.Config == nil || len(run.Config.Ignored) != 1 {
		t.Fatalf("Sync() config = %#v, want one ignored ref", run.Config)
	}
	if got, want := run.Config.Ignored[0], configRef+"/notes"; got != want {
		t.Fatalf("ignored ref = %q, want it named as origin holds it, %q", got, want)
	}
	if run.Fetch.Status != SyncPhaseCompleted || run.Push.Status != SyncPhaseCompleted {
		t.Fatalf("phases = fetch %q, push %q, want both completed", run.Fetch.Status, run.Push.Status)
	}
}

// TestConfigLedgerRidesTheTaskPublicationPath is the publication invariant: a
// vocabulary change reaches origin through the same paths a task change does,
// rather than waiting for a full synchronization.
func TestConfigLedgerRidesTheTaskPublicationPath(t *testing.T) {
	ctx := context.Background()
	first, _, config := syncRepositories(t)

	// The shape of an automatically synchronizing mutation: fetch, write, push
	// exactly the ref that changed.
	if _, err := first.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	seeded := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	task := createSyncTask(t, first, config, "Published beside its vocabulary")
	if _, err := first.PushTask(ctx, config, task.ID); err != nil {
		t.Fatalf("PushTask() error = %v", err)
	}

	if !remoteRefExists(t, first, configRef) {
		t.Fatalf("origin has no %s after a mutation published a task ref", configRef)
	}
	if got := remoteRefValue(t, first, configRef); got != seeded.Head {
		t.Fatalf("origin's ledger = %q, want %q", got, seeded.Head)
	}
	report, found := first.ConfigReport()
	if !found || report.Status != SyncConfigPublished {
		t.Fatalf("ConfigReport() = (%#v, %t), want a published report", report, found)
	}
}

// TestVocabularyPropagatesAndCorrectsOnTouch is the end-to-end claim the whole
// story rests on: one clone renames a status, another clone reads its stored
// tasks into the new column, and the next write to such a task settles the
// stored token.
func TestVocabularyPropagatesAndCorrectsOnTouch(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	stored := createSyncTask(t, first, config, "Stored under the old name")
	readyStatus := core.Status("ready")
	if _, err := syncService(first, config).UpdateMutation(ctx, stored.ID, core.UpdateInput{Status: &readyStatus}); err != nil {
		t.Fatalf("UpdateMutation(ready) error = %v", err)
	}
	writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	fetched := openSyncCloneAt(t, second.Root)
	if _, err := fetched.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	reader := openSyncCloneAt(t, second.Root)
	vocabulary, err := reader.LoadVocabulary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service := syncService(reader, config)
	service.Vocabulary = vocabulary

	task, err := service.Show(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if task.Status != "todo" {
		t.Fatalf("resolved status = %q, want todo", task.Status)
	}
	if task.StoredStatus != "ready" {
		t.Fatalf("stored status = %q, want the untouched ready", task.StoredStatus)
	}

	// Correct on touch: the next write to this task settles the stored token,
	// and the settlement is a real appended operation rather than a projection.
	title := "Touched"
	result, err := service.UpdateMutation(ctx, stored.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if result.StatusCorrected == nil ||
		result.StatusCorrected.From != "ready" ||
		result.StatusCorrected.To != "todo" {
		t.Fatalf("StatusCorrected = %#v, want ready → todo", result.StatusCorrected)
	}
	if result.Task.StoredStatus != "" {
		t.Fatalf("stored status = %q, want the settled task to carry none", result.Task.StoredStatus)
	}
	snapshot, err := reader.Get(ctx, config, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.State.Task.Status; got != "todo" {
		t.Fatalf("stored status after the touch = %q, want todo", got)
	}
	settled := false
	for _, operation := range snapshot.Operation.Operations {
		if operation.Type == core.OperationFieldSet && operation.Field == "status" && operation.Value == "todo" {
			settled = true
		}
	}
	if !settled {
		t.Fatalf("the appended pack = %#v, want it to carry the status settlement", snapshot.Operation.Operations)
	}
}

// TestConfigLedgerFoldFailureDoesNotBlockTaskSynchronization is the ordering
// decision made falsifiable: one disputed status must not stop a team's tasks
// from moving.
func TestConfigLedgerFoldFailureDoesNotBlockTaskSynchronization(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	publishConfigRef(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, first, config, configOperations(renameOperation("todo", "queued"))...)
	publishConfigRef(t, first)
	writeConfig(t, second, config, configOperations(renameOperation("todo", "next-up"))...)

	// A task that has nothing to do with the disputed status.
	task := createSyncTask(t, first, config, "Unrelated task")
	publishTaskRefs(t, first)

	fresh := openSyncCloneAt(t, second.Root)
	result, err := fresh.Fetch(ctx, config)
	if got, want := core.CategoryOf(err), core.CategoryConflict; got != want {
		t.Fatalf("Fetch() category = %q, want %q; error = %v", got, want, err)
	}
	if result.Status != SyncPhaseCompleted {
		t.Fatalf("fetch phase = %q, want it to have completed the task work anyway", result.Status)
	}
	assertSyncOutcome(t, result, task.ID, SyncCreated)
	if got := refValue(t, second, taskRefPrefix+task.ID); got == "" {
		t.Fatalf("task %s was not fetched past the configuration conflict", task.ID)
	}
}

func assertConfigStatus(t *testing.T, result *SyncConfigResult, want SyncConfigStatus) {
	t.Helper()
	if result == nil {
		t.Fatalf("configuration result = nil, want status %q", want)
	}
	if result.Status != want {
		t.Fatalf("configuration status = %q, want %q; result = %#v", result.Status, want, result)
	}
}

// openSyncCloneAt reopens an existing clone, so a test can observe what a fresh
// process sees rather than what the previous one memoized.
func openSyncCloneAt(t *testing.T, path string) *Repository {
	t.Helper()
	repo, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestConfigLedgerReplaysThroughASHA256Origin covers the ledger's own transport
// and compare-and-swap against 64-character object IDs. Every other
// configuration test runs SHA-1, so a fixed-width assumption on the parking
// name, the ref transaction, or the replay would survive all of them.
func TestConfigLedgerReplaysThroughASHA256Origin(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositoriesWithObjectFormat(t, testrepo.FormatSHA256)

	seeded := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	if len(seeded.Head) != 64 || strings.TrimLeft(seeded.Head, "0123456789abcdef") != "" {
		t.Fatalf("ledger head = %q, want a 64-character SHA-256 object ID", seeded.Head)
	}
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	// Diverge and replay across the transport in this format.
	writeConfig(t, first, config, configOperations(relabelOperation("todo", "To Do"))...)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	local := writeConfig(t, second, config, configOperations(addOperation("triage", "Triage", "1/2"))...)

	result, err := openSyncCloneAt(t, second.Root).Fetch(ctx, config)
	if err != nil {
		t.Fatalf("Fetch(diverged) error = %v; result = %#v", err, result)
	}
	assertConfigStatus(t, result.Config, SyncConfigReconciled)
	if got := refValue(t, second, parkedConfigRefPrefix+"0"); got != local.Head {
		t.Fatalf("parked ref = %q, want the orphaned local tip %q", got, local.Head)
	}
	settled, err := openSyncCloneAt(t, second.Root).LoadVocabulary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settled.Has("triage") || settled.Label("todo") != "To Do" {
		t.Fatalf("statuses = %#v, want both sides' changes", settled.Definitions())
	}
}

// TestConcurrentConfigWritesConvergeOnOneLedger races two independently opened
// repository handles on the same Git directory, which is what two Workbook
// processes racing a status change look like from Git's side. There is no CLI
// verb to run as two binaries yet, so the race is driven through the ledger API
// the verbs will call.
//
// The claim is not that both writes land: only one can, because the ledger's ref
// compare-and-swap is the exclusion. The claim is that the loser is refused
// rather than overwriting, and that the survivor is one well formed ledger with
// exactly one root.
func TestConcurrentConfigWritesConvergeOnOneLedger(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)

	handles := []*Repository{openSyncCloneAt(t, repo.Root), openSyncCloneAt(t, repo.Root)}
	operations := [][]core.ConfigOperation{
		configOperations(addOperation("triage", "Triage", "1/2")),
		configOperations(addOperation("review", "Review", "5/2")),
	}
	results := make([]error, len(handles))
	var wait sync.WaitGroup
	for index := range handles {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, results[index] = handles[index].WriteConfigOperation(
				ctx, config, core.CryptoULIDSource{}, operations[index], "")
		}(index)
	}
	wait.Wait()

	succeeded := 0
	for index, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		// A loser is refused, not silently dropped, and the refusal names a
		// retryable condition rather than corruption.
		if category := core.CategoryOf(err); category != core.CategoryStaleWrite && category != core.CategoryOperational {
			t.Fatalf("concurrent write %d category = %q, want a retryable refusal; error = %v", index, category, err)
		}
	}
	if succeeded == 0 {
		t.Fatalf("no concurrent configuration write succeeded; results = %v", results)
	}

	// Whatever the interleaving, the repository holds one readable ledger with
	// one root, and the surviving intent is in it.
	fresh := openSyncCloneAt(t, repo.Root)
	records := configChain(t, fresh, config)
	if len(records) < 2 {
		t.Fatalf("ledger holds %d commit(s), want at least a root and one change", len(records))
	}
	if records[0].Operation.Operations[0].Type != core.ConfigGenesis {
		t.Fatalf("root pack = %#v, want one config.genesis", records[0].Operation.Operations)
	}
	for _, record := range records[1:] {
		for _, operation := range record.Operation.Operations {
			if operation.Type == core.ConfigGenesis {
				t.Fatalf("commit %s carries a second genesis", record.ObjectID)
			}
		}
	}
	vocabulary, err := fresh.LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if !vocabulary.Has("triage") && !vocabulary.Has("review") {
		t.Fatalf("statuses = %#v, want the winning write to be readable", vocabulary.Definitions())
	}
}
