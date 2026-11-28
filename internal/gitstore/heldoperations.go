package gitstore

import (
	"encoding/json"

	"github.com/dgoings/workbook/internal/core"
)

// heldOperations is what one history records, by operation ID: the commit
// that records each operation and what it records. It is the account a replay
// consults before writing a pack, and it compares content as well as IDs,
// because an operation ID names one operation: a pack that reuses a held ID
// for another change is not that operation, and skipping it as already
// recorded would quietly take one of the two values.
type heldOperations map[string]heldOperation

// heldOperation is one entry of heldOperations.
type heldOperation struct {
	commit string
	// content is the operation as its pack records it (operationContent). It
	// is empty when only the ID is known, as for an operation a checkpoint
	// summarizes, and such an entry is matched by ID alone.
	content string
}

// operationContent is one operation as its operation.json records it: the
// pack's JSON encoding with that operation alone in it, and with the three
// members a replay rewrites cleared, the logical clock, the history
// generation and the minimum reader, which a description merge appended to a
// pack raises (see taskReplay.next). It is encoding/json's rendering of the
// decoded pack, which is what the canonical form of operation.json is
// (decodeCanonicalOperation), without the validation a stored document gets:
// the cleared members would not pass it. The actor, the wall time and every member
// of the operation itself are compared, so a replayed copy matches its
// original and nothing else does.
func operationContent(pack core.OperationPack, operation core.Operation) (string, error) {
	pack.LogicalClock = 0
	pack.HistoryGeneration = ""
	pack.MinReader = 0
	pack.Operations = []core.Operation{operation}
	encoded, err := json.Marshal(pack)
	if err != nil {
		return "", core.Wrap(core.CategoryCorruptData, "cannot canonicalize task operation "+operation.ID, err)
	}
	return string(encoded), nil
}

// recordIDs holds operations known by ID alone.
func (held heldOperations) recordIDs(ids []string) {
	for _, id := range ids {
		if _, found := held[id]; !found {
			held[id] = heldOperation{}
		}
	}
}

// record holds every operation pack records, as recorded by commit. An ID
// held already keeps its first entry.
func (held heldOperations) record(commit string, pack core.OperationPack) error {
	for _, operation := range pack.Operations {
		if entry, found := held[operation.ID]; found && entry.content != "" {
			continue
		}
		content, err := operationContent(pack, operation)
		if err != nil {
			return err
		}
		held[operation.ID] = heldOperation{commit: commit, content: content}
	}
	return nil
}

// recordChain holds every operation on chain.
func (held heldOperations) recordChain(chain []OperationCommit) error {
	for _, commit := range chain {
		if err := held.record(commit.ObjectID, commit.Operation); err != nil {
			return err
		}
	}
	return nil
}

// match reports how many of pack's operations are held, and refuses with a
// corrupt-data error a held ID whose recorded content differs from pack's.
func (held heldOperations) match(taskID, commit string, pack core.OperationPack) (int, error) {
	recorded := 0
	for _, operation := range pack.Operations {
		entry, found := held[operation.ID]
		if !found {
			continue
		}
		recorded++
		if entry.content == "" || entry.commit == commit {
			continue
		}
		content, err := operationContent(pack, operation)
		if err != nil {
			return 0, err
		}
		if content != entry.content {
			return 0, reusedOperationError(taskID, operation.ID, entry.commit, commit)
		}
	}
	return recorded, nil
}

// has reports whether id is held.
func (held heldOperations) has(id string) bool {
	_, found := held[id]
	return found
}

// derivedFromHeld reports whether every operation of pack that isHeld does
// not hold is a description.merge derived from one it does hold, so that the
// pack counts as held as a whole. This is the one rule by which an operation
// a history does not record still counts as held there.
//
// A description.merge is derived from the operation it names as other: it
// records that edit merged onto the text it met, and names an earlier
// description operation of its own pack (core validation). It counts as held
// where following other, on through any earlier merge of the pack that is not
// held, ends at an operation that is held. The history holding that edit
// already has it, and whichever replay there brought it together with the
// other side's text recorded its own merge, under another ID. Writing this
// pack there would record the held edit a second time.
//
// It arises when move-tasks carried an edit published at the old place with
// a merge, [field.set S, description.merge M] at the new place and [field.set
// S] at the old one, and the tasks then move back. Any operation that is not
// held and is not such a merge makes this false, so a pack only part of whose
// own edits are held is still refused. Replay (taskReplay.next) applies it,
// and so does every test of whether a commit is accounted for
// (operationAccount.covers): what is left to replay or carry across a
// checkpoint (operationsNotIn, for fetch, adoption, the stuck-task report and
// the carry), the compaction guards and lease (chainAccountsFor), and the
// move's verification (verifyMovedTasks), which the clean-up after a move
// relies on. A checkpoint lists the edit and not such a merge made at another
// place, so the rule is needed there as much as on an uncompacted history.
func derivedFromHeld(pack core.OperationPack, isHeld func(string) bool) bool {
	byID := make(map[string]core.Operation, len(pack.Operations))
	for _, operation := range pack.Operations {
		byID[operation.ID] = operation
	}
	for _, operation := range pack.Operations {
		if !isHeld(operation.ID) && !mergeOfHeld(operation, byID, isHeld) {
			return false
		}
	}
	return true
}

// mergeOfHeld reports whether operation is a description.merge whose other
// chain within its pack (byID) ends at an operation isHeld holds.
func mergeOfHeld(operation core.Operation, byID map[string]core.Operation, isHeld func(string) bool) bool {
	// Each step goes to an earlier operation of the pack, so the chain is at
	// most as long as the pack; the bound only guards one that loops.
	for steps := 0; steps <= len(byID); steps++ {
		if operation.Type != core.OperationDescriptionMerge || operation.Merge == nil {
			return false
		}
		other, found := byID[operation.Merge.Other]
		if !found {
			return false
		}
		if isHeld(other.ID) {
			return true
		}
		operation = other
	}
	return false
}

// reusedOperationError is the refusal for two commits that record different
// changes under one operation ID.
func reusedOperationError(taskID, operationID, first, second string) error {
	return core.Errorf(core.CategoryCorruptData,
		"task %s: commit %s and commit %s both record operation %s, with different content; "+
			"an operation ID names one operation, so one of the two commits is not what it claims to be, "+
			"and nothing was changed; find out which one is genuine before synchronizing this task again",
		taskID, first, second, operationID)
}
