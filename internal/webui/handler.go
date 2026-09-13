package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/presentation"
)

// securityPolicy is what the page itself is served under.
//
// `img-src 'self'` is here because a description may draw an attachment of its
// own task, through this server's own download route. Without the directive
// those images fall back to `default-src 'none'` and a browser blocks every one
// of them: measured in Chrome before it was added, twelve requests refused with
// reason "csp" and a 19px broken-image box where the picture should be. The
// fake DOM cannot see this — it has no loader and no policy — so the same page
// that passed every test drew nothing at all.
//
// `'self'` and not one character more. The renderer already refuses every image
// target that is not an attachment of the task, and this is the second lock on
// the same door: an external image in a task description is a tracking beacon
// fired by every reader of the board, and a policy that named a host, or a
// scheme, or `data:` would be the way one gets through a bug in the first lock.
const securityPolicy = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

//go:embed assets/index.html
var assets embed.FS

// pageFuncs are the derivations the page template reaches for rather than
// re-deriving in template syntax.
//
// There is one, and it is the chip row, because that row is the one thing on a
// server-rendered card that the client also draws: the template calls this and
// the poll's presentation carries what this returned, so the card a reader loads
// and the card the first poll redraws hold the same chips. Every other card fact
// already arrives on presentation.TaskView for exactly that reason.
var pageFuncs = template.FuncMap{"cardAssignees": assignmentRow}

type TaskLister func(context.Context) ([]core.Task, error)

type TaskStatusUpdater func(context.Context, string, core.Status, string) (core.MutationResult, error)

type TaskPositionUpdater func(context.Context, string, core.PlaceInput) (core.MutationResult, error)

type TaskCreator func(context.Context, core.CreateInput) (core.MutationResult, error)

type TaskUpdater func(context.Context, string, core.UpdateInput) (core.MutationResult, error)

type TaskDeleter func(context.Context, string, core.DeleteInput) (core.MutationResult, error)

type TaskRestorer func(context.Context, string, core.RestoreInput) (core.MutationResult, error)

type TaskDependencyAdder func(context.Context, string, string) (core.MutationResult, error)

type TaskDependencyRemover func(context.Context, string, string) (core.MutationResult, error)

// TaskHistoryReader reads one task together with its complete change log. The
// detail view shows history by default rather than behind an opt-in flag, and
// the status lifecycle lane it derives has to reach back to the task's
// creation, so the board asks for the whole chain rather than the CLI's
// ten-change default window.
type TaskHistoryReader func(context.Context, string) (core.TaskDetail, error)

// SyncStateReporter answers what the board will do with the next mutation.
type SyncStateReporter func(context.Context) SyncState

// SyncModeSetter shifts between handing publication to a watcher and waiting
// for the push. It rejects a mode it does not recognize.
type SyncModeSetter func(context.Context, string) (SyncState, error)

// SyncModeDeferred hands publication to a running watcher; SyncModeInline waits
// for the push so a successful response means origin has the change.
const (
	SyncModeDeferred = "deferred"
	SyncModeInline   = "inline"
)

// SyncState is what the board reports about publication. Watcher is false when
// no trustworthy watcher answers, in which case a deferred board still falls
// back to publishing inline and the indicator says so.
//
// Reason names why Watcher is false in a form a program can branch on, because
// the two cases are not the same news and Detail is prose. A clone with no
// origin publishes nothing in either mode and no watcher will ever change that;
// a clone with an origin and nobody answering is one `workbook sync --watch`
// away from deferring again. A reader told the second when the first is true is
// being sent to start a watcher that cannot help.
type SyncState struct {
	Mode    string `json:"mode"`
	Watcher bool   `json:"watcher"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// The reasons a board reports for having no watcher to defer to. Both are
// absent while Watcher is true, and a client that does not recognize the value
// it is given still has Detail to show.
const (
	// SyncReasonNoOrigin means this clone has no origin, so nothing is
	// published in either mode and a watcher would have nowhere to push.
	SyncReasonNoOrigin = "no-origin"
	// SyncReasonNoWatcher means no watcher answers for the board to defer to —
	// none running, none reachable, or one whose last synchronization left it
	// untrustworthy — so both modes publish inline.
	SyncReasonNoWatcher = "no-watcher"
)

type SyncDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Sync    SyncState `json:"sync"`
}

type syncModeRequest struct {
	Mode string `json:"mode"`
}

type TasksDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// VocabularyHead is the configuration ledger tip the columns in this
	// response were built from, empty for a project that has never recorded one.
	//
	// It rides along with the tasks because the board polls this route once a
	// second and nothing else would tell it the columns had changed. A client
	// compares it with the head it rendered under and says so; it deliberately
	// does not carry the vocabulary itself, which is what /api/vocabulary is
	// for and is far larger than a poll should move every second.
	VocabularyHead string             `json:"vocabularyHead"`
	Tasks          []core.Task        `json:"tasks"`
	Presentation   []TaskPresentation `json:"presentation"`
}

// VocabularyState is what a resolver reports: the project's statuses, the
// display settings recorded beside them, and the configuration ledger tip both
// were read from.
type VocabularyState struct {
	Vocabulary core.Vocabulary
	// Head is the configuration ledger's tip, empty for a project with none.
	Head string
	// Display is what this project calls its board and the colors it draws it
	// in, zero for a project that has configured none of them.
	//
	// It rides in the state the statuses ride in rather than behind a resolver
	// of its own because it is the same commit: a board that read its columns
	// and then its name could be answered from either side of a fetch and would
	// draw one page out of two configurations. gitstore.LoadVocabularyState
	// answers both from one read for exactly that reason.
	Display core.DisplaySettings
	// Priorities is this project's priority vocabulary, recorded in the same
	// ledger section beside the statuses and the display settings and read from
	// the same commit, for the reason Display is: a board that read its columns
	// and then its priorities could be answered from either side of a fetch and
	// would sort one page's cards against another page's order.
	//
	// It is the zero vocabulary for a project that has configured none, which
	// every PriorityVocabulary accessor but Document and Validate reads as the
	// built-in three. That substitution is why every producer of this state has
	// to fill this field or say in a comment that it means not to: a producer
	// that leaves it zero does not answer "I did not read the priorities", it
	// answers "this project's priorities are high, medium and low" — and a
	// client that adopts an answer wholesale would write that over a project's
	// own priorities on the strength of a status rename.
	Priorities core.PriorityVocabulary
}

// VocabularyResolver reads the project's current statuses.
//
// It is a function rather than a value because `workbook serve` runs for hours:
// a snapshot taken at startup would keep drawing a column somebody deleted at
// lunchtime, and no reload of the page would fix it. Every request that needs
// statuses calls this, so a change on disk reaches a running server on the next
// poll.
type VocabularyResolver func(context.Context) (VocabularyState, error)

// VocabularyDocument is the project's status configuration as the board reads
// it.
//
// The shape is core's own: Statuses in configured order, each with its token,
// label, rank and tags; Aliases and Retired as the forwarding chains a stored
// status is resolved through. Default is derived rather than left to the client
// to find, because "the status tagged default" is a rule and a client that
// re-derived it could disagree with the server about where a new task lands.
//
// It is what GET /api/vocabulary serves and what every vocabulary mutation
// answers with, so a client hands both to the same renderer.
type VocabularyDocument struct {
	Format   string                  `json:"format"`
	Version  int                     `json:"version"`
	Head     string                  `json:"head"`
	Default  core.Status             `json:"default"`
	Statuses []core.StatusDefinition `json:"statuses"`
	Aliases  []core.StatusAlias      `json:"aliases"`
	Retired  []core.RetiredStatus    `json:"retired"`
	// Display is this project's own name for its board and the colors it draws
	// it in, absent for a project that has configured none of them.
	//
	// It rides here rather than behind a route of its own so that a client
	// reads both halves of one configuration from one commit: the two are
	// recorded in the same ledger, and a page that asked separately could be
	// answered from either side of a change and would offer a Save composed
	// against a configuration nobody was shown. Its head is this document's
	// head, from the one state a request resolves.
	//
	// Absent rather than empty for an unconfigured project, so every response
	// this route gave before display settings existed is the response it gives
	// now, byte for byte.
	Display *DisplayDocument `json:"display,omitempty"`
	// Priorities is this project's priority vocabulary, riding here for the
	// reason Display does: the two are sections of one ledger, and a client that
	// asked for them separately could be answered from either side of a change
	// and would offer a Save composed against a configuration nobody was shown.
	//
	// Present always rather than omitted when unconfigured, which is where it
	// differs from Display: a board that has not been given a name has no name,
	// and a project that has not configured its priorities still has priorities
	// — the built-in three, which is what core substitutes and what the board
	// draws. So this member is the effective reading, and a client never has to
	// carry a fallback set of its own.
	Priorities PriorityVocabularyDocument `json:"priorities"`
}

// PriorityVocabularyDocument is a project's priority configuration as the board
// reads it: the live priorities in configured order, the forwarding chains a
// stored priority is resolved through, and the derived default.
//
// It mirrors the status half above member for member, Default included, and for
// the same reason — "the priority tagged default" is a rule, and a client that
// re-derived it could disagree with the server about the priority a new task is
// given.
type PriorityVocabularyDocument struct {
	Default    core.Priority             `json:"default"`
	Priorities []core.PriorityDefinition `json:"priorities"`
	Aliases    []core.PriorityAlias      `json:"aliases"`
	Retired    []core.RetiredPriority    `json:"retired"`
	// Ink is the per-priority stylesheet the board draws these priorities with,
	// composed here by the same priorityInk the page's own `<style>` block is
	// rendered from. It rides on the document so that a page that adopts a
	// change adopts what the change looks like: the client replaces the text of
	// that element and the board is drawn in the new colors without a reload.
	//
	// It is the composed CSS rather than the colors it was composed from, and
	// that is the whole of why this is safe. Every byte of it is a property name
	// this package wrote or a number it formatted out of three integers parsed
	// from a value core had already validated — see priorityInk, which answers
	// for all of them. A member that carried the stored colors instead would
	// leave the composition to a client that can vouch for none of that, and a
	// stored string would reach the page.
	//
	// It is not omitted when empty. A project whose priorities compose no ink at
	// all is a real reading, and a client handed no member would go on drawing
	// the ink it was opened with.
	Ink string `json:"ink"`
}

// VocabularyStatusAddition is a status the board asks this project to define.
//
// Tags are the words the CLI's --tag takes rather than core.StatusTag values,
// because the same parse that refuses an unknown --tag refuses an unknown one
// here, and it refuses the word somebody sent.
type VocabularyStatusAddition struct {
	Status core.Status
	// Label is the column heading. Empty means the client named none, and the
	// label is derived from the token exactly as `workbook status add` derives
	// one when --label is not given.
	Label string
	Tags  []string
	// Before and After place the new status next to a live one. At most one is
	// set; neither appends.
	Before core.Status
	After  core.Status
	// ExpectedHead is the configuration ledger tip the client composed this
	// change against. See requireVocabularyHead for why it is required here and
	// optional on a task mutation.
	ExpectedHead string
}

// VocabularyStatusEdit renames, relabels and retags one status, in any subset:
// a nil member is a member this change does not touch, which is what lets one
// form send one intent.
type VocabularyStatusEdit struct {
	Name         *core.Status
	Label        *string
	Tags         *[]string
	ExpectedHead string
}

// VocabularyStatusRemoval removes a status. Into is where its tasks belong and
// is never guessed: a removal with nowhere to forward to is a removal nobody
// could have meant.
type VocabularyStatusRemoval struct {
	Into         core.Status
	ExpectedHead string
}

// VocabularyOrder is the whole column order, because a drag is one gesture and
// one intent rather than a sequence of pairwise moves the client would have to
// keep in step with the server.
type VocabularyOrder struct {
	Statuses     []core.Status
	ExpectedHead string
}

// VocabularyTaskCounts prices a status change in the terms a person removing a
// column cares about. Both members are zero for every change but a removal, and
// stated rather than omitted, exactly as the CLI states them.
type VocabularyTaskCounts struct {
	// Affected counts the active tasks that resolved through the removed status.
	Affected int `json:"affected"`
	// ClaimableAfter counts how many of those become eligible for `workbook
	// next` where they land.
	ClaimableAfter int `json:"claimableAfter"`
}

