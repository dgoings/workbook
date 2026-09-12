package gitstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The priority operation constructors below mirror the status ones in
// configledger_test.go (renameOperation, addOperation, relabelOperation),
// carrying the priority-domain fields instead.

func addPriorityOperation(name core.Priority, label, rank string, tags ...core.PriorityTag) core.ConfigOperation {
	if tags == nil {
		tags = []core.PriorityTag{}
	}
	return core.ConfigOperation{
		Type: core.ConfigPriorityAdd, PriorityName: name, Label: label, Rank: rank, PriorityTags: tags,
	}
}

func renamePriorityOperation(from, to core.Priority) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityRename, PriorityFrom: from, PriorityTo: to}
}

func relabelPriorityOperation(priority core.Priority, label string) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityRelabel, Priority: priority, Label: label}
}

func removePriorityOperation(priority, destination core.Priority) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityRemove, Priority: priority, PriorityDestination: destination}
}

func tagPriorityOperation(priority core.Priority, tag core.PriorityTag) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityTag, Priority: priority, PriorityTag: tag}
}

func untagPriorityOperation(priority core.Priority, tag core.PriorityTag) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityUntag, Priority: priority, PriorityTag: tag}
}

func recolorPriorityOperation(priority core.Priority, color string) core.ConfigOperation {
	return core.ConfigOperation{Type: core.ConfigPriorityRecolor, Priority: priority, Value: color}
}

// priorityConfigData wraps a priority document as a whole configuration, the
// shape newConfigView reads. The vocabulary section is filled with the
// default statuses purely so this is a valid ConfigData; nothing here reads
// it.
func priorityConfigData(document core.PriorityDocument) core.ConfigData {
	return core.ConfigData{Vocabulary: core.DefaultVocabulary().Document(), Priorities: &document}
}

// priorityView builds a configView whose parent and fork are the same
// priority document — every test below classifies a single operation against
// one fetched state, so the fork never has to differ from it.
func priorityView(document core.PriorityDocument) configView {
	config := priorityConfigData(document)
	return newConfigView(config, config)
}

