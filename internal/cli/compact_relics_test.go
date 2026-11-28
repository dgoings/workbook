package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/compaction"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testrepo"
)

const wantRelicUpgradeNotice = "Clones on Workbook 0.7.0 or earlier never fetch relics; clones built before relic compaction read compacted relics but must upgrade to change them."

// relicCompactDocument decodes compact --relics' JSON result.
type relicCompactDocument struct {
	Data          compactRelicsSummary `json:"data"`
	Sync          *syncReport          `json:"sync"`
	Warnings      []core.Warning       `json:"warnings"`
	RelicConflict []core.RelicConflict `json:"relicConflict"`
}

func compactRelicsJSON(t *testing.T, repository string, args ...string) relicCompactDocument {
	t.Helper()
	full := append(append([]string{"compact", "--relics"}, args...), "--json")
	stdout := compactRun(t, repository, full...)
	assertJSONResult(t, stdout, "compact")
	var document relicCompactDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode compact --relics: %v; output = %q", err, stdout)
	}
	return document
}

func compactedRelicIDs(summary compactRelicsSummary) []string {
	ids := make([]string, 0, len(summary.Relics))
	for _, relic := range summary.Relics {
		ids = append(ids, relic.ID)
	}
	return ids
}

func localRelicRefs(t *testing.T, repository string) string {
	t.Helper()
	return refsUnder(t, repository, "refs/workbook/relics/")
}

func originRelicRefs(t *testing.T, clone string) string {
	t.Helper()
	return cliGitOutput(t, clone, "ls-remote", "origin", "refs/workbook/relics/*")
}

// relicsToCompact is a project holding a retired relic with one live and one
// removed part, a deleted relic with two parts, and a relic still in use.
type relicsToCompact struct {
	retired, deleted, kept string
	live, removed          string
}

func newRelicsToCompact(t *testing.T, repository string, extra ...string) relicsToCompact {
	t.Helper()
	var f relicsToCompact
	f.retired = mustCreateRelic(t, repository, append([]string{"Retired plan", "--type", "plan"}, extra...)...).Relic.ID
	parts := addRelicParts(t, repository, f.retired, "section", "note")
	f.live, f.removed = parts[0], parts[1]
	mustUpdateRelic(t, repository, f.retired, append([]string{"--part", f.removed,
		"--body-file", writeRelicBodyFile(t, "note.md", "dropped text\n")}, extra...)...)
	mustUpdateRelic(t, repository, f.retired, append([]string{"--remove-part", f.removed}, extra...)...)
	mustRelic(t, repository, nil, append([]string{"retire", f.retired}, extra...)...)
	f.deleted = mustCreateRelic(t, repository, append([]string{"Deleted spec", "--type", "spec"}, extra...)...).Relic.ID
	addRelicParts(t, repository, f.deleted, "section", "section")
	mustRelic(t, repository, nil, append([]string{"delete", f.deleted}, extra...)...)
	f.kept = mustCreateRelic(t, repository, append([]string{"Current spec", "--type", "spec"}, extra...)...).Relic.ID
	return f
}

// relicSyncPairIn returns two clones of one bare origin under objectFormat,
// both set up.
func relicSyncPairIn(t *testing.T, objectFormat string) (first, second string) {
	t.Helper()
	if objectFormat == testrepo.FormatSHA256 {
		testrepo.RequireObjectFormat(t, testrepo.FormatSHA256)
	}
	project := codeProject(t, objectFormat)
	second = cloneCode(t, project)
	if code, stdout, stderr := run(t, second, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	return project.seed, second
}

func forBothRelicFormats(t *testing.T, test func(t *testing.T, objectFormat string)) {
	t.Helper()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			test(t, objectFormat)
		})
	}
}

