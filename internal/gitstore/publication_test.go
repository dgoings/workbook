package gitstore

import (
	"context"
	"strings"
	"testing"
)

// repeatOperation makes the task's ref a commit that records the operation
// its tip already records: the tip's own tree, again, on top of the tip. A
// projection reads the repeat as a delivery it already applied, so nothing
// before the publication notices it.
func repeatOperation(t *testing.T, repo *Repository, taskID string) (tip, repeat string) {
	t.Helper()
	tip = refValue(t, repo, taskRefPrefix+taskID)
	tree := syncGit(t, repo.Root, "rev-parse", tip+"^{tree}")
	repeat = syncGit(t, repo.Root, "-c", "commit.gpgsign=false", "commit-tree", tree, "-p", tip, "-m", "workbook: repeated operation")
	syncGit(t, repo.Root, "update-ref", taskRefPrefix+taskID, repeat, tip)
	return tip, repeat
}

func requireRepeatNamed(t *testing.T, detail, tip, repeat string) {
	t.Helper()
	for _, want := range []string{"recorded by commit " + tip, "again by commit " + repeat, "workbook validate"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q lacks %q", detail, want)
		}
	}
}

// A history that records one operation ID twice reaches no remote by any
// publication path, and the task beside it is still published. Workbook does
// not write such a history; this is the check that holds whatever did.
func TestPublicationWithholdsAHistoryThatRecordsAnOperationTwice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, path := range []string{"Push", "PushTask", "Sync"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			repo, _, config := syncRepositories(t)
			repeated := createSyncTask(t, repo, config, "Repeated")
			updateSyncTask(t, repo, config, repeated.ID, "Repeated, edited")
			beside := createSyncTask(t, repo, config, "Beside")
			if _, err := repo.Push(ctx, config); err != nil {
				t.Fatal(err)
			}
			published := remoteRefValue(t, repo, taskRefPrefix+repeated.ID)
			updateSyncTask(t, repo, config, beside.ID, "Beside, edited")
			tip, repeat := repeatOperation(t, repo, repeated.ID)
			if _, err := syncService(repo, config).Show(ctx, repeated.ID); err != nil {
				t.Fatalf("the repeat no longer projects, so this test no longer reaches the publication check: %v", err)
			}

			switch path {
			case "Push":
				result, err := repo.Push(ctx, config)
				if err == nil {
					t.Fatalf("Push error = nil; result = %#v", result)
				}
				assertSyncOutcome(t, result, repeated.ID, SyncInvalid)
				assertSyncOutcome(t, result, beside.ID, SyncPublished)
				requireRepeatNamed(t, taskResult(t, result, repeated.ID).Detail, tip, repeat)
			case "PushTask":
				result, err := repo.PushTask(ctx, config, repeated.ID)
				if err == nil || !strings.Contains(err.Error(), "was not published") {
					t.Fatalf("PushTask = (%#v, %v), want it withheld", result, err)
				}
				requireRepeatNamed(t, result.Detail, tip, repeat)
				if _, err := repo.PushTask(ctx, config, beside.ID); err != nil {
					t.Fatal(err)
				}
			case "Sync":
				result, err := repo.Sync(ctx, config)
				if err == nil {
					t.Fatalf("Sync error = nil; result = %#v", result)
				}
				assertSyncOutcome(t, result.Push, repeated.ID, SyncInvalid)
				assertSyncOutcome(t, result.Push, beside.ID, SyncPublished)
				requireRepeatNamed(t, taskResult(t, result.Push, repeated.ID).Detail, tip, repeat)
			}
			if got := remoteRefValue(t, repo, taskRefPrefix+repeated.ID); got != published {
				t.Fatalf("%s published the repeated history: origin moved from %s to %s", path, published, got)
			}
			if got, want := remoteRefValue(t, repo, taskRefPrefix+beside.ID), refValue(t, repo, taskRefPrefix+beside.ID); got != want {
				t.Fatalf("%s did not publish the task beside it: origin %s, local %s", path, got, want)
			}
		})
	}
}

func taskResult(t *testing.T, result SyncResult, taskID string) SyncTaskResult {
	t.Helper()
	for _, item := range result.Tasks {
		if item.TaskID == taskID {
			return item
		}
	}
	t.Fatalf("result has no task %s: %#v", taskID, result)
	return SyncTaskResult{}
}
