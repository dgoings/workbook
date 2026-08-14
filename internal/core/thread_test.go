package core

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fold's half of comments and attachments: what an operation means when it
// is replayed, which is every time a clone reads a history it did not author.
//
// Every case here is stated against Apply rather than against the service,
// because Apply is what a fetch, a reconciliation and `workbook validate` run.
// The service's refusals are in the tests below these, and the two halves are
// deliberately different: the boundary refuses what the fold tolerates.

const (
	threadTaskID  = "WB-01K0M6B8A4FTT8C39MXXYTW7D1"
	threadActor   = "author@example.com"
	commentOneID  = "01K0M6B8A4FTT8C39MXXYTWC01"
	commentTwoID  = "01K0M6B8A4FTT8C39MXXYTWC02"
	editOneID     = "01K0M6B8A4FTT8C39MXXYTWE01"
	editTwoID     = "01K0M6B8A4FTT8C39MXXYTWE02"
	attachOneID   = "01K0M6B8A4FTT8C39MXXYTWA01"
	attachTwoID   = "01K0M6B8A4FTT8C39MXXYTWA02"
	removeOneID   = "01K0M6B8A4FTT8C39MXXYTWR01"
	threadBlobOID = "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"
)

var threadWallTime = time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)

func threadParent(task TaskData) StateDocument {
	if task.Title == "" {
		task.Title = "Commented task"
	}
	if task.Status == "" {
		task.Status = StatusBacklog
	}
	if task.Priority == "" {
		task.Priority = PriorityMedium
	}
	if task.Rank == "" {
		task.Rank = "1/1"
	}
	task.Labels = []string{}
	task.Dependencies = []string{}
	task.CreatedAt = threadWallTime.Add(-time.Hour)
	task.UpdatedAt = task.CreatedAt
	return StateDocument{
		Format:       stateDocumentFormat,
		Version:      documentVersion,
		MinReader:    0,
		ProjectID:    serviceTestConfig.ProjectID,
		TaskID:       threadTaskID,
		History:      History{Generation: "01K0M6B8A4FTT8C39MXXYTW7D9"},
		LogicalClock: 1,
		Task:         task,
	}
}

// threadPack builds one pack against a parent, at the next logical clock.
func threadPack(parent StateDocument, operations ...Operation) OperationPack {
	return NewOperationPack(
		parent.ProjectID, parent.TaskID, parent.History.Generation, threadActor,
		parent.LogicalClock+1, threadWallTime, operations,
	)
}

func applyThread(t *testing.T, parent StateDocument, operations ...Operation) StateDocument {
	t.Helper()
	state, err := Apply(&parent, threadPack(parent, operations...), serviceTestConfig.Key)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	return state
}

func comment(id, body string) Operation {
	return Operation{ID: id, Type: OperationCommentAdd, Body: body}
}

func fileAttachment(id, name string) Operation {
	return Operation{ID: id, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
		Name: name, Kind: AttachmentFile, Media: "text/plain", Size: 11, Blob: threadBlobOID,
	}}
}

func linkAttachment(id, url, label string) Operation {
	return Operation{ID: id, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
		Kind: AttachmentLink, URL: url, Label: label,
	}}
}

// A comment takes its identity, author and time from the pack that recorded it.
func TestCommentAddMaterializesTheThreadFromThePack(t *testing.T) {
	state := applyThread(t, threadParent(TaskData{}), comment(commentOneID, "looks good"))

	want := []Comment{{
		ID: commentOneID, Author: threadActor, Body: "looks good", CreatedAt: threadWallTime,
	}}
	if !reflect.DeepEqual(state.Task.Comments, want) {
		t.Fatalf("comments = %#v, want %#v", state.Task.Comments, want)
	}
	if state.Task.Comments[0].Edited() {
		t.Fatal("a new comment reports itself as edited")
	}
	// The watermark is the whole reason this operation exists at generation 1.
	if state.MinReader != 1 {
		t.Fatalf("checkpoint minReader = %d, want 1", state.MinReader)
	}
}

func TestCommentEditRecordsTheEditWithoutRewritingTheOriginal(t *testing.T) {
	parent := applyThread(t, threadParent(TaskData{}), comment(commentOneID, "looks good"))
	edited := applyThread(t, parent, Operation{
		ID: editOneID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "looks good to me",
	})

	got := edited.Task.Comments[0]
	if got.Body != "looks good to me" {
		t.Fatalf("edited body = %q", got.Body)
	}
	if got.ID != commentOneID || got.Author != threadActor || !got.CreatedAt.Equal(threadWallTime) {
		t.Fatalf("an edit rewrote the comment's provenance: %#v", got)
	}
	if !got.Edited() || !got.EditedAt.Equal(threadWallTime) {
		t.Fatalf("edited comment editedAt = %v, want the pack's wall time", got.EditedAt)
	}
}

func TestCommentRemoveDropsOnlyItsOwnComment(t *testing.T) {
	parent := applyThread(t, threadParent(TaskData{}),
		comment(commentOneID, "first"), comment(commentTwoID, "second"))
	state := applyThread(t, parent, Operation{
		ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID,
	})

	if len(state.Task.Comments) != 1 || state.Task.Comments[0].ID != commentTwoID {
		t.Fatalf("comments after removal = %#v", state.Task.Comments)
	}
}

// Duplicate delivery is the property the whole identifier scheme buys. The same
// pack folded twice has to leave one comment, because it is one comment that
// arrived twice.
func TestFoldingTheSameCommentPackTwiceIsIdempotent(t *testing.T) {
	parent := threadParent(TaskData{})
	once := applyThread(t, parent, comment(commentOneID, "looks good"))
	twice := applyThread(t, once, comment(commentOneID, "looks good"))

	if len(twice.Task.Comments) != 1 {
		t.Fatalf("comments after duplicate delivery = %#v, want one", twice.Task.Comments)
	}
	if !reflect.DeepEqual(once.Task.Comments, twice.Task.Comments) {
		t.Fatalf("duplicate delivery changed the thread: %#v then %#v", once.Task.Comments, twice.Task.Comments)
	}
}

func TestFoldingTheSameAttachmentPackTwiceIsIdempotent(t *testing.T) {
	parent := threadParent(TaskData{})
	once := applyThread(t, parent, fileAttachment(attachOneID, "screenshot.png"))
	twice := applyThread(t, once, fileAttachment(attachOneID, "screenshot.png"))

	if len(twice.Task.Attachments) != 1 {
		t.Fatalf("attachments after duplicate delivery = %#v, want one", twice.Task.Attachments)
	}
}

