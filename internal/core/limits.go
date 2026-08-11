package core

import (
	"regexp"
	"strings"
)

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
	// MaxStatusNameBytes bounds one status name. A name is a machine token, not
	// prose: it is typed as a flag value, matched in a filter, and written into
	// a commit subject, so the ceiling is set where a name stops being
	// typeable rather than where storage starts to hurt.
	MaxStatusNameBytes = 40
	// MaxStatusLabelBytes bounds one status display label. A label is a column
	// heading; anything longer stops fitting a terminal board column.
	MaxStatusLabelBytes = 60
	// MaxStatusCount bounds how many live statuses one project may define.
	//
	// This one is a usability ceiling as much as a storage ceiling: every live
	// status is a board column, and a board nobody can read on one screen has
	// stopped being a board.
	MaxStatusCount = 24
	// MaxStatusAliasCount bounds how many rename aliases the ledger may carry,
	// and MaxStatusRetiredCount how many retirements.
	//
	// These two stand in for a compaction pass that does not exist yet. An
	// alias and a retirement are both permanent forwarding pointers: they are
	// what lets a clone that has not fetched a rename still resolve a task
	// stored under the old name, so nothing may drop them while any unsynced
	// clone might still hold the old value. Until a compaction operation can
	// declare a prefix of the ledger settled and fold the chains away, the
	// ceiling is the only thing bounding them, and a project that reaches it
	// has renamed and removed statuses hundreds of times.
	MaxStatusAliasCount   = 256
	MaxStatusRetiredCount = 256
)

// The three status ceilings above are unlike every other ceiling in this file,
// and the difference is worth stating where somebody raising one will read it.
//
// A task ceiling is a property of one document, decided by whoever wrote it: a
// title is 500 bytes or it is not, and the clone that wrote an oversized one is
// the clone that erred. The status ceilings are properties of a project that
// several clones edit concurrently, so no single author decides them. Two
// people adding a status on the same afternoon can carry a project past
// MaxStatusCount without either one being told anything — there is no operation
// either could have written differently.
//
// So these three are checked when a pack is authored (validateVocabularyGrowth,
// reached from ValidateConfigAuthoring) and never when one is folded. A fold
// that could fail on a count would let that pair of ordinary edits produce a
// history no clone can ever read, and append-only means there is no repair: the
// removal that would bring the project back under the ceiling sits behind the
// fold that already failed. A folded state may therefore exceed one of these,
// and nothing reports that yet: PR-B will surface it as a historyvalidation
// advisory, and PR-C's status list will say so where a person can act on it.

// statusTokenPattern is the one rule for a status name, everywhere.
//
// Lowercase kebab-case is not a style preference. A status name is a bare word
// in four different grammars at once, and this charset is the intersection that
// needs no escaping in any of them:
//
//   - a shell token, typed as `--status in-progress` with no quoting;
//   - an HTML attribute value, emitted as data-status="in-progress" by the web
//     board, where a quote or an angle bracket would be an injection;
//   - a Git commit subject, written as "status backlog → in-progress", where a
//     newline would end the subject line;
//   - a SQLite TEXT column and a JSON object value in the projection.
//
// Case folding is settled the same way: allowing "Done" and "done" to name
// different statuses would make a filter's behavior depend on a shift key, and
// allowing them to name the same one would require every consumer to agree on a
// folding rule. Forbidding uppercase settles it once. Display casing lives in
// the label, which is free text.
//
// All six built-in statuses conform, so this is a rule the vocabulary work
// adopts rather than one it introduces.
var statusTokenPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validateStatusToken reports whether a status is well formed as a token. It is
// deliberately not a membership check: a stored status names whatever the clone
// that wrote it had configured, and a build that rejected an unknown-but-well-
// formed name would refuse to read a repository a teammate can read. Membership
// belongs at the mutation boundary, where a person is choosing a value and can
// be told which ones exist.
func validateStatusToken(status Status) error {
	if status == "" {
		return Errorf(CategoryValidation, "status must not be blank")
	}
	if len(status) > MaxStatusNameBytes {
		return Errorf(
			CategoryValidation,
			"status is %d bytes and must not exceed %d",
			len(status), MaxStatusNameBytes,
		)
	}
	if !statusTokenPattern.MatchString(string(status)) {
		return Errorf(
			CategoryValidation,
			"status %q must be lowercase letters and digits separated by single hyphens",
			status,
		)
	}
	return nil
}

// validateStatusLabel bounds a status display label. A blank label is rejected
// rather than defaulted: a column with no heading is a rendering bug that would
// otherwise reach every consumer before anyone noticed.
func validateStatusLabel(label string) error {
	if strings.TrimSpace(label) == "" {
		return Errorf(CategoryValidation, "status label must not be blank")
	}
	if len(label) > MaxStatusLabelBytes {
		return Errorf(
			CategoryValidation,
			"status label is %d bytes and must not exceed %d",
			len(label), MaxStatusLabelBytes,
		)
	}
	return nil
}

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
