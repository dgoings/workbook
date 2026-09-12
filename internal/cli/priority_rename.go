package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityRename gives a priority a new machine value, leaving every task
// stored under the old one to resolve through the rename.
func runPriorityRename(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority rename", []string{"<priority>", "<new-priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "rename")
	label := flags.String("label", "", "display label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	from, to := core.Priority(values[0]), core.Priority(values[1])
	if err := core.ValidatePriorityToken(to); err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidatePriorityLabel(*label); err != nil {
			return err
		}
	}

	return runPriorityMutation(ctx, cwd, "priority rename", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityRename(ctx, session.priorityScope(), vocabulary, from, to, *label)
		})
}

// planPriorityRename moves a priority onto a new value, keeping a label
// somebody chose and re-deriving one nobody did.
//
// label is what the caller asked for, and empty means they asked for nothing,
// which is the case the derived-label rule is about. The rule is
// planStatusRename's, unchanged, down to emitting the relabel only when the
// label actually moves: a pack that recorded a relabel to the value the label
// already had would make the log say a label changed when nothing did, and
// would make the rename's inverse carry a --label it does not need.
func planPriorityRename(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	from, to core.Priority,
	label string,
) (priorityPlan, error) {
	if err := core.ValidatePriorityToken(to); err != nil {
		return priorityPlan{}, err
	}
	subject, err := requireLivePriority(ctx, scope, vocabulary, from)
	if err != nil {
		return priorityPlan{}, err
	}
	if subject == to {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q already has that value", to)
	}
	if vocabulary.Has(to) {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines priority %q", to)
	}

	// The derived-label rule. A label nobody chose follows the name it was
	// derived from; a label somebody chose is theirs and survives a rename of
	// the machine value underneath it.
	current := vocabulary.Label(subject)
	display, derived := current, false
	switch {
	case label != "":
		display = label
	case current == core.DerivedPriorityLabel(subject):
		display, derived = core.DerivedPriorityLabel(to), true
	}
	if err := core.ValidatePriorityLabel(display); err != nil {
		return priorityPlan{}, err
	}

	rename := core.ConfigOperation{Type: core.ConfigPriorityRename, PriorityFrom: subject, PriorityTo: to}
	operations := []core.ConfigOperation{rename}
	if display != current {
		operations = append(operations, core.ConfigOperation{
			Type: core.ConfigPriorityRelabel, Priority: to, Label: display,
		})
	}
	labelDerived := derived
	return priorityPlan{
		operations: operations,
		change: priorityChange{
			Operation:    "rename",
			Priority:     to,
			From:         subject,
			Label:        &priorityLabel{From: current, To: display},
			LabelDerived: &labelDerived,
			Tags:         priorityTags(vocabulary, subject),
		},
	}, nil
}
