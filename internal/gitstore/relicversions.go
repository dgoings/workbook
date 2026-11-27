package gitstore

import (
	"context"

	"github.com/dgoings/workbook/internal/core"
)

var _ core.RelicHistoryReader = (*Repository)(nil)

// RelicChain reads the relic's whole first-parent chain ending at head, oldest
// first, in one ReadRelicOperations request. Packs are decoded, not checked
// against their parents. A pack that cannot be read ends the chain as its
// truncation, with the commits below it kept; the truncation keeps its
// category only when a newer Workbook wrote the pack.
func (r *Repository) RelicChain(ctx context.Context, config core.ProjectConfig, head core.RelicHead) (core.RelicChain, error) {
	results, err := r.ReadRelicOperations(ctx, config, []RelicHistoryRequest{{Head: head}})
	if err != nil {
		return core.RelicChain{}, err
	}
	if len(results) != 1 {
		return core.RelicChain{}, core.Errorf(core.CategoryCorruptData, "relic operation read returned %d results, want 1", len(results))
	}
	result := results[0]
	chain := core.RelicChain{
		RelicID: head.RelicID,
		Head:    head.ObjectID,
		Commits: make([]core.RelicChainCommit, 0, len(result.Commits)),
	}
	for _, commit := range result.Commits {
		chain.Commits = append(chain.Commits, core.RelicChainCommit{
			Commit: commit.ObjectID, Parent: commit.Parent, Operation: commit.Operation,
		})
	}
	if failure := result.Truncated; failure != nil {
		truncation := &core.HistoryTruncation{Commit: failure.Commit, Message: failure.Err.Error()}
		if core.CategoryOf(failure.Err) == core.CategoryNewerWriter {
			truncation.Category = core.CategoryNewerWriter
		}
		chain.Truncated = truncation
	}
	return chain, nil
}

// ReadRelicVersions reads the relic's commits for a comparison, in the order
// given: each as a snapshot of that commit and the objects its parts are
// stored in. Each commit is read as a tip read reads one, by rules R2 to R4
// and the canonical documents, and is not checked against its parent; Git is
// started at most four times. A commit that cannot be read, or that is not
// one of this relic's, fails the read naming the relic, and a commit a newer
// Workbook wrote fails it as CategoryNewerWriter.
func (r *Repository) ReadRelicVersions(
	ctx context.Context,
	config core.ProjectConfig,
	relicID string,
	commits []string,
) ([]core.RelicVersion, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return nil, err
	}
	if err := r.validateRepositoryConfig(config); err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return []core.RelicVersion{}, nil
	}
	heads := make([]core.RelicHead, len(commits))
	for index, commit := range commits {
		heads[index] = core.RelicHead{RelicID: relicID, ObjectID: commit}
	}
	read, errs := r.readRelicCommitsPartial(ctx, config, heads)
	versions := make([]core.RelicVersion, len(commits))
	for index, commit := range read {
		if errs[index] != nil {
			return nil, namedRelicError(relicID, errs[index])
		}
		if commit.needsNewerReader() {
			return nil, core.Errorf(core.CategoryNewerWriter,
				"relic %s: commit %s was written by a newer workbook; upgrade workbook to read it", relicID, commits[index])
		}
		parts := make(map[string]core.RelicPartObjectIDs, len(commit.Parts))
		for partID, objects := range commit.Parts {
			parts[partID] = core.RelicPartObjectIDs{Fields: objects.Fields, Body: objects.Body}
		}
		versions[index] = core.RelicVersion{
			Snapshot: core.RelicSnapshot{Head: commit.ObjectID, Operation: commit.Operation, Record: commit.Record},
			Parts:    parts,
		}
	}
	return versions, nil
}
