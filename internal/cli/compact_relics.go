package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// compactExecuteRelics runs a relic compaction. It is a seam for tests that
// need a run to fail after its refs moved, or origin to change before the
// checkpoints are published.
var compactExecuteRelics = compaction.ExecuteRelics

// compactRelicsSummary is the data member of compact --relics' JSON result,
// and what its text output is rendered from. Relics lists what was compacted,
// or on a dry run what would be.
type compactRelicsSummary struct {
	DryRun  bool                   `json:"dryRun"`
	RunID   string                 `json:"runId"`
	Relics  []compactedRelic       `json:"relics"`
	Skipped []compaction.RelicSkip `json:"skipped"`
	Totals  compactRelicTotals     `json:"totals"`
	Bundle  string                 `json:"bundle,omitempty"`
	// Notice is what an older Workbook does with compacted relics, carried on
	// every run so a caller reading JSON hears what the text output says.
	Notice string `json:"notice"`
}

// compactRelicNotice is what every relic run, dry or real, says about older
// builds.
const compactRelicNotice = "Clones on Workbook 0.7.0 or earlier never fetch relics; " +
	"clones built before relic compaction read compacted relics but must upgrade to change them."

type compactedRelic struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Kind is the boundary the relic is compacted at: retired or deleted.
	Kind                string `json:"kind"`
	Commits             int    `json:"commits"`
	DroppedParts        int    `json:"droppedParts"`
	DroppedBytes        int64  `json:"droppedBytes"`
	EstimatedSavedBytes int64  `json:"estimatedSavedBytes"`
}

type compactRelicTotals struct {
	Retired             int   `json:"retired"`
	Deleted             int   `json:"deleted"`
	Commits             int   `json:"commits"`
	DroppedParts        int   `json:"droppedParts"`
	DroppedBytes        int64 `json:"droppedBytes"`
	EstimatedSavedBytes int64 `json:"estimatedSavedBytes"`
}

// compactRelicsRequest is what compact --relics was asked to do, after its
// flags have been checked.
type compactRelicsRequest struct {
	criteria compaction.RelicCriteria
	backup   string
	noBackup bool
	dryRun   bool
	noSync   bool
	flags    []string
	jsonMode bool
}

