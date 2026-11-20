package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

func runStatusRename(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status rename", []string{"<status>", "<new-status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "rename")
	label := flags.String("label", "", "display label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	from, to := core.Status(values[0]), core.Status(values[1])
	if err := core.ValidateStatusToken(to); err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidateStatusLabel(*label); err != nil {
			return err
		}
	}

	return runStatusMutation(ctx, cwd, "status rename", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusRename(ctx, session.statusScope(), vocabulary, from, to, *label)
		})
}

// planStatusRename moves a status onto a new token, keeping a label somebody
// chose and re-deriving one nobody did.
//
// label is what the caller asked for and empty means they asked for nothing,
// which is the case the derived-label rule is about.
func planStatusRename(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	from, to core.Status,
	label string,
) (statusPlan, error) {
	if err := core.ValidateStatusToken(to); err != nil {
		return statusPlan{}, err
	}
	subject, err := requireLiveStatus(ctx, scope, vocabulary, from)
	if err != nil {
		return statusPlan{}, err
	}
	if subject == to {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already has that value", to)
	}
	if vocabulary.Has(to) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines status %q", to)
	}

	// The derived-label rule. A label nobody chose follows the name it was
	// derived from; a label somebody chose is theirs and survives a rename of
	// the machine value underneath it.
	current := vocabulary.Label(subject)
	display, derived := current, false
	switch {
	case label != "":
		display = label
	case current == core.DerivedStatusLabel(subject):
		display, derived = core.DerivedStatusLabel(to), true
	}
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}

	reresolved, err := renameReresolution(ctx, scope, vocabulary, subject, to)
	if err != nil {
		return statusPlan{}, err
	}

	rename := core.ConfigOperation{Type: core.ConfigStatusRename, From: subject, To: to}
	operations := []core.ConfigOperation{rename}
	if display != current {
		operations = append(operations, core.ConfigOperation{
			Type: core.ConfigStatusRelabel, Status: to, Label: display,
		})
	}
	labelDerived := derived
	return statusPlan{
		reresolved: reresolved,
		operations: operations,
		change: statusChange{
			Operation:    "rename",
			Status:       to,
			From:         subject,
			Label:        &statusLabel{From: current, To: display},
			LabelDerived: &labelDerived,
			Tags:         statusTags(vocabulary, subject),
		},
	}, nil
}

// renameReresolution reports what renaming subject onto to does to the history
// a retirement left under to, or nothing when to is not retired or its history
// already resolves to subject.
//
// The second case is the everyday one: renaming back is the inverse every
// rename prints, and a removal into this status followed by taking the removed
// name leaves every task in the column it was already in. Warning there would
// teach people to skip the warning.
//
// The count reads the stored value of every active task, which is the value the
// forwarding chain starts from, and walks the chain as it stands before the
// rename. It is what a removal pays to price itself, and only a rename onto a
// retired name pays it.
func renameReresolution(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	subject, to core.Status,
) (*statusReresolution, error) {
	via, operation, forwarded := vocabulary.Forwarding(to)
	if !forwarded {
		return nil, nil
	}
	previously, live := vocabulary.Resolve(to)
	if !live {
		previously = ""
	} else if previously == subject {
		return nil, nil
	}
	tasks, err := scope.service.List(ctx, core.ListFilter{})
	if err != nil {
		return nil, err
	}
	count := 0
	for _, task := range tasks {
		stored := task.Status
		if task.StoredStatus != "" {
			stored = task.StoredStatus
		}
		if forwardsThrough(vocabulary, stored, to) {
			count++
		}
	}
	return &statusReresolution{
		Status:     to,
		RetiredBy:  operation,
		Via:        via,
		Previously: previously,
		Now:        to,
		Tasks:      count,
	}, nil
}

// forwardsThrough reports whether a stored status reaches via on its way along
// the forwarding chain, via itself included. The walk stops at a live status,
// at a value nothing forwards, and at a value it has already visited, so a
// cycle read from a ref ends it rather than hanging it.
func forwardsThrough(vocabulary core.Vocabulary, status, via core.Status) bool {
	visited := make(map[core.Status]bool)
	for current := status; !visited[current]; {
		if current == via {
			return true
		}
		if vocabulary.Has(current) {
			return false
		}
		visited[current] = true
		next, _, forwarded := vocabulary.Forwarding(current)
		if !forwarded {
			return false
		}
		current = next
	}
	return false
}
