package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A task mutation's text output passes its warnings through the control-byte
// filter, as the relic warnings beside them are: one of them is the fetch's
// relic defect, which can quote what a relic on origin holds.
func TestTaskMutationTextWarningsAreTerminalSafe(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	result := core.MutationResult{Warnings: []core.Warning{{
		Code:    core.WarningAutoSync,
		Message: "the change synchronized, but origin holds relics this fetch could not check: relic WB-R-1: \x1b[31mred\x1b[0m\nsecond line",
	}}}
	writeThreadMutationResult(&stdout, &stderr, "edit", result, nil, nil, false, nil)
	if strings.ContainsAny(stderr.String(), "\x1b") || strings.Count(stderr.String(), "\n") != 1 {
		t.Fatalf("stderr = %q, want one filtered warning line", stderr.String())
	}
}
