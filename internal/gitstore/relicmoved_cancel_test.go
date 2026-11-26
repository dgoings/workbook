package gitstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A relic transaction refused because the command was cancelled says so in
// words of its own: Git's error text, which names paths and lock files, does
// not reach the user.
func TestRelicMovedDuringSyncOnCancelHidesGitsText(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cause := errors.New("fatal: cannot lock ref 'refs/workbook/relics/WB-R-1': Unable to create '/secret/path.lock'")
	err := (&Repository{}).relicMovedDuringSync(ctx, []relicRefUpdate{{RelicID: "WB-R-1"}}, cause)
	if err == nil || strings.Contains(err.Error(), "/secret/path") || strings.Contains(err.Error(), "fatal") ||
		!strings.Contains(err.Error(), "cancel") {
		t.Fatalf("error = %v, want a fixed message saying the command was cancelled", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
}
