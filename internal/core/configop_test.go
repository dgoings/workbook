package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	configTestProjectID  = "01K0M6B8A4FTT8C39MXXYTW7C1"
	configTestGeneration = "01K0M6B8A4FTT8C39MXXYTW7D9"
)

var configTestNow = time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)

// configOperationID builds a canonical ULID from an index so a table can name
// as many operations as it likes without a fixture constant per operation.
func configOperationID(index int) string {
	return fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05d", index)
}

func configPack(clock uint64, operations ...ConfigOperation) ConfigOperationPack {
	return ConfigOperationPack{
		Format:            configOperationPackFormat,
		Version:           documentVersion,
		ProjectID:         configTestProjectID,
		HistoryGeneration: configTestGeneration,
		Actor:             Actor{ID: "developer@example.com"},
		LogicalClock:      clock,
		WallTime:          configTestNow,
		Operations:        operations,
	}
}

// identify assigns fresh operation IDs to a batch. Uniqueness only has to hold
// within a pack, so the seed keeps unrelated batches readable rather than
// correct.
func identify(seed int, operations []ConfigOperation) []ConfigOperation {
	assigned := make([]ConfigOperation, len(operations))
	for index, operation := range operations {
		operation.ID = configOperationID(seed + index)
		assigned[index] = operation
	}
	return assigned
}

// testVocabulary is a deliberately small three-status workflow: one default,
// one middle, one done. It makes an arity repair visible, which the six-status
// built-in would bury.
func testVocabulary(t *testing.T) Vocabulary {
	t.Helper()
	vocabulary, err := NewVocabulary([]StatusDefinition{
		{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
		{Status: "doing", Label: "Doing", Rank: "2/1", Tags: []StatusTag{}},
		{Status: "shipped", Label: "Shipped", Rank: "3/1", Tags: []StatusTag{StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

func genesisPack(vocabulary Vocabulary) ConfigOperationPack {
	config := ConfigData{Vocabulary: vocabulary.Document()}
	return configPack(1, identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &config}})...)
}

func genesisState(t *testing.T, vocabulary Vocabulary) ConfigStateDocument {
	t.Helper()
	state, err := ApplyConfig(nil, genesisPack(vocabulary))
	if err != nil {
		t.Fatalf("ApplyConfig(genesis) error = %v", err)
	}
	return state
}

// fold applies one pack per batch, advancing the logical clock the way a chain
// of ledger commits does.
func fold(t *testing.T, state ConfigStateDocument, batches ...[]ConfigOperation) ConfigStateDocument {
	t.Helper()
	for index, batch := range batches {
		pack := configPack(state.LogicalClock+1, identify(100*(index+1), batch)...)
		next, err := ApplyConfig(&state, pack)
		if err != nil {
			t.Fatalf("ApplyConfig(batch %d) error = %v", index, err)
		}
		state = next
	}
	return state
}

func statusNames(state ConfigStateDocument) []string {
	names := make([]string, 0, len(state.Config.Vocabulary.Statuses))
	for _, definition := range state.Config.Vocabulary.Statuses {
		names = append(names, string(definition.Status))
	}
	return names
}

func labelOf(t *testing.T, state ConfigStateDocument, status Status) string {
	t.Helper()
	for _, definition := range state.Config.Vocabulary.Statuses {
		if definition.Status == status {
			return definition.Label
		}
	}
	t.Fatalf("status %q is not live; live statuses are %v", status, statusNames(state))
	return ""
}

func add(name Status, label, rank string, tags ...StatusTag) ConfigOperation {
	if tags == nil {
		tags = []StatusTag{}
	}
	return ConfigOperation{Type: ConfigStatusAdd, Name: name, Label: label, Rank: rank, Tags: tags}
}

func rename(from, to Status) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusRename, From: from, To: to}
}

func relabel(status Status, label string) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusRelabel, Status: status, Label: label}
}

func remove(status, destination Status) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusRemove, Status: status, Destination: destination}
}

func reorder(status Status, rank string) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusReorder, Status: status, Rank: rank}
}

func tag(status Status, value StatusTag) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusTag, Status: status, Tag: value}
}

func untag(status Status, value StatusTag) ConfigOperation {
	return ConfigOperation{Type: ConfigStatusUntag, Status: status, Tag: value}
}

// ---------------------------------------------------------------------------
// Ledger shapes: root, linear, branched, merged.
// ---------------------------------------------------------------------------

