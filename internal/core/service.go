package core

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

type Service struct {
	Config ProjectConfig
	// Vocabulary is the project's status configuration. The zero value means
	// "not configured", and every accessor substitutes DefaultVocabulary for
	// it, so a Service built the way every caller built one before per-project
	// statuses existed keeps exactly its previous behavior without being
	// edited. A caller that has read the project's configuration ledger sets
	// this; PR-B wires that read.
	Vocabulary Vocabulary
	Reader     TaskReader
	Writer     CanonicalTaskWriter
	Projection ProjectionUpdater
	History    TaskHistorySource
	IDs        IDSource
	Now        func() time.Time
	Actor      string
}

// vocabulary returns the configured vocabulary, or the built-in default when
// none was configured.
func (s Service) vocabulary() Vocabulary {
	if s.Vocabulary.IsZero() {
		return DefaultVocabulary()
	}
	return s.Vocabulary
}

// requireStatusMember rejects a status the project does not define.
//
// This is the mutation boundary the vocabulary check moved to, and the reason
// it belongs here rather than in NormalizeTask: at this point a person or an
// agent is choosing a value and can be told it does not exist, whereas
// NormalizeTask also runs over documents that were written elsewhere and are
// not anybody's to choose.
func (s Service) requireStatusMember(status Status) error {
	if err := validateStatusToken(status); err != nil {
		return err
	}
	if !s.vocabulary().Has(status) {
		return Errorf(CategoryValidation, "invalid task status %q", status)
	}
	return nil
}

type CreateInput struct {
	Title       string
	Description string
	Status      Status
	Priority    Priority
	Labels      []string
}

type UpdateInput struct {
	Title       *string
	Description *string
	Status      *Status
	Priority    *Priority
	Labels      *[]string
	// ExpectedHead is the task tip the caller believes it is changing. An
	// empty value means the caller is not tracking one and accepts whatever
	// the store currently holds, which is how every command behaved before
	// this field existed.
	ExpectedHead string
}

type MoveInput struct {
	Before string
	After  string
}

type PlaceInput struct {
	Status Status
	Before string
	After  string
	// ExpectedHead carries the same meaning it does on UpdateInput.
	ExpectedHead string
}

type ListFilter struct {
	Status   *Status
	Priority *Priority
	Label    string
	All      bool
}

func (s Service) CreateMutation(ctx context.Context, input CreateInput) (MutationResult, error) {
	status := input.Status
	if status == "" {
		status = s.vocabulary().Default()
	}
	if err := s.requireStatusMember(status); err != nil {
		return MutationResult{}, err
	}
	priority := input.Priority
	if priority == "" {
		priority = PriorityMedium
	}

	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}
	rank, err := nextRank(snapshots, status, priority)
	if err != nil {
		return MutationResult{}, err
	}
	now := s.now()
	taskData, err := normalizeCanonicalTask(s.Config.Key, TaskData{
		Title:        input.Title,
		Description:  input.Description,
		Status:       status,
		Priority:     priority,
		Labels:       input.Labels,
		Rank:         rank,
		Dependencies: []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		return MutationResult{}, err
	}

	ids, err := s.newMutationIDs(3)
	if err != nil {
		return MutationResult{}, err
	}
	taskULID, generation, operationID := ids[0], ids[1], ids[2]
	taskID := s.Config.Key + "-" + taskULID
	pack := OperationPack{
		Format:            operationPackFormat,
		Version:           documentVersion,
		ProjectID:         s.Config.ProjectID,
		TaskID:            taskID,
		HistoryGeneration: generation,
		Actor:             Actor{ID: s.Actor},
		LogicalClock:      1,
		WallTime:          now,
		Operations: []Operation{{
			ID: operationID, Type: OperationTaskCreate, Task: &taskData,
		}},
	}
	state, err := Apply(nil, pack, s.Config.Key)
	if err != nil {
		return MutationResult{}, err
	}
	return s.persistMutation(ctx, nil, pack, state, createCommitSubject(taskID, taskData))
}

