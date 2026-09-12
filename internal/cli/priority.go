package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/projection"
)

// priorityChange is what one priority command did, in the shape every mutating
// priority envelope carries.
//
// It is statusChange's counterpart, member for member, plus the one field a
// priority has that a status does not — its color — and minus nothing. One
// shape for nine verbs is deliberate for the reason statusChange's comment
// gives: a caller that reads `change.priority` and `change.operation` can
// handle a verb it has never heard of, and the members a verb does not use are
// omitted rather than zeroed, so the presence of `into` is what says a removal
// happened.
type priorityChange struct {
	// Operation is the verb, not the durable operation type.
	Operation string `json:"operation"`
	// Priority is the subject after the change. A rename reports the new value
	// here and the old one in From, because the new value is what every later
	// command names.
	Priority core.Priority `json:"priority"`
	From     core.Priority `json:"from,omitempty"`
	// Into is where a removal forwarded the priority's tasks.
	Into     core.Priority     `json:"into,omitempty"`
	Position *priorityPosition `json:"position,omitempty"`
	Label    *priorityLabel    `json:"label,omitempty"`
	// LabelDerived reports that the label came from the priority's name rather
	// than from anybody's choice. It is meaningful for a rename, where it says
	// whether a custom label was kept.
	LabelDerived *bool `json:"labelDerived,omitempty"`
	// Tags is the priority's whole tag set after the change, not the
	// difference, so a caller never has to reconstruct it.
	Tags []core.PriorityTag `json:"tags,omitempty"`
	// DefaultFrom names the priority that gave up the default tag, when this
	// change moved it. Exactly one priority carries that tag, so taking it is
	// always also giving it up somewhere.
	DefaultFrom core.Priority `json:"defaultFrom,omitempty"`
	// Color is the priority's ink before and after the change. It is a pointer
	// because only `priority color` touches it, and an empty To is a cleared
	// color rather than an absent member — which is the whole difference
	// between "the board derives one now" and "this verb did not say".
	Color *priorityColor `json:"color,omitempty"`
}

// priorityPosition is where a priority sits after the change: the neighbour
// that was named, the rank it produced, and the 1-based place a person reads,
// counting from the most urgent.
type priorityPosition struct {
	Before core.Priority `json:"before,omitempty"`
	After  core.Priority `json:"after,omitempty"`
	Rank   string        `json:"rank"`
	Order  int           `json:"order"`
}

type priorityLabel struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// priorityColor is the stored ink before and after a recolor. An empty To
// means the color was cleared and the board derives one from the position.
type priorityColor struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// priorityView is one priority as the envelopes present it.
type priorityView struct {
	Priority core.Priority      `json:"priority"`
	Label    string             `json:"label"`
	Tags     []core.PriorityTag `json:"tags"`
	// Color is the stored ink, omitted when nothing is stored. Nothing stores a
	// default, so an absent member says the board derives one rather than
	// naming a value nobody chose.
	Color string `json:"color,omitempty"`
	Order int    `json:"order"`
	// Tasks counts the active tasks resolving to this priority. It is a pointer
	// for the reason statusView.Tasks is: a mutating verb does not count them,
	// and paying for the projection on every write to report a number nobody
	// asked for would be the wrong trade.
	Tasks *int `json:"tasks,omitempty"`
}

// priorityVocabularyView is the project's priorities as they stand after a
// change.
type priorityVocabularyView struct {
	Head string `json:"head"`
	// Seeded reports that a configuration ledger supplied these priorities.
	// False means they are the built-in three this build carries, which is
	// every project until somebody changes a priority.
	Seeded     bool           `json:"seeded"`
	Default    core.Priority  `json:"default"`
	Priorities []priorityView `json:"priorities"`
}

// priorityTaskCounts reports what a change means for the tasks at the priority.
//
// It carries one member where statusTaskCounts carries two, and the missing one
// is missing on purpose: `claimableAfter` answers whether moved tasks become
// eligible for `workbook next`, and eligibility is decided by a status's tags
// and a task's dependencies. Nothing about a priority gates it. A priority
// decides the order `next` considers tasks in, not which ones it may return, so
// a member reporting how many became claimable would be zero for every priority
// change forever.
type priorityTaskCounts struct {
	// Affected counts the active tasks that resolved through the removed
	// priority. It is stated rather than omitted, so a caller reading
	// `tasks.affected` gets an answer from every priority command.
	Affected int `json:"affected"`
}

// priorityInverse is the command that undoes a change, and how completely. It
// is statusInverse's counterpart and means exactly what that means: there is no
// `workbook priority undo`, and an inverse a person reads, edits, and runs is
// worth more than a verb promising to reverse something it cannot always
// reverse.
type priorityInverse struct {
	Command string `json:"command"`
	Exact   bool   `json:"exact"`
	Note    string `json:"note,omitempty"`
}

// priorityMutationResult is the data member of every mutating priority
// envelope.
type priorityMutationResult struct {
	Change     priorityChange         `json:"change"`
	Vocabulary priorityVocabularyView `json:"vocabulary"`
	Tasks      priorityTaskCounts     `json:"tasks"`
	Inverse    priorityInverse        `json:"inverse"`
	// Docs reports what happened to the generated documentation this change
	// invalidated, omitted when --no-docs skipped the regeneration.
	Docs *agentdocs.Report `json:"docs,omitempty"`
}

