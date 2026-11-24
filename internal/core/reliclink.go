package core

import "sort"

const (
	RelicOperationLinkAdd    RelicOperationType = "link.add"
	RelicOperationLinkRemove RelicOperationType = "link.remove"
)

// RelicLinkTarget is what a link points at. It has exactly one member: Task,
// a task ID, in this generation. A later generation may add relic and part
// targets as members of their own.
type RelicLinkTarget struct {
	Task string `json:"task,omitempty"`
}

// RelicLinkChange is what a link.add records: the link without its ID, which
// is the operation's own. From is a part ID, or empty for the whole relic.
type RelicLinkChange struct {
	From string          `json:"from,omitempty"`
	To   RelicLinkTarget `json:"to"`
	Rel  string          `json:"rel"`
}

// RelicLink is one live link as links.json lists it. Its ID is the ID of the
// link.add that made it, as a part's ID is its part.add's.
type RelicLink struct {
	ID   string          `json:"id"`
	From string          `json:"from,omitempty"`
	To   RelicLinkTarget `json:"to"`
	Rel  string          `json:"rel"`
}

// Link finds a live link by ID.
func (links RelicLinksDocument) Link(id string) (RelicLink, bool) {
	for _, link := range links.Links {
		if link.ID == id {
			return link, true
		}
	}
	return RelicLink{}, false
}

// ValidateRelicRelation checks a link's relationship name: a lowercase token,
// as a relic type name is, of at most MaxRelicRelationBytes.
func ValidateRelicRelation(rel string) error {
	return validateRelicToken("link relationship", rel, MaxRelicRelationBytes)
}

// ValidateRelicLinkTarget checks what a link points at: a task, named by a
// valid task ID.
func ValidateRelicLinkTarget(target RelicLinkTarget) error {
	if target.Task == "" {
		return Errorf(CategoryValidation, "a link must point at a task")
	}
	return ValidateTaskIDShape(target.Task)
}

// ValidateRelicLinks checks the links against the state stored beside them:
// every link from a part names a part the state lists, live or removed.
func ValidateRelicLinks(state RelicStateDocument, links RelicLinksDocument) error {
	for _, link := range links.Links {
		if link.From == "" {
			continue
		}
		if _, ok := state.Relic.Part(link.From); !ok {
			return corrupt("relic link %s is from part %s, which the relic does not have", link.ID, link.From)
		}
	}
	return nil
}

// validateRelicLinkChange checks one link's members, in what kind of thing
// they belong to ("link.add" or "relic link <id>"), as corrupt data.
func validateRelicLinkChange(what string, link RelicLinkChange) error {
	if link.From != "" {
		if err := ValidatePartID(link.From); err != nil {
			return Wrap(CategoryCorruptData, what+" is from an invalid part", err)
		}
	}
	if err := ValidateRelicLinkTarget(link.To); err != nil {
		return Wrap(CategoryCorruptData, what+" has an invalid target", err)
	}
	if err := ValidateRelicRelation(link.Rel); err != nil {
		return Wrap(CategoryCorruptData, what+" has an invalid relationship", err)
	}
	return nil
}

// validateRelicLinkList checks each stored link and that the list is sorted
// by ID with no ID twice. It never checks how many links there are.
func validateRelicLinkList(links []RelicLink) error {
	for index, link := range links {
		if err := validateCanonicalULID("relic link ID", link.ID); err != nil {
			return err
		}
		if index > 0 && links[index-1].ID >= link.ID {
			if links[index-1].ID == link.ID {
				return corrupt("relic links list link %s twice", link.ID)
			}
			return corrupt("relic links are not ordered by ID")
		}
		if err := validateRelicLinkChange("relic link "+link.ID, RelicLinkChange{From: link.From, To: link.To, Rel: link.Rel}); err != nil {
			return err
		}
	}
	return nil
}

// copyRelicLinks copies a links document so that folding onto the copy
// leaves the original untouched. Links is never nil in the copy.
func copyRelicLinks(links RelicLinksDocument) RelicLinksDocument {
	copied := EmptyRelicLinks()
	copied.Links = append(make([]RelicLink, 0, len(links.Links)), links.Links...)
	return copied
}

// applyRelicLinkOperation folds one validated link operation onto links.
//
// A link.add appends its link; the fold refuses one whose ID is taken (see
// refuseReusedRelicID) or whose from names a part the relic does not have. A
// link.remove drops the live link it names, and naming no live link changes
// nothing: the link was removed on another clone, or the remove was
// delivered twice.
func applyRelicLinkOperation(links *RelicLinksDocument, relic RelicData, operation RelicOperation) error {
	switch operation.Type {
	case RelicOperationLinkAdd:
		if from := operation.Link.From; from != "" {
			if _, ok := relic.Part(from); !ok {
				return corrupt("link.add %s is from part %s, which the relic does not have", operation.ID, from)
			}
		}
		links.Links = append(links.Links, RelicLink{
			ID:   operation.ID,
			From: operation.Link.From,
			To:   operation.Link.To,
			Rel:  operation.Link.Rel,
		})
	case RelicOperationLinkRemove:
		kept := links.Links[:0]
		for _, link := range links.Links {
			if link.ID != operation.LinkID {
				kept = append(kept, link)
			}
		}
		links.Links = kept
	}
	return nil
}

// sortRelicLinks orders links by ID, the order links.json stores.
func sortRelicLinks(links []RelicLink) {
	sort.Slice(links, func(a, b int) bool { return links[a].ID < links[b].ID })
}

// refuseReusedRelicLinkID refuses an operation that takes the ID of a live
// link, and a link.add that takes the ID of a part or a body's current
// version. An operation ID is used once in a relic's history; see
// refuseReusedRelicID.
func refuseReusedRelicLinkID(relic RelicData, links RelicLinksDocument, operation RelicOperation) error {
	switch operation.Type {
	case RelicOperationLinkAdd, RelicOperationPartAdd, RelicOperationPartBodySet:
		if _, exists := links.Link(operation.ID); exists {
			return corrupt("%s %s takes the ID of a link the relic has; an operation ID is used once", operation.Type, operation.ID)
		}
	}
	if operation.Type != RelicOperationLinkAdd {
		return nil
	}
	for _, part := range relic.Parts {
		if part.ID == operation.ID {
			return corrupt("link.add %s takes the ID of a part of the relic; an operation ID is used once", operation.ID)
		}
		if part.Body != nil && part.Body.Version == operation.ID {
			return corrupt("link.add %s takes the ID that is the body version of part %s; an operation ID is used once", operation.ID, part.ID)
		}
	}
	return nil
}

// relicTotalsOf is the counts and totals checkRelicGrowth judges, for a whole
// record: its parts and its live links.
func relicTotalsOf(record RelicRecord) relicPartTotals {
	totals := relicPartTotalsOf(record.State.Relic)
	totals.links = len(record.Links.Links)
	return totals
}
