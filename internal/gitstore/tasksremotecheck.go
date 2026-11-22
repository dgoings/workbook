package gitstore

import (
	"bytes"
	"context"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// CheckDistinctTasksRemote refuses a tasks remote that is the same repository
// as origin, however its address is spelled, for `workbook setup`.
//
// Setup cannot write to tell the two apart, as `workbook move-tasks` does
// (refuseUnfitDestination), because a teammate may set up with read access
// alone. It compares what the two hold instead: the same repository lists the
// same refs at the same commits. A repository for tasks holds no branches, so a
// listing with branches that matches origin's ref for ref is the code
// repository, and a legitimate tasks repository, fresh or filled beforehand,
// never matches: it has no branches. The cost is one listing of each remote,
// and setup is run once per checkout.
func (r *Repository) CheckDistinctTasksRemote(ctx context.Context) error {
	tasksRemote := r.configuredRemote()
	if tasksRemote == core.DefaultTasksRemote || !r.hasGitRemote(ctx, core.DefaultTasksRemote) {
		return nil
	}
	theirs, err := r.Git(ctx, nil, "ls-remote", "--refs", tasksRemote)
	if err != nil {
		// ProbeTasksRemote says what an unreachable remote needs.
		return nil
	}
	if !strings.Contains(string(theirs), "\trefs/heads/") {
		return nil
	}
	ours, err := r.Git(ctx, nil, "ls-remote", "--refs", core.DefaultTasksRemote)
	if err != nil || !bytes.Equal(bytes.TrimSpace(theirs), bytes.TrimSpace(ours)) {
		return nil
	}
	return sameRepositoryRefusal(tasksRemote, redactURLCredentials(r.gitRemoteURL(ctx, tasksRemote)),
		core.DefaultTasksRemote, redactURLCredentials(r.gitRemoteURL(ctx, core.DefaultTasksRemote)))
}

func sameRepositoryRefusal(remote, address, other, otherAddress string) error {
	return core.TasksRemoteError(core.CategoryValidation,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteSameRepository,
			Remote:  remote,
			URL:     address,
			Command: "workbook config set tasks-remote " + remote + " --url <the tasks repository's address>",
		},
		"%s (%s) is the same repository as %s (%s), whatever its address says, so it cannot hold the tasks apart from it; "+
			"give tasks-remote the address of a separate repository that holds no branches",
		remote, address, other, otherAddress)
}

// OriginMarkedMoved reports the marker on origin's identity ref, asking origin
// when it can and falling back on what this checkout last read from it. It
// is for refusing to share tasks through origin again after they moved away
// from it, which this version cannot undo.
func (r *Repository) OriginMarkedMoved(ctx context.Context) (*TasksMoved, error) {
	const origin = core.DefaultTasksRemote
	if !r.hasGitRemote(ctx, origin) {
		return nil, nil
	}
	tracking := remoteIdentityRefFor(origin)
	if listing, err := r.Git(ctx, nil, "ls-remote", "--refs", origin, identityRef); err == nil {
		head, err := parseRemoteIdentityHead(listing)
		if err == nil && head != "" {
			current, _ := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", tracking)
			if strings.TrimSpace(string(current)) != head {
				if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", origin,
					identityFetchRefspecFor(origin)); err != nil {
					return nil, core.Wrap(core.CategoryOperational, "cannot read origin's identity ref", err)
				}
			}
		}
	}
	record, found, err := r.readIdentityRef(ctx, tracking)
	if err != nil || !found {
		return nil, err
	}
	return record.Moved, nil
}

// MovedForGood is the refusal for sharing tasks through origin again once
// they have moved away from it.
func MovedForGood(moved TasksMoved) error {
	return core.TasksRemoteError(core.CategoryValidation,
		core.TasksRemoteRefusal{
			Reason:  core.TasksRemoteMovedForGood,
			Remote:  core.DefaultTasksRemote,
			MovedTo: &core.TasksRemoteTarget{Remote: moved.Remote, URL: moved.URL},
		},
		"this project's tasks have moved from origin to %s (remote %q), and this version of Workbook cannot move them back: "+
			"origin is marked as the place they left, and every checkout still reading it is refused. "+
			"The move is one-way; docs/reference.md says what moving them back by hand involves",
		moved.URL, moved.Remote)
}
