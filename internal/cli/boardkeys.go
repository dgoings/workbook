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

// boardKeys is the key verb family's second surface.
//
// It is boardVocabulary and boardPriorities for the third section of the same
// ledger, and it authors nothing of its own for the same reason: every change
// goes through the planner `workbook key` uses, so the board refuses what the
// CLI refuses, in the same words, and records the same operations — an add is
// still the one operation gitstore back-fills the founding key ahead of, which
// is the pack shape the log and the inverse both classify. A second
// implementation of what retiring a key means is how two surfaces drift until
// they disagree about the same project.
//
// It carries no `service` where its two siblings do, and the omission is a
// property of this section rather than an oversight: the three key planners
// take a bare core.KeySet and read neither the ledger nor the project's tasks,
// because no key change moves a task. There is nothing for a scope to carry.
//
// What differs from the verbs is only what a server can honestly do around the
// write: see apply.
type boardKeys struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	publisher  *boardPublisher
}

// boardKeyAddition is a key the board asks this project to add.
//
// Current carries `workbook key add --current`'s choice, which is one change
// rather than two: the two operations are one pack, authored by one planner,
// and a board that sent them as two requests could land the first and lose the
// second — leaving a key nobody asked for behind.
type boardKeyAddition struct {
	Key     string
	Current bool
	// ExpectedHead is the configuration ledger tip the client composed this
	// change against. It is required here for the reason it is required on a
	// status change: a configuration edit composed against a ledger somebody
	// else has since moved is not a change anybody chose.
	ExpectedHead string
}

// boardKeyEdit is one of the three things that happen to a key this project
// already has. Exactly one member is true; the route refuses a body naming two,
// because there is no planner that reads two of them at once.
type boardKeyEdit struct {
	Current      bool
	Retire       bool
	Reactivate   bool
	ExpectedHead string
}

// boardKeyMutation is what one key change produced: the configuration as it now
// stands, and the tip it was written to.
//
// It prices nothing where both of its siblings do, and the missing member is
// the point. A status removal forwards the tasks in a column and a priority
// removal forwards the tasks at a priority; a key change moves no task at all,
// because a task ID is a permanent name and the key is part of it.
type boardKeyMutation struct {
	// State is the whole configuration, not the keys alone, for the reason a
	// status mutation answers with the priorities: the client adopts a mutation
	// answer wholesale, and an answer that left a section zero would not say
	// "this change was about keys", it would say "this project's statuses are
	// the built-in set". See boardDisplay.set, where that hazard is recorded as
	// something that already happened once.
	State    webui.VocabularyState
	Warnings []core.Warning
}

func (board *boardKeys) add(ctx context.Context, addition boardKeyAddition) (boardKeyMutation, error) {
	return board.apply(ctx, addition.ExpectedHead, func(keys core.KeySet) (keyPlan, error) {
		return planKeyAdd(keys, addition.Key, addition.Current)
	})
}

// edit routes the one intent a PATCH carries to the planner that owns it.
//
// Reactivation reaches planKeyAdd rather than a planner of its own, which is
// the same routing `workbook key add` takes for a retired key: bringing a key
// back is adding it, and the fold's convergence on an add it already has is
// what makes the two the same operation. The route has already refused a body
// naming more than one intent, so the default arm is unreachable over HTTP —
// it is here because a bare boardKeyEdit is constructible in this package, and
// a silent no-op would be the wrong answer to one.
func (board *boardKeys) edit(ctx context.Context, key string, change boardKeyEdit) (boardKeyMutation, error) {
	return board.apply(ctx, change.ExpectedHead, func(keys core.KeySet) (keyPlan, error) {
		switch {
		case change.Current:
			return planKeyCurrent(keys, key)
		case change.Retire:
			return planKeyRetire(keys, key)
		case change.Reactivate:
			return planKeyAdd(keys, key, false)
		default:
			return keyPlan{}, core.Errorf(core.CategoryInvocation,
				"a key change names exactly one of current, retire or reactivate")
		}
	})
}

