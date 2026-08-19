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

// ConfigConflictType discriminates the concurrent configuration situations
// Workbook refuses to decide on its own.
//
// This is a second, separate union rather than four more members on
// ConflictType, and deliberately so. The task union names situations that stop
// one task's replay and are reported against a task ID; these name situations
// that stop the project's configuration replay and are reported against a
// status. Merging them would give every consumer of a task conflict a member
// that can never be populated for it, and would make the task union — which is
// documented as closed and small — open.
type ConfigConflictType string

const (
	// ConfigConflictStatusRename reports two renames of the same status to
	// different tokens. The fold keeps whichever applied first and leaves the
	// other's token unclaimed, which converges but discards an intent.
	ConfigConflictStatusRename ConfigConflictType = "status-rename"
	// ConfigConflictStatusRetired reports a status removed on both sides into
	// different destinations, or an operation whose subject the fetched
	// history had already retired. Where a task's tasks land is not something
	// a tiebreak should decide.
	ConfigConflictStatusRetired ConfigConflictType = "status-retired"
	// ConfigConflictStatusDefinition reports two definitions of the same
	// status name that disagree — most often two clones adding the same status
	// with different labels. The fold keeps the first; this reports the
	// discarded one so the label is not lost silently.
	ConfigConflictStatusDefinition ConfigConflictType = "status-definition"
	// ConfigConflictStatusArity reports a replay whose result violates an arity
	// rule the author's clone would have refused. ApplyConfig normalizes it so
	// the project stays usable; this names what was normalized, because the
	// repair picked a status by position and nobody chose it.
	ConfigConflictStatusArity ConfigConflictType = "status-arity"
	// ConfigConflictRootVocabulary reports two configuration ledgers that were
	// started from different vocabularies. Adopting one root over the other is
	// how unrelated ledgers converge, and it is safe only while both roots say
	// the same thing; when they do not, adopting silently redefines the whole
	// project — a column can vanish with tasks in it, or appear in a project
	// that never had it.
	//
	// It is the one member that names no status, because the two intents
	// disagree about the starting point rather than about one column. Consumers
	// that render a status render nothing for it; both vocabularies are in Ours
	// and Theirs.
	ConfigConflictRootVocabulary ConfigConflictType = "root-vocabulary"
	// ConfigConflictDisplaySetting reports two clones setting the same display
	// setting to different things, or one setting it while the other cleared it.
	// The fold converges by keeping whichever applied first, which after a
	// reconciliation is always upstream's, so the local intent is the one that
	// would vanish without being mentioned.
	//
	// Like root-vocabulary it names no status, because a display setting is a
	// property of the project rather than of a column; the setting it is about is
	// named in Detail, where a consumer that renders a status renders nothing.
	// Two clones that chose the same value are not a conflict — there is nothing
	// for anybody to decide — and two clones that changed different settings are
	// not either, because the section holds three independent values.
	ConfigConflictDisplaySetting ConfigConflictType = "display-setting"
)

// ConfigConflict names one status whose configuration replay needs a decision.
//
// Ours and Theirs carry the two competing values in whatever form the type
// implies — two rename targets, two destinations, two labels — as strings,
// because a caller renders them rather than acting on them, and a union of
// detail structs would be four types to carry one pair each.
type ConfigConflict struct {
	Type   ConfigConflictType `json:"type"`
	Status Status             `json:"status"`
	Ours   string             `json:"ours,omitempty"`
	Theirs string             `json:"theirs,omitempty"`
	Detail string             `json:"detail,omitempty"`
}

// ConfigConflictError summarizes a configuration conflict list as the command's
// failure.
func ConfigConflictError(conflicts []ConfigConflict) error {
	if len(conflicts) == 1 {
		// A conflict about the ledger's starting point names no status, so the
		// message leads with what happened rather than with an empty name.
		if conflicts[0].Status == "" {
			return Errorf(CategoryConflict, "%s", ConfigConflictDetail(conflicts[0]))
		}
		return Errorf(CategoryConflict, "status %s: %s", conflicts[0].Status, ConfigConflictDetail(conflicts[0]))
	}
	return Errorf(
		CategoryConflict,
		"%d status change(s) need a decision before the project configuration can be replayed",
		len(conflicts),
	)
}

// ConfigConflictDetail renders one configuration conflict as a single line.
func ConfigConflictDetail(conflict ConfigConflict) string {
	if conflict.Detail != "" {
		return conflict.Detail
	}
	switch conflict.Type {
	case ConfigConflictStatusRename:
		return "this status was renamed to " + conflict.Ours + " here and to " + conflict.Theirs + " on origin"
	case ConfigConflictStatusRetired:
		return "this status was removed into " + conflict.Ours + " here and into " + conflict.Theirs + " on origin"
	case ConfigConflictStatusDefinition:
		return "this status was defined as " + conflict.Ours + " here and as " + conflict.Theirs + " on origin"
	case ConfigConflictStatusArity:
		return "replaying this change left the project without a required status role"
	case ConfigConflictRootVocabulary:
		return "this project's configuration started from " + conflict.Ours +
			" here and from " + conflict.Theirs + " on origin"
	case ConfigConflictDisplaySetting:
		// Reached only by a conflict somebody built without a detail line; the
		// classifier always writes one, because the setting's name is the whole
		// subject and this union has no member to put it in.
		return "this display setting was changed here and on origin"
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
