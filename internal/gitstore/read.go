package gitstore

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// TaskHead identifies a task ref and the commit at its tip.
type TaskHead struct {
	TaskID   string
	ObjectID string
}

const taskRefFormat = "%(refname)%00%(objectname)%00%(symref)"

const trackingTaskRefPrefix = "refs/workbook/remotes/origin/tasks/"

// ListTaskHeads returns every Workbook task ref tip, ordered by task ID. It
// uses Git's ref enumeration so packed and loose refs behave the same way.
func (r *Repository) ListTaskHeads(ctx context.Context, config core.ProjectConfig) ([]TaskHead, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}

	refs, err := r.listTaskRefs(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].taskID < refs[j].taskID })

	heads := make([]TaskHead, 0, len(refs))
	for _, ref := range refs {
		if err := core.ValidateTaskID(config.Key, ref.taskID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task ref ID is invalid", err)
		}
		heads = append(heads, TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID})
	}
	return heads, nil
}

// InspectTaskHead returns the exact canonical task ref without reading its
// target object. Prefix resolution is intentionally not performed here.
func (r *Repository) InspectTaskHead(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
) (TaskHead, bool, error) {
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return TaskHead{}, false, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return TaskHead{}, false, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return TaskHead{}, false, err
	}

	ref, found, err := r.taskRef(ctx, taskID)
	if err != nil {
		return TaskHead{}, false, err
	}
	if !found {
		return TaskHead{}, false, nil
	}
	return TaskHead{TaskID: ref.taskID, ObjectID: ref.objectID}, true, nil
}

// ReadTaskHead returns the validated snapshot stored at head.
func (r *Repository) ReadTaskHead(ctx context.Context, config core.ProjectConfig, head TaskHead) (core.Snapshot, error) {
	snapshots, err := r.ReadTaskHeads(ctx, config, []TaskHead{head})
	if err != nil {
		return core.Snapshot{}, err
	}
	return snapshots[0], nil
}

// List returns every validated Workbook task snapshot, ordered by canonical
// task ID. It uses Git's ref enumeration so packed and loose refs behave the
// same way.
func (r *Repository) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
	heads, err := r.ListTaskHeads(ctx, config)
	if err != nil {
		return nil, err
	}
	return r.ReadTaskHeads(ctx, config, heads)
}

// Get returns the validated current snapshot stored at taskID's ref. It reads
// only the tip commit's two documents; replaying task history is a separate
// projection concern.
func (r *Repository) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	head, found, err := r.InspectTaskHead(ctx, config, taskID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if !found {
		return core.Snapshot{}, core.Errorf(core.CategoryNotFound, "task %q was not found", taskID)
	}
	return r.ReadTaskHead(ctx, config, head)
}

// Resolve returns a canonical full task ID for an unambiguous case-insensitive
// prefix. Candidates are obtained from List so corrupt owned refs never hide
// behind an otherwise matching result.
func (r *Repository) Resolve(ctx context.Context, config core.ProjectConfig, prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", core.Errorf(core.CategoryValidation, "task ID prefix must not be blank")
	}
	snapshots, err := r.List(ctx, config)
	if err != nil {
		return "", err
	}

	needle := strings.ToLower(prefix)
	var matches []string
	for _, snapshot := range snapshots {
		id := snapshot.Operation.TaskID
		if strings.HasPrefix(strings.ToLower(id), needle) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", core.Errorf(core.CategoryNotFound, "no task matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", core.Errorf(core.CategoryValidation, "task ID prefix %q is ambiguous", prefix)
	}
}

type taskRefRecord struct {
	taskID   string
	objectID string
}

func (r *Repository) listTaskRefs(ctx context.Context) ([]taskRefRecord, error) {
	config, err := r.LoadConfig()
	if err != nil {
		return nil, err
	}
	return r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
}

