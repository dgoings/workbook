package cli

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
)

// The v0.6.1 release has never heard of relics. It must keep working in a
// clone of an origin that holds them: its fetch refspecs name only tasks,
// project identity and configuration, so relic refs stay out of its clone, and
// nothing it does touches them on origin.

// TestReleasedV061IgnoresRelicsOnOrigin plays the v0.6.1 binary against an
// origin whose relics and tasks this build published.
func TestReleasedV061IgnoresRelicsOnOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the v0.6.1 release; skipped in -short mode")
	}
	t.Parallel()
	binary := v0061Binary(t)

	writer, older := cliSyncRepositories(t)
	first := cliCreateTask(t, writer, "Before the relics")
	relicOne := mustCreateRelic(t, writer, "First relic", "--type", "paper")
	relicTwo := mustCreateRelic(t, writer, "Second relic", "--type", "paper")
	// A link from one relic to another, with a pointer: v0.6.1 knows neither.
	mustUpdateRelic(t, writer, relicTwo.Relic.ID, "--link", relicOne.Relic.ID, "--rel", "supersedes",
		"--pointer", `{"quote":"all of it"}`)
	last := cliCreateTask(t, writer, "After the relics")
	if code, _, stderr := run(t, writer, "push"); code != 0 {
		t.Fatalf("writer push code = %d; stderr = %q", code, stderr)
	}
	relicLines := func(clone string) string {
		t.Helper()
		return cliGitOutput(t, clone, "ls-remote", "origin", "refs/workbook/relics/*")
	}
	originRelics := relicLines(writer)
	if strings.Count(originRelics, "refs/workbook/relics/") != 2 {
		t.Fatalf("origin's relic refs = %q, want both relics", originRelics)
	}
	relicList := func(clone string) string {
		t.Helper()
		code, stdout, stderr := run(t, clone, "relic", "list", "--json")
		if code != 0 {
			t.Fatalf("relic list code = %d; stderr = %q", code, stderr)
		}
		return stdout
	}
	listBefore := relicList(writer)

	if code, stdout, stderr := runBinary(t, binary, older, "setup"); code != 0 {
		t.Fatalf("v0.6.1 setup code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	code, stdout, stderr := runBinary(t, binary, older, "list", "--json")
	if code != 0 {
		t.Fatalf("v0.6.1 list code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	var listed struct {
		Data []core.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil {
		t.Fatalf("decode v0.6.1 list: %v; stdout = %q", err, stdout)
	}
	listedIDs := map[string]bool{}
	for _, task := range listed.Data {
		listedIDs[task.ID] = true
	}
	if len(listed.Data) != 2 || !listedIDs[first.ID] || !listedIDs[last.ID] {
		t.Fatalf("v0.6.1 list holds %d tasks %v, want exactly %s and %s", len(listed.Data), listedIDs, first.ID, last.ID)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "show", last.ID); code != 0 {
		t.Fatalf("v0.6.1 show code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "update", last.ID, "--title", "Renamed"); code != 0 {
		t.Fatalf("v0.6.1 update code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("v0.6.1 sync code = %d; stdout = %q; stderr = %q", code, stdout, stderr)
	}

	if refs := cliGitOutput(t, older, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "relics") {
		t.Fatalf("the v0.6.1 clone holds a relic ref:\n%s", refs)
	}
	if got := relicLines(older); got != originRelics {
		t.Fatalf("origin's relic refs changed:\nbefore: %s\nafter:  %s", originRelics, got)
	}

	// This build reads the rename and every relic unchanged.
	if code, _, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stderr = %q", code, stderr)
	}
	code, stdout, stderr = run(t, writer, "show", last.ID, "--json")
	if code != 0 {
		t.Fatalf("writer show code = %d; stderr = %q", code, stderr)
	}
	var shown struct {
		Data core.Task `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("decode show: %v; stdout = %q", err, stdout)
	}
	if shown.Data.Title != "Renamed" {
		t.Fatalf("this build shows the task as %q, want Renamed", shown.Data.Title)
	}
	if got := relicList(writer); got != listBefore {
		t.Fatalf("relic list changed:\nbefore: %s\nafter:  %s", listBefore, got)
	}
	for _, id := range []string{relicOne.Relic.ID, relicTwo.Relic.ID} {
		if !strings.Contains(originRelics, id) {
			t.Fatalf("origin's relic refs %q do not name %s", originRelics, id)
		}
	}
}

var supportedGeneration = regexp.MustCompile(`(?m)^const SupportedFormatGeneration = (\d+)$`)

// preMergeBinary compiles the newest first-parent ancestor of HEAD that reads
// the generation just below a merged relic body's, found by what its
// internal/core/operation.go claims rather than by a commit named here. Only
// that generation will do: a build from further back may have no relic
// commands at all. A history that holds no such commit, as after a squash
// merge of the work that added both generations, skips the test.
func preMergeBinary(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"git", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			testenv.MissingCapability(t, "%s is not installed", tool)
		}
	}
	root := repositoryRoot(t)
	revisions, err := exec.Command("git", "-C", root, "rev-list", "--first-parent", "HEAD").Output()
	if err != nil {
		testenv.MissingCapability(t, "the source tree's Git history cannot be listed: %v", err)
	}
	for _, revision := range strings.Fields(string(revisions)) {
		operations, err := exec.Command("git", "-C", root, "show", revision+":internal/core/operation.go").Output()
		if err != nil {
			continue
		}
		claimed := supportedGeneration.FindSubmatch(operations)
		if claimed == nil {
			continue
		}
		generation, err := strconv.Atoi(string(claimed[1]))
		if err != nil {
			t.Fatalf("read the generation %s claims: %v", revision, err)
		}
		if generation == core.RelicMergeMinReader-1 {
			t.Logf("building %s, which reads generation %d", revision, generation)
			return revisionBinary(t, revision, "commit "+revision+" is not in this checkout")
		}
		if generation < core.RelicMergeMinReader-1 {
			break
		}
	}
	shallow, err := exec.Command("git", "-C", root, "rev-parse", "--is-shallow-repository").Output()
	if err != nil || strings.TrimSpace(string(shallow)) != "false" {
		testenv.MissingCapability(t, "this checkout is shallow; fetch the full history to run the older-build interop test")
	}
	t.Skipf("no first-parent ancestor of HEAD reads generation %d, the one just before merged relic bodies", core.RelicMergeMinReader-1)
	return ""
}

// A build from before relic body merges meets a relic this build merged. It
// shows it from its stored state, ends its history at the merge as a newer
// writer's, and refuses to change it. A clone of its own
// holding a change from before the merge cannot replay it, and keeps it: its
// sync reports the relic as needing an upgrade and moves neither its ref nor
// origin's. This build then replays that change onto the merge.
func TestPreMergeBuildRefusesAMergedRelic(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a pre-merge build; skipped in -short mode")
	}
	t.Parallel()
	binary := preMergeBinary(t)
	writer, other := cliSyncRepositories(t)
	older := cliClone(t, cliGitOutput(t, writer, "remote", "get-url", "origin"))

	const base = "one\ntwo\nthree\nfour\nfive\n"
	relicID := mustCreateRelic(t, writer, "Merged plan", "--type", "paper").Relic.ID
	added := mustUpdateRelic(t, writer, relicID, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, "plan.md", base))
	partID, version := added.Parts[0].ID, added.Parts[0].Body.Version
	if code, stdout, stderr := run(t, other, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	// The older clone takes the relic as it stands before the merge and saves a
	// title it does not publish.
	if code, stdout, stderr := runBinary(t, binary, older, "setup"); code != 0 {
		t.Fatalf("older setup = (%d, %q, %q)", code, stdout, stderr)
	}
	if code, stdout, stderr := runBinary(t, binary, older, "relic", "update", relicID, "--title", "Mine locally", "--no-sync"); code != 0 {
		t.Fatalf("older relic update = (%d, %q, %q)", code, stdout, stderr)
	}
	held := relicHeadOf(t, older, relicID)

	// Two saves of different lines, merged by the second one's sync.
	mustUpdateRelic(t, writer, relicID, "--part", partID, "--base", version, "--no-sync",
		"--body-file", writeRelicBodyFile(t, "plan.md", strings.Replace(base, "one", "ONE", 1)))
	mustUpdateRelic(t, other, relicID, "--part", partID, "--base", version, "--no-sync",
		"--body-file", writeRelicBodyFile(t, "plan.md", strings.Replace(base, "five", "FIVE", 1)))
	if code, stdout, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync = (%d, %q, %q)", code, stdout, stderr)
	}
	code, stdout, stderr := run(t, other, "sync", "--json")
	if code != 0 || !strings.Contains(stdout, `"`+core.WarningRelicBodyMerged+`"`) {
		t.Fatalf("merging sync = (%d, %q, %q), want exit 0 and %s", code, stdout, stderr, core.WarningRelicBodyMerged)
	}
	merged := originRelicHead(t, other, relicID)
	if merged != relicHeadOf(t, other, relicID) {
		t.Fatalf("origin holds %s, want the merged %s", merged, relicHeadOf(t, other, relicID))
	}
	for _, name := range []string{"operation.json", "state.json"} {
		if document := gitOutput(t, other, "show", merged+":"+name); !strings.Contains(document, `"minReader":9`) {
			t.Fatalf("%s of the merge carries no generation-nine marker: %s", name, document)
		}
	}

	// The clone holding its own change cannot replay it onto the merge.
	for attempt := range 2 {
		code, stdout, stderr = runBinary(t, binary, older, "sync", "--json")
		if report := stdout + stderr; code != 9 ||
			!strings.Contains(report, `"relicId":"`+relicID+`","status":"needs-upgrade"`) {
			t.Fatalf("older sync %d = (%d, %q, %q), want exit 9 and %s as needs-upgrade", attempt+1, code, stdout, stderr, relicID)
		}
		if got := relicHeadOf(t, older, relicID); got != held {
			t.Fatalf("older sync %d moved its relic from %s to %s", attempt+1, held, got)
		}
		if got := originRelicHead(t, older, relicID); got != merged {
			t.Fatalf("older sync %d moved origin's relic from %s to %s", attempt+1, merged, got)
		}
	}

	// A clone with no change of its own takes the merge, shows it and refuses
	// to change it.
	fresh := cliClone(t, cliGitOutput(t, writer, "remote", "get-url", "origin"))
	if code, stdout, stderr := runBinary(t, binary, fresh, "setup"); code != 0 {
		t.Fatalf("older setup of a fresh clone = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := relicHeadOf(t, fresh, relicID); got != merged {
		t.Fatalf("older fresh clone holds %s, want the merge %s", got, merged)
	}
	code, stdout, stderr = runBinary(t, binary, fresh, "relic", "show", relicID+"#"+partID, "--body")
	if code != 0 || stdout != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("older relic show --body = (%d, %q, %q), want the merged body", code, stdout, stderr)
	}
	var shown core.RelicDocument
	code, stdout, stderr = runBinary(t, binary, fresh, "relic", "show", relicID, "--json")
	if code != 0 {
		t.Fatalf("older relic show --json = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "relic show").Data, &shown); err != nil {
		t.Fatalf("decode older relic show: %v", err)
	}
	if shown.Head != merged || shown.Title != "Merged plan" || len(shown.Parts) != 1 {
		t.Fatalf("older relic show = %+v, want the merge %s as it is stored", shown, merged)
	}
	// Such a build adds no newer-writer warning to a relic it shows. It says
	// so where it has to read the merge itself, as the history does.
	var history core.RelicHistory
	code, stdout, stderr = runBinary(t, binary, fresh, "relic", "show", relicID, "--history", "--json")
	if code != 0 {
		t.Fatalf("older relic history = (%d, %q, %q), want exit 0", code, stdout, stderr)
	}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "relic show").Data, &history); err != nil {
		t.Fatalf("decode older relic history: %v", err)
	}
	if history.Truncated == nil || history.Truncated.Commit != merged || history.Truncated.Category != core.CategoryNewerWriter {
		t.Fatalf("older history truncation = %+v, want %s as a newer writer's", history.Truncated, merged)
	}
	code, stdout, stderr = runBinary(t, binary, fresh, "relic", "update", relicID, "--title", "Changed by an older build")
	if code != 9 || !strings.Contains(stderr, "written by a newer workbook; upgrade workbook") {
		t.Fatalf("older relic update = (%d, %q, %q), want exit 9", code, stdout, stderr)
	}
	if got := relicHeadOf(t, fresh, relicID); got != merged {
		t.Fatalf("a refused update moved the relic from %s to %s", merged, got)
	}
	if got := originRelicHead(t, fresh, relicID); got != merged {
		t.Fatalf("origin's relic moved from %s to %s", merged, got)
	}

	// Upgraded, the clone that was left behind replays its title onto the merge.
	code, stdout, stderr = run(t, older, "sync", "--json")
	if code != 0 {
		t.Fatalf("sync after the upgrade = (%d, %q, %q)", code, stdout, stderr)
	}
	shown = core.RelicDocument{}
	mustRelic(t, older, &shown, "show", relicID+"#"+partID)
	if shown.Title != "Mine locally" {
		t.Fatalf("title after the upgrade = %q, want the change made by the older build", shown.Title)
	}
	if code, stdout, stderr := run(t, older, "relic", "show", relicID+"#"+partID, "--body"); code != 0 || stdout != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("body after the upgrade = (%d, %q, %q), want the merged body", code, stdout, stderr)
	}
	if got, want := originRelicHead(t, older, relicID), relicHeadOf(t, older, relicID); got != want || got == merged {
		t.Fatalf("origin holds %s after the upgraded sync, want the replayed %s", got, want)
	}
}
