package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// HeadAdvance pairs a previously validated snapshot with a newly observed
// current task head.
type HeadAdvance struct {
	Previous core.Snapshot
	Current  TaskHead
}

type tipReadResult struct {
	Head     TaskHead
	Snapshot core.Snapshot
	Err      error
}

// ReadTaskHeads returns validated tip snapshots in the same order as heads,
// using one Git batch process for every requested object.
func (r *Repository) ReadTaskHeads(
	ctx context.Context,
	config core.ProjectConfig,
	heads []TaskHead,
) ([]core.Snapshot, error) {
	results, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}
	snapshots := make([]core.Snapshot, 0, len(results))
	for _, result := range results {
		if result.Err != nil {
			return nil, result.Err
		}
		snapshots = append(snapshots, result.Snapshot)
	}
	return snapshots, nil
}

// readTaskHeadsPartial reads every valid requested commit through one batch
// process. Requests may repeat a task ID so callers can flatten histories into
// one batch. Object and Workbook-document failures remain attributed to one
// request; malformed batch framing is fatal because later records cannot be
// trusted.
func (r *Repository) readTaskHeadsPartial(
	ctx context.Context,
	config core.ProjectConfig,
	heads []TaskHead,
) ([]tipReadResult, error) {
	return r.readTaskHeadsPartialBatch(ctx, config, heads, false)
}

// readTaskHeadsPartialBatch optionally runs an empty batch so fixed-shape
// callers retain the same framing and command-failure boundary with no objects.
func (r *Repository) readTaskHeadsPartialBatch(
	ctx context.Context,
	config core.ProjectConfig,
	heads []TaskHead,
	runEmpty bool,
) ([]tipReadResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if len(heads) == 0 && !runEmpty {
		return []tipReadResult{}, nil
	}

	results := make([]tipReadResult, len(heads))
	var input bytes.Buffer
	type batchRequest struct {
		index         int
		head          TaskHead
		objectIDBytes int
	}
	validRequests := make([]batchRequest, 0, len(heads))
	for i, head := range heads {
		results[i].Head = head
		if err := core.ValidateTaskID(config.Key, head.TaskID); err != nil {
			results[i].Err = core.Wrap(core.CategoryCorruptData, "task head ID is invalid", err)
			continue
		}
		decoded, err := decodeObjectID(head.ObjectID)
		if err != nil {
			results[i].Err = core.Wrap(core.CategoryCorruptData, "task head object ID is invalid", err)
			continue
		}
		validRequests = append(validRequests, batchRequest{index: i, head: head, objectIDBytes: len(decoded)})
		fmt.Fprintf(
			&input,
			"%s\n%s^{tree}\n%s:operation.json\n%s:state.json\n",
			head.ObjectID,
			head.ObjectID,
			head.ObjectID,
			head.ObjectID,
		)
	}
	if len(validRequests) == 0 && !runEmpty {
		return results, nil
	}

	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot read task tips", err)
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	for _, batch := range validRequests {
		objects, err := readBatchObjects(reader)
		if err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "cannot read task objects from Git batch", err)
		}
		snapshot, err := validateBatchSnapshot(objects, config, batch.head, batch.objectIDBytes)
		if err != nil {
			results[batch.index].Err = err
			continue
		}
		if err := r.rememberGitObjectID(snapshot.Head); err != nil {
			results[batch.index].Err = core.Wrap(core.CategoryCorruptData, "Git returned an invalid task object ID", err)
			continue
		}
		results[batch.index].Snapshot = snapshot
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "cannot finish reading Git object batch", err)
		}
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned unexpected trailing batch data")
	}
	return results, nil
}

type batchObject struct {
	objectID string
	kind     string
	contents []byte
	missing  bool
}

func readBatchObjects(reader *bufio.Reader) ([4]batchObject, error) {
	var objects [4]batchObject
	for i := range objects {
		object, err := readBatchObject(reader)
		if err != nil {
			return [4]batchObject{}, err
		}
		objects[i] = object
	}
	return objects, nil
}

