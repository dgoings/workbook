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
			Name:   "a short row is left short and a long one cut to the header",
			Source: "| a | b |\n|---|---|\n| 1 |\n| 1 | 2 | 3 |",
			Relic:  `table[thead[tr[th["a"],th["b"]]],tbody[tr[td["1"]],tr[td["1"],td["2"]]]]`,
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

// A delimiter row is read in time that grows with its length. A body is a
// megabyte of a teammate's text and is drawn on the page's own thread, so a
// row that took the square of its length to refuse would hang the board for
// everyone who opened the relic.
func TestRelicTableDelimiterRowIsReadInLinearTime(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: boardTasks()})
	program := clientDOMHarness("/", string(document)) + liftedMarkdownRenderer(t) + markdownHelpers + `
setTimeout(() => {
  const run = " ".repeat(1 << 20);
  const sources = {
    "spaces after a dash": "a|b\n-" + run + "x",
    "spaces between pipes": "a|b\n|-" + run + "|" + run + "x",
    "tabs after a colon": "a|b\n:-" + "\t".repeat(1 << 20) + "x",
    "inside a paragraph": "intro\na|b\n-" + run + "x\nafter",
  };
  const failures = [];
  Object.keys(sources).forEach((name) => {
    const began = performance.now();
    const root = renderMarkdown(document.createElement("div"), sources[name], { tables: true });
    const took = performance.now() - began;
    if (took > 5000) failures.push(name + " took " + Math.round(took) + " ms");
    if (elementsUnder(root).some((element) => element.tagName === "TABLE")) failures.push(name + " drew a table");
  });
  if (failures.length) throw new Error(failures.join("\n"));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("draw long delimiter rows: %v\n%s", err, output)
	}
}

// A body is drawn in time that grows with its length, whatever it holds. Two
// shapes once took the square of it: a wide header over many short rows,
// when every row was padded to the header's width, and a heading line with a
// long run of blanks inside it.
func TestRelicBodyIsDrawnInLinearTime(t *testing.T) {
	t.Parallel()
	node := requireNode(t)
	document := mustJSON(t, TasksDocument{Format: "workbook.tasks", Version: 1, Tasks: boardTasks()})
	program := clientDOMHarness("/", string(document)) + liftedMarkdownRenderer(t) + markdownHelpers + `
setTimeout(() => {
  const failures = [];
  const draw = (name, source, limit) => {
    const began = performance.now();
    const root = renderMarkdown(document.createElement("div"), source, { tables: true });
    const took = performance.now() - began;
    if (took > limit) failures.push(name + " took " + Math.round(took) + " ms");
    return elementsUnder(root);
  };
  const wide = 20000;
  const cells = draw("a wide header over short rows",
    "a|".repeat(wide) + "\n" + "-|".repeat(wide) + "\n" + "|\n".repeat(wide), 5000)
    .filter((element) => element.tagName === "TH" || element.tagName === "TD").length;
  // The header's cells and one for each short row: never rows times columns.
  if (cells > 3 * wide) failures.push("a wide header over short rows built " + cells + " cells");
  const run = " ".repeat(1 << 20);
  const inside = draw("blanks inside a heading", "# x" + run + "y", 5000).filter((element) => /^H[1-6]$/.test(element.tagName));
  if (inside.length !== 1) failures.push("blanks inside a heading drew " + inside.length + " headings");
  const after = draw("blanks after a heading", "# x" + run, 5000).filter((element) => /^H[1-6]$/.test(element.tagName));
  if (after.length !== 1) failures.push("blanks after a heading drew " + after.length + " headings");
  // What a heading holds is unchanged: its text without the blanks around it.
  for (const [source, want] of [["#  title \t ", 'h3["title"]'], ["#", "h3"], ["# a #", 'h3["a #"]'], ["####### seven", 'p["####### seven"]'], ["#\t", "h3"]]) {
    const shape = shapeOf(renderMarkdown(document.createElement("div"), source, { tables: true }));
    if (shape !== want) failures.push(JSON.stringify(source) + " rendered as " + shape + ", want " + want);
  }
  // A line separator is not a line end here, and a pattern whose dot stopped
  // at one tried every way to divide the run before it.
  const long = 1 << 17;
  const separated = {
    "a heading ending in a line separator": "# " + " ".repeat(long) + "\u2028",
    "a heading of tabs ending in a paragraph separator": "#" + "\t".repeat(long) + "\u2029x",
    "a bullet ending in a line separator": "- " + " ".repeat(long) + "\u2028",
    "a numbered item ending in a line separator": "1. " + " ".repeat(long) + "\u2028",
    "a backtick fence ending in a line separator": "` + "`" + `".repeat(long) + "\u2028",
    "a tilde fence ending in a line separator": "~".repeat(long) + "\u2028",
    "many bullet-like lines in one paragraph": ("x\n" + ("- " + " ".repeat(5000) + "\u2028\n").repeat(200)),
  };
  Object.keys(separated).forEach((name) => draw(name, separated[name], 2000));
  if (!draw("a heading holding a line separator", "# a\u2028b", 2000).some((element) => element.tagName === "H3")) {
    failures.push("a heading holding a line separator is not a heading");
  }
  // An opening bracket whose label or target is refused is not searched for
  // again from the next one.
  const brackets = {
    "a run of opening brackets": "[".repeat((1 << 20) - 1) + "]",
    "a run of image openers": "![".repeat((1 << 19) - 1) + "]",
    "a run of empty links": "[](".repeat(349525) + ")",
  };
  Object.keys(brackets).forEach((name) => draw(name, brackets[name], 2000));
  // A long body of short lines is drawn whole: 200,000 lines in one paragraph.
  const lines = 200000;
  const breaks = draw("a paragraph of many lines", "a\n".repeat(lines), 5000).filter((element) => element.tagName === "BR").length;
  if (breaks !== lines - 1) failures.push("a paragraph of " + lines + " lines drew " + breaks + " line breaks");
  if (failures.length) throw new Error(failures.join("\n"));
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("draw hostile bodies: %v\n%s", err, output)
	}
}
