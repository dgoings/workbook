package core

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The three key operations and their fold.
// ---------------------------------------------------------------------------

func keyAdd(key string) ConfigOperation {
	return ConfigOperation{Type: ConfigKeyAdd, Key: key}
}

func keyCurrent(key string) ConfigOperation {
	return ConfigOperation{Type: ConfigKeyCurrent, Key: key}
}

func keyRetire(key string) ConfigOperation {
	return ConfigOperation{Type: ConfigKeyRetire, Key: key}
}

// keyPack builds a one-batch configuration pack whose clock advances the given
// parent by exactly one, playing priorityPack's role for the key section.
func keyPack(t *testing.T, parent ConfigStateDocument, operations ...ConfigOperation) ConfigOperationPack {
	t.Helper()
	return configPack(parent.LogicalClock+1, identify(0, operations)...)
}

// The founding key is not in the ledger until somebody records it, so the pack
// that adds a project's second key prepends the first — and the prepend has to
// land first in add order, because add order is what every later reader shows a
// person, and because the first key recorded is the current one until a
// key.current says otherwise.
func TestKeyAddRecordsTheFoundingKeyFirstWhenTheAuthorPrependsIt(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	pack := keyPack(t, parent, keyAdd("WB"), keyAdd("NEW"))

	state, err := ApplyConfig(&parent, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	keys := state.KeySet("WB")
	if got := keys.Keys(); len(got) != 2 || got[0].Key != "WB" || got[1].Key != "NEW" {
		t.Fatalf("Keys() = %#v, want WB then NEW", got)
	}
	if got := keys.Current(); got != "WB" {
		t.Fatalf("Current() = %q, want WB: the first key added is current until key.current says otherwise", got)
	}
}

// A retired key comes back where it was rather than at the end. Add order is
// the order a key was first added in, and a key that has minted tasks was added
// when it was added — moving it would renumber a person's mental list of their
// own project's history.
func TestKeyAddReactivatesARetiredKeyInPlace(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	state := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW"), keyAdd("THIRD")})
	state = fold(t, state, []ConfigOperation{keyRetire("NEW")})
	if got, want := KeyNameList(state.KeySet("WB")), "WB, NEW (retired), THIRD"; got != want {
		t.Fatalf("after retiring, keys = %q, want %q", got, want)
	}

	state = fold(t, state, []ConfigOperation{keyAdd("NEW")})
	if got, want := KeyNameList(state.KeySet("WB")), "WB, NEW, THIRD"; got != want {
		t.Fatalf("after re-adding, keys = %q, want %q", got, want)
	}
	if got := state.KeySet("WB").Current(); got != "WB" {
		t.Fatalf("Current() = %q, want WB: re-activating a key does not make it current", got)
	}
}

// Adding a key the project already has active changes nothing at all, which is
// what makes a redelivered pack a no-op and two clones that both add NEW
// converge on one key rather than on an error.
func TestKeyAddOnAnActiveKeyIsANoOp(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	once := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW")})
	twice := fold(t, once, []ConfigOperation{keyAdd("NEW")})

	if got, want := KeyNameList(twice.KeySet("WB")), KeyNameList(once.KeySet("WB")); got != want {
		t.Fatalf("replaying an add changed the keys to %q, want %q", got, want)
	}
	if got := twice.KeySet("WB").Current(); got != "WB" {
		t.Fatalf("Current() = %q, want WB: re-adding a key does not move the current key", got)
	}
}

// key.current is one operation rather than a tag-and-untag pair, so a replay
// that delivers it twice lands on the same state as a replay that delivers it
// once — there is no intermediate state with no current key for a concurrent
// clone to fetch.
func TestKeyCurrentMovesTheCurrentKeyAndIsIdempotent(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	seeded := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW")})

	moved := fold(t, seeded, []ConfigOperation{keyCurrent("NEW")})
	if got := moved.KeySet("WB").Current(); got != "NEW" {
		t.Fatalf("Current() = %q, want NEW", got)
	}
	again := fold(t, moved, []ConfigOperation{keyCurrent("NEW")})
	if got := again.KeySet("WB").Current(); got != "NEW" {
		t.Fatalf("Current() = %q after a redelivered key.current, want NEW", got)
	}
	if got, want := KeyNameList(again.KeySet("WB")), "WB, NEW"; got != want {
		t.Fatalf("keys = %q, want %q: key.current adds nothing", got, want)
	}
}

// Minting under a retired key, or under a key this project does not have, is
// not a state the fold may produce. Both are silent no-ops here rather than
// failures, because a pack reaching the fold has already happened somewhere;
// the author is refused in words by the planners in internal/cli.
func TestKeyCurrentOnARetiredOrUnknownKeyIsANoOp(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	seeded := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW"), keyRetire("NEW")})
	if got, want := KeyNameList(seeded.KeySet("WB")), "WB, NEW (retired)"; got != want {
		t.Fatalf("keys = %q, want %q", got, want)
	}

	for name, operation := range map[string]ConfigOperation{
		"retired": keyCurrent("NEW"),
		"unknown": keyCurrent("OTHER"),
	} {
		t.Run(name, func(t *testing.T) {
			state := fold(t, seeded, []ConfigOperation{operation})
			if got := state.KeySet("WB").Current(); got != "WB" {
				t.Fatalf("Current() = %q, want WB: key.current on a %s key is a no-op", got, name)
			}
		})
	}
}

// The two retirements that would leave a project unable to mint anything are
// refused, and a retirement of a key the project does not have is nothing to
// refuse. The last-active case cannot be reached through a ledger — the current
// key is always active, so the last active key is always the current one — so
// it is asked of the section directly, which is where the guard has to hold if
// a hand-built genesis ever presents that shape.
func TestKeyRetireRefusesTheCurrentKeyAndTheLastActiveKeyInTheFold(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	seeded := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW")})

	for name, operation := range map[string]ConfigOperation{
		"current": keyRetire("WB"),
		"unknown": keyRetire("OTHER"),
	} {
		t.Run(name, func(t *testing.T) {
			state := fold(t, seeded, []ConfigOperation{operation})
			if got, want := KeyNameList(state.KeySet("WB")), "WB, NEW"; got != want {
				t.Fatalf("keys = %q, want %q: retiring the %s key is a no-op", got, want, name)
			}
		})
	}

	retired := fold(t, seeded, []ConfigOperation{keyRetire("NEW")})
	if got, want := KeyNameList(retired.KeySet("WB")), "WB, NEW (retired)"; got != want {
		t.Fatalf("keys = %q, want %q: a key that is neither current nor the last active one retires", got, want)
	}

	last := &configKeys{order: []string{"WB"}, retired: map[string]bool{"WB": false}}
	last.applyRetire("WB")
	if last.retired["WB"] {
		t.Fatal("applyRetire retired the last active key, leaving the project unable to mint anything")
	}
}

// Every key operation raises the reader bar, and this build claims it can fold
// that bar. The two move in the same commit: a pack this build would refuse to
// fold is a pack it must not write.
func TestKeyOperationsCarryGenerationFour(t *testing.T) {
	for _, operationType := range []ConfigOperationType{ConfigKeyAdd, ConfigKeyCurrent, ConfigKeyRetire} {
		if got := ConfigPackMinReader([]ConfigOperation{{Type: operationType, Key: "NEW"}}); got != 4 {
			t.Errorf("ConfigPackMinReader(%s) = %d, want 4", operationType, got)
		}
	}
	if SupportedFormatGeneration < 4 {
		t.Fatalf("SupportedFormatGeneration = %d, want at least 4: this build folds key operations", SupportedFormatGeneration)
	}
}

// The compatibility guarantee. Nothing seeds the key section, so a project this
// build creates records no keys, encodes no `keys` member, and stays readable by
// every generation-3 clone on the team. A genesis that recorded the founding key
// here would push every new project to generation 4 for a fact its identity ref
// already states.
func TestAGenesisWithoutKeysKeepsItsExactBytes(t *testing.T) {
	priorities := BuiltInPriorityVocabulary().Document()
	config := ConfigData{Vocabulary: testVocabulary(t).Document(), Priorities: &priorities}
	genesisOperations := identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &config}})

	state, err := ApplyConfig(nil, configPack(1, genesisOperations...))
	if err != nil {
		t.Fatalf("ApplyConfig(genesis) error = %v", err)
	}
	if state.Config.Keys != nil {
		t.Fatalf("Config.Keys = %#v, want nil: nothing seeds the key section", state.Config.Keys)
	}
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"keys"`) {
		t.Fatalf("a checkpoint with no key operations encoded %s, want no keys member", encoded)
	}
	if got := ConfigPackMinReader(genesisOperations); got != 3 {
		t.Fatalf("genesis minReader = %d, want 3: recording no keys must not push a project to generation 4", got)
	}
}

// And a checkpoint that records nothing reads as the founding key alone, which
// is what lets every caller hold a total KeySet without asking whether this
// project ever touched the section. A checkpoint that does record keys ignores
// the fallback entirely.
func TestConfigStateKeySetFallsBackToTheFoundingKey(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	if got, want := KeyNameList(parent.KeySet("WB")), "WB"; got != want {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if got := parent.KeySet("WB").Current(); got != "WB" {
		t.Fatalf("Current() = %q, want WB", got)
	}
	if !parent.KeySet("WB").Owns("WB-01K0M6B8A4FTT8C39MXXYTW7C1") {
		t.Fatal("the fallback key set does not own a task minted under the founding key")
	}

	configured := fold(t, parent, []ConfigOperation{keyAdd("WB"), keyAdd("NEW"), keyCurrent("NEW")})
	if got := configured.KeySet("OTHER").Current(); got != "NEW" {
		t.Fatalf("Current() = %q, want NEW: a recorded section is not overridden by the founding argument", got)
	}
	if configured.KeySet("OTHER").Contains("OTHER") {
		t.Fatal("a recorded section adopted the founding key it never recorded")
	}
}

// The ceiling is asked at the authoring boundary, never in the fold, for the
// reason every other ceiling is: a fold that can fail on a count can be made to
// fail forever by two clones each doing something they were allowed to do.
func TestValidateConfigAuthoringRefusesMoreKeysThanTheCeiling(t *testing.T) {
	parent := genesisState(t, testVocabulary(t))
	operations := make([]ConfigOperation, 0, MaxProjectKeys+1)
	operations = append(operations, keyAdd("WB"))
	for index := 0; index < MaxProjectKeys; index++ {
		operations = append(operations, keyAdd("K"+string(rune('A'+index))))
	}
	pack := keyPack(t, parent, operations...)

	if err := ValidateConfigAuthoring(&parent, pack); err == nil {
		t.Fatal("ValidateConfigAuthoring() error = nil, want a refusal naming the ceiling")
	} else if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("ValidateConfigAuthoring() category = %q, want %q", got, CategoryValidation)
	}
	if _, err := ApplyConfig(&parent, pack); err != nil {
		t.Fatalf("ApplyConfig() error = %v, want the fold to accept what authoring refused", err)
	}
}

// nonCanonicalKeySections are the stored key sections no document may carry:
// three normalizeKeyDocument refuses outright, and one — the empty-but-present
// section — it merely rewrites, to the absent member. Both gates below read
// this one table, because a checkpoint and the genesis that produced it are
// held to the same rule, and a table per gate would eventually hold two.
func nonCanonicalKeySections() map[string]*KeyDocument {
	return map[string]*KeyDocument{
		"duplicate key": {
			Keys:    []KeyDefinition{{Key: "WB"}, {Key: "WB"}},
			Current: "WB",
		},
		"retired current": {
			Keys:    []KeyDefinition{{Key: "WB"}, {Key: "NEW", Retired: true}},
			Current: "NEW",
		},
		"empty but present": {
			Keys:    []KeyDefinition{},
			Current: "",
		},
		"malformed key": {
			Keys:    []KeyDefinition{{Key: "wb"}},
			Current: "wb",
		},
	}
}

// A stored key section has to be canonical, the same rule the vocabulary, the
// display settings and the priorities are held to: a peer's ref carrying a
// section with a duplicate key, or with a current key that is retired, is
// corrupt data rather than a configuration anybody folded.
func TestValidateConfigStateDocumentRefusesANonCanonicalKeySection(t *testing.T) {
	for name, document := range nonCanonicalKeySections() {
		t.Run(name, func(t *testing.T) {
			state := genesisState(t, testVocabulary(t))
			state.Config.Keys = document
			if err := validateConfigStateDocument(state); err == nil {
				t.Fatal("validateConfigStateDocument() error = nil, want a corrupt-data refusal")
			} else if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("validateConfigStateDocument() category = %q, want %q", got, CategoryCorruptData)
			}
		})
	}
}

// And so does the genesis that carries one. A genesis is the one operation that
// records a whole configuration as data, so it is the one place a key section
// enters the ledger without any key operation having been folded — and this
// check is the only gate in front of it. Without it a ledger could be rooted on
// a section every later reader refuses the checkpoint computed from.
func TestGenesisRefusesANonCanonicalKeySection(t *testing.T) {
	genesisCarrying := func(keys *KeyDocument) ConfigOperation {
		return ConfigOperation{
			ID:     configOperationID(1),
			Type:   ConfigGenesis,
			Config: &ConfigData{Vocabulary: testVocabulary(t).Document(), Keys: keys},
		}
	}
	// The same genesis carrying no key section validates, which is what makes
	// each refusal below the key check talking rather than the vocabulary's.
	if err := validateConfigOperationDocument(genesisCarrying(nil)); err != nil {
		t.Fatalf("validateConfigOperationDocument(genesis without keys) = %v, want nil", err)
	}
	for name, document := range nonCanonicalKeySections() {
		t.Run(name, func(t *testing.T) {
			if err := validateConfigOperationDocument(genesisCarrying(document)); err == nil {
				t.Fatal("validateConfigOperationDocument() error = nil, want a corrupt-data refusal")
			} else if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("validateConfigOperationDocument() category = %q, want %q", got, CategoryCorruptData)
			}
		})
	}
}

// A key operation naming something that is not a project key is corrupt rather
// than a no-op: the grammar is what every task ID this project ever mints is
// built from, so a document recording a key outside it never came from a
// boundary this build wrote.
func TestKeyOperationsRefuseAnInvalidKey(t *testing.T) {
	for _, key := range []string{"wb", "W", "TOOLONGAKEY", "W-B", ""} {
		operation := ConfigOperation{ID: configOperationID(1), Type: ConfigKeyAdd, Key: key}
		if err := validateConfigOperationDocument(operation); err == nil {
			t.Errorf("validateConfigOperationDocument(key.add %q) = nil, want a corrupt-data refusal", key)
		} else if got := CategoryOf(err); got != CategoryCorruptData {
			t.Errorf("validateConfigOperationDocument(key.add %q) category = %q, want %q", key, got, CategoryCorruptData)
		}
	}
}

// Four separate tables decide what happens to a key operation, exactly as they
// do for a priority one, and the compiler checks none of them. This is the
// key section's copy of priorityoptype_test.go's membership check.
func TestEveryKeyOperationTypeIsAccountedFor(t *testing.T) {
	want := []ConfigOperationType{ConfigKeyAdd, ConfigKeyCurrent, ConfigKeyRetire}
	for _, operationType := range want {
		if _, ok := configOperationShapes[operationType]; !ok {
			t.Errorf("configOperationShapes has no entry for %q", operationType)
		}
		if !operationType.TouchesKeys() {
			t.Errorf("TouchesKeys(%q) = false, want true", operationType)
		}
		if got := configOperationMinReader[operationType]; got != 4 {
			t.Errorf("configOperationMinReader[%q] = %d, want 4; an unstamped pack is misfolded "+
				"by builds that predate the key section", operationType, got)
		}
	}
	found := 0
	for operationType := range configOperationShapes {
		named := strings.HasPrefix(string(operationType), "key.")
		if routed := operationType.TouchesKeys(); routed != named {
			t.Errorf("%q: TouchesKeys() = %t, but its wire name says %t", operationType, routed, named)
		}
		if named {
			found++
		}
	}
	if found != len(want) {
		t.Errorf("configOperationShapes holds %d key operations, want %d; a new one needs an entry in "+
			"TouchesKeys, configKeys.apply, configOperationShapes and configOperationMinReader",
			found, len(want))
	}
}
