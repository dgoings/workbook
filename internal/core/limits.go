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
	// MaxAssignmentPrincipalBytes bounds an assignment's principal, which is an
	// email address. RFC 5321 caps a reverse-path at 256 octets including the
	// angle brackets, so 254 is the longest address that can be delivered — a
	// ceiling the format did not choose so much as inherit.
	MaxAssignmentPrincipalBytes = 254
	// MaxAssignmentLabelBytes bounds an assignment's agent label. It matches
	// MaxLabelBytes because the two are the same kind of thing: a short name
	// somebody gives a thing, read in a board chip and a commit subject.
	MaxAssignmentLabelBytes = 100
	// MaxAssignmentBytes bounds one whole assignment value, principal and
	// label and the separator between them. It is stated rather than derived so
	// that the one number a message quotes is the one a reader can look up.
	MaxAssignmentBytes = MaxAssignmentPrincipalBytes + 1 + MaxAssignmentLabelBytes
	// MaxAssignmentCount bounds how many assignments one task may be given, and
	// it is checked only where somebody is giving one.
	//
	// It belongs with the status ceilings below rather than with the task
	// ceilings above, for the reason stated there at length: no single author
	// decides it. Two people — or an orchestrator's two agents — assigning
	// themselves the same task on the same afternoon can carry it past this
	// number without either one being told anything, because neither operation
	// could have been written differently. A fold that failed on the count
	// would turn that pair of ordinary acts into a task no clone can ever read,
	// and append-only means the removal that would bring it back under the
	// ceiling sits behind the fold that already failed.
	//
	// Fifty is set where an assignment list stops being a signal. A pairing
	// session is two, a fleet is a handful, and a task with fifty holders has
	// stopped saying who is responsible for it.
	MaxAssignmentCount = 50
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
	// MaxProjectNameBytes bounds the display name a project may give itself.
	//
	// It sits with the status ceilings rather than with the task ceilings above
	// because it is the same kind of value: something one project records once,
	// read as a heading by everyone who fetches, and typed as a command
	// argument. It matches MaxLabelBytes because a project name and a label are
	// the same shape of thing — a short name somebody gives a thing — and
	// because a hundred bytes is already past where a name stops fitting a
	// browser tab, which is the place it exists to be read.
	MaxProjectNameBytes = 100
)

// Ceilings on a task's thread and its attachments.
//
// These are the vocabulary's kind of ceiling rather than the task document's,
// and the distinction decides where they are asked. A title is one author's
// value: whoever wrote an oversized one erred, and refusing it in the fold
// refuses exactly their document. A thread and an attachment list are
// collections several clones extend at once, so two people commenting on the
// same afternoon can carry a task past a count without either having written
// anything they could have written differently. A fold that failed on that
// would produce a history no clone can ever read, and append-only means there
// is no repair — the removal that would bring the task back under the ceiling
// sits behind the fold that already failed.
//
// So every ceiling below is asked at the mutation boundary, in Service, and
// never while folding. A replayed pack is somebody's recorded history and folds
// whatever it says; a person about to write one is told no, in a message that
// names the bound.
const (
	// MaxCommentBodyBytes bounds one comment body. A comment is a remark rather
	// than the record of the work, which is what the description is and why
	// that ceiling is four times this one; a body approaching this belongs in
	// the description or in the repository.
	MaxCommentBodyBytes = 16 << 10
	// MaxCommentCount bounds how many live comments one task may carry. Every
	// clone reads every task's checkpoint on every list, so a thread is not
	// only its own task's cost.
	MaxCommentCount = 500
	// MaxAttachmentCount bounds how many live attachments one task may carry,
	// of both kinds together.
	MaxAttachmentCount = 50
	// MaxAttachmentNameBytes bounds a file attachment's name, at the width a
	// file name has on the systems it will be saved onto.
	MaxAttachmentNameBytes = 255
	// MaxAttachmentLabelBytes bounds a link attachment's display text.
	MaxAttachmentLabelBytes = 200
	// MaxAttachmentURLBytes bounds a link's URL.
	MaxAttachmentURLBytes = 2048
	// MaxAttachmentMediaBytes bounds a stored media type.
	MaxAttachmentMediaBytes = 100
	// MaxAttachmentFileBytes bounds one attached file.
	//
	// A megabyte is a screenshot, a log excerpt, or a patch, and it is under
	// the four-megabyte object ceiling every read of a Git object is bounded
	// by, so an attachment is always readable in one `cat-file`. Anything
	// larger belongs somewhere with its own storage, which is what the refusal
	// says by suggesting a link.
	MaxAttachmentFileBytes = 1 << 20
	// MaxLiveAttachmentBytes bounds what one task's live file attachments add
	// up to.
	//
	// This is the ceiling that actually protects the team. Task refs are
	// fetched by every clone, and a task nobody bounded could quietly become
	// the reason a fresh clone takes minutes. Ten megabytes is ten screenshots
	// of the largest size a single attachment may be.
	//
	// It counts live attachments only, which is a deliberate understatement:
	// removing an attachment frees room under this ceiling while reclaiming
	// nothing on disk, because the bytes stay in the commit that added them
	// until a compaction pass rewrites the task's history. The alternative —
	// counting every attachment a task ever held — would make the ceiling
	// permanent and unrepairable, which is the failure mode this whole comment
	// block exists to avoid.
	MaxLiveAttachmentBytes = 10 << 20
)

