package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/projection"
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
	// next` where they land, which is the number an agent's queue changes by.
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

type unresolvedStatusView struct {
	Status  core.Status `json:"status"`
	Tasks   int         `json:"tasks"`
	TaskIDs []string    `json:"taskIds"`
}

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
	Inverse   *statusInverse           `json:"inverse,omitempty"`
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

	document := state.Vocabulary.Document()
	result := statusListResult{
		Head:       state.Head,
		Seeded:     state.Seeded,
		Default:    state.Vocabulary.Default(),
		Statuses:   statusViews(state.Vocabulary, counts),
		Unresolved: unresolved,
		Advisories: historyvalidation.StatusCeilingAdvisories(document),
	}
	// Retirement dates come from the ledger, so a project that has none skips
	// the walk entirely — and has nothing retired to date anyway.
	var retiredAt map[core.Status]time.Time
	if state.Seeded {
		ledger, _, _, err := readConfigLedger(ctx, repository, config)
		if err != nil {
			return err
		}
		retiredAt = forwardingTimes(ledger)
	}
	result.Retired = retiredStatusViews(document, retiredAt)

	if *jsonMode {
		writeResult(stdout, "status list", result)
		return nil
	}
	return writeStatusList(stdout, result)
}

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
		views = append(views, unresolvedStatusView{Status: status, Tasks: len(ids), TaskIDs: ids})
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
	window := 0
	if *limit != "" {
		parsed, err := strconv.Atoi(*limit)
		if err != nil || parsed < 1 {
			return core.Errorf(core.CategoryInvocation, "status log --limit must be a positive whole number")
		}
		if *all {
			return core.Errorf(core.CategoryInvocation, "cannot use --limit with --all")
		}
		window = parsed
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	ledger, truncation, found, err := readConfigLedger(ctx, repository, config)
	if err != nil {
		return err
	}
	result := buildStatusLog(ledger, truncation, window, *all)
	if *jsonMode {
		writeResult(stdout, "status log", result)
		return nil
	}
	writeStatusLog(stdout, result, found)
	return nil
}

// configLedgerCommit is one commit of the configuration ledger with the
// vocabulary its parent held, which is what every inverse is computed against.
type configLedgerCommit struct {
	Commit string
	Pack   core.ConfigOperationPack
	Before core.Vocabulary
}

// readConfigLedger folds the whole configuration ledger, oldest commit first.
//
// A project with no ledger returns nothing and no error: the ledger is seeded
// lazily, so its absence is the ordinary state and not something to report as a
// failure. A ledger that stops validating partway returns the prefix that did
// validate plus the truncation, exactly as a task history does — the recorded
// changes before the bad commit are still the project's history, and refusing
// to show them would withhold the only context somebody has for repairing it.
func readConfigLedger(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
) ([]configLedgerCommit, *core.HistoryTruncation, bool, error) {
	var commits []configLedgerCommit
	var previous core.Vocabulary
	var truncation *core.HistoryTruncation
	found, err := repository.ReadConfigHistoryStream(ctx, config, gitstore.ConfigHistoryStream{
		Begin: func(gitstore.ConfigHistoryStart) error { return nil },
		Commit: func(commit gitstore.ConfigHistoryCommit) error {
			commits = append(commits, configLedgerCommit{
				Commit: commit.ObjectID,
				Pack:   commit.Operation,
				Before: previous,
			})
			previous = commit.State.Vocabulary()
			return nil
		},
		End: func(result gitstore.ConfigHistoryResult) error {
			if result.Failure != nil {
				truncation = &core.HistoryTruncation{
					Commit:  result.Failure.Commit,
					Message: result.Failure.Err.Error(),
				}
			}
			return nil
		},
	})
	if err != nil {
		return nil, nil, false, err
	}
	return commits, truncation, found, nil
}

