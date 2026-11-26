package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicSyncEnvelope decodes the members of a relic command's or a sync's
// result envelope these tests assert on.
type relicSyncEnvelope struct {
	Data          json.RawMessage      `json:"data"`
	RelicConflict []core.RelicConflict `json:"relicConflict"`
	Warnings      []core.Warning       `json:"warnings"`
	Sync          *struct {
		Enabled   bool   `json:"enabled"`
		Status    string `json:"status"`
		Detail    string `json:"detail"`
		PushRelic *struct {
			RelicID string `json:"relicId"`
			Status  string `json:"status"`
		} `json:"pushRelic"`
	} `json:"sync"`
}

func decodeRelicSyncEnvelope(t *testing.T, output, command string) relicSyncEnvelope {
	t.Helper()
	assertJSONResult(t, output, command)
	var envelope relicSyncEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode %s envelope: %v; output = %q", command, err, output)
	}
	return envelope
}

// originRelicHead is the tip origin holds for a relic, or "" when it holds
// none.
func originRelicHead(t *testing.T, clone, relicID string) string {
	t.Helper()
	fields := strings.Fields(cliGitOutput(t, clone, "ls-remote", "origin", relicRef(relicID)))
	if len(fields) != 2 {
		return ""
	}
	return fields[0]
}

// runRelicJSON runs a relic command with --json, requires exit code want, and
// decodes its envelope.
func runRelicJSON(t *testing.T, repository string, want int, args ...string) relicSyncEnvelope {
	t.Helper()
	full := append([]string{"relic"}, append(args, "--json")...)
	code, stdout, stderr := run(t, repository, full...)
	if code != want {
		t.Fatalf("%s = (%d, %q, %q), want exit %d", strings.Join(full, " "), code, stdout, stderr, want)
	}
	return decodeRelicSyncEnvelope(t, stdout, "relic "+args[0])
}

func requirePublishedRelic(t *testing.T, envelope relicSyncEnvelope, relicID string) {
	t.Helper()
	if envelope.Sync == nil || !envelope.Sync.Enabled || envelope.Sync.Status != "completed" ||
		envelope.Sync.PushRelic == nil || envelope.Sync.PushRelic.RelicID != relicID ||
		envelope.Sync.PushRelic.Status != "published" {
		t.Fatalf("sync = %+v, want completed with %s published", envelope.Sync, relicID)
	}
}

func TestRelicMutationsPublishThemselves(t *testing.T) {
	t.Parallel()
	clone, _ := cliSyncRepositories(t)

	created := runRelicJSON(t, clone, 0, "create", "Published relic", "--type", "spec")
	var result core.RelicMutationResult
	if err := json.Unmarshal(created.Data, &result); err != nil {
		t.Fatal(err)
	}
	relicID := result.Relic.ID
	steps := []struct {
		name string
		args []string
	}{
		{"create", nil},
		{"update", []string{"update", relicID, "--title", "Renamed relic"}},
		{"delete", []string{"delete", relicID}},
		{"restore", []string{"restore", relicID}},
	}
	for _, step := range steps {
		envelope := created
		if step.args != nil {
			envelope = runRelicJSON(t, clone, 0, step.args...)
		}
		requirePublishedRelic(t, envelope, relicID)
		if local, remote := relicHeadOf(t, clone, relicID), originRelicHead(t, clone, relicID); local != remote {
			t.Fatalf("after relic %s origin holds %q, the clone %q", step.name, remote, local)
		}
	}
}

