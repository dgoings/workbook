package core

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func setDisplay(setting, value string) ConfigOperation {
	return ConfigOperation{Type: ConfigDisplaySet, Setting: setting, Value: value}
}

func unsetDisplay(setting string) ConfigOperation {
	return ConfigOperation{Type: ConfigDisplayUnset, Setting: setting}
}

// The bytes of every checkpoint written before display settings existed have to
// stay exactly what they were, because ValidateConfigCheckpoint compares them
// rather than comparing structures. A section that encoded as `"display":{}`
// when nobody configured anything would make every stored checkpoint in every
// clone disagree with what this build recomputes for it.
//
// The two halves are the whole rule: a project that never configured a display
// setting encodes no member at all, and a project that configured one and then
// cleared it goes back to encoding no member — the canonical form of an
// all-empty section is the absent one, not an empty object.
func TestAConfigCheckpointWithNoDisplaySettingsEncodesNoDisplayMember(t *testing.T) {
	state := genesisState(t, testVocabulary(t))
	encoded := mustEncode(t, state)
	if bytes.Contains(encoded, []byte(`"display"`)) {
		t.Fatalf("a checkpoint nobody configured carries a display member: %s", encoded)
	}

	configured := fold(t, state, []ConfigOperation{setDisplay(DisplayProjectName, "Atlas")})
	if !bytes.Contains(mustEncode(t, configured), []byte(`"display":{"name":"Atlas"}`)) {
		t.Fatalf("a configured checkpoint does not carry the name: %s", mustEncode(t, configured))
	}

	cleared := fold(t, configured, []ConfigOperation{unsetDisplay(DisplayProjectName)})
	if !reflect.DeepEqual(cleared.Config, state.Config) {
		t.Fatalf("clearing the last display setting left %#v, want %#v", cleared.Config, state.Config)
	}
	if bytes.Contains(mustEncode(t, cleared), []byte(`"display"`)) {
		t.Fatalf("a cleared checkpoint still carries a display member: %s", mustEncode(t, cleared))
	}
}

// A stored checkpoint whose display section is present but says nothing is not
// something this build ever writes, so reading one back has to be refused: it
// would re-encode to different bytes than it was read from, which is the state
// the canonicality check exists to catch.
func TestAConfigCheckpointRefusesAnEmptyDisplaySection(t *testing.T) {
	state := genesisState(t, testVocabulary(t))
	state.Config.Display = &DisplayDocument{}
	if err := validateConfigStateDocument(state); err == nil {
		t.Fatal("validateConfigStateDocument() accepted an empty display section")
	} else if CategoryOf(err) != CategoryCorruptData {
		t.Fatalf("validateConfigStateDocument() category = %q, want %q", CategoryOf(err), CategoryCorruptData)
	}
}

func TestApplyConfigFoldsEveryDisplaySetting(t *testing.T) {
	state := genesisState(t, testVocabulary(t))
	folded := fold(t, state, []ConfigOperation{
		setDisplay(DisplayProjectName, "Atlas"),
		setDisplay(DisplayPrimaryColor, "#1a7f4b"),
		setDisplay(DisplayTextColor, "#101820"),
	})
	want := DisplaySettings{Name: "Atlas", PrimaryColor: "#1a7f4b", TextColor: "#101820"}
	if got := folded.Display(); got != want {
		t.Fatalf("Display() = %#v, want %#v", got, want)
	}

	// Each setting is cleared on its own, and clearing one leaves the others
	// exactly where they were.
	cleared := fold(t, folded, []ConfigOperation{unsetDisplay(DisplayPrimaryColor)})
	if got := cleared.Display(); got != (DisplaySettings{Name: "Atlas", TextColor: "#101820"}) {
		t.Fatalf("Display() after one unset = %#v", got)
	}
	// A later set replaces the value rather than accumulating one.
	replaced := fold(t, cleared, []ConfigOperation{setDisplay(DisplayProjectName, "Borealis")})
	if got := replaced.Display().Name; got != "Borealis" {
		t.Fatalf("Display().Name = %q, want %q", got, "Borealis")
	}
	// And unsetting something nobody set is a no-op rather than a failure, which
	// is what makes a duplicated pack idempotent.
	twice := fold(t, replaced, []ConfigOperation{unsetDisplay(DisplayPrimaryColor)})
	if !reflect.DeepEqual(twice.Config, replaced.Config) {
		t.Fatalf("a redundant unset changed the configuration: %#v", twice.Config)
	}
}

