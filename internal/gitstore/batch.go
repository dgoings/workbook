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

// MaxObjectBytes is the largest Git object Workbook will read into memory.
//
// Every object Workbook reads is one commit, one task tree, or one Workbook
// document, and core's field ceilings bound the largest task document this
// version can write to roughly 70 KiB. This ceiling sits about sixty times
// above that so it never fires on a document a Workbook wrote, and stays low
// enough that a hand-built object pushed by a collaborator cannot exhaust
// memory in a clone that fetches it. It is checked against the size in Git's
// batch header, before the object is allocated, so an absurd claim costs one
// comparison rather than the memory it names.
const MaxObjectBytes = 4 << 20

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

	// Responses are streamed rather than buffered. Buffering the whole batch
	// holds every requested object resident at once, which makes List's cost
	// additive across tasks and leaves the per-object ceiling with nothing left
	// to protect: the memory is already spent by the time a header is parsed.
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(input.Bytes())
		return err
	})
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for _, request := range validRequests {
		objects, err := readBatchObjects(batch.Reader())
		if err != nil {
			return nil, batchReadError("cannot read task objects from Git batch", err)
		}
		snapshot, err := validateBatchSnapshot(objects, config, request.head, request.objectIDBytes)
		if err != nil {
			results[request.index].Err = err
			continue
		}
		if err := r.rememberGitObjectID(snapshot.Head); err != nil {
			results[request.index].Err = core.Wrap(core.CategoryCorruptData, "Git returned an invalid task object ID", err)
			continue
		}
		results[request.index].Snapshot = snapshot
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return results, nil
}

type batchObject struct {
	objectID string
	kind     string
	contents []byte
	missing  bool
}

// batchReadError categorizes a batch-reader failure for a caller.
//
// The reader states framing failures as plain errors, which need both a
// category and the context of what was being read. It states a refused object
// as a categorized error that already names the object and the ceiling, and
// error reporting shows only the outermost message, so re-wrapping that one
// would replace the only sentence that explains the failure with a generic one.
func batchReadError(context string, err error) error {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed
	}
	return core.Wrap(core.CategoryCorruptData, context, err)
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
	if err != nil {
		return batchObject{}, fmt.Errorf("invalid Git batch object size")
	}
	// The header is Git's claim about an object this process has not read yet,
	// and on a fetched ref it originates with whoever pushed it. Refusing here
	// is the whole point: allocating first and validating afterwards would spend
	// the memory the ceiling exists to withhold.
	if size > MaxObjectBytes {
		return batchObject{}, core.Errorf(
			core.CategoryCorruptData,
			"Git object %s is %d bytes, over Workbook's %d byte object ceiling",
			fields[0], size, MaxObjectBytes,
		)
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