func TestApplyConfigRootRequiresExactlyOneGenesis(t *testing.T) {
	vocabulary := testVocabulary(t)

	t.Run("genesis is a valid root", func(t *testing.T) {
		state := genesisState(t, vocabulary)
		if got, want := state.Format, configStateDocumentFormat; got != want {
			t.Fatalf("format = %q, want %q", got, want)
		}
		if got, want := state.LogicalClock, uint64(1); got != want {
			t.Fatalf("logical clock = %d, want %d", got, want)
		}
		if got, want := state.History.Generation, configTestGeneration; got != want {
			t.Fatalf("history generation = %q, want %q", got, want)
		}
		if got, want := state.Config.Vocabulary, vocabulary.Document(); !reflect.DeepEqual(got, want) {
			t.Fatalf("vocabulary = %#v, want %#v", got, want)
		}
	})

	t.Run("a non-genesis root is invalid", func(t *testing.T) {
		pack := configPack(1, identify(0, []ConfigOperation{add("triage", "Triage", "1/1")})...)
		if _, err := ApplyConfig(nil, pack); err == nil {
			t.Fatal("ApplyConfig() error = nil, want a rejection of a non-genesis root")
		} else if got := CategoryOf(err); got != CategoryCorruptData {
			t.Fatalf("ApplyConfig() category = %q, want %q", got, CategoryCorruptData)
		}
	})

	t.Run("genesis beside another operation is invalid", func(t *testing.T) {
		config := ConfigData{Vocabulary: vocabulary.Document()}
		pack := configPack(1, identify(0, []ConfigOperation{
			{Type: ConfigGenesis, Config: &config},
			add("triage", "Triage", "4/1"),
		})...)
		if _, err := ApplyConfig(nil, pack); err == nil {
			t.Fatal("ApplyConfig() error = nil, want a rejection")
		}
	})

	t.Run("genesis with a parent is invalid", func(t *testing.T) {
		state := genesisState(t, vocabulary)
		config := ConfigData{Vocabulary: vocabulary.Document()}
		pack := configPack(2, identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &config}})...)
		if _, err := ApplyConfig(&state, pack); err == nil {
			t.Fatal("ApplyConfig() error = nil, want a rejection of a second genesis")
		}
	})

	t.Run("a root clock other than one is invalid", func(t *testing.T) {
		pack := genesisPack(vocabulary)
		pack.LogicalClock = 2
		if _, err := ApplyConfig(nil, pack); err == nil {
			t.Fatal("ApplyConfig() error = nil, want a rejection")
		}
	})
}

func TestApplyConfigFoldsALinearHistory(t *testing.T) {
	state := fold(t, genesisState(t, testVocabulary(t)),
		[]ConfigOperation{add("triage", "Triage", "1/2")},
		[]ConfigOperation{relabel("triage", "Intake")},
		[]ConfigOperation{tag("doing", StatusTagNext)},
	)

	if got, want := statusNames(state), []string{"triage", "todo", "doing", "shipped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if got, want := labelOf(t, state, "triage"), "Intake"; got != want {
		t.Fatalf("triage label = %q, want %q", got, want)
	}
	if !state.Vocabulary().IsNext("doing") {
		t.Fatal("doing is not tagged next")
	}
	if got, want := state.LogicalClock, uint64(4); got != want {
		t.Fatalf("logical clock = %d, want %d", got, want)
	}
}

// Two packs applied to the same parent are two branches. Each is a valid
// checkpoint on its own; neither can see the other.
func TestApplyConfigFoldsBranchedHistories(t *testing.T) {
	base := genesisState(t, testVocabulary(t))
	ours := fold(t, base, []ConfigOperation{add("triage", "Triage", "1/2")})
	theirs := fold(t, base, []ConfigOperation{add("blocked", "Blocked", "5/2")})

	if got, want := statusNames(ours), []string{"triage", "todo", "doing", "shipped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("our branch = %v, want %v", got, want)
	}
	if got, want := statusNames(theirs), []string{"todo", "doing", "blocked", "shipped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("their branch = %v, want %v", got, want)
	}
	if ours.LogicalClock != theirs.LogicalClock {
		t.Fatalf("branch clocks = %d and %d, want them equal", ours.LogicalClock, theirs.LogicalClock)
	}
}

// A merge is a replay: the fetched branch becomes the parent and the local
// operations are applied on top of it. Reconciling in either direction reaches
// the same state, which is what a merge has to mean.
func TestApplyConfigMergesByReplayingOntoTheFetchedBranch(t *testing.T) {
	base := genesisState(t, testVocabulary(t))
	oursFirst := fold(t, base,
		[]ConfigOperation{add("triage", "Triage", "1/2")},
		[]ConfigOperation{add("blocked", "Blocked", "5/2")},
	)
	theirsFirst := fold(t, base,
		[]ConfigOperation{add("blocked", "Blocked", "5/2")},
		[]ConfigOperation{add("triage", "Triage", "1/2")},
	)

	if got, want := oursFirst.Config.Vocabulary, theirsFirst.Config.Vocabulary; !reflect.DeepEqual(got, want) {
		t.Fatalf("merge order changed the result:\n ours-first = %#v\ntheirs-first = %#v", got, want)
	}
	if got, want := statusNames(oursFirst), []string{"triage", "todo", "doing", "blocked", "shipped"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged statuses = %v, want %v", got, want)
	}
}

