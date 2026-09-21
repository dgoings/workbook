package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
)

// keyChange is what one key command did, in the shape every mutating key
// envelope carries.
//
// One shape for three verbs, for the reason statusChange is one shape for
// seven: a caller that reads `change.operation` and `change.key` can handle a
// verb it has never heard of, and a verb that grows a member does not become a
// new document. The members a verb does not use are omitted rather than zeroed,
// so the presence of `from` is what says the current key moved.
type keyChange struct {
	// Operation is the verb: add, current, or retire.
	Operation string `json:"operation"`
	// Key is the subject.
	Key string `json:"key"`
	// From names the key that gave up the current role, when this change moved
	// it. Exactly one key is current, so taking it is always also giving it up.
	From string `json:"from,omitempty"`
	// Reactivated reports an add that brought a retired key back rather than
	// adding a new one, which is the one case where `add` changes nothing about
	// the order.
	Reactivated bool `json:"reactivated,omitempty"`
	// State is what the subject is after the change.
	State core.KeyState `json:"state"`
}

// keyView is one key as the envelopes present it.
type keyView struct {
	Key   string        `json:"key"`
	State core.KeyState `json:"state"`
	// Current marks the one key a new task is minted under. It is a member of
	// its own rather than a third KeyState because being current is not a state
	// a key set stores: the document names the current key once, and a reader
	// that folded it into the state word would have to take it back apart to
	// write the section again.
	Current bool `json:"current"`
	// Tasks counts the tasks whose ID carries this key. It is a pointer for the
	// reason statusView.Tasks is: a mutating verb does not count them, because
	// a key change is not a task read and paying for the projection on every
	// write to report a number nobody asked for would be the wrong trade.
	Tasks *int `json:"tasks,omitempty"`
}

// keySetView is the project's keys as they stand after a change, and the whole
// of what `workbook key list` answers.
//
// It is one type for both because there is nothing a listing of keys says that
// a change does not: keys have no retired-forwarding table, no value that
// resolves to nothing, and no ceiling advisory, which are the three things that
// make statusListResult more than a vocabularyView.
type keySetView struct {
	Head string `json:"head"`
	// Seeded reports that a configuration ledger recorded these keys. False
	// means this is the founding key alone, which is every project until
	// somebody adds a second one.
	Seeded  bool      `json:"seeded"`
	Current string    `json:"current"`
	Keys    []keyView `json:"keys"`
}

// keyMutationResult is the data member of every mutating key envelope.
type keyMutationResult struct {
	Change  keyChange     `json:"change"`
	Keys    keySetView    `json:"keys"`
	Inverse statusInverse `json:"inverse"`
	// Docs reports what happened to the generated documentation this change
	// invalidated, in the shape `workbook setup` and `workbook status`/`workbook
	// priority` already report. The generated guidelines name the current key
	// (`RenderGuidelines`'s "Task ID prefix" row), so `key add --current`, `key
	// current`, and `key retire` — every verb that can move the current key or
	// change the set the row and the active-keys sentence describe — invalidate
	// them exactly as a status or priority change does. Docs is omitted when
	// --no-docs skipped the regeneration, which is the same distinction setup
	// draws between "nothing was managed" and "these files were".
	Docs *agentdocs.Report `json:"docs,omitempty"`
}

// keyLogResult mirrors statusLogResult and priorityLogResult, because it answers
// the same question about a third section of the same ledger.
type keyLogResult struct {
	Showing   int                     `json:"showing"`
	Total     int                     `json:"total"`
	Entries   []keyLogEntry           `json:"entries"`
	Truncated *core.HistoryTruncation `json:"truncated,omitempty"`
}

type keyLogEntry struct {
	Commit      string    `json:"commit"`
	OperationID string    `json:"operationId"`
	WallTime    time.Time `json:"wallTime"`
	Actor       string    `json:"actor"`
	// Operation is the durable operation type, because this is the ledger's own
	// record rather than a report of a command somebody ran.
	Operation core.ConfigOperationType `json:"operation"`
	Summary   string                   `json:"summary"`
	// Collapsed counts the key operations this commit recorded beyond the one
	// the entry names. It counts key operations alone, and it counts only the
	// authored ones: a commit that also changed a status is another section's
	// news, and the founding key the store records ahead of a project's first
	// key change is bookkeeping rather than a key somebody added.
	Collapsed int            `json:"collapsed"`
	Inverse   *statusInverse `json:"inverse,omitempty"`
}