// VocabularyMutation is what one vocabulary change produced: the statuses as
// they now stand, the tip they were written to, and what it cost.
type VocabularyMutation struct {
	State    VocabularyState
	Tasks    VocabularyTaskCounts
	Warnings []core.Warning
}

// The four capabilities behind the vocabulary mutation routes. Each answers
// with the whole vocabulary rather than with what it changed, because a status
// change can move a status the client did not name — a tag is exclusive, a
// removal retires a token — and a client that patched its own model from a
// description of one change would disagree with the server about the rest.
type (
	VocabularyStatusAdder   func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error)
	VocabularyStatusEditor  func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error)
	VocabularyStatusRemover func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error)
	VocabularyReorderer     func(context.Context, VocabularyOrder) (VocabularyMutation, error)
)

// VocabularyMutationDocument is what every vocabulary mutation answers with.
//
// It carries the whole vocabulary document, in the shape GET /api/vocabulary
// serves it, so the client renders the result of a change through the same code
// that rendered the page — including the new head, which is what its next
// change has to name.
type VocabularyMutationDocument struct {
	Format     string               `json:"format"`
	Version    int                  `json:"version"`
	Vocabulary VocabularyDocument   `json:"vocabulary"`
	Tasks      VocabularyTaskCounts `json:"tasks"`
	Warnings   []core.Warning       `json:"warnings,omitempty"`
}

// VocabularyPriorityAddition is a priority the board asks this project to
// define. It is the status addition's counterpart minus the tags: a priority
// carries exactly one role, and giving it is a change of its own — see
// VocabularyPriorityDefault.
type VocabularyPriorityAddition struct {
	Priority core.Priority
	// Label is what the priority is called on screen. Empty means the client
	// named none, and the label is derived from the token exactly as `workbook
	// priority add` derives one when --label is not given.
	Label string
	// Before and After place the new priority next to a live one. Naming both
	// is a contradiction, and it is refused by the writer rather than here: the
	// priority verbs already refuse it in one sentence, and a second check at
	// this layer would be a second place that sentence could change.
	Before core.Priority
	After  core.Priority
	// ExpectedHead is the configuration ledger tip the client composed this
	// change against. See vocabularyHead for why it is required here and
	// optional on a task mutation.
	ExpectedHead string
}

// VocabularyPriorityEdit renames and relabels one priority, in either subset: a
// nil member is a member this change does not touch, which is what lets one
// form send one intent.
//
// It has no Tags member where a status edit has one, and no role member
// either. A priority holds exactly one role, taking it is a transfer rather
// than a set to reconcile, and the route that does it is the default route —
// so a panel offering "rename this and make it the default" in one Save makes
// two requests, the second against the head the first answered with.
type VocabularyPriorityEdit struct {
	Name         *core.Priority
	Label        *string
	ExpectedHead string
}

// VocabularyPriorityRemoval removes a priority. Into is where its tasks belong
// and is never guessed: a removal with nowhere to forward to is a removal
// nobody could have meant.
type VocabularyPriorityRemoval struct {
	Into         core.Priority
	ExpectedHead string
}

// VocabularyPriorityMove moves one priority among its peers, naming the
// neighbor it goes next to.
//
// There is no whole-order counterpart to VocabularyOrder here, and the
// asymmetry is deliberate rather than an omission: a status drag is authored by
// a planner that sets every rank at once, and the priority section's planner
// names a neighbor. A drag on a priorities panel therefore has to reduce "this
// is the new order" to one anchor before it is sent. A second way to say where
// a priority goes is a second thing that can disagree with the first.
type VocabularyPriorityMove struct {
	Before       core.Priority
	After        core.Priority
	ExpectedHead string
}

// VocabularyPriorityDefault gives one priority the role a task with no priority
// lands on. It carries only a head because the change is its subject: the
// priority that held the role gives it up in the same operation.
//
// There is no clearing counterpart, because a project with no default priority
// is a project where a new task has nowhere to land, and the vocabulary's own
// validation refuses it.
type VocabularyPriorityDefault struct {
	ExpectedHead string
}

// VocabularyPriorityRecolor sets or clears the ink one priority is drawn in. An
// empty Color clears the stored value and returns the priority to the color the
// board derives from its position.
//
// The value travels as the client typed it: trimming it and reading what a
// color is belongs to the verb family, and the board keeps exactly one reading
// of that.
type VocabularyPriorityRecolor struct {
	Color        string
	ExpectedHead string
}

// VocabularyPriorityTaskCounts prices a priority change in the one term a
// priority change has: the tasks that were filed under a priority somebody
// removed.
//
// It carries one member where VocabularyTaskCounts carries two, and the missing
// one is missing on purpose rather than by oversight. `claimableAfter` reports
// how many moved tasks became eligible for `workbook next`, and eligibility is
// decided by a status's tags and a task's dependencies — nothing about a
// priority gates it. Reusing the status envelope would therefore ship
// `"claimableAfter": 0` on every priority change this project will ever record,
// which invites a client to branch on it and tells a reader that priorities
// take part in something they do not.
type VocabularyPriorityTaskCounts struct {
	// Affected counts the active tasks that resolved through the removed
	// priority. It is stated rather than omitted, so a client reading
	// `tasks.affected` gets an answer from every priority change.
	Affected int `json:"affected"`
}

// VocabularyPriorityMutation is what one priority change produced: the
// configuration as it now stands, the tip it was written to, and what it cost.
type VocabularyPriorityMutation struct {
	State    VocabularyState
	Tasks    VocabularyPriorityTaskCounts
	Warnings []core.Warning
}

// The six capabilities behind the priority mutation routes. Each answers with
// the whole configuration rather than with what it changed, for the reason the
// status mutations do: a priority change can move a priority the client did not
// name — the default role transfers, a removal retires a token — and a client
// that patched its own model from a description of one change would disagree
// with the server about the rest.
//
// There are six where the statuses have four because the priority verbs are
// six: a rename and a relabel are one edit, but a move, a role and a color are
// each their own change with their own refusal, and folding them into the edit
// would be this package inventing a change the verb family has no reading of.
type (
	VocabularyPriorityAdder     func(context.Context, VocabularyPriorityAddition) (VocabularyPriorityMutation, error)
	VocabularyPriorityEditor    func(context.Context, core.Priority, VocabularyPriorityEdit) (VocabularyPriorityMutation, error)
	VocabularyPriorityRemover   func(context.Context, core.Priority, VocabularyPriorityRemoval) (VocabularyPriorityMutation, error)
	VocabularyPriorityMover     func(context.Context, core.Priority, VocabularyPriorityMove) (VocabularyPriorityMutation, error)
	VocabularyPriorityDefaulter func(context.Context, core.Priority, VocabularyPriorityDefault) (VocabularyPriorityMutation, error)
	VocabularyPriorityRecolorer func(context.Context, core.Priority, VocabularyPriorityRecolor) (VocabularyPriorityMutation, error)
)

// VocabularyPriorityMutationDocument is what every priority mutation answers
// with.
//
// It carries the whole vocabulary document, in the shape GET /api/vocabulary
// serves it — both halves of one configuration, because they are recorded in
// one commit and the client adopts what it is handed — so the client renders
// the result of a change through the same code that rendered the page,
// including the new head, which is what its next change has to name.
//
// Its format names the priority half rather than reusing the status mutation's,
// because the two documents differ where it counts: this one prices a change in
// the one number a priority change has. A client that read this as a status
// mutation would be reading a `tasks` member that is not the one it expects.
type VocabularyPriorityMutationDocument struct {
	Format     string                       `json:"format"`
	Version    int                          `json:"version"`
	Vocabulary VocabularyDocument           `json:"vocabulary"`
	Tasks      VocabularyPriorityTaskCounts `json:"tasks"`
	Warnings   []core.Warning               `json:"warnings,omitempty"`
}

// VocabularyErrorDocument is the error envelope with the statuses a refused
// change should be recomposed against.
//
// The envelope is byte-for-byte the ordinary one — same format, same version,
// same error body — so a client that only knows how to read errors reads this
// one. The vocabulary rides along for the refusal that needs it: a stale write
// means the client is looking at columns somebody else has already changed, and
// answering with the current ones saves it the refetch it would otherwise have
// to make before it could tell the reader anything.
type VocabularyErrorDocument struct {
	Format     string              `json:"format"`
	Version    int                 `json:"version"`
	Error      ErrorBody           `json:"error"`
	Vocabulary *VocabularyDocument `json:"vocabulary,omitempty"`
}

type TaskMutationDocument struct {
	Format   string         `json:"format"`
	Version  int            `json:"version"`
	Task     core.Task      `json:"task"`
	Warnings []core.Warning `json:"warnings,omitempty"`
	// Assignments is the task's assignment section as the page draws it, carried
	// only by the two routes that move one. Every other mutation leaves it out,
	// so the answer they send is the one they have always sent.
	//
	// It is here rather than derived by the client for the reason the poll's
	// presentation is: a row's staleness hint is presentation.AssignedAgo's
	// wording and its withdrawal is core's removal rule, and neither is a member
	// of core.Task. A panel that composed them from the task it was handed would
	// be the second copy of two rules the server owns.
	//
	// A pointer, because the member has three states and a slice has two. Absent
	// is "this answer says nothing about assignments", which is every other
	// mutation on this board; `[]` is "nobody holds this task", which is what a
	// withdrawal of the last one produces. Collapsing those two would send the
	// page that emptied a task's assignments an answer indistinguishable from a
	// title save, and it would redraw the row it had just removed.
	Assignments *[]AssignmentPresentation `json:"assignments,omitempty"`
}

type TaskPresentation struct {
	TaskID                string `json:"taskId"`
	IDPrefix              string `json:"idPrefix"`
	DependenciesComplete  int    `json:"dependenciesComplete"`
	DependenciesTotal     int    `json:"dependenciesTotal"`
	WaitingOnDependencies bool   `json:"waitingOnDependencies"`
	// AssignmentChips is the card's chip row and MoreAssignments is what the row
	// left out; Assignments is the whole list, for the task page's section. All
	// three are absent for a task nobody holds, which is what keeps an unheld
	// card drawing exactly the nodes it drew before assignments existed.
	//
	// They are derived here rather than in the client for the reason IDPrefix is:
	// the short form of an assignment, the number a capped row hides, and the
	// words a staleness hint is phrased in are all rules, and a second copy of a
	// rule in JavaScript is a copy that goes on saying the old thing the day the
	// rule changes. presentation.AssignmentChip and presentation.AssignedAgo are
	// the same functions `workbook board` and `workbook show` print through, so
	// the three surfaces cannot drift.
	AssignmentChips []string                 `json:"assignmentChips,omitempty"`
	MoreAssignments int                      `json:"moreAssignments,omitempty"`
	Assignments     []AssignmentPresentation `json:"assignments,omitempty"`
}

