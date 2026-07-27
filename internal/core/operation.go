package core

import (
	"reflect"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	operationPackFormat = "workbook.operation-pack"
	stateDocumentFormat = "workbook.task-state"
	documentVersion     = 1
)

type Actor struct {
	ID string `json:"id"`
}

type OperationType string

const (
	OperationTaskCreate    OperationType = "task.create"
	OperationFieldSet      OperationType = "field.set"
	OperationSetAdd        OperationType = "set.add"
	OperationSetRemove     OperationType = "set.remove"
	OperationTaskTombstone OperationType = "task.tombstone"
	OperationTaskRestore   OperationType = "task.restore"
)

type Operation struct {
	ID    string        `json:"id"`
	Type  OperationType `json:"type"`
	Field string        `json:"field,omitempty"`
	Value string        `json:"value,omitempty"`
	Task  *TaskData     `json:"task,omitempty"`
}

type OperationPack struct {
	Format            string      `json:"format"`
	Version           int         `json:"version"`
	ProjectID         string      `json:"projectId"`
	TaskID            string      `json:"taskId"`
	HistoryGeneration string      `json:"historyGeneration"`
	Actor             Actor       `json:"actor"`
	LogicalClock      uint64      `json:"logicalClock"`
	WallTime          time.Time   `json:"wallTime"`
	Operations        []Operation `json:"operations"`
}

type History struct {
	Generation    string  `json:"generation"`
	CompactedFrom *string `json:"compactedFrom"`
}

type StateDocument struct {
	Format       string   `json:"format"`
	Version      int      `json:"version"`
	ProjectID    string   `json:"projectId"`
	TaskID       string   `json:"taskId"`
	History      History  `json:"history"`
	LogicalClock uint64   `json:"logicalClock"`
	Task         TaskData `json:"task"`
}

// Apply validates and applies one immutable operation pack to a task state.
func Apply(parent *StateDocument, pack OperationPack, projectKey string) (StateDocument, error) {
	if err := validateOperationPackDocument(pack, projectKey); err != nil {
		return StateDocument{}, err
	}

	if parent == nil {
		if pack.LogicalClock != 1 {
			return StateDocument{}, corrupt("root operation pack logical clock must be 1")
		}
		if len(pack.Operations) != 1 || pack.Operations[0].Type != OperationTaskCreate {
			return StateDocument{}, corrupt("root operation pack must contain exactly one task.create operation")
		}
		return applyCreate(pack, projectKey)
	}

	if err := validateStateDocument(*parent, projectKey); err != nil {
		return StateDocument{}, err
	}
	if err := validateParentMatchesPack(*parent, pack); err != nil {
		return StateDocument{}, err
	}
	if pack.LogicalClock != parent.LogicalClock+1 {
		return StateDocument{}, corrupt("operation pack logical clock must advance parent by one")
	}
	if parent.Task.Deleted {
		if len(pack.Operations) != 1 || pack.Operations[0].Type != OperationTaskRestore {
			return StateDocument{}, corrupt("cannot mutate a tombstoned task")
		}
	} else {
		for _, operation := range pack.Operations {
			if operation.Type == OperationTaskRestore {
				return StateDocument{}, corrupt("task.restore requires a tombstoned task")
			}
		}
	}

	task := copyTaskData(parent.Task)
	for _, operation := range pack.Operations {
		if task.Deleted && operation.Type != OperationTaskRestore {
			return StateDocument{}, corrupt("cannot mutate a tombstoned task")
		}
		if operation.Type == OperationTaskCreate {
			return StateDocument{}, corrupt("task.create requires no parent")
		}
		if err := applyOperation(&task, operation, projectKey); err != nil {
			return StateDocument{}, err
		}
	}

	task.UpdatedAt = pack.WallTime
	normalized, err := normalizeCanonicalTask(projectKey, task)
	if err != nil {
		return StateDocument{}, Wrap(CategoryCorruptData, "operation pack produced an invalid task", err)
	}

	return StateDocument{
		Format:       stateDocumentFormat,
		Version:      documentVersion,
		ProjectID:    pack.ProjectID,
		TaskID:       pack.TaskID,
		History:      parent.History,
		LogicalClock: pack.LogicalClock,
		Task:         normalized,
	}, nil
}