func (r *Repository) listOwnedTaskRefs(ctx context.Context, config core.ProjectConfig, prefix string) ([]taskRefRecord, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format="+taskRefFormat, prefix)
	if err != nil {
		return nil, err
	}
	if err := r.rememberObjectIDWidthFromOwnedRefOutput(contents); err != nil {
		return nil, err
	}
	return r.parseOwnedRefRecords(config, prefix, contents, "")
}

func (r *Repository) taskRef(ctx context.Context, taskID string) (taskRefRecord, bool, error) {
	config, err := r.LoadConfig()
	if err != nil {
		return taskRefRecord{}, false, err
	}
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format="+taskRefFormat, taskRefPrefix+taskID)
	if err != nil {
		return taskRefRecord{}, false, err
	}
	if err := r.rememberObjectIDWidthFromOwnedRefOutput(contents); err != nil {
		return taskRefRecord{}, false, err
	}
	refs, err := r.parseOwnedRefRecords(config, taskRefPrefix, contents, taskID)
	if err != nil {
		return taskRefRecord{}, false, err
	}
	switch len(refs) {
	case 0:
		return taskRefRecord{}, false, nil
	case 1:
		return refs[0], true, nil
	default:
		return taskRefRecord{}, false, core.Errorf(core.CategoryCorruptData, "task ref %q has nested entries", taskRefPrefix+taskID)
	}
}

// rememberObjectIDWidthFromOwnedRefOutput learns object width only from the
// direct output of Git's full-width %(objectname) atom. The parser itself never
// lets caller-supplied ref records establish this validation boundary.
func (r *Repository) rememberObjectIDWidthFromOwnedRefOutput(contents []byte) error {
	lineEnd := bytes.IndexByte(contents, '\n')
	if lineEnd < 0 {
		return nil
	}
	parts := bytes.Split(contents[:lineEnd], []byte{0})
	if len(parts) != 3 || len(parts[1]) == 0 {
		return nil
	}
	if err := r.rememberGitObjectID(string(parts[1])); err != nil {
		return core.Wrap(core.CategoryCorruptData, "Git returned an invalid task ref object ID", err)
	}
	return nil
}

func (r *Repository) parseOwnedRefRecords(
	config core.ProjectConfig,
	prefix string,
	contents []byte,
	expectedTaskID string,
) ([]taskRefRecord, error) {
	if prefix != taskRefPrefix && prefix != trackingTaskRefPrefix {
		return nil, core.Errorf(core.CategoryCorruptData, "unsupported Workbook task ref namespace %q", prefix)
	}
	if expectedTaskID != "" {
		if err := core.ValidateTaskID(config.Key, expectedTaskID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "expected task ref ID is invalid", err)
		}
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated task ref record")
	}

	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	refs := make([]taskRefRecord, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid task ref record")
		}
		refName, objectID, symbolicTarget := string(parts[0]), string(parts[1]), string(parts[2])
		if !strings.HasPrefix(refName, prefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside the task namespace")
		}
		taskID := strings.TrimPrefix(refName, prefix)
		if taskID == "" || strings.Contains(taskID, "/") {
			return nil, core.Errorf(core.CategoryCorruptData, "task ref %q does not name one task", refName)
		}
		if err := core.ValidateTaskID(config.Key, taskID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task ref ID is invalid", err)
		}
		if expectedTaskID != "" && taskID != expectedTaskID {
			return nil, core.Errorf(core.CategoryCorruptData, "task ref %q has nested entries", prefix+expectedTaskID)
		}
		if symbolicTarget != "" {
			return nil, core.Errorf(core.CategoryCorruptData, "task ref %q must not be symbolic", refName)
		}
		if err := r.validateFullObjectID(objectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task ref object ID is invalid", err)
		}
		if err := r.rememberGitObjectID(objectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task ref object ID is invalid", err)
		}
		if _, duplicate := seen[taskID]; duplicate {
			return nil, core.Errorf(core.CategoryCorruptData, "task ref %q was returned more than once", refName)
		}
		seen[taskID] = struct{}{}
		refs = append(refs, taskRefRecord{taskID: taskID, objectID: objectID})
	}
	return refs, nil
}

