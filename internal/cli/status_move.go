package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusMove(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status move", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "move")
	before := flags.String("before", "", "move before this status")
	after := flags.String("after", "", "move after this status")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "status move requires exactly one of --before or --after")
	}

	return runStatusMutation(ctx, cwd, "status move", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			placeBefore := *before != ""
			anchor := core.Status(*after)
			if placeBefore {
				anchor = core.Status(*before)
			}
			return planStatusMove(ctx, session.statusScope(), vocabulary,
				core.Status(values[0]), anchor, placeBefore)
		})
}

// planStatusMove places one status next to another, leaving every other status
// where it is.
func planStatusMove(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status, anchor core.Status,
	placeBefore bool,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	resolvedAnchor, err := requireLiveStatus(ctx, scope, vocabulary, anchor)
	if err != nil {
		return statusPlan{}, err
	}
	if resolvedAnchor == subject {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"cannot move status %q relative to itself", subject)
	}
	rank, err := vocabulary.InsertRank(subject, resolvedAnchor, placeBefore)
	if err != nil {
		return statusPlan{}, err
	}
	position := &statusPosition{Rank: rank}
	if placeBefore {
		position.Before = resolvedAnchor
	} else {
		position.After = resolvedAnchor
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusReorder, Status: subject, Rank: rank}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "move",
			Status:    subject,
			Position:  position,
			Label:     &statusLabel{To: vocabulary.Label(subject)},
			Tags:      statusTags(vocabulary, subject),
		},
	}, nil
}
