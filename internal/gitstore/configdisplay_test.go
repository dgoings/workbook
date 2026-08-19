package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func setDisplayOperation(setting, value string) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigDisplaySet, Setting: setting, Value: value}
}

func unsetDisplayOperation(setting string) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigDisplayUnset, Setting: setting}
}

func displayConfig(display core.DisplaySettings) core.ConfigData {
	return core.ConfigData{
		Vocabulary: core.DefaultVocabulary().Document(),
		Display: &core.DisplayDocument{
			Name:         display.Name,
			PrimaryColor: display.PrimaryColor,
			TextColor:    display.TextColor,
		},
	}
}

// displayView builds the two sides classification compares: what origin's tip
// says the settings are, and what they were in the commit the local pack was
// authored onto.
func displayView(fetched, fork core.DisplaySettings) configView {
	return newConfigView(displayConfig(fetched), displayConfig(fork))
}

// The classification matrix for a section whose three values are independent.
//
// The rule it pins is the whole difference between a conflict and convergence:
// two intents about the same setting that cannot both hold need a person, and
// everything else — the same value chosen twice, two different settings, a
// setting nobody upstream has touched since the fork — converges without one.
// The `default: return nil` arm of classifyConfigOperation would answer every
// row of this table with "no conflict", so every conflicting row here is also
// the test that the display types are classified at all.
//
// Every row carries a fork as well as a fetched tip, because origin's current
// value alone cannot tell "origin cleared this since we forked" from "origin
// never configured it": both read as the empty string. The fork is what makes
// the two directions of a set-against-unset symmetric.
func TestClassifyConfigDisplaySurfacesOnlyDisagreement(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		fork      core.DisplaySettings
		fetched   core.DisplaySettings
		operation core.ConfigOperation
		conflict  bool
	}{
		{
			name:      "origin never configured it",
			operation: setDisplayOperation(core.DisplayProjectName, "Atlas"),
		},
		{
			name:      "both sides chose the same name",
			fetched:   core.DisplaySettings{Name: "Atlas"},
			operation: setDisplayOperation(core.DisplayProjectName, "Atlas"),
		},
		{
			name:      "both sides chose different names",
			fetched:   core.DisplaySettings{Name: "Borealis"},
			operation: setDisplayOperation(core.DisplayProjectName, "Atlas"),
			conflict:  true,
		},
		{
			name:      "a different setting moved upstream",
			fetched:   core.DisplaySettings{PrimaryColor: "#1a7f4b"},
			operation: setDisplayOperation(core.DisplayProjectName, "Atlas"),
		},
		{
			name:      "this clone is the only one that moved the setting",
			fork:      core.DisplaySettings{Name: "Seed"},
			fetched:   core.DisplaySettings{Name: "Seed"},
			operation: setDisplayOperation(core.DisplayProjectName, "Atlas"),
		},
		{
			name:      "this clone cleared what nobody upstream had touched",
			fork:      core.DisplaySettings{Name: "Seed"},
			fetched:   core.DisplaySettings{Name: "Seed"},
			operation: unsetDisplayOperation(core.DisplayProjectName),
		},
		{
			// Both sides had the fork's color and both moved off it: origin to
			// nothing, this clone to another color. Origin's tip reads empty,
			// which is what "never configured" reads as too — the fork is the
			// only thing that tells the two apart.
			name:      "origin cleared what this clone set",
			fork:      core.DisplaySettings{PrimaryColor: "#1a7f4b"},
			operation: setDisplayOperation(core.DisplayPrimaryColor, "#2457d6"),
			conflict:  true,
		},
		{
			name:      "this clone cleared what origin set",
			fetched:   core.DisplaySettings{PrimaryColor: "#1a7f4b"},
			operation: unsetDisplayOperation(core.DisplayPrimaryColor),
			conflict:  true,
		},
		{
			name:      "both sides cleared it",
			fork:      core.DisplaySettings{TextColor: "#101820"},
			operation: unsetDisplayOperation(core.DisplayTextColor),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conflict := classifyConfigOperation(displayView(testCase.fetched, testCase.fork), testCase.operation)
			if testCase.conflict != (conflict != nil) {
				t.Fatalf("classifyConfigOperation() = %#v, want conflict = %t", conflict, testCase.conflict)
			}
			if conflict == nil {
				return
			}
			if conflict.Type != core.ConfigConflictDisplaySetting {
				t.Fatalf("conflict type = %q, want %q", conflict.Type, core.ConfigConflictDisplaySetting)
			}
			if conflict.Status != "" {
				t.Fatalf("conflict status = %q, want a display conflict to name no status", conflict.Status)
			}
			if !strings.Contains(conflict.Detail, testCase.operation.Setting) {
				t.Fatalf("conflict detail = %q, want it to name the setting %q",
					conflict.Detail, testCase.operation.Setting)
			}
		})
	}
}

