package core

import (
	"context"
	"fmt"
	"sort"
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

// RelicLinkMatch is one link a listed relic has to the task asked about.
type RelicLinkMatch struct {
	Link     string `json:"link"`
	Part     string `json:"part,omitempty"`
	PartType string `json:"partType,omitempty"`
	Rel      string `json:"rel"`
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
}

// RelicLinkSourcesOf returns the links of one relic that point at target, as
// a reverse lookup reports them.
func RelicLinkSourcesOf(snapshot RelicSnapshot, target RelicLinkTarget) []RelicLinkSource {
	var sources []RelicLinkSource
	var summary *RelicSummary
	parts := snapshot.Record.State.Relic.Parts
	for _, link := range snapshot.Record.Links.Links {
		if link.To != target {
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

// LinkedTo lists the relics that link to a task, each with the links it has
// to it, in relic ID order. The task may be deleted. Links from removed parts
// are left out, and so are deleted relics unless filter.IncludeDeleted;
// filter.Type keeps only relics of that type. A relic the index cannot read
// is a warning, not a failure.
func (s RelicService) LinkedTo(ctx context.Context, taskIDOrPrefix string, filter RelicListFilter) ([]RelicSummary, []Warning, error) {
	if s.Tasks == nil {
		return nil, nil, errNoTaskReader()
	}
	if s.Links == nil {
		return nil, nil, Errorf(CategoryOperational, "relic service has no link index")
	}
	taskID, err := s.Tasks.Resolve(ctx, s.Config, taskIDOrPrefix)
	if err != nil {
		return nil, nil, err
	}
	return s.LinkedToTask(ctx, taskID, filter)
}

// LinkedToTask is LinkedTo for a task ID the caller has already resolved, as
// workbook show has: it reads no task.
func (s RelicService) LinkedToTask(ctx context.Context, taskID string, filter RelicListFilter) ([]RelicSummary, []Warning, error) {
	if s.Links == nil {
		return nil, nil, Errorf(CategoryOperational, "relic service has no link index")
	}
	lookup, err := s.Links.RelicLinksTo(ctx, s.Config, RelicLinkTarget{Task: taskID})
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
		if relic.Deleted && !filter.IncludeDeleted || source.PartRemoved || filter.Type != "" && relic.Type != filter.Type {
			continue
		}
		if len(summaries) == 0 || summaries[len(summaries)-1].ID != relic.ID {
			relic.LinkedBy = nil
			numberLinkedSchema(&relic, listing)
			summaries = append(summaries, relic)
		}
		last := &summaries[len(summaries)-1]
		last.LinkedBy = append(last.LinkedBy, RelicLinkMatch{
			Link: source.Link.ID, Part: source.Link.From, PartType: source.PartType, Rel: source.Link.Rel,
		})
	}
	unread := append([]string(nil), lookup.Unread...)
	sort.Strings(unread)
	var warnings []Warning
	for _, relicID := range unread {
		warnings = append(warnings, RelicLinksUnreadWarning(relicID))
	}
	return summaries, warnings, nil
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
			})
		}
	}
	return links
}