// priorityListResult is `workbook priority list`.
type priorityListResult struct {
	Head       string         `json:"head"`
	Seeded     bool           `json:"seeded"`
	Default    core.Priority  `json:"default"`
	Priorities []priorityView `json:"priorities"`
	// Retired lists the values that still resolve here without being live, so a
	// teammate reading a stored value can find out what happened to it.
	Retired []retiredPriorityView `json:"retired,omitempty"`
	// Unresolved lists stored values that resolve to nothing at all. They are
	// the one state in this document that needs somebody to act.
	Unresolved []unresolvedPriorityView `json:"unresolved,omitempty"`
	// Advisories carry what is true about this configuration without being
	// wrong with it — a folded state over one of the size ceilings, which two
	// clones can reach without either author being refused anything.
	Advisories []historyvalidation.Advisory `json:"advisories,omitempty"`
}

type retiredPriorityView struct {
	Priority core.Priority `json:"priority"`
	Becomes  core.Priority `json:"becomes"`
	// Operation is `priority.rename` or `priority.remove`, which is the
	// difference between "this priority is called something else now" and "this
	// priority is gone".
	Operation core.ConfigOperationType `json:"operation"`
	At        string                   `json:"at,omitempty"`
}

// unresolvedPriorityView is one stored value nothing in this project resolves,
// with the work standing behind it. Tasks is the whole count and TaskIDs a
// bounded sample of it, the same pair unresolvedStatusView carries and for the
// same reason; see maxUnresolvedTaskIDs.
type unresolvedPriorityView struct {
	Priority core.Priority `json:"priority"`
	Tasks    int           `json:"tasks"`
	TaskIDs  []string      `json:"taskIds"`
}

// priorityLogResult mirrors statusLogResult, because it answers the same
// question about the other half of the same ledger.
type priorityLogResult struct {
	Showing   int                     `json:"showing"`
	Total     int                     `json:"total"`
	Entries   []priorityLogEntry      `json:"entries"`
	Truncated *core.HistoryTruncation `json:"truncated,omitempty"`
}

type priorityLogEntry struct {
	Commit      string    `json:"commit"`
	OperationID string    `json:"operationId"`
	WallTime    time.Time `json:"wallTime"`
	Actor       string    `json:"actor"`
	// Operation is the durable operation type, because this is the ledger's own
	// record rather than a report of a command somebody ran.
	Operation core.ConfigOperationType `json:"operation"`
	Summary   string                   `json:"summary"`
	// Collapsed counts the priority operations this commit recorded beyond the
	// one the entry names. It counts priority operations alone: a commit that
	// also changed a status is two sections' news, and saying "+1 more change"
	// about a column would be reporting somebody else's edit in this log.
	Collapsed int              `json:"collapsed"`
	Inverse   *priorityInverse `json:"inverse,omitempty"`
}

// prioritySubcommands lists the verbs, for the error a bare `workbook priority`
// produces. It is derived from the help schema so a verb cannot be added
// without the refusal learning about it.
func prioritySubcommands() []string {
	return commandSchemas["priority"].SubcommandOrder
}

// runPriority dispatches the group.
//
// Every verb the schema declares is named here, including the ones whose files
// are still stubs, so that a verb going missing is a build failure rather than
// a silently lost command.
func runPriority(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := firstPriorityArgument(args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "list":
		return runPriorityList(ctx, args, cwd, stdout, stderr)
	case "add":
		return runPriorityAdd(ctx, args, cwd, stdout, stderr)
	case "rename":
		return runPriorityRename(ctx, args, cwd, stdout, stderr)
	case "label":
		return runPriorityLabel(ctx, args, cwd, stdout, stderr)
	case "move":
		return runPriorityMove(ctx, args, cwd, stdout, stderr)
	case "tag":
		return runPriorityTag(ctx, args, cwd, stdout, stderr)
	case "delete":
		return runPriorityDelete(ctx, args, cwd, stdout, stderr)
	case "color":
		return runPriorityColor(ctx, args, cwd, stdout, stderr)
	case "log":
		return runPriorityLog(ctx, args, cwd, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown priority command %q; %s",
			subcommand, priorityCommandList())
	}
}

// firstPriorityArgument takes the subcommand, and answers the one mistake worth
// answering specifically.
//
// `workbook priority WB-01J...` is not a typo, it is a different command: a
// caller — usually an agent — reaching for a task's priority and finding a verb
// family. Naming `workbook show` costs a clause and turns a dead end into the
// command they wanted. This is firstStatusArgument's rule, and shares its
// task-reference test, because the two families are mistyped the same way.
func firstPriorityArgument(args []string) (string, []string, error) {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation, "priority takes a subcommand; %s", priorityCommandList())
	}
	if _, known := commandMetadataFor([]string{"priority", args[0]}); !known && looksLikeTaskReference(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation,
			"workbook priority takes a subcommand; to read a task use: workbook show %s", args[0])
	}
	return args[0], args[1:], nil
}

func priorityCommandList() string {
	return "the subcommands are " + strings.Join(prioritySubcommands(), ", ")
}

// priorityScope is what authoring a priority change needs from the project
// besides the vocabulary it is authored against: the ledger, to date a value
// that is no longer live, and the project's tasks, to price a removal. It is
// statusScope's counterpart and exists for the same reason — the planners are
// reachable from more than the verbs.
type priorityScope struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	// service reads the project's tasks. Only a removal needs it, and only to
	// count what it moves.
	service core.Service
}

func (session *taskSession) priorityScope() priorityScope {
	return priorityScope{repository: session.repository, config: session.config, service: session.service}
}

