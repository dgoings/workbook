package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// resetPushBatch bounds how many refs one deleting push names, so a remote
// holding thousands of tasks is not limited by the length of a command line.
const resetPushBatch = 500

// RemoteResetPlan is what a reset of one remote would remove there.
type RemoteResetPlan struct {
	Remote string
	URL    string
	// MovedTo is set when the remote is marked as a place the tasks moved
	// away from: its identity ref, which carries the marker, is kept.
	MovedTo *core.TasksRemoteTarget
	// Refs is every ref the remote holds under refs/workbook/ that the reset
	// removes, by the name the remote holds it under; Kept names the ones it
	// keeps.
	Refs []ResetRef
	Kept []string
	// Tasks counts the task refs among Refs.
	Tasks int
	// Unreadable is why the remote's identity ref could not be read, when it
	// could not; such a remote is treated as holding the tasks.
	Unreadable string
	// left names the refs PlanMoveCleanUp leaves at the remote because they
	// are not this project's: neither deleted nor reported as remaining.
	left []string
}

// Live reports a remote that is not marked as a place the tasks left: wiping
// it removes the tasks themselves.
func (plan RemoteResetPlan) Live() bool { return plan.MovedTo == nil }

// tracking is plan's refs under this checkout's tracking names for them.
func (plan RemoteResetPlan) tracking() []ResetRef {
	refs := make([]ResetRef, 0, len(plan.Refs))
	for _, ref := range plan.Refs {
		ref.Ref = remoteTrackingPrefix(plan.Remote) + strings.TrimPrefix(ref.Ref, workbookRefRoot)
		refs = append(refs, ref)
	}
	return refs
}

// PlanRemoteReset reads what remote holds under refs/workbook/. It fetches
// all of it into this checkout's tracking refs for remote, pruning what the
// remote no longer holds, which is what the bundle is written from, and
// changes nothing at the remote.
func (r *Repository) PlanRemoteReset(ctx context.Context, remote string) (RemoteResetPlan, error) {
	if err := core.ValidateTasksRemoteName(remote); err != nil {
		return RemoteResetPlan{}, err
	}
	if !r.hasGitRemote(ctx, remote) {
		return RemoteResetPlan{}, core.Errorf(core.CategoryValidation,
			"this checkout has no Git remote named %s; add it with git remote add %s <address> and run this again", remote, remote)
	}
	plan := RemoteResetPlan{Remote: remote, URL: r.gitRemoteURL(ctx, remote)}
	if err := r.fetchForReset(ctx, remote); err != nil {
		return RemoteResetPlan{}, err
	}
	local, err := r.listLocalWorkbookRefs(ctx)
	if err != nil {
		return RemoteResetPlan{}, err
	}
	tracking := remoteTrackingPrefix(remote)
	for _, ref := range local {
		if !strings.HasPrefix(ref.Ref, tracking) {
			continue
		}
		ref.Ref = workbookRefRoot + strings.TrimPrefix(ref.Ref, tracking)
		plan.Refs = append(plan.Refs, ref)
	}
	for index, ref := range plan.Refs {
		if ref.Ref != identityRef {
			continue
		}
		record, err := r.identityRecordAt(ctx, remoteIdentityRefFor(remote), ref.ObjectID)
		switch {
		case err != nil:
			plan.Unreadable = err.Error()
		case record.Moved != nil:
			plan.MovedTo = &core.TasksRemoteTarget{Remote: record.Moved.Remote, URL: record.Moved.URL}
			plan.Kept = []string{identityRef}
			plan.Refs = append(plan.Refs[:index:index], plan.Refs[index+1:]...)
		}
		break
	}
	for _, ref := range plan.Refs {
		if strings.HasPrefix(ref.Ref, taskRefPrefix) {
			plan.Tasks++
		}
	}
	return plan, nil
}

// fetchForReset mirrors everything remote holds under refs/workbook/ into
// this checkout's tracking namespace for it, and prunes what it no longer
// holds there.
func (r *Repository) fetchForReset(ctx context.Context, remote string) error {
	refspec := "+" + workbookRefRoot + "*:" + remoteTrackingPrefix(remote) + "*"
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--prune", remote, refspec); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot read "+remote+"'s Workbook refs", err)
	}
	return nil
}

// RemoteResetResult is what a reset of one remote did.
type RemoteResetResult struct {
	Removed []string
	Bundle  string
}

