package core

import (
	"bytes"
	"encoding/json"
	"sort"
)

// refuseNewerRelicWriter refuses a relic whose parent state or pack needs a
// newer reader. Both sides are checked, as for tasks: a newer pack further back
// leaves its watermark in every state after it.
func refuseNewerRelicWriter(parent *RelicRecord, pack RelicOperationPack) error {
	if parent != nil && parent.State.RequiresNewerReader() {
		return newerWriterRelic(parent.State.RelicID)
	}
	if pack.RequiresNewerReader() {
		return newerWriterRelic(pack.RelicID)
	}
	return nil
}

// foldedRelicMinReader carries the history's generation watermark forward: it
// is the larger of the parent's and the pack's, never lower than either.
func foldedRelicMinReader(parent *RelicRecord, pack RelicOperationPack) int {
	generation := pack.MinReader
	if parent != nil && parent.State.MinReader > generation {
		generation = parent.State.MinReader
	}
	return generation
}

// ApplyRelic validates one relic operation pack and folds it onto its parent's
// state, or starts a relic when there is no parent.
//
// The fold is tolerant where an operation can only be stale: a body edit from
// a base that is not the part's current version, an operation naming a part
// the relic does not have, removing a removed part, restoring a live part and
// clearing an absent name all change nothing, and the operation stays in
// history. So does removing a link the relic does not have. Tombstone and
// restore keep the task rules and are corrupt data when misused.
//
// The parent's links are carried forward, link operations apply in order,
// and the links are kept sorted by ID.
//
// It is not tolerant of an ID used twice where the state shows it: see
// refuseReusedRelicID.
func ApplyRelic(parent *RelicRecord, pack RelicOperationPack) (RelicRecord, error) {
	// The generation gate runs before anything else, so a newer document is
	// reported as newer rather than judged by rules this build may not know.
	if err := refuseNewerRelicWriter(parent, pack); err != nil {
		return RelicRecord{}, err
	}
	if err := validateRelicOperationPackDocument(pack); err != nil {
		return RelicRecord{}, err
	}

	var (
		relic      RelicData
		links      RelicLinksDocument
		history    History
		operations []RelicOperation
	)
	if parent == nil {
		root, err := startRelic(pack)
		if err != nil {
			return RelicRecord{}, err
		}
		relic = root
		links = EmptyRelicLinks()
		history = History{Generation: pack.HistoryGeneration}
		operations = pack.Operations[1:]
	} else {
		if err := checkRelicChild(parent.State, pack); err != nil {
			return RelicRecord{}, err
		}
		relic = copyRelicData(parent.State.Relic)
		links = copyRelicLinks(parent.Links)
		history = parent.State.History
		operations = pack.Operations
	}

	for _, operation := range operations {
		if relic.Deleted && operation.Type != RelicOperationRestore {
			return RelicRecord{}, corrupt("cannot change a deleted relic")
		}
		if err := refuseReusedRelicID(relic, operation); err != nil {
			return RelicRecord{}, err
		}
		if err := refuseReusedRelicLinkID(relic, links, operation); err != nil {
			return RelicRecord{}, err
		}
		applyRelicOperation(&relic, operation)
		if err := applyRelicLinkOperation(&links, relic, operation); err != nil {
			return RelicRecord{}, err
		}
	}
	sortRelicLinks(links.Links)

	if err := sortRelicParts(relic.Parts); err != nil {
		return RelicRecord{}, Wrap(CategoryCorruptData, "relic operation pack produced an invalid relic", err)
	}
	relic.UpdatedAt = pack.WallTime
	state := RelicStateDocument{
		Format:       relicStateFormat,
		Version:      documentVersion,
		MinReader:    foldedRelicMinReader(parent, pack),
		ProjectID:    pack.ProjectID,
		RelicID:      pack.RelicID,
		History:      history,
		LogicalClock: pack.LogicalClock,
		Relic:        relic,
	}
	if err := validateRelicStateDocument(state); err != nil {
		return RelicRecord{}, Wrap(CategoryCorruptData, "relic operation pack produced an invalid relic", err)
	}
	if err := validateRelicLinksDocument(links); err != nil {
		return RelicRecord{}, Wrap(CategoryCorruptData, "relic operation pack produced invalid links", err)
	}
	if err := ValidateRelicLinks(state, links); err != nil {
		return RelicRecord{}, Wrap(CategoryCorruptData, "relic operation pack produced invalid links", err)
	}
	return RelicRecord{State: state, Links: links}, nil
}

