package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/historyvalidation"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/webui"
)

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
		err = runSync(ctx, commandArgs, cwd, stdout)
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
		if kind == stringFlag && !hasValue {
			index++
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
	if *jsonMode {
		writeResult(stdout, "fetch", result)
	} else {
		writeSyncResult(stdout, result)
	}
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

func runSync(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("sync")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	result, syncErr := repository.Sync(ctx, config)
	if *jsonMode {
		writeResult(stdout, "sync", result)
	} else {
		writeSyncRunResult(stdout, result)
	}
	return syncErr
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
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return core.Errorf(core.CategoryValidation, "title is required")
	}
	result, err := service.CreateMutation(ctx, core.CreateInput{
		Title:       title,
		Description: *description,
		Status:      core.Status(*status),
		Priority:    core.Priority(*priority),
		Labels:      labels.values,
	})
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, "create", result, *jsonMode)
	return nil
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
	flags := newFlagSet("show")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openReadService(ctx, cwd)
	if err != nil {
		return err
	}
	task, err := service.Show(ctx, id)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "show", task)
	} else {
		writeShow(stdout, task)
	}
	return nil
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

	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	result, err := service.UpdateMutation(ctx, id, input)
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, "update", result, *jsonMode)
	return nil
}

func runDelete(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("delete", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("delete")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	result, err := service.DeleteMutation(ctx, id)
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, "delete", result, *jsonMode)
	return nil
}

func runRestore(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("restore", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("restore")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	result, err := service.RestoreMutation(ctx, id)
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, "restore", result, *jsonMode)
	return nil
}

func runMove(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	id, args, err := requiredFirstArgument("move", "task ID", args)
	if err != nil {
		return err
	}
	flags := newFlagSet("move")
	before := flags.String("before", "", "move before task ID")
	after := flags.String("after", "", "move after task ID")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if (*before == "") == (*after == "") {
		return core.Errorf(core.CategoryInvocation, "move requires exactly one of --before or --after")
	}
	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	result, err := service.MoveMutation(ctx, id, core.MoveInput{Before: *before, After: *after})
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, "move", result, *jsonMode)
	return nil
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
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, err := openService(ctx, cwd)
	if err != nil {
		return err
	}
	var result core.MutationResult
	if command == "depend" {
		result, err = service.DependMutation(ctx, ids[0], ids[1])
	} else {
		result, err = service.FreeMutation(ctx, ids[0], ids[1])
	}
	if err != nil {
		return err
	}
	writeMutationResult(stdout, stderr, command, result, *jsonMode)
	return nil
}

func runNext(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("next")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	service, err := openReadService(ctx, cwd)
	if err != nil {
		return err
	}
	task, err := service.Next(ctx)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "next", task)
	} else if task == nil {
		fmt.Fprintln(stdout, "No eligible task.")
	} else {
		writeShow(stdout, *task)
	}
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
	return core.Service{Config: config, Reader: store, IDs: core.CryptoULIDSource{}, Now: time.Now}, nil
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