func TestApplyConfigRejectsAPackThatDoesNotAdvanceItsParent(t *testing.T) {
	state := genesisState(t, testVocabulary(t))

	tests := map[string]func(*ConfigOperationPack){
		"clock does not advance": func(pack *ConfigOperationPack) { pack.LogicalClock = 1 },
		"clock skips":            func(pack *ConfigOperationPack) { pack.LogicalClock = 3 },
		"foreign project":        func(pack *ConfigOperationPack) { pack.ProjectID = "01K0M6B8A4FTT8C39MXXYTW7C2" },
		"foreign generation": func(pack *ConfigOperationPack) {
			pack.HistoryGeneration = "01K0M6B8A4FTT8C39MXXYTW7D8"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pack := configPack(2, identify(0, []ConfigOperation{relabel("todo", "To Do")})...)
			mutate(&pack)
			if _, err := ApplyConfig(&state, pack); err == nil {
				t.Fatal("ApplyConfig() error = nil, want a rejection")
			} else if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("ApplyConfig() category = %q, want %q", got, CategoryCorruptData)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The concurrency matrix. Every case is folded in both orderings from the same
// base, because reconciliation may replay either side first.
// ---------------------------------------------------------------------------

type concurrencyCase struct {
	name  string
	setup []ConfigOperation
	ours  []ConfigOperation
	// theirs is the concurrent batch. It is applied after ours in one ordering
	// and before it in the other.
	theirs []ConfigOperation
	// converges says whether both orderings must reach the same observable
	// vocabulary. Where it is false the case is a genuine conflict: the fold is
	// still deterministic per ordering, and PR-B's reconcile classifies it so a
	// person decides, but no tiebreak in this package would be honest.
	converges bool
	// check runs against each ordering's result, named by which side applied
	// first, and asserts the facts that hold in that ordering.
	check func(t *testing.T, first string, state ConfigStateDocument)
}

func TestApplyConfigConcurrencyMatrix(t *testing.T) {
	tests := []concurrencyCase{
		{
			name:      "add/add different names: both survive",
			ours:      []ConfigOperation{add("triage", "Triage", "1/2")},
			theirs:    []ConfigOperation{add("blocked", "Blocked", "5/2")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"triage", "todo", "doing", "blocked", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:      "add/add identical: idempotent",
			ours:      []ConfigOperation{add("triage", "Triage", "1/2")},
			theirs:    []ConfigOperation{add("triage", "Triage", "1/2")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"triage", "todo", "doing", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:   "add/add same name differing label: the first applied wins",
			ours:   []ConfigOperation{add("triage", "Ours", "1/2")},
			theirs: []ConfigOperation{add("triage", "Theirs", "1/2")},
			// Not convergent by construction, and deliberately so. A tiebreak
			// on the label would be arbitrary; reconciliation replays the
			// fetched history first, so upstream's label is the one that
			// survives and the local one is reported as a definition conflict.
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := map[string]string{"ours": "Ours", "theirs": "Theirs"}[first]
				if got := labelOf(t, state, "triage"); got != want {
					t.Fatalf("%s first: triage label = %q, want %q", first, got, want)
				}
			},
		},
		{
			name:      "rename identical: idempotent",
			ours:      []ConfigOperation{rename("doing", "active")},
			theirs:    []ConfigOperation{rename("doing", "active")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "active", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
				if resolved, live := state.Vocabulary().Resolve("doing"); !live || resolved != "active" {
					t.Fatalf("%s first: Resolve(doing) = (%q, %t), want (active, true)", first, resolved, live)
				}
			},
		},
		{
			name:   "rename/rename same source, different targets",
			ours:   []ConfigOperation{rename("doing", "active")},
			theirs: []ConfigOperation{rename("doing", "wip")},
			// A conflict, not a convergence. Each ordering ends on the
			// last-applied target with the earlier one chained into it, which
			// is deterministic per ordering and enough for PR-B's classify to
			// see both intents.
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := Status(map[string]string{"ours": "wip", "theirs": "active"}[first])
				vocabulary := state.Vocabulary()
				if !vocabulary.Has(want) {
					t.Fatalf("%s first: statuses = %v, want %q live", first, statusNames(state), want)
				}
				// Whatever the winner, every earlier token still resolves to
				// it: no task is ever stranded by the disagreement.
				for _, stale := range []Status{"doing", "active", "wip"} {
					if stale == want {
						continue
					}
					if resolved, live := vocabulary.Resolve(stale); !live || resolved != want {
						t.Fatalf("%s first: Resolve(%q) = (%q, %t), want (%q, true)", first, stale, resolved, live, want)
					}
				}
			},
		},
		{
			name:   "upstream rename plus local remove",
			ours:   []ConfigOperation{remove("doing", "shipped")},
			theirs: []ConfigOperation{rename("doing", "active")},
			// The two directions are not the same question, which is why this
			// is one case with two different answers rather than a convergence.
			//
			// Replaying a local removal onto a fetched rename is the case the
			// design names: the removal resolves its subject through the rename
			// and retires the new name, so both tokens land on the
			// destination and nothing is stranded.
			//
			// The other direction — replaying a local rename onto a fetched
			// removal — cannot be made to work by any rule here. The status the
			// rename names is gone, and inventing it back would resurrect a
			// column the other clone deliberately removed. The rename is
			// dropped, and PR-B reports it as a status-retired conflict so the
			// person who wrote it decides whether to re-add the status.
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
				vocabulary := state.Vocabulary()
				if resolved, live := vocabulary.Resolve("doing"); !live || resolved != "shipped" {
					t.Fatalf("%s first: Resolve(doing) = (%q, %t), want (shipped, true)", first, resolved, live)
				}
				resolved, live := vocabulary.Resolve("active")
				if first == "theirs" && (!live || resolved != "shipped") {
					t.Fatalf("theirs first: Resolve(active) = (%q, %t), want (shipped, true)", resolved, live)
				}
				if first == "ours" && live {
					t.Fatalf("ours first: Resolve(active) = (%q, true), want the dropped rename to leave no token", resolved)
				}
			},
		},
		{
			name:      "remove/remove identical: idempotent",
			ours:      []ConfigOperation{remove("doing", "shipped")},
			theirs:    []ConfigOperation{remove("doing", "shipped")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:   "remove/remove different destinations",
			setup:  []ConfigOperation{add("triage", "Triage", "1/2")},
			ours:   []ConfigOperation{remove("doing", "shipped")},
			theirs: []ConfigOperation{remove("doing", "triage")},
			// A conflict: the first removal decides where the tasks go and the
			// second is a no-op, because its subject is already retired and a
			// removal does not chase a retirement. Nothing is stranded and no
			// innocent status is touched, but the two sides disagree about the
			// destination and only a person can settle it. PR-B classifies it.
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := Status(map[string]string{"ours": "shipped", "theirs": "triage"}[first])
				if resolved, live := state.Vocabulary().Resolve("doing"); !live || resolved != want {
					t.Fatalf("%s first: Resolve(doing) = (%q, %t), want (%q, true)", first, resolved, live, want)
				}
				if !state.Vocabulary().Has("shipped") || !state.Vocabulary().Has("triage") {
					t.Fatalf("%s first: statuses = %v, want both destinations still live", first, statusNames(state))
				}
			},
		},
		{
			name:      "reorder/reorder different statuses: literal ranks converge",
			ours:      []ConfigOperation{reorder("shipped", "1/2")},
			theirs:    []ConfigOperation{reorder("todo", "5/1")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"shipped", "doing", "todo"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:      "reorder/reorder onto the same rank: ties break by name",
			ours:      []ConfigOperation{reorder("shipped", "7/2")},
			theirs:    []ConfigOperation{reorder("doing", "7/2")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "doing", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:      "concurrent inserts into the same gap",
			ours:      []ConfigOperation{add("triage", "Triage", "3/2")},
			theirs:    []ConfigOperation{add("review", "Review", "5/2")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "triage", "doing", "review", "shipped"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:      "tag/tag default: exactly one default in either order",
			ours:      []ConfigOperation{tag("doing", StatusTagDefault)},
			theirs:    []ConfigOperation{tag("shipped", StatusTagDefault)},
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := Status(map[string]string{"ours": "shipped", "theirs": "doing"}[first])
				vocabulary := state.Vocabulary()
				if got := vocabulary.Default(); got != want {
					t.Fatalf("%s first: Default() = %q, want %q", first, got, want)
				}
				if got := countTagged(state, StatusTagDefault); got != 1 {
					t.Fatalf("%s first: %d statuses tagged default, want exactly 1", first, got)
				}
			},
		},
		{
			name:      "tag/untag of a role with slack converges",
			setup:     []ConfigOperation{tag("doing", StatusTagNext)},
			ours:      []ConfigOperation{tag("shipped", StatusTagNext)},
			theirs:    []ConfigOperation{untag("todo", StatusTagNext)},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"doing", "shipped"}
				if got := taggedNames(state, StatusTagNext); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: next tags = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:   "tag/untag of the last holder of a role",
			ours:   []ConfigOperation{tag("doing", StatusTagNext)},
			theirs: []ConfigOperation{untag("todo", StatusTagNext)},
			// Arity repair is not commutative, and this is the case that shows
			// it. Untagging first empties the role, so normalization puts it
			// back on the default before the other side's tag arrives and both
			// statuses end up tagged; tagging first leaves slack, so the untag
			// is an ordinary removal. Both results are usable and neither is
			// wrong — which is exactly a status-arity conflict for PR-B to
			// report, because the repair chose a status by position and nobody
			// asked for it.
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := map[string][]string{
					"ours":   {"doing"},
					"theirs": {"todo", "doing"},
				}[first]
				if got := taggedNames(state, StatusTagNext); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: next tags = %v, want %v", first, got, want)
				}
			},
		},
		{
			name:      "remove of a status another side already renamed",
			ours:      []ConfigOperation{rename("shipped", "released")},
			theirs:    []ConfigOperation{remove("doing", "shipped")},
			converges: true,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := []string{"todo", "released"}
				if got := statusNames(state); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s first: statuses = %v, want %v", first, got, want)
				}
				if resolved, live := state.Vocabulary().Resolve("doing"); !live || resolved != "released" {
					t.Fatalf("%s first: Resolve(doing) = (%q, %t), want (released, true)", first, resolved, live)
				}
			},
		},
		{
			name:      "relabel/relabel: the last applied wins, silently",
			ours:      []ConfigOperation{relabel("doing", "Ours")},
			theirs:    []ConfigOperation{relabel("doing", "Theirs")},
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				want := map[string]string{"ours": "Theirs", "theirs": "Ours"}[first]
				if got := labelOf(t, state, "doing"); got != want {
					t.Fatalf("%s first: doing label = %q, want %q", first, got, want)
				}
			},
		},
		{
			name:      "add/rename onto the same name: the loser is a no-op",
			ours:      []ConfigOperation{add("active", "Active", "5/2")},
			theirs:    []ConfigOperation{rename("doing", "active")},
			converges: false,
			check: func(t *testing.T, first string, state ConfigStateDocument) {
				vocabulary := state.Vocabulary()
				if !vocabulary.Has("active") {
					t.Fatalf("%s first: statuses = %v, want active live", first, statusNames(state))
				}
				// The rename only takes the name when it is still free; when
				// the add got there first, "doing" is untouched and nothing is
				// stranded.
				if first == "ours" && !vocabulary.Has("doing") {
					t.Fatalf("ours first: doing was renamed onto a taken name")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := genesisState(t, testVocabulary(t))
			if len(test.setup) > 0 {
				base = fold(t, base, test.setup)
			}
			oursFirst := fold(t, base, test.ours, test.theirs)
			theirsFirst := fold(t, base, test.theirs, test.ours)

			test.check(t, "ours", oursFirst)
			test.check(t, "theirs", theirsFirst)

			converged := observablyEqual(oursFirst, theirsFirst)
			if converged != test.converges {
				t.Fatalf(
					"orderings converged = %t, want %t\n ours first: %#v\ntheirs first: %#v",
					converged, test.converges, oursFirst.Config.Vocabulary, theirsFirst.Config.Vocabulary,
				)
			}
		})
	}
}

