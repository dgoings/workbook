package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RelicReplayInput is what a replay knows of origin's side beyond the state
// it writes onto. Held is every operation origin's history holds, by ID.
// SameBody[partID] is true when the pack's saved body for the part equals the
// parent's, by blob and stored format label.
type RelicReplayInput struct {
	Held     map[string]RelicOperation
	SameBody map[string]bool
}

// RelicReplayStep is what replay does with one local pack: skip it, stop on
// Conflict, or write it with Record as the state beside it. ConflictOperation
// is the ID of the pack's operation that Conflict names.
type RelicReplayStep struct {
	Skip              bool
	Conflict          *RelicConflict
	ConflictOperation string
	Record            RelicRecord
}

// PlanRelicReplay decides what replaying one local pack onto parent does. It
// applies, in order, the rules of a relic replay:
//
//  1. Onto a deleted relic, a pack of anything but relic.tombstone stops on a
//     tombstone conflict; a pack of only relic.tombstone is skipped, as is a
//     pack of only relic.restore onto a live relic.
//  2. A pack whose every operation origin already holds, with the same
//     content, is skipped. One held in part, or held with other content, is
//     an error.
//  3. A part.body.set whose base is not the part's current version stops on
//     a body conflict, unless the saved body equals the parent's: then the
//     two sides agree, and the fold ignores the operation.
//
// Counts and totals are never refused here; RelicOverLimitWarnings reports
// them. Otherwise Record is the fold of the pack onto parent, at the clock of
// a child of parent. An error from the fold is returned in its own category,
// naming the relic.
func PlanRelicReplay(parent RelicRecord, pack RelicOperationPack, input RelicReplayInput) (RelicReplayStep, error) {
	relicID := parent.State.RelicID
	// A document this build may not understand is refused as newer before
	// any rule here judges it.
	if err := refuseNewerRelicWriter(&parent, pack); err != nil {
		return RelicReplayStep{}, relicReplayError(relicID, err)
	}
	if len(pack.Operations) == 0 {
		return RelicReplayStep{}, relicReplayError(relicID, corrupt("the relic operation pack holds no operation"))
	}

	if step, decided := planRelicReplayOnDeletion(parent, pack); decided {
		return step, nil
	}
	if skip, err := relicPackHeld(pack, input.Held); err != nil || skip {
		return RelicReplayStep{Skip: skip}, relicReplayError(relicID, err)
	}

	child := pack
	child.LogicalClock = parent.State.LogicalClock + 1
	record, err := ApplyRelic(&parent, child)
	if err != nil {
		return RelicReplayStep{}, relicReplayError(relicID, err)
	}
	stale, err := staleRelicBody(parent.State.Relic, pack, input.SameBody)
	if err != nil {
		return RelicReplayStep{}, relicReplayError(relicID, err)
	}
	if stale != nil {
		return RelicReplayStep{
			Conflict:          &RelicConflict{RelicID: relicID, Type: RelicConflictBody, Part: stale.Part},
			ConflictOperation: stale.ID,
		}, nil
	}
	return RelicReplayStep{Record: record}, nil
}

// relicReplayError names the relic at the front of an error replay returns,
// keeping its category. A message that already names the relic is kept.
func relicReplayError(relicID string, err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), "relic "+relicID+" ") || strings.HasPrefix(err.Error(), "relic "+relicID+":") {
		return err
	}
	if typed, ok := err.(*Error); ok {
		named := *typed
		named.Message = "relic " + relicID + ": " + typed.Message
		return &named
	}
	return Wrap(CategoryCorruptData, "relic "+relicID, err)
}

// planRelicReplayOnDeletion is rule 1. decided is false when the rule leaves
// the pack to the rules after it.
func planRelicReplayOnDeletion(parent RelicRecord, pack RelicOperationPack) (RelicReplayStep, bool) {
	if !parent.State.Relic.Deleted {
		if relicPackOnly(pack, RelicOperationRestore) {
			return RelicReplayStep{Skip: true}, true
		}
		return RelicReplayStep{}, false
	}
	if relicPackOnly(pack, RelicOperationTombstone) {
		return RelicReplayStep{Skip: true}, true
	}
	// The operation named is the first edit; a pack that only restores, and
	// perhaps deletes again, names its restore.
	var blocked *RelicOperation
	for index := range pack.Operations {
		operation := &pack.Operations[index]
		switch operation.Type {
		case RelicOperationTombstone:
			continue
		case RelicOperationRestore:
			if blocked == nil {
				blocked = operation
			}
			continue
		}
		blocked = operation
		break
	}
	return RelicReplayStep{Conflict: &RelicConflict{
		RelicID:   parent.State.RelicID,
		Type:      RelicConflictTombstone,
		Tombstone: &RelicTombstoneConflict{OperationID: blocked.ID, Operation: blocked.Type},
	}, ConflictOperation: blocked.ID}, true
}

