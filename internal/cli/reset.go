package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/syncloop"
)

// resetWatcherWait bounds how long a reset waits for this checkout's watcher
// to finish its last synchronization and exit. It is a seam for tests.
var resetWatcherWait = 20 * time.Second

// resetSummary is the data member of reset's JSON result, and what its text
// output is rendered from. Refs lists what was removed, or on a dry run what
// would be.
type resetSummary struct {
	DryRun bool `json:"dryRun"`
	// Scope is "checkout" for a reset of this checkout and "remote" for
	// --remote.
	Scope string `json:"scope"`
	// Remote is the remote wiped, or for a checkout the tasks remote its
	// unpublished work is measured against; URL is its address.
	Remote string `json:"remote"`
	URL    string `json:"url,omitempty"`
	// MovedTo is set for a remote marked as a place the tasks moved away
	// from, whose identity ref, named in Kept, stays.
	MovedTo *core.TasksRemoteTarget `json:"movedTo,omitempty"`
	Refs    []gitstore.ResetRef     `json:"refs"`
	Kept    []string                `json:"kept"`
	Tasks   int                     `json:"tasks"`
	// Unpublished and SetAside are the work that existed only in this
	// checkout, for a reset of it.
	Unpublished []gitstore.UnpublishedRef `json:"unpublished"`
	SetAside    []string                  `json:"setAside"`
	// Unchecked is why the tasks remote could not be read, when it could
	// not: Unpublished is then what this checkout last saw it lacked.
	Unchecked string        `json:"unchecked,omitempty"`
	Watcher   *resetWatcher `json:"watcher,omitempty"`
	Caches    []string      `json:"caches"`
	Bundle    string        `json:"bundle,omitempty"`
	// Restore is the Git command that fetches the bundle's refs back.
	Restore string   `json:"restore,omitempty"`
	Next    []string `json:"next"`
}

// resetWatcher is this checkout's watcher as a reset found it.
type resetWatcher struct {
	PID     int  `json:"pid"`
	Stopped bool `json:"stopped"`
}

func runReset(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	given := append([]string{}, args...)
	flags := newFlagSet("reset")
	remote := flags.String("remote", "", "wipe this Git remote's Workbook refs instead of this checkout's")
	confirm := flags.String("confirm", "", "with --remote, the remote's name again, to wipe the place the tasks live")
	dryRun := flags.Bool("dry-run", false, "report what would be removed and change nothing")
	noBackup := flags.Bool("no-backup", false, "write no backup bundle")
	backup := flags.String("backup", "", "write the backup bundle here, or into this directory")
	discard := flags.Bool("discard-unpublished", false, "with --no-backup, remove work that exists only in this checkout")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	typed := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) { typed[visited.Name] = true })
	switch {
	case typed["remote"] && *remote == "":
		return core.Errorf(core.CategoryInvocation, "reset --remote requires a remote")
	case typed["confirm"] && !typed["remote"]:
		return core.Errorf(core.CategoryInvocation, "--confirm requires --remote")
	case *discard && typed["remote"]:
		return core.Errorf(core.CategoryInvocation, "--discard-unpublished is for a reset of this checkout and cannot be combined with --remote")
	case *discard && !*noBackup:
		return core.Errorf(core.CategoryInvocation,
			"--discard-unpublished requires --no-backup: without it the bundle keeps the work, and nothing needs discarding")
	case *noBackup && typed["backup"]:
		return core.Errorf(core.CategoryInvocation, "--no-backup and --backup cannot be combined")
	case typed["backup"] && *backup == "":
		return core.Errorf(core.CategoryInvocation, "reset --backup requires a path")
	}
	if *backup == "-" {
		// As for compact: typed as a path, "-" is a file of that name.
		*backup = "./-"
	}
	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return err
	}
	options := gitstore.ResetOptions{NoBackup: *noBackup, Backup: *backup, Flags: given}
	if typed["remote"] {
		return runResetRemote(ctx, repository, *remote, *confirm, *dryRun, options, *jsonMode, stdout, stderr)
	}
	return runResetCheckout(ctx, repository, *dryRun, *discard, options, *jsonMode, stdout)
}

