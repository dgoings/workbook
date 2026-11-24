package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// The relic commands that change a relic open the ordinary task session, so
// --no-sync, the auto-sync setting, a running watcher and the refusals about
// a moved tasks remote apply to them exactly as to a task command. Each one
// fetches first, stops on its own relic's conflict, records its change and
// publishes the one relic ref it moved.

// newRelicService is the service a relic command reads and writes through:
// the repository, under the project's keys, recording changes under actor.
func newRelicService(repository *gitstore.Repository, config core.ProjectConfig, keys core.KeySet, actor string) core.RelicService {
	return core.RelicService{
		Config: config,
		Keys:   keys,
		Reader: repository,
		Writer: repository,
		Tasks:  repository,
		IDs:    core.CryptoULIDSource{Now: recordedNow},
		// Relic documents record their times in UTC, as the stored format
		// shows them.
		Now:   func() time.Time { return recordedNow().UTC() },
		Actor: actor,
	}
}

// mutateRelic is mutate for a relic: the fetch, the change and the push of the
// relic's ref, in that order. The service is built after the fetch, from the
// keys it settled on, so a relic is created under a key a teammate just added.
//
// target is the relic or prefix the command was given, or empty when it
// creates one. A conflict the fetch met on that relic stops the command before
// anything is written, so the same command run again is the retry.
func (session *taskSession) mutateRelic(
	ctx context.Context,
	target string,
	apply func(context.Context, core.RelicService) (core.RelicMutationResult, error),
) (core.RelicMutationResult, error) {
	if err := session.fetchBefore(ctx); err != nil {
		return core.RelicMutationResult{}, err
	}
	if err := session.refreshConfiguration(ctx); err != nil {
		return core.RelicMutationResult{}, err
	}
	service := newRelicService(session.repository, session.config, session.service.Keys, session.service.Actor)
	if conflict := session.relicConflictFor(ctx, service, target); conflict != nil {
		session.acknowledgeRelic(*conflict)
		return core.RelicMutationResult{}, core.RelicConflictError([]core.RelicConflict{*conflict})
	}
	result, err := apply(ctx, service)
	if err != nil {
		return core.RelicMutationResult{}, err
	}
	if result.Changed {
		session.publishRelic(ctx, result.Relic.ID)
	}
	if err := session.publicationRefusal(); err != nil {
		return core.RelicMutationResult{}, err
	}
	result.Warnings = append(result.Warnings, session.mutationWarnings()...)
	return result, nil
}

// relicConflictFor reports whether the relic a command names is one the fetch
// could not finish replaying. A target that does not resolve is left for the
// command itself to report.
func (session *taskSession) relicConflictFor(ctx context.Context, service core.RelicService, target string) *core.RelicConflict {
	if len(session.relicConflicts) == 0 || target == "" {
		return nil
	}
	heads, err := service.Reader.ListRelicHeads(ctx, service.Config)
	if err != nil {
		return nil
	}
	named, err := core.ResolveRelicPrefix(heads, target)
	if err != nil {
		return nil
	}
	for index := range session.relicConflicts {
		if session.relicConflicts[index].RelicID == named.RelicID {
			return &session.relicConflicts[index]
		}
	}
	return nil
}

// publishRelic sends exactly the relic ref the command changed, as publish
// sends a task's: a live watcher is nudged, and otherwise the push is inline
// and skipped when the fetch did not complete. A rejection is reported in the
// sync report and as a warning; the change is recorded here either way.
//
// The nudge carries no task ID, as publishConfig's does not: it only wakes
// the watcher's loop, which synchronizes every ref this clone holds.
func (session *taskSession) publishRelic(ctx context.Context, relicID string) {
	if !session.report.Enabled {
		return
	}
	if session.deferred {
		if err := session.nudge(""); err == nil {
			return
		}
		// The watcher answered the probe and was gone by the time the write
		// landed; publishing here keeps "deferred" from being an empty promise.
		session.deferred = false
		session.report.Status = syncStatusCompleted
		session.report.Detail = ""
		session.pushRelicInline(ctx, relicID)
		return
	}
	if session.fetched == nil {
		return
	}
	session.pushRelicInline(ctx, relicID)
}

func (session *taskSession) pushRelicInline(ctx context.Context, relicID string) {
	push := session.repository.PushRelic
	if session.fetched != nil {
		push = session.repository.PushRelicAfterFetch
	}
	pushed, err := push(ctx, session.config, relicID)
	session.report.PushRelic = &pushed
	session.report.Identity, _ = session.repository.IdentityReport()
	if published, found := session.repository.ConfigReport(); found {
		session.report.Config = published
	}
	if err != nil {
		session.report.Status = syncStatusFailed
		session.report.Detail = "push failed: " + err.Error()
		session.noteRefusal(err)
		return
	}
	session.report.Status = syncStatusCompleted
}

