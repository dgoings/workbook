package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// OperationCommit is one commit's operation pack together with its address in
// the chain. State is deliberately absent: every reader of this path
// reconstructs state by replaying from the root, so reading the stored
// checkpoint would be work nobody uses.
type OperationCommit struct {
	ObjectID  string
	Parent    string
	Operation core.OperationPack
}

// TaskOperationsResult is one task's operation chain, oldest first.
//
// BoundaryReached reports whether the walk stopped at the requested StopAt
// commit. A false value on a request that named one means the new head does not
// descend from it, which is what a reconciliation looks like from here.
type TaskOperationsResult struct {
	TaskID          string
	Head            string
	BoundaryReached bool
	Commits         []OperationCommit
	// Truncated names the commit that stopped the read. The valid prefix is
	// still returned, because a caller showing a history is better served by
	// most of it plus an honest boundary than by a failed command.
	Truncated *HistoryFailure
}

// ReadTaskOperations reads the requested operation chains with one existence
// probe, one shared parent-graph walk, and one operation-blob batch.
//
// It deliberately does not revalidate each commit's documents, tree shape, or
// checkpoint. The write path already prevents this clone from recording an
// invalid commit, and setup, sync, and fetch validate what arrives from
// elsewhere, so revalidating a whole history on every read buys nothing and
// makes an ordinary read scale with history depth. `workbook validate` remains
// the path that audits stored checkpoints.
func (r *Repository) ReadTaskOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []TaskHistoryRequest,
) ([]TaskOperationsResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return []TaskOperationsResult{}, nil
	}
	// A commit named on a command line is caller input, so a bad one is a
	// validation failure the caller can fix rather than repository corruption.
	if err := r.validateHistoryRequests(ctx, config, requests, core.CategoryValidation); err != nil {
		return nil, err
	}

	results := make([]TaskOperationsResult, len(requests))
	for index, request := range requests {
		results[index].TaskID = request.Head.TaskID
		results[index].Head = request.Head.ObjectID
	}

	present, err := r.presentCommits(ctx, requests)
	if err != nil {
		return nil, err
	}
	walkable := make([]int, 0, len(requests))
	for index, request := range requests {
		if _, found := present[request.Head.ObjectID]; !found {
			results[index].Truncated = &HistoryFailure{
				TaskID: request.Head.TaskID,
				Commit: request.Head.ObjectID,
				Err: core.Errorf(
					core.CategoryNotFound,
					"commit %s is not a commit object this clone holds",
					request.Head.ObjectID,
				),
			}
			continue
		}
		walkable = append(walkable, index)
	}
	if len(walkable) == 0 {
		return results, nil
	}

	graph, err := r.parentGraphFor(ctx, requests, walkable)
	if err != nil {
		return nil, err
	}

	chains := make([][]historyCandidate, len(requests))
	var heads []TaskHead
	for _, index := range walkable {
		chain, boundaryReached, err := walkCommitChain(graph, requests[index], index)
		if err != nil {
			return nil, err
		}
		chains[index] = chain
		results[index].BoundaryReached = boundaryReached
		for _, candidate := range chain {
			heads = append(heads, TaskHead{TaskID: requests[index].Head.TaskID, ObjectID: candidate.objectID})
		}
	}

	packs, err := r.readOperationPacks(ctx, heads)
	if err != nil {
		return nil, err
	}
	position := 0
	for _, index := range walkable {
		chain := chains[index]
		read := packs[position : position+len(chain)]
		position += len(chain)
		result := &results[index]
		for offset, candidate := range chain {
			if candidate.structuralErr != nil {
				result.Truncated = historyFailure(requests[index], candidate.objectID, candidate.structuralErr)
				break
			}
			if read[offset].err != nil {
				result.Truncated = historyFailure(requests[index], candidate.objectID, read[offset].err)
				break
			}
			pack := read[offset].pack
			if pack.TaskID != requests[index].Head.TaskID || pack.ProjectID != config.ProjectID {
				result.Truncated = historyFailure(requests[index], candidate.objectID, core.Errorf(
					core.CategoryCorruptData,
					"commit %s records an operation for a different task or project",
					candidate.objectID,
				))
				break
			}
			parent := ""
			if len(candidate.parents) == 1 {
				parent = candidate.parents[0]
			}
			result.Commits = append(result.Commits, OperationCommit{
				ObjectID:  candidate.objectID,
				Parent:    parent,
				Operation: pack,
			})
		}
	}
	return results, nil
}

