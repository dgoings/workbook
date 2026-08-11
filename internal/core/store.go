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
