package core

import (
	"encoding/json"
	"time"
)

// RelicCheckpointMinReader is the writer-format generation relic.retire,
// relic.unretire and history.checkpoint declare. See SupportedFormatGeneration
// for what generation seven means.
const RelicCheckpointMinReader = 7

// The two boundaries a relic checkpoint compacts at: the relic's tip was
// retired and not deleted, or it was deleted.
const (
	RelicBoundaryRetired = "retired"
	RelicBoundaryDeleted = "deleted"
)

// RelicCheckpointData is the summary a relic's history.checkpoint carries. A
// relic checkpoint carries no content: the state, links and part subtrees
// beside it are what it kept, and ValidateRelicCheckpointRoot checks them
// against this summary.
//
// CompactedFrom is the object ID of the tip it replaced, which the state
// records as history.compactedFrom. Since is when the relic last became
// retired or deleted, by boundary. CompactedOperations is every operation ID
// in the replaced history, sorted and unique. The wall times are display
// metadata.
type RelicCheckpointData struct {
	RunID               string    `json:"runId"`
	CompactedFrom       string    `json:"compactedFrom"`
	PreviousGeneration  string    `json:"previousGeneration"`
	Boundary            string    `json:"boundary"`
	Since               time.Time `json:"since"`
	CompactedCommits    int       `json:"compactedCommits"`
	FirstWallTime       time.Time `json:"firstWallTime"`
	LastWallTime        time.Time `json:"lastWallTime"`
	CompactedOperations []string  `json:"compactedOperations"`
}

// RelicCompaction is the state's record of the checkpoint its history starts
// at: the run, when it ran, the boundary, and when the relic last became
// retired or deleted. Every state after the checkpoint carries it forward.
type RelicCompaction struct {
	RunID    string    `json:"runId"`
	At       time.Time `json:"at"`
	Boundary string    `json:"boundary"`
	Since    time.Time `json:"since"`
}

// RelicPartDrop marks a part as a stub: removed, with its content dropped by
// a checkpoint. RemovedAt is when it was removed (or the relic deleted), and
// DroppedAt is when the checkpoint dropped its content.
type RelicPartDrop struct {
	RemovedAt time.Time `json:"removedAt"`
	DroppedAt time.Time `json:"droppedAt"`
}

// Checkpoint returns the summary when the pack is a single history.checkpoint
// carrying one, which is the only shape a relic checkpoint takes.
func (pack RelicOperationPack) Checkpoint() (RelicCheckpointData, bool) {
	if len(pack.Operations) != 1 || pack.Operations[0].Type != RelicOperationCheckpoint ||
		pack.Operations[0].Checkpoint == nil {
		return RelicCheckpointData{}, false
	}
	return *pack.Operations[0].Checkpoint, true
}

// ValidateRelicCheckpointRoot checks a parentless commit that opens with
// history.checkpoint against the record stored beside it. A checkpoint carries
// no content, so the record cannot be folded from nothing; these root rules
// take the place of that fold:
//
//	R1. The pack is one history.checkpoint, and pack and state are at 7.
//	R2. The state is in the pack's generation, which is not the one it
//	    replaced; it names the tip the checkpoint replaced; and its clock is
//	    the pack's.
//	R3. The state's compacted member is the run, the pack's wall time, the
//	    boundary and the summary's since.
//	R4. The boundary agrees with the relic. A retired root is retired and not
//	    deleted, and every removed part is a stub. A deleted root is deleted,
//	    has no properties, and every part is a stub.
//	R5. Every stub is removed, has no body, and was dropped no later than the
//	    pack's wall time. The list of replaced operations is sorted, unique,
//	    made of ULIDs, and does not hold the checkpoint's own ID.
//	R6. The links are valid against the state.
//
// The part subtrees are Git's to check (each stub's is the empty fields.json),
// and so are the replaced history and the old tip's updatedAt and clock.
func ValidateRelicCheckpointRoot(pack RelicOperationPack, stored RelicRecord) error {
	state := stored.State
	// The generation gate runs before anything else, as in ApplyRelic.
	if pack.RequiresNewerReader() {
		return newerWriterRelic(pack.RelicID)
	}
	if state.RequiresNewerReader() {
		return newerWriterRelic(state.RelicID)
	}
	if err := validateRelicOperationPackDocument(pack); err != nil {
		return err
	}
	if err := validateRelicStateDocument(state); err != nil {
		return err
	}
	if err := validateRelicLinksDocument(stored.Links); err != nil {
		return err
	}

	// R1.
	summary, ok := pack.Checkpoint()
	if !ok {
		return corrupt("a relic checkpoint commit must hold exactly one history.checkpoint")
	}
	if pack.MinReader != RelicCheckpointMinReader || state.MinReader != RelicCheckpointMinReader {
		return corrupt("a relic checkpoint and its state must declare minimum reader generation %d",
			RelicCheckpointMinReader)
	}

	// R2. The summary and the operation pack were checked above, including
	// that the summary's list leaves out the checkpoint's own ID (R5).
	if state.ProjectID != pack.ProjectID || state.RelicID != pack.RelicID {
		return corrupt("the relic checkpoint's state names another project or relic")
	}
	if state.History.Generation != pack.HistoryGeneration {
		return corrupt("the relic checkpoint's state is not in the checkpoint's history generation")
	}
	if summary.PreviousGeneration == pack.HistoryGeneration {
		return corrupt("a relic checkpoint must open a new history generation")
	}
	if state.History.CompactedFrom == nil || *state.History.CompactedFrom != summary.CompactedFrom {
		return corrupt("the relic checkpoint's state must name the tip it was compacted from")
	}
	if state.LogicalClock != pack.LogicalClock {
		return corrupt("the relic checkpoint's state clock is not the checkpoint's")
	}

	// R3.
	relic := state.Relic
	want := RelicCompaction{RunID: summary.RunID, At: pack.WallTime, Boundary: summary.Boundary, Since: summary.Since}
	if relic.Compacted == nil || !sameRelicCompaction(*relic.Compacted, want) {
		return corrupt("the relic checkpoint's state does not record the compaction its checkpoint describes")
	}

	// R4 and R5.
	if err := checkRelicCheckpointBoundary(summary.Boundary, relic); err != nil {
		return err
	}
	for _, part := range relic.Parts {
		if part.Dropped != nil && part.Dropped.DroppedAt.After(pack.WallTime) {
			return corrupt("relic state part %s was dropped after the checkpoint that dropped it", part.ID)
		}
	}

	// R6.
	return ValidateRelicLinks(state, stored.Links)
}

