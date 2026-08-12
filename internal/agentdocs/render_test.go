package agentdocs

import (
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func testProject() core.ProjectConfig {
	return core.ProjectConfig{
		Format:    "workbook.project",
		Version:   1,
		ProjectID: "01KY8964C8TQVBKVACB45DYTNY",
		Key:       "WB",
	}
}

// defaultVocabularySections is the rendering the built-in six statuses produce,
// pinned byte for byte.
//
// It exists so that making the guidelines vocabulary-driven is reviewable: the
// statuses and the lifecycle prose are now computed from tags rather than
// written out, and the only way to see what that changed for the project every
// reader already has is to read the two sections side by side. A deliberate
// wording change edits this constant in the same commit; an accidental one
// fails here.
const defaultVocabularySections = `## Statuses

This project's statuses, in order. Pass the machine value, never the display
label.

| # | Machine value | Display label | Tags |
| --- | --- | --- | --- |
| 1 | ` + "`backlog`" + ` | Backlog | ` + "`default`" + ` |
| 2 | ` + "`ready`" + ` | Ready | ` + "`next`" + ` |
| 3 | ` + "`blocked`" + ` | Blocked | none |
| 4 | ` + "`in-progress`" + ` | In Progress | none |
| 5 | ` + "`in-review`" + ` | In Review | none |
| 6 | ` + "`done`" + ` | Done | ` + "`done`" + ` |

Write ` + "`--status in-progress`" + `, not ` + "`In Progress`" + `.
The same applies to ` + "`in-review`" + `.
A display label is rejected as a validation error.

A tag is a job the machine gives a status, not a description of its name:

| Tag | What it makes Workbook do |
| --- | --- |
| ` + "`default`" + ` | A task created without ` + "`--status`" + ` lands here. Exactly one status carries it. |
| ` + "`done`" + ` | A dependency sitting here is satisfied, so the work waiting on it can be claimed. |
| ` + "`next`" + ` | ` + "`workbook next`" + ` may return a task sitting here. |

A status carrying no tag is an ordinary column: work rests there and nothing
else follows from it.

These statuses belong to this project and another project's are different, so
read them here or with ` + "`workbook status list --json`" + ` rather than assuming the
ones you have seen elsewhere. This section is rewritten whenever they change.
`

const defaultLifecycleSection = `## Task lifecycle

New tasks land in ` + "`backlog`" + `.
` + "`workbook next`" + ` claims from ` + "`ready`" + `.
A dependency is satisfied once it reaches ` + "`done`" + `.

1. Select work with ` + "`workbook next --json`" + `, or read a known task with
   ` + "`workbook show <id> --json`" + `. Keep the canonical full ID from ` + "`data.id`" + `.
2. Claim it with ` + "`workbook update <id> --status <status> --json`" + ` before
   editing files, naming the status this project uses for work under way.
3. Move it along the statuses above as the work progresses, including the
   one this project uses for review, and into a status tagged ` + "`done`" + ` only
   after the work is accepted and merged.
`

// section returns one "## " heading's block, so a pinned section fails on its
// own content rather than on an unrelated edit elsewhere in the document.
func section(t *testing.T, guidelines, heading string) string {
	t.Helper()
	start := strings.Index(guidelines, heading+"\n")
	if start < 0 {
		t.Fatalf("guidelines have no %q section:\n%s", heading, guidelines)
	}
	rest := guidelines[start+len(heading)+1:]
	end := strings.Index(rest, "\n## ")
	if end < 0 {
		t.Fatalf("section %q is not terminated:\n%s", heading, guidelines)
	}
	return heading + "\n" + strings.TrimRight(rest[:end], "\n") + "\n"
}

func TestRenderGuidelinesPinsTheDefaultVocabularyRendering(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), core.DefaultVocabulary())

	if got := section(t, guidelines, "## Statuses"); got != defaultVocabularySections {
		t.Errorf("statuses section =\n%s\nwant\n%s", got, defaultVocabularySections)
	}
	if got := section(t, guidelines, "## Task lifecycle"); got != defaultLifecycleSection {
		t.Errorf("lifecycle section =\n%s\nwant\n%s", got, defaultLifecycleSection)
	}
	// A caller that configured no vocabulary documents the same six statuses,
	// because that is what such a project is using.
	if unconfigured := RenderGuidelines(testProject(), core.Vocabulary{}); unconfigured != guidelines {
		t.Errorf("the zero vocabulary renders differently from the built-in default:\n%s", unconfigured)
	}
}

// A project that renamed its columns gets its own table, its own legend, and
// its own lifecycle prose. Every value here is one the built-in vocabulary does
// not contain, so a rendering that fell back to the default fails.
func TestRenderGuidelinesRendersACustomVocabulary(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), customVocabulary(t))

	for _, want := range []string{
		"| 1 | `icebox` | Icebox | none |",
		"| 2 | `sorting` | Sorting | `default` |",
		"| 3 | `todo` | Next Up | `next` |",
		"| 4 | `doing` | Doing | none |",
		"| 5 | `shipped` | Shipped | `done` |",
		"| 6 | `cancelled` | Cancelled | `done` |",
		"Write `--status todo`, not `Next Up`.",
		"New tasks land in `sorting`.",
		"`workbook next` claims from `todo`.",
		"A dependency is satisfied once it reaches `shipped` or `cancelled`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q:\n%s", want, guidelines)
		}
	}
	for _, unwanted := range []string{"backlog", "in-progress", "in-review"} {
		if strings.Contains(guidelines, unwanted) {
			t.Errorf("guidelines still document the built-in %q:\n%s", unwanted, guidelines)
		}
	}
}

