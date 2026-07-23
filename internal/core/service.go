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
	Store  TaskStore
	IDs    IDSource
	Now    func() time.Time
	Actor  string
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

type ListFilter struct {
	Status   *Status
	Priority *Priority
	Label    string
	All      bool
}

func (s Service) Create(ctx context.Context, input CreateInput) (Task, error) {
	status := input.Status
	if status == "" {
		status = StatusBacklog
	}
	priority := input.Priority
	if priority == "" {
		priority = PriorityMedium
	}

	snapshots, err := s.Store.List(ctx, s.Config)
	if err != nil {
		return Task{}, err
	}
	rank, err := nextRank(snapshots, status, priority)
	if err != nil {
		return Task{}, err
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
		return Task{}, err
	}

	ids, err := s.newMutationIDs(3)
	if err != nil {
		return Task{}, err
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
		return Task{}, err
	}
	written, err := s.Store.Write(ctx, s.Config, nil, pack, state, "create task")
	if err != nil {
		return Task{}, err
	}
	return Project(written), nil
}

func (s Service) List(ctx context.Context, filter ListFilter) ([]Task, error) {
	if filter.Status != nil && !isValidStatus(*filter.Status) {
		return nil, Errorf(CategoryValidation, "invalid task status %q", *filter.Status)
	}
	if filter.Priority != nil && !isValidPriority(*filter.Priority) {
		return nil, Errorf(CategoryValidation, "invalid task priority %q", *filter.Priority)
	}
	snapshots, err := s.Store.List(ctx, s.Config)
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

func (s Service) Show(ctx context.Context, idOrPrefix string) (Task, error) {
	snapshot, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return Task{}, err
	}
	return Project(snapshot), nil
}

func (s Service) Update(ctx context.Context, idOrPrefix string, input UpdateInput) (Task, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return Task{}, err
	}
	if parent.State.Task.Deleted {
		return Task{}, Errorf(CategoryValidation, "cannot update a tombstoned task")
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
		return Task{}, err
	}

	operations := changedOperations(parent.State.Task, next)
	if len(operations) == 0 {
		return Task{}, Errorf(CategoryValidation, "update does not change task")
	}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return Task{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "update task")
}

func (s Service) Delete(ctx context.Context, idOrPrefix string) (Task, error) {
	parent, err := s.resolveSnapshot(ctx, idOrPrefix)
	if err != nil {
		return Task{}, err
	}
	if parent.State.Task.Deleted {
		return Task{}, Errorf(CategoryValidation, "cannot delete a tombstoned task")
	}
	operations := []Operation{{Type: OperationTaskTombstone}}
	if err := s.assignOperationIDs(operations, taskULIDSuffix(parent.State.TaskID, s.Config.Key), parent.State.History.Generation); err != nil {
		return Task{}, err
	}
	return s.writeMutation(ctx, &parent, operations, "delete task")
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
	id, err := s.Store.Resolve(ctx, s.Config, idOrPrefix)
	if err != nil {
		return Snapshot{}, err
	}
	return s.Store.Get(ctx, s.Config, id)
}

func (s Service) writeMutation(ctx context.Context, parent *Snapshot, operations []Operation, reason string) (Task, error) {
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
		return Task{}, err
	}
	written, err := s.Store.Write(ctx, s.Config, parent, pack, state, reason)
	if err != nil {
		return Task{}, err
	}
	return Project(written), nil
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

func (s Service) newID() (string, error) {
	if s.IDs == nil {
		return "", Errorf(CategoryInvocation, "ID source is required")
	}
	return s.IDs.New()
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
	maximum := big.NewInt(0)
	for _, snapshot := range snapshots {
		task := snapshot.State.Task
		if task.Deleted || task.Status != status || task.Priority != priority {
			continue
		}
		rank, ok := rankInteger(task.Rank)
		if !ok {
			return "", Errorf(CategoryCorruptData, "task %q has invalid rank %q", snapshot.State.TaskID, task.Rank)
		}
		if rank.Cmp(maximum) > 0 {
			maximum = rank
		}
	}
	return new(big.Int).Add(maximum, big.NewInt(1)).String() + "/1", nil
}

func compareTasks(left, right Task) int {
	if compare := statusOrder(left.Status) - statusOrder(right.Status); compare != 0 {
		return compare
	}
	if compare := priorityOrder(left.Priority) - priorityOrder(right.Priority); compare != 0 {
		return compare
	}
	leftRank, leftOK := rankInteger(left.Rank)
	rightRank, rightOK := rankInteger(right.Rank)
	if leftOK && rightOK {
		if compare := leftRank.Cmp(rightRank); compare != 0 {
			return compare
		}
	} else if leftOK != rightOK {
		if leftOK {
			return -1
		}
		return 1
	}
	return strings.Compare(left.ID, right.ID)
}

func statusOrder(status Status) int {
	switch status {
	case StatusBacklog:
		return 0
	case StatusReady:
		return 1
	case StatusInProgress:
		return 2
	case StatusBlocked:
		return 3
	case StatusDone:
		return 4
	default:
		return 5
	}
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

func rankInteger(rank string) (*big.Int, bool) {
	numerator, denominator, ok := strings.Cut(rank, "/")
	if !ok || denominator != "1" || !rankPattern.MatchString(rank) {
		return nil, false
	}
	value, ok := new(big.Int).SetString(numerator, 10)
	return value, ok
}

func hasLabel(labels []string, wanted string) bool {
	for _, label := range labels {
		if label == wanted {
			return true
		}
	}
	return false
}
