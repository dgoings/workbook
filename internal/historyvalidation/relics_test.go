package historyvalidation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// relicValidatorSource also answers for relic histories. It embeds the
// configuration source so a test can combine a ledger failure with relic
// results; the existing sources, which do not implement the relic reader, must
// still validate everything else and report no relic section.
type relicValidatorSource struct {
	configValidatorSource
	relicHeads []core.RelicHead
	relicList  error
	// relicResults answers ValidateRelicHistory by relic ID.
	relicResults map[string]relicHistoryAnswer
	validated    []string
}

type relicHistoryAnswer struct {
	checked int
	failure *gitstore.RelicHistoryFailure
	err     error
}

func (s *relicValidatorSource) ListRelicHeads(context.Context, core.ProjectConfig) ([]core.RelicHead, error) {
	return append([]core.RelicHead(nil), s.relicHeads...), s.relicList
}

func (s *relicValidatorSource) ValidateRelicHistory(
	_ context.Context,
	_ core.ProjectConfig,
	head core.RelicHead,
) (int, *gitstore.RelicHistoryFailure, error) {
	s.validated = append(s.validated, head.RelicID)
	answer := s.relicResults[head.RelicID]
	return answer.checked, answer.failure, answer.err
}

func relicValidationID(n int) string { return fmt.Sprintf("WB-R-01M9A%021d", n) }

func relicValidationHead(n int) core.RelicHead {
	return core.RelicHead{RelicID: relicValidationID(n), ObjectID: fmt.Sprintf("relic-commit-%d", n)}
}

func validRelicAnswer(checked int) relicHistoryAnswer { return relicHistoryAnswer{checked: checked} }

func failedRelicAnswer(checked int, commit string, category core.Category, message string) relicHistoryAnswer {
	return relicHistoryAnswer{
		checked: checked,
		failure: &gitstore.RelicHistoryFailure{Commit: commit, Err: core.Errorf(category, "%s", message)},
	}
}

func validateWithRelics(t *testing.T, source *relicValidatorSource) (Result, error) {
	t.Helper()
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	return (&Validator{source: source, cache: cache, config: testConfig()}).Validate(ctx, false)
}

func TestValidateReportsNoRelicSectionWithoutARelic(t *testing.T) {
	source := &relicValidatorSource{}
	result, err := validateWithRelics(t, source)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Relics != nil {
		t.Fatalf("Relics = %#v, want nothing reported for a repository with no relic", result.Relics)
	}
}

func TestValidateReportsNoRelicSectionForASourceWithoutRelics(t *testing.T) {
	ctx := context.Background()
	cache := openTestCache(t, ctx, testConfig())
	result, err := (&Validator{source: &validatorSource{}, cache: cache, config: testConfig()}).Validate(ctx, false)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Relics != nil {
		t.Fatalf("Relics = %#v, want nothing reported by a source that cannot read relics", result.Relics)
	}
}

func TestValidateCountsEveryRelicAndListsItsFailures(t *testing.T) {
	newerMessage := "relic " + relicValidationID(4) + " was written by a newer workbook; upgrade workbook to check its history"
	source := &relicValidatorSource{
		relicHeads: []core.RelicHead{relicValidationHead(1), relicValidationHead(2), relicValidationHead(3), relicValidationHead(4)},
		relicResults: map[string]relicHistoryAnswer{
			relicValidationID(1): validRelicAnswer(3),
			relicValidationID(2): failedRelicAnswer(2, "forged-2", core.CategoryCorruptData, "the body does not follow"),
			relicValidationID(3): validRelicAnswer(1),
			relicValidationID(4): failedRelicAnswer(1, "newer-4", core.CategoryNewerWriter, newerMessage),
		},
	}
	result, err := validateWithRelics(t, source)
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Validate() category = %q, want %q; error = %v", got, want, err)
	}
	if want := "semantic history validation found 1 invalid relic(s)"; err.Error() != want {
		t.Fatalf("Validate() error = %q, want %q", err, want)
	}
	want := &RelicValidation{
		RelicCount:     4,
		CommitsChecked: 7,
		Valid:          2,
		Invalid:        1,
		NewerWriter:    1,
		Failures: []RelicFailure{
			{RelicID: relicValidationID(2), Commit: "forged-2", Category: "corrupt-data", Message: "the body does not follow"},
			{RelicID: relicValidationID(4), Commit: "newer-4", Category: "newer-writer", Message: newerMessage},
		},
	}
	if !reflect.DeepEqual(result.Relics, want) {
		t.Fatalf("Relics = %#v, want %#v", result.Relics, want)
	}
}