func TestRelicNoSyncAndAutoSyncOffStayLocal(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"--no-sync", "auto-sync off"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			clone, _ := cliSyncRepositories(t)
			var extra []string
			if mode == "--no-sync" {
				extra = []string{"--no-sync"}
			} else if code, _, stderr := run(t, clone, "config", "set", "auto-sync", "false", "--json"); code != 0 {
				t.Fatalf("config set auto-sync false = %d; stderr = %q", code, stderr)
			}
			envelope := runRelicJSON(t, clone, 0, append([]string{"create", "Kept here", "--type", "spec"}, extra...)...)
			var result core.RelicMutationResult
			if err := json.Unmarshal(envelope.Data, &result); err != nil {
				t.Fatal(err)
			}
			relicID := result.Relic.ID
			for _, args := range [][]string{
				nil,
				{"update", relicID, "--title", "Still here"},
				{"delete", relicID},
				{"restore", relicID},
			} {
				if args != nil {
					envelope = runRelicJSON(t, clone, 0, append(args, extra...)...)
				}
				if envelope.Sync == nil || envelope.Sync.Enabled {
					t.Fatalf("relic %v sync = %+v, want disabled", args, envelope.Sync)
				}
				if got := originRelicHead(t, clone, relicID); got != "" {
					t.Fatalf("origin holds the relic at %s after relic %v", got, args)
				}
			}
			if code, stdout, stderr := run(t, clone, "sync"); code != 0 {
				t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
			}
			if got, want := originRelicHead(t, clone, relicID), relicHeadOf(t, clone, relicID); got != want {
				t.Fatalf("origin holds %q after sync, want %s", got, want)
			}
		})
	}
}

func TestRelicCreatedInOneCloneIsReadableInAnother(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	relicID := mustCreateRelic(t, first, "Shared plan", "--type", "plan").Relic.ID
	mustUpdateRelic(t, first, relicID, "--set", "owner=board",
		"--add-part", "--part-type", "section", "--field", "heading=Goals",
		"--body-file", writeRelicBodyFile(t, "goals.md", "Ship the relics.\n"))

	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	export := func(repository string) string {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "export", relicID, "--json")
		if code != 0 {
			t.Fatalf("relic export = (%d, %q, %q)", code, stdout, stderr)
		}
		return stdout
	}
	if got, want := export(second), export(first); got != want {
		t.Fatalf("the second clone exports\n%s\nthe first\n%s", got, want)
	}
}

func TestSetupBringsEveryRelicToAFreshCloneAndATasksOnlyFolder(t *testing.T) {
	t.Parallel()
	for _, format := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			if format == testrepo.FormatSHA256 {
				testrepo.RequireObjectFormat(t, testrepo.FormatSHA256)
			}
			project := codeProject(t, format)
			createThreeRelics(t, project.seed)
			listed := func(repository string) string {
				t.Helper()
				code, stdout, stderr := run(t, repository, "relic", "list", "--json")
				if code != 0 {
					t.Fatalf("relic list = (%d, %q, %q)", code, stdout, stderr)
				}
				return stdout
			}
			want := listed(project.seed)

			clone := cloneCode(t, project)
			if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
				t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
			}
			if got := listed(clone); got != want {
				t.Fatalf("the fresh clone lists\n%s\nthe source\n%s", got, want)
			}

			folder := filepath.Join(t.TempDir(), "board")
			if err := os.Mkdir(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.code); code != 0 {
				t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
			}
			if got := listed(folder); got != want {
				t.Fatalf("the tasks-only folder lists\n%s\nthe source\n%s", got, want)
			}
			if got := refsUnder(t, folder, "refs/heads/"); got != "" {
				t.Fatalf("the tasks-only folder holds branches:\n%s", got)
			}
		})
	}
}