// A display change is not a status change, and the two must not classify each
// other: a clone that renamed a status while origin renamed the board converges
// on both.
func TestClassifyConfigDisplayIgnoresStatusChanges(t *testing.T) {
	view := displayView(core.DisplaySettings{Name: "Atlas"}, core.DisplaySettings{})
	if conflict := classifyConfigOperation(view, renameOperation(core.StatusReady, "todo")); conflict != nil {
		t.Fatalf("a status rename against a configured display = %#v, want no conflict", conflict)
	}
	statusConfig := core.ConfigData{Vocabulary: core.DefaultVocabulary().Document()}
	statusView := newConfigView(statusConfig, statusConfig)
	if conflict := classifyConfigOperation(statusView, setDisplayOperation(core.DisplayTextColor, "#101820")); conflict != nil {
		t.Fatalf("a display set against an unconfigured project = %#v, want no conflict", conflict)
	}
}

// The ledger carries display settings the way it carries statuses: written by
// one clone, read back resolved, and reported as part of the state the status
// verbs already read.
func TestWriteConfigOperationRecordsDisplaySettings(t *testing.T) {
	ctx := context.Background()
	repo, config := writeRepository(t)

	written := writeConfig(t, repo, config,
		setDisplayOperation(core.DisplayProjectName, "Atlas"),
		setDisplayOperation(core.DisplayPrimaryColor, "#1a7f4b"))
	if got := written.State.Display(); got.Name != "Atlas" || got.PrimaryColor != "#1a7f4b" {
		t.Fatalf("written display = %#v", got)
	}

	state, err := repo.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display != (core.DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b"}) {
		t.Fatalf("LoadVocabularyState() display = %#v", state.Display)
	}
	if !state.Seeded || state.Head != written.Head {
		t.Fatalf("LoadVocabularyState() = %#v, want the tip this write produced", state)
	}

	// The genesis the lazy seed wrote carries no display section at all, which
	// is what keeps a project that predates this section byte-identical to what
	// it was.
	root := gitOutput(t, repo, "rev-list", "--max-parents=0", configRef)
	genesis := gitOutput(t, repo, "show", strings.TrimSpace(root)+":state.json")
	if strings.Contains(genesis, "display") {
		t.Fatalf("the seeded genesis carries a display section: %s", genesis)
	}
}