// keySubcommands lists the verbs, for the error a bare `workbook key`
// produces. It is derived from the help schema so a verb cannot be added
// without the refusal learning about it.
func keySubcommands() []string {
	return commandSchemas["key"].SubcommandOrder
}

// runKey dispatches the group. Every verb the schema declares is named here, so
// a verb going missing is a build failure rather than a silently lost command.
func runKey(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := firstKeyArgument(args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "list":
		return runKeyList(ctx, args, cwd, stdout, stderr)
	case "add":
		return runKeyAdd(ctx, args, cwd, stdout, stderr)
	case "current":
		return runKeyCurrent(ctx, args, cwd, stdout, stderr)
	case "retire":
		return runKeyRetire(ctx, args, cwd, stdout, stderr)
	case "log":
		return runKeyLog(ctx, args, cwd, stdout, stderr)
	default:
		return core.Errorf(core.CategoryInvocation, "unknown key command %q; %s",
			subcommand, keyCommandList())
	}
}

// firstKeyArgument takes the subcommand, and answers the one mistake worth
// answering specifically.
//
// `workbook key WB-01J...` is not a typo, it is a different command: a caller —
// usually an agent — reaching for a task and finding a verb family. This is
// firstStatusArgument's rule and shares its task-reference test, because the
// families are mistyped the same way; here it is also the likelier mistake,
// since a task ID is the one place a reader meets a key at all.
func firstKeyArgument(args []string) (string, []string, error) {
	if len(args) == 0 || !isRequiredFirstArgument(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation, "key takes a subcommand; %s", keyCommandList())
	}
	if _, known := commandMetadataFor([]string{"key", args[0]}); !known && looksLikeTaskReference(args[0]) {
		return "", nil, core.Errorf(core.CategoryInvocation,
			"workbook key takes a subcommand; to read a task use: workbook show %s", args[0])
	}
	return args[0], args[1:], nil
}

func keyCommandList() string {
	return "the subcommands are " + strings.Join(keySubcommands(), ", ")
}

// runKeyList reads the project's keys without touching the network.
//
// It fetches nothing on purpose, exactly as `status list` and `priority list`
// do not: listing keys is a read, and a read that synchronized would make it
// slower and less predictable than `workbook list` for no gain — the ledger this
// clone holds is what every other command in this shell is already using.
func runKeyList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("key", "list")
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
	service, err := keyReadService(ctx, repository, config, state)
	if err != nil {
		return err
	}
	tasks, err := service.List(ctx, core.ListFilter{})
	if err != nil {
		return err
	}
	result := keySetView{
		Head:    state.Head,
		Seeded:  keysRecorded(config.Key, state.Keys),
		Current: state.Keys.Current(),
		Keys:    keyViews(state.Keys, keyTaskCensus(state.Keys, tasks)),
	}
	if *jsonMode {
		writeResult(stdout, "key list", result)
		return nil
	}
	writeKeyList(stdout, result)
	return nil
}

// keyReadService builds a read-only service on a repository that is already
// open, so a key command holds one projection handle rather than two.
//
// It is handed the whole vocabulary state rather than the keys alone. The
// listing resolves every task's stored status and priority through this
// project's own chains before counting it, and a service that took only the
// keys would count tasks the rest of this CLI describes differently.
func keyReadService(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	state gitstore.VocabularyState,
) (core.Service, error) {
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	return core.Service{
		Config:     config,
		Vocabulary: state.Vocabulary,
		Priorities: state.Priorities,
		Keys:       state.Keys,
		Reader:     store,
		History:    store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
	}, nil
}

// keysRecorded reports a project whose configuration ledger carries a key
// section of its own, rather than the founding key the set falls back to.
//
// It is derived from the set rather than read off the checkpoint because the
// two cannot disagree: gitstore back-fills the founding key into the same pack
// as a project's first key change, so a recorded section always names either a
// second key or a key that is not the founding one. A set that is exactly the
// founding key is the fallback whichever it came from, and reporting it as
// recorded would promise a reader a ledger entry that says nothing they cannot
// already see in the project identity.
func keysRecorded(founding string, keys core.KeySet) bool {
	definitions := keys.Keys()
	if len(definitions) != 1 {
		return true
	}
	return definitions[0].Key != founding
}

