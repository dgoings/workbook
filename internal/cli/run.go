package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/syncloop"
	"github.com/dgoings/workbook/internal/webui"
)

// watcherProbeDeadline bounds every exchange with a watcher. It is short on
// purpose: a command consults one to save roughly half a second, so a slow
// answer is worth abandoning for the inline path rather than waiting on.
const watcherProbeDeadline = 50 * time.Millisecond

type rebuildResult struct {
	TaskCount int    `json:"taskCount"`
	CachePath string `json:"cachePath"`
}

type helpRequest struct {
	Target []string
}

func Run(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) int {
	help, handled, helpErr := parseHelpRequest(args)
	if handled {
		if helpErr == nil {
			helpErr = renderCommandHelp(stdout, help.Target)
		}
		if helpErr != nil {
			writeError(stderr, helpErr, false)
			return core.ExitCode(helpErr)
		}
		return 0
	}

	jsonMode := requestedJSON(args)
	if len(args) == 0 {
		err := core.Errorf(core.CategoryInvocation, "a command is required")
		writeError(stderr, err, false)
		return core.ExitCode(err)
	}

	command := args[0]
	commandArgs := args[1:]
	var err error
	switch command {
	case "setup":
		err = runSetup(ctx, commandArgs, cwd, stdout)
	case "create":
		err = runCreate(ctx, commandArgs, cwd, stdout, stderr)
	case "list":
		err = runList(ctx, commandArgs, cwd, stdout, stderr)
	case "board":
		err = runBoard(ctx, commandArgs, cwd, stdout, stderr)
	case "show":
		err = runShow(ctx, commandArgs, cwd, stdout, stderr)
	case "update":
		err = runUpdate(ctx, commandArgs, cwd, stdout, stderr)
	case "delete":
		err = runDelete(ctx, commandArgs, cwd, stdout, stderr)
	case "restore":
		err = runRestore(ctx, commandArgs, cwd, stdout, stderr)
	case "move":
		err = runMove(ctx, commandArgs, cwd, stdout, stderr)
	case "depend":
		err = runDepend(ctx, commandArgs, cwd, stdout, stderr)
	case "free":
		err = runFree(ctx, commandArgs, cwd, stdout, stderr)
	case "next":
		err = runNext(ctx, commandArgs, cwd, stdout, stderr)
	case "rebuild":
		err = runRebuild(ctx, commandArgs, cwd, stdout, stderr)
	case "validate":
		err = runValidate(ctx, commandArgs, cwd, stdout, stderr)
	case "version":
		err = runVersion(commandArgs, stdout)
	case "fetch":
		err = runFetch(ctx, commandArgs, cwd, stdout, stderr)
	case "push":
		err = runPush(ctx, commandArgs, cwd, stdout, stderr)
	case "sync":
		err = runSync(ctx, commandArgs, cwd, stdout, stderr)
	case "status":
		err = runStatus(ctx, commandArgs, cwd, stdout, stderr)
	case "config":
		err = runConfig(ctx, commandArgs, cwd, stdout, stderr)
	case "docs":
		err = runDocs(ctx, commandArgs, cwd, stdout, stderr)
	case "hooks":
		err = runHooks(ctx, commandArgs, cwd, stdout, stderr)
	case "serve":
		err = runServe(ctx, commandArgs, cwd, stdout, stderr)
	default:
		err = core.Errorf(core.CategoryInvocation, "unknown command %q", command)
	}
	if err != nil {
		writeError(stderr, err, jsonMode)
		return core.ExitCode(err)
	}
	return 0
}

func runVersion(args []string, stdout io.Writer) error {
	flags := newFlagSet("version")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "version", release.Current())
		return nil
	}
	metadata := release.Current()
	fmt.Fprintf(stdout, "workbook %s (%s)\n", metadata.Version, metadata.Commit)
	return nil
}

func parseHelpRequest(args []string) (helpRequest, bool, error) {
	if len(args) == 0 {
		return helpRequest{}, true, nil
	}
	if args[0] == "-h" || args[0] == "--help" {
		if len(args) == 1 {
			return helpRequest{}, true, nil
		}
		return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "global help accepts no additional arguments")
	}
	if args[0] == "help" {
		return parseExplicitHelpRequest(args[1:])
	}

	if _, exists := commandSchemas[args[0]]; !exists {
		return helpRequest{}, false, nil
	}
	if len(args) >= 2 && (args[1] == "-h" || args[1] == "--help") {
		if len(args) != 2 {
			return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "%s help accepts no additional arguments", args[0])
		}
		return helpRequest{Target: args[:1]}, true, nil
	}
	if len(args) >= 3 && (args[2] == "-h" || args[2] == "--help") {
		if _, exists := commandMetadataFor(args[:2]); exists {
			if len(args) != 3 {
				return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "%s %s help accepts no additional arguments", args[0], args[1])
			}
			return helpRequest{Target: args[:2]}, true, nil
		}
	}
	if hasLocalHelpAlias(args) {
		return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "%s help accepts no additional arguments", args[0])
	}
	return helpRequest{}, false, nil
}

