package historyvalidation

import (
	"context"
	"fmt"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// ConfigValidation reports the configuration ledger's audit.
//
// It is reported under its own section rather than as one more entry in the
// task failure list, because it is not a task: nothing in it is attributable to
// a task ID, and a caller counting invalid tasks must not start counting the
// ledger among them.
type ConfigValidation struct {
	// Head is the ledger tip this audit read.
	Head string `json:"head"`
	// CommitsChecked counts the configuration commits folded.
	CommitsChecked int `json:"commitsChecked"`
	// Valid reports whether every stored checkpoint recomputed from its parent.
	Valid   bool           `json:"valid"`
	Failure *ConfigFailure `json:"failure,omitempty"`
}

// ConfigFailure attributes a ledger failure to one commit.
type ConfigFailure struct {
	Commit   string `json:"commit"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// Advisory reports something true about the validated state that is not a
// validation failure.
//
// The distinction is the whole reason this member exists. A failure says a
// stored document does not follow from its history and the repository needs
// repair; an advisory says the history is exactly what everybody wrote and the
// result is nevertheless worth knowing about. The size ceilings are the case
// that forced it: two clones can each add a status on the same afternoon and
// carry a project past MaxStatusCount without either author being told
// anything, and refusing to fold that would produce a history no clone can ever
// read. So the fold accepts it, `workbook validate` says so here, and the exit
// status is unaffected.
type Advisory struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AdvisoryStatusCeiling reports a folded configuration sitting above one of the
// project's documented status ceilings.
const AdvisoryStatusCeiling = "status-ceiling-exceeded"

type configSource interface {
	ReadConfigHistoryStream(context.Context, core.ProjectConfig, gitstore.ConfigHistoryStream) (bool, error)
}

// validateConfig folds the configuration ledger and compares every stored
// checkpoint with the one its parent and its pack produce.
//
// It uses the same Begin/Commit/End stream the task audit uses, and holds the
// same thing resident: one commit's decoded documents and one accumulated
// parent state. A ledger is short compared with a corpus of task histories, but
// the streaming shape is what keeps that from being an assumption.
func (v *Validator) validateConfig(ctx context.Context) (*ConfigValidation, []Advisory, error) {
	source, capable := v.source.(configSource)
	if !capable {
		return nil, nil, nil
	}
	validation := &ConfigValidation{Valid: true}
	var parent *core.ConfigStateDocument
	var latest core.ConfigStateDocument
	fold := func(commit gitstore.ConfigHistoryCommit) error {
		if validation.Failure != nil {
			return nil
		}
		if err := core.ValidateConfigCheckpoint(parent, commit.Operation, commit.State); err != nil {
			validation.Valid = false
			validation.Failure = configFailure(commit.ObjectID, err)
			return nil
		}
		state := commit.State
		parent = &state
		latest = state
		return nil
	}
	found, err := source.ReadConfigHistoryStream(ctx, v.config, gitstore.ConfigHistoryStream{
		Begin: func(start gitstore.ConfigHistoryStart) error {
			validation.Head = start.Head
			return nil
		},
		Commit: func(commit gitstore.ConfigHistoryCommit) error { return fold(commit) },
		End: func(result gitstore.ConfigHistoryResult) error {
			validation.CommitsChecked = result.CheckedCommits
			if validation.Failure == nil && result.Failure != nil {
				validation.Valid = false
				validation.Failure = configFailure(result.Failure.Commit, result.Failure.Err)
			}
			return nil
		},
	})
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}
	if !validation.Valid {
		return validation, nil, nil
	}
	return validation, statusCeilingAdvisories(latest.Config.Vocabulary), nil
}

// statusCeilingAdvisories reports a folded vocabulary over one of the authoring
// ceilings.
//
// Reaching one of these is never anybody's mistake and never makes the
// checkpoint wrong: validateVocabularyGrowth refuses the pack that would push a
// project over a ceiling, but only on the clone authoring it, and two clones
// authoring concurrently are each refused nothing. Shrinkage is always allowed,
// so the way back is always open — which is exactly what these messages say.
func statusCeilingAdvisories(document core.VocabularyDocument) []Advisory {
	advisories := make([]Advisory, 0, 3)
	if count := len(document.Statuses); count > core.MaxStatusCount {
		advisories = append(advisories, Advisory{
			Code: AdvisoryStatusCeiling,
			Message: fmt.Sprintf(
				"the project defines %d statuses, over the ceiling of %d; "+
					"concurrent additions can reach this without either author being refused, "+
					"and removing one brings it back under",
				count, core.MaxStatusCount),
		})
	}
	if count := len(document.Aliases); count > core.MaxStatusAliasCount {
		advisories = append(advisories, Advisory{
			Code: AdvisoryStatusCeiling,
			Message: fmt.Sprintf(
				"the project has recorded %d status renames, over the ceiling of %d; "+
					"nothing can drop a rename yet, because a clone that has not fetched it "+
					"still needs it to read tasks stored under the old name",
				count, core.MaxStatusAliasCount),
		})
	}
	if count := len(document.Retired); count > core.MaxStatusRetiredCount {
		advisories = append(advisories, Advisory{
			Code: AdvisoryStatusCeiling,
			Message: fmt.Sprintf(
				"the project has recorded %d status removals, over the ceiling of %d; "+
					"nothing can drop a removal yet, because a clone that has not fetched it "+
					"still needs it to read tasks stored under the removed name",
				count, core.MaxStatusRetiredCount),
		})
	}
	if len(advisories) == 0 {
		return nil
	}
	return advisories
}

func configFailure(commit string, err error) *ConfigFailure {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryCorruptData
	}
	message := "invalid configuration history"
	if err != nil && err.Error() != "" {
		message = err.Error()
	}
	return &ConfigFailure{Commit: commit, Category: string(category), Message: message}
}
