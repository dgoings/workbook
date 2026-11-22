package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// tasksRemoteSettingName is the tracked-file setting naming the Git remote a
// project shares its tasks through when that is not origin.
const tasksRemoteSettingName = "tasks-remote"

// tasksRemoteView is the tasks remote as `config show` reports it.
type tasksRemoteView struct {
	// Name is the Git remote.
	Name string `json:"name"`
	// URL is the address the project's settings record, which setup adds
	// the remote from.
	URL string `json:"url,omitempty"`
	// CheckoutURL is the address this checkout's own Git configuration gives
	// the remote, with any credential removed, or absent when the checkout
	// does not have the remote yet.
	CheckoutURL string `json:"checkoutUrl,omitempty"`
}

func newTasksRemoteView(ctx context.Context, repository *gitstore.Repository, config core.ProjectConfig) *tasksRemoteView {
	if !config.TasksRemote.Chosen() {
		return nil
	}
	return &tasksRemoteView{
		Name:        config.TasksRemote.Name,
		URL:         config.TasksRemote.URL,
		CheckoutURL: repository.TasksRemote(ctx).URL,
	}
}

func writeTasksRemoteView(output io.Writer, view *tasksRemoteView) {
	if view == nil {
		return
	}
	fmt.Fprintf(output, "Tasks remote:\t%s\t%s\n", view.Name, view.URL)
	switch {
	case view.CheckoutURL == "":
		fmt.Fprintf(output, "  checkout:\tnot added yet\t(run workbook setup)\n")
	case view.CheckoutURL != view.URL:
		fmt.Fprintf(output, "  checkout:\t%s\t(this checkout's own address)\n", view.CheckoutURL)
	}
}

// runConfigSetTasksRemote records the remote a project's tasks live in.
//
// `config set tasks-remote origin` with no address is the same as unsetting
// it: the project shares its tasks through origin, which is what no setting
// means. An address beside origin is refused, because origin's address is the
// code's, and the setting exists to name a different repository.
func runConfigSetTasksRemote(
	ctx context.Context,
	cwd string,
	stdout, stderr io.Writer,
	name, address string,
	jsonMode bool,
) error {
	if name == core.DefaultTasksRemote {
		if address != "" {
			return core.Errorf(core.CategoryValidation,
				"origin is the code's own remote, so it cannot be given another address here; "+
					"name the tasks repository's remote, such as tasks, or run workbook config unset %s to share tasks through origin",
				tasksRemoteSettingName)
		}
		return writeTasksRemoteSetting(ctx, cwd, stdout, stderr, configSetCommand, core.TasksRemoteSetting{}, jsonMode)
	}
	if err := core.ValidateTasksRemoteName(name); err != nil {
		return err
	}
	if address == "" {
		return core.Errorf(core.CategoryValidation,
			"%s needs --url <address>: the address of the repository that holds the tasks, which workbook setup adds the remote from",
			tasksRemoteSettingName)
	}
	if err := core.ValidateTasksRemoteURL(address); err != nil {
		return err
	}
	return writeTasksRemoteSetting(ctx, cwd, stdout, stderr, configSetCommand,
		core.TasksRemoteSetting{Name: name, URL: address}, jsonMode)
}

func writeTasksRemoteSetting(
	ctx context.Context,
	cwd string,
	stdout, stderr io.Writer,
	command string,
	setting core.TasksRemoteSetting,
	jsonMode bool,
) error {
	repository, before, err := openRepositoryFor(ctx, cwd, stderr, "", false)
	if err != nil {
		return err
	}
	if setting.URL != "" {
		if tasksOnly, err := repository.TasksOnly(ctx); err != nil {
			return err
		} else if tasksOnly {
			return gitstore.TasksOnlyUnavailable("workbook config set "+tasksRemoteSettingName,
				"a tasks-only folder's origin is the tasks repository already, and its settings are not committed anywhere")
		}
		if origin := repository.GitRemoteURL(ctx, core.DefaultTasksRemote); origin != "" && origin == setting.URL {
			return core.Errorf(core.CategoryValidation,
				"%s is origin's address, the code's own repository; give the address of the repository that is to hold the tasks",
				setting.URL)
		}
	}
	updated, err := repository.SetProjectTasksRemote(ctx, setting)
	if err != nil {
		return err
	}
	result := configWriteResult{
		Path:     repository.Root + "/.workbook/config.json",
		Version:  updated.Version,
		Setting:  tasksRemoteSettingName,
		Value:    updated.TasksRemote.RemoteName(),
		URL:      updated.TasksRemote.URL,
		Upgraded: before.Version != updated.Version,
	}
	if jsonMode {
		writeResult(stdout, command, result)
		return nil
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", result.Setting, result.Value, result.Path)
	if result.URL != "" {
		fmt.Fprintf(stdout, "Address:\t%s\n", result.URL)
		fmt.Fprintf(stdout, "Next:\trun workbook move-tasks to copy the tasks there and mark origin, "+
			"then commit %s; everyone else runs workbook setup after pulling it\n", ".workbook/config.json")
	} else if before.TasksRemote.Chosen() {
		fmt.Fprintf(stdout, "Next:\tcommit %s; tasks are shared through origin again, "+
			"and %s keeps the copy it holds\n", ".workbook/config.json", before.TasksRemote.Name)
	}
	if result.Upgraded {
		fmt.Fprintf(stdout, "Upgraded the project configuration to version %d; commit it, and note that older Workbook versions cannot read it.\n", result.Version)
	}
	return nil
}

func runMoveTasks(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	flags := newFlagSet("move-tasks")
	from := flags.String("from", "", "the remote the tasks are in now (default origin)")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	repository, config, err := openRepositoryFor(ctx, cwd, stderr, "", false)
	if err != nil {
		return err
	}
	if tasksOnly, err := repository.TasksOnly(ctx); err != nil {
		return err
	} else if tasksOnly {
		return gitstore.TasksOnlyUnavailable("workbook move-tasks",
			"the move is made from a clone of the code, whose committed settings name the new place")
	}
	// A run that stops partway reports only its error: every step is safe to
	// repeat, and the error says to run the command again.
	result, err := repository.MoveTasks(ctx, config, *from)
	if err != nil {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "move-tasks", result)
		return nil
	}
	fmt.Fprintf(stdout, "Moved:\t%s (%s)\t->\t%s (%s)\n", result.From.Remote, result.From.URL, result.To.Remote, result.To.URL)
	fmt.Fprintf(stdout, "Tasks:\t%d\t(%d identical, %d with newer edits at %s)\n",
		result.Tasks, result.Identical, result.Ahead, result.To.Remote)
	switch result.Marked {
	case "marked":
		fmt.Fprintf(stdout, "Marked:\t%s\t(checkouts still using it are refused and told where the tasks went)\n", result.From.Remote)
	case "already":
		fmt.Fprintf(stdout, "Marked:\t%s\t(already marked by an earlier run)\n", result.From.Remote)
	}
	fmt.Fprintf(stdout, "Next:\tcommit .workbook/config.json and push it; everyone else pulls it and runs workbook setup\n")
	return nil
}
