package core

// Ceilings on what one task document may hold.
//
// A task ref is shared history: every clone that fetches, synchronizes, or runs
// an auto-syncing mutation reads every task tip into memory, so an unbounded
// field is not one collaborator's problem but the whole team's. These ceilings
// exist to bound that cost, not to express editorial taste, so they are set far
// above what a task written by a person or an agent needs. A title is a
// headline, a description is prose and short code fences, and labels are
// vocabulary; none of them approaches these numbers in ordinary use.
//
// Lengths are counted in bytes, not runes, because bytes are what a reader must
// allocate. They are validated after normalization, so a trimmed title and a
// deduplicated label set are what the ceilings describe.
//
// Raising one of these is a compatible change: a clone running an older version
// rejects a document a newer one accepted. Lowering one is not, because a task
// already stored at the old size stops reading. Treat them as part of the
// storage format.
const (
	// MaxTitleBytes bounds a task title after surrounding whitespace is
	// trimmed. Roughly five lines of terminal text.
	MaxTitleBytes = 500
	// MaxDescriptionBytes bounds a task description. Roughly a thousand lines
	// of prose, well past the point where the body belongs in the repository
	// and the task should link to it.
	MaxDescriptionBytes = 64 << 10
	// MaxLabelBytes bounds one label.
	MaxLabelBytes = 100
	// MaxLabelCount bounds how many distinct labels one task may carry.
	MaxLabelCount = 50
	// MaxRankBytes bounds a task rank.
	//
	// A rank is a reduced rational, and this ceiling is unlike the others: it
	// bounds work rather than storage. Decimal conversion of a digit string
	// costs more than linear time, every stored rank is parsed on every read,
	// and ordering parses each one again per comparison, so an unbounded rank is
	// a cheaper denial of service than an unbounded document — 4,000,000 digits
	// is well under one Git object and takes seconds to parse.
	//
	// Ordinary ranks are a few bytes. Placing a task between two neighbours
	// halves the gap, which adds about one byte for every three placements into
	// the same shrinking gap, so this leaves room for thousands of them.
	MaxRankBytes = 4096
	// MaxDependencyCount bounds how many distinct dependencies one task may
	// declare. Dependency edges are walked for cycles across the whole project,
	// so this bounds that graph as well as the document.
	MaxDependencyCount = 100
)

// validateTaskFieldSizes rejects a task whose normalized fields exceed the
// documented ceilings. Messages state the offending size and the ceiling so a
// caller can tell a near miss from an absurd one, and never quote the value
// itself: echoing a rejected megabyte back through an error string would
// reproduce the cost the ceiling exists to prevent.
func validateTaskFieldSizes(task TaskData) error {
	if len(task.Title) > MaxTitleBytes {
		return Errorf(
			CategoryValidation,
			"task title is %d bytes and must not exceed %d",
			len(task.Title), MaxTitleBytes,
		)
	}
	if len(task.Description) > MaxDescriptionBytes {
		return Errorf(
			CategoryValidation,
			"task description is %d bytes and must not exceed %d",
			len(task.Description), MaxDescriptionBytes,
		)
	}
	if len(task.Labels) > MaxLabelCount {
		return Errorf(
			CategoryValidation,
			"task has %d labels and must not exceed %d",
			len(task.Labels), MaxLabelCount,
		)
	}
	for _, label := range task.Labels {
		if len(label) > MaxLabelBytes {
			return Errorf(
				CategoryValidation,
				"task label is %d bytes and must not exceed %d",
				len(label), MaxLabelBytes,
			)
		}
	}
	if len(task.Dependencies) > MaxDependencyCount {
		return Errorf(
			CategoryValidation,
			"task has %d dependencies and must not exceed %d",
			len(task.Dependencies), MaxDependencyCount,
		)
	}
	return validateRankSize(task.Rank)
}

// validateRankSize bounds a rank before anything parses it.
//
// It is called from parseRank rather than from validateTaskFieldSizes, because
// every other ceiling guards memory a caller has already spent, while this one
// guards work that has not happened yet: NormalizeTask parses the rank before it
// checks any field size, and a stored operation document reaches parseRank
// without passing through NormalizeTask at all. Both paths run on every read.
func validateRankSize(rank string) error {
	if len(rank) > MaxRankBytes {
		return Errorf(
			CategoryValidation,
			"task rank is %d bytes and must not exceed %d",
			len(rank), MaxRankBytes,
		)
	}
	return nil
}
