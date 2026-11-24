package gitstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// A project shares its tasks through exactly one Git remote. It is origin
// unless the tracked .workbook/config.json names another, and every place that
// talks to a remote about tasks asks remote() which one that is, so the choice
// is made in one function rather than assumed in twenty.
//
// Each remote's copy of the Workbook namespace is mirrored under its own
// tracking root, refs/workbook/remotes/<remote>/, and a fetch from one remote
// prunes only that remote's root: the refspecs a fetch names are the only ones
// its --prune applies to.
const remoteTrackingRoot = "refs/workbook/remotes/"

func remoteTrackingPrefix(remote string) string { return remoteTrackingRoot + remote + "/" }

func remoteTaskRefPrefixFor(remote string) string { return remoteTrackingPrefix(remote) + "tasks/" }

// relicTrackingRefPrefixFor is where a fetch from remote mirrors its relic
// refs. Only `git fetch` writes there.
func relicTrackingRefPrefixFor(remote string) string { return remoteTrackingPrefix(remote) + "relics/" }

func relicFetchRefspecFor(remote string) string {
	return "+" + relicRefPrefix + "*:" + relicTrackingRefPrefixFor(remote) + "*"
}

func remoteIdentityRefFor(remote string) string { return remoteTrackingPrefix(remote) + "project" }

func remoteConfigRefFor(remote string) string { return remoteTrackingPrefix(remote) + "config" }

func identityFetchRefspecFor(remote string) string {
	return "+" + identityRef + "*:" + remoteIdentityRefFor(remote) + "*"
}

func configFetchRefspecFor(remote string) string {
	return "+" + configRef + "*:" + remoteConfigRefFor(remote) + "*"
}

// remote names the remote this handle synchronizes tasks with: the one a
// command named with --remote, or else the one the project's settings name,
// or else origin. It never falls back to origin for a project that named
// another remote, even one this checkout lacks: a Git command naming a remote
// that does not exist fails, and failing is the safe answer, where quietly
// using origin would publish edits to the place the tasks left.
func (r *Repository) remote() string {
	r.metadataMu.RLock()
	override, resolved := r.remoteOverride, r.remoteName
	r.metadataMu.RUnlock()
	if override != "" {
		return override
	}
	if resolved != "" {
		return resolved
	}
	name := r.configuredRemote()
	r.metadataMu.Lock()
	r.remoteName = name
	r.metadataMu.Unlock()
	return name
}

// configuredRemote reads the project's choice from the tracked file. A file
// that cannot be read answers origin here: every command loads and validates
// that file before it synchronizes anything, so such a command has already
// failed with the file's own error.
func (r *Repository) configuredRemote() string {
	tracked, exists, err := r.readConfig()
	if err != nil || !exists {
		return core.DefaultTasksRemote
	}
	return tracked.TasksRemote.RemoteName()
}

func (r *Repository) forgetTasksRemote() {
	r.metadataMu.Lock()
	r.remoteName = ""
	r.metadataMu.Unlock()
}

// isTrackingTaskRefPrefix recognizes one remote's task tracking namespace,
// refs/workbook/remotes/<remote>/tasks/, for a remote name Workbook accepts.
func isTrackingTaskRefPrefix(prefix string) bool {
	rest, found := strings.CutPrefix(prefix, remoteTrackingRoot)
	if !found {
		return false
	}
	name, found := strings.CutSuffix(rest, "/tasks/")
	return found && core.ValidateTasksRemoteName(name) == nil
}

func (r *Repository) remoteTaskRefPrefix() string { return remoteTaskRefPrefixFor(r.remote()) }

func (r *Repository) remoteRelicRefPrefix() string { return relicTrackingRefPrefixFor(r.remote()) }

func (r *Repository) remoteIdentityRef() string { return remoteIdentityRefFor(r.remote()) }

func (r *Repository) remoteConfigRef() string { return remoteConfigRefFor(r.remote()) }

func (r *Repository) identityFetchRefspec() string { return identityFetchRefspecFor(r.remote()) }

func (r *Repository) configFetchRefspec() string { return configFetchRefspecFor(r.remote()) }

// UseRemote makes this handle synchronize with name instead of the project's
// tasks remote, for `workbook fetch`, `push` and `sync --remote`. Those three
// are the only commands that may name another remote, and naming one is how a
// project's tasks are copied from one remote to another.
func (r *Repository) UseRemote(ctx context.Context, name string) error {
	if err := core.ValidateTasksRemoteName(name); err != nil {
		return core.Wrap(core.CategoryInvocation, "--remote must name a Git remote", err)
	}
	if !r.hasGitRemote(ctx, name) {
		return core.Errorf(core.CategoryNotFound,
			"this checkout has no Git remote named %q; add it with git remote add %s <address>", name, name)
	}
	r.metadataMu.Lock()
	r.remoteOverride = name
	r.metadataMu.Unlock()
	return nil
}