func TestRelicCommandsSyncInATasksOnlyFolder(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	folder := filepath.Join(t.TempDir(), "board")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run(t, folder, "setup", "--tasks-only", project.code); code != 0 {
		t.Fatalf("setup --tasks-only = (%d, %q, %q)", code, stdout, stderr)
	}
	cliGit(t, folder, "config", "user.name", "Board Server")
	cliGit(t, folder, "config", "user.email", "board@example.test")

	relicID := mustCreateRelic(t, folder, "Made on the board", "--type", "spec").Relic.ID
	if got, want := originRelicHead(t, folder, relicID), relicHeadOf(t, folder, relicID); got != want {
		t.Fatalf("origin holds %q after relic create in the folder, want %s", got, want)
	}

	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	mustUpdateRelic(t, teammate, relicID, "--set", "reviewer=teammate")

	updated := runRelicJSON(t, folder, 0, "update", relicID, "--title", "Renamed on the board")
	requirePublishedRelic(t, updated, relicID)
	if code, stdout, stderr := run(t, teammate, "fetch"); code != 0 {
		t.Fatalf("teammate fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	shown := shownRelic(t, teammate, relicID)
	if shown.Title != "Renamed on the board" || string(shown.Properties["reviewer"]) != `"teammate"` {
		t.Fatalf("the teammate sees %+v, want both changes", shown)
	}
}

// relicBodyConflict is two clones that each saved one part's body from the
// same version, the first of them synchronized.
type relicBodyConflict struct {
	first, second       string
	relicID, partID     string
	baseVersion, theirs string // theirs is the first clone's saved version
}

// newRelicBodyConflict builds the conflict. extra is added to the second
// clone's save of its body, as other changes made in the same command.
func newRelicBodyConflict(t *testing.T, title, base, theirs, ours, name string, extra ...string) relicBodyConflict {
	t.Helper()
	first, second := cliSyncRepositories(t)
	state := relicBodyConflict{first: first, second: second}
	state.relicID = mustCreateRelic(t, first, title, "--type", "spec").Relic.ID
	added := mustUpdateRelic(t, first, state.relicID, "--add-part", "--part-type", "section",
		"--body-file", writeRelicBodyFile(t, name, base))
	state.partID, state.baseVersion = added.Parts[0].ID, added.Parts[0].Body.Version
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	saved := mustUpdateRelic(t, first, state.relicID, "--part", state.partID,
		"--body-file", writeRelicBodyFile(t, name, theirs), "--base", state.baseVersion, "--no-sync")
	state.theirs = saved.Parts[0].Body.Version
	mustUpdateRelic(t, second, state.relicID, append([]string{"--part", state.partID,
		"--body-file", writeRelicBodyFile(t, name, ours), "--base", state.baseVersion, "--no-sync"}, extra...)...)
	if code, stdout, stderr := run(t, first, "sync"); code != 0 {
		t.Fatalf("first sync = (%d, %q, %q)", code, stdout, stderr)
	}
	return state
}

func textOf(version *core.RelicBodyVersion) string {
	if version == nil || version.Text == nil {
		return "<no text>"
	}
	return *version.Text
}

func base64Of(version *core.RelicBodyVersion) string {
	if version == nil || version.Base64 == nil || version.Text != nil {
		return "<no base64>"
	}
	return *version.Base64
}

func showPartBody(t *testing.T, repository, relicID, partID string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, "relic", "show", relicID+"#"+partID, "--body")
	if code != 0 {
		t.Fatalf("relic show --body = (%d, %q, %q)", code, stdout, stderr)
	}
	return stdout
}

func TestRelicBodyConflictReportsThreeVersions(t *testing.T) {
	t.Parallel()
	const base, theirs, ours = "The original text.\n", "The first clone's text.\n", "The second clone's text.\n"
	// The second clone's body save also sets a property and the title, and a
	// later command sets another property; none of them is applied.
	state := newRelicBodyConflict(t, "Contested", base, theirs, ours, "body.md",
		"--set", "samepackprop=a", "--title", "Lost title")
	withBody := cliGitOutput(t, state.second, "rev-parse", relicRef(state.relicID))
	mustUpdateRelic(t, state.second, state.relicID, "--set", "later=a", "--no-sync")
	later := cliGitOutput(t, state.second, "rev-parse", relicRef(state.relicID))

	code, stdout, stderr := run(t, state.second, "sync", "--json")
	if code != 8 {
		t.Fatalf("second sync = (%d, %q, %q), want exit 8", code, stdout, stderr)
	}
	envelope := decodeRelicSyncEnvelope(t, stdout, "sync")
	if len(envelope.RelicConflict) != 1 {
		t.Fatalf("relicConflict = %+v, want one", envelope.RelicConflict)
	}
	conflict := envelope.RelicConflict[0]
	unapplied := conflict.Unapplied
	if len(unapplied) != 3 || unapplied[0].Commit != withBody || unapplied[1].Commit != withBody || unapplied[2].Commit != later ||
		unapplied[2].Operation != core.RelicOperationPropertySet {
		t.Fatalf("unapplied = %+v, want the two other changes of commit %s and the property.set of %s", unapplied, withBody, later)
	}
	kinds := map[core.RelicOperationType]bool{unapplied[0].Operation: true, unapplied[1].Operation: true}
	if !kinds[core.RelicOperationPropertySet] || !kinds[core.RelicOperationTitleSet] {
		t.Fatalf("unapplied = %+v, want a property.set and a title.set from the body's command", unapplied)
	}
	for _, operation := range unapplied {
		if shown := cliGitOutput(t, state.second, "cat-file", "blob", operation.Commit+":operation.json"); !strings.Contains(shown, operation.OperationID) {
			t.Fatalf("commit %s's operation.json does not hold %s:\n%s", operation.Commit, operation.OperationID, shown)
		}
	}
	line := "; 3 other local change(s) were not applied either: " +
		string(unapplied[0].Operation) + " " + unapplied[0].OperationID + ", " +
		string(unapplied[1].Operation) + " " + unapplied[1].OperationID + ", " +
		string(unapplied[2].Operation) + " " + unapplied[2].OperationID + "; they are kept at " + conflict.ParkedRef +
		"; `git cat-file blob " + withBody + ":operation.json` shows the first"
	if !strings.Contains(stderr, strings.ReplaceAll(line, ">", `\u003e`)) {
		t.Fatalf("the error envelope does not name the other changes, %q:\n%s", line, stderr)
	}
	if !strings.Contains(stdout, line) {
		t.Fatalf("the fetch's relic detail does not name the other changes, %q:\n%s", line, stdout)
	}
	if conflict.RelicID != state.relicID || conflict.Type != core.RelicConflictBody || conflict.Part != state.partID ||
		conflict.Body == nil {
		t.Fatalf("conflict = %+v, want a body conflict on %s#%s", conflict, state.relicID, state.partID)
	}
	if got := textOf(conflict.Body.Base); got != base {
		t.Errorf("body.base.text = %q, want %q", got, base)
	}
	if got := textOf(&conflict.Body.Ours); got != ours {
		t.Errorf("body.ours.text = %q, want %q", got, ours)
	}
	if got := textOf(&conflict.Body.Theirs); got != theirs {
		t.Errorf("body.theirs.text = %q, want %q", got, theirs)
	}
	if conflict.Body.Theirs.Version != state.theirs {
		t.Errorf("body.theirs.version = %q, want %q", conflict.Body.Theirs.Version, state.theirs)
	}
	if conflict.ParkedRef == "" {
		t.Fatal("the conflict names no parked ref")
	}
	cliGitOutput(t, state.second, "rev-parse", "--verify", conflict.ParkedRef)

	if got := showPartBody(t, state.second, state.relicID, state.partID); got != theirs {
		t.Fatalf("relic show --body = %q, want the first clone's %q", got, theirs)
	}

	const combined = "Both clones' text, combined.\n"
	saved := runRelicJSON(t, state.second, 0, "update", state.relicID, "--part", state.partID,
		"--body-file", writeRelicBodyFile(t, "mine.md", combined), "--base", conflict.Body.Theirs.Version)
	requirePublishedRelic(t, saved, state.relicID)
	if code, stdout, stderr := run(t, state.first, "fetch"); code != 0 {
		t.Fatalf("first fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := showPartBody(t, state.first, state.relicID, state.partID); got != combined {
		t.Fatalf("the first clone's body = %q, want %q", got, combined)
	}
}

func TestRelicBinaryBodyConflictUsesBase64(t *testing.T) {
	t.Parallel()
	const base, theirs, ours = "\xff\x00base", "\xfe\x01theirs", "\xfd\x02ours"
	state := newRelicBodyConflict(t, "Binary", base, theirs, ours, "body.bin")

	code, stdout, stderr := run(t, state.second, "sync", "--json")
	if code != 8 {
		t.Fatalf("second sync = (%d, %q, %q), want exit 8", code, stdout, stderr)
	}
	envelope := decodeRelicSyncEnvelope(t, stdout, "sync")
	if len(envelope.RelicConflict) != 1 || envelope.RelicConflict[0].Body == nil {
		t.Fatalf("relicConflict = %+v, want one body conflict", envelope.RelicConflict)
	}
	body := envelope.RelicConflict[0].Body
	for _, version := range []struct {
		name    string
		version *core.RelicBodyVersion
		want    string
	}{
		{"base", body.Base, base},
		{"ours", &body.Ours, ours},
		{"theirs", &body.Theirs, theirs},
	} {
		if got := base64Of(version.version); got != base64.StdEncoding.EncodeToString([]byte(version.want)) {
			t.Errorf("body.%s = %+v, want only base64 of %q", version.name, version.version, version.want)
		}
	}
}

// requireTerminalSafe fails on any byte below 0x20 other than tab and newline.
func requireTerminalSafe(t *testing.T, name, output string) {
	t.Helper()
	for index := 0; index < len(output); index++ {
		if b := output[index]; b < 0x20 && b != '\t' && b != '\n' {
			t.Fatalf("%s holds byte %#x at %d: %q", name, b, index, output)
		}
	}
}

func TestRelicConflictTextOutputIsTerminalSafe(t *testing.T) {
	t.Parallel()
	const (
		base   = "BASE-TEXT \x1b[2J\a\n"
		theirs = "THEIRS-TEXT \x1b[2J\a\n"
		ours   = "OURS-TEXT \x1b[2J\a\n"
	)
	state := newRelicBodyConflict(t, "Hostile \x1b[2J\a title", base, theirs, ours, "body.md")
	mustUpdateRelic(t, state.second, state.relicID, "--set", "later=a", "--no-sync")
	later := cliGitOutput(t, state.second, "rev-parse", relicRef(state.relicID))

	code, stdout, stderr := run(t, state.second, "sync")
	if code != 8 {
		t.Fatalf("second sync = (%d, %q, %q), want exit 8", code, stdout, stderr)
	}
	requireTerminalSafe(t, "standard output", stdout)
	requireTerminalSafe(t, "standard error", stderr)
	for _, marker := range []string{"BASE-TEXT", "THEIRS-TEXT", "OURS-TEXT"} {
		if strings.Contains(stdout+stderr, marker) {
			t.Fatalf("the text output holds body text %s:\n%s\n%s", marker, stdout, stderr)
		}
	}
	if !strings.Contains(stdout, "Relic conflict:\t"+state.relicID+"\tbody\t") {
		t.Fatalf("standard output has no relic conflict line:\n%s", stdout)
	}
	parked := "refs/workbook/reconciled-relics/" + state.relicID + "/1"
	commands := []string{
		"git cat-file blob " + parked + ":parts/" + state.partID + "/body > mine.md",
		"; 1 other local change(s) were not applied either: property.set ",
		"git cat-file blob " + later + ":operation.json",
		"workbook relic show " + state.relicID + "#" + state.partID + " --body > current.md",
		"workbook relic update " + state.relicID + " --part " + state.partID + " --body-file mine.md --base " + state.theirs,
	}
	for _, command := range commands {
		if !strings.Contains(stdout, command) {
			t.Fatalf("standard output does not name %q:\n%s", command, stdout)
		}
	}

	// The commands as printed recover the second clone's text and save it.
	work := t.TempDir()
	mine, err := exec.Command("git", "-C", state.second, "cat-file", "blob", parked+":parts/"+state.partID+"/body").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(mine) != ours {
		t.Fatalf("the parked text = %q, want %q", mine, ours)
	}
	if current := showPartBody(t, state.second, state.relicID, state.partID); current != theirs {
		t.Fatalf("current.md = %q, want %q", current, theirs)
	}
	minePath := filepath.Join(work, "mine.md")
	if err := os.WriteFile(minePath, mine, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run(t, state.second, "relic", "update", state.relicID, "--part", state.partID,
		"--body-file", minePath, "--base", state.theirs); code != 0 {
		t.Fatalf("relic update = (%d, %q, %q)", code, stdout, stderr)
	}
	if got := showPartBody(t, state.second, state.relicID, state.partID); got != ours {
		t.Fatalf("the saved body = %q, want %q", got, ours)
	}
}

func TestRelicCommandStopsOnItsOwnConflict(t *testing.T) {
	t.Parallel()
	state := newRelicBodyConflict(t, "Contested", "base\n", "theirs\n", "ours\n", "body.md")

	envelope := runRelicJSON(t, state.second, 8, "update", state.relicID, "--title", "T")
	if len(envelope.RelicConflict) != 1 || envelope.RelicConflict[0].RelicID != state.relicID {
		t.Fatalf("relicConflict = %+v, want the relic's conflict", envelope.RelicConflict)
	}
	if got, want := relicHeadOf(t, state.second, state.relicID), originRelicHead(t, state.second, state.relicID); got != want {
		t.Fatalf("the relic is at %s after the stopped update, want origin's %s", got, want)
	}
	if shown := shownRelic(t, state.second, state.relicID); shown.Title == "T" {
		t.Fatal("the stopped update changed the title")
	}

	again := runRelicJSON(t, state.second, 0, "update", state.relicID, "--title", "T")
	requirePublishedRelic(t, again, state.relicID)
	if shown := shownRelic(t, state.second, state.relicID); shown.Title != "T" {
		t.Fatalf("title = %q after the retry, want T", shown.Title)
	}
}

func TestSyncTextOutputNamesChangedRelics(t *testing.T) {
	t.Parallel()
	first, second := cliSyncRepositories(t)
	unchanged := mustCreateRelic(t, first, "Already here", "--type", "spec").Relic.ID
	if code, stdout, stderr := run(t, second, "fetch"); code != 0 {
		t.Fatalf("fetch = (%d, %q, %q)", code, stdout, stderr)
	}
	created := mustCreateRelic(t, first, "New on origin", "--type", "spec").Relic.ID

	code, stdout, stderr := run(t, second, "sync")
	if code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	var lines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, created) {
			lines = append(lines, line)
		}
		if strings.Contains(line, unchanged) {
			t.Fatalf("sync names the unchanged relic: %q", line)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], created+"\tcreated") {
		t.Fatalf("sync lines naming %s = %q, want one created line; output:\n%s", created, lines, stdout)
	}
}

// A sync in which every local change was already on origin, here the same
// body saved from the same version in both clones, moves the relic to
// origin's tip and says so as a fast-forward: nothing was replayed.
func TestSyncReportsAgreeingLocalChangesAsAFastForward(t *testing.T) {
	t.Parallel()
	state := newRelicBodyConflict(t, "Agreed", "one\n", "same\n", "same\n", "agreed.md")

	code, stdout, stderr := run(t, state.second, "sync")
	if code != 0 {
		t.Fatalf("sync = (%d, %q, %q)", code, stdout, stderr)
	}
	var lines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, state.relicID+"\t") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 || lines[0] != state.relicID+"\tfast-forwarded" {
		t.Fatalf("sync lines naming %s = %q, want the fetch's to read fast-forwarded; output:\n%s", state.relicID, lines, stdout)
	}
	if local, remote := relicHeadOf(t, state.second, state.relicID), originRelicHead(t, state.second, state.relicID); local != remote {
		t.Fatalf("the clone holds %s, origin %s; want origin's tip", local, remote)
	}
}