// checkRelicCheckpointBoundary is rule R4.
func checkRelicCheckpointBoundary(boundary string, relic RelicData) error {
	switch boundary {
	case RelicBoundaryRetired:
		if !relic.Retired || relic.Deleted {
			return corrupt("a relic checkpoint at the retired boundary must hold a retired relic that is not deleted")
		}
		for _, part := range relic.Parts {
			if part.Removed && part.Dropped == nil {
				return corrupt("a relic checkpoint at the retired boundary keeps removed part %s; it must be a stub", part.ID)
			}
		}
	case RelicBoundaryDeleted:
		if !relic.Deleted {
			return corrupt("a relic checkpoint at the deleted boundary must hold a deleted relic")
		}
		if len(relic.Properties) != 0 {
			return corrupt("a relic checkpoint at the deleted boundary keeps no properties")
		}
		for _, part := range relic.Parts {
			if part.Dropped == nil {
				return corrupt("a relic checkpoint at the deleted boundary keeps part %s; it must be a stub", part.ID)
			}
		}
	default:
		return corrupt("relic checkpoint boundary %q is unsupported", boundary)
	}
	return nil
}

// sameRelicCompaction compares two compaction records by their stored form,
// so a time is the same only when it is written the same.
func sameRelicCompaction(a, b RelicCompaction) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(encodedA) == string(encodedB)
}

// validateRelicCheckpointSummary checks the members of one history.checkpoint
// summary; operationID is the checkpoint's own ID.
func validateRelicCheckpointSummary(operationID string, summary RelicCheckpointData) error {
	if err := validateCanonicalULID("history.checkpoint run ID", summary.RunID); err != nil {
		return err
	}
	if err := validateCanonicalULID("history.checkpoint previous generation", summary.PreviousGeneration); err != nil {
		return err
	}
	// Not quoted, for the reason validateStateEnvelope gives.
	if !objectIDPattern.MatchString(summary.CompactedFrom) {
		return corrupt("history.checkpoint compactedFrom must be a Git object ID")
	}
	if err := validateRelicBoundary("history.checkpoint", summary.Boundary); err != nil {
		return err
	}
	if err := validateRelicStoredTime("history.checkpoint since", summary.Since); err != nil {
		return err
	}
	if summary.CompactedCommits < 1 {
		return corrupt("history.checkpoint must compact at least one commit")
	}
	if err := validateRelicStoredTime("history.checkpoint first wall time", summary.FirstWallTime); err != nil {
		return err
	}
	if err := validateRelicStoredTime("history.checkpoint last wall time", summary.LastWallTime); err != nil {
		return err
	}
	if len(summary.CompactedOperations) == 0 {
		return corrupt("history.checkpoint must list the operations it compacted")
	}
	for index, id := range summary.CompactedOperations {
		if err := validateCanonicalULID("history.checkpoint compacted operation", id); err != nil {
			return err
		}
		if index > 0 && summary.CompactedOperations[index-1] >= id {
			return corrupt("history.checkpoint compacted operations must be sorted and unique")
		}
		if id == operationID {
			return corrupt("history.checkpoint %s lists itself among the operations it compacted", operationID)
		}
	}
	return nil
}

// validateRelicCompaction checks a state's compacted member.
func validateRelicCompaction(compaction RelicCompaction) error {
	if err := validateCanonicalULID("relic state compaction run ID", compaction.RunID); err != nil {
		return err
	}
	if err := validateRelicStoredTime("relic state compaction time", compaction.At); err != nil {
		return err
	}
	if err := validateRelicBoundary("relic state compaction", compaction.Boundary); err != nil {
		return err
	}
	return validateRelicStoredTime("relic state compaction since", compaction.Since)
}

// validateRelicPartDrop checks a stub: removed, with no body and both dates.
func validateRelicPartDrop(part RelicPart) error {
	if !part.Removed {
		return corrupt("relic state part %s is dropped but not removed", part.ID)
	}
	if part.Body != nil {
		return corrupt("relic state part %s is dropped but keeps a body", part.ID)
	}
	if err := validateRelicStoredTime("relic state part "+part.ID+" removal time", part.Dropped.RemovedAt); err != nil {
		return err
	}
	return validateRelicStoredTime("relic state part "+part.ID+" drop time", part.Dropped.DroppedAt)
}

func validateRelicBoundary(what, boundary string) error {
	switch boundary {
	case RelicBoundaryRetired, RelicBoundaryDeleted:
		return nil
	}
	return corrupt("%s boundary must be %q or %q", what, RelicBoundaryRetired, RelicBoundaryDeleted)
}

// validateRelicStoredTime requires a stored time that is present and written
// in its canonical form.
func validateRelicStoredTime(what string, stored time.Time) error {
	if stored.IsZero() {
		return corrupt("%s must be present", what)
	}
	return validateRelicTimeZone(what, stored)
}
