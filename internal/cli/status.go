package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/terminalui"
)

// statusChange is what one status command did, in the shape every mutating
// status envelope carries.
//
// One shape for seven verbs is deliberate. A caller that reads `change.status`
// and `change.operation` can handle a verb it has never heard of, and a verb
// that grows a member does not become a new document. The members a verb does
// not use are omitted rather than zeroed, so the presence of `into` is what
// says a removal happened.
type statusChange struct {
	// Operation is the verb, not the durable operation type: `workbook status
	// tag` writes several operations and is still one change.
	Operation string `json:"operation"`
	// Status is the subject after the change. A rename reports the new value
	// here and the old one in From, because the new value is what every later
	// command names.
	Status core.Status `json:"status"`
	From   core.Status `json:"from,omitempty"`
	// Into is where a removal forwarded the status's tasks.
	Into     core.Status     `json:"into,omitempty"`
	Position *statusPosition `json:"position,omitempty"`
	Label    *statusLabel    `json:"label,omitempty"`
	// LabelDerived reports that the label came from the status name rather than
	// from anybody's choice. It is meaningful for a rename, where it says
	// whether a custom label was kept.
	LabelDerived *bool `json:"labelDerived,omitempty"`
	// Tags is the status's whole tag set after the change, not the difference,
	// so a caller never has to reconstruct it.
	Tags []core.StatusTag `json:"tags,omitempty"`
	// DefaultFrom names the status that gave up the default tag, when this
	// change moved it. Exactly one status carries that tag, so taking it is
	// always also giving it up somewhere.
	DefaultFrom core.Status `json:"defaultFrom,omitempty"`
}

// statusPosition is where a status sits after the change: the neighbour that
// was named, the rank it produced, and the 1-based place a person reads.
type statusPosition struct {
	Before core.Status `json:"before,omitempty"`
	After  core.Status `json:"after,omitempty"`
	Rank   string      `json:"rank"`
	Order  int         `json:"order"`
}

type statusLabel struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// statusView is one status as the envelopes present it.
type statusView struct {
	Status core.Status      `json:"status"`
	Label  string           `json:"label"`
	Tags   []core.StatusTag `json:"tags"`
	Order  int              `json:"order"`
	// Tasks counts the active tasks resolving to this status. It is a pointer
	// because a mutating verb does not count them: a status change is not a
	// task read, and paying for the projection on every write to report a
	// number nobody asked for would be the wrong trade.
	Tasks *int `json:"tasks,omitempty"`
}

// vocabularyView is the project's statuses as they stand after a change.
type vocabularyView struct {
	Head string `json:"head"`
	// Seeded reports that a configuration ledger supplied these statuses. False
	// means they are the fallback this build carries, which is every project
	// until somebody changes a status.
	Seeded   bool         `json:"seeded"`
	Default  core.Status  `json:"default"`
	Statuses []statusView `json:"statuses"`
}

// statusTaskCounts reports what a change means for the tasks in the status.
//
// Both members are zero for every verb but delete, and stated rather than
// omitted: a caller reading `tasks.affected` gets an answer from every status
// command instead of having to know which ones populate it.
type statusTaskCounts struct {
	// Affected counts the active tasks that resolved through the removed
	// status.
	Affected int `json:"affected"`
	// ClaimableAfter counts how many of those become eligible for `workbook
	// next` where they land. It is a floor on what an agent's queue gains and
	// not the whole of it: removing a status into a done-tagged one can also
	// satisfy the last dependency of a task that was never in the removed
	// status, and such a task is outside the population this counts.
	ClaimableAfter int `json:"claimableAfter"`
}

// statusInverse is the command that undoes a change, and how completely.
//
// There is no `workbook status undo`. An inverse that a person reads, edits,
// and runs is worth more than a verb that promises to reverse something it
// cannot always reverse: exact says whether running it restores the state this
// change found, and note says what it will not restore.
type statusInverse struct {
	Command string `json:"command"`
	Exact   bool   `json:"exact"`
	Note    string `json:"note,omitempty"`
}

// statusMutationResult is the data member of every mutating status envelope.
type statusMutationResult struct {
	Change     statusChange     `json:"change"`
	Vocabulary vocabularyView   `json:"vocabulary"`
	Tasks      statusTaskCounts `json:"tasks"`
	Inverse    statusInverse    `json:"inverse"`
	// Docs reports what happened to the generated documentation this change
	// invalidated, in the shape `workbook setup` and `workbook docs` already
	// report. It is omitted when --no-docs skipped the regeneration, which is
	// the same distinction setup draws between "nothing was managed" and "these
	// files were".
	Docs *agentdocs.Report `json:"docs,omitempty"`
}

// statusListResult is `workbook status list`.
type statusListResult struct {
	Head     string       `json:"head"`
	Seeded   bool         `json:"seeded"`
	Default  core.Status  `json:"default"`
	Statuses []statusView `json:"statuses"`
	// Retired lists the values that still resolve here without being live, so a
	// teammate reading a stored value can find out what happened to it.
	Retired []retiredStatusView `json:"retired,omitempty"`
	// Unresolved lists stored values that resolve to nothing at all. They are
	// the one state in this document that needs somebody to act.
	Unresolved []unresolvedStatusView `json:"unresolved,omitempty"`
	// Advisories carry what is true about this configuration without being
	// wrong with it — a folded state over one of the size ceilings, which two
	// clones can reach without either author being refused anything. `workbook
	// validate` reports the same list; this is where a person is already
	// looking at the statuses they would shrink.
	Advisories []historyvalidation.Advisory `json:"advisories,omitempty"`
	// Migrations name a status this project still defines that Workbook no
	// longer ships, and the command that retires it. Nothing acts on them: a
	// column with tasks in it is not this build's to remove, so the listing says
	// what changed and prints the command rather than running it.
	//
	// It is absent for every project that does not define such a status — every
	// project minted by this build and every project that has already migrated —
	// and for every project whose ledger records somebody defining one on
	// purpose, which is an answer to the note rather than a state it describes.
	Migrations []statusMigrationView `json:"migrations,omitempty"`
}

// statusMigrationView is one default Workbook dropped that this project kept.
type statusMigrationView struct {
	Status core.Status `json:"status"`
	// Reason says why the status left the default set, so the note is an
	// explanation rather than an instruction with no argument behind it.
	Reason string `json:"reason"`
	// Command is the exact removal, `--into` included, naming this project's own
	// default status rather than assuming it is still called `backlog`.
	//
	// It is absent when no single command performs the removal, and its absence
	// is the shape a caller reads that from: today the one such case is a project
	// whose new tasks land in the dropped status, which another status has to be
	// given the `default` tag before anything can remove it. `first` carries that
	// step, and exactly one of the two members is ever present.
	Command string `json:"command,omitempty"`
	// First is the step that has to happen before a removal is even expressible,
	// with `<status>` left for a person to fill in because only they know which
	// column their new work should land in.
	First string `json:"first,omitempty"`
}

// droppedDefaultStatuses reports the statuses this project defines that Workbook
// has stopped shipping, with the command that removes each one.
//
// The test is provenance rather than membership, because the vocabulary alone
// cannot tell the `blocked` a project never migrated away from apart from one
// somebody added back on purpose — and telling the second reader about a
// decision they already made, on every listing, forever, with nothing to
// suppress it, is nagging rather than explaining. The ledger can tell them
// apart: a `status.add` after the genesis is recorded history, and a `blocked`
// the genesis itself carried is an inheritance. See blockedTracesToADecision
// for what the walk counts and what it does when the answer is out of reach.
//
// Provenance is answered first, ahead of every shape the note can take. A
// project that inherited the status and one that chose it are different readers
// before they are different configurations, and the ordering costs nothing: the
// membership gate above already keeps the walk off every listing that does not
// define `blocked` at all, so answering provenance first adds it to no listing
// that would otherwise have skipped it.
//
// A project whose new tasks land in the dropped status gets the same note with
// a different next step rather than no note at all. `status delete` refuses to
// forward a status into itself and refuses to leave a project with nowhere for
// new work to land, so the removal is not a command that exists yet for them —
// but they are the readers with the most invested in the column and the least
// reason to be told nothing. Naming the tag handoff is the whole difference
// between "you have a status Workbook no longer ships" and silence.
//
// That is the one shape with no escape — no removal to run, no window to fall
// out of — which is exactly why it sits below the provenance test rather than
// above it. A project that added the column and made it the place new work
// lands built a workflow deliberately, and advice on dismantling it is not
// something to repeat on every listing forever; `status delete` explains the
// handoff at the moment somebody actually tries the removal.
func droppedDefaultStatuses(vocabulary core.Vocabulary, ledger configLedgerWindow) []statusMigrationView {
	if !vocabulary.Has(core.StatusBlocked) {
		return nil
	}
	if blockedTracesToADecision(ledger) {
		return nil
	}
	reason := "task dependencies record what a task is waiting on, so `blocked` is no longer " +
		"one of the statuses Workbook gives a new project"
	if vocabulary.Default() == core.StatusBlocked {
		return []statusMigrationView{{
			Status: core.StatusBlocked,
			Reason: reason + ", and this project's new tasks land in it",
			// Written out rather than built by statusCommand: `<status>` is a
			// placeholder for a person to replace, and quoting it the way a real
			// argument is quoted would make it read as a status called
			// "<status>". This is the same spelling the arity refusals use.
			First: "workbook status tag <status> --tag " + string(core.StatusTagDefault),
		}}
	}
	return []statusMigrationView{{
		Status: core.StatusBlocked,
		Reason: reason,
		Command: statusCommand("delete", string(core.StatusBlocked),
			"--into", string(vocabulary.Default())),
	}}
}