// runResetCheckout wipes this checkout's Workbook data: its watcher first, so
// its last synchronization publishes what it held, then every ref under
// refs/workbook/, then the caches.
func runResetCheckout(
	ctx context.Context,
	repository *gitstore.Repository,
	dryRun, discard bool,
	options gitstore.ResetOptions,
	jsonMode bool,
	stdout io.Writer,
) error {
	projectID, remote := repository.ResetSettings()
	watcher, err := stopResetWatcher(repository.CommonGitDir, dryRun)
	if err != nil {
		return err
	}
	plan, err := repository.PlanLocalReset(ctx, remote)
	if err != nil {
		return err
	}
	summary := resetSummary{
		DryRun: dryRun, Scope: "checkout", Remote: remote, URL: repository.GitRemoteURL(ctx, remote),
		Refs: emptyIfNil(plan.Refs), Kept: []string{}, Unpublished: emptyIfNil(plan.Unpublished),
		SetAside: emptyIfNil(plan.SetAside), Unchecked: plan.Unchecked, Watcher: watcher, Caches: []string{},
	}
	for _, ref := range plan.Refs {
		if strings.HasPrefix(ref.Ref, "refs/workbook/tasks/") {
			summary.Tasks++
		}
	}
	if options.NoBackup && plan.Holds() && !discard {
		return gitstore.NewResetRefusal(core.CategoryValidation, gitstore.ResetRefusal{
			Reason:      gitstore.ResetUnpublished,
			Remote:      remote,
			Command:     "workbook reset --no-backup --discard-unpublished",
			Unpublished: plan.Unpublished,
			SetAside:    plan.SetAside,
		}, "this checkout holds work that exists only here, which %s %s: %s. Run workbook sync to publish it, "+
			"or reset without --no-backup to keep it in a bundle, or add --discard-unpublished to lose it",
			remote, lacks(plan), describeLocalOnly(plan))
	}
	// A checkout with no project settings left, as leaving Workbook makes
	// it, has nothing to set up again.
	summary.Next = []string{}
	if tasksOnly, _ := repository.TasksOnly(ctx); tasksOnly {
		summary.Next = []string{"workbook setup --tasks-only " + summary.URL}
	} else if projectID != "" {
		summary.Next = []string{"workbook setup", "workbook sync"}
	}
	if !dryRun {
		result, err := repository.ResetLocal(ctx, projectID, plan, options)
		if err != nil {
			return err
		}
		summary.Bundle, summary.Caches = result.Bundle, emptyIfNil(result.Caches)
		if result.Bundle != "" {
			summary.Restore = repository.RestoreCommand(result.Bundle, plan.Refs)
		}
	}
	if jsonMode {
		writeResult(stdout, "reset", summary)
		return nil
	}
	writeResetSummary(stdout, summary)
	return nil
}

// runResetRemote wipes one remote's Workbook refs. A remote the tasks live
// on, rather than a marked place they left, is wiped only when --confirm
// names it, and always with the bundle.
func runResetRemote(
	ctx context.Context,
	repository *gitstore.Repository,
	remote, confirm string,
	dryRun bool,
	options gitstore.ResetOptions,
	jsonMode bool,
	stdout, stderr io.Writer,
) error {
	projectID, _ := repository.ResetSettings()
	plan, err := repository.PlanRemoteReset(ctx, remote)
	if err != nil {
		return err
	}
	summary, err := resetRemote(ctx, repository, projectID, plan, confirm, dryRun, options)
	if err != nil {
		var refusal *gitstore.ResetRefusalError
		if errors.As(err, &refusal) && refusal.Refusal.Bundle != "" && !jsonMode {
			fmt.Fprintf(stderr, "Backup:\t%s\n", refusal.Refusal.Bundle)
		}
		return err
	}
	if jsonMode {
		writeResult(stdout, "reset", summary)
		return nil
	}
	writeResetSummary(stdout, summary)
	return nil
}