// HasTasksRemote reports whether the remote this handle synchronizes with is
// configured in Git. For a project that never chose a remote it asks Git
// exactly what it always asked: whether origin exists.
//
// Any failure reads as "not configured" because that is the only actionable
// distinction: a caller consults this to decide whether attempting the network
// is worth a timeout. A project that chose a remote this checkout lacks never
// reaches such a caller, because opening the project refuses first (see
// CheckTasksRemote).
func (r *Repository) HasTasksRemote(ctx context.Context) bool {
	return r.hasGitRemote(ctx, r.remote())
}

func (r *Repository) hasGitRemote(ctx context.Context, name string) bool {
	_, err := r.Git(ctx, nil, "remote", "get-url", name)
	return err == nil
}

// GitRemoteURL is the address this checkout's Git configuration gives the
// remote name, or "" when it has none.
func (r *Repository) GitRemoteURL(ctx context.Context, name string) string {
	return r.gitRemoteURL(ctx, name)
}

// gitRemoteURL is the address this checkout's Git configuration gives name,
// or "" when it has none.
func (r *Repository) gitRemoteURL(ctx context.Context, name string) string {
	output, err := r.Git(ctx, nil, "remote", "get-url", name)
	if err != nil {
		return ""
	}
	address, err := gitSingleLine(output)
	if err != nil {
		return ""
	}
	return address
}

// TasksRemoteInfo describes the remote a checkout shares its tasks through.
type TasksRemoteInfo struct {
	// Name is the Git remote.
	Name string `json:"name"`
	// URL is the address this checkout's own Git configuration gives it, with
	// any credential removed, or empty when the checkout does not have it.
	URL string `json:"url,omitempty"`
	// SettingsURL is the address .workbook/config.json records, which setup
	// adds the remote from.
	SettingsURL string `json:"settingsUrl,omitempty"`
	// Source says where Name came from: "settings" for the project's choice,
	// "flag" for --remote, and "default" for origin.
	Source string `json:"source"`
}

// TasksRemote reports which remote this handle shares tasks through.
func (r *Repository) TasksRemote(ctx context.Context) TasksRemoteInfo {
	info := TasksRemoteInfo{Name: r.remote(), Source: "default"}
	r.metadataMu.RLock()
	override := r.remoteOverride
	r.metadataMu.RUnlock()
	if tracked, exists, err := r.readConfig(); err == nil && exists && tracked.TasksRemote.Chosen() {
		if override == "" || override == tracked.TasksRemote.Name {
			info.Source = "settings"
			info.SettingsURL = tracked.TasksRemote.URL
		}
	}
	if override != "" && info.Source != "settings" {
		info.Source = "flag"
	}
	info.URL = redactURLCredentials(r.gitRemoteURL(ctx, info.Name))
	return info
}

// CheckTasksRemote refuses a checkout that must not use its tasks until
// `workbook setup` has run again, so no task command reads or writes against
// the wrong place. Every command that opens a project calls it, except setup
// and the settings commands, which are how a checkout gets out of these
// states.
//
// It refuses three states:
//
//   - the project's settings name a remote this checkout does not have, which
//     is what an ordinary pull of the settings change leaves;
//   - the settings name a remote this checkout has never read the tasks from,
//     which is what a setup without access to it leaves, so the board shows no
//     empty list as if the project had no tasks;
//   - this checkout's last look at its tasks remote found it marked as moved,
//     which is a checkout that has not pulled the settings change yet.
//
// A project that never chose a remote pays nothing for the first two, and the
// third reads what resolving the identity already listed.
func (r *Repository) CheckTasksRemote(ctx context.Context) error {
	tracked, exists, err := r.readConfig()
	if err != nil {
		return err
	}
	name := r.configuredRemote()
	if exists && tracked.TasksRemote.Chosen() {
		setting := tracked.TasksRemote
		if !r.hasGitRemote(ctx, name) {
			return tasksRemoteMissing(setting)
		}
	}
	head, found, err := r.trackingIdentityHead(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		if exists && tracked.TasksRemote.Chosen() {
			return tasksRemoteNotRead(name, redactURLCredentials(r.gitRemoteURL(ctx, name)))
		}
		return nil
	}
	moved, found, err := r.trackedMoveMarker(ctx, name, head)
	if err != nil || !found {
		return err
	}
	moved, found = r.stillMoved(ctx, name, moved)
	if !found {
		return nil
	}
	return r.tasksMovedRefusalHere(ctx, moved)
}

