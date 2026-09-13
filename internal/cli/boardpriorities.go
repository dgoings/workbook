package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/webui"
)

// boardPriorities is the priority verb family's second surface.
//
// It is boardVocabulary for the other section of the same ledger, and it
// authors nothing of its own for the same reason: every change goes through the
// planner `workbook priority` uses, so the board refuses what the CLI refuses,
// in the same words, and records the same operations — a rename is still a
// rename followed by the relabel the derived-label rule asks for, which is the
// pack shape reconciliation knows how to classify. A second implementation of
// what a rename or a removal means is how two surfaces drift until they
// disagree about the same project.
//
// What differs from the verbs is only what a server can honestly do around the
// write: see apply.
type boardPriorities struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	publisher  *boardPublisher
	// service reads the project's tasks against a given vocabulary, for the one
	// change that has to count them. It takes the vocabulary rather than reading
	// one, so a removal counts what it moves against the same priorities it is
	// authored against.
	service func(core.PriorityVocabulary) core.Service
}

// boardPriorityAddition is a priority the board asks this project to define.
//
// It is the verb's own priorityAddition plus the head the client composed the
// change against, rather than a second spelling of the same four members: what
// `workbook priority add` resolves its argument and flags into is exactly what
// the planner takes, and the board has nothing to add to it but the head.
type boardPriorityAddition struct {
	priorityAddition
	// ExpectedHead is the configuration ledger tip the client composed this
	// change against. It is required here for the reason it is required on a
	// status change: a vocabulary edit composed against columns somebody else
	// has since moved is not a change anybody chose.
	ExpectedHead string
}

// boardPriorityEdit renames and relabels one priority, in any subset: a nil
// member is a member this change does not touch, which is what lets one form
// send one intent.
//
// It has no Tags member, where a status edit has one. A priority carries
// exactly one role — `default`, held by exactly one priority at a time — so
// there is no tag set to reconcile: setDefault transfers it in the single
// operation the fold already understands.
type boardPriorityEdit struct {
	Name         *core.Priority
	Label        *string
	ExpectedHead string
}

// boardPriorityRemoval removes a priority. Into is where its tasks belong and
// is never guessed: a removal with nowhere to forward to is a removal nobody
// could have meant.
type boardPriorityRemoval struct {
	Into         core.Priority
	ExpectedHead string
}

// boardPriorityMove moves a priority among its peers.
//
// It names exactly one anchor, because `--before` and `--after` describe the
// same position from opposite sides: naming both is a contradiction rather than
// a stronger instruction, and naming neither is a move with nowhere to go.
//
// There is no whole-order counterpart to VocabularyOrder here, because there is
// no planner for one: a status drag is authored by planStatusOrder, which sets
// every rank at once, and the priority section's planner is planPriorityMove,
// which names a neighbour. Writing an order planner for priorities would be
// authoring a change the CLI has no reading of.
type boardPriorityMove struct {
	Before       core.Priority
	After        core.Priority
	ExpectedHead string
}

// boardPriorityDefault gives one priority the role a new task lands on. It
// carries only a head because the change is the subject itself: the fold takes
// the tag off whoever held it.
type boardPriorityDefault struct {
	ExpectedHead string
}

// boardPriorityRecolor sets or clears the ink one priority is drawn in. An
// empty Color clears the stored value and returns the priority to the color the
// board derives from its position.
type boardPriorityRecolor struct {
	Color        string
	ExpectedHead string
}

