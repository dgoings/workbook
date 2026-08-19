package cli

import (
	"context"
	"fmt"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/webui"
)

// boardVocabulary is the status verb family's second surface.
//
// It authors nothing of its own. Every change goes through the same planners
// `workbook status` uses, so the board refuses what the CLI refuses, in the same
// words, and records the same operations — a rename is still a rename followed
// by the relabel the derived-label rule asks for, which is the pack shape
// reconciliation knows how to classify. What differs is only what a server can
// honestly do around the write: see apply.
type boardVocabulary struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	publisher  *boardPublisher
	// service reads the project's tasks against a given vocabulary, for the one
	// change that has to count them. It takes the vocabulary rather than reading
	// one, so a removal counts what it moves against the same statuses it is
	// authored against.
	service func(core.Vocabulary) core.Service
}

func (board *boardVocabulary) add(
	ctx context.Context,
	addition webui.VocabularyStatusAddition,
) (webui.VocabularyMutation, error) {
	return board.apply(ctx, addition.ExpectedHead,
		func(scope statusScope, vocabulary core.Vocabulary) (statusPlan, error) {
			tags, err := parseStatusTags(addition.Tags)
			if err != nil {
				return statusPlan{}, err
			}
			return planStatusAdd(ctx, scope, vocabulary, statusAddition{
				Status: addition.Status,
				Label:  addition.Label,
				Tags:   tags,
				Before: addition.Before,
				After:  addition.After,
			})
		})
}

func (board *boardVocabulary) edit(
	ctx context.Context,
	status core.Status,
	change webui.VocabularyStatusEdit,
) (webui.VocabularyMutation, error) {
	return board.apply(ctx, change.ExpectedHead,
		func(scope statusScope, vocabulary core.Vocabulary) (statusPlan, error) {
			edit := statusEdit{Name: change.Name, Label: change.Label}
			if change.Tags != nil {
				tags, err := parseStatusTags(*change.Tags)
				if err != nil {
					return statusPlan{}, err
				}
				edit.Tags = &tags
			}
			return planStatusEdit(ctx, scope, vocabulary, status, edit)
		})
}

func (board *boardVocabulary) remove(
	ctx context.Context,
	status core.Status,
	removal webui.VocabularyStatusRemoval,
) (webui.VocabularyMutation, error) {
	return board.apply(ctx, removal.ExpectedHead,
		func(scope statusScope, vocabulary core.Vocabulary) (statusPlan, error) {
			// The destination is required and never guessed, for the reason
			// `status delete` never guesses it: the tasks in the column have to
			// go somewhere, and only the person removing it knows where. The
			// refusal names this project's statuses so the retry is one edit
			// away, exactly as the verb's does.
			if removal.Into == "" {
				return statusPlan{}, core.Errorf(core.CategoryInvocation,
					"removing a status requires naming where its tasks belong; "+
						"this project's statuses are: %s", statusNameList(vocabulary))
			}
			return planStatusDelete(ctx, scope, vocabulary, status, removal.Into)
		})
}

func (board *boardVocabulary) reorder(
	ctx context.Context,
	order webui.VocabularyOrder,
) (webui.VocabularyMutation, error) {
	return board.apply(ctx, order.ExpectedHead,
		func(scope statusScope, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusOrder(ctx, scope, vocabulary, order.Statuses)
		})
}