// validateThreadGrowth refuses a pack that would push a task past one of the
// ceilings above, and only when the pack is what pushes it there.
//
// The comparison is against the parent rather than against the ceiling alone,
// for the reason validateVocabularyGrowth states about statuses and this file's
// header restates about threads: a folded task may already sit over a ceiling,
// because two people can comment concurrently and neither wrote anything they
// could have written differently. A rule that refused every mutation while over
// one would refuse the removals that bring the task back under, which is the
// only way out. So growth is refused and shrinkage is always allowed — and the
// mutations that touch neither, a move or a status change on a task with a long
// thread, are never refused for a collection they do not change.
//
// The per-item ceilings are asked only of items this pack introduces, on the
// same principle: a comment already in the thread is history, and the mutation
// adding a second one is not the moment to relitigate the first.
func validateThreadGrowth(before, after TaskData) error {
	if len(after.Comments) > MaxCommentCount && len(after.Comments) > len(before.Comments) {
		return Errorf(
			CategoryValidation,
			"task would hold %d comments and must not exceed %d; remove one first",
			len(after.Comments), MaxCommentCount,
		)
	}
	for _, comment := range after.Comments {
		index := findComment(before.Comments, comment.ID)
		if index >= 0 && before.Comments[index].Body == comment.Body {
			continue
		}
		if len(comment.Body) > MaxCommentBodyBytes {
			return Errorf(
				CategoryValidation,
				"comment body is %d bytes and must not exceed %d",
				len(comment.Body), MaxCommentBodyBytes,
			)
		}
	}
	if len(after.Attachments) > MaxAttachmentCount && len(after.Attachments) > len(before.Attachments) {
		return Errorf(
			CategoryValidation,
			"task would hold %d attachments and must not exceed %d; remove one first",
			len(after.Attachments), MaxAttachmentCount,
		)
	}
	for _, attachment := range after.Attachments {
		if findAttachment(before.Attachments, attachment.ID) >= 0 {
			continue
		}
		if err := validateAttachmentAuthoring(attachment.AttachmentData); err != nil {
			return err
		}
	}
	total := LiveAttachmentBytes(after.Attachments)
	if total > MaxLiveAttachmentBytes && total > LiveAttachmentBytes(before.Attachments) {
		return Errorf(
			CategoryValidation,
			"task would hold %d bytes of attached files and must not exceed %d; attach a link instead",
			total, MaxLiveAttachmentBytes,
		)
	}
	return nil
}

