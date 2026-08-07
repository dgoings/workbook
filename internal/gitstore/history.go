package gitstore

import (
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
)

// TaskHistoryRequest identifies one task history and an optional already-seen
// boundary. StopAt is excluded from the returned commits.
type TaskHistoryRequest struct {
	Head   TaskHead
	StopAt string
}

// HistoryCommit is one structurally validated Workbook history commit.
type HistoryCommit struct {
	ObjectID  string
	Parents   []string
	Operation core.OperationPack
	State     core.StateDocument
}

// HistoryFailure attributes a structural or document failure to one task and
// one candidate commit.
type HistoryFailure struct {
	TaskID string
	Commit string
	Err    error
}

// TaskHistoryResult preserves one request's identity and validated prefix.
type TaskHistoryResult struct {
	TaskID          string
	Head            string
	BoundaryReached bool
	Commits         []HistoryCommit
	CheckedCommits  int
	Failure         *HistoryFailure
}

// TaskHistoryStart announces one task history before any of its commits. It
// carries the identity a streaming caller needs to open its own per-task state.
type TaskHistoryStart struct {
	TaskID          string
	Head            string
	BoundaryReached bool
}

// TaskHistoryStream receives one task history at a time. Begin runs once per
// request in request order, Commit runs once per validated commit oldest first,
// and End runs once with that task's checked count and first failure. End's
// result never repeats the commits Commit already delivered.
//
// The contract exists so a caller that folds history incrementally never has to
// hold more than one commit and one task's accumulated state resident. Any
// handler error stops the read and is returned unchanged, so a caller's own
// error category survives the boundary.
type TaskHistoryStream struct {
	Begin  func(TaskHistoryStart) error
	Commit func(taskID string, commit HistoryCommit) error
	End    func(TaskHistoryResult) error
}

type historyCandidate struct {
	requestIndex  int
	objectID      string
	parents       []string
	structuralErr error
}

