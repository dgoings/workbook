package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Only a comment's author may change its text; anybody may remove it, and the
// thread says who did. The rule is enforced where an edit is authored and
// nowhere else, so these tests come in two halves: the boundary refuses, and
// the fold and the trace treat whatever history already holds as recorded.

const (
	commentAuthor = "bob@example.com"
	commentOther  = "alice@example.com"
)

func authoredThreadTask(author string) TaskData {
	task := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	task.Comments = []Comment{{ID: commentOneID, Author: author, Body: "as written", CreatedAt: threadWallTime}}
	return task
}

func TestEditingAnotherPersonsCommentIsRefusedWithoutWriting(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentOther

	_, err := service.CommentEditMutation(context.Background(), threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "rewritten"})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	for _, want := range []string{commentOneID, commentAuthor, "only its author may edit it", "anyone may remove it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused edit wrote %d packs", len(store.writes))
	}
}

// The refusal is asked before the no-op check, so somebody else's comment is
// refused as somebody else's even when the words would not change.
func TestTheAuthorshipRefusalComesBeforeTheUnchangedRefusal(t *testing.T) {
	service, _, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentOther

	_, err := service.CommentEditMutation(context.Background(), threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "as written"})
	if err == nil || !strings.Contains(err.Error(), "only its author may edit it") {
		t.Fatalf("error = %v, want the authorship refusal", err)
	}
}

// An update that carries a foreign edit beside other changes records none of
// them: one pack, or nothing.
func TestAForeignEditRefusesTheWholeUpdate(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentOther

	done := StatusDone
	_, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{
		Status:   &done,
		Comments: []CommentChange{{Body: "a new remark"}, {CommentID: commentOneID, Body: "rewritten"}},
	})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused update wrote %d packs", len(store.writes))
	}
}

func TestTheAuthorStillEditsTheirOwnComment(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentAuthor

	result, err := service.CommentEditMutation(context.Background(), threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "rewritten by its author"})
	if err != nil {
		t.Fatalf("CommentEditMutation() error = %v", err)
	}
	if len(store.writes) != 1 || store.writes[0].pack.Actor.ID != commentAuthor {
		t.Fatalf("writes = %#v, want one pack recorded under the author", store.writes)
	}
	if got := result.Task.Comments[0].Body; got != "rewritten by its author" {
		t.Fatalf("body = %q, want the edit", got)
	}
}

// The acting person is the one the context names, not the one the service was
// built with: the same seam WORKBOOK_ACTOR and the board's requests use.
func TestTheContextsActorDecidesWhoTheAuthorIs(t *testing.T) {
	ctx, err := WithActor(context.Background(), commentAuthor)
	if err != nil {
		t.Fatalf("WithActor() error = %v", err)
	}

	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentOther
	if _, err := service.CommentEditMutation(ctx, threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "the author, through the context"}); err != nil {
		t.Fatalf("CommentEditMutation(author on context) error = %v", err)
	}
	if len(store.writes) != 1 || store.writes[0].pack.Actor.ID != commentAuthor {
		t.Fatalf("writes = %#v, want one pack under the context's actor", store.writes)
	}

	service, store, _ = threadServiceUnderTest(t, authoredThreadTask(commentOther))
	service.Actor = commentOther
	_, err = service.CommentEditMutation(ctx, threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "not the author, whatever the service says"})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused edit wrote %d packs", len(store.writes))
	}
}

func TestAServiceWithNoIdentityMayEditNothing(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = ""
	_, err := service.CommentEditMutation(context.Background(), threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "rewritten"})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused edit wrote %d packs", len(store.writes))
	}
}

func TestAnyoneMayRemoveAComment(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, authoredThreadTask(commentAuthor))
	service.Actor = commentOther

	result, err := service.CommentRemoveMutation(context.Background(), threadTaskID,
		CommentRemoveInput{CommentID: commentOneID})
	if err != nil {
		t.Fatalf("CommentRemoveMutation() error = %v", err)
	}
	if len(store.writes) != 1 || store.writes[0].pack.Actor.ID != commentOther {
		t.Fatalf("writes = %#v, want one pack recorded under the remover", store.writes)
	}
	if len(result.Task.Comments) != 0 {
		t.Fatalf("comments = %#v, want the comment gone", result.Task.Comments)
	}
}

