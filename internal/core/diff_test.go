package core

import (
	"strings"
	"testing"
)

func TestWordDiffSpansReconstructBothTexts(t *testing.T) {
	// Mutation caught: normalizing whitespace, so a rendered diff no longer
	// reproduces the text it claims to describe.
	tests := []struct {
		name          string
		before, after string
	}{
		{name: "empty", before: "", after: ""},
		{name: "created", before: "", after: "Alpha beta gamma."},
		{name: "cleared", before: "Alpha beta gamma.", after: ""},
		{name: "unchanged", before: "Alpha beta gamma.", after: "Alpha beta gamma."},
		{name: "word replaced", before: "Alpha beta gamma.", after: "Alpha delta gamma."},
		{name: "word inserted", before: "Alpha gamma.", after: "Alpha beta gamma."},
		{name: "word removed", before: "Alpha beta gamma.", after: "Alpha gamma."},
		{name: "reflowed", before: "Alpha beta\ngamma.", after: "Alpha  beta gamma."},
		{name: "unrelated", before: "One two three", after: "Four five six"},
		{name: "paragraphs", before: "First line.\n\nSecond line.", after: "First line.\n\nSecond line, revised."},
		{name: "multibyte", before: "α β γ", after: "α δ γ"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spans := WordDiff(test.before, test.after)
			var old, current strings.Builder
			for _, span := range spans {
				switch span.Kind {
				case DiffEqual:
					old.WriteString(span.Text)
					current.WriteString(span.Text)
				case DiffDelete:
					old.WriteString(span.Text)
				case DiffInsert:
					current.WriteString(span.Text)
				default:
					t.Fatalf("span kind = %q, want equal, delete, or insert", span.Kind)
				}
				if span.Text == "" {
					t.Fatalf("spans = %#v, want no empty span", spans)
				}
			}
			if got := old.String(); got != test.before {
				t.Fatalf("equal+delete spans = %q, want the old text %q", got, test.before)
			}
			if got := current.String(); got != test.after {
				t.Fatalf("equal+insert spans = %q, want the new text %q", got, test.after)
			}
		})
	}
}

func TestWordDiffMergesAdjacentSpansOfOneKind(t *testing.T) {
	// Mutation caught: emitting one span per token, which makes a rendered diff
	// unreadable and every consumer re-merge them.
	spans := WordDiff("Alpha beta gamma delta", "Alpha epsilon zeta delta")
	for index := 1; index < len(spans); index++ {
		if spans[index].Kind == spans[index-1].Kind {
			t.Fatalf("spans = %#v, want no two adjacent spans of one kind", spans)
		}
	}
	if spans[0].Kind != DiffEqual || spans[0].Text != "Alpha " {
		t.Fatalf("first span = %#v, want the untouched opening kept equal", spans[0])
	}
	last := spans[len(spans)-1]
	if last.Kind != DiffEqual || last.Text != " delta" {
		t.Fatalf("last span = %#v, want the untouched ending kept equal", last)
	}
}

func TestWordDiffKeepsUnchangedWordsEqualAroundAnEdit(t *testing.T) {
	// Mutation caught: reporting a one-word edit as a whole-text replacement.
	spans := WordDiff("The quick brown fox", "The quick red fox")
	equal := 0
	for _, span := range spans {
		if span.Kind == DiffEqual {
			equal += len(strings.Fields(span.Text))
		}
	}
	if equal != 3 {
		t.Fatalf("equal words = %d, want the three untouched words; spans = %#v", equal, spans)
	}
}

func TestWordDiffReportsOneReplacementBeyondTheSearchBound(t *testing.T) {
	// Mutation caught: an unbounded search that makes two unrelated documents
	// cost time quadratic in their size.
	before := strings.TrimSuffix(strings.Repeat("alpha ", maxDiffTokens), " ")
	after := strings.TrimSuffix(strings.Repeat("omega ", maxDiffTokens), " ")
	spans := WordDiff(before, after)
	deletes, inserts := 0, 0
	for _, span := range spans {
		switch span.Kind {
		case DiffDelete:
			deletes++
		case DiffInsert:
			inserts++
		}
	}
	if deletes != 1 || inserts != 1 {
		t.Fatalf("spans = %d delete(s) and %d insert(s), want one whole-middle replacement", deletes, inserts)
	}
}
