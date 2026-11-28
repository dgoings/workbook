package webui

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

// liftedMarkdownRenderer is markdown.js as the page carries it, with the two
// text functions of preferences.js it calls, so a test can hand it a context
// no page surface passes yet. Nothing else of the page runs: the renderer
// needs no state of the board's, and an attachment's media types are asked of
// an empty set because a relic has no attachments.
func liftedMarkdownRenderer(t *testing.T) string {
	t.Helper()
	read := func(name string) string {
		content, err := fs.ReadFile(assets, "assets/script/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	preferences := read("preferences.js")
	function := func(name string) string {
		start := strings.Index(preferences, "      function "+name+"(")
		if start < 0 {
			t.Fatalf("preferences.js has no function %s", name)
		}
		end := strings.Index(preferences[start:], "\n      }\n")
		if end < 0 {
			t.Fatalf("preferences.js never closes function %s", name)
		}
		return preferences[start : start+end+len("\n      }\n")]
	}
	return "\nconst inlineImageMedia = new Set();\nfunction shownFileName(name) { return String(name); }\n" +
		function("withoutDirectionControls") + function("shownLine") + read("markdown.js")
}

// relicMarkdownCase is one source drawn once with a relic's context and once
// with none, and the shape each must have.
type relicMarkdownCase struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// Relic is the shape with tables switched on, as a relic body draws.
	Relic string `json:"relic"`
	// Plain is the shape a task description draws, which is today's.
	Plain string `json:"plain"`
}

// Pipe tables are drawn only where a relic body asks for them. A task's
// description and comments draw exactly what they always have, so every case
// here is also drawn with no context, and markdown_test.go is left as it is.
func TestRelicTablesOnlyInRelicContext(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	cases := []relicMarkdownCase{
		{
			Name:   "a pipe table",
			Source: "| Name | Value |\n| --- | :-: |\n| one | <b>x</b> |\n| two | **bold** |",
			Relic:  `table[thead[tr[th["Name"],th["Value"]]],tbody[tr[td["one"],td["<b>x</b>"]],tr[td["two"],td[strong["bold"]]]]]`,
			Plain:  `p["| Name | Value |",br,"| --- | :-: |",br,"| one | <b>x</b> |",br,"| two | ",strong["bold"]," |"]`,
		},
		{
			Name:   "outer pipes are optional",
			Source: "a | b\n--- | ---\n1 | 2",
			Relic:  `table[thead[tr[th["a"],th["b"]]],tbody[tr[td["1"],td["2"]]]]`,
			Plain:  `p["a | b",br,"--- | ---",br,"1 | 2"]`,
		},
		{
			Name:   "a short row is padded and a long one cut to the header",
			Source: "| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |",
			Relic:  `table[thead[tr[th["a"],th["b"]]],tbody[tr[td["1"],td],tr[td["1"],td["2"]]]]`,
			Plain:  `p["| a | b |",br,"|---|---|",br,"| 1 |",br,"| 1 | 2 | 3 |"]`,
		},
		{
			Name:   "a header with no rows",
			Source: "| a |\n| --- |",
			Relic:  `table[thead[tr[th["a"]]]]`,
			Plain:  `p["| a |",br,"| --- |"]`,
		},
		{
			Name:   "a table ends the paragraph above it and a blank line ends the table",
			Source: "intro\n| a |\n| --- |\n| 1 |\n\nafter",
			Relic:  `p["intro"],table[thead[tr[th["a"]]],tbody[tr[td["1"]]]],p["after"]`,
			Plain:  `p["intro",br,"| a |",br,"| --- |",br,"| 1 |"],p["after"]`,
		},
		{
			Name:   "no delimiter row is no table",
			Source: "| a | b |\n| 1 | 2 |",
			Relic:  `p["| a | b |",br,"| 1 | 2 |"]`,
			Plain:  `p["| a | b |",br,"| 1 | 2 |"]`,
		},
		{
			Name:   "a delimiter row of another width is no table",
			Source: "| a | b |\n| --- |",
			Relic:  `p["| a | b |",br,"| --- |"]`,
			Plain:  `p["| a | b |",br,"| --- |"]`,
		},
		{
			Name:   "a cell holding a script is its text",
			Source: "| <script>alert(1)</script> |\n| --- |\n| <img src=x onerror=alert(1)> |",
			Relic:  `table[thead[tr[th["<script>alert(1)</script>"]]],tbody[tr[td["<img src=x onerror=alert(1)>"]]]]`,
			Plain:  `p["| <script>alert(1)</script> |",br,"| --- |",br,"| <img src=x onerror=alert(1)> |"]`,
		},
		{
			Name: "an image in a relic body is its text",
			// A relic has no attachments, and a board that fetched an image named
			// in a relic would tell whoever wrote it who reads it.
			Source: "![chart](attachment:01K0M6B8A4FTT8C39MXXYTWA01) ![remote](https://example.test/a.png)",
			Relic:  `p["![chart](attachment:01K0M6B8A4FTT8C39MXXYTWA01) ![remote](https://example.test/a.png)"]`,
			Plain:  `p["![chart](attachment:01K0M6B8A4FTT8C39MXXYTWA01) ![remote](https://example.test/a.png)"]`,
		},
	}
	table, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: boardTasks()})
	program := clientDOMHarness("/", string(document)) + liftedMarkdownRenderer(t) + markdownHelpers + `
const relicCases = ` + string(table) + `;
// The tags and attributes a relic body may hold: the markdown subset and the
// table's six, with no attribute on any of them but a link's.
const relicTags = markdownTags.concat(["TABLE", "THEAD", "TBODY", "TR", "TH", "TD"]);
function relicViolations(root) {
  const findings = [];
  elementsUnder(root).forEach((element) => {
    if (!relicTags.includes(element.tagName)) findings.push("built a <" + element.tagName.toLowerCase() + ">");
    Object.keys(element.attributes || {}).forEach((name) => findings.push(element.tagName + " carries " + name));
    Object.keys(element.dataset || {}).forEach((name) => findings.push(element.tagName + " carries data " + name));
    Object.keys(element.style || {}).forEach((name) => findings.push(element.tagName + " carries style " + name));
    if (element.className) findings.push(element.tagName + " carries class " + element.className);
  });
  return findings;
}
setTimeout(() => {
  const failures = [];
  relicCases.forEach((expectation) => {
    for (const [context, want, label] of [[{ tables: true }, expectation.relic, "relic"], [undefined, expectation.plain, "plain"], [{}, expectation.plain, "empty context"]]) {
      const root = renderMarkdown(document.createElement("div"), expectation.source, context);
      const findings = relicViolations(root);
      if (findings.length) failures.push(expectation.name + " (" + label + ") drew " + findings.join("; "));
      const shape = shapeOf(root);
      if (shape !== want) failures.push(expectation.name + " (" + label + ") rendered as " + shape + ", want " + want);
    }
  });
  if (failures.length) throw new Error(failures.join("\n"));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("draw relic tables: %v\n%s", err, output)
	}
}