// A genesis may carry a display section, because a genesis carries the whole
// configuration as data; what it may not carry is a section that is not in the
// canonical form this build writes.
func TestConfigGenesisCarriesDisplaySettingsCanonically(t *testing.T) {
	vocabulary := testVocabulary(t)
	config := ConfigData{Vocabulary: vocabulary.Document(), Display: &DisplayDocument{Name: "Atlas"}}
	pack := configPack(1, identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &config}})...)
	state, err := ApplyConfig(nil, pack)
	if err != nil {
		t.Fatalf("ApplyConfig(genesis with display) error = %v", err)
	}
	if got := state.Display().Name; got != "Atlas" {
		t.Fatalf("Display().Name = %q, want %q", got, "Atlas")
	}

	empty := ConfigData{Vocabulary: vocabulary.Document(), Display: &DisplayDocument{}}
	emptyPack := configPack(1, identify(0, []ConfigOperation{{Type: ConfigGenesis, Config: &empty}})...)
	if _, err := ApplyConfig(nil, emptyPack); err == nil {
		t.Fatal("ApplyConfig() accepted a genesis carrying an empty display section")
	}
}

func TestDisplayOperationDocumentsRefuseWhatTheyCannotMean(t *testing.T) {
	longName := strings.Repeat("n", MaxProjectNameBytes+1)
	for _, testCase := range []struct {
		name      string
		operation ConfigOperation
	}{
		{"unknown setting", setDisplay("primary-colour", "#1a7f4b")},
		{"blank setting", setDisplay("", "Atlas")},
		{"set with no value", ConfigOperation{Type: ConfigDisplaySet, Setting: DisplayProjectName}},
		{"unset carrying a value", ConfigOperation{Type: ConfigDisplayUnset, Setting: DisplayProjectName, Value: "Atlas"}},
		{"display op carrying a status", ConfigOperation{
			Type: ConfigDisplaySet, Setting: DisplayProjectName, Value: "Atlas", Status: "todo"}},
		{"blank name", setDisplay(DisplayProjectName, "   ")},
		{"oversized name", setDisplay(DisplayProjectName, longName)},
		{"untrimmed name", setDisplay(DisplayProjectName, " Atlas ")},
		{"three-digit color", setDisplay(DisplayPrimaryColor, "#abc")},
		{"color with no hash", setDisplay(DisplayTextColor, "1a7f4b")},
		{"uppercase color", setDisplay(DisplayPrimaryColor, "#1A7F4B")},
		{"named color", setDisplay(DisplayTextColor, "rebeccapurple")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			operation := testCase.operation
			operation.ID = configOperationID(1)
			err := validateConfigOperationDocument(operation)
			if err == nil {
				t.Fatalf("validateConfigOperationDocument(%#v) accepted it", operation)
			}
			if CategoryOf(err) != CategoryCorruptData {
				t.Fatalf("category = %q, want %q (%v)", CategoryOf(err), CategoryCorruptData, err)
			}
		})
	}
}

