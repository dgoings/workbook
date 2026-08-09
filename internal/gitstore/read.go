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

// IgnoredRef names one ref under origin's Workbook task namespace that this
// version does not recognize as naming exactly one task, together with why.
// Ref is always stated under the canonical prefix origin holds it at, even when
// the ref was observed through the local tracking mirror, because that is the
// name a user would have to act on.
type IgnoredRef struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
	// PlausibleTask reports that the name could still be some Workbook's task
	// ref — this project's key with an ID format this build predates, or a
	// second project's key sharing origin's namespace — even though this build
	// does not read it as one. Shared task history is append-only, so a caller
	// must never suggest deleting such a ref; only a name no project's format
	// can produce is safe to offer for removal.
	PlausibleTask bool `json:"plausibleTask"`
}

func (r *Repository) listTaskRefs(ctx context.Context) ([]taskRefRecord, error) {
	config, err := r.LoadConfig()
	if err != nil {
		return nil, err
	}
	// The canonical namespace holds no unrecognized names to report; every one
	// of them fails this listing outright.
	refs, _, err := r.listOwnedTaskRefs(ctx, config, taskRefPrefix)
	return refs, err
}

// listOwnedTaskRefs enumerates one Workbook task namespace. Its second result
// is populated only for the tracking namespace, where a name this version does
// not recognize is skipped rather than fatal.
func (r *Repository) listOwnedTaskRefs(
	ctx context.Context,
	config core.ProjectConfig,
	prefix string,
) ([]taskRefRecord, []IgnoredRef, error) {
	contents, err := r.Git(ctx, nil, "for-each-ref", "--format="+taskRefFormat, prefix)
	if err != nil {
		return nil, nil, err
	}
	if err := r.rememberObjectIDWidthFromOwnedRefOutput(contents); err != nil {
		return nil, nil, err
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
	refs, _, err := r.parseOwnedRefRecords(config, taskRefPrefix, contents, taskID)
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

// parseOwnedRefRecords validates a for-each-ref listing of one Workbook task
// namespace, applying the strictness that namespace has earned.
//
// The canonical namespace is under this tool's exclusive control, so a name
// there that does not resolve to one task is corruption and fails the listing.
// The tracking namespace mirrors whatever origin's collaborators pushed, and
// anyone with push access can write an arbitrary name under it; refusing the
// whole listing would let one stray ref deny fetch, push, and sync to every
// clone. Such a name is skipped and reported instead. Everything that is not a
// name — Git's own record framing, object ID width, symbolic refs, and repeated
// task IDs — stays fatal in both namespaces, because none of it is something a
// collaborator can write by choosing a ref name.
func (r *Repository) parseOwnedRefRecords(
	config core.ProjectConfig,
	prefix string,
	contents []byte,
	expectedTaskID string,
) ([]taskRefRecord, []IgnoredRef, error) {
	if prefix != taskRefPrefix && prefix != trackingTaskRefPrefix {
		return nil, nil, core.Errorf(core.CategoryCorruptData, "unsupported Workbook task ref namespace %q", prefix)
	}
	tolerateUnrecognizedNames := prefix == trackingTaskRefPrefix
	if expectedTaskID != "" {
		if err := core.ValidateTaskID(config.Key, expectedTaskID); err != nil {
			return nil, nil, core.Wrap(core.CategoryCorruptData, "expected task ref ID is invalid", err)
		}
	}
	if len(contents) == 0 {
		return nil, nil, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated task ref record")
	}

	lines := bytes.Split(contents[:len(contents)-1], []byte{'\n'})
	refs := make([]taskRefRecord, 0, len(lines))
	var ignored []IgnoredRef
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 {
			return nil, nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid task ref record")
		}
		refName, objectID, symbolicTarget := string(parts[0]), string(parts[1]), string(parts[2])
		if !strings.HasPrefix(refName, prefix) {
			return nil, nil, core.Errorf(core.CategoryCorruptData, "Git returned a ref outside the task namespace")
		}
		taskID := strings.TrimPrefix(refName, prefix)
		if taskID == "" || strings.Contains(taskID, "/") {
			if tolerateUnrecognizedNames {
				ignored = append(ignored, ignoredTaskRef(config, prefix, refName, "the ref does not name one task"))
				continue
			}
			return nil, nil, core.Errorf(core.CategoryCorruptData, "task ref %q does not name one task", refName)
		}
		if err := core.ValidateTaskID(config.Key, taskID); err != nil {
			if tolerateUnrecognizedNames {
				ignored = append(ignored, ignoredTaskRef(config, prefix, refName, err.Error()))
				continue
			}
			return nil, nil, core.Wrap(core.CategoryCorruptData, "task ref ID is invalid", err)
		}
		if expectedTaskID != "" && taskID != expectedTaskID {
			return nil, nil, core.Errorf(core.CategoryCorruptData, "task ref %q has nested entries", prefix+expectedTaskID)
		}
		if symbolicTarget != "" {
			return nil, nil, core.Errorf(core.CategoryCorruptData, "task ref %q must not be symbolic", refName)
		}
		if err := r.validateFullObjectID(objectID); err != nil {
			return nil, nil, core.Wrap(core.CategoryCorruptData, "task ref object ID is invalid", err)
		}
		if err := r.rememberGitObjectID(objectID); err != nil {
			return nil, nil, core.Wrap(core.CategoryCorruptData, "task ref object ID is invalid", err)
		}
		if _, duplicate := seen[taskID]; duplicate {
			return nil, nil, core.Errorf(core.CategoryCorruptData, "task ref %q was returned more than once", refName)
		}
		seen[taskID] = struct{}{}
		refs = append(refs, taskRefRecord{taskID: taskID, objectID: objectID})
	}
	return refs, ignored, nil
}

// ignoredTaskRef restates a ref observed in the local tracking mirror under the
// canonical prefix origin holds it at. Removing one is a push to origin, so
// naming the local mirror would name a ref the user must not, and cannot
// usefully, delete: the next fetch prunes it once origin's copy is gone.
//
// It also records whether the name could still be another Workbook's task, so
// no caller has to re-derive that from the reason text before deciding what to
// advise.
func ignoredTaskRef(config core.ProjectConfig, prefix, refName, reason string) IgnoredRef {
	name := strings.TrimPrefix(refName, prefix)
	return IgnoredRef{
		Ref:           taskRefPrefix + name,
		Reason:        reason,
		PlausibleTask: core.PlausibleTaskID(config.Key, name),
	}
}

// readTip reads one tip through the batch reader, which is deliberately the
// only code in this package that reads object contents.
//
// A per-object reader used to sit beside it, pulling operation.json,
// state.json, the raw commit, and the tree through `git show` and `git ls-tree`
// into an uncapped buffer. Routing every content read through one reader is
// what lets MaxObjectBytes bound them all; a second, unbounded path would leave
// standing exactly the hazard that ceiling exists to close.
func (r *Repository) readTip(ctx context.Context, config core.ProjectConfig, taskID, objectID string) (core.Snapshot, error) {
	return r.ReadTaskHead(ctx, config, TaskHead{TaskID: taskID, ObjectID: objectID})
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