// keyTaskCensus counts the tasks each key has minted.
//
// There is no unresolved bucket, which is the one way this differs from the
// status and priority censuses: a task ID whose key this project does not have
// is not a task of this project's at all, so it never reaches a listing to be
// counted. Every task here parses, because ownership was decided at the
// boundary that produced it.
func keyTaskCensus(keys core.KeySet, tasks []core.Task) map[string]int {
	counts := make(map[string]int, len(keys.Keys()))
	for _, task := range tasks {
		if key, _, ok := keys.Parse(task.ID); ok {
			counts[key]++
		}
	}
	return counts
}

// keyViews renders the set in add order. counts nil leaves the task counts out,
// which is what a mutating verb wants.
func keyViews(keys core.KeySet, counts map[string]int) []keyView {
	definitions := keys.Keys()
	views := make([]keyView, 0, len(definitions))
	for _, definition := range definitions {
		state := core.KeyStateActive
		if definition.Retired {
			state = core.KeyStateRetired
		}
		view := keyView{
			Key:     definition.Key,
			State:   state,
			Current: definition.Key == keys.Current(),
		}
		if counts != nil {
			count := counts[definition.Key]
			view.Tasks = &count
		}
		views = append(views, view)
	}
	return views
}

// runKeyLog lists the recorded key changes, oldest first.
//
// It mirrors runPriorityLog exactly, including the one place both depart from
// `status log`: the window. `status log` bounds the ledger read itself, because
// every commit it delivers is an entry. A key change is a subsequence of the
// same ledger — a project can record forty status changes between two key ones —
// so a bound on commits read is not a bound on changes shown. The read is
// therefore the whole ledger and the bound is applied to what it found, which is
// what makes --limit mean "this many key changes" rather than "whatever was in
// the last N commits".
func runKeyLog(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("key", "log")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// The window is decided before anything is read. --limit is checked for
	// having been given at all rather than for being non-empty: `--limit=` sets
	// it to the empty string, which would otherwise slip past the conflict
	// check and let --all win silently.
	window := core.DefaultChangeLimit
	limited := false
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
			return core.Errorf(core.CategoryInvocation, "key log --limit must be a positive whole number")
		}
		window = parsed
	}
	if *all {
		window = 0
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	ledger, err := readConfigLedgerWindow(ctx, repository, config, 0)
	if err != nil {
		return err
	}
	result := buildKeyLog(ledger, window)
	if *jsonMode {
		writeResult(stdout, "key log", result)
		return nil
	}
	writeKeyLog(stdout, result, ledger.Found)
	return nil
}

// buildKeyLog renders a read of the ledger as the key change log.
//
// One entry is one commit that changed a key. A commit that changed only
// statuses or priorities is not this log's news and is skipped rather than shown
// with an empty summary: the sections share a ledger and do not share a history
// a reader is asking about.
//
// Total is the number of key commits the ledger holds, not the ledger's length,
// so "showing 10 of 12" counts the things this log lists.
func buildKeyLog(ledger configLedgerWindow, window int) keyLogResult {
	entries := make([]keyLogEntry, 0, len(ledger.Commits))
	for _, commit := range ledger.Commits {
		// Every member below reads the authored operations rather than the
		// recorded pack, because an entry is an account of what somebody ran.
		// The one commit where the two differ is a project's first key change,
		// which gitstore records together with the founding key every existing
		// task ID already carries; see authoredKeyOperations.
		authored := authoredKeyOperations(commit.Before.keys, commit.Pack.Operations)
		primary, found := subjectKeyOperation(authored)
		if !found {
			continue
		}
		entries = append(entries, keyLogEntry{
			Commit:      commit.Commit,
			OperationID: primary.ID,
			WallTime:    commit.Pack.WallTime,
			Actor:       commit.Pack.Actor.ID,
			Operation:   primary.Type,
			Summary:     keyPackSummary(authored),
			// The back-filled founding key is left out of this count as well as
			// out of the summary, and that is the honest number rather than a
			// convenient one: "+1 more key change" would claim the commit added
			// a key it did not. The project already had that key — every task ID
			// in it carries the key — and what the backfill changed is where it
			// is written down, which is not news about a key.
			Collapsed: keyOperationCount(authored) - 1,
			Inverse:   keyPackInverse(commit.Before.keys, commit.Pack.Operations),
		})
	}
	total := len(entries)
	if window > 0 && total > window {
		entries = entries[total-window:]
	}
	return keyLogResult{
		Total:     total,
		Showing:   len(entries),
		Entries:   entries,
		Truncated: ledger.Truncation,
	}
}