// validateAttachmentAuthoring bounds one attachment. The file-size refusal
// names the link because that is the whole advice: the task is the wrong place
// for these bytes, and there is somewhere right.
func validateAttachmentAuthoring(data AttachmentData) error {
	if len(data.Name) > MaxAttachmentNameBytes {
		return Errorf(
			CategoryValidation,
			"attachment name is %d bytes and must not exceed %d",
			len(data.Name), MaxAttachmentNameBytes,
		)
	}
	if len(data.Label) > MaxAttachmentLabelBytes {
		return Errorf(
			CategoryValidation,
			"attachment label is %d bytes and must not exceed %d",
			len(data.Label), MaxAttachmentLabelBytes,
		)
	}
	if len(data.URL) > MaxAttachmentURLBytes {
		return Errorf(
			CategoryValidation,
			"attachment URL is %d bytes and must not exceed %d",
			len(data.URL), MaxAttachmentURLBytes,
		)
	}
	if len(data.Media) > MaxAttachmentMediaBytes {
		return Errorf(
			CategoryValidation,
			"attachment media type is %d bytes and must not exceed %d",
			len(data.Media), MaxAttachmentMediaBytes,
		)
	}
	if data.Size > MaxAttachmentFileBytes {
		return Errorf(
			CategoryValidation,
			"attachment is %d bytes and must not exceed %d; attach a link instead",
			data.Size, MaxAttachmentFileBytes,
		)
	}
	return nil
}

// Bounds on how much configuration history one process is willing to fold.
//
// These two are a third kind of ceiling, and the distinction matters more than
// the numbers. A task ceiling says what a document may contain. The status
// ceilings above say what a project may be authored into. These say what this
// process will spend on somebody else's history — and nothing more. Exceeding
// one is never a statement that a checkpoint is wrong: the pack is somebody's
// recorded history, it folds perfectly well on a clone willing to spend the
// time, and calling it corrupt would take a repository away from a whole team
// because one of them scripted a large change. So a pack over one of these is
// refused as an operational failure that names the bound, the local canonical
// ref is left exactly where it was, and the next run with a raised bound
// succeeds. Nothing here may ever produce CategoryCorruptData.
//
// They are not enforced by validateConfigOperationPackDocument, deliberately,
// for the reason normalizeVocabularyDocument records at length: a rule inside
// the fold that can fail on a count can be made to fail forever by two clones
// each doing something they were allowed to do. These are asked by the reader
// and by reconciliation, which can decline without deciding anything about the
// data.
//
// The numbers are set against the measured cost of the fold, which is
// quadratic in the ledger's accumulated forwarding chains: every operation
// resolves its subject through them, and every rename adds one, so a ledger of
// 10,000 renames folds in about 2.7 seconds. One reconciliation folds at most
// MaxConfigOperationsPerPack × MaxConfigLedgerReplayCommits = 8,192
// operations, which is 0.82 of that measurement's size and therefore about
// 0.67 of its time — under two seconds for the worst pack a peer can
// construct, and microseconds for every pack a Workbook command writes.
const (
	// MaxConfigOperationsPerPack bounds how many configuration operations one
	// commit's pack may carry.
	//
	// Every configuration command writes one operation; the largest batch a
	// Workbook command can author is a reorder of every live status, which
	// MaxStatusCount holds to 24. Sixty-four therefore leaves a factor of two
	// and a half over anything this version can produce, while keeping a
	// hand-built pack from turning one fold into an unbounded one.
	MaxConfigOperationsPerPack = 64
	// MaxConfigLedgerReplayCommits bounds how many local-only configuration
	// commits one reconciliation will replay onto a fetched tip.
	//
	// A local-only configuration commit is one status change made while this
	// clone could not reach origin, so 128 of them is weeks of offline
	// configuration work by one person. The shared prefix both clones already
	// hold is not folded and is not bounded here: replay starts at the fetched
	// tip's stored checkpoint, so the cost of a reconciliation depends on how
	// far this clone has drifted, not on how old the project is.
	MaxConfigLedgerReplayCommits = 128
)

