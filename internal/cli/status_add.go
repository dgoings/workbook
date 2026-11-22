package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusAdd(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status add", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "add")
	label := flags.String("label", "", "display label")
	before := flags.String("before", "", "place before this status")
	after := flags.String("after", "", "place after this status")
	var tags stringListValue
	flags.Var(&tags, "tag", "role to give it")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with the tasks remote (origin unless configured)")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *before != "" && *after != "" {
		return core.Errorf(core.CategoryInvocation, "status add accepts --before or --after, not both")
	}

	status := core.Status(values[0])
	if err := core.ValidateStatusToken(status); err != nil {
		return err
	}
	wanted, err := parseStatusTags(tags.values)
	if err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidateStatusLabel(*label); err != nil {
			return err
		}
	}

	return runStatusMutation(ctx, cwd, "status add", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusAdd(ctx, session.statusScope(), vocabulary, statusAddition{
				Status: status,
				Label:  *label,
				Tags:   wanted,
				Before: core.Status(*before),
				After:  core.Status(*after),
			})
		})
}

// planStatusAdd defines a status this project does not have, optionally next to
// one it does.
func planStatusAdd(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	addition statusAddition,
) (statusPlan, error) {
	if err := core.ValidateStatusToken(addition.Status); err != nil {
		return statusPlan{}, err
	}
	if vocabulary.Has(addition.Status) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines status %q", addition.Status)
	}
	display := addition.Label
	if display == "" {
		display = core.DerivedStatusLabel(addition.Status)
	}
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}

	rank := vocabulary.AppendRank()
	anchor, placeBefore := addition.Before, addition.Before != ""
	if addition.After != "" {
		anchor = addition.After
	}
	position := &statusPosition{}
	if anchor != "" {
		resolved, err := requireLiveStatus(ctx, scope, vocabulary, anchor)
		if err != nil {
			return statusPlan{}, err
		}
		anchor = resolved
		rank, err = vocabulary.InsertRank("", anchor, placeBefore)
		if err != nil {
			return statusPlan{}, err
		}
		if placeBefore {
			position.Before = anchor
		} else {
			position.After = anchor
		}
	}
	position.Rank = rank

	operation := core.ConfigOperation{
		Type:  core.ConfigStatusAdd,
		Name:  addition.Status,
		Label: display,
		Rank:  rank,
		Tags:  addition.Tags,
	}
	change := statusChange{
		Operation: "add",
		Status:    addition.Status,
		Position:  position,
		Label:     &statusLabel{To: display},
		Tags:      addition.Tags,
	}
	if containsStatusTag(addition.Tags, core.StatusTagDefault) {
		change.DefaultFrom = vocabulary.Default()
	}
	return statusPlan{operations: []core.ConfigOperation{operation}, change: change}, nil
}

// statusAddition is one status somebody is defining, however they said it: the
// verb's arguments and flags, or the board's JSON body.
type statusAddition struct {
	Status core.Status
	// Label empty derives one from the token.
	Label  string
	Tags   []core.StatusTag
	Before core.Status
	After  core.Status
}