// ValidateCheckpoint verifies that a stored state is the canonical result of applying a pack.
func ValidateCheckpoint(parent *StateDocument, pack OperationPack, stored StateDocument, projectKey string) error {
	computed, err := Apply(parent, pack, projectKey)
	if err != nil {
		return err
	}
	computedBytes, err := EncodeDocument(computed)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode computed checkpoint", err)
	}
	storedBytes, err := EncodeDocument(stored)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode stored checkpoint", err)
	}
	if !reflect.DeepEqual(computedBytes, storedBytes) {
		return corrupt("stored checkpoint differs from computed state")
	}
	return nil
}

func applyCreate(pack OperationPack, projectKey string) (StateDocument, error) {
	operation := pack.Operations[0]
	if operation.Task == nil || operation.Field != "" || operation.Value != "" {
		return StateDocument{}, corrupt("task.create must contain only task data")
	}
	if operation.Task.Deleted {
		return StateDocument{}, corrupt("task.create cannot create a deleted task")
	}
	if operation.Task.CreatedAt.IsZero() {
		return StateDocument{}, corrupt("task.create requires createdAt")
	}

	task := copyTaskData(*operation.Task)
	task.UpdatedAt = pack.WallTime
	normalized, err := normalizeCanonicalTask(projectKey, task)
	if err != nil {
		return StateDocument{}, Wrap(CategoryCorruptData, "task.create contains an invalid task", err)
	}
	return StateDocument{
		Format:       stateDocumentFormat,
		Version:      documentVersion,
		ProjectID:    pack.ProjectID,
		TaskID:       pack.TaskID,
		History:      History{Generation: pack.HistoryGeneration},
		LogicalClock: pack.LogicalClock,
		Task:         normalized,
	}, nil
}

func applyOperation(task *TaskData, operation Operation, projectKey string) error {
	if err := validateOperation(operation); err != nil {
		return err
	}
	switch operation.Type {
	case OperationFieldSet:
		return applyFieldSet(task, operation)
	case OperationSetAdd:
		return applySetAdd(task, operation, projectKey)
	case OperationSetRemove:
		return applySetRemove(task, operation, projectKey)
	case OperationTaskTombstone:
		task.Deleted = true
		return nil
	case OperationTaskRestore:
		task.Deleted = false
		return nil
	default:
		return corrupt("unsupported operation type %q", operation.Type)
	}
}

func applyFieldSet(task *TaskData, operation Operation) error {
	if err := validateFieldSetOperation(operation); err != nil {
		return err
	}
	switch operation.Field {
	case "title":
		task.Title = operation.Value
	case "description":
		task.Description = operation.Value
	case "status":
		task.Status = Status(operation.Value)
	case "priority":
		task.Priority = Priority(operation.Value)
	case "rank":
		task.Rank = operation.Value
	}
	return nil
}

func applySetAdd(task *TaskData, operation Operation, projectKey string) error {
	if err := validateSetOperation(operation, projectKey); err != nil {
		return err
	}
	switch operation.Field {
	case "labels":
		task.Labels = append(task.Labels, operation.Value)
	case "dependencies":
		task.Dependencies = append(task.Dependencies, operation.Value)
	}
	return nil
}

func applySetRemove(task *TaskData, operation Operation, projectKey string) error {
	if err := validateSetOperation(operation, projectKey); err != nil {
		return err
	}
	switch operation.Field {
	case "labels":
		task.Labels = removeValue(task.Labels, operation.Value)
	case "dependencies":
		task.Dependencies = removeValue(task.Dependencies, operation.Value)
	}
	return nil
}

