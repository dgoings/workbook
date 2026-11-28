package core

import (
	"crypto/sha256"
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