// observablyEqual is the convergence predicate, and it is deliberately not byte
// equality of the two checkpoints.
//
// Two clones that applied the same operations in different orders have
// different ledger histories, and their bookkeeping differs accordingly: one
// records "doing was retired into released", the other "doing was retired into
// shipped, and shipped was later renamed to released". Nothing a user or a
// command can observe distinguishes them — the same statuses are live, with the
// same labels, ranks and roles, and every stored token resolves to the same
// place. Demanding identical bytes would fail cases that have genuinely
// converged, and would say nothing extra about the ones that have not.
//
// Byte equality still matters where it is the actual claim: a clone re-applying
// one history must reproduce one checkpoint, which is what
// ValidateConfigCheckpoint asserts.
func observablyEqual(left, right ConfigStateDocument) bool {
	if !reflect.DeepEqual(left.Config.Vocabulary.Statuses, right.Config.Vocabulary.Statuses) {
		return false
	}
	leftVocabulary, rightVocabulary := left.Vocabulary(), right.Vocabulary()
	for _, token := range mentionedStatuses(left, right) {
		leftResolved, leftLive := leftVocabulary.Resolve(token)
		rightResolved, rightLive := rightVocabulary.Resolve(token)
		if leftResolved != rightResolved || leftLive != rightLive {
			return false
		}
	}
	return true
}

