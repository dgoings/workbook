package core

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"
	"unicode/utf8"
)

// RelicBodyIsText reports whether a body is text: its bytes are valid UTF-8.
// Formats are free labels, so content decides, as relic show and the conflict
// report decide.
func RelicBodyIsText(content []byte) bool {
	return utf8.Valid(content)
}

// RelicValueChange is one named value compared between two versions: a
// relic-level member, a property or a field. From and To are the stored,
// canonical values, left out on the side the value is absent.
type RelicValueChange struct {
	Name string          `json:"name"`
	Kind ChangeKind      `json:"kind"`
	From json.RawMessage `json:"from,omitempty"`
	To   json.RawMessage `json:"to,omitempty"`
}

// RelicPartMove reports a part whose position changed: its positions, and its
// index among the live parts on each side, -1 on a side where it is not live.
type RelicPartMove struct {
	From      string `json:"from"`
	To        string `json:"to"`
	FromIndex int    `json:"fromIndex"`
	ToIndex   int    `json:"toIndex"`
}

// RelicBodyDiff compares a part's body. From and To are null where the part
// has no body. Text is false when either side is not text, and then there are
// no hunks; Approximate reports a line diff past the edit bound.
type RelicBodyDiff struct {
	From        *RelicBody      `json:"from"`
	To          *RelicBody      `json:"to"`
	Text        bool            `json:"text"`
	Approximate bool            `json:"approximate,omitempty"`
	Hunks       []RelicDiffHunk `json:"hunks,omitempty"`
}

// RelicPartChanged is the kind of a part diff whose part is live, or removed,
// on both sides and differs in position, fields or body. The other kinds are
// ChangeAdded, ChangeRemoved and ChangeRestored.
const RelicPartChanged ChangeKind = "changed"

// RelicPartDiff is one part that differs between two versions. Kind is added
// (absent on the from side), removed (live there and removed, or absent, on the
// to side), restored (removed there and live here) or changed.
type RelicPartDiff struct {
	ID     string             `json:"id"`
	Type   string             `json:"type"`
	Kind   ChangeKind         `json:"kind"`
	Moved  *RelicPartMove     `json:"moved,omitempty"`
	Fields []RelicValueChange `json:"fields,omitempty"`
	Body   *RelicBodyDiff     `json:"body,omitempty"`
}

// RelicLinksDiff lists the links one version has and the other lacks, each
// sorted by ID.
type RelicLinksDiff struct {
	Added   []RelicLink `json:"added,omitempty"`
	Removed []RelicLink `json:"removed,omitempty"`
}

// RelicDiffPoint names one side of a diff: its version, commit, and the actor
// and wall time of that commit's change.
type RelicDiffPoint struct {
	Version  string    `json:"version"`
	Commit   string    `json:"commit"`
	Actor    string    `json:"actor"`
	WallTime time.Time `json:"wallTime"`
}

// RelicDiff compares two points of one relic's history, in the order given.
// From is null for the side before the relic. Changes holds the relic-level
// members (title, schema, retired, deleted); it, Properties and Links are left
// out of a part's diff.
type RelicDiff struct {
	Relic      string             `json:"relic"`
	Part       string             `json:"part,omitempty"`
	From       *RelicDiffPoint    `json:"from"`
	To         RelicDiffPoint     `json:"to"`
	Changes    []RelicValueChange `json:"changes,omitempty"`
	Properties []RelicValueChange `json:"properties,omitempty"`
	Parts      []RelicPartDiff    `json:"parts,omitempty"`
	Links      *RelicLinksDiff    `json:"links,omitempty"`

	// unread records a changed body the comparison was not given; see Err.
	unread error
}

// Err reports that the comparison could not be made: a body that differs
// between the sides was not read on a side that has one. That is a fault of
// the caller, and the diff must not be shown, because it would lack the change.
func (d RelicDiff) Err() error {
	return d.unread
}

// RelicVersionSide is one side of a comparison: the record at that point, nil
// for the side before the relic; the objects each part is stored in; and the
// content of the parts read, which RelicPartsToRead chooses.
type RelicVersionSide struct {
	Record  *RelicRecord
	Objects map[string]RelicPartObjectIDs
	Content map[string]RelicPartContent
}