// AssignmentPresentation is one assignment as the task page draws it: who holds
// the task, which of their agents holds it, when that was recorded, and how long
// ago that was.
//
// Ago is a server-derived string rather than a client computation over CreatedAt
// because it is `workbook show`'s own wording, from presentation.AssignedAgo. It
// is recomputed on every poll, so an open page's "assigned 59 minutes ago"
// becomes "assigned 1 hour ago" a minute later without a reload. CreatedAt rides
// along beside it because the exact time is what a reader settling a stale
// assignment between themselves actually needs, and a phrase in whole days
// cannot carry it.
// Removable is whether the identity this board writes as may withdraw this
// assignment. It is core.Assignment.RemovableBy, asked on the server, because
// the rule is decided from the assignment's own principal and creator and the
// page must not draw a control the service would refuse. A board that cannot
// assign at all carries the member on nothing, so its document is the one it
// has always published.
// Value is the whole assignment as one token — principal[/label] — which is what
// `--unassign` takes and what the withdrawal route's `from` member carries. It
// rides along beside the two parts rather than being re-composed on the client
// for the reason assignTaskRequest gives for not splitting it: the separator is
// core's, and a second place that decided where the first slash falls would
// address a different assignment than the row the reader pointed at. It is
// carried on the same boards Removable is, and for the same reason — it exists
// for the control, so a board that draws none publishes the document it always
// did.
type AssignmentPresentation struct {
	Principal string    `json:"principal"`
	Label     string    `json:"label,omitempty"`
	Value     string    `json:"value,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Ago       string    `json:"ago"`
	Removable bool      `json:"removable,omitempty"`
}

// AssignmentRow is the chip row one card draws: the chips themselves and how
// many assignments they left out.
//
// The cap exists because a task may hold core.MaxAssignmentCount assignments and
// a card is one box in one column. Chips wrap, so an uncapped row on a
// fifty-times-assigned task would grow that card by fifty lines and push every
// card under it off the screen — while saying nothing a reader could act on. The
// row is lossy in the same way the chip itself is, and for the same reason: it
// is never the only place an assignment is shown, and the task page below it
// lists every one of them with its timestamp.
type AssignmentRow struct {
	Chips []string
	More  int
}

// maxCardAssignees bounds a card's chip row. Three, because a card that names
// three holders has already told the reader what they needed to know — this task
// is worked by several people — and the fourth line costs more than it says.
const maxCardAssignees = 3

// assignmentRow derives the capped chip row a card draws.
//
// It is one function reached from two places: the page template calls it through
// the `cardAssignees` template function for the cards the server renders, and
// taskPresentation calls it for the cards the client renders from a poll. A
// second implementation on either side would be a card whose chips changed when
// the first poll landed.
func assignmentRow(assignments []core.Assignment) AssignmentRow {
	chips := presentation.AssignmentChips(assignments)
	if len(chips) <= maxCardAssignees {
		return AssignmentRow{Chips: chips}
	}
	return AssignmentRow{Chips: chips[:maxCardAssignees], More: len(chips) - maxCardAssignees}
}

// assignmentPresentation renders a task's assignments for the task page, in the
// stored order — by principal, then label — so the section and the chip row
// above it agree about which comes first.
//
// The actor is the identity this board writes as, empty for a board that writes
// none. It decides the two members the withdrawal control needs — the token the
// route takes and whether the row may be withdrawn from here — and an empty one
// leaves both off every row, which is what keeps a read-only board's document
// byte-for-byte what it was.
func assignmentPresentation(assignments []core.Assignment, now time.Time, actor string) []AssignmentPresentation {
	if len(assignments) == 0 {
		return nil
	}
	rendered := make([]AssignmentPresentation, 0, len(assignments))
	for _, assignment := range assignments {
		value := ""
		if actor != "" {
			value = assignment.Value()
		}
		rendered = append(rendered, AssignmentPresentation{
			Principal: assignment.Principal,
			Label:     assignment.Label,
			Value:     value,
			CreatedAt: assignment.CreatedAt,
			Ago:       presentation.AssignedAgo(assignment, now),
			Removable: actor != "" && assignment.RemovableBy(actor),
		})
	}
	return rendered
}

// LifecycleStage is one stop on a task's status lane. WallTime, Commit, and
// Actor are absent for a stop no recorded change entered, which is how a task
// that never changed status and a history a read could not walk in full both
// render honestly.
type LifecycleStage struct {
	Status   core.Status `json:"status"`
	Label    string      `json:"label"`
	Commit   string      `json:"commit,omitempty"`
	Actor    string      `json:"actor,omitempty"`
	WallTime *time.Time  `json:"wallTime,omitempty"`
	Current  bool        `json:"current"`
}

// TaskHistoryDocument carries one task's change log and the status lane derived
// from it. The lane is derived on the server from the whole chain, so a client
// that renders only part of the log still shows every status the task stood in.
type TaskHistoryDocument struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	TaskID    string           `json:"taskId"`
	Lifecycle []LifecycleStage `json:"lifecycle"`
	History   core.ChangeLog   `json:"history"`
}

type HealthDocument struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type ErrorBody struct {
	Category core.Category `json:"category"`
	Message  string        `json:"message"`
}

type ErrorDocument struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

// Options names every capability a board can be built with. A nil field is a
// capability this board does not have, and its route says so rather than
// pretending: that is what lets a read-only board and the full one come from
// the same constructor.
//
// The list is named rather than positional because Depend and Free share a
// signature. Passed positionally, a transposed pair compiles and silently
// inverts the semantics; named, the same mistake is visible in the call site
// itself. Delete and Restore used to be such a pair and no longer are: each now
// carries its own input type, so the compiler refuses the swap outright.
type Options struct {
	// Vocabulary reads the project's statuses per request. A nil resolver means
	// this board was built without one and draws the built-in statuses, which
	// is what every construction that predates per-project columns did.
	Vocabulary VocabularyResolver
	// The four vocabulary mutations. A board given none of them renders its
	// columns and refuses to change them, which is every board that predates the
	// route that administers them.
	AddStatus     VocabularyStatusAdder
	EditStatus    VocabularyStatusEditor
	RemoveStatus  VocabularyStatusRemover
	ReorderStatus VocabularyReorderer
	// The six priority mutations, which administer the other section of the
	// same configuration ledger. A board given none of them draws its
	// priorities and refuses to change them, which is every board that predates
	// the routes that administer them.
	//
	// They are six fields rather than one because they are six capabilities,
	// and the surface that offers them decides for itself what a partial set
	// means: each route reports the one it was not given, the way every route
	// here reports a capability it does not have.
	AddPriority        VocabularyPriorityAdder
	EditPriority       VocabularyPriorityEditor
	RemovePriority     VocabularyPriorityRemover
	MovePriority       VocabularyPriorityMover
	SetDefaultPriority VocabularyPriorityDefaulter
	RecolorPriority    VocabularyPriorityRecolorer
	// SetDisplay records what this project calls its board and the colors it
	// draws it in. A board given none draws no board settings section on its
	// configuration page, the way a board given no vocabulary mutations draws
	// no statuses to administer.
	SetDisplay DisplaySettingsWriter
	// RepoName is the checkout this board is serving, as the header's eyebrow
	// names it — the base name of the worktree root, which is what distinguishes
	// two boards a reader has open at once before either of them is named.
	//
	// It is a value rather than a reader because it cannot change while the
	// server runs: `workbook serve` is bound to one worktree, and a checkout that
	// moved out from under it has taken every other answer with it. A board built
	// without one keeps the generic eyebrow every board carried before this.
	RepoName     string
	List         TaskLister
	Create       TaskCreator
	Update       TaskUpdater
	UpdateStatus TaskStatusUpdater
	Position     TaskPositionUpdater
	Delete       TaskDeleter
	Restore      TaskRestorer
	Depend       TaskDependencyAdder
	Free         TaskDependencyRemover
	// The five thread mutations and the two reads behind the attachment
	// download. They are capabilities of their own rather than members of
	// Update because the routes are their own: a board wired for one of them is
	// wired for the surface that offers it, and a board given none answers those
	// addresses the way every other unwired route answers.
	//
	// Attachment finds one attachment on one task and AttachmentContent reads a
	// file's bytes, in two steps rather than one, because the two answers are
	// decided in different places: what an attachment *is* decides this
	// package's response headers and its refusal for a link, and only a file's
	// bytes are core's to hand back.
	AddComment        TaskCommentAdder
	EditComment       TaskCommentEditor
	RemoveComment     TaskCommentRemover
	AddAttachment     TaskAttachmentAdder
	RemoveAttachment  TaskAttachmentRemover
	Attachment        TaskAttachmentFinder
	AttachmentContent AttachmentContentReader
	// The two assignment mutations, and the identity they are recorded against.
	//
	// The identity is a value rather than a reader for the reason RepoName is:
	// `workbook serve` is bound to one worktree, and the `user.email` an
	// assignment made from that worktree's command line would carry is the one
	// this board carries. It is what dissolves the objection the display half of
	// this feature was built under — that a browser is not a principal — because
	// nothing here asks the browser to be one: the checkout asserts its own
	// identity, exactly as it does for a commit.
	//
	// A board given the mutations without an identity, or an identity without
	// the mutations, draws no assignment control at all. See assignIdentity.
	Assign      TaskAssigner
	Unassign    TaskUnassigner
	Identity    string
	History     TaskHistoryReader
	SyncState   SyncStateReporter
	SetSyncMode SyncModeSetter
}

// handler embeds Options rather than copying it field by field, so there is no
// second list to keep in step and no assignment that could cross two
// capabilities on the way in.
type handler struct {
	Options
	page *template.Template
	mux  *http.ServeMux
}

// pageData is what the server hands the page, and it is also the client's only
// source for the same facts: the script reads the columns back out of the DOM
// rather than carrying a status list of its own, so these fields are the whole
// vocabulary contract with the browser.
type pageData struct {
	Board presentation.Board
	// ProjectName is what this project calls its board, or core's generic name
	// for a board nobody has named. It is the page's title, its heading, and the
	// name the client titles every other route with, all from one value: the
	// header is byte-compared across routes, so nothing about it may be decided
	// per-route, and a client that derived the fallback itself would hold a
	// second copy of a default core owns.
	ProjectName string
	// TitleSuffix is what a route that is not the board appends to its own name
	// — "New task · Atlas". It is the project's name where there is one and the
	// product's where there is not, which is why it is not ProjectName: "New
	// task · Workbook board" reads as a board called "New task".
	TitleSuffix string
	// DefaultProjectName is what a board with no name of its own is called. It
	// is rendered beside the resolved name because the two answer different
	// questions: the resolved name is what this board *is* called, and this is
	// what it would be called if the name were cleared — which is what the
	// settings form's own placeholder has to say, whatever the project is called
	// today. Both come from the server so the script holds no copy of a fallback
	// core owns.
	DefaultProjectName string
	// Eyebrow is the line above the heading: which checkout this is. It is
	// composed here rather than in the page so that a board built without a
	// repository name keeps the words it had rather than trailing a colon.
	Eyebrow string
	// Theme is the `:root` override a project's chosen colors ask for, empty for
	// a project that has chosen none. It is template.CSS because it is composed
	// in Go out of validated values — see boardTheme for why the page cannot
	// interpolate the values themselves.
	Theme template.CSS
	// PriorityInk is the stylesheet that draws each of this project's priorities
	// in its own color: one custom property per priority and the rule that reads
	// it. It is separate from Theme because it answers a different question — a
	// theme is what a project's chosen colors ask for and is empty when none were
	// chosen, while every board has priorities and the stylesheet can only name
	// three of them by hand. See priorityInk, which is also where every byte of
	// this template.CSS is answered for.
	PriorityInk template.CSS
	// DefaultStatus is where a new task lands, rendered as an attribute because
	// the client needs it before it has fetched anything and must not guess.
	DefaultStatus core.Status
	// VocabularyHead is the ledger tip these columns were built from, so the
	// poll can tell that the columns it is looking at have been superseded.
	VocabularyHead string
	// Priorities are this project's priorities in configured order, as JSON,
	// rendered into the page for the reason DefaultStatus and StatusTags are:
	// the client needs them before it has fetched anything and must not guess.
	// A board whose project renamed its priorities, added a fourth or colored
	// one of them draws what the project configured; nothing here is a set the
	// script keeps a copy of.
	//
	// It is already-encoded JSON rather than the values because the page has one
	// template derivation and the comment on pageFuncs says why; see
	// pagePriorities for what it carries.
	Priorities string
	// DefaultPriority is what a new task is created at when nothing named a
	// priority, rendered as its own attribute for the reason DefaultStatus is.
	//
	// The role is also among each priority's published tags, and the client is
	// deliberately not left to find it there: which tag means "this is where a
	// task with none lands" is the server's to know, exactly as the status tags
	// are, and a script spelling the name of a role would be keeping a copy of a
	// set core owns.
	DefaultPriority core.Priority
	// AttachmentFileLimit is core's ceiling on one attached file, rendered into
	// the page for the same reason StatusTags is: the upload control refuses a
	// file this large before it spends a minute encoding and sending one the
	// server would refuse, and a number the script carried itself would be a
	// second copy of a ceiling core owns — one that would go on naming the old
	// number the day core's changed.
	AttachmentFileLimit int64
	// AttachmentTotalLimit is core's ceiling on what one task's live file
	// attachments add up to, rendered into the page for exactly the reason the
	// per-file ceiling above is. It is the ceiling a create form has to ask
	// before the task exists: a reader staging six screenshots is over it long
	// before any of them is sent, and the alternative is a create that succeeds
	// followed by uploads that do not.
	AttachmentTotalLimit int64
	// AttachmentNameLimit is core's ceiling on a file attachment's name, in
	// bytes, rendered into the page for the reason the two above are — and it is
	// bytes rather than characters, which is the whole reason the client cannot
	// carry its own copy of the rule. An 85-character Japanese file name is over
	// a 255-byte ceiling and under every count a reader would make of it.
	AttachmentNameLimit int
	// AttachmentLabelLimit and AttachmentURLLimit are core's ceilings on a link
	// attachment's display text and its destination, in bytes, rendered into the
	// page for the reason the name ceiling above is. A link staged on a create
	// form is refused by the server only after the task has been made, so these
	// are the only place the reader can hear about them in time.
	AttachmentLabelLimit int
	AttachmentURLLimit   int
	// InlineImageMediaTypes are the media types the attachment route serves
	// inline, space separated, rendered into the page for the reason the ceiling
	// above is: the markdown renderer draws an attachment reference as an <img>
	// only for a type that comes back as pixels, and the set of those types is
	// the download route's to decide.
	InlineImageMediaTypes string
	// AssignIdentity is the identity this board would record an assignment
	// against, and empty for a board that can record none. It is one value doing
	// two jobs, which is why it is not a boolean beside a name: the section
	// draws its controls only where there is an identity, and the field's
	// placeholder has to say which one, so a page that carried the flag and not
	// the name could offer to assign somebody it could not name. See
	// handler.assignIdentity for what makes it empty.
	AssignIdentity string
	// StatusTags are the three roles a status may carry, rendered into the
	// configuration page's forms for the reason the columns are rendered into
	// the board: the client must not carry a second copy of a set the server
	// owns.
	// It is also what keeps the script from naming `done`, which is a tag here
	// and a status name in most projects.
	StatusTags []core.StatusTag
	// PriorityTags are the roles a priority may carry — one of them today —
	// rendered into the priorities section for the reason StatusTags is
	// rendered into the statuses one: the set belongs to the vocabulary, and a
	// script holding its own copy is a script that can disagree with it. The two
	// sets are separate and happen to share a word, which is the other half of
	// the reason this is published rather than spelled in the client.
	PriorityTags []core.PriorityTag
	// Administrable is whether this board was built with all four vocabulary
	// mutations. It decides whether the page carries the configuration route's
	// link and its statuses section at all, and serveConfig answers the address
	// itself with a 404 when it is false — one gate, read on both sides.
	//
	// `workbook serve` is the only production caller of NewHandler and always
	// supplies the four, so this is true wherever a person meets it. It is
	// computed rather than assumed for the two callers that are not that one:
	// the tests, which build boards without the capabilities and are what holds
	// the gate honest, and any future embedding that wires fewer of them, which
	// gets a board that draws its columns and offers no control that would only
	// ever answer "this board has no such capability".
	//
	// All four rather than any, because the page is one surface: a partial set
	// would draw controls that look alike and fail differently.
	Administrable bool
	// DisplayAdministrable is whether this board was built with the display
	// writer, and it decides whether the configuration page carries the board
	// settings section at all.
	//
	// It is asked separately from Administrable rather than folded into it
	// because the two capabilities are separate: a board wired for the four
	// vocabulary mutations but not for this one would otherwise draw a Save that
	// could only ever be refused. It still requires Administrable as well,
	// because the route belongs to the statuses — a board that cannot administer
	// those answers /config with a 404, and a section served onto a page nobody
	// can reach is a section that is never seen and never taken away.
	// `workbook serve` supplies both, so a person meets them together.
	DisplayAdministrable bool
	// PrioritiesAdministrable is whether this board was built with the priority
	// mutations the configuration page's priorities section drives, and it
	// decides whether that section is served at all.
	//
	// It is asked separately from Administrable for the reason
	// DisplayAdministrable is: the capabilities are separate, and a board that
	// could draw a list of priorities but change none of them would draw four
	// controls per row that could only ever answer "this board has no such
	// capability". It still requires Administrable, because /config answers 404
	// without the status mutations and a section served onto a page nobody can
	// reach is a section that is never seen.
	//
	// All six, the recolor included: the row's edit form carries a color field,
	// so a board wired for the other five would draw a control that could only
	// ever answer "this board has no such capability". That term was added when
	// the field landed; before it, counting the recolor would have withheld a
	// working section over a capability nothing on it used.
	PrioritiesAdministrable bool
}

// expectedHead is the task tip the browser rendered before proposing a change.
// It is optional on every request that carries it: a client that omits it keeps
// the behavior these routes had before the field existed, which is what lets
// the server half land before any client sends one.
type updateStatusRequest struct {
	Status       core.Status `json:"status"`
	ExpectedHead string      `json:"expectedHead"`
}

type positionTaskRequest struct {
	Status       core.Status `json:"status"`
	Before       string      `json:"before"`
	After        string      `json:"after"`
	ExpectedHead string      `json:"expectedHead"`
}

// restoreTaskRequest and deleteTaskRequest are the two bodies a client may omit
// entirely. Every member is optional, and a request with no body at all is the
// bare verb — which is what every client that predates these members sends, and
// what keeps the routes answering it unchanged.
//
// restoreTaskRequest names its destination `status` rather than `into` because
// it is the same drag the position route already describes that way, and a
// board that moves a card should not have to describe the move twice.
type restoreTaskRequest struct {
	Status       core.Status `json:"status"`
	Before       string      `json:"before"`
	After        string      `json:"after"`
	ExpectedHead string      `json:"expectedHead"`
}

// deleteTaskRequest is converted directly to core.DeleteInput, so its fields
// must stay identical in name, type, and order.
type deleteTaskRequest struct {
	ExpectedHead string `json:"expectedHead"`
}

type createTaskRequest struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      core.Status   `json:"status"`
	Priority    core.Priority `json:"priority"`
	Labels      []string      `json:"labels"`
}

// The four vocabulary mutation bodies. expectedHead is a member of each rather
// than a header or a query parameter because it is part of the change: the
// board is proposing this edit to these columns, and the two travel together.
type addStatusRequest struct {
	Status       core.Status `json:"status"`
	Label        string      `json:"label"`
	Tags         []string    `json:"tags"`
	Before       core.Status `json:"before"`
	After        core.Status `json:"after"`
	ExpectedHead *string     `json:"expectedHead"`
}

// editStatusRequest takes pointers so that an omitted member and an emptied one
// are different requests: no `label` leaves the label alone, and `"label": ""`
// is a blank label the vocabulary refuses.
type editStatusRequest struct {
	Name         *core.Status `json:"name"`
	Label        *string      `json:"label"`
	Tags         *[]string    `json:"tags"`
	ExpectedHead *string      `json:"expectedHead"`
}

type removeStatusRequest struct {
	Into         core.Status `json:"into"`
	ExpectedHead *string     `json:"expectedHead"`
}

type reorderStatusesRequest struct {
	Statuses     []core.Status `json:"statuses"`
	ExpectedHead *string       `json:"expectedHead"`
}

// The six priority mutation bodies. expectedHead is a member of each for the
// reason it is a member of the status bodies: it is part of the change, and the
// two travel together.
type addPriorityRequest struct {
	Priority     core.Priority `json:"priority"`
	Label        string        `json:"label"`
	Before       core.Priority `json:"before"`
	After        core.Priority `json:"after"`
	ExpectedHead *string       `json:"expectedHead"`
}

// editPriorityRequest takes pointers for the reason editStatusRequest does: no
// `label` leaves the label alone, and `"label": ""` is a blank label the
// vocabulary refuses.
type editPriorityRequest struct {
	Name         *core.Priority `json:"name"`
	Label        *string        `json:"label"`
	ExpectedHead *string        `json:"expectedHead"`
}

type removePriorityRequest struct {
	Into         core.Priority `json:"into"`
	ExpectedHead *string       `json:"expectedHead"`
}

type movePriorityRequest struct {
	Before       core.Priority `json:"before"`
	After        core.Priority `json:"after"`
	ExpectedHead *string       `json:"expectedHead"`
}

// defaultPriorityRequest carries a head and nothing else: the priority is the
// address, and the role is the route.
type defaultPriorityRequest struct {
	ExpectedHead *string `json:"expectedHead"`
}

// recolorPriorityRequest takes a pointer so that clearing a priority's ink is
// something a client says rather than something it omits. `"color": ""` returns
// the priority to the color the board derives for it; no `color` member at all
// is a client that named no intention, and the route refuses it rather than
// clearing on its behalf.
type recolorPriorityRequest struct {
	Color        *string `json:"color"`
	ExpectedHead *string `json:"expectedHead"`
}

// updateTaskRequest is the shape this endpoint accepts, which is deliberately
// narrower than core.UpdateInput and no longer tied to it.
//
// It used to be converted to that type directly, which required the two structs
// to stay identical in name, type, and order — a coupling that made the API
// surface change whenever the service input did. The service input now also
// carries the comment and attachment intents an update may ride with, which
// this endpoint does not accept; see input below.
type updateTaskRequest struct {
	Title        *string        `json:"title"`
	Description  *string        `json:"description"`
	Status       *core.Status   `json:"status"`
	Priority     *core.Priority `json:"priority"`
	Labels       *[]string      `json:"labels"`
	ExpectedHead string         `json:"expectedHead"`
}

// input maps the request onto the service input field by field.
//
// It used to be a struct conversion, which was shorter and quietly wrong the
// moment the two shapes stopped matching: core.UpdateInput now also carries the
// comment and attachment intents an update may ride with, which this endpoint
// does not accept and must not accept by accident. Naming the fields is what
// keeps a new member of either struct from silently becoming part of this API.
func (body updateTaskRequest) input() core.UpdateInput {
	return core.UpdateInput{
		Title:        body.Title,
		Description:  body.Description,
		Status:       body.Status,
		Priority:     body.Priority,
		Labels:       body.Labels,
		ExpectedHead: body.ExpectedHead,
	}
}

// The page template used to ask a `knownStatus` helper whether this build had a
// column for a task's status, and the helper answered from a fixed list. The
// answer is per-project and therefore per-request now, so it is not a template
// function at all: presentation.TaskView carries StatusUnresolved, computed
// against the vocabulary this request resolved, and the template reads the
// view. A card and its column cannot disagree, because one value produced both.
//
// The client script answers the same question from the columns this template
// rendered — it reads the emitted [data-status] nodes rather than a status list
// of its own — so the two cannot disagree about a card even while the page is
// being served by a build the script does not match. Neither side reads it off
// the containing list, so a card that changes status carries the right answer
// with it as it moves.

// vocabularyKey addresses the statuses a request has already resolved.
type vocabularyKey struct{}

// VocabularyFrom reports the statuses a request resolved, for a capability that
// needs the same answer the route is rendering under.
//
// It exists so a request resolves the vocabulary once. A lister that read the
// project's statuses for itself would pay a second ledger read on every poll —
// measurably, at 1 Hz — and, worse, could read a different answer: a status
// change landing between the two reads produces a response whose tasks resolve
// under the new vocabulary while its vocabularyHead names the old one, which is
// exactly the pair a client uses to decide that nothing has changed. One read
// per request makes that window unrepresentable rather than merely narrow.
//
// The second return is false for a context that never passed through a route
// that resolves — every mutation route, and any caller outside a request — and
// such a caller must read the vocabulary itself.
func VocabularyFrom(ctx context.Context) (VocabularyState, bool) {
	state, carried := ctx.Value(vocabularyKey{}).(VocabularyState)
	return state, carried
}

// vocabulary reads the statuses this request renders under, and returns a
// request carrying them so that nothing this route calls afterwards resolves
// them a second time.
//
// A board built without a resolver reports the pre-ledger statuses and no
// ledger head, which is exactly what every construction that predates
// per-project columns saw. A resolver that fails is reported rather than
// papered over: drawing the built-in six for a project that renamed half of
// them would put every task in the wrong column and accept drops the server
// would refuse.
//
// That fallback deliberately names no priorities, and it is the one producer of
// a VocabularyState that means to leave the field zero: a board with no
// resolver has no project to read them from, and the zero vocabulary is read
// everywhere as the built-in three — which is precisely what a board built
// without a resolver is using. It is left zero rather than filled with them so
// that the substitution stays in the one place core makes it.
func (handler *handler) vocabulary(request *http.Request) (VocabularyState, *http.Request, error) {
	if state, carried := VocabularyFrom(request.Context()); carried {
		return state, request, nil
	}
	state := VocabularyState{Vocabulary: core.LegacyVocabulary()}
	if handler.Vocabulary != nil {
		resolved, err := handler.Vocabulary(request.Context())
		if err != nil {
			return VocabularyState{}, request, err
		}
		state = resolved
		if state.Vocabulary.IsZero() {
			state.Vocabulary = core.LegacyVocabulary()
		}
	}
	return state, request.WithContext(context.WithValue(request.Context(), vocabularyKey{}, state)), nil
}

// NewHandler builds the board from the capabilities it is given. It is the only
// constructor: the tiered NewHandlerWithTaskMutations and
// NewHandlerWithSyncControl existed to append capabilities to a positional list
// without renaming every earlier call, and a named field expresses the same
// tier by being set or left nil.
func NewHandler(options Options) http.Handler {
	page := template.Must(template.New("index.html").Funcs(pageFuncs).ParseFS(assets, "assets/index.html"))
	handler := &handler{Options: options, page: page, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /{$}", handler.serveBoard)
	handler.mux.HandleFunc("GET /config", handler.serveConfig)
	handler.mux.HandleFunc("GET /tasks/new", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/{id}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("GET /api/vocabulary", handler.serveVocabulary)
	handler.mux.HandleFunc("POST /api/vocabulary/statuses", handler.addVocabularyStatus)
	handler.mux.HandleFunc("PATCH /api/vocabulary/statuses/{status}", handler.editVocabularyStatus)
	handler.mux.HandleFunc("DELETE /api/vocabulary/statuses/{status}", handler.removeVocabularyStatus)
	handler.mux.HandleFunc("PUT /api/vocabulary/order", handler.reorderVocabulary)
	// The priority half of the same ledger. A move, a role and a color each get
	// an address of their own under the priority they change, because each is
	// its own change with its own head and its own refusal — the verb family
	// has no planner that takes two of them at once, and a route that accepted
	// two would be this package inventing one.
	handler.mux.HandleFunc("POST /api/vocabulary/priorities", handler.addVocabularyPriority)
	handler.mux.HandleFunc("PATCH /api/vocabulary/priorities/{priority}", handler.editVocabularyPriority)
	handler.mux.HandleFunc("DELETE /api/vocabulary/priorities/{priority}", handler.removeVocabularyPriority)
	handler.mux.HandleFunc("PATCH /api/vocabulary/priorities/{priority}/position", handler.moveVocabularyPriority)
	handler.mux.HandleFunc("PATCH /api/vocabulary/priorities/{priority}/default", handler.setDefaultVocabularyPriority)
	handler.mux.HandleFunc("PATCH /api/vocabulary/priorities/{priority}/color", handler.recolorVocabularyPriority)
	handler.mux.HandleFunc("PATCH /api/display", handler.updateDisplay)
	handler.mux.HandleFunc("GET /api/tasks/{id}/history", handler.serveTaskHistory)
	handler.mux.HandleFunc("POST /api/tasks", handler.createTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}", handler.updateTask)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/status", handler.updateTaskStatus)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/position", handler.positionTask)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}", handler.deleteTask)
	handler.mux.HandleFunc("POST /api/tasks/{id}/restore", handler.restoreTask)
	handler.mux.HandleFunc("PUT /api/tasks/{id}/dependencies/{dependency}", handler.addTaskDependency)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/dependencies/{dependency}", handler.removeTaskDependency)
	handler.mux.HandleFunc("POST /api/tasks/{id}/comments", handler.addTaskComment)
	handler.mux.HandleFunc("PATCH /api/tasks/{id}/comments/{comment}", handler.editTaskComment)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/comments/{comment}", handler.removeTaskComment)
	handler.mux.HandleFunc("POST /api/tasks/{id}/assignments", handler.addTaskAssignment)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/assignments", handler.removeTaskAssignment)
	handler.mux.HandleFunc("POST /api/tasks/{id}/attachments", handler.addTaskAttachment)
	handler.mux.HandleFunc("GET /api/tasks/{id}/attachments/{attachment}", handler.serveTaskAttachment)
	handler.mux.HandleFunc("DELETE /api/tasks/{id}/attachments/{attachment}", handler.removeTaskAttachment)
	handler.mux.HandleFunc("GET /api/sync", handler.serveSyncState)
	handler.mux.HandleFunc("PUT /api/sync", handler.updateSyncMode)
	handler.mux.HandleFunc("GET /healthz", handler.serveHealth)
	return http.HandlerFunc(handler.serveHTTP)
}

// writeSecurityHeaders states the page's own restrictions on every response,
// including the ones the same-origin guard refuses before this handler runs.
func writeSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func (handler *handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writeSecurityHeaders(writer)
	// Bounded here rather than at each route so no handler, present or future,
	// can read an unbounded body by forgetting to ask for a limit. A route that
	// never reads its body is covered too, and http.MaxBytesReader is given the
	// ResponseWriter so a sender that ignores the limit loses its connection
	// rather than keeping it to try again.
	request.Body = http.MaxBytesReader(writer, request.Body, requestBodyLimit(request.URL.Path))
	if malformedTaskDependencyRequestPath(request.URL.Path, request.URL.EscapedPath()) {
		http.NotFound(writer, request)
		return
	}
	if method, known := allowedMethod(request.URL.Path); known && !methodAllowed(request.Method, method) {
		writer.Header().Set("Allow", method)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	handler.mux.ServeHTTP(writer, request)
}

func methodAllowed(requestMethod, allowed string) bool {
	for _, method := range strings.Split(allowed, ", ") {
		if requestMethod == method {
			return true
		}
	}
	return false
}

func malformedTaskDependencyRequestPath(decodedPath, escapedPath string) bool {
	if escapedPath != decodedPath &&
		(taskDependencyRouteShaped(decodedPath) || malformedTaskDependencyPath(decodedPath)) {
		return true
	}
	return malformedTaskDependencyPath(escapedPath)
}

func taskDependencyRouteShaped(requestPath string) bool {
	_, _, valid := taskDependencyPathIDs(requestPath)
	return valid
}

func malformedTaskDependencyPath(requestPath string) bool {
	if _, _, valid := taskDependencyPathIDs(requestPath); valid {
		return false
	}
	cleanedPath := pathpkg.Clean(requestPath)
	if _, _, cleanedDependency := taskDependencyPathIDs(cleanedPath); cleanedDependency {
		return true
	}
	if !taskAPIPath(requestPath) && !taskAPIPath(cleanedPath) {
		return false
	}
	return hasPathSegment(requestPath, "dependencies") ||
		hasPathSegment(cleanedPath, "dependencies")
}

func taskAPIPath(path string) bool {
	return path == "/api/tasks" || strings.HasPrefix(path, "/api/tasks/")
}

func hasPathSegment(path, marker string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == marker {
			return true
		}
	}
	return false
}

func allowedMethod(path string) (string, bool) {
	switch path {
	case "/", "/healthz", "/config", "/tasks/new":
		return http.MethodGet, true
	case "/api/tasks":
		return http.MethodGet + ", " + http.MethodPost, true
	case "/api/vocabulary":
		return http.MethodGet, true
	case "/api/vocabulary/statuses":
		return http.MethodPost, true
	case "/api/vocabulary/priorities":
		return http.MethodPost, true
	case "/api/vocabulary/order":
		return http.MethodPut, true
	case "/api/display":
		return http.MethodPatch, true
	case "/api/sync":
		return http.MethodGet + ", " + http.MethodPut, true
	default:
		if vocabularyStatusPathName(path) != "" {
			return http.MethodPatch + ", " + http.MethodDelete, true
		}
		if vocabularyPriorityPathName(path) != "" {
			return http.MethodPatch + ", " + http.MethodDelete, true
		}
		// The three per-member addresses answer PATCH alone. A member nobody
		// defined is deliberately not known here, so it reaches the mux and is
		// answered 404: a method refusal naming what it allows would be this
		// table claiming a route exists.
		if _, _, ok := vocabularyPriorityMemberPath(path); ok {
			return http.MethodPatch, true
		}
		if _, _, ok := taskDependencyPathIDs(path); ok {
			return http.MethodPut + ", " + http.MethodDelete, true
		}
		if _, _, ok := taskCommentPathIDs(path); ok {
			return http.MethodPatch + ", " + http.MethodDelete, true
		}
		if _, _, ok := taskAttachmentPathIDs(path); ok {
			return http.MethodGet + ", " + http.MethodDelete, true
		}
		// Both verbs at the collection's own address, because an assignment's
		// name carries the separator a path segment cannot. See
		// taskAssignmentsPathID.
		if taskAssignmentsPathID(path) != "" {
			return http.MethodPost + ", " + http.MethodDelete, true
		}
		if taskCommentsPathID(path) != "" || taskAttachmentsPathID(path) != "" {
			return http.MethodPost, true
		}
		if taskPositionPathID(path) != "" {
			return http.MethodPatch, true
		}
		if taskStatusPathID(path) != "" {
			return http.MethodPatch, true
		}
		if taskRestorePathID(path) != "" {
			return http.MethodPost, true
		}
		if taskHistoryPathID(path) != "" {
			return http.MethodGet, true
		}
		if taskPathID(path) != "" {
			return http.MethodPatch + ", " + http.MethodDelete, true
		}
		if taskPagePathID(path) != "" {
			return http.MethodGet, true
		}
		return "", false
	}
}

func taskDependencyPathIDs(path string) (string, string, bool) {
	return taskMemberPathIDs(path, "dependencies")
}

func taskCommentPathIDs(path string) (string, string, bool) {
	return taskMemberPathIDs(path, "comments")
}

func taskAttachmentPathIDs(path string) (string, string, bool) {
	return taskMemberPathIDs(path, "attachments")
}

// taskMemberPathIDs reads the task and the member one of the per-member routes
// addresses — a dependency, a comment, an attachment — and reports whether the
// path is that route's shape at all.
//
// The three collections share it because they share the shape and the rules: a
// path segment that is empty or is a relative segment names nothing, and a path
// with any other number of segments is a different route.
func taskMemberPathIDs(path, collection string) (string, string, bool) {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 3 || parts[0] == "" ||
		parts[0] == "." || parts[0] == ".." ||
		parts[1] != collection || parts[2] == "" ||
		parts[2] == "." || parts[2] == ".." {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// taskCommentsPathID and taskAttachmentsPathID read the task a collection route
// addresses, the way taskHistoryPathID reads the task its route addresses.
func taskCommentsPathID(path string) string {
	return taskCollectionPathID(path, "/comments")
}

func taskAttachmentsPathID(path string) string {
	return taskCollectionPathID(path, "/attachments")
}

func taskCollectionPathID(path, suffix string) string {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

// vocabularyStatusPathName reads the status one of the per-status routes
// addresses: it is what answers the method question for a path the mux has not
// matched yet, and what a request that arrived without the mux's pattern
// variables falls back to.
//
// It asks only whether the path addresses one status, and nothing about whether
// that status is a status. It cannot usefully: the mux hands the routes their
// own decoded pattern value, so a check made only here would be a check most
// requests never pass through. What every status name goes through instead is
// core.ValidateStatusToken, at the planner, on both surfaces — which is where a
// name that is not a token gets an answer that says so.
func vocabularyStatusPathName(path string) string {
	const prefix = "/api/vocabulary/statuses/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	status := strings.TrimPrefix(path, prefix)
	if status == "" || strings.Contains(status, "/") {
		return ""
	}
	return status
}

// vocabularyPriorityPathName reads the priority the per-priority routes
// address, and vocabularyPriorityMemberPath reads the priority and the member
// one of the three per-member routes addresses. They are the priority half of
// vocabularyStatusPathName and exist for what that comment says: the method
// table has to answer for a path the mux has not matched yet, and a request
// built without the mux's pattern variables falls back to them.
//
// Neither asks whether the priority is a priority. What every priority name
// goes through instead is core.ValidatePriorityToken, at the planner, on both
// surfaces — which is where a name that is not a token gets an answer that says
// so, in the words the command line would use.
func vocabularyPriorityPathName(path string) string {
	priority, member := vocabularyPrioritySegments(path)
	if member != "" {
		return ""
	}
	return priority
}

// vocabularyPriorityMemberPath reports a member address only for the three
// members these routes actually define. An undefined member is reported as no
// member at all, so it is answered where an address nobody defined should be
// answered: by the mux, with a 404.
func vocabularyPriorityMemberPath(path string) (string, string, bool) {
	priority, member := vocabularyPrioritySegments(path)
	if priority == "" || member == "" {
		return "", "", false
	}
	switch member {
	case "position", "default", "color":
		return priority, member, true
	default:
		return "", "", false
	}
}

// vocabularyPrioritySegments splits a priority route's path into the priority
// it addresses and the member beneath it, either of which may be empty for a
// path that is not one of these routes.
func vocabularyPrioritySegments(path string) (string, string) {
	const prefix = "/api/vocabulary/priorities/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(path, prefix)
	priority, member, split := strings.Cut(rest, "/")
	if priority == "" {
		return "", ""
	}
	if !split {
		return priority, ""
	}
	if member == "" || strings.Contains(member, "/") {
		return "", ""
	}
	return priority, member
}

func taskPagePathID(path string) string {
	const prefix = "/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || id == "new" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskPathID(path string) string {
	const prefix = "/api/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskStatusPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/status"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskPositionPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/position"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskRestorePathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/restore"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func taskHistoryPathID(path string) string {
	const prefix = "/api/tasks/"
	const suffix = "/history"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func (handler *handler) serveBoard(writer http.ResponseWriter, request *http.Request) {
	vocabulary, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	tasks, err := handler.listTasks(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.page.Execute(writer, pageData{
		Board:                 presentation.NewBoard(activeTasks(tasks), vocabulary.Vocabulary),
		ProjectName:           projectName(vocabulary.Display),
		TitleSuffix:           boardTitleSuffix(vocabulary.Display),
		DefaultProjectName:    core.DefaultProjectName,
		Eyebrow:               boardEyebrow(handler.RepoName),
		Theme:                 boardTheme(vocabulary.Display),
		PriorityInk:           priorityInk(vocabulary.Priorities),
		DefaultStatus:         vocabulary.Vocabulary.Default(),
		VocabularyHead:        vocabulary.Head,
		Priorities:            pagePriorities(vocabulary.Priorities),
		DefaultPriority:       vocabulary.Priorities.Default(),
		AttachmentFileLimit:   core.MaxAttachmentFileBytes,
		AttachmentTotalLimit:  core.MaxLiveAttachmentBytes,
		AttachmentNameLimit:   core.MaxAttachmentNameBytes,
		AttachmentLabelLimit:  core.MaxAttachmentLabelBytes,
		AttachmentURLLimit:    core.MaxAttachmentURLBytes,
		InlineImageMediaTypes: strings.Join(InlineAttachmentMediaTypes(), " "),
		AssignIdentity:        handler.assignIdentity(),
		StatusTags:            core.StatusTags(),
		PriorityTags:          core.PriorityTags(),
		Administrable:         handler.administrable(),
		DisplayAdministrable:  handler.administrable() && handler.SetDisplay != nil,
		PrioritiesAdministrable: handler.administrable() && handler.AddPriority != nil &&
			handler.EditPriority != nil && handler.RemovePriority != nil &&
			handler.MovePriority != nil && handler.SetDefaultPriority != nil &&
			handler.RecolorPriority != nil,
	}); err != nil {
		return
	}
}

// serveConfig answers the configuration route, which is the board's page under
// another path: the client renders the route it reads out of the address, so a
// hard load of /config and a click through to it from the board have to be
// served the same document — the same way a task's own page is.
//
// The address used to be /statuses, which named the one thing the page held. It
// holds the project's display settings too now, so the page is the project's
// configuration and the address says so. Nothing forwards the old path: it is an
// address a bookmark may hold and nothing else, this board's routes are read out
// of the address by a client that has to agree with the server about every one
// of them, and a redirect would be a second name for a page with one.
//
// It is the one page route that a board can be built without. The route is
// registered whatever the board can do, so the method question has one answer
// everywhere, and a board that cannot change its statuses answers the address
// with a 404 rather than a page whose every control would be refused.
func (handler *handler) serveConfig(writer http.ResponseWriter, request *http.Request) {
	if !handler.administrable() {
		http.NotFound(writer, request)
		return
	}
	handler.serveBoard(writer, request)
}

// administrable is whether this board was built with all four vocabulary
// mutations. See pageData.Administrable for why all four rather than any.
func (handler *handler) administrable() bool {
	return handler.AddStatus != nil && handler.EditStatus != nil &&
		handler.RemoveStatus != nil && handler.ReorderStatus != nil
}

// serveVocabulary reports the project's statuses to a client that wants more
// than the columns already in the page — the labels behind a token in a history
// entry, or the chain a stored status was forwarded along.
func (handler *handler) serveVocabulary(writer http.ResponseWriter, request *http.Request) {
	state, _, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, vocabularyDocument(state))
}

// vocabularyDocument renders one read of the project's statuses. The read route
// and every mutation answer with it, so a client that can draw the board from
// GET /api/vocabulary can draw the result of a change it made without a second
// code path.
func vocabularyDocument(state VocabularyState) VocabularyDocument {
	document := state.Vocabulary.Document()
	rendered := VocabularyDocument{
		Format:   "workbook.vocabulary",
		Version:  1,
		Head:     state.Head,
		Default:  state.Vocabulary.Default(),
		Statuses: document.Statuses,
		Aliases:  document.Aliases,
		Retired:  document.Retired,
		// EffectiveDocument rather than Document, because this is a reading:
		// what the board draws for a project that has configured no priorities
		// is the built-in three, not an empty list. Document is for a caller
		// writing a checkpoint, which this is not.
		Priorities: priorityVocabularyDocument(state.Priorities),
	}
	if state.Display.Configured() {
		display := displayDocument(state)
		rendered.Display = &display
	}
	return rendered
}

// priorityVocabularyDocument renders one read of the project's priorities, in
// the shape the read route and every mutation answer with.
func priorityVocabularyDocument(priorities core.PriorityVocabulary) PriorityVocabularyDocument {
	document := priorities.EffectiveDocument()
	return PriorityVocabularyDocument{
		Default:    priorities.Default(),
		Priorities: document.Priorities,
		Aliases:    document.Aliases,
		Retired:    document.Retired,
		// The same composer the page's own stylesheet is rendered from, called
		// on the same priorities, so the board a change produces is drawn by the
		// code that drew the board the change was made from.
		Ink: string(priorityInk(priorities)),
	}
}

// pagePriority is one priority as the page carries it: what it is called, what
// it is called on screen, what role it holds, and what color it is drawn in.
// Its members mirror core.PriorityDefinition's minus the rank, for the reason
// pagePriorities gives.
type pagePriority struct {
	Priority core.Priority      `json:"priority"`
	Label    string             `json:"label"`
	Tags     []core.PriorityTag `json:"tags"`
	Color    string             `json:"color,omitempty"`
}

// pagePriorities encodes a project's priorities for the attribute the page
// carries them in.
//
// It is JSON rather than the space-separated list StatusTags uses because a
// priority is four facts — its token, its label, its role and its ink — and the
// client must not carry a second copy of any of them. It is encoded here rather
// than in the template because the template has one derivation and the comment
// on pageFuncs says why it has one.
//
// The rank is deliberately not among them. It is the server's own ordering
// arithmetic, and the array is already in rank order, so publishing it would
// invite the client to re-derive a sequence it was handed — and then to
// disagree with the server about it. The client needs to know what the order
// IS, never how it was arrived at.
//
// What this carries is the EFFECTIVE reading: a project that has configured no
// priorities is answered with the built-in three rather than with nothing,
// which is what the board has to draw either way. It therefore cannot tell a
// configured vocabulary from a substituted one — so a caller that means to
// write these back has to diff against the ledger rather than round-tripping
// them, or it would record the built-ins as a decision the project never made
// and stamp the compatibility marker that parks older clones for it.
func pagePriorities(priorities core.PriorityVocabulary) string {
	definitions := priorities.EffectiveDocument().Priorities
	published := make([]pagePriority, 0, len(definitions))
	for _, definition := range definitions {
		published = append(published, pagePriority{
			Priority: definition.Priority,
			Label:    definition.Label,
			Tags:     definition.Tags,
			Color:    definition.Color,
		})
	}
	encoded, err := json.Marshal(published)
	if err != nil {
		// A priority definition is three strings, a tag list and a color, so
		// there is nothing here encoding/json can refuse. A board that drew no
		// priorities at all is a better answer to the impossible case than a
		// page that will not load.
		return "[]"
	}
	return string(encoded)
}

// addVocabularyStatus defines a status this project does not have.
func (handler *handler) addVocabularyStatus(writer http.ResponseWriter, request *http.Request) {
	if handler.AddStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "status addition is not configured"))
		return
	}
	var body addStatusRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode status add", err))
		return
	}
	if body.Before != "" && body.After != "" {
		handler.writeError(writer, core.Errorf(core.CategoryInvocation,
			"a status is placed before or after another status, not both"))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.AddStatus(request.Context(), VocabularyStatusAddition{
		Status:       body.Status,
		Label:        body.Label,
		Tags:         body.Tags,
		Before:       body.Before,
		After:        body.After,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writeVocabularyMutation(writer, mutation)
}

// editVocabularyStatus renames, relabels and retags one status in any
// combination, because the configuration page edits a status as one form and a
// form is one intent.
func (handler *handler) editVocabularyStatus(writer http.ResponseWriter, request *http.Request) {
	if handler.EditStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "status editing is not configured"))
		return
	}
	var body editStatusRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode status change", err))
		return
	}
	if body.Name == nil && body.Label == nil && body.Tags == nil {
		handler.writeError(writer, core.Errorf(core.CategoryInvocation,
			"a status change must set at least one of name, label or tags"))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.EditStatus(request.Context(), vocabularyStatusOf(request), VocabularyStatusEdit{
		Name:         body.Name,
		Label:        body.Label,
		Tags:         body.Tags,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writeVocabularyMutation(writer, mutation)
}

// removeVocabularyStatus retires a status and forwards its tasks.
//
// The destination travels in the body of a DELETE, which is unusual and
// deliberate: a removal is meaningless without somewhere for the work in that
// column to go, so the one member the route cannot do without is the one member
// a bare DELETE would have no room for.
func (handler *handler) removeVocabularyStatus(writer http.ResponseWriter, request *http.Request) {
	if handler.RemoveStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "status removal is not configured"))
		return
	}
	var body removeStatusRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode status removal", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.RemoveStatus(request.Context(), vocabularyStatusOf(request), VocabularyStatusRemoval{
		Into:         body.Into,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writeVocabularyMutation(writer, mutation)
}

// reorderVocabulary sets the whole column order at once.
func (handler *handler) reorderVocabulary(writer http.ResponseWriter, request *http.Request) {
	if handler.ReorderStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "status ordering is not configured"))
		return
	}
	var body reorderStatusesRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode status order", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.ReorderStatus(request.Context(), VocabularyOrder{
		Statuses:     body.Statuses,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writeVocabularyMutation(writer, mutation)
}

// The six priority mutation routes.
//
// Each is the status routes' shape with one difference worth stating once here
// rather than six times below: almost nothing about a request is refused at
// this layer. The priority writer already refuses a placement naming both
// neighbors, a move naming none, a removal with nowhere to forward to, an edit
// that changes nothing, a recolor that records nothing and the removal of a
// project's last priority — each in the sentence `workbook priority` uses, each
// tested once against the real planners. A check repeated here would be a
// second place those sentences could change, and a body this layer flattened
// into "a configuration write must carry at least one operation" would be true
// and would tell a person nothing.
//
// What these routes do decide is what only a route can: that a change names the
// configuration it was composed against, that a recolor says whether it is
// setting or clearing, and which HTTP status a category reads as.

// addVocabularyPriority defines a priority this project does not have.
func (handler *handler) addVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.AddPriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "priority addition is not configured"))
		return
	}
	var body addPriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode priority add", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.AddPriority(request.Context(), VocabularyPriorityAddition{
		Priority:     body.Priority,
		Label:        body.Label,
		Before:       body.Before,
		After:        body.After,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// editVocabularyPriority renames and relabels one priority, because a panel
// edits a priority as one form and a form is one intent.
//
// It does not take the default role, and a panel offering "rename this and make
// it the default" in one Save therefore makes two requests: this one, and then
// the default route against the head this one answered with. That is faithful
// to the section rather than convenient — the role is one operation that the
// fold transfers off whoever held it, and there is no planner that renames and
// transfers in one pack.
func (handler *handler) editVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.EditPriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "priority editing is not configured"))
		return
	}
	var body editPriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode priority change", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.EditPriority(request.Context(), vocabularyPriorityOf(request), VocabularyPriorityEdit{
		Name:         body.Name,
		Label:        body.Label,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// removeVocabularyPriority retires a priority and forwards its tasks.
//
// The destination travels in the body of a DELETE for the reason a status
// removal's does: a removal is meaningless without somewhere for the work at
// that priority to go, so the one member the route cannot do without is the one
// member a bare DELETE would have no room for.
func (handler *handler) removeVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.RemovePriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "priority removal is not configured"))
		return
	}
	var body removePriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode priority removal", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.RemovePriority(request.Context(), vocabularyPriorityOf(request), VocabularyPriorityRemoval{
		Into:         body.Into,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// moveVocabularyPriority moves one priority next to one of its peers.
//
// It takes an anchor rather than an order, which is the one place a priorities
// panel cannot be written as a copy of the statuses panel: there is no planner
// that takes a whole sequence, so a drag has to be reduced to "before X" or
// "after X" before it is sent. See VocabularyPriorityMove.
func (handler *handler) moveVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.MovePriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "priority ordering is not configured"))
		return
	}
	var body movePriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode priority move", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.MovePriority(request.Context(), vocabularyPriorityOf(request), VocabularyPriorityMove{
		Before:       body.Before,
		After:        body.After,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// setDefaultVocabularyPriority gives one priority the role a task created with
// no priority lands on. The priority that held it gives it up in the same
// operation, which is why there is nothing in the body but the head.
func (handler *handler) setDefaultVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.SetDefaultPriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "the default priority is not configurable here"))
		return
	}
	var body defaultPriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode default priority", err))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.SetDefaultPriority(request.Context(), vocabularyPriorityOf(request), VocabularyPriorityDefault{
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// recolorVocabularyPriority sets or clears the ink one priority is drawn in.
//
// The color member is required, and an empty one is the clearing. An absent one
// is refused rather than read as a clearing, because clearing a priority's ink
// is a decision somebody makes: the value the writer refuses to record twice is
// the value a client must be explicit about sending.
func (handler *handler) recolorVocabularyPriority(writer http.ResponseWriter, request *http.Request) {
	if handler.RecolorPriority == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "priority colors are not configurable here"))
		return
	}
	var body recolorPriorityRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode priority color", err))
		return
	}
	if body.Color == nil {
		handler.writeError(writer, core.Errorf(core.CategoryInvocation,
			"a priority color change must name a color; send an empty one to clear it"))
		return
	}
	head, err := vocabularyHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.RecolorPriority(request.Context(), vocabularyPriorityOf(request), VocabularyPriorityRecolor{
		// Handed on as it was sent. Trimming a color and deciding what one is
		// belongs to the verb family, and the board keeps exactly one reading of
		// that — the one whose refusal a person has already seen on a terminal.
		Color:        *body.Color,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeVocabularyError(writer, request, err)
		return
	}
	handler.writePriorityMutation(writer, mutation)
}

// vocabularyPriorityOf reads the priority a per-priority route addresses, from
// the mux's pattern where there is one and from the path where a caller built
// the request itself.
func vocabularyPriorityOf(request *http.Request) core.Priority {
	if priority := request.PathValue("priority"); priority != "" {
		return core.Priority(priority)
	}
	if priority, _, ok := vocabularyPriorityMemberPath(request.URL.Path); ok {
		return core.Priority(priority)
	}
	return core.Priority(vocabularyPriorityPathName(request.URL.Path))
}

// writePriorityMutation answers a recorded priority change with the whole
// configuration, in its own envelope rather than the status mutation's. See
// VocabularyPriorityTaskCounts for what that envelope is not carrying and why.
func (handler *handler) writePriorityMutation(writer http.ResponseWriter, mutation VocabularyPriorityMutation) {
	writeJSON(writer, http.StatusOK, VocabularyPriorityMutationDocument{
		Format:     "workbook.priority-mutation",
		Version:    1,
		Vocabulary: vocabularyDocument(mutation.State),
		Tasks:      mutation.Tasks,
		Warnings:   mutation.Warnings,
	})
}

// vocabularyStatusOf reads the status a per-status route addresses, from the
// mux's pattern where there is one and from the path where a caller built the
// request itself.
func vocabularyStatusOf(request *http.Request) core.Status {
	if status := request.PathValue("status"); status != "" {
		return core.Status(status)
	}
	return core.Status(vocabularyStatusPathName(request.URL.Path))
}

// vocabularyHead reads the head a change was composed against, refusing one
// that names none.
//
// It is required where a task mutation's expectedHead is optional, and the
// asymmetry is the point. A task's optimistic queue re-bases on a refusal and
// carries on; a status change is a decision about every column on the board and
// about where every task in one of them lands, so it is made against columns
// somebody has seen or it is not made. A client that cannot say which
// vocabulary it composed the change against does not know what it is changing.
//
// What is required is that the member is there, not that it says something. A
// project whose configuration ledger has never been seeded has no head, GET
// /api/vocabulary reports it as the empty string, and a client that sends that
// back is telling the truth about what it read — while a client that omits the
// member entirely is telling us nothing.
func vocabularyHead(expected *string) (string, error) {
	if expected == nil {
		return "", core.Errorf(core.CategoryValidation,
			"expectedHead is required; it names the vocabulary this change was composed against")
	}
	return *expected, nil
}

func (handler *handler) writeVocabularyMutation(writer http.ResponseWriter, mutation VocabularyMutation) {
	writeJSON(writer, http.StatusOK, VocabularyMutationDocument{
		Format:     "workbook.vocabulary-mutation",
		Version:    1,
		Vocabulary: vocabularyDocument(mutation.State),
		Tasks:      mutation.Tasks,
		Warnings:   mutation.Warnings,
	})
}

// writeVocabularyError reports a refused change, and hands back the statuses
// the client should recompose it against when the refusal was that it was
// looking at old ones.
//
// Nothing is rebased and nothing is merged. A vocabulary is a decision somebody
// made about how this project works, and a server that resolved two of them by
// applying both would be inventing a third that neither author chose. So a
// stale write is refused, and the current state travels with the refusal so the
// person who made the change can see what happened while they were composing
// it.
//
// A vocabulary that cannot be read at the moment of the refusal costs the
// client its re-render, not its refusal: the error is reported as it stands
// rather than replaced by the read's own failure.
func (handler *handler) writeVocabularyError(writer http.ResponseWriter, request *http.Request, err error) {
	body := errorBody(err)
	if body.Category != core.CategoryStaleWrite {
		handler.writeError(writer, err)
		return
	}
	state, _, readErr := handler.vocabulary(request)
	if readErr != nil {
		handler.writeError(writer, err)
		return
	}
	document := vocabularyDocument(state)
	writeJSON(writer, statusForError(body.Category), VocabularyErrorDocument{
		Format:     "workbook.error",
		Version:    1,
		Error:      body,
		Vocabulary: &document,
	})
}

// listTasks reads the board's tasks, reporting a board built without a lister
// the way every other route reports a capability it was not given. Listing was
// mandatory by signature while the constructor was positional; a named field
// can be left out, so the check has to exist.
func (handler *handler) listTasks(request *http.Request) ([]core.Task, error) {
	if handler.List == nil {
		return nil, core.Errorf(core.CategoryOperational, "task listing is not configured")
	}
	return handler.List(request.Context())
}

func (handler *handler) serveTasks(writer http.ResponseWriter, request *http.Request) {
	vocabulary, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	tasks, err := handler.listTasks(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	// Which tasks this poll is for, asked as one parameter with three answers
	// rather than as two parameters that could disagree. `true` is the deleted
	// tasks alone and keeps meaning exactly that: the relationship picker still
	// asks it, and a value it never sends must not change under it. `include` is
	// the whole board — the active tasks and the deleted ones in one document,
	// each carrying the `deleted` flag that says which it is — because the board
	// draws both at once when its Deleted column is shown, and two polls could
	// only disagree about the moment they read.
	switch request.URL.Query().Get("deleted") {
	case "true":
		tasks = deletedTasks(tasks)
	case "include":
	default:
		tasks = activeTasks(tasks)
	}
	writeJSON(writer, http.StatusOK, TasksDocument{
		Format:         "workbook.tasks",
		Version:        1,
		VocabularyHead: vocabulary.Head,
		Tasks:          tasks,
		Presentation:   taskPresentation(tasks, vocabulary.Vocabulary, handler.assignIdentity()),
	})
}

func (handler *handler) serveTaskHistory(writer http.ResponseWriter, request *http.Request) {
	if handler.History == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task history is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskHistoryPathID(request.URL.Path)
	}
	vocabulary, request, err := handler.vocabulary(request)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	detail, err := handler.History(request.Context(), id)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if detail.History == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task history reader returned no change log"))
		return
	}
	writeJSON(writer, http.StatusOK, TaskHistoryDocument{
		Format:    "workbook.task-history",
		Version:   1,
		TaskID:    detail.ID,
		Lifecycle: lifecycleStages(*detail.History, detail.Status, vocabulary.Vocabulary),
		History:   *detail.History,
	})
}

func lifecycleStages(log core.ChangeLog, current core.Status, vocabulary core.Vocabulary) []LifecycleStage {
	stops := presentation.Lifecycle(log, current, vocabulary)
	stages := make([]LifecycleStage, len(stops))
	for index, stop := range stops {
		stages[index] = LifecycleStage{
			Status:   stop.Status,
			Label:    stop.Label,
			Commit:   stop.Commit,
			Actor:    stop.Actor,
			WallTime: stop.WallTime,
			Current:  stop.Current,
		}
	}
	return stages
}

func activeTasks(tasks []core.Task) []core.Task {
	active := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		if !task.Deleted {
			active = append(active, task)
		}
	}
	return active
}

func deletedTasks(tasks []core.Task) []core.Task {
	deleted := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Deleted {
			deleted = append(deleted, task)
		}
	}
	return deleted
}

func (handler *handler) updateTaskStatus(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" {
		id = taskStatusPathID(request.URL.Path)
	}
	var input updateStatusRequest
	if err := decodeRequest(request.Body, &input); err != nil {
		handler.writeError(writer, decodeRequestError("decode status update", err))
		return
	}
	if handler.UpdateStatus == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task status updating is not configured"))
		return
	}
	result, err := handler.UpdateStatus(request.Context(), id, input.Status, input.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) positionTask(writer http.ResponseWriter, request *http.Request) {
	if handler.Position == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task positioning is not configured"))
		return
	}
	var body positionTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task position", err))
		return
	}
	result, err := handler.Position(
		request.Context(),
		request.PathValue("id"),
		core.PlaceInput{Status: body.Status, Before: body.Before, After: body.After, ExpectedHead: body.ExpectedHead},
	)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) createTask(writer http.ResponseWriter, request *http.Request) {
	var body createTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task create", err))
		return
	}
	if handler.Create == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task creation is not configured"))
		return
	}
	result, err := handler.Create(request.Context(), core.CreateInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) updateTask(writer http.ResponseWriter, request *http.Request) {
	var body updateTaskRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task update", err))
		return
	}
	if handler.Update == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task updating is not configured"))
		return
	}
	id := request.PathValue("id")
	if id == "" {
		id = taskPathID(request.URL.Path)
	}
	result, err := handler.Update(request.Context(), id, body.input())
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) deleteTask(writer http.ResponseWriter, request *http.Request) {
	var body deleteTaskRequest
	if err := decodeOptionalRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task delete", err))
		return
	}
	if handler.Delete == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task deletion is not configured"))
		return
	}
	result, err := handler.Delete(request.Context(), request.PathValue("id"), core.DeleteInput(body))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) restoreTask(writer http.ResponseWriter, request *http.Request) {
	var body restoreTaskRequest
	if err := decodeOptionalRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode task restore", err))
		return
	}
	if handler.Restore == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task restoration is not configured"))
		return
	}
	result, err := handler.Restore(
		request.Context(),
		request.PathValue("id"),
		core.RestoreInput{Into: body.Status, Before: body.Before, After: body.After, ExpectedHead: body.ExpectedHead},
	)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) addTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if err := requireEmptyRequestBody(request.Body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "validate dependency request", err))
		return
	}
	if handler.Depend == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency addition is not configured"))
		return
	}
	result, err := handler.Depend(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func (handler *handler) removeTaskDependency(writer http.ResponseWriter, request *http.Request) {
	if err := requireEmptyRequestBody(request.Body); err != nil {
		handler.writeError(writer, core.Wrap(core.CategoryInvocation, "validate dependency request", err))
		return
	}
	if handler.Free == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "task dependency removal is not configured"))
		return
	}
	result, err := handler.Free(request.Context(), request.PathValue("id"), request.PathValue("dependency"))
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	handler.writeTaskMutation(writer, result)
}

func requireEmptyRequestBody(body io.Reader) error {
	read, err := io.CopyN(io.Discard, body, 1)
	if read > 0 {
		return errors.New("request body must be empty")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// MaxRequestBodyBytes bounds one request body.
//
// The largest task this version can store is a title, a description, and a
// label set at core's ceilings — under 70 KiB of task text. JSON escaping can
// expand that severalfold in the worst case, so this sits an order of magnitude
// above the largest body the board could honestly send while still refusing a
// client that means to stream indefinitely. A request over it is the sender's
// mistake, so it reads as an invocation failure rather than a validation one.
const MaxRequestBodyBytes = 1 << 20

// MaxAttachmentUploadBodyBytes bounds the one body that is legitimately larger
// than the ceiling above: an attachment upload.
//
// An attached file may be core.MaxAttachmentFileBytes — exactly the ceiling
// above — and it travels as base64 inside a JSON object, because the board's
// same-origin guard requires every mutation to declare application/json and a
// multipart body is one of the three types a cross-site form can send without a
// preflight. Base64 costs four bytes for every three, so the same file needs
// four thirds of that ceiling before a single member of the envelope is
// written; the remaining room is for the envelope itself, whose largest members
// are a file name and a head.
//
// It is a ceiling on the encoding, not a second ceiling on attachments. The
// attachment itself is bounded by core, once, and the upload route refuses an
// over-sized file by that number and in core's own words before anything is
// staged.
const MaxAttachmentUploadBodyBytes = ((core.MaxAttachmentFileBytes+2)/3)*4 + 64<<10

// requestBodyLimit is how many bytes this path's body may carry. Asked of the
// path rather than of the matched route, because the limit has to be in place
// before the mux has matched anything.
func requestBodyLimit(path string) int64 {
	if taskAttachmentsPathID(path) != "" {
		return MaxAttachmentUploadBodyBytes
	}
	return MaxRequestBodyBytes
}

// decodeRequest reads exactly one JSON value from the request body, which
// serveHTTP has already bounded.
func decodeRequest(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requireDecoderExhausted(decoder)
}

// decodeOptionalRequest reads one JSON value from a body the client is allowed
// to leave out. No body at all leaves the value zero and is not an error; a
// body that is present is held to exactly what decodeRequest demands, unknown
// members and trailing values included.
//
// This is what lets a route gain members without breaking the clients that
// already call it: they keep sending nothing, and keep getting the behavior
// they had.
func decodeOptionalRequest(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		// Only an empty body reads as EOF here. A body that stops partway
		// through a value reads as an unexpected EOF, which is a malformed
		// request rather than an absent one.
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return requireDecoderExhausted(decoder)
}

// requireDecoderExhausted rejects a body carrying more than the one value the
// route asked for.
func requireDecoderExhausted(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// decodeRequestError categorizes a body-decoding failure for a client.
//
// Only the outermost message reaches the response, so a body stopped by the
// ceiling is reported as itself rather than wrapped in the route's context: the
// route's "decode task create" alone would leave the sender to guess whether
// its JSON was malformed or merely too large, which is the one decode failure a
// client can act on without seeing the body.
func decodeRequestError(context string, err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		// The ceiling the reader ran into rather than the ordinary one: the
		// attachment upload route carries a larger one, and quoting a number
		// that route does not enforce would send a sender to shrink a body that
		// was already inside the limit it was refused by.
		return core.Errorf(
			core.CategoryInvocation,
			"request body must not exceed %d bytes",
			tooLarge.Limit,
		)
	}
	return core.Wrap(core.CategoryInvocation, context, err)
}

func (handler *handler) writeTaskMutation(writer http.ResponseWriter, result core.MutationResult) {
	writeJSON(writer, http.StatusOK, TaskMutationDocument{
		Format:   "workbook.task-mutation",
		Version:  1,
		Task:     result.Task,
		Warnings: result.Warnings,
	})
}

func taskPresentation(tasks []core.Task, vocabulary core.Vocabulary, actor string) []TaskPresentation {
	views := presentation.TaskViews(tasks, vocabulary)
	// One clock for the whole document, read once. A staleness hint computed per
	// task could cross a minute boundary halfway down the board and report two
	// ages for two assignments recorded in the same second.
	now := time.Now()
	result := make([]TaskPresentation, len(views))
	for index, view := range views {
		row := assignmentRow(view.Task.Assignments)
		result[index] = TaskPresentation{
			TaskID:                view.Task.ID,
			IDPrefix:              view.IDPrefix,
			DependenciesComplete:  view.DependenciesComplete,
			DependenciesTotal:     view.DependenciesTotal,
			WaitingOnDependencies: view.WaitingOnDependencies,
			AssignmentChips:       row.Chips,
			MoreAssignments:       row.More,
			Assignments:           assignmentPresentation(view.Task.Assignments, now, actor),
		}
	}
	return result
}

func (handler *handler) serveSyncState(writer http.ResponseWriter, request *http.Request) {
	if handler.SyncState == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "publication state is not configured"))
		return
	}
	writeJSON(writer, http.StatusOK, SyncDocument{Format: "workbook.sync", Version: 1, Sync: handler.SyncState(request.Context())})
}

func (handler *handler) updateSyncMode(writer http.ResponseWriter, request *http.Request) {
	if handler.SetSyncMode == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "publication mode is not configured"))
		return
	}
	var body syncModeRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode publication mode", err))
		return
	}
	state, err := handler.SetSyncMode(request.Context(), body.Mode)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, SyncDocument{Format: "workbook.sync", Version: 1, Sync: state})
}

func (handler *handler) serveHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, HealthDocument{
		Format:  "workbook.health",
		Version: 1,
		Status:  "ok",
	})
}

func (handler *handler) writeError(writer http.ResponseWriter, err error) {
	body := errorBody(err)
	writeJSON(writer, statusForError(body.Category), ErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error:   body,
	})
}

// errorBody is how any failure reads to a client, and the one place that
// decides it: an uncategorized failure is operational, and an operational one
// keeps the context its wrapping added because nobody can act on "permission
// denied" without knowing what was denied.
func errorBody(err error) ErrorBody {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryOperational
	}
	message := err.Error()
	var typed *core.Error
	if errors.As(err, &typed) && category != core.CategoryOperational {
		message = typed.Message
	}
	return ErrorBody{Category: category, Message: message}
}

func statusForError(category core.Category) int {
	switch category {
	case core.CategoryInvocation, core.CategoryValidation:
		return http.StatusBadRequest
	case core.CategoryNotFound:
		return http.StatusNotFound
	// A newer-writer refusal sits with the other conflicts rather than with the
	// server errors. Nothing failed here: the request was well formed, the
	// resource is readable, and this build declines to change it until it is
	// upgraded. Answering 500 would report a fault the server does not have and
	// would send a client retrying rather than reporting.
	case core.CategoryNotInitialized, core.CategoryStaleWrite, core.CategoryConflict, core.CategoryNewerWriter:
		return http.StatusConflict
	case core.CategoryCorruptData, core.CategoryOperational:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