// The whole point of the marker: a pack carrying a display operation says so,
// and the checkpoint carries the watermark forward for every later read.
func TestADisplayPackRequiresTheDisplayGeneration(t *testing.T) {
	if configOperationMinReader[ConfigDisplaySet] != 2 || configOperationMinReader[ConfigDisplayUnset] != 2 {
		t.Fatalf("display operations declare generation %d and %d, want 2 and 2",
			configOperationMinReader[ConfigDisplaySet], configOperationMinReader[ConfigDisplayUnset])
	}
	pack, err := NewConfigOperationPack(configTestProjectID, configTestGeneration, "developer@example.com", 2,
		configTestNow, identify(1, []ConfigOperation{setDisplay(DisplayProjectName, "Atlas")}))
	if err != nil {
		t.Fatalf("NewConfigOperationPack() error = %v", err)
	}
	if pack.MinReader != 2 {
		t.Fatalf("pack minReader = %d, want 2", pack.MinReader)
	}
	state := genesisState(t, testVocabulary(t))
	if state.MinReader != 0 {
		t.Fatalf("a genesis carrying no display section declares generation %d, want 0", state.MinReader)
	}
	written, err := ApplyConfig(&state, pack)
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if written.MinReader != 2 {
		t.Fatalf("checkpoint minReader = %d, want 2", written.MinReader)
	}
	// The watermark stays put when a later pack needs nothing new, which is what
	// lets a clone answer "can I still change this?" from the tip alone.
	later := fold(t, written, []ConfigOperation{add("triage", "Triage", "5/1")})
	if later.MinReader != 2 {
		t.Fatalf("checkpoint minReader after an ordinary pack = %d, want 2", later.MinReader)
	}
	// And a genesis that carries the section declares it too, even though no
	// display operation appears in the pack. Nothing this build writes seeds one
	// that way; the marker must not be off by one if something ever does.
	config := ConfigData{Vocabulary: testVocabulary(t).Document(), Display: &DisplayDocument{Name: "Atlas"}}
	if got := ConfigPackMinReader([]ConfigOperation{{Type: ConfigGenesis, Config: &config}}); got != 2 {
		t.Fatalf("a genesis carrying display settings declares generation %d, want 2", got)
	}
}

func TestValidateThemeColorCanonicalizesToLowercase(t *testing.T) {
	canonical, err := ValidateThemeColor("#1A7F4B")
	if err != nil {
		t.Fatalf("ValidateThemeColor() error = %v", err)
	}
	if canonical != "#1a7f4b" {
		t.Fatalf("ValidateThemeColor() = %q, want %q", canonical, "#1a7f4b")
	}
	for _, rejected := range []string{"", "#abc", "abc123", "#12345g", "#1234567", " #1a7f4b"} {
		if _, err := ValidateThemeColor(rejected); err == nil {
			t.Fatalf("ValidateThemeColor(%q) accepted it", rejected)
		} else if CategoryOf(err) != CategoryValidation {
			t.Fatalf("ValidateThemeColor(%q) category = %q, want %q", rejected, CategoryOf(err), CategoryValidation)
		}
	}
}

func TestValidateProjectNameQuotesItsRule(t *testing.T) {
	if err := ValidateProjectName("Atlas"); err != nil {
		t.Fatalf("ValidateProjectName() error = %v", err)
	}
	err := ValidateProjectName(strings.Repeat("n", MaxProjectNameBytes+1))
	if err == nil {
		t.Fatal("ValidateProjectName() accepted an oversized name")
	}
	if CategoryOf(err) != CategoryValidation || !strings.Contains(err.Error(), "100") {
		t.Fatalf("ValidateProjectName() error = %v, want a validation failure naming the ceiling", err)
	}
	if err := ValidateProjectName("   "); err == nil {
		t.Fatal("ValidateProjectName() accepted a blank name")
	}
}