func validateBatchSnapshot(
	objects [4]batchObject,
	config core.ProjectConfig,
	head TaskHead,
	objectIDBytes int,
) (core.Snapshot, error) {
	commit, tree, operationBlob, stateBlob := objects[0], objects[1], objects[2], objects[3]
	for _, object := range objects {
		if object.missing {
			return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "requested task object %q is missing", object.objectID)
		}
	}

	if commit.objectID != head.ObjectID || commit.kind != "commit" {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task ref does not point directly to a commit")
	}
	if tree.kind != "tree" {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task commit does not point to a tree")
	}
	if operationBlob.kind != "blob" || stateBlob.kind != "blob" {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task documents are not blobs")
	}

	commitTree, err := commitTreeObjectID(commit.contents)
	if err != nil {
		return core.Snapshot{}, err
	}
	if tree.objectID != commitTree {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task commit tree does not match its batch object")
	}
	treeEntries, err := parseRawTaskTree(tree.contents, objectIDBytes)
	if err != nil {
		return core.Snapshot{}, err
	}
	if treeEntries["operation.json"] != operationBlob.objectID ||
		treeEntries["state.json"] != stateBlob.objectID {
		return core.Snapshot{}, core.Errorf(core.CategoryCorruptData, "task tree entries do not match their batch objects")
	}

	pack, err := decodeCanonicalOperation(operationBlob.contents)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := validateTipTopologyBytes(commit.contents, pack); err != nil {
		return core.Snapshot{}, err
	}
	state, err := decodeCanonicalState(stateBlob.contents)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := validateTipIdentity(config, head.TaskID, pack, state); err != nil {
		return core.Snapshot{}, err
	}
	if len(pack.Operations) == 1 && pack.Operations[0].Type == core.OperationTaskCreate {
		if err := core.ValidateCheckpoint(nil, pack, state, config.Key); err != nil {
			return core.Snapshot{}, err
		}
	}
	return core.Snapshot{Head: head.ObjectID, Operation: pack, State: state}, nil
}

func readBatchObject(reader *bufio.Reader) (batchObject, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return batchObject{}, err
	}
	fields := strings.Fields(strings.TrimSuffix(header, "\n"))
	if len(fields) == 2 && fields[1] == "missing" {
		return batchObject{objectID: fields[0], missing: true}, nil
	}
	if len(fields) != 3 {
		return batchObject{}, fmt.Errorf("invalid Git batch object header")
	}
	if _, err := decodeObjectID(fields[0]); err != nil {
		return batchObject{}, fmt.Errorf("invalid Git batch object ID: %w", err)
	}
	size, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil || size > uint64(^uint(0)>>1) {
		return batchObject{}, fmt.Errorf("invalid Git batch object size")
	}
	contents := make([]byte, int(size))
	if _, err := io.ReadFull(reader, contents); err != nil {
		return batchObject{}, err
	}
	terminator, err := reader.ReadByte()
	if err != nil {
		return batchObject{}, err
	}
	if terminator != '\n' {
		return batchObject{}, fmt.Errorf("Git batch object has no newline terminator")
	}
	return batchObject{objectID: fields[0], kind: fields[1], contents: contents}, nil
}

func decodeObjectID(objectID string) ([]byte, error) {
	if objectID == "" || objectID != strings.ToLower(objectID) {
		return nil, fmt.Errorf("expected a lowercase hexadecimal object ID")
	}
	decoded, err := hex.DecodeString(objectID)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("expected a nonempty even-length hexadecimal object ID")
	}
	return decoded, nil
}

func commitTreeObjectID(contents []byte) (string, error) {
	headerEnd := bytes.Index(contents, []byte("\n\n"))
	if headerEnd < 0 {
		return "", core.Errorf(core.CategoryCorruptData, "task commit has no header terminator")
	}
	var treeID string
	for _, line := range bytes.Split(contents[:headerEnd], []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("tree ")) {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) != 2 || treeID != "" {
			return "", core.Errorf(core.CategoryCorruptData, "task commit has an invalid tree header")
		}
		treeID = string(fields[1])
		if _, err := decodeObjectID(treeID); err != nil {
			return "", core.Wrap(core.CategoryCorruptData, "task commit tree ID is invalid", err)
		}
	}
	if treeID == "" {
		return "", core.Errorf(core.CategoryCorruptData, "task commit has no tree header")
	}
	return treeID, nil
}

func parseRawTaskTree(contents []byte, objectIDBytes int) (map[string]string, error) {
	if objectIDBytes <= 0 {
		return nil, core.Errorf(core.CategoryCorruptData, "task tree object ID width is invalid")
	}
	entries := make(map[string]string, 2)
	for offset := 0; offset < len(contents); {
		modeEnd := bytes.IndexByte(contents[offset:], ' ')
		if modeEnd < 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree has an invalid mode")
		}
		modeEnd += offset
		nameEnd := bytes.IndexByte(contents[modeEnd+1:], 0)
		if nameEnd < 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree has an invalid name")
		}
		nameEnd += modeEnd + 1
		objectStart := nameEnd + 1
		objectEnd := objectStart + objectIDBytes
		if objectEnd > len(contents) {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree has a truncated object ID")
		}

		mode := string(contents[offset:modeEnd])
		name := string(contents[modeEnd+1 : nameEnd])
		if mode != "100644" {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree entry %q is not a regular blob", name)
		}
		if name != "operation.json" && name != "state.json" {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree has an unexpected entry")
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, core.Errorf(core.CategoryCorruptData, "task tree contains duplicate entry %q", name)
		}
		entries[name] = hex.EncodeToString(contents[objectStart:objectEnd])
		offset = objectEnd
	}
	if len(entries) != 2 {
		return nil, core.Errorf(core.CategoryCorruptData, "task tree must contain exactly operation.json and state.json")
	}
	return entries, nil
}

