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

func TestRenderGuidelinesStatesEveryCanonicalStatus(t *testing.T) {
	// Production mutation: hardcoding a status list here instead of deriving it
	// from core would let generated documentation drift from CLI validation.
	guidelines := RenderGuidelines(testProject())

	for _, definition := range core.WorkflowStatuses() {
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
	guidelines := RenderGuidelines(testProject())

	if !strings.Contains(guidelines, "in-progress") {
		t.Errorf("guidelines missing the canonical in-progress value:\n%s", guidelines)
	}
	if !strings.Contains(guidelines, "not `In Progress`") {
		t.Errorf("guidelines do not warn against the In Progress display label:\n%s", guidelines)
	}
}

func TestRenderGuidelinesIncludesProjectIdentity(t *testing.T) {
	guidelines := RenderGuidelines(testProject())

	for _, want := range []string{"01KY8964C8TQVBKVACB45DYTNY", "WB-"} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines missing %q:\n%s", want, guidelines)
		}
	}
}

func TestRenderGuidelinesDocumentsExitCodesFromCore(t *testing.T) {
	guidelines := RenderGuidelines(testProject())

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
	guidelines := RenderGuidelines(testProject())

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
	// telling them where a title comes from. The show envelope carries
	// dependencies as bare IDs, so an agent with no resolution step either
	// invents a plausible title or falls back to the ULID the same section
	// tells it not to lead with.
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
		// Bad news is still reported by title.
		"blocked",
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