// ReadTaskHistories reads all requested linear histories with one tip batch,
// one shared parent-graph walk, and one commit batch. Per-task structural and
// document failures retain every other task's result.
//
// It collects the streaming read, so a caller that wants the whole corpus
// resident asks for it explicitly rather than getting it by default.
func (r *Repository) ReadTaskHistories(
	ctx context.Context,
	config core.ProjectConfig,
	requests []TaskHistoryRequest,
) ([]TaskHistoryResult, error) {
	results := make([]TaskHistoryResult, 0, len(requests))
	err := r.ReadTaskHistoriesStream(ctx, config, requests, TaskHistoryStream{
		Begin: func(start TaskHistoryStart) error {
			results = append(results, TaskHistoryResult{
				TaskID:          start.TaskID,
				Head:            start.Head,
				BoundaryReached: start.BoundaryReached,
			})
			return nil
		},
		Commit: func(_ string, commit HistoryCommit) error {
			current := &results[len(results)-1]
			current.Commits = append(current.Commits, commit)
			return nil
		},
		End: func(result TaskHistoryResult) error {
			current := &results[len(results)-1]
			current.CheckedCommits = result.CheckedCommits
			current.Failure = result.Failure
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// ReadTaskHistoriesStream reads the same histories as ReadTaskHistories with
// the same three Git processes, but hands each commit to the caller as Git
// answers for it instead of materializing every commit of every task first.
//
// Streaming is what keeps a full audit's memory bounded: the residual is the
// shared parent graph plus one decoded commit, not the whole corpus decoded
// several times over.
func (r *Repository) ReadTaskHistoriesStream(
	ctx context.Context,
	config core.ProjectConfig,
	requests []TaskHistoryRequest,
	stream TaskHistoryStream,
) error {
	if stream.Begin == nil || stream.Commit == nil || stream.End == nil {
		return core.Errorf(core.CategoryOperational, "task history stream requires begin, commit, and end handlers")
	}
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	if err := r.validateHistoryRequests(ctx, config, requests, core.CategoryCorruptData); err != nil {
		return err
	}

	heads := make([]TaskHead, len(requests))
	for i, request := range requests {
		heads[i] = request.Head
	}
	tipResults, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return err
	}

	tipFailures := make([]*HistoryFailure, len(requests))
	graphIndexes := make([]int, 0, len(requests))
	for i, tip := range tipResults {
		if tip.Err != nil {
			tipFailures[i] = historyFailure(requests[i], requests[i].Head.ObjectID, tip.Err)
			continue
		}
		graphIndexes = append(graphIndexes, i)
	}
	// Only each tip's outcome matters from here, and the decoded tip snapshots
	// are one whole task document per request. Dropping them keeps them out of
	// the resident set for the whole streamed read.
	tipResults = nil

	chains, boundaries, err := r.walkRequestedChains(ctx, requests, graphIndexes)
	if err != nil {
		return err
	}

	objectIDBytes, err := r.objectIDWidth()
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot read task histories", err)
	}

	// The batch runs even with no candidates so that a request set whose tips
	// all failed, or whose boundaries all equal their heads, keeps the same
	// fixed transport shape and the same command-failure boundary.
	batch, err := r.startObjectBatch(ctx, func(writer io.Writer) error {
		return writeHistoryObjectRequests(writer, chains)
	})
	if err != nil {
		return err
	}
	defer batch.Close()

	for index, request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		result := TaskHistoryResult{
			TaskID:          request.Head.TaskID,
			Head:            request.Head.ObjectID,
			BoundaryReached: boundaries[index],
		}
		if failure := tipFailures[index]; failure != nil {
			result.CheckedCommits = 1
			result.Failure = failure
		}
		if err := stream.Begin(TaskHistoryStart{
			TaskID:          result.TaskID,
			Head:            result.Head,
			BoundaryReached: result.BoundaryReached,
		}); err != nil {
			return err
		}
		for _, candidate := range chains[index] {
			// Every requested object is consumed even after this task failed,
			// because the response stream is shared with every later task.
			objects, err := readBatchObjects(batch.Reader())
			if err != nil {
				return core.Wrap(core.CategoryCorruptData, "cannot read task objects from Git batch", err)
			}
			if result.Failure != nil {
				continue
			}
			result.CheckedCommits++
			if candidate.structuralErr != nil {
				result.Failure = historyFailure(request, candidate.objectID, candidate.structuralErr)
				continue
			}
			head := TaskHead{TaskID: request.Head.TaskID, ObjectID: candidate.objectID}
			snapshot, err := validateBatchSnapshot(objects, config, head, objectIDBytes)
			if err != nil {
				result.Failure = historyFailure(request, candidate.objectID, err)
				continue
			}
			if err := r.rememberGitObjectID(snapshot.Head); err != nil {
				result.Failure = historyFailure(request, candidate.objectID, core.Wrap(
					core.CategoryCorruptData,
					"Git returned an invalid task object ID",
					err,
				))
				continue
			}
			if err := stream.Commit(request.Head.TaskID, HistoryCommit{
				ObjectID:  candidate.objectID,
				Parents:   candidate.parents,
				Operation: snapshot.Operation,
				State:     snapshot.State,
			}); err != nil {
				return err
			}
		}
		if err := stream.End(result); err != nil {
			return err
		}
	}
	return batch.Finish()
}

// walkRequestedChains resolves every walkable request to its commit chain. The
// shared parent graph is the largest structure a read builds, and each chain
// copies the parents it keeps, so the graph is scoped to this call and released
// before the streamed object read begins.
func (r *Repository) walkRequestedChains(
	ctx context.Context,
	requests []TaskHistoryRequest,
	graphIndexes []int,
) ([][]historyCandidate, []bool, error) {
	graph, err := r.parentGraphFor(ctx, requests, graphIndexes)
	if err != nil {
		return nil, nil, err
	}
	chains := make([][]historyCandidate, len(requests))
	boundaries := make([]bool, len(requests))
	for _, index := range graphIndexes {
		candidates, boundaryReached, err := walkCommitChain(graph, requests[index], index)
		if err != nil {
			return nil, nil, err
		}
		boundaries[index] = boundaryReached
		chains[index] = candidates
	}
	return chains, boundaries, nil
}

// writeHistoryObjectRequests writes the commit, tree, operation, and state
// request for every candidate in task order, which is the order the reader
// consumes responses in.
func writeHistoryObjectRequests(writer io.Writer, chains [][]historyCandidate) error {
	for _, chain := range chains {
		for _, candidate := range chain {
			if _, err := fmt.Fprintf(
				writer,
				"%s\n%s^{tree}\n%s:operation.json\n%s:state.json\n",
				candidate.objectID,
				candidate.objectID,
				candidate.objectID,
				candidate.objectID,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func historyFailure(request TaskHistoryRequest, commit string, err error) *HistoryFailure {
	return &HistoryFailure{
		TaskID: request.Head.TaskID,
		Commit: commit,
		Err:    err,
	}
}