// boardPriorityMutation is what one priority change produced: the configuration
// as it now stands, the tip it was written to, and what it cost.
//
// It carries priorityTaskCounts rather than webui.VocabularyTaskCounts, and the
// missing member is missing on purpose. `claimableAfter` answers whether moved
// tasks become eligible for `workbook next`, and eligibility is decided by a
// status's tags and a task's dependencies; nothing about a priority gates it.
// See priorityTaskCounts' own comment: a member reporting how many became
// claimable would be zero for every priority change forever, and a client
// reading one would be reading an answer this section cannot give.
type boardPriorityMutation struct {
	// State is the whole vocabulary, not the priorities alone, for the reason
	// a status mutation answers with the priorities: the client adopts a
	// mutation answer wholesale, and an answer that left a section zero would
	// not say "this change was about priorities", it would say "this project's
	// statuses are the built-in set".
	//
	// Its Display member is left zero, exactly as a status mutation leaves it.
	// The display section did not move, and the document these render into
	// omits an unconfigured one rather than writing an empty one, so a reader
	// keeps what it already has.
	State    webui.VocabularyState
	Tasks    priorityTaskCounts
	Warnings []core.Warning
}

func (board *boardPriorities) add(
	ctx context.Context,
	addition boardPriorityAddition,
) (boardPriorityMutation, error) {
	return board.apply(ctx, addition.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			// Refused here rather than left to the planner, which prefers After
			// silently. `priority add` refuses the same invocation, and a board
			// that quietly picked one of two contradictory placements would put
			// a priority somewhere nobody asked for.
			if addition.Before != "" && addition.After != "" {
				return priorityPlan{}, core.Errorf(core.CategoryInvocation,
					"a priority is placed before or after another priority, not both")
			}
			return planPriorityAdd(ctx, scope, vocabulary, addition.priorityAddition)
		})
}

// edit renames and relabels one priority.
//
// Each request reaches exactly one planner, which is what keeps this from
// becoming a second reading of what an edit means: planPriorityRename already
// takes the label, so a form that moved both members is the rename planner's
// own case, and a form that moved only the label is planPriorityRelabel's.
//
// A member that repeats what the priority already says is not a mistake here,
// which is where this parts company with the verbs: a form sends every field it
// has, so "rename it to the name it has" is the client saying leave it alone.
// When that is the whole change, the verb's own refusal stands — the rename
// planner is still the one that answers.
func (board *boardPriorities) edit(
	ctx context.Context,
	priority core.Priority,
	change boardPriorityEdit,
) (boardPriorityMutation, error) {
	return board.apply(ctx, change.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
			if err != nil {
				return priorityPlan{}, err
			}
			if change.Name == nil && change.Label == nil {
				return priorityPlan{}, core.Errorf(core.CategoryValidation,
					"priority %q was given nothing to change", subject)
			}
			// A label somebody sent is a label they chose, blank included, so it
			// is validated before the rename sees it: planPriorityRename reads an
			// empty label as "nothing was asked for" and derives one, which is
			// right for a flag nobody typed and wrong for a member somebody
			// emptied.
			if change.Label != nil {
				if err := core.ValidatePriorityLabel(*change.Label); err != nil {
					return priorityPlan{}, err
				}
			}
			if change.Name != nil && *change.Name != subject {
				label := ""
				if change.Label != nil {
					label = *change.Label
				}
				return planPriorityRename(ctx, scope, vocabulary, subject, *change.Name, label)
			}
			if change.Label != nil {
				return planPriorityRelabel(ctx, scope, vocabulary, subject, *change.Label)
			}
			// The name this priority already has, and nothing else: the verb's
			// own refusal, in the verb's own words.
			return planPriorityRename(ctx, scope, vocabulary, subject, *change.Name, "")
		})
}

func (board *boardPriorities) remove(
	ctx context.Context,
	priority core.Priority,
	removal boardPriorityRemoval,
) (boardPriorityMutation, error) {
	return board.apply(ctx, removal.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			// The destination is required and never guessed, for the reason
			// `priority delete` never guesses it: the tasks filed under it have
			// to go somewhere, and only the person removing it knows where. The
			// refusal names this project's priorities so the retry is one edit
			// away, exactly as the verb's does — without naming --into, which is
			// a flag this surface does not have.
			if removal.Into == "" {
				return priorityPlan{}, core.Errorf(core.CategoryInvocation,
					"removing a priority requires naming where its tasks belong; "+
						"this project's priorities are: %s", priorityNameList(vocabulary))
			}
			return planPriorityDelete(ctx, scope, vocabulary, priority, removal.Into)
		})
}

