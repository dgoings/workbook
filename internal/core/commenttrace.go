package core

import (
	"sort"
	"time"
)

// EditableBy reports whether an actor may change this comment's text.
//
// Only the person who wrote it may. That is the whole rule, and it is decided
// from the comment's recorded author and the acting person and nothing else,
// exactly as Assignment.RemovableBy is: the author is the actor of the pack
// that added the comment, and the acting person is core.Service.ActorFor's
// answer. Both are asserted names, so this checks who an edit claims to come
// from, not who is really behind it. An actor of no name may edit nothing.
//
// Removing a comment is deliberately not governed by it. Anyone may remove a
// comment, and the thread says who did (see TraceComments).
//
// Unlike the assignment rule, this is enforced only where an edit is authored,
// never in the fold. Builds before this rule let anyone edit any comment, so
// histories already hold edits by someone other than the author, and an older
// clone can still record one. Folding those away would rewrite what every
// existing thread says and make two builds disagree about one history; they
// are applied as recorded, and the thread names whoever made them.
func (comment Comment) EditableBy(actor string) bool {
	return actor != "" && actor == comment.Author
}

// foreignEditMessage is the refusal somebody sees for editing another
// person's comment. It names the author, because that is who can make the
// change, and says what the reader can still do.
func foreignEditMessage(comment Comment) string {
	return "comment " + comment.ID + " was written by " + DisplayLine(comment.Author) +
		", and only its author may edit it; anyone may remove it"
}

// ThreadComment is one live comment as a reader is shown it: the stored
// comment, who last edited it, and whether this reader may edit it.
//
// It is a read-side view and is never stored. EditedBy and Editable are not
// members of Comment because a checkpoint is shared, append-only history and
// neither belongs in it: EditedBy is derived from the operations that recorded
// the edits, and Editable is a question about whoever is reading.
type ThreadComment struct {
	Comment
	// EditedBy is the person who made the edit EditedAt records. It is empty
	// for a comment never edited, and for one whose last edit the history read
	// does not reach — a compacted task's checkpoint keeps the edit time but
	// not who made it — and then EditorNotRecorded is set.
	EditedBy string `json:"editedBy,omitempty"`
	// EditorNotRecorded says the comment was edited and the history read holds
	// no record of that edit, so nobody can say who made it: the edit came
	// before the task was compacted, whose checkpoint keeps the edit time and
	// not the editor. A reader must not show such a comment as reading the way
	// its author wrote it, because it may be somebody else's words. It is set
	// only when the history was read; a thread drawn without one says
	// nothing either way.
	EditorNotRecorded bool `json:"editorNotRecorded,omitempty"`
	// Editable is EditableBy for the person reading. It is always present,
	// true or false, so a client never has to tell "not editable" from "not
	// said".
	Editable bool `json:"editable"`
}

// EditedByOther reports an edit made by someone other than the comment's
// author: an edit the authoring rule would now refuse, recorded before it
// existed or by a build without it.
func (comment ThreadComment) EditedByOther() bool {
	return comment.EditedBy != "" && comment.EditedBy != comment.Author
}

// RemovedComment is what a thread keeps of a comment somebody removed: whose
// it was, when it was written, and who removed it when. It never carries the
// removed text. The text is still in the history, where `workbook show
// --history` shows it; the thread only says that something was there.
type RemovedComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	RemovedBy string    `json:"removedBy"`
	RemovedAt time.Time `json:"removedAt"`
}

// CommentTrace is what a task's history says about its thread beyond the live
// comments a checkpoint holds: who removed each removed comment, and who made
// each comment's latest edit.
type CommentTrace struct {
	removed []RemovedComment
	// edits is every edit each live comment received, in chain order. All of
	// them rather than the latest, so a thread read from a task snapshot a
	// little older than the history still finds the edit its text shows.
	edits map[string][]commentEdit
	// read is whether this trace came from a history at all. The zero trace,
	// for a caller that read none, cannot say an edit is unrecorded.
	read bool
}

type commentEdit struct {
	actor string
	at    time.Time
}

type tracedComment struct {
	author    string
	createdAt time.Time
}