// classifyConfigOperation dispatches every one of the eight priority.*
// operation types to a priority classifier, exactly as it already dispatches
// the status ones to classifyConfigRename, classifyConfigRemove and friends.
// Each case below exercises one branch of one classifier through that single
// entry point, because that is what a replay actually calls.
func TestClassifyConfigPriorityOperations(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		document  core.PriorityDocument
		operation core.ConfigOperation
		want      *core.ConfigConflict
	}{
		{
			name: "rename target collides with a priority origin already defines",
			document: core.PriorityDocument{Priorities: []core.PriorityDefinition{
				{Priority: core.PriorityHigh, Label: "High", Rank: "1"},
				{Priority: core.PriorityMedium, Label: "Medium", Rank: "2", Tags: []core.PriorityTag{core.PriorityTagDefault}},
			}},
			operation: renamePriorityOperation(core.PriorityHigh, core.PriorityMedium),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRename, Priority: core.PriorityHigh,
				Ours: "medium", Theirs: "medium",
			},
		},
		{
			name: "both sides renamed it to the same token",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: "critical", Label: "Critical", Rank: "1"}},
				Aliases:    []core.PriorityAlias{{From: core.PriorityHigh, To: "critical"}},
			},
			operation: renamePriorityOperation(core.PriorityHigh, "critical"),
			want:      nil,
		},
		{
			name: "both sides renamed it to different tokens",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: "critical", Label: "Critical", Rank: "1"}},
				Aliases:    []core.PriorityAlias{{From: core.PriorityHigh, To: "critical"}},
			},
			operation: renamePriorityOperation(core.PriorityHigh, "urgent"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRename, Priority: core.PriorityHigh,
				Ours: "urgent", Theirs: "critical",
			},
		},
		{
			name: "rename subject origin already removed",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: "critical", Label: "Critical", Rank: "1"}},
				Retired:    []core.RetiredPriority{{Priority: core.PriorityHigh, Destination: "critical"}},
			},
			operation: renamePriorityOperation(core.PriorityHigh, "extreme"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRetired, Priority: core.PriorityHigh,
				Ours: "extreme", Theirs: "critical",
			},
		},
		{
			name: "rename subject origin never defined",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityMedium, Label: "Medium", Rank: "1"}},
			},
			operation: renamePriorityOperation(core.PriorityHigh, "extreme"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityDefinition, Priority: core.PriorityHigh,
				Ours: "a local priority.rename of this priority", Theirs: "not defined",
			},
		},
		{
			name: "removing the only live priority is refused as arity",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1"}},
			},
			operation: removePriorityOperation(core.PriorityHigh, core.PriorityMedium),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityArity, Priority: core.PriorityHigh,
			},
		},
		{
			name: "both sides removed it into different destinations",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{
					{Priority: core.PriorityLow, Label: "Low", Rank: "1"},
					{Priority: core.PriorityMedium, Label: "Medium", Rank: "2"},
				},
				Retired: []core.RetiredPriority{{Priority: core.PriorityHigh, Destination: core.PriorityLow}},
			},
			operation: removePriorityOperation(core.PriorityHigh, core.PriorityMedium),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRetired, Priority: core.PriorityHigh,
				Ours: "medium", Theirs: "low",
			},
		},
		{
			name: "both sides removed it into the same destination",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{
					{Priority: core.PriorityLow, Label: "Low", Rank: "1"},
					{Priority: core.PriorityMedium, Label: "Medium", Rank: "2"},
				},
				Retired: []core.RetiredPriority{{Priority: core.PriorityHigh, Destination: core.PriorityLow}},
			},
			operation: removePriorityOperation(core.PriorityHigh, core.PriorityLow),
			want:      nil,
		},
		{
			name: "an in-place edit lands on a priority origin removed",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityLow, Label: "Low", Rank: "1"}},
				Retired:    []core.RetiredPriority{{Priority: core.PriorityHigh, Destination: core.PriorityLow}},
			},
			operation: relabelPriorityOperation(core.PriorityHigh, "Critical"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRetired, Priority: core.PriorityHigh, Theirs: "low",
			},
		},
		{
			name: "an in-place edit lands on a priority origin never defined",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityLow, Label: "Low", Rank: "1"}},
			},
			operation: relabelPriorityOperation(core.PriorityHigh, "Critical"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityDefinition, Priority: core.PriorityHigh,
				Ours: "a local priority.relabel of this priority", Theirs: "not defined",
			},
		},
		{
			// priority.recolor dispatches through classifyConfigOperation to
			// classifyConfigPrioritySubject exactly like relabel/reorder/tag/
			// untag — nothing distinguishes it in the switch — so a recolor
			// landing on a priority origin already retired is reported the
			// same way a relabel landing there is, rather than silently
			// falling through to `default: return nil`.
			name: "priority.recolor lands on a priority origin removed",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityLow, Label: "Low", Rank: "1"}},
				Retired:    []core.RetiredPriority{{Priority: core.PriorityHigh, Destination: core.PriorityLow}},
			},
			operation: recolorPriorityOperation(core.PriorityHigh, "#ff0000"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityRetired, Priority: core.PriorityHigh, Theirs: "low",
			},
		},
		{
			// A recolor of a priority that still exists converges silently,
			// like every other in-place edit: classifyConfigPrioritySubject
			// never compares the edit's value (the new color) against what is
			// stored, only whether the subject exists. This is the concrete
			// case ConfigConflictPriorityDefinition's own doc comment points
			// to when it says a color disagreement cannot reach a conflict
			// through this classifier.
			name: "priority.recolor on a live priority converges silently",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1", Color: "#00ff00"}},
			},
			operation: recolorPriorityOperation(core.PriorityHigh, "#ff0000"),
			want:      nil,
		},
		{
			name: "two clones add the same priority with the same definition",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1"}},
			},
			operation: addPriorityOperation(core.PriorityHigh, "High", "1"),
			want:      nil,
		},
		{
			name: "two clones add the same priority with different labels",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1"}},
			},
			operation: addPriorityOperation(core.PriorityHigh, "Critical", "1"),
			want: &core.ConfigConflict{
				Type: core.ConfigConflictPriorityDefinition, Priority: core.PriorityHigh,
				Ours: `"Critical" at rank 1`, Theirs: `"High" at rank 1`,
			},
		},
		{
			// Color is deliberately NOT compared here even though it is part of
			// the definition classifyConfigPriorityAdd otherwise compares whole.
			// priority.add can never carry a color (see configOperationShapes;
			// only priority.recolor can), so this local add expresses no opinion
			// about color at all — there is no local intent to lose, only a
			// field this operation cannot assert. Comparing it against whatever
			// origin has would manufacture a conflict out of every ordinary
			// concurrent add of a priority origin has since recolored.
			name: "origin recolored the priority this clone is adding, unnoticed by the add",
			document: core.PriorityDocument{
				Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1", Color: "#ff0000"}},
			},
			operation: addPriorityOperation(core.PriorityHigh, "High", "1"),
			want:      nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyConfigOperation(priorityView(testCase.document), testCase.operation)
			if (got == nil) != (testCase.want == nil) {
				t.Fatalf("classifyConfigOperation() = %#v, want conflict = %v", got, testCase.want != nil)
			}
			if got == nil {
				return
			}
			if got.Type != testCase.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, testCase.want.Type)
			}
			if got.Priority != testCase.want.Priority {
				t.Errorf("Priority = %q, want %q", got.Priority, testCase.want.Priority)
			}
			if got.Status != "" {
				t.Errorf("Status = %q, want a priority conflict to name no status", got.Status)
			}
			if got.Ours != testCase.want.Ours {
				t.Errorf("Ours = %q, want %q", got.Ours, testCase.want.Ours)
			}
			if got.Theirs != testCase.want.Theirs {
				t.Errorf("Theirs = %q, want %q", got.Theirs, testCase.want.Theirs)
			}
		})
	}
}