// List returns the project's tasks, filtered and ordered.
//
// A status filter outside the vocabulary is still rejected, exactly as it was
// before per-project statuses existed, and deliberately so.
//
// Relaxing it is the right end state: under a distributed vocabulary, naming a
// status this clone has not fetched yet is an ordinary thing to type, and
// failing tells the caller their repository is broken when it is merely behind.
// But an empty table and a zero exit status is a worse answer than a clear
// refusal — a script that greps the output would silently start finding
// nothing. The relaxation is only honest once the result envelope can carry
// "no such status here; the configuration may be behind", and that surface is
// the CLI's warning path, which this PR does not touch. PR-C relaxes this check
// and adds the warning in the same change.
func (s Service) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	if filter.Status != nil && !s.vocabulary().Has(*filter.Status) {
		return nil, Errorf(CategoryValidation, "invalid task status %q", *filter.Status)
	}
	if filter.Priority != nil && !isValidPriority(*filter.Priority) {
		return nil, Errorf(CategoryValidation, "invalid task priority %q", *filter.Priority)
	}
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	vocabulary := s.vocabulary()
	tasks := make([]Task, 0, len(snapshots))
	for _, snapshot := range snapshots {
		task := s.Project(snapshot)
		if !filter.All && task.Deleted {
			continue
		}
		if filter.Status != nil && task.Status != *filter.Status {
			continue
		}
		if filter.Priority != nil && task.Priority != *filter.Priority {
			continue
		}
		if filter.Label != "" && !hasLabel(task.Labels, filter.Label) {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return compareTasks(vocabulary, tasks[i], tasks[j]) < 0
	})
	return tasks, nil
}

// Next returns the highest-priority task in a status tagged next whose
// dependencies are all active tasks in a status tagged done. It returns nil
// when no task is eligible.
//
// Both questions are asked of the resolved status, not the stored one, so a
// task still carrying a token a rename replaced is still eligible.
func (s Service) Next(ctx context.Context) (*Task, error) {
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	vocabulary := s.vocabulary()

	active := make(map[string]TaskData, len(snapshots))
	for _, snapshot := range snapshots {
		task := snapshot.State.Task
		if !task.Deleted {
			active[snapshot.State.TaskID] = task
		}
	}

	var selected *Task
	var selectedRank *big.Rat
	for _, snapshot := range snapshots {
		task := snapshot.State.Task
		resolved, _ := vocabulary.Resolve(task.Status)
		if task.Deleted || !vocabulary.IsNext(resolved) || !dependenciesDone(vocabulary, task.Dependencies, active) {
			continue
		}
		rank, err := parseRank(task.Rank)
		if err != nil {
			return nil, Errorf(CategoryCorruptData, "task %q has invalid rank %q", snapshot.State.TaskID, task.Rank)
		}
		projected := s.Project(snapshot)
		if selected == nil || priorityOrder(projected.Priority) < priorityOrder(selected.Priority) ||
			(priorityOrder(projected.Priority) == priorityOrder(selected.Priority) &&
				(rank.Cmp(selectedRank) < 0 || (rank.Cmp(selectedRank) == 0 && projected.ID < selected.ID))) {
			selected = &projected
			selectedRank = rank
		}
	}
	return selected, nil
}

func (s Service) Show(ctx context.Context, idOrPrefix string) (Task, error) {
	snapshot, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return Task{}, err
	}
	return s.Project(snapshot), nil
}

// ShowOptions selects the opt-in history views. Plain show requests neither.
type ShowOptions struct {
	History bool
	Limit   int
	All     bool
	Compare *ComparePoints
}

func (options ShowOptions) requested() bool {
	return options.History || options.Compare != nil
}

// TaskDetail is a task plus the history views the caller asked for. Both extra
// members are omitted when they are not requested, so a consumer that reads
// plain show sees an unchanged shape.
type TaskDetail struct {
	Task
	History    *ChangeLog  `json:"history,omitempty"`
	Comparison *Comparison `json:"comparison,omitempty"`
}

// ShowDetail returns one task, optionally with its change log and a field-level
// comparison between two points in its history.
func (s Service) ShowDetail(ctx context.Context, idOrPrefix string, options ShowOptions) (TaskDetail, error) {
	snapshot, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return TaskDetail{}, err
	}
	detail := TaskDetail{Task: s.Project(snapshot)}
	if !options.requested() {
		return detail, nil
	}
	if s.History == nil {
		return TaskDetail{}, Errorf(CategoryOperational, "task history source is not configured")
	}

	if options.History {
		history, err := s.History.TaskHistory(ctx, s.Config, detail.ID)
		if err != nil {
			return TaskDetail{}, err
		}
		log := BuildChangeLog(s.Config.Key, history, options.Limit, options.All)
		detail.History = &log
	}
	if options.Compare != nil {
		comparison, err := s.compare(ctx, detail.ID, *options.Compare)
		if err != nil {
			return TaskDetail{}, err
		}
		detail.Comparison = &comparison
	}
	return detail, nil
}