// forwardingTimes records when each retired value stopped being live.
//
// The last recorded forwarding wins, because a value can be retired more than
// once: adding a name back deletes its forwarding pointer, and retiring it
// again writes a new one.
func forwardingTimes(ledger []configLedgerCommit) map[core.Status]time.Time {
	times := make(map[core.Status]time.Time)
	for _, commit := range ledger {
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

// buildStatusLog windows the ledger the way BuildChangeLog windows a task's
// chain: oldest first, the most recent changes kept, and the count of what was
// left out stated rather than implied.
func buildStatusLog(ledger []configLedgerCommit, truncation *core.HistoryTruncation, limit int, all bool) statusLogResult {
	entries := make([]statusLogEntry, 0, len(ledger))
	for _, commit := range ledger {
		for index, operation := range commit.Pack.Operations {
			subject := packSubject(commit.Pack, index, operation.Status)
			entries = append(entries, statusLogEntry{
				Commit:      commit.Commit,
				OperationID: operation.ID,
				WallTime:    commit.Pack.WallTime,
				Actor:       commit.Pack.Actor.ID,
				Operation:   operation.Type,
				Summary:     configOperationSummary(operation),
				Inverse:     configOperationInverse(commit.Before, operation, subject),
			})
		}
	}
	result := statusLogResult{Total: len(entries), Truncated: truncation}
	if !all {
		if limit <= 0 {
			limit = core.DefaultChangeLimit
		}
		if len(entries) > limit {
			entries = entries[len(entries)-limit:]
		}
	}
	result.Entries = entries
	result.Showing = len(entries)
	return result
}

// packSubject maps an operation's subject back to the name the pack's parent
// knew it by, following renames earlier in the same pack.
//
// Every inverse is read against the pack's parent rather than against the state
// immediately before the operation, and for a pack of one — which is what most
// commands write — those are the same thing. For a longer pack the parent is
// deliberately still the reference: the useful inverse of one operation in a
// batch is the command that restores what the whole batch found, so a tag
// replacement reports the set the command replaced rather than an intermediate
// nobody saw. Only the name needs translating, and only when an earlier
// operation in the same pack renamed it, because that name did not exist before
// the pack.
func packSubject(pack core.ConfigOperationPack, index int, subject core.Status) core.Status {
	for step := index - 1; step >= 0; step-- {
		operation := pack.Operations[step]
		if operation.Type == core.ConfigStatusRename && operation.To == subject {
			subject = operation.From
		}
	}
	return subject
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
	display := core.DerivedStatusLabel(status)
	if *label != "" {
		display = *label
	}
	if err := core.ValidateStatusLabel(display); err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status add", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			if vocabulary.Has(status) {
				return statusPlan{}, core.Errorf(core.CategoryValidation,
					"this project already defines status %q", status)
			}
			rank := vocabulary.AppendRank()
			anchor, placeBefore := core.Status(*before), *before != ""
			if *after != "" {
				anchor = core.Status(*after)
			}
			position := &statusPosition{}
			if anchor != "" {
				resolved, err := requireLiveStatus(ctx, session, vocabulary, anchor)
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
				Name:  status,
				Label: display,
				Rank:  rank,
				Tags:  wanted,
			}
			change := statusChange{
				Operation: "add",
				Status:    status,
				Position:  position,
				Label:     &statusLabel{To: display},
				Tags:      wanted,
			}
			if containsStatusTag(wanted, core.StatusTagDefault) {
				change.DefaultFrom = vocabulary.Default()
			}
			return statusPlan{
				operations: []core.ConfigOperation{operation},
				primary:    operation,
				change:     change,
			}, nil
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

	return runStatusMutation(ctx, cwd, "status rename", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, from)
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

			// The derived-label rule. A label nobody chose follows the name it
			// was derived from; a label somebody chose is theirs and survives a
			// rename of the machine value underneath it.
			current := vocabulary.Label(subject)
			display, derived := current, false
			switch {
			case *label != "":
				display = *label
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
				primary:    rename,
				change: statusChange{
					Operation:    "rename",
					Status:       to,
					From:         subject,
					Label:        &statusLabel{From: current, To: display},
					LabelDerived: &labelDerived,
					Tags:         statusTags(vocabulary, subject),
				},
				restoreLabel: current,
			}, nil
		})
}