// The tolerant half of the boundary split. An edit or a removal that names a
// comment the history no longer holds is a concurrent removal having won, and
// the fold has to say so by doing nothing rather than by refusing a history
// somebody already committed.
func TestFoldingAnEditOrRemovalOfAMissingCommentChangesNothing(t *testing.T) {
	parent := applyThread(t, threadParent(TaskData{}), comment(commentOneID, "first"))
	for _, operation := range []Operation{
		{ID: editOneID, Type: OperationCommentEdit, CommentID: commentTwoID, Body: "ghost"},
		{ID: removeOneID, Type: OperationCommentRemove, CommentID: commentTwoID},
		{ID: removeOneID, Type: OperationAttachmentRemove, AttachmentID: attachTwoID},
	} {
		state := applyThread(t, parent, operation)
		if !reflect.DeepEqual(state.Task.Comments, parent.Task.Comments) {
			t.Fatalf("%s changed the thread: %#v", operation.Type, state.Task.Comments)
		}
		if !reflect.DeepEqual(state.Task.Attachments, parent.Task.Attachments) {
			t.Fatalf("%s changed the attachments: %#v", operation.Type, state.Task.Attachments)
		}
	}
}

// Two clones editing one comment converge on whichever edit the linearized
// history puts last, with no conflict type between them — the same rule a
// scalar field already follows.
func TestConcurrentCommentEditsConvergeByOperationOrder(t *testing.T) {
	base := applyThread(t, threadParent(TaskData{}), comment(commentOneID, "original"))
	ours := Operation{ID: editOneID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "ours"}
	theirs := Operation{ID: editTwoID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "theirs"}

	oursFirst := applyThread(t, applyThread(t, base, ours), theirs)
	theirsFirst := applyThread(t, applyThread(t, base, theirs), ours)

	if got := oursFirst.Task.Comments[0].Body; got != "theirs" {
		t.Fatalf("body after ours then theirs = %q, want the last edit to win", got)
	}
	if got := theirsFirst.Task.Comments[0].Body; got != "ours" {
		t.Fatalf("body after theirs then ours = %q, want the last edit to win", got)
	}
	// Whichever order a reconciliation produced, every clone that folds that
	// order reaches the same bytes.
	again := applyThread(t, applyThread(t, base, ours), theirs)
	if !reflect.DeepEqual(oursFirst.Task.Comments, again.Task.Comments) {
		t.Fatal("folding one order twice produced two answers")
	}
}

// An edit racing a removal converges on the removal, in both orders. That is
// what makes the pair need no conflict type: there is one answer and it does
// not depend on which clone synchronized first.
func TestAnEditAndARemovalConvergeOnTheRemoval(t *testing.T) {
	base := applyThread(t, threadParent(TaskData{}), comment(commentOneID, "original"))
	edit := Operation{ID: editOneID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "ours"}
	remove := Operation{ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID}

	editFirst := applyThread(t, applyThread(t, base, edit), remove)
	removeFirst := applyThread(t, applyThread(t, base, remove), edit)

	if len(editFirst.Task.Comments) != 0 || len(removeFirst.Task.Comments) != 0 {
		t.Fatalf("thread after edit/remove race = %#v and %#v, want empty in both orders",
			editFirst.Task.Comments, removeFirst.Task.Comments)
	}
}

// The thread's order is the comments' identifiers, so a clone that folded the
// same two comments in the other order reaches identical bytes.
func TestTheThreadIsOrderedByIdentifierRatherThanByFoldOrder(t *testing.T) {
	parent := threadParent(TaskData{})
	forward := applyThread(t, parent, comment(commentOneID, "first"), comment(commentTwoID, "second"))
	backward := applyThread(t, parent, comment(commentTwoID, "second"), comment(commentOneID, "first"))

	forwardBytes, err := EncodeDocument(forward)
	if err != nil {
		t.Fatalf("EncodeDocument(forward) error = %v", err)
	}
	backwardBytes, err := EncodeDocument(backward)
	if err != nil {
		t.Fatalf("EncodeDocument(backward) error = %v", err)
	}
	if !bytes.Equal(forwardBytes, backwardBytes) {
		t.Fatalf("fold order changed the checkpoint:\n%s\n%s", forwardBytes, backwardBytes)
	}
	if forward.Task.Comments[0].ID != commentOneID {
		t.Fatalf("thread order = %#v, want identifier order", forward.Task.Comments)
	}
}

