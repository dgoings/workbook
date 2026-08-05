package cli

import (
	"context"
	"time"

	"github.com/dgoings/workbook/internal/autosync"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/projection"
	"github.com/dgoings/workbook/internal/userconfig"
)

const (
	syncStatusCompleted = "completed"
	syncStatusSkipped   = "skipped"
	syncStatusFailed    = "failed"
)

// syncReport is the machine-readable account of what a command did about
// synchronization. It is reported even when nothing was attempted so a caller
// can tell a deliberate skip from a silent one.
type syncReport struct {
	Enabled bool                     `json:"enabled"`
	Source  autosync.Source          `json:"source"`
	Status  string                   `json:"status"`
	Detail  string                   `json:"detail,omitempty"`
	Fetch   *gitstore.SyncResult     `json:"fetch,omitempty"`
	Push    *gitstore.SyncTaskResult `json:"push,omitempty"`
}

// taskSession carries the repository, service, and resolved synchronization
// policy through one command so the fetch, the mutation, and the targeted push
// share a single opened repository.
type taskSession struct {
	repository *gitstore.Repository
	config     core.ProjectConfig
	service    core.Service
	report     syncReport
	fetched    *gitstore.SyncResult
	conflicts  []core.Conflict
}

func openTaskSession(ctx context.Context, cwd string, noSync, withWriter bool) (*taskSession, error) {
	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return nil, err
	}
	user, err := userconfig.Load()
	if err != nil {
		return nil, err
	}
	policy, err := autosync.Resolve(noSync, config, user)
	if err != nil {
		return nil, err
	}

	store, err := projection.Open(ctx, repository, config)
	if err != nil {
		return nil, err
	}
	service := core.Service{
		Config: config,
		Reader: store,
		IDs:    core.CryptoULIDSource{},
		Now:    time.Now,
	}
	if withWriter {
		actor, err := repository.Actor(ctx)
		if err != nil {
			return nil, err
		}
		service.Writer = repository
		service.Projection = store
		service.Actor = actor
	}

	session := &taskSession{
		repository: repository,
		config:     config,
		service:    service,
		report:     syncReport{Enabled: policy.Enabled, Source: policy.Source, Status: syncStatusSkipped},
	}
	if !policy.Enabled {
		session.report.Detail = "automatic synchronization is disabled"
	}
	return session, nil
}

// fetchBefore refreshes shared task refs so the mutation that follows applies
// to origin's current tip. A teammate's advance is absorbed as a fast-forward,
// and local work origin does not have is replayed onto the fetched tip.
//
// An unreachable origin is a warning rather than a failure: the local write is
// the durable result, and refusing to record work because the network is down
// would defeat the local-first design. A conflict is different — the fetch
// itself completed and advanced refs, so it is recorded and left for the caller
// to act on.
func (session *taskSession) fetchBefore(ctx context.Context) {
	if !session.report.Enabled {
		return
	}
	if !session.repository.HasOrigin(ctx) {
		session.report.Status = syncStatusSkipped
		session.report.Detail = "no origin remote configured"
		return
	}
	result, err := session.repository.Fetch(ctx, session.config)
	session.report.Fetch = &result
	session.conflicts = result.Conflicts
	if err != nil && core.CategoryOf(err) != core.CategoryConflict {
		session.report.Status = syncStatusFailed
		session.report.Detail = "fetch failed: " + err.Error()
		return
	}
	session.fetched = &result
	session.report.Status = syncStatusCompleted
}

// publish sends exactly the ref the mutation changed.
//
// It is skipped when the fetch did not complete, because a second connection to
// an unreachable origin only buys a second timeout. A rejection is reported as
// a warning: the change is durable locally, and the next fetch replays it onto
// whatever origin holds by then.
func (session *taskSession) publish(ctx context.Context, taskID string) {
	if !session.report.Enabled || session.fetched == nil {
		return
	}
	pushed, err := session.repository.PushTask(ctx, session.config, taskID)
	session.report.Push = &pushed
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		return
	}
	session.report.Status = syncStatusCompleted
}

// conflictFor reports whether this command's own task is one the fetch could
// not finish replaying. An unrelated task's conflict never blocks a mutation;
// this task's does, because the mutation would otherwise build on a history
// that silently dropped the caller's earlier local operations.
func (session *taskSession) conflictFor(ctx context.Context, target string) *core.Conflict {
	if len(session.conflicts) == 0 || target == "" {
		return nil
	}
	taskID := target
	if core.ValidateTaskID(session.config.Key, taskID) != nil {
		resolved, err := session.service.Reader.Resolve(ctx, session.config, target)
		if err != nil {
			return nil
		}
		taskID = resolved
	}
	for index := range session.conflicts {
		if session.conflicts[index].TaskID == taskID {
			return &session.conflicts[index]
		}
	}
	return nil
}

// mutate runs the fetch, the mutation, and the targeted push in order. The
// mutation's result names the canonical task ID, which is what makes one
// sequence work for create as well as for the commands that take an ID.
//
// target is the id-or-prefix the command was given, or empty when it creates a
// task. It is checked before the mutation runs so a conflicted task is left
// exactly where the fetch put it and the caller can retry this same command.
func (session *taskSession) mutate(
	ctx context.Context,
	target string,
	apply func(context.Context) (core.MutationResult, error),
) (core.MutationResult, error) {
	session.fetchBefore(ctx)
	if conflict := session.conflictFor(ctx, target); conflict != nil {
		return core.MutationResult{}, core.ConflictError([]core.Conflict{*conflict})
	}
	result, err := apply(ctx)
	if err != nil {
		return core.MutationResult{}, err
	}
	session.publish(ctx, result.Task.ID)
	if session.report.Status == syncStatusFailed {
		result.Warnings = append(result.Warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change was recorded locally, but " + session.report.Detail,
		})
	}
	return result, nil
}