// blockedTracesToADecision reports whether the `blocked` this project defines
// was put there by somebody rather than inherited.
//
// The walk is over the whole read window in order, keeping the last operation
// that established the name, because a status can be established and retired
// more than once: a project that removed `blocked` and later added it back
// decided twice, and the second decision is the one the reader is living with.
// The arms that reset are restatements of the caller's invariant rather than
// answers this reaches; see the note on the rename case.
//
// An answer this cannot reach is inherited. The window is bounded (see
// maxDatedConfigCommits), so an add older than it looks exactly like no add at
// all, and the two directions cost differently: showing the note to somebody who
// has already decided costs them a sentence they can act on once, while hiding
// it from a project that never migrated withholds the only place the migration
// is explained. So the conservative direction is to nag, and absence of evidence
// is never read as evidence of a decision. A project with no ledger — unseeded,
// on the legacy fallback — reaches this with an empty window and gets the note
// for the same reason.
func blockedTracesToADecision(ledger configLedgerWindow) bool {
	decided := false
	for _, commit := range ledger.Commits {
		for _, operation := range commit.Pack.Operations {
			switch operation.Type {
			case core.ConfigGenesis:
				decided = false
			case core.ConfigStatusAdd:
				if operation.Name == core.StatusBlocked {
					decided = true
				}
			case core.ConfigStatusRename:
				// Renaming a column onto the name is as deliberate as adding it.
				//
				// The three arms that reset — a genesis, a rename away, a removal —
				// are defensive restatements of an invariant the caller's membership
				// gate already enforces rather than decisions made here. This runs
				// only for a project whose `blocked` is live, and a live one is
				// always established by the newest add or rename onto the name that
				// the window holds, so nothing after such an operation can reset it
				// and no reset can survive to be returned. They are written out
				// because the invariant lives in another function and a walk that
				// silently depended on it would be wrong the day it moved — but the
				// provenance table does not cover them, because no input can reach
				// them and change the answer.
				switch core.StatusBlocked {
				case operation.To:
					decided = true
				case operation.From:
					decided = false
				}
			case core.ConfigStatusRemove:
				if operation.Status == core.StatusBlocked {
					decided = false
				}
			}
		}
	}
	return decided
}

type retiredStatusView struct {
	Status  core.Status `json:"status"`
	Becomes core.Status `json:"becomes"`
	// Operation is `status.rename` or `status.remove`, which is the difference
	// between "this column is called something else now" and "this column is
	// gone".
	Operation core.ConfigOperationType `json:"operation"`
	At        string                   `json:"at,omitempty"`
}

// unresolvedStatusView is one stored value nothing in this project resolves,
// with the work standing behind it.
//
// Tasks is the whole count and TaskIDs is a bounded sample of it, so the two
// disagree exactly when the sample was cut short — which is the signal a reader
// of the JSON gets that there are more. See maxUnresolvedTaskIDs for why a
// listing samples rather than enumerates.
type unresolvedStatusView struct {
	Status  core.Status `json:"status"`
	Tasks   int         `json:"tasks"`
	TaskIDs []string    `json:"taskIds"`
}

// maxUnresolvedTaskIDs bounds the task IDs one unresolved status contributes to
// the listing.
//
// The IDs are here so a person can act — each one is an argument to the command
// printed beneath them — and a status that stranded a whole project's backlog
// would otherwise turn a status table into a task dump nobody can read past.
// The count beside them is never sampled, so "how much work is stranded" stays
// exact while "which tasks" becomes a place to start. `status log` bounds its
// window the same way and for the same reason, and both say what they showed
// out of what there was.
const maxUnresolvedTaskIDs = 10

// statusLogResult mirrors the change log `workbook show --history` renders,
// because it answers the same question about a different history.
type statusLogResult struct {
	Showing   int                     `json:"showing"`
	Total     int                     `json:"total"`
	Entries   []statusLogEntry        `json:"entries"`
	Truncated *core.HistoryTruncation `json:"truncated,omitempty"`
}

type statusLogEntry struct {
	Commit      string    `json:"commit"`
	OperationID string    `json:"operationId"`
	WallTime    time.Time `json:"wallTime"`
	Actor       string    `json:"actor"`
	// Operation is the durable operation type, because this is the ledger's own
	// record rather than a report of a command somebody ran.
	Operation core.ConfigOperationType `json:"operation"`
	Summary   string                   `json:"summary"`
	// Collapsed counts the operations this commit recorded beyond the one the
	// entry names. `workbook status tag` records the transfer of the default tag
	// and every tag it dropped in a single commit, and the summary says how many
	// more there were rather than listing them — which left the count reachable
	// only by parsing an English sentence. It is stated rather than omitted, so
	// a caller reading `entries[].collapsed` gets an answer from every entry.
	Collapsed int            `json:"collapsed"`
	Inverse   *statusInverse `json:"inverse,omitempty"`
}

// statusSubcommands lists the verbs, for the error a bare `workbook status`
// produces. It is derived from the help schema so a verb cannot be added
// without the refusal learning about it.
func statusSubcommands() []string {
	return commandSchemas["status"].SubcommandOrder
}

func runStatus(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := firstStatusArgument(args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "list":
		return runStatusList(ctx, args, cwd, stdout, stderr)
	case "add":
		return runStatusAdd(ctx, args, cwd, stdout, stderr)
	case "rename":
		return runStatusRename(ctx, args, cwd, stdout, stderr)
	case "label":
		return runStatusLabel(ctx, args, cwd, stdout, stderr)
	case "move":
		return runStatusMove(ctx, args, cwd, stdout, stderr)
	case "tag":
		return runStatusTag(ctx, args, cwd, stdout, stderr)
	case "untag":
		return runStatusUntag(ctx, args, cwd, stdout, stderr)
	case "delete":
		return runStatusDelete(ctx, args, cwd, stdout, stderr)
	case "log":
		return runStatusLog(ctx, args, cwd, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown status command %q; %s",
			subcommand, statusCommandList())
	}
}

// firstStatusArgument takes the subcommand, and answers the one mistake worth
// answering specifically.
//
// `workbook status WB-01J...` is not a typo, it is a different command: a
// caller — usually an agent — reaching for a task's status and finding a verb
// family. Naming `workbook show` costs a clause and turns a dead end into the
// command they wanted.
func firstStatusArgument(args []string) (string, []string, error) {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation, "status takes a subcommand; %s", statusCommandList())
	}
	if _, known := commandMetadataFor([]string{"status", args[0]}); !known && looksLikeTaskReference(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation,
			"workbook status takes a subcommand; to read a task use: workbook show %s", args[0])
	}
	return args[0], args[1:], nil
}

func statusCommandList() string {
	return "the subcommands are " + strings.Join(statusSubcommands(), ", ")
}

// taskReferenceShape matches a task ID or the prefix of one: a project key, a
// hyphen, and enough of a ULID to be worth resolving. The alphabet is
// Crockford's, so the letters ULIDs never contain are not in it.
var taskReferenceShape = regexp.MustCompile(`(?i)^[a-z][a-z0-9]{1,9}-[0-9a-hjkmnp-tv-z]{4,26}$`)

// looksLikeTaskReference reports whether a word is far likelier to be a task
// than a mistyped subcommand.
//
// Both answers produce the same refusal and the same exit code, so a false
// positive costs one sentence of advice. It still asks for two signals rather
// than one, because a status name and a task ID prefix can be spelled alike:
// the shape above, plus either an uppercase letter — which a status token may
// never contain — or a body that begins with a digit, which is what a ULID's
// leading timestamp makes overwhelmingly common and a status name rare.
func looksLikeTaskReference(argument string) bool {
	if !taskReferenceShape.MatchString(argument) {
		return false
	}
	if strings.ToLower(argument) != argument {
		return true
	}
	_, body, _ := strings.Cut(argument, "-")
	return body != "" && body[0] >= '0' && body[0] <= '9'
}

// runStatusList reads the project's statuses without touching the network.
//
// It fetches nothing on purpose. Listing statuses is a read, and a read that
// synchronized would make `workbook status list` slower and less predictable
// than `workbook list` for no gain: the ledger this clone holds is what every
// other command in this shell is already using.
func runStatusList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("status", "list")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	service, err := statusReadService(ctx, repository, config, state.Vocabulary)
	if err != nil {
		return err
	}
	tasks, err := service.List(ctx, core.ListFilter{})
	if err != nil {
		return err
	}
	counts, unresolved := statusTaskCensus(state.Vocabulary, tasks)

	// Two of this listing's answers are read off the ledger — when a value was
	// retired, and whether the `blocked` a project still defines was chosen —
	// so it is read once, ahead of both. A project that has none skips the read
	// entirely: it has nothing retired to date, and nothing recorded to have
	// decided anything.
	var ledger configLedgerWindow
	if state.Seeded {
		ledger, err = readConfigLedgerWindow(ctx, repository, config, maxDatedConfigCommits)
		if err != nil {
			return err
		}
	}

	document := state.Vocabulary.Document()
	result := statusListResult{
		Head:       state.Head,
		Seeded:     state.Seeded,
		Default:    state.Vocabulary.Default(),
		Statuses:   statusViews(state.Vocabulary, counts),
		Retired:    retiredStatusViews(document, forwardingTimes(ledger)),
		Unresolved: unresolved,
		Advisories: historyvalidation.StatusCeilingAdvisories(document),
		Migrations: droppedDefaultStatuses(state.Vocabulary, ledger),
	}

	if *jsonMode {
		writeResult(stdout, "status list", result)
		return nil
	}
	return writeStatusList(stdout, result)
}

// maxDatedConfigCommits bounds how far back `status list` reads its ledger.
//
// The date on a retirement is a courtesy — it turns "it was renamed" into
// something a person can place among their own weeks — and a courtesy must not
// make a listing cost the whole history of a project that has been configured
// for a year. Reading the newest commits answers it for every recent change,
// which is the only kind anybody is confused about; a retirement older than this
// reports no date rather than a wrong one, and the listing says nothing where
// the clause would have been.
//
// The same window bounds the one other question this listing asks of the
// ledger, deliberately rather than incidentally: it is one read, so a second
// bound would be a second read for no gain, and both questions fall back the
// same way — an answer older than the window is reported as no answer rather
// than as a wrong one. See blockedTracesToADecision for which direction that
// makes the migration note fall.
const maxDatedConfigCommits = 64

// statusReadService builds a read-only service on a repository that is already
// open, so a status command holds one projection handle rather than two.
func statusReadService(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	vocabulary core.Vocabulary,
) (core.Service, error) {
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	return core.Service{
		Config:     config,
		Vocabulary: vocabulary,
		Reader:     store,
		History:    store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
	}, nil
}

// statusTaskCensus counts the active tasks each status holds, and collects the
// stored values that resolve to no status at all.
//
// The unresolved bucket is the reason this is one pass rather than a filter per
// status. A task whose stored status resolves nowhere is invisible to every
// count — it is in no column — and is exactly the task somebody has to act on,
// so the census that produces the counts is also what finds it.
func statusTaskCensus(vocabulary core.Vocabulary, tasks []core.Task) (map[core.Status]int, []unresolvedStatusView) {
	counts := make(map[core.Status]int, len(vocabulary.Definitions()))
	unresolved := make(map[core.Status][]string)
	for _, task := range tasks {
		if vocabulary.Has(task.Status) {
			counts[task.Status]++
			continue
		}
		unresolved[task.Status] = append(unresolved[task.Status], task.ID)
	}
	names := make([]core.Status, 0, len(unresolved))
	for status := range unresolved {
		names = append(names, status)
	}
	sort.Slice(names, func(left, right int) bool { return names[left] < names[right] })
	views := make([]unresolvedStatusView, 0, len(names))
	for _, status := range names {
		ids := unresolved[status]
		sort.Strings(ids)
		// The count is of everything; the IDs are the first few of it. Sorting
		// before the cut is what makes the sample the same sample on every
		// clone rather than whatever order the projection happened to list.
		total := len(ids)
		views = append(views, unresolvedStatusView{
			Status:  status,
			Tasks:   total,
			TaskIDs: ids[:min(total, maxUnresolvedTaskIDs)],
		})
	}
	return counts, views
}

