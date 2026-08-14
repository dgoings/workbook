package gitstore

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// refuseNewerWriterReplay is belt-and-braces, and this pins it as such.
//
// The synchronization outcome does not depend on it — core.Apply refuses the
// fold either way — so no integration test can tell whether it is there. What
// it contributes is the early refusal and its wording, and those are what this
// checks: the message names the task, says a newer Workbook wrote origin's
// history, and tells the reader where their unpublished work went.
func TestRefuseNewerWriterReplayNamesTheTaskAndTheLocalWork(t *testing.T) {
	const taskID = "WB-01K0M6B8A4FTT8C39MXXYTW7C2"
	newer := core.Snapshot{State: core.StateDocument{
		TaskID:    taskID,
		MinReader: core.SupportedFormatGeneration + 1,
	}}
	ordinary := core.Snapshot{State: core.StateDocument{TaskID: taskID}}

	for _, testCase := range []struct {
		name    string
		request reconcileRequest
		refused bool
	}{
		{
			name:    "origin's history is newer",
			request: reconcileRequest{TaskID: taskID, Local: ordinary, Remote: newer},
			refused: true,
		},
		{
			name:    "this clone's own tip is newer",
			request: reconcileRequest{TaskID: taskID, Local: newer, Remote: ordinary},
			refused: true,
		},
		{
			name:    "neither side is newer",
			request: reconcileRequest{TaskID: taskID, Local: ordinary, Remote: ordinary},
			refused: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := refuseNewerWriterReplay(testCase.request)
			if !testCase.refused {
				if err != nil {
					t.Fatalf("an ordinary divergence was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a divergence against a history this build cannot fold was allowed to replay")
			}
			if core.CategoryOf(err) != core.CategoryNewerWriter {
				t.Fatalf("category = %q, want %q; error = %v", core.CategoryOf(err), core.CategoryNewerWriter, err)
			}
			message := err.Error()
			for _, want := range []string{taskID, "newer workbook", "upgrade workbook", "unchanged on this clone's task ref"} {
				if !strings.Contains(message, want) {
					t.Fatalf("message = %q, want it to contain %q", message, want)
				}
			}
			for _, forbidden := range []string{"corrupt", "damaged", "unreadable"} {
				if strings.Contains(message, forbidden) {
					t.Fatalf("message = %q, want it not to imply damage with %q", message, forbidden)
				}
			}
		})
	}
}
