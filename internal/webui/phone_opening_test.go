package webui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A phone opens the board on the column `workbook next` draws from, and that
// is the status the project tagged next, by whatever name it gave it and
// wherever it put it — not a status called ready, and not the first column.
// A project that tagged nothing next opens on its first column, and one with
// no columns names none.
func TestHandlerOpensAPhoneOnTheColumnNextDrawsFrom(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T, next string) core.Vocabulary {
		t.Helper()
		definitions := make([]core.StatusDefinition, len(phoneStatuses))
		for index, status := range phoneStatuses {
			var tags []core.StatusTag
			for _, tag := range status.tags {
				if tag != core.StatusTagNext {
					tags = append(tags, tag)
				}
			}
			if string(status.status) == next {
				tags = append(tags, core.StatusTagNext)
			}
			definitions[index] = core.StatusDefinition{Status: status.status, Label: status.label, Rank: fmt.Sprintf("%d/1", index+1), Tags: tags}
		}
		vocabulary, err := core.NewVocabulary(definitions, nil, nil)
		if err != nil {
			t.Fatalf("build statuses with %q next: %v", next, err)
		}
		return vocabulary
	}
	for _, test := range []struct {
		name, next string
		want       core.Status
	}{
		{"second column", "queued", "queued"},
		{"moved and renamed", "review", "review"},
		{"nothing next", "", "inbox"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			vocabulary := build(t, test.next)
			if got := openingStatus(vocabulary); got != test.want {
				t.Fatalf("a phone opens on %q, want %q", got, test.want)
			}
			body := boardPageWith(t, vocabulary)
			if !strings.Contains(body, `data-opening-status="`+string(test.want)+`"`) {
				t.Errorf("the page does not name %q as the column a phone opens on", test.want)
			}
			if count := strings.Count(body, ` data-phone-current>`); count != 1 || !strings.Contains(body, `<section class="column" data-phone-column="`+string(test.want)+`" data-phone-current>`) {
				t.Errorf("the page marks %d columns as the one a phone shows first, want %q alone", count, test.want)
			}
			if !strings.Contains(body, `data-status-tab="`+string(test.want)+`" aria-pressed="true"`) || strings.Count(body, `" aria-pressed="true"><span class="status-tab__label">`) != 1 {
				t.Errorf("the page does not press %q alone in the row of columns", test.want)
			}
		})
	}
	if got := openingStatus(statuslessVocabulary(t)); got != "" {
		t.Errorf("a project with no columns opens a phone on %q, want none", got)
	}
}