func statusViews(vocabulary core.Vocabulary, counts map[core.Status]int) []statusView {
	definitions := vocabulary.Definitions()
	views := make([]statusView, 0, len(definitions))
	for index, definition := range definitions {
		view := statusView{
			Status: definition.Status,
			Label:  definition.Label,
			Tags:   definition.Tags,
			Order:  index + 1,
		}
		if counts != nil {
			count := counts[definition.Status]
			view.Tasks = &count
		}
		views = append(views, view)
	}
	return views
}

func retiredStatusViews(document core.VocabularyDocument, at map[core.Status]time.Time) []retiredStatusView {
	views := make([]retiredStatusView, 0, len(document.Aliases)+len(document.Retired))
	for _, alias := range document.Aliases {
		views = append(views, retiredStatusView{
			Status: alias.From, Becomes: alias.To, Operation: core.ConfigStatusRename,
			At: forwardingTimestamp(at, alias.From),
		})
	}
	for _, entry := range document.Retired {
		views = append(views, retiredStatusView{
			Status: entry.Status, Becomes: entry.Destination, Operation: core.ConfigStatusRemove,
			At: forwardingTimestamp(at, entry.Status),
		})
	}
	sort.Slice(views, func(left, right int) bool { return views[left].Status < views[right].Status })
	if len(views) == 0 {
		return nil
	}
	return views
}

func forwardingTimestamp(at map[core.Status]time.Time, status core.Status) string {
	when, found := at[status]
	if !found {
		return ""
	}
	return when.UTC().Format(time.RFC3339)
}

func runStatusLog(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("status", "log")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// The window is decided before anything is read, because it is what bounds
	// the read. --limit is checked for having been given at all rather than for
	// being non-empty: `--limit=` sets it to the empty string, which used to
	// slip past the conflict check and let --all win silently.
	window := core.DefaultChangeLimit
	limited := false
	var offending error
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == "limit" {
			limited = true
		}
	})
	if limited {
		if *all {
			return core.Errorf(core.CategoryInvocation, "cannot use --limit with --all")
		}
		parsed, err := strconv.Atoi(*limit)
		if err != nil || parsed < 1 {
			return core.Errorf(core.CategoryInvocation, "status log --limit must be a positive whole number")
		}
		window = parsed
	}
	if offending != nil {
		return offending
	}
	if *all {
		window = 0
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	ledger, err := readConfigLedgerWindow(ctx, repository, config, window)
	if err != nil {
		return err
	}
	result := buildStatusLog(ledger)
	if *jsonMode {
		writeResult(stdout, "status log", result)
		return nil
	}
	writeStatusLog(stdout, result, ledger.Found)
	return nil
}

// configLedgerCommit is one commit of the configuration ledger with the
// vocabulary its parent held, which is what every inverse is computed against.
type configLedgerCommit struct {
	Commit string
	Pack   core.ConfigOperationPack
	Before core.Vocabulary
}

// configLedgerWindow is what one bounded read of the ledger saw: the commits it
// delivered, and how much history it did not.
type configLedgerWindow struct {
	// Found reports that the project has a ledger at all.
	Found bool
	// Commits are the delivered commits, oldest first, each carrying the
	// vocabulary its parent held.
	Commits []configLedgerCommit
	// Total is the ledger's whole length in commits, however few were read.
	Total      int
	Truncation *core.HistoryTruncation
}

// readConfigLedgerWindow reads the newest commits of the configuration ledger,
// oldest of them first, and reports how long the whole ledger is.
//
// window bounds the commits delivered; zero or less reads everything. One extra
// commit is always requested and then dropped, because an inverse is read
// against the vocabulary its commit found, and for the oldest commit in a window
// that vocabulary is the one the commit before it produced. Bounding matters
// because the per-commit cost is two documents decoded and re-encoded to compare
// canonical bytes, so an unbounded read makes a ten-line log cost the whole
// history of a project that has been configured for a year.
//
// A project with no ledger returns nothing and no error: the ledger is seeded
// lazily, so its absence is the ordinary state and not something to report as a
// failure. A ledger that stops validating partway returns the prefix that did
// validate plus the truncation, exactly as a task history does — the recorded
// changes before the bad commit are still the project's history, and refusing to
// show them would withhold the only context somebody has for repairing it.
func readConfigLedgerWindow(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	window int,
) (configLedgerWindow, error) {
	requested := 0
	if window > 0 {
		requested = window + 1
	}
	result := configLedgerWindow{}
	var previous core.Vocabulary
	found, err := repository.ReadConfigHistoryTail(ctx, config, requested, gitstore.ConfigHistoryStream{
		Begin: func(start gitstore.ConfigHistoryStart) error {
			result.Total = start.Commits
			return nil
		},
		Commit: func(commit gitstore.ConfigHistoryCommit) error {
			result.Commits = append(result.Commits, configLedgerCommit{
				Commit: commit.ObjectID,
				Pack:   commit.Operation,
				Before: previous,
			})
			previous = commit.State.Vocabulary()
			return nil
		},
		End: func(outcome gitstore.ConfigHistoryResult) error {
			if outcome.Failure != nil {
				result.Truncation = &core.HistoryTruncation{
					Commit:  outcome.Failure.Commit,
					Message: outcome.Failure.Err.Error(),
				}
			}
			return nil
		},
	})
	if err != nil {
		return configLedgerWindow{}, err
	}
	result.Found = found
	// The extra commit was read for its checkpoint alone, and the window is
	// applied here rather than being assumed from what came back: a ledger
	// shorter than the window returns every commit it has, and trimming to the
	// window is what keeps `--limit 1` showing one change whether the project
	// has two commits or two hundred.
	if window > 0 && len(result.Commits) > window {
		result.Commits = result.Commits[len(result.Commits)-window:]
	}
	return result, nil
}

// forwardingTimes records when each retired value stopped being live.
//
// The last recorded forwarding wins, because a value can be retired more than
// once: adding a name back deletes its forwarding pointer, and retiring it
// again writes a new one.
func forwardingTimes(ledger configLedgerWindow) map[core.Status]time.Time {
	times := make(map[core.Status]time.Time)
	for _, commit := range ledger.Commits {
		for _, operation := range commit.Pack.Operations {
			switch operation.Type {
			case core.ConfigStatusRename:
				times[operation.From] = commit.Pack.WallTime
			case core.ConfigStatusRemove:
				times[operation.Status] = commit.Pack.WallTime
			}
		}
	}
	return times
}

// buildStatusLog renders a read window as the change log.
//
// One entry is one commit, which is the same unit `show --history` counts: a
// Change there is one operation pack, whatever the pack contains. That matters
// here beyond symmetry, because a commit is also one command — `workbook status
// tag` records a transfer and the tags it dropped in one — and the inverse worth
// printing is the one that undoes the command somebody ran rather than a
// fragment of it. It is also what makes the total exact under a bounded read:
// counting commits costs the walk, counting operations would cost reading every
// pack.
func buildStatusLog(ledger configLedgerWindow) statusLogResult {
	entries := make([]statusLogEntry, 0, len(ledger.Commits))
	for _, commit := range ledger.Commits {
		if len(commit.Pack.Operations) == 0 {
			continue
		}
		primary := commit.Pack.Operations[0]
		entries = append(entries, statusLogEntry{
			Commit:      commit.Commit,
			OperationID: primary.ID,
			WallTime:    commit.Pack.WallTime,
			Actor:       commit.Pack.Actor.ID,
			Operation:   primary.Type,
			Summary:     configPackSummary(commit.Pack.Operations),
			Collapsed:   len(commit.Pack.Operations) - 1,
			Inverse:     statusPackInverse(commit.Before, commit.Pack.Operations),
		})
	}
	return statusLogResult{
		Total:     ledger.Total,
		Showing:   len(entries),
		Entries:   entries,
		Truncated: ledger.Truncation,
	}
}

func runStatusAdd(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status add", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "add")
	label := flags.String("label", "", "display label")
	before := flags.String("before", "", "place before this status")
	after := flags.String("after", "", "place after this status")
	var tags stringListValue
	flags.Var(&tags, "tag", "role to give it")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *before != "" && *after != "" {
		return core.Errorf(core.CategoryInvocation, "status add accepts --before or --after, not both")
	}

	status := core.Status(values[0])
	if err := core.ValidateStatusToken(status); err != nil {
		return err
	}
	wanted, err := parseStatusTags(tags.values)
	if err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidateStatusLabel(*label); err != nil {
			return err
		}
	}

	return runStatusMutation(ctx, cwd, "status add", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusAdd(ctx, session.statusScope(), vocabulary, statusAddition{
				Status: status,
				Label:  *label,
				Tags:   wanted,
				Before: core.Status(*before),
				After:  core.Status(*after),
			})
		})
}

func runStatusRename(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status rename", []string{"<status>", "<new-status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "rename")
	label := flags.String("label", "", "display label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	from, to := core.Status(values[0]), core.Status(values[1])
	if err := core.ValidateStatusToken(to); err != nil {
		return err
	}
	if *label != "" {
		if err := core.ValidateStatusLabel(*label); err != nil {
			return err
		}
	}

	return runStatusMutation(ctx, cwd, "status rename", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusRename(ctx, session.statusScope(), vocabulary, from, to, *label)
		})
}

func runStatusLabel(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status label", []string{"<status>", "<display-label>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	display := values[1]
	if err := core.ValidateStatusLabel(display); err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status label", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusRelabel(ctx, session.statusScope(), vocabulary, core.Status(values[0]), display)
		})
}

func runStatusMove(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status move", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "move")
	before := flags.String("before", "", "move before this status")
	after := flags.String("after", "", "move after this status")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "status move requires exactly one of --before or --after")
	}

	return runStatusMutation(ctx, cwd, "status move", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			placeBefore := *before != ""
			anchor := core.Status(*after)
			if placeBefore {
				anchor = core.Status(*before)
			}
			return planStatusMove(ctx, session.statusScope(), vocabulary,
				core.Status(values[0]), anchor, placeBefore)
		})
}