func hasLocalHelpAlias(args []string) bool {
	metadata, optionArgs, exists := commandInvocationMetadata(args)
	if !exists {
		return false
	}
	for index := 0; index < len(optionArgs); index++ {
		argument := optionArgs[index]
		if argument == "--" {
			return false
		}
		name, _, hasValue, isFlag := splitFlag(argument)
		if !isFlag {
			return false
		}
		if name == "h" || name == "help" {
			return true
		}
		kind, known := metadata.optionKind(name)
		if !known {
			return false
		}
		if !hasValue {
			index += optionValueCount(kind)
		}
	}
	return false
}

func parseExplicitHelpRequest(args []string) (helpRequest, bool, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "help accepts no flags")
		}
	}
	switch len(args) {
	case 0:
		return helpRequest{}, true, nil
	case 1:
		return helpRequest{Target: args}, true, nil
	case 2:
		if _, exists := commandMetadataFor(args); exists {
			return helpRequest{Target: args}, true, nil
		}
	}
	return helpRequest{}, true, core.Errorf(core.CategoryInvocation, "help accepts a command or one of its subcommands")
}

func runFetch(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("fetch")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	result, syncErr := repository.Fetch(ctx, config)
	writeSyncPhaseResultWithConfig(stdout, "fetch", result, result.Conflicts, result.ConfigConflicts, *jsonMode,
		func(output io.Writer) {
			writeSyncResult(output, result)
			writeConfigWarning(stderr, result.Config)
		})
	return syncErr
}

func runPush(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("push")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	result, syncErr := repository.Push(ctx, config)
	if *jsonMode {
		writeResult(stdout, "push", result)
	} else {
		writeSyncResult(stdout, result)
		writeIdentityWarning(stderr, result.Identity)
		writeConfigWarning(stderr, result.Config)
	}
	return syncErr
}

func runSync(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("sync")
	jsonMode := flags.Bool("json", false, "emit JSON")
	watch := flags.Bool("watch", false, "synchronize continuously until interrupted")
	interval := flags.String("interval", "", "time between synchronizations while watching")
	status := flags.Bool("status", false, "report whether a watcher is running for this repository")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if *watch && *status {
		return core.Errorf(core.CategoryInvocation, "sync accepts --watch or --status, not both")
	}
	if *interval != "" && !*watch {
		return core.Errorf(core.CategoryInvocation, "sync --interval requires --watch")
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	if *status {
		return runSyncStatus(repository, stdout, *jsonMode)
	}
	if *watch {
		return runSyncWatch(ctx, repository, config, *interval, stdout, stderr, *jsonMode)
	}

	result, syncErr := repository.Sync(ctx, config)
	writeSyncPhaseResultWithConfig(stdout, "sync", result, result.Fetch.Conflicts, result.Fetch.ConfigConflicts, *jsonMode,
		func(output io.Writer) {
			writeSyncRunResult(output, result)
			writeIdentityWarning(stderr, result.Identity)
			writeConfigWarning(stderr, result.Config)
		})
	return syncErr
}

// watcherRemote names the remote a watcher synchronizes with. Its status
// carries the refs it skipped but no remote of its own, and the collaborative
// POC supports only origin.
const watcherRemote = "origin"

// watcherStatusResult reports what `sync --status` found. It answers even when
// no watcher is running, because "nothing is running" is the ordinary state and
// a caller has to be able to tell it from a failure to look.
type watcherStatusResult struct {
	Running    bool            `json:"running"`
	PID        int             `json:"pid,omitempty"`
	IntervalMS int64           `json:"intervalMs,omitempty"`
	LastSyncAt string          `json:"lastSyncAt,omitempty"`
	LastSyncOK bool            `json:"lastSyncOk,omitempty"`
	LastError  string          `json:"lastSyncError,omitempty"`
	Conflicts  []core.Conflict `json:"conflicts,omitempty"`
	// IgnoredRefs carries what the watcher's last synchronization skipped under
	// origin's task namespace, so asking a watcher what it has been doing
	// reports it the same way a foreground fetch does.
	IgnoredRefs []gitstore.IgnoredRef `json:"ignoredRefs,omitempty"`
}

func runSyncStatus(repository *gitstore.Repository, stdout io.Writer, jsonMode bool) error {
	result := watcherStatusResult{}
	client, err := syncloop.Dial(repository.CommonGitDir, watcherProbeDeadline)
	if err == nil {
		defer client.Close()
		if status, statusErr := client.Status(); statusErr == nil {
			result.Running = true
			result.PID = status.PID
			result.IntervalMS = status.IntervalMS
			result.LastSyncAt = status.LastSyncAt.Format(time.RFC3339)
			result.LastSyncOK = status.LastSyncOK
			result.LastError = status.LastError
			result.IgnoredRefs = status.IgnoredRefs
			for _, entry := range status.Conflicts {
				result.Conflicts = append(result.Conflicts, entry.Conflict)
			}
		}
	}

	if jsonMode {
		writeResult(stdout, "sync", result)
		return nil
	}
	if !result.Running {
		fmt.Fprintln(stdout, "No sync watcher is running for this repository.")
		return nil
	}
	fmt.Fprintf(stdout, "Sync watcher running (pid %d), every %s.\n", result.PID, time.Duration(result.IntervalMS)*time.Millisecond)
	if result.LastSyncOK {
		fmt.Fprintf(stdout, "Last synchronized %s.\n", result.LastSyncAt)
	} else {
		fmt.Fprintf(stdout, "Last synchronization failed: %s\n", result.LastError)
	}
	// What the watcher skipped is reported the way a foreground fetch reports
	// it, because asking a watcher what it has been doing is the only way to
	// hear about a poisoned namespace nobody was present for.
	writeIgnoredRefs(stdout, watcherRemote, result.IgnoredRefs)
	if len(result.Conflicts) > 0 {
		writeConflicts(stdout, result.Conflicts)
	}
	return nil
}

func runSyncWatch(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	interval string,
	stdout, stderr io.Writer,
	jsonMode bool,
) error {
	every := syncloop.DefaultInterval
	if interval != "" {
		parsed, err := time.ParseDuration(interval)
		if err != nil {
			return core.Wrap(core.CategoryInvocation, "sync --interval must be a duration such as 5s", err)
		}
		if parsed <= 0 {
			return core.Errorf(core.CategoryInvocation, "sync --interval must be positive")
		}
		every = parsed
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return err
	}

	// The watcher reports to stderr, leaving stdout free for the terminating
	// result. --json changes only that final document, not the running report.
	err = syncloop.Run(ctx, syncloop.Options{
		CommonGitDir: repository.CommonGitDir,
		Repository:   repository,
		Config:       config,
		Projection:   store,
		Interval:     every,
		Stderr:       stderr,
	})
	if errors.Is(err, syncloop.ErrWatcherLive) {
		return core.Errorf(core.CategoryOperational, "a sync watcher is already running for this repository")
	}
	if err != nil {
		return err
	}
	if jsonMode {
		writeResult(stdout, "sync", watcherStoppedResult{Stopped: true})
	} else {
		fmt.Fprintln(stdout, "Sync watcher stopped.")
	}
	return nil
}

type watcherStoppedResult struct {
	Stopped bool `json:"stopped"`
}

func runHooks(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := requiredFirstArgument("hooks", "hook command", args)
	if err != nil {
		return err
	}
	if subcommand != "install" {
		return core.Errorf(core.CategoryInvocation, "unknown hooks command %q", subcommand)
	}
	flags := newFlagSet("hooks", subcommand)
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, _, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	result, err := repository.InstallHooks(ctx)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "hooks install", result)
	} else {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", result.Hook, result.Status, result.Path)
	}
	return nil
}

