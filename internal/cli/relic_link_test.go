package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// linkedRelicRepository is a repository with a task and a relic with one
// part, which the link tests link from.
func linkedRelicRepository(t *testing.T) (repository string, task core.Task, relicID, partID string) {
	t.Helper()
	repository = initializedRepository(t)
	task = cliCreateTask(t, repository, "Implement compaction")
	relicID = mustCreateRelic(t, repository, "Archive plan", "--type", "paper", "--no-sync").Relic.ID
	partID = addRelicParts(t, repository, relicID, "section")[0]
	return repository, task, relicID, partID
}

func TestRelicUpdateLinkAndUnlinkJSON(t *testing.T) {
	t.Parallel()
	repository, task, relicID, partID := linkedRelicRepository(t)
	before := relicCommitCount(t, repository, relicID)
	linked := mustUpdateRelic(t, repository, relicID,
		"--link", strings.ToLower(task.ID[:len(task.ID)-4]), "--from", partID[:len(partID)-4], "--rel", "implemented-by")
	if !linked.Changed || relicCommitCount(t, repository, relicID) != before+1 {
		t.Fatalf("relic update --link = %+v, want one commit", linked)
	}
	shown := shownRelic(t, repository, relicID)
	if len(shown.Links) != 1 {
		t.Fatalf("relic show links = %+v, want one", shown.Links)
	}
	link := shown.Links[0]
	if link.From != partID || link.To.Task != task.ID || link.Rel != "implemented-by" || link.TargetRemoved || link.ID == "" {
		t.Fatalf("relic show link = %+v", link)
	}

	again := mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by")
	if again.Changed {
		t.Fatalf("the same --link again = %+v, want no change", again)
	}

	unlinked := mustUpdateRelic(t, repository, relicID, "--unlink", strings.ToLower(link.ID[:len(link.ID)-2]))
	if !unlinked.Changed {
		t.Fatalf("relic update --unlink = %+v, want a change", unlinked)
	}
	if shown := shownRelic(t, repository, relicID); len(shown.Links) != 0 {
		t.Fatalf("relic show links after --unlink = %+v, want none", shown.Links)
	}
	if again := mustUpdateRelic(t, repository, relicID, "--unlink", link.ID); again.Changed {
		t.Fatalf("the same --unlink again = %+v, want no change", again)
	}
	if message := relicErrorMessage(t, repository, 4, core.CategoryNotFound, "update", relicID, "--unlink", link.ID[:len(link.ID)-2]); !strings.Contains(message, "has no link") {
		t.Fatalf("--unlink of a removed link's prefix message = %q", message)
	}
	if message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", relicID, "--link", relicID, "--rel", "relates-to"); !strings.Contains(message, "a link cannot point at what it starts from") {
		t.Fatalf("--link <relic> message = %q", message)
	}
	assertRelicHistoriesValid(t, repository, 1)
}

func TestRelicUpdateLinkInvocationErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{"--from before any --link", []string{"WB-R-X", "--from", "P", "--link", "T", "--rel", "r"}, []string{"--from", "--link"}},
		{"--rel before any --link", []string{"WB-R-X", "--rel", "r", "--link", "T"}, []string{"--rel", "--link"}},
		{"--link without --rel", []string{"WB-R-X", "--link", "T"}, []string{"--link requires --rel"}},
		{"--link without --rel before another", []string{"WB-R-X", "--link", "T", "--link", "U", "--rel", "r"}, []string{"--link requires --rel"}},
		{"--rel twice", []string{"WB-R-X", "--link", "T", "--rel", "r", "--rel", "s"}, []string{"--rel once"}},
		{"--from twice", []string{"WB-R-X", "--link", "T", "--from", "P", "--from", "Q", "--rel", "r"}, []string{"--from once"}},
		{"--unlink with --rel", []string{"WB-R-X", "--unlink", "L", "--rel", "r"}, []string{"--rel", "--unlink"}},
		{"--from after --add-part", []string{"WB-R-X", "--add-part", "--part-type", "s", "--from", "P"}, []string{"--from", "--link"}},
		{"--link without a value", []string{"WB-R-X", "--link"}, []string{"--link requires a value"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			readFile, reads := relicBodyFiles(nil)
			_, err := parseRelicUpdateArgs(test.args, readFile)
			if core.CategoryOf(err) != core.CategoryInvocation {
				t.Fatalf("parseRelicUpdateArgs(%q) error = %v, want an invocation error", test.args, err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("parseRelicUpdateArgs(%q) error = %q, want it to hold %q", test.args, err, want)
				}
			}
			if *reads != 0 {
				t.Errorf("parseRelicUpdateArgs(%q) read %d files", test.args, *reads)
			}
		})
	}

	request, err := parseRelicUpdateArgs([]string{"WB-R-X", "--link", "WB-01", "--rel", "implemented-by", "--from", "P",
		"--unlink", "L", "--link", "WB-02", "--rel", "relates-to"}, nil)
	if err != nil {
		t.Fatalf("parseRelicUpdateArgs: %v", err)
	}
	want := []core.RelicEdit{
		{Kind: core.RelicEditAddLink, Target: "WB-01", From: "P", Rel: "implemented-by"},
		{Kind: core.RelicEditRemoveLink, Link: "L"},
		{Kind: core.RelicEditAddLink, Target: "WB-02", Rel: "relates-to"},
	}
	if got := mustJSON(t, request.Edits); got != mustJSON(t, want) {
		t.Fatalf("edits = %s, want %s", got, mustJSON(t, want))
	}

	// Through the command, exit 2 with nothing read or written.
	repository, task, relicID, partID := linkedRelicRepository(t)
	head := relicHeadOf(t, repository, relicID)
	for _, args := range [][]string{
		{"update", relicID, "--from", partID, "--link", task.ID, "--rel", "relates-to"},
		{"update", relicID, "--link", task.ID},
		{"update", relicID, "--link", task.ID, "--rel", "relates-to", "--rel", "relates-to"},
		{"update", relicID, "--unlink", "L", "--rel", "relates-to"},
	} {
		relicErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	if after := relicHeadOf(t, repository, relicID); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
}

func TestRelicShowTextListsLinks(t *testing.T) {
	t.Parallel()
	repository, task, relicID, partID := linkedRelicRepository(t)
	other := cliCreateTask(t, repository, "Write the docs")
	updated := mustUpdateRelic(t, repository, relicID,
		"--link", task.ID, "--from", partID, "--rel", "implemented-by",
		"--link", other.ID, "--rel", "relates-to")
	shown := shownRelic(t, repository, relicID)
	if len(shown.Links) != 2 || !updated.Changed {
		t.Fatalf("relic show links = %+v", shown.Links)
	}
	byTask := map[string]string{}
	for _, link := range shown.Links {
		byTask[link.To.Task] = link.ID
	}

	showText := func() string {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "show", relicID)
		if code != 0 || stderr != "" {
			t.Fatalf("relic show = (%d, %q, %q)", code, stdout, stderr)
		}
		return stdout
	}
	lines := map[string]string{
		task.ID:  "\t" + byTask[task.ID] + "\t" + partID + "\timplemented-by\t" + task.ID + "\n",
		other.ID: "\t" + byTask[other.ID] + "\trelic\trelates-to\t" + other.ID + "\n",
	}
	text := showText()
	block := "Links:\n"
	for _, link := range shown.Links {
		block += lines[link.To.Task]
	}
	if !strings.HasSuffix(text, block) {
		t.Fatalf("relic show text = %q, want it to end with %q", text, block)
	}

	if code, stdout, stderr := run(t, repository, "delete", other.ID, "--no-sync"); code != 0 {
		t.Fatalf("delete = (%d, %q, %q)", code, stdout, stderr)
	}
	removed := strings.TrimSuffix(lines[other.ID], "\n") + " (removed)\n"
	if text := showText(); !strings.Contains(text, removed) || !strings.Contains(text, lines[task.ID]) {
		t.Fatalf("relic show text after delete = %q, want %q", text, removed)
	}
	if shown := shownRelic(t, repository, relicID); !linkRemovedFor(shown, other.ID) || linkRemovedFor(shown, task.ID) {
		t.Fatalf("relic show --json links after delete = %+v", shown.Links)
	}
	var exported core.RelicDocument
	mustRelic(t, repository, &exported, "export", relicID)
	if len(exported.Links) != 2 || linkRemovedFor(exported, other.ID) {
		t.Fatalf("relic export links = %+v, want both as stored", exported.Links)
	}
}

func linkRemovedFor(document core.RelicDocument, taskID string) bool {
	for _, link := range document.Links {
		if link.To.Task == taskID {
			return link.TargetRemoved
		}
	}
	return false
}

