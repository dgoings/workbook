package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityUntag takes one role away from a priority.
//
// The role is a flag here where `status untag` takes it positionally. That
// divergence from the sibling is deliberate: `priority tag` names the role with
// `--tag`, and a pair of commands that give and take the same thing should name
// it the same way. A role typed where the status verb would take it is refused
// as a leftover positional rather than read as a second priority.
func runPriorityUntag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority untag", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "untag")
	tag := flags.String("tag", "", "role to take away: default")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *tag == "" {
		return core.Errorf(core.CategoryInvocation, "priority untag requires --tag <tag>")
	}
	unwanted, err := parsePriorityTag(*tag)
	if err != nil {
		return err
	}

	return runPriorityMutation(ctx, cwd, "priority untag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityUntag(ctx, session.priorityScope(), vocabulary, core.Priority(values[0]), unwanted)
		})
}

// planPriorityUntag takes one role away, and refuses only the one thing it can
// answer better than the write can: a role the priority does not carry, which
// is a typo rather than a change.
//
// Taking the project's last `default` away is left to the authoring boundary,
// exactly as planStatusUntag leaves the last `done` there. The arity rule lives
// with the vocabulary that has it, so one message says what is wrong and names
// the command that fixes it, whether the pack came from this verb or from the
// board; and the same operation arriving from a peer still folds, because by
// then it is history rather than a choice anybody can still make differently.
func planPriorityUntag(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority core.Priority,
	tag core.PriorityTag,
) (priorityPlan, error) {
	subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
	if err != nil {
		return priorityPlan{}, err
	}
	current := priorityTags(vocabulary, subject)
	if !containsPriorityTag(current, tag) {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q does not carry the %q tag", subject, tag)
	}
	remaining := make([]core.PriorityTag, 0, len(current))
	for _, candidate := range current {
		if candidate != tag {
			remaining = append(remaining, candidate)
		}
	}
	operation := core.ConfigOperation{Type: core.ConfigPriorityUntag, Priority: subject, PriorityTag: tag}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "untag",
			Priority:  subject,
			Tags:      remaining,
		},
	}, nil
}