func TestCompactRelicsFlagRefusals(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	f := newRelicsToCompact(t, repository)
	before := localRelicRefs(t, repository)
	const mixed = "--relics cannot be combined with --since, --done-only, --keep-attachments or --adopt"
	const retiredAlone = "--retired-only requires --relics"
	for _, test := range []struct {
		args    []string
		message string
	}{
		{[]string{"--relics", "--since", "7d"}, mixed},
		{[]string{"--relics", "--done-only"}, mixed},
		{[]string{"--relics", "--done-only", "7d"}, mixed},
		{[]string{"--relics", "--deleted-only", "7d"}, mixed},
		{[]string{"--deleted-only=7d", "--relics"}, mixed},
		{[]string{"--relics", "--keep-attachments"}, mixed},
		{[]string{"--relics", "--keep-attachments=false"}, mixed},
		{[]string{"--relics", "--adopt", f.retired}, mixed},
		{[]string{"--relics", "--adopt", f.retired, "--discard-local"}, mixed},
		{[]string{"--retired-only"}, retiredAlone},
		{[]string{"--retired-only", "--deleted-only"}, retiredAlone},
		{[]string{"--relics=false", "--retired-only"}, retiredAlone},
		{[]string{"--relics", "--retired-only", "--deleted-only"}, "--retired-only and --deleted-only cannot be combined"},
		{[]string{"--relics", "--no-backup", "--backup", t.TempDir()}, "--no-backup and --backup cannot be combined"},
		{[]string{"--relics", "--backup", ""}, "compact --backup requires a path"},
		{[]string{"--relics", "--discard-local"}, "--discard-local requires --adopt"},
	} {
		assertCompactRefused(t, repository, test.message, test.args...)
	}
	if code, _, _ := run(t, repository, "compact", "--relics", "--since", "7d"); code != 2 {
		t.Fatalf("refusal code = %d, want 2", code)
	}
	if after := localRelicRefs(t, repository); after != before {
		t.Fatalf("relic refs after refusals =\n%s\nwant\n%s", after, before)
	}
}

// indexRows is every row of every table in the projection cache.
func indexRows(t *testing.T, repository string) string {
	t.Helper()
	database, err := sql.Open("sqlite", relicCachePath(t, repository))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	query := func(statement string) []string {
		rows, err := database.Query(statement)
		if err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("%v", values))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		slices.Sort(lines)
		return lines
	}
	var dump strings.Builder
	for _, line := range query("SELECT name FROM sqlite_master WHERE type = 'table'") {
		table := strings.Trim(line, "[]")
		fmt.Fprintf(&dump, "%s:\n%s\n", table, strings.Join(query(`SELECT * FROM "`+table+`"`), "\n"))
	}
	return dump.String()
}

func TestCompactRelicsDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		repository, _ := relicSyncPairIn(t, objectFormat)
		f := newRelicsToCompact(t, repository)
		// A synchronized dry run fetches, as a task dry run does, so bring the
		// tracking refs up to date first: what changes after is the run's.
		compactRun(t, repository, "fetch")
		// Bring the index up to the refs, so a row written by the dry run shows.
		mustRun(t, repository, "relic", "list", "--all", "--json")
		mustRun(t, repository, "list", "--json")
		refs := func() string { return cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)") }
		objects := func() string { return cliGitOutput(t, repository, "count-objects", "-v") }
		beforeRefs, beforeObjects, beforeRows := refs(), objects(), indexRows(t, repository)
		beforeOrigin := originRelicRefs(t, repository)
		if !strings.Contains(beforeRows, f.retired) {
			t.Fatalf("index rows do not name the retired relic:\n%s", beforeRows)
		}

		for _, args := range [][]string{{"--dry-run", "--no-sync"}, {"--dry-run"}} {
			document := compactRelicsJSON(t, repository, args...)
			summary := document.Data
			if !summary.DryRun || summary.RunID == "" || summary.Bundle != "" {
				t.Fatalf("%v summary = %+v, want a dry run with a run ID and no bundle", args, summary)
			}
			if got, want := compactedRelicIDs(summary), sortedIDs(f.retired, f.deleted); !reflect.DeepEqual(got, want) {
				t.Fatalf("%v relics = %v, want %v", args, got, want)
			}
			if document.Sync == nil || document.Sync.Pushed != nil || document.Sync.PushRelic != nil || document.Warnings != nil {
				t.Fatalf("%v sync = %+v, warnings = %+v; want no push and no warning", args, document.Sync, document.Warnings)
			}
			text := compactRun(t, repository, append([]string{"compact", "--relics"}, args...)...)
			if !strings.Contains(text, "Would compact 2 relics (1 retired, 1 deleted), replacing ") ||
				!strings.Contains(text, wantRelicUpgradeNotice+"\n") ||
				strings.Contains(text, "Backup:") || strings.Contains(text, "git gc") {
				t.Fatalf("%v text = %q", args, text)
			}
		}
		if got := refs(); got != beforeRefs {
			t.Errorf("refs =\n%s\nwant\n%s", got, beforeRefs)
		}
		if got := objects(); got != beforeObjects {
			t.Errorf("objects =\n%s\nwant\n%s", got, beforeObjects)
		}
		if got := indexRows(t, repository); got != beforeRows {
			t.Errorf("index rows =\n%s\nwant\n%s", got, beforeRows)
		}
		if got := originRelicRefs(t, repository); got != beforeOrigin {
			t.Errorf("origin =\n%s\nwant\n%s", got, beforeOrigin)
		}
		assertNoArchiveDirectory(t, repository)
	})
}