// The boundary canonicalizes before anything is authored, so an uppercase color
// typed by a person and the same color typed in lowercase are one stored value
// rather than two.
func TestCanonicalDisplayValueTrimsAndFolds(t *testing.T) {
	for _, testCase := range []struct{ setting, given, want string }{
		{DisplayProjectName, "  Atlas  ", "Atlas"},
		{DisplayPrimaryColor, "#1A7F4B", "#1a7f4b"},
		{DisplayTextColor, "  #101820  ", "#101820"},
	} {
		got, err := CanonicalDisplayValue(testCase.setting, testCase.given)
		if err != nil {
			t.Fatalf("CanonicalDisplayValue(%q, %q) error = %v", testCase.setting, testCase.given, err)
		}
		if got != testCase.want {
			t.Fatalf("CanonicalDisplayValue(%q, %q) = %q, want %q", testCase.setting, testCase.given, got, testCase.want)
		}
	}
	if _, err := CanonicalDisplayValue("primary-colour", "#1a7f4b"); err == nil {
		t.Fatal("CanonicalDisplayValue() accepted an unknown setting")
	} else if CategoryOf(err) != CategoryValidation {
		t.Fatalf("unknown setting category = %q, want %q", CategoryOf(err), CategoryValidation)
	}
}

// A stored section with more than one thing wrong names the same one every
// time.
//
// The check used to walk a Go map, whose iteration order is randomized per run,
// so a checkpoint that had been hand-edited into two non-canonical values would
// be refused with one message on one clone and another on the next. A refusal
// somebody has to act on is a refusal that has to be the same refusal twice, so
// the walk follows DisplaySettingNames — the order every other surface presents
// these settings in.
func TestNormalizeDisplayDocumentRefusesInAStableOrder(t *testing.T) {
	document := &DisplayDocument{
		Name:         " Untrimmed",
		PrimaryColor: "#ABC123",
		TextColor:    "not-a-color",
	}
	_, err := normalizeDisplayDocument(document)
	if err == nil {
		t.Fatal("normalizeDisplayDocument() accepted a section no author could have written")
	}
	first := err.Error()
	for range 64 {
		_, again := normalizeDisplayDocument(document)
		if again == nil {
			t.Fatal("normalizeDisplayDocument() accepted the same section on a later call")
		}
		if again.Error() != first {
			t.Fatalf("normalizeDisplayDocument() = %q then %q; the refusal depends on map order", first, again.Error())
		}
	}
	// And the one it names is the first in the order every surface reads.
	if !strings.Contains(first, DisplayProjectName) {
		t.Fatalf("refusal = %q, want it to name %q, the first setting in DisplaySettingNames", first, DisplayProjectName)
	}
}

// The fallback line for a display conflict somebody built without a detail.
//
// The classifier always writes one, because this union has no member to put a
// setting name in — but ConfigConflictDetail is what every surface calls, and
// its default arm returns the bare type string. A display conflict that reached
// a person as "display-setting" would be a report nobody could act on, so the
// arm exists; it is untested until something asks for it.
func TestConfigConflictDetailAnswersForADisplaySettingWithoutOne(t *testing.T) {
	detail := ConfigConflictDetail(ConfigConflict{Type: ConfigConflictDisplaySetting, Ours: "Atlas"})
	if detail == string(ConfigConflictDisplaySetting) {
		t.Fatal("ConfigConflictDetail() fell through to the bare type string")
	}
	if !strings.Contains(detail, "display setting") {
		t.Fatalf("ConfigConflictDetail() = %q, want it to say what kind of conflict this is", detail)
	}
	// A conflict that carries its own line keeps it, which is every conflict the
	// classifier produces.
	written := ConfigConflict{
		Type:   ConfigConflictDisplaySetting,
		Detail: "project-name was set to Atlas here and cleared on origin",
	}
	if got := ConfigConflictDetail(written); got != written.Detail {
		t.Fatalf("ConfigConflictDetail() = %q, want the conflict's own line", got)
	}
	// And a display conflict names no status, so the failure it summarizes to
	// leads with what happened rather than with an empty name.
	err := ConfigConflictError([]ConfigConflict{written})
	if CategoryOf(err) != CategoryConflict {
		t.Fatalf("ConfigConflictError() category = %q, want %q", CategoryOf(err), CategoryConflict)
	}
	if err.Error() != written.Detail {
		t.Fatalf("ConfigConflictError() = %q, want exactly the conflict's line", err.Error())
	}
}