// priorityPlan is one authored priority change: the operations to record, and
// everything the envelope says about them that the operations alone do not.
//
// It carries nothing for the inverse, for the reason statusPlan carries
// nothing: everything an inverse needs is in the operations and in the
// vocabulary they were authored against, which is what lets the log — which has
// only those two things — reach the same answer.
type priorityPlan struct {
	operations []core.ConfigOperation
	change     priorityChange
	tasks      priorityTaskCounts
}

// runPriorityMutation is the one path every priority change takes.
//
// It is runStatusMutation with the other section at the centre, and every step
// is there for the reason the original's comment gives: the same session, so
// the same fetch-before, the same watcher deferral, the same --no-sync, and the
// same sync member; and the same publish-after of the configuration ledger,
// which has no task ref to name.
//
// The refresh between the fetch and the build is the step worth understanding
// rather than copying. The vocabulary the fetch settled on is the one this
// change is authored against, which is what makes `priority rename` land on a
// teammate's newer name rather than on the one this clone opened with. It
// refreshes both sections from one read, because a change authored against
// priorities from one tip and statuses from another would regenerate the
// guidelines from a configuration that never existed.
func runPriorityMutation(
	ctx context.Context,
	cwd string,
	command string,
	noSync, noDocs bool,
	jsonMode bool,
	stdout, stderr io.Writer,
	build func(context.Context, *taskSession, core.PriorityVocabulary) (priorityPlan, error),
) error {
	session, err := openTaskSession(ctx, cwd, noSync, true, stderr)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}
	before := session.service.Priorities
	plan, err := build(ctx, session, before)
	if err != nil {
		return err
	}

	written, err := session.repository.WriteConfigOperation(
		ctx, session.config, core.CryptoULIDSource{}, plan.operations, priorityCommitSubject(plan))
	if err != nil {
		return priorityWriteError(err)
	}
	session.publishConfig(ctx)

	after := written.PriorityVocabulary()
	result := priorityMutationResult{
		Change: plan.change,
		Vocabulary: priorityVocabularyView{
			Head:       written.Head,
			Seeded:     true,
			Default:    after.Default(),
			Priorities: priorityViews(after, nil),
		},
		Tasks:   plan.tasks,
		Inverse: priorityChangeInverse(before, plan.operations),
	}
	if position := result.Change.Position; position != nil {
		position.Order = after.Order(plan.change.Priority) + 1
	}
	// The priorities this write produced, not the ones the session opened with,
	// and the statuses the same refresh settled on. regenerateGuidelines
	// documents both sections, so a priority change that let the statuses
	// default would overwrite a project's configured columns with the built-in
	// set — the mirror of the mistake that function's own comment describes.
	docs, docsErr := regenerateGuidelines(session, session.service.Vocabulary, after, noDocs)
	result.Docs = docs
	writePriorityMutation(stdout, stderr, command, result, session, docsErr, jsonMode)
	return nil
}

// priorityReadService builds a read-only service on a repository that is
// already open, with both vocabularies, so a priority command holds one
// projection handle rather than two and every task it lists has had its stored
// priority resolved through this project's own chains.
func priorityReadService(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	vocabulary core.Vocabulary,
	priorities core.PriorityVocabulary,
) (core.Service, error) {
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	return core.Service{
		Config:     config,
		Vocabulary: vocabulary,
		Priorities: priorities,
		Reader:     store,
		History:    store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
	}, nil
}

// priorityTaskCensus counts the active tasks each priority holds, and collects
// the stored values that resolve to no priority at all.
//
// It is one pass rather than a filter per priority for statusTaskCensus's
// reason: a task whose stored priority resolves nowhere is invisible to every
// count and is exactly the task somebody has to act on, so the census that
// produces the counts is also what finds it.
func priorityTaskCensus(
	vocabulary core.PriorityVocabulary,
	tasks []core.Task,
) (map[core.Priority]int, []unresolvedPriorityView) {
	counts := make(map[core.Priority]int, len(vocabulary.Definitions()))
	unresolved := make(map[core.Priority][]string)
	for _, task := range tasks {
		if vocabulary.Has(task.Priority) {
			counts[task.Priority]++
			continue
		}
		unresolved[task.Priority] = append(unresolved[task.Priority], task.ID)
	}
	names := make([]core.Priority, 0, len(unresolved))
	for priority := range unresolved {
		names = append(names, priority)
	}
	sort.Slice(names, func(left, right int) bool { return names[left] < names[right] })
	views := make([]unresolvedPriorityView, 0, len(names))
	for _, priority := range names {
		ids := unresolved[priority]
		sort.Strings(ids)
		// The count is of everything; the IDs are the first few of it. Sorting
		// before the cut is what makes the sample the same sample on every
		// clone rather than whatever order the projection happened to list.
		total := len(ids)
		views = append(views, unresolvedPriorityView{
			Priority: priority,
			Tasks:    total,
			TaskIDs:  ids[:min(total, maxUnresolvedTaskIDs)],
		})
	}
	return counts, views
}

func priorityViews(vocabulary core.PriorityVocabulary, counts map[core.Priority]int) []priorityView {
	definitions := vocabulary.Definitions()
	views := make([]priorityView, 0, len(definitions))
	for index, definition := range definitions {
		view := priorityView{
			Priority: definition.Priority,
			Label:    definition.Label,
			Tags:     definition.Tags,
			Color:    definition.Color,
			Order:    index + 1,
		}
		if counts != nil {
			count := counts[definition.Priority]
			view.Tasks = &count
		}
		views = append(views, view)
	}
	return views
}

