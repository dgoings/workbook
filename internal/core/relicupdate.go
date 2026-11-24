package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// RelicEditKind names one kind of change relic update makes.
type RelicEditKind string

const (
	RelicEditTitle         RelicEditKind = "title"
	RelicEditSetProperty   RelicEditKind = "set-property"
	RelicEditClearProperty RelicEditKind = "clear-property"
	RelicEditAddPart       RelicEditKind = "add-part"
	RelicEditPart          RelicEditKind = "edit-part"
	RelicEditMovePart      RelicEditKind = "move-part"
	RelicEditRemovePart    RelicEditKind = "remove-part"
	RelicEditRestorePart   RelicEditKind = "restore-part"
)

// RelicFieldEdit sets one field of a part, or clears it when Clear is set, in
// which case Value is ignored.
type RelicFieldEdit struct {
	Name  string
	Value json.RawMessage
	Clear bool
}

// RelicEdit is one change in a relic update. Which members it reads depends on
// its kind, named beside each.
type RelicEdit struct {
	Kind          RelicEditKind
	Title         string           // title
	Name          string           // set-property, clear-property
	Value         json.RawMessage  // set-property
	Part          string           // part ID or prefix: edit, move, remove, restore
	PartType      string           // add-part
	Fields        []RelicFieldEdit // add-part (sets only), edit-part
	Body          *RelicBodyInput  // add-part, edit-part
	Base          string           // edit-part with a body
	Before, After string           // add-part, move-part: anchor part ID or prefix
}

// relicPartTotals is what the add-time limits measure of a relic: how many of
// its parts are live, how many it has counting removed ones, and how large the
// bodies of its live parts are together.
type relicPartTotals struct {
	live, all     int
	liveBodyBytes int64
}

func relicPartTotalsOf(relic RelicData) relicPartTotals {
	totals := relicPartTotals{all: len(relic.Parts)}
	for _, part := range relic.Parts {
		if part.Removed {
			continue
		}
		totals.live++
		if part.Body != nil {
			totals.liveBodyBytes += part.Body.Bytes
		}
	}
	return totals
}

// checkRelicGrowth refuses a change that takes a relic past a limit on how
// many parts it has or how large their bodies are together, and only when the
// change is what takes it there. A new relic grows from the zero totals.
//
// The comparison is with the relic before the change and not with the limit
// alone, as validateThreadGrowth compares a task's thread. A relic may already
// be over a limit, because reading never checks one. A rule that refused every
// change to such a relic would refuse the removals that bring it back under,
// so only growth is refused: a change that adds nothing is never refused for a
// count or total the relic is already over.
func checkRelicGrowth(before, after relicPartTotals) error {
	if after.live > MaxRelicLiveParts && after.live > before.live {
		return Errorf(CategoryValidation,
			"the relic would have %d live parts; the limit is %d; remove a part first, or use another relic",
			after.live, MaxRelicLiveParts)
	}
	if after.all > MaxRelicParts && after.all > before.all {
		return Errorf(CategoryValidation,
			"the relic would hold %d parts counting removed ones; the limit is %d; put new parts in another relic",
			after.all, MaxRelicParts)
	}
	if after.liveBodyBytes > MaxRelicLiveBodyBytes && after.liveBodyBytes > before.liveBodyBytes {
		return Errorf(CategoryValidation,
			"the bodies of the relic's live parts would total %d bytes; the limit is %d bytes; shorten a body or remove a part first, or use another relic",
			after.liveBodyBytes, MaxRelicLiveBodyBytes)
	}
	return nil
}

// relicToChange reads the relic a command is about to change. It refuses one
// written by a newer Workbook, which this build can show but cannot judge a
// change to, even a change that would turn out to write nothing.
func (s RelicService) relicToChange(ctx context.Context, idOrPrefix string) (RelicSnapshot, error) {
	snapshot, err := s.getRelic(ctx, idOrPrefix)
	if err != nil {
		return RelicSnapshot{}, err
	}
	if snapshot.Operation.RequiresNewerReader() || snapshot.Record.State.RequiresNewerReader() {
		return RelicSnapshot{}, newerWriterRelic(snapshot.Record.State.RelicID)
	}
	return snapshot, nil
}