func (r *Repository) readTip(ctx context.Context, config core.ProjectConfig, taskID, objectID string) (core.Snapshot, error) {
	return r.ReadTaskHead(ctx, config, TaskHead{TaskID: taskID, ObjectID: objectID})
}

func (r *Repository) readTipAtRef(
	ctx context.Context,
	config core.ProjectConfig,
	taskID string,
	objectID string,
	refName string,
) (core.Snapshot, error) {
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "task ref ID is invalid", err)
	}
	if err := r.rejectSymbolicTaskRef(ctx, refName); err != nil {
		return core.Snapshot{}, err
	}
	objectType, err := r.Git(ctx, nil, "cat-file", "-t", objectID)
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "cannot determine task ref object type", err)
	}
	typeName, err := gitSingleLine(objectType)
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "Git returned an invalid task object type", err)
	}
	if typeName != "commit" {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task ref does not point directly to a commit")
	}

	if err := r.validateTaskTree(ctx, objectID); err != nil {
		return core.Snapshot{}, err
	}
	operationBytes, err := r.Git(ctx, nil, "show", objectID+":operation.json")
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "cannot read task operation", err)
	}
	stateBytes, err := r.Git(ctx, nil, "show", objectID+":state.json")
	if err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "cannot read task state", err)
	}

	pack, err := decodeCanonicalOperation(operationBytes)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := r.validateTipTopology(ctx, objectID, pack); err != nil {
		return core.Snapshot{}, err
	}
	state, err := decodeCanonicalState(stateBytes)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := validateTipIdentity(config, taskID, pack, state); err != nil {
		return core.Snapshot{}, err
	}
	if len(pack.Operations) == 1 && pack.Operations[0].Type == core.OperationTaskCreate {
		if err := core.ValidateCheckpoint(nil, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	}
	return core.Snapshot{Head: objectID, Operation: pack, State: state}, nil
}

func (r *Repository) validateTipTopology(ctx context.Context, objectID string, pack core.OperationPack) error {
	contents, err := r.Git(ctx, nil, "cat-file", "commit", objectID)
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot read raw task commit", err)
	}
	return validateTipTopologyBytes(contents, pack)
}

func validateTipTopologyBytes(contents []byte, pack core.OperationPack) error {
	headerEnd := bytes.Index(contents, []byte("\n\n"))
	if headerEnd < 0 {
		return core.Errorf(core.CategoryCorruptData, "task commit has no header terminator")
	}

	parentCount := 0
	for _, line := range bytes.Split(contents[:headerEnd], []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("parent ")) {
			continue
		}
		if fields := bytes.Fields(line); len(fields) != 2 {
			return core.Errorf(core.CategoryCorruptData, "task commit has an invalid parent header")
		}
		parentCount++
	}

	containsCreate := false
	for _, operation := range pack.Operations {
		if operation.Type == core.OperationTaskCreate {
			containsCreate = true
		}
	}
	if containsCreate {
		if len(pack.Operations) != 1 || pack.Operations[0].Type != core.OperationTaskCreate {
			return core.Errorf(core.CategoryCorruptData, "task.create must be the only operation in a root pack")
		}
		if parentCount != 0 {
			return core.Errorf(core.CategoryCorruptData, "task.create tip must have no parents")
		}
		return nil
	}
	if parentCount != 1 {
		return core.Errorf(core.CategoryCorruptData, "ordinary task tip must have exactly one parent")
	}
	return nil
}

func (r *Repository) rejectSymbolicTaskRef(ctx context.Context, refName string) error {
	if _, err := r.Git(ctx, nil, "symbolic-ref", "--quiet", refName); err == nil {
		return core.Errorf(core.CategoryCorruptData, "task ref %q must not be symbolic", refName)
	} else {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return nil
		}
		return core.Wrap(core.CategoryCorruptData, "cannot determine whether task ref is symbolic", err)
	}
}

