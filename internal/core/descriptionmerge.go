package core

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
)

// DescriptionMergeMinReader is the writer-format generation a task pack
// holding a description.merge declares. See SupportedFormatGeneration for what
// generation ten means.
const DescriptionMergeMinReader = 10

// DescriptionLineMergeMethod names the three-way line merge a description
// merge makes: the same method, under its own bounds, a relic body merge
// records.
const DescriptionLineMergeMethod = RelicLineMergeMethod

// DescriptionMerge is what a description.merge records besides the merged
// text in its value: the local description edit it merged in, and the two
// other texts of the merge, the ancestor both edits started from and the
// current text it was merged onto.
//
// The texts are held inline rather than named, because a replay that is
// repeated moves the commit holding the merge onto a new parent, and a
// reference to "the parent's text" would then stop being true. Other names an
// earlier description operation of the same pack, a field.set of the
// description or an earlier description.merge, and the text it merged in is
// that operation's value.
type DescriptionMerge struct {
	Other    string `json:"other"`
	Ancestor string `json:"ancestor"`
	Current  string `json:"current"`
	Method   string `json:"method"`
}

// DescriptionMergeOperationID is the ID of the description.merge that merges
// the description operation other of a task onto current, from ancestor, so
// every clone that makes the same merge makes the same operation. It is a
// canonical ULID whose time is wallTime in milliseconds, the pack's wall time,
// and whose 80 random bits are the first ten bytes of SHA-256 over the inputs,
// each separated by a zero byte. The two texts are hashed first, because a
// description may hold a zero byte of its own.
func DescriptionMergeOperationID(taskID, other, ancestor, current, method string, wallTime time.Time) string {
	ancestorSum := sha256.Sum256([]byte(ancestor))
	currentSum := sha256.Sum256([]byte(current))
	digest := sha256.New()
	for index, value := range []string{
		"workbook.description-merge", taskID, other,
		hex.EncodeToString(ancestorSum[:]), hex.EncodeToString(currentSum[:]), method,
	} {
		if index > 0 {
			digest.Write([]byte{0})
		}
		digest.Write([]byte(value))
	}
	sum := digest.Sum(nil)
	var id ulid.ULID
	// A pack's wall time is a real time, inside the 48-bit range; one outside it
	// keeps the zero time field, which is still a deterministic ID.
	_ = id.SetTime(ulid.Timestamp(wallTime))
	_ = id.SetEntropy(sum[:10])
	return id.String()
}

// validateDescriptionMergeOperation checks a description.merge on its own: a
// value and a merge member, and nothing else. That other names an earlier
// description operation of the pack is a property of the pack; see
// validateDescriptionMergeOthers.
//
// No text is quoted in an error: each is bounded only by the object ceiling
// until it has been measured.
func validateDescriptionMergeOperation(operation Operation) error {
	if operation.Merge == nil {
		return corrupt("description.merge %s must carry a merge", operation.ID)
	}
	if operation.Field != "" || operation.Task != nil || operation.Checkpoint != nil {
		return corrupt("description.merge %s must carry only a value and a merge", operation.ID)
	}
	if err := validateNoThreadPayload(operation); err != nil {
		return err
	}
	merge := operation.Merge
	if err := validateCanonicalULID("description.merge other", merge.Other); err != nil {
		return err
	}
	if !validRelicMergeMethod(merge.Method) {
		return corrupt("description.merge %s method must be 1 to %d bytes of a-z, 0-9 and -",
			operation.ID, maxRelicMergeMethodBytes)
	}
	for _, text := range []struct{ name, value string }{
		{"value", operation.Value}, {"ancestor", merge.Ancestor}, {"current", merge.Current},
	} {
		if len(text.value) > MaxDescriptionBytes {
			return corrupt("description.merge %s %s is %d bytes, over the description limit of %d",
				operation.ID, text.name, len(text.value), MaxDescriptionBytes)
		}
	}
	return nil
}

// validateNoMergePayload keeps a merge member on the one operation type that
// means something by it, for the reason validateNoThreadPayload gives.
func validateNoMergePayload(operation Operation) error {
	if operation.Merge != nil && operation.Type != OperationDescriptionMerge {
		return corrupt("%s must not contain merge data", operation.Type)
	}
	return nil
}

// describesDescription reports an operation that sets a task's description.
func describesDescription(operation Operation) bool {
	return (operation.Type == OperationFieldSet && operation.Field == "description") ||
		operation.Type == OperationDescriptionMerge
}

// validateDescriptionMergeOthers requires every description.merge of a pack
// to name, as other, an earlier description operation of the same pack, and
// the pack to declare the generation a merge needs. The merged text is
// computed from that operation's value, so naming anything else would record
// a merge no writer made; and a merge written without its marker would reach
// an older build as corrupt data rather than as an upgrade notice.
func validateDescriptionMergeOthers(pack OperationPack) error {
	edits := map[string]struct{}{}
	for _, operation := range pack.Operations {
		if operation.Type == OperationDescriptionMerge {
			if pack.MinReader < DescriptionMergeMinReader {
				return corrupt("operation pack holding a description.merge has minimum reader generation %d, below %d",
					pack.MinReader, DescriptionMergeMinReader)
			}
			if _, ok := edits[operation.Merge.Other]; !ok {
				return corrupt("description.merge %s merges in %s, which is no earlier description edit in its pack",
					operation.ID, operation.Merge.Other)
			}
		}
		if describesDescription(operation) {
			edits[operation.ID] = struct{}{}
		}
	}
	return nil
}

