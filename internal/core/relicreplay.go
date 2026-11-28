package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RelicReplayInput is what a replay knows of origin's side beyond the state
// it writes onto. Held is every operation origin's history holds, by ID.
// SameBody[partID] is true when the pack's saved body for the part equals the
// parent's, by blob and stored format label. Source is the state of the local
// commit the pack was first recorded in, and SourceParent the state of that
// commit's parent: a stale body save is merged from them (rule 4). Without
// both, a stale save is a body conflict that cannot be merged.
type RelicReplayInput struct {
	Held                 map[string]RelicOperation
	SameBody             map[string]bool
	Source, SourceParent *RelicRecord
}

// RelicBodyMergeNeed is one part whose body a replay has to merge: the body
// both saves started from (the source parent's), the one that stands (the
// replay parent's) and the one saved here (the source's), each by version,
// label and size. A side with no body has an empty Version. OtherOperation is
// the last body save of the part the local fold took, the save the merge
// takes in, and Stale is the first, the one a conflict names.
type RelicBodyMergeNeed struct {
	Part                     string
	Ancestor, Current, Other RelicBody
	OtherOperation           string
	Stale                    string
}

// RelicReplayStep is what replay does with one local pack: skip it, stop on
// Conflict, or write it with Record as the state beside it. ConflictOperation
// is the ID of the pack's operation that Conflict names. Merges is set,
// sorted by part, when the pack's stale body saves have to be merged before it
// is written; Conflict, Skip and Record are then zero, and the caller reads the
// texts, merges them with MergeRelicBody and folds the pack with
// AppendRelicBodyMerges.
type RelicReplayStep struct {
	Skip              bool
	Conflict          *RelicConflict
	ConflictOperation string
	Record            RelicRecord
	Merges            []RelicBodyMergeNeed
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
//  3. A pack with an operation naming a stub of the parent, a part whose
//     content a checkpoint dropped, stops on a dropped conflict: the fold
//     would ignore it, and the change it records has nothing left to apply
//     to. A part.remove of a stub is the exception: both sides wanted the
//     part gone, so it replays as the no-op the fold makes it.
//  4. A body save the local fold took but the fold onto parent would not is
//     stale. When the saved body equals the parent's, the two sides agree and
//     the fold ignores it. Otherwise, given Source and SourceParent, the
//     part's body is merged (Merges) when every save of the part the local
//     fold took is stale onto parent and every one it ignored is ignored
//     there too. Any other shape, which no honest writer makes, stops on a
//     body conflict with reason unmergeable. A save the local fold ignored,
//     and that parent ignores as well, changed nothing and is no conflict.
//
// Retire and unretire need no rule: they set a mark as title.set sets a
// title, so the replayed pack's mark stands and neither conflicts with an
// edit.
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
	if operation, part := relicOperationOnStub(parent.State.Relic, pack); operation != "" {
		return RelicReplayStep{
			Conflict:          &RelicConflict{RelicID: relicID, Type: RelicConflictDropped, Part: part},
			ConflictOperation: operation,
		}, nil
	}

	child := pack
	child.LogicalClock = parent.State.LogicalClock + 1
	record, err := ApplyRelic(&parent, child)
	if err != nil {
		return RelicReplayStep{}, relicReplayError(relicID, err)
	}
	if input.Source == nil || input.SourceParent == nil {
		stale, err := staleRelicBody(parent.State.Relic, pack, input.SameBody)
		if err != nil {
			return RelicReplayStep{}, relicReplayError(relicID, err)
		}
		if stale != nil {
			return unmergeableRelicBody(relicID, stale.Part, stale.ID), nil
		}
		return RelicReplayStep{Record: record}, nil
	}
	needs, unmergeable, err := planRelicBodyMerges(parent.State.Relic, *input.SourceParent, *input.Source, pack, input.SameBody)
	if err != nil {
		return RelicReplayStep{}, relicReplayError(relicID, err)
	}
	if unmergeable != nil {
		return unmergeableRelicBody(relicID, unmergeable.Part, unmergeable.ID), nil
	}
	if len(needs) > 0 {
		return RelicReplayStep{Merges: needs}, nil
	}
	return RelicReplayStep{Record: record}, nil
}

// unmergeableRelicBody is the step that stops on a body conflict no merge can
// settle, naming the stale operation. The caller fills in the three bodies.
func unmergeableRelicBody(relicID, part, operation string) RelicReplayStep {
	return RelicReplayStep{
		Conflict: &RelicConflict{RelicID: relicID, Type: RelicConflictBody, Part: part,
			Body: &RelicBodyConflict{Reason: RelicMergeUnmergeable}},
		ConflictOperation: operation,
	}
}

