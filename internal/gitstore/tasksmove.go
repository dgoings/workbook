package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// MoveTasksResult reports one run of `workbook move-tasks`.
type MoveTasksResult struct {
	// From is the remote the tasks were copied from and that is now marked.
	From core.TasksRemoteTarget `json:"from"`
	// To is the project's tasks remote, which now holds them.
	To core.TasksRemoteTarget `json:"to"`
	// Remote reports what this run did about the tasks remote in this
	// checkout: "added" from the settings, or "kept" as it was.
	Remote string `json:"remote"`
	// Marked is "marked" when this run marked the old place, and "already"
	// when an earlier run had.
	Marked string `json:"marked"`
	// Tasks is the number of task refs the old place holds.
	Tasks int `json:"tasks"`
	// Identical counts those the new place holds at exactly the same commit,
	// and Ahead those it holds at a descendant: edits made at the new place
	// after they were copied. Every task ref is one or the other, or the run
	// fails.
	Identical int `json:"identical"`
	Ahead     int `json:"ahead"`
	// Copy and Recopy are the two synchronizations with the new place, before
	// and after the old place was marked.
	Copy   SyncRunResult `json:"copy"`
	Recopy SyncRunResult `json:"recopy"`
}

// switchRemote points this handle at name for the next synchronization and
// forgets everything it established about the remote it pointed at before:
// whether that remote agreed about the identity, and whether it was marked.
func (r *Repository) switchRemote(name string) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.remoteOverride = name
	r.originIdentity = originIdentityUnknown
	r.originMoved = nil
	r.identityReport = nil
}

// MoveTasks copies this project's tasks from the remote named from (origin
// when empty) to the tasks remote its settings name, marks the old place so
// every checkout that has not picked up the settings is refused there with a
// message saying where the tasks went, and proves that the new place holds
// every task ref the old place does.
//
// Every step is safe to repeat, so an interrupted run is finished by running
// it again:
//
//  1. read the old place, folding its tasks into this checkout's;
//  2. synchronize with the new place, which publishes them there;
//  3. mark the old place: one fast-forward of its refs/workbook/project;
//  4. read the old place again, for edits published there before the mark;
//  5. synchronize with the new place again, publishing those;
//  6. compare every task ref of the two places.
//
// Until step 3 the old place is unmarked, and a checkout still on the old
// settings goes on publishing there; step 4 is what carries those edits
// across. After it, such a checkout is refused before it records anything.
func (r *Repository) MoveTasks(ctx context.Context, config core.ProjectConfig, from string) (MoveTasksResult, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return MoveTasksResult{}, err
	}
	tracked, exists, err := r.readConfig()
	if err != nil {
		return MoveTasksResult{}, err
	}
	if !exists || !tracked.TasksRemote.Chosen() {
		return MoveTasksResult{}, core.Errorf(core.CategoryValidation,
			"this project's settings name no tasks remote to move to; "+
				"run workbook config set tasks-remote <name> --url <address> first")
	}
	to := tracked.TasksRemote
	if from == "" {
		from = core.DefaultTasksRemote
	}
	if err := core.ValidateTasksRemoteName(from); err != nil {
		return MoveTasksResult{}, core.Wrap(core.CategoryInvocation, "--from must name a Git remote", err)
	}
	if from == to.Name {
		return MoveTasksResult{}, core.Errorf(core.CategoryInvocation,
			"--from names %s, which is already this project's tasks remote", from)
	}
	if !r.hasGitRemote(ctx, from) {
		return MoveTasksResult{}, core.Errorf(core.CategoryNotFound,
			"this checkout has no Git remote named %q to move the tasks from", from)
	}
	setup, err := r.EnsureTasksRemote(ctx)
	if err != nil {
		return MoveTasksResult{}, err
	}
	result := MoveTasksResult{
		From:   core.TasksRemoteTarget{Remote: from, URL: redactURLCredentials(r.gitRemoteURL(ctx, from))},
		To:     core.TasksRemoteTarget{Remote: to.Name, URL: to.URL},
		Remote: setup.Status,
	}
	defer r.switchRemote("")

	// Both places are asked before anything is written, so a run without
	// access to one of them stops having changed nothing.
	r.switchRemote(to.Name)
	if _, err := r.ProbeTasksRemote(ctx); err != nil {
		return result, err
	}
	r.switchRemote(from)
	if _, err := r.ProbeTasksRemote(ctx); err != nil {
		return result, err
	}

	if err := r.moveStep("read"); err != nil {
		return result, err
	}
	if err := r.moveReadSource(ctx, config); err != nil {
		return result, err
	}
	if err := r.moveStep("copy"); err != nil {
		return result, err
	}
	r.switchRemote(to.Name)
	result.Copy, err = r.Sync(ctx, config)
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		return result, wrapKeepingCategory("cannot copy the tasks to "+to.Name, err)
	}

	if err := r.moveStep("mark"); err != nil {
		return result, err
	}
	r.switchRemote(from)
	marked, err := r.markTasksMoved(ctx, from, to)
	if err != nil {
		return result, err
	}
	result.Marked = marked

	if err := r.moveStep("reread"); err != nil {
		return result, err
	}
	if err := r.moveReadSource(ctx, config); err != nil {
		return result, err
	}
	if err := r.moveStep("recopy"); err != nil {
		return result, err
	}
	r.switchRemote(to.Name)
	result.Recopy, err = r.Sync(ctx, config)
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		return result, wrapKeepingCategory("cannot copy the tasks to "+to.Name, err)
	}

	if err := r.moveStep("verify"); err != nil {
		return result, err
	}
	if err := r.verifyMovedTasks(ctx, from, to.Name, &result); err != nil {
		return result, err
	}
	return result, nil
}