func (board *boardPriorities) move(
	ctx context.Context,
	priority core.Priority,
	move boardPriorityMove,
) (boardPriorityMutation, error) {
	return board.apply(ctx, move.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			switch {
			case move.Before != "" && move.After != "":
				return priorityPlan{}, core.Errorf(core.CategoryInvocation,
					"a priority moves before or after another priority, not both")
			case move.Before == "" && move.After == "":
				return priorityPlan{}, core.Errorf(core.CategoryInvocation,
					"moving a priority requires naming the priority it goes before or after")
			}
			anchor, placeBefore := move.After, move.Before != ""
			if placeBefore {
				anchor = move.Before
			}
			return planPriorityMove(ctx, scope, vocabulary, priority, anchor, placeBefore)
		})
}

// setDefault gives one priority the role a task with no priority lands on.
//
// It is one operation rather than a tag set reconciled across the list: a
// priority carries exactly one role, and the fold takes it off whoever held it
// when it grants it here. There is no clearing counterpart, because a project
// with no default priority is a project where a new task has nowhere to land —
// the vocabulary's own validation refuses it.
func (board *boardPriorities) setDefault(
	ctx context.Context,
	priority core.Priority,
	change boardPriorityDefault,
) (boardPriorityMutation, error) {
	return board.apply(ctx, change.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityTag(ctx, scope, vocabulary, priority, core.PriorityTagDefault)
		})
}

// recolor sets or clears the ink one priority is drawn in.
//
// The refusal worth knowing about is planPriorityColor's: a recolor that
// records nothing — the color a priority already has, or clearing one it does
// not have — is refused rather than accepted as a no-op, and this passes that
// refusal through in the CLI's own words. It is not pedantry about an empty
// commit. A priority write on a project that has configured none backfills the
// built-in three and stamps the priority section's generation marker, which
// parks every teammate below that generation on an older build for the life of
// the project; a change that changes nothing must not cost a team that. See
// planPriorityColor's own comment for the whole reasoning.
func (board *boardPriorities) recolor(
	ctx context.Context,
	priority core.Priority,
	change boardPriorityRecolor,
) (boardPriorityMutation, error) {
	return board.apply(ctx, change.ExpectedHead,
		func(scope priorityScope, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			// The verb's own reading of what the caller sent: surrounding space
			// is not part of a color, and an empty value is the clearing rather
			// than a color to validate, because nothing renders it.
			color := ""
			if trimmed := strings.TrimSpace(change.Color); trimmed != "" {
				canonical, err := core.ValidateThemeColor(trimmed)
				if err != nil {
					return priorityPlan{}, err
				}
				color = canonical
			}
			return planPriorityColor(ctx, scope, vocabulary, priority, color)
		})
}