func TestCompactRelicsRetiredAndDeleted(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	f := newRelicsToCompact(t, repository)
	kept := relicHeadOf(t, repository, f.kept)
	old := map[string]string{f.retired: relicHeadOf(t, repository, f.retired), f.deleted: relicHeadOf(t, repository, f.deleted)}

	planned := compactRelicsJSON(t, repository, "--dry-run").Data
	if len(planned.Relics) != 2 || len(planned.Skipped) != 0 || planned.Notice != wantRelicUpgradeNotice {
		t.Fatalf("dry run = %+v", planned)
	}
	var want compactRelicTotals
	for _, relic := range planned.Relics {
		switch relic.ID {
		case f.retired:
			if relic.Kind != core.RelicBoundaryRetired || relic.Title != "Retired plan" || relic.DroppedParts != 1 ||
				relic.DroppedBytes <= 0 || relic.Commits != 6 {
				t.Fatalf("retired relic = %+v", relic)
			}
			want.Retired++
		case f.deleted:
			if relic.Kind != core.RelicBoundaryDeleted || relic.Title != "Deleted spec" || relic.DroppedParts != 2 ||
				relic.Commits != 4 {
				t.Fatalf("deleted relic = %+v", relic)
			}
			want.Deleted++
		}
		want.Commits += relic.Commits
		want.DroppedParts += relic.DroppedParts
		want.DroppedBytes += relic.DroppedBytes
		want.EstimatedSavedBytes += relic.EstimatedSavedBytes
	}
	if planned.Totals != want {
		t.Fatalf("totals = %+v, want %+v", planned.Totals, want)
	}
	stdout := compactRun(t, repository, "compact", "--relics", "--dry-run")
	if !strings.Contains(stdout, "Estimated space saved: "+humanBytes(want.EstimatedSavedBytes)) {
		t.Fatalf("dry-run text = %q", stdout)
	}
	data := compactRun(t, repository, "compact", "--relics", "--dry-run", "--json")
	for _, member := range []string{`"dryRun":true`, `"runId":"`, `"relics":[{"id":"`, `"kind":"`, `"droppedParts":`,
		`"droppedBytes":`, `"estimatedSavedBytes":`, `"skipped":[]`,
		`"totals":{"retired":1,"deleted":1,"commits":10,"droppedParts":3,"droppedBytes":`, `"notice":"`} {
		if !strings.Contains(data, member) {
			t.Errorf("compact --relics --json = %s, want %s", data, member)
		}
	}
	if strings.Contains(data, `"bundle"`) || strings.Contains(data, `"tasks":[{`) {
		t.Errorf("compact --relics --json = %s, want no bundle and no compacted task", data)
	}

	// The two selections, one at a time.
	if got := compactedRelicIDs(compactRelicsJSON(t, repository, "--dry-run", "--retired-only").Data); !reflect.DeepEqual(got, []string{f.retired}) {
		t.Fatalf("--retired-only = %v", got)
	}
	if got := compactedRelicIDs(compactRelicsJSON(t, repository, "--dry-run", "--deleted-only").Data); !reflect.DeepEqual(got, []string{f.deleted}) {
		t.Fatalf("--deleted-only = %v", got)
	}

	text := compactRun(t, repository, "compact", "--relics", "--no-sync")
	bundles, err := filepath.Glob(filepath.Join(archiveDirectory(repository), "*.bundle"))
	if err != nil || len(bundles) != 1 {
		t.Fatalf("bundles = %v, %v; want one at the default path", bundles, err)
	}
	var retiredLine, deletedLine string
	for _, relic := range planned.Relics {
		line := fmt.Sprintf("%s  %s  %s  %d commits  %s dropped (%s)\n", relic.ID, relic.Title, relic.Kind,
			relic.Commits, plural(relic.DroppedParts, "part"), humanBytes(relic.DroppedBytes))
		if relic.ID == f.retired {
			retiredLine = line
		} else {
			deletedLine = line
		}
	}
	wantText := retiredLine + deletedLine +
		"Compacted 2 relics (1 retired, 1 deleted), replacing 10 commits.\n" +
		fmt.Sprintf("Dropped parts: 3 (%s).\n", humanBytes(want.DroppedBytes)) +
		"Estimated space saved: " + humanBytes(want.EstimatedSavedBytes) +
		" (applies once Git cleans up unreachable objects)\n" +
		"Backup: " + bundles[0] + "\n" +
		wantRelicUpgradeNotice + "\n" +
		"Run \"git gc\" to reclaim the space locally; origin cleans up on its own schedule.\n"
	if text != wantText {
		t.Fatalf("compact --relics = %q, want %q", text, wantText)
	}
	gitOutput(t, repository, "bundle", "verify", bundles[0])
	for id, head := range old {
		if relicHeadOf(t, repository, id) == head {
			t.Fatalf("%s did not move", id)
		}
		if parents := gitOutput(t, repository, "rev-list", "--parents", "--max-count=1", relicRef(id)); strings.Contains(parents, " ") {
			t.Fatalf("%s head = %q, want a parentless checkpoint", id, parents)
		}
	}
	if relicHeadOf(t, repository, f.kept) != kept {
		t.Fatal("the relic in use moved")
	}
	shown := shownRelic(t, repository, f.retired, "--removed")
	if shown.Compacted == nil || len(shown.Parts) != 2 || shown.Parts[1].Dropped == nil {
		t.Fatalf("relic show = %s", mustJSON(t, shown))
	}
	assertRelicHistoriesValid(t, repository, 3)

	// A second run changes nothing and says so.
	refs := cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)")
	again := compactRun(t, repository, "compact", "--relics")
	for _, line := range []string{
		"Compacted 0 relics (0 retired, 0 deleted), replacing 0 commits.\n",
		"Dropped parts: 0 (0 bytes).\n",
		"Skipped: 2 already compacted\n",
	} {
		if !strings.Contains(again, line) {
			t.Fatalf("second run = %q, want %q", again, line)
		}
	}
	if strings.Contains(again, "Backup:") {
		t.Fatalf("second run = %q, want no bundle", again)
	}
	summary := compactRelicsJSON(t, repository).Data
	wantSkips := []compaction.RelicSkip{
		{RelicID: f.retired, Title: "Retired plan", Reason: compaction.ReasonAlreadyCompacted},
		{RelicID: f.deleted, Title: "Deleted spec", Reason: compaction.ReasonAlreadyCompacted},
	}
	if len(summary.Relics) != 0 || summary.Relics == nil || !reflect.DeepEqual(summary.Skipped, wantSkips) {
		t.Fatalf("second run = %+v, want two already compacted", summary)
	}
	if got := cliGitOutput(t, repository, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
		t.Fatalf("refs after the second run =\n%s\nwant\n%s", got, refs)
	}
}

