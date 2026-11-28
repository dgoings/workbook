package core

import (
	"crypto/sha256"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"
)

// RelicMergeMinReader is the writer-format generation a relic pack holding a
// part.body.merge declares. See SupportedFormatGeneration for what generation
// nine means.
const RelicMergeMinReader = 9

// RelicLineMergeMethod names the three-way line merge MergeLines makes. Each
// merge records the method that made it.
const RelicLineMergeMethod = "line-3way-1"

// maxRelicMergeMethodBytes bounds a merge's method label.
const maxRelicMergeMethodBytes = 32

// RelicBodyMerge is what a part.body.merge records besides its body: the
// version both saves started from, the save it merges in, and the method that
// combined them. Other is always an earlier body save of the same part in the
// same pack; the operation's body.base is the version it is applied onto.
type RelicBodyMerge struct {
	Ancestor string `json:"ancestor"`
	Other    string `json:"other"`
	Method   string `json:"method"`
}

// RelicMergeOperationID is the ID of the part.body.merge that merges the save
// other onto version base of part, so every clone that makes the same merge
// makes the same operation. It is a canonical ULID whose time is wallTime in
// milliseconds, the pack's wall time, and whose 80 random bits are the first
// ten bytes of SHA-256 over the inputs, each separated by a zero byte.
func RelicMergeOperationID(relicID, part, other, base, method string, wallTime time.Time) string {
	digest := sha256.New()
	for index, value := range []string{"workbook.relic-merge", relicID, part, other, base, method} {
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

// validateRelicBodyMerge checks the merge member of a part.body.merge on its
// own. That other names an earlier save of the part is a property of the pack;
// see validateRelicMergeOthers.
func validateRelicBodyMerge(operation RelicOperation) error {
	merge := operation.Merge
	if err := validateCanonicalULID("part.body.merge ancestor", merge.Ancestor); err != nil {
		return err
	}
	if merge.Ancestor == operation.ID {
		return corrupt("part.body.merge %s names itself as its ancestor", operation.ID)
	}
	if merge.Ancestor == operation.Body.Base {
		return corrupt("part.body.merge %s names its base as its ancestor", operation.ID)
	}
	if err := validateCanonicalULID("part.body.merge other", merge.Other); err != nil {
		return err
	}
	if !validRelicMergeMethod(merge.Method) {
		return corrupt("part.body.merge %s method %q must be 1 to %d bytes of a-z, 0-9 and -",
			operation.ID, merge.Method, maxRelicMergeMethodBytes)
	}
	return nil
}

func validRelicMergeMethod(method string) bool {
	if len(method) == 0 || len(method) > maxRelicMergeMethodBytes {
		return false
	}
	for index := 0; index < len(method); index++ {
		character := method[index]
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

// validateRelicMergeOthers requires every part.body.merge of a pack to name,
// as other, an earlier part.body.set or part.body.merge of the same part in
// the same pack with the same body format. The merge's text is computed from
// that save's, so naming anything else would record a merge no writer made.
func validateRelicMergeOthers(operations []RelicOperation) error {
	saves := map[string]RelicOperation{}
	for _, operation := range operations {
		switch operation.Type {
		case RelicOperationPartBodyMerge:
			other, ok := saves[operation.Merge.Other]
			if !ok {
				return corrupt("part.body.merge %s merges in %s, which is no earlier body save in its pack",
					operation.ID, operation.Merge.Other)
			}
			if other.Part != operation.Part {
				return corrupt("part.body.merge %s merges in %s, a save of another part", operation.ID, other.ID)
			}
			if other.Body.Format != operation.Body.Format {
				return corrupt("part.body.merge %s merges in %s, a save with another body format", operation.ID, other.ID)
			}
			saves[operation.ID] = operation
		case RelicOperationPartBodySet:
			saves[operation.ID] = operation
		}
	}
	return nil
}

// CheckRelicBodyMerge judges a merge need by its metadata alone, before any
// text is read: no-base when the ancestor or the current version has no body,
// format when the three labels differ, and "" when the texts are worth
// reading.
func CheckRelicBodyMerge(need RelicBodyMergeNeed) RelicMergeReason {
	switch {
	case need.Ancestor.Version == "" || need.Current.Version == "" || need.Other.Version == "":
		return RelicMergeNoBase
	case need.Ancestor.Format != need.Current.Format || need.Other.Format != need.Current.Format:
		return RelicMergeFormat
	}
	return ""
}

// RelicBodyMergeTexts are the three texts of a merge need: the ancestor's,
// the current version's and the other side's.
type RelicBodyMergeTexts struct{ Ancestor, Current, Other []byte }

// MergeRelicBody merges the other side's edit of a part's body onto the
// current version, with the ancestor both started from, by RelicLineMergeMethod
// under RelicLineMergeLimits. It returns the merged text and an empty reason,
// or no text and the reason it cannot, with the overlaps for overlap. It reads
// no clock and nothing beyond its arguments, so every clone given the same
// texts gets the same answer.
func MergeRelicBody(need RelicBodyMergeNeed, texts RelicBodyMergeTexts) (merged []byte, reason RelicMergeReason, overlaps []LineMergeOverlap) {
	if reason := CheckRelicBodyMerge(need); reason != "" {
		return nil, reason, nil
	}
	for _, text := range [][]byte{texts.Ancestor, texts.Current, texts.Other} {
		if !RelicBodyIsText(text) {
			return nil, RelicMergeNotText, nil
		}
	}
	result := MergeLines(texts.Ancestor, texts.Current, texts.Other, RelicLineMergeLimits)
	switch result.Reason {
	case "":
		return result.Text, "", nil
	case LineMergeOverlaps:
		return nil, RelicMergeOverlap, result.Overlaps
	case LineMergeTooManyChanges:
		return nil, RelicMergeTooManyChanges, nil
	case LineMergeTooLarge:
		return nil, RelicMergeTooLarge, nil
	default:
		return nil, RelicMergeReason(result.Reason), nil
	}
}

// RelicBodyMergeOperation is the part.body.merge that records merged as the
// body of need's part, applied onto the current version. Its ID is
// RelicMergeOperationID's, from the pack's wall time, so the same merge is
// the same operation on every clone.
func RelicBodyMergeOperation(relicID string, need RelicBodyMergeNeed, merged []byte, wallTime time.Time) RelicOperation {
	return RelicOperation{
		ID:   RelicMergeOperationID(relicID, need.Part, need.OtherOperation, need.Current.Version, RelicLineMergeMethod, wallTime),
		Type: RelicOperationPartBodyMerge,
		Part: need.Part,
		Body: &RelicBodyChange{Format: need.Current.Format, Bytes: int64(len(merged)), Base: need.Current.Version},
		Merge: &RelicBodyMerge{
			Ancestor: need.Ancestor.Version,
			Other:    need.OtherOperation,
			Method:   RelicLineMergeMethod,
		},
	}
}

// AppendRelicBodyMerges appends merges to the end of pack, in part order,
// recomputes the generation the pack declares, and folds it onto parent at
// the clock of a child of parent. With no merges it is the fold of the pack
// alone. The caller's pack is left as it was.
func AppendRelicBodyMerges(parent RelicRecord, pack RelicOperationPack, merges []RelicOperation) (RelicOperationPack, RelicRecord, error) {
	sorted := append([]RelicOperation(nil), merges...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Part < sorted[j].Part })
	child := pack
	child.Operations = append(append([]RelicOperation(nil), pack.Operations...), sorted...)
	child.MinReader = relicPackMinReader(child.Operations)
	child.LogicalClock = parent.State.LogicalClock + 1
	record, err := ApplyRelic(&parent, child)
	if err != nil {
		return RelicOperationPack{}, RelicRecord{}, relicReplayError(parent.State.RelicID, err)
	}
	return child, record, nil
}

// RelicBodyMergedWarning reports a replay that merged a stale save of a part's
// body: the version it was saved from here, the version it met on remote, and
// the merge that now stands.
func RelicBodyMergedWarning(relicID, remote string, merge RelicOperation) Warning {
	return Warning{
		Code: WarningRelicBodyMerged,
		Message: "relic " + relicID + ": part " + merge.Part + ": the body was saved here from version " +
			merge.Merge.Ancestor + " and on " + conflictRemote(remote) + " as version " + merge.Body.Base +
			"; the two edits changed different lines and were merged as version " + merge.ID +
			"; `workbook relic diff " + relicID + "#" + merge.Part + "` shows the merge",
	}
}
