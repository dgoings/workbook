package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityDelete removes a priority and forwards the tasks filed under it.
func runPriorityDelete(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority delete", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "delete")
	into := flags.String("into", "", "where the removed priority's tasks belong")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// --into is required and never guessed, and the refusal names the
	// priorities it could have been. A priority is removed rarely and the tasks
	// filed under it are somebody's work: every one of them reads as being at
	// the priority this flag names, on every clone, including the ones that
	// have not fetched the removal yet. Guessing a destination — the default,
	// the neighbour, the one next to it in the order — would be this command
	// deciding how urgent somebody else's work is. Prompting is not an option
	// either: agents run this command, and a prompt would hang one.
	//
	// It is refused before the session opens rather than inside the change,
	// because an invocation nobody could have meant should not first fetch from
	// origin. Naming the priorities costs a local read of the ledger, which is
	// what the reading verbs pay anyway.
	if *into == "" {
		return missingPriorityRemovalDestination(ctx, cwd, stderr)
	}

	return runPriorityMutation(ctx, cwd, "priority delete", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityDelete(ctx, session.priorityScope(), vocabulary,
				core.Priority(values[0]), core.Priority(*into))
		})
}

// planPriorityDelete retires a priority and forwards its tasks to a live one.
//
// It refuses the last remaining priority, which planStatusDelete has no
// counterpart to because the arity rule it would enforce is the priority
// vocabulary's alone: every task is at exactly one priority, a new task lands
// on the one tagged default, and a project with none configured is not a
// vocabulary of nothing but the built-in three read back — so removing the last
// one would either be refused deeper down with a message about a document, or
// quietly answer a later reader with priorities nobody chose. It is refused
// here, where the command that fixes it can be named, and before the
// destination is resolved, because with one priority left every destination a
// caller could type is either that priority or a value this project does not
// have, and neither refusal would say what is actually wrong.
func planPriorityDelete(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority, into core.Priority,
) (priorityPlan, error) {
	subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
	if err != nil {
		return priorityPlan{}, err
	}
	if len(vocabulary.Definitions()) == 1 {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority delete cannot remove %q; it is this project's only priority, and every task has to be "+
				"at one; add another first: workbook priority add <priority>", subject)
	}
	destination, err := requireLivePriority(ctx, scope, vocabulary, into)
	if err != nil {
		return priorityPlan{}, err
	}
	if destination == subject {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority delete cannot forward %q into itself; name where its tasks belong", subject)
	}
	definition, _ := priorityDefinition(vocabulary, subject)
	counts, err := removalPriorityTaskCounts(ctx, scope, subject)
	if err != nil {
		return priorityPlan{}, err
	}
	operation := core.ConfigOperation{
		Type: core.ConfigPriorityRemove, Priority: subject, PriorityDestination: destination,
	}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "delete",
			Priority:  subject,
			Into:      destination,
			Label:     &priorityLabel{To: definition.Label},
			Tags:      definition.Tags,
		},
		tasks: counts,
	}, nil
}
