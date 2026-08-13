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

// SupportedFormatGeneration is the highest writer-format generation this build
// can fold, for task packs and configuration packs alike.
//
// The generation is a second versioning axis beside `version`, and the two
// answer different questions. `version` names the shape of the envelope: what
// members exist and what a document is. The generation names the semantics
// inside it: whether folding this pack needs rules a reader might not have. A
// new operation type does not change the envelope at all, so bumping `version`
// for one would tell every reader that everything changed; bumping the
// generation tells them exactly what is true, which is that this one pack needs
// a newer fold.
//
// Zero is the generation every build up to and including v0.5.0 writes, and it
// is spelled by saying nothing: a document with no `minReader` member is
// generation zero. That is what keeps the marker free — see the golden byte
// tables, which would fail the moment a document this build writes gained it.
//
// Raising this constant is the last step of shipping a new operation type, not
// the first: the build has to be able to fold generation N before it may claim
// to.
//
// COUPLING. Anything that caches a verdict about a history has to record this
// value alongside it, because a verdict is a property of the history and of the
// build that read it, not of the history alone. `workbook validate` is the case
// that exists today: it stores its result per task head, and without the
// generation in the cache key an upgrade that raises this constant leaves every
// newer-writer verdict standing — the command keeps demanding an upgrade that
// has already happened, from cache, while the mutations it refused now succeed.
// See historyvalidation.readerGeneration. Any future cache of a fold's outcome
// owes the same.
const SupportedFormatGeneration = 0

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
	Format  string `json:"format"`
	Version int    `json:"version"`
	// MinReader is the lowest writer-format generation that can fold this pack.
	//
	// It is omitted when it is zero, and zero is what every pack this build
	// writes carries, so the member is absent from every document in every
	// repository today. A future build that ships an operation type older
	// readers cannot fold sets it, per operation type, through
	// operationMinReader — and from that moment an older clone reads the task
	// from its checkpoint and refuses to mutate it, instead of calling the
	// history corrupt.
	MinReader         int         `json:"minReader,omitempty"`
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
	Format  string `json:"format"`
	Version int    `json:"version"`
	// MinReader is the highest generation any pack in this task's history has
	// required, carried forward from the parent checkpoint by Apply.
	//
	// It is a running maximum rather than this commit's own requirement, and
	// that is the whole reason it is here. A newer build may write a pack that
	// needs generation 1 and then ordinary generation-0 packs on top of it; a
	// reader looking only at the tip pack would see nothing and would fold a
	// history it does not understand. Recording the watermark in the checkpoint
	// makes the question "can I fold this task at all?" cost one object read
	// rather than a walk of the whole chain — which is the same reason the
	// checkpoint exists at all.
	MinReader    int      `json:"minReader,omitempty"`
	ProjectID    string   `json:"projectId"`
	TaskID       string   `json:"taskId"`
	History      History  `json:"history"`
	LogicalClock uint64   `json:"logicalClock"`
	Task         TaskData `json:"task"`
}

// operationMinReader declares, per operation type, the writer-format generation
// a reader needs in order to fold a pack containing it.
//
// Every entry is zero, and the table exists anyway. It is the one place a
// future story bumps: the comments and attachments work adds its operation
// types here with the generation they need, and every writing path picks the
// value up from PackMinReader without being edited. Declaring it per operation
// type rather than per release is what keeps the marker honest — a pack of
// ordinary field.set operations written by a build that also knows how to write
// comments still carries no marker, so an older clone keeps folding everything
// it genuinely can.
var operationMinReader = map[OperationType]int{
	OperationTaskCreate:    0,
	OperationFieldSet:      0,
	OperationSetAdd:        0,
	OperationSetRemove:     0,
	OperationTaskTombstone: 0,
	OperationTaskRestore:   0,
}

// PackMinReader returns the generation a reader needs to fold these operations,
// which is the highest any one of them requires.
//
// An operation type this build does not know is not this function's problem: it
// is only ever called on operations this build is about to write, and it cannot
// write a type it does not have. An unknown type therefore keeps the zero value
// the map returns, which is the same answer as "nothing special is needed" —
// correct here precisely because it is unreachable.
func PackMinReader(operations []Operation) int {
	generation := 0
	for _, operation := range operations {
		if required := operationMinReader[operation.Type]; required > generation {
			generation = required
		}
	}
	return generation
}

// RequiresNewerReader reports a pack this build must not fold.
func (pack OperationPack) RequiresNewerReader() bool {
	return pack.MinReader > SupportedFormatGeneration
}

// RequiresNewerReader reports a checkpoint whose history this build must not
// fold past, either because the pack beside it needs a newer reader or because
// one further back in the chain did.
func (state StateDocument) RequiresNewerReader() bool {
	return state.MinReader > SupportedFormatGeneration
}

// newerWriter builds the one refusal every newer-writer path returns.
//
// The register is deliberate. It names the subject, says a newer Workbook wrote
// it, and says what to do; it does not say "damaged", "corrupt", "invalid" or
// "unreadable", because none of those is true and every one of them would send
// somebody to repair a repository that is exactly as its authors left it.
func newerWriter(format string, args ...any) error {
	return Errorf(CategoryNewerWriter, format, args...)
}

// newerWriterTask is the refusal for one task, in the wording every surface
// that refuses a task repeats.
func newerWriterTask(taskID string) error {
	return newerWriter(
		"task %s was written by a newer workbook; upgrade workbook to change it", taskID)
}