func runCreate(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	title, args, err := requiredFirstArgument("create", "title", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("create")
	description := flags.String("description", "", "task description")
	status := flags.String("status", "", "task status")
	priority := flags.String("priority", "", "task priority")
	var labels stringListValue
	flags.Var(&labels, "label", "task label")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	if strings.TrimSpace(title) == "" {
		return core.Errorf(core.CategoryValidation, "title is required")
	}
	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, "", func(ctx context.Context) (core.MutationResult, error) {
		return session.service.CreateMutation(ctx, core.CreateInput{
			Title:       title,
			Description: *description,
			Status:      core.Status(*status),
			Priority:    core.Priority(*priority),
			Labels:      labels.values,
		})
	})
	return writeMutationOutcome(stdout, stderr, "create", session, result, err, *jsonMode)
}

func runList(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("list")
	status := flags.String("status", "", "task status")
	priority := flags.String("priority", "", "task priority")
	label := flags.String("label", "", "task label")
	all := flags.Bool("all", false, "include tombstoned tasks")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	filter := core.ListFilter{Label: *label, All: *all}
	if *status != "" {
		value := core.Status(*status)
		filter.Status = &value
	}
	if *priority != "" {
		value := core.Priority(*priority)
		filter.Priority = &value
	}
	tasks, err := service.List(ctx, filter)
	if err != nil {
		return err
	}
	warnings := statusFilterWarnings(service, filter)
	if *jsonMode {
		writeResultWithWarnings(stdout, "list", tasks, warnings)
	} else {
		if err := writeList(stdout, tasks); err != nil {
			return core.Wrap(core.CategoryOperational, "render list", err)
		}
		writeWarnings(stderr, warnings)
	}
	return nil
}

// statusFilterWarnings says what a status filter turned out to select, when
// that is not what the caller typed.
//
// A filter outside the vocabulary succeeds and returns an empty list, which is
// the honest answer to "which tasks are in a status this project does not
// have". It is also indistinguishable from an empty column, so the miss is
// named here; a filter that had to be forwarded says so too, because the tasks
// that came back are not stored under the value that was asked for.
func statusFilterWarnings(service core.Service, filter core.ListFilter) []core.Warning {
	if filter.Status == nil {
		return nil
	}
	resolution := service.ResolveStatusFilter(*filter.Status)
	switch {
	case !resolution.Known:
		return []core.Warning{{
			Code:    core.WarningStatusFilter,
			Message: fmt.Sprintf("no status %q in this project's vocabulary", resolution.Requested),
		}}
	case resolution.Forwarded:
		// The verb belongs to the one hop it describes, and the end of the
		// chain gets its own clause; see statusChainClause. Pairing the first
		// hop's verb with the last hop's destination reported a rename that
		// never happened.
		return []core.Warning{{
			Code: core.WarningStatusFilter,
			Message: fmt.Sprintf("no status %q in this project's vocabulary; it was %s %q%s, and %q is what was listed",
				resolution.Requested, forwardingVerb(resolution.Operation), resolution.Via,
				statusChainClause(resolution.Via, resolution.Resolved), resolution.Resolved),
		}}
	default:
		return nil
	}
}