// Two clones that name the project differently is the conflict a person has to
// settle; the ledger still converges on origin's value so neither clone is
// wedged.
func TestConfigSyncReportsDivergentDisplaySettings(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	writeConfig(t, first, config, setDisplayOperation(core.DisplayProjectName, "Atlas"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	writeConfig(t, second, config, setDisplayOperation(core.DisplayProjectName, "Borealis"))

	run, err := second.Sync(ctx, config)
	if err == nil || core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("second Sync() error = %v, want a conflict", err)
	}
	if len(run.Fetch.ConfigConflicts) != 1 {
		t.Fatalf("config conflicts = %#v, want one", run.Fetch.ConfigConflicts)
	}
	conflict := run.Fetch.ConfigConflicts[0]
	if conflict.Type != core.ConfigConflictDisplaySetting {
		t.Fatalf("conflict = %#v, want a display-setting conflict", conflict)
	}
	if conflict.Ours != "Borealis" || conflict.Theirs != "Atlas" {
		t.Fatalf("conflict values = (%q, %q), want the local and origin names", conflict.Ours, conflict.Theirs)
	}
	// The clone converged on origin's ledger rather than keeping two, which is
	// what makes the conflict a decision to make rather than a wedge.
	state, err := second.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display.Name != "Atlas" {
		t.Fatalf("display after the conflicted replay = %#v, want origin's", state.Display)
	}
}

// The direction origin's tip alone cannot see: origin cleared a setting both
// clones had, and this one set it to something else.
//
// It is the same disagreement as the passing direction — one clone kept a value
// and the other decided the project should have none — and it has to surface as
// one. Reading only origin's current value cannot tell this apart from "origin
// never configured it", because both are the empty string, so before the fork
// was consulted this converged silently on exit 0 and origin's deliberate
// clearing was overwritten without anybody being told.
func TestConfigSyncSurfacesOriginClearingWhatThisCloneSet(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// Both clones start from one published value, which is what makes the two
	// later changes a disagreement rather than two independent decisions.
	writeConfig(t, first, config, setDisplayOperation(core.DisplayProjectName, "Seed"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	if _, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	state, err := second.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display.Name != "Seed" {
		t.Fatalf("second clone display = %#v, want the published seed", state.Display)
	}

	writeConfig(t, first, config, unsetDisplayOperation(core.DisplayProjectName))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() after clearing error = %v", err)
	}
	writeConfig(t, second, config, setDisplayOperation(core.DisplayProjectName, "Atlas"))

	run, err := second.Sync(ctx, config)
	if err == nil || core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("second Sync() error = %v, want a conflict", err)
	}
	if len(run.Fetch.ConfigConflicts) != 1 {
		t.Fatalf("config conflicts = %#v, want one", run.Fetch.ConfigConflicts)
	}
	conflict := run.Fetch.ConfigConflicts[0]
	if conflict.Type != core.ConfigConflictDisplaySetting {
		t.Fatalf("conflict = %#v, want a display-setting conflict", conflict)
	}
	if conflict.Ours != "Atlas" || conflict.Theirs != "" {
		t.Fatalf("conflict values = (%q, %q), want the local name against origin's clearing",
			conflict.Ours, conflict.Theirs)
	}
	// The line has its own arm, and the arm is the whole reason this direction
	// reads as anything: the shared wording puts origin's value in a sentence
	// with nowhere to say there is none, so a clearing rendered through it comes
	// out as "and to  on origin" — a double space where a fact should be.
	wanted := "project-name was set to Atlas here and cleared on origin, so origin's clearing stands; " +
		"set it again to keep the local intent"
	if conflict.Detail != wanted {
		t.Fatalf("conflict detail = %q, want %q", conflict.Detail, wanted)
	}
	// Origin's clearing stands, as it does in the other direction, so the two
	// clones still hold one ledger while somebody decides.
	state, err = second.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display.Name != "" {
		t.Fatalf("display after the conflicted replay = %#v, want origin's clearing", state.Display)
	}
}

// Two local changes to one setting replay in order, and the second is not a
// conflict with the first.
//
// The fork advances along the local chain while the replay's parent advances
// along the rewritten one, and they are two different questions: the parent is
// what an operation lands on, the fork is what its author was looking at. Hold
// the fork still at the shared base and the second local pack is classified
// against a value its own author had already replaced — so this clone's second
// change reads as a disagreement with origin, is discarded, and the conflict
// report names origin as holding a value origin never had.
func TestConfigSyncReplaysASecondDisplayChangeOntoTheFirst(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// One published value both clones fork from.
	writeConfig(t, first, config, setDisplayOperation(core.DisplayProjectName, "Seed"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	if _, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	// Origin then moves something that is not this setting. That is what makes
	// the two ledgers diverge, so the local packs are replayed rather than
	// fast-forwarded past — and it leaves origin's own value for the setting
	// exactly where the fork left it.
	writeConfig(t, first, config, relabelOperation(core.StatusBacklog, "Inbox"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() after the relabel error = %v", err)
	}

	// Two local changes to the same setting, in order, each its own pack.
	writeConfig(t, second, config, setDisplayOperation(core.DisplayProjectName, "Alpha"))
	writeConfig(t, second, config, setDisplayOperation(core.DisplayProjectName, "Beta"))

	run, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("second Sync() error = %v, want both local packs replayed", err)
	}
	if len(run.Fetch.ConfigConflicts) != 0 {
		t.Fatalf("config conflicts = %#v, want none: this clone disagreed with nobody",
			run.Fetch.ConfigConflicts)
	}
	if run.Fetch.Config == nil {
		t.Fatal("the fetch reported nothing about the configuration ledger")
	}
	if !strings.Contains(run.Fetch.Config.Detail, "replayed 2 configuration operation(s)") {
		t.Fatalf("config stage detail = %q, want both local packs replayed", run.Fetch.Config.Detail)
	}

	state, err := second.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display.Name != "Beta" {
		t.Fatalf("display after the replay = %#v, want this clone's later change", state.Display)
	}
	// And origin's own change came through, which is the other half of a replay
	// that lost nothing.
	if label := state.Vocabulary.Label(core.StatusBacklog); label != "Inbox" {
		t.Fatalf("backlog label after the replay = %q, want origin's %q", label, "Inbox")
	}
}

// The other clone's display change lands on this one, unconflicted, which is
// the ordinary case the conflict above is the exception to.
func TestConfigSyncCarriesADisplaySettingBetweenClones(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	writeConfig(t, first, config,
		setDisplayOperation(core.DisplayProjectName, "Atlas"),
		setDisplayOperation(core.DisplayTextColor, "#101820"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	if _, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	state, err := second.LoadVocabularyState(ctx, config)
	if err != nil {
		t.Fatalf("LoadVocabularyState() error = %v", err)
	}
	if state.Display != (core.DisplaySettings{Name: "Atlas", TextColor: "#101820"}) {
		t.Fatalf("fetched display = %#v", state.Display)
	}
}