// A priority created earlier in the same pack is not undefined, the same rule
// markPackSubjects already enforces for a status add followed by a relabel in
// one pack.
func TestClassifyConfigPriorityOperationIgnoresItsOwnPacksAdd(t *testing.T) {
	document := core.PriorityDocument{
		Priorities: []core.PriorityDefinition{{Priority: core.PriorityMedium, Label: "Medium", Rank: "1"}},
	}
	view := priorityView(document)
	operations := []core.ConfigOperation{
		addPriorityOperation("critical", "Critical", "0"),
		relabelPriorityOperation("critical", "Urgent"),
	}
	markPackSubjects(view, operations)
	if conflict := classifyConfigOperation(view, operations[1]); conflict != nil {
		t.Fatalf("classifyConfigOperation() = %#v, want no conflict for a subject this pack just defined", conflict)
	}
}

// A status change must not be classified against the priority maps; the two
// sections are independent, mirroring TestClassifyConfigDisplayIgnoresStatusChanges's
// first half.
func TestClassifyConfigPriorityIgnoresStatusChanges(t *testing.T) {
	document := core.PriorityDocument{
		Priorities: []core.PriorityDefinition{{Priority: core.PriorityHigh, Label: "High", Rank: "1"}},
	}
	view := priorityView(document)
	if conflict := classifyConfigOperation(view, renameOperation(core.StatusReady, "todo")); conflict != nil {
		t.Fatalf("a status rename against a configured priority section = %#v, want no conflict", conflict)
	}
}

// A project with no priorities section at all — the nil ConfigData.Priorities
// every project carried before this stage — still classifies a priority
// operation correctly rather than panicking on a nil dereference: the section
// reads as configured-nothing, and an edit to an undefined priority is a
// conflict, not a silent no-op. See priorityDocumentOf.
func TestClassifyConfigPriorityOperationOnAnUnconfiguredProject(t *testing.T) {
	statusOnly := core.ConfigData{Vocabulary: core.DefaultVocabulary().Document()}
	view := newConfigView(statusOnly, statusOnly)
	conflict := classifyConfigOperation(view, relabelPriorityOperation(core.PriorityHigh, "Critical"))
	if conflict == nil || conflict.Type != core.ConfigConflictPriorityDefinition {
		t.Fatalf("classifyConfigOperation() = %#v, want a priority-definition conflict", conflict)
	}
}