// apply is the one path every key change from the board takes.
//
// It is runKeyMutation with the three differences a server has from a command,
// which are boardVocabulary.apply's three differences over another section of
// the same ledger, and each of them is a decision rather than an omission:
//
//   - It fetches before authoring, exactly as the verbs do and for the same
//     reason — a change composed against a stale ledger would be refused as a
//     stale write that nothing was actually wrong with. A trustworthy watcher
//     answering means the tip is already current within its staleness window,
//     so the round trip is skipped; that is the CLI's own rule.
//   - The head the client named must be the head the write lands on, and a
//     mismatch is refused rather than resolved. Nothing here rebases or merges:
//     two people moving the same project's current key mean two different
//     things, and a server that applied both would invent a third that neither
//     of them chose.
//   - It regenerates no documentation, where `workbook key` does. The generated
//     guidelines name the current key, so a key change leaves them as stale as
//     a status change does — but this is a server that may be answering while
//     somebody rebases the checkout it lives in, and writing a tracked file on
//     an HTTP request is not a thing a board should do behind their back. The
//     ledger is canonical and the file is a rendering of it, so the change is
//     recorded and the staleness is reported; the next key verb or `workbook
//     docs update` settles it.
//
// Two changes racing are settled by the ledger's own compare-and-swap, which is
// what serializes every other writer of this ref: the loser wrote nothing, and
// reads as a stale write to the client that lost.
func (board *boardKeys) apply(
	ctx context.Context,
	expectedHead string,
	build func(core.KeySet) (keyPlan, error),
) (boardKeyMutation, error) {
	fetchConfigBefore(ctx, board.repository, board.config, board.publisher)
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		return boardKeyMutation{}, err
	}
	// Cheap and early, so a change nobody could land does not first author a
	// plan. The enforcement is at the write.
	if state.Head != expectedHead {
		return boardKeyMutation{}, staleKeyWrite(expectedHead)
	}
	plan, err := build(state.Keys)
	if err != nil {
		return boardKeyMutation{}, err
	}

	// The head travels all the way to the ref transaction. The comparison above
	// is what saves the work of authoring a change that cannot land; this is
	// what makes the promise, because between that read and this write is
	// exactly where a teammate's `workbook key current` fits.
	written, err := board.repository.WriteConfigOperationOnto(
		ctx, board.config, core.CryptoULIDSource{}, plan.operations, keyCommitSubject(plan), expectedHead)
	if err != nil {
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			// Whether the ledger moved while this change was being composed or
			// while it was being written, the reader's situation is the same one
			// and reads the same way.
			return boardKeyMutation{}, staleKeyWrite(expectedHead)
		}
		return boardKeyMutation{}, err
	}
	after := written.KeySet(board.config.Key)
	// The statuses and the priorities as this write left them, read off the same
	// result the keys are, so the answer below describes one configuration
	// rather than this project's keys and some other project's columns — and so
	// the staleness report compares the file against one configuration too.
	vocabulary := written.Vocabulary()
	priorities := written.PriorityVocabulary()
	display := written.Display()
	return boardKeyMutation{
		State: webui.VocabularyState{
			Vocabulary: vocabulary, Head: written.Head, Display: display, Priorities: priorities, Keys: after,
		},
		Warnings: append(board.publisher.publishConfig(ctx),
			staleKeyGuidelinesWarnings(board, vocabulary, priorities, after)...),
	}, nil
}

// staleKeyWrite refuses a change composed against keys that have since moved.
//
// It says what the client has to do rather than what happened to the ref,
// because the client can do exactly one thing about it: re-read the
// configuration — which travels with this refusal — and let the person decide
// again with the current keys in front of them. It names the keys where
// staleVocabularyWrite names the statuses, because a reader told their statuses
// changed while they were retiring a key would go looking for a change nobody
// made.
func staleKeyWrite(expected string) error {
	if expected == "" {
		return core.Errorf(core.CategoryStaleWrite,
			"this project's keys have been configured since this change was composed; reload and try again")
	}
	return core.Errorf(core.CategoryStaleWrite,
		"this project's keys have changed since %s; reload and try again", expected)
}

// staleKeyGuidelinesWarnings reports that the generated guidelines now describe
// a key this project no longer mints under, since the board deliberately does
// not rewrite them. It is best-effort: a file it cannot read is not a reason to
// report a recorded, published change as anything but recorded.
//
// It takes the statuses and the priorities as well as the keys although it
// changes neither, for the reason stalePriorityGuidelinesWarnings takes the
// statuses: the comparison renders the whole document, so a reader supplied with
// only the part that moved would find a difference it had just invented.
func staleKeyGuidelinesWarnings(
	board *boardKeys,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
	keys core.KeySet,
) []core.Warning {
	state, err := agentdocs.GuidelinesState(agentdocs.Options{
		Root:       board.repository.Root,
		Project:    board.config,
		Vocabulary: vocabulary,
		Priorities: priorities,
		Keys:       keys,
		Generator:  release.Version,
	})
	if err != nil {
		return nil
	}
	switch state {
	case agentdocs.StateModified:
		return []core.Warning{{
			Code: core.WarningDocsRefresh,
			Message: fmt.Sprintf(
				"the key change was recorded, but %s was modified locally and now describes "+
					"keys this project no longer has; overwrite it with: workbook docs update --force",
				agentdocs.GuidelinesPath),
		}}
	case agentdocs.StateStale:
		return []core.Warning{{
			Code: core.WarningDocsRefresh,
			Message: fmt.Sprintf(
				"the key change was recorded, but %s still describes this project's previous "+
					"keys; the board does not write files, so refresh it with: workbook docs update",
				agentdocs.GuidelinesPath),
		}}
	default:
		return nil
	}
}