// compare diffs the two named commits in the order given, the way git diff
// does. Sorting them would be wrong: operation ULIDs sort by authoring time and
// no longer track chain position once a task has been reconciled.
func (s Service) compare(ctx context.Context, taskID string, points ComparePoints) (Comparison, error) {
	from, err := s.stateAtCommit(ctx, taskID, points.From)
	if err != nil {
		return Comparison{}, err
	}
	to, err := s.stateAtCommit(ctx, taskID, points.To)
	if err != nil {
		return Comparison{}, err
	}
	return Comparison{From: points.From, To: points.To, Fields: CompareTasks(from, to)}, nil
}

func (s Service) stateAtCommit(ctx context.Context, taskID, commit string) (TaskData, error) {
	history, err := s.History.CommitHistory(ctx, s.Config, taskID, commit)
	if err != nil {
		return TaskData{}, err
	}
	return StateAt(s.Config.Key, history)
}

func (s Service) UpdateMutation(ctx context.Context, idOrPrefix string, input UpdateInput) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if err := requireExpectedHead(parent, input.ExpectedHead); err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot update a tombstoned task")
	}

	next := copyTaskData(parent.State.Task)
	if input.Title != nil {
		next.Title = *input.Title
	}
	if input.Description != nil {
		next.Description = *input.Description
	}
	if input.Status != nil {
		if err := s.requireStatusMember(*input.Status); err != nil {
			return MutationResult{}, err
		}
		next.Status = *input.Status
	}
	if input.Priority != nil {
		next.Priority = *input.Priority
	}
	if input.Labels != nil {
		next.Labels = append([]string(nil), (*input.Labels)...)
	}
	next, err = normalizeCanonicalTask(s.Config.Key, next)
	if err != nil {
		return MutationResult{}, err
	}

	operations := changedOperations(parent.State.Task, next)
	if len(operations) == 0 {
		return MutationResult{}, Errorf(CategoryValidation, "update does not change task")
	}
	// The emptiness check comes first on purpose: a correction rides along with
	// a pack that was going to be written anyway, and must not turn an update
	// that changes nothing into a write the caller did not ask for.
	var corrected *StatusCorrection
	if input.Status == nil {
		operations, corrected = s.settle(operations, parent.State.Task)
		if corrected != nil {
			next.Status = corrected.To
		}
	}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, updateCommitSubject(parent.State.TaskID, parent.State.Task, next))
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

// statusCorrection reports the implicit status write a pack should carry, or
// nil when there is nothing to settle.
//
// This is the "correct on touch" rule. A rename cannot rewrite tasks: history
// is append-only, and the clones holding those tasks may be offline for weeks.
// So a task keeps its old token, resolution covers for it on every read, and
// the stored value is settled the next time something writes to the task
// anyway. Nothing sweeps; a task nobody touches keeps resolving forever, which
// is correct and costs nothing.
//
// It is refused in the two cases where it would be a guess rather than a
// settlement: a tombstoned task, whose ref must not gain edits, and a status
// whose chain does not terminate at a live status, where there is no answer to
// write.
func (s Service) statusCorrection(task TaskData) *StatusCorrection {
	if task.Deleted {
		return nil
	}
	resolved, live := s.vocabulary().Resolve(task.Status)
	if !live || resolved == task.Status {
		return nil
	}
	return &StatusCorrection{From: task.Status, To: resolved}
}

// settle appends the correct-on-touch status write to a pack that is already
// being written, and reports what it settled.
//
// Every mutation that writes to a task settles its status, not only the ones
// that were about status. That is the whole meaning of "correct on touch": the
// stored token is stale because no clone may rewrite another's task from the
// outside, so the only moment it can be repaired is a moment somebody was
// writing to that task anyway. A move, a dependency edit and a restore are all
// such moments, and leaving them out would mean a task that gets reordered
// every day but never re-statused resolves through the forwarding chain
// forever.
//
// It is deliberately not called where no pack is written. A move that computes
// the rank it already has returns without a commit, and a settlement that
// turned that into a write would make a no-op command produce history.
//
// The two anchor comparisons in MoveMutation and PlaceMutation still read
// stored statuses rather than resolved ones, and stay that way here. Fixing
// them is not a settlement but a change to which tasks share a rank bucket:
// two tasks whose stored tokens differ while resolving to the same column are
// in one bucket for ordering purposes and two for these checks, and reconciling
// that is the bucketing work PR-D does with the rendering that depends on it.
// Doing half of it here would let a move succeed against an anchor the board
// draws in another column.
func (s Service) settle(operations []Operation, task TaskData) ([]Operation, *StatusCorrection) {
	correction := s.statusCorrection(task)
	if correction == nil {
		return operations, nil
	}
	return append(operations, Operation{
		Type:  OperationFieldSet,
		Field: "status",
		Value: string(correction.To),
	}), correction
}

