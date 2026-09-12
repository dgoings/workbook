package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// This file is a placeholder for `workbook priority delete`, which removes a
// priority and forwards its tasks. The agent implementing this verb replaces
// this file wholesale and owns nothing outside it; the shared machinery it
// builds on — runPriorityMutation, requireLivePriority,
// missingPriorityRemovalDestination, removalPriorityTaskCounts, priorityPlan
// and the rest — lives in priority.go.
func runPriorityDelete(_ context.Context, _ []string, _ string, _, _ io.Writer) error {
	return core.Errorf(core.CategoryInvocation, "priority delete is not implemented yet")
}
