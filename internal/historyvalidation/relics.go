package historyvalidation

import (
	"context"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// RelicValidation reports the audit of every relic history.
//
// It is a section of its own, as the configuration ledger's is, because a
// relic is not a task: a caller counting invalid tasks must not start counting
// relics among them.
type RelicValidation struct {
	// RelicCount is the number of relic refs the audit found.
	RelicCount int `json:"relicCount"`
	// CommitsChecked counts the commits that passed every rule, summed over
	// all relics.
	CommitsChecked int `json:"commitsChecked"`
	Valid          int `json:"valid"`
	Invalid        int `json:"invalid"`
	// NewerWriter counts relics whose history this build is too old to check.
	// As for tasks, they are not counted as invalid, and the member is omitted
	// when zero.
	NewerWriter int            `json:"newerWriter,omitempty"`
	Failures    []RelicFailure `json:"failures"`
}

// RelicFailure attributes one relic's failure to the first commit that broke a
// rule, or that this build could not check.
type RelicFailure struct {
	RelicID  string `json:"relicId"`
	Commit   string `json:"commit"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

type relicSource interface {
	ListRelicHeads(context.Context, core.ProjectConfig) ([]core.RelicHead, error)
	ValidateRelicHistory(context.Context, core.ProjectConfig, core.RelicHead) (int, *gitstore.RelicHistoryFailure, error)
}

// validateRelics checks every relic history from its root on each run. Nothing
// is cached: the rules read objects Git already has, and a cache would be one
// more thing that could vouch for a history nobody re-read.
//
// It returns nil for a source that cannot read relics and for a repository
// that holds none, so such a repository's result is the one it gave before
// relics existed.
func (v *Validator) validateRelics(ctx context.Context) (*RelicValidation, error) {
	source, capable := v.source.(relicSource)
	if !capable {
		return nil, nil
	}
	heads, err := source.ListRelicHeads(ctx, v.config)
	if err != nil {
		return nil, err
	}
	if len(heads) == 0 {
		return nil, nil
	}
	validation := &RelicValidation{RelicCount: len(heads), Failures: []RelicFailure{}}
	for _, head := range heads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		checked, failure, err := source.ValidateRelicHistory(ctx, v.config, head)
		if err != nil {
			return nil, err
		}
		validation.CommitsChecked += checked
		if failure == nil {
			validation.Valid++
			continue
		}
		reported := relicFailure(head.RelicID, failure)
		if core.Category(reported.Category) == core.CategoryNewerWriter {
			validation.NewerWriter++
		} else {
			validation.Invalid++
		}
		validation.Failures = append(validation.Failures, reported)
	}
	return validation, nil
}

func relicFailure(relicID string, failure *gitstore.RelicHistoryFailure) RelicFailure {
	category := core.CategoryOf(failure.Err)
	if category == "" {
		category = core.CategoryCorruptData
	}
	message := "invalid relic history"
	if failure.Err != nil && strings.TrimSpace(failure.Err.Error()) != "" {
		message = failure.Err.Error()
	}
	return RelicFailure{RelicID: relicID, Commit: failure.Commit, Category: string(category), Message: message}
}

var _ relicSource = (*gitstore.Repository)(nil)