// relicPackOnly reports whether every operation of the pack has the type.
func relicPackOnly(pack RelicOperationPack, kind RelicOperationType) bool {
	for _, operation := range pack.Operations {
		if operation.Type != kind {
			return false
		}
	}
	return true
}

// relicPackHeld is rule 2: true when origin holds every operation of the pack
// with the same content, false when it holds none, and an error otherwise.
func relicPackHeld(pack RelicOperationPack, held map[string]RelicOperation) (bool, error) {
	count := 0
	for _, operation := range pack.Operations {
		origin, ok := held[operation.ID]
		if !ok {
			continue
		}
		count++
		same, err := sameRelicOperation(operation, origin)
		if err != nil {
			return false, err
		}
		if !same {
			return false, corrupt("operation %s is held on the remote with other content; an operation ID is used once", operation.ID)
		}
	}
	switch count {
	case 0:
		return false, nil
	case len(pack.Operations):
		return true, nil
	default:
		return false, corrupt("the remote holds %d of the %d operations of the local pack at logical clock %d; a pack is written whole",
			count, len(pack.Operations), pack.LogicalClock)
	}
}

// sameRelicOperation compares two operations by their stored form.
func sameRelicOperation(a, b RelicOperation) (bool, error) {
	encodedA, err := json.Marshal(a)
	if err != nil {
		return false, Wrap(CategoryCorruptData, fmt.Sprintf("operation %s cannot be encoded", a.ID), err)
	}
	encodedB, err := json.Marshal(b)
	if err != nil {
		return false, Wrap(CategoryCorruptData, fmt.Sprintf("operation %s cannot be encoded", b.ID), err)
	}
	return bytes.Equal(encodedA, encodedB), nil
}

// staleRelicBody is rule 3. It walks the pack's body saves against the
// parent's versions, as the fold will, and returns the first save that is
// stale and disagrees with the parent's body, or nil when none is. A save
// for a part neither the parent nor the pack has is an error: no honest
// writer saved it.
func staleRelicBody(parent RelicData, pack RelicOperationPack, sameBody map[string]bool) (*RelicOperation, error) {
	versions := map[string]string{}
	for _, part := range parent.Parts {
		versions[part.ID] = ""
		if part.Body != nil {
			versions[part.ID] = part.Body.Version
		}
	}
	for index := range pack.Operations {
		operation := &pack.Operations[index]
		switch operation.Type {
		case RelicOperationPartAdd:
			if _, exists := versions[operation.ID]; exists {
				continue
			}
			versions[operation.ID] = ""
			if operation.Body != nil {
				versions[operation.ID] = operation.ID
			}
		case RelicOperationPartBodySet:
			current, exists := versions[operation.Part]
			if !exists {
				return nil, corrupt("part.body.set %s names part %s, which the relic does not have", operation.ID, operation.Part)
			}
			if operation.Body.Base == current {
				versions[operation.Part] = operation.ID
				continue
			}
			if !sameBody[operation.Part] {
				return operation, nil
			}
		}
	}
	return nil, nil
}

// RelicRemovedPartWarnings warns, once per part removed in result, when one
// side of a synchronization removed the part and the other edited it: set or
// cleared a field, saved its body, moved it or linked from it. An operation both sides hold
// was not made apart from the other side, so it counts for neither.
func RelicRemovedPartWarnings(relicID string, local, remote []RelicOperationPack, result RelicRecord) []Warning {
	localIDs, remoteIDs := relicOperationIDs(local), relicOperationIDs(remote)
	localSide := relicPartChanges(local, remoteIDs)
	remoteSide := relicPartChanges(remote, localIDs)

	var warnings []Warning
	for _, part := range result.State.Relic.Parts {
		if !part.Removed {
			continue
		}
		var where string
		switch {
		case localSide.removed[part.ID] && remoteSide.edited[part.ID]:
			where = "removed here and edited in another clone"
		case remoteSide.removed[part.ID] && localSide.edited[part.ID]:
			where = "edited here and removed in another clone"
		default:
			continue
		}
		warnings = append(warnings, Warning{
			Code: WarningRelicEditOnRemovedPart,
			Message: "relic " + relicID + ": part " + part.ID + " was " + where +
				"; it stays removed and holds the edit; `workbook relic show " + relicID + "#" + part.ID +
				"` shows the part with the edit, and `workbook relic update " + relicID +
				" --restore-part " + part.ID + "` restores it for everyone",
		})
	}
	return warnings
}