func runStatusTag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status tag", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "tag")
	var tags stringListValue
	flags.Var(&tags, "tag", "role to give it")
	clear := flags.Bool("clear-tags", false, "replace its roles with an empty set")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if tags.set && *clear {
		return core.Errorf(core.CategoryInvocation, "cannot use --tag with --clear-tags")
	}
	if !tags.set && !*clear {
		return core.Errorf(core.CategoryInvocation, "status tag requires --tag or --clear-tags")
	}
	wanted, err := parseStatusTags(tags.values)
	if err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status tag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusTagSet(ctx, session.statusScope(), vocabulary, core.Status(values[0]), wanted)
		})
}

func runStatusUntag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status untag", []string{"<status>", "<tag>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "untag")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	tag := core.StatusTag(values[1])
	if err := core.ValidateStatusTag(tag); err != nil {
		return statusTagError(err)
	}

	return runStatusMutation(ctx, cwd, "status untag", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusUntag(ctx, session.statusScope(), vocabulary, core.Status(values[0]), tag)
		})
}

func runStatusDelete(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status delete", []string{"<status>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "delete")
	into := flags.String("into", "", "where the removed status's tasks belong")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// --into is required and never guessed, and the refusal names the statuses
	// it could have been. Prompting is not an option: agents run this command,
	// and a prompt would hang one.
	//
	// It is refused before the session opens rather than inside the change,
	// because an invocation nobody could have meant should not first fetch from
	// origin. Naming the statuses costs a local read of the ledger, which is
	// what the reading verbs pay anyway.
	if *into == "" {
		return missingRemovalDestination(ctx, cwd, stderr)
	}

	return runStatusMutation(ctx, cwd, "status delete", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			return planStatusDelete(ctx, session.statusScope(), vocabulary,
				core.Status(values[0]), core.Status(*into))
		})
}

// missingRemovalDestination refuses `status delete` with no --into, naming the
// statuses this project has so the retry is one edit away.
func missingRemovalDestination(ctx context.Context, cwd string, stderr io.Writer) error {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	return core.Errorf(core.CategoryInvocation,
		"status delete requires --into <status>, naming where the removed status's tasks belong; "+
			"this project's statuses are: %s", statusNameList(state.Vocabulary))
}

// removalTaskCounts prices a removal in the terms the person running it cares
// about: how many tasks move, and how many of those become claimable where they
// land.
//
// Both are counted before the write, against the vocabulary the removal is
// about to change, because afterwards the tasks resolve into the destination and
// the question can no longer be asked.
//
// Claimable means what `workbook next` means, through the function Next itself
// admits a task by: every dependency is an active task in a status that
// satisfies one. Counting only the tasks with no dependencies at all was a
// different, smaller number — it called a task blocked when the work it waited
// for was finished weeks ago, and reported a queue growing by one where the
// agent would find two.
//
// Every dependency is read as it will resolve after the removal, because a
// dependency sitting in the status being removed lands in the destination with
// everything else, and whether that satisfies anything is exactly what the
// destination's tags decide.
func removalTaskCounts(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	subject, destination core.Status,
) (statusTaskCounts, error) {
	tasks, err := scope.service.List(ctx, core.ListFilter{})
	if err != nil {
		return statusTaskCounts{}, err
	}
	active := make(map[string]core.TaskData, len(tasks))
	for _, task := range tasks {
		data := task.TaskData
		if data.Status == subject {
			data.Status = destination
		}
		active[task.ID] = data
	}

	counts := statusTaskCounts{}
	claimable := vocabulary.IsNext(destination)
	for _, task := range tasks {
		if task.Status != subject {
			continue
		}
		counts.Affected++
		if claimable && core.DependenciesDone(vocabulary, task.Dependencies, active) {
			counts.ClaimableAfter++
		}
	}
	return counts, nil
}

// statusPlan is one authored status change: the operations to record, and
// everything the envelope says about them that the operations alone do not.
//
// It carries nothing for the inverse. Everything an inverse needs is in the
// operations and in the vocabulary they were authored against, which is what
// lets the log — which has only those two things — reach the same answer.
type statusPlan struct {
	operations []core.ConfigOperation
	change     statusChange
	tasks      statusTaskCounts
}

// statusScope is what authoring a status change needs from the project besides
// the vocabulary it is authored against: the ledger, to date a value that is no
// longer live, and the project's tasks, to price a removal.
//
// It is a parameter rather than a session because the verbs are no longer the
// only surface that authors these changes. `workbook status` fills it from the
// session it already opened; the board fills it from the repository `serve` is
// already holding. Both then call the same planners, which is what makes the
// web routes a second surface over these operations rather than a second
// implementation of them — one set of refusals, in one voice.
type statusScope struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	// service reads the project's tasks. Only a removal needs it, and only to
	// count what it moves.
	service core.Service
}

func (session *taskSession) statusScope() statusScope {
	return statusScope{repository: session.repository, config: session.config, service: session.service}
}

// planStatusAdd defines a status this project does not have, optionally next to
// one it does.
func planStatusAdd(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	addition statusAddition,
) (statusPlan, error) {
	if err := core.ValidateStatusToken(addition.Status); err != nil {
		return statusPlan{}, err
	}
	if vocabulary.Has(addition.Status) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines status %q", addition.Status)
	}
	display := addition.Label
	if display == "" {
		display = core.DerivedStatusLabel(addition.Status)
	}
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}

	rank := vocabulary.AppendRank()
	anchor, placeBefore := addition.Before, addition.Before != ""
	if addition.After != "" {
		anchor = addition.After
	}
	position := &statusPosition{}
	if anchor != "" {
		resolved, err := requireLiveStatus(ctx, scope, vocabulary, anchor)
		if err != nil {
			return statusPlan{}, err
		}
		anchor = resolved
		rank, err = vocabulary.InsertRank("", anchor, placeBefore)
		if err != nil {
			return statusPlan{}, err
		}
		if placeBefore {
			position.Before = anchor
		} else {
			position.After = anchor
		}
	}
	position.Rank = rank

	operation := core.ConfigOperation{
		Type:  core.ConfigStatusAdd,
		Name:  addition.Status,
		Label: display,
		Rank:  rank,
		Tags:  addition.Tags,
	}
	change := statusChange{
		Operation: "add",
		Status:    addition.Status,
		Position:  position,
		Label:     &statusLabel{To: display},
		Tags:      addition.Tags,
	}
	if containsStatusTag(addition.Tags, core.StatusTagDefault) {
		change.DefaultFrom = vocabulary.Default()
	}
	return statusPlan{operations: []core.ConfigOperation{operation}, change: change}, nil
}

// statusAddition is one status somebody is defining, however they said it: the
// verb's arguments and flags, or the board's JSON body.
type statusAddition struct {
	Status core.Status
	// Label empty derives one from the token.
	Label  string
	Tags   []core.StatusTag
	Before core.Status
	After  core.Status
}

// planStatusRename moves a status onto a new token, keeping a label somebody
// chose and re-deriving one nobody did.
//
// label is what the caller asked for and empty means they asked for nothing,
// which is the case the derived-label rule is about.
func planStatusRename(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	from, to core.Status,
	label string,
) (statusPlan, error) {
	if err := core.ValidateStatusToken(to); err != nil {
		return statusPlan{}, err
	}
	subject, err := requireLiveStatus(ctx, scope, vocabulary, from)
	if err != nil {
		return statusPlan{}, err
	}
	if subject == to {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already has that value", to)
	}
	if vocabulary.Has(to) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"this project already defines status %q", to)
	}

	// The derived-label rule. A label nobody chose follows the name it was
	// derived from; a label somebody chose is theirs and survives a rename of
	// the machine value underneath it.
	current := vocabulary.Label(subject)
	display, derived := current, false
	switch {
	case label != "":
		display = label
	case current == core.DerivedStatusLabel(subject):
		display, derived = core.DerivedStatusLabel(to), true
	}
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}

	rename := core.ConfigOperation{Type: core.ConfigStatusRename, From: subject, To: to}
	operations := []core.ConfigOperation{rename}
	if display != current {
		operations = append(operations, core.ConfigOperation{
			Type: core.ConfigStatusRelabel, Status: to, Label: display,
		})
	}
	labelDerived := derived
	return statusPlan{
		operations: operations,
		change: statusChange{
			Operation:    "rename",
			Status:       to,
			From:         subject,
			Label:        &statusLabel{From: current, To: display},
			LabelDerived: &labelDerived,
			Tags:         statusTags(vocabulary, subject),
		},
	}, nil
}

// planStatusRelabel changes what a column is called without touching the value
// stored on its tasks.
func planStatusRelabel(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	display string,
) (statusPlan, error) {
	if err := core.ValidateStatusLabel(display); err != nil {
		return statusPlan{}, err
	}
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := vocabulary.Label(subject)
	if current == display {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already has that label", subject)
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusRelabel, Status: subject, Label: display}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "label",
			Status:    subject,
			Label:     &statusLabel{From: current, To: display},
			Tags:      statusTags(vocabulary, subject),
		},
	}, nil
}

// planStatusMove places one status next to another, leaving every other status
// where it is.
func planStatusMove(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status, anchor core.Status,
	placeBefore bool,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	resolvedAnchor, err := requireLiveStatus(ctx, scope, vocabulary, anchor)
	if err != nil {
		return statusPlan{}, err
	}
	if resolvedAnchor == subject {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"cannot move status %q relative to itself", subject)
	}
	rank, err := vocabulary.InsertRank(subject, resolvedAnchor, placeBefore)
	if err != nil {
		return statusPlan{}, err
	}
	position := &statusPosition{Rank: rank}
	if placeBefore {
		position.Before = resolvedAnchor
	} else {
		position.After = resolvedAnchor
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusReorder, Status: subject, Rank: rank}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "move",
			Status:    subject,
			Position:  position,
			Label:     &statusLabel{To: vocabulary.Label(subject)},
			Tags:      statusTags(vocabulary, subject),
		},
	}, nil
}

// planStatusTagSet replaces a status's whole tag set.
func planStatusTagSet(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	wanted []core.StatusTag,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := statusTags(vocabulary, subject)
	operations := tagSetOperations(subject, current, wanted)
	if len(operations) == 0 {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already carries exactly those tags", subject)
	}
	change := statusChange{Operation: "tag", Status: subject, Tags: wanted}
	if containsStatusTag(wanted, core.StatusTagDefault) && !containsStatusTag(current, core.StatusTagDefault) {
		change.DefaultFrom = vocabulary.Default()
	}
	return statusPlan{operations: operations, change: change}, nil
}

