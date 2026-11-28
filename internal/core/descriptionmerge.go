package core

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

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
