package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityAdd defines a priority this project does not have.
//
// It is runStatusAdd's counterpart and reads like it, with one difference worth
// stating rather than leaving to be noticed: there is no --tag. A status
// carries three roles and `status add` takes the set it is born with; a
// priority carries exactly one role, `default`, and exactly one priority holds
// it at a time, so an added priority is born holding none and `workbook
// priority tag` transfers it in a single operation afterwards.
func runPriorityAdd(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority add", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "add")
	label := flags.String("label", "", "display label")
	before := flags.String("before", "", "place before this priority")
	after := flags.String("after", "", "place after this priority")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *before != "" && *after != "" {
		return core.Errorf(core.CategoryInvocation, "priority add accepts --before or --after, not both")
	}

	priority := core.Priority(values[0])
	if err := core.ValidatePriorityToken(priority); err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidatePriorityLabel(*label); err != nil {
			return err
		}
	}

	return runPriorityMutation(ctx, cwd, "priority add", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityAdd(ctx, session.priorityScope(), vocabulary, priorityAddition{
				Priority: priority,
				Label:    *label,
				Before:   core.Priority(*before),
				After:    core.Priority(*after),
			})
		})
}

// priorityAddition is one priority somebody is defining: the verb's argument
// and its flags, resolved into the terms the plan is authored in.
type priorityAddition struct {
	Priority core.Priority
	// Label empty derives one from the name.
	Label  string
	Before core.Priority
	After  core.Priority
}

// planPriorityAdd authors the addition against the vocabulary the fetch
// settled on, optionally next to a priority this project already has.
//
// The vocabulary it reads may be the built-in three a project that configured
// none is read as having, and that is the case worth understanding: the
// operation this returns is the project's first priority change, and gitstore
// backfills those built-in three into the same commit ahead of it. So the rank
// computed here is a rank among the priorities that commit will record, and
// every task already filed under one of them still resolves afterwards.
func planPriorityAdd(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	addition priorityAddition,
) (priorityPlan, error) {
	if err := core.ValidatePriorityToken(addition.Priority); err != nil {
		return priorityPlan{}, err
	}
	if vocabulary.Has(addition.Priority) {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines priority %q", addition.Priority)
	}
	display := addition.Label
	if display == "" {
		display = core.DerivedPriorityLabel(addition.Priority)
	}
	if err := core.ValidatePriorityLabel(display); err != nil {
		return priorityPlan{}, err
	}

	rank := vocabulary.AppendRank()
	anchor, placeBefore := addition.Before, addition.Before != ""
	if addition.After != "" {
		anchor = addition.After
	}
	position := &priorityPosition{}
	if anchor != "" {
		resolved, err := requireLivePriority(ctx, scope, vocabulary, anchor)
		if err != nil {
			return priorityPlan{}, err
		}
		anchor = resolved
		rank, err = vocabulary.InsertRank("", anchor, placeBefore)
		if err != nil {
			return priorityPlan{}, err
		}
		if placeBefore {
			position.Before = anchor
		} else {
			position.After = anchor
		}
	}
	position.Rank = rank

	operation := core.ConfigOperation{
		Type:         core.ConfigPriorityAdd,
		PriorityName: addition.Priority,
		Label:        display,
		Rank:         rank,
	}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "add",
			Priority:  addition.Priority,
			Position:  position,
			Label:     &priorityLabel{To: display},
		},
	}, nil
}
