package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityLabel changes what a priority is called without touching the value
// stored on its tasks.
func runPriorityLabel(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority label", []string{"<priority>", "<display-label>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	display := values[1]
	if err := core.ValidatePriorityLabel(display); err != nil {
		return err
	}

	return runPriorityMutation(ctx, cwd, "priority label", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityRelabel(ctx, session.priorityScope(), vocabulary, core.Priority(values[0]), display)
		})
}

// planPriorityRelabel authors the label change, and refuses the one that would
// record nothing.
//
// The change carries no LabelDerived: that member answers a rename's question —
// whether a custom label was kept — and a caller who typed a label chose it, so
// reporting it as derived or kept would be answering a question nobody asked.
func planPriorityRelabel(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority core.Priority,
	display string,
) (priorityPlan, error) {
	if err := core.ValidatePriorityLabel(display); err != nil {
		return priorityPlan{}, err
	}
	subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
	if err != nil {
		return priorityPlan{}, err
	}
	current := vocabulary.Label(subject)
	if current == display {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q already has that label", subject)
	}
	operation := core.ConfigOperation{Type: core.ConfigPriorityRelabel, Priority: subject, Label: display}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "label",
			Priority:  subject,
			Label:     &priorityLabel{From: current, To: display},
			Tags:      priorityTags(vocabulary, subject),
		},
	}, nil
}
