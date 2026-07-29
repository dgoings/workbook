package gitstore

import (
	"bytes"
	"context"
	"fmt"

	"github.com/dgoings/workbook/internal/core"
)

type taskHeadPair struct {
	TaskID string
	Local  core.Snapshot
	Remote core.Snapshot
}

type taskHeadRelationship string

const (
	taskHeadsEqual       taskHeadRelationship = "equal"
	taskHeadsLocalAhead  taskHeadRelationship = "local-ahead"
	taskHeadsRemoteAhead taskHeadRelationship = "remote-ahead"
	taskHeadsDiverged    taskHeadRelationship = "diverged"
)

type taskHeadRelationshipResult struct {
	TaskID       string
	Relationship taskHeadRelationship
}

type canonicalRefUpdate struct {
	TaskID   string
	Next     string
	Expected string
}

// classifyTaskHeadRelationships classifies validated canonical and tracking
// snapshots with at most one Git ancestry walk.
func (r *Repository) classifyTaskHeadRelationships(
	ctx context.Context,
	config core.ProjectConfig,
	pairs []taskHeadPair,
) ([]taskHeadRelationshipResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return nil, err
	}

	results := make([]taskHeadRelationshipResult, len(pairs))
	unequal := make([]taskHeadPair, 0, len(pairs))
	seenTaskIDs := make(map[string]struct{}, len(pairs))
	for i, pair := range pairs {
		if _, duplicate := seenTaskIDs[pair.TaskID]; duplicate {
			return nil, core.Errorf(core.CategoryCorruptData, "task head pairs contain duplicate task ID %q", pair.TaskID)
		}
		seenTaskIDs[pair.TaskID] = struct{}{}
		if err := r.validateClassifiableTaskHead(config, pair.TaskID, pair.Local); err != nil {
			return nil, err
		}
		if err := r.validateClassifiableTaskHead(config, pair.TaskID, pair.Remote); err != nil {
			return nil, err
		}
		if pair.Local.Operation.HistoryGeneration != pair.Remote.Operation.HistoryGeneration {
			return nil, core.Errorf(core.CategoryCorruptData, "task %q heads use different history generations", pair.TaskID)
		}

		results[i].TaskID = pair.TaskID
		if pair.Local.Head == pair.Remote.Head {
			results[i].Relationship = taskHeadsEqual
			continue
		}
		unequal = append(unequal, pair)
	}
	if len(unequal) == 0 {
		return results, nil
	}

	var input bytes.Buffer
	seenHeads := make(map[string]struct{}, len(unequal)*2)
	for _, pair := range unequal {
		for _, head := range []string{pair.Local.Head, pair.Remote.Head} {
			if _, seen := seenHeads[head]; seen {
				continue
			}
			seenHeads[head] = struct{}{}
			fmt.Fprintln(&input, head)
		}
	}
	output, err := r.Git(ctx, input.Bytes(), "rev-list", "--parents", "--stdin")
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "cannot classify task head relationships", err)
	}
	graph, err := parseParentGraph(output)
	if err != nil {
		return nil, err
	}
	for objectID, parents := range graph {
		if err := r.validateFullObjectID(objectID); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
		}
		for _, parent := range parents {
			if err := r.validateFullObjectID(parent); err != nil {
				return nil, core.Wrap(core.CategoryCorruptData, "Git returned an invalid parent graph object ID", err)
			}
		}
	}

	for i, pair := range pairs {
		if results[i].Relationship == taskHeadsEqual {
			continue
		}
		if _, found := graph[pair.Local.Head]; !found {
			return nil, core.Errorf(core.CategoryCorruptData, "Git parent graph omitted local head for task %q", pair.TaskID)
		}
		if _, found := graph[pair.Remote.Head]; !found {
			return nil, core.Errorf(core.CategoryCorruptData, "Git parent graph omitted remote head for task %q", pair.TaskID)
		}
		switch {
		case graphReaches(graph, pair.Remote.Head, pair.Local.Head):
			results[i].Relationship = taskHeadsRemoteAhead
		case graphReaches(graph, pair.Local.Head, pair.Remote.Head):
			results[i].Relationship = taskHeadsLocalAhead
		default:
			results[i].Relationship = taskHeadsDiverged
		}
	}
	return results, nil
}

func (r *Repository) validateClassifiableTaskHead(config core.ProjectConfig, taskID string, snapshot core.Snapshot) error {
	if err := core.ValidateTaskID(config.Key, taskID); err != nil {
		return core.Wrap(core.CategoryCorruptData, "task head ID is invalid", err)
	}
	if err := r.validateFullObjectID(snapshot.Head); err != nil {
		return core.Wrap(core.CategoryCorruptData, "task head object ID is invalid", err)
	}
	if err := validateTipIdentity(config, taskID, snapshot.Operation, snapshot.State); err != nil {
		return err
	}
	return nil
}

// updateCanonicalRefs atomically creates and compare-and-swaps canonical task
// refs. A transaction failure leaves every requested change unapplied.
func (r *Repository) updateCanonicalRefs(
	ctx context.Context,
	config core.ProjectConfig,
	updates []canonicalRefUpdate,
) error {
	if err := r.verifyIdentity(ctx); err != nil {
		return err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	if err := r.ensureGitObjectIDWidth(ctx); err != nil {
		return err
	}

	seenTaskIDs := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		if _, duplicate := seenTaskIDs[update.TaskID]; duplicate {
			return core.Errorf(core.CategoryCorruptData, "canonical ref updates contain duplicate task ID %q", update.TaskID)
		}
		seenTaskIDs[update.TaskID] = struct{}{}
		if err := core.ValidateTaskID(config.Key, update.TaskID); err != nil {
			return core.Wrap(core.CategoryCorruptData, "canonical task ref ID is invalid", err)
		}
		if err := r.validateFullObjectID(update.Next); err != nil {
			return core.Wrap(core.CategoryCorruptData, "canonical task ref target is invalid", err)
		}
		if update.Expected != "" {
			if err := r.validateFullObjectID(update.Expected); err != nil {
				return core.Wrap(core.CategoryCorruptData, "canonical task ref expected target is invalid", err)
			}
		}
	}

	var input bytes.Buffer
	input.WriteString("start\noption no-deref\n")
	for _, update := range updates {
		ref := taskRefPrefix + update.TaskID
		if update.Expected == "" {
			fmt.Fprintf(&input, "create %s %s\n", ref, update.Next)
			continue
		}
		fmt.Fprintf(&input, "update %s %s %s\n", ref, update.Next, update.Expected)
	}
	input.WriteString("prepare\ncommit\n")
	if _, err := r.Git(
		ctx,
		input.Bytes(),
		"update-ref",
		"--no-deref",
		"--create-reflog",
		"-m",
		"workbook: fetch origin",
		"--stdin",
	); err != nil {
		return core.Wrap(core.CategoryStaleWrite, "canonical task refs changed during fetch", err)
	}
	return nil
}