func runKeyAdd(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("key add", []string{"<key>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("key", "add")
	makeCurrent := flags.Bool("current", false, "also make it the key new tasks are minted under")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	key := values[0]
	if err := core.ValidateProjectKey(key); err != nil {
		return err
	}
	return runKeyMutation(ctx, cwd, "key add", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(keys core.KeySet) (keyPlan, error) { return planKeyAdd(keys, key, *makeCurrent) })
}

func runKeyCurrent(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("key current", []string{"<key>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("key", "current")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	key := values[0]
	if err := core.ValidateProjectKey(key); err != nil {
		return err
	}
	return runKeyMutation(ctx, cwd, "key current", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(keys core.KeySet) (keyPlan, error) { return planKeyCurrent(keys, key) })
}

func runKeyRetire(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	values, args, err := requiredArguments("key retire", []string{"<key>"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet("key", "retire")
	noSync := flags.Bool("no-sync", false, "skip synchronizing refs with origin")
	noDocs := flags.Bool("no-docs", false, "skip regenerating the generated guidelines")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	key := values[0]
	if err := core.ValidateProjectKey(key); err != nil {
		return err
	}
	return runKeyMutation(ctx, cwd, "key retire", *noSync, *noDocs, *jsonMode, stdout, stderr,
		func(keys core.KeySet) (keyPlan, error) { return planKeyRetire(keys, key) })
}

// keyPlan is one authored key change: the operations to record, and everything
// the envelope says about them that the operations alone do not.
//
// It carries nothing for the inverse, for the reason statusPlan carries
// nothing: everything an inverse needs is in the operations and in the key set
// they were authored against, which is what lets the log — which has only those
// two things — reach the same answer.
type keyPlan struct {
	operations []core.ConfigOperation
	change     keyChange
}

// runKeyMutation is the one path every key change takes.
//
// It is runStatusMutation with the key section at the center, and every step is
// there for the reason that function's comment gives: the same session, so the
// same fetch-before, the same watcher deferral, the same --no-sync and the same
// sync member; and the same publish-after of the configuration ledger, which
// has no task ref to name.
//
// It does regenerate the guidelines, through the same regenerateGuidelines
// status and priority mutations use. The generated guidelines name the current
// key in their "Task ID prefix" row, and every verb here can change what that
// row says — `add --current` and `current` move it, `retire` can take the last
// key a caller might expect it to stay at away — so a key change leaves the
// file exactly as stale as a status or priority change does, and skips the
// regeneration for the same reason and the same flag: --no-docs.
//
// The refresh between the fetch and the build is the step worth understanding
// rather than copying. The key set the fetch settled on is the one this change
// is authored against, which is what makes `key retire` land on a teammate's
// newer set rather than on the one this clone opened with — and what keeps two
// people who each add a key from refusing each other's.
func runKeyMutation(
	ctx context.Context,
	cwd string,
	command string,
	noSync, noDocs bool,
	jsonMode bool,
	stdout, stderr io.Writer,
	build func(core.KeySet) (keyPlan, error),
) error {
	session, err := openTaskSession(ctx, cwd, noSync, true, stderr)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}
	before := session.service.KeySet()
	plan, err := build(before)
	if err != nil {
		return err
	}

	written, err := session.repository.WriteConfigOperation(
		ctx, session.config, core.CryptoULIDSource{}, plan.operations, keyCommitSubject(plan))
	if err != nil {
		return keyWriteError(err)
	}
	session.publishConfig(ctx)

	after := written.KeySet(session.config.Key)
	result := keyMutationResult{
		Change: plan.change,
		Keys: keySetView{
			Head: written.Head,
			// A key change is exactly the write that records the section, so
			// there is nothing to derive here: the set below came out of the
			// ledger this command just appended to.
			Seeded:  true,
			Current: after.Current(),
			Keys:    keyViews(after, nil),
		},
		Inverse: keyInverse(before, plan.operations),
	}
	// The statuses and priorities the guidelines also document are unmoved by a
	// key change, so they travel off the session's own service exactly as
	// regenerateGuidelines's other two callers pass the half of the document
	// their own change did not touch. Keys is the fresh set this write just
	// produced, not the session's pre-write one, for the reason status and
	// priority mutations pass their own freshly-written vocabulary.
	docs, docsErr := regenerateGuidelines(session, session.service.Vocabulary, session.service.Priorities, after, noDocs)
	result.Docs = docs
	writeKeyMutation(stdout, stderr, command, result, session, docsErr, jsonMode)
	return nil
}

// planKeyAdd adds a key, or brings a retired one back.
//
// The refusals are the author's, not the fold's: the fold converges on a key
// that is already active by doing nothing, which is what makes a redelivered
// pack a no-op and two clones that both add NEW agree on one key. A person who
// typed it gets a sentence instead. See core's configKeys.applyAdd.
//
// The ceiling is asked here, ahead of everything, and it is asked of a set that
// already contains the founding key — gitstore back-fills that key into this
// very pack, and VocabularyState reports it for a project whose ledger records
// nothing, so counting the set counts what will be written.
// core.ValidateConfigAuthoring asks the same question at the boundary and is
// what actually holds the ceiling; this asks first so the answer is a sentence
// naming the verb that makes room.
func planKeyAdd(keys core.KeySet, key string, makeCurrent bool) (keyPlan, error) {
	if err := core.ValidateProjectKey(key); err != nil {
		return keyPlan{}, err
	}
	state, known := keys.State(key)
	switch {
	case known && state == core.KeyStateActive && keys.Current() == key:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"project key %q is already active and already current, so there is nothing to add", key)
	case known && state == core.KeyStateActive:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"project key %q is already active; to mint new tasks under it: workbook key current %s", key, key)
	case !known && len(keys.Keys())+1 > core.MaxProjectKeys:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"this project has %d keys and may have at most %d; retire one instead of adding another: workbook key retire <key>",
			len(keys.Keys()), core.MaxProjectKeys)
	}
	operations := []core.ConfigOperation{{Type: core.ConfigKeyAdd, Key: key}}
	change := keyChange{
		Operation:   "add",
		Key:         key,
		Reactivated: known,
		State:       core.KeyStateActive,
	}
	if makeCurrent {
		operations = append(operations, core.ConfigOperation{Type: core.ConfigKeyCurrent, Key: key})
		change.From = keys.Current()
	}
	return keyPlan{operations: operations, change: change}, nil
}

// planKeyCurrent moves the current key.
//
// An unknown key is answered with every key this project has, retired ones
// marked, because the likeliest reason somebody named a key that cannot become
// current is that they are looking at a key that was retired; a retired key is
// answered with the active ones, which are exactly what this verb accepts.
func planKeyCurrent(keys core.KeySet, key string) (keyPlan, error) {
	if err := core.ValidateProjectKey(key); err != nil {
		return keyPlan{}, err
	}
	state, known := keys.State(key)
	switch {
	case !known:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"no project key %q in this project; its keys are: %s", key, core.KeyNameList(keys))
	case state == core.KeyStateRetired:
		// core's own refusal, which names the active keys: one sentence about
		// minting under a retired key, wherever the attempt comes from.
		return keyPlan{}, keys.RequireActive(key)
	case keys.Current() == key:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"project key %q is already this project's current key; workbook key list shows every key this project has", key)
	}
	return keyPlan{
		operations: []core.ConfigOperation{{Type: core.ConfigKeyCurrent, Key: key}},
		change:     keyChange{Operation: "current", Key: key, From: keys.Current(), State: core.KeyStateActive},
	}, nil
}

