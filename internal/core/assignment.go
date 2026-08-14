package core

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Assignment is one recorded claim of responsibility for a task.
//
// It is a convergent collection member, exactly like a label: concurrent
// assignments from two clones both survive, because multi-assignment is the
// semantics rather than a conflict. Pairing and independent spikes are working
// modes a tracker has no business calling a disagreement, and a data model that
// treated the second assignment as one would have to pick a winner — which is
// the claim-fight loop this design exists to dissolve.
//
// Four members, and each answers a different question. Principal is who is
// responsible, and it is the asserted Git identity that authored the operation
// or was named by it; there is no roster and no verification protocol, because
// verifying identity would import the central authority Workbook deliberately
// lacks. Label says which agent of that principal holds it — a fleet member, a
// worktree, a spike — and is free text because no vocabulary could anticipate
// what somebody names their agents. Creator is the actor whose operation
// recorded the assignment, which is what makes a mistaken tag of a teammate
// undoable by its author. CreatedAt is the recording pack's wall time, which is
// what a staleness display is computed from.
//
// Creator and CreatedAt are recorded rather than derived on read for one
// reason: they are the fold's evidence. The removal rule is decided from the
// assignment's own principal and creator, so both have to be in the history
// that every clone folds, not in anything local.
type Assignment struct {
	Principal string    `json:"principal"`
	Label     string    `json:"label,omitempty"`
	Creator   string    `json:"creator"`
	CreatedAt time.Time `json:"createdAt"`
}

// assignmentLabelSeparator divides an assignment value's principal from its
// agent label. It is a slash because that is how the identity reads out loud —
// dylan@example.com/impl-1 — and because it cannot occur in the principal,
// which an email address never contains.
const assignmentLabelSeparator = "/"

// Value renders the assignment as the token an operation carries and a person
// types: the principal, optionally qualified by its label.
func (assignment Assignment) Value() string {
	return AssignmentValue(assignment.Principal, assignment.Label)
}

// AssignmentValue composes the wire form of a principal and an optional label.
func AssignmentValue(principal, label string) string {
	if label == "" {
		return principal
	}
	return principal + assignmentLabelSeparator + label
}

// Identifies reports whether this assignment is the one a value names. The
// pair (principal, label) is an assignment's whole identity: creator and
// creation time are attribution, not identity, so re-adding an assignment
// somebody else already recorded finds the existing one rather than making a
// second.
func (assignment Assignment) Identifies(principal, label string) bool {
	return assignment.Principal == principal && assignment.Label == label
}

// RemovableBy reports whether an actor may remove this assignment.
//
// This is the removal rule, and it is a pure function of data that lives in the
// history: the actor stamped on the removing pack, and the principal and
// creator this assignment recorded when it was added. Nothing about the local
// clone — not its configured identity, not its clock, not its vocabulary —
// takes part, which is what lets Apply enforce the rule identically on every
// honest reader replaying the same bytes.
//
// Two branches, for two different people:
//
//   - the assignee-principal, whatever label the assignment carries. That is
//     the clause an orchestrator sweeps its fleet up with: agents assign
//     themselves as principal/impl-1, principal/impl-2, and the principal can
//     clear all of them, including the ones whose agent crashed.
//   - the actor who created it, so a mistaken tag of a teammate is undoable by
//     the person who made the mistake rather than only by the person it
//     inconvenienced.
//
// The label plays no part in authority. It answers which agent holds the
// assignment, and an agent is not a separate person; making it authoritative
// would strand an assignment whose agent no longer exists.
func (assignment Assignment) RemovableBy(actor string) bool {
	return actor == assignment.Principal || actor == assignment.Creator
}

// assignmentValuePattern is the structural rule for an assignment value,
// applied inside the fold.
//
// It is deliberately weaker than what the mutation boundary asks for, and the
// difference is the same one ValidateStatusToken draws: this runs over
// operations another clone already committed, so it may only reject values no
// build could have written. What it excludes is whitespace and ASCII control
// characters, for the reasons a status token excludes them — an assignment
// value is written into a Git commit subject, an HTML attribute on the board,
// and a shell word.
//
// The empty principal and the empty label are refused too, but separately, so
// that each says which half is missing rather than restating the whole grammar.
//
// Whether the principal looks like an email address is a question for the
// person typing it, and ValidateAssigneeAuthoring is where it is asked.
var assignmentValuePattern = regexp.MustCompile(`^[^\x00-\x20\x7f]+$`)