// startRelic checks a root pack and builds the relic its relic.create
// describes, before the rest of the pack applies.
func startRelic(pack RelicOperationPack) (RelicData, error) {
	if pack.LogicalClock != 1 {
		return RelicData{}, corrupt("root relic operation pack logical clock must be 1")
	}
	if pack.Operations[0].Type != RelicOperationCreate {
		return RelicData{}, corrupt("root relic operation pack must open with relic.create")
	}
	for _, operation := range pack.Operations[1:] {
		switch operation.Type {
		case RelicOperationCreate:
			return RelicData{}, corrupt("a relic operation pack must contain only one relic.create")
		case RelicOperationTombstone, RelicOperationRestore:
			return RelicData{}, corrupt("root relic operation pack must not contain %s", operation.Type)
		}
	}
	create := pack.Operations[0]
	return RelicData{
		Title:      create.Relic.Title,
		Type:       create.Relic.Type,
		Author:     pack.Actor.ID,
		CreatedAt:  create.Relic.CreatedAt,
		Properties: map[string]json.RawMessage{},
		Parts:      []RelicPart{},
	}, nil
}

// checkRelicChild checks a pack against its parent's state: the same relic and
// history, the next clock, and the task rules for tombstone and restore.
func checkRelicChild(parent RelicStateDocument, pack RelicOperationPack) error {
	if err := validateRelicStateDocument(parent); err != nil {
		return err
	}
	if parent.ProjectID != pack.ProjectID {
		return corrupt("relic operation pack project ID does not match parent")
	}
	if parent.RelicID != pack.RelicID {
		return corrupt("relic operation pack relic ID does not match parent")
	}
	if parent.History.Generation != pack.HistoryGeneration {
		return corrupt("relic operation pack history generation does not match parent")
	}
	if pack.LogicalClock != parent.LogicalClock+1 {
		return corrupt("relic operation pack logical clock must advance parent by one")
	}
	for index, operation := range pack.Operations {
		switch operation.Type {
		case RelicOperationCreate:
			return corrupt("relic.create requires no parent")
		case RelicOperationRestore:
			if !parent.Relic.Deleted {
				return corrupt("relic.restore requires a deleted relic")
			}
			if index != 0 {
				return corrupt("relic.restore must be the first operation in its pack")
			}
		}
	}
	// A pack against a deleted relic restores it first and may then edit it,
	// but a pack restores or deletes, never both.
	if parent.Relic.Deleted {
		if pack.Operations[0].Type != RelicOperationRestore {
			return corrupt("cannot change a deleted relic")
		}
		for _, operation := range pack.Operations[1:] {
			if operation.Type == RelicOperationTombstone {
				return corrupt("relic.restore must not be followed by relic.tombstone")
			}
		}
	}
	return nil
}

