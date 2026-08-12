package gitstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

// mintedLedgerClone gives a clone a root carrying core.DefaultVocabulary, the
// way `workbook setup` does when it mints a project, and records the caller's
// operations on top of it.
//
// It is half the fixture the divergent-root cases need. The other half is an
// ordinary writeConfig on a clone with no ledger, which seeds a root from
// core.LegacyVocabulary — the only way two roots can now disagree.
func mintedLedgerClone(
	t *testing.T,
	repo *Repository,
	config core.ProjectConfig,
	operations ...core.ConfigOperation,
) ConfigWriteResult {
	t.Helper()
	seeded, err := repo.MintConfigLedger(context.Background(), config, core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}
	if !seeded {
		t.Fatal("MintConfigLedger() = false, want a minted root for a clone with no ledger")
	}
	return writeConfig(t, repo, config, operations...)
}

// TestConfigLedgerRefusesToAdoptARootWithADifferentVocabulary is the case the
// adopt-and-replay path must not settle on its own.
//
// Two unrelated roots converge by adopting origin's, and that is bookkeeping
// only while both roots say the same thing. A minted root carries the statuses
// this build ships and a lazily seeded root carries the ones a pre-ledger
// project was using, so a root can now say either — and adopting across the
// difference redefines the project: a column vanishes with tasks in it, or one
// appears in a project that never had it. Neither is a tiebreak anybody asked
// for, so both are refused and reported.
//
// Both publication orders run, because the harm is not symmetric and a fix that
// caught only the losing direction would leave the other silent.
func TestConfigLedgerRefusesToAdoptARootWithADifferentVocabulary(t *testing.T) {
	for _, test := range []struct {
		name string
		// mintedPublishes says which clone won the publication race, which is
		// the only thing that differs between the two runs.
		mintedPublishes bool
	}{
		{name: "minted root published first", mintedPublishes: true},
		{name: "lazily seeded root published first", mintedPublishes: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			first, second, config := syncRepositories(t)

			// Neither clone synchronizes before writing, so each roots its own
			// ledger — one minted, one seeded from the pre-ledger vocabulary.
			minted := mintedLedgerClone(t, first, config, addOperation("triage", "Triage", "1/2"))
			legacy := writeConfig(t, second, config, configOperations(relabelOperation("blocked", "Waiting"))...)

			publisher, joiner := first, second
			joinerTip := legacy.Head
			if !test.mintedPublishes {
				publisher, joiner = second, first
				joinerTip = minted.Head
			}
			publishConfigRef(t, publisher)

			fresh := openSyncCloneAt(t, joiner.Root)
			result, err := fresh.Fetch(ctx, config)
			if got, want := core.CategoryOf(err), core.CategoryConflict; got != want {
				t.Fatalf("Fetch(divergent roots) category = %q, want %q; error = %v; result = %#v",
					got, want, err, result)
			}
			assertConfigStatus(t, result.Config, SyncConfigConflicted)
			if len(result.ConfigConflicts) != 1 {
				t.Fatalf("configuration conflicts = %#v, want exactly one", result.ConfigConflicts)
			}
			conflict := result.ConfigConflicts[0]
			if conflict.Type != core.ConfigConflictRootVocabulary {
				t.Fatalf("conflict = %#v, want a root-vocabulary conflict", conflict)
			}
			if conflict.Status != "" {
				t.Fatalf("conflict status = %q, want none: the disagreement is about the whole vocabulary",
					conflict.Status)
			}

			// Both starting points are named, so the report is the difference
			// itself rather than a pointer at two commits to read by hand. Ours
			// is always the joiner's own root and Theirs is always origin's,
			// which is what makes the two runs read them the other way round.
			preLedgerSide, mintedSide := conflict.Theirs, conflict.Ours
			if test.mintedPublishes {
				preLedgerSide, mintedSide = conflict.Ours, conflict.Theirs
			}
			if !strings.Contains(preLedgerSide, string(core.StatusBlocked)) {
				t.Fatalf("the pre-ledger side = %q, want it to name the status it defines", preLedgerSide)
			}
			if strings.Contains(mintedSide, string(core.StatusBlocked)) {
				t.Fatalf("the minted side = %q, want it not to name blocked", mintedSide)
			}
			if !strings.Contains(conflict.Detail, parkedConfigRefPrefix) {
				t.Fatalf("detail = %q, want it to name where the local ledger was kept", conflict.Detail)
			}

			// Nothing local was replayed onto a root this clone never agreed to,
			// and nothing was counted as already applied upstream — the two ways
			// an operation disappears quietly.
			if strings.Contains(result.Config.Detail, "already applied upstream") {
				t.Fatalf("detail = %q, want no replay accounting for a refused adoption", result.Config.Detail)
			}
			// The whole local tip is parked, so every operation it recorded is
			// still readable while somebody decides what to do about it.
			if got := refValue(t, joiner, parkedConfigRefPrefix+"0"); got != joinerTip {
				t.Fatalf("parked ref = %q, want the orphaned local tip %q", got, joinerTip)
			}
			// The ledger is origin's, exactly and only origin's.
			if got, want := refValue(t, joiner, configRef), refValue(t, publisher, configRef); got != want {
				t.Fatalf("ledger = %q, want origin's tip %q", got, want)
			}

			settled, err := openSyncCloneAt(t, joiner.Root).LoadVocabulary(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Whichever side published, the joiner reads origin's vocabulary and
			// none of its own intent. Its own operation is not silently folded
			// in; it is parked, reported, and reapplied by hand.
			if got, want := settled.Has(core.StatusBlocked), !test.mintedPublishes; got != want {
				t.Fatalf("Has(blocked) = %t, want %t: the joiner reads origin's root", got, want)
			}
			if got, want := settled.Has("triage"), test.mintedPublishes; got != want {
				t.Fatalf("Has(triage) = %t, want %t: only origin's own operations survive", got, want)
			}
			if !test.mintedPublishes {
				definition, found := statusDefinitionOf(settled, core.StatusBlocked)
				if !found || definition.Label != "Waiting" {
					t.Fatalf("blocked = %#v, want origin's own relabel to have survived", definition)
				}
			}
		})
	}
}