func TestRelicCreateFromFileRefusesLinks(t *testing.T) {
	t.Parallel()
	repository, task, relicID, _ := linkedRelicRepository(t)
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--rel", "relates-to")
	code, exported, stderr := run(t, repository, "relic", "export", relicID)
	if code != 0 || stderr != "" {
		t.Fatalf("relic export = (%d, %q, %q)", code, exported, stderr)
	}
	code, stdout, stderr := runWithInput(t, repository, strings.NewReader(exported), "relic", "create", "--from-file", "-", "--json")
	if code != 5 || stdout != "" {
		t.Fatalf("relic create --from-file <linked export> = (%d, %q, %q), want exit 5", code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "")
	for _, want := range []string{"cannot create a relic with links", "relic update", "--link"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("relic create --from-file refusal %q does not hold %q", stderr, want)
		}
	}
}

// With a cache SQLite cannot open, relic show still lists the links and marks
// a deleted task, reading the tasks from Git.
func TestRelicShowOfALinkedRelicAnswersWithNoUsableCache(t *testing.T) {
	t.Parallel()
	repository, task, relicID, partID := linkedRelicRepository(t)
	other := cliCreateTask(t, repository, "Write the docs")
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by",
		"--link", other.ID, "--rel", "relates-to")
	if code, stdout, stderr := run(t, repository, "delete", other.ID, "--no-sync"); code != 0 {
		t.Fatalf("delete = (%d, %q, %q)", code, stdout, stderr)
	}
	show := func() string {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "show", relicID, "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("relic show = (%d, %q, %q)", code, stdout, stderr)
		}
		return stdout
	}
	want := show()
	cache := relicCachePath(t, repository)
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := show(); got != want {
		t.Fatalf("relic show with no usable cache = %s, want %s", got, want)
	}
	if !strings.Contains(want, `"targetRemoved":true`) {
		t.Fatalf("relic show = %s, want the deleted task's link marked", want)
	}
}

// A damaged task tip leaves the tasks unreadable. relic show of a linked
// relic, whole or one part, as text or JSON, still succeeds: it lists the
// links unmarked and says why, in the envelope or on stderr. relic export is
// unchanged and warns on stderr.
func TestRelicShowOfALinkedRelicWarnsWhenTheTasksCannotBeRead(t *testing.T) {
	t.Parallel()
	repository, task, relicID, partID := linkedRelicRepository(t)
	other := cliCreateTask(t, repository, "Write the docs")
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by",
		"--link", other.ID, "--rel", "relates-to")
	if code, stdout, stderr := run(t, repository, "delete", other.ID, "--no-sync"); code != 0 {
		t.Fatalf("delete = (%d, %q, %q)", code, stdout, stderr)
	}
	addresses := []string{relicID, relicID + "#" + partID}
	showJSON := func(address string) (resultDocument, core.RelicDocument, string) {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "show", address, "--json")
		if code != 0 {
			t.Fatalf("relic show %s --json = (%d, %q, %q)", address, code, stdout, stderr)
		}
		result := assertJSONResult(t, stdout, "relic show")
		var document core.RelicDocument
		if err := json.Unmarshal(result.Data, &document); err != nil {
			t.Fatal(err)
		}
		return result, document, stderr
	}
	showText := func(address string) (string, string) {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "show", address)
		if code != 0 {
			t.Fatalf("relic show %s = (%d, %q, %q)", address, code, stdout, stderr)
		}
		return stdout, stderr
	}
	exported := func() (string, string) {
		t.Helper()
		code, stdout, stderr := run(t, repository, "relic", "export", relicID)
		if code != 0 {
			t.Fatalf("relic export = (%d, %q, %q)", code, stdout, stderr)
		}
		return stdout, stderr
	}

	healthyText := map[string]string{}
	for _, address := range addresses {
		result, document, stderr := showJSON(address)
		if stderr != "" || len(result.Warnings) != 0 {
			t.Fatalf("relic show %s --json with healthy tasks warned: %q, %+v", address, stderr, result.Warnings)
		}
		if address == relicID && !linkRemovedFor(document, other.ID) {
			t.Fatalf("relic show %s links = %+v, want the deleted task's link marked", address, document.Links)
		}
		text, stderr := showText(address)
		if stderr != "" {
			t.Fatalf("relic show %s with healthy tasks warned: %q", address, stderr)
		}
		healthyText[address] = text
	}
	healthyExport, stderr := exported()
	if stderr != "" {
		t.Fatalf("relic export with healthy tasks warned: %q", stderr)
	}

	cliCreateTask(t, repository, "Damaged task")
	var damagedRef string
	for _, ref := range strings.Fields(gitOutput(t, repository, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/")) {
		if !strings.HasSuffix(ref, task.ID) && !strings.HasSuffix(ref, other.ID) {
			damagedRef = ref
		}
	}
	blob := cliGitOutputInput(t, repository, "not a commit\n", "hash-object", "-w", "--stdin")
	cliGit(t, repository, "update-ref", damagedRef, blob)
	cache := relicCachePath(t, repository)
	if err := os.Remove(cache); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if code, stdout, _ := run(t, repository, "list", "--json"); code == 0 {
		t.Fatalf("list succeeded with a damaged task tip: %q", stdout)
	}

	for _, address := range addresses {
		result, document, stderr := showJSON(address)
		if stderr != "" {
			t.Fatalf("relic show %s --json stderr = %q, want the warning in the envelope", address, stderr)
		}
		if len(result.Warnings) != 1 || result.Warnings[0].Code != core.WarningRelicLinkTargetsUnread ||
			!strings.Contains(result.Warnings[0].Message, "links are not checked") {
			t.Fatalf("relic show %s --json warnings = %+v, want one %s", address, result.Warnings, core.WarningRelicLinkTargetsUnread)
		}
		if len(document.Links) == 0 {
			t.Fatalf("relic show %s listed no links", address)
		}
		for _, link := range document.Links {
			if link.TargetRemoved {
				t.Fatalf("relic show %s marked link %s with the tasks unread", address, link.ID)
			}
		}
		text, stderr := showText(address)
		if !strings.HasPrefix(stderr, "workbook: warning: the tasks cannot be read") || strings.Count(stderr, "\n") != 1 {
			t.Fatalf("relic show %s stderr = %q, want one warning", address, stderr)
		}
		if want := strings.ReplaceAll(healthyText[address], " (removed)", ""); text != want {
			t.Fatalf("relic show %s text = %q, want %q", address, text, want)
		}
	}
	export, stderr := exported()
	if export != healthyExport || stderr != "" {
		t.Fatalf("relic export with the tasks unread = (%q, %q), want (%q, \"\")", export, stderr, healthyExport)
	}
}

