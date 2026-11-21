package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusUntag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status untag", []string{"<status>", "<tag>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "untag")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	tag := core.StatusTag(values[1])
	if err := core.ValidateStatusTag(tag); err != nil {
		return statusTagError(err)
	}

	return runStatusMutation(ctx, cwd, "status untag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusUntag(ctx, session.statusScope(), vocabulary, core.Status(values[0]), tag)
		})
}

// planStatusUntag takes one role away from a status.
func planStatusUntag(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	tag core.StatusTag,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := statusTags(vocabulary, subject)
	if !containsStatusTag(current, tag) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q does not carry the %q tag", subject, tag)
	}
	remaining := make([]core.StatusTag, 0, len(current))
	for _, candidate := range current {
		if candidate != tag {
			remaining = append(remaining, candidate)
		}
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusUntag, Status: subject, Tag: tag}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "untag",
			Status:    subject,
			Tags:      remaining,
		},
	}, nil
}