// planStatusUntag takes one role away from a status.
func planStatusUntag(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	tag core.StatusTag,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	current := statusTags(vocabulary, subject)
	if !containsStatusTag(current, tag) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q does not carry the %q tag", subject, tag)
	}
	remaining := make([]core.StatusTag, 0, len(current))
	for _, candidate := range current {
		if candidate != tag {
			remaining = append(remaining, candidate)
		}
	}
	operation := core.ConfigOperation{Type: core.ConfigStatusUntag, Status: subject, Tag: tag}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "untag",
			Status:    subject,
			Tags:      remaining,
		},
	}, nil
}

// planStatusDelete retires a status and forwards its tasks to a live one.
func planStatusDelete(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status, into core.Status,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	destination, err := requireLiveStatus(ctx, scope, vocabulary, into)
	if err != nil {
		return statusPlan{}, err
	}
	if destination == subject {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status delete cannot forward %q into itself; name where its tasks belong", subject)
	}
	definition, _ := statusDefinition(vocabulary, subject)
	counts, err := removalTaskCounts(ctx, scope, vocabulary, subject, destination)
	if err != nil {
		return statusPlan{}, err
	}
	operation := core.ConfigOperation{
		Type: core.ConfigStatusRemove, Status: subject, Destination: destination,
	}
	return statusPlan{
		operations: []core.ConfigOperation{operation},
		change: statusChange{
			Operation: "delete",
			Status:    subject,
			Into:      destination,
			Label:     &statusLabel{To: definition.Label},
			Tags:      definition.Tags,
		},
		tasks: counts,
	}, nil
}

// statusEdit is a change to one status's name, label and roles, in any subset.
// A nil member is one this change does not touch, which is the difference
// between leaving a label alone and blanking it.
type statusEdit struct {
	Name  *core.Status
	Label *string
	Tags  *[]core.StatusTag
}

// planStatusEdit is the board's status form: a rename, a relabel and a tag set
// as one change, because that is how somebody edits a column.
//
// It composes the verbs' own planners rather than authoring anything new, so
// the operations it records are the operations `workbook status rename`,
// `label` and `tag` record — a rename is still a rename followed by the relabel
// the derived-label rule asks for, which is the pack shape reconciliation knows
// how to classify.
//
// The tag operations name the status by the value it has after a rename in the
// same pack. A pack is ordered and atomic, so operation N+1 reads against
// operation N's effect; markPackSubjects is what makes that true for a token
// the pack itself created.
//
// The reported change is the one the commit subject names — a rename if this
// edit renames, otherwise a relabel, otherwise the tags — with the rest of the
// pack carried alongside it. That is the shape `workbook status tag` already
// writes, and `status log` reports the extra operations as collapsed changes.
func planStatusEdit(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
	edit statusEdit,
) (statusPlan, error) {
	subject, err := requireLiveStatus(ctx, scope, vocabulary, status)
	if err != nil {
		return statusPlan{}, err
	}
	members := 0
	for _, named := range []bool{edit.Name != nil, edit.Label != nil, edit.Tags != nil} {
		if named {
			members++
		}
	}
	if members == 0 {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q was given nothing to change", subject)
	}

	// A member that repeats what the status already says is not a mistake here,
	// which is where this parts company with the verbs. A form sends every field
	// it has, so "rename it to the name it has" is the client saying leave it
	// alone; typing the same thing into `workbook status rename` means something
	// else, and is still refused — as it is here when it is the whole change,
	// below, so a single-member request gets the verb's own answer either way.
	if edit.Name != nil && *edit.Name == subject && members == 1 {
		return planStatusRename(ctx, scope, vocabulary, subject, *edit.Name, "")
	}
	// A label somebody sent is a label they chose, blank included, so it is
	// validated before the rename sees it: planStatusRename reads an empty label
	// as "nothing was asked for" and derives one, which is right for a flag
	// nobody typed and wrong for a member somebody emptied.
	if edit.Label != nil {
		if err := core.ValidateStatusLabel(*edit.Label); err != nil {
			return statusPlan{}, err
		}
	}

	plan := statusPlan{}
	named := subject
	// refusal keeps what a half of this edit said about changing nothing, for
	// the request that turns out to change nothing at all.
	var refusal error
	switch {
	case edit.Name != nil && *edit.Name != subject:
		label := ""
		if edit.Label != nil {
			label = *edit.Label
		}
		renamed, err := planStatusRename(ctx, scope, vocabulary, subject, *edit.Name, label)
		if err != nil {
			return statusPlan{}, err
		}
		plan, named = renamed, *edit.Name
	case edit.Label != nil:
		// The label has already been validated, so the only thing left for this
		// to refuse is a label the status already has.
		relabelled, err := planStatusRelabel(ctx, scope, vocabulary, subject, *edit.Label)
		if err != nil {
			refusal = err
		} else {
			plan = relabelled
		}
	}

	if edit.Tags != nil {
		tagged, err := planStatusTagSet(ctx, scope, vocabulary, subject, *edit.Tags)
		if err != nil {
			refusal = err
		} else {
			for _, operation := range tagged.operations {
				operation.Status = named
				plan.operations = append(plan.operations, operation)
			}
			if plan.change.Operation == "" {
				plan.change = tagged.change
			} else {
				plan.change.Tags = tagged.change.Tags
				if tagged.change.DefaultFrom != "" {
					plan.change.DefaultFrom = tagged.change.DefaultFrom
				}
			}
		}
	}
	if len(plan.operations) == 0 {
		if members == 1 && refusal != nil {
			// One member, and it changed nothing: the verb's own refusal, in the
			// verb's own words.
			return statusPlan{}, refusal
		}
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"status %q already reads exactly that way", subject)
	}
	return plan, nil
}

// planStatusOrder sets the whole column order at once, which is what a drag
// across a board means and what no verb expresses: `status move` names a
// neighbour, and a client that had to translate a drop into a sequence of
// pairwise moves would be authoring a different change with every intermediate
// state visible to everybody else.
//
// The order must name every live status exactly once. A partial list is refused
// rather than interpreted, because there is no reading of "put these three
// first" that does not also decide something about the statuses it left out —
// and the client that sent it is a client whose columns are already out of step
// with the project's.
//
// Ranks are rewritten as whole numbers in the requested order, and only for the
// statuses whose rank is not already the one they need. That is deliberately
// literal: applyReorder records a rank rather than a relation, so two clones
// that dragged a column to the same place converge, and a status this order did
// not move keeps the rank a teammate's insertion gave it.
func planStatusOrder(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	wanted []core.Status,
) (statusPlan, error) {
	definitions := vocabulary.Definitions()
	seen := make(map[core.Status]struct{}, len(wanted))
	resolved := make([]core.Status, 0, len(wanted))
	for _, status := range wanted {
		live, err := requireLiveStatus(ctx, scope, vocabulary, status)
		if err != nil {
			return statusPlan{}, err
		}
		if _, repeated := seen[live]; repeated {
			return statusPlan{}, core.Errorf(core.CategoryValidation,
				"the order names status %q twice; name each of this project's statuses exactly once", live)
		}
		seen[live] = struct{}{}
		resolved = append(resolved, live)
	}
	if len(resolved) != len(definitions) {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"the order names %d of this project's %d statuses; name each of them exactly once: %s",
			len(resolved), len(definitions), statusNameList(vocabulary))
	}

	unchanged := true
	for index, status := range resolved {
		if definitions[index].Status != status {
			unchanged = false
			break
		}
	}
	if unchanged {
		return statusPlan{}, core.Errorf(core.CategoryValidation,
			"this project's statuses are already in that order")
	}

	operations := make([]core.ConfigOperation, 0, len(resolved))
	for index, status := range resolved {
		rank := strconv.Itoa(index+1) + "/1"
		if definition, live := statusDefinition(vocabulary, status); live && definition.Rank == rank {
			continue
		}
		operations = append(operations, core.ConfigOperation{
			Type: core.ConfigStatusReorder, Status: status, Rank: rank,
		})
	}
	return statusPlan{operations: operations, change: statusChange{Operation: "order"}}, nil
}

// runStatusMutation is the one path every status change takes.
//
// It is the task mutation path with a different write at the centre: the same
// session, so the same fetch-before, the same watcher deferral, the same
// --no-sync, and the same sync member; and the same publish-after, except that
// what it publishes is the configuration ledger, which has no task ref to name.
func runStatusMutation(
	ctx context.Context,
	cwd string,
	command string,
	noSync, noDocs bool,
	jsonMode bool,
	stdout, stderr io.Writer,
	build func(context.Context, *taskSession, core.Vocabulary) (statusPlan, error),
) error {
	session, err := openTaskSession(ctx, cwd, noSync, true, stderr)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
	// The vocabulary the fetch settled on is the one this change is authored
	// against, which is what makes `status rename` land on a teammate's newer
	// name rather than on the one this clone opened with.
	if err := session.refreshVocabulary(ctx); err != nil {
		return err
	}
	before := session.service.Vocabulary
	plan, err := build(ctx, session, before)
	if err != nil {
		return err
	}

	written, err := session.repository.WriteConfigOperation(
		ctx, session.config, core.CryptoULIDSource{}, plan.operations, configCommitSubject(plan))
	if err != nil {
		return statusWriteError(err)
	}
	session.publishConfig(ctx)

	after := written.Vocabulary()
	result := statusMutationResult{
		Change: plan.change,
		Vocabulary: vocabularyView{
			Head:     written.Head,
			Seeded:   true,
			Default:  after.Default(),
			Statuses: statusViews(after, nil),
		},
		Tasks:   plan.tasks,
		Inverse: statusChangeInverse(before, plan.operations),
	}
	if position := result.Change.Position; position != nil {
		position.Order = after.Order(plan.change.Status) + 1
	}
	docs, docsErr := regenerateGuidelines(session, after, noDocs)
	result.Docs = docs
	writeStatusMutation(stdout, stderr, command, result, session, docsErr, jsonMode)
	return nil
}

// regenerateGuidelines rewrites the generated guidelines against the statuses
// this change produced.
//
// The guidelines state a project's statuses, so every status change makes them
// stale, and a generated file that has to be refreshed by hand is a generated
// file that is wrong most of the time. It goes through the same Reconcile the
// documentation commands use, which is what keeps the one promise that matters
// about a generated file: Workbook rewrites what it wrote, and never overwrites
// what somebody edited.
//
// It returns its failure rather than raising it. The configuration change is
// already recorded and published by the time this runs, so a documentation
// refresh that could not finish is news to report beside a success, not a
// reason to exit non-zero on a durable write — the same trade `sync` makes.
func regenerateGuidelines(
	session *taskSession,
	vocabulary core.Vocabulary,
	noDocs bool,
) (*agentdocs.Report, error) {
	if noDocs {
		return nil, nil
	}
	report, err := agentdocs.ApplyGuidelines(agentdocs.Options{
		Root:       session.repository.Root,
		Project:    session.config,
		Vocabulary: vocabulary,
		Generator:  release.Version,
	})
	return &report, err
}