// validateHistoryRequests rejects a malformed request set before any Git
// process runs, so a caller cannot transport an invalid ID. objectIDCategory
// decides how a malformed object ID is reported: repository-sourced requests
// are corrupt data, while a caller-supplied commit is a validation failure.
func (r *Repository) validateHistoryRequests(
	ctx context.Context,
	config core.ProjectConfig,
	requests []TaskHistoryRequest,
	objectIDCategory core.Category,
) error {
	seenTaskIDs := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if _, duplicate := seenTaskIDs[request.Head.TaskID]; duplicate {
			return core.Errorf(
				core.CategoryCorruptData,
				"task history requests contain duplicate task ID %q",
				request.Head.TaskID,
			)
		}
		seenTaskIDs[request.Head.TaskID] = struct{}{}
		if err := core.ValidateTaskID(config.Key, request.Head.TaskID); err != nil {
			return core.Wrap(core.CategoryCorruptData, "task history request ID is invalid", err)
		}
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return err
	}
	for _, request := range requests {
		if err := r.validateFullObjectID(request.Head.ObjectID); err != nil {
			return core.Wrap(
				objectIDCategory,
				fmt.Sprintf("task history commit %q must be a full Git object ID", request.Head.ObjectID),
				err,
			)
		}
		if request.StopAt != "" {
			if err := r.validateFullObjectID(request.StopAt); err != nil {
				return core.Wrap(
					objectIDCategory,
					fmt.Sprintf("task history boundary %q must be a full Git object ID", request.StopAt),
					err,
				)
			}
		}
	}
	return nil
}

// presentCommits reports which requested heads name a commit object this clone
// still holds. A named object that no longer resolves is an ordinary
// not-found for that argument: reconciliation retires the oldest parked
// pre-replay tips, after which their commits become collectable.
func (r *Repository) presentCommits(ctx context.Context, requests []TaskHistoryRequest) (map[string]struct{}, error) {
	var input bytes.Buffer
	wanted := make([]string, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if _, duplicate := seen[request.Head.ObjectID]; duplicate {
			continue
		}
		seen[request.Head.ObjectID] = struct{}{}
		wanted = append(wanted, request.Head.ObjectID)
		fmt.Fprintln(&input, request.Head.ObjectID)
	}
	output, err := r.Git(ctx, input.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot probe task history commits", err)
	}
	if len(output) == 0 || output[len(output)-1] != '\n' {
		return nil, core.Errorf(core.CategoryCorruptData, "Git returned an unterminated object probe")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != len(wanted) {
		return nil, core.Errorf(core.CategoryCorruptData, "Git probed %d objects, want %d", len(lines), len(wanted))
	}
	present := make(map[string]struct{}, len(wanted))
	for index, line := range lines {
		fields := bytes.Fields(line)
		if len(fields) == 3 && string(fields[0]) == wanted[index] && string(fields[1]) == "commit" {
			present[wanted[index]] = struct{}{}
		}
	}
	return present, nil
}

func (r *Repository) parentGraphFor(
	ctx context.Context,
	requests []TaskHistoryRequest,
	indexes []int,
) (map[string][]string, error) {
	var input bytes.Buffer
	for _, index := range indexes {
		fmt.Fprintln(&input, requests[index].Head.ObjectID)
	}
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--reverse", "--topo-order", "--parents", "--stdin")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot read task history parent graph", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return nil, err
	}
	for objectID, parents := range graph {
		if err := r.validateFullObjectID(objectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid history graph object ID", err)
		}
		for _, parent := range parents {
			if err := r.validateFullObjectID(parent); err != nil {
				return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid history graph object ID", err)
			}
		}
	}
	if err := validateCompleteParentGraph(graph); err != nil {
		return nil, err
	}
	return graph, nil
}

