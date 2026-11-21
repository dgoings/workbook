package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/presentation"
)

// compactNow is the clock a compact run measures its cutoff from and records
// itself at. It is a seam for tests that need a task to have finished long ago.
var compactNow = time.Now

// commandNow is the clock a task session's mutations record their wall time
// from. It is a seam for tests that need changes made on chosen days; the
// wall time is display metadata only, so nothing but what is shown reads it.
var commandNow = time.Now

// compactExecute runs the compaction. It is a seam for tests that need a run
// to fail after its refs moved.
var compactExecute = compaction.Execute

// compactSummary is the data member of compact's JSON result, and what its
// text output is rendered from. Tasks lists what was compacted, or on a dry
// run what would be.
type compactSummary struct {
	DryRun  bool              `json:"dryRun"`
	RunID   string            `json:"runId"`
	Tasks   []compactTask     `json:"tasks"`
	Skipped []compaction.Skip `json:"skipped"`
	Totals  compactTotals     `json:"totals"`
	Bundle  string            `json:"bundle,omitempty"`
	// Notice is the upgrade requirement, carried on every run so a caller
	// reading JSON hears what the text output says.
	Notice string `json:"notice"`
}

// compactUpgradeNotice is what every run, dry or real, says about the
// upgrade. It holds before a run and after one alike: a clone still on the
// last release without compaction cannot sync a compacted task, whether or
// not this run has published one yet.
const compactUpgradeNotice = "Every clone must run Workbook 0.7.0 or later; clones on v0.6.1 cannot sync compacted tasks."

type compactTask struct {
	ID                  string             `json:"id"`
	Title               string             `json:"title"`
	Kind                compaction.Kind    `json:"kind"`
	Commits             int                `json:"commits"`
	Attachments         compactAttachments `json:"attachments"`
	EstimatedSavedBytes int64              `json:"estimatedSavedBytes"`
}

type compactAttachments struct {
	Dropped      int   `json:"dropped"`
	Kept         int   `json:"kept"`
	DroppedBytes int64 `json:"droppedBytes"`
	KeptBytes    int64 `json:"keptBytes"`
}

type compactTotals struct {
	Done                int   `json:"done"`
	Deleted             int   `json:"deleted"`
	Commits             int   `json:"commits"`
	AttachmentsDropped  int   `json:"attachmentsDropped"`
	AttachmentsKept     int   `json:"attachmentsKept"`
	DroppedBytes        int64 `json:"droppedBytes"`
	KeptBytes           int64 `json:"keptBytes"`
	EstimatedSavedBytes int64 `json:"estimatedSavedBytes"`
}

