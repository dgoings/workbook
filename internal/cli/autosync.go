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
// to origin's current tip. A teammate's advance is therefore absorbed as a
// fast-forward instead of becoming a divergence to reconcile later.
//
// An unreachable origin is a warning rather than a failure: the local write is
// the durable result, and refusing to record work because the network is down
// would defeat the local-first design.
func (session *taskSession) fetchBefore(ctx context.Context) {
	if !session.report.Enabled {
		return
	}
	if _, err := session.repository.Git(ctx, nil, "remote", "get-url", "origin"); err != nil {
		session.report.Status = syncStatusSkipped
		session.report.Detail = "no origin remote configured"
		return
	}
	result, err := session.repository.Fetch(ctx, session.config)
	session.report.Fetch = &result
	if err != nil {
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
// an unreachable origin only buys a second timeout, and when the fetch reported
// this task as divergent, because origin would reject the push anyway.
func (session *taskSession) publish(ctx context.Context, taskID string) error {
	if !session.report.Enabled || session.fetched == nil {
		return nil
	}
	if session.divergedDuringFetch(taskID) {
		session.report.Status = syncStatusFailed
		session.report.Detail = "task " + taskID + " diverged from origin"
		return core.Errorf(core.CategoryStaleWrite,
			"change recorded locally, but task %s diverged from origin and was not published; reconcile with `workbook sync`",
			taskID)
	}

	pushed, err := session.repository.PushTask(ctx, session.config, taskID)
	session.report.Push = &pushed
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		if core.CategoryOf(err) == core.CategoryStaleWrite {
			return core.Errorf(core.CategoryStaleWrite,
				"change recorded locally, but origin rejected task %s; reconcile with `workbook sync`", taskID)
		}
		return nil
	}
	session.report.Status = syncStatusCompleted
	return nil
}

func (session *taskSession) divergedDuringFetch(taskID string) bool {
	if session.fetched == nil {
		return false
	}
	for _, task := range session.fetched.Tasks {
		if task.TaskID == taskID && task.Status == gitstore.SyncDiverged {
			return true
		}
	}
	return false
}

// mutate runs the fetch, the mutation, and the targeted push in order. The
// mutation's result names the canonical task ID, which is what makes one
// sequence work for create as well as for the commands that take an ID.
func (session *taskSession) mutate(
	ctx context.Context,
	apply func(context.Context) (core.MutationResult, error),
) (core.MutationResult, error) {
	session.fetchBefore(ctx)
	result, err := apply(ctx)
	if err != nil {
		return core.MutationResult{}, err
	}
	if err := session.publish(ctx, result.Task.ID); err != nil {
		return core.MutationResult{}, err
	}
	if session.report.Status == syncStatusFailed {
		result.Warnings = append(result.Warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the change was recorded locally, but " + session.report.Detail,
		})
	}
	return result, nil
}