// writeRelicChange records operations as the relic's next commit, unless they
// take the relic past an add-time limit. A stale write from the store is
// returned as it is, with no retry: the caller's edits were checked against a
// relic that has since changed.
func (s RelicService) writeRelicChange(
	ctx context.Context,
	parent RelicSnapshot,
	operations []RelicOperation,
	content map[string]RelicPartContent,
	reason string,
) (RelicSnapshot, error) {
	state := parent.Record.State
	pack := NewRelicOperationPack(s.Config.ProjectID, state.RelicID, state.History.Generation, s.actorFor(ctx),
		state.LogicalClock+1, s.now(), operations)
	record, err := ApplyRelic(&parent.Record, pack)
	if err != nil {
		return RelicSnapshot{}, err
	}
	if err := checkRelicGrowth(relicPartTotalsOf(state.Relic), relicPartTotalsOf(record.State.Relic)); err != nil {
		return RelicSnapshot{}, err
	}
	return s.Writer.WriteRelic(ctx, s.Config, &parent, pack, record, content, reason)
}

// relicMutationOf reports a change that added or edited no part, or, with
// changed false, a command that had nothing to write.
func relicMutationOf(snapshot RelicSnapshot, changed bool) RelicMutationResult {
	return RelicMutationResult{Relic: relicSummaryOf(snapshot), Parts: []RelicDocumentPart{}, Changed: changed}
}

// Update applies edits to a relic in order and records them as one commit, or
// writes nothing. Each edit sees the ones before it. An edit that would change
// nothing leaves no operation, and when none is left the relic is returned as
// it stands with Changed false, so a repeated command is safe. One refused
// edit refuses the whole update.
//
// The result's Parts are the parts this update added or edited, in the order
// the edits first changed them.
func (s RelicService) Update(ctx context.Context, idOrPrefix string, edits []RelicEdit) (RelicMutationResult, error) {
	if err := checkRelicPropertiesNamedOnce(edits); err != nil {
		return RelicMutationResult{}, err
	}
	snapshot, err := s.relicToChange(ctx, idOrPrefix)
	if err != nil {
		return RelicMutationResult{}, err
	}
	relicID := snapshot.Record.State.RelicID
	if snapshot.Record.State.Relic.Deleted {
		return RelicMutationResult{}, Errorf(CategoryValidation,
			"relic %s is deleted; restore it first with \"workbook relic restore %s\"", relicID, relicID)
	}

	update := relicUpdate{
		service: s,
		parent:  snapshot,
		relicID: relicID,
		relic:   copyRelicData(snapshot.Record.State.Relic),
		minted:  map[string]struct{}{},
		named:   map[string]map[string]bool{},
		before:  map[string]map[string]json.RawMessage{},
		added:   map[string]struct{}{},
		bodies:  map[string][]byte{},
	}
	for _, edit := range edits {
		if err := update.apply(ctx, edit); err != nil {
			return RelicMutationResult{}, err
		}
	}
	if len(update.operations) == 0 {
		return relicMutationOf(snapshot, false), nil
	}

	// Sizes are measured on what would be stored, once every edit has applied.
	if err := checkRelicPropertiesSize(update.relic.Properties); err != nil {
		return RelicMutationResult{}, err
	}
	content := make(map[string]RelicPartContent, len(update.changed))
	fields := make(map[string]map[string]json.RawMessage, len(update.changed))
	for _, partID := range update.changed {
		fields[partID] = update.fieldsNow(partID)
		var stored RelicPartContent
		if update.storesFields(partID) {
			if err := checkRelicFieldsSize("part "+partID, fields[partID]); err != nil {
				return RelicMutationResult{}, err
			}
			stored.Fields = fields[partID]
		}
		if body, saved := update.bodies[partID]; saved {
			stored.Body, stored.HasBody = body, true
		}
		content[partID] = stored
	}

	written, err := s.writeRelicChange(ctx, snapshot, update.operations, content, "update relic "+relicID)
	if err != nil {
		return RelicMutationResult{}, err
	}
	result := RelicMutationResult{
		Relic:   relicSummaryOf(written),
		Parts:   make([]RelicDocumentPart, 0, len(update.changed)),
		Changed: true,
	}
	for _, partID := range update.changed {
		part, _ := written.Record.State.Relic.Part(partID)
		result.Parts = append(result.Parts, newRelicDocumentPart(part, RelicPartContent{Fields: fields[partID]}))
	}
	return result, nil
}