func retiredPriorityViews(document core.PriorityDocument, at map[core.Priority]time.Time) []retiredPriorityView {
	views := make([]retiredPriorityView, 0, len(document.Aliases)+len(document.Retired))
	for _, alias := range document.Aliases {
		views = append(views, retiredPriorityView{
			Priority: alias.From, Becomes: alias.To, Operation: core.ConfigPriorityRename,
			At: priorityForwardingTimestamp(at, alias.From),
		})
	}
	for _, entry := range document.Retired {
		views = append(views, retiredPriorityView{
			Priority: entry.Priority, Becomes: entry.Destination, Operation: core.ConfigPriorityRemove,
			At: priorityForwardingTimestamp(at, entry.Priority),
		})
	}
	sort.Slice(views, func(left, right int) bool { return views[left].Priority < views[right].Priority })
	if len(views) == 0 {
		return nil
	}
	return views
}

func priorityForwardingTimestamp(at map[core.Priority]time.Time, priority core.Priority) string {
	when, found := at[priority]
	if !found {
		return ""
	}
	return when.UTC().Format(time.RFC3339)
}

// priorityForwardingTimes records when each retired priority stopped being
// live. The last recorded forwarding wins, because a value can be retired more
// than once: adding a name back deletes its forwarding pointer, and retiring it
// again writes a new one.
func priorityForwardingTimes(ledger configLedgerWindow) map[core.Priority]time.Time {
	times := make(map[core.Priority]time.Time)
	for _, commit := range ledger.Commits {
		for _, operation := range commit.Pack.Operations {
			switch operation.Type {
			case core.ConfigPriorityRename:
				times[operation.PriorityFrom] = commit.Pack.WallTime
			case core.ConfigPriorityRemove:
				times[operation.Priority] = commit.Pack.WallTime
			}
		}
	}
	return times
}

// requireLivePriority resolves a priority the caller named, and explains a
// value that is no longer live rather than reporting it missing.
//
// The chain is the whole point, exactly as it is in requireLiveStatus: a
// teammate renamed `high` to `urgent` last week, and typing `high` here is not
// a mistake worth a bare "not found" but a value that used to be right and
// whose replacement this clone can name.
func requireLivePriority(
	ctx context.Context,
	scope priorityScope,
	vocabulary core.PriorityVocabulary,
	priority core.Priority,
) (core.Priority, error) {
	if err := core.ValidatePriorityToken(priority); err != nil {
		return "", err
	}
	if vocabulary.Has(priority) {
		return priority, nil
	}
	via, operation, forwarded := vocabulary.Forwarding(priority)
	if !forwarded {
		return "", core.Errorf(core.CategoryNotFound,
			"no priority %q in this project; the priorities are: %s", priority, priorityNameList(vocabulary))
	}
	resolved, _ := vocabulary.Resolve(priority)
	return "", core.Errorf(core.CategoryNotFound, "no priority %q; it was %s %q%s%s",
		priority, forwardingVerb(operation), via,
		priorityForwardedOn(ctx, scope, priority), priorityChainClause(via, resolved))
}

// priorityForwardedOn dates a forwarding from the ledger, and says nothing when
// it cannot. Reading the ledger for it is affordable here because this path has
// already failed.
func priorityForwardedOn(ctx context.Context, scope priorityScope, priority core.Priority) string {
	ledger, err := readConfigLedgerWindow(ctx, scope.repository, scope.config, maxDatedConfigCommits)
	if err != nil || !ledger.Found {
		return ""
	}
	when, dated := priorityForwardingTimes(ledger)[priority]
	if !dated {
		return ""
	}
	return " on " + when.UTC().Format("2006-01-02")
}

func priorityNameList(vocabulary core.PriorityVocabulary) string {
	definitions := vocabulary.Definitions()
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, string(definition.Priority))
	}
	return strings.Join(names, ", ")
}

// parsePriorityTag turns one --tag value into a role, refusing an unknown one
// here rather than letting the operation document call it corrupt data.
//
// It takes one value where parseStatusTags takes a list, because a priority
// carries one role and `priority tag` gives or moves that one role. The fold
// transfers the default itself — see priorityTagOperation — so there is no set
// to reconcile and nothing for a repeated flag to mean.
func parsePriorityTag(value string) (core.PriorityTag, error) {
	tag := core.PriorityTag(value)
	if err := core.ValidatePriorityTag(tag); err != nil {
		return "", priorityTagError(err)
	}
	return tag, nil
}

// priorityTagError names the tags that exist, because a caller who typed
// another has no other way to learn them without reading help.
func priorityTagError(err error) error {
	names := make([]string, 0, len(core.PriorityTags()))
	for _, tag := range core.PriorityTags() {
		names = append(names, string(tag))
	}
	return core.Errorf(core.CategoryValidation, "%s; the tags are: %s",
		publicErrorMessage(err), strings.Join(names, ", "))
}

func containsPriorityTag(tags []core.PriorityTag, wanted core.PriorityTag) bool {
	for _, tag := range tags {
		if tag == wanted {
			return true
		}
	}
	return false
}

// priorityDefinition reads one priority's definition, reporting whether the
// vocabulary defines it, so that no caller indexes a slice with Order's
// past-the-end answer for a priority it does not.
func priorityDefinition(
	vocabulary core.PriorityVocabulary,
	priority core.Priority,
) (core.PriorityDefinition, bool) {
	definitions := vocabulary.Definitions()
	index := vocabulary.Order(priority)
	if index >= len(definitions) || definitions[index].Priority != priority {
		return core.PriorityDefinition{}, false
	}
	return definitions[index], true
}