func TestAttachmentsMaterializeBothKindsInOneList(t *testing.T) {
	state := applyThread(t, threadParent(TaskData{}),
		fileAttachment(attachOneID, "trace.log"),
		linkAttachment(attachTwoID, "https://example.test/design", "Design doc"),
	)

	want := []Attachment{
		{ID: attachOneID, Author: threadActor, AddedAt: threadWallTime, AttachmentData: AttachmentData{
			Name: "trace.log", Kind: AttachmentFile, Media: "text/plain", Size: 11, Blob: threadBlobOID,
		}},
		{ID: attachTwoID, Author: threadActor, AddedAt: threadWallTime, AttachmentData: AttachmentData{
			Kind: AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
	}
	if !reflect.DeepEqual(state.Task.Attachments, want) {
		t.Fatalf("attachments = %#v, want %#v", state.Task.Attachments, want)
	}
	if got := LiveAttachmentBytes(state.Task.Attachments); got != 11 {
		t.Fatalf("live attachment bytes = %d, want only the file's 11", got)
	}
}

func TestAttachmentRemoveHidesWithoutTouchingTheOther(t *testing.T) {
	parent := applyThread(t, threadParent(TaskData{}),
		fileAttachment(attachOneID, "trace.log"),
		linkAttachment(attachTwoID, "https://example.test/design", ""),
	)
	state := applyThread(t, parent, Operation{
		ID: removeOneID, Type: OperationAttachmentRemove, AttachmentID: attachOneID,
	})

	if len(state.Task.Attachments) != 1 || state.Task.Attachments[0].ID != attachTwoID {
		t.Fatalf("attachments after removal = %#v", state.Task.Attachments)
	}
	if got := LiveAttachmentBytes(state.Task.Attachments); got != 0 {
		t.Fatalf("live bytes after removing the only file = %d, want 0", got)
	}
}

// The ceilings are not the fold's business. A body far past the authoring
// ceiling still folds, because refusing it would make a task somebody already
// committed unreadable on every clone that fetches it.
func TestTheFoldToleratesWhatTheBoundaryWouldRefuse(t *testing.T) {
	oversized := strings.Repeat("x", MaxCommentBodyBytes+1)
	state := applyThread(t, threadParent(TaskData{}), comment(commentOneID, oversized))
	if got := len(state.Task.Comments[0].Body); got != len(oversized) {
		t.Fatalf("folded body length = %d, want %d", got, len(oversized))
	}

	huge := Operation{ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
		Name: "core.dump", Kind: AttachmentFile, Size: MaxAttachmentFileBytes * 4, Blob: threadBlobOID,
	}}
	attached := applyThread(t, threadParent(TaskData{}), huge)
	if got := LiveAttachmentBytes(attached.Task.Attachments); got != MaxAttachmentFileBytes*4 {
		t.Fatalf("folded attachment size = %d", got)
	}
}

// Shape, on the other hand, is the fold's business: a document nobody could
// have written on purpose is corrupt, not tolerated.
func TestMalformedThreadOperationsAreCorruptData(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		operation Operation
	}{
		{"blank comment body", comment(commentOneID, "   ")},
		{"comment.add naming a comment", Operation{
			ID: commentOneID, Type: OperationCommentAdd, CommentID: commentTwoID, Body: "hi"}},
		{"comment.edit without a comment", Operation{
			ID: editOneID, Type: OperationCommentEdit, Body: "hi"}},
		{"comment.remove carrying a body", Operation{
			ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID, Body: "hi"}},
		{"comment.remove without a comment", Operation{ID: removeOneID, Type: OperationCommentRemove}},
		{"attachment.add without data", Operation{ID: attachOneID, Type: OperationAttachmentAdd}},
		{"attachment of an unknown kind", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{Kind: "paste"}}},
		{"file attachment without a blob", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd,
			Attachment: &AttachmentData{Name: "a.txt", Kind: AttachmentFile}}},
		{"file attachment with an abbreviated blob", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd,
			Attachment: &AttachmentData{Name: "a.txt", Kind: AttachmentFile, Blob: threadBlobOID[:12]}}},
		{"file attachment with a path in its name", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd,
			Attachment: &AttachmentData{Name: "../a.txt", Kind: AttachmentFile, Blob: threadBlobOID}}},
		{"file attachment carrying a link", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
				Name: "a.txt", Kind: AttachmentFile, Blob: threadBlobOID, URL: "https://example.test"}}},
		{"link attachment without a URL", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{Kind: AttachmentLink}}},
		{"link attachment carrying a blob", Operation{
			ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
				Kind: AttachmentLink, URL: "https://example.test", Blob: threadBlobOID}}},
		{"attachment.remove without an attachment", Operation{ID: removeOneID, Type: OperationAttachmentRemove}},
		{"field.set carrying a comment body", Operation{
			ID: editOneID, Type: OperationFieldSet, Field: "title", Value: "New", Body: "hi"}},
		{"task.tombstone carrying an attachment", Operation{
			ID: removeOneID, Type: OperationTaskTombstone, AttachmentID: attachOneID}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			parent := threadParent(TaskData{})
			_, err := Apply(&parent, threadPack(parent, testCase.operation), serviceTestConfig.Key)
			if got := CategoryOf(err); got != CategoryCorruptData {
				t.Fatalf("Apply() category = %q, want %q; error = %v", got, CategoryCorruptData, err)
			}
		})
	}
}

// A created task has nothing said about it yet. A create carrying a thread
// would assert comments no operation authored, which nothing could then remove.
func TestTaskCreateMayNotCarryAThread(t *testing.T) {
	task := threadParent(TaskData{}).Task
	task.Comments = []Comment{{ID: commentOneID, Author: threadActor, Body: "hi", CreatedAt: threadWallTime}}
	pack := NewOperationPack(
		serviceTestConfig.ProjectID, threadTaskID, "01K0M6B8A4FTT8C39MXXYTW7D9", threadActor, 1, threadWallTime,
		[]Operation{{ID: commentTwoID, Type: OperationTaskCreate, Task: &task}},
	)
	_, err := Apply(nil, pack, serviceTestConfig.Key)
	if got := CategoryOf(err); got != CategoryCorruptData {
		t.Fatalf("Apply() category = %q, want %q; error = %v", got, CategoryCorruptData, err)
	}
}

// The marker is per operation type, so a pack that changes a title beside a
// comment needs the newer reader and a pack that only changes the title does
// not — which is the whole point of declaring the requirement per type.
func TestOnlyThreadOperationsRaiseTheWriterFormatMarker(t *testing.T) {
	plain := threadPack(threadParent(TaskData{}), Operation{
		ID: editOneID, Type: OperationFieldSet, Field: "title", Value: "Renamed"})
	if plain.MinReader != 0 {
		t.Fatalf("a field.set pack carries minReader %d, want none", plain.MinReader)
	}
	encoded, err := EncodeDocument(plain)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("minReader")) {
		t.Fatalf("a field.set pack encodes the marker: %s", encoded)
	}

	for _, operation := range []Operation{
		comment(commentOneID, "hi"),
		{ID: editOneID, Type: OperationCommentEdit, CommentID: commentOneID, Body: "hi"},
		{ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID},
		fileAttachment(attachOneID, "a.txt"),
		{ID: removeOneID, Type: OperationAttachmentRemove, AttachmentID: attachOneID},
	} {
		pack := threadPack(threadParent(TaskData{}), operation)
		if pack.MinReader != 1 {
			t.Fatalf("%s pack carries minReader %d, want 1", operation.Type, pack.MinReader)
		}
	}

	// And a mixed pack takes the highest requirement its operations imply.
	mixed := threadPack(threadParent(TaskData{}),
		Operation{ID: editOneID, Type: OperationFieldSet, Field: "title", Value: "Renamed"},
		comment(commentOneID, "and a remark"),
	)
	if mixed.MinReader != 1 {
		t.Fatalf("mixed pack minReader = %d, want 1", mixed.MinReader)
	}
}

