package core

import (
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
		`{"a":1,"a":2}`:           `{"a":2}`,
		`"<"`:                     `"<"`,
	} {
		got, err := CanonicalRelicValue([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("CanonicalRelicValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "{", "1 2"} {
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