// priorityTags reads a live priority's tag set. Every caller has already
// resolved the priority, so an absent one comes back with no tags rather than
// with a panic.
func priorityTags(vocabulary core.PriorityVocabulary, priority core.Priority) []core.PriorityTag {
	definition, _ := priorityDefinition(vocabulary, priority)
	return definition.Tags
}

// priorityOperationSubject names the priority an operation is about, whichever
// member its type carries it in.
func priorityOperationSubject(operation core.ConfigOperation) core.Priority {
	switch operation.Type {
	case core.ConfigPriorityAdd:
		return operation.PriorityName
	case core.ConfigPriorityRename:
		return operation.PriorityFrom
	default:
		return operation.Priority
	}
}

// removalPriorityTaskCounts prices a removal in the term the person running it
// cares about: how many tasks move.
//
// It is counted before the write, against the vocabulary the removal is about
// to change, because afterwards the tasks resolve into the destination and the
// question can no longer be asked. There is no claimable-after counterpart;
// see priorityTaskCounts for why there never will be.
func removalPriorityTaskCounts(
	ctx context.Context,
	scope priorityScope,
	subject core.Priority,
) (priorityTaskCounts, error) {
	tasks, err := scope.service.List(ctx, core.ListFilter{})
	if err != nil {
		return priorityTaskCounts{}, err
	}
	counts := priorityTaskCounts{}
	for _, task := range tasks {
		if task.Priority == subject {
			counts.Affected++
		}
	}
	return counts, nil
}

// missingPriorityRemovalDestination refuses `priority delete` with no --into,
// naming the priorities this project has so the retry is one edit away.
//
// It is refused before the session opens rather than inside the change, for the
// reason missingRemovalDestination is: an invocation nobody could have meant
// should not first fetch from origin. Prompting is not an option — agents run
// this command, and a prompt would hang one.
func missingPriorityRemovalDestination(ctx context.Context, cwd string, stderr io.Writer) error {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	return core.Errorf(core.CategoryInvocation,
		"priority delete requires --into <priority>, naming where the removed priority's tasks belong; "+
			"this project's priorities are: %s", priorityNameList(state.Priorities))
}

// priorityCommitSubject writes what the ledger's `git log` says about this
// change, in the same voice the status verbs and the seeded root use.
func priorityCommitSubject(plan priorityPlan) string {
	return "workbook: " + plan.change.summary()
}

// summary renders a change as one clause, for a commit subject and for the
// text-mode heading.
func (change priorityChange) summary() string {
	switch change.Operation {
	case "add":
		return fmt.Sprintf("add priority %s", change.Priority)
	case "rename":
		return fmt.Sprintf("rename priority %s to %s", change.From, change.Priority)
	case "label":
		return fmt.Sprintf("relabel priority %s", change.Priority)
	case "move":
		return fmt.Sprintf("move priority %s", change.Priority)
	case "tag":
		return fmt.Sprintf("tag priority %s", change.Priority)
	case "delete":
		return fmt.Sprintf("remove priority %s into %s", change.Priority, change.Into)
	case "color":
		if change.Color != nil && change.Color.To == "" {
			return fmt.Sprintf("clear the color of priority %s", change.Priority)
		}
		return fmt.Sprintf("recolor priority %s", change.Priority)
	default:
		return "update project configuration"
	}
}

// priorityWriteError explains the failure a configuration write has that a task
// write does not phrase the same way.
//
// A lost compare-and-swap is the one worth rewording: gitstore says the ledger
// changed concurrently, which is accurate and says nothing about what to do.
// The answer is always to run the same command again, and saying so is the
// difference between an error a script retries and one it reports.
func priorityWriteError(err error) error {
	if core.CategoryOf(err) == core.CategoryStaleWrite {
		return core.Wrap(core.CategoryStaleWrite,
			"another process changed this project's priorities while this command was writing; "+
				"nothing was recorded, so run it again",
			err)
	}
	return err
}

// priorityCommand renders a runnable command line, quoting only what a shell
// would otherwise split or interpret. It shares quoteStatusArgument, because
// the rule is about a POSIX shell rather than about either vocabulary.
func priorityCommand(parts ...string) string {
	quoted := make([]string, 0, len(parts)+2)
	quoted = append(quoted, "workbook", "priority")
	for _, part := range parts {
		quoted = append(quoted, quoteStatusArgument(part))
	}
	return strings.Join(quoted, " ")
}

// priorityChangeInverse is the verb path's inverse: the same computation the
// log performs over the same operations, with nothing left for a mutating
// command to add. A change whose inverse cannot be expressed reports an empty
// one rather than a command that would not run.
func priorityChangeInverse(before core.PriorityVocabulary, operations []core.ConfigOperation) priorityInverse {
	if inverse := priorityPackInverse(configBefore{priorities: before}, operations); inverse != nil {
		return *inverse
	}
	return priorityInverse{}
}

