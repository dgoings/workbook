package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// WarningRelicLinksUnread says that a relic that may link to the task could
// not be read, so its links are not listed.
const WarningRelicLinksUnread = "relic-links-unread"

// WarningRelicLinkTargetsUnread says that the tasks a relic's links point at
// could not be read, so links to removed tasks are not marked.
const WarningRelicLinkTargetsUnread = "relic-link-targets-unread"

// RelicLinkSource is one link that points at a target, with the relic that
// holds it and, for a link from a part, that part's type, whether it is
// removed and its place among the relic's parts. PartOrdinal is -1 for a link
// from the whole relic.
type RelicLinkSource struct {
	Relic       RelicSummary
	Link        RelicLink
	PartType    string
	PartRemoved bool
	// Dropped says that the part the link is from is a stub.
	Dropped     *RelicPartDrop
	PartOrdinal int
}

// RelicLinkLookup is what a reverse lookup found: every link to the target,
// sorted by relic ID, part ordinal and link ID, and the IDs of the relics that
// could not be read, whose links are not among them.
type RelicLinkLookup struct {
	Sources []RelicLinkSource
	Unread  []string
}

// RelicLinkIndex answers which relics link to a target.
type RelicLinkIndex interface {
	RelicLinksTo(context.Context, ProjectConfig, RelicLinkTarget) (RelicLinkLookup, error)
}

// RelicLinkMatch is one link a listed relic has to the task, relic or part
// asked about. ToPart is the part of the target relic it points at, and
// Pointer where in the target it points, when it records one.
type RelicLinkMatch struct {
	Link     string          `json:"link"`
	Part     string          `json:"part,omitempty"`
	PartType string          `json:"partType,omitempty"`
	Rel      string          `json:"rel"`
	ToPart   string          `json:"toPart,omitempty"`
	Pointer  json.RawMessage `json:"pointer,omitempty"`
	Dropped  *RelicPartDrop  `json:"dropped,omitempty"`
}

// RelicIncomingLink is one link another relic, or a part of one, has to the
// relic or part relic show presents: the relic it is on, with its title and
// type, the part it is from and that part's type, the part of the shown relic
// it points at, its relationship, the version of the shown relic it saw and
// its pointer.
type RelicIncomingLink struct {
	Relic    string          `json:"relic"`
	Title    string          `json:"title"`
	Type     string          `json:"type"`
	Link     string          `json:"link"`
	From     string          `json:"from,omitempty"`
	FromType string          `json:"fromType,omitempty"`
	ToPart   string          `json:"toPart,omitempty"`
	Rel      string          `json:"rel"`
	Seen     string          `json:"seen"`
	Pointer  json.RawMessage `json:"pointer,omitempty"`
}

// TaskRelicLink is one link to a task, as workbook show lists it.
type TaskRelicLink struct {
	Relic    string `json:"relic"`
	Title    string `json:"title"`
	Type     string `json:"type"`
	Part     string `json:"part,omitempty"`
	PartType string `json:"partType,omitempty"`
	Link     string `json:"link"`
	Rel      string `json:"rel"`
	// Retired and Deleted are the relic's marks, and RemovedAt the date its
	// content, or the part's, was removed when compaction dropped it.
	Retired   bool       `json:"retired,omitempty"`
	Deleted   bool       `json:"deleted,omitempty"`
	RemovedAt *time.Time `json:"removedAt,omitempty"`
}

// RelicLinkRemovedAt is the date a link of the relic, from the part dropped
// belongs to if it is from a stub, shows as "removed on": when the stub's part
// was removed, or else, for a deleted relic whose content a compaction
// dropped, when it was deleted. It is nil when nothing was dropped.
func RelicLinkRemovedAt(relic RelicSummary, dropped *RelicPartDrop) *time.Time {
	switch {
	case dropped != nil:
		removed := dropped.RemovedAt
		return &removed
	case relic.Deleted && relic.Compacted != nil && relic.Compacted.Boundary == RelicBoundaryDeleted:
		since := relic.Compacted.Since
		return &since
	default:
		return nil
	}
}

