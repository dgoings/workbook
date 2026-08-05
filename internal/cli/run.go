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
		err = runList(ctx, commandArgs, cwd, stdout)
	case "board":
		err = runBoard(ctx, commandArgs, cwd, stdout)
	case "show":
		err = runShow(ctx, commandArgs, cwd, stdout)
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
		err = runNext(ctx, commandArgs, cwd, stdout)
	case "rebuild":
		err = runRebuild(ctx, commandArgs, cwd, stdout)
	case "validate":
		err = runValidate(ctx, commandArgs, cwd, stdout)
	case "version":
		err = runVersion(commandArgs, stdout)
	case "fetch":
		err = runFetch(ctx, commandArgs, cwd, stdout)
	case "push":
		err = runPush(ctx, commandArgs, cwd, stdout)
	case "sync":
		err = runSync(ctx, commandArgs, cwd, stdout, stderr)
	case "config":
		err = runConfig(ctx, commandArgs, cwd, stdout)
	case "docs":
		err = runDocs(ctx, commandArgs, cwd, stdout)
	case "hooks":
		err = runHooks(ctx, commandArgs, cwd, stdout)
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

func runFetch(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("fetch")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	result, syncErr := repository.Fetch(ctx, config)
	writeSyncPhaseResult(stdout, "fetch", result, result.Conflicts, *jsonMode, func(output io.Writer) {
		writeSyncResult(output, result)
	})
	return syncErr
}

func runPush(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("push")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	result, syncErr := repository.Push(ctx, config)
	if *jsonMode {
		writeResult(stdout, "push", result)
	} else {
		writeSyncResult(stdout, result)
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
	repository, config, err := openRepository(ctx, cwd)
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
	writeSyncPhaseResult(stdout, "sync", result, result.Fetch.Conflicts, *jsonMode, func(output io.Writer) {
		writeSyncRunResult(output, result)
	})
	return syncErr
}

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

func runHooks(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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
	repository, _, err := openRepository(ctx, cwd)
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
	session, err := openTaskSession(ctx, cwd, *noSync, true)
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

func runList(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("list")
	status := flags.String("status", "", "task status")
	priority := flags.String("priority", "", "task priority")
	label := flags.String("label", "", "task label")
	all := flags.Bool("all", false, "include tombstoned tasks")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openReadService(ctx, cwd)
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
	if *jsonMode {
		writeResult(stdout, "list", tasks)
	} else {
		if err := writeList(stdout, tasks); err != nil {
			return core.Wrap(core.CategoryOperational, "render list", err)
		}
	}
	return nil
}

func runShow(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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

	service, err := openReadService(ctx, cwd)
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

	session, err := openTaskSession(ctx, cwd, *noSync, true)
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

	session, err := openTaskSession(ctx, cwd, *noSync, true)
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

	session, err := openTaskSession(ctx, cwd, *noSync, true)
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
	session, err := openTaskSession(ctx, cwd, *noSync, true)
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
	session, err := openTaskSession(ctx, cwd, *noSync, true)
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

func runNext(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("next")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	session, err := openTaskSession(ctx, cwd, *noSync, false)
	if err != nil {
		return err
	}
	session.fetchBefore(ctx)
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

func runRebuild(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("rebuild")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
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

func runValidate(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("validate")
	full := flags.Bool("full", false, "bypass cached validation results")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
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
}

func runServe(ctx context.Context, args []string, cwd string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("serve")
	addr := flags.String("addr", "127.0.0.1:7331", "listener address")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	handler := webui.NewHandlerWithTaskMutations(
		func(requestContext context.Context) ([]core.Task, error) {
			return service.List(requestContext, core.ListFilter{All: true})
		},
		func(requestContext context.Context, input core.CreateInput) (core.MutationResult, error) {
			return service.CreateMutation(requestContext, input)
		},
		func(requestContext context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
			return service.UpdateMutation(requestContext, id, input)
		},
		func(requestContext context.Context, id string, status core.Status) (core.MutationResult, error) {
			return service.UpdateMutation(requestContext, id, core.UpdateInput{Status: &status})
		},
		func(requestContext context.Context, id string, input core.PlaceInput) (core.MutationResult, error) {
			return service.PlaceMutation(requestContext, id, input)
		},
		func(requestContext context.Context, id string) (core.MutationResult, error) {
			return service.DeleteMutation(requestContext, id)
		},
		func(requestContext context.Context, id string) (core.MutationResult, error) {
			return service.RestoreMutation(requestContext, id)
		},
		func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
			return service.DependMutation(requestContext, id, dependency)
		},
		func(requestContext context.Context, id, dependency string) (core.MutationResult, error) {
			return service.FreeMutation(requestContext, id, dependency)
		},
	)
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "open board listener", err)
	}
	fmt.Fprintf(stderr, "Workbook board: http://%s\n", listener.Addr())
	if err := webui.Serve(ctx, listener, handler); err != nil {
		return core.Wrap(core.CategoryOperational, "serve board", err)
	}
	return nil
}

func openService(ctx context.Context, cwd string) (core.Service, error) {
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return core.Service{}, err
	}
	actor, err := repository.Actor(ctx)
	if err != nil {
		return core.Service{}, err
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	return core.Service{
		Config:     config,
		Reader:     store,
		Writer:     repository,
		Projection: store,
		IDs:        core.CryptoULIDSource{},
		Now:        time.Now,
		Actor:      actor,
	}, nil
}

func openReadService(ctx context.Context, cwd string) (core.Service, error) {
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return core.Service{}, err
	}
	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return core.Service{}, err
	}
	return core.Service{
		Config:  config,
		Reader:  store,
		History: store,
		IDs:     core.CryptoULIDSource{},
		Now:     time.Now,
	}, nil
}

func openRepository(ctx context.Context, cwd string) (*gitstore.Repository, core.ProjectConfig, error) {
	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return nil, core.ProjectConfig{}, err
	}
	config, err := repository.LoadConfig()
	if err != nil {
		return nil, core.ProjectConfig{}, err
	}
	return repository, config, nil
}
