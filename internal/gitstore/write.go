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
	if err := validateWriteIdentity(config, pack, state); err != nil {
		return core.Snapshot{}, err
	}

	ref := taskRefPrefix + pack.TaskID
	if _, err := r.Git(ctx, nil, "check-ref-format", ref); err != nil {
		return core.Snapshot{}, core.Wrap(core.CategoryValidation, "task ref is invalid", err)
	}

	if parent == nil {
		if err := core.ValidateCheckpoint(nil, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	} else {
		if strings.TrimSpace(parent.Head) == "" {
			return core.Snapshot{}, core.Errorf(core.CategoryValidation, "parent head must not be blank")
		}
		if err := r.validateParentHead(ctx, parent.Head); err != nil {
			return core.Snapshot{}, err
		}
		if err := core.ValidateCheckpoint(&parent.State, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
		storedParent, err := r.readState(ctx, parent.Head)
		if err != nil {
			return core.Snapshot{}, err
		}
		if err := validateStoredParentIdentity(config, pack, storedParent); err != nil {
			return core.Snapshot{}, err
		}
		if err := core.ValidateCheckpoint(&storedParent, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	}

	packBytes, err := core.EncodeDocument(pack)
	if err != nil {
		return core.Snapshot{}, err
	}
	stateBytes, err := core.EncodeDocument(state)
	if err != nil {
		return core.Snapshot{}, err
	}
	operationBlob, err := r.writeBlob(ctx, packBytes)
	if err != nil {
		return core.Snapshot{}, err
	}
	stateBlob, err := r.writeBlob(ctx, stateBytes)
	if err != nil {
		return core.Snapshot{}, err
	}
	tree, err := r.writeTaskTree(ctx, operationBlob, stateBlob)
	if err != nil {
		return core.Snapshot{}, err
	}
	head, err := r.writeCommit(ctx, tree, parent, reason)
	if err != nil {
		return core.Snapshot{}, err
	}

	expected := ""
	if parent != nil {
		expected = parent.Head
	}
	if _, err := r.Git(ctx, nil, "update-ref", "--create-reflog", "-m", "workbook: "+reason, ref, head, expected); err != nil {
		if r.refValueDiffers(ctx, ref, expected) {
			return core.Snapshot{}, core.Wrap(core.CategoryStaleWrite, "task ref changed concurrently", err)
		}
		return core.Snapshot{}, err
	}

	return core.Snapshot{Head: head, Operation: pack, State: state}, nil
}

func (r *Repository) validateParentHead(ctx context.Context, head string) error {
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
	if config.Version != projectVersion {
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

func (r *Repository) readState(ctx context.Context, head string) (core.StateDocument, error) {
	contents, err := r.Git(ctx, nil, "show", head+":state.json")
	if err != nil {
		return core.StateDocument{}, core.Wrap(core.CategoryCorruptData, "cannot read parent state", err)
	}
	state, err := core.DecodeStateDocument(contents)
	if err != nil {
		return core.StateDocument{}, err
	}
	return state, nil
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

func (r *Repository) writeCommit(ctx context.Context, tree string, parent *core.Snapshot, reason string) (string, error) {
	args := []string{"commit-tree", tree}
	if parent != nil {
		args = append(args, "-p", parent.Head)
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