// RelicLinkSourcesOf returns the links of one relic that point at target, as
// a reverse lookup reports them. A whole-relic target covers links to its
// parts too.
func RelicLinkSourcesOf(snapshot RelicSnapshot, target RelicLinkTarget) []RelicLinkSource {
	var sources []RelicLinkSource
	var summary *RelicSummary
	parts := snapshot.Record.State.Relic.Parts
	for _, link := range snapshot.Record.Links.Links {
		if !target.Covers(link.To) {
			continue
		}
		if summary == nil {
			made := relicSummaryOf(snapshot)
			summary = &made
		}
		source := RelicLinkSource{Relic: *summary, Link: link, PartOrdinal: -1}
		if link.From != "" {
			for ordinal, part := range parts {
				if part.ID == link.From {
					source.PartType, source.PartRemoved, source.PartOrdinal = part.Type, part.Removed, ordinal
					source.Dropped = part.Dropped
					break
				}
			}
		}
		sources = append(sources, source)
	}
	return sources
}

// SortRelicLinkSources orders sources by relic ID, part ordinal and link ID.
func SortRelicLinkSources(sources []RelicLinkSource) {
	sort.Slice(sources, func(a, b int) bool {
		left, right := sources[a], sources[b]
		if left.Relic.ID != right.Relic.ID {
			return left.Relic.ID < right.Relic.ID
		}
		if left.PartOrdinal != right.PartOrdinal {
			return left.PartOrdinal < right.PartOrdinal
		}
		return left.Link.ID < right.Link.ID
	})
}

// RelicLinksUnreadWarning is the warning a reverse lookup gives for a relic it
// could not read.
func RelicLinksUnreadWarning(relicID string) Warning {
	return Warning{
		Code:    WarningRelicLinksUnread,
		Message: fmt.Sprintf("relic %s cannot be read, so its links are not listed; run `workbook validate`", relicID),
	}
}

// LinkedTo lists the relics that link to a task, a relic or a part of one,
// each with the links it has to it, in relic ID order. A link to a part of a
// relic counts as a link to the relic. The target may be deleted or removed.
// A relic or part is found by prefix among this clone's relics; a full relic
// ID this clone lacks, and a full part ID under it, are looked up as given,
// so links to a relic not fetched yet are listed. Links from removed parts
// are left out, and so are deleted relics unless filter.IncludeDeleted and
// retired ones unless filter.IncludeRetired; filter.Type keeps only relics of
// that type. A relic the index cannot read is a warning, not a failure.
func (s RelicService) LinkedTo(ctx context.Context, target string, filter RelicListFilter) ([]RelicSummary, []Warning, error) {
	if s.Links == nil {
		return nil, nil, Errorf(CategoryOperational, "relic service has no link index")
	}
	if typedRelicAddress(target) {
		resolved, err := s.linkedToRelicTarget(ctx, target)
		if err != nil {
			return nil, nil, err
		}
		return s.linkedToTarget(ctx, resolved, filter)
	}
	if s.Tasks == nil {
		return nil, nil, errNoTaskReader()
	}
	taskID, err := s.Tasks.Resolve(ctx, s.Config, target)
	if err != nil {
		return nil, nil, err
	}
	return s.LinkedToTask(ctx, taskID, filter)
}