func TestCompactRelicsBackupChoicesAndOtherSkips(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	f := newRelicsToCompact(t, repository)
	file := filepath.Join(t.TempDir(), "before.bundle")
	summary := compactRelicsJSON(t, repository, "--retired-only", "--backup", file).Data
	if summary.Bundle != file || !reflect.DeepEqual(compactedRelicIDs(summary), []string{f.retired}) {
		t.Fatalf("summary = %+v, want the retired relic bundled at %s", summary, file)
	}
	gitOutput(t, repository, "bundle", "verify", file)
	assertNoArchiveDirectory(t, repository)

	text := compactRun(t, repository, "compact", "--relics", "--no-backup")
	if strings.Contains(text, "Backup:") || !strings.Contains(text, "Compacted 1 relic (0 retired, 1 deleted), replacing 4 commits.\n") ||
		!strings.Contains(text, "Skipped: 1 already compacted\n") {
		t.Fatalf("compact --relics --no-backup = %q", text)
	}
	assertNoArchiveDirectory(t, repository)
}

func TestCompactRelicsSkipsUnpushedEvenWithoutSync(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		first, _ := relicSyncPairIn(t, objectFormat)
		relicID := mustCreateRelic(t, first, "Never published", "--type", "spec", "--no-sync").Relic.ID
		mustRelic(t, first, nil, "retire", relicID, "--no-sync")
		head := relicHeadOf(t, first, relicID)
		want := []compaction.RelicSkip{{RelicID: relicID, Title: "Never published", Reason: compaction.ReasonNotPushed}}
		for _, args := range [][]string{{"--no-sync"}, {}} {
			document := compactRelicsJSON(t, first, args...)
			if len(document.Data.Relics) != 0 || !reflect.DeepEqual(document.Data.Skipped, want) || document.Warnings != nil {
				t.Fatalf("%v = %+v, warnings %+v; want it skipped", args, document.Data, document.Warnings)
			}
		}
		text := compactRun(t, first, "compact", "--relics", "--no-sync")
		if line := "Skipped:\n" + relicID + "  Never published  " + compaction.ReasonNotPushed + "\n"; !strings.Contains(text, line) {
			t.Fatalf("text = %q, want %q", text, line)
		}
		if relicHeadOf(t, first, relicID) != head {
			t.Fatal("a skipped relic moved")
		}
	})
}

