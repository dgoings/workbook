package core

// ConflictType discriminates the three concurrent situations Workbook refuses
// to decide on its own. Every other concurrent field change is silent
// last-syncer-wins, so this set is deliberately closed and small.
type ConflictType string

const (
	// ConflictDescription reports that both sides edited the description.
	// Descriptions carry the reasoning a person or agent wrote, so silently
	// dropping one side's text loses work no later command can recover.
	ConflictDescription ConflictType = "description"
	// ConflictDependencyCycle reports a replayed dependency edge that closes a
	// cycle against the fetched dependency graph. Recording it would make the
	// tasks on the cycle permanently ineligible for `workbook next`.
	ConflictDependencyCycle ConflictType = "dependency-cycle"
	// ConflictTombstone reports a local operation that cannot apply because
	// the fetched history tombstoned the task first.
	ConflictTombstone ConflictType = "tombstone"
)

// Conflict names one task whose replay stopped and carries exactly the detail
// a caller needs to decide what to do. Exactly one detail member is populated,
// selected by Type.
type Conflict struct {
	TaskID      string               `json:"taskId"`
	Type        ConflictType         `json:"type"`
	Description *DescriptionConflict `json:"description,omitempty"`
	Dependency  *DependencyConflict  `json:"dependency,omitempty"`
	Tombstone   *TombstoneConflict   `json:"tombstone,omitempty"`
}

// DescriptionConflict presents the three descriptions a caller needs to write
// a replacement. Workbook never merges them and never writes a marker into a
// commit; the values are reported and the local operation is dropped.
type DescriptionConflict struct {
	Base   string `json:"base"`
	Ours   string `json:"ours"`
	Theirs string `json:"theirs"`
}

// DependencyConflict carries the rejected edge and the existing path that
// closes it into a cycle. Path starts at To and ends at From.
type DependencyConflict struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Path []string `json:"path"`
}

// TombstoneConflict carries the local operation that could not apply to a
// task the fetched history had already tombstoned.
type TombstoneConflict struct {
	OperationID string        `json:"operationId"`
	Operation   OperationType `json:"operation"`
	Field       string        `json:"field,omitempty"`
	Value       string        `json:"value,omitempty"`
}

// ConflictError summarizes a conflict list as the command's failure. The
// envelope carries the detail; this message only has to tell a person which
// task or how many need a decision.
func ConflictError(conflicts []Conflict) error {
	if len(conflicts) == 1 {
		return Errorf(CategoryConflict, "task %s: %s", conflicts[0].TaskID, ConflictDetail(conflicts[0]))
	}
	return Errorf(
		CategoryConflict,
		"%d task(s) need a decision before their local operations can be replayed",
		len(conflicts),
	)
}

// ConflictDetail renders one conflict as a single line for terminal output and
// per-task synchronization detail.
func ConflictDetail(conflict Conflict) string {
	switch conflict.Type {
	case ConflictDescription:
		return "the description changed here and on origin; reapply the wanted text"
	case ConflictDependencyCycle:
		return "the dependency on " + conflict.Dependency.To + " would close a cycle against the fetched graph"
	case ConflictTombstone:
		return "origin tombstoned this task, so the local " +
			string(conflict.Tombstone.Operation) + " operation cannot apply"
	default:
		return string(conflict.Type)
	}
}

// DependencyClosingPath returns the existing dependency path that adding the
// edge from -> to would close into a cycle, or nil when the edge is safe. The
// returned path starts at to and ends at from.
//
// dependencies must contain only tasks that participate in scheduling; a
// tombstoned task is absent, which matches the eligibility rule `workbook next`
// applies.
func DependencyClosingPath(dependencies map[string][]string, from, to string) []string {
	if from == to {
		return []string{from}
	}

	path := []string{}
	visited := make(map[string]struct{}, len(dependencies))
	var visit func(string) bool
	visit = func(id string) bool {
		if _, seen := visited[id]; seen {
			return false
		}
		visited[id] = struct{}{}
		path = append(path, id)
		if id == from {
			return true
		}
		for _, dependency := range dependencies[id] {
			if visit(dependency) {
				return true
			}
		}
		path = path[:len(path)-1]
		return false
	}
	if !visit(to) {
		return nil
	}
	return path
}