func (r *Repository) validateTaskTree(ctx context.Context, objectID string) error {
	contents, err := r.Git(ctx, nil, "ls-tree", objectID)
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot read task tree", err)
	}
	if len(contents) == 0 || contents[len(contents)-1] != '\n' {
		return core.Errorf(core.CategoryCorruptData, "task tree has invalid entries")
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != 2 {
		return core.Errorf(core.CategoryCorruptData, "task tree must contain exactly operation.json and state.json")
	}

	entries := make(map[string]struct{}, 2)
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || (parts[1] != "operation.json" && parts[1] != "state.json") {
			return core.Errorf(core.CategoryCorruptData, "task tree has an unexpected entry")
		}
		fields := strings.Fields(parts[0])
		if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
			return core.Errorf(core.CategoryCorruptData, "task tree entry %q is not a regular blob", parts[1])
		}
		if _, exists := entries[parts[1]]; exists {
			return core.Errorf(core.CategoryCorruptData, "task tree contains duplicate entry %q", parts[1])
		}
		entries[parts[1]] = struct{}{}
	}
	if len(entries) != 2 {
		return core.Errorf(core.CategoryCorruptData, "task tree must contain exactly operation.json and state.json")
	}
	return nil
}

func decodeCanonicalOperation(contents []byte) (core.OperationPack, error) {
	pack, err := core.DecodeOperationPack(contents)
	if err != nil {
		return core.OperationPack{}, err
	}
	canonical, err := core.EncodeDocument(pack)
	if err != nil {
		return core.OperationPack{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize task operation", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.OperationPack{}, core.Errorf(core.CategoryCorruptData, "task operation is not canonical")
	}
	return pack, nil
}

func decodeCanonicalState(contents []byte) (core.StateDocument, error) {
	state, err := core.DecodeStateDocument(contents)
	if err != nil {
		return core.StateDocument{}, err
	}
	canonical, err := core.EncodeDocument(state)
	if err != nil {
		return core.StateDocument{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize task state", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.StateDocument{}, core.Errorf(core.CategoryCorruptData, "task state is not canonical")
	}
	return state, nil
}

func validateReadConfig(config core.ProjectConfig) error {
	if config.Format != projectFormat {
		return core.Errorf(core.CategoryValidation, "unsupported Workbook configuration format %q", config.Format)
	}
	if !supportedProjectVersion(config.Version) {
		return core.Errorf(core.CategoryValidation, "unsupported Workbook configuration version %d", config.Version)
	}
	if err := validateProjectID(config.ProjectID); err != nil {
		return err
	}
	if err := core.ValidateProjectKey(config.Key); err != nil {
		return err
	}
	return nil
}

func (r *Repository) validateRepositoryConfig(config core.ProjectConfig) error {
	if err := validateReadConfig(config); err != nil {
		return err
	}
	canonical, err := r.LoadConfig()
	if err != nil {
		return err
	}
	if config != canonical {
		return core.Errorf(core.CategoryValidation, "project configuration does not match this repository")
	}
	return nil
}

func validateTipIdentity(config core.ProjectConfig, taskID string, pack core.OperationPack, state core.StateDocument) error {
	if pack.ProjectID != config.ProjectID || state.ProjectID != config.ProjectID {
		return core.Errorf(core.CategoryCorruptData, "task documents do not match the configured project")
	}
	if pack.TaskID != taskID || state.TaskID != taskID {
		return core.Errorf(core.CategoryCorruptData, "task documents do not match the task ref")
	}
	if pack.TaskID != state.TaskID {
		return core.Errorf(core.CategoryCorruptData, "task operation and state task IDs do not match")
	}
	if pack.HistoryGeneration != state.History.Generation {
		return core.Errorf(core.CategoryCorruptData, "task operation and state history generations do not match")
	}
	if pack.LogicalClock != state.LogicalClock {
		return core.Errorf(core.CategoryCorruptData, "task operation and state logical clocks do not match")
	}
	return nil
}
