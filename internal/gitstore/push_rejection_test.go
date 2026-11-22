package gitstore

import (
	"context"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A push origin refuses says why in its error, with Git's own reason for each
// refused ref, so a server log tells a hook's refusal from a ref somebody
// else published first.
func TestPushRejectionNamesGitsReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, path := range []string{"push", "sync", "push-tasks"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			first, _, config := syncRepositories(t)
			task := createSyncTask(t, first, config, "Refused")
			refuseTaskRefUpdates(t, first)
			var err error
			switch path {
			case "push":
				_, err = first.Push(ctx, config)
			case "sync":
				_, err = first.Sync(ctx, config)
			case "push-tasks":
				_, err = first.PushTasks(ctx, config, []string{task.ID})
			}
			want := "1 task ref(s) were rejected by origin: " + task.ID + " [remote rejected] (hook declined)"
			if core.CategoryOf(err) != core.CategoryOperational || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s error = %v, want it to contain %q", path, err, want)
			}
		})
	}
}

// A push that many refs were refused for names the first three and counts the
// rest, so the line stays one line.
func TestPushRejectionNamesTheFirstThree(t *testing.T) {
	t.Parallel()
	var rejected []SyncTaskResult
	for _, id := range []string{"WB-1", "WB-2", "WB-3", "WB-4", "WB-5"} {
		rejected = append(rejected, SyncTaskResult{TaskID: id, Status: SyncRejected, Detail: "[rejected] (fetch first)"})
	}
	rejected[1].Detail = ""
	want := "5 task ref(s) were rejected by origin: WB-1 [rejected] (fetch first); WB-2; WB-3 [rejected] (fetch first); and 2 more"
	repository := &Repository{remoteName: "origin"}
	if got := repository.rejectedByRemote(rejected).Error(); got != want {
		t.Fatalf("rejectedByRemote() = %q, want %q", got, want)
	}
}
