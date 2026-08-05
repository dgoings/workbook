package gitstore

import (
	"context"

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

type historyCandidate struct {
	requestIndex  int
	objectID      string
	parents       []string
	structuralErr error
}

// ReadTaskHistories reads all requested linear histories with one tip batch,
// one shared parent-graph walk, and one commit batch. Per-task structural and
// document failures retain every other task's result.
func (r *Repository) ReadTaskHistories(
	ctx context.Context,
	config core.ProjectConfig,
	requests []TaskHistoryRequest,
) ([]TaskHistoryResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return []TaskHistoryResult{}, nil
	}

	if err := r.validateHistoryRequests(ctx, config, requests, core.CategoryCorruptData); err != nil {
		return nil, err
	}
	results := make([]TaskHistoryResult, len(requests))
	for i, request := range requests {
		results[i].TaskID = request.Head.TaskID
		results[i].Head = request.Head.ObjectID
	}

	heads := make([]TaskHead, len(requests))
	for i, request := range requests {
		heads[i] = request.Head
	}
	tipResults, err := r.readTaskHeadsPartial(ctx, config, heads)
	if err != nil {
		return nil, err
	}

	graphIndexes := make([]int, 0, len(requests))
	for i, tip := range tipResults {
		if tip.Err != nil {
			results[i].CheckedCommits = 1
			results[i].Failure = historyFailure(requests[i], requests[i].Head.ObjectID, tip.Err)
			continue
		}
		graphIndexes = append(graphIndexes, i)
	}
	graph, err := r.parentGraphFor(ctx, requests, graphIndexes)
	if err != nil {
		return nil, err
	}

	perRequest := make([][]historyCandidate, len(requests))
	for _, index := range graphIndexes {
		candidates, boundaryReached, err := walkCommitChain(graph, requests[index], index)
		if err != nil {
			return nil, err
		}
		results[index].BoundaryReached = boundaryReached
		perRequest[index] = candidates
	}

	var candidates []historyCandidate
	var candidateHeads []TaskHead
	for index := range requests {
		for _, candidate := range perRequest[index] {
			candidates = append(candidates, candidate)
			candidateHeads = append(candidateHeads, TaskHead{
				TaskID:   requests[index].Head.TaskID,
				ObjectID: candidate.objectID,
			})
		}
	}
	commitResults, err := r.readTaskHeadsPartialBatch(ctx, config, candidateHeads, true)
	if err != nil {
		return nil, err
	}
	for i, candidate := range candidates {
		result := &results[candidate.requestIndex]
		if result.Failure != nil {
			continue
		}
		result.CheckedCommits++
		if candidate.structuralErr != nil {
			result.Failure = historyFailure(
				requests[candidate.requestIndex],
				candidate.objectID,
				candidate.structuralErr,
			)
			continue
		}
		if commitResults[i].Err != nil {
			result.Failure = historyFailure(
				requests[candidate.requestIndex],
				candidate.objectID,
				commitResults[i].Err,
			)
			continue
		}
		snapshot := commitResults[i].Snapshot
		result.Commits = append(result.Commits, HistoryCommit{
			ObjectID:  candidate.objectID,
			Parents:   candidate.parents,
			Operation: snapshot.Operation,
			State:     snapshot.State,
		})
	}
	return results, nil
}

func historyFailure(request TaskHistoryRequest, commit string, err error) *HistoryFailure {
	return &HistoryFailure{
		TaskID: request.Head.TaskID,
		Commit: commit,
		Err:    err,
	}
}