func TestCompactWithoutRelicsFlagLeavesRelics(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		first, second := relicSyncPairIn(t, objectFormat)
		f := newRelicsToCompact(t, first)
		publishedDoneTask(t, first, "A task to compact")
		compactRun(t, second, "sync")
		// Origin holds a change this clone has not fetched.
		mustUpdateRelic(t, second, f.kept, "--title", "Changed elsewhere")
		local := refsUnder(t, first, "refs/workbook/relics/")
		tracking := refsUnder(t, first, "refs/workbook/remotes/origin/relics/")
		origin := originRelicRefs(t, first)

		summary, document := compactJSON(t, first)
		if len(summary.Tasks) != 1 {
			t.Fatalf("summary = %+v, warnings %+v; want the one task", summary, document.Warnings)
		}
		if got := refsUnder(t, first, "refs/workbook/relics/"); got != local {
			t.Fatalf("relic refs =\n%s\nwant\n%s", got, local)
		}
		if got := refsUnder(t, first, "refs/workbook/remotes/origin/relics/"); got != tracking {
			t.Fatalf("tracking relic refs =\n%s\nwant\n%s", got, tracking)
		}
		if got := originRelicRefs(t, first); got != origin {
			t.Fatalf("origin relic refs =\n%s\nwant\n%s", got, origin)
		}
	})
}

func TestCompactRelicsPublishesAndSecondCloneConverges(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		first, stale := relicSyncPairIn(t, objectFormat)
		f := newRelicsToCompact(t, first)
		// stale holds the history from before the compaction; fresh never
		// synchronized at all.
		compactRun(t, stale, "sync")
		fresh := cliClone(t, cliGitOutput(t, first, "config", "remote.origin.url"))

		document := compactRelicsJSON(t, first)
		if got, want := compactedRelicIDs(document.Data), sortedIDs(f.retired, f.deleted); !reflect.DeepEqual(got, want) {
			t.Fatalf("relics = %v, want %v (skipped %+v)", got, want, document.Data.Skipped)
		}
		wantResults := []gitstore.SyncRelicResult{}
		for _, id := range sortedIDs(f.retired, f.deleted) {
			wantResults = append(wantResults, gitstore.SyncRelicResult{RelicID: id, Status: gitstore.SyncCompactionPublished})
		}
		if document.Sync == nil || document.Sync.Status != syncStatusCompleted || document.Sync.Pushed == nil ||
			!reflect.DeepEqual(document.Sync.Pushed.Relics, wantResults) || document.Sync.PushRelic != nil ||
			len(document.Warnings) != 0 {
			t.Fatalf("sync = %s, warnings %+v; want both checkpoints published", mustJSON(t, document.Sync), document.Warnings)
		}
		for _, id := range []string{f.retired, f.deleted} {
			if local, remote := relicHeadOf(t, first, id), originRelicHead(t, first, id); local != remote {
				t.Fatalf("origin holds %s for %s, want %s", remote, id, local)
			}
		}

		compactRun(t, stale, "sync")
		compactRun(t, fresh, "setup")
		compactRun(t, fresh, "sync")
		want := shownRelic(t, first, f.retired, "--removed")
		for name, clone := range map[string]string{"stale": stale, "fresh": fresh} {
			got := shownRelic(t, clone, f.retired, "--removed")
			if got.Head != want.Head || got.Compacted == nil || len(got.Parts) != 2 || got.Parts[1].Dropped == nil {
				t.Fatalf("%s clone shows %s, want %s", name, mustJSON(t, got), mustJSON(t, want))
			}
			stub := shownRelic(t, clone, f.retired+"#"+f.removed, "--removed")
			if len(stub.Parts) != 1 || stub.Parts[0].Dropped == nil || stub.Parts[0].Body != nil {
				t.Fatalf("%s clone shows the stub as %s", name, mustJSON(t, stub))
			}
			assertRelicHistoriesValid(t, clone, 3)
		}
	})
}