func runStatusLabel(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status label", []string{"<status>", "<display-label>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	display := values[1]
	if err := core.ValidateStatusLabel(display); err != nil {
		return err
	}

	return runStatusMutation(ctx, cwd, "status label", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, core.Status(values[0]))
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
				primary:    operation,
				change: statusChange{
					Operation: "label",
					Status:    subject,
					Label:     &statusLabel{From: current, To: display},
					Tags:      statusTags(vocabulary, subject),
				},
			}, nil
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
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "status move requires exactly one of --before or --after")
	}

	return runStatusMutation(ctx, cwd, "status move", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, core.Status(values[0]))
			if err != nil {
				return statusPlan{}, err
			}
			placeBefore := *before != ""
			anchorInput := core.Status(*after)
			if placeBefore {
				anchorInput = core.Status(*before)
			}
			anchor, err := requireLiveStatus(ctx, session, vocabulary, anchorInput)
			if err != nil {
				return statusPlan{}, err
			}
			if anchor == subject {
				return statusPlan{}, core.Errorf(core.CategoryValidation,
					"cannot move status %q relative to itself", subject)
			}
			rank, err := vocabulary.InsertRank(subject, anchor, placeBefore)
			if err != nil {
				return statusPlan{}, err
			}
			position := &statusPosition{Rank: rank}
			if placeBefore {
				position.Before = anchor
			} else {
				position.After = anchor
			}
			operation := core.ConfigOperation{Type: core.ConfigStatusReorder, Status: subject, Rank: rank}
			return statusPlan{
				operations: []core.ConfigOperation{operation},
				primary:    operation,
				change: statusChange{
					Operation: "move",
					Status:    subject,
					Position:  position,
					Label:     &statusLabel{To: vocabulary.Label(subject)},
					Tags:      statusTags(vocabulary, subject),
				},
			}, nil
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

	return runStatusMutation(ctx, cwd, "status tag", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, core.Status(values[0]))
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
			return statusPlan{
				operations:  operations,
				primary:     operations[0],
				change:      change,
				restoreTags: current,
			}, nil
		})
}