// apply is the one path every vocabulary change from the board takes.
//
// It is runStatusMutation with the three differences a server has from a
// command, and each of them is a decision rather than an omission:
//
//   - It fetches before authoring, exactly as the verbs do and for the same
//     reason — a change composed against a stale ledger would be refused as a
//     stale write that nothing was actually wrong with. A trustworthy watcher
//     answering means the tip is already current within its staleness window,
//     so the round trip is skipped; that is the CLI's own rule.
//   - The head the client named must be the head the write lands on, and a
//     mismatch is refused rather than resolved. Nothing here rebases or merges:
//     two people renaming the same column mean two different things, and a
//     server that applied both would invent a third that neither of them chose.
//     The head is checked here and again where it counts — WriteConfigOperationOnto
//     hands it to the ref transaction — so a change landing while this one is
//     being authored is refused rather than silently accepted.
//   - It regenerates no documentation. The verbs rewrite the generated
//     guidelines because a person ran them in a working tree they were looking
//     at; this is a server that may be answering while somebody rebases the
//     checkout it lives in, and writing a tracked file on an HTTP request is not
//     a thing a board should do behind their back. The ledger is canonical and
//     the file is a rendering of it, so the change is recorded and the staleness
//     is reported — the next status verb or `workbook docs update` settles it,
//     which is the correct-on-touch rule the rest of Workbook already follows.
//
// Two changes racing are settled by the ledger's own compare-and-swap, which is
// what serializes every other writer of this ref: the loser wrote nothing, and
// reads as a stale write to the client that lost.
func (board *boardVocabulary) apply(
	ctx context.Context,
	expectedHead string,
	build func(statusScope, core.Vocabulary) (statusPlan, error),
) (webui.VocabularyMutation, error) {
	fetchConfigBefore(ctx, board.repository, board.config, board.publisher)
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		return webui.VocabularyMutation{}, err
	}
	// Cheap and early, so a change nobody could land does not first read the
	// project's tasks and author a plan. The enforcement is at the write.
	if state.Head != expectedHead {
		return webui.VocabularyMutation{}, staleVocabularyWrite(expectedHead)
	}
	plan, err := build(statusScope{
		repository: board.repository,
		config:     board.config,
		service:    board.service(state.Vocabulary),
	}, state.Vocabulary)
	if err != nil {
		return webui.VocabularyMutation{}, err
	}

	// The head travels all the way to the ref transaction. The comparison above
	// is what saves the work of authoring a change that cannot land; this is
	// what makes the promise, because between that read and this write is
	// exactly where a teammate's `workbook status rename` fits.
	written, err := board.repository.WriteConfigOperationOnto(
		ctx, board.config, core.CryptoULIDSource{}, plan.operations, configCommitSubject(plan), expectedHead)
	if err != nil {
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			// Whether the ledger moved while this change was being composed or
			// while it was being written, the reader's situation is the same one
			// and reads the same way.
			return webui.VocabularyMutation{}, staleVocabularyWrite(expectedHead)
		}
		return webui.VocabularyMutation{}, err
	}
	after := written.Vocabulary()
	return webui.VocabularyMutation{
		State: webui.VocabularyState{Vocabulary: after, Head: written.Head},
		Tasks: webui.VocabularyTaskCounts{
			Affected:       plan.tasks.Affected,
			ClaimableAfter: plan.tasks.ClaimableAfter,
		},
		Warnings: append(board.publisher.publishConfig(ctx), staleGuidelinesWarnings(board, after)...),
	}, nil
}

// fetchConfigBefore refreshes the configuration ledger before a change is
// authored against it.
//
// It is best-effort in the same way the CLI's is: an unreachable origin is not a
// reason to refuse a local, durable write, and whatever the fetch could not do
// the compare-and-swap still catches. A trustworthy watcher answering means this
// clone is already synchronizing on its own schedule, and `serve` usually is
// that watcher, so the common case costs one socket probe rather than a network
// round trip inside a request.
//
// It is a function rather than a method because both of the board's writers into
// that ledger need it — the statuses and the display settings — and they need
// exactly the same thing: there is one ledger, and its tip is refreshed the same
// way whichever half of it is about to move.
func fetchConfigBefore(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	publisher *boardPublisher,
) {
	if publisher.watcherAnswers() || !repository.HasOrigin(ctx) {
		return
	}
	_, _ = repository.Fetch(ctx, config)
}

// staleVocabularyWrite refuses a change composed against statuses that have
// since moved.
//
// It says what the client has to do rather than what happened to the ref,
// because the client can do exactly one thing about it: re-read the vocabulary
// — which travels with this refusal — and let the person decide again with the
// current columns in front of them.
func staleVocabularyWrite(expected string) error {
	if expected == "" {
		return core.Errorf(core.CategoryStaleWrite,
			"this project's statuses have been configured since this change was composed; reload and try again")
	}
	return core.Errorf(core.CategoryStaleWrite,
		"this project's statuses have changed since %s; reload and try again", expected)
}

// staleGuidelinesWarnings reports that the generated guidelines now describe
// statuses this project no longer has, since the board deliberately does not
// rewrite them. It is best-effort: a file it cannot read is not a reason to
// report a recorded, published change as anything but recorded.
func staleGuidelinesWarnings(board *boardVocabulary, vocabulary core.Vocabulary) []core.Warning {
	state, err := agentdocs.GuidelinesState(agentdocs.Options{
		Root:       board.repository.Root,
		Project:    board.config,
		Vocabulary: vocabulary,
		Generator:  release.Version,
	})
	if err != nil {
		return nil
	}
	switch state {
	case agentdocs.StateModified:
		return []core.Warning{{
			Code:    core.WarningDocsRefresh,
			Message: docsBlockedMessage(agentdocs.GuidelinesPath),
		}}
	case agentdocs.StateStale:
		return []core.Warning{{
			Code: core.WarningDocsRefresh,
			Message: fmt.Sprintf(
				"the status change was recorded, but %s still describes this project's previous "+
					"statuses; the board does not write files, so refresh it with: workbook docs update",
				agentdocs.GuidelinesPath),
		}}
	default:
		return nil
	}
}