// A task with an empty thread encodes to the bytes it encoded to before either
// member existed. The golden tables assert this over real refs; this states it
// as the property they are instances of.
func TestAnEmptyThreadIsAbsentFromTheCheckpoint(t *testing.T) {
	parent := threadParent(TaskData{})
	state := applyThread(t, parent, Operation{
		ID: editOneID, Type: OperationFieldSet, Field: "title", Value: "Renamed"})
	encoded, err := EncodeDocument(state)
	if err != nil {
		t.Fatalf("EncodeDocument() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("comments")) || bytes.Contains(encoded, []byte("attachments")) {
		t.Fatalf("an empty thread appears in the checkpoint: %s", encoded)
	}

	// And a thread emptied by removals goes back to absent rather than to an
	// empty array, so a task that gained and lost a comment is byte-identical
	// to one that never had one.
	added := applyThread(t, parent, comment(commentOneID, "hi"))
	removed := applyThread(t, added, Operation{
		ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID})
	emptied, err := EncodeDocument(removed)
	if err != nil {
		t.Fatalf("EncodeDocument(emptied) error = %v", err)
	}
	if bytes.Contains(emptied, []byte("comments")) {
		t.Fatalf("an emptied thread left a member behind: %s", emptied)
	}
}

// The change log says what a thread operation did. A pack whose operations the
// log cannot describe renders as an entry with no fields and the summary
// "recorded no visible change", which is exactly the wrong thing to say about
// somebody's remark.
func TestTheChangeLogDescribesThreadOperations(t *testing.T) {
	parent := threadParent(TaskData{})
	added := applyThread(t, parent, comment(commentOneID, "looks good"))
	removed := applyThread(t, added, Operation{
		ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID})
	attached := applyThread(t, removed, linkAttachment(attachOneID, "https://example.test/pr", "The pull request"))

	history := TaskHistory{Entries: []HistoryEntry{
		{Commit: "commit-1", Operation: threadPack(parent, Operation{
			ID: commentTwoID, Type: OperationTaskCreate, Task: &parent.Task})},
		{Commit: "commit-2", Operation: threadPack(parent, comment(commentOneID, "looks good"))},
		{Commit: "commit-3", Operation: threadPack(added, Operation{
			ID: removeOneID, Type: OperationCommentRemove, CommentID: commentOneID})},
		{Commit: "commit-4", Operation: threadPack(removed,
			linkAttachment(attachOneID, "https://example.test/pr", "The pull request"))},
	}}
	// The root entry has to be a genuine root for the replay to start, so the
	// create pack's clock is rewritten to one.
	history.Entries[0].Operation.LogicalClock = 1
	history.Entries[1].Operation.LogicalClock = 2
	history.Entries[2].Operation.LogicalClock = 3
	history.Entries[3].Operation.LogicalClock = 4
	for index := 1; index < len(history.Entries); index++ {
		history.Entries[index].Parent = history.Entries[index-1].Commit
	}
	if len(attached.Task.Attachments) != 1 {
		t.Fatalf("fixture attachments = %#v", attached.Task.Attachments)
	}

	log := BuildChangeLog(serviceTestConfig.Key, history, 0, true)
	if log.Truncated != nil {
		t.Fatalf("change log truncated at %#v", log.Truncated)
	}
	if len(log.Changes) != 4 {
		t.Fatalf("change log = %#v, want four entries", log.Changes)
	}
	wants := []FieldChange{
		{Field: "comment", Kind: ChangeAdded, To: "looks good"},
		{Field: "comment", Kind: ChangeRemoved, From: "looks good"},
		{Field: "attachment", Kind: ChangeAdded, To: "The pull request"},
	}
	for index, want := range wants {
		fields := log.Changes[index+1].Fields
		if len(fields) != 1 || !reflect.DeepEqual(fields[0], want) {
			t.Fatalf("change %d fields = %#v, want %#v", index+1, fields, want)
		}
	}
}

func TestAttachmentMediaTypeIsDerivedWithoutTheOperatingSystem(t *testing.T) {
	for name, want := range map[string]string{
		"shot.PNG":     "image/png",
		"trace.log":    "text/plain",
		"notes.md":     "text/markdown",
		"archive.zip":  "application/zip",
		"mystery":      DefaultAttachmentMedia,
		"diagram.svg":  DefaultAttachmentMedia,
		"data.unknown": DefaultAttachmentMedia,
	} {
		if got := AttachmentMediaType(name); got != want {
			t.Fatalf("AttachmentMediaType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateAttachmentURLAcceptsOnlyWebLinks(t *testing.T) {
	for _, accepted := range []string{
		"https://example.test/design",
		"http://example.test:8080/a?b=c#d",
	} {
		if err := ValidateAttachmentURL(accepted); err != nil {
			t.Fatalf("ValidateAttachmentURL(%q) error = %v", accepted, err)
		}
	}
	for _, refused := range []string{
		"",
		"   ",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"ftp://example.test/x",
		"https://",
		"example.test/design",
		"https://example.test/" + strings.Repeat("a", MaxAttachmentURLBytes),
	} {
		err := ValidateAttachmentURL(refused)
		if got := CategoryOf(err); got != CategoryValidation {
			t.Fatalf("ValidateAttachmentURL(%q) category = %q, want %q", refused, got, CategoryValidation)
		}
	}
}

// The service's half: what a person is told, which is where every ceiling and
// every missing-subject refusal lives.

type stagedBlob struct {
	content []byte
}

type blobStoreSpy struct {
	staged   []stagedBlob
	objectID string
	err      error
}

func (s *blobStoreSpy) StageAttachment(_ context.Context, _ ProjectConfig, content []byte) (string, error) {
	s.staged = append(s.staged, stagedBlob{content: append([]byte(nil), content...)})
	if s.err != nil {
		return "", s.err
	}
	if s.objectID != "" {
		return s.objectID, nil
	}
	return threadBlobOID, nil
}

// blobReaderSpy is the read half, which no writing test needs and which the two
// refusals below are entirely about.
type blobReaderSpy struct {
	read     []string
	contents []byte
	err      error
}

func (s *blobReaderSpy) ReadAttachment(_ context.Context, _ ProjectConfig, objectID string) ([]byte, error) {
	s.read = append(s.read, objectID)
	if s.err != nil {
		return nil, s.err
	}
	return s.contents, nil
}

func threadServiceUnderTest(t *testing.T, task TaskData, ids ...string) (Service, *memoryTaskStore, *blobStoreSpy) {
	t.Helper()
	store := newMemoryTaskStore(serviceSnapshot(threadTaskID, task))
	blobs := &blobStoreSpy{}
	service := serviceUnderTest(store, &sequenceIDSource{values: append([]string{
		"01K0M6B8A4FTT8C39MXXYTWF01",
		"01K0M6B8A4FTT8C39MXXYTWF02",
		"01K0M6B8A4FTT8C39MXXYTWF03",
	}, ids...)})
	service.Blobs = blobs
	return service, store, blobs
}

func TestCommentAddMutationWritesOnePackWhoseOperationIDIsTheCommentID(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})

	result, err := service.CommentAddMutation(context.Background(), threadTaskID, CommentAddInput{Body: "  shipped  "})
	if err != nil {
		t.Fatalf("CommentAddMutation() error = %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want one pack", len(store.writes))
	}
	operations := store.writes[0].pack.Operations
	if len(operations) != 1 || operations[0].Type != OperationCommentAdd {
		t.Fatalf("operations = %#v, want one comment.add", operations)
	}
	if operations[0].Body != "shipped" {
		t.Fatalf("stored body = %q, want it trimmed", operations[0].Body)
	}
	if len(result.Task.Comments) != 1 || result.Task.Comments[0].ID != operations[0].ID {
		t.Fatalf("comment ID = %#v, want the operation's %q", result.Task.Comments, operations[0].ID)
	}
	if got, want := store.writes[0].reason, "workbook: update WB-01K0M6B8 comment added"; got != want {
		t.Fatalf("commit subject = %q, want %q", got, want)
	}
}

// One pack, whatever a caller combined into it. This is the promise the command
// line's `update X --status done --comment "shipped"` rests on.
func TestAnUpdateCarriesItsCommentAndItsFieldsInOnePack(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})

	done := StatusDone
	result, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{
		Status:      &done,
		Comments:    []CommentChange{{Body: "shipped"}},
		Attachments: []AttachmentChange{{Kind: AttachmentLink, URL: "https://example.test/pr"}},
	})
	if err != nil {
		t.Fatalf("UpdateMutation() error = %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want exactly one pack", len(store.writes))
	}
	types := []OperationType{}
	for _, operation := range store.writes[0].pack.Operations {
		types = append(types, operation.Type)
	}
	want := []OperationType{OperationFieldSet, OperationCommentAdd, OperationAttachmentAdd}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("operation types = %v, want %v", types, want)
	}
	if result.Task.Status != StatusDone || len(result.Task.Comments) != 1 || len(result.Task.Attachments) != 1 {
		t.Fatalf("resulting task = %#v", result.Task)
	}
	if store.writes[0].pack.MinReader != 1 {
		t.Fatalf("combined pack minReader = %d, want 1", store.writes[0].pack.MinReader)
	}
}