// A folded configuration can arrive from a peer with no default and nothing
// claimable — two clones each removing a tag is enough — and guidelines that
// claimed otherwise would send an agent looking for a column that is not there.
func TestRenderGuidelinesSaysWhenATagIsUnheld(t *testing.T) {
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{
		{Status: "todo", Label: "todo", Rank: "1/1", Tags: []core.StatusTag{}},
		{Status: "shipped", Label: "shipped", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}

	guidelines := RenderGuidelines(testProject(), vocabulary)

	for _, want := range []string{
		"No status is tagged `default`, so a new task has nowhere to land.",
		"No status is tagged `next`, so `workbook next` never returns a task.",
		"A dependency is satisfied once it reaches `shipped`.",
	} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q:\n%s", want, guidelines)
		}
	}
	// Labels that are their own tokens invite no display-label mistake, so no
	// example is invented for one.
	if strings.Contains(guidelines, "Write `--status") {
		t.Errorf("guidelines warn about display labels this project does not have:\n%s", guidelines)
	}
}

// A display label is written by whoever can push to the configuration ref, so
// it is untrusted text landing in a Markdown table. A pipe would add a column
// and shift every value in the row; a newline would end the row early and take
// the rest of the table with it.
func TestRenderGuidelinesKeepsAHostileLabelInsideItsCell(t *testing.T) {
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{
		{Status: "todo", Label: "Next | Up\nand more", Rank: "1/1", Tags: []core.StatusTag{
			core.StatusTagDefault, core.StatusTagNext,
		}},
		{Status: "done", Label: "Done", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}

	guidelines := RenderGuidelines(testProject(), vocabulary)

	if !strings.Contains(guidelines, "| 1 | `todo` | Next \\| Up and more | `default`, `next` |") {
		t.Fatalf("the label escaped its cell:\n%s", guidelines)
	}
	// A status row opens with its position, and its four columns are five
	// unescaped pipes. An escaped pipe is content rather than a boundary.
	rows := 0
	for _, line := range strings.Split(section(t, guidelines, "## Statuses"), "\n") {
		if !strings.HasPrefix(line, "| 1 |") && !strings.HasPrefix(line, "| 2 |") {
			continue
		}
		rows++
		if strings.Count(line, "|")-strings.Count(line, "\\|") != 5 {
			t.Errorf("status table row has the wrong number of cells: %q", line)
		}
	}
	if rows != 2 {
		t.Errorf("checked %d status rows, want both", rows)
	}
}

func customVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{
		{Status: "icebox", Label: "Icebox", Rank: "1/1", Tags: []core.StatusTag{}},
		{Status: "sorting", Label: "Sorting", Rank: "2/1", Tags: []core.StatusTag{core.StatusTagDefault}},
		{Status: "todo", Label: "Next Up", Rank: "3/1", Tags: []core.StatusTag{core.StatusTagNext}},
		{Status: "doing", Label: "Doing", Rank: "4/1", Tags: []core.StatusTag{}},
		{Status: "shipped", Label: "Shipped", Rank: "5/1", Tags: []core.StatusTag{core.StatusTagDone}},
		{Status: "cancelled", Label: "Cancelled", Rank: "6/1", Tags: []core.StatusTag{core.StatusTagDone}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

func TestRenderGuidelinesStatesEveryCanonicalStatus(t *testing.T) {
	// Production mutation: hardcoding a status list here instead of deriving it
	// from core would let generated documentation drift from CLI validation.
	guidelines := RenderGuidelines(testProject(), core.Vocabulary{})

	for _, definition := range core.DefaultVocabulary().Definitions() {
		if !strings.Contains(guidelines, string(definition.Status)) {
			t.Errorf("guidelines missing status %q:\n%s", definition.Status, guidelines)
		}
	}
	for _, definition := range core.Priorities() {
		if !strings.Contains(guidelines, string(definition.Priority)) {
			t.Errorf("guidelines missing priority %q:\n%s", definition.Priority, guidelines)
		}
	}
}

func TestRenderGuidelinesWarnsAgainstDisplayLabels(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), core.Vocabulary{})

	if !strings.Contains(guidelines, "in-progress") {
		t.Errorf("guidelines missing the canonical in-progress value:\n%s", guidelines)
	}
	if !strings.Contains(guidelines, "not `In Progress`") {
		t.Errorf("guidelines do not warn against the In Progress display label:\n%s", guidelines)
	}
}

func TestRenderGuidelinesIncludesProjectIdentity(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), core.Vocabulary{})

	for _, want := range []string{"01KY8964C8TQVBKVACB45DYTNY", "WB-"} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q:\n%s", want, guidelines)
		}
	}
}

