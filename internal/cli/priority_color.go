package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// This file is a placeholder for `workbook priority color`, which chooses the
// color the board draws a priority in. The agent implementing this verb
// replaces this file wholesale and owns nothing outside it; the shared
// machinery it builds on — runPriorityMutation, requireLivePriority,
// priorityPlan and the rest — lives in priority.go.
func runPriorityColor(_ context.Context, _ []string, _ string, _, _ io.Writer) error {
	return core.Errorf(core.CategoryInvocation, "priority color is not implemented yet")
}