// walkCommitChain follows one single-parent history from its head to the
// requested boundary or its root, returning the chain oldest first.
func walkCommitChain(
	graph map[string][]string,
	request TaskHistoryRequest,
	requestIndex int,
) ([]historyCandidate, bool, error) {
	if _, found := graph[request.Head.ObjectID]; !found {
		return nil, false, core.Errorf(
			core.CategoryCorruptData,
			"Git parent graph omitted task history head %q",
			request.Head.ObjectID,
		)
	}

	var newestFirst []historyCandidate
	boundaryReached := false
	// The cycle guard covers one chain, not the shared graph. Pre-sizing it to
	// the graph zeroed one slot per commit in the whole corpus for every task,
	// which is quadratic work nobody uses.
	seenCommits := make(map[string]struct{})
	current := request.Head.ObjectID
	for {
		if current == request.StopAt {
			boundaryReached = true
			break
		}
		if _, seen := seenCommits[current]; seen {
			for candidate := range newestFirst {
				if newestFirst[candidate].objectID == current {
					newestFirst[candidate].structuralErr = core.Errorf(
						core.CategoryCorruptData,
						"task history contains a parent cycle at commit %q",
						current,
					)
					break
				}
			}
			break
		}
		seenCommits[current] = struct{}{}

		parents, found := graph[current]
		if !found {
			return nil, false, core.Errorf(
				core.CategoryCorruptData,
				"Git parent graph omitted task history commit %q",
				current,
			)
		}
		candidate := historyCandidate{
			requestIndex: requestIndex,
			objectID:     current,
			parents:      append([]string{}, parents...),
		}
		if len(parents) > 1 {
			candidate.structuralErr = core.Errorf(
				core.CategoryCorruptData,
				"task history commit %q has more than one parent",
				current,
			)
		}
		newestFirst = append(newestFirst, candidate)
		if candidate.structuralErr != nil || len(parents) == 0 {
			break
		}
		current = parents[0]
	}

	chain := make([]historyCandidate, len(newestFirst))
	for i := range newestFirst {
		chain[len(newestFirst)-1-i] = newestFirst[i]
	}
	return chain, boundaryReached, nil
}

type operationBlobResult struct {
	pack core.OperationPack
	err  error
}

// readOperationPacks reads one operation blob per commit in a single batch.
func (r *Repository) readOperationPacks(ctx context.Context, heads []TaskHead) ([]operationBlobResult, error) {
	results := make([]operationBlobResult, len(heads))
	if len(heads) == 0 {
		return results, nil
	}
	var input bytes.Buffer
	for _, head := range heads {
		fmt.Fprintf(&input, "%s:operation.json\n", head.ObjectID)
	}
	// Streamed rather than buffered, so one operation document is resident at a
	// time and the per-object ceiling bounds this read instead of merely
	// describing memory already spent.
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		_, err := writer.Write(input.Bytes())
		return err
	})
	if err != nil {
		return nil, err
	}
	defer batch.Close()

	for index := range heads {
		object, err := readBatchObject(batch.Reader())
		if err != nil {
			return nil, batch.ReadFailure("cannot read task operations from Git batch", err)
		}
		switch {
		case object.refused != nil:
			results[index].err = object.refused
		case object.missing:
			results[index].err = core.Errorf(core.CategoryCorruptData, "task commit has no operation document")
		case object.kind != "blob":
			results[index].err = core.Errorf(core.CategoryCorruptData, "task operation document is not a blob")
		default:
			results[index].pack, results[index].err = core.DecodeOperationPack(object.contents)
		}
	}
	if err := batch.Finish(); err != nil {
		return nil, err
	}
	return results, nil
}