func TestEditingOrRemovingAMissingCommentIsRefusedAtTheBoundary(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	for _, testCase := range []struct {
		name string
		call func(Service) error
	}{
		{"edit", func(s Service) error {
			_, err := s.CommentEditMutation(context.Background(), threadTaskID,
				CommentEditInput{CommentID: commentTwoID, Body: "ghost"})
			return err
		}},
		{"remove", func(s Service) error {
			_, err := s.CommentRemoveMutation(context.Background(), threadTaskID,
				CommentRemoveInput{CommentID: commentTwoID})
			return err
		}},
		{"attachment remove", func(s Service) error {
			_, err := s.AttachmentRemoveMutation(context.Background(), threadTaskID,
				AttachmentRemoveInput{AttachmentID: attachTwoID})
			return err
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, store, _ := threadServiceUnderTest(t, base)
			err := testCase.call(service)
			if got := CategoryOf(err); got != CategoryNotFound {
				t.Fatalf("category = %q, want %q; error = %v", got, CategoryNotFound, err)
			}
			if len(store.writes) != 0 {
				t.Fatalf("a refused mutation wrote %d packs", len(store.writes))
			}
		})
	}
}

func TestABlankCommentBodyIsRefusedWithoutWriting(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	_, err := service.CommentAddMutation(context.Background(), threadTaskID, CommentAddInput{Body: "   "})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused comment wrote %d packs", len(store.writes))
	}
}

func TestACommentBodyOverTheCeilingIsRefusedAtTheBoundary(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	_, err := service.CommentAddMutation(context.Background(), threadTaskID,
		CommentAddInput{Body: strings.Repeat("x", MaxCommentBodyBytes+1)})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused comment wrote %d packs", len(store.writes))
	}
}

