package core

import "context"

// Snapshot is one task head together with the operation and checkpoint stored
// in that head's commit.
type Snapshot struct {
	Head      string
	Operation OperationPack
	State     StateDocument
}

// TaskReader retrieves task snapshots from a selected read model.
type TaskReader interface {
	List(context.Context, ProjectConfig) ([]Snapshot, error)
	Get(context.Context, ProjectConfig, string) (Snapshot, error)
	Resolve(context.Context, ProjectConfig, string) (string, error)
}

// CanonicalTaskWriter durably appends a mutation whose parent was already
// observed and validated through the canonical repository read path.
type CanonicalTaskWriter interface {
	WriteValidated(context.Context, ProjectConfig, *Snapshot, OperationPack, StateDocument, string) (Snapshot, error)
}

// AttachmentBlobStore durably records an attached file's bytes and reports the
// object ID the operation will name.
//
// It is a separate interface from CanonicalTaskWriter, and separate because the
// two are needed at different moments. The bytes have to exist as an object
// before the operation that names them can be built, because the checkpoint
// stores the object ID — that is what makes reading an attachment one
// `cat-file` rather than a walk. Writing an object touches no ref, so a
// mutation that is refused afterwards leaves an unreferenced blob, which is
// what every other object this build writes ahead of a ref update also leaves
// and what Git collects.
//
// A Service without one can do everything except attach a file, which is why it
// is a separate field rather than a widened writer: the read-only services the
// command line builds have no writer at all, and the ones that do gain this in
// the same breath.
type AttachmentBlobStore interface {
	StageAttachment(context.Context, ProjectConfig, []byte) (string, error)
}

// AttachmentBlobReader returns an attached file's bytes by the object ID the
// checkpoint recorded.
//
// It is the read half of AttachmentBlobStore and a separate interface for the
// same reason that one is separate from the writer: the two are needed by
// different services. Every read-only service can serve an attachment and none
// of them may write one, so a single read-write interface would have forced a
// writer onto `show`.
type AttachmentBlobReader interface {
	ReadAttachment(context.Context, ProjectConfig, string) ([]byte, error)
}

// ProjectionUpdater conditionally advances or invalidates disposable task
// projection rows after a canonical mutation succeeds.
type ProjectionUpdater interface {
	Advance(context.Context, ProjectConfig, string, Snapshot) (bool, error)
	Invalidate(context.Context, ProjectConfig, string, string, string) error
}

const WarningProjectionUpdate = "projection-update-failed"

// WarningAutoSync reports that automatic synchronization did not complete
// cleanly. Usually the change was recorded locally and nothing was published;
// it also covers a synchronization that published while leaving something
// behind, such as a ref on origin it could not validate.
const WarningAutoSync = "auto-sync-incomplete"

// WarningStatusFilter reports that a status filter did not name a live status
// of this project: either nothing at all, or a value that had to be forwarded
// through a rename or a removal to select anything.
//
// It is a warning rather than a refusal because a filter authors nothing, and a
// warning rather than silence because the result it accompanies is usually
// empty, and an empty table with a zero exit status is exactly the answer a
// script cannot tell from "there is genuinely nothing here".
const WarningStatusFilter = "status-filter-unresolved"

// WarningDocsRefresh reports that generated documentation this change
// invalidated could not be rewritten — usually because somebody edited the
// generated file, which Workbook never overwrites without being told to.
//
// It is a warning rather than a refusal for the same reason an incomplete
// synchronization is: the change itself is recorded, and undoing a durable,
// published configuration change because a Markdown file could not be rewritten
// would be the worse outcome by far.
const WarningDocsRefresh = "docs-refresh-incomplete"

// WarningNewerWriter reports that something this read showed was written by a
// newer Workbook than the one running.
//
// It is a warning rather than a refusal because the read succeeded and the
// answer is the author's own: every read serves a task from its stored
// checkpoint, and a checkpoint a newer build wrote is still that build's
// account of the task. What the reader has to know is the part that is not
// visible in the answer — that this build cannot replay the history behind it
// and will refuse to change it until it is upgraded.
const WarningNewerWriter = "newer-writer"

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StatusCorrection reports that a mutation rewrote a task's stored status to
// the live status it already resolved to.
//
// It is deliberately not a Warning. A warning says something went wrong and
// asks the reader to consider acting; this says the opposite — a task that had
// been carrying a stale token because no clone could rewrite it from outside
// finally got written to, and the write took the opportunity to settle it.
// Filing that under warnings would train people to ignore warnings.
type StatusCorrection struct {
	From Status `json:"from"`
	To   Status `json:"to"`
}

type MutationResult struct {
	Task Task `json:"task"`
	// StatusCorrected is set when this mutation also settled a stale stored
	// status. It is a pointer so that the common case adds nothing to the
	// JSON envelope.
	StatusCorrected *StatusCorrection `json:"statusCorrected,omitempty"`
	Warnings        []Warning         `json:"warnings,omitempty"`
}