func TestRenderGuidelinesDocumentsExitCodesFromCore(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), core.Vocabulary{})

	for _, category := range []core.Category{
		core.CategoryInvocation,
		core.CategoryNotInitialized,
		core.CategoryNotFound,
		core.CategoryValidation,
	} {
		if !strings.Contains(guidelines, string(category)) {
			t.Errorf("guidelines missing error category %q:\n%s", category, guidelines)
		}
	}
}

func TestRenderGuidelinesNamesTheRefreshCommand(t *testing.T) {
	guidelines := RenderGuidelines(testProject(), core.Vocabulary{})

	if !strings.Contains(guidelines, "workbook docs update") {
		t.Errorf("guidelines do not name the refresh command:\n%s", guidelines)
	}
}

func TestRenderReferencePointsAtTheGuidelines(t *testing.T) {
	reference := RenderReference()

	if !strings.Contains(reference, GuidelinesPath) {
		t.Errorf("reference block does not point at %q:\n%s", GuidelinesPath, reference)
	}
}

func TestSkillDocumentSeparatesFrontmatterFromTheManagedBody(t *testing.T) {
	// Production mutation: wrapping YAML frontmatter inside the markers would
	// move it off line one, where skill discovery requires it.
	document, err := skillDocument("0.2.0")
	if err != nil {
		t.Fatalf("skillDocument() error = %v", err)
	}
	if !strings.HasPrefix(document.Preamble, "---\n") {
		t.Fatalf("skill preamble does not start with frontmatter: %q", document.Preamble)
	}
	if !strings.HasSuffix(document.Preamble, "---\n") {
		t.Fatalf("skill preamble does not end with frontmatter: %q", document.Preamble)
	}
	if strings.Contains(document.Body, "---\nname:") {
		t.Fatalf("skill body contains frontmatter:\n%s", document.Body)
	}
	if !strings.Contains(document.Body, "workbook") {
		t.Fatalf("skill body is empty of guidance:\n%s", document.Body)
	}
}

func TestSkillDocumentSeparatesMachineIDsFromHumanTitles(t *testing.T) {
	// Production mutation: dropping either half of this guidance would let
	// agents run CLI commands against ambiguous prefixes, or flood
	// human-facing prose with ULIDs no human can parse or remember.
	document, err := skillDocument("0.2.0")
	if err != nil {
		t.Fatalf("skillDocument() error = %v", err)
	}

	for _, want := range []string{
		// The machine interface: every CLI invocation uses the full ID.
		"resolved full ID for every",
		// The human interface: prose leads with the title.
		"titles are for humans",
		"progress reports, completion summaries, questions, and error",
		// The surfaces where IDs leak into prose.
		"Announce a selected task by title",
		"lifecycle transitions",
		"dependencies and blockers by the titles",
		// The exception that keeps IDs useful to humans.
		"similarly titled",
	} {
		if !strings.Contains(document.Body, want) {
			t.Errorf("skill body missing %q:\n%s", want, document.Body)
		}
	}
}

func TestSkillDocumentResolvesDependencyTitlesThroughTheCLI(t *testing.T) {
	// Production mutation: telling agents to name blockers by title without
	// telling them where a title comes from, or without saying that bad news
	// is announced by title like everything else. The show envelope carries
	// dependencies as bare IDs. In the run recorded under
	// docs/superpowers/evidence/2026-08-08-skill-titles-over-ids-behavior.md an
	// agent reading the shipped skill resolved the dependency on its own
	// initiative and still led with both ULIDs; one that had skipped that
	// unprompted second read would have had no title to lead with at all.
	document, err := skillDocument("0.2.0")
	if err != nil {
		t.Fatalf("skillDocument() error = %v", err)
	}

	for _, want := range []string{
		// The field that holds only IDs.
		"`data.dependencies`",
		// The command that turns one of those IDs into a title.
		"`workbook show <dependency-id> --json`",
		// The member of that second envelope the title comes from.
		"`data.title`",
		// The failure mode the resolution step exists to prevent.
		"never invent one",
		// A dependency this clone cannot read does not abort the task.
		"keep working rather than stopping",
		// Bad news is announced by title too: the scenario that failed
		// against the shipped skill. Do not weaken this to "blocked",
		// which the lifecycle section already contains.
		"Bad news is not an exception",
		"is blocked by",
		// The same rule restated where agents look for it.
		"before naming the blocker",
	} {
		if !strings.Contains(document.Body, want) {
			t.Errorf("skill body missing %q:\n%s", want, document.Body)
		}
	}
}

func TestSkillDocumentRendersFrontmatterOnLineOne(t *testing.T) {
	document, err := skillDocument("0.2.0")
	if err != nil {
		t.Fatalf("skillDocument() error = %v", err)
	}

	contents := string(document.Reconcile(nil).Contents)

	if !strings.HasPrefix(contents, "---\n") {
		t.Fatalf("rendered skill does not begin with frontmatter:\n%s", contents)
	}
}