// refuseNewerTaskWriter reports whether either side of a fold carries a
// generation this build cannot read. The task is named from whichever side
// carries an ID, so the refusal is scoped even when the pack is the unreadable
// one.
func refuseNewerTaskWriter(parent *StateDocument, pack OperationPack) error {
	if parent != nil && parent.RequiresNewerReader() {
		return newerWriterTask(parent.TaskID)
	}
	if pack.RequiresNewerReader() {
		return newerWriterTask(pack.TaskID)
	}
	return nil
}

// foldedMinReader carries the history's generation watermark forward.
//
// It is a maximum rather than the pack's own value because the watermark is a
// claim about the whole chain: once one pack in a task's history has needed
// generation N, every checkpoint after it needs a reader that can replay from
// the root, and a later generation-zero pack does not take that back.
func foldedMinReader(parent *StateDocument, pack OperationPack) int {
	generation := pack.MinReader
	if parent != nil && parent.MinReader > generation {
		generation = parent.MinReader
	}
	return generation
}

// Apply validates and applies one immutable operation pack to a task state.
func Apply(parent *StateDocument, pack OperationPack, projectKey string) (StateDocument, error) {
	// The generation gate runs before anything else, because everything else is
	// a judgment this build is not entitled to make about a document it has
	// been told it cannot read. A pack that needs a newer reader is refused as
	// newer-writer even when it would also fail some other rule: the marker is
	// what tells us the failure is our age rather than the pack's contents.
	//
	// Both sides are checked. The pack because it may be the newer one; the
	// parent because a newer pack further back in the chain leaves its
	// watermark in every checkpoint after it, and folding a generation-zero
	// pack onto a task whose history this build cannot replay would produce a
	// checkpoint nobody could justify.
	if err := refuseNewerTaskWriter(parent, pack); err != nil {
		return StateDocument{}, err
	}
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
	// A pack against a tombstone must open with task.restore, and may then carry
	// ordinary operations: the restore has already brought the task back, so
	// what follows is applied to a live task. That is what lets a restore that
	// names a destination be one pack — one history entry, one refusal surface —
	// rather than a restore followed by a separate placement that a crash or a
	// concurrent write could leave half-done.
	//
	// "Ordinary" excludes task.tombstone, which is why the rule is not simply
	// "restore first". A pack that restored and then tombstoned would fold to a
	// tombstone with the clock advanced, and every later reader would take that
	// as a valid history for a task nothing legibly deleted. A pack restores or
	// it deletes; the two are separate intents and belong to separate packs.
	//
	// The rule was "exactly one task.restore" before. What that costs a clone on
	// an older build is narrow and worth stating precisely, because the obvious
	// guess is worse than the truth: reads and synchronization are unaffected.
	// Projection, list, board, show, rebuild, fetch, push, sync, further
	// mutations and divergence replay all read the state checkpoint beside the
	// pack rather than folding the pack, so an older clone shows the restored
	// task correctly and keeps working with it. The one symptom is `workbook
	// validate`, which does fold: it reports that one task as corrupt with
	// "cannot mutate a tombstoned task", permanently, and says nothing about any
	// other task. Nothing wedges and no ref is held back — which is a softer
	// failure than the custom-status precedent, where an older build refuses the
	// ref at fetch time and keeps stale data until it is upgraded.
	//
	// A restore is legal nowhere else. Against a live task it is meaningless,
	// and anywhere but first it is either an edit to a tombstone or a claim
	// about a task that is no longer tombstoned.
	for index, operation := range pack.Operations {
		if operation.Type != OperationTaskRestore {
			continue
		}
		if !parent.Task.Deleted {
			return StateDocument{}, corrupt("task.restore requires a tombstoned task")
		}
		if index != 0 {
			return StateDocument{}, corrupt("task.restore must be the first operation in its pack")
		}
	}
	if parent.Task.Deleted {
		if pack.Operations[0].Type != OperationTaskRestore {
			return StateDocument{}, corrupt("cannot mutate a tombstoned task")
		}
		for _, operation := range pack.Operations[1:] {
			if operation.Type == OperationTaskTombstone {
				return StateDocument{}, corrupt("task.restore must not be followed by task.tombstone")
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
		MinReader:    foldedMinReader(parent, pack),
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
		MinReader:    foldedMinReader(nil, pack),
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
	// A negative generation is not a claim about the future, it is a malformed
	// number, and no writer produces one.
	if pack.MinReader < 0 {
		return corrupt("operation pack minimum reader generation %d is invalid", pack.MinReader)
	}
	// Envelope validation is also what stands between a newer pack and
	// EncodeDocument, which would otherwise write it back out having silently
	// dropped every member this build could not decode.
	if pack.RequiresNewerReader() {
		return newerWriterTask(pack.TaskID)
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
	if state.MinReader < 0 {
		return corrupt("task state minimum reader generation %d is invalid", state.MinReader)
	}
	if state.RequiresNewerReader() {
		return newerWriterTask(state.TaskID)
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
		// Structural, not membership. This gate runs during replay, over
		// operations another clone already committed; refusing one because this
		// clone's vocabulary does not contain its status would turn a fetched
		// history into corrupt data, which is a claim about the repository
		// rather than about the configuration. What it still refuses is a value
		// that is not a status token at all, because no build ever wrote one.
		if err := ValidateStatusToken(Status(operation.Value)); err != nil {
			return Wrap(CategoryCorruptData, "field.set status is invalid", err)
		}
	case "priority":
		if !isValidPriority(Priority(operation.Value)) {
			return corrupt("field.set priority %q is invalid", operation.Value)
		}
	case "rank":
		// The value is not quoted here. parseRank names it when it is small
		// enough to name, and a stored operation document is bounded only by the
		// object ceiling, so formatting one into an error message would spend the
		// cost the rank ceiling withholds.
		if _, err := parseRank(operation.Value); err != nil {
			return Wrap(CategoryCorruptData, "field.set rank is invalid", err)
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