// Delete marks a relic deleted with a relic.tombstone. Its ref and everything
// it holds stay. Deleting a deleted relic changes nothing.
func (s RelicService) Delete(ctx context.Context, idOrPrefix string) (RelicMutationResult, error) {
	return s.setRelicDeleted(ctx, idOrPrefix, true)
}

// Restore brings a deleted relic back with a relic.restore. Restoring a relic
// that is not deleted changes nothing.
func (s RelicService) Restore(ctx context.Context, idOrPrefix string) (RelicMutationResult, error) {
	return s.setRelicDeleted(ctx, idOrPrefix, false)
}

func (s RelicService) setRelicDeleted(ctx context.Context, idOrPrefix string, deleted bool) (RelicMutationResult, error) {
	snapshot, err := s.relicToChange(ctx, idOrPrefix)
	if err != nil {
		return RelicMutationResult{}, err
	}
	if snapshot.Record.State.Relic.Deleted == deleted {
		return relicMutationOf(snapshot, false), nil
	}
	kind, verb := RelicOperationRestore, "restore"
	if deleted {
		kind, verb = RelicOperationTombstone, "delete"
	}
	ids, err := s.newIDs(1)
	if err != nil {
		return RelicMutationResult{}, err
	}
	written, err := s.writeRelicChange(ctx, snapshot, []RelicOperation{{ID: ids[0], Type: kind}}, nil,
		verb+" relic "+snapshot.Record.State.RelicID)
	if err != nil {
		return RelicMutationResult{}, err
	}
	return relicMutationOf(written, true), nil
}

// relicMessageNameLimit is the longest property or field name a refusal
// repeats. A name has no limit of its own, so a longer one is given by size.
const relicMessageNameLimit = 100

// relicNamedInMessage names a property or field for a refusal, by its size
// alone when it is too long to repeat.
func relicNamedInMessage(kind, name string) string {
	if len(name) > relicMessageNameLimit {
		return fmt.Sprintf("a %s whose name is %d bytes long", kind, len(name))
	}
	return fmt.Sprintf("%s %q", kind, name)
}

// relicNamedTwice refuses an update that names one property, or one field of
// one part, a second time. what is the thing named, as relicNamedInMessage
// gives it, and firstClear and clear say whether each naming cleared it.
//
// With each name given once, the size check on the update's end state bounds
// every value its operations record, and a command that contradicts itself is
// reported instead of quietly keeping its last value.
func relicNamedTwice(what string, firstClear, clear bool, advice string) error {
	switch {
	case firstClear != clear:
		return Errorf(CategoryValidation, "this update sets and clears %s; %s", what, advice)
	case clear:
		return Errorf(CategoryValidation, "this update clears %s twice; %s", what, advice)
	default:
		return Errorf(CategoryValidation, "this update sets %s twice; %s", what, advice)
	}
}

// checkRelicPropertiesNamedOnce refuses edits that set or clear one property
// more than once. It is decided from the edits alone, before the relic is
// read. A name that is no valid name is left to the edit's own check.
func checkRelicPropertiesNamedOnce(edits []RelicEdit) error {
	named := map[string]bool{} // whether the first edit to name it cleared it
	for _, edit := range edits {
		if edit.Kind != RelicEditSetProperty && edit.Kind != RelicEditClearProperty {
			continue
		}
		if ValidateRelicName("property", edit.Name) != nil {
			continue
		}
		clear := edit.Kind == RelicEditClearProperty
		if firstClear, seen := named[edit.Name]; seen {
			return relicNamedTwice(relicNamedInMessage("property", edit.Name), firstClear, clear,
				"name a property once in one update")
		}
		named[edit.Name] = clear
	}
	return nil
}

// nameField records that this update sets or clears a field of a part, and
// refuses a field the update has already named for that part. partID is the
// part as resolved, so two prefixes of one part name the same part.
func (u *relicUpdate) nameField(partID string, field RelicFieldEdit) error {
	fields := u.named[partID]
	if fields == nil {
		fields = map[string]bool{}
		u.named[partID] = fields
	}
	if firstClear, seen := fields[field.Name]; seen {
		return relicNamedTwice(relicNamedInMessage("field", field.Name)+" of part "+partID, firstClear, field.Clear,
			"name a field once for each part in one update")
	}
	fields[field.Name] = field.Clear
	return nil
}

