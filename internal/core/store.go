package core

import "context"

// Snapshot is one task head together with the operation and checkpoint stored
// in that head's commit.
type Snapshot struct {
	Head      string
	Operation OperationPack
	State     StateDocument
}

// TaskStore persists and retrieves immutable task operation snapshots.
type TaskStore interface {
	List(context.Context, ProjectConfig) ([]Snapshot, error)
	Get(context.Context, ProjectConfig, string) (Snapshot, error)
	Resolve(context.Context, ProjectConfig, string) (string, error)
	Write(context.Context, ProjectConfig, *Snapshot, OperationPack, StateDocument, string) (Snapshot, error)
}