// docsWarning says what a status change could not do to the documentation it
// invalidated, and how to finish the job.
//
// A blocked artifact is named with the command that overwrites it, because the
// state it describes — a generated file somebody edited — is the one a person
// has to decide about. Anything else is reported as it arrived.
func docsWarning(report *agentdocs.Report, err error) []core.Warning {
	if report == nil || err == nil {
		return nil
	}
	if blocked := report.Blocked(); len(blocked) > 0 {
		names := make([]string, 0, len(blocked))
		for _, artifact := range blocked {
			names = append(names, artifact.Path)
		}
		return []core.Warning{{
			Code:    core.WarningDocsRefresh,
			Message: docsBlockedMessage(strings.Join(names, ", ")),
		}}
	}
	return []core.Warning{{
		Code: core.WarningDocsRefresh,
		Message: "the status change was recorded, but the guidelines could not be regenerated: " +
			publicErrorMessage(err),
	}}
}

// docsBlockedMessage names a generated file a status change invalidated and
// could not rewrite because somebody had edited it, with the command that
// overwrites it anyway.
//
// It is one sentence in one place because two surfaces reach it: the verbs,
// which tried to rewrite the file and were refused, and the board, which does
// not write files at all and finds the same file describing statuses this
// project no longer has.
func docsBlockedMessage(names string) string {
	return fmt.Sprintf(
		"the status change was recorded, but %s was modified locally and now describes "+
			"statuses this project no longer has; overwrite it with: workbook docs update --force",
		names,
	)
}

// configCommitSubject writes what the ledger's `git log` says about this
// change, in the same voice the seeded root uses.
func configCommitSubject(plan statusPlan) string {
	return "workbook: " + plan.change.summary()
}

// summary renders a change as one clause, for a commit subject and for the
// text-mode heading.
func (change statusChange) summary() string {
	switch change.Operation {
	case "add":
		return fmt.Sprintf("add status %s", change.Status)
	case "rename":
		return fmt.Sprintf("rename status %s to %s", change.From, change.Status)
	case "label":
		return fmt.Sprintf("relabel status %s", change.Status)
	case "move":
		return fmt.Sprintf("move status %s", change.Status)
	case "tag":
		return fmt.Sprintf("tag status %s", change.Status)
	case "untag":
		return fmt.Sprintf("untag status %s", change.Status)
	case "delete":
		return fmt.Sprintf("remove status %s into %s", change.Status, change.Into)
	case "order":
		// No status is named, because a reorder is about the arrangement rather
		// than about any one column: the board sends the whole order and the
		// pack records every status the arrangement moved.
		return "reorder statuses"
	default:
		return "update project configuration"
	}
}

// statusWriteError explains the two failures a configuration write has that a
// task write does not phrase the same way.
//
// A lost compare-and-swap is the one worth rewording: gitstore says the ledger
// changed concurrently, which is accurate and says nothing about what to do.
// The answer is always to run the same command again, and saying so is the
// difference between an error a script retries and one it reports.
func statusWriteError(err error) error {
	if core.CategoryOf(err) == core.CategoryStaleWrite {
		return core.Wrap(core.CategoryStaleWrite,
			"another process changed this project's statuses while this command was writing; nothing was recorded, so run it again",
			err)
	}
	return err
}

// requireLiveStatus resolves a status the caller named, and explains a value
// that is no longer live rather than reporting it missing.
//
// The chain is the whole point. A teammate renamed `ready` to `todo` last week;
// typing `ready` here is not a mistake worth a bare "not found", it is a value
// that used to be right and whose replacement this clone can name.
func requireLiveStatus(
	ctx context.Context,
	scope statusScope,
	vocabulary core.Vocabulary,
	status core.Status,
) (core.Status, error) {
	if err := core.ValidateStatusToken(status); err != nil {
		return "", err
	}
	if vocabulary.Has(status) {
		return status, nil
	}
	via, operation, forwarded := vocabulary.Forwarding(status)
	if !forwarded {
		return "", core.Errorf(core.CategoryNotFound,
			"no status %q in this project; the statuses are: %s", status, statusNameList(vocabulary))
	}
	resolved, _ := vocabulary.Resolve(status)
	return "", core.Errorf(core.CategoryNotFound, "no status %q; it was %s %q%s%s",
		status, forwardingVerb(operation), via,
		statusForwardedOn(ctx, scope, status), statusChainClause(via, resolved))
}

// statusChainClause says where a chain ends when that is not where its first
// hop went.
//
// The first hop is what happened to the value somebody typed, and it is the only
// hop this clone can name a verb for: Forwarding answers about one hop by
// contract. Pairing that verb with the chain's final destination produced
// sentences describing a change nobody made — "it was renamed to backlog", for a
// value that was renamed to `sorting` and only reached `backlog` when somebody
// later removed that. So the hop keeps its verb, and the end of the chain gets
// its own clause.
func statusChainClause(via, resolved core.Status) string {
	if resolved == "" || resolved == via {
		return ""
	}
	return fmt.Sprintf(", which now resolves to %q", resolved)
}

// statusForwardedOn dates a forwarding from the ledger, and says nothing when
// it cannot. The date is what turns "it was renamed" into something a person
// can place among their own weeks, and reading the ledger for it is affordable
// here because this path has already failed.
func statusForwardedOn(ctx context.Context, scope statusScope, status core.Status) string {
	ledger, err := readConfigLedgerWindow(ctx, scope.repository, scope.config, maxDatedConfigCommits)
	if err != nil || !ledger.Found {
		return ""
	}
	when, dated := forwardingTimes(ledger)[status]
	if !dated {
		return ""
	}
	return " on " + when.UTC().Format("2006-01-02")
}

func statusNameList(vocabulary core.Vocabulary) string {
	definitions := vocabulary.Definitions()
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, string(definition.Status))
	}
	return strings.Join(names, ", ")
}