// assignmentPrincipalPattern is the boundary's plausibility check.
//
// A principal is a Git identity in a repository several people push to, so
// "plausible" means routable: a local part, one at sign, and a domain with a
// dot in it. This rejects a typo like `--assign dylan` before it becomes a
// permanent row in shared history, and it is asked only where somebody is
// choosing a value — a fetched assignment that does not match is still folded,
// because a teammate's identity is not this clone's to judge.
var assignmentPrincipalPattern = regexp.MustCompile(`^[^\x00-\x20\x7f@/]+@[^\x00-\x20\x7f@/]+\.[^\x00-\x20\x7f@/]+$`)

// SplitAssignmentValue divides an assignment value into its principal and its
// optional label, checking only the structure every generation of this format
// shares.
//
// It splits on the first separator, so a label may itself contain slashes: the
// principal is an email address and cannot, which makes the first slash
// unambiguous and leaves the free-form half free.
func SplitAssignmentValue(value string) (principal, label string, err error) {
	if err := validateAssignmentValueShape(value); err != nil {
		return "", "", err
	}
	principal, label, _ = strings.Cut(value, assignmentLabelSeparator)
	return principal, label, nil
}

// validateAssignmentValueShape bounds and shapes an assignment value without
// deciding anything about who wrote it.
//
// The size is checked before the pattern, and reported without quoting the
// value: an assignment value is bounded only by the object ceiling once it is
// inside a stored document, and formatting a rejected megabyte into an error
// message would spend exactly the cost the ceiling withholds.
func validateAssignmentValueShape(value string) error {
	if value == "" {
		return Errorf(CategoryValidation, "assignment must not be blank")
	}
	if len(value) > MaxAssignmentBytes {
		return Errorf(
			CategoryValidation,
			"assignment is %d bytes and must not exceed %d",
			len(value), MaxAssignmentBytes,
		)
	}
	if !assignmentValuePattern.MatchString(value) {
		return Errorf(
			CategoryValidation,
			"assignment %q must not contain spaces or control characters",
			value,
		)
	}
	principal, label, labelled := strings.Cut(value, assignmentLabelSeparator)
	if principal == "" {
		return Errorf(CategoryValidation, "assignment %q must name a principal before its /label", value)
	}
	if labelled && label == "" {
		return Errorf(CategoryValidation, "assignment %q must not end with /; the agent label must not be blank", value)
	}
	if len(principal) > MaxAssignmentPrincipalBytes {
		return Errorf(
			CategoryValidation,
			"assignment principal is %d bytes and must not exceed %d",
			len(principal), MaxAssignmentPrincipalBytes,
		)
	}
	if len(label) > MaxAssignmentLabelBytes {
		return Errorf(
			CategoryValidation,
			"assignment label is %d bytes and must not exceed %d",
			len(label), MaxAssignmentLabelBytes,
		)
	}
	return nil
}

// ValidateAssigneeAuthoring rejects an assignment value somebody is choosing.
//
// It is the mutation boundary's rule and is exported for the same reason
// ValidateStatusToken is: every check inside the fold reports a malformed value
// as corrupt data, which is the right verdict for a document read off a ref and
// the wrong one for a typo. Asking here first is what makes assigning to
// "dylan" a validation failure that quotes the rule rather than a claim that
// the repository is damaged.
func ValidateAssigneeAuthoring(value string) error {
	principal, _, err := SplitAssignmentValue(value)
	if err != nil {
		return err
	}
	if !assignmentPrincipalPattern.MatchString(principal) {
		return Errorf(
			CategoryValidation,
			"assignment principal %q must be an email address, optionally followed by /label",
			principal,
		)
	}
	return nil
}

