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

type MutationResult struct {
	Task     Task      `json:"task"`
	Warnings []Warning `json:"warnings,omitempty"`
}