// The fold applies an edit by somebody other than the author exactly as it
// always has. A history may already hold one, and an older clone may still
// record one; folding it away would rewrite what existing threads say and
// make two builds disagree about the same history.
func TestTheFoldAppliesAnEditByAnotherPersonAsRecorded(t *testing.T) {
	parent := threadParent(TaskData{Comments: []Comment{
		{ID: commentOneID, Author: commentAuthor, Body: "as written", CreatedAt: threadWallTime},
	}})
	pack := NewOperationPack(parent.ProjectID, parent.TaskID, parent.History.Generation, commentOther,
		parent.LogicalClock+1, threadWallTime.Add(time.Minute),
		[]Operation{{ID: editOneID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "rewritten"}})
	state, err := Apply(&parent, pack)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	got := state.Task.Comments[0]
	if got.Author != commentAuthor || got.Body != "rewritten" || got.EditedAt == nil {
		t.Fatalf("comment = %#v, want the foreign edit applied under the original author", got)
	}
}

// commentChain builds a task history one pack at a time, folding each so the
// trace can be checked against the state the fold actually reached.
type commentChain struct {
	t       *testing.T
	state   StateDocument
	history TaskHistory
	at      time.Time
}

func newCommentChain(t *testing.T) *commentChain {
	t.Helper()
	pack := createPack()
	state, err := Apply(nil, pack)
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	return &commentChain{
		t: t, state: state, at: pack.WallTime,
		history: TaskHistory{Entries: []HistoryEntry{{Commit: "c1", Operation: pack}}},
	}
}

func (chain *commentChain) record(actor string, operations ...Operation) {
	chain.t.Helper()
	chain.at = chain.at.Add(time.Minute)
	pack := NewOperationPack(chain.state.ProjectID, chain.state.TaskID, chain.state.History.Generation,
		actor, chain.state.LogicalClock+1, chain.at, operations)
	state, err := Apply(&chain.state, pack)
	if err != nil {
		chain.t.Fatalf("Apply(%v) error = %v", operations, err)
	}
	previous := chain.history.Entries[len(chain.history.Entries)-1].Commit
	chain.history.Entries = append(chain.history.Entries, HistoryEntry{
		Commit: previous + "+", Parent: previous, Operation: pack,
	})
	chain.state = state
}

func (chain *commentChain) removed() []RemovedComment {
	return TraceComments(chain.history).Removed(chain.state.Task.Comments)
}

const (
	traceCommentA = "01K0M6B8A4FTT8C39MXXYTWT01"
	traceCommentB = "01K0M6B8A4FTT8C39MXXYTWT02"
	traceOp1      = "01K0M6B8A4FTT8C39MXXYTWT11"
	traceOp2      = "01K0M6B8A4FTT8C39MXXYTWT12"
	traceOp3      = "01K0M6B8A4FTT8C39MXXYTWT13"
	traceOp4      = "01K0M6B8A4FTT8C39MXXYTWT14"
)

func TestTheTraceNamesWhoRemovedACommentWithoutItsText(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "a secret remark"))
	written := chain.at
	chain.record(commentAuthor, comment(traceCommentB, "still here"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentRemove, CommentID: traceCommentA})

	want := []RemovedComment{{
		ID: traceCommentA, Author: commentAuthor, CreatedAt: written,
		RemovedBy: commentOther, RemovedAt: chain.at,
	}}
	if got := chain.removed(); !reflect.DeepEqual(got, want) {
		t.Fatalf("removed = %#v, want %#v", got, want)
	}
}

// A second removal of a comment already gone is history, not a removal: the
// fold does nothing with it, and the trace keeps the person who removed it.
func TestASecondRemovalDoesNotRenameTheRemover(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "remark"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentRemove, CommentID: traceCommentA})
	first := chain.at
	chain.record(commentAuthor, Operation{ID: traceOp2, Type: OperationCommentRemove, CommentID: traceCommentA})

	removed := chain.removed()
	if len(removed) != 1 || removed[0].RemovedBy != commentOther || !removed[0].RemovedAt.Equal(first) {
		t.Fatalf("removed = %#v, want the first removal by %s", removed, commentOther)
	}
}

