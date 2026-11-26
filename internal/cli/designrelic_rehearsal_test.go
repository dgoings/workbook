package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// TestDesignRelicStepsRehearsal runs the steps the product owner holds for
// storing the relics design as the first relic and linking the stories that
// build it (H1 to H7 of "Held for the product owner" in the story 6 plan), in
// the same order and with the same commands, against a temporary origin and a
// small sample document instead of the real repository and design. H8, which
// deletes the design's HTML copy from the real checkout, has no command to
// rehearse here.
func TestDesignRelicStepsRehearsal(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(filepath.Join("testdata", "design-relic-rehearsal.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := core.DecodeRelicDocument(source)
	if err != nil {
		t.Fatalf("the rehearsal document does not decode: %v", err)
	}
	statusSections := requireDecidedShape(t, document)

	// An origin and clone A, set up as a checkout is, with three tasks
	// standing for the stories, published.
	bare := cliBareOrigin(t)
	seed := initializedRepository(t)
	cliGit(t, seed, "branch", "-M", "main")
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, seed, "remote", "add", "origin", bare)
	cliGit(t, seed, "push", "--quiet", "-u", "origin", "main")
	cliGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	clone := cliClone(t, bare)
	rehearsalStep(t, clone, "setup", "--json")
	const oldNotes = "## Design\n\nSee specs/2026-11-21-relics-design.html."
	stories := make([]string, 3)
	for index, title := range []string{"Schemas for relics", "Relic links", "When two edits meet, part two"} {
		created := rehearsalStep(t, clone, "create", title, "--description", "Build it.\n\n"+oldNotes, "--json")
		var task core.Task
		if err := json.Unmarshal(created.Data, &task); err != nil {
			t.Fatal(err)
		}
		stories[index] = task.ID
	}
	// The held step names design-relic.json in the checkout. Run reads a
	// relative path against the test process's directory, not the cwd it is
	// given, so the rehearsal names the same file by its full path.
	designFile := filepath.Join(clone, "design-relic.json")
	if err := os.WriteFile(designFile, source, 0o600); err != nil {
		t.Fatal(err)
	}

	// H1: create the relic from the document; keep data.relic.id as R.
	created := rehearsalStep(t, clone, "relic", "create", "--from-file", designFile, "--json")
	if created.Sync == nil || created.Sync.Status != "completed" {
		t.Fatalf("H1 sync = %+v, want status completed", created.Sync)
	}
	var mutation core.RelicMutationResult
	if err := json.Unmarshal(created.Data, &mutation); err != nil {
		t.Fatal(err)
	}
	relicID := mutation.Relic.ID

	// H2: show it and map part titles to part IDs.
	var shown core.RelicDocument
	if err := json.Unmarshal(rehearsalStep(t, clone, "relic", "show", relicID, "--json").Data, &shown); err != nil {
		t.Fatal(err)
	}
	if len(shown.Parts) != len(document.Parts) {
		t.Fatalf("H2 relic show has %d parts, want %d", len(shown.Parts), len(document.Parts))
	}
	partByTitle := map[string]string{}
	shownStatuses := 0
	for _, part := range shown.Parts {
		var title string
		if err := json.Unmarshal(part.Fields["title"], &title); err != nil {
			t.Fatalf("H2 part %s title: %v", part.ID, err)
		}
		partByTitle[title] = part.ID
		if _, found := part.Fields["status"]; found && part.Type == "section" {
			shownStatuses++
		}
	}
	if shownStatuses != statusSections {
		t.Fatalf("H2 shows %d sections with a status, want the document's %d", shownStatuses, statusSections)
	}
	// The stories link from section parts; the last two share one, as stories
	// 13 and 14 do.
	links := map[string]string{
		stories[0]: partByTitle["IDs and addresses"],
		stories[1]: partByTitle["When two edits meet"],
		stories[2]: partByTitle["When two edits meet"],
	}

	// H3: one command, one --link group per story.
	update := []string{"relic", "update", relicID}
	for _, story := range stories {
		if links[story] == "" {
			t.Fatalf("H2 found no part for story %s: %v", story, partByTitle)
		}
		update = append(update, "--link", story, "--from", links[story], "--rel", "implemented-by")
	}
	rehearsalStep(t, clone, append(update, "--json")...)
	if err := json.Unmarshal(rehearsalStep(t, clone, "relic", "show", relicID, "--json").Data, &shown); err != nil {
		t.Fatal(err)
	}
	if len(shown.Links) != 3 {
		t.Fatalf("H3 left %d links, want 3: %s", len(shown.Links), mustJSON(t, shown.Links))
	}
	head := shown.Head

	// H4: a sync finds nothing left to publish.
	var synced gitstore.SyncRunResult
	if err := json.Unmarshal(rehearsalStep(t, clone, "sync", "--json").Data, &synced); err != nil {
		t.Fatal(err)
	}
	for _, relic := range synced.Push.Relics {
		t.Errorf("H4 sync pushed relic %s (%s), want nothing left to publish", relic.RelicID, relic.Status)
	}
	for _, task := range synced.Push.Tasks {
		if task.Status != gitstore.SyncUpToDate {
			t.Errorf("H4 sync pushed task %s (%s), want nothing left to publish", task.TaskID, task.Status)
		}
	}

	// H5: a fresh clone, set up, has the relic at the same head, every story
	// lists its link, and the linked part reads back as the document wrote it.
	fresh := cliClone(t, bare)
	rehearsalStep(t, fresh, "setup", "--json")
	var freshShown core.RelicDocument
	if err := json.Unmarshal(rehearsalStep(t, fresh, "relic", "show", relicID, "--json").Data, &freshShown); err != nil {
		t.Fatal(err)
	}
	if freshShown.Head != head {
		t.Fatalf("H5 relic head in the fresh clone = %s, want %s", freshShown.Head, head)
	}
	bodyOf := func(partID string) string {
		for index, part := range shown.Parts {
			if part.ID == partID {
				return *document.Parts[index].Body.Text
			}
		}
		t.Fatalf("no part %s", partID)
		return ""
	}
	for _, story := range stories {
		var detail core.TaskDetail
		if err := json.Unmarshal(rehearsalStep(t, fresh, "show", story, "--json").Data, &detail); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, link := range detail.Relics {
			found = found || (link.Relic == relicID && link.Part == links[story] && link.Rel == "implemented-by")
		}
		if !found {
			t.Errorf("H5 show %s relics = %s, want %s#%s implemented-by", story, mustJSON(t, detail.Relics), relicID, links[story])
		}
		var part core.RelicDocument
		if err := json.Unmarshal(rehearsalStep(t, fresh, "relic", "show", relicID+"#"+links[story], "--body", "--json").Data, &part); err != nil {
			t.Fatal(err)
		}
		if len(part.Parts) != 1 || part.Parts[0].Body == nil || part.Parts[0].Body.Text == nil || *part.Parts[0].Body.Text != bodyOf(links[story]) {
			t.Errorf("H5 relic show %s#%s --body = %s, want the document's body", relicID, links[story], mustJSON(t, part.Parts))
		}
	}

	// H6: the fresh clone's export carries every body as the document has it.
	var exported core.RelicDocument
	if err := json.Unmarshal(rehearsalStep(t, fresh, "relic", "export", relicID, "--json").Data, &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.Parts) != len(document.Parts) {
		t.Fatalf("H6 export has %d parts, want %d", len(exported.Parts), len(document.Parts))
	}
	for index, part := range exported.Parts {
		want := document.Parts[index]
		if part.Type != want.Type || part.Body == nil || part.Body.Text == nil ||
			*part.Body.Text != *want.Body.Text || part.Body.Format != want.Body.Format ||
			mustJSON(t, part.Fields) != mustJSON(t, want.Fields) {
			t.Errorf("H6 export part %d = %s, want %s", index, mustJSON(t, part), mustJSON(t, want))
		}
	}

	// H7: each story's description ends with a section naming the relic, the
	// part and the command that reads it, and the fresh clone fetches it.
	newNotes := map[string]string{}
	for _, story := range stories {
		newNotes[story] = "## Design\n\nThe design is relic " + relicID + ", part " + links[story] +
			". Read it with `workbook relic show " + relicID + "#" + links[story] + " --body`."
		rehearsalStep(t, clone, "update", story, "--description", "Build it.\n\n"+newNotes[story], "--json")
	}
	rehearsalStep(t, fresh, "fetch", "--json")
	for _, story := range stories {
		var task core.Task
		if err := json.Unmarshal(rehearsalStep(t, fresh, "show", story, "--json").Data, &task); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(task.Description, newNotes[story]) {
			t.Errorf("H7 description of %s in the fresh clone = %q, want it to end with %q", story, task.Description, newNotes[story])
		}
	}
}

// requireDecidedShape checks that the sample has the shape the design relic
// was decided to take: a heading tagged Decided or Proposed is a section part
// whose fields carry status decided or proposed, and it makes no decision part
// of its own. A decision part stands for one tagged list item, so it never
// shares a section's title or repeats a section's body. It returns how many
// sections carry a status, at least one.
func requireDecidedShape(t *testing.T, document core.RelicDocument) int {
	t.Helper()
	fieldString := func(part core.RelicDocumentPart, name string) (string, bool) {
		raw, found := part.Fields[name]
		if !found {
			return "", false
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("part %s field %s = %s, want a string", part.Type, name, raw)
		}
		return value, true
	}
	bodyText := func(part core.RelicDocumentPart) string {
		if part.Body == nil || part.Body.Text == nil {
			return ""
		}
		return *part.Body.Text
	}
	sections := map[string]core.RelicDocumentPart{}
	statuses := 0
	for _, part := range document.Parts {
		if part.Type != "section" {
			continue
		}
		title, _ := fieldString(part, "title")
		sections[title] = part
		if status, found := fieldString(part, "status"); found {
			if status != "decided" && status != "proposed" {
				t.Fatalf("section %q status = %q, want decided or proposed", title, status)
			}
			statuses++
		}
	}
	if statuses == 0 {
		t.Fatal("no section part carries a status field; the decided shape tags sections, not decision parts")
	}
	for _, part := range document.Parts {
		if part.Type != "decision" {
			continue
		}
		title, _ := fieldString(part, "title")
		if _, clash := sections[title]; clash {
			t.Fatalf("decision %q repeats a section's title", title)
		}
		name, _ := fieldString(part, "section")
		section, found := sections[name]
		if !found {
			t.Fatalf("decision %q names section %q, which the document lacks", title, name)
		}
		if text := bodyText(part); text == "" || text == bodyText(section) {
			t.Fatalf("decision %q body repeats its section %q or is empty", title, name)
		}
	}
	return statuses
}

// rehearsalResult is a command's JSON result with its sync report.
type rehearsalResult struct {
	Data json.RawMessage `json:"data"`
	Sync *struct {
		Status string `json:"status"`
	} `json:"sync"`
}

// rehearsalStep runs one command of the rehearsal, which must exit 0 with
// nothing on standard error, and decodes its JSON result.
func rehearsalStep(t *testing.T, repository string, args ...string) rehearsalResult {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%s = (%d, %q, %q)", strings.Join(args, " "), code, stdout, stderr)
	}
	var result rehearsalResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("%s output is not JSON: %v; %q", strings.Join(args, " "), err, stdout)
	}
	return result
}
