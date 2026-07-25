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
	"github.com/dgoings/workbook/internal/webui"
)

type initResult struct {
	Repository string `json:"repository"`
	ProjectID  string `json:"projectId"`
	Key        string `json:"key"`
	TaskCount  int    `json:"taskCount"`
}

func Run(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) int {
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
	case "init":
		err = runInit(ctx, commandArgs, cwd, stdout)
	case "create":
		err = runCreate(ctx, commandArgs, cwd, stdout)
	case "list":
		err = runList(ctx, commandArgs, cwd, stdout)
	case "board":
		err = runBoard(ctx, commandArgs, cwd, stdout)
	case "show":
		err = runShow(ctx, commandArgs, cwd, stdout)
	case "update":
		err = runUpdate(ctx, commandArgs, cwd, stdout)
	case "delete":
		err = runDelete(ctx, commandArgs, cwd, stdout)
	case "fetch":
		err = runFetch(ctx, commandArgs, cwd, stdout)
	case "push":
		err = runPush(ctx, commandArgs, cwd, stdout)
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

func runHooks(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	subcommand, args, err := requiredFirstArgument("hooks", "hook command", args)
	if err != nil {
		return err
	}
	if subcommand != "install" {
		return core.Errorf(core.CategoryInvocation, "unknown hooks command %q", subcommand)
	}
	flags := newFlagSet("hooks")
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

func runInit(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("init")
	key := flags.String("key", "WB", "project key")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return err
	}
	config, _, err := repository.Init(ctx, *key, core.CryptoULIDSource{})
	if err != nil {
		return err
	}
	tasks, err := repository.List(ctx, config)
	if err != nil {
		return err
	}
	result := initResult{
		Repository: repository.Root,
		ProjectID:  config.ProjectID,
		Key:        config.Key,
		TaskCount:  len(tasks),
	}
	if *jsonMode {
		writeResult(stdout, "init", result)
	} else {
		fmt.Fprintf(stdout, "Repository:\t%s\n", result.Repository)
		fmt.Fprintf(stdout, "Project ID:\t%s\n", result.ProjectID)
		fmt.Fprintf(stdout, "Key:\t%s\n", result.Key)
		fmt.Fprintf(stdout, "Tasks:\t%d\n", result.TaskCount)
	}
	return nil
}

func runCreate(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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
	task, err := service.Create(ctx, core.CreateInput{
		Title:       title,
		Description: *description,
		Status:      core.Status(*status),
		Priority:    core.Priority(*priority),
		Labels:      labels.values,
	})
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "create", task)
	} else {
		writeMutation(stdout, task)
	}
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

	service, err := openService(ctx, cwd)
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

	service, err := openService(ctx, cwd)
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

func runUpdate(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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
	task, err := service.Update(ctx, id, input)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "update", task)
	} else {
		writeMutation(stdout, task)
	}
	return nil
}

func runDelete(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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
	task, err := service.Delete(ctx, id)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "delete", task)
	} else {
		writeMutation(stdout, task)
	}
	return nil
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
	handler := webui.NewHandler(func(requestContext context.Context) ([]core.Task, error) {
		return service.List(requestContext, core.ListFilter{})
	})
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
	return core.Service{
		Config: config,
		Store:  repository,
		IDs:    core.CryptoULIDSource{},
		Now:    time.Now,
		Actor:  actor,
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