func TestValidateReportsAnEmptyRelicFailureList(t *testing.T) {
	source := &relicValidatorSource{
		relicHeads:   []core.RelicHead{relicValidationHead(1)},
		relicResults: map[string]relicHistoryAnswer{relicValidationID(1): validRelicAnswer(2)},
	}
	result, err := validateWithRelics(t, source)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Relics == nil || result.Relics.Failures == nil || len(result.Relics.Failures) != 0 {
		t.Fatalf("Relics = %#v, want an empty, non-nil failure list so JSON carries []", result.Relics)
	}
}

func TestValidateReportsInvalidTasksBeforeInvalidRelics(t *testing.T) {
	bad := validationHistory(t, taskID(1), generationID(1), 30, 2)
	bad[1].State.Task.Title = "tampered"
	source := &relicValidatorSource{
		relicHeads:   []core.RelicHead{relicValidationHead(1)},
		relicResults: map[string]relicHistoryAnswer{relicValidationID(1): failedRelicAnswer(1, "forged-1", core.CategoryCorruptData, "bad")},
	}
	source.heads = headsFor(bad)
	source.histories = map[string]gitstore.TaskHistoryResult{taskID(1): historyResult(taskID(1), bad[1].ObjectID, false, bad)}
	result, err := validateWithRelics(t, source)
	if want := "semantic history validation found 1 invalid task(s)"; err == nil || err.Error() != want {
		t.Fatalf("Validate() error = %v, want %q", err, want)
	}
	if result.Relics == nil || result.Relics.Invalid != 1 {
		t.Fatalf("Relics = %#v, want the invalid relic still reported", result.Relics)
	}
}

func TestValidateReportsANewerWritersRelicAfterEverythingElse(t *testing.T) {
	newerRelic := map[string]relicHistoryAnswer{
		relicValidationID(1): failedRelicAnswer(0, "newer-1", core.CategoryNewerWriter, "newer"),
	}

	t.Run("alone", func(t *testing.T) {
		source := &relicValidatorSource{relicHeads: []core.RelicHead{relicValidationHead(1)}, relicResults: newerRelic}
		result, err := validateWithRelics(t, source)
		if got, want := core.CategoryOf(err), core.CategoryNewerWriter; got != want {
			t.Fatalf("Validate() category = %q, want %q; error = %v", got, want, err)
		}
		if want := "1 relic history(s) were written by a newer workbook; upgrade workbook to validate them"; err.Error() != want {
			t.Fatalf("Validate() error = %q, want %q", err, want)
		}
		if result.Relics == nil || result.Relics.NewerWriter != 1 || result.Relics.Invalid != 0 {
			t.Fatalf("Relics = %#v, want one newer-writer relic and none invalid", result.Relics)
		}
	})

	t.Run("after a configuration failure", func(t *testing.T) {
		source := &relicValidatorSource{relicHeads: []core.RelicHead{relicValidationHead(1)}, relicResults: newerRelic}
		source.present = true
		source.commits = configLedger(t, core.LegacyVocabulary().Document())
		source.failure = &gitstore.ConfigHistoryFailure{Commit: "0f0f0f0f", Err: core.Errorf(core.CategoryOperational, "too large")}
		_, err := validateWithRelics(t, source)
		if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
			t.Fatalf("Validate() category = %q, want %q; error = %v", got, want, err)
		}
		if !strings.HasPrefix(err.Error(), "configuration ledger validation failed") {
			t.Fatalf("Validate() error = %q, want the ledger failure first", err)
		}
	})
}

func TestValidatePassesOnARelicHistoryReadError(t *testing.T) {
	readFailure := core.Errorf(core.CategoryOperational, "git failed")
	for name, source := range map[string]*relicValidatorSource{
		"list": {relicList: readFailure},
		"history": {
			relicHeads:   []core.RelicHead{relicValidationHead(1)},
			relicResults: map[string]relicHistoryAnswer{relicValidationID(1): {err: readFailure}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateWithRelics(t, source); !errors.Is(err, readFailure) {
				t.Fatalf("Validate() error = %v, want the read error %v", err, readFailure)
			}
		})
	}
}