func validateOperationPackEnvelope(pack OperationPack, projectKey string) error {
	if pack.Format != operationPackFormat {
		return corrupt("unsupported operation pack format %q", pack.Format)
	}
	if pack.Version != documentVersion {
		return corrupt("unsupported operation pack version %d", pack.Version)
	}
	if err := validateCanonicalULID("operation pack project ID", pack.ProjectID); err != nil {
		return err
	}
	if err := ValidateTaskID(projectKey, pack.TaskID); err != nil {
		return Wrap(CategoryCorruptData, "operation pack task ID is invalid", err)
	}
	if err := validateCanonicalULID("operation pack history generation", pack.HistoryGeneration); err != nil {
		return err
	}
	if strings.TrimSpace(pack.Actor.ID) == "" {
		return corrupt("operation pack actor ID must not be blank")
	}
	if pack.LogicalClock == 0 {
		return corrupt("operation pack logical clock must be positive")
	}
	if pack.WallTime.IsZero() {
		return corrupt("operation pack wall time must be present")
	}
	if len(pack.Operations) == 0 {
		return corrupt("operation pack must contain at least one operation")
	}
	for _, operation := range pack.Operations {
		if strings.TrimSpace(operation.ID) == "" {
			return corrupt("operation ID must not be blank")
		}
	}
	return nil
}

func validateStateDocument(state StateDocument, projectKey string) error {
	if err := validateStateEnvelope(state, projectKey); err != nil {
		return err
	}
	normalized, err := normalizeCanonicalTask(projectKey, state.Task)
	if err != nil {
		return Wrap(CategoryCorruptData, "task state contains an invalid task", err)
	}
	if !reflect.DeepEqual(state.Task, normalized) {
		return corrupt("task state is not canonical")
	}
	return nil
}

func validateStateEnvelope(state StateDocument, projectKey string) error {
	if state.Format != stateDocumentFormat {
		return corrupt("unsupported task state format %q", state.Format)
	}
	if state.Version != documentVersion {
		return corrupt("unsupported task state version %d", state.Version)
	}
	if err := validateCanonicalULID("task state project ID", state.ProjectID); err != nil {
		return err
	}
	if err := ValidateTaskID(projectKey, state.TaskID); err != nil {
		return Wrap(CategoryCorruptData, "task state task ID is invalid", err)
	}
	if err := validateCanonicalULID("task state history generation", state.History.Generation); err != nil {
		return err
	}
	if state.History.CompactedFrom != nil {
		return corrupt("task state compaction metadata is unsupported in the append-only POC")
	}
	if state.LogicalClock == 0 {
		return corrupt("task state logical clock must be positive")
	}
	return nil
}

func validateParentMatchesPack(parent StateDocument, pack OperationPack) error {
	if parent.ProjectID != pack.ProjectID {
		return corrupt("operation pack project ID does not match parent")
	}
	if parent.TaskID != pack.TaskID {
		return corrupt("operation pack task ID does not match parent")
	}
	if parent.History.Generation != pack.HistoryGeneration {
		return corrupt("operation pack history generation does not match parent")
	}
	return nil
}

func validateOperation(operation Operation) error {
	if strings.TrimSpace(operation.ID) == "" {
		return corrupt("operation ID must not be blank")
	}
	switch operation.Type {
	case OperationFieldSet, OperationSetAdd, OperationSetRemove:
		if operation.Task != nil {
			return corrupt("%s must not contain task data", operation.Type)
		}
	case OperationTaskTombstone, OperationTaskRestore:
		if operation.Task != nil || operation.Field != "" || operation.Value != "" {
			return corrupt("%s must not contain a payload", operation.Type)
		}
	default:
		return corrupt("unsupported operation type %q", operation.Type)
	}
	return nil
}