func runStatusUntag(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("status untag", []string{"<status>", "<tag>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("status", "untag")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	tag := core.StatusTag(values[1])
	if err := core.ValidateStatusTag(tag); err != nil {
		return statusTagError(err)
	}

	return runStatusMutation(ctx, cwd, "status untag", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, core.Status(values[0]))
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
				primary:    operation,
				change: statusChange{
					Operation: "untag",
					Status:    subject,
					Tags:      remaining,
				},
				restoreTags: current,
			}, nil
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

	return runStatusMutation(ctx, cwd, "status delete", *noSync, *jsonMode, stdout, stderr,
		func(ctx context.Context, session *taskSession, vocabulary core.Vocabulary) (statusPlan, error) {
			subject, err := requireLiveStatus(ctx, session, vocabulary, core.Status(values[0]))
			if err != nil {
				return statusPlan{}, err
			}
			destination, err := requireLiveStatus(ctx, session, vocabulary, core.Status(*into))
			if err != nil {
				return statusPlan{}, err
			}
			if destination == subject {
				return statusPlan{}, core.Errorf(core.CategoryValidation,
					"status delete cannot forward %q into itself; name where its tasks belong", subject)
			}
			definition, _ := statusDefinition(vocabulary, subject)
			counts, err := removalTaskCounts(ctx, session, vocabulary, subject, destination)
			if err != nil {
				return statusPlan{}, err
			}
			operation := core.ConfigOperation{
				Type: core.ConfigStatusRemove, Status: subject, Destination: destination,
			}
			return statusPlan{
				operations: []core.ConfigOperation{operation},
				primary:    operation,
				change: statusChange{
					Operation: "delete",
					Status:    subject,
					Into:      destination,
					Label:     &statusLabel{To: definition.Label},
					Tags:      definition.Tags,
				},
				tasks: counts,
			}, nil
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
// about to change, because afterwards the tasks resolve into the destination
// and the question can no longer be asked. Claimability is the coarse form on
// purpose — a task with no dependencies in a status tagged next is eligible now,
// while one with dependencies depends on tasks this command is not looking at.
func removalTaskCounts(
	ctx context.Context,
	session *taskSession,
	vocabulary core.Vocabulary,
	subject, destination core.Status,
) (statusTaskCounts, error) {
	tasks, err := session.service.List(ctx, core.ListFilter{})
	if err != nil {
		return statusTaskCounts{}, err
	}
	counts := statusTaskCounts{}
	claimable := vocabulary.IsNext(destination)
	for _, task := range tasks {
		if task.Status != subject {
			continue
		}
		counts.Affected++
		if claimable && len(task.Dependencies) == 0 {
			counts.ClaimableAfter++
		}
	}
	return counts, nil
}

// statusPlan is one authored status change: the operations to record, and
// everything the envelope says about them that the operations alone do not.
type statusPlan struct {
	operations []core.ConfigOperation
	// primary is the operation the inverse is derived from. A command that
	// writes several operations still has one of them as its subject matter.
	primary core.ConfigOperation
	change  statusChange
	tasks   statusTaskCounts
	// restoreLabel and restoreTags carry what the inverse has to name to be
	// exact, for the two commands whose inverse cannot be read off the
	// operation alone.
	restoreLabel string
	restoreTags  []core.StatusTag
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
	noSync bool,
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
		Inverse: statusPlanInverse(before, plan),
	}
	if position := result.Change.Position; position != nil {
		position.Order = after.Order(plan.change.Status) + 1
	}
	writeStatusMutation(stdout, stderr, command, result, session, jsonMode)
	return nil
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
	session *taskSession,
	vocabulary core.Vocabulary,
	status core.Status,
) (core.Status, error) {
	if err := core.ValidateStatusToken(status); err != nil {
		return "", err
	}
	if vocabulary.Has(status) {
		return status, nil
	}
	destination, operation, forwarded := vocabulary.Forwarding(status)
	if !forwarded {
		return "", core.Errorf(core.CategoryNotFound,
			"no status %q in this project; the statuses are: %s", status, statusNameList(vocabulary))
	}
	return "", core.Errorf(core.CategoryNotFound, "no status %q; it was %s %q%s",
		status, forwardingVerb(operation), destination, statusForwardedOn(ctx, session, status))
}

// statusForwardedOn dates a forwarding from the ledger, and says nothing when
// it cannot. The date is what turns "it was renamed" into something a person
// can place among their own weeks, and reading the ledger for it is affordable
// here because this path has already failed.
func statusForwardedOn(ctx context.Context, session *taskSession, status core.Status) string {
	ledger, _, found, err := readConfigLedger(ctx, session.repository, session.config)
	if err != nil || !found {
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

// statusPlanInverse is the command that undoes a whole status command.
//
// It starts from the operation's inverse — the same function the log uses, so
// one matrix answers for both — and adds what a multi-operation command needs
// to be exact: the label a rename replaced, and the tag set a replacement
// discarded.
func statusPlanInverse(before core.Vocabulary, plan statusPlan) statusInverse {
	inverse := configOperationInverse(before, plan.primary, statusOperationSubject(plan.primary))
	if inverse == nil {
		return statusInverse{}
	}
	switch plan.change.Operation {
	case "rename":
		if plan.change.Label != nil && plan.change.Label.From != plan.change.Label.To {
			inverse.Command += " --label " + quoteStatusArgument(plan.restoreLabel)
		}
	case "tag", "untag":
		*inverse = tagSetInverse(before, plan)
	}
	return *inverse
}

// tagSetInverse restores the tag set a replacement discarded.
//
// The default tag is the case that makes this more than a rewrite of the old
// set. Exactly one status carries it, so a command that took it also took it
// from somebody: restoring the subject's old set alone would leave the project
// with no default at all, which the authoring gate refuses outright. So the
// inverse names the status that gave it up — one command that is valid on its
// own — and the note carries the second command that finishes the job.
func tagSetInverse(before core.Vocabulary, plan statusPlan) statusInverse {
	subject := plan.change.Status
	restore := tagCommand(subject, plan.restoreTags)
	if plan.change.DefaultFrom == "" {
		return statusInverse{Command: restore, Exact: true}
	}
	previous := plan.change.DefaultFrom
	definition, live := statusDefinition(before, previous)
	if !live {
		return statusInverse{Command: restore}
	}
	return statusInverse{
		Command: tagCommand(previous, definition.Tags),
		Exact:   false,
		Note: fmt.Sprintf("that returns the default tag to %q; %s restores %q's own tags",
			previous, restore, subject),
	}
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

// configOperationInverse is the command that undoes one recorded operation,
// read against the vocabulary that operation found.
//
// Every answer is a command somebody can run, never a verb Workbook implements:
// an undo that is sometimes exact and sometimes not is worse than a printed
// command whose limits are stated beside it. A genesis has no inverse, and says
// so by returning nothing rather than by offering to delete a project's
// configuration.
//
// subject is the name `before` knew the operation's subject by; see packSubject
// for the one case where that is not the operation's own subject. The command
// this returns always names the status by the name it carries now, because that
// is the name the command would have to be run against.
func configOperationInverse(before core.Vocabulary, operation core.ConfigOperation, subject core.Status) *statusInverse {
	switch operation.Type {
	case core.ConfigStatusAdd:
		return addInverse(before, operation)
	case core.ConfigStatusRename:
		return &statusInverse{
			Command: statusCommand("rename", string(operation.To), string(operation.From)),
			Exact:   true,
		}
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
		return reorderInverse(before, operation, subject)
	case core.ConfigStatusTag, core.ConfigStatusUntag:
		definition, live := statusDefinition(before, subject)
		if !live {
			return nil
		}
		return &statusInverse{
			Command: tagCommand(operation.Status, definition.Tags),
			Exact:   true,
		}
	case core.ConfigStatusRemove:
		return removeInverse(before, operation, subject)
	default:
		return nil
	}
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
	inverse := &statusInverse{
		Command: statusCommand("delete", string(operation.Name), "--into", string(destination)),
		Note:    fmt.Sprintf("tasks created in %q since are forwarded to %q", operation.Name, destination),
	}
	if containsStatusTag(operation.Tags, core.StatusTagDefault) {
		// Removing the status that holds the default tag is refused outright,
		// so the note names the command that has to come first rather than
		// leaving somebody to discover it from the refusal.
		inverse.Note = fmt.Sprintf("%q holds the default tag, so give it back first: %s",
			operation.Name,
			tagCommand(destination, append(append([]core.StatusTag(nil), definition.Tags...), core.StatusTagDefault)))
	}
	return inverse
}

// reorderInverse puts a status back between the neighbours it left.
//
// It names the status that preceded it, or the one that followed when it was
// first, because those are the two ways to describe a position without
// depending on a rank that the reorder itself replaced.
func reorderInverse(before core.Vocabulary, operation core.ConfigOperation, subject core.Status) *statusInverse {
	definitions := before.Definitions()
	index := before.Order(subject)
	if index >= len(definitions) || definitions[index].Status != subject {
		return nil
	}
	if index == 0 {
		if len(definitions) < 2 {
			return nil
		}
		return &statusInverse{
			Command: statusCommand("move", string(operation.Status), "--before", string(definitions[1].Status)),
			Exact:   true,
		}
	}
	return &statusInverse{
		Command: statusCommand("move", string(operation.Status), "--after", string(definitions[index-1].Status)),
		Exact:   true,
	}
}

// removeInverse defines the status again where it was, with the label and tags
// it had.
//
// It is never exact, and the note says the part a command cannot do: the tasks
// that were forwarded are stored under the destination now, or will be settled
// there the next time anything writes to them, and defining the name again does
// not bring them back.
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
		Note: fmt.Sprintf("tasks that resolved into %q are not moved back",
			operation.Destination),
	}
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
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the status change was recorded locally, but " + session.report.Detail,
		})
	}
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
		fmt.Fprintf(output, "\tNo status change is recorded yet, so these are the statuses Workbook ships with.\n")
	}
	for _, retired := range result.Retired {
		fmt.Fprintf(output, "\tRetired:\t%s → %s\t%s%s\n",
			retired.Status, retired.Becomes, retirementVerb(retired.Operation), retiredOnClause(retired.At))
	}
	for _, unresolved := range result.Unresolved {
		fmt.Fprintf(output, "\tUnresolved:\t%s\t%d task(s)\t%s\n",
			unresolved.Status, unresolved.Tasks, strings.Join(unresolved.TaskIDs, ", "))
		fmt.Fprintf(output, "\t\tcorrect with: workbook update <task> --status <status>, or define it again: %s\n",
			statusCommand("add", string(unresolved.Status)))
	}
	for _, advisory := range result.Advisories {
		fmt.Fprintf(output, "\tAdvisory:\t%s\t%s\n", advisory.Code, advisory.Message)
	}
	return nil
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