// An edit that reaches a comment after somebody removed it changes nothing in
// the fold, and the trace still shows the removal.
func TestAnEditAfterARemovalLeavesTheRemoval(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "remark"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentRemove, CommentID: traceCommentA})
	chain.record(commentAuthor, Operation{ID: traceOp2, Type: OperationCommentEdit, CommentID: traceCommentA, Body: "too late"})

	if len(chain.state.Task.Comments) != 0 {
		t.Fatalf("comments = %#v, want the removal to stand", chain.state.Task.Comments)
	}
	if removed := chain.removed(); len(removed) != 1 || removed[0].RemovedBy != commentOther {
		t.Fatalf("removed = %#v, want the removal by %s", removed, commentOther)
	}
}

// The thread names whoever made the edit the comment records, so an edit an
// older build let somebody else make is no longer shown as the author's words.
func TestTheThreadNamesWhoMadeTheLatestEdit(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "first"))
	chain.record(commentAuthor, comment(traceCommentB, "second"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentEdit, CommentID: traceCommentA, Body: "rewritten"})
	chain.record(commentOther, Operation{ID: traceOp2, Type: OperationCommentEdit, CommentID: traceCommentB, Body: "rewritten"})
	chain.record(commentAuthor, Operation{ID: traceOp3, Type: OperationCommentEdit, CommentID: traceCommentB, Body: "put back"})

	thread := TraceComments(chain.history).Thread(chain.state.Task.Comments, commentAuthor)
	if len(thread) != 2 {
		t.Fatalf("thread = %#v, want two comments", thread)
	}
	if got := thread[0]; got.EditedBy != commentOther || !got.EditedByOther() || !got.Editable {
		t.Fatalf("first = %#v, want it edited by %s and editable by its author", got, commentOther)
	}
	if got := thread[1]; got.EditedBy != commentAuthor || got.EditedByOther() {
		t.Fatalf("second = %#v, want its latest edit to be the author's", got)
	}
	if thread[0].EditorNotRecorded || thread[1].EditorNotRecorded {
		t.Fatalf("thread = %#v, want no edit marked unrecorded when the history records both", thread)
	}
	if other := TraceComments(chain.history).Thread(chain.state.Task.Comments, commentOther); other[0].Editable || other[1].Editable {
		t.Fatalf("thread for %s = %#v, want nothing editable", commentOther, other)
	}
}

// ShowDetail reads the task and then its history, so an edit that lands
// between the two reads is in the trace but not in the text being shown. The
// thread names the person whose edit the text shows, never a later editor
// whose words it does not show, and a comment nobody edited names nobody.
func TestTheThreadNamesNoEditorForAnEditItsTextDoesNotShow(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "first"))
	chain.record(commentAuthor, comment(traceCommentB, "second"))
	chain.record(commentAuthor, Operation{ID: traceOp1, Type: OperationCommentEdit, CommentID: traceCommentA, Body: "revised"})
	shown := chain.state.Task.Comments
	chain.record(commentOther, Operation{ID: traceOp2, Type: OperationCommentEdit, CommentID: traceCommentA, Body: "rewritten"})
	chain.record(commentOther, Operation{ID: traceOp3, Type: OperationCommentEdit, CommentID: traceCommentB, Body: "rewritten"})

	thread := TraceComments(chain.history).Thread(shown, commentAuthor)
	if len(thread) != 2 || thread[0].Body != "revised" || thread[1].Body != "second" {
		t.Fatalf("thread = %#v, want the text as it was first read", thread)
	}
	if thread[0].EditedBy != commentAuthor || thread[0].EditorNotRecorded {
		t.Fatalf("revised comment = %#v, want it to name %s, whose edit the text shows, and not %s", thread[0], commentAuthor, commentOther)
	}
	if thread[1].EditedBy != "" || thread[1].EditorNotRecorded {
		t.Fatalf("unedited comment = %#v, want nobody named and nothing unrecorded", thread[1])
	}
}