// mentionedStatuses collects every status token either document names, which is
// the set a stored task could plausibly be carrying.
func mentionedStatuses(states ...ConfigStateDocument) []Status {
	seen := map[Status]struct{}{}
	ordered := []Status{}
	note := func(status Status) {
		if _, already := seen[status]; already {
			return
		}
		seen[status] = struct{}{}
		ordered = append(ordered, status)
	}
	for _, state := range states {
		for _, definition := range state.Config.Vocabulary.Statuses {
			note(definition.Status)
		}
		for _, alias := range state.Config.Vocabulary.Aliases {
			note(alias.From)
			note(alias.To)
		}
		for _, entry := range state.Config.Vocabulary.Retired {
			note(entry.Status)
			note(entry.Destination)
		}
	}
	return ordered
}

func countTagged(state ConfigStateDocument, wanted StatusTag) int {
	return len(taggedNames(state, wanted))
}

func taggedNames(state ConfigStateDocument, wanted StatusTag) []string {
	names := []string{}
	for _, definition := range state.Config.Vocabulary.Statuses {
		if definition.HasTag(wanted) {
			names = append(names, string(definition.Status))
		}
	}
	return names
}

// ---------------------------------------------------------------------------
// Individual semantics the matrix assumes.
// ---------------------------------------------------------------------------

// Moving the default is one operation, not an untag and a tag, so there is no
// intermediate state in which the project has no default for a concurrent clone
// to fetch and normalize.
func TestApplyConfigTransfersTheDefaultTagAtomically(t *testing.T) {
	state := fold(t, genesisState(t, testVocabulary(t)), []ConfigOperation{tag("doing", StatusTagDefault)})

	if got, want := state.Vocabulary().Default(), Status("doing"); got != want {
		t.Fatalf("Default() = %q, want %q", got, want)
	}
	if got, want := taggedNames(state, StatusTagDefault), []string{"doing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default tags = %v, want %v", got, want)
	}
}

func TestApplyConfigResolvesRetirementChainsTransitively(t *testing.T) {
	state := fold(t, genesisState(t, testVocabulary(t)),
		[]ConfigOperation{add("review", "Review", "5/2")},
		[]ConfigOperation{remove("doing", "review")},
		[]ConfigOperation{remove("review", "shipped")},
	)

	vocabulary := state.Vocabulary()
	for _, stale := range []Status{"doing", "review"} {
		if resolved, live := vocabulary.Resolve(stale); !live || resolved != "shipped" {
			t.Fatalf("Resolve(%q) = (%q, %t), want (shipped, true)", stale, resolved, live)
		}
	}
}

// A retirement cycle is refused by the shape of the fold rather than by a
// check, so the checkpoint decoder can never meet one. This asserts the
// property from the outside: retiring back into an already-retired status
// resolves forward instead of looping.
func TestApplyConfigCannotBuildARetirementCycle(t *testing.T) {
	state := fold(t, genesisState(t, testVocabulary(t)),
		[]ConfigOperation{remove("doing", "shipped")},
		// "doing" is retired now, so this asks to retire "shipped" into a name
		// that forwards straight back to it.
		[]ConfigOperation{remove("shipped", "doing")},
	)

	vocabulary := state.Vocabulary()
	if resolved, live := vocabulary.Resolve("doing"); !live {
		t.Fatalf("Resolve(doing) = (%q, %t), want a live status", resolved, live)
	}
	if resolved, live := vocabulary.Resolve("shipped"); !live {
		t.Fatalf("Resolve(shipped) = (%q, %t), want a live status", resolved, live)
	}
	if _, err := DecodeConfigStateDocument(mustEncode(t, state)); err != nil {
		t.Fatalf("DecodeConfigStateDocument() error = %v, want a decodable checkpoint", err)
	}
}

