package projection

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// RelicIndex is what the local index answers for relics: every relic read,
// and which relics link to a target.
type RelicIndex interface {
	core.RelicReader
	core.RelicLinkIndex
}

var _ RelicIndex = (*relicIndexReader)(nil)

// RelicLinksTo answers which relics link to target, from the reader's one
// pass and one query on relic_links_by_target joined to the relic and part
// rows. A relic the pass read from Git itself is answered from that read, and
// a relic the pass refused is listed as unread and its links are not looked
// for. When the index cannot be used, every relic is read from Git instead,
// and a relic Git refuses is unread there too.
func (r *relicIndexReader) RelicLinksTo(ctx context.Context, config core.ProjectConfig, target core.RelicLinkTarget) (core.RelicLinkLookup, error) {
	pass, err := r.refreshed(ctx, config)
	if err != nil {
		return core.RelicLinkLookup{}, err
	}
	if pass == nil {
		return r.relicLinksFromGit(ctx, config, target)
	}
	var rows []core.RelicLinkSource
	if err := r.store.withActiveDatabase(ctx, func(ctx context.Context) error {
		var err error
		rows, err = r.store.queryRelicLinksTo(ctx, target)
		return err
	}); err != nil {
		if !indexUnusable(ctx) {
			return core.RelicLinkLookup{}, err
		}
		return r.relicLinksFromGit(ctx, config, target)
	}

	var lookup core.RelicLinkLookup
	for relicID := range pass.failed {
		lookup.Unread = append(lookup.Unread, relicID)
	}
	answered := map[string]struct{}{}
	for relicID, relic := range pass.fromGit {
		if head, listed := pass.head(relicID); listed && head.ObjectID == relic.Snapshot.Head {
			answered[relicID] = struct{}{}
			lookup.Sources = append(lookup.Sources, core.RelicLinkSourcesOf(relic.Snapshot, target)...)
		}
	}
	// A row the pass did not leave at the listed head, which only a writer
	// racing this read leaves, is read from Git.
	stale := map[string]struct{}{}
	for _, row := range rows {
		relicID := row.Relic.ID
		if _, failed := pass.failed[relicID]; failed {
			continue
		}
		if _, done := answered[relicID]; done {
			continue
		}
		head, listed := pass.head(relicID)
		if !listed {
			continue
		}
		if head.ObjectID != row.Relic.Head {
			stale[relicID] = struct{}{}
			continue
		}
		lookup.Sources = append(lookup.Sources, row)
	}
	for relicID := range stale {
		snapshot, err := r.store.relics.GetRelic(ctx, config, relicID)
		if err != nil {
			if ctx.Err() != nil {
				return core.RelicLinkLookup{}, ctx.Err()
			}
			lookup.Unread = append(lookup.Unread, relicID)
			continue
		}
		lookup.Sources = append(lookup.Sources, core.RelicLinkSourcesOf(snapshot, target)...)
	}
	core.SortRelicLinkSources(lookup.Sources)
	sort.Strings(lookup.Unread)
	return lookup, nil
}

// relicLinksFromGit reads every relic's tip from Git, verified as GetRelic
// verifies it, in one batch.
func (r *relicIndexReader) relicLinksFromGit(ctx context.Context, config core.ProjectConfig, target core.RelicLinkTarget) (core.RelicLinkLookup, error) {
	heads, err := r.store.relics.ListRelicHeads(ctx, config)
	if err != nil {
		return core.RelicLinkLookup{}, err
	}
	var lookup core.RelicLinkLookup
	if len(heads) == 0 {
		return lookup, nil
	}
	requests := make([]gitstore.RelicIndexRequest, len(heads))
	for index, head := range heads {
		requests[index] = gitstore.RelicIndexRequest{Head: head}
	}
	relics, errs := r.store.relics.ReadIndexedRelics(ctx, config, requests)
	if len(relics) != len(requests) || len(errs) != len(requests) {
		return core.RelicLinkLookup{}, core.Errorf(core.CategoryCorruptData,
			"relic index read returned %d relics and %d errors, want %d", len(relics), len(errs), len(requests))
	}
	for index, head := range heads {
		if err := errs[index]; err != nil {
			switch core.CategoryOf(err) {
			case "", core.CategoryOperational, core.CategoryStaleWrite:
				return core.RelicLinkLookup{}, err
			}
			lookup.Unread = append(lookup.Unread, head.RelicID)
			continue
		}
		lookup.Sources = append(lookup.Sources, core.RelicLinkSourcesOf(relics[index].Snapshot, target)...)
	}
	core.SortRelicLinkSources(lookup.Sources)
	sort.Strings(lookup.Unread)
	return lookup, nil
}

// queryRelicLinksTo is the reverse lookup's one query: the links to target
// with the relic each is on, its live part count, and the part each is from.
func (s *Store) queryRelicLinksTo(ctx context.Context, target core.RelicLinkTarget) ([]core.RelicLinkSource, error) {
	if target.Task == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.relic_id, l.link_id, l.from_part, l.rel,
		r.head, r.title, r.type, r.author, r.retired, r.deleted, r.created_at, r.updated_at,
		(SELECT COUNT(*) FROM relic_parts live WHERE live.relic_id = l.relic_id AND live.removed = 0),
		p.type, p.removed, p.ordinal
		FROM relic_links l
		JOIN relics r ON r.relic_id = l.relic_id
		LEFT JOIN relic_parts p ON p.relic_id = l.relic_id AND p.part_id = l.from_part
		WHERE l.to_kind = 'task' AND l.to_id = ? AND l.to_part = ''
		ORDER BY l.relic_id, l.link_id`, target.Task)
	if err != nil {
		return nil, s.databaseError("query relic links to a task", err)
	}
	defer rows.Close()
	var sources []core.RelicLinkSource
	for rows.Next() {
		var source core.RelicLinkSource
		var createdAt, updatedAt string
		var retired, deleted int
		var partType sql.NullString
		var partRemoved, partOrdinal sql.NullInt64
		summary := &source.Relic
		if err := rows.Scan(&summary.ID, &source.Link.ID, &source.Link.From, &source.Link.Rel,
			&summary.Head, &summary.Title, &summary.Type, &summary.Author, &retired, &deleted, &createdAt, &updatedAt,
			&summary.Parts, &partType, &partRemoved, &partOrdinal); err != nil {
			return nil, s.databaseError("read relic link to a task", err)
		}
		if summary.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+summary.ID+" has an invalid creation time", err)
		}
		if summary.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
			return nil, core.Wrap(core.CategoryCorruptData, "indexed relic "+summary.ID+" has an invalid update time", err)
		}
		summary.Retired, summary.Deleted = retired != 0, deleted != 0
		source.Link.To = target
		source.PartOrdinal = -1
		if source.Link.From != "" {
			if !partOrdinal.Valid {
				return nil, core.Errorf(core.CategoryCorruptData,
					"indexed relic %s has link %s from part %s, which it has no row for", summary.ID, source.Link.ID, source.Link.From)
			}
			source.PartType, source.PartRemoved, source.PartOrdinal = partType.String, partRemoved.Int64 != 0, int(partOrdinal.Int64)
		}
		sources = append(sources, source)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, s.databaseError("read relic links to a task", err)
	}
	return sources, nil
}
