package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// This file is a placeholder for `workbook priority untag`, which takes one
// role away from a priority. The agent implementing this verb replaces this
// file wholesale and owns nothing outside it; the shared machinery it builds on
// — runPriorityMutation, requireLivePriority, parsePriorityTag, priorityPlan
// and the rest — lives in priority.go.
func runPriorityUntag(_ context.Context, _ []string, _ string, _, _ io.Writer) error {
	return core.Errorf(core.CategoryInvocation, "priority untag is not implemented yet")
}