func TestApplyConfigRefusesToRemoveTheLastLiveStatus(t *testing.T) {
	state := fold(t, genesisState(t, testVocabulary(t)),
		[]ConfigOperation{remove("doing", "todo")},
		[]ConfigOperation{remove("shipped", "todo")},
		[]ConfigOperation{remove("todo", "shipped")},
	)

	if got, want := statusNames(state), []string{"todo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	// The survivor has to carry every role, or the project would have a column
	// nothing can be created in and a dependency nothing can satisfy.
	vocabulary := state.Vocabulary()
	if vocabulary.Default() != "todo" || !vocabulary.IsNext("todo") || !vocabulary.IsDone("todo") {
		t.Fatalf("sole status roles = %v, want default, next and done", state.Config.Vocabulary.Statuses[0].Tags)
	}
	if err := vocabulary.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// Apply repairs arity; the authoring gate refuses it. This is the pair that
// makes both true at once.
func TestConfigArityIsRefusedWhenAuthoredAndRepairedWhenReplayed(t *testing.T) {
	tests := map[string]struct {
		operations  []ConfigOperation
		wantCommand string
		check       func(t *testing.T, state ConfigStateDocument)
	}{
		"untagging the last done": {
			operations:  []ConfigOperation{untag("shipped", StatusTagDone)},
			wantCommand: "workbook status tag <status> --tag done",
			check: func(t *testing.T, state ConfigStateDocument) {
				// Repaired onto the highest-ranked status, which is the one it
				// was just taken from.
				if got, want := taggedNames(state, StatusTagDone), []string{"shipped"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("done tags = %v, want %v", got, want)
				}
			},
		},
		"untagging the last next": {
			operations:  []ConfigOperation{untag("todo", StatusTagNext)},
			wantCommand: "workbook status tag <status> --tag next",
			check: func(t *testing.T, state ConfigStateDocument) {
				// Repaired onto the default, which is the one status guaranteed
				// to hold tasks.
				if got, want := taggedNames(state, StatusTagNext), []string{"todo"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("next tags = %v, want %v", got, want)
				}
			},
		},
		"untagging the default": {
			operations:  []ConfigOperation{untag("todo", StatusTagDefault)},
			wantCommand: "workbook status tag <status> --tag default",
			check: func(t *testing.T, state ConfigStateDocument) {
				// Repaired onto the lowest-ranked status.
				if got, want := state.Vocabulary().Default(), Status("todo"); got != want {
					t.Fatalf("Default() = %q, want %q", got, want)
				}
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			parent := genesisState(t, testVocabulary(t))
			pack := configPack(2, identify(0, test.operations)...)

			err := ValidateConfigAuthoring(&parent, pack)
			if err == nil {
				t.Fatal("ValidateConfigAuthoring() error = nil, want a refusal")
			}
			if got := CategoryOf(err); got != CategoryValidation {
				t.Fatalf("ValidateConfigAuthoring() category = %q, want %q", got, CategoryValidation)
			}
			if !strings.Contains(err.Error(), test.wantCommand) {
				t.Fatalf("ValidateConfigAuthoring() = %q, want it to name %q", err, test.wantCommand)
			}

			state, err := ApplyConfig(&parent, pack)
			if err != nil {
				t.Fatalf("ApplyConfig() error = %v, want the same pack to fold", err)
			}
			if err := state.Vocabulary().Validate(); err != nil {
				t.Fatalf("ApplyConfig() left an unusable vocabulary: %v", err)
			}
			test.check(t, state)
		})
	}
}

// The property behind the pair above: over every reachable single-operation
// pack, ApplyConfig succeeds and its output satisfies the arity invariants.
func TestApplyConfigNeverFailsOnArity(t *testing.T) {
	base := fold(t, genesisState(t, testVocabulary(t)), []ConfigOperation{add("review", "Review", "5/2")})
	statuses := []Status{"todo", "doing", "review", "shipped", "gone"}

	operations := []ConfigOperation{}
	for _, status := range statuses {
		for _, role := range []StatusTag{StatusTagDefault, StatusTagNext, StatusTagDone} {
			operations = append(operations, tag(status, role), untag(status, role))
		}
		for _, other := range statuses {
			if status != other {
				operations = append(operations, remove(status, other), rename(status, other))
			}
		}
		operations = append(operations,
			relabel(status, "Renamed"),
			reorder(status, "9/2"),
			add(status, "Added", "9/1"),
		)
	}

	for index, operation := range operations {
		pack := configPack(base.LogicalClock+1, identify(0, []ConfigOperation{operation})...)
		state, err := ApplyConfig(&base, pack)
		if err != nil {
			t.Fatalf("ApplyConfig(%d: %s on %q) error = %v", index, operation.Type, operationSubject(operation), err)
		}
		if err := state.Vocabulary().Validate(); err != nil {
			t.Fatalf("ApplyConfig(%d: %s on %q) left an unusable vocabulary: %v", index, operation.Type, operationSubject(operation), err)
		}
		// Idempotence: replaying the same operation on the result changes
		// nothing further, which is what makes a duplicated pack delivery safe.
		replayed := fold(t, state, []ConfigOperation{operation})
		if !reflect.DeepEqual(state.Config.Vocabulary, replayed.Config.Vocabulary) {
			t.Fatalf(
				"replaying %s on %q changed the vocabulary:\nfirst = %#v\nagain = %#v",
				operation.Type, operationSubject(operation), state.Config.Vocabulary, replayed.Config.Vocabulary,
			)
		}
	}
}

func operationSubject(operation ConfigOperation) Status {
	switch {
	case operation.Name != "":
		return operation.Name
	case operation.From != "":
		return operation.From
	default:
		return operation.Status
	}
}

// Duplicate delivery is the ordinary case, not an exception: a retried push and
// a re-fetched ref both hand the same pack over twice.
func TestApplyConfigIsIdempotentForADuplicatedPack(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	pack := configPack(2, identify(0, []ConfigOperation{add("triage", "Triage", "1/2")})...)

	first, err := ApplyConfig(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	again, err := ApplyConfig(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() second delivery error = %v", err)
	}
	if !reflect.DeepEqual(first, again) {
		t.Fatalf("second delivery = %#v, want %#v", again, first)
	}
}

// ---------------------------------------------------------------------------
// Documents.
// ---------------------------------------------------------------------------

func mustEncode(t *testing.T, document any) []byte {
	t.Helper()
	encoded, err := EncodeDocument(document)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	return encoded
}

func TestConfigDocumentsRoundTripThroughCanonicalBytes(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	pack := configPack(2, identify(0, []ConfigOperation{
		add("triage", "Triage", "1/2", StatusTagNext),
		rename("doing", "active"),
	})...)
	state, err := ApplyConfig(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}

	encodedPack := mustEncode(t, pack)
	decodedPack, err := DecodeConfigOperationPack(encodedPack)
	if err != nil {
		t.Fatalf("DecodeConfigOperationPack() error = %v", err)
	}
	if !bytes.Equal(mustEncode(t, decodedPack), encodedPack) {
		t.Fatalf("pack did not round-trip to the same bytes")
	}

	encodedState := mustEncode(t, state)
	decodedState, err := DecodeConfigStateDocument(encodedState)
	if err != nil {
		t.Fatalf("DecodeConfigStateDocument() error = %v", err)
	}
	if !bytes.Equal(mustEncode(t, decodedState), encodedState) {
		t.Fatalf("checkpoint did not round-trip to the same bytes")
	}
	if encodedState[len(encodedState)-1] != '\n' || bytes.Count(encodedState, []byte{'\n'}) != 1 {
		t.Fatalf("checkpoint must be one canonical JSON line, got %q", encodedState)
	}
}

// The checkpoint's arrays are sorted here rather than by the encoder, so the
// bytes are a property of the configuration. This pins the shape a reader will
// see in a `git show`.
func TestConfigCheckpointBytesAreCanonical(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	state := fold(t, parent, []ConfigOperation{rename("doing", "active"), remove("shipped", "todo")})

	want := `{"format":"workbook.config-state","version":1,"projectId":"01K0M6B8A4FTT8C39MXXYTW7C1",` +
		`"history":{"generation":"01K0M6B8A4FTT8C39MXXYTW7D9","compactedFrom":null},"logicalClock":2,` +
		`"config":{"vocabulary":{` +
		`"statuses":[` +
		`{"status":"todo","label":"Todo","rank":"1/1","tags":["default","next"]},` +
		// "shipped" carried the done tag and was removed in the same pack, so
		// normalization put it on the highest-ranked survivor.
		`{"status":"active","label":"Doing","rank":"2/1","tags":["done"]}],` +
		`"aliases":[{"from":"doing","to":"active"}],` +
		`"retired":[{"status":"shipped","destination":"todo"}]}}}` + "\n"
	if got := string(mustEncode(t, state)); got != want {
		t.Fatalf("checkpoint bytes =\n%s\nwant\n%s", got, want)
	}
}

func TestValidateConfigCheckpointComparesBytes(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	pack := configPack(2, identify(0, []ConfigOperation{relabel("doing", "Active")})...)
	stored, err := ApplyConfig(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}

	if err := ValidateConfigCheckpoint(&parent, pack, stored); err != nil {
		t.Fatalf("ValidateConfigCheckpoint() error = %v", err)
	}

	tampered := stored
	tampered.Config.Vocabulary = cloneVocabularyDocument(stored.Config.Vocabulary)
	tampered.Config.Vocabulary.Statuses[1].Label = "Something Else"
	if err := ValidateConfigCheckpoint(&parent, pack, tampered); err == nil {
		t.Fatal("ValidateConfigCheckpoint() error = nil, want a corrupt-data failure")
	} else if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("ValidateConfigCheckpoint() category = %q, want %q", got, CategoryCorruptData)
	}
}

func cloneVocabularyDocument(document VocabularyDocument) VocabularyDocument {
	clone := VocabularyDocument{
		Statuses: append([]StatusDefinition(nil), document.Statuses...),
		Aliases:  append([]StatusAlias(nil), document.Aliases...),
		Retired:  append([]RetiredStatus(nil), document.Retired...),
	}
	for index := range clone.Statuses {
		clone.Statuses[index].Tags = copyStatusTags(clone.Statuses[index].Tags)
	}
	return clone
}

func TestDecodeConfigDocumentsRejectMalformedInput(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	valid := string(mustEncode(t, parent))

	t.Run("checkpoint", func(t *testing.T) {
		tests := map[string]string{
			"malformed":      `{"format":`,
			"foreign format": strings.Replace(valid, "workbook.config-state", "workbook.task-state", 1),
			"future version": strings.Replace(valid, `"version":1`, `"version":2`, 1),
			"unknown field":  strings.Replace(valid, `"logicalClock":1`, `"logicalClock":1,"autoSync":true`, 1),
			"zero clock":     strings.Replace(valid, `"logicalClock":1`, `"logicalClock":0`, 1),
			"unsorted statuses": strings.Replace(
				valid,
				`"statuses":[{"status":"todo"`,
				`"statuses":[{"status":"zzz","label":"Z","rank":"9/1","tags":[]},{"status":"todo"`,
				1,
			),
			"null tags":  strings.Replace(valid, `"tags":[]`, `"tags":null`, 1),
			"two values": valid + "{}",
		}
		for name, contents := range tests {
			t.Run(name, func(t *testing.T) {
				if _, err := DecodeConfigStateDocument([]byte(contents)); err == nil {
					t.Fatalf("DecodeConfigStateDocument(%q) error = nil, want a corrupt-data failure", contents)
				} else if got := CategoryOf(err); got != CategoryCorruptData {
					t.Fatalf("DecodeConfigStateDocument() category = %q, want %q", got, CategoryCorruptData)
				}
			})
		}
	})

	t.Run("pack", func(t *testing.T) {
		tests := map[string]func(*ConfigOperationPack){
			"foreign format":     func(pack *ConfigOperationPack) { pack.Format = operationPackFormat },
			"future version":     func(pack *ConfigOperationPack) { pack.Version = 2 },
			"invalid project ID": func(pack *ConfigOperationPack) { pack.ProjectID = "not-a-ulid" },
			"blank actor":        func(pack *ConfigOperationPack) { pack.Actor = Actor{ID: "  "} },
			"zero clock":         func(pack *ConfigOperationPack) { pack.LogicalClock = 0 },
			"zero wall time":     func(pack *ConfigOperationPack) { pack.WallTime = time.Time{} },
			"no operations":      func(pack *ConfigOperationPack) { pack.Operations = nil },
			"duplicate operation ID": func(pack *ConfigOperationPack) {
				pack.Operations = append(pack.Operations, pack.Operations[0])
			},
			"invalid operation ID": func(pack *ConfigOperationPack) { pack.Operations[0].ID = "not-a-ulid" },
			"unknown type":         func(pack *ConfigOperationPack) { pack.Operations[0].Type = "status.frobnicate" },
			"extra member":         func(pack *ConfigOperationPack) { pack.Operations[0].Destination = "shipped" },
			"missing member":       func(pack *ConfigOperationPack) { pack.Operations[0].Label = "" },
			"invalid status token": func(pack *ConfigOperationPack) { pack.Operations[0].Name = "Not A Token" },
			"invalid label":        func(pack *ConfigOperationPack) { pack.Operations[0].Label = " " },
			"invalid rank":         func(pack *ConfigOperationPack) { pack.Operations[0].Rank = "2/2" },
			"unknown tag":          func(pack *ConfigOperationPack) { pack.Operations[0].Tags = []StatusTag{"urgent"} },
		}
		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				pack := configPack(2, identify(0, []ConfigOperation{add("triage", "Triage", "1/2")})...)
				mutate(&pack)
				encoded, err := json.Marshal(pack)
				if err != nil {
					t.Fatalf("json.Marshal() error = %v", err)
				}
				if _, err := DecodeConfigOperationPack(encoded); err == nil {
					t.Fatalf("DecodeConfigOperationPack() error = nil, want a corrupt-data failure")
				} else if got := CategoryOf(err); got != CategoryCorruptData {
					t.Fatalf("DecodeConfigOperationPack() category = %q, want %q", got, CategoryCorruptData)
				}
			})
		}
	})
}