func runCompact(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	given := append([]string{}, args...)
	// A value that starts with a digit is a cutoff even when it is not a
	// valid one, so `--done-only 30` says what is wrong with 30 rather than
	// calling it a stray argument. Anything else has to parse.
	isCutoff := func(value string) bool {
		if value[0] >= '0' && value[0] <= '9' {
			return true
		}
		_, err := parseDuration(value)
		return err == nil
	}
	doneOnly, doneCutoff, args, err := takeOptionalValueOption("compact", "done-only", args, isCutoff)
	if err != nil {
		return err
	}
	deletedOnly, deletedCutoff, args, err := takeOptionalValueOption("compact", "deleted-only", args, isCutoff)
	if err != nil {
		return err
	}
	flags := newFlagSet("compact")
	since := flags.String("since", "", "compact only tasks that became Done or Deleted longer ago than this")
	keepAttachments := flags.Bool("keep-attachments", false, "keep attachment contents in the checkpoints")
	noBackup := flags.Bool("no-backup", false, "write no backup bundle")
	backup := flags.String("backup", "", "write the backup bundle here, or into this directory")
	dryRun := flags.Bool("dry-run", false, "report what would be compacted and change nothing")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	adopt := flags.String("adopt", "", "settle one task a compaction left stuck, retrying the replay")
	discardLocal := flags.Bool("discard-local", false, "with --adopt, take origin's version after saving the local history to a bundle")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	typed := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) { typed[visited.Name] = true })

	// --adopt is a different command sharing the name: it settles one task
	// rather than selecting many, so none of the selection flags mean
	// anything beside it, and it has to fetch to know what origin holds.
	if *discardLocal && !typed["adopt"] {
		return core.Errorf(core.CategoryInvocation, "--discard-local requires --adopt")
	}
	if typed["adopt"] {
		if doneOnly || deletedOnly || typed["since"] || typed["keep-attachments"] || typed["dry-run"] {
			return core.Errorf(core.CategoryInvocation,
				"--adopt cannot be combined with --done-only, --deleted-only, --since, --keep-attachments or --dry-run")
		}
		if typed["no-sync"] {
			return core.Errorf(core.CategoryInvocation, "--adopt cannot be combined with --no-sync")
		}
		// Only a discard writes a bundle; a retry keeps the local history.
		if !*discardLocal && (typed["no-backup"] || typed["backup"]) {
			return core.Errorf(core.CategoryInvocation,
				"--no-backup and --backup require --discard-local when used with --adopt")
		}
	}

	if doneOnly && deletedOnly {
		return core.Errorf(core.CategoryInvocation, "--done-only and --deleted-only cannot be combined")
	}
	if typed["since"] && (doneCutoff != "" || deletedCutoff != "") {
		return core.Errorf(core.CategoryInvocation,
			"give the cutoff once: --since cannot be combined with a --done-only or --deleted-only duration")
	}
	if *noBackup && typed["backup"] {
		return core.Errorf(core.CategoryInvocation, "--no-backup and --backup cannot be combined")
	}
	if typed["backup"] && *backup == "" {
		return core.Errorf(core.CategoryInvocation, "compact --backup requires a path")
	}
	if typed["adopt"] {
		if strings.TrimSpace(*adopt) == "" {
			return core.Errorf(core.CategoryInvocation, "compact --adopt requires a task")
		}
		return runCompactAdopt(ctx, compactAdoptRequest{
			target:       *adopt,
			discardLocal: *discardLocal,
			noBackup:     *noBackup,
			backup:       *backup,
			flags:        given,
			jsonMode:     *jsonMode,
		}, cwd, stdout, stderr)
	}

	criteria := compaction.Criteria{Done: !deletedOnly, Deleted: !doneOnly}
	cutoff, cutoffFlag := *since, "since"
	switch {
	case doneCutoff != "":
		cutoff, cutoffFlag = doneCutoff, "done-only"
	case deletedCutoff != "":
		cutoff, cutoffFlag = deletedCutoff, "deleted-only"
	}
	if typed["since"] || cutoff != "" {
		if criteria.Cutoff, err = parseCompactCutoff(cutoffFlag, cutoff); err != nil {
			return err
		}
	}

	options := compaction.Options{
		Criteria:        criteria,
		KeepAttachments: *keepAttachments,
		Backup:          *backup,
		Flags:           given,
		Now:             compactionNow(),
	}
	switch {
	case *noBackup:
		options.Backup = "-"
	case *backup == "-":
		// The engine reads "-" as no bundle. Typed as a path, it is a file
		// named "-" in the working directory, never a silent skip.
		options.Backup = "./-"
	}

	session, err := openTaskSession(ctx, cwd, *noSync, !*dryRun, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	// Compaction starts from a tip origin already holds, so with sync on the
	// tracking refs are brought up to date first. A real run publishes the
	// checkpoints at the end, as every mutation publishes what it changed.
	session.fetchBefore(ctx)
	if err := session.refreshConfiguration(ctx); err != nil {
		return err
	}
	options.RequirePushed = session.report.Enabled && session.repository.HasOrigin(ctx)

	service := session.service
	service.History = session.store
	deps := compaction.Deps{
		Repository: session.repository,
		Config:     session.config,
		Service:    service,
		Projection: session.store,
	}
	plan, err := compaction.Prepare(ctx, deps, options)
	if err != nil {
		return err
	}

	var warnings []core.Warning
	summary := compactSummary{DryRun: *dryRun, RunID: plan.RunID, Skipped: plan.Skipped, Notice: compactUpgradeNotice}
	compacted := plan.Candidates
	if !*dryRun {
		result, err := compactExecute(ctx, deps, plan, options)
		var projectionErr *compaction.ProjectionError
		switch {
		case errors.As(err, &projectionErr):
			// Every ref moved and the bundle is complete, so the run is done.
			// Only the cache lags, and it can be rebuilt from the refs.
			warnings = append(warnings, core.Warning{
				Code: core.WarningProjectionUpdate,
				Message: "the tasks were compacted, but the SQLite cache could not be updated; run " +
					"`workbook rebuild` before `git gc`: " + projectionErr.Err.Error(),
			})
		case err != nil:
			return err
		}
		compacted, summary.Skipped, summary.Bundle = result.Compacted, result.Skipped, result.BundlePath
		// Each checkpoint goes out as a leased non-fast-forward, which origin
		// accepts only while it still holds the tip that was compacted. A
		// refusal leaves the task compacted here and is reported below.
		taskIDs := make([]string, 0, len(compacted))
		for _, candidate := range compacted {
			taskIDs = append(taskIDs, candidate.TaskID)
		}
		session.publishTasks(ctx, taskIDs)
	}
	summary.Tasks, summary.Totals = compactTasks(compacted)
	if summary.Skipped == nil {
		summary.Skipped = []compaction.Skip{}
	}
	published := 0
	if !*dryRun {
		published = len(compacted)
	}
	warnings = append(warnings, compactSyncWarnings(session, published)...)

	if *jsonMode {
		writeSyncedResult(stdout, "compact", summary, &session.report, session.conflicts, warnings)
		return nil
	}
	writeCompactSummary(stdout, summary, compactShortIDs(ctx, session.service))
	writeSyncReport(stdout, &session.report)
	writeConflicts(stdout, session.conflicts)
	writeConfigConflicts(stdout, session.report.configConflicts)
	writeCompactHints(stdout, summary)
	writeWarnings(stderr, warnings)
	writeIdentityWarning(stderr, session.report.Identity)
	writeConfigWarning(stderr, session.report.Config)
	return nil
}

