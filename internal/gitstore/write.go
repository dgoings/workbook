package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

const taskRefPrefix = "refs/workbook/tasks/"

// Write persists one task operation pack and its validated checkpoint, then
// atomically advances the task's ref from parent.Head.
func (r *Repository) Write(
	ctx context.Context,
	config core.ProjectConfig,
	parent *core.Snapshot,
	pack core.OperationPack,
	state core.StateDocument,
	reason string,
) (core.Snapshot, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.Snapshot{}, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := validateWriteIdentity(config, pack, state); err != nil {
		return core.Snapshot{}, err
	}

	ref := taskRefPrefix + pack.TaskID
	if _, err := r.Git(ctx, nil, "check-ref-format", ref); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ref is invalid", err)
	}
	if err := r.rejectSymbolicTaskRef(ctx, ref); err != nil {
		return core.Snapshot{}, err
	}

	if parent == nil {
		if err := core.ValidateCheckpoint(nil, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	} else {
		if err := r.validateParentHead(ctx, parent.Head); err != nil {
			return core.Snapshot{}, err
		}
		if err := core.ValidateCheckpoint(&parent.State, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
		current, found, err := r.taskRef(ctx, pack.TaskID)
		if err != nil {
			return core.Snapshot{}, err
		}
		if !found || current.objectID != parent.Head {
			return core.Snapshot{}, core.Errorf(core.CategoryStaleWrite, "task ref changed concurrently")
		}
		storedParent, err := r.readTip(ctx, config, pack.TaskID, parent.Head)
		if err != nil {
			return core.Snapshot{}, err
		}
		if err := validateStoredParentIdentity(config, pack, storedParent.State); err != nil {
			return core.Snapshot{}, err
		}
		if err := core.ValidateCheckpoint(&storedParent.State, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	}

	return r.writeCanonical(ctx, config, ref, parent, pack, state, reason)
}

// WriteValidated persists one task operation pack after its parent snapshot
// has already been observed and validated through the repository read path.
// The task ref compare-and-swap remains the authority for concurrent changes.
func (r *Repository) WriteValidated(
	ctx context.Context,
	config core.ProjectConfig,
	parent *core.Snapshot,
	pack core.OperationPack,
	state core.StateDocument,
	reason string,
) (core.Snapshot, error) {
	if err := r.validateRepositoryConfig(config); err != nil {
		return core.Snapshot{}, err
	}
	if err := validateWriteIdentity(config, pack, state); err != nil {
		return core.Snapshot{}, err
	}

	ref := taskRefPrefix + pack.TaskID
	if parent == nil {
		if err := core.ValidateCheckpoint(nil, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	} else {
		if err := r.validateFullObjectID(parent.Head); err != nil {
			return core.Snapshot{}, core.Wrap(core.CategoryValidation, "parent head must be a canonical object ID", err)
		}
		if err := core.ValidateCheckpoint(&parent.State, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	}

	return r.writeCanonical(ctx, config, ref, parent, pack, state, reason)
}

func (r *Repository) writeCanonical(
	ctx context.Context,
	config core.ProjectConfig,
	ref string,
	parent *core.Snapshot,
	pack core.OperationPack,
	state core.StateDocument,
	reason string,
) (core.Snapshot, error) {
	parentHead := ""
	if parent != nil {
		parentHead = parent.Head
	}
	head, err := r.writeTaskObjects(ctx, parentHead, pack, state, reason)
	if err != nil {
		return core.Snapshot{}, err
	}

	// A successful mutation of this task is the one moment the clone is
	// certainly acting on this task and already writing a ref, so it is where
	// superseded parked tips are retired. Reads and fetches leave them alone:
	// pruning during a fetch would delete recoverable local work in the same
	// command that orphaned it.
	pruned, err := r.prunableParkedRefs(ctx, config, pack.TaskID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := r.commitTaskRefUpdate(ctx, ref, head, parentHead, pruned, reason); err != nil {
		return core.Snapshot{}, err
	}
	return core.Snapshot{Head: head, Operation: pack, State: state}, nil
}

// writeTaskObjects durably records one operation pack and its checkpoint
// without touching any ref. Unreferenced objects are harmless, so a caller may
// write every commit it plans and then move refs in one transaction.
func (r *Repository) writeTaskObjects(
	ctx context.Context,
	parentHead string,
	pack core.OperationPack,
	state core.StateDocument,
	reason string,
) (string, error) {
	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		return "", err
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		return "", err
	}
	operationBlob, err := r.writeBlob(ctx, packBytes)
	if err != nil {
		return "", err
	}
	stateBlob, err := r.writeBlob(ctx, stateBytes)
	if err != nil {
		return "", err
	}
	tree, err := r.writeTaskTree(ctx, operationBlob, stateBlob)
	if err != nil {
		return "", err
	}
	return r.writeCommit(ctx, tree, parentHead, reason)
}

// commitTaskRefUpdate compare-and-swaps the task ref and retires superseded
// parked refs in one transaction, so a mutation never leaves the ref advanced
// while stale bookkeeping refs survive.
func (r *Repository) commitTaskRefUpdate(
	ctx context.Context,
	ref, head, expected string,
	pruned []string,
	reason string,
) error {
	reflogReason := reason
	if !strings.HasPrefix(reflogReason, "workbook:") {
		reflogReason = "workbook: " + reflogReason
	}

	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	fmt.Fprintf(&input, "update %s %s %s\n", ref, head, expected)
	for _, name := range pruned {
		fmt.Fprintf(&input, "delete %s\n", name)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(
		ctx,
		input.Bytes(),
		"update-ref", "--no-deref", "--create-reflog", "-m", reflogReason, "--stdin",
	); err != nil {
		if symbolicErr := r.rejectSymbolicTaskRef(ctx, ref); symbolicErr != nil {
			return symbolicErr
		}
		if r.refValueDiffers(ctx, ref, expected) {
			return core.Wrap(core.CategoryStaleWrite, "task ref changed concurrently", err)
		}
		return err
	}
	return nil
}

func (r *Repository) validateParentHead(ctx context.Context, head string) error {
	if strings.TrimSpace(head) == "" {
		return core.Errorf(core.CategoryValidation, "parent head must not be blank")
	}
	resolved, err := r.Git(ctx, nil, "rev-parse", "--verify", head+"^{commit}")
	if err != nil {
		return core.Wrap(core.CategoryValidation, "parent head must be a commit object ID", err)
	}
	resolvedHead, err := gitSingleLine(resolved)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "Git returned an invalid parent object ID", err)
	}
	if resolvedHead != head {
		return core.Errorf(core.CategoryValidation, "parent head must be a canonical commit object ID")
	}
	return nil
}

func validateWriteIdentity(config core.ProjectConfig, pack core.OperationPack, state core.StateDocument) error {
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
	if err := core.ValidateTaskID(config.Key, pack.TaskID); err != nil {
		return core.Wrap(core.CategoryValidation, "operation pack task ID is invalid", err)
	}
	if pack.ProjectID != config.ProjectID {
		return core.Errorf(core.CategoryValidation, "operation pack project ID does not match configuration")
	}
	if state.ProjectID != config.ProjectID {
		return core.Errorf(core.CategoryValidation, "state project ID does not match configuration")
	}
	if state.TaskID != pack.TaskID {
		return core.Errorf(core.CategoryValidation, "state task ID does not match operation pack")
	}
	return nil
}

func validateStoredParentIdentity(config core.ProjectConfig, pack core.OperationPack, state core.StateDocument) error {
	if state.ProjectID != config.ProjectID || state.ProjectID != pack.ProjectID {
		return core.Errorf(core.CategoryCorruptData, "parent state project ID does not match operation pack")
	}
	if state.TaskID != pack.TaskID {
		return core.Errorf(core.CategoryCorruptData, "parent state task ID does not match operation pack")
	}
	return nil
}

func (r *Repository) writeBlob(ctx context.Context, contents []byte) (string, error) {
	output, err := r.Git(ctx, contents, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	objectID, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid blob object ID", err)
	}
	return objectID, nil
}

func (r *Repository) writeTaskTree(ctx context.Context, operationBlob, stateBlob string) (string, error) {
	var entries bytes.Buffer
	fmt.Fprintf(&entries, "100644 blob %s\toperation.json\n", operationBlob)
	fmt.Fprintf(&entries, "100644 blob %s\tstate.json\n", stateBlob)
	output, err := r.Git(ctx, entries.Bytes(), "mktree")
	if err != nil {
		return "", err
	}
	treeID, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid tree object ID", err)
	}
	return treeID, nil
}

func (r *Repository) writeCommit(ctx context.Context, tree string, parentHead string, reason string) (string, error) {
	args := []string{"commit-tree", tree}
	if parentHead != "" {
		args = append(args, "-p", parentHead)
	}
	args = append(args, "-m", reason)
	output, err := r.Git(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	commitID, err := gitSingleLine(output)
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "Git returned an invalid commit object ID", err)
	}
	return commitID, nil
}

func (r *Repository) refValueDiffers(ctx context.Context, ref, expected string) bool {
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname) %(objectname)", ref)
	if err != nil {
		return false
	}
	if len(output) == 0 {
		return expected != ""
	}
	if output[len(output)-1] != '\n' {
		return false
	}
	for _, line := range strings.Split(string(output[:len(output)-1]), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == ref {
			return fields[1] != expected
		}
	}
	return expected != ""
}