// resetRemote refuses or carries out plan, the part of a remote's reset that
// move-tasks --clean-up shares.
func resetRemote(
	ctx context.Context,
	repository *gitstore.Repository,
	projectID string,
	plan gitstore.RemoteResetPlan,
	confirm string,
	dryRun bool,
	options gitstore.ResetOptions,
) (resetSummary, error) {
	summary := resetSummary{
		DryRun: dryRun, Scope: "remote", Remote: plan.Remote, URL: plan.URL, MovedTo: plan.MovedTo,
		Refs: emptyIfNil(plan.Refs), Kept: emptyIfNil(plan.Kept), Tasks: plan.Tasks,
		Unpublished: []gitstore.UnpublishedRef{}, SetAside: []string{}, Caches: []string{}, Next: []string{},
	}
	command := "workbook reset --remote " + plan.Remote + " --confirm " + plan.Remote
	if plan.Live() && len(plan.Refs) > 0 && !dryRun {
		if options.NoBackup {
			return summary, gitstore.NewResetRefusal(core.CategoryValidation, gitstore.ResetRefusal{
				Reason: gitstore.ResetBackupRequired, Remote: plan.Remote, Command: command, Tasks: plan.Tasks,
			}, "%s holds the project's tasks, not a copy left behind by a move, so its reset always writes the bundle first; run it without --no-backup", plan.Remote)
		}
		if confirm != plan.Remote {
			unreadable := ""
			if plan.Unreadable != "" {
				unreadable = " (its identity ref cannot be read, so it is treated as holding them: " + plan.Unreadable + ")"
			}
			return summary, gitstore.NewResetRefusal(core.CategoryValidation, gitstore.ResetRefusal{
				Reason: gitstore.ResetConfirm, Remote: plan.Remote, Command: command, Tasks: plan.Tasks,
			}, "%s holds the project's tasks, not a copy left behind by a move%s: resetting it removes all %d of them and every other Workbook ref there, "+
				"for everyone. A bundle of them is written first. To go ahead, run %s", plan.Remote, unreadable, plan.Tasks, command)
		}
	}
	if dryRun {
		return summary, nil
	}
	result, err := repository.ResetRemote(ctx, projectID, plan, options)
	if err != nil {
		return summary, err
	}
	summary.Bundle = result.Bundle
	if result.Bundle != "" {
		summary.Restore = gitstore.RemoteRestoreCommand(result.Bundle, plan.Remote)
	}
	if plan.Live() && plan.Remote == resetTasksRemote(repository) && len(plan.Refs) > 0 && holdsTasks(ctx, repository) {
		summary.Next = append(summary.Next, "workbook reset")
	}
	return summary, nil
}

func emptyIfNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// lacks says how the tasks remote was found to lack what this checkout holds.
func lacks(plan gitstore.LocalResetPlan) string {
	if plan.Unchecked != "" {
		return "lacked when this checkout last read it (it cannot be read now: " + plan.Unchecked + ")"
	}
	return "does not hold"
}

// describeLocalOnly names what exists only in this checkout, for a refusal.
func describeLocalOnly(plan gitstore.LocalResetPlan) string {
	var parts []string
	for _, item := range plan.Unpublished {
		parts = append(parts, describeUnpublished(item))
	}
	for _, ref := range plan.SetAside {
		parts = append(parts, "set-aside edits at "+ref)
	}
	return strings.Join(parts, "; ")
}

func describeUnpublished(item gitstore.UnpublishedRef) string {
	name := item.Ref
	if item.TaskID != "" {
		name = item.TaskID
		if item.Title != "" {
			name += " " + fmt.Sprintf("%q", singleLine(item.Title))
		}
	} else if item.Ref == "refs/workbook/config" {
		name = "the project configuration"
	}
	text := fmt.Sprintf("%s, %s", name, plural(len(item.Operations), "operation"))
	if item.New {
		text += ", never published"
	}
	return text
}