// DescriptionLineMergeLimits are the bounds a description merge runs under:
// the relic diff's edit bound and work budget, and the description's own size
// limit for the merged text. Each may be raised, never lowered.
var DescriptionLineMergeLimits = LineMergeLimits{EditLines: maxRelicDiffEditLines, Work: maxLineMergeWork, Bytes: MaxDescriptionBytes}

// DescriptionMergeRefusal says why two description edits were not merged. It
// is empty for a clean merge. Every refusal is the description conflict a
// replay reported before descriptions merged, unchanged.
type DescriptionMergeRefusal string

const (
	// DescriptionMergeOverlap reports edits that changed the same or touching
	// lines (LineMergeOverlaps).
	DescriptionMergeOverlap DescriptionMergeRefusal = "overlap"
	// DescriptionMergeTooManyChanges reports a diff of either edit that passed
	// the edit bound or the work budget.
	DescriptionMergeTooManyChanges DescriptionMergeRefusal = "too-many-changes"
	// DescriptionMergeTooLarge reports a merged text longer than
	// MaxDescriptionBytes.
	DescriptionMergeTooLarge DescriptionMergeRefusal = "too-large"
	// DescriptionMergeNotText reports a text that is not UTF-8. A description
	// travels in a JSON string, so none can be; the refusal only fails safe.
	DescriptionMergeNotText DescriptionMergeRefusal = "not-text"
)

// MergeDescription merges other, a description edit made from ancestor, onto
// current, the description that stands, line by line with MergeLines under
// DescriptionLineMergeLimits. It gives the merged text and an empty refusal,
// or no text and the reason the two cannot be merged safely.
func MergeDescription(ancestor, current, other string) (merged string, refusal DescriptionMergeRefusal) {
	for _, text := range []string{ancestor, current, other} {
		if !utf8.ValidString(text) {
			return "", DescriptionMergeNotText
		}
	}
	result := MergeLines([]byte(ancestor), []byte(current), []byte(other), DescriptionLineMergeLimits)
	switch result.Reason {
	case "":
		return string(result.Text), ""
	case LineMergeOverlaps:
		return "", DescriptionMergeOverlap
	case LineMergeTooManyChanges:
		return "", DescriptionMergeTooManyChanges
	case LineMergeTooLarge:
		return "", DescriptionMergeTooLarge
	default:
		return "", DescriptionMergeRefusal(result.Reason)
	}
}

// DescriptionReplay is what replaying one pack needs to do about the
// description. Merge is false when the pack replays as it did before
// descriptions merged; when it is true, the three texts are the merge's:
// Ancestor the description the local edit started from, Ours the local
// fold's result, and Theirs the description of the commit replayed onto.
type DescriptionReplay struct {
	Merge                  bool
	Ancestor, Ours, Theirs string
}

// PlanDescriptionReplay decides, from the texts alone, whether a pack being
// replayed must have its description merged. sourceParent is the state of the
// local commit's own Git parent, source the local commit's state and parent
// the state of the commit the pack is replayed onto.
//
// A pack that sets no description replays as before. So does one whose
// description origin left alone since the local edit began (parent equals
// sourceParent), and one whose text origin already holds (source equals
// parent). Anything else needs MergeDescription to decide.
func PlanDescriptionReplay(sourceParent, source, parent TaskData, pack OperationPack) DescriptionReplay {
	touches := false
	for _, operation := range pack.Operations {
		if describesDescription(operation) {
			touches = true
			break
		}
	}
	if !touches || parent.Description == sourceParent.Description || source.Description == parent.Description {
		return DescriptionReplay{}
	}
	return DescriptionReplay{
		Merge:    true,
		Ancestor: sourceParent.Description,
		Ours:     source.Description,
		Theirs:   parent.Description,
	}
}

// AppendDescriptionMerge records merged, the merge of the pack's description
// edit onto current from ancestor, as a description.merge appended to the
// pack, and folds the pack onto parent at parent's next logical clock. The
// merge names the pack's last description operation, and the pack declares
// the generation the merge needs. The pack passed in is not changed.
func AppendDescriptionMerge(parent StateDocument, pack OperationPack, ancestor, current, merged string) (OperationPack, StateDocument, error) {
	other := ""
	for _, operation := range pack.Operations {
		if describesDescription(operation) {
			other = operation.ID
		}
	}
	if other == "" {
		return pack, StateDocument{}, Errorf(CategoryCorruptData,
			"task %s pack has no description edit to record a merge of", pack.TaskID)
	}
	operations := make([]Operation, 0, len(pack.Operations)+1)
	operations = append(operations, pack.Operations...)
	operations = append(operations, Operation{
		ID:    DescriptionMergeOperationID(pack.TaskID, other, ancestor, current, DescriptionLineMergeMethod, pack.WallTime),
		Type:  OperationDescriptionMerge,
		Value: merged,
		Merge: &DescriptionMerge{Other: other, Ancestor: ancestor, Current: current, Method: DescriptionLineMergeMethod},
	})
	pack.Operations = operations
	pack.MinReader = max(pack.MinReader, PackMinReader(operations))
	pack.LogicalClock = parent.LogicalClock + 1
	state, err := Apply(&parent, pack)
	if err != nil {
		return pack, StateDocument{}, err
	}
	return pack, state, nil
}

// DescriptionMergedWarning reports a replay that merged task taskID's
// description edit made here with one made on remote.
func DescriptionMergedWarning(taskID, remote string) Warning {
	return Warning{
		Code: WarningDescriptionMerged,
		Message: "task " + taskID + ": the description was changed here and on " + conflictRemote(remote) +
			"; the two edits changed different lines and were merged; `workbook show " + taskID +
			" --history` shows the merge",
	}
}