// forwardingVerb names how a status stopped being live, in the voice a message
// about the value somebody typed reads in.
func forwardingVerb(operation core.ConfigOperationType) string {
	if operation == core.ConfigStatusRemove {
		return "removed into"
	}
	return "renamed to"
}

func runShow(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("show", "task ID", args)
	if err != nil {
		return err
	}
	compare, args, comparing, err := takePairOption("show", "compare", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("show")
	history := flags.Bool("history", false, "list this task's changes")
	limit := flags.String("limit", "", "show this many recent changes")
	all := flags.Bool("all", false, "show every change")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	options := core.ShowOptions{History: *history, All: *all}
	if comparing {
		options.Compare = &core.ComparePoints{From: compare[0], To: compare[1]}
	}
	if *limit != "" {
		parsed, err := strconv.Atoi(*limit)
		if err != nil || parsed < 1 {
			return core.Errorf(core.CategoryInvocation, "show --limit must be a positive whole number")
		}
		options.Limit = parsed
	}
	if err := validateHistoryOptions(flags, options); err != nil {
		return err
	}

	service, err := openReadService(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	detail, err := service.ShowDetail(ctx, id, options)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "show", detail)
	} else {
		writeShowDetail(stdout, detail)
	}
	return nil
}

// validateHistoryOptions rejects windowing flags that would silently do
// nothing, so a caller who asked for ten changes and got a plain task never has
// to wonder which flag it ignored.
func validateHistoryOptions(flags *commandFlagSet, options core.ShowOptions) error {
	if options.History {
		if options.Limit > 0 && options.All {
			return core.Errorf(core.CategoryInvocation, "cannot use --limit with --all")
		}
		return nil
	}
	var offending error
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == "limit" || visited.Name == "all" {
			offending = core.Errorf(core.CategoryInvocation, "show --%s requires --history", visited.Name)
		}
	})
	return offending
}

func runUpdate(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("update", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("update")
	title := flags.String("title", "", "task title")
	description := flags.String("description", "", "task description")
	status := flags.String("status", "", "task status")
	priority := flags.String("priority", "", "task priority")
	var labels stringListValue
	flags.Var(&labels, "label", "replacement task label")
	clearLabels := flags.Bool("clear-labels", false, "replace labels with an empty set")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if labels.set && *clearLabels {
		return core.Errorf(core.CategoryInvocation, "cannot use --label with --clear-labels")
	}

	input := core.UpdateInput{}
	flags.Visit(func(visited *flag.Flag) {
		switch visited.Name {
		case "title":
			input.Title = title
		case "description":
			input.Description = description
		case "status":
			value := core.Status(*status)
			input.Status = &value
		case "priority":
			value := core.Priority(*priority)
			input.Priority = &value
		}
	})
	if labels.set {
		input.Labels = &labels.values
	} else if *clearLabels {
		empty := []string{}
		input.Labels = &empty
	}

	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, id, func(ctx context.Context) (core.MutationResult, error) {
		return session.service.UpdateMutation(ctx, id, input)
	})
	return writeMutationOutcome(stdout, stderr, "update", session, result, err, *jsonMode)
}

func runDelete(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("delete", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("delete")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, id, func(ctx context.Context) (core.MutationResult, error) {
		return session.service.DeleteMutation(ctx, id)
	})
	return writeMutationOutcome(stdout, stderr, "delete", session, result, err, *jsonMode)
}

func runRestore(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("restore", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("restore")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, id, func(ctx context.Context) (core.MutationResult, error) {
		return session.service.RestoreMutation(ctx, id)
	})
	return writeMutationOutcome(stdout, stderr, "restore", session, result, err, *jsonMode)
}

func runMove(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("move", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("move")
	before := flags.String("before", "", "move before task ID")
	after := flags.String("after", "", "move after task ID")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "move requires exactly one of --before or --after")
	}
	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, id, func(ctx context.Context) (core.MutationResult, error) {
		return session.service.MoveMutation(ctx, id, core.MoveInput{Before: *before, After: *after})
	})
	return writeMutationOutcome(stdout, stderr, "move", session, result, err, *jsonMode)
}

func runDepend(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	return runDependencyMutation(ctx, "depend", args, cwd, stdout, stderr)
}

func runFree(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	return runDependencyMutation(ctx, "free", args, cwd, stdout, stderr)
}

func runDependencyMutation(ctx context.Context, command string, args []string, cwd string, stdout, stderr io.Writer) error {
	ids, args, err := requiredArguments(command, []string{"task ID", "dependency task ID"}, args)
	if err != nil {
		return err
	}
	flags := newFlagSet(command)
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	session, err := openTaskSession(ctx, cwd, *noSync, true, stderr)
	if err != nil {
		return err
	}
	result, err := session.mutate(ctx, ids[0], func(ctx context.Context) (core.MutationResult, error) {
		if command == "depend" {
			return session.service.DependMutation(ctx, ids[0], ids[1])
		}
		return session.service.FreeMutation(ctx, ids[0], ids[1])
	})
	return writeMutationOutcome(stdout, stderr, command, session, result, err, *jsonMode)
}