func TestCompactRelicsNoSyncStaysLocalUntilSync(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		first, _ := relicSyncPairIn(t, objectFormat)
		f := newRelicsToCompact(t, first)
		compactRun(t, first, "fetch")
		origin := originRelicRefs(t, first)
		const unpublished = "the compacted relics are not published; run `workbook sync` to publish them to origin"

		code, text, stderr := run(t, first, "compact", "--relics", "--retired-only", "--no-sync")
		if code != 0 || !strings.Contains(text, "Compacted 1 relic (1 retired, 0 deleted)") ||
			stderr != "workbook: warning: "+unpublished+"\n" {
			t.Fatalf("compact --relics --no-sync = (%d, %q, %q)", code, text, stderr)
		}
		document := compactRelicsJSON(t, first, "--deleted-only", "--no-sync")
		if want := []core.Warning{{Code: core.WarningAutoSync, Message: unpublished}}; !reflect.DeepEqual(document.Warnings, want) {
			t.Fatalf("warnings = %+v, want %+v", document.Warnings, want)
		}
		if document.Sync == nil || document.Sync.Enabled || document.Sync.Pushed != nil {
			t.Fatalf("sync = %+v, want disabled", document.Sync)
		}
		if got := originRelicRefs(t, first); got != origin {
			t.Fatalf("origin =\n%s\nwant unchanged\n%s", got, origin)
		}
		// A run that compacted nothing has nothing unpublished to say.
		if again := compactRelicsJSON(t, first, "--no-sync"); again.Warnings != nil {
			t.Fatalf("warnings of an empty run = %+v", again.Warnings)
		}

		compactRun(t, first, "sync")
		for _, id := range []string{f.retired, f.deleted} {
			if local, remote := relicHeadOf(t, first, id), originRelicHead(t, first, id); local != remote {
				t.Fatalf("after sync origin holds %s for %s, want %s", remote, id, local)
			}
		}
	})
}

