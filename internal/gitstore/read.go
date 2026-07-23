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

// List returns every validated Workbook task snapshot, ordered by canonical
// task ID. It uses Git's ref enumeration so packed and loose refs behave the
// same way.
func (r *Repository) List(ctx context.Context, config core.ProjectConfig) ([]core.Snapshot, error) {
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

	snapshots := make([]core.Snapshot, 0, len(refs))
	for _, ref := range refs {
		snapshot, err := r.readTip(ctx, config, ref.taskID, ref.objectID)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// Get returns the validated current snapshot stored at taskID's ref. It reads
// only the tip commit's two documents; replaying task history is a separate
// projection concern.
func (r *Repository) Get(ctx context.Context, config core.ProjectConfig, taskID string) (core.Snapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ID is invalid", err)
	}

	ref, found, err := r.taskRef(ctx, taskID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if !found {
		return core.Snapshot{}, core.Errorf(core.CategoryNotFound, "task %q was not found", taskID)
	}
	return r.readTip(ctx, config, ref.taskID, ref.objectID)
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
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", taskRefPrefix)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated task ref record")
	}

	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	refs := make([]taskRefRecord, 0, len(lines))
	for _, line := range lines {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid task ref record")
		}
		refName, objectID := string(parts[0]), string(parts[1])
		if !strings.HasPrefix(refName, taskRefPrefix) {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside the task namespace")
		}
		taskID := strings.TrimPrefix(refName, taskRefPrefix)
		if taskID == "" || strings.Contains(taskID, "/") {
			return nil, core.Errorf(core.CategoryCorruptData, "task ref %q does not name one task", refName)
		}
		refs = append(refs, taskRefRecord{taskID: taskID, objectID: objectID})
	}
	return refs, nil
}

func (r *Repository) taskRef(ctx context.Context, taskID string) (taskRefRecord, bool, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", taskRefPrefix+taskID)
	if err != nil {
		return taskRefRecord{}, false, err
	}
	if len(contents) == 0 {
		return taskRefRecord{}, false, nil
	}
	if contents[len(contents)-1] != '\n' {
		return taskRefRecord{}, false, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated task ref record")
	}
	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	if len(lines) != 1 {
		return taskRefRecord{}, false, core.Errorf(core.CategoryCorruptData, "task ref %q has nested entries", taskRefPrefix+taskID)
	}
	parts := bytes.Split(lines[0], []byte{0})
	if len(parts) != 2 || string(parts[0]) != taskRefPrefix+taskID || len(parts[1]) == 0 {
		return taskRefRecord{}, false, core.Errorf(core.CategoryCorruptData, "Git returned an invalid task ref record")
	}
	return taskRefRecord{taskID: taskID, objectID: string(parts[1])}, true, nil
}

func (r *Repository) readTip(ctx context.Context, config core.ProjectConfig, taskID, objectID string) (core.Snapshot, error) {
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryCorruptData, "task ref ID is invalid", err)
	}
	if err := r.rejectSymbolicTaskRef(ctx, taskRefPrefix+taskID); err != nil {
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
	return core.Snapshot{Head: objectID, Operation: pack, State: state}, nil
}

func (r *Repository) validateTipTopology(ctx context.Context, objectID string, pack core.OperationPack) error {
	contents, err := r.Git(ctx, nil, "cat-file", "commit", objectID)
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot read raw task commit", err)
	}
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
	if config.Version != projectVersion {
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
