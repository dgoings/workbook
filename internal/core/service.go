package core

import (
	"context"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

type Service struct {
	Config     ProjectConfig
	Reader     TaskReader
	Writer     CanonicalTaskWriter
	Projection ProjectionUpdater
	IDs        IDSource
	Now        func() time.Time
	Actor      string
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
}

type MoveInput struct {
	Before string
	After  string
}

type PlaceInput struct {
	Status Status
	Before string
	After  string
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
		status = StatusBacklog
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

func (s Service) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	if filter.Status != nil && !isValidStatus(*filter.Status) {
		return nil, Errorf(CategoryValidation, "invalid task status %q", *filter.Status)
	}
	if filter.Priority != nil && !isValidPriority(*filter.Priority) {
		return nil, Errorf(CategoryValidation, "invalid task priority %q", *filter.Priority)
	}
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(snapshots))
	for _, snapshot := range snapshots {
		task := Project(snapshot)
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
		return compareTasks(tasks[i], tasks[j]) < 0
	})
	return tasks, nil
}

// Next returns the highest-priority ready task whose dependencies are all
// active done tasks. It returns nil when no task is eligible.
func (s Service) Next(ctx context.Context) (*Task, error) {
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return nil, err
	}

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
		if task.Deleted || task.Status != StatusReady || !dependenciesDone(task.Dependencies, active) {
			continue
		}
		rank, err := parseRank(task.Rank)
		if err != nil {
			return nil, Errorf(CategoryCorruptData, "task %q has invalid rank %q", snapshot.State.TaskID, task.Rank)
		}
		projected := Project(snapshot)
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
	return Project(snapshot), nil
}

func (s Service) UpdateMutation(ctx context.Context, idOrPrefix string, input UpdateInput) (MutationResult, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
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
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, updateCommitSubject(parent.State.TaskID, parent.State.Task, next))
}

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
		return MutationResult{Task: Project(parent)}, nil
	}
	operations := []Operation{{Type: OperationFieldSet, Field: "rank", Value: rank}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "move task")
}

func (s Service) PlaceMutation(ctx context.Context, idOrPrefix string, input PlaceInput) (MutationResult, error) {
	if !isValidStatus(input.Status) {
		return MutationResult{}, Errorf(CategoryValidation, "invalid task status %q", input.Status)
	}
	if input.Before != "" && input.After != "" {
		return MutationResult{}, Errorf(CategoryValidation, "placement accepts at most one anchor direction")
	}

	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
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
	if parent.State.Task.Status != input.Status {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "status", Value: string(input.Status)})
	}
	if parent.State.Task.Rank != rank {
		operations = append(operations, Operation{Type: OperationFieldSet, Field: "rank", Value: rank})
	}
	if len(operations) == 0 {
		return MutationResult{Task: Project(parent)}, nil
	}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "place task")
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
		return MutationResult{Task: Project(parent)}, nil
	}
	snapshots, err := s.Reader.List(ctx, s.Config)
	if err != nil {
		return MutationResult{}, err
	}
	if dependencyReaches(snapshots, dependency.State.TaskID, parent.State.TaskID) {
		return MutationResult{}, Errorf(CategoryValidation, "dependency would create a cycle")
	}
	operations := []Operation{{Type: OperationSetAdd, Field: "dependencies", Value: dependency.State.TaskID}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "add dependency")
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
		return MutationResult{Task: Project(parent)}, nil
	}
	operations := []Operation{{Type: OperationSetRemove, Field: "dependencies", Value: dependencyID}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return MutationResult{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "remove dependency")
}

func Project(snapshot Snapshot) Task {
	return Task{
		ID:                snapshot.State.TaskID,
		ProjectID:         snapshot.State.ProjectID,
		TaskData:          snapshot.State.Task,
		HistoryGeneration: snapshot.State.History.Generation,
		Head:              snapshot.Head,
	}
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

	result := MutationResult{Task: Project(written)}
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
	title = formatCommitFragment(title)
	runes := []rune(title)
	if len(runes) > 72 {
		return string(runes[:71]) + "…"
	}
	return title
}

func formatCommitLabels(labels []string) []string {
	formatted := make([]string, len(labels))
	for i, label := range labels {
		formatted[i] = formatCommitFragment(label)
	}
	return formatted
}

func formatCommitFragment(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
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

func dependenciesDone(dependencies []string, active map[string]TaskData) bool {
	for _, dependency := range dependencies {
		task, ok := active[dependency]
		if !ok || task.Status != StatusDone {
			return false
		}
	}
	return true
}

func compareTasks(left, right Task) int {
	if compare := statusOrder(left.Status) - statusOrder(right.Status); compare != 0 {
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

func statusOrder(status Status) int {
	for index, definition := range workflowStatuses {
		if status == definition.Status {
			return index
		}
	}
	return len(workflowStatuses)
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