// The two file ceilings, and the advice they carry. Neither refusal may have
// staged the bytes it refused.
func TestFileAttachmentCeilingsRefuseAndSuggestALink(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	t.Run("one file over the per-file ceiling", func(t *testing.T) {
		service, store, blobs := threadServiceUnderTest(t, base)
		_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
			Kind: AttachmentFile, Name: "core.dump", Content: make([]byte, MaxAttachmentFileBytes+1),
		})
		if got := CategoryOf(err); got != CategoryValidation {
			t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
		}
		if !strings.Contains(err.Error(), "attach a link instead") {
			t.Fatalf("refusal = %q, want it to suggest a link", err)
		}
		if len(blobs.staged) != 0 {
			t.Fatalf("a refused attachment staged %d blobs", len(blobs.staged))
		}
		if len(store.writes) != 0 {
			t.Fatalf("a refused attachment wrote %d packs", len(store.writes))
		}
	})

	t.Run("one file past the live total", func(t *testing.T) {
		crowded := base
		crowded.Attachments = []Attachment{{
			ID: attachOneID, Author: threadActor, AddedAt: threadWallTime,
			AttachmentData: AttachmentData{
				Name: "already.bin", Kind: AttachmentFile, Size: MaxLiveAttachmentBytes, Blob: threadBlobOID,
			},
		}}
		service, store, blobs := threadServiceUnderTest(t, crowded)
		_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
			Kind: AttachmentFile, Name: "one-more.png", Content: []byte("more bytes"),
		})
		if got := CategoryOf(err); got != CategoryValidation {
			t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
		}
		if !strings.Contains(err.Error(), "attach a link instead") {
			t.Fatalf("refusal = %q, want it to suggest a link", err)
		}
		// This is the half that was claimed and not checked: the ceiling has to
		// refuse before the bytes are written, or a refused attachment leaves
		// its object in the database for a later `git gc` to find.
		if len(blobs.staged) != 0 {
			t.Fatalf("a refused attachment staged %d blobs", len(blobs.staged))
		}
		if len(store.writes) != 0 {
			t.Fatalf("a refused attachment wrote %d packs", len(store.writes))
		}
	})

	// The budget's growth rule, pinned where a flat ceiling would answer
	// differently: the swap below leaves the task *still* over the ceiling and
	// yet smaller than it was. validateThreadGrowth allows that after the fold,
	// so the price charged before the bytes are staged has to allow it too — a
	// budget that compared only against the ceiling would refuse a mutation the
	// fold would have accepted, which is the one way the two checks can
	// disagree about what is allowed.
	t.Run("a shrinking swap that stays over the ceiling", func(t *testing.T) {
		over := base
		over.Attachments = []Attachment{
			{
				ID: attachOneID, Author: threadActor, AddedAt: threadWallTime,
				AttachmentData: AttachmentData{
					Name: "huge.bin", Kind: AttachmentFile,
					Size: MaxLiveAttachmentBytes * 2, Blob: threadBlobOID,
				},
			},
			{
				ID: attachTwoID, Author: threadActor, AddedAt: threadWallTime,
				AttachmentData: AttachmentData{
					Name: "large.bin", Kind: AttachmentFile,
					Size: MaxLiveAttachmentBytes + 1, Blob: threadBlobOID,
				},
			},
		}
		service, store, blobs := threadServiceUnderTest(t, over)
		result, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{
			Attachments: []AttachmentChange{
				{AttachmentID: attachOneID, Remove: true},
				{Kind: AttachmentFile, Name: "small.png", Content: []byte("a few bytes")},
			},
		})
		if err != nil {
			t.Fatalf("UpdateMutation() error = %v; a swap that shrinks an over-ceiling task must be admitted "+
				"before staging, exactly as the post-fold check admits it", err)
		}
		if got := LiveAttachmentBytes(result.Task.Attachments); got <= MaxLiveAttachmentBytes {
			t.Fatalf("live bytes after the swap = %d, want the task still over %d; the fixture stopped "+
				"exercising the growth rule", got, MaxLiveAttachmentBytes)
		}
		if len(blobs.staged) != 1 || len(store.writes) != 1 {
			t.Fatalf("swap staged %d blobs and wrote %d packs, want one of each",
				len(blobs.staged), len(store.writes))
		}
	})

	// The pre-staging price and the post-fold ceiling have to agree, including
	// where the growth rule makes them permissive: a task already over the
	// ceiling may swap a large attachment for a smaller one, and a budget that
	// only compared against the ceiling would refuse that.
	t.Run("a swap that shrinks an over-ceiling task", func(t *testing.T) {
		over := base
		over.Attachments = []Attachment{{
			ID: attachOneID, Author: threadActor, AddedAt: threadWallTime,
			AttachmentData: AttachmentData{
				Name: "already.bin", Kind: AttachmentFile, Size: MaxLiveAttachmentBytes * 2, Blob: threadBlobOID,
			},
		}}
		service, store, blobs := threadServiceUnderTest(t, over)
		result, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{
			Attachments: []AttachmentChange{
				{AttachmentID: attachOneID, Remove: true},
				{Kind: AttachmentFile, Name: "smaller.png", Content: []byte("a few bytes")},
			},
		})
		if err != nil {
			t.Fatalf("UpdateMutation() error = %v; shrinking below what a task already holds must be allowed", err)
		}
		if len(blobs.staged) != 1 || len(store.writes) != 1 {
			t.Fatalf("swap staged %d blobs and wrote %d packs, want one of each", len(blobs.staged), len(store.writes))
		}
		if len(result.Task.Attachments) != 1 || result.Task.Attachments[0].Name != "smaller.png" {
			t.Fatalf("attachments after the swap = %#v", result.Task.Attachments)
		}
	})

	// And a task already over a ceiling can still be worked with, which is what
	// keeps a concurrent pair of additions from wedging a task forever.
	t.Run("a task already over the ceiling still moves", func(t *testing.T) {
		over := base
		over.Attachments = []Attachment{{
			ID: attachOneID, Author: threadActor, AddedAt: threadWallTime,
			AttachmentData: AttachmentData{
				Name: "already.bin", Kind: AttachmentFile, Size: MaxLiveAttachmentBytes * 2, Blob: threadBlobOID,
			},
		}}
		service, store, _ := threadServiceUnderTest(t, over)
		title := "Renamed anyway"
		if _, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{Title: &title}); err != nil {
			t.Fatalf("UpdateMutation() error = %v; a ceiling this pack does not raise must not refuse it", err)
		}
		if len(store.writes) != 1 {
			t.Fatalf("writes = %d, want one", len(store.writes))
		}
		// Removing an attachment while over the ceiling is the way back under,
		// so it must never be refused.
		service, store, _ = threadServiceUnderTest(t, over)
		if _, err := service.AttachmentRemoveMutation(context.Background(), threadTaskID,
			AttachmentRemoveInput{AttachmentID: attachOneID}); err != nil {
			t.Fatalf("AttachmentRemoveMutation() error = %v; shrinking must always be allowed", err)
		}
		if len(store.writes) != 1 {
			t.Fatalf("writes = %d, want one", len(store.writes))
		}
	})
}

func TestAttachingAFileStagesTheBytesAndNamesTheBlob(t *testing.T) {
	service, store, blobs := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})

	result, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentFile, Name: "trace.log", Content: []byte("hello world"),
	})
	if err != nil {
		t.Fatalf("AttachmentAddMutation() error = %v", err)
	}
	if len(blobs.staged) != 1 || string(blobs.staged[0].content) != "hello world" {
		t.Fatalf("staged = %#v, want the file's bytes once", blobs.staged)
	}
	attached := result.Task.Attachments[0]
	if attached.Blob != threadBlobOID || attached.Size != 11 || attached.Media != "text/plain" {
		t.Fatalf("attachment = %#v", attached)
	}
	if attached.ID != store.writes[0].pack.Operations[0].ID {
		t.Fatalf("attachment ID = %q, want the operation's %q", attached.ID, store.writes[0].pack.Operations[0].ID)
	}
}

func TestAttachingALinkStoresNothingAndValidatesTheScheme(t *testing.T) {
	service, _, blobs := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	result, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentLink, URL: "https://example.test/design", Label: "  Design doc  ",
	})
	if err != nil {
		t.Fatalf("AttachmentAddMutation() error = %v", err)
	}
	if len(blobs.staged) != 0 {
		t.Fatalf("a link staged %d blobs, want none", len(blobs.staged))
	}
	if got := result.Task.Attachments[0]; got.Label != "Design doc" || got.Size != 0 || got.Blob != "" {
		t.Fatalf("link attachment = %#v", got)
	}

	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	_, err = service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentLink, URL: "javascript:alert(1)",
	})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused link wrote %d packs", len(store.writes))
	}
}