// planKeyRetire stops a key minting new tasks, and refuses the two retirements
// that would leave the project unable to mint: the current key, and the last
// active one.
//
// The fold refuses both silently, because by the time a pack reaches it the
// change has already happened somewhere and refusing would strand the clone
// that fetched it rather than the person who authored it. This is where the
// person is, so this is where the sentence belongs.
func planKeyRetire(keys core.KeySet, key string) (keyPlan, error) {
	if err := core.ValidateProjectKey(key); err != nil {
		return keyPlan{}, err
	}
	state, known := keys.State(key)
	switch {
	case !known:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"no project key %q in this project; its keys are: %s", key, core.KeyNameList(keys))
	case state == core.KeyStateRetired:
		return keyPlan{}, core.Errorf(core.CategoryValidation, "project key %q is already retired", key)
	// The last active key is asked about before the current one, and the order
	// is what makes the advice true rather than merely correct. The current key
	// is always active, so a project down to one active key is a project whose
	// current key is that key — and telling its owner to "make another key
	// current first" would name a command with no argument they could give it.
	// So the narrower answer comes first: there is no other key.
	case len(keys.Active()) <= 1:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"project key %q is this project's only active key, and a project must keep one to mint new tasks under; "+
				"add another first: workbook key add <key>", key)
	case keys.Current() == key:
		return keyPlan{}, core.Errorf(core.CategoryValidation,
			"project key %q is this project's current key, so new tasks would have nowhere to go; "+
				"make another key current first: workbook key current <key>", key)
	}
	return keyPlan{
		operations: []core.ConfigOperation{{Type: core.ConfigKeyRetire, Key: key}},
		change:     keyChange{Operation: "retire", Key: key, State: core.KeyStateRetired},
	}, nil
}