// priorityPackInverse is the command that undoes one recorded pack's priority
// change, and the only place a priority inverse is decided.
//
// It answers about the first priority operation in the pack rather than about
// operations[0], which is the one way it departs from statusPackInverse: a
// ledger commit can carry a project's built-in priorities backfilled ahead of
// its first priority change — see gitstore's prependBuiltInPriorities — and
// answering about the first operation would describe the backfill instead of
// the change somebody made.
func priorityPackInverse(before configBefore, operations []core.ConfigOperation) *priorityInverse {
	operation, found := subjectPriorityOperation(operations)
	if !found {
		return nil
	}
	vocabulary := before.priorities
	subject := priorityOperationSubject(operation)
	switch operation.Type {
	case core.ConfigPriorityAdd:
		return priorityAddInverse(vocabulary, operation)
	case core.ConfigPriorityRename:
		return priorityRenameInverse(vocabulary, operation, operations)
	case core.ConfigPriorityRelabel:
		definition, live := priorityDefinition(vocabulary, subject)
		if !live {
			return nil
		}
		return &priorityInverse{
			Command: priorityCommand("label", string(subject), definition.Label),
			Exact:   true,
		}
	case core.ConfigPriorityReorder:
		return priorityReorderInverse(vocabulary, subject, operations)
	case core.ConfigPriorityTag:
		return priorityTagInverse(vocabulary, operation)
	case core.ConfigPriorityUntag:
		return &priorityInverse{
			Command: priorityCommand("tag", string(subject), "--tag", string(operation.PriorityTag)),
			Exact:   true,
		}
	case core.ConfigPriorityRecolor:
		return priorityRecolorInverse(vocabulary, subject)
	case core.ConfigPriorityRemove:
		return priorityRemoveInverse(vocabulary, operation, subject)
	default:
		return nil
	}
}

// subjectPriorityOperation is the operation in a pack that the commit is about,
// for the priority section: the first one that touches priorities.
func subjectPriorityOperation(operations []core.ConfigOperation) (core.ConfigOperation, bool) {
	for _, operation := range operations {
		if operation.Type.TouchesPriorities() {
			return operation, true
		}
	}
	return core.ConfigOperation{}, false
}

// priorityAddInverse removes what an add defined, and names where the tasks
// that have since been filed under it go.
//
// It is never exact, and the reason is worth saying rather than implying: the
// priority is empty when it is added and need not be when it is removed, so the
// inverse moves tasks the add never touched.
func priorityAddInverse(before core.PriorityVocabulary, operation core.ConfigOperation) *priorityInverse {
	destination := before.Default()
	if _, live := priorityDefinition(before, destination); !live {
		return nil
	}
	removal := priorityCommand("delete", string(operation.PriorityName), "--into", string(destination))
	if !containsPriorityTag(operation.PriorityTags, core.PriorityTagDefault) {
		return &priorityInverse{
			Command: removal,
			Note: fmt.Sprintf("tasks filed under %q since are forwarded to %q",
				operation.PriorityName, destination),
		}
	}
	// The priority holds the default tag, and removing the priority that holds
	// it is refused outright. So the command is the transfer that makes the
	// removal possible, and the removal is the second step — in that order,
	// because printing a command that exits 5 would be worse than printing
	// nothing.
	return &priorityInverse{
		Command: priorityCommand("tag", string(destination), "--tag", string(core.PriorityTagDefault)),
		Note: fmt.Sprintf("that returns the default tag to %q, which %q holds; %s then removes the priority, "+
			"forwarding the tasks filed under it since",
			destination, operation.PriorityName, removal),
	}
}

// priorityRenameInverse renames back, and names the label when the same commit
// moved it.
//
// Without that clause the inverse is a lie in both directions the label can
// move: a rename that re-derived `High` into `Urgent` would leave `Urgent` on a
// priority called `high`, and a rename given an explicit `--label` would leave
// that label behind.
func priorityRenameInverse(
	before core.PriorityVocabulary,
	operation core.ConfigOperation,
	pack []core.ConfigOperation,
) *priorityInverse {
	command := priorityCommand("rename", string(operation.PriorityTo), string(operation.PriorityFrom))
	if priorityRelabelled(pack, operation.PriorityTo) {
		definition, live := priorityDefinition(before, operation.PriorityFrom)
		if !live {
			return nil
		}
		command += " --label " + quoteStatusArgument(definition.Label)
	}
	return &priorityInverse{Command: command, Exact: true}
}

// priorityRelabelled reports whether a pack also set the priority's display
// label, which is how a rename learns that its inverse has to restore one.
func priorityRelabelled(pack []core.ConfigOperation, priority core.Priority) bool {
	for _, operation := range pack {
		if operation.Type == core.ConfigPriorityRelabel && operation.Priority == priority {
			return true
		}
	}
	return false
}

// priorityTagInverse gives the default tag back to the priority that held it.
//
// Exactly one priority carries the tag, so a commit that took it also took it
// from somebody, and the fold performs that transfer inside the single tag
// operation. Naming the previous holder is therefore both the whole inverse and
// a command valid on its own — where restoring only the subject's old set would
// leave the project with no default at all, which the authoring gate refuses
// outright.
//
// A tag operation that describes no transfer has no inverse, and reports none.
// Nothing takes a role away from a priority: the group has nine verbs and
// `untag` is not among them. So the alternative is naming a command that cannot
// be run, which is worse than saying nothing — the whole point of printing an
// inverse is that somebody can paste it. An absent inverse is how this file
// already says a change cannot be expressed: the log omits the line and
// priorityChangeInverse renders the empty one.
func priorityTagInverse(before core.PriorityVocabulary, operation core.ConfigOperation) *priorityInverse {
	subject := priorityOperationSubject(operation)
	if operation.PriorityTag != core.PriorityTagDefault {
		// A role this build's vocabulary does not contain, so the operation was
		// authored by a build whose vocabulary is wider. It still folds and the
		// log still describes it; but this build knows neither that role's arity
		// rule nor whom the tag was taken from, and will not name a command in a
		// vocabulary it does not have.
		return nil
	}
	previous := before.Default()
	if previous == "" || previous == subject {
		// The tag took nothing from anybody. Either no priority carried it — a
		// state the authoring gate refuses to write, so such an operation
		// reached the log as somebody else's history — and undoing it would put
		// the project back to having no default, which nothing here can author;
		// or the subject already carried it, making the operation a no-op whose
		// inverse is to do nothing. Neither is a command.
		return nil
	}
	return &priorityInverse{
		Command: priorityCommand("tag", string(previous), "--tag", string(core.PriorityTagDefault)),
		Exact:   true,
	}
}