// apply is the one path every priority change from the board takes.
//
// It is runPriorityMutation with the three differences a server has from a
// command, which are boardVocabulary.apply's three differences over the other
// section of the same ledger, and each of them is a decision rather than an
// omission:
//
//   - It fetches before authoring, exactly as the verbs do and for the same
//     reason — a change composed against a stale ledger would be refused as a
//     stale write that nothing was actually wrong with. A trustworthy watcher
//     answering means the tip is already current within its staleness window,
//     so the round trip is skipped; that is the CLI's own rule.
//   - The head the client named must be the head the write lands on, and a
//     mismatch is refused rather than resolved. Nothing here rebases or merges:
//     two people renaming the same priority mean two different things, and a
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
//     is reported — the next priority verb or `workbook docs update` settles it.
//
// Two changes racing are settled by the ledger's own compare-and-swap, which is
// what serializes every other writer of this ref: the loser wrote nothing, and
// reads as a stale write to the client that lost.
func (board *boardPriorities) apply(
	ctx context.Context,
	expectedHead string,
	build func(priorityScope, core.PriorityVocabulary) (priorityPlan, error),
) (boardPriorityMutation, error) {
	fetchConfigBefore(ctx, board.repository, board.config, board.publisher)
	state, err := board.repository.LoadVocabularyState(ctx, board.config)
	if err != nil {
		return boardPriorityMutation{}, err
	}
	// Cheap and early, so a change nobody could land does not first read the
	// project's tasks and author a plan. The enforcement is at the write.
	if state.Head != expectedHead {
		return boardPriorityMutation{}, stalePriorityWrite(expectedHead)
	}
	plan, err := build(priorityScope{
		repository: board.repository,
		config:     board.config,
		service:    board.service(state.Priorities),
	}, state.Priorities)
	if err != nil {
		return boardPriorityMutation{}, err
	}

	// The head travels all the way to the ref transaction. The comparison above
	// is what saves the work of authoring a change that cannot land; this is
	// what makes the promise, because between that read and this write is
	// exactly where a teammate's `workbook priority rename` fits.
	written, err := board.repository.WriteConfigOperationOnto(
		ctx, board.config, core.CryptoULIDSource{}, plan.operations, priorityCommitSubject(plan), expectedHead)
	if err != nil {
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			// Whether the ledger moved while this change was being composed or
			// while it was being written, the reader's situation is the same one
			// and reads the same way.
			return boardPriorityMutation{}, stalePriorityWrite(expectedHead)
		}
		return boardPriorityMutation{}, err
	}
	after := written.PriorityVocabulary()
	// The statuses as this write left them, read off the same result the
	// priorities are, so the staleness report below compares the file against
	// one configuration rather than this project's priorities and some other
	// project's statuses.
	vocabulary := written.Vocabulary()
	return boardPriorityMutation{
		State: webui.VocabularyState{Vocabulary: vocabulary, Head: written.Head, Priorities: after},
		Tasks: plan.tasks,
		Warnings: append(board.publisher.publishConfig(ctx),
			stalePriorityGuidelinesWarnings(board, vocabulary, after)...),
	}, nil
}

// stalePriorityWrite refuses a change composed against priorities that have
// since moved.
//
// It says what the client has to do rather than what happened to the ref,
// because the client can do exactly one thing about it: re-read the
// configuration — which travels with this refusal — and let the person decide
// again with the current priorities in front of them. It names the priorities
// where staleVocabularyWrite names the statuses, because a reader told their
// statuses changed while they were recoloring a priority would go looking for a
// change nobody made.
func stalePriorityWrite(expected string) error {
	if expected == "" {
		return core.Errorf(core.CategoryStaleWrite,
			"this project's priorities have been configured since this change was composed; reload and try again")
	}
	return core.Errorf(core.CategoryStaleWrite,
		"this project's priorities have changed since %s; reload and try again", expected)
}

// stalePriorityGuidelinesWarnings reports that the generated guidelines now
// describe priorities this project no longer has, since the board deliberately
// does not rewrite them. It is best-effort: a file it cannot read is not a
// reason to report a recorded, published change as anything but recorded.
//
// It takes the statuses as well as the priorities although it changes neither,
// for the reason staleGuidelinesWarnings takes the priorities: the comparison
// renders the whole document, so a reader supplied with only the half that
// moved would find a difference it had just invented.
func stalePriorityGuidelinesWarnings(
	board *boardPriorities,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
) []core.Warning {
	state, err := agentdocs.GuidelinesState(agentdocs.Options{
		Root:       board.repository.Root,
		Project:    board.config,
		Vocabulary: vocabulary,
		Priorities: priorities,
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
				"the priority change was recorded, but %s was modified locally and now describes "+
					"priorities this project no longer has; overwrite it with: workbook docs update --force",
				agentdocs.GuidelinesPath),
		}}
	case agentdocs.StateStale:
		return []core.Warning{{
			Code: core.WarningDocsRefresh,
			Message: fmt.Sprintf(
				"the priority change was recorded, but %s still describes this project's previous "+
					"priorities; the board does not write files, so refresh it with: workbook docs update",
				agentdocs.GuidelinesPath),
		}}
	default:
		return nil
	}
}
