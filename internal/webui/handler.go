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
type SyncState struct {
	Mode    string `json:"mode"`
	Watcher bool   `json:"watcher"`
	Detail  string `json:"detail,omitempty"`
}

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

// VocabularyState is what a resolver reports: the project's statuses and the
// configuration ledger tip they were read from.
type VocabularyState struct {
	Vocabulary core.Vocabulary
	// Head is the configuration ledger's tip, empty for a project with none.
	Head string
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
type AssignmentPresentation struct {
	Principal string    `json:"principal"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Ago       string    `json:"ago"`
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
func assignmentPresentation(assignments []core.Assignment, now time.Time) []AssignmentPresentation {
	if len(assignments) == 0 {
		return nil
	}
	rendered := make([]AssignmentPresentation, 0, len(assignments))
	for _, assignment := range assignments {
		rendered = append(rendered, AssignmentPresentation{
			Principal: assignment.Principal,
			Label:     assignment.Label,
			CreatedAt: assignment.CreatedAt,
			Ago:       presentation.AssignedAgo(assignment, now),
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
	// statuses route.
	AddStatus     VocabularyStatusAdder
	EditStatus    VocabularyStatusEditor
	RemoveStatus  VocabularyStatusRemover
	ReorderStatus VocabularyReorderer
	List          TaskLister
	Create        TaskCreator
	Update        TaskUpdater
	UpdateStatus  TaskStatusUpdater
	Position      TaskPositionUpdater
	Delete        TaskDeleter
	Restore       TaskRestorer
	Depend        TaskDependencyAdder
	Free          TaskDependencyRemover
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
	History           TaskHistoryReader
	SyncState         SyncStateReporter
	SetSyncMode       SyncModeSetter
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
	// DefaultStatus is where a new task lands, rendered as an attribute because
	// the client needs it before it has fetched anything and must not guess.
	DefaultStatus core.Status
	// VocabularyHead is the ledger tip these columns were built from, so the
	// poll can tell that the columns it is looking at have been superseded.
	VocabularyHead string
	// AttachmentFileLimit is core's ceiling on one attached file, rendered into
	// the page for the same reason StatusTags is: the upload control refuses a
	// file this large before it spends a minute encoding and sending one the
	// server would refuse, and a number the script carried itself would be a
	// second copy of a ceiling core owns — one that would go on naming the old
	// number the day core's changed.
	AttachmentFileLimit int64
	// InlineImageMediaTypes are the media types the attachment route serves
	// inline, space separated, rendered into the page for the reason the ceiling
	// above is: the markdown renderer draws an attachment reference as an <img>
	// only for a type that comes back as pixels, and the set of those types is
	// the download route's to decide.
	InlineImageMediaTypes string
	// StatusTags are the three roles a status may carry, rendered into the
	// statuses page's forms for the reason the columns are rendered into the
	// board: the client must not carry a second copy of a set the server owns.
	// It is also what keeps the script from naming `done`, which is a tag here
	// and a status name in most projects.
	StatusTags []core.StatusTag
	// Administrable is whether this board was built with all four vocabulary
	// mutations. It decides whether the page carries the statuses route's link
	// and body at all, and serveStatuses answers the address itself with a 404
	// when it is false — one gate, read on both sides.
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
	handler.mux.HandleFunc("GET /statuses", handler.serveStatuses)
	handler.mux.HandleFunc("GET /tasks/new", handler.serveBoard)
	handler.mux.HandleFunc("GET /tasks/{id}", handler.serveBoard)
	handler.mux.HandleFunc("GET /api/tasks", handler.serveTasks)
	handler.mux.HandleFunc("GET /api/vocabulary", handler.serveVocabulary)
	handler.mux.HandleFunc("POST /api/vocabulary/statuses", handler.addVocabularyStatus)
	handler.mux.HandleFunc("PATCH /api/vocabulary/statuses/{status}", handler.editVocabularyStatus)
	handler.mux.HandleFunc("DELETE /api/vocabulary/statuses/{status}", handler.removeVocabularyStatus)
	handler.mux.HandleFunc("PUT /api/vocabulary/order", handler.reorderVocabulary)
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
	case "/", "/healthz", "/statuses", "/tasks/new":
		return http.MethodGet, true
	case "/api/tasks":
		return http.MethodGet + ", " + http.MethodPost, true
	case "/api/vocabulary":
		return http.MethodGet, true
	case "/api/vocabulary/statuses":
		return http.MethodPost, true
	case "/api/vocabulary/order":
		return http.MethodPut, true
	case "/api/sync":
		return http.MethodGet + ", " + http.MethodPut, true
	default:
		if vocabularyStatusPathName(path) != "" {
			return http.MethodPatch + ", " + http.MethodDelete, true
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
		DefaultStatus:         vocabulary.Vocabulary.Default(),
		VocabularyHead:        vocabulary.Head,
		AttachmentFileLimit:   core.MaxAttachmentFileBytes,
		InlineImageMediaTypes: strings.Join(InlineAttachmentMediaTypes(), " "),
		StatusTags:            core.StatusTags(),
		Administrable:         handler.administrable(),
	}); err != nil {
		return
	}
}

// serveStatuses answers the statuses route, which is the board's page under
// another path: the client renders the route it reads out of the address, so a
// hard load of /statuses and a click through to it from the board have to be
// served the same document — the same way a task's own page is.
//
// It is the one page route that a board can be built without. The route is
// registered whatever the board can do, so the method question has one answer
// everywhere, and a board that cannot change its statuses answers the address
// with a 404 rather than a page whose every control would be refused.
func (handler *handler) serveStatuses(writer http.ResponseWriter, request *http.Request) {
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
	return VocabularyDocument{
		Format:   "workbook.vocabulary",
		Version:  1,
		Head:     state.Head,
		Default:  state.Vocabulary.Default(),
		Statuses: document.Statuses,
		Aliases:  document.Aliases,
		Retired:  document.Retired,
	}
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
// combination, because the statuses page edits a status as one form and a form
// is one intent.
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
		Presentation:   taskPresentation(tasks, vocabulary.Vocabulary),
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

func taskPresentation(tasks []core.Task, vocabulary core.Vocabulary) []TaskPresentation {
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
			Assignments:           assignmentPresentation(view.Task.Assignments, now),
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