// relicUpdate is one update while its edits apply: the relic as the edits so
// far leave it, and the operations and content they add up to.
type relicUpdate struct {
	service RelicService
	parent  RelicSnapshot
	relicID string

	// relic is the working copy every edit reads and changes, with its parts
	// kept in order. It is what folding operations onto parent gives.
	relic      RelicData
	operations []RelicOperation
	minted     map[string]struct{}
	// named holds, by part, the fields an edit so far has set or cleared, and
	// whether the first to name each cleared it.
	named map[string]map[string]bool

	// before holds a part's fields as they were before any operation here: read
	// from the store for a part the relic had, given for a part an edit added.
	// A part's fields now are before with the operations applied.
	before map[string]map[string]json.RawMessage
	added  map[string]struct{}
	// bodies holds each body an edit saved, by part.
	bodies map[string][]byte
	// changed lists the parts an edit added or edited, in the order first
	// changed.
	changed []string
}

func (u *relicUpdate) apply(ctx context.Context, edit RelicEdit) error {
	switch edit.Kind {
	case RelicEditTitle:
		return u.setTitle(edit)
	case RelicEditSetProperty:
		return u.setProperty(edit)
	case RelicEditClearProperty:
		return u.clearProperty(edit)
	case RelicEditAddPart:
		return u.addPart(edit)
	case RelicEditPart:
		return u.editPart(ctx, edit)
	case RelicEditMovePart:
		return u.movePart(edit)
	case RelicEditRemovePart:
		return u.setPartRemoved(edit, true)
	case RelicEditRestorePart:
		return u.setPartRemoved(edit, false)
	default:
		return Errorf(CategoryValidation, "a relic update has no %q edit", string(edit.Kind))
	}
}

// record adds one operation, minting its ID unless it has one, and applies it
// to the working copy the way the fold will.
func (u *relicUpdate) record(operation RelicOperation) error {
	if operation.ID == "" {
		id, err := u.service.newID(u.minted)
		if err != nil {
			return err
		}
		operation.ID = id
	}
	u.operations = append(u.operations, operation)
	applyRelicOperation(&u.relic, operation)
	if operation.Type == RelicOperationPartAdd || operation.Type == RelicOperationPartMove {
		return sortRelicParts(u.relic.Parts)
	}
	return nil
}

func (u *relicUpdate) markChanged(partID string) {
	for _, changed := range u.changed {
		if changed == partID {
			return
		}
	}
	u.changed = append(u.changed, partID)
}

func (u *relicUpdate) setTitle(edit RelicEdit) error {
	title, err := ValidateRelicTitle(edit.Title)
	if err != nil {
		return err
	}
	if title == u.relic.Title {
		return nil
	}
	return u.record(RelicOperation{Type: RelicOperationTitleSet, Title: title})
}

func (u *relicUpdate) setProperty(edit RelicEdit) error {
	value, err := canonicalRelicValue("property", edit.Name, edit.Value)
	if err != nil {
		return err
	}
	if stored, present := u.relic.Properties[edit.Name]; present && bytes.Equal(stored, value) {
		return nil
	}
	return u.record(RelicOperation{Type: RelicOperationPropertySet, Name: edit.Name, Value: value})
}

func (u *relicUpdate) clearProperty(edit RelicEdit) error {
	if err := ValidateRelicName("property", edit.Name); err != nil {
		return err
	}
	if _, present := u.relic.Properties[edit.Name]; !present {
		return nil
	}
	return u.record(RelicOperation{Type: RelicOperationPropertyClear, Name: edit.Name})
}

// livePart finds the part an edit changes or places another part next to, and
// refuses a removed one.
func (u *relicUpdate) livePart(prefix string) (RelicPart, error) {
	part, err := resolvePartPrefix(u.relicID, u.relic.Parts, prefix)
	if err != nil {
		return RelicPart{}, err
	}
	if part.Removed {
		return RelicPart{}, Errorf(CategoryValidation,
			"part %s was removed; restore it first with --restore-part %s", part.ID, part.ID)
	}
	return part, nil
}