func writeResetSummary(output io.Writer, summary resetSummary) {
	verb := "Removed"
	if summary.DryRun {
		verb = "Would remove"
	}
	where := "this checkout"
	if summary.Scope == "remote" {
		where = summary.Remote
		if summary.URL != "" {
			where += " (" + summary.URL + ")"
		}
	}
	fmt.Fprintf(output, "%s:\t%s under refs/workbook/ at %s, %s\n", verb, plural(len(summary.Refs), "ref"), where, plural(summary.Tasks, "task"))
	for _, ref := range summary.Refs {
		if ref.Damaged != "" {
			fmt.Fprintf(output, "Damaged:\t%s\t(%s; nothing of it can be kept)\n", ref.Ref, ref.Damaged)
		}
	}
	for _, kept := range summary.Kept {
		target := ""
		if summary.MovedTo != nil {
			target = fmt.Sprintf(", which says the tasks moved to %s (%s)", summary.MovedTo.Remote, summary.MovedTo.URL)
		}
		fmt.Fprintf(output, "Kept:\t%s\t(the moved marker%s, so a checkout on old settings is still refused)\n", kept, target)
	}
	for _, item := range summary.Unpublished {
		fmt.Fprintf(output, "Unpublished:\t%s\n", describeUnpublished(item))
	}
	for _, ref := range summary.SetAside {
		fmt.Fprintf(output, "Set aside:\t%s\n", ref)
	}
	if summary.Unchecked != "" {
		fmt.Fprintf(output, "Unchecked:\t%s could not be read, so what it lacks is what this checkout last saw it lack: %s\n",
			summary.Remote, summary.Unchecked)
	}
	if summary.Watcher != nil {
		if summary.Watcher.Stopped {
			fmt.Fprintf(output, "Watcher:\tstopped (process %d)\n", summary.Watcher.PID)
		} else {
			fmt.Fprintf(output, "Watcher:\trunning (process %d); a real run stops it first\n", summary.Watcher.PID)
		}
	}
	if len(summary.Caches) > 0 {
		fmt.Fprintf(output, "Caches:\t%s removed\n", plural(len(summary.Caches), "file"))
	}
	if summary.Bundle != "" {
		fmt.Fprintf(output, "Backup:\t%s\n", summary.Bundle)
	}
	if summary.Restore != "" {
		if summary.Scope == "remote" {
			fmt.Fprintf(output, "Restore:\tin a new empty repository, %s\n", summary.Restore)
		} else {
			fmt.Fprintf(output, "Restore:\tbefore setting up again, %s\n", summary.Restore)
		}
	}
	switch {
	case summary.Scope == "checkout" && len(summary.Next) > 0:
		fmt.Fprintf(output, "Next:\t%s\n", strings.Join(summary.Next, ", then "))
	case summary.Scope == "remote" && len(summary.Next) > 0 && !summary.DryRun:
		fmt.Fprintf(output, "Next:\tthis checkout still holds the tasks and publishes them back on its next sync; "+
			"run workbook reset here too, unless that is what you want\n")
	}
}

// stopResetWatcher stops this checkout's watcher, if one answers, and waits
// for it to exit. Stopping it is a request to the process the watcher reports
// itself as: an interrupt, which runs its last synchronization and so
// publishes what it held, where the platform has one, and otherwise a kill. A
// dry run only reports it.
func stopResetWatcher(commonGitDir string, dryRun bool) (*resetWatcher, error) {
	client, err := syncloop.Dial(commonGitDir, watcherProbeDeadline)
	if err != nil {
		return nil, nil
	}
	status, err := client.Status()
	_ = client.Close()
	if err != nil || status.PID <= 0 {
		return nil, nil
	}
	watcher := &resetWatcher{PID: status.PID}
	if dryRun {
		return watcher, nil
	}
	refuse := func(detail string) error {
		return gitstore.NewResetRefusal(core.CategoryOperational, gitstore.ResetRefusal{
			Reason: gitstore.ResetWatcherRunning, Command: "workbook reset",
		}, "this checkout's watcher, process %d, %s; stop it (Ctrl-C in its terminal) and run workbook reset again", status.PID, detail)
	}
	if status.PID == os.Getpid() {
		return nil, refuse("runs in this same process")
	}
	process, err := os.FindProcess(status.PID)
	if err != nil {
		return nil, refuse("cannot be found: " + err.Error())
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if err := process.Kill(); err != nil {
			return nil, refuse("could not be stopped: " + err.Error())
		}
	}
	deadline := time.Now().Add(resetWatcherWait)
	for time.Now().Before(deadline) {
		client, err := syncloop.Dial(commonGitDir, watcherProbeDeadline)
		if err != nil {
			watcher.Stopped = true
			return watcher, nil
		}
		_ = client.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return nil, refuse(fmt.Sprintf("did not stop within %s", resetWatcherWait))
}

// holdsTasks reports whether this checkout still holds a task ref, which its
// next sync would publish back to a wiped remote. A checkout that cannot tell
// is told to reset, which is harmless where it holds nothing.
func holdsTasks(ctx context.Context, repository *gitstore.Repository) bool {
	output, err := repository.Git(ctx, nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/workbook/tasks/")
	return err != nil || strings.TrimSpace(string(output)) != ""
}

// resetTasksRemote is the tasks remote the settings name, origin by default.
func resetTasksRemote(repository *gitstore.Repository) string {
	_, remote := repository.ResetSettings()
	return remote
}