func runNext(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("next")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	session, err := openTaskSession(ctx, cwd, *noSync, false, stderr)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
	if err := session.refreshVocabulary(ctx); err != nil {
		return err
	}
	task, err := session.service.Next(ctx)
	if err != nil {
		return err
	}
	// Selecting work succeeds even when some other task needs a decision, so
	// the conflicts are reported without failing the command. An agent that
	// only wants the next task is not the caller who must resolve them.
	if *jsonMode {
		writeSyncedResult(stdout, "next", task, &session.report, session.conflicts)
		return nil
	}
	if task == nil {
		fmt.Fprintln(stdout, "No eligible task.")
	} else {
		writeShow(stdout, *task)
	}
	writeConflicts(stdout, session.conflicts)
	return nil
}

func runRebuild(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("rebuild")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return err
	}
	count, err := store.Rebuild(ctx)
	if err != nil {
		return err
	}
	result := rebuildResult{TaskCount: count, CachePath: store.CachePath()}
	if *jsonMode {
		writeResult(stdout, "rebuild", result)
	} else {
		fmt.Fprintf(stdout, "Rebuilt %d task(s) at %s.\n", result.TaskCount, result.CachePath)
	}
	return nil
}

func runValidate(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("validate")
	full := flags.Bool("full", false, "bypass cached validation results")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	validator, err := historyvalidation.Open(ctx, repository, config)
	if err != nil {
		return err
	}
	defer validator.Close()
	result, validateErr := validator.Validate(ctx, *full)
	if *jsonMode {
		writeResult(stdout, "validate", result)
	} else {
		writeValidationResult(stdout, result)
	}
	return validateErr
}

func writeValidationResult(output io.Writer, result historyvalidation.Result) {
	fmt.Fprintf(output, "Validated %d task(s): %d commit(s) checked, %d cache hit(s); %d valid, %d invalid, %d pending.\n",
		result.TaskCount, result.CommitsChecked, result.CacheHits, result.Valid, result.Invalid, result.Pending)
	for _, failure := range result.Failures {
		fmt.Fprintf(output, "Invalid %s at %s [%s]: %s\n", failure.TaskID, failure.Commit, failure.Category, failure.Message)
	}
	if result.Config != nil {
		verdict := "valid"
		if !result.Config.Valid {
			verdict = "invalid"
		}
		fmt.Fprintf(output, "Configuration ledger: %d commit(s) checked; %s.\n", result.Config.CommitsChecked, verdict)
		if result.Config.Failure != nil {
			fmt.Fprintf(output, "Invalid configuration at %s [%s]: %s\n",
				result.Config.Failure.Commit, result.Config.Failure.Category, result.Config.Failure.Message)
		}
	}
	// Advisories are printed after the verdict and never change it: they
	// describe a state everybody wrote deliberately and nobody can be blamed
	// for, which is the whole reason they are not failures.
	for _, advisory := range result.Advisories {
		fmt.Fprintf(output, "Advisory:\t%s\t%s\n", advisory.Code, advisory.Message)
	}
}

// defaultServeAddr is where the board prefers to live, so the common
// single-project setup keeps a stable, bookmarkable URL. The port is a
// preference rather than a contract: when nobody passed --addr and another
// process already holds it — a second project's board on the same machine is
// an expected setup — serve falls back to an OS-assigned port instead of
// dying. An explicit --addr is a contract and never falls back.
const defaultServeAddr = "127.0.0.1:7331"

// openBoardListenerWith binds the board's listener. The fallback triggers only
// for an unchosen default address that is already in use; every other bind
// failure, such as permission denied on a privileged port, stays a loud
// operational error because moving to another port would not cure it. The bind
// itself is a parameter so a test can present outcomes a test process cannot
// provoke for real, that permission denial among them.
//
// The second result reports whether the returned listener is the fallback, so
// the caller states a decision this function already made rather than
// re-deriving it. Comparing the requested and bound addresses would agree
// today only because the one address that can move is the literal
// defaultServeAddr, which binds to itself; the comparison would start lying
// the moment a name that resolves elsewhere, such as a "localhost:7331" bound
// as "127.0.0.1:7331", could reach the fallback path.
func openBoardListenerWith(listen func(network, address string) (net.Listener, error), addr string, explicit bool) (net.Listener, bool, error) {
	listener, err := listen("tcp", addr)
	if err == nil {
		return listener, false, nil
	}
	if explicit || !errors.Is(err, syscall.EADDRINUSE) {
		return nil, false, core.Wrap(core.CategoryOperational, "open board listener", err)
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, false, core.Wrap(core.CategoryOperational, "open board listener", err)
	}
	// Port 0 hands the choice to the OS, and the caller prints the resolved
	// address, so the user learns where the board actually is.
	fallback, fallbackErr := listen("tcp", net.JoinHostPort(host, "0"))
	if fallbackErr != nil {
		return nil, false, core.Wrap(core.CategoryOperational, "open board listener", fallbackErr)
	}
	return fallback, true, nil
}