// Restore is the one mutation that writes a pack and does not settle, and the
// reason is the fold rather than a policy: Apply refuses a pack against a
// tombstoned parent unless it is exactly one task.restore operation, so a
// settlement riding along would be rejected as an attempt to mutate a
// tombstone. Making it ride would mean widening that rule, which is a change to
// the durable operation semantics every clone folds — not a settlement, and not
// something to slip into the change that turns vocabularies on. A restored task
// settles on its next ordinary write, one command later.

func (s Service) DeleteMutation(ctx context.Context, idOrPrefix string) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot delete a tombstoned task")
	}
	operations := []Operation{{Type: OperationTaskTombstone}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "delete task")
}

func (s Service) RestoreMutation(ctx context.Context, idOrPrefix string) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if !parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot restore an active task")
	}
	// No settlement here; see the note above settle.
	operations := []Operation{{Type: OperationTaskRestore}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "restore task")
}

func (s Service) MoveMutation(ctx context.Context, idOrPrefix string, input MoveInput) (MutationResult, error) {
	if (input.Before == "") == (input.After == "") {
		return MutationResult{}, Errorf(CategoryValidation, "move requires exactly one anchor direction")
	}
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot move a tombstoned task")
	}
	anchorInput := input.Before
	if anchorInput == "" {
		anchorInput = input.After
	}
	anchor, err := s.resolveSnapshot(ctx, anchorInput)
	if err != nil {
		return MutationResult{}, err
	}
	if anchor.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot use a tombstoned task as an anchor")
	}
	if anchor.State.TaskID == parent.State.TaskID {
		return MutationResult{}, Errorf(CategoryValidation, "cannot use a task as its own move anchor")
	}
	if anchor.State.Task.Status != parent.State.Task.Status || anchor.State.Task.Priority != parent.State.Task.Priority {
		return MutationResult{}, Errorf(CategoryValidation, "move anchor must be in the same status and priority bucket")
	}
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}
	rank, err := movedRank(snapshots, parent.State.TaskID, anchor.State.TaskID, anchor.State.Task, input.Before != "")
	if err != nil {
		return MutationResult{}, err
	}
	if rank == parent.State.Task.Rank {
		return MutationResult{Task: s.Project(parent)}, nil
	}
	operations, corrected := s.settle(
		[]Operation{{Type: OperationFieldSet, Field: "rank", Value: rank}},
		parent.State.Task,
	)
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, "move task")
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

func (s Service) PlaceMutation(ctx context.Context, idOrPrefix string, input PlaceInput) (MutationResult, error) {
	if err := s.requireStatusMember(input.Status); err != nil {
		return MutationResult{}, err
	}
	if input.Before != "" && input.After != "" {
		return MutationResult{}, Errorf(CategoryValidation, "placement accepts at most one anchor direction")
	}

	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if err := requireExpectedHead(parent, input.ExpectedHead); err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot place a tombstoned task")
	}

	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}

	anchorInput := input.Before
	if anchorInput == "" {
		anchorInput = input.After
	}
	rank := parent.State.Task.Rank
	if anchorInput == "" {
		for _, snapshot := range snapshots {
			task := snapshot.State.Task
			if snapshot.State.TaskID != parent.State.TaskID &&
				!task.Deleted &&
				task.Status == input.Status &&
				task.Priority == parent.State.Task.Priority {
				return MutationResult{}, Errorf(CategoryValidation, "placement requires an anchor when the destination bucket is not empty")
			}
		}
	} else {
		anchor, resolveErr := s.resolveSnapshot(ctx, anchorInput)
		if resolveErr != nil {
			return MutationResult{}, resolveErr
		}
		if anchor.State.Task.Deleted ||
			anchor.State.TaskID == parent.State.TaskID ||
			anchor.State.Task.Status != input.Status ||
			anchor.State.Task.Priority != parent.State.Task.Priority {
			return MutationResult{}, Errorf(CategoryValidation, "placement anchor must be an active different task in the destination status and priority bucket")
		}
		rank, err = movedRank(snapshots, parent.State.TaskID, anchor.State.TaskID, anchor.State.Task, input.Before != "")
		if err != nil {
			return MutationResult{}, err
		}
	}

	operations := make([]Operation, 0, 2)
	// A placement always names its destination, so the status operation this
	// writes is already the settlement a correction would have appended —
	// which is why there is no second one. What still has to be reported is
	// when the write settles a stale token rather than moving the task: the
	// resolved status is unchanged, only the stored one moves.
	var corrected *StatusCorrection
	if parent.State.Task.Status != input.Status {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "status", Value: string(input.Status)})
		if resolved, live := s.vocabulary().Resolve(parent.State.Task.Status); live && resolved == input.Status {
			corrected = &StatusCorrection{From: parent.State.Task.Status, To: input.Status}
		}
	}
	if parent.State.Task.Rank != rank {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "rank", Value: rank})
	}
	if len(operations) == 0 {
		return MutationResult{Task: s.Project(parent)}, nil
	}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, "place task")
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

