package core

import (
	"strings"
	"testing"
)

func TestFoundingKeySetIsOneActiveCurrentKey(t *testing.T) {
	set := FoundingKeySet("WB")
	if got := set.Current(); got != "WB" {
		t.Fatalf("Current() = %q, want %q", got, "WB")
	}
	if got := set.Active(); len(got) != 1 || got[0] != "WB" {
		t.Fatalf("Active() = %v, want [WB]", got)
	}
	if !set.Contains("WB") || !set.IsActive("WB") {
		t.Fatalf("founding key is not active in %#v", set)
	}
	if set.Contains("NEW") {
		t.Fatal("Contains(NEW) = true, want false")
	}
	if set.IsZero() {
		t.Fatal("IsZero() = true, want false")
	}
}

func TestKeySetOwnsOnlyItsOwnKeysAndCanonicalULIDs(t *testing.T) {
	set, err := NewKeySet(KeyDocument{
		Keys:    []KeyDefinition{{Key: "WB"}, {Key: "OLD", Retired: true}},
		Current: "WB",
	})
	if err != nil {
		t.Fatalf("NewKeySet() error = %v", err)
	}
	owned := []string{"WB-01K0M6B8A4FTT8C39MXXYTW7C1", "OLD-01K0M6B8A4FTT8C39MXXYTW7C1"}
	for _, id := range owned {
		if !set.Owns(id) {
			t.Errorf("Owns(%q) = false, want true", id)
		}
	}
	foreign := []string{
		"NEW-01K0M6B8A4FTT8C39MXXYTW7C1", // a key this project does not have
		"WB-01k0m6b8a4ftt8c39mxxytw7c1",  // not canonical uppercase
		"WB-01K0M6B8A4FTT8C39MXXYTW7C",   // too short
		"WB01K0M6B8A4FTT8C39MXXYTW7C1",   // no separator
		"wb-01K0M6B8A4FTT8C39MXXYTW7C1",  // key grammar
		"WB-", "", "WB-01K0M6B8A4FTT8C39MXXYTW7C1/1",
	}
	for _, id := range foreign {
		if set.Owns(id) {
			t.Errorf("Owns(%q) = true, want false", id)
		}
	}
	key, body, ok := set.Parse("OLD-01K0M6B8A4FTT8C39MXXYTW7C1")
	if !ok || key != "OLD" || body != "01K0M6B8A4FTT8C39MXXYTW7C1" {
		t.Fatalf("Parse() = (%q, %q, %v), want (OLD, 01K0M6B8A4FTT8C39MXXYTW7C1, true)", key, body, ok)
	}
	if state, found := set.State("OLD"); !found || state != KeyStateRetired {
		t.Fatalf("State(OLD) = (%q, %v), want (retired, true)", state, found)
	}
}

func TestKeySetRequireActiveNamesTheActiveKeys(t *testing.T) {
	set, err := NewKeySet(KeyDocument{
		Keys:    []KeyDefinition{{Key: "WB"}, {Key: "OLD", Retired: true}},
		Current: "WB",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.RequireActive("WB"); err != nil {
		t.Fatalf("RequireActive(WB) = %v, want nil", err)
	}
	for _, test := range []struct{ key, want string }{
		{key: "OLD", want: "retired"},
		{key: "NEW", want: "WB"},
	} {
		err := set.RequireActive(test.key)
		if err == nil || CategoryOf(err) != CategoryValidation {
			t.Fatalf("RequireActive(%q) = %v, want a validation error", test.key, err)
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Errorf("RequireActive(%q) = %q, want it to mention %q", test.key, err, test.want)
		}
	}
}

func TestNormalizeKeyDocumentRefusesIncoherentDocuments(t *testing.T) {
	for name, document := range map[string]KeyDocument{
		"no keys":         {Keys: []KeyDefinition{}, Current: "WB"},
		"duplicate key":   {Keys: []KeyDefinition{{Key: "WB"}, {Key: "WB"}}, Current: "WB"},
		"malformed key":   {Keys: []KeyDefinition{{Key: "wb"}}, Current: "wb"},
		"unknown current": {Keys: []KeyDefinition{{Key: "WB"}}, Current: "NEW"},
		"retired current": {Keys: []KeyDefinition{{Key: "WB", Retired: true}}, Current: "WB"},
		// The retired-current rule needs a document that still has an active
		// key, or the no-active-key rule above refuses it first and the branch
		// that names the current key is never reached. A project with two keys
		// whose current one is retired is the shape a hand-edited section or a
		// corrupted peer actually produces.
		"retired current beside an active key": {
			Keys:    []KeyDefinition{{Key: "WB"}, {Key: "NEW", Retired: true}},
			Current: "NEW",
		},
		"no active key": {Keys: []KeyDefinition{{Key: "WB", Retired: true}}, Current: ""},
		"blank current": {Keys: []KeyDefinition{{Key: "WB"}}, Current: ""},
	} {
		if _, err := normalizeKeyDocument(&document); err == nil {
			t.Errorf("normalizeKeyDocument(%s) = nil, want an error", name)
		}
	}
	if got, err := normalizeKeyDocument(nil); err != nil || got != nil {
		t.Fatalf("normalizeKeyDocument(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestKeySetAdoptAdviceNamesAForeignKeyOnce(t *testing.T) {
	set := FoundingKeySet("WB")
	if got := set.AdoptableKey("NEW-01K0M6B8A4FTT8C39MXXYTW7C1"); got != "NEW" {
		t.Fatalf("AdoptableKey(NEW-…) = %q, want NEW", got)
	}
	if got := set.AdoptableKey("WB-01K0M6B8A4FTT8C39MXXYTW7C1"); got != "" {
		t.Fatalf("AdoptableKey(own key) = %q, want empty", got)
	}
	if got := set.AdoptableKey("scratch"); got != "" {
		t.Fatalf("AdoptableKey(non-task) = %q, want empty", got)
	}
	if !set.PlausibleTaskID("NEW-01K0M6B8A4FTT8C39MXXYTW7C1") ||
		!set.PlausibleTaskID("WB-whatever-this-is") ||
		set.PlausibleTaskID("scratch") {
		t.Fatal("PlausibleTaskID disagrees with the two rules it inherited from PlausibleTaskID(key, name)")
	}
}