// parseStatusTags turns the repeated --tag values into a set, refusing an
// unknown one here rather than letting the operation document call it corrupt
// data.
//
// The result is in the canonical order a stored vocabulary keeps, not the order
// somebody typed, so that the set an envelope reports is the set the ledger
// holds. Two commands that name the same three tags differently are the same
// change and have to read as one.
func parseStatusTags(values []string) ([]core.StatusTag, error) {
	seen := make(map[core.StatusTag]struct{}, len(values))
	for _, value := range values {
		tag := core.StatusTag(value)
		if err := core.ValidateStatusTag(tag); err != nil {
			return nil, statusTagError(err)
		}
		seen[tag] = struct{}{}
	}
	tags := make([]core.StatusTag, 0, len(seen))
	for _, tag := range core.StatusTags() {
		if _, wanted := seen[tag]; wanted {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

// statusTagError names the three tags that exist, because a caller who typed a
// fourth has no other way to learn them without reading help.
func statusTagError(err error) error {
	names := make([]string, 0, 3)
	for _, tag := range core.StatusTags() {
		names = append(names, string(tag))
	}
	return core.Errorf(core.CategoryValidation, "%s; the tags are: %s",
		publicErrorMessage(err), strings.Join(names, ", "))
}

func containsStatusTag(tags []core.StatusTag, wanted core.StatusTag) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

// tagSetOperations turns a replacement set into the operations that reach it.
//
// Tagging is expressed as a set rather than as a difference because that is the
// idiom `workbook update --label` already established, and because a set is the
// only form whose inverse is a single command. The operations it produces are
// still individual tags and untags, which is what the durable format has: a
// clone that fetches them applies each one, and the default transfer inside one
// of them stays atomic.
func tagSetOperations(subject core.Status, current, wanted []core.StatusTag) []core.ConfigOperation {
	operations := make([]core.ConfigOperation, 0, len(current)+len(wanted))
	// Additions first, so that a set taking the default tag never passes
	// through a state with no default at all.
	for _, tag := range wanted {
		if !containsStatusTag(current, tag) {
			operations = append(operations, core.ConfigOperation{
				Type: core.ConfigStatusTag, Status: subject, Tag: tag,
			})
		}
	}
	for _, tag := range current {
		if !containsStatusTag(wanted, tag) {
			operations = append(operations, core.ConfigOperation{
				Type: core.ConfigStatusUntag, Status: subject, Tag: tag,
			})
		}
	}
	return operations
}

// statusChangeInverse is the verb path's inverse: the same computation the log
// performs over the same operations, with nothing left for a mutating command
// to add. A change whose inverse cannot be expressed reports an empty one rather
// than a command that would not run.
func statusChangeInverse(before core.Vocabulary, operations []core.ConfigOperation) statusInverse {
	if inverse := statusPackInverse(before, operations); inverse != nil {
		return *inverse
	}
	return statusInverse{}
}

// statusPackInverse is the command that undoes one recorded pack, and the only
// place an inverse is decided.
//
// It takes the whole pack rather than one operation because three of the
// answers depend on what else was recorded in the same commit: a rename that
// moved the label has to name the old label, a tag set that took the default
// has to give it back before it restores anything, and an add that took the
// default cannot simply be deleted. Deriving those from the pack means the verb
// that authored it and the log that reads it later reach the same answer by
// running the same code, rather than by two implementations agreeing.
//
// It answers about operations[0], which every status command puts its subject
// operation first, and which for the ledger's own packs is the change the
// commit is about.
func statusPackInverse(before core.Vocabulary, operations []core.ConfigOperation) *statusInverse {
	if len(operations) == 0 {
		return nil
	}
	operation := operations[0]
	subject := statusOperationSubject(operation)
	switch operation.Type {
	case core.ConfigStatusAdd:
		return addInverse(before, operation)
	case core.ConfigStatusRename:
		return renameInverse(before, operation, operations)
	case core.ConfigStatusRelabel:
		definition, live := statusDefinition(before, subject)
		if !live {
			return nil
		}
		return &statusInverse{
			Command: statusCommand("label", string(operation.Status), definition.Label),
			Exact:   true,
		}
	case core.ConfigStatusReorder:
		return reorderInverse(before, operation, subject, operations)
	case core.ConfigStatusTag, core.ConfigStatusUntag:
		return tagInverse(before, operation, operations)
	case core.ConfigStatusRemove:
		return removeInverse(before, operation, subject)
	default:
		return nil
	}
}

// renameInverse renames back, and names the label when the same commit moved
// it.
//
// Without that clause the inverse is a lie in both directions the label can
// move: a rename that re-derived `Ready` into `Next Up` would leave `Next Up`
// on a status called `ready`, and a rename given an explicit `--label` would
// leave that label behind. Naming it is also what makes the answer independent
// of the derive-or-keep rule running backwards.
func renameInverse(before core.Vocabulary, operation core.ConfigOperation, pack []core.ConfigOperation) *statusInverse {
	command := statusCommand("rename", string(operation.To), string(operation.From))
	if relabelled(pack, operation.To) {
		definition, live := statusDefinition(before, operation.From)
		if !live {
			return nil
		}
		command += " --label " + quoteStatusArgument(definition.Label)
	}
	// The board's status form can rename and retag in one commit, which no verb
	// does. Renaming back does not give the tags back, so the inverse says so
	// rather than claiming to restore a state it leaves half changed.
	if tagged := taggedIn(pack); tagged != "" {
		definition, live := statusDefinition(before, operation.From)
		if !live {
			return &statusInverse{Command: command}
		}
		return &statusInverse{
			Command: command,
			Note: fmt.Sprintf("this commit also changed %q's tags; %s restores them",
				operation.From, tagCommand(operation.From, definition.Tags)),
		}
	}
	return &statusInverse{Command: command, Exact: true}
}

// taggedIn names the status a pack tagged or untagged beside whatever else it
// did, or nothing when the pack changed no tags.
func taggedIn(pack []core.ConfigOperation) core.Status {
	for _, operation := range pack {
		if operation.Type == core.ConfigStatusTag || operation.Type == core.ConfigStatusUntag {
			return operation.Status
		}
	}
	return ""
}

// relabelled reports whether a pack also set the status's display label, which
// is how a rename learns that its inverse has to restore one.
func relabelled(pack []core.ConfigOperation, status core.Status) bool {
	for _, operation := range pack {
		if operation.Type == core.ConfigStatusRelabel && operation.Status == status {
			return true
		}
	}
	return false
}

// tagInverse restores the tag set a replacement discarded, and gives back the
// default tag first when the same commit took it.
//
// The default is what makes this more than a rewrite of the old set. Exactly one
// status carries it, so a commit that took it also took it from somebody:
// restoring only the subject's old set would leave the project with no default
// at all, which the authoring gate refuses outright — the inverse would exit 5.
// So the command names the status that gave the tag up, which is valid on its
// own, and the note carries the second command that finishes the job.
//
// The condition is a property of the whole pack rather than of this operation,
// because `workbook status tag` records the transfer and the tags it dropped as
// separate operations in one commit, and undoing either half alone has the same
// problem.
func tagInverse(before core.Vocabulary, operation core.ConfigOperation, pack []core.ConfigOperation) *statusInverse {
	subject := statusOperationSubject(operation)
	definition, live := statusDefinition(before, subject)
	if !live {
		return nil
	}
	restore := tagCommand(subject, definition.Tags)
	previous := defaultTagTakenFrom(before, pack)
	if previous == "" {
		return &statusInverse{Command: restore, Exact: true}
	}
	holder, live := statusDefinition(before, previous)
	if !live {
		return &statusInverse{Command: restore}
	}
	return &statusInverse{
		Command: tagCommand(previous, holder.Tags),
		Note: fmt.Sprintf("that returns the default tag to %q; %s restores %q's own tags",
			previous, restore, subject),
	}
}

// defaultTagTakenFrom names the status a pack took the default tag from, or
// nothing when the pack left it where it was.
func defaultTagTakenFrom(before core.Vocabulary, pack []core.ConfigOperation) core.Status {
	holder := before.Default()
	if holder == "" {
		return ""
	}
	for _, operation := range pack {
		taken := operation.Type == core.ConfigStatusTag && operation.Tag == core.StatusTagDefault
		if operation.Type == core.ConfigStatusAdd && containsStatusTag(operation.Tags, core.StatusTagDefault) {
			taken = true
		}
		if taken && statusOperationSubject(operation) != holder {
			return holder
		}
	}
	return ""
}

func tagCommand(subject core.Status, tags []core.StatusTag) string {
	if len(tags) == 0 {
		return statusCommand("tag", string(subject), "--clear-tags")
	}
	parts := []string{"tag", string(subject)}
	for _, tag := range tags {
		parts = append(parts, "--tag", string(tag))
	}
	return statusCommand(parts...)
}

// statusDefinition reads one status's definition, reporting whether the
// vocabulary defines it, so that no caller indexes a slice with Order's
// past-the-end answer for a status it does not.
func statusDefinition(vocabulary core.Vocabulary, status core.Status) (core.StatusDefinition, bool) {
	definitions := vocabulary.Definitions()
	index := vocabulary.Order(status)
	if index >= len(definitions) || definitions[index].Status != status {
		return core.StatusDefinition{}, false
	}
	return definitions[index], true
}

// statusTags reads a live status's tag set. Every caller has already resolved
// the status, so an absent one comes back with no tags rather than with a
// panic.
func statusTags(vocabulary core.Vocabulary, status core.Status) []core.StatusTag {
	definition, _ := statusDefinition(vocabulary, status)
	return definition.Tags
}

// statusOperationSubject names the status an operation is about, whichever
// member its type carries it in.
func statusOperationSubject(operation core.ConfigOperation) core.Status {
	switch operation.Type {
	case core.ConfigStatusAdd:
		return operation.Name
	case core.ConfigStatusRename:
		return operation.From
	default:
		return operation.Status
	}
}

// addInverse removes what an add defined, and names where the tasks that have
// since been created in it go.
//
// It is never exact, and the reason is worth saying rather than implying: the
// status is empty when it is added and need not be when it is removed, so the
// inverse moves tasks that the add never touched.
func addInverse(before core.Vocabulary, operation core.ConfigOperation) *statusInverse {
	destination := before.Default()
	definition, live := statusDefinition(before, destination)
	if !live {
		return nil
	}
	removal := statusCommand("delete", string(operation.Name), "--into", string(destination))
	if !containsStatusTag(operation.Tags, core.StatusTagDefault) {
		return &statusInverse{
			Command: removal,
			Note:    fmt.Sprintf("tasks created in %q since are forwarded to %q", operation.Name, destination),
		}
	}
	// The status holds the default tag, and removing the status that holds it
	// is refused outright. So the command is the transfer that makes the
	// removal possible, and the removal is the second step — in that order,
	// because printing a command that exits 5 would be worse than printing
	// nothing.
	return &statusInverse{
		Command: tagCommand(destination, definition.Tags),
		Note: fmt.Sprintf("that returns the default tag to %q, which %q holds; %s then removes the status, "+
			"forwarding the tasks created in it since",
			destination, operation.Name, removal),
	}
}

// reorderInverse puts a status back between the neighbours it left.
//
// It names the status that preceded it, or the one that followed when it was
// first, because those are the two ways to describe a position without
// depending on a rank that the reorder itself replaced.
func reorderInverse(
	before core.Vocabulary,
	operation core.ConfigOperation,
	subject core.Status,
	pack []core.ConfigOperation,
) *statusInverse {
	definitions := before.Definitions()
	index := before.Order(subject)
	if index >= len(definitions) || definitions[index].Status != subject {
		return nil
	}
	inverse := &statusInverse{Exact: true}
	if index == 0 {
		if len(definitions) < 2 {
			return nil
		}
		inverse.Command = statusCommand("move", string(operation.Status), "--before", string(definitions[1].Status))
	} else {
		inverse.Command = statusCommand("move", string(operation.Status), "--after", string(definitions[index-1].Status))
	}
	// A drag on the board sets the whole order in one commit, which moves as
	// many statuses as the drag disturbed. One move puts one of them back, so
	// the inverse says how many it does not.
	if moved := reorderedIn(pack); moved > 1 {
		inverse.Exact = false
		inverse.Note = fmt.Sprintf("this commit moved %d statuses; that restores %q alone", moved, subject)
	}
	return inverse
}

// reorderedIn counts the statuses a pack moved.
func reorderedIn(pack []core.ConfigOperation) int {
	moved := 0
	for _, operation := range pack {
		if operation.Type == core.ConfigStatusReorder {
			moved++
		}
	}
	return moved
}

// removeInverse defines the status again where it was, with the label and tags
// it had.
//
// It is never exact, and the note says exactly which tasks come back. Defining
// the name again drops the forwarding pointer — an add deletes the alias and the
// retirement for the name it defines — so every task still stored under the old
// value reads as being in that column again. What does not come back is a task
// some later write settled: correct-on-touch rewrote its stored value to the
// destination, and no configuration change can find it again.
func removeInverse(before core.Vocabulary, operation core.ConfigOperation, subject core.Status) *statusInverse {
	definitions := before.Definitions()
	index := before.Order(subject)
	if index >= len(definitions) || definitions[index].Status != subject {
		return nil
	}
	definition := definitions[index]
	parts := []string{"add", string(definition.Status)}
	if index == 0 {
		if len(definitions) > 1 {
			parts = append(parts, "--before", string(definitions[1].Status))
		}
	} else {
		parts = append(parts, "--after", string(definitions[index-1].Status))
	}
	parts = append(parts, "--label", definition.Label)
	for _, tag := range definition.Tags {
		parts = append(parts, "--tag", string(tag))
	}
	return &statusInverse{
		Command: statusCommand(parts...),
		Note: fmt.Sprintf(
			"tasks still stored under %q return to it, because defining the name again drops the forwarding "+
				"pointer; tasks a later write settled into %q stay there",
			definition.Status, operation.Destination),
	}
}

// configPackSummary says what one recorded commit did, in one clause.
//
// A commit is one command, so the summary is the summary of the operation the
// command was about, and a pack carrying more than that says how much more
// rather than listing it: `workbook status tag` records a transfer and every tag
// it dropped, and "tagged status triage default" is what happened.
func configPackSummary(operations []core.ConfigOperation) string {
	if len(operations) == 0 {
		return "recorded nothing"
	}
	summary := configOperationSummary(operations[0])
	if len(operations) > 1 {
		summary += fmt.Sprintf(" (+%d more change(s) in this commit)", len(operations)-1)
	}
	return summary
}

// configOperationSummary says what one recorded operation did.
func configOperationSummary(operation core.ConfigOperation) string {
	switch operation.Type {
	case core.ConfigGenesis:
		count := 0
		if operation.Config != nil {
			count = len(operation.Config.Vocabulary.Statuses)
		}
		return fmt.Sprintf("seeded this project's configuration with %d status(es)", count)
	case core.ConfigStatusAdd:
		summary := fmt.Sprintf("added status %s", operation.Name)
		if len(operation.Tags) > 0 {
			summary += " tagged " + joinStatusTags(operation.Tags)
		}
		return summary
	case core.ConfigStatusRename:
		return fmt.Sprintf("renamed status %s to %s", operation.From, operation.To)
	case core.ConfigStatusRelabel:
		return fmt.Sprintf("labelled status %s %q", operation.Status, operation.Label)
	case core.ConfigStatusReorder:
		return fmt.Sprintf("moved status %s", operation.Status)
	case core.ConfigStatusTag:
		return fmt.Sprintf("tagged status %s %s", operation.Status, operation.Tag)
	case core.ConfigStatusUntag:
		return fmt.Sprintf("untagged status %s %s", operation.Status, operation.Tag)
	case core.ConfigStatusRemove:
		return fmt.Sprintf("removed status %s into %s", operation.Status, operation.Destination)
	default:
		return string(operation.Type)
	}
}

func joinStatusTags(tags []core.StatusTag) string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, string(tag))
	}
	return strings.Join(names, ",")
}

