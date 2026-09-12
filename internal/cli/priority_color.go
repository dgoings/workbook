package cli

import (
	"context"
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityColor chooses the color the board draws a priority in, or clears
// a stored one back to the automatic color its position implies.
//
// The second positional is optional — `priority color <priority>` with
// nothing after it is how a color is cleared — so it cannot go through
// requiredArguments the way every other verb's positionals do. This pulls it
// off the argument list itself, before parseFlags, which refuses leftover
// positionals it was not told about.
func runPriorityColor(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority color", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	var raw string
	if len(args) > 0 && isRequiredFirstArgument(args[0]) {
		raw = args[0]
		args = args[1:]
	}

	flags := newFlagSet("priority", "color")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating .workbook/guidelines.md")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	// Validated at the CLI boundary, before the session that would write it
	// even opens: a malformed color is refused, and nothing about this project
	// is touched. core.ValidateThemeColor is exactly the rule
	// `workbook config set primary-color` already uses for the same reason —
	// stage 3 emits this value into the board's theme block as CSS that
	// bypasses Go's contextual escaping by design, so a color the ledger
	// stores has to already be something no peer, malicious or corrupted,
	// could turn into an injection. An empty value means clear; it needs no
	// validation because it is never rendered.
	color := ""
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		canonical, err := core.ValidateThemeColor(trimmed)
		if err != nil {
			return err
		}
		color = canonical
	}

	return runPriorityMutation(ctx, cwd, "priority color", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityColor(ctx, session.priorityScope(), vocabulary, core.Priority(values[0]), color)
		})
}

// planPriorityColor authors the one operation a recolor ever needs: a plain
// assignment of the new color, or of an empty value to clear it — unless the
// assignment would record nothing, which it refuses before anything is
// authored, the same way planPriorityRelabel refuses the label a priority
// already has.
//
// That refusal was not part of this verb when it was designed, and the
// original reasoning was sound for what it weighed: the fold in internal/core
// — see configPriorities.applyRecolor's own comment — treats a recolor as a
// plain assignment rather than something to judge, so refusing here meant
// fighting that model for a value the CLI itself had no other reason to
// compare. What changed is not the fold, but what a priority write costs.
// core.BuiltInPriorityVocabulary now backfills into the very commit that
// first touches a project's priorities, and that commit's operation carries
// the priority section's own generation-three marker — see
// configOperationMinReader's comment in internal/core/configop.go — which is
// permanent: every clone below that generation cannot change the project's
// configuration again until it upgrades. config.go's refuseUnchangedDisplay
// already documents the identical trade for the display section's
// generation-two marker. A recolor that changes nothing would spend that
// cost — on every teammate, for the life of the project — to record a value
// nobody asked to change, which is a real cost the original reasoning did
// not have in front of it. Both directions count: setting a color to the one
// already stored, and clearing a priority that has none.
func planPriorityColor(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority core.Priority,
	color string,
) (priorityPlan, error) {
	subject, err := requireLivePriority(ctx, scope, vocabulary, priority)
	if err != nil {
		return priorityPlan{}, err
	}
	definition, _ := priorityDefinition(vocabulary, subject)
	current := definition.Color
	if color == current {
		if color == "" {
			return priorityPlan{}, core.Errorf(core.CategoryValidation,
				"priority %q has no color to clear", subject)
		}
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q already has that color", subject)
	}

	operation := core.ConfigOperation{Type: core.ConfigPriorityRecolor, Priority: subject, Value: color}
	return priorityPlan{
		operations: []core.ConfigOperation{operation},
		change: priorityChange{
			Operation: "color",
			Priority:  subject,
			Color:     &priorityColor{From: current, To: color},
		},
	}, nil
}