func runServe(ctx context.Context, args []string, cwd string, stdout io.Writer, stderr io.Writer) error {
	return runServeWith(ctx, net.Listen, args, cwd, stdout, stderr)
}

// runServeWith takes the bind as a parameter for the same reason
// openBoardListenerWith does, one step further out: whether serve announces a
// collision depends on what the bind did, and a test cannot ask the OS for the
// interesting answers. Binding the real default port would make the
// no-collision case race every other board on the machine, so a test that
// wants a free 127.0.0.1:7331 says so here instead.
func runServeWith(ctx context.Context, listen func(network, address string) (net.Listener, error), args []string, cwd string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("serve")
	addr := flags.String("addr", defaultServeAddr, "listener address (default "+defaultServeAddr+", or a free port when that one is taken)")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	// An address the user typed is a contract; the default is a preference.
	addrChosen := false
	flags.Visit(func(visited *flag.Flag) {
		if visited.Name == "addr" {
			addrChosen = true
		}
	})

	service, repository, store, err := openServiceParts(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	publisher := &boardPublisher{repository: repository, config: service.Config}
	// Every request reads the project's statuses again rather than reusing the
	// ones this process opened with.
	//
	// A board is open for hours, and a teammate's `workbook status add` reaches
	// this checkout on the next fetch. A snapshot taken here would keep drawing
	// the old columns until somebody restarted serve — and, worse, would keep
	// refusing writes into a column the page had started drawing, because the
	// service's own membership check reads the same value. So both halves are
	// re-read: the resolver below draws the columns, and `current` gives every
	// mutation a service that agrees with them. LoadVocabularyState is the read
	// that skips the repository's memo, which exists for one-shot commands.
	current := func(requestContext context.Context) (core.Service, error) {
		state, err := repository.LoadVocabularyState(requestContext, service.Config)
		if err != nil {
			return core.Service{}, err
		}
		fresh := service
		fresh.Vocabulary = state.Vocabulary
		return fresh, nil
	}
	handler := webui.NewHandler(webui.Options{
		Vocabulary: func(requestContext context.Context) (webui.VocabularyState, error) {
			state, err := repository.LoadVocabularyState(requestContext, service.Config)
			if err != nil {
				return webui.VocabularyState{}, err
			}
			return webui.VocabularyState{Vocabulary: state.Vocabulary, Head: state.Head}, nil
		},
		List: func(requestContext context.Context) ([]core.Task, error) {
			reader, err := current(requestContext)
			if err != nil {
				return nil, err
			}
			return reader.List(requestContext, core.ListFilter{All: true})
		},
		Create: func(requestContext context.Context, input core.CreateInput) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.CreateMutation(requestContext, input)
			return publisher.publish(requestContext, result, err)
		},
		Update: func(requestContext context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.UpdateMutation(requestContext, id, input)
			return publisher.publish(requestContext, result, err)
		},
		UpdateStatus: func(requestContext context.Context, id string, status core.Status, expectedHead string) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.UpdateMutation(requestContext, id, core.UpdateInput{Status: &status, ExpectedHead: expectedHead})
			return publisher.publish(requestContext, result, err)
		},
		Position: func(requestContext context.Context, id string, input core.PlaceInput) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.PlaceMutation(requestContext, id, input)
			return publisher.publish(requestContext, result, err)
		},
		Delete: func(requestContext context.Context, id string) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.DeleteMutation(requestContext, id)
			return publisher.publish(requestContext, result, err)
		},
		Restore: func(requestContext context.Context, id string) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.RestoreMutation(requestContext, id)
			return publisher.publish(requestContext, result, err)
		},
		Depend: func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.DependMutation(requestContext, id, dependency)
			return publisher.publish(requestContext, result, err)
		},
		Free: func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
			writer, err := current(requestContext)
			if err != nil {
				return core.MutationResult{}, err
			}
			result, err := writer.FreeMutation(requestContext, id, dependency)
			return publisher.publish(requestContext, result, err)
		},
		// The board's detail view shows history by default and derives a status
		// lane that reaches back to the task's creation, so it reads the whole
		// chain rather than the CLI's ten-change default window.
		History: func(requestContext context.Context, id string) (core.TaskDetail, error) {
			reader, err := current(requestContext)
			if err != nil {
				return core.TaskDetail{}, err
			}
			return reader.ShowDetail(requestContext, id, core.ShowOptions{History: true, All: true})
		},
		SyncState:   publisher.state,
		SetSyncMode: publisher.setMode,
	})
	listener, fellBack, err := openBoardListenerWith(listen, *addr, addrChosen)
	if err != nil {
		return err
	}
	// The reason precedes the banner rather than replacing it: the banner is the
	// line other tools scan for — the benchmark harness waits on its exact
	// prefix — and the address a person copies, so a fallback adds a line
	// instead of reshaping one.
	if fellBack {
		fmt.Fprintln(stderr, boardFallbackNotice(*addr, listener.Addr().String()))
	}
	fmt.Fprintf(stderr, "Workbook board: http://%s\n", listener.Addr())
	if warning := boardExposureWarning(listener.Addr().String()); warning != "" {
		fmt.Fprintln(stderr, warning)
	}

	// The board polls its own API once a second, so a loop running here is all
	// it takes for a teammate's change to appear: no new endpoint, no client
	// change, and no server-sent events.
	watcher := serveWatcher(ctx, repository, service.Config, store, stderr)
	defer watcher.stop()

	// webui.Serve refuses foreign Host headers, cross-origin requests, and
	// mutations that do not declare JSON. That guard is the board's only access
	// control, and a non-loopback bind is what the warning above is about.
	if err := webui.Serve(ctx, listener, handler); err != nil {
		return core.Wrap(core.CategoryOperational, "serve board", err)
	}
	return nil
}

