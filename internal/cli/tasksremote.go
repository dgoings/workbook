package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

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
		// A first line only: the spelling of an address cannot prove two
		// repositories differ. move-tasks and setup decide it by what the
		// two hold (gitstore refuseUnfitDestination, CheckDistinctTasksRemote).
		if origin := repository.GitRemoteURL(ctx, core.DefaultTasksRemote); origin != "" &&
			sameAddress(repository.Root, origin, setting.URL) {
			return core.Errorf(core.CategoryValidation,
				"%s is origin's address, the code's own repository; give the address of the repository that is to hold the tasks",
				setting.URL)
		}
	} else if before.TasksRemote.Chosen() {
		// No supported undo: once origin is marked, sharing tasks through it
		// again would point every checkout at a place they all refuse.
		moved, err := repository.OriginMarkedMoved(ctx)
		if err != nil {
			return err
		}
		if moved != nil {
			return gitstore.MovedForGood(*moved)
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
		// Where the tasks are now: origin, or, once they have moved away from
		// it, the tasks remote the settings named until this change.
		source := core.DefaultTasksRemote
		if before.TasksRemote.Chosen() && before.TasksRemote.Name != setting.Name {
			if moved, err := repository.OriginMarkedMoved(ctx); err == nil && moved != nil {
				source = before.TasksRemote.Name
			}
		}
		fmt.Fprintf(stdout, "Address:\t%s\n", result.URL)
		fmt.Fprintf(stdout, "Next:\trun workbook move-tasks to copy the tasks from %s to %s and mark %s, "+
			"then commit %s; everyone else runs workbook setup after pulling it\n",
			source, setting.Name, source, ".workbook/config.json")
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
	from := flags.String("from", "", "the remote the tasks are in now (default origin, or the place origin's moved marker leads to)")
	allowBranches := flags.Bool("allow-branches", false, "accept a tasks remote that holds branches")
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
	// repeat, and the error says to run the command again. A run that set
	// operations aside for a conflict did finish, so it reports what it did
	// and then the conflict.
	result, err := repository.MoveTasks(ctx, config, gitstore.MoveTasksOptions{From: *from, AllowBranches: *allowBranches})
	if err != nil && !result.Verified {
		return err
	}
	if *jsonMode {
		writeResult(stdout, "move-tasks", result)
		return err
	}
	fmt.Fprintf(stdout, "Moved:\t%s (%s)\t->\t%s (%s)\n", result.From.Remote, result.From.URL, result.To.Remote, result.To.URL)
	fmt.Fprintf(stdout, "Tasks:\t%d\t(%d at the same commit, %d holding every operation under other commits)\n",
		result.Tasks, result.Identical, result.Carried)
	switch result.Marked {
	case "marked":
		fmt.Fprintf(stdout, "Marked:\t%s\t(checkouts still using it are refused and told where the tasks went)\n", result.From.Remote)
	case "already":
		fmt.Fprintf(stdout, "Marked:\t%s\t(already marked by an earlier run)\n", result.From.Remote)
	}
	if stragglers := result.Stragglers; stragglers != nil {
		fmt.Fprintf(stdout, "Carried:\t%d operation(s) on %s by %s\t(found at %s and missing at %s)\n",
			stragglers.Operations, strings.Join(stragglers.Tasks, ", "), strings.Join(stragglers.Actors, ", "),
			result.From.Remote, result.To.Remote)
	}
	for _, aside := range result.SetAside {
		where := "parked earlier in this checkout"
		if aside.ParkedRef != "" {
			where = "parked at " + aside.ParkedRef
		}
		fmt.Fprintf(stdout, "Set aside:\t%s\t%d operation(s) that conflict with its history at %s, %s\n",
			aside.TaskID, len(aside.Operations), result.To.Remote, where)
	}
	if result.Config == "not-carried" {
		fmt.Fprintf(stdout, "Config:\tnot carried\t(%s holds configuration changes this checkout's have diverged from; "+
			"make them again with workbook config set)\n", result.From.Remote)
	}
	fmt.Fprintf(stdout, "Next:\tcommit .workbook/config.json and push it; everyone else pulls it and runs workbook setup\n")
	return err
}

// sameAddress reports two spellings of one address: equal once a trailing
// slash, a .git suffix and a file:// scheme are dropped, and, for a local
// path, once it is made absolute against root and its links are resolved. It
// does not fold case, because two paths that differ only in case are two
// repositories on most file systems.
func sameAddress(root, a, b string) bool {
	return normalizedAddress(root, a) == normalizedAddress(root, b)
}

func normalizedAddress(root, address string) string {
	address = strings.TrimSpace(address)
	local := strings.HasPrefix(address, "file://")
	address = strings.TrimPrefix(address, "file://")
	address = strings.TrimRight(address, "/")
	address = strings.TrimSuffix(address, ".git")
	address = strings.TrimRight(address, "/")
	if !local && strings.Contains(address, "://") {
		return address
	}
	if !local && !filepath.IsAbs(address) && !strings.HasPrefix(address, ".") && strings.Contains(address, ":") {
		// scp-like: host:path
		return address
	}
	if !filepath.IsAbs(address) {
		address = filepath.Join(root, address)
	}
	for _, candidate := range []string{address, address + ".git"} {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			return strings.TrimSuffix(filepath.Clean(resolved), ".git")
		}
	}
	return filepath.Clean(address)
}