// ResetRemote deletes every ref plan names at its remote, writing the bundle
// of them first unless options.NoBackup. Each deletion is leased on the
// object plan read, so a ref that changed since is refused rather than lost.
// A host that refuses some deletions gets one *ResetRefusalError naming its
// reason and every ref it still holds; running the reset again once the host
// allows it finishes the job. Afterwards this checkout's tracking refs for
// the remote are pruned to what it still holds.
func (r *Repository) ResetRemote(ctx context.Context, projectID string, plan RemoteResetPlan, options ResetOptions) (RemoteResetResult, error) {
	options, err := options.withDefaults()
	if err != nil {
		return RemoteResetResult{}, err
	}
	var result RemoteResetResult
	if !options.NoBackup {
		if err := r.CheckResetBackup(options.Backup); err != nil {
			return result, err
		}
	}
	if len(plan.Refs) > 0 && !options.NoBackup {
		path, err := r.writeResetBundle(ctx, projectID, plan.tracking(), plan.Remote, options)
		if err != nil {
			return result, err
		}
		result.Bundle = path
	}
	if err := r.resetStep("bundle"); err != nil {
		return result, err
	}
	var refused []string
	var reasons, messages []string
	for start := 0; start < len(plan.Refs); start += resetPushBatch {
		end := min(start+resetPushBatch, len(plan.Refs))
		deleted, rejected, message, err := r.pushDeletions(ctx, plan.Remote, plan.Refs[start:end])
		result.Removed = append(result.Removed, deleted...)
		if err != nil {
			return result, r.withBundle(err, result.Bundle)
		}
		for ref, reason := range rejected {
			refused = append(refused, ref)
			reasons = appendOnce(reasons, reason)
		}
		if message != "" {
			messages = appendOnce(messages, message)
		}
		if err := r.resetStep("push"); err != nil {
			return result, err
		}
	}
	if err := r.fetchForReset(ctx, plan.Remote); err != nil {
		return result, r.withBundle(err, result.Bundle)
	}
	if err := requireBundle(result.Bundle); err != nil {
		return result, err
	}
	if len(refused) == 0 {
		return result, nil
	}
	remaining, err := r.remainingAfterReset(ctx, plan)
	if err != nil {
		return result, r.withBundle(err, result.Bundle)
	}
	sort.Strings(refused)
	rule := strings.Join(reasons, "; ")
	if len(messages) > 0 {
		rule += " (" + strings.Join(messages, " ") + ")"
	}
	command := "workbook reset --remote " + plan.Remote
	if plan.Live() {
		command += " --confirm " + plan.Remote
	}
	return result, NewResetRefusal(core.CategoryOperational, ResetRefusal{
		Reason:    ResetDeletionRefused,
		Remote:    plan.Remote,
		Command:   command,
		Remaining: remaining,
		Bundle:    result.Bundle,
	}, "%s refused to delete %d of its Workbook refs: %s. It still holds %s. Once the host allows deleting refs under %s, run %s again to finish%s",
		plan.Remote, len(refused), rule, strings.Join(remaining, ", "), workbookRefRoot, command, keepBundle(result.Bundle))
}

// keepBundle tells a person whose remote reset stopped partway to keep its
// bundle: the run that finishes the job writes one of only what is left.
func keepBundle(bundle string) string {
	if bundle == "" {
		return ""
	}
	return ". Keep the backup bundle at " + bundle + ": it holds every ref this run was to delete, " +
		"and the run that finishes writes a bundle of only the ones still there"
}

// withBundle names the bundle written before err, which nothing else would.
func (r *Repository) withBundle(err error, bundle string) error {
	if bundle == "" {
		return err
	}
	return wrapKeepingCategory("the backup bundle is at "+bundle, err)
}