// TraceComments derives a task's CommentTrace from its operation chain.
//
// Nothing here is stored. A removal and an edit already record who made them —
// the pack's actor and wall time — so the trace is read from the operations
// the history holds, on every read, and two clones holding the same chain
// derive the same trace. No format changes and no generation is raised, and an
// existing repository shows traces for removals made before this build.
//
// It mirrors the fold rather than reading intent. A removal counts only when
// it took a live comment out of the thread, and an edit only when the comment
// was live to receive it, which are exactly the cases applyCommentRemove and
// applyCommentEdit act on. So a second removal of a comment already gone does
// not overwrite who removed it, and an edit of a removed comment is not an
// edit of anything. A root checkpoint replaces the thread wholesale, the way
// the fold starts from it: a compacted task's earlier removals left the
// history with the operations that recorded them, and so leave the trace. Its
// earlier edits leave it too, and Thread marks a comment they changed as
// edited by somebody the history no longer records.
//
// The chain is the one TaskHistory returns, oldest first. A truncated history
// is traced as far as it was read.
func TraceComments(history TaskHistory) CommentTrace {
	live := map[string]tracedComment{}
	removed := map[string]RemovedComment{}
	edits := map[string][]commentEdit{}
	for _, entry := range history.Entries {
		pack := entry.Operation
		for _, operation := range pack.Operations {
			switch operation.Type {
			case OperationTaskCreate, OperationHistoryCheckpoint:
				if operation.Task == nil {
					continue
				}
				live = map[string]tracedComment{}
				removed = map[string]RemovedComment{}
				edits = map[string][]commentEdit{}
				for _, comment := range operation.Task.Comments {
					live[comment.ID] = tracedComment{author: comment.Author, createdAt: comment.CreatedAt}
				}
			case OperationCommentAdd:
				if _, present := live[operation.ID]; present {
					continue
				}
				live[operation.ID] = tracedComment{author: pack.Actor.ID, createdAt: pack.WallTime}
				delete(removed, operation.ID)
				delete(edits, operation.ID)
			case OperationCommentEdit:
				if _, present := live[operation.CommentID]; !present {
					continue
				}
				edits[operation.CommentID] = append(edits[operation.CommentID], commentEdit{actor: pack.Actor.ID, at: pack.WallTime})
			case OperationCommentRemove:
				comment, present := live[operation.CommentID]
				if !present {
					continue
				}
				delete(live, operation.CommentID)
				delete(edits, operation.CommentID)
				removed[operation.CommentID] = RemovedComment{
					ID:        operation.CommentID,
					Author:    comment.author,
					CreatedAt: comment.createdAt,
					RemovedBy: pack.Actor.ID,
					RemovedAt: pack.WallTime,
				}
			}
		}
	}
	trace := CommentTrace{edits: edits, read: true}
	for _, removal := range removed {
		trace.removed = append(trace.removed, removal)
	}
	sort.Slice(trace.removed, func(i, j int) bool { return trace.removed[i].ID < trace.removed[j].ID })
	return trace
}

// Removed returns the removed comments in identifier order — creation order,
// the order the live thread is in — leaving out any the given thread still
// holds. A comment that is live is not removed, whatever the trace read; the
// checkpoint is the authority on what the thread holds.
func (trace CommentTrace) Removed(live []Comment) []RemovedComment {
	if len(trace.removed) == 0 {
		return nil
	}
	present := make(map[string]struct{}, len(live))
	for _, comment := range live {
		present[comment.ID] = struct{}{}
	}
	removed := make([]RemovedComment, 0, len(trace.removed))
	for _, removal := range trace.removed {
		if _, still := present[removal.ID]; !still {
			removed = append(removed, removal)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return removed
}

// Thread returns the live comments as actor reads them, in the order given.
//
// EditedBy names whoever made the edit the comment records: the edit at the
// same instant as its EditedAt, the latest in chain order if two share one.
// Only that edit, so a trace read a moment after the comments names the
// person behind the text shown and not whoever edited it since.
//
// When the comment was edited and the history read holds no edit at that
// instant, EditorNotRecorded says so. That is a compacted task's earlier edit:
// its checkpoint keeps when the comment changed but not who changed it, and a
// thread that said nothing would show somebody else's words as the author's.
func (trace CommentTrace) Thread(comments []Comment, actor string) []ThreadComment {
	if len(comments) == 0 {
		return nil
	}
	thread := make([]ThreadComment, 0, len(comments))
	for _, comment := range comments {
		view := ThreadComment{Comment: comment, Editable: comment.EditableBy(actor)}
		if comment.EditedAt != nil {
			for _, edit := range trace.edits[comment.ID] {
				if comment.EditedAt.Equal(edit.at) {
					view.EditedBy = edit.actor
				}
			}
			view.EditorNotRecorded = trace.read && view.EditedBy == ""
		}
		thread = append(thread, view)
	}
	return thread
}