// priorityReorderInverse puts a priority back between the neighbours it left.
//
// It names the priority that preceded it, or the one that followed when it was
// the most urgent, because those are the two ways to describe a position
// without depending on a rank that the reorder itself replaced.
func priorityReorderInverse(
	before core.PriorityVocabulary,
	subject core.Priority,
	pack []core.ConfigOperation,
) *priorityInverse {
	definitions := before.Definitions()
	index := before.Order(subject)
	if index >= len(definitions) || definitions[index].Priority != subject {
		return nil
	}
	inverse := &priorityInverse{Exact: true}
	if index == 0 {
		if len(definitions) < 2 {
			return nil
		}
		inverse.Command = priorityCommand("move", string(subject), "--before", string(definitions[1].Priority))
	} else {
		inverse.Command = priorityCommand("move", string(subject), "--after", string(definitions[index-1].Priority))
	}
	// A drag on the board can set a whole order in one commit, which moves as
	// many priorities as the drag disturbed. One move puts one of them back, so
	// the inverse says how many it does not.
	if moved := priorityReorderedIn(pack); moved > 1 {
		inverse.Exact = false
		inverse.Note = fmt.Sprintf("this commit moved %d priorities; that restores %q alone", moved, subject)
	}
	return inverse
}

// priorityReorderedIn counts the priorities a pack moved.
func priorityReorderedIn(pack []core.ConfigOperation) int {
	moved := 0
	for _, operation := range pack {
		if operation.Type == core.ConfigPriorityReorder {
			moved++
		}
	}
	return moved
}

// priorityRecolorInverse restores the ink the recolor replaced, including the
// absence of one: `workbook priority color <priority>` with no value is how a
// stored color is cleared, so an inverse that restores "nothing was stored" is
// the same command with nothing after it.
func priorityRecolorInverse(before core.PriorityVocabulary, subject core.Priority) *priorityInverse {
	definition, live := priorityDefinition(before, subject)
	if !live {
		return nil
	}
	if definition.Color == "" {
		return &priorityInverse{Command: priorityCommand("color", string(subject)), Exact: true}
	}
	return &priorityInverse{
		Command: priorityCommand("color", string(subject), definition.Color),
		Exact:   true,
	}
}

// priorityRemoveInverse defines the priority again where it was, with the label
// and the color it had.
//
// It is never exact, and the note says exactly which tasks come back. Defining
// the name again drops the forwarding pointer — an add deletes the alias and
// the retirement for the name it defines — so every task still stored under the
// old value reads as being at that priority again. What does not come back is a
// task some later write settled: correct-on-touch rewrote its stored value to
// the destination, and no configuration change can find it again.
//
// A color is restored by a second command rather than a flag, because `priority
// add` has no color flag: color is the one field that can be cleared, which is
// why it is its own verb and its own operation.
func priorityRemoveInverse(
	before core.PriorityVocabulary,
	operation core.ConfigOperation,
	subject core.Priority,
) *priorityInverse {
	definitions := before.Definitions()
	index := before.Order(subject)
	if index >= len(definitions) || definitions[index].Priority != subject {
		return nil
	}
	definition := definitions[index]
	parts := []string{"add", string(definition.Priority)}
	if index == 0 {
		if len(definitions) > 1 {
			parts = append(parts, "--before", string(definitions[1].Priority))
		}
	} else {
		parts = append(parts, "--after", string(definitions[index-1].Priority))
	}
	parts = append(parts, "--label", definition.Label)
	note := fmt.Sprintf(
		"tasks still stored under %q return to it, because defining the name again drops the forwarding "+
			"pointer; tasks a later write settled into %q stay there",
		definition.Priority, operation.PriorityDestination)
	if definition.Color != "" {
		note += "; " + priorityCommand("color", string(definition.Priority), definition.Color) +
			" restores its color"
	}
	return &priorityInverse{Command: priorityCommand(parts...), Note: note}
}

// priorityOperationSummary says what one recorded priority operation did, and
// reports whether the type was one of this section's at all.
//
// The second return is what lets configOperationSummary delegate here without
// this function having to know what a status operation is: a type it does not
// word is not its to word.
func priorityOperationSummary(operation core.ConfigOperation) (string, bool) {
	switch operation.Type {
	case core.ConfigPriorityAdd:
		summary := fmt.Sprintf("added priority %s", operation.PriorityName)
		if len(operation.PriorityTags) > 0 {
			summary += " tagged " + joinPriorityTags(operation.PriorityTags)
		}
		return summary, true
	case core.ConfigPriorityRename:
		return fmt.Sprintf("renamed priority %s to %s", operation.PriorityFrom, operation.PriorityTo), true
	case core.ConfigPriorityRelabel:
		return fmt.Sprintf("labelled priority %s %q", operation.Priority, operation.Label), true
	case core.ConfigPriorityReorder:
		return fmt.Sprintf("moved priority %s", operation.Priority), true
	case core.ConfigPriorityTag:
		return fmt.Sprintf("tagged priority %s %s", operation.Priority, operation.PriorityTag), true
	case core.ConfigPriorityUntag:
		return fmt.Sprintf("untagged priority %s %s", operation.Priority, operation.PriorityTag), true
	case core.ConfigPriorityRemove:
		return fmt.Sprintf("removed priority %s into %s", operation.Priority, operation.PriorityDestination), true
	case core.ConfigPriorityRecolor:
		// Quoted for the reason the relabel arm quotes a label: a value
		// somebody typed leaves a reader to guess where the sentence ends and
		// the value begins. An empty one is a clearing rather than a color.
		if operation.Value == "" {
			return fmt.Sprintf("cleared the color of priority %s", operation.Priority), true
		}
		return fmt.Sprintf("colored priority %s %q", operation.Priority, operation.Value), true
	default:
		return "", false
	}
}