// part returns the side's part with the ID, if it has one.
func (side RelicVersionSide) part(id string) (RelicPart, bool) {
	if side.Record == nil {
		return RelicPart{}, false
	}
	return side.Record.State.Relic.Part(id)
}

// parts returns the side's parts in state order: position, then ID.
func (side RelicVersionSide) parts() []RelicPart {
	if side.Record == nil {
		return nil
	}
	return side.Record.State.Relic.Parts
}

// comparedPartIDs lists the parts a comparison looks at: the one asked for,
// or every part of either side, the to side's in its order and then those only
// the from side has, in its order.
func comparedPartIDs(from, to RelicVersionSide, partID string) []string {
	if partID != "" {
		return []string{partID}
	}
	ids := make([]string, 0, len(to.parts()))
	seen := map[string]bool{}
	for _, part := range to.parts() {
		ids = append(ids, part.ID)
		seen[part.ID] = true
	}
	for _, part := range from.parts() {
		if !seen[part.ID] {
			ids = append(ids, part.ID)
		}
	}
	return ids
}

// relicBodyFormat is a part's body format, empty without a body.
func relicBodyFormat(part RelicPart, exists bool) (string, bool) {
	if !exists || part.Body == nil {
		return "", false
	}
	return part.Body.Format, true
}

// RelicPartsToRead returns, for each side, the parts whose content a
// comparison must read: those whose fields or body object differs from the
// other side's, including a part only one side has. Equal object IDs mean
// equal content, so those are never read, with one exception: a body whose
// bytes are the same on both sides under a new format label is listed on the
// to side alone, so the diff can tell whether the body is text. Whether a
// listed part needs its body read is whether its body object, or its format,
// differs; see RelicBodiesToRead.
func RelicPartsToRead(from, to RelicVersionSide, partID string) (fromParts, toParts []string) {
	for _, id := range comparedPartIDs(from, to, partID) {
		fromPart, onFrom := from.part(id)
		toPart, onTo := to.part(id)
		fromObjects, toObjects := from.Objects[id], to.Objects[id]
		switch {
		case onFrom && onTo:
			if fromObjects != toObjects {
				fromParts = append(fromParts, id)
				toParts = append(toParts, id)
				continue
			}
			fromFormat, _ := relicBodyFormat(fromPart, onFrom)
			toFormat, hasBody := relicBodyFormat(toPart, onTo)
			if hasBody && fromFormat != toFormat {
				toParts = append(toParts, id)
			}
		case onFrom:
			fromParts = append(fromParts, id)
		case onTo:
			toParts = append(toParts, id)
		}
	}
	return fromParts, toParts
}

// RelicBodiesToRead reports whether reading the parts RelicPartsToRead lists
// for a side needs their bodies: whether any of them has a body whose object
// or format differs from the other side's.
func RelicBodiesToRead(side, other RelicVersionSide, parts []string) bool {
	for _, id := range parts {
		part, exists := side.part(id)
		format, hasBody := relicBodyFormat(part, exists)
		if !hasBody {
			continue
		}
		otherPart, otherExists := other.part(id)
		otherFormat, _ := relicBodyFormat(otherPart, otherExists)
		if side.Objects[id].Body != other.Objects[id].Body || format != otherFormat {
			return true
		}
	}
	return false
}

// CompareRelicVersions compares two versions of a relic, or one part of it when
// partID is set, from the first side to the second. The caller fills in the
// points. Only parts that differ are listed, and the content of a part is read
// from the sides' Content, which must hold what RelicPartsToRead lists; the
// diff's Err reports a changed body that is not there.
func CompareRelicVersions(from, to RelicVersionSide, partID string, context int) RelicDiff {
	diff := RelicDiff{Part: partID}
	if to.Record != nil {
		diff.Relic = to.Record.State.RelicID
	} else if from.Record != nil {
		diff.Relic = from.Record.State.RelicID
	}
	for _, id := range comparedPartIDs(from, to, partID) {
		part, differs, err := compareRelicPart(from, to, id, context)
		if err != nil {
			diff.unread = err
		}
		if differs {
			diff.Parts = append(diff.Parts, part)
		}
	}
	if partID != "" {
		return diff
	}
	var fromData, toData *RelicData
	var fromLinks, toLinks []RelicLink
	if from.Record != nil {
		fromData, fromLinks = &from.Record.State.Relic, from.Record.Links.Links
	}
	if to.Record != nil {
		toData, toLinks = &to.Record.State.Relic, to.Record.Links.Links
	}
	diff.Changes = compareRelicMembers(fromData, toData)
	diff.Properties = compareRelicValues(relicProperties(fromData), relicProperties(toData))
	diff.Links = compareRelicLinks(fromLinks, toLinks)
	return diff
}