func statusDefinitionOf(vocabulary core.Vocabulary, status core.Status) (core.StatusDefinition, bool) {
	for _, definition := range vocabulary.Definitions() {
		if definition.Status == status {
			return definition, true
		}
	}
	return core.StatusDefinition{}, false
}

// TestConfigReplayReportsAnEditToAStatusOriginNeverDefined closes the second
// half of the same hole.
//
// An operation whose subject the fetched history neither defines nor ever
// retired used to classify as "a no-op nobody needs to decide about". The fold
// applies it to nothing, the result equals the parent, and the replay counts it
// as already applied upstream — so a deliberate change is discarded and the
// report says it landed. It reads as a conflict now.
func TestConfigReplayReportsAnEditToAStatusOriginNeverDefined(t *testing.T) {
	view := newConfigView(core.DefaultVocabulary().Document())

	conflict := classifyConfigOperation(view, core.ConfigOperation{
		Type: core.ConfigStatusRelabel, Status: core.StatusBlocked, Label: "Waiting",
	})
	if conflict == nil {
		t.Fatal("relabelling a status origin never defined classified as a no-op")
	}
	if conflict.Type != core.ConfigConflictStatusDefinition || conflict.Status != core.StatusBlocked {
		t.Fatalf("conflict = %#v, want a status-definition conflict on blocked", conflict)
	}
	if conflict.Theirs != "not defined" {
		t.Fatalf("conflict theirs = %q, want it to say origin does not define the status", conflict.Theirs)
	}

	// A rename and a removal of the same absent subject are the same loss and
	// get the same answer.
	for _, operation := range []core.ConfigOperation{
		{Type: core.ConfigStatusRename, From: core.StatusBlocked, To: "waiting"},
		{Type: core.ConfigStatusRemove, Status: core.StatusBlocked, Destination: core.StatusBacklog},
	} {
		if got := classifyConfigOperation(view, operation); got == nil {
			t.Fatalf("%s of a status origin never defined classified as a no-op", operation.Type)
		}
	}

	// A status the pack defines itself is not absent. The view is built once per
	// pack, before the pack applies, so without this an add and an edit recorded
	// together would report the author's own status as missing.
	pending := newConfigView(core.DefaultVocabulary().Document())
	pending.pending["triage"] = struct{}{}
	if got := classifyConfigOperation(pending, core.ConfigOperation{
		Type: core.ConfigStatusRelabel, Status: "triage", Label: "Triage",
	}); got != nil {
		t.Fatalf("editing a status the same pack adds = %#v, want no conflict", got)
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

// A project with no tasks still publishes its statuses.
//
// Push returned early on an empty task-ref listing, which made it the one
// publication path that could silently keep a vocabulary local — and the
// project likeliest to have a vocabulary and no tasks is one being set up,
// where the columns are exactly what a teammate needs before anything else.
// Every existing publication test creates a task first, which is why none of
// them saw it.
func TestPushPublishesTheLedgerForAProjectWithNoTasks(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	written := writeConfig(t, first, config, configOperations(addOperation("triage", "Triage", "1/2"))...)
	result, err := first.Push(ctx, config)
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if result.Status != SyncPhaseCompleted {
		t.Fatalf("Push() status = %q, want completed", result.Status)
	}
	if result.Config == nil || result.Config.Status != SyncConfigPublished {
		t.Fatalf("Push() config report = %#v, want a published ledger", result.Config)
	}
	if got := remoteRefValue(t, first, configRef); got != written.Head {
		t.Fatalf("origin's ledger = %q, want %q", got, written.Head)
	}

	// The teammate reads the published statuses after an ordinary fetch.
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	vocabulary, err := openSyncCloneAt(t, second.Root).LoadVocabulary(ctx)
	if err != nil {
		t.Fatalf("LoadVocabulary() error = %v", err)
	}
	if !vocabulary.Has("triage") {
		t.Fatalf("teammate statuses = %#v, want the published triage", vocabulary.Definitions())
	}
}

// PushConfig is the publication a status change makes: the ledger alone, with
// no task ref to name, and the same identity settlement in front of it.
func TestPushConfigPublishesTheLedgerAlone(t *testing.T) {
	ctx := context.Background()
	first, _, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Kept local")
	written := writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)

	published, err := first.PushConfig(ctx, config)
	if err != nil {
		t.Fatalf("PushConfig() error = %v", err)
	}
	if published == nil || published.Status != SyncConfigPublished || published.Head != written.Head {
		t.Fatalf("PushConfig() = %#v, want the ledger published", published)
	}
	if got := remoteRefValue(t, first, configRef); got != written.Head {
		t.Fatalf("origin's ledger = %q, want %q", got, written.Head)
	}
	if remoteRefExists(t, first, taskRefPrefix+task.ID) {
		t.Fatal("PushConfig() published a task ref, want the ledger alone")
	}

	// A second call has nothing to do and says nothing, which is what keeps a
	// status command that changed nothing from reporting a publication.
	repeat, err := first.PushConfig(ctx, config)
	if err != nil {
		t.Fatalf("PushConfig() error = %v", err)
	}
	if repeat != nil {
		t.Fatalf("PushConfig() = %#v, want no report when origin is current", repeat)
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

// TestPushSucceedsAgainstAnOriginHoldingTheLedger guards a publication that
// could not survive its own success.
//
// Push widened its one remote listing to ask about the configuration ref, and
// the parser reading that listing skipped only the identity ref, so the first
// project ever to publish a vocabulary broke publication for every teammate —
// and with it their code pushes, because the managed pre-push hook's only
// statement is the Workbook publication this test drives. Every existing
// publication test pushes before any ledger exists, which is exactly why none
// of them saw it.
func TestPushSucceedsAgainstAnOriginHoldingTheLedger(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// Origin gains a ledger, published by somebody else.
	writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	publishConfigRef(t, first)

	// A teammate who has never touched a status publishes a task.
	fresh := openSyncCloneAt(t, second.Root)
	task := createSyncTask(t, fresh, config, "Pushed past origin's ledger")
	result, err := fresh.Push(ctx, config)
	if err != nil {
		t.Fatalf("Push() error = %v; result = %#v", err, result)
	}
	assertSyncOutcome(t, result, task.ID, SyncPublished)
	if !remoteRefExists(t, fresh, taskRefPrefix+task.ID) {
		t.Fatalf("origin has no %s after a push that reported publishing it", taskRefPrefix+task.ID)
	}
	// Origin's ledger is untouched by a push that had nothing to say about it.
	if got, want := remoteRefValue(t, fresh, configRef), refValue(t, first, configRef); got != want {
		t.Fatalf("origin's ledger = %q, want the untouched %q", got, want)
	}

	// A full Sync reads the same listing shape and behaves the same way.
	syncTask := createSyncTask(t, fresh, config, "Synced past origin's ledger")
	run, err := fresh.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, run)
	}
	assertSyncOutcome(t, run.Push, syncTask.ID, SyncPublished)

	// And so does the targeted push every automatically synchronizing mutation
	// makes, which never lists origin at all.
	targeted := createSyncTask(t, fresh, config, "Targeted past origin's ledger")
	if _, err := fresh.PushTask(ctx, config, targeted.ID); err != nil {
		t.Fatalf("PushTask() error = %v", err)
	}
}

// TestReplayBudgetRefusesAHugeDivergenceWithoutDecidingAnything is the second
// resource bound's regression guard, driven through a real divergence rather
// than asserted about the constant.
//
// The refusal has to have four properties at once, and only a real reconcile
// can show all four: it is operational, it names the bound so somebody can
// raise it, it leaves this clone's ledger exactly where it was — no move, no
// park — and it does not stop the tasks from synchronizing.
func TestReplayBudgetRefusesAHugeDivergenceWithoutDecidingAnything(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// A shared ledger, then one commit on each side so the two diverge.
	writeConfig(t, first, config, configOperations(renameOperation("ready", "todo"))...)
	publishConfigRef(t, first)
	if _, err := second.Fetch(ctx, config); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, first, config, configOperations(relabelOperation("doing", "Doing"))...)
	publishConfigRef(t, first)
	// A task nobody disputes, to prove the tasks still move.
	task := createSyncTask(t, first, config, "Unrelated to the ledger")
	publishTaskRefs(t, first)

	// One commit past the budget on this side. The objects are written directly
	// rather than through WriteConfigOperation, so the fixture costs three Git
	// processes a commit instead of eight; what is under test is the fold's
	// refusal, not the authoring path.
	local := growConfigLedger(t, second, config, core.MaxConfigLedgerReplayCommits+1)

	fresh := openSyncCloneAt(t, second.Root)
	result, err := fresh.Fetch(ctx, config)
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("Fetch() category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), "MaxConfigLedgerReplayCommits") {
		t.Fatalf("error = %q, want it to name the bound so it can be raised", err)
	}
	if result.Config == nil || result.Config.Status != SyncConfigInvalid {
		t.Fatalf("configuration result = %#v, want the refusal recorded", result.Config)
	}
	if result.Config.Moved {
		t.Fatalf("configuration result = %#v, want a refusal that moved nothing", result.Config)
	}
	if got := refValue(t, second, configRef); got != local {
		t.Fatalf("%s = %q, want the refusal to have left it at %q", configRef, got, local)
	}
	if refExists(t, second, parkedConfigRefPrefix+"0") {
		t.Fatal("a refusal parked a tip it never replaced")
	}
	// The tasks are the project's history, and one oversized ledger does not
	// stop them.
	if result.Status != SyncPhaseCompleted {
		t.Fatalf("fetch phase = %q, want it to have completed the task work", result.Status)
	}
	assertSyncOutcome(t, result, task.ID, SyncCreated)
}

