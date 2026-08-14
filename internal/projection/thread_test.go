package projection

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// threadService builds the service every test here mutates through, with the
// cache as its reader — which is the arrangement that makes a dropped column a
// data loss rather than a display bug.
func threadProjectionService(store *Store, repository threadWriter, config core.ProjectConfig, ids ...string) core.Service {
	index := 0
	return core.Service{
		Config: config, Reader: store, Writer: repository, Blobs: repository, Projection: store,
		History: store, Actor: "test@example.test",
		Now: func() time.Time { return time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC) },
		IDs: core.IDSourceFunc(func() (string, error) {
			value := ids[index]
			index++
			return value, nil
		}),
	}
}

type threadWriter interface {
	core.CanonicalTaskWriter
	core.AttachmentBlobStore
}

// A task's thread survives the cache, which is the property the schema bump is
// for.
//
// A mutation folds onto the parent snapshot the projection served, so a comment
// this cache dropped would be a comment the next ordinary write erases from the
// checkpoint — silently, because the write would still validate against the
// truncated parent it was given. The assertion is therefore not only that a
// read shows the thread but that a later unrelated write preserves it.
func TestAProjectedTaskKeepsItsCommentsAndAttachments(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Task with a thread")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	commentService := threadProjectionService(store, repository, config, "01K0M6B8A4FTT8C39MXXYTWC01")
	commented, err := commentService.CommentAddMutation(ctx, created.ID, core.CommentAddInput{Body: "a remark"})
	if err != nil {
		t.Fatalf("CommentAddMutation() error = %v", err)
	}
	attachService := threadProjectionService(store, repository, config, "01K0M6B8A4FTT8C39MXXYTWA01")
	attached, err := attachService.AttachmentAddMutation(ctx, created.ID, core.AttachmentAddInput{
		Kind: core.AttachmentFile, Name: "trace.log", Content: []byte("hello world"),
	})
	if err != nil {
		t.Fatalf("AttachmentAddMutation() error = %v", err)
	}
	if len(commented.Task.Comments) != 1 || len(attached.Task.Attachments) != 1 {
		t.Fatalf("mutations did not record the thread: %#v %#v", commented.Task, attached.Task)
	}

	// Served incrementally from the cache the mutations advanced.
	fromCache, err := store.Get(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertThreadMatches(t, fromCache.State.Task, attached.Task.TaskData)

	// Served from a cache rebuilt from Git alone, which is the path a deleted
	// cache and `workbook rebuild` take.
	if err := os.Remove(store.CachePath()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := store.List(ctx, config)
	if err != nil || len(rebuilt) != 1 {
		t.Fatalf("List(after rebuild) = %#v, %v", rebuilt, err)
	}
	assertThreadMatches(t, rebuilt[0].State.Task, attached.Task.TaskData)

	// And an ordinary write folded onto the projected parent keeps the thread
	// rather than erasing it.
	titleService := threadProjectionService(store, repository, config, "01K0M6B8A4FTT8C39MXXYTWT01")
	title := "Renamed"
	renamed, err := titleService.UpdateMutation(ctx, created.ID, core.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	assertThreadMatches(t, renamed.Task.TaskData, attached.Task.TaskData)
}

// The projected operation rows are replayed, not only displayed, so the comment
// bodies have to survive the cache too.
func TestAProjectedChainReplaysCommentOperations(t *testing.T) {
	ctx := context.Background()
	repository, config := initializeWorkbook(t, testrepo.New(t))
	created := createTask(t, repository, config, "Task with a thread")

	store, err := Open(ctx, repository, config)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	service := threadProjectionService(store, repository, config, "01K0M6B8A4FTT8C39MXXYTWC01")
	commented, err := service.CommentAddMutation(ctx, created.ID, core.CommentAddInput{Body: "a remark"})
	if err != nil {
		t.Fatalf("CommentAddMutation() error = %v", err)
	}

	history, err := store.TaskHistory(ctx, config, created.ID)
	if err != nil {
		t.Fatalf("TaskHistory() error = %v", err)
	}
	last := history.Entries[len(history.Entries)-1].Operation.Operations[0]
	if last.Type != core.OperationCommentAdd || last.Body != "a remark" {
		t.Fatalf("projected operation = %#v, want the comment with its body", last)
	}
	// Replaying the projected chain has to reconstruct the same task the
	// checkpoint holds; an operation stored without its payload would either
	// refuse here or fold to a different thread.
	state, err := core.StateAt(config.Key, history)
	if err != nil {
		t.Fatalf("StateAt() error = %v", err)
	}
	assertThreadMatches(t, state, commented.Task.TaskData)
}

func assertThreadMatches(t *testing.T, got, want core.TaskData) {
	t.Helper()
	if !reflect.DeepEqual(got.Comments, want.Comments) {
		t.Fatalf("comments = %#v, want %#v", got.Comments, want.Comments)
	}
	if !reflect.DeepEqual(got.Attachments, want.Attachments) {
		t.Fatalf("attachments = %#v, want %#v", got.Attachments, want.Attachments)
	}
}