func (s Service) DependMutation(ctx context.Context, idOrPrefix, dependencyOrPrefix string) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	dependency, err := s.resolveSnapshot(ctx, dependencyOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted || dependency.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot add a dependency involving a tombstoned task")
	}
	if parent.State.TaskID == dependency.State.TaskID {
		return MutationResult{}, Errorf(CategoryValidation, "a task cannot depend on itself")
	}
	if hasDependency(parent.State.Task.Dependencies, dependency.State.TaskID) {
		return MutationResult{Task: s.Project(parent)}, nil
	}
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}
	if dependencyReaches(snapshots, dependency.State.TaskID, parent.State.TaskID) {
		return MutationResult{}, Errorf(CategoryValidation, "dependency would create a cycle")
	}
	operations, corrected := s.settle(
		[]Operation{{Type: OperationSetAdd, Field: "dependencies", Value: dependency.State.TaskID}},
		parent.State.Task,
	)
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, "add dependency")
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

func (s Service) FreeMutation(ctx context.Context, idOrPrefix, dependencyOrPrefix string) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return MutationResult{}, err
	}
	if parent.State.Task.Deleted {
		return MutationResult{}, Errorf(CategoryValidation, "cannot remove a dependency from a tombstoned task")
	}
	dependencyID := dependencyOrPrefix
	if ValidateTaskID(s.Config.Key, dependencyID) != nil ||
		!hasDependency(parent.State.Task.Dependencies, dependencyID) {
		dependency, err := s.resolveSnapshot(ctx, dependencyOrPrefix)
		if err != nil {
			return MutationResult{}, err
		}
		dependencyID = dependency.State.TaskID
	}
	if !hasDependency(parent.State.Task.Dependencies, dependencyID) {
		return MutationResult{Task: s.Project(parent)}, nil
	}
	operations, corrected := s.settle(
		[]Operation{{Type: OperationSetRemove, Field: "dependencies", Value: dependencyID}},
		parent.State.Task,
	)
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	result, err := s.writeMutation(ctx, &parent, operations, "remove dependency")
	if err != nil {
		return MutationResult{}, err
	}
	result.StatusCorrected = corrected
	return result, nil
}

// Project turns a stored snapshot into the task a caller sees, and is the one
// place a stored status is resolved.
//
// Resolution has to happen exactly once, and this is the only constructor of a
// Task, so this is where. Doing it in each consumer would let a board and a
// filter disagree about which column a task is in; doing it in the fold would
// bake one clone's configuration into shared history, which is precisely what
// the forwarding chains exist to avoid.
func (s Service) Project(snapshot Snapshot) Task {
	task := Task{
		ID:                snapshot.State.TaskID,
		ProjectID:         snapshot.State.ProjectID,
		TaskData:          snapshot.State.Task,
		HistoryGeneration: snapshot.State.History.Generation,
		Head:              snapshot.Head,
	}
	if resolved, live := s.vocabulary().Resolve(task.Status); live && resolved != task.Status {
		task.StoredStatus = task.Status
		task.Status = resolved
	}
	return task
}

// requireExpectedHead reports a stale write when the caller named a tip that is
// no longer the task's.
//
// The Git ref compare-and-swap already guards the window between this process
// reading the parent and writing over it, which is milliseconds. This guards
// the window a client actually experiences: a view rendered seconds ago
// proposing a change against state that has since moved. An empty expectation
// opts out, so callers that do not track a head are unaffected.
func requireExpectedHead(parent Snapshot, expected string) error {
	if expected == "" || expected == parent.Head {
		return nil
	}
	return Errorf(
		CategoryStaleWrite,
		"task %s has changed since %s; reload and try again",
		parent.State.TaskID,
		expected,
	)
}