// moveReadSource reads the old place into this checkout. Once the old place
// is marked the identity stage reports it as moved and reads it all the same,
// because --from named it rather than the settings.
func (r *Repository) moveReadSource(ctx context.Context, config core.ProjectConfig) error {
	_, err := r.Fetch(ctx, config)
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		return wrapKeepingCategory("cannot read the tasks from "+r.remote(), err)
	}
	return nil
}

// markTasksMoved publishes the marker on from's identity ref. The marker
// commit is deterministic, so a repeated run pushes the same commit and Git
// reports it up to date.
func (r *Repository) markTasksMoved(ctx context.Context, from string, to core.TasksRemoteSetting) (string, error) {
	local, found, err := r.readIdentityRef(ctx, identityRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", core.Errorf(core.CategoryCorruptData, "%s is missing; rerun workbook setup", identityRef)
	}
	moved := TasksMoved{
		Format:    tasksMovedFormat,
		Version:   tasksMovedVersion,
		ProjectID: local.Identity.ProjectID,
		Remote:    to.Name,
		URL:       to.URL,
	}
	marker, err := r.writeTasksMovedCommit(ctx, local, moved)
	if err != nil {
		return "", err
	}
	tracking := remoteIdentityRefFor(from)
	listing, err := r.listIdentityRefs(ctx, tracking)
	if err != nil {
		return "", err
	}
	if listing.Heads[tracking] == marker {
		return "already", nil
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil,
		"push", "--porcelain", from, marker+":"+identityRef)
	if push.err != nil {
		return "", core.Wrap(core.CategoryOperational,
			fmt.Sprintf("cannot mark %s as the place the tasks moved away from; the tasks are copied to %s, "+
				"and running workbook move-tasks again finishes the move", from, to.Name),
			gitCommandResultError(push))
	}
	if _, err := r.Git(ctx, nil, "update-ref", "--no-deref", tracking, marker); err != nil {
		return "", core.Wrap(core.CategoryOperational, "cannot record the published marker", err)
	}
	return "marked", nil
}