// noteRelicReport keeps what a completed fetch reported about relics, with
// every body a body conflict names read in, and hands both lists to the sync
// report so every command's envelope carries them.
func (session *taskSession) noteRelicReport(ctx context.Context, fetched gitstore.SyncResult) {
	session.relicConflicts, session.relicWarnings = relicReport(ctx, session.repository, fetched)
	session.report.relicConflicts = session.relicConflicts
	session.report.relicWarnings = session.relicWarnings
}

// relicReport is a fetch's relic conflicts, with the content of each body
// version filled in, and its relic warnings. A body that cannot be read is
// left without content and said in one more warning: the conflict is still
// reported, and the report names the blob that holds each body.
func relicReport(ctx context.Context, repository *gitstore.Repository, fetched gitstore.SyncResult) ([]core.RelicConflict, []core.Warning) {
	warnings := append([]core.Warning(nil), fetched.RelicWarnings...)
	if len(fetched.RelicConflicts) == 0 {
		return nil, warnings
	}
	conflicts, err := repository.FillRelicConflictBodies(ctx, fetched.RelicConflicts)
	if err != nil {
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the relic conflict report leaves out a body it could not read: " + err.Error(),
		})
	}
	return conflicts, warnings
}

// onlyRelicDefects reports that a completed fetch's defects were all a
// relic's: no task failed its check or waits on an upgrade, and a relic did.
func onlyRelicDefects(fetched gitstore.SyncResult) bool {
	for _, task := range fetched.Tasks {
		if task.Status == gitstore.SyncInvalid || task.Status == gitstore.SyncNeedsUpgrade {
			return false
		}
	}
	for _, relic := range fetched.Relics {
		if relic.Status == gitstore.SyncInvalid || relic.Status == gitstore.SyncNeedsUpgrade {
			return true
		}
	}
	return false
}

// writeRelicMutationOutcome reports what a relic command did, including when a
// conflict stopped it before it did anything: the envelope then carries the
// conflict lists, which hold what the caller needs to resolve it.
func writeRelicMutationOutcome(
	stdout, stderr io.Writer,
	command string,
	session *taskSession,
	result core.RelicMutationResult,
	err error,
	jsonMode bool,
) error {
	if err != nil {
		if core.CategoryOf(err) != core.CategoryConflict {
			return err
		}
		writeSyncPhaseResultWithConfig(stdout, command, nil, session.conflicts, session.report.configConflicts,
			session.relicConflicts, session.relicWarnings, jsonMode, func(io.Writer) {})
		if !jsonMode {
			writeRelicWarnings(stderr, session.relicWarnings)
		}
		return err
	}
	if jsonMode {
		_ = json.NewEncoder(stdout).Encode(ResultEnvelope{
			Format:         "workbook.result",
			Version:        1,
			Command:        command,
			Data:           result,
			Conflict:       session.conflicts,
			ConfigConflict: session.report.configConflicts,
			RelicConflict:  session.relicConflicts,
			Warnings:       append(append([]core.Warning(nil), result.Warnings...), session.relicWarnings...),
			Sync:           &session.report,
		})
		return nil
	}
	writeRelicMutation(stdout, command, result, false)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeRelicConflicts(stdout, session.relicConflicts)
	writeRelicWarnings(stderr, result.Warnings)
	writeRelicWarnings(stderr, session.relicWarnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
	return nil
}

// writeRelicConflicts renders the relic conflict list the JSON envelope
// carries, one line each. It never prints body text: the detail names the
// versions and the commands that write each text into a file. Every column
// passes through the control-byte filter, since a relic's stored strings are
// written by anyone who can push.
func writeRelicConflicts(output io.Writer, conflicts []core.RelicConflict) {
	for _, conflict := range conflicts {
		fmt.Fprintf(output, "Relic conflict:\t%s\t%s\t%s\n",
			singleLine(conflict.RelicID), singleLine(string(conflict.Type)), singleLine(core.RelicConflictDetail(conflict)))
	}
}

// writeRelicWarnings is writeWarnings for warnings that may quote what a
// relic holds, each passed through the control-byte filter.
func writeRelicWarnings(stderr io.Writer, warnings []core.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "workbook: warning: %s\n", singleLine(warning.Message))
	}
}