// anchor finds the part an add or a move goes next to. given is false when the
// edit names neither a part to go before nor one to go after.
func (u *relicUpdate) anchor(edit RelicEdit) (anchor RelicPart, before, given bool, err error) {
	switch {
	case edit.Before != "" && edit.After != "":
		return RelicPart{}, false, false, Errorf(CategoryValidation,
			"a part goes before one part or after one; give --before or --after, not both")
	case edit.Before != "":
		anchor, err = u.livePart(edit.Before)
		return anchor, true, true, err
	case edit.After != "":
		anchor, err = u.livePart(edit.After)
		return anchor, false, true, err
	}
	return RelicPart{}, false, false, nil
}

func relicPlacements(parts []RelicPart) []PartPlacement {
	placements := make([]PartPlacement, len(parts))
	for index, part := range parts {
		placements[index] = PartPlacement{ID: part.ID, Position: part.Position}
	}
	return placements
}

func (u *relicUpdate) addPart(edit RelicEdit) error {
	if err := ValidateRelicPartType(edit.PartType); err != nil {
		return err
	}
	fields := make(map[string]json.RawMessage, len(edit.Fields))
	for _, field := range edit.Fields {
		if field.Clear {
			return Errorf(CategoryValidation, "a new part has no field to clear; set the fields it starts with")
		}
		value, err := canonicalRelicValue("field", field.Name, field.Value)
		if err != nil {
			return err
		}
		fields[field.Name] = value
	}
	if edit.Body != nil {
		if err := checkRelicBodyInput("the new part", *edit.Body); err != nil {
			return err
		}
	}
	anchor, before, given, err := u.anchor(edit)
	if err != nil {
		return err
	}

	// The part's ID is its part.add's, and a tie between positions is settled
	// by ID, so the ID comes before the position.
	id, err := u.service.newID(u.minted)
	if err != nil {
		return err
	}
	for _, field := range edit.Fields {
		if err := u.nameField(id, field); err != nil {
			return err
		}
	}
	var position string
	if given {
		position, err = PlacePart(relicPlacements(u.relic.LiveParts()), id, anchor.ID, before)
	} else {
		// After every part, removed ones too, so that a part restored later
		// comes back ahead of this one instead of sharing its position.
		position, err = AppendPartPosition(relicPlacements(u.relic.Parts))
	}
	if err != nil {
		return err
	}
	operation := RelicOperation{ID: id, Type: RelicOperationPartAdd, PartType: edit.PartType, Position: position}
	if edit.Body != nil {
		operation.Body = &RelicBodyChange{Format: edit.Body.Format, Bytes: int64(len(edit.Body.Content))}
		u.bodies[id] = edit.Body.Content
	}
	// The fields and the body go in the write's content, not in operations.
	u.before[id] = fields
	u.added[id] = struct{}{}
	u.markChanged(id)
	return u.record(operation)
}

// loadFields reads a part's stored fields the first time an edit names it.
func (u *relicUpdate) loadFields(ctx context.Context, partID string) error {
	if _, loaded := u.before[partID]; loaded {
		return nil
	}
	read, err := u.service.Reader.ReadRelicParts(ctx, u.service.Config, u.parent, []string{partID}, false)
	if err != nil {
		return err
	}
	fields := read[partID].Fields
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	u.before[partID] = fields
	return nil
}

// fieldsNow returns a loaded part's fields as the operations so far leave
// them. This is the one way a part's new fields are computed, and it is the
// way a reader checks them: the stored fields with the pack's field operations
// applied in order.
func (u *relicUpdate) fieldsNow(partID string) map[string]json.RawMessage {
	return ApplyRelicFields(u.before[partID], partID, u.operations)
}

// storesFields reports whether the write must carry the part's fields: the
// part is new, or an operation set or cleared one of its fields.
func (u *relicUpdate) storesFields(partID string) bool {
	if _, added := u.added[partID]; added {
		return true
	}
	for _, operation := range u.operations {
		if operation.Part == partID &&
			(operation.Type == RelicOperationPartFieldSet || operation.Type == RelicOperationPartFieldClear) {
			return true
		}
	}
	return false
}

// checkRelicBodyBase refuses a body save that did not start from the part's
// current body: the base must be the current version exactly, and there is
// none to give when the part has no body.
func checkRelicBodyBase(relicID string, part RelicPart, base string) error {
	if part.Body == nil {
		if base != "" {
			return Errorf(CategoryStaleWrite, "part %s has no body, so there is no version %s to start from", part.ID, base)
		}
		return nil
	}
	current := part.Body.Version
	switch base {
	case current:
		return nil
	case "":
		return Errorf(CategoryStaleWrite, "part %s body is at version %s; pass --base %s to save over it",
			part.ID, current, current)
	default:
		return Errorf(CategoryStaleWrite,
			"part %s body is at version %s, not %s; read it again with \"workbook relic show %s#%s --body\" and save from that version",
			part.ID, current, base, relicID, part.ID)
	}
}