// threadFixtureComment builds the nth comment of a crowded thread. The
// identifiers are sequential so the fixture is already in the canonical order
// the fold would put it in.
func threadFixtureComment(index int) Comment {
	return Comment{
		ID:        fmt.Sprintf("01K0M6B8A4FTT8C39MXXY%05d", index),
		Author:    threadActor,
		Body:      fmt.Sprintf("remark %d", index),
		CreatedAt: threadWallTime,
	}
}

func threadFixtureAttachment(index int) Attachment {
	return Attachment{
		ID:      fmt.Sprintf("01K0M6B8A4FTT8C39MXXZ%05d", index),
		Author:  threadActor,
		AddedAt: threadWallTime,
		AttachmentData: AttachmentData{
			Kind: AttachmentLink,
			URL:  fmt.Sprintf("https://example.test/%d", index),
		},
	}
}

// The two count ceilings, which nothing else in the suite would miss.
//
// Both were claimed and neither was checked: deleting the guards left every
// package green. What they have to do is refuse the addition that crosses the
// bound, name the bound, and — because two people can cross one concurrently
// without either being told — keep letting a task that is already over it be
// worked with and, above all, be shrunk.
func TestTheCommentCountCeilingRefusesGrowthAndAllowsRepair(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	full := base
	for index := 0; index < MaxCommentCount; index++ {
		full.Comments = append(full.Comments, threadFixtureComment(index))
	}

	service, store, _ := threadServiceUnderTest(t, full)
	_, err := service.CommentAddMutation(context.Background(), threadTaskID, CommentAddInput{Body: "one too many"})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(MaxCommentCount)) {
		t.Fatalf("refusal = %q, want it to name the ceiling %d", err, MaxCommentCount)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused comment wrote %d packs", len(store.writes))
	}

	// A task carried past the ceiling by a replay nobody could have written
	// differently is not a wedged task.
	over := full
	over.Comments = append(append([]Comment(nil), full.Comments...), threadFixtureComment(MaxCommentCount))
	title := "Renamed anyway"
	service, store, _ = threadServiceUnderTest(t, over)
	if _, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{Title: &title}); err != nil {
		t.Fatalf("UpdateMutation() error = %v; a ceiling this pack does not raise must not refuse it", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want one", len(store.writes))
	}

	service, store, _ = threadServiceUnderTest(t, over)
	if _, err := service.CommentRemoveMutation(context.Background(), threadTaskID,
		CommentRemoveInput{CommentID: over.Comments[0].ID}); err != nil {
		t.Fatalf("CommentRemoveMutation() error = %v; the way back under a ceiling must never be refused", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want one", len(store.writes))
	}
}

func TestTheAttachmentCountCeilingRefusesGrowthAndAllowsRepair(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	full := base
	for index := 0; index < MaxAttachmentCount; index++ {
		full.Attachments = append(full.Attachments, threadFixtureAttachment(index))
	}

	service, store, _ := threadServiceUnderTest(t, full)
	_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentLink, URL: "https://example.test/one-too-many",
	})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(MaxAttachmentCount)) {
		t.Fatalf("refusal = %q, want it to name the ceiling %d", err, MaxAttachmentCount)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused attachment wrote %d packs", len(store.writes))
	}

	// A task carried past the ceiling by a replay nobody could have written
	// differently is not a wedged task. This block is the only assertion the
	// growth term in the guard changes, and its absence is why dropping that
	// term used to leave every package green.
	over := full
	over.Attachments = append(append([]Attachment(nil), full.Attachments...),
		threadFixtureAttachment(MaxAttachmentCount))
	title := "Renamed anyway"
	service, store, _ = threadServiceUnderTest(t, over)
	if _, err := service.UpdateMutation(context.Background(), threadTaskID, UpdateInput{Title: &title}); err != nil {
		t.Fatalf("UpdateMutation() error = %v; a ceiling this pack does not raise must not refuse it", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want one", len(store.writes))
	}

	service, store, _ = threadServiceUnderTest(t, over)
	if _, err := service.AttachmentRemoveMutation(context.Background(), threadTaskID,
		AttachmentRemoveInput{AttachmentID: over.Attachments[0].ID}); err != nil {
		t.Fatalf("AttachmentRemoveMutation() error = %v; the way back under a ceiling must never be refused", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("writes = %d, want one", len(store.writes))
	}
}

// A stored media type is written into an HTTP response header by the web board,
// so the shape rule is a security rule and these are the shapes that make it
// one. Each is refused by the fold as corrupt data — a document nobody wrote on
// purpose — rather than tolerated into a checkpoint some later reader trusts.
func TestAStoredMediaTypeIsRefusedUnlessItIsAPlainToken(t *testing.T) {
	for _, media := range []string{
		"text/plain\r\nX-Injected: 1",
		"text/plain\nX-Injected: 1",
		"text/plain; charset=utf-8",
		"text/plain ",
		" text/plain",
		`text/"plain"`,
		"text/plain, text/html",
		"Text/Plain",
		"text",
		"text/",
		"/plain",
		"text/plain/extra",
	} {
		parent := threadParent(TaskData{})
		operation := Operation{ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
			Name: "a.txt", Kind: AttachmentFile, Media: media, Size: 1, Blob: threadBlobOID,
		}}
		_, err := Apply(&parent, threadPack(parent, operation), serviceTestConfig.Key)
		if got := CategoryOf(err); got != CategoryCorruptData {
			t.Fatalf("Apply(media %q) category = %q, want %q; error = %v",
				media, got, CategoryCorruptData, err)
		}
	}

	// And the ordinary ones still fold, so the rule is a filter rather than a
	// wall.
	for _, media := range []string{"text/plain", "image/png", "application/vnd.api+json", "text/x-diff"} {
		parent := threadParent(TaskData{})
		operation := Operation{ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
			Name: "a.bin", Kind: AttachmentFile, Media: media, Size: 1, Blob: threadBlobOID,
		}}
		if _, err := Apply(&parent, threadPack(parent, operation), serviceTestConfig.Key); err != nil {
			t.Fatalf("Apply(media %q) error = %v", media, err)
		}
	}
}