// planRelicBodyMerges is rule 4 given the local commit the pack came from. It
// walks the pack's body saves twice, onto the source parent as the local fold
// took them and onto parent as the replay's fold will, and judges each part in
// ID order:
//
//   - A part whose saves both folds take alike needs nothing.
//   - A part where the local fold took some saves and parent takes none is
//     stale: it needs a merge, unless the saved body equals parent's.
//   - Any other part is returned as unmergeable, with the first save the two
//     folds disagree on.
//
// The merge takes the source parent's body as the ancestor, parent's as the
// current one and the source's as the other, so OtherOperation, the last save
// the local fold took, is the source's body version.
func planRelicBodyMerges(
	parent RelicData,
	sourceParent, source RelicRecord,
	pack RelicOperationPack,
	sameBody map[string]bool,
) ([]RelicBodyMergeNeed, *RelicOperation, error) {
	onParent, err := relicBodySavesTaken(parent, pack)
	if err != nil {
		return nil, nil, err
	}
	locally, err := relicBodySavesTaken(sourceParent.State.Relic, pack)
	if err != nil {
		return nil, nil, err
	}
	saves := map[string][]int{}
	var parts []string
	for index, operation := range pack.Operations {
		if operation.Type != RelicOperationPartBodySet && operation.Type != RelicOperationPartBodyMerge {
			continue
		}
		if _, seen := saves[operation.Part]; !seen {
			parts = append(parts, operation.Part)
		}
		saves[operation.Part] = append(saves[operation.Part], index)
	}
	sort.Strings(parts)

	var needs []RelicBodyMergeNeed
	for _, partID := range parts {
		first, last, differing, takenThere := -1, -1, -1, false
		for _, index := range saves[partID] {
			if locally[index] != onParent[index] && differing < 0 {
				differing = index
			}
			if locally[index] {
				if first < 0 {
					first = index
				}
				last = index
			}
			takenThere = takenThere || onParent[index]
		}
		if differing < 0 {
			continue
		}
		if takenThere || first < 0 {
			return nil, &pack.Operations[differing], nil
		}
		if sameBody[partID] {
			continue
		}
		need := RelicBodyMergeNeed{
			Part:           partID,
			Ancestor:       relicPartBodyOf(sourceParent.State.Relic, partID),
			Current:        relicPartBodyOf(parent, partID),
			Other:          relicPartBodyOf(source.State.Relic, partID),
			OtherOperation: pack.Operations[last].ID,
			Stale:          pack.Operations[first].ID,
		}
		if need.Other.Version != need.OtherOperation {
			// The source does not hold what its own pack saved last, which a
			// verified commit never does; nothing is merged from it.
			return nil, &pack.Operations[first], nil
		}
		needs = append(needs, need)
	}
	return needs, nil, nil
}

// relicBodySavesTaken reports, for each operation of the pack, whether the
// fold onto start takes it as a body save: a part.body.set or part.body.merge
// whose base is the part's version at that point, empty when it has no body.
// A save for a part neither start nor the pack has is an error: no honest
// writer saved it.
func relicBodySavesTaken(start RelicData, pack RelicOperationPack) ([]bool, error) {
	versions := map[string]string{}
	for _, part := range start.Parts {
		versions[part.ID] = ""
		if part.Body != nil {
			versions[part.ID] = part.Body.Version
		}
	}
	taken := make([]bool, len(pack.Operations))
	for index, operation := range pack.Operations {
		switch operation.Type {
		case RelicOperationPartAdd:
			if _, exists := versions[operation.ID]; exists {
				continue
			}
			versions[operation.ID] = ""
			if operation.Body != nil {
				versions[operation.ID] = operation.ID
			}
		case RelicOperationPartBodySet, RelicOperationPartBodyMerge:
			current, exists := versions[operation.Part]
			if !exists {
				return nil, corrupt("%s %s names part %s, which the relic does not have", operation.Type, operation.ID, operation.Part)
			}
			if operation.Body.Base == current {
				versions[operation.Part] = operation.ID
				taken[index] = true
			}
		}
	}
	return taken, nil
}

// relicPartBodyOf is a part's body in data, or the zero body, whose version is
// empty, when the part has none or is not there.
func relicPartBodyOf(data RelicData, partID string) RelicBody {
	part, ok := data.Part(partID)
	if !ok || part.Body == nil {
		return RelicBody{}
	}
	return *part.Body
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

// relicOperationOnStub is rule 3: the first operation of the pack, other than
// a part.remove, that names a stub of the parent, as the part it changes or
// the part a link is from, and that stub's ID; or two empty strings.
func relicOperationOnStub(parent RelicData, pack RelicOperationPack) (operation, part string) {
	stubs := map[string]bool{}
	for _, candidate := range parent.Parts {
		if candidate.Dropped != nil {
			stubs[candidate.ID] = true
		}
	}
	if len(stubs) == 0 {
		return "", ""
	}
	for _, candidate := range pack.Operations {
		if candidate.Type == RelicOperationPartRemove {
			continue
		}
		named := candidate.Part
		if candidate.Type == RelicOperationLinkAdd && candidate.Link != nil {
			named = candidate.Link.From
		}
		if stubs[named] {
			return candidate.ID, named
		}
	}
	return "", ""
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

// staleRelicBody is rule 4. It walks the pack's body saves against the
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
		case RelicOperationPartBodySet, RelicOperationPartBodyMerge:
			current, exists := versions[operation.Part]
			if !exists {
				return nil, corrupt("%s %s names part %s, which the relic does not have", operation.Type, operation.ID, operation.Part)
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
			case RelicOperationPartFieldSet, RelicOperationPartFieldClear, RelicOperationPartBodySet,
				RelicOperationPartBodyMerge, RelicOperationPartMove:
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
