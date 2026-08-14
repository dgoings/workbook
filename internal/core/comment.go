package core

import (
	"sort"
	"strings"
	"time"
)

// Comment is one remark on a task, as the checkpoint materializes it.
//
// Everything here except the body comes from the operation pack that recorded
// it rather than from anything a caller typed. The identifier is the adding
// operation's own ULID, the author is the pack's actor, and the timestamps are
// the pack's wall time — which is why a comment cannot claim an author it did
// not have or a time it was not written at without the pack claiming the same
// thing about every other operation it carries.
//
// There is no `edited` member beside EditedAt, deliberately. A canonical
// document with two spellings of one fact has two ways to be wrong and one of
// them is unrecoverable, because both spellings are already on other clones'
// refs. The flag every consumer wants is EditedAt's presence, which the Edited
// accessor states in Go and which reads in JSON as the member being there.
type Comment struct {
	ID        string     `json:"id"`
	Author    string     `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	EditedAt  *time.Time `json:"editedAt,omitempty"`
}

// Edited reports a comment whose body has been changed since it was written.
func (comment Comment) Edited() bool {
	return comment.EditedAt != nil
}

// normalizeComments puts a task's thread in its one canonical order and shape.
//
// The order is by identifier, and identifiers are ULIDs minted when the comment
// was written, so this is creation order. It is chosen over the order the fold
// happened to append in because two clones that folded the same comments in
// different orders — which is exactly what a reconciliation produces on the two
// sides of a divergence before either has replayed — must reach byte-identical
// checkpoints, and an append-ordered list would not.
//
// An empty thread is nil rather than an empty slice, so that a task with no
// comments encodes to the bytes it encoded to before comments existed. That is
// the whole reason the member carries `omitempty`, and the reason this function
// erases the distinction rather than preserving whichever one the fold produced.
func normalizeComments(comments []Comment) []Comment {
	if len(comments) == 0 {
		return nil
	}
	normalized := append([]Comment(nil), comments...)
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].ID < normalized[j].ID
	})
	return normalized
}

func copyComments(comments []Comment) []Comment {
	if comments == nil {
		return nil
	}
	return append([]Comment(nil), comments...)
}

// SameComments compares two threads member by member.
//
// It is exported for reconciliation, which decides whether a replayed pack
// earns a commit by asking whether it changed anything an operator can see — the
// same question SameAssignments answers for assignments, and answered in the
// same place so the two cannot drift apart. A comment is invisible in every
// scalar field of a task, so without this a replayed comment would look like a
// pack that changed nothing and a person's remark would be dropped by the
// synchronization that was supposed to publish it.
//
// The comparison is not `==`, for the reason sameAssignmentRecord records: a
// time carrying a monotonic reading is not `==` to the same instant read back
// out of JSON, and one side here is routinely a freshly folded state.
func SameComments(left, right []Comment) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameCommentRecord(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameCommentRecord(left, right Comment) bool {
	if left.ID != right.ID || left.Author != right.Author || left.Body != right.Body ||
		!left.CreatedAt.Equal(right.CreatedAt) || (left.EditedAt == nil) != (right.EditedAt == nil) {
		return false
	}
	return left.EditedAt == nil || left.EditedAt.Equal(*right.EditedAt)
}

func findComment(comments []Comment, id string) int {
	for index, comment := range comments {
		if comment.ID == id {
			return index
		}
	}
	return -1
}

// applyCommentAdd records a comment, and does nothing at all when the task
// already holds one under this identifier.
//
// Doing nothing is what makes duplicate delivery safe. An operation ULID is
// unique to the operation that minted it, so a second arrival of the same
// comment.add is the same comment arriving twice — through a replay, a retry,
// or a reconciliation that folded a pack the checkpoint already accounts for —
// and appending it again would double a remark nobody wrote twice.
func applyCommentAdd(task *TaskData, operation Operation, authored authorship) error {
	if findComment(task.Comments, operation.ID) >= 0 {
		return nil
	}
	task.Comments = append(copyComments(task.Comments), Comment{
		ID:        operation.ID,
		Author:    authored.actor,
		Body:      operation.Body,
		CreatedAt: authored.at,
	})
	return nil
}

// applyCommentEdit rewrites a comment's body, and tolerates a comment that is
// no longer there.
//
// Tolerance is the collection rule this repository already applies to labels
// and dependencies, and it is what keeps an edit and a removal from needing a
// conflict type between them. One clone edits a comment while another removes
// it; whichever order the reconciliation linearizes them in, the fold produces
// the same answer on every clone — the comment is gone, because a removal is
// not undone by an edit that arrives after it. The mutation boundary is where
// somebody editing a comment that is not there is told so; by the time a pack
// is being folded, it is history and the question is only what it means.
func applyCommentEdit(task *TaskData, operation Operation, authored authorship) error {
	index := findComment(task.Comments, operation.CommentID)
	if index < 0 {
		return nil
	}
	comments := copyComments(task.Comments)
	editedAt := authored.at
	comments[index].Body = operation.Body
	comments[index].EditedAt = &editedAt
	task.Comments = comments
	return nil
}

// applyCommentRemove drops a comment, tolerating one that is already gone for
// the reason applyCommentEdit records.
func applyCommentRemove(task *TaskData, operation Operation) error {
	index := findComment(task.Comments, operation.CommentID)
	if index < 0 {
		return nil
	}
	comments := make([]Comment, 0, len(task.Comments)-1)
	comments = append(comments, task.Comments[:index]...)
	comments = append(comments, task.Comments[index+1:]...)
	task.Comments = comments
	return nil
}

// validateCommentOperation checks a comment operation's shape, and nothing
// about its size.
//
// The split is the one the ceilings in limits.go describe. Shape is a property
// of the document that whoever wrote it either got right or did not, so a
// malformed one is corrupt data. Size is a property of a growing collection
// that several clones edit at once, so a fold that could fail on it could be
// made to fail forever by two people each doing something they were allowed to
// do — which is why the ceilings are asked at the authoring boundary and never
// here.
func validateCommentOperation(operation Operation) error {
	if operation.Task != nil || operation.Attachment != nil ||
		operation.AttachmentID != "" || operation.Field != "" || operation.Value != "" {
		return corrupt("%s must not contain a payload for another operation", operation.Type)
	}
	switch operation.Type {
	case OperationCommentAdd:
		// The comment's identifier is the operation's own, so naming a second
		// one would be naming a comment this operation is not about.
		if operation.CommentID != "" {
			return corrupt("comment.add must not name a comment")
		}
		if strings.TrimSpace(operation.Body) == "" {
			return corrupt("comment.add body must not be blank")
		}
	case OperationCommentEdit:
		if err := validateCanonicalULID("comment ID", operation.CommentID); err != nil {
			return err
		}
		if strings.TrimSpace(operation.Body) == "" {
			return corrupt("comment.edit body must not be blank")
		}
	case OperationCommentRemove:
		if operation.Body != "" {
			return corrupt("comment.remove must not carry a body")
		}
		if err := validateCanonicalULID("comment ID", operation.CommentID); err != nil {
			return err
		}
	}
	return nil
}
