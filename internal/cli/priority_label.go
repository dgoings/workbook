package cli

import (
	"context"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// This file is a placeholder for `workbook priority label`, which changes a
// priority's display label. The agent implementing this verb replaces this file
// wholesale and owns nothing outside it; the shared machinery it builds on —
// runPriorityMutation, requireLivePriority, priorityPlan and the rest — lives
// in priority.go.
func runPriorityLabel(_ context.Context, _ []string, _ string, _, _ io.Writer) error {
	return core.Errorf(core.CategoryInvocation, "priority label is not implemented yet")
}