func (u *relicUpdate) editPart(ctx context.Context, edit RelicEdit) error {
	part, err := u.livePart(edit.Part)
	if err != nil {
		return err
	}
	for _, field := range edit.Fields {
		if err := ValidateRelicName("field", field.Name); err != nil {
			return err
		}
		if err := u.nameField(part.ID, field); err != nil {
			return err
		}
	}
	if edit.Body != nil {
		if err := checkRelicBodyInput("part "+part.ID, *edit.Body); err != nil {
			return err
		}
		if err := checkRelicBodyBase(u.relicID, part, edit.Base); err != nil {
			return err
		}
	} else if edit.Base != "" {
		return Errorf(CategoryValidation,
			"a base version says which body a save starts from; give one for part %s only with a body", part.ID)
	}
	if err := u.loadFields(ctx, part.ID); err != nil {
		return err
	}

	recorded := len(u.operations)
	for _, field := range edit.Fields {
		operation := RelicOperation{Type: RelicOperationPartFieldClear, Part: part.ID, Name: field.Name}
		current, present := u.fieldsNow(part.ID)[field.Name]
		if field.Clear {
			if !present {
				continue
			}
		} else {
			value, err := canonicalRelicValue("field", field.Name, field.Value)
			if err != nil {
				return err
			}
			if present && bytes.Equal(current, value) {
				continue
			}
			operation.Type, operation.Value = RelicOperationPartFieldSet, value
		}
		if err := u.record(operation); err != nil {
			return err
		}
	}
	if edit.Body != nil {
		operation := RelicOperation{
			Type: RelicOperationPartBodySet,
			Part: part.ID,
			Body: &RelicBodyChange{Format: edit.Body.Format, Bytes: int64(len(edit.Body.Content)), Base: edit.Base},
		}
		if err := u.record(operation); err != nil {
			return err
		}
		u.bodies[part.ID] = edit.Body.Content
	}
	if len(u.operations) > recorded {
		u.markChanged(part.ID)
	}
	return nil
}

// relicPartSitsBy reports whether, among parts in order, moved is already
// directly before anchor, or directly after it when before is false.
func relicPartSitsBy(parts []RelicPart, moved, anchor string, before bool) bool {
	first, second := anchor, moved
	if before {
		first, second = moved, anchor
	}
	for index := 0; index+1 < len(parts); index++ {
		if parts[index].ID == first && parts[index+1].ID == second {
			return true
		}
	}
	return false
}

func (u *relicUpdate) movePart(edit RelicEdit) error {
	part, err := u.livePart(edit.Part)
	if err != nil {
		return err
	}
	anchor, before, given, err := u.anchor(edit)
	if err != nil {
		return err
	}
	if !given {
		return Errorf(CategoryValidation,
			"say where part %s goes: give --before or --after with the part to put it next to", part.ID)
	}
	// A part already where it is asked to go stays at the position it has,
	// whatever position a fresh placement there would choose. Two parts that
	// share a position are the exception: moving one next to the other is how
	// they are given positions of their own, so that move is placed afresh.
	live := u.relic.LiveParts()
	if part.Position != anchor.Position && relicPartSitsBy(live, part.ID, anchor.ID, before) {
		return nil
	}
	position, err := PlacePart(relicPlacements(live), part.ID, anchor.ID, before)
	if err != nil {
		return err
	}
	if position == part.Position {
		return nil
	}
	return u.record(RelicOperation{Type: RelicOperationPartMove, Part: part.ID, Position: position})
}

func (u *relicUpdate) setPartRemoved(edit RelicEdit, removed bool) error {
	part, err := resolvePartPrefix(u.relicID, u.relic.Parts, edit.Part)
	if err != nil {
		return err
	}
	if part.Removed == removed {
		return nil
	}
	kind := RelicOperationPartRestore
	if removed {
		kind = RelicOperationPartRemove
	}
	return u.record(RelicOperation{Type: kind, Part: part.ID})
}
