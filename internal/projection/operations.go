package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// Rows are keyed on the operation ULID rather than the commit object ID.
// Replay preserves operation ULIDs and rewrites logical clocks, which rewrites
// every downstream object ID, so a ULID-keyed row that survives a
// reconciliation changes only its ordering and object-ID columns.
//
// Surviving is not guaranteed, so the refresh cannot be upsert-only. Replay
// drops operations three ways, and each removes a ULID from canonical history
// for good: a pack whose effect the fetched history already contains is
// skipped, a pack that applies but leaves no operator-visible change records no
// commit, and a conflict stops replay so every pack from that one onward is
// dropped. Upserting alone would strand those rows, and because state is
// reconstructed by replaying from the root, a stranded row breaks the
// logical-clock chain rather than merely showing a stale entry.

const insertOperationStatement = `
	INSERT INTO operations (
		operation_id, task_id, commit_id, chain_index, pack_index,
		logical_clock, history_generation, actor, wall_time, type, field, value, task_data
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const selectOperationColumns = `
	SELECT commit_id, chain_index, pack_index, operation_id, logical_clock,
	       history_generation, actor, wall_time, type, field, value, task_data
	FROM operations WHERE task_id = ?`

// replaceOperations rewrites one task's whole chain. A refresh that restarted
// at the root is the reconciliation signal, and its rows must replace rather
// than merge with whatever the replay left behind.
func replaceOperations(ctx context.Context, transaction *sql.Tx, taskID string, commits []gitstore.OperationCommit) error {
	if err := deleteOperations(ctx, transaction, taskID); err != nil {
		return err
	}
	return insertOperations(ctx, transaction, taskID, 0, commits)
}

// appendOperations extends a chain whose projected tail is still parent. Any
// other tail means the projection and Git disagree about this task's shape, so
// the rows are dropped and the bounded Git read answers instead of a chain with
// a hole in it.
func appendOperations(
	ctx context.Context,
	transaction *sql.Tx,
	taskID string,
	parent string,
	commits []gitstore.OperationCommit,
) error {
	tail, tailIndex, found, err := chainTail(ctx, transaction, taskID)
	if err != nil {
		return err
	}
	switch {
	case !found && parent == "":
		return insertOperations(ctx, transaction, taskID, 0, commits)
	case found && tail == parent:
		return insertOperations(ctx, transaction, taskID, tailIndex+1, commits)
	default:
		return deleteOperations(ctx, transaction, taskID)
	}
}

func deleteOperations(ctx context.Context, transaction *sql.Tx, taskID string) error {
	if _, err := transaction.ExecContext(ctx, `DELETE FROM operations WHERE task_id = ?`, taskID); err != nil {
		return cacheError("remove projected task operations", err)
	}
	return nil
}

func chainTail(ctx context.Context, transaction *sql.Tx, taskID string) (string, int, bool, error) {
	var commit string
	var chainIndex int64
	err := transaction.QueryRowContext(
		ctx,
		`SELECT commit_id, chain_index FROM operations WHERE task_id = ? ORDER BY chain_index DESC LIMIT 1`,
		taskID,
	).Scan(&commit, &chainIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, cacheError("read projected operation chain tail", err)
	}
	if chainIndex < 0 || chainIndex > math.MaxInt32 {
		return "", 0, false, core.Errorf(core.CategoryCorruptData, "projected task %q has an invalid operation chain index", taskID)
	}
	return commit, int(chainIndex), true, nil
}

func insertOperations(
	ctx context.Context,
	transaction *sql.Tx,
	taskID string,
	firstChainIndex int,
	commits []gitstore.OperationCommit,
) error {
	for offset, commit := range commits {
		pack := commit.Operation
		if pack.TaskID != taskID {
			return core.Errorf(core.CategoryCorruptData, "cannot project commit %q under task %q", commit.ObjectID, taskID)
		}
		if pack.LogicalClock > math.MaxInt64 {
			return core.Errorf(core.CategoryCorruptData, "commit %q has an unprojectable logical clock", commit.ObjectID)
		}
		for packIndex, operation := range pack.Operations {
			taskData := ""
			if operation.Task != nil {
				encoded, err := json.Marshal(operation.Task)
				if err != nil {
					return core.Wrap(core.CategoryCorruptData, "cannot project task creation payload", err)
				}
				taskData = string(encoded)
			}
			if _, err := transaction.ExecContext(
				ctx,
				insertOperationStatement,
				operation.ID, taskID, commit.ObjectID, firstChainIndex+offset, packIndex,
				int64(pack.LogicalClock), pack.HistoryGeneration, pack.Actor.ID, formatTime(pack.WallTime),
				string(operation.Type), operation.Field, operation.Value, taskData,
			); err != nil {
				return cacheError("insert projected task operation", err)
			}
		}
	}
	return nil
}

type operationScan struct {
	commit            string
	chainIndex        int64
	packIndex         int64
	operationID       string
	logicalClock      int64
	historyGeneration string
	actor             string
	wallTime          string
	operationType     string
	field             string
	value             string
	taskData          string
}

// readProjectedHistory rebuilds one task's chain from projected rows. It
// reports found=false when the projection holds no usable chain for the task,
// which is the caller's signal to fall back to a bounded Git read.
func (s *Store) readProjectedHistory(ctx context.Context, taskID, throughCommit string) (core.TaskHistory, bool, error) {
	query := selectOperationColumns + ` ORDER BY chain_index, pack_index`
	arguments := []any{taskID}
	if throughCommit != "" {
		query = selectOperationColumns + `
			AND chain_index <= (SELECT MIN(chain_index) FROM operations WHERE task_id = ? AND commit_id = ?)
			ORDER BY chain_index, pack_index`
		arguments = append(arguments, taskID, throughCommit)
	}

	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return core.TaskHistory{}, false, s.databaseError("query projected task operations", err)
	}
	defer rows.Close()

	scanned := make([]operationScan, 0, 16)
	for rows.Next() {
		var row operationScan
		if err := rows.Scan(
			&row.commit, &row.chainIndex, &row.packIndex, &row.operationID, &row.logicalClock,
			&row.historyGeneration, &row.actor, &row.wallTime, &row.operationType,
			&row.field, &row.value, &row.taskData,
		); err != nil {
			return core.TaskHistory{}, false, s.databaseError("read projected task operation", err)
		}
		scanned = append(scanned, row)
	}
	if err := rows.Err(); err != nil {
		return core.TaskHistory{}, false, s.databaseError("read projected task operations", err)
	}
	if len(scanned) == 0 {
		return core.TaskHistory{}, false, nil
	}

	history, err := assembleHistory(s.config.ProjectID, taskID, scanned)
	if err != nil {
		return core.TaskHistory{}, false, err
	}
	if throughCommit != "" && history.Entries[len(history.Entries)-1].Commit != throughCommit {
		return core.TaskHistory{}, false, nil
	}
	return history, true, nil
}

// assembleHistory groups operation rows back into the packs they were recorded
// in. A chain that does not start at the root is treated as unusable rather
// than repaired, because replaying from a mid-chain state is exactly the thing
// the projection does not store.
func assembleHistory(projectID, taskID string, rows []operationScan) (core.TaskHistory, error) {
	entries := make([]core.HistoryEntry, 0, len(rows))
	expectedChainIndex := int64(0)
	for index := 0; index < len(rows); {
		row := rows[index]
		if row.chainIndex != expectedChainIndex {
			return core.TaskHistory{}, core.Errorf(
				core.CategoryCorruptData,
				"projected task %q operation chain skips position %d",
				taskID,
				expectedChainIndex,
			)
		}
		operations := make([]core.Operation, 0, 2)
		for ; index < len(rows) && rows[index].chainIndex == row.chainIndex; index++ {
			operation, err := decodeOperation(rows[index])
			if err != nil {
				return core.TaskHistory{}, err
			}
			operations = append(operations, operation)
		}
		wallTime, err := parseProjectedTime(row.wallTime)
		if err != nil {
			return core.TaskHistory{}, err
		}
		if row.logicalClock < 0 {
			return core.TaskHistory{}, core.Errorf(core.CategoryCorruptData, "projected operation has an invalid logical clock")
		}
		parent := ""
		if len(entries) > 0 {
			parent = entries[len(entries)-1].Commit
		}
		entries = append(entries, core.HistoryEntry{
			Commit: row.commit,
			Parent: parent,
			Operation: core.NewOperationPack(
				projectID, taskID, row.historyGeneration, row.actor,
				uint64(row.logicalClock), wallTime, operations,
			),
		})
		expectedChainIndex++
	}
	return core.TaskHistory{Entries: entries}, nil
}

func decodeOperation(row operationScan) (core.Operation, error) {
	operation := core.Operation{
		ID:    row.operationID,
		Type:  core.OperationType(row.operationType),
		Field: row.field,
		Value: row.value,
	}
	if row.taskData != "" {
		var task core.TaskData
		if err := json.Unmarshal([]byte(row.taskData), &task); err != nil {
			return core.Operation{}, core.Wrap(core.CategoryCorruptData, "cannot decode projected task creation payload", err)
		}
		operation.Task = &task
	}
	return operation, nil
}

// gitHistory reads one chain straight from Git. It is the bounded fallback for
// a commit the projection does not hold, which includes every parked
// pre-replay tip: the projection is fed from refs/workbook/tasks/ only, so such
// a commit has no row even when its task is fully projected.
func (s *Store) gitHistory(ctx context.Context, taskID, commit string) (core.TaskHistory, error) {
	results, err := s.source.ReadTaskOperations(ctx, s.config, []gitstore.TaskHistoryRequest{
		{Head: gitstore.TaskHead{TaskID: taskID, ObjectID: commit}},
	})
	if err != nil {
		return core.TaskHistory{}, err
	}
	if len(results) != 1 {
		return core.TaskHistory{}, core.Errorf(core.CategoryCorruptData, "task operation read returned %d results, want 1", len(results))
	}
	result := results[0]
	history := core.TaskHistory{Entries: make([]core.HistoryEntry, 0, len(result.Commits))}
	for _, recorded := range result.Commits {
		history.Entries = append(history.Entries, core.HistoryEntry{
			Commit:    recorded.ObjectID,
			Parent:    recorded.Parent,
			Operation: recorded.Operation,
		})
	}
	if result.Truncated != nil {
		if len(history.Entries) == 0 {
			return core.TaskHistory{}, notFoundCommit(commit, result.Truncated.Err)
		}
		history.Truncated = &core.HistoryTruncation{
			Commit:  result.Truncated.Commit,
			Message: result.Truncated.Err.Error(),
		}
	}
	if len(history.Entries) == 0 {
		return core.TaskHistory{}, notFoundCommit(commit, nil)
	}
	return history, nil
}

// notFoundCommit reports a named commit this clone cannot read as an ordinary
// not-found for that argument rather than as corruption. Reconciliation parks
// pre-replay tips under refs/workbook/reconciled/, keeps at most a few per
// task, and retires the excess inside a later mutation, after which the oldest
// pre-replay chain is unreferenced and collectable.
func notFoundCommit(commit string, cause error) error {
	message := "commit " + commit + " was not found; it is no longer reachable in this clone, " +
		"which is what happens to a pre-replay tip once reconciliation retires it"
	if cause == nil {
		return core.Errorf(core.CategoryNotFound, "%s", message)
	}
	return core.Wrap(core.CategoryNotFound, message, cause)
}