func (s Service) resolveSnapshot(ctx context.Context, idOrPrefix string) (Snapshot, error) {
	if ValidateTaskID(s.Config.Key, idOrPrefix) == nil {
		return s.Reader.Get(ctx, s.Config, idOrPrefix)
	}
	id, err := s.Reader.Resolve(ctx, s.Config, idOrPrefix)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Reader.Get(ctx, s.Config, id)
}

func (s Service) writeMutation(ctx context.Context, parent *Snapshot, operations []Operation, reason string) (MutationResult, error) {
	pack := OperationPack{
		Format:            operationPackFormat,
		Version:           documentVersion,
		ProjectID:         s.Config.ProjectID,
		TaskID:            parent.State.TaskID,
		HistoryGeneration: parent.State.History.Generation,
		Actor:             Actor{ID: s.Actor},
		LogicalClock:      parent.State.LogicalClock + 1,
		WallTime:          s.now(),
		Operations:        operations,
	}
	state, err := Apply(&parent.State, pack, s.Config.Key)
	if err != nil {
		return MutationResult{}, err
	}
	return s.persistMutation(ctx, parent, pack, state, reason)
}

func (s Service) persistMutation(
	ctx context.Context,
	parent *Snapshot,
	pack OperationPack,
	state StateDocument,
	reason string,
) (MutationResult, error) {
	if s.Writer == nil {
		return MutationResult{}, Errorf(CategoryOperational, "canonical task writer is not configured")
	}
	written, err := s.Writer.WriteValidated(ctx, s.Config, parent, pack, state, reason)
	if err != nil {
		return MutationResult{}, err
	}

	result := MutationResult{Task: s.Project(written)}
	if s.Projection == nil {
		return result, nil
	}

	expectedParent := ""
	if parent != nil {
		expectedParent = parent.Head
	}
	advanced, advanceErr := s.Projection.Advance(ctx, s.Config, expectedParent, written)
	if advanceErr != nil || !advanced {
		message := "Git mutation succeeded, but the SQLite cache declined the conditional update because its task row no longer matched the observed parent; run `workbook rebuild` if the warning persists"
		if advanceErr != nil {
			message = "Git mutation succeeded, but the SQLite cache could not be updated; run `workbook rebuild` if the warning persists: " + advanceErr.Error()
		}
		if invalidateErr := s.Projection.Invalidate(ctx, s.Config, written.State.TaskID, expectedParent, written.Head); invalidateErr != nil {
			message += "; cache invalidation also failed: " + invalidateErr.Error()
		}
		result.Warnings = []Warning{{
			Code:    WarningProjectionUpdate,
			Message: message,
		}}
	}
	return result, nil
}

func (s Service) assignOperationIDs(operations []Operation, reserved ...string) error {
	ids, err := s.newMutationIDs(len(operations), reserved...)
	if err != nil {
		return err
	}
	for i, id := range ids {
		operations[i].ID = id
	}
	return nil
}

func (s Service) newMutationIDs(count int, reserved ...string) ([]string, error) {
	seen := make(map[string]struct{}, count+len(reserved))
	for _, id := range reserved {
		seen[id] = struct{}{}
	}
	ids := make([]string, count)
	for i := range ids {
		id, err := s.newID()
		if err != nil {
			return nil, err
		}
		if err := validateGeneratedULID(id); err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, Errorf(CategoryValidation, "ID source returned duplicate ULID %q", id)
		}
		seen[id] = struct{}{}
		ids[i] = id
	}
	return ids, nil
}

func validateGeneratedULID(id string) error {
	parsed, err := ulid.ParseStrict(id)
	if err != nil || parsed.String() != id {
		return Errorf(CategoryValidation, "ID source returned a noncanonical ULID %q", id)
	}
	return nil
}

func taskULIDSuffix(taskID, projectKey string) string {
	return strings.TrimPrefix(taskID, projectKey+"-")
}

func createCommitSubject(taskID string, task TaskData) string {
	return "workbook: create " + taskCommitShortID(taskID) + " " + formatCommitTitle(task.Title)
}