// The three status ceilings above — and MaxAssignmentCount with them — are
// unlike every other ceiling in this file, and the difference is worth stating
// where somebody raising one will read it.
//
// A task ceiling is a property of one document, decided by whoever wrote it: a
// title is 500 bytes or it is not, and the clone that wrote an oversized one is
// the clone that erred. The status ceilings are properties of a project that
// several clones edit concurrently, so no single author decides them. Two
// people adding a status on the same afternoon can carry a project past
// MaxStatusCount without either one being told anything — there is no operation
// either could have written differently.
//
// So these are checked when a pack is authored — the status ceilings in
// validateVocabularyGrowth, reached from ValidateConfigAuthoring, and
// MaxAssignmentCount in Service.assignmentOperations — and never when folded.
// A fold
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

// ValidateStatusToken reports whether a status is well formed as a token. It is
// deliberately not a membership check: a stored status names whatever the clone
// that wrote it had configured, and a build that rejected an unknown-but-well-
// formed name would refuse to read a repository a teammate can read. Membership
// belongs at the mutation boundary, where a person is choosing a value and can
// be told which ones exist.
//
// It is exported for that boundary. The status verbs build a configuration
// operation out of a word somebody typed, and every check inside the operation
// document reports a malformed member as corrupt data — the right verdict for
// a document read off a ref, and the wrong one for a typo. Asking here first is
// what makes `workbook status add "Not A Token"` a validation failure that
// quotes the rule.
func ValidateStatusToken(status Status) error {
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

// ValidateStatusLabel bounds a status display label. A blank label is rejected
// rather than defaulted: a column with no heading is a rendering bug that would
// otherwise reach every consumer before anyone noticed.
func ValidateStatusLabel(label string) error {
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

// ValidateProjectName bounds the display name a project gives itself, on the
// same terms ValidateStatusLabel bounds a column heading.
//
// A blank name is rejected rather than stored, and the difference from clearing
// one matters: `workbook config unset project-name` records that the project
// has no name and the board falls back to what it always said, while a name of
// spaces would be a configured name that renders as an empty heading. There is
// one way to mean "no name", and it is not a value.
func ValidateProjectName(name string) error {
	if strings.TrimSpace(name) == "" {
		return Errorf(CategoryValidation, "project name must not be blank; clear it with `workbook config unset project-name`")
	}
	if len(name) > MaxProjectNameBytes {
		return Errorf(
			CategoryValidation,
			"project name is %d bytes and must not exceed %d",
			len(name), MaxProjectNameBytes,
		)
	}
	return nil
}

// themeColorPattern is the one color notation Workbook stores.
//
// Six hex digits behind a hash is the notation every surface that reads one can
// already parse — a CSS declaration, an HTML attribute, a color picker's value —
// and the only one that needs no interpretation to be exact. The three-digit
// shorthand, the eight-digit form with alpha, `rgb()` and the named colors are
// all refused rather than expanded, because each would be a second spelling of a
// value the ledger compares by bytes: two clones that configured the same color
// two ways would hold two different configurations.
//
// Case is accepted on input and folded on the way in, for the same reason a
// status token forbids uppercase outright: allowing `#ABC123` and `#abc123` to
// be two stored values would make the durable format depend on a shift key. The
// difference is that a color has an obvious canonical form, so this one folds
// where a status name refuses.
var themeColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// ValidateThemeColor accepts a color in either case and returns the lowercase
// form the ledger stores.
func ValidateThemeColor(value string) (string, error) {
	if value == "" {
		return "", Errorf(CategoryValidation, "color must not be blank")
	}
	if !themeColorPattern.MatchString(value) {
		return "", Errorf(
			CategoryValidation,
			"color %q must be six hexadecimal digits behind a hash, as in #1a7f4b",
			value,
		)
	}
	return strings.ToLower(value), nil
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
