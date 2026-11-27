package projection

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// Relic history reads a relic's first-parent chain from relic_operations when
// those rows hold the whole chain of the head asked about, and from Git
// otherwise. The rows hold every scalar of each pack and each operation as
// stored, so a chain rebuilt from them is the one Git gives. The versions a
// diff compares are always read from Git by commit.

// relicOperationPackFormat and relicOperationPackVersion are what every relic
// pack this build indexes declares, which a pack rebuilt from rows carries:
// only packs this build can read are rebuilt.
const (
	relicOperationPackFormat  = "workbook.relic-operation-pack"
	relicOperationPackVersion = 1
)

var _ core.RelicHistoryReader = (*relicIndexReader)(nil)

func (s repositorySource) RelicChain(ctx context.Context, config core.ProjectConfig, head core.RelicHead) (core.RelicChain, error) {
	return s.repository.RelicChain(ctx, config, head)
}

func (s repositorySource) ReadRelicVersions(
	ctx context.Context,
	config core.ProjectConfig,
	relicID string,
	commits []string,
) ([]core.RelicVersion, error) {
	return s.repository.ReadRelicVersions(ctx, config, relicID, commits)
}

// RelicChain answers from the relic's operation rows when, after the reader's
// pass, they are complete for head: the relic's row names head, the chain
// indexes run from 0 without a gap, each commit's operations run from 0
// without a gap, and the last row's commit is head. Zero rows say nothing,
// so they too are answered from Git, as is everything when the index cannot
// be used.
func (r *relicIndexReader) RelicChain(ctx context.Context, config core.ProjectConfig, head core.RelicHead) (core.RelicChain, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return core.RelicChain{}, err
	}
	if pass == nil {
		return r.store.relics.RelicChain(ctx, config, head)
	}
	if _, failed := pass.failed[head.RelicID]; !failed {
		var (
			chain    core.RelicChain
			complete bool
		)
		if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
			var err error
			chain, complete, err = r.store.queryRelicChain(ctx, head)
			return err
		}); err != nil {
			if !indexUnusable(ctx) {
				return core.RelicChain{}, err
			}
		} else if complete {
			return chain, nil
		}
	}
	return r.store.relics.RelicChain(ctx, config, head)
}

// ReadRelicVersions reads the versions from Git: the rows hold no part
// objects of any commit but the head.
func (r *relicIndexReader) ReadRelicVersions(
	ctx context.Context,
	config core.ProjectConfig,
	relicID string,
	commits []string,
) ([]core.RelicVersion, error) {
	if err := r.store.validateConfig(config); err != nil {
		return nil, err
	}
	if r.store.relics == nil {
		return nil, errNoRelicSource()
	}
	return r.store.relics.ReadRelicVersions(ctx, config, relicID, commits)
}

// queryRelicChain rebuilds head's chain from its operation rows in one query,
// and reports whether the rows hold all of it. Rows that do not are not an
// error: the chain is then read from Git.
func (s *Store) queryRelicChain(ctx context.Context, head core.RelicHead) (core.RelicChain, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.commit_id, o.chain_index, o.pack_index, o.logical_clock,
		o.history_generation, o.min_reader, o.actor, o.wall_time, o.payload, r.head, r.project_id
		FROM relic_operations o JOIN relics r ON r.relic_id = o.relic_id
		WHERE o.relic_id = ? ORDER BY o.chain_index, o.pack_index`, head.RelicID)
	if err != nil {
		return core.RelicChain{}, false, s.databaseError("query indexed relic operations", err)
	}
	chain := core.RelicChain{RelicID: head.RelicID, Head: head.ObjectID}
	complete := true
	for rows.Next() {
		var row relicOperationRow
		if err := rows.Scan(&row.commit, &row.chainIndex, &row.packIndex, &row.clock, &row.generation, &row.minReader,
			&row.actor, &row.wallTime, &row.payload, &row.head, &row.project); err != nil {
			rows.Close()
			return core.RelicChain{}, false, s.databaseError("read indexed relic operation", err)
		}
		// The rest of the rows are read only to close the query cleanly.
		complete = complete && row.extend(&chain, head)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return core.RelicChain{}, false, s.databaseError("read indexed relic operations", err)
	}
	if !complete || len(chain.Commits) == 0 || chain.Commits[len(chain.Commits)-1].Commit != head.ObjectID {
		return core.RelicChain{}, false, nil
	}
	return chain, true, nil
}

// relicOperationRow is one row of relic_operations, with the relic row's head
// and project beside it.
type relicOperationRow struct {
	commit                       string
	chainIndex, packIndex, clock int64
	generation, actor, wallTime  string
	minReader                    int
	payload, head, project       string
}

// extend adds the row to the chain being rebuilt, in chain and pack order, and
// reports whether the rows still describe a whole chain of head: anything
// else, a gap, a disagreement between the rows of one commit, or a value this
// build cannot read back, leaves the chain to Git.
func (row relicOperationRow) extend(chain *core.RelicChain, head core.RelicHead) bool {
	if row.head != head.ObjectID || row.clock < 0 {
		return false
	}
	var operation core.RelicOperation
	if err := json.Unmarshal([]byte(row.payload), &operation); err != nil {
		return false
	}
	commits := chain.Commits
	if len(commits) > 0 && row.chainIndex == int64(len(commits)-1) {
		// Another operation of the commit being rebuilt.
		last := &commits[len(commits)-1]
		pack := &last.Operation
		if row.commit != last.Commit || row.packIndex != int64(len(pack.Operations)) ||
			uint64(row.clock) != pack.LogicalClock || row.generation != pack.HistoryGeneration ||
			row.minReader != pack.MinReader || row.actor != pack.Actor.ID || row.wallTime != formatTime(pack.WallTime) {
			return false
		}
		pack.Operations = append(pack.Operations, operation)
		return true
	}
	if row.chainIndex != int64(len(commits)) || row.packIndex != 0 {
		return false
	}
	wallTime, err := time.Parse(time.RFC3339Nano, row.wallTime)
	if err != nil {
		return false
	}
	parent := ""
	if len(commits) > 0 {
		parent = commits[len(commits)-1].Commit
	}
	pack := core.RelicOperationPack{
		Format:            relicOperationPackFormat,
		Version:           relicOperationPackVersion,
		MinReader:         row.minReader,
		ProjectID:         row.project,
		RelicID:           head.RelicID,
		HistoryGeneration: row.generation,
		Actor:             core.Actor{ID: row.actor},
		LogicalClock:      uint64(row.clock),
		WallTime:          wallTime,
		Operations:        []core.RelicOperation{operation},
	}
	if pack.RequiresNewerReader() {
		return false
	}
	chain.Commits = append(commits, core.RelicChainCommit{Commit: row.commit, Parent: parent, Operation: pack})
	return true
}