// statusCommand renders a runnable command line, quoting only what a shell
// would otherwise split or interpret.
func statusCommand(parts ...string) string {
	quoted := make([]string, 0, len(parts)+2)
	quoted = append(quoted, "workbook", "status")
	for _, part := range parts {
		quoted = append(quoted, quoteStatusArgument(part))
	}
	return strings.Join(quoted, " ")
}

// shellSafeArgument matches the characters a POSIX shell passes through
// untouched. Anything else is quoted, which is what keeps a label like
// `Next Up` one argument.
var shellSafeArgument = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func quoteStatusArgument(value string) string {
	if value != "" && shellSafeArgument.MatchString(value) {
		return value
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(value) + `"`
}

// writeStatusMutation reports a status change on both surfaces.
//
// The JSON envelope carries the same members a task mutation's does — the sync
// report, the conflict lists, the warnings — because a caller that already
// parses one should not need a second parser for the other.
func writeStatusMutation(
	stdout, stderr io.Writer,
	command string,
	result statusMutationResult,
	session *taskSession,
	docsErr error,
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the status change was recorded locally, but " + session.report.Detail,
		})
	}
	warnings = append(warnings, docsWarning(result.Docs, docsErr)...)
	if jsonMode {
		writeStatusEnvelope(stdout, command, result, session, warnings)
		return
	}
	writeStatusChange(stdout, result)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
}

func writeStatusEnvelope(
	stdout io.Writer,
	command string,
	result statusMutationResult,
	session *taskSession,
	warnings []core.Warning,
) {
	envelope := ResultEnvelope{
		Format:         "workbook.result",
		Version:        1,
		Command:        command,
		Data:           result,
		Conflict:       session.conflicts,
		ConfigConflict: session.report.configConflicts,
		Warnings:       warnings,
		Sync:           &session.report,
	}
	_ = json.NewEncoder(stdout).Encode(envelope)
}

// writeStatusChange renders a change as a heading and its details, the shape
// every other structured text block in this CLI uses: one column-zero line that
// cannot be forged from inside a value, then tab-indented fields.
func writeStatusChange(output io.Writer, result statusMutationResult) {
	change := result.Change
	fmt.Fprintf(output, "Status:\t%s\t%s\n", change.Operation, change.Status)
	if change.From != "" {
		fmt.Fprintf(output, "\tfrom:\t%s\n", change.From)
	}
	if change.Into != "" {
		fmt.Fprintf(output, "\tinto:\t%s\n", change.Into)
	}
	if label := change.Label; label != nil {
		fmt.Fprintf(output, "\tlabel:\t%s\n", statusLabelLine(change))
	}
	if position := change.Position; position != nil {
		fmt.Fprintf(output, "\tposition:\t%s\n", statusPositionLine(*position, len(result.Vocabulary.Statuses)))
	}
	if change.Tags != nil {
		fmt.Fprintf(output, "\ttags:\t%s\n", statusTagsLine(change.Tags))
	}
	if change.DefaultFrom != "" {
		fmt.Fprintf(output, "\tdefault:\t%s → %s\n", change.DefaultFrom, change.Status)
	}
	if result.Tasks.Affected > 0 {
		fmt.Fprintf(output, "\ttasks:\t%d affected, %d claimable after\n",
			result.Tasks.Affected, result.Tasks.ClaimableAfter)
	}
	if result.Inverse.Command != "" {
		exactness := "\t(not exact)"
		if result.Inverse.Exact {
			exactness = ""
		}
		fmt.Fprintf(output, "\tinverse:\t%s%s\n", singleLine(result.Inverse.Command), exactness)
		if result.Inverse.Note != "" {
			fmt.Fprintf(output, "\tnote:\t%s\n", singleLine(result.Inverse.Note))
		}
	}
	if result.Docs == nil {
		fmt.Fprintf(output, "\tdocs:\tskipped\n")
		return
	}
	for _, artifact := range result.Docs.Artifacts {
		fmt.Fprintf(output, "\tdocs:\t%s\t%s\n", artifact.Path, artifactAction(artifact))
	}
}

// statusLabelLine renders the label the way the derived rule has to be read: an
// arrow when it moved, and which of the two rules moved it.
func statusLabelLine(change statusChange) string {
	label := *change.Label
	rendered := singleLine(label.To)
	if label.From != "" && label.From != label.To {
		rendered = singleLine(label.From) + " → " + singleLine(label.To)
	}
	if change.LabelDerived == nil {
		return rendered
	}
	if *change.LabelDerived {
		return rendered + " (derived)"
	}
	return rendered + " (kept)"
}

func statusPositionLine(position statusPosition, total int) string {
	switch {
	case position.Before != "":
		return fmt.Sprintf("before %s (%d of %d)", position.Before, position.Order, total)
	case position.After != "":
		return fmt.Sprintf("after %s (%d of %d)", position.After, position.Order, total)
	default:
		return fmt.Sprintf("last (%d of %d)", position.Order, total)
	}
}

func statusTagsLine(tags []core.StatusTag) string {
	if len(tags) == 0 {
		return "none"
	}
	return joinStatusTags(tags)
}

// writeStatusList renders the status table and the two note blocks under it.
//
// Retired and unresolved values are notes rather than rows because they are not
// columns: putting them in the table would make a board's worth of statuses
// indistinguishable from the values that merely still resolve into one.
func writeStatusList(output io.Writer, result statusListResult) error {
	rows := make([]terminalui.StatusRow, 0, len(result.Statuses))
	for _, status := range result.Statuses {
		tasks := ""
		if status.Tasks != nil {
			tasks = strconv.Itoa(*status.Tasks)
		}
		rows = append(rows, terminalui.StatusRow{
			Position: status.Order,
			Status:   string(status.Status),
			Label:    singleLine(status.Label),
			Tags:     statusTagsLine(status.Tags),
			Tasks:    tasks,
		})
	}
	width, measured := terminalWidth(output)
	if !measured {
		width = nonInteractiveWidth
	}
	if err := terminalui.RenderStatusList(output, rows, width); err != nil {
		return core.Wrap(core.CategoryOperational, "render status list", err)
	}
	if !result.Seeded {
		// Not "the statuses this project started with": a project can reach the
		// fallback by never having recorded anything and by having lost the
		// ledger that recorded something, and only one of those started here.
		// What is true in both is that nothing is recorded now.
		fmt.Fprintf(output, "\tNo status change is recorded, so these are the statuses Workbook reads for a project that has none of its own.\n")
	}
	for _, migration := range result.Migrations {
		fmt.Fprintf(output, "\tNo longer a default:\t%s\t%s\n", migration.Status, migration.Reason)
		if migration.Command != "" {
			fmt.Fprintf(output, "\t\tremove it when this project no longer needs it: %s\n", migration.Command)
			continue
		}
		fmt.Fprintf(output, "\t\tremoving it starts by giving the default tag to another status: %s\n", migration.First)
	}
	for _, retired := range result.Retired {
		fmt.Fprintf(output, "\tRetired:\t%s → %s\t%s%s\n",
			retired.Status, retired.Becomes, retirementVerb(retired.Operation), retiredOnClause(retired.At))
	}
	for _, unresolved := range result.Unresolved {
		fmt.Fprintf(output, "\tUnresolved:\t%s\t%d task(s)\t%s\n",
			unresolved.Status, unresolved.Tasks, unresolvedTaskIDsLine(unresolved))
		fmt.Fprintf(output, "\t\tcorrect with: workbook update <task> --status <status>, or define it again: %s\n",
			statusCommand("add", string(unresolved.Status)))
	}
	for _, advisory := range result.Advisories {
		fmt.Fprintf(output, "\tAdvisory:\t%s\t%s\n", advisory.Code, advisory.Message)
	}
	return nil
}

// unresolvedTaskIDsLine renders the tasks stranded under one value, saying so
// when it is showing only the first few.
//
// A sample that did not announce itself would read as the whole set, and a
// person who filed every ID they were given would think they were finished.
func unresolvedTaskIDsLine(unresolved unresolvedStatusView) string {
	ids := strings.Join(unresolved.TaskIDs, ", ")
	if len(unresolved.TaskIDs) >= unresolved.Tasks {
		return ids
	}
	return fmt.Sprintf("%s (first %d of %d)", ids, len(unresolved.TaskIDs), unresolved.Tasks)
}

// retirementVerb names how a value stopped being live, for a column rather
// than for a sentence: the arrow beside it already says where it went.
func retirementVerb(operation core.ConfigOperationType) string {
	if operation == core.ConfigStatusRemove {
		return "removed"
	}
	return "renamed"
}

func retiredOnClause(at string) string {
	if at == "" {
		return ""
	}
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	return " on " + when.UTC().Format("2006-01-02")
}

// writeStatusLog renders the ledger the way `show --history` renders a task's
// chain: oldest first, wall times as attribution only, and the window's size
// stated before its contents.
func writeStatusLog(output io.Writer, result statusLogResult, found bool) {
	if !found {
		fmt.Fprintln(output, "No status change is recorded; this project has not configured its statuses.")
		return
	}
	if result.Showing < result.Total {
		fmt.Fprintf(output, "Showing %d most recent changes out of %d.\n", result.Showing, result.Total)
	} else {
		fmt.Fprintf(output, "Showing all %d change(s).\n", result.Total)
	}
	for _, entry := range result.Entries {
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\n",
			entry.Commit,
			entry.WallTime.Format(time.RFC3339),
			singleLine(entry.Actor),
			singleLine(entry.Summary),
		)
		if entry.Inverse == nil {
			continue
		}
		exactness := "\t(not exact)"
		if entry.Inverse.Exact {
			exactness = ""
		}
		fmt.Fprintf(output, "\tinverse:\t%s%s\n", singleLine(entry.Inverse.Command), exactness)
		if entry.Inverse.Note != "" {
			fmt.Fprintf(output, "\tnote:\t%s\n", singleLine(entry.Inverse.Note))
		}
	}
	writeHistoryTruncation(output, result.Truncated)
}