// boardFallbackNotice says why the board is not at the address the user
// expects it at.
//
// The move itself is deliberate and usually dull: a second project's board on
// the same machine should start rather than die. What the move must not be is
// silent, because a squatter that binds the default port to serve a look-alike
// board is indistinguishable from that dull case once the real board is quietly
// somewhere else — the habitual bookmark still answers, with someone else's
// page. Naming the collision costs one line and makes a squat something the
// person who started the board can notice.
func boardFallbackNotice(requested string, bound string) string {
	return fmt.Sprintf(
		"%s is in use; serving on http://%s instead. If you did not start another board, check what is holding that address.",
		requested, bound)
}

// boardExposureWarning names what a board off this machine gives away.
//
// The board has no accounts, no tokens, and no authorization: whoever opens the
// port reads every task and writes changes that publish to the project's
// origin. The same-origin guard stops a browser on another site from acting for
// a person at this machine; it cannot tell a teammate from a stranger on the
// same network. A non-loopback bind is therefore a deliberate exposure and is
// said out loud rather than left to be discovered.
//
// A wildcard bind gives away more than network reach, and gets a second
// sentence for it. The guard pins the Host header to the address the listener
// reports, which a wildcard bind does not have, so it falls back to the port
// alone. Any page on the web can then point its own DNS name at this machine
// and hold same-origin read and write on the board through the browser of
// whoever opens it — an exposure that does not need the attacker on the
// network at all. Naming the fix in the same breath keeps the warning
// actionable rather than merely alarming.
func boardExposureWarning(address string) string {
	if webui.BoundToLoopback(address) {
		return ""
	}
	warning := fmt.Sprintf(
		"Warning: the board at %s is reachable beyond this machine and has no authentication. Anyone who can open that address can read and change every task.",
		address,
	)
	if webui.BoundToWildcard(address) {
		warning += " A wildcard bind also cannot pin the Host header, so any page on the web can point its own name at this machine and read and change tasks through the browser of whoever opens it. Bind the one address you mean instead."
	}
	return warning
}

// boardPublisher publishes what a web mutation wrote.
//
// It hands the change to a watcher rather than fetching and pushing inline.
// Inline would put two network round trips inside every request — roughly
// 530 ms and 16 Git processes on the measured CLI path — which is the latency
// the board's optimistic rendering exists to hide.
type boardPublisher struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	// inline shifts the board to attempting the push itself and answering
	// afterwards, rather than handing the change to a watcher. It does not make
	// publication a condition of success: publish below still turns a failed
	// push into a warning, so neither mode lets a response promise that origin
	// has the change. It lives in memory for the life of this server: it is a
	// preference about how this board behaves, not a project setting, and
	// `workbook config set auto-sync` already means something different.
	inline atomic.Bool
}

// state reports what the board will do with the next mutation. The watcher is
// probed rather than remembered, so the indicator describes the next mutation
// instead of a cached opinion that may have gone stale.
func (p *boardPublisher) state(ctx context.Context) webui.SyncState {
	if !p.repository.HasOrigin(ctx) {
		return webui.SyncState{Mode: p.mode(), Detail: "no origin is configured, so nothing is published"}
	}
	if watching := p.watcherAnswers(); !watching {
		return webui.SyncState{Mode: p.mode(), Detail: "no watcher is running, so changes publish inline"}
	}
	return webui.SyncState{Mode: p.mode(), Watcher: true}
}

func (p *boardPublisher) mode() string {
	if p.inline.Load() {
		return webui.SyncModeInline
	}
	return webui.SyncModeDeferred
}

func (p *boardPublisher) setMode(ctx context.Context, mode string) (webui.SyncState, error) {
	switch mode {
	case webui.SyncModeDeferred:
		p.inline.Store(false)
	case webui.SyncModeInline:
		p.inline.Store(true)
	default:
		return webui.SyncState{}, core.Errorf(
			core.CategoryValidation,
			"publication mode must be %q or %q",
			webui.SyncModeDeferred, webui.SyncModeInline,
		)
	}
	return p.state(ctx), nil
}

func (p *boardPublisher) watcherAnswers() bool {
	client, err := syncloop.Dial(p.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return false
	}
	defer client.Close()
	status, err := client.Status()
	return err == nil && status.Trustworthy(time.Now())
}