// linkedToRelicTarget resolves the relic or part relic list --linked-to
// names. A deleted relic and a removed part resolve; a full ID this clone
// lacks is taken as given; another project's key is refused.
func (s RelicService) linkedToRelicTarget(ctx context.Context, typed string) (RelicLinkTarget, error) {
	relicPrefix, partPrefix, hasPart, err := ParsePartAddress(typed)
	if err != nil {
		return RelicLinkTarget{}, err
	}
	if key, _, _ := strings.Cut(strings.ToUpper(relicPrefix), "-"); !s.keys().Contains(key) {
		return RelicLinkTarget{}, Errorf(CategoryValidation,
			"relic %s is in another project; a link stays within one project", relicTypedInMessage(relicPrefix))
	}
	heads, err := s.Reader.ListRelicHeads(ctx, s.Config)
	if err != nil {
		return RelicLinkTarget{}, err
	}
	head, err := ResolveRelicPrefix(heads, relicPrefix)
	if err != nil {
		if CategoryOf(err) != CategoryNotFound || ValidateRelicIDShape(relicPrefix) != nil ||
			hasPart && ValidatePartID(partPrefix) != nil {
			return RelicLinkTarget{}, err
		}
		return RelicLinkTarget{Relic: relicPrefix, Part: partPrefix}, nil
	}
	target := RelicLinkTarget{Relic: head.RelicID}
	if !hasPart {
		return target, nil
	}
	snapshot, err := s.Reader.GetRelic(ctx, s.Config, head.RelicID)
	if err != nil {
		return RelicLinkTarget{}, err
	}
	part, err := resolvePartPrefix(head.RelicID, snapshot.Record.State.Relic.Parts, partPrefix)
	if err != nil {
		if CategoryOf(err) != CategoryNotFound || ValidatePartID(partPrefix) != nil {
			return RelicLinkTarget{}, err
		}
		// The local copy lacks a part a newer copy may have.
		target.Part = partPrefix
		return target, nil
	}
	target.Part = part.ID
	return target, nil
}

// LinkedToTask is LinkedTo for a task ID the caller has already resolved, as
// workbook show has: it reads no task.
func (s RelicService) LinkedToTask(ctx context.Context, taskID string, filter RelicListFilter) ([]RelicSummary, []Warning, error) {
	if s.Links == nil {
		return nil, nil, Errorf(CategoryOperational, "relic service has no link index")
	}
	return s.linkedToTarget(ctx, RelicLinkTarget{Task: taskID}, filter)
}

// linkedToTarget is LinkedTo for a resolved target: one reverse lookup, then
// the summaries, each judged as list judges it.
func (s RelicService) linkedToTarget(ctx context.Context, target RelicLinkTarget, filter RelicListFilter) ([]RelicSummary, []Warning, error) {
	lookup, err := s.Links.RelicLinksTo(ctx, s.Config, target)
	if err != nil {
		return nil, nil, err
	}
	listing, err := s.schemaListing(ctx)
	if err != nil {
		return nil, nil, err
	}
	summaries := []RelicSummary{}
	for _, source := range lookup.Sources {
		relic := source.Relic
		if relic.Deleted && !filter.IncludeDeleted || relic.Retired && !filter.IncludeRetired ||
			source.PartRemoved || filter.Type != "" && relic.Type != filter.Type {
			continue
		}
		if len(summaries) == 0 || summaries[len(summaries)-1].ID != relic.ID {
			relic.LinkedBy = nil
			schemaID := ""
			if relic.Schema != nil {
				schemaID = relic.Schema.ID
			}
			relic.Behind = RelicBehind(RelicData{Type: relic.Type, Schema: schemaID}, listing)
			if filter.Behind && !relic.Behind {
				continue
			}
			numberLinkedSchema(&relic, listing)
			if err := s.linkedConformance(ctx, &relic, schemaID, listing); err != nil {
				return nil, nil, err
			}
			summaries = append(summaries, relic)
		}
		last := &summaries[len(summaries)-1]
		last.LinkedBy = append(last.LinkedBy, RelicLinkMatch{
			Link: source.Link.ID, Part: source.Link.From, PartType: source.PartType, Rel: source.Link.Rel,
			ToPart: source.Link.To.Part, Pointer: source.Link.Pointer, Dropped: source.Dropped,
		})
	}
	return summaries, relicLinksUnreadWarnings(lookup.Unread), nil
}