// A compacted task starts from its checkpoint, which keeps an edit's time but
// not who made it, and no removal at all. The trace says only what it knows,
// and says that it does not know who made the edit: a comment that silently
// read as edited would read as its author's own words.
func TestACheckpointRootStartsTheTraceAfresh(t *testing.T) {
	editedAt := threadWallTime.Add(time.Hour)
	task := threadParent(TaskData{}).Task
	task.Comments = []Comment{{ID: traceCommentA, Author: commentAuthor, Body: "kept", CreatedAt: threadWallTime, EditedAt: &editedAt}}
	checkpoint := OperationPack{
		Actor:    Actor{ID: commentOther},
		WallTime: threadWallTime.Add(2 * time.Hour),
		Operations: []Operation{{
			ID: traceOp1, Type: OperationHistoryCheckpoint, Task: &task, Checkpoint: &CheckpointData{},
		}},
	}
	remove := OperationPack{
		Actor:      Actor{ID: commentOther},
		WallTime:   threadWallTime.Add(3 * time.Hour),
		Operations: []Operation{{ID: traceOp2, Type: OperationCommentRemove, CommentID: traceCommentA}},
	}
	history := TaskHistory{Entries: []HistoryEntry{{Commit: "k", Operation: checkpoint}}}

	thread := TraceComments(history).Thread(task.Comments, commentAuthor)
	if len(thread) != 1 || thread[0].EditedBy != "" || !thread[0].EditorNotRecorded {
		t.Fatalf("thread = %#v, want the edit with nobody named and its editor marked not recorded", thread)
	}
	// A thread drawn with no history read at all knows nothing about any
	// edit, so it claims nothing about one either.
	if bare := (CommentTrace{}).Thread(task.Comments, commentAuthor); len(bare) != 1 || bare[0].EditorNotRecorded {
		t.Fatalf("thread with no history read = %#v, want nothing marked", bare)
	}

	history.Entries = append(history.Entries, HistoryEntry{Commit: "r", Parent: "k", Operation: remove})
	removed := TraceComments(history).Removed(nil)
	want := []RemovedComment{{
		ID: traceCommentA, Author: commentAuthor, CreatedAt: threadWallTime,
		RemovedBy: commentOther, RemovedAt: remove.WallTime,
	}}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %#v, want %#v", removed, want)
	}
}

// The trace is a function of the chain alone: the same chain, whichever clone
// reads it, gives the same answer, and reading it twice changes nothing.
func TestTheTraceIsDeterministicForOneChain(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "a"), comment(traceCommentB, "b"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentRemove, CommentID: traceCommentB},
		Operation{ID: traceOp2, Type: OperationCommentRemove, CommentID: traceCommentA})
	chain.record(commentAuthor, Operation{ID: traceOp4, Type: OperationCommentRemove, CommentID: traceCommentA})

	first := chain.removed()
	second := chain.removed()
	if !reflect.DeepEqual(first, second) || len(first) != 2 || first[0].ID != traceCommentA || first[1].ID != traceCommentB {
		t.Fatalf("removed = %#v then %#v, want both removals in identifier order, twice", first, second)
	}
}

type commentHistoryStub struct{ history TaskHistory }

func (stub commentHistoryStub) TaskHistory(context.Context, ProjectConfig, string) (TaskHistory, error) {
	return stub.history, nil
}

func (stub commentHistoryStub) CommitHistory(context.Context, ProjectConfig, string, string) (TaskHistory, error) {
	return stub.history, nil
}

func TestShowDetailCarriesTheReadersThreadAndItsRemovals(t *testing.T) {
	chain := newCommentChain(t)
	chain.record(commentAuthor, comment(traceCommentA, "mine"))
	chain.record(commentOther, comment(traceCommentB, "theirs"))
	chain.record(commentOther, Operation{ID: traceOp1, Type: OperationCommentRemove, CommentID: traceCommentA})
	chain.record(commentAuthor, comment(traceOp2, "mine again"))

	service, _, _ := threadServiceUnderTest(t, chain.state.Task)
	service.History = commentHistoryStub{history: chain.history}
	ctx, err := WithActor(context.Background(), commentAuthor)
	if err != nil {
		t.Fatalf("WithActor() error = %v", err)
	}
	detail, err := service.ShowDetail(ctx, threadTaskID, ShowOptions{})
	if err != nil {
		t.Fatalf("ShowDetail() error = %v", err)
	}
	editable := map[string]bool{}
	for _, comment := range detail.Comments {
		editable[comment.ID] = comment.Editable
	}
	if want := map[string]bool{traceCommentB: false, traceOp2: true}; !reflect.DeepEqual(editable, want) {
		t.Fatalf("editable = %v, want %v", editable, want)
	}
	if len(detail.RemovedComments) != 1 || detail.RemovedComments[0].ID != traceCommentA ||
		detail.RemovedComments[0].RemovedBy != commentOther || detail.RemovedComments[0].Author != commentAuthor {
		t.Fatalf("removed = %#v, want the removal of %s by %s", detail.RemovedComments, traceCommentA, commentOther)
	}
	if detail.History != nil {
		t.Fatalf("history = %#v, want none on a plain read", detail.History)
	}
}