func TestCompactRelicsWarnsCheckpointsStayLocalWhenTheFetchFails(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	newRelicsToCompact(t, first)
	cliGit(t, first, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	const stayLocal = "; the compacted relics stay local (unpublished) until the next successful sync"

	dry := compactRelicsJSON(t, first, "--dry-run")
	if len(dry.Warnings) != 1 || strings.Contains(dry.Warnings[0].Message, stayLocal) {
		t.Fatalf("dry-run warnings = %+v", dry.Warnings)
	}
	document := compactRelicsJSON(t, first)
	if len(document.Data.Relics) != 2 || document.Sync.Status != syncStatusFailed || document.Sync.Pushed != nil {
		t.Fatalf("document = %s", mustJSON(t, document))
	}
	if len(document.Warnings) != 1 || document.Warnings[0].Code != core.WarningAutoSync ||
		!strings.HasPrefix(document.Warnings[0].Message, "the run used this clone's last view of origin, because fetch failed: ") ||
		!strings.HasSuffix(document.Warnings[0].Message, stayLocal) {
		t.Fatalf("warnings = %+v", document.Warnings)
	}
}

// Not parallel: it replaces compactExecuteRelics.
func TestCompactRelicsReportsEveryPushWhenOriginRefusesOne(t *testing.T) {
	for _, jsonMode := range []bool{true, false} {
		first, second := cliSyncRepositories(t)
		f := newRelicsToCompact(t, first)
		compactRun(t, second, "sync")
		// The retired relic is the first of the two to be pushed, so the
		// accepted push after it must not hide the refusal.
		if f.retired > f.deleted {
			t.Fatalf("fixture IDs out of order: %s, %s", f.retired, f.deleted)
		}
		real := compactExecuteRelics
		t.Cleanup(func() { compactExecuteRelics = real })
		compactExecuteRelics = func(ctx context.Context, deps compaction.RelicDeps, plan compaction.RelicPlan, options compaction.RelicOptions) (compaction.RelicResult, error) {
			result, err := real(ctx, deps, plan, options)
			mustUpdateRelic(t, second, f.retired, "--title", "Changed while compacting")
			return result, err
		}
		args := []string{"compact", "--relics"}
		if jsonMode {
			args = append(args, "--json")
		}
		code, stdout, stderr := run(t, first, args...)
		compactExecuteRelics = real
		if code != 0 {
			t.Fatalf("compact --relics = (%d, %q, %q)", code, stdout, stderr)
		}
		detail := "relic " + f.retired + ": origin changed it since this clone compacted it"
		if !jsonMode {
			if !strings.Contains(stdout, "Sync:\tfailed") || !strings.Contains(stderr, "workbook: warning: the relics were compacted locally, but ") ||
				!strings.Contains(stderr, detail) {
				t.Fatalf("text = %q, stderr %q", stdout, stderr)
			}
			continue
		}
		var document relicCompactDocument
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatal(err)
		}
		if document.Sync.Status != syncStatusFailed || document.Sync.Pushed == nil || len(document.Sync.Pushed.Relics) != 2 {
			t.Fatalf("sync = %s", mustJSON(t, document.Sync))
		}
		refused, accepted := document.Sync.Pushed.Relics[0], document.Sync.Pushed.Relics[1]
		if refused.RelicID != f.retired || refused.Status != gitstore.SyncRejected ||
			accepted.RelicID != f.deleted || accepted.Status != gitstore.SyncCompactionPublished {
			t.Fatalf("pushed = %+v", document.Sync.Pushed.Relics)
		}
		if len(document.Warnings) != 1 || document.Warnings[0].Code != core.WarningAutoSync ||
			!strings.Contains(document.Warnings[0].Message, detail) {
			t.Fatalf("warnings = %+v", document.Warnings)
		}
	}
}

// Not parallel: it replaces compactExecuteRelics.
func TestCompactRelicsReportsProjectionFailureAsAWarning(t *testing.T) {
	repository := initializedRepository(t)
	newRelicsToCompact(t, repository)
	real := compactExecuteRelics
	t.Cleanup(func() { compactExecuteRelics = real })
	compactExecuteRelics = func(ctx context.Context, deps compaction.RelicDeps, plan compaction.RelicPlan, options compaction.RelicOptions) (compaction.RelicResult, error) {
		result, err := real(ctx, deps, plan, options)
		if err != nil {
			return result, err
		}
		return result, &compaction.ProjectionError{Err: errors.New("disk full"), Relics: true}
	}
	document := compactRelicsJSON(t, repository, "--no-sync")
	want := []core.Warning{{
		Code: core.WarningProjectionUpdate,
		Message: "the relics were compacted, but the SQLite cache could not be updated; run " +
			"`workbook rebuild`: disk full",
	}}
	if len(document.Data.Relics) != 2 || !reflect.DeepEqual(document.Warnings, want) {
		t.Fatalf("relics = %+v, warnings = %+v, want %+v", document.Data.Relics, document.Warnings, want)
	}
}

func TestCompactRelicsDefersPublicationToALiveWatcher(t *testing.T) {
	t.Parallel()
	first, _ := cliSyncRepositories(t)
	f := newRelicsToCompact(t, first)
	startCLIWatcher(t, first, "1h")

	document := compactRelicsJSON(t, first)
	if document.Sync.Status != syncStatusDeferred || document.Sync.Pushed != nil || len(document.Data.Relics) != 2 {
		t.Fatalf("document = %s", mustJSON(t, document))
	}
	waitForRelicPublication(t, first, f.retired, f.deleted)
}