// relicLinksUnreadWarnings is one warning per relic a lookup could not read,
// in relic ID order.
func relicLinksUnreadWarnings(unread []string) []Warning {
	sorted := append([]string(nil), unread...)
	sort.Strings(sorted)
	var warnings []Warning
	for _, relicID := range sorted {
		warnings = append(warnings, RelicLinksUnreadWarning(relicID))
	}
	return warnings
}

// relicIncomingLinks lists the live links other relics have to relicID, or to
// one of its parts: with whole, links to the relic itself and to the parts
// listed, and otherwise links to the one part listed. Links from removed parts
// and from deleted relics are left out, as LinkedTo leaves them out. One
// reverse lookup answers it; a service with no link index lists none.
func (s RelicService) relicIncomingLinks(ctx context.Context, relicID string, parts []RelicPart, whole bool) ([]RelicIncomingLink, []Warning, error) {
	if s.Links == nil {
		return nil, nil, nil
	}
	target := RelicLinkTarget{Relic: relicID}
	if !whole && len(parts) == 1 {
		target.Part = parts[0].ID
	}
	lookup, err := s.Links.RelicLinksTo(ctx, s.Config, target)
	if err != nil {
		return nil, nil, err
	}
	listed := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		listed[part.ID] = struct{}{}
	}
	var incoming []RelicIncomingLink
	for _, source := range lookup.Sources {
		if source.Relic.Deleted || source.PartRemoved {
			continue
		}
		toPart := source.Link.To.Part
		if _, partListed := listed[toPart]; toPart == "" && !whole || toPart != "" && !partListed {
			continue
		}
		incoming = append(incoming, RelicIncomingLink{
			Relic: source.Relic.ID, Title: source.Relic.Title, Type: source.Relic.Type,
			Link: source.Link.ID, From: source.Link.From, FromType: source.PartType, ToPart: toPart,
			Rel: source.Link.Rel, Seen: source.Link.Seen, Pointer: source.Link.Pointer,
		})
	}
	return incoming, relicLinksUnreadWarnings(lookup.Unread), nil
}

// TaskRelicLinks flattens LinkedTo's answer into the links workbook show
// lists, one per link.
func TaskRelicLinks(summaries []RelicSummary) []TaskRelicLink {
	var links []TaskRelicLink
	for _, summary := range summaries {
		for _, match := range summary.LinkedBy {
			links = append(links, TaskRelicLink{
				Relic: summary.ID, Title: summary.Title, Type: summary.Type,
				Part: match.Part, PartType: match.PartType, Link: match.Link, Rel: match.Rel,
				Retired: summary.Retired, Deleted: summary.Deleted,
				RemovedAt: RelicLinkRemovedAt(summary, match.Dropped),
			})
		}
	}
	return links
}

// linkedConformance fills how a linked relic stands, as list does: a
// judgment the lookup carried when it agrees with the listing, and otherwise
// the one list gives for the relic as the reader reads it, which is the
// judgment the index stored with the relic when it has one. A service with
// no schema store, as workbook show of a task has, judges nothing, and a
// relic that cannot be read now is listed unjudged rather than fail the list.
func (s RelicService) linkedConformance(ctx context.Context, summary *RelicSummary, schemaID string, listing RelicSchemaListing) error {
	if s.Schemas == nil {
		summary.RelicConformance = nil
		return nil
	}
	if stored := summary.RelicConformance; stored != nil && conformanceAgrees(*stored, schemaID, listing) {
		return nil
	}
	var conformance RelicConformance
	var err error
	if _, known := listing.ByID(schemaID); !known {
		conformance, err = JudgeRelic(RelicData{Schema: schemaID}, nil, nil, listing, nil)
	} else {
		var snapshot RelicSnapshot
		if snapshot, err = s.Reader.GetRelic(ctx, s.Config, summary.ID); err == nil {
			conformance, err = s.listedConformance(ctx, snapshot, listing)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		summary.RelicConformance = nil
		return nil
	}
	summary.RelicConformance = &conformance
	return nil
}