// stillMoved asks remote, whose copy here carries the marker stale, whether
// it is marked still: the tasks may have been moved back to it since this
// checkout last read it, and then it is live again and this checkout's copy
// is brought up to date. It returns the marker the remote carries now, or
// stale when the remote cannot be asked, and false when it is live.
//
// It runs only on the way to a refusal, so a checkout that has not been
// refused asks nothing more than before.
func (r *Repository) stillMoved(ctx context.Context, remote string, stale TasksMoved) (TasksMoved, bool) {
	record, found, err := r.remoteIdentityRecord(ctx, remote)
	if err != nil || !found {
		return stale, true
	}
	if record.Moved != nil {
		return *record.Moved, true
	}
	r.metadataMu.Lock()
	if r.trackedIdentity.known && r.trackedIdentity.remote == remote {
		r.trackedIdentity.head, r.trackedIdentity.found = record.Head, true
	}
	r.originMoved = nil
	r.metadataMu.Unlock()
	return TasksMoved{}, false
}

// trackedIdentityHead is one observation of a remote's tracking identity ref.
type trackedIdentityHead struct {
	known  bool
	remote string
	head   string
	found  bool
}

// recordPublishedIdentity points the tracking identity ref of a chosen tasks
// remote at the identity commit this checkout just published there, so a
// checkout that gave an empty tasks remote its identity counts as having read
// it. A project on origin keeps the tracking refs only a fetch writes, exactly
// as before a remote could be chosen.
func (r *Repository) recordPublishedIdentity(ctx context.Context, head string) error {
	if r.remote() == core.DefaultTasksRemote {
		return nil
	}
	if _, err := r.Git(ctx, nil, "update-ref", "--no-deref", r.remoteIdentityRef(), head); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot record the published project identity", err)
	}
	return nil
}

// observedMove returns the tasks-moved marker this handle found on the remote
// it synchronizes with during this command, if it found one.
func (r *Repository) observedMove() *TasksMoved {
	r.metadataMu.RLock()
	defer r.metadataMu.RUnlock()
	if r.originMoved == nil {
		return nil
	}
	moved := *r.originMoved
	return &moved
}

// remoteMarkedMoved decides what finding a tasks-moved marker on this handle's
// remote means. On the project's own tasks remote it is the refusal every
// command gives a checkout that has not picked up the new setting. On a remote
// named with --remote it is a place that may still be read and is never
// published to, so it refuses only when publishing.
func (r *Repository) remoteMarkedMoved(ctx context.Context, moved TasksMoved, publishing bool) error {
	r.metadataMu.Lock()
	r.originMoved = &moved
	r.metadataMu.Unlock()
	if r.remote() == r.configuredRemote() {
		return r.tasksMovedRefusalHere(ctx, moved)
	}
	if publishing {
		return movedRemotePublication(r.remote(), moved)
	}
	return nil
}

// trackingIdentityHead reads where this checkout last saw remote's identity
// ref.
func (r *Repository) trackingIdentityHead(ctx context.Context, remote string) (string, bool, error) {
	r.metadataMu.RLock()
	memo := r.trackedIdentity
	r.metadataMu.RUnlock()
	if memo.known && memo.remote == remote {
		return memo.head, memo.found, nil
	}
	tracking := remoteIdentityRefFor(remote)
	listing, err := r.listIdentityRefs(ctx, tracking)
	if err != nil {
		return "", false, err
	}
	head, found := listing.Heads[tracking]
	return head, found, nil
}

// tasksRemoteMissing is the refusal for a checkout whose settings name a
// remote it has not added.
func tasksRemoteMissing(setting core.TasksRemoteSetting) error {
	refusal := core.TasksRemoteRefusal{
		Reason:  core.TasksRemoteMissing,
		Remote:  setting.Name,
		URL:     setting.URL,
		Command: "workbook setup",
	}
	if setting.URL == "" {
		refusal.Command = ""
		return core.TasksRemoteError(core.CategoryNotInitialized, refusal,
			"this project shares its tasks through the Git remote %q, which this checkout does not have, and %s gives no address for it; "+
				"add it with git remote add %s <address>, then run workbook setup",
			setting.Name, configPath, setting.Name)
	}
	return core.TasksRemoteError(core.CategoryNotInitialized, refusal,
		"this project shares its tasks through the Git remote %q (%s), which this checkout does not have yet; "+
			"run workbook setup to add it and read the tasks from there",
		setting.Name, setting.URL)
}

// tasksRemoteNotRead is the refusal for a checkout that has the tasks remote
// and has never read the tasks from it, which is what a contributor who can
// reach the code and not the tasks repository is left with. Setup reports the
// same sentence with Git's reason added.
func tasksRemoteNotRead(name, address string) error {
	where := fmt.Sprintf("the Git remote %q", name)
	if address != "" {
		where = fmt.Sprintf("%s (remote %q)", address, name)
	}
	return core.TasksRemoteError(core.CategoryNotInitialized,
		core.TasksRemoteRefusal{Reason: core.TasksRemoteNotRead, Remote: name, URL: address, Command: "workbook setup"},
		"%s", tasksRemoteAccessSentence(where, false))
}