func validateOperationPackDocument(pack OperationPack, projectKey string) error {
	if err := validateOperationPackEnvelope(pack, projectKey); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(pack.Operations))
	for _, operation := range pack.Operations {
		if err := validateOperationDocument(operation, projectKey); err != nil {
			return err
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			return corrupt("operation pack contains duplicate operation ID %q", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	return nil
}

func validateOperationDocument(operation Operation, projectKey string) error {
	if err := validateOperationID(operation.ID); err != nil {
		return err
	}
	switch operation.Type {
	case OperationTaskCreate:
		return validateTaskCreateOperation(operation, projectKey)
	case OperationFieldSet:
		return validateFieldSetOperation(operation)
	case OperationSetAdd, OperationSetRemove:
		return validateSetOperation(operation, projectKey)
	case OperationTaskTombstone:
		if operation.Task != nil || operation.Field != "" || operation.Value != "" {
			return corrupt("task.tombstone must not contain a payload")
		}
		return nil
	case OperationTaskRestore:
		if operation.Task != nil || operation.Field != "" || operation.Value != "" {
			return corrupt("task.restore must not contain a payload")
		}
		return nil
	default:
		return corrupt("unsupported operation type %q", operation.Type)
	}
}

func validateOperationID(id string) error {
	return validateCanonicalULID("operation ID", id)
}

func validateCanonicalULID(description, id string) error {
	parsed, err := ulid.ParseStrict(id)
	if err != nil || parsed.String() != id {
		return corrupt("%s %q must contain a canonical uppercase ULID", description, id)
	}
	return nil
}

func validateTaskCreateOperation(operation Operation, projectKey string) error {
	if operation.Task == nil || operation.Field != "" || operation.Value != "" {
		return corrupt("task.create must contain only task data")
	}
	if operation.Task.Deleted {
		return corrupt("task.create cannot create a deleted task")
	}
	if operation.Task.CreatedAt.IsZero() {
		return corrupt("task.create requires createdAt")
	}
	normalized, err := normalizeCanonicalTask(projectKey, copyTaskData(*operation.Task))
	if err != nil {
		return Wrap(CategoryCorruptData, "task.create contains an invalid task", err)
	}
	if !reflect.DeepEqual(*operation.Task, normalized) {
		return corrupt("task.create task data is not canonical")
	}
	return nil
}

func validateFieldSetOperation(operation Operation) error {
	if operation.Task != nil {
		return corrupt("field.set must not contain task data")
	}
	switch operation.Field {
	case "title":
		if strings.TrimSpace(operation.Value) == "" {
			return corrupt("field.set title must not be blank")
		}
	case "description":
	case "status":
		if !isValidStatus(Status(operation.Value)) {
			return corrupt("field.set status %q is invalid", operation.Value)
		}
	case "priority":
		if !isValidPriority(Priority(operation.Value)) {
			return corrupt("field.set priority %q is invalid", operation.Value)
		}
	case "rank":
		if _, err := parseRank(operation.Value); err != nil {
			return corrupt("field.set rank %q is invalid", operation.Value)
		}
	default:
		return corrupt("field.set does not support field %q", operation.Field)
	}
	return nil
}

func validateSetOperation(operation Operation, projectKey string) error {
	if operation.Task != nil {
		return corrupt("%s must not contain task data", operation.Type)
	}
	switch operation.Field {
	case "labels":
		if operation.Value == "" {
			return corrupt("%s label must not be empty", operation.Type)
		}
	case "dependencies":
		if err := ValidateTaskID(projectKey, operation.Value); err != nil {
			return Wrap(CategoryCorruptData, string(operation.Type)+" dependency is invalid", err)
		}
	default:
		return corrupt("%s does not support field %q", operation.Type, operation.Field)
	}
	return nil
}

func normalizeCanonicalTask(projectKey string, task TaskData) (TaskData, error) {
	normalized, err := NormalizeTask(projectKey, task)
	if err != nil {
		return TaskData{}, err
	}
	if normalized.Labels == nil {
		normalized.Labels = []string{}
	}
	if normalized.Dependencies == nil {
		normalized.Dependencies = []string{}
	}
	return normalized, nil
}

func copyTaskData(task TaskData) TaskData {
	copy := task
	copy.Labels = append([]string(nil), task.Labels...)
	copy.Dependencies = append([]string(nil), task.Dependencies...)
	return copy
}

func removeValue(values []string, value string) []string {
	filtered := make([]string, 0, len(values))
	for _, candidate := range values {
		if candidate != value {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func corrupt(format string, args ...any) error {
	return Errorf(CategoryCorruptData, format, args...)
}