// keyCommitSubject writes what the ledger's `git log` says about this change,
// in the same voice the seeded root and the other two sections use.
func keyCommitSubject(plan keyPlan) string {
	return "workbook: " + plan.change.summary()
}

// summary renders a change as one clause, for a commit subject and for the
// text-mode heading.
func (change keyChange) summary() string {
	switch change.Operation {
	case "add":
		if change.Reactivated {
			return fmt.Sprintf("reactivate project key %s", change.Key)
		}
		return fmt.Sprintf("add project key %s", change.Key)
	case "current":
		return fmt.Sprintf("mint new tasks under project key %s", change.Key)
	case "retire":
		return fmt.Sprintf("retire project key %s", change.Key)
	default:
		return "update project configuration"
	}
}

// keyWriteError explains a lost compare-and-swap in words a caller can act on,
// exactly as statusWriteError does: gitstore says the ledger changed
// concurrently, which is accurate and says nothing about what to do. The answer
// is always to run the same command again.
func keyWriteError(err error) error {
	if core.CategoryOf(err) == core.CategoryStaleWrite {
		return core.Wrap(core.CategoryStaleWrite,
			"another process changed this project's keys while this command was writing; nothing was recorded, so run it again",
			err)
	}
	return err
}

// keyInverse is the verb path's inverse: the same computation the log performs
// over the same operations, with nothing left for a mutating command to add. A
// change whose inverse cannot be expressed reports an empty one rather than a
// command that would not run.
func keyInverse(before core.KeySet, operations []core.ConfigOperation) statusInverse {
	if inverse := keyPackInverse(before, operations); inverse != nil {
		return *inverse
	}
	return statusInverse{}
}

// keyPackInverse is the command that undoes one recorded key pack.
//
// It is one command, never two joined by `&&`. `key add NEW --current` is two
// operations, and `key retire NEW` is refused while NEW is current — so the
// reversal that can actually be pasted is the one that gives the role back, and
// what it leaves behind goes in the note. Exactness says so: running `key
// current WB` does not restore the set this change found, because NEW is still
// active, and a reversal that claimed otherwise would be a lie a reader acts on.
//
// It takes the whole pack because the answer for an add depends on what else was
// recorded in the same commit, and it reads the authored operations rather than
// operations[0]: a project's first key change carries the founding key ahead of
// itself, and answering about that would offer to retire the key every existing
// task ID in the project carries.
func keyPackInverse(before core.KeySet, pack []core.ConfigOperation) *statusInverse {
	// Stripping is idempotent, so the verb path — which never had the backfill —
	// loses nothing by not having to know.
	pack = authoredKeyOperations(before, pack)
	operation, found := subjectKeyOperation(pack)
	if !found {
		return nil
	}
	switch operation.Type {
	case core.ConfigKeyAdd:
		if moved := currentIn(pack); moved == operation.Key &&
			before.Current() != "" && before.Current() != operation.Key {
			return &statusInverse{
				Command: "workbook key current " + before.Current(),
				Exact:   false,
				Note: fmt.Sprintf("then `workbook key retire %s` if it should not stay",
					operation.Key),
			}
		}
		return &statusInverse{Command: "workbook key retire " + operation.Key, Exact: true}
	case core.ConfigKeyCurrent:
		if before.Current() == "" || before.Current() == operation.Key {
			return nil
		}
		return &statusInverse{Command: "workbook key current " + before.Current(), Exact: true}
	case core.ConfigKeyRetire:
		return &statusInverse{Command: "workbook key add " + operation.Key, Exact: true}
	default:
		return nil
	}
}