// parseCompactCutoff reads a cutoff given through flagName.
func parseCompactCutoff(flagName, value string) (time.Duration, error) {
	parsed, err := parseDuration(value)
	if errors.Is(err, errNonPositiveDuration) {
		return 0, core.Errorf(core.CategoryInvocation, "compact --%s must be positive", flagName)
	}
	if err != nil {
		return 0, core.Wrap(core.CategoryInvocation, "compact --"+flagName+" must be a duration such as 30d", err)
	}
	return parsed, nil
}

// compactSyncWarnings says what the fetch before the run, or the publication
// after it, could not do, as a mutation's warnings do. A refused checkpoint is
// named with origin's reason, because that reason is what the person has to act
// on: a stale lease clears with the next sync, a host policy does not.
//
// compacted counts the checkpoints a real run wrote. When the fetch failed,
// publishing them was skipped, and the warning says so.
func compactSyncWarnings(session *taskSession, compacted int) []core.Warning {
	warnings := fetchSyncWarnings(session, compacted)
	if warning, found := session.compactedOnOriginWarning(""); found {
		warnings = append(warnings, warning)
	}
	return append(warnings, session.unsettledWarnings()...)
}

// fetchSyncWarnings is the one warning, if any, about what synchronization
// itself could not do.
func fetchSyncWarnings(session *taskSession, compacted int) []core.Warning {
	switch {
	case session.report.Status == syncStatusFailed && session.report.Pushed != nil:
		message := "the tasks were compacted locally, but " + session.report.Detail
		for _, item := range session.report.Pushed.Tasks {
			if item.Status == gitstore.SyncRejected || item.Status == gitstore.SyncInvalid {
				message += fmt.Sprintf("; %s: %s", item.TaskID, item.Detail)
			}
		}
		return []core.Warning{{Code: core.WarningAutoSync, Message: message}}
	case session.report.Status == syncStatusFailed:
		message := "the run used this clone's last view of origin, because " + session.report.Detail
		if compacted > 0 {
			message += "; the compacted tasks stay local (unpublished) until the next successful sync"
		}
		return []core.Warning{{Code: core.WarningAutoSync, Message: message}}
	case session.defects != "":
		return []core.Warning{{
			Code:    core.WarningAutoSync,
			Message: "origin holds task refs this fetch could not validate: " + session.defects,
		}}
	}
	return nil
}

func compactTasks(candidates []compaction.Candidate) ([]compactTask, compactTotals) {
	tasks := make([]compactTask, 0, len(candidates))
	var totals compactTotals
	for _, candidate := range candidates {
		task := compactTask{
			ID:      candidate.TaskID,
			Title:   candidate.Title,
			Kind:    candidate.Kind,
			Commits: candidate.Commits,
			Attachments: compactAttachments{
				Dropped:      candidate.DroppedAttachments,
				Kept:         candidate.KeptAttachments,
				DroppedBytes: candidate.DroppedBytes,
				KeptBytes:    candidate.KeptBytes,
			},
			EstimatedSavedBytes: candidate.EstimatedSavedBytes,
		}
		tasks = append(tasks, task)
		switch task.Kind {
		case compaction.KindDone:
			totals.Done++
		case compaction.KindDeleted:
			totals.Deleted++
		}
		totals.Commits += task.Commits
		totals.AttachmentsDropped += task.Attachments.Dropped
		totals.AttachmentsKept += task.Attachments.Kept
		totals.DroppedBytes += task.Attachments.DroppedBytes
		totals.KeptBytes += task.Attachments.KeptBytes
		totals.EstimatedSavedBytes += task.EstimatedSavedBytes
	}
	return tasks, totals
}

