package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// Relics are projected beside tasks, into four tables of their own:
//
//	relics            one row per relic: its state's scalars and its head
//	relic_properties  one row per property, its value as canonical JSON
//	relic_parts       one row per part, live or removed, in state order, with
//	                  its fields.json exactly as stored and the object ID,
//	                  format, version and size of its body
//	relic_operations  one row per recorded operation along the first-parent
//	                  chain, its pack's scalars beside it
//
// Body text is never copied in. A body is read from Git by the object ID its
// row holds, once per body shown. Every row is derived from a relic ref, and a
// relic the index cannot hold, damaged or written by a newer Workbook, gets no
// row at all and is read from Git on every read.
//
// Only relic reads and `workbook rebuild` read relic refs. A task path, the
// automatic rebuild included, never does, so after a cache loss the relic
// tables are empty until the first relic read fills them.

// relicSource is what the projection reads relics from: the reads a relic
// service makes, for the relics the index cannot answer, and the three batch
// readers a relic pass indexes with.
type relicSource interface {
	core.RelicReader
	core.RelicHistoryReader
	core.RelicSchemaStore
	ReadIndexedRelics(context.Context, core.ProjectConfig, []gitstore.RelicIndexRequest) ([]gitstore.IndexedRelic, []error)
	ReadRelicOperations(context.Context, core.ProjectConfig, []gitstore.RelicHistoryRequest) ([]gitstore.RelicOperationsResult, error)
	ReadRelicBodies(context.Context, core.ProjectConfig, []gitstore.RelicBodyRequest) ([][]byte, error)
}

func (s repositorySource) ListRelicHeads(ctx context.Context, config core.ProjectConfig) ([]core.RelicHead, error) {
	return s.repository.ListRelicHeads(ctx, config)
}

func (s repositorySource) ReadRelicTips(ctx context.Context, config core.ProjectConfig, heads []core.RelicHead) ([]core.RelicSnapshot, error) {
	return s.repository.ReadRelicTips(ctx, config, heads)
}

func (s repositorySource) GetRelic(ctx context.Context, config core.ProjectConfig, relicID string) (core.RelicSnapshot, error) {
	return s.repository.GetRelic(ctx, config, relicID)
}

func (s repositorySource) ReadRelicParts(
	ctx context.Context,
	config core.ProjectConfig,
	snapshot core.RelicSnapshot,
	partIDs []string,
	bodies bool,
) (map[string]core.RelicPartContent, error) {
	return s.repository.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
}

func (s repositorySource) RelicSchemaListing(ctx context.Context, config core.ProjectConfig) (core.RelicSchemaListing, error) {
	return s.repository.RelicSchemaListing(ctx, config)
}

func (s repositorySource) ReadRelicSchema(
	ctx context.Context,
	config core.ProjectConfig,
	version core.RelicSchemaVersion,
) (core.RelicSchemaDocument, *core.RelicSchema, error) {
	return s.repository.ReadRelicSchema(ctx, config, version)
}

func (s repositorySource) ReadIndexedRelics(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.RelicIndexRequest,
) ([]gitstore.IndexedRelic, []error) {
	return s.repository.ReadIndexedRelics(ctx, config, requests)
}

func (s repositorySource) ReadRelicOperations(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.RelicHistoryRequest,
) ([]gitstore.RelicOperationsResult, error) {
	return s.repository.ReadRelicOperations(ctx, config, requests)
}

func (s repositorySource) ReadRelicBodies(
	ctx context.Context,
	config core.ProjectConfig,
	requests []gitstore.RelicBodyRequest,
) ([][]byte, error) {
	return s.repository.ReadRelicBodies(ctx, config, requests)
}

// relicSchemaChecks name every column of every relic table, for
// requiredSchemaExists.
var relicSchemaChecks = [4]string{
	`SELECT relic_id, head, project_id, state_version, min_reader, history_generation, compacted_from, logical_clock, title, type, schema, author, retired, deleted, created_at, updated_at, part_count, link_count, conformance, schema_problem, compacted_run, compacted_at, compacted_boundary, compacted_since FROM relics LIMIT 0`,
	`SELECT relic_id, name, value FROM relic_properties LIMIT 0`,
	`SELECT relic_id, part_id, ordinal, type, position, removed, fields_object, fields, body_object, body_format, body_version, body_bytes, dropped_removed_at, dropped_at FROM relic_parts LIMIT 0`,
	`SELECT relic_id, commit_id, chain_index, pack_index, operation_id, logical_clock, history_generation, min_reader, actor, wall_time, type, part, payload FROM relic_operations LIMIT 0`,
}

func errNoRelicSource() error {
	return core.Errorf(core.CategoryOperational, "projection has no relic source")
}

// relicPass is what one relic pass saw: every relic head it listed, the Git
// read of each changed relic whose row another writer moved first, which the
// pass left alone, and the refusal of each relic it could not index.
type relicPass struct {
	heads   []core.RelicHead
	fromGit map[string]gitstore.IndexedRelic
	failed  map[string]error
}

// RebuildCounts is what RebuildWithRelics indexed.
type RebuildCounts struct {
	Tasks, Relics int
}

