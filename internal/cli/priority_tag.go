package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// runPriorityTag gives a priority a role.
//
// It takes one `--tag` where runStatusTag takes a repeatable one and a
// `--clear-tags`, and the difference is the vocabulary's rather than this
// command's: a status has three roles and is therefore described by a set,
// while a priority has one. There is no set to replace, so there is nothing for
// a repeated flag or a clearing flag to mean — the role is taken away by
// `workbook priority untag`.
func runPriorityTag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("priority tag", []string{"<priority>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("priority", "tag")
	tag := flags.String("tag", "", "role to give it: default")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *tag == "" {
		return core.Errorf(core.CategoryInvocation, "priority tag requires --tag <tag>")
	}
	wanted, err := parsePriorityTag(*tag)
	if err != nil {
		return err
	}

	return runPriorityMutation(ctx, cwd, "priority tag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.PriorityVocabulary) (priorityPlan, error) {
			return planPriorityTag(ctx, session.priorityScope(), vocabulary, core.Priority(values[0]), wanted)
		})
}

// planPriorityTag gives one role to one priority, in one operation.
//
// The single operation is the whole point, and is why this is not a mirror of
// planStatusTagSet: giving the default tag also takes it from whichever
// priority held it, and the fold does that transfer itself, inside
// `priority.tag` (configPriorities.applyTag → clearDefaultExcept). Recording a
// tag and an untag instead would be two operations describing one change, and
// a clone folding only the first half of a pack it is already committed to
// would never see a project without a default — but every reader of the log
// would have to know that the pair means one thing.
//
// DefaultFrom names the priority that gave the tag up, because the envelope is
// the only place that transfer is visible: the operations say what was given,
// not what was taken.
func planPriorityTag(
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
	if containsPriorityTag(current, tag) {
		return priorityPlan{}, core.Errorf(core.CategoryValidation,
			"priority %q already carries the %q tag", subject, tag)
	}
	operation := core.ConfigOperation{Type: core.ConfigPriorityTag, Priority: subject, PriorityTag: tag}
	change := priorityChange{
		Operation: "tag",
		Priority:  subject,
		Tags:      priorityTagsWith(current, tag),
	}
	if tag == core.PriorityTagDefault {
		change.DefaultFrom = vocabulary.Default()
	}
	return priorityPlan{operations: []core.ConfigOperation{operation}, change: change}, nil
}

// priorityTagsWith is a priority's tag set once it carries one more role, in
// the canonical order a stored vocabulary keeps rather than the order the roles
// were given. The envelope reports the whole set after the change, so it has to
// read the way the same set read before it.
func priorityTagsWith(current []core.PriorityTag, added core.PriorityTag) []core.PriorityTag {
	tags := make([]core.PriorityTag, 0, len(current)+1)
	for _, tag := range core.PriorityTags() {
		if tag == added || containsPriorityTag(current, tag) {
			tags = append(tags, tag)
		}
	}
	return tags
}