// classifyConfigPriorityArity reads the default-tag repair out of the fold's
// own result, mirroring how classifyConfigArity reads a status role repair.
func TestClassifyConfigPriorityArity(t *testing.T) {
	t.Run("repair moved the default tag by position", func(t *testing.T) {
		before := core.PriorityDocument{Priorities: []core.PriorityDefinition{
			{Priority: core.PriorityHigh, Rank: "1"},
			{Priority: core.PriorityMedium, Rank: "2", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		}}
		after := core.PriorityDocument{Priorities: []core.PriorityDefinition{
			{Priority: core.PriorityHigh, Rank: "1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		}}
		pack := core.ConfigOperationPack{Operations: []core.ConfigOperation{
			removePriorityOperation(core.PriorityMedium, core.PriorityHigh),
		}}
		conflicts := classifyConfigPriorityArity(before, after, pack)
		if len(conflicts) != 1 {
			t.Fatalf("classifyConfigPriorityArity() = %#v, want exactly one conflict", conflicts)
		}
		conflict := conflicts[0]
		if conflict.Type != core.ConfigConflictPriorityArity || conflict.Priority != core.PriorityHigh {
			t.Fatalf("conflict = %#v, want the arity type naming %q", conflict, core.PriorityHigh)
		}
		if !strings.Contains(conflict.Detail, "default") {
			t.Fatalf("conflict detail = %q, want it to name the default role", conflict.Detail)
		}
	})

	t.Run("the pack itself tagged the survivor, so nothing was repaired", func(t *testing.T) {
		before := core.PriorityDocument{Priorities: []core.PriorityDefinition{
			{Priority: core.PriorityHigh, Rank: "1"},
			{Priority: core.PriorityMedium, Rank: "2", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		}}
		after := core.PriorityDocument{Priorities: []core.PriorityDefinition{
			{Priority: core.PriorityHigh, Rank: "1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
			{Priority: core.PriorityMedium, Rank: "2"},
		}}
		pack := core.ConfigOperationPack{Operations: []core.ConfigOperation{
			tagPriorityOperation(core.PriorityHigh, core.PriorityTagDefault),
			untagPriorityOperation(core.PriorityMedium, core.PriorityTagDefault),
		}}
		if conflicts := classifyConfigPriorityArity(before, after, pack); len(conflicts) != 0 {
			t.Fatalf("classifyConfigPriorityArity() = %#v, want none: the pack chose this explicitly", conflicts)
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		document := core.PriorityDocument{Priorities: []core.PriorityDefinition{
			{Priority: core.PriorityMedium, Rank: "1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		}}
		pack := core.ConfigOperationPack{Operations: []core.ConfigOperation{relabelPriorityOperation(core.PriorityMedium, "Medium")}}
		if conflicts := classifyConfigPriorityArity(document, document, pack); len(conflicts) != 0 {
			t.Fatalf("classifyConfigPriorityArity() = %#v, want none", conflicts)
		}
	})
}

// End-to-end: two clones rename the same priority to different tokens, and
// the second clone's sync reports the conflict rather than silently keeping
// origin's, mirroring TestConfigSyncReportsDivergentDisplaySettings.
func TestConfigSyncReportsDivergentPriorityRename(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	writeConfig(t, first, config, addPriorityOperation(core.PriorityHigh, "High", "1/1", core.PriorityTagDefault))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() (seed) error = %v", err)
	}
	if _, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() (fetch seed) error = %v", err)
	}

	writeConfig(t, first, config, renamePriorityOperation(core.PriorityHigh, "critical"))
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() (rename) error = %v", err)
	}
	writeConfig(t, second, config, renamePriorityOperation(core.PriorityHigh, "urgent"))

	run, err := second.Sync(ctx, config)
	if err == nil || core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("second Sync() error = %v, want a conflict", err)
	}
	if len(run.Fetch.ConfigConflicts) != 1 {
		t.Fatalf("config conflicts = %#v, want one", run.Fetch.ConfigConflicts)
	}
	conflict := run.Fetch.ConfigConflicts[0]
	if conflict.Type != core.ConfigConflictPriorityRename {
		t.Fatalf("conflict = %#v, want a priority-rename conflict", conflict)
	}
	if conflict.Priority != core.PriorityHigh {
		t.Fatalf("conflict priority = %q, want %q", conflict.Priority, core.PriorityHigh)
	}
	if conflict.Ours != "urgent" || conflict.Theirs != "critical" {
		t.Fatalf("conflict values = (%q, %q), want the local and origin tokens", conflict.Ours, conflict.Theirs)
	}
	if got := core.ConfigConflictError(run.Fetch.ConfigConflicts).Error(); !strings.HasPrefix(got, "priority high: ") {
		t.Fatalf("ConfigConflictError() = %q, want it to lead with %q", got, "priority high: ")
	}
}

// End-to-end: neither clone's own pack ever asks ApplyConfig to repair
// anything — each is valid against the fork it was authored on — but their
// composition during replay is not, and the replay reports what it repaired
// rather than deciding a new default silently.
//
// ValidateConfigAuthoring refuses a pack that removes the only default-tagged
// priority without retagging one, against the author's OWN state (Ruling 33),
// which is exactly why this cannot be built from a single clone's history: it
// takes two clones each doing something individually unremarkable — origin
// moves the default from high to medium, this clone (still on the old fork)
// removes medium, a plain priority at the time it authored the removal — and
// only their combination, seen from origin's tip, leaves nothing tagged
// default. The repair then picks the lowest-ranked survivor by position
// (high), not the removal's own destination (low), which is what makes this
// the reported case rather than the silent one classifyConfigPriorityArity's
// own comment describes.
func TestConfigSyncReportsPriorityArityRepair(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	// The genesis now carries the built-in three outright (medium tagged
	// default; see writeConfigGenesis), so adding high, medium and low here
	// would be three no-ops under applyAdd's idempotency rule — the names are
	// already live — and would never move the default onto high the way this
	// test's precondition needs. Moving it explicitly is what actually
	// establishes "high is the default" before the rest of the test relies on
	// it.
	writeConfig(t, first, config,
		untagPriorityOperation(core.PriorityMedium, core.PriorityTagDefault),
		tagPriorityOperation(core.PriorityHigh, core.PriorityTagDefault),
	)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() (seed) error = %v", err)
	}
	if _, err := second.Sync(ctx, config); err != nil {
		t.Fatalf("second Sync() (fetch seed) error = %v", err)
	}

	// This clone's pack is authored against the seed above, where high — not
	// medium — is the default, so removing medium is an ordinary edit that
	// never touches arity from where this clone sits.
	writeConfig(t, second, config, removePriorityOperation(core.PriorityMedium, core.PriorityLow))

	// Origin, meanwhile, moves the default from high to medium — an explicit,
	// individually valid change — and publishes first.
	writeConfig(t, first, config,
		untagPriorityOperation(core.PriorityHigh, core.PriorityTagDefault),
		tagPriorityOperation(core.PriorityMedium, core.PriorityTagDefault),
	)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatalf("first Sync() (move the default) error = %v", err)
	}

	run, err := second.Sync(ctx, config)
	if err == nil || core.CategoryOf(err) != core.CategoryConflict {
		t.Fatalf("second Sync() error = %v, want a conflict", err)
	}
	if len(run.Fetch.ConfigConflicts) != 1 {
		t.Fatalf("config conflicts = %#v, want one", run.Fetch.ConfigConflicts)
	}
	conflict := run.Fetch.ConfigConflicts[0]
	if conflict.Type != core.ConfigConflictPriorityArity {
		t.Fatalf("conflict = %#v, want a priority-arity conflict", conflict)
	}
	if conflict.Priority != core.PriorityHigh {
		t.Fatalf("arity conflict priority = %q, want %q, the lowest-ranked survivor the repair picked by position",
			conflict.Priority, core.PriorityHigh)
	}
}

// MintConfigLedger records core.BuiltInPriorityVocabulary() into the genesis
// alongside the vocabulary, the priority counterpart to
// TestMintConfigLedgerRecordsTheDefaultVocabulary. The written pack's minimum
// version is 3, the same as any other priorities section: an older reader
// does not fall back to its own built-ins for a section it does not
// recognize, it refuses the checkpoint, so recording priorities at creation
// marks every project this build creates — the deliberate trade
// ConfigPackMinReader's comment describes.
func TestMintConfigLedgerRecordsBuiltInPriorities(t *testing.T) {
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
	genesis := root.Operation.Operations[0]
	if genesis.Type != core.ConfigGenesis || genesis.Config.Priorities == nil {
		t.Fatalf("root pack = %#v, want one config.genesis carrying a priorities section", root.Operation.Operations)
	}
	if got := *genesis.Config.Priorities; !reflect.DeepEqual(got, core.BuiltInPriorityVocabulary().Document()) {
		t.Fatalf("genesis priorities = %#v, want the built-in three", got)
	}
	if got := root.Operation.MinReader; got != 3 {
		t.Fatalf("genesis pack MinReader = %d, want 3", got)
	}
}

// A project with no ledger whose first authored change is an ordinary status
// operation still gets a genesis recording the built-in three priorities —
// the lazy-seed counterpart to TestMintConfigLedgerRecordsBuiltInPriorities —
// and the genesis pack it seeds with still carries a minimum version of 3:
// an ordinary status change is what triggers the seed, but the genesis it
// produces is marked on its own terms because it, too, now carries a
// priorities section.
func TestWriteConfigOperationSeedsGenesisWithBuiltInPriorities(t *testing.T) {
	repo, config := writeRepository(t)

	result := writeConfig(t, repo, config, configOperations(renameOperation("ready", "todo"))...)
	if !result.Seeded {
		t.Fatal("WriteConfigOperation() did not report seeding the ledger")
	}

	records := configChain(t, repo, config)
	if len(records) != 2 {
		t.Fatalf("ledger holds %d commit(s), want a genesis root and the author's pack", len(records))
	}
	root := records[0]
	genesis := root.Operation.Operations[0]
	if genesis.Type != core.ConfigGenesis || genesis.Config.Priorities == nil {
		t.Fatalf("root pack = %#v, want one config.genesis carrying a priorities section", root.Operation.Operations)
	}
	if got := *genesis.Config.Priorities; !reflect.DeepEqual(got, core.BuiltInPriorityVocabulary().Document()) {
		t.Fatalf("genesis priorities = %#v, want the built-in three", got)
	}
	if got := root.Operation.MinReader; got != 3 {
		t.Fatalf("genesis pack MinReader = %d, want 3", got)
	}
}

// writeLegacyConfigGenesis seeds a ledger the way a project created before
// this build's priorities section existed would have one: a genesis whose
// ConfigData carries a vocabulary but no priorities section at all. No path
// in this build produces such a genesis anymore — seedConfigLedger and
// MintConfigLedger both record core.BuiltInPriorityVocabulary() now — so this
// exists purely to give the tests below the tip Task 2 has to backfill
// against: a real, already-existing ledger whose immutable root predates
// priorities.
func writeLegacyConfigGenesis(t *testing.T, repo *Repository, config core.ProjectConfig) {
	t.Helper()
	ctx := context.Background()
	ids := core.CryptoULIDSource{}
	generation, err := ids.New()
	if err != nil {
		t.Fatalf("generation ID error = %v", err)
	}
	genesisID, err := ids.New()
	if err != nil {
		t.Fatalf("genesis ID error = %v", err)
	}
	actor, err := repo.Actor(ctx)
	if err != nil {
		t.Fatalf("Actor() error = %v", err)
	}
	pack, err := core.NewConfigOperationPack(config.ProjectID, generation, actor, 1, configWallTime(),
		[]core.ConfigOperation{{
			ID:   genesisID,
			Type: core.ConfigGenesis,
			Config: &core.ConfigData{
				Vocabulary: core.LegacyVocabulary().Document(),
			},
		}})
	if err != nil {
		t.Fatalf("NewConfigOperationPack() error = %v", err)
	}
	state, err := core.ApplyConfig(nil, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	head, err := repo.writeConfigObjects(ctx, "", pack, state, configGenesisSubject)
	if err != nil {
		t.Fatalf("writeConfigObjects() error = %v", err)
	}
	if err := repo.createRefWithReason(ctx, configRef, head, configRefLogReason); err != nil {
		t.Fatalf("createRefWithReason() error = %v", err)
	}
}

// A project whose ledger predates the priorities section entirely — the
// state writeLegacyConfigGenesis recreates — gets the built-in three
// backfilled into the very same pack as its first priority.* operation, so
// the tasks that were always high/medium/low keep resolving under a section
// that, until this write, did not exist.
func TestAppendConfigOperationBackfillsBuiltInPrioritiesOnFirstPriorityChange(t *testing.T) {
	repo, config := writeRepository(t)
	writeLegacyConfigGenesis(t, repo, config)

	result := writeConfig(t, repo, config, addPriorityOperation("critical", "Critical", "4/1"))

	document := result.State.Config.Priorities
	if document == nil {
		t.Fatal("state.Config.Priorities = nil, want the built-in three plus the added priority")
	}
	if len(document.Priorities) != 4 {
		t.Fatalf("priorities = %#v, want the built-in three plus one added", document.Priorities)
	}
	for _, want := range core.BuiltInPriorityVocabulary().Definitions() {
		found := false
		for _, got := range document.Priorities {
			if got.Priority == want.Priority {
				found = true
				if got.Label != want.Label || got.Rank != want.Rank || !reflect.DeepEqual(got.Tags, want.Tags) {
					t.Fatalf("backfilled priority %q = %#v, want %#v", want.Priority, got, want)
				}
			}
		}
		if !found {
			t.Fatalf("priorities = %#v, missing built-in %q", document.Priorities, want.Priority)
		}
	}

	// A task filed under low before this project ever touched priorities
	// still resolves: low is live, not stranded by an unrelated add.
	vocabulary := result.State.PriorityVocabulary()
	if !vocabulary.Has(core.PriorityLow) {
		t.Fatalf("PriorityVocabulary().Has(low) = false, want the built-in low to still resolve")
	}
}

// A project whose priorities section is already configured — whether seeded
// at genesis by this build or backfilled by an earlier priority change of its
// own — does not get a second helping of the built-in three merely because
// another priority.* operation is authored against it.
func TestAppendConfigOperationDoesNotDuplicateBuiltInsWhenPrioritiesAlreadyConfigured(t *testing.T) {
	repo, config := writeRepository(t)

	seeded, err := repo.MintConfigLedger(context.Background(), config, core.CryptoULIDSource{})
	if err != nil {
		t.Fatalf("MintConfigLedger() error = %v", err)
	}
	if !seeded {
		t.Fatal("MintConfigLedger() = false, want a genesis written for a project with no ledger")
	}

	result := writeConfig(t, repo, config, addPriorityOperation("critical", "Critical", "4/1"))

	document := result.State.Config.Priorities
	if document == nil {
		t.Fatal("state.Config.Priorities = nil, want the built-in three plus the added priority")
	}
	if len(document.Priorities) != 4 {
		t.Fatalf("priorities = %#v, want exactly the built-in three plus one added, not a second helping of built-ins",
			document.Priorities)
	}

	// The folded document alone cannot tell a correctly-skipped backfill apart
	// from a backfill that ran anyway and no-oped against priorities already
	// live (applyAdd is idempotent by name) — so the pack actually written to
	// the ledger is what this test has to inspect: it must carry the one
	// operation the caller authored, not that operation preceded by three
	// redundant priority.add operations nobody asked for.
	records := configChain(t, repo, config)
	last := records[len(records)-1]
	if len(last.Operation.Operations) != 1 {
		t.Fatalf("written pack operations = %#v, want only the caller's priority.add: the section was already "+
			"configured, so nothing should have been prepended", last.Operation.Operations)
	}
}

// The invariant the whole task protects: a project whose ledger predates
// priorities, given an ordinary STATUS change, must come out with no
// priorities section at all — the backfill trigger is priority.* operations
// only, never widened to "any configuration write."
func TestAppendConfigOperationStatusChangeLeavesPrioritiesSectionAbsent(t *testing.T) {
	repo, config := writeRepository(t)
	writeLegacyConfigGenesis(t, repo, config)

	result := writeConfig(t, repo, config, renameOperation(core.StatusReady, "todo"))

	if result.State.Config.Priorities != nil {
		t.Fatalf("state.Config.Priorities = %#v, want nil: a status change must not fill the priorities section",
			result.State.Config.Priorities)
	}

	// The stored bytes carry no priorities section either — not just the
	// decoded struct — the same check TestWriteConfigOperationRecordsDisplaySettings
	// makes for a display-blind genesis.
	stateJSON := gitOutput(t, repo, "show", result.Head+":state.json")
	if strings.Contains(stateJSON, "priorities") {
		t.Fatalf("stored state.json = %s, want no priorities section for an unrelated status change", stateJSON)
	}
}

// The backfill adds to the pack, so the pack-size ceiling has to hold against
// what is written rather than against what the caller asked for.
//
// This is the one failure in this area that cannot be walked back. A ledger is
// append-only: an oversized pack passes the write path, gets a commit, and is
// then refused by the budget check every reader runs — including the reader in
// the clone that wrote it — so the project's configuration becomes unfoldable
// forever. Refusing the write costs the caller one error message.
func TestAppendConfigOperationRefusesAWriteTheBackfillWouldPushOverTheCeiling(t *testing.T) {
	repo, config := writeRepository(t)
	writeLegacyConfigGenesis(t, repo, config)

	operations := make([]core.ConfigOperation, 0, core.MaxConfigOperationsPerPack)
	for i := 0; i < core.MaxConfigOperationsPerPack; i++ {
		operations = append(operations, relabelPriorityOperation(core.PriorityHigh, fmt.Sprintf("High %d", i)))
	}

	_, err := repo.WriteConfigOperation(context.Background(), config, core.CryptoULIDSource{}, operations, "")
	if err == nil {
		t.Fatal("WriteConfigOperation() error = nil, want a refusal: 64 authored operations plus the three " +
			"backfilled built-ins is 67, over the pack ceiling")
	}
	if got := core.CategoryOf(err); got != core.CategoryValidation {
		t.Fatalf("WriteConfigOperation() error category = %v, want %v", got, core.CategoryValidation)
	}

	// The refusal has to come before anything is written. A ledger that gained
	// a commit here is the unrepairable state this test exists to prevent, so
	// the tip must still be the genesis.
	records := configChain(t, repo, config)
	if len(records) != 1 {
		t.Fatalf("configuration ledger has %d commits, want only the genesis: the refused write must not have "+
			"appended anything", len(records))
	}
}

// A write that fits once the backfill is counted still goes through, so the
// check above is a ceiling rather than a new, lower one.
func TestAppendConfigOperationAcceptsAWriteThatFitsWithTheBackfill(t *testing.T) {
	repo, config := writeRepository(t)
	writeLegacyConfigGenesis(t, repo, config)

	operations := make([]core.ConfigOperation, 0, core.MaxConfigOperationsPerPack-3)
	for i := 0; i < core.MaxConfigOperationsPerPack-3; i++ {
		operations = append(operations, relabelPriorityOperation(core.PriorityHigh, fmt.Sprintf("High %d", i)))
	}

	result := writeConfig(t, repo, config, operations...)

	records := configChain(t, repo, config)
	last := records[len(records)-1]
	if got := len(last.Operation.Operations); got != core.MaxConfigOperationsPerPack {
		t.Fatalf("written pack carries %d operations, want exactly the ceiling of %d",
			got, core.MaxConfigOperationsPerPack)
	}
	if result.State.Config.Priorities == nil {
		t.Fatal("state.Config.Priorities = nil, want the backfilled built-ins")
	}
}