// RebuildWithRelics is Rebuild with every relic indexed into the same new
// cache before it is installed. Relics counts the relics indexed, not the ones
// the index refused.
func (s *Store) RebuildWithRelics(ctx context.Context) (RebuildCounts, error) {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	if s.closed {
		return RebuildCounts{}, errStoreClosed()
	}
	if s.relics == nil {
		return RebuildCounts{}, errNoRelicSource()
	}
	tasks, err := s.rebuildLocked(ctx, true)
	if err != nil {
		return RebuildCounts{}, err
	}
	// The cache in place may be one another process installed instead, which
	// can hold no relics yet. A pass brings it to the refs; on this build's own
	// cache it only lists them.
	if _, err := s.indexRelics(ctx, s.db, true); err != nil {
		return RebuildCounts{}, err
	}
	var relics int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relics`).Scan(&relics); err != nil {
		return RebuildCounts{}, s.databaseError("count indexed relics", err)
	}
	return RebuildCounts{Tasks: tasks, Relics: relics}, nil
}

// refreshRelicsActive brings the active cache's relic rows up to the relic
// refs, with rebuildMu read-held and the cache validated, judging each relic
// it indexes.
func (s *Store) refreshRelicsActive(ctx context.Context) (relicPass, error) {
	return s.refreshRelics(ctx, true)
}

// RefreshRelics brings the relic rows up to the relic refs with one judging
// pass, the pass a relic read runs, and returns its error. A relic compaction
// calls it once after its refs move: a relic whose head changed is read again
// and its rows replaced, its operation rows from the new root when the chain
// no longer reaches the head the rows ended at. Unlike a read, it does not
// fall back to Git when the index cannot be used; it reports why.
func (s *Store) RefreshRelics(ctx context.Context) error {
	return s.withActiveDatabase(ctx, func(ctx context.Context) error {
		_, err := s.refreshRelicsActive(ctx)
		return err
	})
}

// refreshRelics is refreshRelicsActive, judging only when judging is set. A
// pass that does not judge reads no schema listing and no schema; it writes
// a changed relic that names a schema as not yet judged, for the next judging
// pass to judge.
func (s *Store) refreshRelics(ctx context.Context, judging bool) (relicPass, error) {
	if s.relics == nil {
		return relicPass{}, errNoRelicSource()
	}
	return s.indexRelics(ctx, s.db, judging)
}

// cachedRelic is one relic row as a pass first reads it.
type cachedRelic struct {
	head                string
	partCount, partRows int
	linkCount, linkRows int
	// schema is the version the row's relic names, empty for none, and
	// conformance the judgment the row stores, empty when not yet judged.
	schema, conformance string
}

// changed reports whether the row cannot answer for head: it names another
// commit, or it is missing part or link rows, which only a write cut short or
// a damaged cache leaves.
func (row cachedRelic) changed(head string) bool {
	return row.head != head || row.partCount != row.partRows || row.linkCount != row.linkRows
}

// judgedStands reports whether the row's judgment can stand under listing: an
// open relic always can; a relic naming a version needs a judgment, and one
// judged unknown exactly when the listing lacks the version. A version, once
// listed, never changes, so any other judgment against it stands. A row that
// cannot stand is indexed again, which judges it.
func (row cachedRelic) judgedStands(listing core.RelicSchemaListing) bool {
	if row.schema == "" {
		return true
	}
	if row.conformance == "" || row.conformance == core.RelicConformanceOpen {
		return false
	}
	_, known := listing.ByID(row.schema)
	return known == (row.conformance != core.RelicConformanceUnknownSchema)
}

// indexRelics runs one relic pass against db, modeled on the task refresh.
//
// It reads the rows' heads and part counts, lists the relic refs once, and
// reads from Git only the relics whose row is missing, names another head, or
// is half-written, with the fields objects their old rows hold given as
// known. Then one transaction writes each of them with a compare-and-swap on
// the head its row had when the pass began, so a row another writer moved in
// between is left as that writer left it and the pass's own Git read goes into
// fromGit for the caller to answer from. A relic Git refuses goes into failed,
// and any row it had is deleted under the same compare-and-swap, so a verdict
// is never cached. A row whose ref is gone is deleted.
func (s *Store) indexRelics(ctx context.Context, db *sql.DB, judging bool) (relicPass, error) {
	cached, err := cachedRelicRows(ctx, db)
	if err != nil {
		return relicPass{}, err
	}
	heads, err := s.relics.ListRelicHeads(ctx, s.config)
	if err != nil {
		return relicPass{}, err
	}
	judge := &relicJudge{store: s, judging: judging}
	pass := relicPass{heads: heads}
	listed := make(map[string]struct{}, len(heads))
	var changed []core.RelicHead
	for _, head := range heads {
		listed[head.RelicID] = struct{}{}
		row, found := cached[head.RelicID]
		if !found || row.changed(head.ObjectID) {
			changed = append(changed, head)
			continue
		}
		stands, err := judge.rowStands(ctx, row)
		if err != nil {
			return relicPass{}, err
		}
		if !stands {
			changed = append(changed, head)
		}
	}
	var gone []string
	for relicID := range cached {
		if _, found := listed[relicID]; !found {
			gone = append(gone, relicID)
		}
	}
	sort.Strings(gone)
	if len(changed) == 0 && len(gone) == 0 {
		return pass, nil
	}

	previous, err := previousRelicRows(ctx, db, changed, cached)
	if err != nil {
		return relicPass{}, err
	}
	requests := make([]gitstore.RelicIndexRequest, len(changed))
	for index, head := range changed {
		requests[index] = gitstore.RelicIndexRequest{Head: head, KnownFields: previous[head.RelicID].knownFields()}
	}
	indexed := map[string]gitstore.IndexedRelic{}
	var verified []core.RelicHead
	if len(requests) > 0 {
		relics, errs := s.relics.ReadIndexedRelics(ctx, s.config, requests)
		if len(relics) != len(requests) || len(errs) != len(requests) {
			return relicPass{}, core.Errorf(core.CategoryCorruptData,
				"relic index read returned %d relics and %d errors, want %d", len(relics), len(errs), len(requests))
		}
		for index, head := range changed {
			if err := errs[index]; err != nil {
				// A read that never reached the relic says nothing about it,
				// so it fails the pass rather than condemn the relic.
				switch core.CategoryOf(err) {
				case "", core.CategoryOperational, core.CategoryStaleWrite:
					return relicPass{}, err
				}
				if pass.failed == nil {
					pass.failed = map[string]error{}
				}
				pass.failed[head.RelicID] = err
				continue
			}
			relic, err := completeIndexedRelic(head, relics[index], previous[head.RelicID].fields)
			if err != nil {
				return relicPass{}, err
			}
			conformance, err := judge.judge(ctx, relic)
			if err != nil {
				switch core.CategoryOf(err) {
				case "", core.CategoryOperational, core.CategoryStaleWrite:
					return relicPass{}, err
				}
				if pass.failed == nil {
					pass.failed = map[string]error{}
				}
				pass.failed[head.RelicID] = err
				continue
			}
			if conformance.State != "" {
				relic.Snapshot.Conformance = &conformance
			}
			indexed[head.RelicID] = relic
			verified = append(verified, head)
		}
	}

	chains := map[string]gitstore.RelicOperationsResult{}
	if len(verified) > 0 {
		historyRequests := make([]gitstore.RelicHistoryRequest, len(verified))
		for index, head := range verified {
			historyRequests[index] = gitstore.RelicHistoryRequest{Head: head, StopAt: previous[head.RelicID].stopAt}
		}
		results, err := s.relics.ReadRelicOperations(ctx, s.config, historyRequests)
		if err != nil {
			return relicPass{}, err
		}
		if len(results) != len(historyRequests) {
			return relicPass{}, core.Errorf(core.CategoryCorruptData,
				"relic operation read returned %d chains, want %d", len(results), len(historyRequests))
		}
		for index, result := range results {
			if result.RelicID != verified[index].RelicID || result.Head != verified[index].ObjectID {
				return relicPass{}, core.Errorf(core.CategoryCorruptData,
					"relic operation read returned relic %s at %s, want %s at %s",
					result.RelicID, result.Head, verified[index].RelicID, verified[index].ObjectID)
			}
			chains[result.RelicID] = result
		}
	}

	if s.beforeRelicWrite != nil {
		s.beforeRelicWrite()
	}
	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return relicPass{}, s.databaseError("begin relic index refresh", err)
	}
	defer transaction.Rollback()
	writer, err := prepareRelicWriter(ctx, transaction)
	if err != nil {
		return relicPass{}, err
	}
	defer writer.close()
	for _, head := range changed {
		relicID := head.RelicID
		actual, err := writer.head(ctx, relicID)
		if err != nil {
			return relicPass{}, err
		}
		expected := cached[relicID].head
		relic, ok := indexed[relicID]
		if !ok {
			if actual == expected {
				if err := writer.deleteRelic(ctx, relicID); err != nil {
					return relicPass{}, err
				}
			}
			continue
		}
		if actual != expected {
			if pass.fromGit == nil {
				pass.fromGit = map[string]gitstore.IndexedRelic{}
			}
			pass.fromGit[relicID] = relic
			continue
		}
		if err := writer.writeRelic(ctx, relic, chains[relicID]); err != nil {
			return relicPass{}, err
		}
	}
	for _, relicID := range gone {
		actual, err := writer.head(ctx, relicID)
		if err != nil {
			return relicPass{}, err
		}
		if actual == cached[relicID].head {
			if err := writer.deleteRelic(ctx, relicID); err != nil {
				return relicPass{}, err
			}
		}
	}
	if s.beforeRelicCommit != nil {
		if err := s.beforeRelicCommit(); err != nil {
			return relicPass{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return relicPass{}, s.databaseError("commit relic index refresh", err)
	}
	return pass, nil
}

// cachedRelicRows reads every relic row's head, part count and link count, and
// how many part and link rows each has.
func cachedRelicRows(ctx context.Context, db *sql.DB) (map[string]cachedRelic, error) {
	cached := map[string]cachedRelic{}
	rows, err := db.QueryContext(ctx, `SELECT relic_id, head, part_count, link_count, schema, conformance FROM relics`)
	if err != nil {
		return nil, cacheError("query indexed relic heads", err)
	}
	for rows.Next() {
		var relicID string
		var row cachedRelic
		if err := rows.Scan(&relicID, &row.head, &row.partCount, &row.linkCount, &row.schema, &row.conformance); err != nil {
			rows.Close()
			return nil, cacheError("read indexed relic head", err)
		}
		cached[relicID] = row
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, cacheError("read indexed relic heads", err)
	}

	// One query counts the part rows and the link rows of every relic.
	counts, err := db.QueryContext(ctx, `SELECT relic_id, 0, COUNT(*) FROM relic_parts GROUP BY relic_id
		UNION ALL SELECT relic_id, 1, COUNT(*) FROM relic_links GROUP BY relic_id`)
	if err != nil {
		return nil, cacheError("count indexed relic parts and links", err)
	}
	defer counts.Close()
	for counts.Next() {
		var relicID string
		var links bool
		var count int
		if err := counts.Scan(&relicID, &links, &count); err != nil {
			return nil, cacheError("read indexed relic part or link count", err)
		}
		if row, found := cached[relicID]; found {
			if links {
				row.linkRows = count
			} else {
				row.partRows = count
			}
			cached[relicID] = row
		}
	}
	if err := counts.Err(); err != nil {
		return nil, cacheError("read indexed relic part and link counts", err)
	}
	return cached, nil
}

// previousRelic is what a changed relic's old rows give its re-read: the
// fields bytes they hold by object ID, and where its operation rows end.
type previousRelic struct {
	fields map[string][]byte
	// stopAt is the row's head when the operation rows end at that commit, so
	// the chain is read from there. Otherwise it is empty and the whole chain
	// is read and replaces the rows.
	stopAt string
}

func (previous previousRelic) knownFields() map[string]struct{} {
	if len(previous.fields) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(previous.fields))
	for objectID := range previous.fields {
		known[objectID] = struct{}{}
	}
	return known
}

func previousRelicRows(
	ctx context.Context,
	db *sql.DB,
	changed []core.RelicHead,
	cached map[string]cachedRelic,
) (map[string]previousRelic, error) {
	previous := map[string]previousRelic{}
	var fieldsQuery, tailQuery *sql.Stmt
	defer func() {
		for _, statement := range []*sql.Stmt{fieldsQuery, tailQuery} {
			if statement != nil {
				_ = statement.Close()
			}
		}
	}()
	for _, head := range changed {
		row, found := cached[head.RelicID]
		if !found {
			continue
		}
		if fieldsQuery == nil {
			var err error
			if fieldsQuery, err = db.PrepareContext(ctx, `SELECT fields_object, fields FROM relic_parts WHERE relic_id = ?`); err != nil {
				return nil, cacheError("prepare indexed relic fields query", err)
			}
			if tailQuery, err = db.PrepareContext(ctx,
				`SELECT commit_id FROM relic_operations WHERE relic_id = ? ORDER BY chain_index DESC, pack_index DESC LIMIT 1`); err != nil {
				return nil, cacheError("prepare indexed relic operation query", err)
			}
		}
		entry := previousRelic{fields: map[string][]byte{}}
		rows, err := fieldsQuery.QueryContext(ctx, head.RelicID)
		if err != nil {
			return nil, cacheError("query indexed relic fields", err)
		}
		for rows.Next() {
			var objectID string
			var fields []byte
			if err := rows.Scan(&objectID, &fields); err != nil {
				rows.Close()
				return nil, cacheError("read indexed relic fields", err)
			}
			entry.fields[objectID] = fields
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, cacheError("read indexed relic fields", err)
		}
		var tail string
		switch err := tailQuery.QueryRowContext(ctx, head.RelicID).Scan(&tail); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, cacheError("read indexed relic operation tail", err)
		case tail == row.head:
			entry.stopAt = row.head
		}
		previous[head.RelicID] = entry
	}
	return previous, nil
}

// completeIndexedRelic checks that Git returned the relic asked for, and fills
// in the fields of every part whose fields object was known from the old rows,
// so the relic carries the fields of every part.
func completeIndexedRelic(head core.RelicHead, relic gitstore.IndexedRelic, known map[string][]byte) (gitstore.IndexedRelic, error) {
	state := relic.Snapshot.Record.State
	if relic.Snapshot.Head != head.ObjectID || state.RelicID != head.RelicID {
		return gitstore.IndexedRelic{}, core.Errorf(core.CategoryCorruptData,
			"relic index read returned relic %s at %s, want %s at %s", state.RelicID, relic.Snapshot.Head, head.RelicID, head.ObjectID)
	}
	fields := make(map[string][]byte, len(state.Relic.Parts))
	for _, part := range state.Relic.Parts {
		objects, found := relic.Parts[part.ID]
		if !found {
			return gitstore.IndexedRelic{}, core.Errorf(core.CategoryCorruptData,
				"relic %s: the index read returned no objects for part %s", head.RelicID, part.ID)
		}
		stored, read := relic.Fields[part.ID]
		if !read {
			stored, read = known[objects.Fields]
		}
		if !read {
			return gitstore.IndexedRelic{}, core.Errorf(core.CategoryOperational,
				"relic %s: the fields of part %s were neither read nor indexed", head.RelicID, part.ID)
		}
		fields[part.ID] = stored
	}
	relic.Fields = fields
	return relic, nil
}

// relicWriter holds the prepared statements one pass writes rows with.
type relicWriter struct {
	statements                            []*sql.Stmt
	selectHead, insertRelic, insertProp   *sql.Stmt
	insertPart, insertOperation, tailStmt *sql.Stmt
	insertLink, deleteOperations          *sql.Stmt
	// deletes remove a relic's rows from every table but its operations.
	deletes []*sql.Stmt
}

func prepareRelicWriter(ctx context.Context, transaction *sql.Tx) (*relicWriter, error) {
	writer := &relicWriter{}
	prepare := func(query string) (*sql.Stmt, error) {
		statement, err := transaction.PrepareContext(ctx, query)
		if err != nil {
			return nil, cacheError("prepare relic index statement", err)
		}
		writer.statements = append(writer.statements, statement)
		return statement, nil
	}
	var err error
	for _, step := range []struct {
		target **sql.Stmt
		query  string
	}{
		{&writer.selectHead, `SELECT head FROM relics WHERE relic_id = ?`},
		{&writer.insertRelic, `INSERT INTO relics (
			relic_id, head, project_id, state_version, min_reader, history_generation, compacted_from,
			logical_clock, title, type, schema, author, retired, deleted, created_at, updated_at, part_count, link_count,
			conformance, schema_problem, compacted_run, compacted_at, compacted_boundary, compacted_since
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&writer.insertLink, `INSERT INTO relic_links (
			relic_id, link_id, from_part, to_kind, to_id, to_part, rel, seen, pointer
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&writer.insertProp, `INSERT INTO relic_properties (relic_id, name, value) VALUES (?, ?, ?)`},
		{&writer.insertPart, `INSERT INTO relic_parts (
			relic_id, part_id, ordinal, type, position, removed, fields_object, fields,
			body_object, body_format, body_version, body_bytes, dropped_removed_at, dropped_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&writer.insertOperation, `INSERT INTO relic_operations (
			relic_id, commit_id, chain_index, pack_index, operation_id, logical_clock,
			history_generation, min_reader, actor, wall_time, type, part, payload
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{&writer.tailStmt, `SELECT commit_id, chain_index FROM relic_operations WHERE relic_id = ? ORDER BY chain_index DESC, pack_index DESC LIMIT 1`},
		{&writer.deleteOperations, `DELETE FROM relic_operations WHERE relic_id = ?`},
	} {
		if *step.target, err = prepare(step.query); err != nil {
			writer.close()
			return nil, err
		}
	}
	for _, table := range []string{"relic_properties", "relic_parts", "relic_links", "relics"} {
		statement, err := prepare(`DELETE FROM ` + table + ` WHERE relic_id = ?`)
		if err != nil {
			writer.close()
			return nil, err
		}
		writer.deletes = append(writer.deletes, statement)
	}
	return writer, nil
}

func (w *relicWriter) close() {
	for _, statement := range w.statements {
		_ = statement.Close()
	}
}

// head returns the head one relic's row names, or "" for no row.
func (w *relicWriter) head(ctx context.Context, relicID string) (string, error) {
	var head string
	err := w.selectHead.QueryRowContext(ctx, relicID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", cacheError("read indexed relic head during refresh", err)
	}
	return head, nil
}

// deleteRelic removes every row of one relic.
func (w *relicWriter) deleteRelic(ctx context.Context, relicID string) error {
	for _, statement := range append(w.deletes, w.deleteOperations) {
		if _, err := statement.ExecContext(ctx, relicID); err != nil {
			return cacheError("remove indexed relic", err)
		}
	}
	return nil
}

// writeRelic replaces one relic's rows with relic, and brings its operation
// rows to chain: appended from where they end when the chain was read from
// there, replaced when the chain restarted at the root, and removed when the
// chain could not be read whole.
func (w *relicWriter) writeRelic(ctx context.Context, relic gitstore.IndexedRelic, chain gitstore.RelicOperationsResult) error {
	state := relic.Snapshot.Record.State
	relicID := state.RelicID
	data := state.Relic
	if relic.Snapshot.Head == "" || relicID == "" || state.ProjectID == "" || state.History.Generation == "" ||
		state.LogicalClock > math.MaxInt64 {
		return core.Errorf(core.CategoryCorruptData, "cannot index invalid relic snapshot")
	}
	for _, statement := range w.deletes {
		if _, err := statement.ExecContext(ctx, relicID); err != nil {
			return cacheError("replace indexed relic", err)
		}
	}
	var compactedFrom sql.NullString
	if state.History.CompactedFrom != nil {
		compactedFrom = sql.NullString{String: *state.History.CompactedFrom, Valid: true}
	}
	var conformance core.RelicConformance
	if relic.Snapshot.Conformance != nil {
		conformance = *relic.Snapshot.Conformance
	}
	// A relic never compacted has NULL in every compaction column.
	var compactedRun, compactedAt, compactedBoundary, compactedSince sql.NullString
	if compaction := data.Compacted; compaction != nil {
		compactedRun = sql.NullString{String: compaction.RunID, Valid: true}
		compactedAt = sql.NullString{String: formatTime(compaction.At), Valid: true}
		compactedBoundary = sql.NullString{String: compaction.Boundary, Valid: true}
		compactedSince = sql.NullString{String: formatTime(compaction.Since), Valid: true}
	}
	if _, err := w.insertRelic.ExecContext(ctx,
		relicID, relic.Snapshot.Head, state.ProjectID, state.Version, state.MinReader, state.History.Generation, compactedFrom,
		int64(state.LogicalClock), data.Title, data.Type, data.Schema, data.Author, sqlBool(data.Retired), sqlBool(data.Deleted),
		formatTime(data.CreatedAt), formatTime(data.UpdatedAt), len(data.Parts), len(relic.Snapshot.Record.Links.Links),
		conformance.State, conformance.Problem, compactedRun, compactedAt, compactedBoundary, compactedSince,
	); err != nil {
		return cacheError("index relic", err)
	}
	// Every link gets a row, whatever it points at, so the rows number what
	// the relic row counts and a read back from them is the whole link.
	for _, link := range relic.Snapshot.Record.Links.Links {
		kind, to := relicLinkKindTask, link.To.Task
		if link.To.Relic != "" {
			kind, to = relicLinkKindRelic, link.To.Relic
		}
		if _, err := w.insertLink.ExecContext(ctx, relicID, link.ID, link.From, kind, to, link.To.Part,
			link.Rel, link.Seen, string(link.Pointer)); err != nil {
			return cacheError("index relic link", err)
		}
	}
	for name, value := range data.Properties {
		canonical, err := core.CanonicalRelicValue(value)
		if err != nil {
			return core.Errorf(core.CategoryCorruptData, "relic %s: the property %q cannot be indexed: %v", relicID, name, err)
		}
		if _, err := w.insertProp.ExecContext(ctx, relicID, name, string(canonical)); err != nil {
			return cacheError("index relic property", err)
		}
	}
	for ordinal, part := range data.Parts {
		objects := relic.Parts[part.ID]
		var bodyObject, bodyFormat, bodyVersion sql.NullString
		var bodyBytes sql.NullInt64
		if part.Body != nil {
			bodyObject = sql.NullString{String: objects.Body, Valid: true}
			bodyFormat = sql.NullString{String: part.Body.Format, Valid: true}
			bodyVersion = sql.NullString{String: part.Body.Version, Valid: true}
			bodyBytes = sql.NullInt64{Int64: part.Body.Bytes, Valid: true}
		}
		// Only a stub, a removed part a checkpoint dropped, has the dates.
		var droppedRemovedAt, droppedAt sql.NullString
		if part.Dropped != nil {
			droppedRemovedAt = sql.NullString{String: formatTime(part.Dropped.RemovedAt), Valid: true}
			droppedAt = sql.NullString{String: formatTime(part.Dropped.DroppedAt), Valid: true}
		}
		if _, err := w.insertPart.ExecContext(ctx,
			relicID, part.ID, ordinal, part.Type, part.Position, sqlBool(part.Removed), objects.Fields, string(relic.Fields[part.ID]),
			bodyObject, bodyFormat, bodyVersion, bodyBytes, droppedRemovedAt, droppedAt,
		); err != nil {
			return cacheError("index relic part", err)
		}
	}

	switch {
	case chain.Truncated != nil:
		// A chain that stops short of the head would read as a whole one.
		return w.dropOperations(ctx, relicID)
	case !chain.BoundaryReached:
		if err := w.dropOperations(ctx, relicID); err != nil {
			return err
		}
		return w.insertOperations(ctx, relicID, 0, chain.Commits)
	}
	// The chain was read from the head the row had. The rows are extended
	// only where they still end at that head; anything else is a chain the
	// index cannot place, so its rows are dropped rather than left with a hole.
	tail, index, found, err := w.operationTail(ctx, relicID)
	if err != nil {
		return err
	}
	if !found || len(chain.Commits) > 0 && tail != chain.Commits[0].Parent {
		return w.dropOperations(ctx, relicID)
	}
	return w.insertOperations(ctx, relicID, index+1, chain.Commits)
}

func (w *relicWriter) dropOperations(ctx context.Context, relicID string) error {
	if _, err := w.deleteOperations.ExecContext(ctx, relicID); err != nil {
		return cacheError("remove indexed relic operations", err)
	}
	return nil
}

func (w *relicWriter) operationTail(ctx context.Context, relicID string) (string, int, bool, error) {
	var commit string
	var index int64
	err := w.tailStmt.QueryRowContext(ctx, relicID).Scan(&commit, &index)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, cacheError("read indexed relic operation tail", err)
	}
	if index < 0 || index > math.MaxInt32 {
		return "", 0, false, core.Errorf(core.CategoryCorruptData, "indexed relic %s has an invalid operation chain index", relicID)
	}
	return commit, int(index), true, nil
}

func (w *relicWriter) insertOperations(ctx context.Context, relicID string, start int, commits []gitstore.RelicOperationCommit) error {
	for offset, commit := range commits {
		pack := commit.Operation
		if pack.LogicalClock > math.MaxInt64 {
			return core.Errorf(core.CategoryCorruptData, "relic %s: commit %s has an invalid logical clock", relicID, commit.ObjectID)
		}
		for packIndex, operation := range pack.Operations {
			payload, err := json.Marshal(operation)
			if err != nil {
				return core.Wrap(core.CategoryCorruptData, "cannot index relic operation", err)
			}
			if _, err := w.insertOperation.ExecContext(ctx,
				relicID, commit.ObjectID, start+offset, packIndex, operation.ID, int64(pack.LogicalClock),
				pack.HistoryGeneration, pack.MinReader, pack.Actor.ID, formatTime(pack.WallTime),
				string(operation.Type), relicOperationPart(operation), string(payload),
			); err != nil {
				return cacheError("index relic operation", err)
			}
		}
	}
	return nil
}

// relicOperationPart is the part an operation names: its own ID for the
// part.add that creates the part, the part it changes otherwise, and nothing
// for an operation on the relic itself.
func relicOperationPart(operation core.RelicOperation) string {
	if operation.Type == core.RelicOperationPartAdd {
		return operation.ID
	}
	return operation.Part
}

func sqlBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

// relicStateFormat is the format every relic state this build reads declares,
// which a snapshot built from rows carries. Only states this build verified
// are indexed, so it is the only format a row can stand for.
const relicStateFormat = "workbook.relic-state"

// RelicReader returns a core.RelicReader that answers from the relic tables.
// Each call returns a new reader, which refreshes the index once, on its first
// ListRelicHeads or GetRelic, and answers every later call from that pass.
//
// It is for reads. Its snapshots carry the full state, but their operation
// holds only the project, relic and history generation, so no write may fold
// onto one: a relic mutation reads its relic from Git.
//
// A relic the index holds no usable row for is read from Git as the
// repository reads it, with the same answers and the same errors. That covers
// a relic the pass could not index, damaged or written by a newer Workbook,
// and one whose row another writer moved during the pass, which is answered
// from the pass's own Git read instead.
//
// A relic read never fails because the index could not be built or read. When
// the pass fails, as it does when a damaged task tip stops the task rebuild a
// missing cache needs, or when SQLite refuses the cache, the reader answers
// every call from the repository, as a read without an index does. Only a
// cancelled read is refused with its context's error.
//
// Its pass judges each relic it indexes against the schema version the relic
// names, so its snapshots carry that judgment. RelicReaderForTasks is the
// reader for a task command, whose pass judges nothing.
func (s *Store) RelicReader() RelicIndex {
	return &relicIndexReader{store: s, judging: true}
}

// RelicReaderForTasks is RelicReader for a task command, such as workbook show
// listing the relics that link to a task: its pass reads no schema listing and
// no schema, and judges no relic. A relic it indexes that names a schema is
// stored as not yet judged, and the next relic read judges it.
func (s *Store) RelicReaderForTasks() RelicIndex {
	return &relicIndexReader{store: s}
}

type relicIndexReader struct {
	store   *Store
	judging bool
	mu      sync.Mutex
	pass    *relicPass
	// gitOnly is set when the pass failed: every call then answers from the
	// repository.
	gitOnly bool
}

var _ core.RelicReader = (*relicIndexReader)(nil)

// refreshed returns the pass this reader answers from, running it the first
// time. It returns no pass and no error when the index cannot be used, and the
// caller then answers from the repository.
func (r *relicIndexReader) refreshed(ctx context.Context, config core.ProjectConfig) (*relicPass, error) {
	if err := r.store.validateConfig(config); err != nil {
		return nil, err
	}
	if r.store.relics == nil {
		return nil, errNoRelicSource()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pass != nil || r.gitOnly {
		return r.pass, nil
	}
	var pass relicPass
	if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
		var err error
		pass, err = r.store.refreshRelics(ctx, r.judging)
		return err
	}); err != nil {
		if !indexUnusable(ctx) {
			return nil, err
		}
		r.gitOnly = true
		return nil, nil
	}
	r.pass = &pass
	return r.pass, nil
}

// indexUnusable reports whether a failed index read is answered from the
// repository instead: any failure but the caller's own cancellation.
func indexUnusable(ctx context.Context) bool {
	return ctx.Err() == nil
}

// ListRelicHeads returns the heads the pass listed.
func (r *relicIndexReader) ListRelicHeads(ctx context.Context, config core.ProjectConfig) ([]core.RelicHead, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return nil, err
	}
	if pass == nil {
		return r.store.relics.ListRelicHeads(ctx, config)
	}
	return slices.Clone(pass.heads), nil
}

// ReadRelicTips answers each head from its row when the row names that head,
// from the pass's Git read when the pass read it, and otherwise from the
// repository, so a tip Git refuses fails the listing as it does without an
// index.
func (r *relicIndexReader) ReadRelicTips(ctx context.Context, config core.ProjectConfig, heads []core.RelicHead) ([]core.RelicSnapshot, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return nil, err
	}
	if pass == nil {
		return r.store.relics.ReadRelicTips(ctx, config, heads)
	}
	var rows map[string]core.RelicSnapshot
	if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
		var err error
		rows, err = r.store.queryIndexedRelics(ctx, "")
		return err
	}); err != nil {
		if !indexUnusable(ctx) {
			return nil, err
		}
		// With no rows, every head the pass did not read is read from Git.
		rows = nil
	}
	snapshots := make([]core.RelicSnapshot, len(heads))
	var missed []core.RelicHead
	var missedAt []int
	for index, head := range heads {
		if snapshot, ok := pass.answer(head, rows); ok {
			snapshots[index] = snapshot
			continue
		}
		missed = append(missed, head)
		missedAt = append(missedAt, index)
	}
	if len(missed) > 0 {
		read, err := r.store.relics.ReadRelicTips(ctx, config, missed)
		if err != nil {
			return nil, err
		}
		if len(read) != len(missed) {
			return nil, core.Errorf(core.CategoryCorruptData, "relic tip read returned %d relics, want %d", len(read), len(missed))
		}
		for position, index := range missedAt {
			snapshots[index] = read[position]
		}
	}
	return snapshots, nil
}

// GetRelic answers from the relic's row, or the pass's Git read of it, and
// otherwise reads it from the repository, which gives the same errors and
// the same pointer to `workbook validate` a read without an index gives.
func (r *relicIndexReader) GetRelic(ctx context.Context, config core.ProjectConfig, relicID string) (core.RelicSnapshot, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return core.RelicSnapshot{}, err
	}
	if pass == nil {
		return r.store.relics.GetRelic(ctx, config, relicID)
	}
	head, listed := pass.head(relicID)
	if listed {
		var rows map[string]core.RelicSnapshot
		if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
			var err error
			rows, err = r.store.queryIndexedRelics(ctx, relicID)
			return err
		}); err != nil {
			if !indexUnusable(ctx) {
				return core.RelicSnapshot{}, err
			}
			rows = nil
		}
		if snapshot, ok := pass.answer(head, rows); ok {
			return snapshot, nil
		}
	}
	return r.store.relics.GetRelic(ctx, config, relicID)
}

// ReadRelicParts reads fields from the rows when snapshot is the head they
// hold, or from the pass's Git read of that head, and bodies from Git by the
// object IDs those hold, in one read. Anything else, and any request the
// rows cannot settle, is read from the repository.
func (r *relicIndexReader) ReadRelicParts(
	ctx context.Context,
	config core.ProjectConfig,
	snapshot core.RelicSnapshot,
	partIDs []string,
	bodies bool,
) (map[string]core.RelicPartContent, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return nil, err
	}
	if pass == nil {
		return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
	}
	relicID := snapshot.Record.State.RelicID
	// The repository says what is wrong with a request the state does not
	// settle: a part ID that is not one, or a part the relic does not have.
	wanted := make([]core.RelicPart, 0, len(partIDs))
	seen := make(map[string]struct{}, len(partIDs))
	for _, partID := range partIDs {
		if _, repeated := seen[partID]; repeated {
			continue
		}
		seen[partID] = struct{}{}
		part, found := snapshot.Record.State.Relic.Part(partID)
		if core.ValidatePartID(partID) != nil || !found {
			return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
		}
		wanted = append(wanted, part)
	}

	var stored map[string]indexedPartContent
	if relic, found := pass.fromGit[relicID]; found && relic.Snapshot.Head == snapshot.Head {
		stored = make(map[string]indexedPartContent, len(relic.Parts))
		for partID, objects := range relic.Parts {
			stored[partID] = indexedPartContent{fields: relic.Fields[partID], body: objects.Body}
		}
	} else if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
		var err error
		stored, err = r.store.queryIndexedParts(ctx, relicID, snapshot.Head)
		return err
	}); err != nil {
		if !indexUnusable(ctx) {
			return nil, err
		}
		return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
	}

	contents := make(map[string]core.RelicPartContent, len(wanted))
	var requests []core.RelicPart
	var bodyRequests []gitstore.RelicBodyRequest
	for _, part := range wanted {
		row, found := stored[part.ID]
		if !found {
			return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
		}
		fields, err := core.DecodeRelicFieldsDocument(row.fields)
		if err != nil {
			return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
		}
		contents[part.ID] = core.RelicPartContent{Fields: fields.Fields}
		if bodies && part.Body != nil {
			if row.body == "" {
				return r.store.relics.ReadRelicParts(ctx, config, snapshot, partIDs, bodies)
			}
			requests = append(requests, part)
			bodyRequests = append(bodyRequests, gitstore.RelicBodyRequest{
				RelicID: relicID, PartID: part.ID, ObjectID: row.body, Bytes: part.Body.Bytes,
			})
		}
	}
	if len(bodyRequests) > 0 {
		read, err := r.store.relics.ReadRelicBodies(ctx, config, bodyRequests)
		if err != nil {
			return nil, err
		}
		if len(read) != len(bodyRequests) {
			return nil, core.Errorf(core.CategoryCorruptData, "relic body read returned %d bodies, want %d", len(read), len(bodyRequests))
		}
		for index, part := range requests {
			content := contents[part.ID]
			content.Body, content.HasBody = read[index], true
			contents[part.ID] = content
		}
	}
	return contents, nil
}

// head returns the head the pass listed for relicID.
func (pass *relicPass) head(relicID string) (core.RelicHead, bool) {
	index, found := slices.BinarySearchFunc(pass.heads, relicID, func(head core.RelicHead, id string) int {
		return strings.Compare(head.RelicID, id)
	})
	if found {
		return pass.heads[index], true
	}
	// A source that lists heads out of order is still answered.
	for _, head := range pass.heads {
		if head.RelicID == relicID {
			return head, true
		}
	}
	return core.RelicHead{}, false
}

// answer returns the snapshot of head the index can give: the pass's own Git
// read when it made one, else the row when it holds that head. A relic the
// pass refused is never answered here.
func (pass *relicPass) answer(head core.RelicHead, rows map[string]core.RelicSnapshot) (core.RelicSnapshot, bool) {
	if _, failed := pass.failed[head.RelicID]; failed {
		return core.RelicSnapshot{}, false
	}
	if relic, found := pass.fromGit[head.RelicID]; found && relic.Snapshot.Head == head.ObjectID {
		return relic.Snapshot, true
	}
	if snapshot, found := rows[head.RelicID]; found && snapshot.Head == head.ObjectID {
		return snapshot, true
	}
	return core.RelicSnapshot{}, false
}

// queryIndexedRelics builds the snapshot every relic row holds, or only
// relicID's when it is not empty, in one read transaction. A relic whose part
// or link rows do not number what its row records is left out, so it is read
// from Git.
func (s *Store) queryIndexedRelics(ctx context.Context, relicID string) (map[string]core.RelicSnapshot, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, s.databaseError("begin indexed relic query", err)
	}
	defer transaction.Rollback()
	where, args := "", []any{}
	if relicID != "" {
		where, args = ` WHERE relic_id = ?`, []any{relicID}
	}

	snapshots := map[string]core.RelicSnapshot{}
	partCounts, linkCounts := map[string]int{}, map[string]int{}
	rows, err := transaction.QueryContext(ctx, `SELECT relic_id, head, project_id, state_version, min_reader,
		history_generation, compacted_from, logical_clock, title, type, schema, author, retired, deleted,
		created_at, updated_at, part_count, link_count, conformance, schema_problem,
		compacted_run, compacted_at, compacted_boundary, compacted_since FROM relics`+where, args...)
	if err != nil {
		return nil, s.databaseError("query indexed relics", err)
	}
	for rows.Next() {
		snapshot, partCount, linkCount, err := scanIndexedRelic(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		id := snapshot.Record.State.RelicID
		snapshots[id], partCounts[id], linkCounts[id] = snapshot, partCount, linkCount
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, s.databaseError("read indexed relics", err)
	}

	properties, err := transaction.QueryContext(ctx, `SELECT relic_id, name, value FROM relic_properties`+where, args...)
	if err != nil {
		return nil, s.databaseError("query indexed relic properties", err)
	}
	for properties.Next() {
		var id, name, value string
		if err := properties.Scan(&id, &name, &value); err != nil {
			properties.Close()
			return nil, s.databaseError("read indexed relic property", err)
		}
		if snapshot, found := snapshots[id]; found {
			snapshot.Record.State.Relic.Properties[name] = json.RawMessage(value)
		}
	}
	if err := errors.Join(properties.Err(), properties.Close()); err != nil {
		return nil, s.databaseError("read indexed relic properties", err)
	}

	parts, err := transaction.QueryContext(ctx, `SELECT relic_id, part_id, type, position, removed,
		body_format, body_version, body_bytes, dropped_removed_at, dropped_at FROM relic_parts`+where+` ORDER BY relic_id, ordinal`, args...)
	if err != nil {
		return nil, s.databaseError("query indexed relic parts", err)
	}
	for parts.Next() {
		var id string
		var part core.RelicPart
		var removed int
		var format, version, droppedRemovedAt, droppedAt sql.NullString
		var size sql.NullInt64
		if err := parts.Scan(&id, &part.ID, &part.Type, &part.Position, &removed, &format, &version, &size,
			&droppedRemovedAt, &droppedAt); err != nil {
			parts.Close()
			return nil, s.databaseError("read indexed relic part", err)
		}
		part.Removed = removed != 0
		if version.Valid {
			part.Body = &core.RelicBody{Format: format.String, Version: version.String, Bytes: size.Int64}
		}
		if droppedRemovedAt.Valid || droppedAt.Valid {
			drop, err := scanRelicPartDrop(id, part.ID, droppedRemovedAt, droppedAt)
			if err != nil {
				parts.Close()
				return nil, err
			}
			part.Dropped = drop
		}
		if snapshot, found := snapshots[id]; found {
			snapshot.Record.State.Relic.Parts = append(snapshot.Record.State.Relic.Parts, part)
			snapshots[id] = snapshot
		}
	}
	if err := errors.Join(parts.Err(), parts.Close()); err != nil {
		return nil, s.databaseError("read indexed relic parts", err)
	}
	links, err := transaction.QueryContext(ctx, `SELECT relic_id, link_id, from_part, to_kind, to_id, to_part, rel, seen, pointer
		FROM relic_links`+where+` ORDER BY relic_id, link_id`, args...)
	if err != nil {
		return nil, s.databaseError("query indexed relic links", err)
	}
	for links.Next() {
		var id string
		link, err := scanIndexedRelicLink(links, &id)
		if err != nil {
			links.Close()
			return nil, err
		}
		if snapshot, found := snapshots[id]; found {
			snapshot.Record.Links.Links = append(snapshot.Record.Links.Links, link)
			snapshots[id] = snapshot
		}
	}
	if err := errors.Join(links.Err(), links.Close()); err != nil {
		return nil, s.databaseError("read indexed relic links", err)
	}
	for id, snapshot := range snapshots {
		if len(snapshot.Record.State.Relic.Parts) != partCounts[id] || len(snapshot.Record.Links.Links) != linkCounts[id] {
			delete(snapshots, id)
		}
	}
	return snapshots, nil
}

// The to_kind values of a relic_links row.
const (
	relicLinkKindTask  = "task"
	relicLinkKindRelic = "relic"
)

// scanIndexedRelicLink reads the relic ID into relicID and the link a
// relic_links row holds, from its columns relic_id, link_id, from_part,
// to_kind, to_id, to_part, rel, seen and pointer, in that order.
func scanIndexedRelicLink(scanner rowScanner, relicID *string) (core.RelicLink, error) {
	var link core.RelicLink
	var kind, to, toPart, pointer string
	if err := scanner.Scan(relicID, &link.ID, &link.From, &kind, &to, &toPart, &link.Rel, &link.Seen, &pointer); err != nil {
		return core.RelicLink{}, cacheError("read indexed relic link", err)
	}
	switch {
	case kind == relicLinkKindTask && toPart == "":
		link.To.Task = to
	case kind == relicLinkKindRelic:
		link.To = core.RelicLinkTarget{Relic: to, Part: toPart}
	default:
		return core.RelicLink{}, core.Errorf(core.CategoryCorruptData,
			"indexed relic %s has link %s with a target this build cannot read", *relicID, link.ID)
	}
	if pointer != "" {
		link.Pointer = json.RawMessage(pointer)
	}
	return link, nil
}

func scanIndexedRelic(scanner rowScanner) (core.RelicSnapshot, int, int, error) {
	var head, schema, createdAt, updatedAt string
	var conformance core.RelicConformance
	var state core.RelicStateDocument
	var compactedFrom, compactedRun, compactedAt, compactedBoundary, compactedSince sql.NullString
	var clock int64
	var retired, deleted, partCount, linkCount int
	relic := &state.Relic
	if err := scanner.Scan(&state.RelicID, &head, &state.ProjectID, &state.Version, &state.MinReader,
		&state.History.Generation, &compactedFrom, &clock, &relic.Title, &relic.Type, &schema, &relic.Author,
		&retired, &deleted, &createdAt, &updatedAt, &partCount, &linkCount, &conformance.State, &conformance.Problem,
		&compactedRun, &compactedAt, &compactedBoundary, &compactedSince); err != nil {
		return core.RelicSnapshot{}, 0, 0, cacheError("read indexed relic", err)
	}
	if clock < 0 {
		return core.RelicSnapshot{}, 0, 0, core.Errorf(core.CategoryCorruptData, "indexed relic %s has an invalid logical clock", state.RelicID)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return core.RelicSnapshot{}, 0, 0, core.Wrap(core.CategoryCorruptData, "indexed relic "+state.RelicID+" has an invalid creation time", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return core.RelicSnapshot{}, 0, 0, core.Wrap(core.CategoryCorruptData, "indexed relic "+state.RelicID+" has an invalid update time", err)
	}
	state.Format = relicStateFormat
	state.LogicalClock = uint64(clock)
	if compactedFrom.Valid {
		from := compactedFrom.String
		state.History.CompactedFrom = &from
	}
	if compactedRun.Valid || compactedAt.Valid || compactedBoundary.Valid || compactedSince.Valid {
		compaction, err := scanRelicCompaction(state.RelicID, compactedRun, compactedAt, compactedBoundary, compactedSince)
		if err != nil {
			return core.RelicSnapshot{}, 0, 0, err
		}
		relic.Compacted = compaction
	}
	relic.Schema = schema
	relic.Retired, relic.Deleted = retired != 0, deleted != 0
	relic.CreatedAt, relic.UpdatedAt = created, updated
	relic.Properties = map[string]json.RawMessage{}
	relic.Parts = make([]core.RelicPart, 0, partCount)
	snapshot := core.RelicSnapshot{
		Head: head,
		Operation: core.RelicOperationPack{
			ProjectID: state.ProjectID, RelicID: state.RelicID, HistoryGeneration: state.History.Generation,
		},
		Record: core.RelicRecord{State: state, Links: core.EmptyRelicLinks()},
	}
	if conformance.State != "" {
		snapshot.Conformance = &conformance
	}
	return snapshot, partCount, linkCount, nil
}

// scanRelicCompaction reads a relic row's compaction columns back into the
// state's member. The writer sets all four or none, so a row with only some is
// damaged.
func scanRelicCompaction(relicID string, run, at, boundary, since sql.NullString) (*core.RelicCompaction, error) {
	if !run.Valid || !at.Valid || !boundary.Valid || !since.Valid {
		return nil, core.Errorf(core.CategoryCorruptData, "indexed relic %s has a partial compaction", relicID)
	}
	atTime, err := time.Parse(time.RFC3339Nano, at.String)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+relicID+" has an invalid compaction time", err)
	}
	sinceTime, err := time.Parse(time.RFC3339Nano, since.String)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+relicID+" has an invalid compaction since time", err)
	}
	return &core.RelicCompaction{RunID: run.String, At: atTime, Boundary: boundary.String, Since: sinceTime}, nil
}

// scanRelicPartDrop reads a stub's two dates back into the part's member.
func scanRelicPartDrop(relicID, partID string, removedAt, droppedAt sql.NullString) (*core.RelicPartDrop, error) {
	if !removedAt.Valid || !droppedAt.Valid {
		return nil, core.Errorf(core.CategoryCorruptData, "indexed relic %s has part %s with a partial drop", relicID, partID)
	}
	removed, err := time.Parse(time.RFC3339Nano, removedAt.String)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+relicID+" has part "+partID+" with an invalid removal time", err)
	}
	dropped, err := time.Parse(time.RFC3339Nano, droppedAt.String)
	if err != nil {
		return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+relicID+" has part "+partID+" with an invalid drop time", err)
	}
	return &core.RelicPartDrop{RemovedAt: removed, DroppedAt: dropped}, nil
}

// indexedPartContent is what the index holds of one part's content: its
// fields.json bytes and its body's object ID, empty for no body.
type indexedPartContent struct {
	fields []byte
	body   string
}

// queryIndexedParts reads the fields and body object of every part of
// relicID, when its row still holds head and every part row is there. It
// returns nil otherwise, which sends the read to Git.
func (s *Store) queryIndexedParts(ctx context.Context, relicID, head string) (map[string]indexedPartContent, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, s.databaseError("begin indexed relic part query", err)
	}
	defer transaction.Rollback()
	var rowHead string
	var partCount int
	err = transaction.QueryRowContext(ctx, `SELECT head, part_count FROM relics WHERE relic_id = ?`, relicID).Scan(&rowHead, &partCount)
	if errors.Is(err, sql.ErrNoRows) || err == nil && rowHead != head {
		return nil, nil
	}
	if err != nil {
		return nil, s.databaseError("query indexed relic head", err)
	}
	rows, err := transaction.QueryContext(ctx, `SELECT part_id, fields, body_object FROM relic_parts WHERE relic_id = ?`, relicID)
	if err != nil {
		return nil, s.databaseError("query indexed relic part content", err)
	}
	defer rows.Close()
	parts := make(map[string]indexedPartContent, partCount)
	for rows.Next() {
		var partID string
		var fields []byte
		var body sql.NullString
		if err := rows.Scan(&partID, &fields, &body); err != nil {
			return nil, s.databaseError("read indexed relic part content", err)
		}
		parts[partID] = indexedPartContent{fields: fields, body: body.String}
	}
	if err := rows.Err(); err != nil {
		return nil, s.databaseError("read indexed relic part content", err)
	}
	if len(parts) != partCount {
		return nil, nil
	}
	return parts, nil
}