// currentIn names the key a pack made current beside whatever else it did.
func currentIn(pack []core.ConfigOperation) string {
	for _, operation := range pack {
		if operation.Type == core.ConfigKeyCurrent {
			return operation.Key
		}
	}
	return ""
}

// subjectKeyOperation is the operation a commit is about, for the key section:
// the first one in what it is handed that touches keys.
//
// It decides nothing about the backfill, and a caller must not hand it a
// recorded pack expecting it to. The founding key a project's first key change
// records ahead of itself is a key operation too, so this would answer about it.
// Strip it with authoredKeyOperations first.
func subjectKeyOperation(operations []core.ConfigOperation) (core.ConfigOperation, bool) {
	for _, operation := range operations {
		if operation.Type.TouchesKeys() {
			return operation, true
		}
	}
	return core.ConfigOperation{}, false
}

// authoredKeyOperations is one recorded pack with gitstore's founding-key
// backfill removed: the operations somebody actually ran.
//
// A project's genesis records nothing about keys — every project's, including the
// ones this build mints — so the first key.* operation anybody authors is
// recorded behind a `key.add <founding>` that says what every existing task ID
// in the project already carries. See gitstore's prependFoundingKey. Everything
// in the log that names, counts or inverts a change has to answer about what
// follows it.
//
// Two facts identify that prefix, and neither would do alone:
//
//   - The commit's previous state had one key, which is what a project whose
//     ledger records nothing about keys reports and exactly when gitstore
//     prepends.
//   - The pack's first operation is an add of that very key, and something else
//     follows it.
//
// Nothing authored can be mistaken for it. planKeyAdd refuses a key the project
// already has as active, and the one key such a project has is active and
// current, so `key add <founding>` never reaches the ledger from this CLI at
// all — the same argument authoredPriorityOperations makes about its own prefix.
//
// It is idempotent: what it returns no longer carries the prefix.
func authoredKeyOperations(before core.KeySet, operations []core.ConfigOperation) []core.ConfigOperation {
	if len(operations) < 2 || len(before.Keys()) != 1 {
		return operations
	}
	first := operations[0]
	if first.Type != core.ConfigKeyAdd || first.Key != before.Keys()[0].Key {
		return operations
	}
	if !subjectFollows(operations[1:]) {
		return operations
	}
	return operations[1:]
}

// subjectFollows reports that something in the rest of a pack belongs to the key
// section, which is what makes a leading `key.add <founding>` a backfill rather
// than the whole of what was recorded. A pack that is nothing but the prefix
// comes back whole rather than emptied: gitstore prepends only ahead of an
// authored key operation, so no such pack exists, and describing one by its
// first operation beats dropping the commit out of the log entirely.
func subjectFollows(operations []core.ConfigOperation) bool {
	_, found := subjectKeyOperation(operations)
	return found
}

// keyOperationSummary says what one recorded key operation did, and reports
// whether it is a key operation at all.
//
// It lives here beside the verbs that author these operations rather than
// growing three more arms on the switch the status verbs own, exactly as
// priorityOperationSummary does.
func keyOperationSummary(operation core.ConfigOperation) (string, bool) {
	switch operation.Type {
	case core.ConfigKeyAdd:
		return fmt.Sprintf("added project key %s", operation.Key), true
	case core.ConfigKeyCurrent:
		return fmt.Sprintf("made project key %s current", operation.Key), true
	case core.ConfigKeyRetire:
		return fmt.Sprintf("retired project key %s", operation.Key), true
	default:
		return "", false
	}
}

// keyPackSummary says what one recorded commit did to this project's keys, in
// one clause, counting only the key operations in it: a commit that also changed
// a status is another section's news, and saying "+1 more change" about a column
// would be reporting somebody else's edit in this log.
func keyPackSummary(operations []core.ConfigOperation) string {
	primary, found := subjectKeyOperation(operations)
	if !found {
		return "recorded nothing about this project's keys"
	}
	summary, _ := keyOperationSummary(primary)
	if count := keyOperationCount(operations); count > 1 {
		summary += fmt.Sprintf(" (+%d more key change(s) in this commit)", count-1)
	}
	return summary
}

