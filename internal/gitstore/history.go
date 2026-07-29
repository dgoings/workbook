package gitstore

import (
	"bytes"
	"context"
	"fmt"

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

	results := make([]TaskHistoryResult, len(requests))
	seenTaskIDs := make(map[string]struct{}, len(requests))
	for i, request := range requests {
		results[i].TaskID = request.Head.TaskID
		results[i].Head = request.Head.ObjectID
		if _, duplicate := seenTaskIDs[request.Head.TaskID]; duplicate {
			return nil, core.Errorf(
				core.CategoryCorruptData,
				"task history requests contain duplicate task ID %q",
				request.Head.TaskID,
			)
		}
		seenTaskIDs[request.Head.TaskID] = struct{}{}
		if err := core.ValidateTaskID(config.Key, request.Head.TaskID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task history request ID is invalid", err)
		}
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, err
	}
	for _, request := range requests {
		if err := r.validateFullObjectID(request.Head.ObjectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "task history head object ID is invalid", err)
		}
		if request.StopAt != "" {
			if err := r.validateFullObjectID(request.StopAt); err != nil {
				return nil, core.Wrap(core.CategoryCorruptData, "task history boundary object ID is invalid", err)
			}
		}
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
	var input bytes.Buffer
	for _, index := range graphIndexes {
		fmt.Fprintln(&input, requests[index].Head.ObjectID)
	}
	output, err := r.Git(
		ctx,
		input.Bytes(),
		"rev-list", "--reverse", "--topo-order", "--parents", "--stdin",
	)
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

	perRequest := make([][]historyCandidate, len(requests))
	for _, index := range graphIndexes {
		request := requests[index]
		if _, found := graph[request.Head.ObjectID]; !found {
			return nil, core.Errorf(
				core.CategoryCorruptData,
				"Git parent graph omitted task history head %q",
				request.Head.ObjectID,
			)
		}

		var newestFirst []historyCandidate
		seenCommits := make(map[string]struct{}, len(graph))
		current := request.Head.ObjectID
		for {
			if current == request.StopAt {
				results[index].BoundaryReached = true
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
				return nil, core.Errorf(
					core.CategoryCorruptData,
					"Git parent graph omitted task history commit %q",
					current,
				)
			}
			candidate := historyCandidate{
				requestIndex: index,
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

		candidates := make([]historyCandidate, len(newestFirst))
		for i := range newestFirst {
			candidates[len(newestFirst)-1-i] = newestFirst[i]
		}
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
