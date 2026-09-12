package gitstore

import (
	"context"
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
// TestMintConfigLedgerRecordsTheDefaultVocabulary. Because that document is
// exactly what an older reader falling back to its own built-ins would
// compute, the pack's minimum version stays 0 — recording it does not, by
// itself, ask anyone to upgrade.
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
	if got := root.Operation.MinReader; got != 0 {
		t.Fatalf("genesis pack MinReader = %d, want 0: a genesis-carried document identical to the built-ins "+
			"must not tell an older reader to upgrade", got)
	}
}

// A project with no ledger whose first authored change is an ordinary status
// operation still gets a genesis recording the built-in three priorities —
// the lazy-seed counterpart to TestMintConfigLedgerRecordsBuiltInPriorities —
// and the pack it seeds with still carries a minimum version of 0: an
// unrelated status change must not be told it needs a newer reader merely
// because the genesis it triggers now always carries a priorities section.
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
	if got := root.Operation.MinReader; got != 0 {
		t.Fatalf("genesis pack MinReader = %d, want 0", got)
	}
}