// refuseReusedRelicID refuses an operation that takes an ID the relic's state
// already uses for something else. An operation ID is used once in a relic's
// history, and two of those uses are names the state keeps: a part's ID, which
// is the ID of the part.add that made it and the version of its first body,
// and a body's version, which is the ID of the operation that saved it.
//
// A part.body.set that took a part's ID would give a body a version that
// already named other text, so a later save from that version would overwrite
// text its author never saw. A part.add that took a part's ID or a body's
// current version would make one name stand for two things the same way. The
// state does not keep a body's earlier versions or any other operation's ID,
// so the fold cannot see those reused; the full history walk does. A live
// link's ID is refused by refuseReusedRelicLinkID.
func refuseReusedRelicID(relic RelicData, operation RelicOperation) error {
	switch operation.Type {
	case RelicOperationPartBodySet:
		if _, exists := relic.Part(operation.ID); exists {
			return corrupt("part.body.set %s takes the ID of a part of the relic; an operation ID is used once", operation.ID)
		}
	case RelicOperationPartAdd:
		for _, part := range relic.Parts {
			if part.ID == operation.ID {
				return corrupt("part.add %s takes the ID of a part the relic already has; an operation ID is used once", operation.ID)
			}
			if part.Body != nil && part.Body.Version == operation.ID {
				return corrupt("part.add %s takes the ID that is the body version of part %s; an operation ID is used once", operation.ID, part.ID)
			}
		}
	}
	return nil
}

// applyRelicOperation folds one validated operation. Operations on a removed
// part apply as on a live one; field operations change only the tree.
func applyRelicOperation(relic *RelicData, operation RelicOperation) {
	switch operation.Type {
	case RelicOperationTitleSet:
		relic.Title = operation.Title
	case RelicOperationTombstone:
		relic.Deleted = true
	case RelicOperationRestore:
		relic.Deleted = false
	case RelicOperationPropertySet:
		relic.Properties[operation.Name] = operation.Value
	case RelicOperationPropertyClear:
		delete(relic.Properties, operation.Name)
	case RelicOperationPartAdd:
		if _, exists := relic.Part(operation.ID); exists {
			return
		}
		part := RelicPart{ID: operation.ID, Type: operation.PartType, Position: operation.Position}
		if operation.Body != nil {
			part.Body = &RelicBody{Format: operation.Body.Format, Version: operation.ID, Bytes: operation.Body.Bytes}
		}
		relic.Parts = append(relic.Parts, part)
	case RelicOperationPartBodySet:
		part := relicPartRef(relic, operation.Part)
		if part == nil {
			return
		}
		current := ""
		if part.Body != nil {
			current = part.Body.Version
		}
		// An edit made from a version that is no longer current changes
		// nothing; it stays in history, and the newer body stands.
		if operation.Body.Base != current {
			return
		}
		part.Body = &RelicBody{Format: operation.Body.Format, Version: operation.ID, Bytes: operation.Body.Bytes}
	case RelicOperationPartMove:
		if part := relicPartRef(relic, operation.Part); part != nil {
			part.Position = operation.Position
		}
	case RelicOperationPartRemove:
		if part := relicPartRef(relic, operation.Part); part != nil {
			part.Removed = true
		}
	case RelicOperationPartRestore:
		if part := relicPartRef(relic, operation.Part); part != nil {
			part.Removed = false
		}
	case RelicOperationPartFieldSet, RelicOperationPartFieldClear:
		// Fields live in the commit's tree; see ApplyRelicFields.
	case RelicOperationLinkAdd, RelicOperationLinkRemove:
		// Links live in links.json; see applyRelicLinkOperation.
	}
}

// relicPartRef returns the part with the ID for changing in place, or nil.
func relicPartRef(relic *RelicData, id string) *RelicPart {
	for index := range relic.Parts {
		if relic.Parts[index].ID == id {
			return &relic.Parts[index]
		}
	}
	return nil
}

// sortRelicParts orders parts by position, then ID.
func sortRelicParts(parts []RelicPart) error {
	placements := make([]PartPlacement, len(parts))
	byID := make(map[string]RelicPart, len(parts))
	for index, part := range parts {
		placements[index] = PartPlacement{ID: part.ID, Position: part.Position}
		byID[part.ID] = part
	}
	if err := SortPartPlacements(placements); err != nil {
		return err
	}
	for index, placement := range placements {
		parts[index] = byID[placement.ID]
	}
	return nil
}