// keyOperationCount counts the key operations in a pack.
func keyOperationCount(operations []core.ConfigOperation) int {
	count := 0
	for _, operation := range operations {
		if operation.Type.TouchesKeys() {
			count++
		}
	}
	return count
}

// writeKeyMutation reports a key change on both surfaces.
//
// The JSON envelope carries the same members a task mutation's does — the sync
// report, the conflict lists, the warnings — because a caller that already
// parses one should not need a second parser for the other.
func writeKeyMutation(
	stdout, stderr io.Writer,
	command string,
	result keyMutationResult,
	session *taskSession,
	docsErr error,
	jsonMode bool,
) {
	var warnings []core.Warning
	if session.report.Status == syncStatusFailed {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the key change was recorded locally, but " + session.report.Detail,
		})
	}
	warnings = append(warnings, docsWarning(result.Docs, docsErr)...)
	if jsonMode {
		writeKeyEnvelope(stdout, command, result, session, warnings)
		return
	}
	writeKeyChange(stdout, result)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
}

func writeKeyEnvelope(
	stdout io.Writer,
	command string,
	result keyMutationResult,
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

// writeKeyChange renders a change as a heading and its details, the shape every
// other structured text block in this CLI uses: one column-zero line that cannot
// be forged from inside a value, then tab-indented fields.
func writeKeyChange(output io.Writer, result keyMutationResult) {
	change := result.Change
	fmt.Fprintf(output, "Key:\t%s\t%s\n", change.Operation, change.Key)
	fmt.Fprintf(output, "\tstate:\t%s\n", change.State)
	if change.Reactivated {
		fmt.Fprintf(output, "\treactivated:\tyes, in the place it already had\n")
	}
	if change.From != "" {
		fmt.Fprintf(output, "\tcurrent:\t%s → %s\n", change.From, change.Key)
	}
	fmt.Fprintf(output, "\tkeys:\t%s\n", keyStatesLine(result.Keys.Keys))
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

// keyStatesLine names every key and what it is now, in add order, so a change
// to one key is read against the set it left behind.
func keyStatesLine(keys []keyView) string {
	words := make([]string, 0, len(keys))
	for _, key := range keys {
		words = append(words, key.Key+" "+keyStateWord(key))
	}
	return strings.Join(words, ", ")
}

// keyStateWord is the one word `key list` marks a key with. Current outranks
// active because it is the more specific answer to the question a reader is
// asking — which key is a new task minted under — and there is exactly one of
// them.
func keyStateWord(key keyView) string {
	if key.Current {
		return "current"
	}
	return string(key.State)
}

// writeKeyList renders the key table and the note under it.
//
// The table is padded rather than tab-separated, in RenderStatusList's style and
// for its reason: every column here is bounded by a ceiling that keeps it
// typeable — a key is at most ten characters — so there is no width to fit
// anything into. It is built here rather than in terminalui because a key row
// has no column a status row has, and sharing the renderer would mean a heading
// that lies about one of the two tables.
func writeKeyList(output io.Writer, result keySetView) {
	keyWidth, stateWidth := len("KEY"), len("STATE")
	for _, key := range result.Keys {
		keyWidth = max(keyWidth, len(key.Key))
		stateWidth = max(stateWidth, len(keyStateWord(key)))
	}
	fmt.Fprintf(output, "%-*s  %-*s  %s\n", keyWidth, "KEY", stateWidth, "STATE", "TASKS")
	for _, key := range result.Keys {
		tasks := ""
		if key.Tasks != nil {
			tasks = strconv.Itoa(*key.Tasks)
		}
		fmt.Fprintf(output, "%-*s  %-*s  %s\n", keyWidth, key.Key, stateWidth, keyStateWord(key), tasks)
	}
	if !result.Seeded {
		// Not "the key this project started with": a project can reach the
		// fallback by never having recorded a key change and by having lost the
		// ledger that recorded one, and only one of those started here. What is
		// true in both is that nothing is recorded now.
		fmt.Fprintf(output, "\tNo key change is recorded, so this is the key this project was created with.\n")
	}
}

// writeKeyLog renders the log the way `status log` and `priority log` render
// theirs: oldest first, wall times as attribution only, and the window's size
// stated before its contents.
func writeKeyLog(output io.Writer, result keyLogResult, found bool) {
	if !found {
		fmt.Fprintln(output, "No key change is recorded; this project has not configured its keys.")
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