// ValidateTaskHeadAdvances verifies all changed heads with one bounded ancestry
// walk. Each previous head and its ancestors are excluded from Git's traversal.
func (r *Repository) ValidateTaskHeadAdvances(
	ctx context.Context,
	config core.ProjectConfig,
	advances []HeadAdvance,
) error {
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}

	seenTaskIDs := make(map[string]struct{}, len(advances))
	changed := make([]HeadAdvance, 0, len(advances))
	for _, advance := range advances {
		taskID := advance.Current.TaskID
		if _, duplicate := seenTaskIDs[taskID]; duplicate {
			return core.Errorf(core.CategoryCorruptData, "task head advance contains duplicate task ID %q", taskID)
		}
		seenTaskIDs[taskID] = struct{}{}
		if err := core.ValidateTaskID(config.Key, taskID); err != nil {
			return core.Wrap(core.CategoryCorruptData, "current task head ID is invalid", err)
		}
		if taskID != advance.Previous.Operation.TaskID ||
			taskID != advance.Previous.State.TaskID {
			return core.Errorf(core.CategoryCorruptData, "task head advance IDs do not match")
		}
		if _, err := decodeObjectID(advance.Current.ObjectID); err != nil {
			return core.Wrap(core.CategoryCorruptData, "current task head is missing or invalid", err)
		}
		if _, err := decodeObjectID(advance.Previous.Head); err != nil {
			return core.Wrap(core.CategoryCorruptData, "previous task head is missing or invalid", err)
		}
		if advance.Current.ObjectID != advance.Previous.Head {
			changed = append(changed, advance)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	var input bytes.Buffer
	for _, advance := range changed {
		fmt.Fprintln(&input, advance.Current.ObjectID)
	}
	for _, advance := range changed {
		fmt.Fprintf(&input, "^%s\n", advance.Previous.Head)
	}
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return core.Wrap(core.CategoryCorruptData, "cannot validate task head advances", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return err
	}
	unreachable := make([]HeadAdvance, 0, len(changed))
	for _, advance := range changed {
		if !graphReaches(graph, advance.Current.ObjectID, advance.Previous.Head) {
			unreachable = append(unreachable, advance)
		}
	}
	if len(unreachable) == 0 {
		return nil
	}

	// Reconciliation is the one thing that legitimately moves a canonical ref
	// off its previous tip, and it parks that tip in the process. The parked
	// ref is therefore the evidence that distinguishes a replay from a ref
	// rolled backwards by corruption, and it costs a Git process only on this
	// already-exceptional path.
	parked, err := r.parkedTaskHeads(ctx, config)
	if err != nil {
		return err
	}
	for _, advance := range unreachable {
		if _, found := parked[advance.Current.TaskID][advance.Previous.Head]; found {
			continue
		}
		return core.Errorf(
			core.CategoryCorruptData,
			"task %q current head is not a descendant of its previous head and that head was not parked by a reconciliation; run `workbook rebuild`",
			advance.Current.TaskID,
		)
	}
	return nil
}

func parseParentGraph(contents []byte) (map[string][]string, error) {
	graph := make(map[string][]string)
	if len(contents) == 0 {
		return graph, nil
	}
	if contents[len(contents)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated parent graph")
	}
	for _, line := range bytes.Split(contents[:len(contents)-1], []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) == 0 {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned an invalid parent graph record")
		}
		for _, field := range fields {
			if _, err := decodeObjectID(string(field)); err != nil {
				return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
			}
		}
		objectID := string(fields[0])
		if _, duplicate := graph[objectID]; duplicate {
			return nil, core.Errorf(core.CategoryCorruptData, "Git returned a duplicate parent graph record")
		}
		parents := make([]string, 0, len(fields)-1)
		for _, parent := range fields[1:] {
			parents = append(parents, string(parent))
		}
		graph[objectID] = parents
	}
	return graph, nil
}

func graphReaches(graph map[string][]string, current, previous string) bool {
	pending := []string{current}
	visited := make(map[string]struct{}, len(graph))
	for len(pending) > 0 {
		last := len(pending) - 1
		objectID := pending[last]
		pending = pending[:last]
		if objectID == previous {
			return true
		}
		if _, seen := visited[objectID]; seen {
			continue
		}
		visited[objectID] = struct{}{}
		parents, found := graph[objectID]
		if !found {
			continue
		}
		pending = append(pending, parents...)
	}
	return false
}