func relicProperties(data *RelicData) map[string]json.RawMessage {
	if data == nil {
		return nil
	}
	return data.Properties
}

// compareRelicMembers compares the relic-level members a diff reports. The side
// before the relic has no title and no schema, and is neither retired nor
// deleted: a flag is always a value, so it is only ever set.
func compareRelicMembers(from, to *RelicData) []RelicValueChange {
	values := func(data *RelicData) (title, schema json.RawMessage, retired, deleted bool) {
		if data == nil {
			return nil, nil, false, false
		}
		title, _ = json.Marshal(data.Title)
		if len(data.Schema) > 0 && string(data.Schema) != "null" {
			schema = data.Schema
		}
		return title, schema, data.Retired, data.Deleted
	}
	fromTitle, fromSchema, fromRetired, fromDeleted := values(from)
	toTitle, toSchema, toRetired, toDeleted := values(to)
	var changes []RelicValueChange
	if change, differs := compareRelicValue("title", fromTitle, fromTitle != nil, toTitle, toTitle != nil); differs {
		changes = append(changes, change)
	}
	if change, differs := compareRelicValue("schema", fromSchema, fromSchema != nil, toSchema, toSchema != nil); differs {
		changes = append(changes, change)
	}
	flag := func(value bool) json.RawMessage {
		if value {
			return json.RawMessage("true")
		}
		return json.RawMessage("false")
	}
	if fromRetired != toRetired {
		changes = append(changes, RelicValueChange{Name: "retired", Kind: ChangeSet, From: flag(fromRetired), To: flag(toRetired)})
	}
	if fromDeleted != toDeleted {
		changes = append(changes, RelicValueChange{Name: "deleted", Kind: ChangeSet, From: flag(fromDeleted), To: flag(toDeleted)})
	}
	return changes
}

// compareRelicValue compares one named value.
func compareRelicValue(name string, from json.RawMessage, onFrom bool, to json.RawMessage, onTo bool) (RelicValueChange, bool) {
	switch {
	case onFrom && onTo:
		if bytes.Equal(from, to) {
			return RelicValueChange{}, false
		}
		return RelicValueChange{Name: name, Kind: ChangeSet, From: from, To: to}, true
	case onFrom:
		return RelicValueChange{Name: name, Kind: ChangeRemoved, From: from}, true
	case onTo:
		return RelicValueChange{Name: name, Kind: ChangeAdded, To: to}, true
	}
	return RelicValueChange{}, false
}