// tasksRemoteAccessSentence says where the tasks are and what access they
// need. unreachable is for a listing that just failed, which says nothing
// about whether this checkout read the tasks before; otherwise the checkout
// has never read them.
func tasksRemoteAccessSentence(where string, unreachable bool) string {
	state := ", and this checkout has not read them yet: "
	if unreachable {
		state = ", which could not be reached just now: "
	}
	return "this project's tasks are in " + where + state +
		"seeing them needs read access to that repository, and changing them needs push access to it; " +
		"once this checkout has that access, run workbook setup"
}

// TasksRemoteSetup reports what setup did about a project's chosen remote.
type TasksRemoteSetup struct {
	Name string `json:"name"`
	// URL is the address the checkout uses, with any credential removed.
	URL string `json:"url"`
	// SettingsURL is the address the project's settings record.
	SettingsURL string `json:"settingsUrl,omitempty"`
	// Status is "added" when this run added the remote from the settings, and
	// "kept" when the checkout already had a remote of that name, whose
	// address this run left alone.
	Status string `json:"status"`
}

// EnsureTasksRemote adds the project's chosen tasks remote to this checkout
// when it does not have it, and leaves an existing one exactly as it is: a
// contributor overrides the address the settings give by setting their own,
// for SSH where the file says HTTPS. It reports nothing for a project that
// never chose a remote.
//
// A remote it adds fetches nothing on a plain `git fetch`, the way a
// tasks-only folder's origin does: Workbook's own fetches name their refspecs
// on the command line, and a tasks repository has no code to download.
func (r *Repository) EnsureTasksRemote(ctx context.Context) (*TasksRemoteSetup, error) {
	tracked, exists, err := r.readConfig()
	if err != nil || !exists || !tracked.TasksRemote.Chosen() {
		return nil, err
	}
	setting := tracked.TasksRemote
	if current := r.gitRemoteURL(ctx, setting.Name); current != "" {
		return &TasksRemoteSetup{
			Name:        setting.Name,
			URL:         redactURLCredentials(current),
			SettingsURL: setting.URL,
			Status:      "kept",
		}, nil
	}
	if setting.URL == "" {
		return nil, tasksRemoteMissing(setting)
	}
	if _, err := r.Git(ctx, nil, "remote", "add", "--no-tags", setting.Name, setting.URL); err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot add the tasks remote "+setting.Name, err)
	}
	if _, err := r.Git(ctx, nil, "config", "--local", "--replace-all",
		"remote."+setting.Name+".fetch", tasksOnlyFetchRefspec); err != nil {
		return nil, core.Wrap(core.CategoryOperational, "cannot configure the tasks remote "+setting.Name, err)
	}
	r.forgetTasksRemote()
	return &TasksRemoteSetup{Name: setting.Name, URL: redactURLCredentials(setting.URL), SettingsURL: setting.URL, Status: "added"}, nil
}

// TasksRemoteProbe is what one listing of a tasks remote found.
type TasksRemoteProbe struct {
	Identity bool
	Tasks    bool
}

// ProbeTasksRemote lists the tasks remote's Workbook refs, and turns a
// failure into the plain sentence a contributor without access needs: which
// address, and what access.
func (r *Repository) ProbeTasksRemote(ctx context.Context) (TasksRemoteProbe, error) {
	name := r.remote()
	listing, err := r.Git(ctx, nil, "ls-remote", "--refs", name, identityRef, taskRefPrefix+"*")
	if err != nil {
		address := redactURLCredentials(r.gitRemoteURL(ctx, name))
		where := fmt.Sprintf("the Git remote %q", name)
		if address != "" {
			where = fmt.Sprintf("%s (remote %q)", address, name)
		}
		return TasksRemoteProbe{}, &core.Error{
			Category:    core.CategoryOperational,
			Message:     tasksRemoteAccessSentence(where, true),
			Cause:       err,
			TasksRemote: &core.TasksRemoteRefusal{Reason: core.TasksRemoteUnreachable, Remote: name, URL: address, Command: "workbook setup"},
		}
	}
	probe := TasksRemoteProbe{}
	for _, line := range strings.Split(string(listing), "\n") {
		_, refname, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if refname == identityRef {
			probe.Identity = true
		}
		if strings.HasPrefix(refname, taskRefPrefix) {
			probe.Tasks = true
		}
	}
	return probe, nil
}

// TasksRemoteName is the Git remote this handle shares tasks through, for a
// message that has to name it.
func (r *Repository) TasksRemoteName() string { return r.remote() }
