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

func displayView(display core.DisplaySettings) configView {
	return newConfigView(core.ConfigData{
		Vocabulary: core.DefaultVocabulary().Document(),
		Display: &core.DisplayDocument{
			Name:         display.Name,
			PrimaryColor: display.PrimaryColor,
			TextColor:    display.TextColor,
		},
	})
}

// The classification matrix for a section whose three values are independent.
//
// The rule it pins is the whole difference between a conflict and convergence:
// two intents about the same setting that cannot both hold need a person, and
// everything else — the same value chosen twice, two different settings, a
// setting origin never touched — converges without one. The `default: return
// nil` arm of classifyConfigOperation would answer every row of this table with
// "no conflict", so every conflicting row here is also the test that the display
// types are classified at all.
func TestClassifyConfigDisplaySurfacesOnlyDisagreement(t *testing.T) {
	for _, testCase := range []struct {
		name      string
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
			name:      "origin cleared what this clone set",
			operation: setDisplayOperation(core.DisplayPrimaryColor, "#1a7f4b"),
		},
		{
			name:      "this clone cleared what origin set",
			fetched:   core.DisplaySettings{PrimaryColor: "#1a7f4b"},
			operation: unsetDisplayOperation(core.DisplayPrimaryColor),
			conflict:  true,
		},
		{
			name:      "both sides cleared it",
			operation: unsetDisplayOperation(core.DisplayTextColor),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conflict := classifyConfigOperation(displayView(testCase.fetched), testCase.operation)
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
	view := displayView(core.DisplaySettings{Name: "Atlas"})
	if conflict := classifyConfigOperation(view, renameOperation(core.StatusReady, "todo")); conflict != nil {
		t.Fatalf("a status rename against a configured display = %#v, want no conflict", conflict)
	}
	statusView := newConfigView(core.ConfigData{Vocabulary: core.DefaultVocabulary().Document()})
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