// copyRelicData copies relic data deeply enough that folding onto the copy
// leaves the original untouched.
func copyRelicData(data RelicData) RelicData {
	copied := data
	copied.Properties = make(map[string]json.RawMessage, len(data.Properties))
	for name, value := range data.Properties {
		copied.Properties[name] = value
	}
	copied.Parts = make([]RelicPart, len(data.Parts))
	for index, part := range data.Parts {
		copied.Parts[index] = part
		if part.Body != nil {
			body := *part.Body
			copied.Parts[index].Body = &body
		}
	}
	return copied
}

// ValidateRelicCheckpoint verifies that a stored record is exactly what
// folding the pack onto the parent computes, byte for byte.
func ValidateRelicCheckpoint(parent *RelicRecord, pack RelicOperationPack, stored RelicRecord) error {
	computed, err := ApplyRelic(parent, pack)
	if err != nil {
		return err
	}
	computedState, err := EncodeDocument(computed.State)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode computed relic state", err)
	}
	computedLinks, err := EncodeDocument(computed.Links)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode computed relic links", err)
	}
	storedState, err := EncodeDocument(stored.State)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode stored relic state", err)
	}
	storedLinks, err := EncodeDocument(stored.Links)
	if err != nil {
		return Wrap(CategoryCorruptData, "cannot encode stored relic links", err)
	}
	if !bytes.Equal(computedState, storedState) || !bytes.Equal(computedLinks, storedLinks) {
		return corrupt("the stored relic is not what its operations produce")
	}
	return nil
}

// ApplyRelicFields applies the pack's part.field.set and part.field.clear
// operations for one part, in order. It returns a new map and never changes
// before; operations for other parts are ignored.
func ApplyRelicFields(before map[string]json.RawMessage, partID string, operations []RelicOperation) map[string]json.RawMessage {
	fields := make(map[string]json.RawMessage, len(before))
	for name, value := range before {
		fields[name] = value
	}
	for _, operation := range operations {
		if operation.Part != partID {
			continue
		}
		switch operation.Type {
		case RelicOperationPartFieldSet:
			fields[operation.Name] = operation.Value
		case RelicOperationPartFieldClear:
			delete(fields, operation.Name)
		}
	}
	return fields
}

// RelicBodySavedBy reports whether the pack saved the body the record lists
// for the part, which is the one case in which a commit may store body bytes
// its parent does not. record is what folding pack onto parent computes, and
// parent is nil for the pack that starts a relic.
//
// It is true only when the part has a body in record, that body's version is
// the ID of an operation in pack, and the version is new: parent is nil, the
// parent has no such part, the parent's part has no body, or its body has
// another version.
//
// The version being an operation of the pack is not enough. A part.body.set
// delivered a second time carries the ID that is already the body's version,
// and from the base it first had it is stale and the fold ignores it. It does
// not change the version, so it saved no body, and whatever text arrives
// beside it is not stored. The writer and the history check both ask this
// function, so they agree on that. A second part.add for a part never gets
// this far: the fold refuses it.
func RelicBodySavedBy(parent *RelicRecord, pack RelicOperationPack, record RelicRecord, partID string) bool {
	part, ok := record.State.Relic.Part(partID)
	if !ok || part.Body == nil {
		return false
	}
	if parent != nil {
		before, existed := parent.State.Relic.Part(partID)
		if existed && before.Body != nil && before.Body.Version == part.Body.Version {
			return false
		}
	}
	for _, operation := range pack.Operations {
		if operation.ID == part.Body.Version {
			return true
		}
	}
	return false
}

// RelicPartsNamedBy returns the IDs of parts the pack adds or names, sorted.
func RelicPartsNamedBy(pack RelicOperationPack) []string {
	named := map[string]struct{}{}
	for _, operation := range pack.Operations {
		if operation.Type == RelicOperationPartAdd {
			named[operation.ID] = struct{}{}
		}
		if operation.Part != "" {
			named[operation.Part] = struct{}{}
		}
	}
	ids := make([]string, 0, len(named))
	for id := range named {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