// publish takes a mutation's result and error so a handler closure can wrap its
// service call in one expression.
//
// It never turns a successful write into a failed request. The local commit is
// the durable result, and refusing to acknowledge recorded work because the
// network is unavailable would defeat the local-first design.
func (p *boardPublisher) publish(
	ctx context.Context,
	result core.MutationResult,
	err error,
) (core.MutationResult, error) {
	if err != nil {
		return result, err
	}
	if !p.repository.HasOrigin(ctx) {
		return result, nil
	}
	if !p.inline.Load() && p.handOff(result.Task.ID) {
		return result, nil
	}
	if _, pushErr := p.repository.PushTask(ctx, p.config, result.Task.ID); pushErr != nil {
		result.Warnings = append(result.Warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change was recorded locally, but publishing it failed: " + pushErr.Error(),
		})
	}
	return result, nil
}

// handOff reports whether a trustworthy watcher accepted the change.
//
// The socket is dialed rather than the in-process loop being called directly,
// because `serve` runs no loop of its own when an external watcher already owns
// the repository. Dialing covers both, and reuses the path the CLI exercises.
//
// An untrustworthy watcher is refused for the same reason the CLI refuses one:
// a watcher whose last synchronization failed knows origin is unreachable, and
// accepting its receipt would swallow the warning that says so.
func (p *boardPublisher) handOff(taskID string) bool {
	client, err := syncloop.Dial(p.repository.CommonGitDir, watcherProbeDeadline)
	if err != nil {
		return false
	}
	defer client.Close()
	status, err := client.Status()
	if err != nil || !status.Trustworthy(time.Now()) {
		return false
	}
	return client.Nudge(taskID) == nil
}

// serveWatcher runs a sync loop alongside the board.
//
// Whoever binds the socket owns the loop. A board that finds an external
// watcher already answering runs none and retries each interval, so the two
// never both fetch, the board still starts, and a watcher's death is picked up
// within one interval.
func serveWatcher(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	store *projection.Store,
	stderr io.Writer,
) *boardWatcher {
	watcher := &boardWatcher{finished: make(chan struct{})}
	go func() {
		defer close(watcher.finished)
		for ctx.Err() == nil {
			err := syncloop.Run(ctx, syncloop.Options{
				CommonGitDir: repository.CommonGitDir,
				Repository:   repository,
				Config:       config,
				Projection:   store,
				Stderr:       stderr,
			})
			if !errors.Is(err, syncloop.ErrWatcherLive) {
				if err != nil && ctx.Err() == nil {
					fmt.Fprintf(stderr, "workbook: board synchronization stopped: %s\n", err)
				}
				return
			}
			watcher.announceExternal(stderr)
			select {
			case <-ctx.Done():
				return
			case <-time.After(syncloop.DefaultInterval):
			}
		}
	}()
	return watcher
}

type boardWatcher struct {
	finished chan struct{}
	once     sync.Once
}

func (w *boardWatcher) announceExternal(stderr io.Writer) {
	w.once.Do(func() {
		fmt.Fprintln(stderr, "Workbook board: an external sync watcher owns this repository; not starting a second one.")
	})
}

// stop waits for the loop to finish, including its final synchronization, so
// the board's shutdown does not race publication. The loop and the HTTP drain
// run concurrently rather than in series, so shutdown stays inside one budget.
func (w *boardWatcher) stop() {
	select {
	case <-w.finished:
	case <-time.After(syncloop.DefaultShutdown + time.Second):
	}
}

func openService(ctx context.Context, cwd string, stderr io.Writer) (core.Service, error) {
	service, _, _, err := openServiceParts(ctx, cwd, stderr)
	return service, err
}

// openServiceParts also returns the repository and projection the service was
// built on, so a long-running command can share them with a sync loop instead
// of opening a second projection handle on the same cache file.
func openServiceParts(ctx context.Context, cwd string, stderr io.Writer) (core.Service, *gitstore.Repository, *projection.Store, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return core.Service{}, nil, nil, err
	}
	actor, err := repository.Actor(ctx)
	if err != nil {
		return core.Service{}, nil, nil, err
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, nil, nil, err
	}
	// The project's own status vocabulary, not the built-in default. This is
	// what turns the per-project statuses on for real: every projected task
	// resolves its stored status through the configured forwarding chains, and
	// every mutation settles a stale token against them.
	vocabulary, err := repository.LoadVocabulary(ctx)
	if err != nil {
		return core.Service{}, nil, nil, err
	}
	return core.Service{
		Config:     config,
		Vocabulary: vocabulary,
		Reader:     store,
		Writer:     repository,
		Projection: store,
		History:    store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
		Actor:      actor,
	}, repository, store, nil
}

func openReadService(ctx context.Context, cwd string, stderr io.Writer) (core.Service, error) {
	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return core.Service{}, err
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	vocabulary, err := repository.LoadVocabulary(ctx)
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

// openRepository opens the repository a command runs against and loads its
// configuration.
//
// It is also the one place a command reports identity drift. Resolving the
// canonical identity can find an advisory record missing or a private guard
// that had to be repaired; none of that stops the command, and none of it
// should be silent either. Every command funnels through here, so saying it
// once here says it once per command.
func openRepository(ctx context.Context, cwd string, stderr io.Writer) (*gitstore.Repository, core.ProjectConfig, error) {
	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return nil, core.ProjectConfig{}, err
	}
	config, err := repository.LoadConfig()
	if err != nil {
		return nil, core.ProjectConfig{}, err
	}
	if drift, found := repository.IdentityDrift(); found && stderr != nil {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", drift.Detail)
	}
	return repository, config, nil
}
