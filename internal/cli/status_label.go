package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusLabel(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status label", []string{"<status>", "<display-label>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	display := values[1]
	if err := core.ValidateStatusLabel(display); err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status label", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusRelabel(ctx, session.statusScope(), vocabulary, core.Status(values[0]), display)
		})
}

// planStatusRelabel changes what a column is called without touching the value
// stored on its tasks.
func planStatusRelabel(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	display string,
) (statusPlan, error) {
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := vocabulary.Label(subject)
	if current == display {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already has that label", subject)
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusRelabel, Status: subject, Label: display}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "label",
			Status:    subject,
			Label:     &statusLabel{From: current, To: display},
			Tags:      statusTags(vocabulary, subject),
		},
	}, nil
}
