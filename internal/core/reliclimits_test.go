package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func requireValidation(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil || CategoryOf(err) != CategoryValidation {
		t.Errorf("%s: error = %v, want validation", label, err)
	}
}

func TestRelicSingleValueLimits(t *testing.T) {
	if got, err := ValidateRelicTitle(strings.Repeat("a", 500)); err != nil || len(got) != 500 {
		t.Errorf("500-byte title: %v", err)
	}
	_, err := ValidateRelicTitle(strings.Repeat("a", 501))
	requireValidation(t, "501-byte title", err)
	if err != nil && (!strings.Contains(err.Error(), "501") || !strings.Contains(err.Error(), "500")) {
		t.Errorf("size message lacks size and limit: %v", err)
	}
	_, err = ValidateRelicTitle("   ")
	requireValidation(t, "blank title", err)
	if got, err := ValidateRelicTitle("  x  "); err != nil || got != "x" {
		t.Errorf("trim = %q, %v", got, err)
	}
	_, err = ValidateRelicTitle("a\xffb")
	requireValidation(t, "invalid UTF-8 title", err)

	for label, validate := range map[string]func(string) error{
		"type": ValidateRelicType, "part type": ValidateRelicPartType,
	} {
		if err := validate(strings.Repeat("a", 100)); err != nil {
			t.Errorf("%s 100 bytes: %v", label, err)
		}
		err := validate(strings.Repeat("a", 101))
		requireValidation(t, label+" 101 bytes", err)
		if err != nil && (!strings.Contains(err.Error(), "101") || !strings.Contains(err.Error(), "100")) {
			t.Errorf("%s size message: %v", label, err)
		}
		for _, bad := range []string{"Spec", "open question", "-x", ""} {
			requireValidation(t, label+" "+bad, validate(bad))
		}
		if err := validate("open-question"); err != nil {
			t.Errorf("%s open-question: %v", label, err)
		}
	}

	for _, good := range []string{"", "md", "c++", "tar.gz", strings.Repeat("a", 16)} {
		if err := ValidateRelicBodyFormat(good); err != nil {
			t.Errorf("format %q: %v", good, err)
		}
	}
	err = ValidateRelicBodyFormat(strings.Repeat("a", 17))
	requireValidation(t, "17-byte format", err)
	if err != nil && (!strings.Contains(err.Error(), "17") || !strings.Contains(err.Error(), "16")) {
		t.Errorf("format size message: %v", err)
	}
	for _, bad := range []string{"MD", ".md", "a b"} {
		requireValidation(t, "format "+bad, ValidateRelicBodyFormat(bad))
	}

	for _, kind := range []string{"property", "field"} {
		for _, good := range []string{"status", "reviewer name"} {
			if err := ValidateRelicName(kind, good); err != nil {
				t.Errorf("%s name %q: %v", kind, good, err)
			}
		}
		for _, bad := range []string{"", "a=b", "a\nb", "a\x7f", "a\xffb"} {
			err := ValidateRelicName(kind, bad)
			requireValidation(t, kind+" name "+bad, err)
			if err != nil && !strings.Contains(err.Error(), kind) {
				t.Errorf("message lacks kind: %v", err)
			}
		}
	}
}

func TestCanonicalRelicValue(t *testing.T) {
	for in, want := range map[string]string{
		`{"b":1,"a":[true,null]}`: `{"a":[true,null],"b":1}`,
		` "x" `:                   `"x"`,
		`1.0`:                     `1.0`,
		`1e2`:                     `1e2`,
		`"<"`:                     `"\u003c"`,
		`"a&b>c"`:                 `"a\u0026b\u003ec"`,
		`"\u003c"`:                `"\u003c"`,
	} {
		got, err := CanonicalRelicValue([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("CanonicalRelicValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`"<"`, `{"k":"a&b"}`, `[1.0,"x>y"]`} {
		canonical, err := CanonicalRelicValue([]byte(in))
		if err != nil {
			t.Fatalf("CanonicalRelicValue(%q): %v", in, err)
		}
		type holder struct {
			Value json.RawMessage `json:"value"`
		}
		encoded, err := json.Marshal(holder{Value: canonical})
		if err != nil {
			t.Fatal(err)
		}
		var back holder
		if err := json.Unmarshal(encoded, &back); err != nil {
			t.Fatal(err)
		}
		if string(back.Value) != string(canonical) {
			t.Errorf("value %q does not survive json.Marshal: read back %q, canonical %q", in, back.Value, canonical)
		}
	}
	// A member named twice kept its last value once; it is refused now.
	for _, bad := range []string{"", "{", "1 2", `{"a":1,"a":2}`} {
		_, err := CanonicalRelicValue([]byte(bad))
		requireValidation(t, "value "+bad, err)
	}
}

func TestBodyFormatFromFileName(t *testing.T) {
	for in, want := range map[string]string{
		"notes.md": "md", "Notes.MD": "md", "notes": "", "archive.tar.gz": "gz", "dir.d/notes": "",
	} {
		if got, err := BodyFormatFromFileName(in); err != nil || got != want {
			t.Errorf("BodyFormatFromFileName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"x.verylongextension1", "x.a b"} {
		_, err := BodyFormatFromFileName(bad)
		requireValidation(t, bad, err)
		if err != nil && !strings.Contains(err.Error(), "--body-format") {
			t.Errorf("%q message lacks --body-format: %v", bad, err)
		}
	}
}

// Mutation witness: lowering any relic limit refuses relics that a released
// Workbook wrote, and raising one is a decision the relic benchmarks have to
// support (docs/architecture.md, "Relic benchmarks"). The benchmarks confirmed
// these as the starting limits, so a change to any of them has to change this
// table too.
func TestRelicLimitsAreTheConfirmedStartingLimits(t *testing.T) {
	limits := []struct {
		name      string
		got, want int
	}{
		{"MaxRelicTitleBytes", MaxRelicTitleBytes, 500},
		{"MaxRelicTypeBytes", MaxRelicTypeBytes, 100},
		{"MaxRelicPartTypeBytes", MaxRelicPartTypeBytes, 100},
		{"MaxRelicBodyFormatBytes", MaxRelicBodyFormatBytes, 16},
		{"MaxRelicPartPositionBytes", MaxRelicPartPositionBytes, 256},
		{"MaxRelicPropertyValueBytes", MaxRelicPropertyValueBytes, 65536},
		{"MaxRelicFieldValueBytes", MaxRelicFieldValueBytes, 16384},
		{"MaxRelicPropertiesBytes", MaxRelicPropertiesBytes, 65536},
		{"MaxRelicPartFieldsBytes", MaxRelicPartFieldsBytes, 16384},
		{"MaxRelicBodyBytes", MaxRelicBodyBytes, 1048576},
		{"MaxRelicLiveParts", MaxRelicLiveParts, 2000},
		{"MaxRelicParts", MaxRelicParts, 4000},
		{"MaxRelicLiveBodyBytes", MaxRelicLiveBodyBytes, 16777216},
		{"MaxRelicNameBytes", MaxRelicNameBytes, 100},
		{"MaxRelicValueDepth", MaxRelicValueDepth, 32},
		{"MaxRelicRelationBytes", MaxRelicRelationBytes, 100},
		{"MaxRelicLinks", MaxRelicLinks, 2000},
	}
	for _, limit := range limits {
		if limit.got != limit.want {
			t.Errorf("%s = %d, want %d", limit.name, limit.got, limit.want)
		}
	}
}