// compareRelicValues compares two maps of stored values, by name. Stored values
// are canonical, so equal values have equal bytes.
func compareRelicValues(from, to map[string]json.RawMessage) []RelicValueChange {
	names := make([]string, 0, len(from)+len(to))
	for name := range from {
		names = append(names, name)
	}
	for name := range to {
		if _, ok := from[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var changes []RelicValueChange
	for _, name := range names {
		fromValue, onFrom := from[name]
		toValue, onTo := to[name]
		if change, differs := compareRelicValue(name, fromValue, onFrom, toValue, onTo); differs {
			changes = append(changes, change)
		}
	}
	return changes
}

// compareRelicLinks lists the links added and removed. A link never changes
// once made, so links compare by ID.
func compareRelicLinks(from, to []RelicLink) *RelicLinksDiff {
	fromIDs, toIDs := map[string]bool{}, map[string]bool{}
	for _, link := range from {
		fromIDs[link.ID] = true
	}
	for _, link := range to {
		toIDs[link.ID] = true
	}
	diff := RelicLinksDiff{}
	for _, link := range to {
		if !fromIDs[link.ID] {
			diff.Added = append(diff.Added, link)
		}
	}
	for _, link := range from {
		if !toIDs[link.ID] {
			diff.Removed = append(diff.Removed, link)
		}
	}
	if diff.Added == nil && diff.Removed == nil {
		return nil
	}
	byID := func(links []RelicLink) {
		sort.Slice(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	}
	byID(diff.Added)
	byID(diff.Removed)
	return &diff
}

// liveIndex is the part's index among the side's live parts, -1 when it is not
// live there.
func liveIndex(side RelicVersionSide, id string) int {
	index := 0
	for _, part := range side.parts() {
		if part.Removed {
			continue
		}
		if part.ID == id {
			return index
		}
		index++
	}
	return -1
}

// compareRelicPart compares one part, reporting whether it differs, and an
// error when its changed body was not read.
func compareRelicPart(from, to RelicVersionSide, id string, context int) (RelicPartDiff, bool, error) {
	fromPart, onFrom := from.part(id)
	toPart, onTo := to.part(id)
	if !onFrom && !onTo {
		return RelicPartDiff{}, false, nil
	}
	diff := RelicPartDiff{ID: id}
	var err error
	switch {
	case !onFrom:
		diff.Type, diff.Kind = toPart.Type, ChangeAdded
	case !onTo:
		diff.Type, diff.Kind = fromPart.Type, ChangeRemoved
	default:
		diff.Type = toPart.Type
		switch {
		case !fromPart.Removed && toPart.Removed:
			diff.Kind = ChangeRemoved
		case fromPart.Removed && !toPart.Removed:
			diff.Kind = ChangeRestored
		default:
			diff.Kind = RelicPartChanged
		}
		if fromPart.Position != toPart.Position {
			diff.Moved = &RelicPartMove{
				From: fromPart.Position, To: toPart.Position,
				FromIndex: liveIndex(from, id), ToIndex: liveIndex(to, id),
			}
		}
	}

	fromObjects, toObjects := from.Objects[id], to.Objects[id]
	if !onFrom || !onTo || fromObjects.Fields != toObjects.Fields {
		diff.Fields = compareRelicValues(from.Content[id].Fields, to.Content[id].Fields)
	}
	fromFormat, fromHasBody := relicBodyFormat(fromPart, onFrom)
	toFormat, toHasBody := relicBodyFormat(toPart, onTo)
	if fromHasBody != toHasBody || fromFormat != toFormat || (fromHasBody && fromObjects.Body != toObjects.Body) {
		diff.Body, err = compareRelicBodies(from, to, id, fromPart, toPart, context)
	}
	if diff.Kind == RelicPartChanged && diff.Moved == nil && diff.Fields == nil && diff.Body == nil {
		return RelicPartDiff{}, false, nil
	}
	return diff, true, err
}

// compareRelicBodies compares a part's body on two sides that differ in
// presence, format or bytes. Bytes on a side whose content was not read are
// the other side's: RelicPartsToRead leaves a side unread only when its body
// object is the other's. When the objects differ, a side that has a body must
// have been read with it.
func compareRelicBodies(from, to RelicVersionSide, id string, fromPart, toPart RelicPart, context int) (*RelicBodyDiff, error) {
	diff := &RelicBodyDiff{}
	if fromPart.Body != nil {
		body := *fromPart.Body
		diff.From = &body
	}
	if toPart.Body != nil {
		body := *toPart.Body
		diff.To = &body
	}
	fromContent, fromRead := from.Content[id]
	toContent, toRead := to.Content[id]
	if from.Objects[id].Body != to.Objects[id].Body &&
		(diff.From != nil && !fromContent.HasBody || diff.To != nil && !toContent.HasBody) {
		return diff, Errorf(CategoryOperational,
			"relic diff of part %s was not given a body that changed", id)
	}
	fromBytes, toBytes := fromContent.Body, toContent.Body
	if !fromRead && diff.From != nil {
		fromBytes = toBytes
	}
	if !toRead && diff.To != nil {
		toBytes = fromBytes
	}
	diff.Text = RelicBodyIsText(fromBytes) && RelicBodyIsText(toBytes)
	if diff.Text {
		diff.Hunks, diff.Approximate = LineDiff(fromBytes, toBytes, context)
	}
	return diff, nil
}