// runCompactRelics is compact --relics. It runs as a relic command does: it
// fetches the relics, compacts the retired and deleted ones, publishes each
// checkpoint, and packs the relic refs it moved. It compacts no task.
func runCompactRelics(ctx context.Context, request compactRelicsRequest, cwd string, stdout, stderr io.Writer) error {
	session, err := openTaskSession(ctx, cwd, request.noSync, !request.dryRun, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	session.relics = true
	defer session.repository.PackMovedRelicRefs(ctx)
	if err := session.fetchBefore(ctx); err != nil {
		return err
	}
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}

	options := compaction.RelicOptions{
		Criteria: request.criteria,
		Backup:   request.backup,
		Flags:    request.flags,
		// A checkpoint replaces a tip origin holds, as far as this clone last
		// fetched, whether or not this run synchronizes.
		RequirePushed: session.repository.HasTasksRemote(ctx),
		Now:           compactionNow(),
	}
	switch {
	case request.noBackup:
		options.Backup = "-"
	case request.backup == "-":
		// As for tasks: typed as a path, "-" is a file of that name.
		options.Backup = "./-"
	}
	deps := compaction.RelicDeps{
		Repository: session.repository,
		Config:     session.config,
		Relics: newRelicService(session.repository, session.config, session.service.Keys, session.schemas,
			session.service.Actor),
		Index: session.store,
	}
	plan, err := compaction.PrepareRelics(ctx, deps, options)
	if err != nil {
		return err
	}

	var warnings []core.Warning
	summary := compactRelicsSummary{DryRun: request.dryRun, RunID: plan.RunID, Skipped: plan.Skipped, Notice: compactRelicNotice}
	compacted := plan.Candidates
	published := 0
	if !request.dryRun {
		result, err := compactExecuteRelics(ctx, deps, plan, options)
		var projectionErr *compaction.ProjectionError
		switch {
		case errors.As(err, &projectionErr):
			// Every ref moved and the bundle is complete, so the run is done.
			// Only the cache lags, and a relic read answers from Git until it
			// can follow.
			warnings = append(warnings, core.Warning{
				Code: core.WarningProjectionUpdate,
				Message: "the relics were compacted, but the SQLite cache could not be updated; run " +
					"`workbook rebuild`: " + projectionErr.Err.Error(),
			})
		case err != nil:
			return err
		}
		compacted, summary.Skipped, summary.Bundle = result.Compacted, result.Skipped, result.BundlePath
		published = len(compacted)
		// Each checkpoint goes out as a leased non-fast-forward, which origin
		// accepts only while it still holds the tip that was compacted. A
		// refusal leaves the relic compacted here and is reported below.
		relicIDs := make([]string, 0, len(compacted))
		for _, candidate := range compacted {
			relicIDs = append(relicIDs, candidate.RelicID)
		}
		session.publishCompactedRelics(ctx, relicIDs)
		if err := session.publicationRefusal(); err != nil {
			return err
		}
	}
	summary.Relics, summary.Totals = compactedRelics(compacted)
	if summary.Skipped == nil {
		summary.Skipped = []compaction.RelicSkip{}
	}
	warnings = append(warnings, compactRelicSyncWarnings(ctx, session, published)...)

	if request.jsonMode {
		writeSyncedResult(stdout, "compact", summary, &session.report, session.conflicts, warnings)
		return nil
	}
	writeCompactRelicsSummary(stdout, summary)
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeRelicConflicts(stdout, session.relicConflicts)
	writeCompactRelicHints(stdout, summary, session.repository.TasksRemoteName())
	writeWarnings(stderr, warnings)
	writeRelicWarnings(stderr, session.relicWarnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
	return nil
}

// publishCompactedRelics is publishRelic for the relics a run compacted. A
// live watcher is nudged once, and its own synchronization publishes them,
// leasing each checkpoint as an inline push does; otherwise each is pushed
// inline, which is skipped when the fetch did not complete. The report lists
// every push's outcome in Pushed, so one accepted push cannot hide another's
// refusal.
func (session *taskSession) publishCompactedRelics(ctx context.Context, relicIDs []string) {
	if !session.report.Enabled || len(relicIDs) == 0 {
		return
	}
	switch {
	case session.deferred:
		// The nudge carries no task ID: it only wakes the watcher's loop.
		if err := session.nudge(""); err == nil {
			return
		}
		// The watcher answered the probe and was gone by the time the refs
		// moved; publishing here keeps "deferred" from being an empty promise.
		session.deferred = false
		session.report.Detail = ""
	case session.fetched == nil:
		return
	}
	pushed := gitstore.SyncResult{
		Remote: session.repository.TasksRemoteName(),
		Status: gitstore.SyncPhaseCompleted,
		Tasks:  []gitstore.SyncTaskResult{},
	}
	var failures []string
	for _, relicID := range relicIDs {
		session.report.PushRelic = nil
		session.pushRelicInline(ctx, relicID)
		if result := session.report.PushRelic; result != nil {
			pushed.Relics = append(pushed.Relics, *result)
		}
		if session.report.Status == syncStatusFailed {
			failures = append(failures, session.report.Detail)
		}
		session.report.Status, session.report.Detail = syncStatusCompleted, ""
	}
	session.report.PushRelic, session.report.Pushed = nil, &pushed
	if len(failures) > 0 {
		session.report.Status = syncStatusFailed
		session.report.Detail = strings.Join(failures, "; ")
	}
}

// compactRelicSyncWarnings says what the fetch before a relic run, or the
// publication after it, could not do. compacted counts the checkpoints a real
// run wrote: with synchronization off, or after a failed fetch, they stay
// unpublished and the warning says so.
func compactRelicSyncWarnings(ctx context.Context, session *taskSession, compacted int) []core.Warning {
	var warnings []core.Warning
	remote := session.repository.TasksRemoteName()
	switch {
	case !session.report.Enabled:
		if compacted > 0 && session.repository.HasTasksRemote(ctx) {
			warnings = append(warnings, core.Warning{
				Code:    core.WarningAutoSync,
				Message: "the compacted relics are not published; run `workbook sync` to publish them to " + remote,
			})
		}
	case session.report.Status == syncStatusFailed && session.report.Pushed != nil:
		warnings = append(warnings, core.Warning{
			Code:    core.WarningAutoSync,
			Message: "the relics were compacted locally, but " + session.report.Detail,
		})
	case session.report.Status == syncStatusFailed:
		message := "the run used this clone's last view of " + remote + ", because " + session.report.Detail
		if compacted > 0 {
			message += "; the compacted relics stay local (unpublished) until the next successful sync"
		}
		warnings = append(warnings, core.Warning{Code: core.WarningAutoSync, Message: message})
	case session.defects != "":
		message := remote + " holds task refs this fetch could not validate: " + session.defects
		if session.relicDefectsOnly {
			message = remote + " holds relics this fetch could not check: " + session.defects
		}
		warnings = append(warnings, core.Warning{Code: core.WarningAutoSync, Message: message})
	}
	if warning, found := session.compactedOnOriginWarning(""); found {
		warnings = append(warnings, warning)
	}
	return append(warnings, session.unsettledWarnings()...)
}

func compactedRelics(candidates []compaction.RelicCandidate) ([]compactedRelic, compactRelicTotals) {
	relics := make([]compactedRelic, 0, len(candidates))
	var totals compactRelicTotals
	for _, candidate := range candidates {
		relic := compactedRelic{
			ID:                  candidate.RelicID,
			Title:               candidate.Title,
			Kind:                candidate.Boundary,
			Commits:             candidate.Commits,
			DroppedParts:        candidate.DroppedParts,
			DroppedBytes:        candidate.DroppedBytes,
			EstimatedSavedBytes: candidate.EstimatedSavedBytes,
		}
		relics = append(relics, relic)
		switch relic.Kind {
		case core.RelicBoundaryRetired:
			totals.Retired++
		case core.RelicBoundaryDeleted:
			totals.Deleted++
		}
		totals.Commits += relic.Commits
		totals.DroppedParts += relic.DroppedParts
		totals.DroppedBytes += relic.DroppedBytes
		totals.EstimatedSavedBytes += relic.EstimatedSavedBytes
	}
	return relics, totals
}

// writeCompactRelicsSummary prints one line per relic, the totals, and the
// skipped relics with their reasons, counting the already compacted ones as
// writeCompactSummary counts tasks. Relics are named by their whole ID, as
// every relic command prints them.
func writeCompactRelicsSummary(output io.Writer, summary compactRelicsSummary) {
	for _, relic := range summary.Relics {
		dropped := "no parts dropped"
		if relic.DroppedParts > 0 {
			dropped = fmt.Sprintf("%s dropped (%s)", plural(relic.DroppedParts, "part"), humanBytes(relic.DroppedBytes))
		}
		fmt.Fprintf(output, "%s  %s  %s  %s  %s\n",
			relic.ID, singleLine(relic.Title), singleLine(relic.Kind), plural(relic.Commits, "commit"), dropped)
	}
	totals := summary.Totals
	verb := "Compacted"
	if summary.DryRun {
		verb = "Would compact"
	}
	fmt.Fprintf(output, "%s %s (%d retired, %d deleted), replacing %s.\n",
		verb, plural(totals.Retired+totals.Deleted, "relic"), totals.Retired, totals.Deleted,
		plural(totals.Commits, "commit"))
	fmt.Fprintf(output, "Dropped parts: %d (%s).\n", totals.DroppedParts, humanBytes(totals.DroppedBytes))
	already := 0
	var listed []compaction.RelicSkip
	for _, skipped := range summary.Skipped {
		if skipped.Reason == compaction.ReasonAlreadyCompacted {
			already++
			continue
		}
		listed = append(listed, skipped)
	}
	if len(listed) > 0 {
		fmt.Fprintln(output, "Skipped:")
		for _, skipped := range listed {
			fmt.Fprintf(output, "%s  %s  %s\n", skipped.RelicID, singleLine(skipped.Title), skipped.Reason)
		}
	}
	if already > 0 {
		fmt.Fprintf(output, "Skipped: %d %s\n", already, compaction.ReasonAlreadyCompacted)
	}
}

// writeCompactRelicHints ends the text output as writeCompactHints does.
func writeCompactRelicHints(output io.Writer, summary compactRelicsSummary, remote string) {
	writeCompactHints(output, compactSummary{
		DryRun: summary.DryRun,
		Totals: compactTotals{EstimatedSavedBytes: summary.Totals.EstimatedSavedBytes},
		Bundle: summary.Bundle,
		Notice: summary.Notice,
	}, remote)
}