// validateStoredAssignment checks one assignment inside a checkpoint.
//
// Every member is required. An assignment with no creator or no creation time
// is not a weaker assignment, it is a record the removal rule cannot be decided
// from — and a fold that accepted one would have to invent an answer to "who
// may remove this?" on the spot, differently in each build that tried.
func validateStoredAssignment(assignment Assignment) error {
	if err := validateAssignmentValueShape(assignment.Value()); err != nil {
		return err
	}
	if strings.TrimSpace(assignment.Creator) == "" {
		return Errorf(CategoryValidation, "task assignment %q records no creator", assignment.Value())
	}
	if len(assignment.Creator) > MaxAssignmentPrincipalBytes {
		return Errorf(
			CategoryValidation,
			"task assignment creator is %d bytes and must not exceed %d",
			len(assignment.Creator), MaxAssignmentPrincipalBytes,
		)
	}
	if assignment.CreatedAt.IsZero() {
		return Errorf(CategoryValidation, "task assignment %q records no creation time", assignment.Value())
	}
	return nil
}

// normalizeAssignments returns the canonical assignment list: every member
// checked, ordered by principal and then label.
//
// An empty list normalizes to nil rather than to an empty slice, which is the
// one thing in this file that is about bytes rather than meaning. The member is
// omitted when it is empty, so every task document written before assignments
// existed — which is every task document in every repository — still encodes to
// exactly the bytes it already has. Labels and dependencies do the opposite and
// materialize as `[]`, because they were in the document from the first
// version; assignments cannot be, and making them absent-when-empty is what
// keeps the golden byte tables green.
//
// A duplicate is refused rather than deduplicated. Two entries naming the same
// principal and label carry different creators or different creation times, and
// keeping one would be a guess about which assignment the history meant —
// where deduplicating labels keeps a value that is identical either way. The
// fold never produces one, so reaching this is a hand-built document.
//
// How many assignments a task may carry is deliberately not asked here. See
// MaxAssignmentCount: it is a property of a task several clones assign
// concurrently rather than of any one document somebody wrote, so it belongs at
// the boundary and nowhere else.
func normalizeAssignments(assignments []Assignment) ([]Assignment, error) {
	if len(assignments) == 0 {
		return nil, nil
	}
	normalized := make([]Assignment, 0, len(assignments))
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		if err := validateStoredAssignment(assignment); err != nil {
			return nil, err
		}
		value := assignment.Value()
		if _, duplicate := seen[value]; duplicate {
			return nil, Errorf(CategoryValidation, "task assignment %q appears more than once", value)
		}
		seen[value] = struct{}{}
		normalized = append(normalized, assignment)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Principal != normalized[j].Principal {
			return normalized[i].Principal < normalized[j].Principal
		}
		return normalized[i].Label < normalized[j].Label
	})
	return normalized, nil
}

// findAssignment reports where a value's assignment sits, if it is there at all.
func findAssignment(assignments []Assignment, principal, label string) (int, bool) {
	for index, assignment := range assignments {
		if assignment.Identifies(principal, label) {
			return index, true
		}
	}
	return -1, false
}

// AssignmentsHeldByOthers returns the assignments naming a principal other than
// the one given, in the stored order.
//
// It exists for the claim path the CLI story builds next: self-assigning has to
// warn when the task is already somebody else's without reading the task twice,
// so the mutation that observed the parent is the one that answers.
func AssignmentsHeldByOthers(assignments []Assignment, principal string) []Assignment {
	others := make([]Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.Principal != principal {
			others = append(others, assignment)
		}
	}
	if len(others) == 0 {
		return nil
	}
	return others
}

func copyAssignments(assignments []Assignment) []Assignment {
	if assignments == nil {
		return nil
	}
	return append([]Assignment(nil), assignments...)
}

// SameAssignments compares two assignment lists member by member, and compares
// the creation times with Equal rather than with ==. A time carrying a
// monotonic reading is not == to the same instant read back out of JSON, and
// one side of every comparison this feeds is a freshly folded state whose clock
// came from time.Now.
//
// It is exported for reconciliation, which decides whether a replayed pack
// earns a commit by asking whether it changed anything an operator can see. A
// replayed assignment that the fetched history already carries — or one the
// removal rule folded away — has to answer "no" there, or every synchronization
// would record an empty commit saying nothing happened.
func SameAssignments(left, right []Assignment) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Principal != right[index].Principal ||
			left[index].Label != right[index].Label ||
			left[index].Creator != right[index].Creator ||
			!left[index].CreatedAt.Equal(right[index].CreatedAt) {
			return false
		}
	}
	return true
}
