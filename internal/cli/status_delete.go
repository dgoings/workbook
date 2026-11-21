package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusDelete(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status delete", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "delete")
	into := flags.String("into", "", "where the removed status's tasks belong")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// --into is required and never guessed, and the refusal names the statuses
	// it could have been. Prompting is not an option: agents run this command,
	// and a prompt would hang one.
	//
	// It is refused before the session opens rather than inside the change,
	// because an invocation nobody could have meant should not first fetch from
	// origin. Naming the statuses costs a local read of the ledger, which is
	// what the reading verbs pay anyway.
	if *into == "" {
		return missingRemovalDestination(ctx, cwd, stderr)
	}

	return runStatusMutation(ctx, cwd, "status delete", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusDelete(ctx, session.statusScope(), vocabulary,
				core.Status(values[0]), core.Status(*into))
		})
}

// planStatusDelete retires a status and forwards its tasks to a live one.
func planStatusDelete(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status, into core.Status,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	destination, err := requireLiveStatus(ctx, scope, vocabulary, into)
	if err != nil {
		return statusPlan{}, err
	}
	if destination == subject {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status delete cannot forward %q into itself; name where its tasks belong", subject)
	}
	definition, _ := statusDefinition(vocabulary, subject)
	counts, err := removalTaskCounts(ctx, scope, vocabulary, subject, destination)
	if err != nil {
		return statusPlan{}, err
	}
	operation := core.ConfigOperation{
		Type: core.ConfigStatusRemove, Status: subject, Destination: destination,
	}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "delete",
			Status:    subject,
			Into:      destination,
			Label:     &statusLabel{To: definition.Label},
			Tags:      definition.Tags,
		},
		tasks: counts,
	}, nil
}
