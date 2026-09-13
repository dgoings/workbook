package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityMove moves a priority among its peers. It is runStatusMove's
// counterpart, down to requiring exactly one anchor: `--before` and `--after`
// describe the same position from opposite sides, so naming both is not a
// stronger instruction but a contradiction, and naming neither is a move with
// nowhere to go.
func runPriorityMove(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority move", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "move")
	before := flags.String("before", "", "move before this priority")
	after := flags.String("after", "", "move after this priority")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "priority move requires exactly one of --before or --after")
	}

	return runPriorityMutation(ctx, cwd, "priority move", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			placeBefore := *before != ""
			anchor := core.Priority(*after)
			if placeBefore {
				anchor = core.Priority(*before)
			}
			return planPriorityMove(ctx, session.priorityScope(), vocabulary,
				core.Priority(values[0]), anchor, placeBefore)
		})
}

// planPriorityMove reranks one priority and leaves every other where it is.
//
// The rank comes from the vocabulary rather than from arithmetic here, for the
// reason planStatusMove's does: InsertRank is the only place that knows how two
// clones inserting between the same pair reach ranks that still order the same
// way once both are folded.
//
// A move to the position the priority already holds is refused rather than
// recorded, for the reason planStatusOrder refuses an order already in force:
// the first priority change on a project that has never configured its
// priorities backfills the built-in three and stamps the generation-three
// marker, so recording a move that reorders nobody parks every teammate on an
// older build in exchange for nothing.
//
// The change reports the label and the tags a move does not touch, because a
// caller reading one envelope should not have to list the vocabulary to find
// out what the priority it just moved is called.
func planPriorityMove(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority, anchor core.Priority,
	placeBefore bool,
) (priorityPlan, error) {
	subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
	if err != nil {
		return priorityPlan{}, err
	}
	resolvedAnchor, err := requireLivePriority(ctx, scope, vocabulary, anchor)
	if err != nil {
		return priorityPlan{}, err
	}
	if resolvedAnchor == subject {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"cannot move priority %q relative to itself", subject)
	}
	if priorityAlreadyPlaced(vocabulary, subject, resolvedAnchor, placeBefore) {
		if placeBefore {
			return priorityPlan{}, core.Errorf(core.CategoryValidation,
				"priority %q is already directly before %q", subject, resolvedAnchor)
		}
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q is already directly after %q", subject, resolvedAnchor)
	}
	rank, err := vocabulary.InsertRank(subject, resolvedAnchor, placeBefore)
	if err != nil {
		return priorityPlan{}, err
	}
	position := &priorityPosition{Rank: rank}
	if placeBefore {
		position.Before = resolvedAnchor
	} else {
		position.After = resolvedAnchor
	}
	operation := core.ConfigOperation{Type: core.ConfigPriorityReorder, Priority: subject, Rank: rank}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "move",
			Priority:  subject,
			Position:  position,
			Label:     &priorityLabel{To: vocabulary.Label(subject)},
			Tags:      priorityTags(vocabulary, subject),
		},
	}, nil
}

// priorityAlreadyPlaced reports whether the move would leave the order exactly
// as it found it: the subject already sits directly on the side of the anchor
// the caller asked for.
//
// The question is asked positionally, about neighbours, rather than by
// comparing the rank InsertRank would return against the one the subject
// holds. Ranks are reduced rationals chosen between whatever pair the subject
// is being placed between, so a move that moves nothing can still arrive at a
// numerically different rank — 2/1 becoming 5/3 reorders nobody, and a rank
// comparison would call that a change and record one. "Is high already right
// after urgent?" is the question a person asking for this move is asking, and
// it is the one with a stable answer.
func priorityAlreadyPlaced(vocabulary core.PriorityVocabulary, subject, anchor core.Priority, placeBefore bool) bool {
	definitions := vocabulary.Definitions()
	subjectIndex, anchorIndex := -1, -1
	for index, definition := range definitions {
		switch definition.Priority {
		case subject:
			subjectIndex = index
		case anchor:
			anchorIndex = index
		}
	}
	if subjectIndex < 0 || anchorIndex < 0 {
		return false
	}
	if placeBefore {
		return subjectIndex+1 == anchorIndex
	}
	return subjectIndex == anchorIndex+1
}