func appendOnce(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// remainingAfterReset is every ref the remote still holds under
// refs/workbook/, other than the ones plan keeps or leaves, as its tracking
// refs show it after the reset's last fetch.
func (r *Repository) remainingAfterReset(ctx context.Context, plan RemoteResetPlan) ([]string, error) {
	output, err := r.Git(ctx, nil, "for-each-ref", "--format=%(refname)", remoteTrackingPrefix(plan.Remote))
	if err != nil {
		return nil, err
	}
	var remaining []string
	for _, name := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if name == "" {
			continue
		}
		name = workbookRefRoot + strings.TrimPrefix(name, remoteTrackingPrefix(plan.Remote))
		if containsName(plan.Kept, name) || containsName(plan.left, name) {
			continue
		}
		remaining = append(remaining, name)
	}
	return remaining, nil
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// pushDeletions asks remote to delete refs, each leased on the object listed.
// It returns the refs deleted, the ones refused with Git's reason for each,
// and what the remote itself said, its "remote:" lines, which is where a
// host's rule is named.
func (r *Repository) pushDeletions(ctx context.Context, remote string, refs []ResetRef) ([]string, map[string]string, string, error) {
	args := []string{"push", "--porcelain", "--no-verify"}
	expected := map[string]bool{}
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, workbookRefRoot) {
			return nil, nil, "", core.Errorf(core.CategoryOperational, "refusing to delete %s, which is outside %s", ref.Ref, workbookRefRoot)
		}
		args = append(args, "--force-with-lease="+ref.Ref+":"+ref.ObjectID)
		expected[ref.Ref] = true
	}
	args = append(args, remote)
	for _, ref := range refs {
		args = append(args, ":"+ref.Ref)
	}
	push := r.gitWithEnvResult(ctx, []string{"WORKBOOK_PRE_PUSH_ACTIVE=1"}, nil, args...)
	var deleted []string
	rejected := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(push.stdout), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "To ") || line == "Done" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || len(fields[0]) != 1 {
			return deleted, nil, "", core.Errorf(core.CategoryOperational, "Git returned malformed push porcelain record %q", line)
		}
		_, destination, found := strings.Cut(fields[1], ":")
		if !found || !expected[destination] {
			return deleted, nil, "", core.Errorf(core.CategoryOperational, "Git returned unexpected push destination %q", fields[1])
		}
		switch fields[0] {
		case "-":
			deleted = append(deleted, destination)
		case "!":
			rejected[destination] = fields[2]
		default:
			return deleted, nil, "", core.Errorf(core.CategoryOperational, "Git returned push porcelain flag %q for a deletion", fields[0])
		}
	}
	var said []string
	for _, line := range strings.Split(string(bytes.ReplaceAll(push.stderr, []byte("\r"), nil)), "\n") {
		if text, found := strings.CutPrefix(line, "remote: "); found {
			if text = strings.TrimSpace(text); text != "" {
				said = append(said, text)
			}
		}
	}
	if push.err != nil && len(rejected) == 0 {
		return deleted, nil, "", core.Wrap(core.CategoryOperational,
			fmt.Sprintf("cannot delete %s's Workbook refs", remote), gitCommandResultError(push))
	}
	if len(deleted)+len(rejected) != len(refs) {
		return deleted, nil, "", core.Errorf(core.CategoryOperational, "Git returned %d push outcomes, want %d", len(deleted)+len(rejected), len(refs))
	}
	return deleted, rejected, strings.Join(said, " "), nil
}

// PlanMoveCleanUp is the plan of the clean-up after a move: PlanRemoteReset's,
// less every ref under the task and relic namespaces whose name is not one of
// this project's task or relic IDs (ownsWorkbookRef). Those are another
// project's refs, or names that fit no project, which a move concerns itself
// with no more than it carries them: the clean-up does not delete, bundle,
// count or name them, and does not report them as remaining when the host
// refuses a deletion. Plain reset --remote plans with PlanRemoteReset and
// still deletes them. The key set is the one the move just verified with
// (moveKeySet): this checkout's, joined with the old place's configuration's
// when the two have diverged, so a run that has them in memory starts no Git
// process for them.
func (r *Repository) PlanMoveCleanUp(ctx context.Context, config core.ProjectConfig, remote string) (RemoteResetPlan, error) {
	plan, err := r.PlanRemoteReset(ctx, remote)
	if err != nil {
		return RemoteResetPlan{}, err
	}
	keys, err := r.moveKeySet(ctx, config)
	if err != nil {
		return RemoteResetPlan{}, err
	}
	refs := plan.Refs[:0:0]
	plan.Tasks = 0
	for _, ref := range plan.Refs {
		if !keys.owns(ref.Ref) {
			plan.left = append(plan.left, ref.Ref)
			continue
		}
		refs = append(refs, ref)
		if strings.HasPrefix(ref.Ref, taskRefPrefix) {
			plan.Tasks++
		}
	}
	plan.Refs = refs
	return plan, nil
}