// growConfigLedger appends count relabel commits to a repository's ledger and
// returns the new tip, writing objects directly and moving the ref once.
func growConfigLedger(t *testing.T, repo *Repository, config core.ProjectConfig, count int) string {
	t.Helper()
	ctx := context.Background()
	tip, found, err := repo.readConfigRef(ctx, config, configRef)
	if err != nil || !found {
		t.Fatalf("readConfigRef() = (found %t, error %v), want an existing ledger", found, err)
	}
	head, state := tip.Head, tip.State
	wallTime := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	for index := 0; index < count; index++ {
		pack, err := core.NewConfigOperationPack(
			state.ProjectID,
			state.History.Generation,
			"author@example.test",
			state.LogicalClock+1,
			wallTime,
			[]core.ConfigOperation{{
				ID:     mustConfigOperationID(t, index),
				Type:   core.ConfigStatusRelabel,
				Status: "todo",
				Label:  fmt.Sprintf("To Do %d", index),
			}},
		)
		if err != nil {
			t.Fatalf("NewConfigOperationPack(%d) error = %v", index, err)
		}
		next, err := core.ApplyConfig(&state, pack)
		if err != nil {
			t.Fatalf("ApplyConfig(%d) error = %v", index, err)
		}
		head, err = repo.writeConfigObjects(ctx, head, pack, next, "workbook: offline configuration change")
		if err != nil {
			t.Fatalf("writeConfigObjects(%d) error = %v", index, err)
		}
		state = next
	}
	syncGit(t, repo.Root, "update-ref", configRef, head, tip.Head)
	return head
}