// The SVG rule, at both layers, because one layer was not enough: the extension
// table declines to derive an SVG type, and a caller can name one anyway.
//
// The property being defended is the web board's, and it is a property of what
// is *stored*: the board serves image/* inline, so an attachment stored as an
// SVG image would be script on the board's origin no matter what the upload
// path checked.
func TestAnSVGIsNeverStoredAsAnImage(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	for _, media := range []string{"image/svg+xml", "image/svg"} {
		service, store, blobs := threadServiceUnderTest(t, base)
		_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
			Kind: AttachmentFile, Name: "diagram.svg", Content: []byte("<svg onload=\"alert(1)\"/>"), Media: media,
		})
		if got := CategoryOf(err); got != CategoryValidation {
			t.Fatalf("AttachmentAddMutation(%q) category = %q, want %q; error = %v",
				media, got, CategoryValidation, err)
		}
		if len(blobs.staged) != 0 || len(store.writes) != 0 {
			t.Fatalf("a refused SVG staged %d blobs and wrote %d packs", len(blobs.staged), len(store.writes))
		}

		// The fold refuses one too, so a clone that got the operation from
		// somewhere else cannot hand the board an inline SVG either.
		parent := threadParent(TaskData{})
		operation := Operation{ID: attachOneID, Type: OperationAttachmentAdd, Attachment: &AttachmentData{
			Name: "diagram.svg", Kind: AttachmentFile, Media: media, Size: 1, Blob: threadBlobOID,
		}}
		if _, err := Apply(&parent, threadPack(parent, operation), serviceTestConfig.Key); CategoryOf(err) != CategoryCorruptData {
			t.Fatalf("Apply(media %q) category = %q, want %q; error = %v",
				media, CategoryOf(err), CategoryCorruptData, err)
		}
	}

	// The file itself is still attachable. It is stored as bytes to download,
	// which is what the extension table's silence about `.svg` produces.
	service, _, _ := threadServiceUnderTest(t, base)
	result, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentFile, Name: "diagram.svg", Content: []byte("<svg/>"),
	})
	if err != nil {
		t.Fatalf("AttachmentAddMutation(no media) error = %v; an SVG must still be attachable", err)
	}
	if got := result.Task.Attachments[0].Media; got != DefaultAttachmentMedia {
		t.Fatalf("stored media = %q, want %q", got, DefaultAttachmentMedia)
	}
}

func TestACallerSuppliedMediaTypeIsCheckedAtTheBoundary(t *testing.T) {
	base := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	service, store, blobs := threadServiceUnderTest(t, base)
	_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentFile, Name: "trace.log", Content: []byte("hello"), Media: "text/plain; charset=utf-8",
	})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 || len(blobs.staged) != 0 {
		t.Fatalf("a refused media type wrote %d packs and staged %d blobs", len(store.writes), len(blobs.staged))
	}

	service, _, _ = threadServiceUnderTest(t, base)
	result, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentFile, Name: "mystery", Content: []byte("hello"), Media: "image/png",
	})
	if err != nil {
		t.Fatalf("AttachmentAddMutation() error = %v", err)
	}
	if got := result.Task.Attachments[0].Media; got != "image/png" {
		t.Fatalf("media = %q, want the caller's", got)
	}
}

func TestAttachingAFileWithoutABlobStoreIsOperational(t *testing.T) {
	service, _, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	service.Blobs = nil
	_, err := service.AttachmentAddMutation(context.Background(), threadTaskID, AttachmentAddInput{
		Kind: AttachmentFile, Name: "trace.log", Content: []byte("hello"),
	})
	if got := CategoryOf(err); got != CategoryOperational {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryOperational, err)
	}
}

// AttachmentContent is the one read every surface that hands an attachment back
// goes through — `show --get-attachment` and the board's download route — and
// the two things it adds over the blob read are refusals. Both are stated here
// rather than on either surface, because both surfaces are entitled to assume
// them: the board's route decides its response headers from the attachment it
// was handed, and a link that reached the blob read would be asking Git for the
// empty object ID.
func TestAttachmentContentRefusesALinkAndAServiceWithNoReader(t *testing.T) {
	service, _, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	reader := &blobReaderSpy{contents: []byte("hello world")}
	service.BlobReads = reader

	link := Attachment{ID: attachOneID, AttachmentData: AttachmentData{
		Kind: AttachmentLink, URL: "https://example.test/design",
	}}
	content, err := service.AttachmentContent(context.Background(), link)
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if content != nil {
		t.Fatalf("a refused link returned %d bytes", len(content))
	}
	if !strings.Contains(err.Error(), attachOneID) {
		t.Fatalf("refusal = %q, want the attachment named", err.Error())
	}
	if len(reader.read) != 0 {
		t.Fatalf("a link reached the blob reader: %#v", reader.read)
	}

	file := Attachment{ID: attachTwoID, AttachmentData: AttachmentData{
		Kind: AttachmentFile, Name: "trace.log", Media: "text/plain", Size: 11, Blob: threadBlobOID,
	}}
	content, err = service.AttachmentContent(context.Background(), file)
	if err != nil {
		t.Fatalf("AttachmentContent() error = %v", err)
	}
	if string(content) != "hello world" {
		t.Fatalf("content = %q, want the blob's bytes", content)
	}
	if len(reader.read) != 1 || reader.read[0] != threadBlobOID {
		t.Fatalf("blob reads = %#v, want the recorded object ID once", reader.read)
	}

	// A service built without the read half answers as a missing capability
	// rather than as a failure of the attachment: nothing is wrong with the
	// attachment, and this build simply cannot hand it back.
	service.BlobReads = nil
	if _, err := service.AttachmentContent(context.Background(), file); CategoryOf(err) != CategoryOperational {
		t.Fatalf("category = %q, want %q; error = %v", CategoryOf(err), CategoryOperational, err)
	}
}

func TestAnEditThatChangesNothingIsRefusedRatherThanWritten(t *testing.T) {
	task := TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"}
	task.Comments = []Comment{{ID: commentOneID, Author: threadActor, Body: "unchanged", CreatedAt: threadWallTime}}
	service, store, _ := threadServiceUnderTest(t, task)

	_, err := service.CommentEditMutation(context.Background(), threadTaskID,
		CommentEditInput{CommentID: commentOneID, Body: "  unchanged  "})
	if got := CategoryOf(err); got != CategoryValidation {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryValidation, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a no-op edit wrote %d packs", len(store.writes))
	}
}

func TestExpectedHeadIsHonoredByEveryThreadMutation(t *testing.T) {
	service, store, _ := threadServiceUnderTest(t, TaskData{Title: "Task", Status: StatusBacklog, Priority: PriorityMedium, Rank: "1/1"})
	_, err := service.CommentAddMutation(context.Background(), threadTaskID,
		CommentAddInput{Body: "shipped", ExpectedHead: "some-other-head"})
	if got := CategoryOf(err); got != CategoryStaleWrite {
		t.Fatalf("category = %q, want %q; error = %v", got, CategoryStaleWrite, err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a stale write wrote %d packs", len(store.writes))
	}
}