func TestRelicUpdateRelicLinkJSON(t *testing.T) {
	t.Parallel()
	repository, _, y, from := linkedRelicRepository(t)
	x := mustCreateRelic(t, repository, "Transcript", "--type", "transcript", "--no-sync").Relic.ID
	turn := addRelicParts(t, repository, x, "turn")[0]
	xHead := relicHeadOf(t, repository, x)
	updated := mustUpdateRelic(t, repository, y, "--link", x+"#"+strings.ToLower(turn[:len(turn)-2]),
		"--from", from[:len(from)-4], "--rel", "decided-in", "--pointer", `{"quote": "<q>", "at":"00:14:32"}`)
	if !updated.Changed {
		t.Fatalf("relic update --link <relic>#<part> = %+v, want a change", updated)
	}
	if relicHeadOf(t, repository, x) != xHead {
		t.Fatal("linking to X moved X's ref")
	}
	shown := shownRelic(t, repository, y)
	if len(shown.Links) != 1 {
		t.Fatalf("relic show links = %+v", shown.Links)
	}
	link := shown.Links[0]
	seen := relicHistoryOf(t, repository, x, "--limit", "1").Changes[0].Operations
	if link.From != from || link.To != (core.RelicLinkTarget{Relic: x, Part: turn}) || link.Rel != "decided-in" ||
		link.Seen != seen[len(seen)-1].ID || string(link.Pointer) != `{"at":"00:14:32","quote":"\u003cq\u003e"}` ||
		link.TargetRemoved || link.TargetState != "" {
		t.Fatalf("relic show link = %+v", link)
	}
	if message := relicErrorMessage(t, repository, 5, core.CategoryValidation, "update", y, "--link", x, "--rel", "relates-to",
		"--pointer", `[1]`); !strings.Contains(message, "a link pointer must be a JSON object") {
		t.Fatalf("--pointer [1] message = %q", message)
	}
}

func TestRelicUpdatePointerInvocationErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{"--pointer before any --link", []string{"WB-R-X", "--pointer", "{}", "--link", "T", "--rel", "r"}, []string{"--pointer", "--link"}},
		{"--pointer twice", []string{"WB-R-X", "--link", "T", "--rel", "r", "--pointer", "{}", "--pointer", "{}"}, []string{"--pointer once"}},
		{"--pointer with --unlink", []string{"WB-R-X", "--unlink", "L", "--pointer", "{}"}, []string{"--pointer", "--unlink"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			readFile, reads := relicBodyFiles(nil)
			_, err := parseRelicUpdateArgs(test.args, readFile)
			if core.CategoryOf(err) != core.CategoryInvocation {
				t.Fatalf("parseRelicUpdateArgs(%q) error = %v, want an invocation error", test.args, err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("parseRelicUpdateArgs(%q) error = %q, want it to hold %q", test.args, err, want)
				}
			}
			if *reads != 0 {
				t.Errorf("parseRelicUpdateArgs(%q) read %d files", test.args, *reads)
			}
		})
	}
	request, err := parseRelicUpdateArgs([]string{"WB-R-X", "--link", "WB-R-Y#P", "--rel", "r", "--pointer", `{"b":1,"a":2}`}, nil)
	if err != nil {
		t.Fatalf("parseRelicUpdateArgs: %v", err)
	}
	want := []core.RelicEdit{{Kind: core.RelicEditAddLink, Target: "WB-R-Y#P", Rel: "r", Pointer: json.RawMessage(`{"a":2,"b":1}`)}}
	if got := mustJSON(t, request.Edits); got != mustJSON(t, want) {
		t.Fatalf("edits = %s, want %s", got, mustJSON(t, want))
	}

	repository, _, relicID, _ := linkedRelicRepository(t)
	head := relicHeadOf(t, repository, relicID)
	for _, args := range [][]string{
		{"update", relicID, "--pointer", "{}", "--link", relicID, "--rel", "relates-to"},
		{"update", relicID, "--link", relicID, "--rel", "relates-to", "--pointer", "{}", "--pointer", "{}"},
		{"update", relicID, "--unlink", "L", "--pointer", "{}"},
	} {
		relicErrorMessage(t, repository, 2, core.CategoryInvocation, args...)
	}
	if after := relicHeadOf(t, repository, relicID); after != head {
		t.Fatalf("a refused update moved the relic from %s to %s", head, after)
	}
}

func TestRelicShowTextListsRelicLinks(t *testing.T) {
	t.Parallel()
	repository, _, y, from := linkedRelicRepository(t)
	x := mustCreateRelic(t, repository, "Transcript", "--type", "transcript", "--no-sync").Relic.ID
	turn := addRelicParts(t, repository, x, "turn")[0]
	gone := mustCreateRelic(t, repository, "Elsewhere", "--type", "note", "--no-sync").Relic.ID
	mustUpdateRelic(t, repository, y, "--link", x+"#"+turn, "--from", from, "--rel", "decided-in", "--pointer", `{"at":"00:14:32"}`,
		"--link", gone, "--rel", "relates-to")
	byTarget := map[string]string{}
	for _, link := range shownRelic(t, repository, y).Links {
		byTarget[link.To.Address()] = link.ID
	}
	// This clone loses the second target, as a clone that never fetched it.
	gitOutput(t, repository, "update-ref", "-d", relicRef(gone))

	code, stdout, stderr := run(t, repository, "relic", "show", y)
	if code != 0 || stderr != "" {
		t.Fatalf("relic show = (%d, %q, %q)", code, stdout, stderr)
	}
	lines := map[string]string{
		x + "#" + turn: "\t" + byTarget[x+"#"+turn] + "\t" + from + "\tdecided-in\t" + x + "#" + turn + "\t" + `{"at":"00:14:32"}` + "\n",
		gone:           "\t" + byTarget[gone] + "\trelic\trelates-to\t" + gone + " (not in this clone)\n",
	}
	for _, line := range lines {
		if !strings.Contains(stdout, line) {
			t.Fatalf("relic show text = %q, want it to hold %q", stdout, line)
		}
	}
	shown := shownRelic(t, repository, y)
	for _, link := range shown.Links {
		if link.To.Relic == gone && (link.TargetState != core.RelicLinkTargetMissing || !link.TargetRemoved) {
			t.Fatalf("the link to a relic not in this clone = %+v", link)
		}
	}
}