// priorityPackSummary says what one recorded commit did to the priorities, in
// one clause.
//
// It counts only this section's operations, so a commit that also changed a
// status does not report that column's edit as "+1 more change" in a log about
// priorities.
func priorityPackSummary(operations []core.ConfigOperation) string {
	subject, found := subjectPriorityOperation(operations)
	if !found {
		return "recorded nothing"
	}
	summary, _ := priorityOperationSummary(subject)
	if extra := priorityOperationCount(operations) - 1; extra > 0 {
		summary += fmt.Sprintf(" (+%d more priority change(s) in this commit)", extra)
	}
	return summary
}

func priorityOperationCount(operations []core.ConfigOperation) int {
	count := 0
	for _, operation := range operations {
		if operation.Type.TouchesPriorities() {
			count++
		}
	}
	return count
}

func joinPriorityTags(tags []core.PriorityTag) string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, string(tag))
	}
	return strings.Join(names, ",")
}

func priorityTagsLine(tags []core.PriorityTag) string {
	if len(tags) == 0 {
		return ""
	}
	return joinPriorityTags(tags)
}

// writePriorityMutation reports a priority change on both surfaces.
//
// The JSON envelope carries the same members a task mutation's does — the sync
// report, the conflict lists, the warnings — because a caller that already
// parses one should not need a second parser for the other.
func writePriorityMutation(
	stdout, stderr io.Writer,
	command string,
	result priorityMutationResult,
	session *taskSession,
	docsErr error,
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the priority change was recorded locally, but " + session.report.Detail,
		})
	}
	warnings = append(warnings, docsWarning(result.Docs, docsErr)...)
	if jsonMode {
		writePriorityEnvelope(stdout, command, result, session, warnings)
		return
	}
	writePriorityChange(stdout, result)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
}

func writePriorityEnvelope(
	stdout io.Writer,
	command string,
	result priorityMutationResult,
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

// writePriorityChange renders a change as a heading and its details, the shape
// every other structured text block in this CLI uses: one column-zero line that
// cannot be forged from inside a value, then tab-indented fields.
func writePriorityChange(output io.Writer, result priorityMutationResult) {
	change := result.Change
	fmt.Fprintf(output, "Priority:\t%s\t%s\n", change.Operation, change.Priority)
	if change.From != "" {
		fmt.Fprintf(output, "\tfrom:\t%s\n", change.From)
	}
	if change.Into != "" {
		fmt.Fprintf(output, "\tinto:\t%s\n", change.Into)
	}
	if change.Label != nil {
		fmt.Fprintf(output, "\tlabel:\t%s\n", priorityLabelLine(change))
	}
	if change.Color != nil {
		fmt.Fprintf(output, "\tcolor:\t%s\n", priorityColorLine(*change.Color))
	}
	if position := change.Position; position != nil {
		fmt.Fprintf(output, "\tposition:\t%s\n", priorityPositionLine(*position, len(result.Vocabulary.Priorities)))
	}
	if change.Tags != nil {
		fmt.Fprintf(output, "\ttags:\t%s\n", priorityTagsSummaryLine(change.Tags))
	}
	if change.DefaultFrom != "" {
		fmt.Fprintf(output, "\tdefault:\t%s → %s\n", change.DefaultFrom, change.Priority)
	}
	if result.Tasks.Affected > 0 {
		fmt.Fprintf(output, "\ttasks:\t%d affected\n", result.Tasks.Affected)
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

// priorityLabelLine renders the label the way the derived rule has to be read:
// an arrow when it moved, and which of the two rules moved it.
func priorityLabelLine(change priorityChange) string {
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

// priorityColorLine renders the ink, naming a cleared one rather than printing
// an empty field: "none" is a state, and a blank is a missing answer.
func priorityColorLine(color priorityColor) string {
	rendered := color.To
	if rendered == "" {
		rendered = "none (derived from its position)"
	}
	if color.From != "" && color.From != color.To {
		return color.From + " → " + rendered
	}
	return rendered
}

func priorityPositionLine(position priorityPosition, total int) string {
	switch {
	case position.Before != "":
		return fmt.Sprintf("before %s (%d of %d)", position.Before, position.Order, total)
	case position.After != "":
		return fmt.Sprintf("after %s (%d of %d)", position.After, position.Order, total)
	default:
		return fmt.Sprintf("last (%d of %d)", position.Order, total)
	}
}

// priorityTagsSummaryLine renders a change's tag set for the text block, where
// an empty set is a stated "none" rather than the blank cell the table uses.
func priorityTagsSummaryLine(tags []core.PriorityTag) string {
	if len(tags) == 0 {
		return "none"
	}
	return joinPriorityTags(tags)
}