// verifyMovedTasks lists both places' task refs afresh and requires the new
// place to hold every task the old place holds, at the same commit or at one
// built on it. Commit IDs name the whole history below them, so an equal
// commit is the same operations with the same IDs, and a descendant is those
// operations and more.
func (r *Repository) verifyMovedTasks(ctx context.Context, from, to string, result *MoveTasksResult) error {
	for _, remote := range []string{from, to} {
		if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", remote,
			"+"+taskRefPrefix+"*:"+remoteTaskRefPrefixFor(remote)+"*"); err != nil {
			return core.Wrap(core.CategoryOperational, "cannot list the task refs "+remote+" holds", err)
		}
	}
	source, err := r.trackingRefHeads(ctx, remoteTaskRefPrefixFor(from))
	if err != nil {
		return err
	}
	destination, err := r.trackingRefHeads(ctx, remoteTaskRefPrefixFor(to))
	if err != nil {
		return err
	}
	names := make([]string, 0, len(source))
	for name := range source {
		names = append(names, name)
	}
	sort.Strings(names)
	var missing []string
	for _, name := range names {
		have, found := destination[name]
		switch {
		case !found:
			missing = append(missing, name+" (absent)")
		case have == source[name]:
			result.Identical++
		default:
			if _, err := r.Git(ctx, nil, "merge-base", "--is-ancestor", source[name], have); err != nil {
				missing = append(missing, name+" (holds a different history)")
				continue
			}
			result.Ahead++
		}
	}
	result.Tasks = len(source)
	if len(missing) > 0 {
		return core.Errorf(core.CategoryConflict,
			"%s does not hold every task %s holds: %s; run workbook move-tasks again",
			to, from, strings.Join(missing, ", "))
	}
	return nil
}

// trackingRefHeads lists one tracking namespace by task ID, without reading
// any object.
func (r *Repository) trackingRefHeads(ctx context.Context, prefix string) (map[string]string, error) {
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string)
	for _, line := range strings.Split(string(bytes.TrimSpace(output)), "\n") {
		objectID, refName, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		heads[strings.TrimPrefix(refName, prefix)] = objectID
	}
	return heads, nil
}

// CheckMoveDone refuses setup in a checkout whose project chose a tasks remote
// while origin, the code's remote, still holds task refs and is not marked as
// moved: the person who chose the remote has not run move-tasks, or it was
// interrupted before the mark, and joining now would publish to one place
// while teammates publish to the other.
//
// It is best effort. A checkout that cannot reach origin, or a project that
// shares its tasks through origin, is not checked.
func (r *Repository) CheckMoveDone(ctx context.Context) error {
	tracked, exists, err := r.readConfig()
	if err != nil || !exists || !tracked.TasksRemote.Chosen() {
		return err
	}
	from := core.DefaultTasksRemote
	if !r.hasGitRemote(ctx, from) {
		return nil
	}
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", from, identityRef, taskRefPrefix+"*")
	if err != nil {
		return nil
	}
	tasks := false
	for _, line := range strings.Split(string(listing), "\n") {
		if _, refName, found := strings.Cut(line, "\t"); found && strings.HasPrefix(refName, taskRefPrefix) {
			tasks = true
			break
		}
	}
	if !tasks {
		return nil
	}
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", from,
		identityFetchRefspecFor(from)); err != nil {
		return nil
	}
	record, found, err := r.readIdentityRef(ctx, remoteIdentityRefFor(from))
	if err != nil {
		return err
	}
	if found && record.Moved != nil {
		return nil
	}
	to := tracked.TasksRemote
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteNotMoved,
			Remote:  to.Name,
			URL:     to.URL,
			MovedTo: &core.TasksRemoteTarget{Remote: to.Name, URL: to.URL},
			Command: "workbook move-tasks",
		},
		"this project's settings say its tasks are in %s (remote %q), but origin still holds them and is not marked as moved, "+
			"so the move has not finished; whoever set tasks-remote runs workbook move-tasks, and this checkout runs workbook setup after that",
		to.URL, to.Name)
}

// wrapKeepingCategory adds context to err without changing its exit code.
func wrapKeepingCategory(message string, err error) error {
	category := core.CategoryOf(err)
	if category == "" {
		category = core.CategoryOperational
	}
	return core.Wrap(category, message, err)
}

func (r *Repository) moveStep(step string) error {
	if r.moveStepHook == nil {
		return nil
	}
	return r.moveStepHook(step)
}