// A genesis carries the whole vocabulary because the built-in default changes
// between releases. A ledger whose root only said "the defaults" would mean
// something different in every build that read it.
func TestGenesisCarriesTheWholeVocabulary(t *testing.T) {
	shipped, err := NewVocabulary([]StatusDefinition{
		{Status: "queued", Label: "Queued", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
		{Status: "released", Label: "Released", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}

	state := genesisState(t, shipped)
	if got, want := state.Config.Vocabulary, shipped.Document(); !reflect.DeepEqual(got, want) {
		t.Fatalf("genesis vocabulary = %#v, want %#v", got, want)
	}
	// Nothing about the built-in default leaked into the fold.
	if state.Vocabulary().Has(StatusBacklog) {
		t.Fatal("the fold substituted a built-in status the genesis did not carry")
	}
}

func TestGenesisRejectsANonCanonicalVocabulary(t *testing.T) {
	config := ConfigData{Vocabulary: VocabularyDocument{
		Statuses: []StatusDefinition{
			{Status: "shipped", Label: "Shipped", Rank: "2/1", Tags: []StatusTag{StatusTagDone}},
			{Status: "todo", Label: "Todo", Rank: "1/1", Tags: []StatusTag{StatusTagDefault, StatusTagNext}},
		},
		Aliases: []StatusAlias{},
		Retired: []RetiredStatus{},
	}}
	pack := configPack(1, identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &config}})...)

	if _, err := ApplyConfig(nil, pack); err == nil {
		t.Fatal("ApplyConfig() error = nil, want a rejection of an unsorted genesis")
	} else if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("ApplyConfig() category = %q, want %q", got, CategoryCorruptData)
	}
}

// Seeding a pre-ledger project reads LegacyVocabulary, minting a new one reads
// DefaultVocabulary, and both are genesis packs this fold accepts. Wiring the
// lazy seed itself — first status mutation seeds, setup seeds on mint — is
// PR-B/C; what has to hold here is that either vocabulary makes a valid root.
func TestGenesisAcceptsBothTheLegacyAndDefaultVocabularies(t *testing.T) {
	for name, vocabulary := range map[string]Vocabulary{
		"legacy":  LegacyVocabulary(),
		"default": DefaultVocabulary(),
	} {
		t.Run(name, func(t *testing.T) {
			state := genesisState(t, vocabulary)
			if got, want := state.Config.Vocabulary, vocabulary.Document(); !reflect.DeepEqual(got, want) {
				t.Fatalf("genesis vocabulary = %#v, want %#v", got, want)
			}
			if err := state.Vocabulary().Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}