func updateCommitSubject(taskID string, before, after TaskData) string {
	changes := make([]string, 0, 6)
	if before.Title != after.Title {
		changes = append(changes, "title "+formatCommitTitle(after.Title))
	}
	if before.Description != after.Description {
		changes = append(changes, "description updated")
	}
	if before.Status != after.Status {
		changes = append(changes, "status "+string(before.Status)+" → "+string(after.Status))
	}
	if before.Priority != after.Priority {
		changes = append(changes, "priority "+string(before.Priority)+" → "+string(after.Priority))
	}
	if removed := setDifference(before.Labels, after.Labels); len(removed) > 0 {
		changes = append(changes, "labels -"+strings.Join(formatCommitLabels(removed), ","))
	}
	if added := setDifference(after.Labels, before.Labels); len(added) > 0 {
		changes = append(changes, "labels +"+strings.Join(formatCommitLabels(added), ","))
	}
	return "workbook: update " + taskCommitShortID(taskID) + " " + strings.Join(changes, "; ")
}

func taskCommitShortID(taskID string) string {
	projectKey, _, _ := strings.Cut(taskID, "-")
	return projectKey + "-" + taskULIDSuffix(taskID, projectKey)[:8]
}

func formatCommitTitle(title string) string {
	title = DisplayLine(title)
	runes := []rune(title)
	if len(runes) > 72 {
		return string(runes[:71]) + "…"
	}
	return title
}

func formatCommitLabels(labels []string) []string {
	formatted := make([]string, len(labels))
	for i, label := range labels {
		formatted[i] = DisplayLine(label)
	}
	return formatted
}

func (s Service) newID() (string, error) {
	if s.IDs == nil {
		return "", Errorf(CategoryOperational, "ID source is required")
	}
	id, err := s.IDs.New()
	if err != nil {
		return "", Wrap(CategoryOperational, "cannot generate ID", err)
	}
	return id, nil
}

func (s Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now()
}

func changedOperations(current, next TaskData) []Operation {
	fields := []struct {
		name string
		from string
		to   string
	}{
		{name: "description", from: current.Description, to: next.Description},
		{name: "priority", from: string(current.Priority), to: string(next.Priority)},
		{name: "status", from: string(current.Status), to: string(next.Status)},
		{name: "title", from: current.Title, to: next.Title},
	}
	operations := make([]Operation, 0, len(fields)+len(current.Labels)+len(next.Labels))
	for _, field := range fields {
		if field.from != field.to {
			operations = append(operations, Operation{Type: OperationFieldSet, Field: field.name, Value: field.to})
		}
	}
	for _, label := range setDifference(current.Labels, next.Labels) {
		operations = append(operations, Operation{Type: OperationSetRemove, Field: "labels", Value: label})
	}
	for _, label := range setDifference(next.Labels, current.Labels) {
		operations = append(operations, Operation{Type: OperationSetAdd, Field: "labels", Value: label})
	}
	return operations
}

func setDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	values := make([]string, 0, len(left))
	for _, value := range left {
		if _, exists := rightSet[value]; !exists {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func nextRank(snapshots []Snapshot, status Status, priority Priority) (string, error) {
	maximum := big.NewRat(0, 1)
	for _, snapshot := range snapshots {
		task := snapshot.State.Task
		if task.Deleted || task.Status != status || task.Priority != priority {
			continue
		}
		rank, err := parseRank(task.Rank)
		if err != nil {
			return "", Errorf(CategoryCorruptData, "task %q has invalid rank %q", snapshot.State.TaskID, task.Rank)
		}
		if rank.Cmp(maximum) > 0 {
			maximum = rank
		}
	}
	return formatRank(new(big.Rat).Add(maximum, big.NewRat(1, 1))), nil
}

func movedRank(snapshots []Snapshot, movedID, anchorID string, anchor TaskData, before bool) (string, error) {
	anchorRank, err := parseRank(anchor.Rank)
	if err != nil {
		return "", Errorf(CategoryCorruptData, "anchor task has invalid rank %q", anchor.Rank)
	}
	var neighbor *big.Rat
	var neighborID string
	for _, snapshot := range snapshots {
		if snapshot.State.TaskID == movedID || snapshot.State.TaskID == anchorID {
			continue
		}
		task := snapshot.State.Task
		if task.Deleted || task.Status != anchor.Status || task.Priority != anchor.Priority {
			continue
		}
		rank, err := parseRank(task.Rank)
		if err != nil {
			return "", Errorf(CategoryCorruptData, "task %q has invalid rank %q", snapshot.State.TaskID, task.Rank)
		}
		anchorComparison := rank.Cmp(anchorRank)
		if anchorComparison == 0 {
			anchorComparison = strings.Compare(snapshot.State.TaskID, anchorID)
		}
		neighborComparison := 0
		if neighbor != nil {
			neighborComparison = rank.Cmp(neighbor)
			if neighborComparison == 0 {
				neighborComparison = strings.Compare(snapshot.State.TaskID, neighborID)
			}
		}
		if before {
			if anchorComparison < 0 && (neighbor == nil || neighborComparison > 0) {
				neighbor = rank
				neighborID = snapshot.State.TaskID
			}
		} else if anchorComparison > 0 && (neighbor == nil || neighborComparison < 0) {
			neighbor = rank
			neighborID = snapshot.State.TaskID
		}
	}
	if neighbor == nil {
		if before {
			return formatRank(new(big.Rat).Quo(anchorRank, big.NewRat(2, 1))), nil
		}
		nextInteger := new(big.Int).Quo(anchorRank.Num(), anchorRank.Denom())
		nextInteger.Add(nextInteger, big.NewInt(1))
		return formatRank(new(big.Rat).SetInt(nextInteger)), nil
	}
	if neighbor.Cmp(anchorRank) == 0 {
		representable := strings.Compare(neighborID, movedID) < 0 && strings.Compare(movedID, anchorID) < 0
		if !before {
			representable = strings.Compare(anchorID, movedID) < 0 && strings.Compare(movedID, neighborID) < 0
		}
		if !representable {
			// Equal ranks are a reachable state, not a race: replaying a move
			// records its literal computed rank, so two clones converge on one.
			// The same command fails the same way on every retry, so this is a
			// validation failure the caller must resolve by moving something
			// else first.
			return "", Errorf(CategoryValidation, "cannot place task in an equal-rank gap without reordering another task")
		}
		return formatRank(anchorRank), nil
	}
	return formatRank(new(big.Rat).Quo(new(big.Rat).Add(anchorRank, neighbor), big.NewRat(2, 1))), nil
}

func dependencyReaches(snapshots []Snapshot, startID, wantedID string) bool {
	active := make(map[string]TaskData, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.State.Task.Deleted {
			active[snapshot.State.TaskID] = snapshot.State.Task
		}
	}
	visiting := make(map[string]struct{}, len(active))
	visited := make(map[string]struct{}, len(active))
	var visit func(string) bool
	visit = func(id string) bool {
		if id == wantedID {
			return true
		}
		if _, seen := visiting[id]; seen {
			return true
		}
		if _, seen := visited[id]; seen {
			return false
		}
		task, ok := active[id]
		if !ok {
			return false
		}
		visiting[id] = struct{}{}
		for _, dependency := range task.Dependencies {
			if visit(dependency) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = struct{}{}
		return false
	}
	return visit(startID)
}

func hasDependency(dependencies []string, wanted string) bool {
	for _, dependency := range dependencies {
		if dependency == wanted {
			return true
		}
	}
	return false
}

func dependenciesDone(vocabulary Vocabulary, dependencies []string, active map[string]TaskData) bool {
	for _, dependency := range dependencies {
		task, ok := active[dependency]
		if !ok {
			return false
		}
		resolved, _ := vocabulary.Resolve(task.Status)
		if !vocabulary.IsDone(resolved) {
			return false
		}
	}
	return true
}

func compareTasks(vocabulary Vocabulary, left, right Task) int {
	if compare := vocabulary.Order(left.Status) - vocabulary.Order(right.Status); compare != 0 {
		return compare
	}
	if compare := priorityOrder(left.Priority) - priorityOrder(right.Priority); compare != 0 {
		return compare
	}
	leftRank, leftErr := parseRank(left.Rank)
	rightRank, rightErr := parseRank(right.Rank)
	if leftErr == nil && rightErr == nil {
		if compare := leftRank.Cmp(rightRank); compare != 0 {
			return compare
		}
	} else if (leftErr == nil) != (rightErr == nil) {
		if leftErr == nil {
			return -1
		}
		return 1
	}
	return strings.Compare(left.ID, right.ID)
}

func priorityOrder(priority Priority) int {
	switch priority {
	case PriorityHigh:
		return 0
	case PriorityMedium:
		return 1
	case PriorityLow:
		return 2
	default:
		return 3
	}
}

func hasLabel(labels []string, wanted string) bool {
	for _, label := range labels {
		if label == wanted {
			return true
		}
	}
	return false
}
