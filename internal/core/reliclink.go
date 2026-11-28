package core

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const (
	RelicOperationLinkAdd    RelicOperationType = "link.add"
	RelicOperationLinkRemove RelicOperationType = "link.remove"
)

// RelicLinkTarget is what a link points at: a task, named by Task, or a
// relic, named by Relic, and optionally one of its parts, named by Part. It
// has exactly one of Task or Relic, and Part only with Relic.
type RelicLinkTarget struct {
	Task  string `json:"task,omitempty"`
	Relic string `json:"relic,omitempty"`
	Part  string `json:"part,omitempty"`
}

// Address is the target as a command names it: the task ID, the relic ID, or
// <relic>#<part>.
func (t RelicLinkTarget) Address() string {
	switch {
	case t.Task != "":
		return t.Task
	case t.Part != "":
		return t.Relic + "#" + t.Part
	}
	return t.Relic
}

// Covers reports whether a lookup for t finds a link to other: other is t,
// or t is a whole relic and other is that relic or one of its parts.
func (t RelicLinkTarget) Covers(other RelicLinkTarget) bool {
	if t == other {
		return true
	}
	return t.Relic != "" && t.Part == "" && other.Relic == t.Relic
}

// RelicLinkChange is what a link.add records: the link without its ID, which
// is the operation's own. From is a part ID, or empty for the whole relic.
// Seen is the last operation ID of the target relic when the link was made,
// present exactly when the target is a relic. Pointer is an optional JSON
// object locating something inside the target, which Workbook stores and
// does not interpret.
type RelicLinkChange struct {
	From    string          `json:"from,omitempty"`
	To      RelicLinkTarget `json:"to"`
	Rel     string          `json:"rel"`
	Seen    string          `json:"seen,omitempty"`
	Pointer json.RawMessage `json:"pointer,omitempty"`
}

// RelicLink is one live link as links.json lists it. Its ID is the ID of the
// link.add that made it, as a part's ID is its part.add's.
type RelicLink struct {
	ID      string          `json:"id"`
	From    string          `json:"from,omitempty"`
	To      RelicLinkTarget `json:"to"`
	Rel     string          `json:"rel"`
	Seen    string          `json:"seen,omitempty"`
	Pointer json.RawMessage `json:"pointer,omitempty"`
}

// change is the link without its ID, as the link.add that made it records it.
func (link RelicLink) change() RelicLinkChange {
	return RelicLinkChange{From: link.From, To: link.To, Rel: link.Rel, Seen: link.Seen, Pointer: link.Pointer}
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

// ValidateRelicLinkTarget checks what a link points at: exactly one of a
// task or a relic, a part only with its relic, each a valid ID.
func ValidateRelicLinkTarget(target RelicLinkTarget) error {
	switch {
	case target.Part != "" && target.Relic == "":
		return Errorf(CategoryValidation, "a link to a part must name its relic")
	case target.Task != "" && target.Relic != "":
		return Errorf(CategoryValidation, "a link points at a task or a relic, not both")
	case target.Task != "":
		return ValidateTaskIDShape(target.Task)
	case target.Relic == "":
		return Errorf(CategoryValidation, "a link must point at a task or a relic")
	}
	if err := ValidateRelicIDShape(target.Relic); err != nil {
		return err
	}
	if target.Part != "" {
		return ValidatePartID(target.Part)
	}
	return nil
}

// CanonicalRelicLinkPointer turns a pointer as given into its stored form:
// the canonical form CanonicalRelicValue gives, which must be a JSON object
// of at most MaxRelicLinkPointerBytes.
func CanonicalRelicLinkPointer(raw []byte) (json.RawMessage, error) {
	canonical, err := CanonicalRelicValue(raw)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 || canonical[0] != '{' {
		return nil, Errorf(CategoryValidation, "a link pointer must be a JSON object")
	}
	if len(canonical) > MaxRelicLinkPointerBytes {
		return nil, relicSizeError("the link pointer", len(canonical), MaxRelicLinkPointerBytes)
	}
	return canonical, nil
}

// validateStoredRelicLinkPointer requires a stored pointer to be within the
// limit, judged first from the stored bytes alone, and exactly its canonical
// form.
func validateStoredRelicLinkPointer(pointer json.RawMessage) error {
	if len(pointer) > MaxRelicLinkPointerBytes {
		return relicSizeError("the link pointer", len(pointer), MaxRelicLinkPointerBytes)
	}
	canonical, err := CanonicalRelicLinkPointer(pointer)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, pointer) {
		return Errorf(CategoryValidation, "the link pointer is not in canonical form")
	}
	return nil
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
	switch {
	case link.To.Relic != "" && link.Seen == "":
		return corrupt("%s: a link to a relic must record the version it saw", what)
	case link.To.Relic == "" && link.Seen != "":
		return corrupt("%s: only a link to a relic records the version it saw", what)
	case link.Seen != "":
		if err := validateCanonicalULID(what+" seen version", link.Seen); err != nil {
			return err
		}
	}
	if link.Pointer != nil {
		if err := validateStoredRelicLinkPointer(link.Pointer); err != nil {
			return Wrap(CategoryCorruptData, what+" has an invalid pointer", err)
		}
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
		if err := validateRelicLinkChange("relic link "+link.ID, link.change()); err != nil {
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
			ID:      operation.ID,
			From:    operation.Link.From,
			To:      operation.Link.To,
			Rel:     operation.Link.Rel,
			Seen:    operation.Link.Seen,
			Pointer: operation.Link.Pointer,
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
	case RelicOperationLinkAdd, RelicOperationPartAdd, RelicOperationPartBodySet, RelicOperationPartBodyMerge:
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

// RelicRelationship is a link relationship name and how a link with it reads,
// from the relic or part that links to what it points at.
type RelicRelationship struct{ Name, Meaning string }

// DefaultRelicRelationships are the relationship names every relic may give a
// new link, in the order Workbook lists them. A schema's links.relationships
// adds names to these and cannot remove one. A link made before a name was
// checked keeps whatever name it has.
var DefaultRelicRelationships = []RelicRelationship{
	{"implemented-by", "This part of a spec or plan is built by that task."},
	{"derived-from", "This was drawn from that source."},
	{"summarizes", "This is a summary of that."},
	{"decided-in", "This decision was made in that meeting or transcript turn."},
	{"changes", "This decision changes that part of a spec."},
	{"supersedes", "This relic replaces that one."},
	{"relates-to", "A general connection with no stronger meaning."},
}

// DefaultRelicRelationNames returns the default relationship names in order,
// as a new list.
func DefaultRelicRelationNames() []string {
	names := make([]string, len(DefaultRelicRelationships))
	for index, relationship := range DefaultRelicRelationships {
		names[index] = relationship.Name
	}
	return names
}

// CheckOpenRelicRelation refuses a new link's relationship on a relic that
// names no schema, which may use the default names only. relicType is the
// relic's type, named so the refusal can say how to add one.
func CheckOpenRelicRelation(relicType, rel string) error {
	if containsString(DefaultRelicRelationNames(), rel) {
		return nil
	}
	return Errorf(CategoryValidation,
		"link relationship %s is not one Workbook knows; use one of %s, or save a schema for %s whose links.relationships adds it",
		strconv.Quote(rel), strings.Join(DefaultRelicRelationNames(), ", "), relicType)
}