type relicPartChangeSet struct{ removed, edited map[string]bool }

func relicOperationIDs(packs []RelicOperationPack) map[string]bool {
	ids := map[string]bool{}
	for _, pack := range packs {
		for _, operation := range pack.Operations {
			ids[operation.ID] = true
		}
	}
	return ids
}

// relicPartChanges lists the parts one side removed and edited, leaving out
// the operations the other side holds too.
func relicPartChanges(packs []RelicOperationPack, other map[string]bool) relicPartChangeSet {
	changes := relicPartChangeSet{removed: map[string]bool{}, edited: map[string]bool{}}
	for _, pack := range packs {
		for _, operation := range pack.Operations {
			if other[operation.ID] {
				continue
			}
			switch operation.Type {
			case RelicOperationPartRemove:
				changes.removed[operation.Part] = true
			case RelicOperationPartFieldSet, RelicOperationPartFieldClear, RelicOperationPartBodySet, RelicOperationPartMove:
				changes.edited[operation.Part] = true
			case RelicOperationLinkAdd:
				// A link from a part is an edit of that part.
				if operation.Link != nil && operation.Link.From != "" {
					changes.edited[operation.Link.From] = true
				}
			}
		}
	}
	return changes
}

// RelicOverLimitWarnings warns for each count or total that after passes and
// before did not: the live parts, the parts counting removed ones, the bodies
// of the live parts together, the properties together and the live links. Each is measured as
// the growth check on save measures it. A relic already over a limit is not
// reported again, and the message never holds a value.
func RelicOverLimitWarnings(relicID string, before, after RelicRecord) []Warning {
	was, now := relicTotalsOf(before), relicTotalsOf(after)
	advice := "; both changes are kept, and the next change that makes it larger is refused"
	var messages []string
	if now.live > MaxRelicLiveParts && was.live <= MaxRelicLiveParts {
		messages = append(messages, fmt.Sprintf("the relic has %d live parts; the limit is %d%s; remove a part to bring it back",
			now.live, MaxRelicLiveParts, advice))
	}
	if now.all > MaxRelicParts && was.all <= MaxRelicParts {
		messages = append(messages, fmt.Sprintf("the relic holds %d parts counting removed ones; the limit is %d%s; put new parts in another relic",
			now.all, MaxRelicParts, advice))
	}
	if now.liveBodyBytes > MaxRelicLiveBodyBytes && was.liveBodyBytes <= MaxRelicLiveBodyBytes {
		messages = append(messages, fmt.Sprintf("the bodies of the relic's live parts total %d bytes; the limit is %d bytes%s; shorten a body or remove a part",
			now.liveBodyBytes, MaxRelicLiveBodyBytes, advice))
	}
	if size := relicPropertiesSize(after.State.Relic.Properties); size > MaxRelicPropertiesBytes &&
		relicPropertiesSize(before.State.Relic.Properties) <= MaxRelicPropertiesBytes {
		messages = append(messages, fmt.Sprintf("the relic's properties are %d bytes; the limit is %d bytes%s; clear or shorten a property",
			size, MaxRelicPropertiesBytes, advice))
	}
	if now.links > MaxRelicLinks && was.links <= MaxRelicLinks {
		messages = append(messages, fmt.Sprintf("the relic has %d links; the limit is %d%s; remove a link to bring it back",
			now.links, MaxRelicLinks, advice))
	}
	warnings := make([]Warning, 0, len(messages))
	for _, message := range messages {
		warnings = append(warnings, Warning{Code: WarningRelicOverLimit, Message: "relic " + relicID + ": " + message})
	}
	if len(warnings) == 0 {
		return nil
	}
	return warnings
}

// relicPropertiesSize is the size checkRelicPropertiesGrowth measures. A value
// that cannot be encoded counts as nothing: the fold and the reader refuse it.
func relicPropertiesSize(properties map[string]json.RawMessage) int {
	if properties == nil {
		return 0
	}
	encoded, err := json.Marshal(properties)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// RelicFieldsOverLimitWarning is the warning for a part whose fields together
// are bytes long, past their total, after a synchronization joined two edits.
func RelicFieldsOverLimitWarning(relicID, part string, bytes int64) Warning {
	return Warning{
		Code: WarningRelicOverLimit,
		Message: fmt.Sprintf("relic %s: the fields of part %s are %d bytes; the limit is %d bytes; "+
			"both changes are kept, and the next change that makes them larger is refused; clear or shorten a field",
			relicID, part, bytes, MaxRelicPartFieldsBytes),
	}
}
