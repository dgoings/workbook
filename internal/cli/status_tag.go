package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusTag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status tag", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "tag")
	var tags stringListValue
	flags.Var(&tags, "tag", "role to give it")
	clear := flags.Bool("clear-tags", false, "replace its roles with an empty set")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with the tasks remote (origin unless configured)")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if tags.set && *clear {
		return core.Errorf(core.CategoryInvocation, "cannot use --tag with --clear-tags")
	}
	if !tags.set && !*clear {
		return core.Errorf(core.CategoryInvocation, "status tag requires --tag or --clear-tags")
	}
	wanted, err := parseStatusTags(tags.values)
	if err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status tag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusTagSet(ctx, session.statusScope(), vocabulary, core.Status(values[0]), wanted)
		})
}

// planStatusTagSet replaces a status's whole tag set.
func planStatusTagSet(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	wanted []core.StatusTag,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := statusTags(vocabulary, subject)
	operations := tagSetOperations(subject, current, wanted)
	if len(operations) == 0 {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already carries exactly those tags", subject)
	}
	change := statusChange{Operation: "tag", Status: subject, Tags: wanted}
	if containsStatusTag(wanted, core.StatusTagDefault) && !containsStatusTag(current, core.StatusTagDefault) {
		change.DefaultFrom = vocabulary.Default()
	}
	return statusPlan{operations: operations, change: change}, nil
}

// tagSetOperations turns a replacement set into the operations that reach it.
//
// Tagging is expressed as a set rather than as a difference because that is the
// idiom `workbook update --label` already established, and because a set is the
// only form whose inverse is a single command. The operations it produces are
// still individual tags and untags, which is what the durable format has: a
// clone that fetches them applies each one, and the default transfer inside one
// of them stays atomic.
func tagSetOperations(subject core.Status, current, wanted []core.StatusTag) []core.ConfigOperation {
	operations := make([]core.ConfigOperation, 0, len(current)+len(wanted))
	// Additions first, so that a set taking the default tag never passes
	// through a state with no default at all.
	for _, tag := range wanted {
		if !containsStatusTag(current, tag) {
			operations = append(operations, core.ConfigOperation{
				Type: core.ConfigStatusTag, Status: subject, Tag: tag,
			})
		}
	}
	for _, tag := range current {
		if !containsStatusTag(wanted, tag) {
			operations = append(operations, core.ConfigOperation{
				Type: core.ConfigStatusUntag, Status: subject, Tag: tag,
			})
		}
	}
	return operations
}