// writeCompactSummary prints one line per task, the totals, and the skipped
// tasks with their reasons. A task an earlier run already compacted is
// counted rather than listed: every later run finds all of them again, and a
// list of them would bury the skips that need a decision. JSON lists each.
//
// shortID names each task; see compactShortIDs.
func writeCompactSummary(output io.Writer, summary compactSummary, shortID func(string) string) {
	for _, task := range summary.Tasks {
		fmt.Fprintf(output, "%s  %s  %s  %s  %s\n",
			shortID(task.ID), singleLine(task.Title), task.Kind,
			plural(task.Commits, "commit"), compactAttachmentText(task.Attachments))
	}
	totals := summary.Totals
	verb := "Compacted"
	if summary.DryRun {
		verb = "Would compact"
	}
	fmt.Fprintf(output, "%s %s (%d done, %d deleted), replacing %s.\n",
		verb, plural(totals.Done+totals.Deleted, "task"), totals.Done, totals.Deleted, plural(totals.Commits, "commit"))
	fmt.Fprintf(output, "Attachments: %d dropped (%s), %d kept (%s).\n",
		totals.AttachmentsDropped, humanBytes(totals.DroppedBytes), totals.AttachmentsKept, humanBytes(totals.KeptBytes))
	already := 0
	var listed []compaction.Skip
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
			fmt.Fprintf(output, "%s  %s  %s\n", shortID(skipped.TaskID), singleLine(skipped.Title), skipped.Reason)
		}
	}
	if already > 0 {
		fmt.Fprintf(output, "Skipped: %d %s\n", already, compaction.ReasonAlreadyCompacted)
	}
}

// writeCompactHints ends the text output: the estimate, the bundle, the
// upgrade requirement, and how to reclaim the space.
func writeCompactHints(output io.Writer, summary compactSummary) {
	fmt.Fprintf(output, "Estimated space saved: %s (applies once Git cleans up unreachable objects)\n",
		humanBytes(summary.Totals.EstimatedSavedBytes))
	if summary.Bundle != "" {
		fmt.Fprintf(output, "Backup: %s\n", summary.Bundle)
	}
	fmt.Fprintln(output, summary.Notice)
	if !summary.DryRun {
		fmt.Fprintln(output, `Run "git gc" to reclaim the space locally; origin cleans up on its own schedule.`)
	}
}

func compactAttachmentText(attachments compactAttachments) string {
	switch {
	case attachments.Dropped > 0 && attachments.Kept > 0:
		return fmt.Sprintf("%s dropped (%s), %d kept (%s)",
			plural(attachments.Dropped, "attachment"), humanBytes(attachments.DroppedBytes),
			attachments.Kept, humanBytes(attachments.KeptBytes))
	case attachments.Dropped > 0:
		return fmt.Sprintf("%s dropped (%s)", plural(attachments.Dropped, "attachment"), humanBytes(attachments.DroppedBytes))
	case attachments.Kept > 0:
		return fmt.Sprintf("%s kept (%s)", plural(attachments.Kept, "attachment"), humanBytes(attachments.KeptBytes))
	default:
		return "no attachments"
	}
}

// compactShortIDs abbreviates each task ID the way the board does: its key
// and the shortest prefix of its ULID, eight characters at least, that no
// other task in the project shares, so `show` accepts it. Tasks created in the
// same instant share ten characters, which is why eight alone is not enough.
//
// Uniqueness is among every task the projection holds, deleted ones included,
// since a prefix resolves against all of them. When they cannot be listed the
// IDs are printed whole, which is always unambiguous.
func compactShortIDs(ctx context.Context, service core.Service) func(string) string {
	tasks, err := service.List(ctx, core.ListFilter{All: true})
	if err != nil {
		return func(taskID string) string { return taskID }
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return func(taskID string) string { return presentation.ShortestUniquePrefix(taskID, ids) }
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// humanBytes renders a byte count in the largest binary unit that keeps it at
// or above one, with one decimal place above bytes.
func humanBytes(count int64) string {
	if count < 1024 {
		return plural(int(count), "byte")
	}
	value := float64(count)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= 1024
		if value < 1024 || unit == "TiB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return ""
}
