package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
		logical_clock, history_generation, min_reader, actor, wall_time, type, field, value, task_data, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const selectOperationColumns = `
	SELECT commit_id, chain_index, pack_index, operation_id, logical_clock,
	       history_generation, min_reader, actor, wall_time, type, field, value, task_data, payload
	FROM operations WHERE task_id = ?`

// operationPayload is everything an operation carries beyond the scalar columns
// and the task-creation payload: what a comment says, and what an attachment
// is.
//
// It is cached because a projected chain is replayed, not only displayed. A
// comparison between two commits folds the rows back into packs and applies
// them, so an operation stored without its body would replay as a comment with
// no text — and the fold would refuse it, which turns a cache omission into a
// task whose history cannot be read.
type operationPayload struct {
	CommentID    string               `json:"commentId,omitempty"`
	Body         string               `json:"body,omitempty"`
	Attachment   *core.AttachmentData `json:"attachment,omitempty"`
	AttachmentID string               `json:"attachmentId,omitempty"`
}

func encodeOperationPayload(operation core.Operation) (string, error) {
	if operation.CommentID == "" && operation.Body == "" &&
		operation.Attachment == nil && operation.AttachmentID == "" {
		return "", nil
	}
	encoded, err := json.Marshal(operationPayload{
		CommentID:    operation.CommentID,
		Body:         operation.Body,
		Attachment:   operation.Attachment,
		AttachmentID: operation.AttachmentID,
	})
	if err != nil {
		return "", core.Wrap(core.CategoryCorruptData, "cannot project operation payload", err)
	}
	return string(encoded), nil
}

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
			payload, err := encodeOperationPayload(operation)
			if err != nil {
				return err
			}
			if _, err := transaction.ExecContext(
				ctx,
				insertOperationStatement,
				operation.ID, taskID, commit.ObjectID, firstChainIndex+offset, packIndex,
				int64(pack.LogicalClock), pack.HistoryGeneration, int64(pack.MinReader),
				pack.Actor.ID, formatTime(pack.WallTime),
				string(operation.Type), operation.Field, operation.Value, taskData, payload,
			); err != nil {
				if duplicateOperationID(err) {
					return duplicateOperationError(taskID, operation.ID)
				}
				return cacheError("insert projected task operation", err)
			}
		}
	}
	return nil
}

// duplicateOperationID reports the operations table refusing a second row for
// one operation ULID. That table's only key is operation_id, so a constraint
// this statement violates is always that one, and it means the history handed
// to the projection repeats a ULID the data model promises is unique.
func duplicateOperationID(err error) bool {
	var sqliteError *sqlite.Error
	if !errors.As(err, &sqliteError) {
		return false
	}
	switch sqliteError.Code() {
	case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return true
	}
	return false
}

// duplicateOperationError names the damage rather than the cache. Every other
// insert failure here is a cache fault whose answer is `workbook rebuild`, and
// this one is the exact opposite: the rows are a faithful copy of what Git
// holds, so a rebuild reads the same duplicate and stops in the same place. The
// hint is therefore withheld and the operation is named, because repairing the
// ref is the only thing that helps.
func duplicateOperationError(taskID, operationID string) error {
	return core.Errorf(
		core.CategoryCorruptData,
		"cannot project task %q: operation %q is recorded more than once; "+
			"its task history repeats an operation ID, which no rebuild can resolve",
		taskID,
		operationID,
	)
}

type operationScan struct {
	commit            string
	chainIndex        int64
	packIndex         int64
	operationID       string
	logicalClock      int64
	historyGeneration string
	minReader         int64
	actor             string
	wallTime          string
	operationType     string
	field             string
	value             string
	taskData          string
	payload           string
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
			&row.historyGeneration, &row.minReader, &row.actor, &row.wallTime, &row.operationType,
			&row.field, &row.value, &row.taskData, &row.payload,
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

	// A chain the projection cannot assemble is reported as absent rather than
	// as a failure. The projection is disposable and Git is canonical, so the
	// bounded Git read both answers the question and repairs the rows on the
	// next refresh.
	history, ok := assembleHistory(s.config.ProjectID, taskID, scanned)
	if !ok {
		return core.TaskHistory{}, false, nil
	}
	if throughCommit != "" && history.Entries[len(history.Entries)-1].Commit != throughCommit {
		return core.TaskHistory{}, false, nil
	}
	return history, true, nil
}

// assembleHistory groups operation rows back into the packs they were recorded
// in, reporting false for a chain it cannot use. A chain that skips a position
// is unusable rather than repairable: replaying from a mid-chain state is
// exactly what the projection deliberately does not store.
func assembleHistory(projectID, taskID string, rows []operationScan) (core.TaskHistory, bool) {
	entries := make([]core.HistoryEntry, 0, len(rows))
	expectedChainIndex := int64(0)
	for index := 0; index < len(rows); {
		row := rows[index]
		if row.chainIndex != expectedChainIndex || row.logicalClock < 0 || row.minReader < 0 || row.minReader > math.MaxInt32 {
			return core.TaskHistory{}, false
		}
		operations := make([]core.Operation, 0, 2)
		usable := true
		for ; index < len(rows) && rows[index].chainIndex == row.chainIndex; index++ {
			operation, ok := decodeOperation(rows[index])
			if !ok {
				usable = false
				continue
			}
			operations = append(operations, operation)
		}
		wallTime, err := time.Parse(time.RFC3339Nano, row.wallTime)
		if !usable || err != nil {
			return core.TaskHistory{}, false
		}
		parent := ""
		if len(entries) > 0 {
			parent = entries[len(entries)-1].Commit
		}
		pack := core.NewOperationPack(
			projectID, taskID, row.historyGeneration, row.actor,
			uint64(row.logicalClock), wallTime, operations,
		)
		// The recorded generation is restored from the row rather than left to
		// the constructor's table. The constructor derives the requirement from
		// the operation types it knows, and a pack a newer build wrote may
		// carry types this one does not — for which the derived answer is zero,
		// the one answer that must never be invented. What was stored is what
		// the pack claimed, so that is what is replayed.
		if recorded := int(row.minReader); recorded > pack.MinReader {
			pack.MinReader = recorded
		}
		entries = append(entries, core.HistoryEntry{
			Commit:    row.commit,
			Parent:    parent,
			Operation: pack,
		})
		expectedChainIndex++
	}
	return core.TaskHistory{Entries: entries}, true
}

func decodeOperation(row operationScan) (core.Operation, bool) {
	operation := core.Operation{
		ID:    row.operationID,
		Type:  core.OperationType(row.operationType),
		Field: row.field,
		Value: row.value,
	}
	if row.taskData != "" {
		var task core.TaskData
		if err := json.Unmarshal([]byte(row.taskData), &task); err != nil {
			return core.Operation{}, false
		}
		operation.Task = &task
	}
	if row.payload != "" {
		var payload operationPayload
		if err := json.Unmarshal([]byte(row.payload), &payload); err != nil {
			return core.Operation{}, false
		}
		operation.CommentID = payload.CommentID
		operation.Body = payload.Body
		operation.Attachment = payload.Attachment
		operation.AttachmentID = payload.AttachmentID
	}
	return operation, true
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