// Every link into a compacted relic still resolves, to current content or to a
// stub with its dates, and is marked with what it reaches. A link from a
// removed part, a dropped one included, is stored but not listed by show
// <task> or --linked-to, as for any removed part.
func TestEveryLinkIntoCompactedRelicResolves(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, objectFormat string) {
		repository, _ := relicSyncPairIn(t, objectFormat)
		task := decodeMutationTask(t, mustRun(t, repository, "create", "Implement compaction", "--json"), "create")
		relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
		parts := addRelicParts(t, repository, relicID, "section", "note")
		live, dropped := parts[0], parts[1]
		mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--rel", "relates-to",
			"--link", task.ID, "--from", dropped, "--rel", "implemented-by",
			"--link", task.ID, "--from", live, "--rel", "tested-by")
		gone := mustCreateRelic(t, repository, "Abandoned plan", "--type", "spec").Relic.ID
		mustUpdateRelic(t, repository, gone, "--link", task.ID, "--rel", "supersedes")
		other := mustCreateRelic(t, repository, "Rollout", "--type", "plan").Relic.ID
		mustUpdateRelic(t, repository, other, "--link", relicID, "--rel", "relates-to",
			"--link", relicID+"#"+dropped, "--rel", "depends-on", "--link", gone, "--rel", "replaces")
		mustUpdateRelic(t, repository, relicID, "--remove-part", dropped)
		mustRelic(t, repository, nil, "retire", relicID)
		mustRelic(t, repository, nil, "delete", gone)

		document := compactRelicsJSON(t, repository)
		if got, want := compactedRelicIDs(document.Data), sortedIDs(relicID, gone); !reflect.DeepEqual(got, want) {
			t.Fatalf("compacted = %+v, want %v", document.Data, want)
		}
		shown := shownRelic(t, repository, relicID, "--removed")
		stub := shown.Parts[1]
		if stub.ID != dropped || stub.Dropped == nil || len(shown.Links) != 3 {
			t.Fatalf("relic show --removed = %s, want the stub and all three links", mustJSON(t, shown))
		}
		deleted := shownRelic(t, repository, gone, "--removed")
		if deleted.Compacted == nil {
			t.Fatalf("relic show of the deleted relic = %s", mustJSON(t, deleted))
		}
		removed := " (removed on " + dateOf(stub.Dropped.RemovedAt) + ")"
		goneSince := " (removed on " + dateOf(deleted.Compacted.Since) + ")"

		text := mustRun(t, repository, "show", task.ID)
		for _, want := range []string{
			"\t" + relicID + " (retired)\trelates-to\tArchive plan\n",
			"\t" + relicID + "#" + live + " (retired)\ttested-by\tArchive plan\n",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("show = %q, want %q", text, want)
			}
		}
		if strings.Contains(text, dropped) {
			t.Errorf("show = %q, want no link from the removed part", text)
		}
		matches := mustRun(t, repository, "relic", "list", "--linked-to", task.ID, "--all")
		for _, want := range []string{
			"\trelic\trelates-to (retired)\n",
			"\t" + live + "\ttested-by (retired)\n",
			"\trelic\tsupersedes" + goneSince + "\n",
		} {
			if !strings.Contains(matches, want) {
				t.Errorf("--linked-to <task> --all = %q, want %q", matches, want)
			}
		}
		// Links from another relic into the relics and into the stub.
		outgoing := mustRun(t, repository, "relic", "show", other)
		for _, want := range []string{
			"\trelates-to\t" + relicID + " (retired)\n",
			"\tdepends-on\t" + relicID + "#" + dropped + removed + "\n",
			"\treplaces\t" + gone + goneSince + "\n",
		} {
			if !strings.Contains(outgoing, want) {
				t.Errorf("relic show of the linking relic = %q, want %q", outgoing, want)
			}
		}
		for _, target := range []string{relicID, relicID + "#" + dropped, gone} {
			var listed []core.RelicSummary
			mustRelic(t, repository, &listed, "list", "--linked-to", target)
			if got := listedIDs(listed); !reflect.DeepEqual(got, []string{other}) {
				t.Errorf("--linked-to %s = %v, want %s", target, got, other)
			}
			mustRun(t, repository, "relic", "show", target, "--removed")
		}
	})
}

func waitForRelicPublication(t *testing.T, clone string, relicIDs ...string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		pending := ""
		for _, id := range relicIDs {
			if originRelicHead(t, clone, id) != relicHeadOf(t, clone, id) {
				pending = id
			}
		}
		if pending == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("origin never reached the local tip of %s", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
