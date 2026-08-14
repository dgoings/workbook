package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The markdown subset, as a browser runs it.
//
// The renderer is a parser over text somebody else typed, which is the whole
// reason this file is the length it is. Three questions are asked of it, over
// and over: does the syntax it claims to support render as the elements it
// claims, does everything else render as the characters somebody typed, and can
// any input at all make it build an element or an attribute that is not on its
// whitelist. The last one is asked of every single case in this file — every
// helper that reads a rendering walks it against the whitelist first — so a
// case added here for its formatting also tests its safety for free.

const (
	markdownTaskID = "WB-01J0000000000000000000MD01"
	// The two twins share the ten characters a person would actually type, so
	// that prefix is ambiguous and resolves to neither of them; every other
	// identifier here is distinct by its tenth character.
	markdownPNGID    = "01K0M6B8A4FTT8C39MXXYTWA01"
	markdownSVGID    = "01K0M6B8B5GVV9D4ANYYZTXB02"
	markdownLinkID   = "01K0M6B8C6HWWAE5BNZZAVYC03"
	markdownTwinOne  = "01K0M6B8D7JXXBF6CP00BWZD04"
	markdownTwinTwo  = "01K0M6B8D7JXXBF6CP00BWZD05"
	markdownAmbigous = "01K0M6B8D7"
)

// markdownAttachments is the list every case in this file resolves an image
// reference against: a raster the download route serves inline, an SVG and a
// text file it does not, a link with no bytes at all, and two files whose
// identifiers share a prefix.
func markdownAttachments() []core.Attachment {
	stamp := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	return []core.Attachment{
		{ID: markdownPNGID, Author: "a@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Name: "diagram.png", Kind: core.AttachmentFile, Media: "image/png", Size: 4096, Blob: strings.Repeat("a", 40),
		}},
		{ID: markdownSVGID, Author: "a@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Name: "chart.svg", Kind: core.AttachmentFile, Media: "image/svg+xml", Size: 512, Blob: strings.Repeat("b", 40),
		}},
		{ID: markdownLinkID, Author: "a@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Kind: core.AttachmentLink, URL: "https://example.test/design", Label: "Design doc",
		}},
		{ID: markdownTwinOne, Author: "a@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Name: "one.png", Kind: core.AttachmentFile, Media: "image/png", Size: 16, Blob: strings.Repeat("c", 40),
		}},
		{ID: markdownTwinTwo, Author: "a@example.com", AddedAt: stamp, AttachmentData: core.AttachmentData{
			Name: "two.png", Kind: core.AttachmentFile, Media: "image/png", Size: 16, Blob: strings.Repeat("d", 40),
		}},
	}
}

// markdownCase is one input and what the page must make of it.
//
// Shape is the rendering written down: element names, their children in
// brackets, text in quotes. It is compared as a whole rather than probed for
// one node, because "what else did it build" is the question this file exists
// to ask. Text is the reading — what somebody sees — and an empty Text is a
// case whose reading the Shape already states.
type markdownCase struct {
	Name  string `json:"name"`
	body  string
	Shape string `json:"shape"`
	Text  string `json:"text"`
}

// markdownCaseTask puts one case in one comment, in order, so a single page
// render exercises the whole table: a comment body is the surface that renders
// the complete subset with this task's attachments in reach.
func markdownCaseTask(cases []markdownCase) core.Task {
	task := clientPlacementTask(markdownTaskID, "Formatted task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Attachments = markdownAttachments()
	stamp := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	for at, one := range cases {
		task.Comments = append(task.Comments, core.Comment{
			ID:        fmt.Sprintf("01K0M6B8A4FTT8C39MXXYT%04d", at),
			Author:    "writer@example.com",
			Body:      one.body,
			CreatedAt: stamp.Add(time.Duration(at) * time.Minute),
		})
	}
	return task
}

// markdownHelpers is the JavaScript every program in this file appends: the
// whitelist walk over anything the renderer drew, and the shape of a rendering
// as a string a table can hold.
const markdownHelpers = `
function markdownClean(root, where) {
  const findings = markdownViolations(root);
  if (findings.length) throw new Error(where + " drew what it must not: " + findings.join("; "));
  return root;
}
function describeNode(node) {
  if (node.nodeType === 3) return JSON.stringify(node.textContent);
  const inner = (node.children || []).map(describeNode).join(",");
  return node.tagName.toLowerCase() + (inner ? "[" + inner + "]" : "");
}
function shapeOf(root) {
  return (root.children || []).map(describeNode).join(",");
}
function commentBody(at) {
  const rows = panelRows("comments");
  if (at >= rows.length) throw new Error("the thread has no comment " + at);
  const body = findElement(rows[at], (element) => hasDataKey(element, "commentBody"));
  if (!body) throw new Error("comment " + at + " drew no body");
  return markdownClean(body, "comment " + at);
}
function checkCases() {
  markdownCases.forEach((expectation, at) => {
    const body = commentBody(at);
    const shape = shapeOf(body);
    if (expectation.shape && shape !== expectation.shape) {
      throw new Error(expectation.name + " rendered as " + shape + ", want " + expectation.shape);
    }
    if (expectation.text && body.textContent !== expectation.text) {
      throw new Error(expectation.name + " reads as " + JSON.stringify(body.textContent) +
        ", want " + JSON.stringify(expectation.text));
    }
  });
}
// The task page's description, its editor, and the control that swaps them.
function descriptionPane() { return findElement(main, (element) => hasDataKey(element, "descriptionRead")); }
function descriptionEditor() { return findElement(main, (element) => element.id === "task-description"); }
function descriptionEditToggle() { return findElement(main, (element) => hasDataKey(element, "descriptionToggle")); }
// Draws one source string in the description pane and hands back the pane. The
// toggle is pressed to reach the editor, the text is typed, and the toggle is
// pressed again — which is what a reader does and what renders.
function drawDescription(source) {
  const toggle = descriptionEditToggle();
  if (!descriptionPane().hidden) toggle.eventListeners.click();
  descriptionEditor().value = source;
  toggle.eventListeners.click();
  return markdownClean(descriptionPane(), "the description");
}
`

// markdownCaseProgram renders the task page for a thread of cases and appends
// the table, the helpers, and the body a test wants run against them.
func markdownCaseProgram(t *testing.T, cases []markdownCase, body string) string {
	t.Helper()
	task := markdownCaseTask(cases)
	table, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	// Concatenated rather than interpolated into a raw string: a case may hold a
	// backtick — several of them do, since fenced code is part of the subset —
	// and a backtick cannot appear inside a Go raw literal at all.
	return threadPageProgram(t, []core.Task{task}, markdownHelpers+
		"\nconst markdownCases = "+string(table)+";\n"+body)
}

// What the subset claims to support, and the elements each piece of it makes.
func TestHandlerClientRendersTheMarkdownSubset(t *testing.T) {
	node := requireNode(t)
	cases := []markdownCase{
		{
			Name: "headings",
			body: "# One\n## Two\n### Three\n#### Four\n##### Five\n###### Six",
			// Every level lands between h3 and h6: the page's own title is the h2
			// above a description and the panels beside it are h3s, so a heading
			// somebody typed can size down from there and never outrank them.
			Shape: `h3["One"],h4["Two"],h5["Three"],h6["Four"],h6["Five"],h6["Six"]`,
			Text:  "OneTwoThreeFourFiveSix",
		},
		{
			Name:  "emphasis and code",
			body:  "**bold** and *italic* and `code` and _under_",
			Shape: `p[strong["bold"]," and ",em["italic"]," and ",code["code"]," and ",em["under"]]`,
			Text:  "bold and italic and code and under",
		},
		{
			Name:  "fenced code",
			body:  "```js\nconst a = 1;\nconst b = 2;\n```",
			Shape: `pre[code["const a = 1;\nconst b = 2;"]]`,
		},
		{
			Name:  "unordered list",
			body:  "- alpha\n- beta\n- gamma",
			Shape: `ul[li["alpha"],li["beta"],li["gamma"]]`,
		},
		{
			Name: "ordered list",
			body: "1. first\n2) second",
			// No start attribute and no numbering carried over: an ordered list
			// counts from one, which is one fewer attribute to write.
			Shape: `ol[li["first"],li["second"]]`,
		},
		{
			Name:  "blockquote",
			body:  "> quoted line\n> second line",
			Shape: `blockquote[p["quoted line",br,"second line"]]`,
		},
		{
			Name:  "link",
			body:  "See [the docs](https://example.test/a?b=c) now.",
			Shape: `p["See ",a["the docs"]," now."]`,
			Text:  "See the docs now.",
		},
		{
			Name: "line breaks",
			body: "one\ntwo",
			// A single newline is a break rather than a space. Every description
			// and comment written before this renderer existed was drawn with its
			// own line breaks, and losing them would be the one regression a
			// formatting change could not pay for.
			Shape: `p["one",br,"two"]`,
		},
		{
			Name:  "paragraphs",
			body:  "first para\n\nsecond para",
			Shape: `p["first para"],p["second para"]`,
		},
		{
			Name:  "a block ends the paragraph above it",
			body:  "text\n# Heading\n- item",
			Shape: `p["text"],h3["Heading"],ul[li["item"]]`,
		},
		{
			Name: "a wrapped bullet stays one bullet",
			body: "- one item\n  continued here\n- two",
			// The continuation keeps its own line break, for the reason a
			// paragraph's does.
			Shape: `ul[li["one item",br,"continued here"],li["two"]]`,
		},
		{
			Name: "a list with air in it is one list",
			body: "- one\n\n- two",
			// A blank line between items does not start a second list, which is
			// what a reader who spaced their bullets out meant.
			Shape: `ul[li["one"],li["two"]]`,
		},
		{
			Name: "nested quotes",
			body: "> outer\n> > inner",
			// One level of quote is stripped per pass, so the second marker is
			// quoted content and quotes itself.
			Shape: `blockquote[p["outer"],blockquote[p["inner"]]]`,
		},
		{
			Name: "a link wrapping code wrapping emphasis",
			body: "[the `**not bold**` one](https://example.test/x)",
			// Code wins inside link text: everything between the backticks is
			// characters, so the asterisks in it are asterisks.
			Shape: `p[a["the ",code["**not bold**"]," one"]]`,
			Text:  "the **not bold** one",
		},
	}
	program := markdownCaseProgram(t, cases, `
setTimeout(() => { checkCases(); }, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the markdown subset: %v\n%s", err, output)
	}
}

// Everything else. The rule the whole renderer is built around is that an input
// it does not understand is drawn as the characters somebody typed — never
// guessed at, never half-parsed, and never turned into markup.
func TestHandlerClientDrawsUnsupportedMarkdownAsText(t *testing.T) {
	node := requireNode(t)
	cases := []markdownCase{
		{
			Name: "raw HTML",
			body: `<img src=x onerror="alert(1)"><script>alert(1)</script>`,
			// The whole point: there is no HTML path in this renderer, so markup
			// in a task is text about markup.
			Shape: `p["<img src=x onerror=\"alert(1)\"><script>alert(1)</script>"]`,
			Text:  `<img src=x onerror="alert(1)"><script>alert(1)</script>`,
		},
		{
			Name:  "a table",
			body:  "| a | b |\n| --- | --- |\n| 1 | 2 |",
			Shape: `p["| a | b |",br,"| --- | --- |",br,"| 1 | 2 |"]`,
		},
		{
			Name: "a bare URL",
			body: "See https://example.test/x for more",
			// No autolinking. A link is written as a link or it is text.
			Shape: `p["See https://example.test/x for more"]`,
		},
		{
			Name:  "unclosed markers",
			body:  "**unclosed and *also unclosed and `code",
			Shape: "p[\"**unclosed and *also unclosed and `code\"]",
		},
		{
			Name: "a seventh hash",
			body: "####### seven",
			// Six is as deep as headings go, so a seventh is a paragraph that
			// starts with hashes.
			Shape: `p["####### seven"]`,
		},
		{
			Name: "three delimiters",
			body: "***triple***",
			// Three in a row is not syntax here. It is drawn rather than guessed
			// at, which is the rule everywhere else in the parser.
			Shape: `p["***triple***"]`,
		},
		{
			Name:  "a thematic break",
			body:  "above\n\n---\n\nbelow",
			Shape: `p["above"],p["---"],p["below"]`,
		},
		{
			Name: "indented code",
			body: "    four spaces",
			// Fenced code is the only code block. An indented line keeps its
			// indentation and is a paragraph.
			Shape: `p["    four spaces"]`,
		},
		{
			Name:  "a setext heading",
			body:  "Title\n=====",
			Shape: `p["Title",br,"====="]`,
		},
		{
			Name: "an identifier with underscores",
			body: "call some_long_name twice",
			// An underscore inside a word is an underscore. This is the one place
			// the parser looks at the character before a delimiter, and it is
			// there because snake_case is not emphasis.
			Shape: `p["call some_long_name twice"]`,
		},
		{
			Name: "the managed block's marker",
			body: "<!-- workbook:begin generator=agentdocs sha256=deadbeef -->",
			// A line that means something to another part of this tool means
			// nothing here, and is drawn as what it is.
			Shape: `p["<!-- workbook:begin generator=agentdocs sha256=deadbeef -->"]`,
		},
		{
			Name: "a forged action row",
			body: "Save · Delete · [Approve](javascript:alert(1))",
			// The target holds a parenthesis, so it is not a target and the whole
			// thing is text. A page that drew it as a link would be drawing a
			// control somebody else wrote.
			Shape: `p["Save · Delete · [Approve](javascript:alert(1))"]`,
		},
		{
			Name: "a link to a scheme this page will not follow",
			body: "[Approve](javascript:alert)",
			// A target with no parentheses in it parses, and then fails the same
			// scheme test the attachment list applies: the words are drawn, the
			// address is not.
			Shape: `p["Approve"]`,
			Text:  "Approve",
		},
		{
			Name: "interleaved fences",
			body: "```\none\n~~~\ntwo\n```\nthree",
			// A fence closes on its own marker, so the tilde line is code and the
			// text after the close is a paragraph.
			Shape: `pre[code["one\n~~~\ntwo"]],p["three"]`,
		},
		{
			Name: "an unclosed fence",
			body: "```\nnever closed\n**not bold**",
			// It runs to the end rather than swallowing anything after it: there
			// is nothing after it.
			Shape: `pre[code["never closed\n**not bold**"]]`,
		},
	}
	program := markdownCaseProgram(t, cases, `
setTimeout(() => { checkCases(); }, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the unsupported markdown cases: %v\n%s", err, output)
	}
}

// A markdown link is an anchor only for the two schemes this page follows, and
// it carries the same relationship the attachment list's links carry.
func TestHandlerClientLinksOnlyHTTPMarkdownTargets(t *testing.T) {
	node := requireNode(t)
	cases := []markdownCase{
		{Name: "https", body: "[a](https://example.test/x)"},
		{Name: "http", body: "[b](http://example.test/y)"},
		{Name: "javascript", body: "[c](javascript:alert)"},
		{Name: "data", body: "[d](data:text/html,<script>alert(1)</script>)"},
		{Name: "relative", body: "[e](/api/tasks)"},
		{Name: "protocol relative", body: "[f](//example.test/z)"},
		{Name: "mailto", body: "[g](mailto:someone@example.test)"},
		{Name: "empty", body: "[h]()"},
	}
	program := markdownCaseProgram(t, cases, `
setTimeout(() => {
  const anchors = (at) => elementsUnder(commentBody(at)).filter((element) => element.tagName === "A");
  const linked = anchors(0).concat(anchors(1));
  if (linked.length !== 2) throw new Error("http(s) targets drew " + linked.length + " anchors, want 2");
  const wanted = ["https://example.test/x", "http://example.test/y"];
  linked.forEach((anchor, at) => {
    if (anchor.href !== wanted[at]) throw new Error("anchor " + at + " points at " + anchor.href);
    // The same relationship an attached link carries: a new tab, no referrer,
    // no window handle back to this page, and no standing lent to somebody
    // else's address by a board that is only drawing what it was handed.
    if (anchor.rel !== "noopener noreferrer nofollow") throw new Error("anchor " + at + " carries rel " + anchor.rel);
    if (anchor.target !== "_blank") throw new Error("anchor " + at + " carries target " + anchor.target);
  });
  for (let at = 2; at < markdownCases.length; at += 1) {
    const drawn = anchors(at);
    if (drawn.length !== 0) {
      throw new Error(markdownCases[at].name + " was drawn as a link to " + drawn[0].href);
    }
  }
  // The words survive whether or not the address does: a refused target costs
  // the link, not the sentence.
  if (commentBody(2).textContent !== "c") throw new Error("a refused target lost its text: " + commentBody(2).textContent);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the markdown link cases: %v\n%s", err, output)
	}
}

// An image is an attachment of this task or it is text. There is no third
// answer, and in particular there is no external image: a board that fetched
// images named in task text would report every reader of every card to whoever
// wrote the task.
func TestHandlerClientDrawsImagesOnlyAsAttachmentsOfTheTask(t *testing.T) {
	node := requireNode(t)
	cases := []markdownCase{
		{Name: "a raster attachment", body: "![the diagram](attachment:" + markdownPNGID + ")"},
		{Name: "an unambiguous prefix", body: "![the diagram](attachment:" + markdownPNGID[:10] + ")"},
		{Name: "an ambiguous prefix", body: "![two of them](attachment:" + markdownAmbigous + ")",
			Shape: `p["![two of them](attachment:` + markdownAmbigous + `)"]`},
		{Name: "an attachment this task does not hold", body: "![missing](attachment:01K0NOTHINGATALLNOTHINGXX)",
			Shape: `p["![missing](attachment:01K0NOTHINGATALLNOTHINGXX)"]`},
		{Name: "an SVG attachment", body: "![the chart](attachment:" + markdownSVGID + ")"},
		{Name: "a link attachment", body: "![the design](attachment:" + markdownLinkID + ")",
			Shape: `p["![the design](attachment:` + markdownLinkID + `)"]`},
		{Name: "an external image", body: "![beacon](https://tracker.test/pixel.png)",
			Shape: `p["![beacon](https://tracker.test/pixel.png)"]`},
		{Name: "a data image", body: "![inline](data:image/png;base64,AAAA)",
			Shape: `p["![inline](data:image/png;base64,AAAA)"]`},
		{Name: "an attachment of another task", body: "![elsewhere](/api/tasks/WB-01J0000000000000000000XX01/attachments/" + markdownPNGID + ")",
			Shape: `p["![elsewhere](/api/tasks/WB-01J0000000000000000000XX01/attachments/` + markdownPNGID + `)"]`},
	}
	program := markdownCaseProgram(t, cases, `
setTimeout(() => {
  checkCases();
  const address = "/api/tasks/" + encodeURIComponent(`+strconv.Quote(markdownTaskID)+`) + "/attachments/";
  // A raster the download route serves inline is drawn as an image pointing at
  // that route, by full identifier and by any prefix that names one attachment.
  [0, 1].forEach((at) => {
    const shape = shapeOf(commentBody(at));
    if (shape !== "p[img]") throw new Error(markdownCases[at].name + " rendered as " + shape);
    const image = elementsUnder(commentBody(at))[1];
    if (image.src !== address + `+strconv.Quote(markdownPNGID)+`) {
      throw new Error(markdownCases[at].name + " points at " + image.src);
    }
    if (image.alt !== "the diagram") throw new Error(markdownCases[at].name + " carries alt " + image.alt);
  });
  // An SVG is markup that can carry script. The route already refuses to hand
  // one back inline, so a reference to one degrades to a link to that route,
  // which answers with Content-Disposition: attachment and downloads it.
  const svg = commentBody(4);
  if (shapeOf(svg) !== 'p[a["the chart"]]') throw new Error("an SVG reference rendered as " + shapeOf(svg));
  const link = elementsUnder(svg)[1];
  if (link.href !== address + `+strconv.Quote(markdownSVGID)+`) throw new Error("the SVG link points at " + link.href);
  if (link.rel !== "noopener noreferrer nofollow" || link.target !== "_blank") {
    throw new Error("the SVG link carries rel " + link.rel + " and target " + link.target);
  }
  // Nothing anywhere in this thread drew an image at an address that is not
  // this task's own attachment route.
  panelRows("comments").forEach((row, at) => {
    elementsUnder(commentBody(at)).filter((element) => element.tagName === "IMG").forEach((image) => {
      if (!image.src.startsWith(address)) throw new Error("case " + at + " drew an image at " + image.src);
    });
  });
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the markdown image cases: %v\n%s", err, output)
	}
}

// The renderer against inputs built to break it: unclosed everything, nesting
// past any depth a person would type, single tokens longer than a paragraph,
// and a body at the ceiling core allows. None of them may build an element
// outside the whitelist, and none of them may take more than a moment.
//
// The time bound is coarse on purpose. It is not measuring how fast the parser
// is; it is the difference between a scan that is linear and one that is not,
// and every quadratic version of this renderer written on the way to this one
// failed it by two orders of magnitude rather than by a hair.
func TestHandlerClientBoundsHostileMarkdown(t *testing.T) {
	node := requireNode(t)
	hostile := []struct {
		name   string
		source string
	}{
		{"nine thousand openers", strings.Repeat("*", 9000)},
		{"nine thousand underscores", strings.Repeat("_", 9000)},
		{"four thousand unclosed brackets", strings.Repeat("[", 4000) + strings.Repeat("]", 4000)},
		{"brackets that never take a target", strings.Repeat("[a]", 4000)},
		{"three thousand backticks", strings.Repeat("`", 3000)},
		{"alternating openers", strings.Repeat("**a", 3000)},
		{"a fifty thousand deep quote", strings.Repeat(">", 50000) + " deep"},
		{"ten thousand quote lines", strings.Repeat(">\n", 10000)},
		{"one very long token", strings.Repeat("z", 900)},
		{"a paragraph of long tokens", strings.Repeat(strings.Repeat("z", 900)+" ", 18)},
		{"nested emphasis", strings.Repeat("*a*", 3000)},
		{"a right to left override", "‮" + strings.Repeat("gnp.txt", 40) + "‬"},
		{"forged chrome", strings.Repeat("Save\nDelete\nApprove\n", 400)},
		{"the managed marker, many times", strings.Repeat("<!-- workbook:begin -->\n", 400)},
		{"interleaved fences", strings.Repeat("```\ncode\n~~~\n", 800)},
		{"list markers all the way down", strings.Repeat("- - - - item\n", 800)},
		{"a full sized body", strings.Repeat("# h\n\n> q\n\n- i\n\n`c` **b** [l](https://e.test/x)\n\n", 300)},
		{"sixteen kibibytes of one line", strings.Repeat("a*b_c`d[e](f)", 1260)},
	}
	sources := make([]string, 0, len(hostile))
	names := make([]string, 0, len(hostile))
	for _, one := range hostile {
		if len(one.source) > 16384 {
			// Core's own ceiling on a comment body. An input past it is not one
			// this page can be handed, and a test that used one would be proving
			// something about a body that cannot exist.
			one.source = one.source[:16384]
		}
		sources = append(sources, one.source)
		names = append(names, one.name)
	}
	encodedSources, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	encodedNames, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	task := clientPlacementTask(markdownTaskID, "Hostile task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Attachments = markdownAttachments()
	program := threadPageProgram(t, []core.Task{task}, markdownHelpers+
		"\nconst hostileSources = "+string(encodedSources)+";\n"+
		"const hostileNames = "+string(encodedNames)+";\n"+`
setTimeout(() => {
  const started = Date.now();
  hostileSources.forEach((source, at) => {
    const pane = drawDescription(source);
    // Something was drawn. A renderer that quietly gave up on hard input would
    // satisfy every other assertion here.
    if (source.trim() && pane.children.length === 0) {
      throw new Error(hostileNames[at] + " rendered nothing at all");
    }
    // And an input that carried a long token still carries it. A renderer that
    // swallowed what it could not parse would pass every other line here.
    if (source.indexOf("z") >= 0 && pane.textContent.indexOf("zzzz") < 0) {
      throw new Error(hostileNames[at] + " lost the text it was given");
    }
  });
  const elapsed = Date.now() - started;
  if (elapsed > 2000) throw new Error("the hostile inputs took " + elapsed + "ms to draw");
  // The deepest nesting any of them can ask for is bounded, so the tree has a
  // floor no input can push through — which is what a stack overflow would be.
  const deep = drawDescription(">".repeat(50000) + " deep");
  let depth = 0;
  for (let node = deep; node; node = node.children[0]) depth += 1;
  if (depth > 12) throw new Error("a fifty thousand deep quote nested " + depth + " elements");
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the hostile markdown inputs: %v\n%s", err, output)
	}
}

// The task page reads its description and edits it in place. What must not
// change is everything around that: the field is the same textarea with the
// same value, a save still sends only what this reader changed, and it still
// names the tip the page rendered.
func TestHandlerClientTaskDescriptionReadsAndEdits(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(markdownTaskID, "Formatted task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "# Overview\n\nSome **bold** text.\n\n- one\n- two"
	task.Attachments = markdownAttachments()
	program := threadPageProgram(t, []core.Task{task}, markdownHelpers+`
setTimeout(async () => {
  const pane = descriptionPane();
  const editor = descriptionEditor();
  const toggle = descriptionEditToggle();
  if (!pane || !editor || !toggle) throw new Error("the task page has no description pane, editor or toggle");
  // Read mode is what the page opens in, and the editor is still in the
  // document holding the same string a save would send.
  if (pane.hidden) throw new Error("the description opened in edit mode");
  if (!editor.hidden) throw new Error("the editor is drawn beside the rendering");
  if (editor.value !== `+strconv.Quote(task.Description)+`) throw new Error("the editor lost the description");
  markdownClean(pane, "the description");
  const shape = shapeOf(pane);
  if (shape !== 'h3["Overview"],p["Some ",strong["bold"]," text."],ul[li["one"],li["two"]]') {
    throw new Error("the description rendered as " + shape);
  }
  // The editor's field is unchanged: same id, same parent, same layout hook.
  if (!editor.parentElement.className.split(/\s+/).includes("field--description")) {
    throw new Error("the editor left the flexible description field");
  }
  if (toggle.textContent !== "Edit") throw new Error("the toggle reads " + toggle.textContent);

  toggle.eventListeners.click();
  if (!pane.hidden || editor.hidden) throw new Error("Edit did not swap the rendering for the editor");
  if (toggle.textContent !== "Done") throw new Error("the toggle still reads " + toggle.textContent);
  if (globalThis.activeElement !== editor) throw new Error("Edit did not put the caret in the editor");

  editor.value = "Rewritten with `+"`code`"+` in it.";
  toggle.eventListeners.click();
  if (pane.hidden || !editor.hidden) throw new Error("Done did not return to the rendering");
  const rewritten = shapeOf(markdownClean(pane, "the description"));
  if (rewritten !== 'p["Rewritten with ",code["code"]," in it."]') {
    throw new Error("Done rendered " + rewritten);
  }

  // And the save is the save it always was: one field, because one field
  // changed, and the tip this page rendered against.
  const form = findElement(main, (element) => hasClassToken(element, "task-layout"));
  await form.eventListeners.submit({ preventDefault() {} });
  const wrote = fetchCalls.filter((call) => (call.options.method || "GET") !== "GET");
  if (wrote.length !== 1) throw new Error("the save sent " + wrote.length + " requests");
  const sent = JSON.parse(wrote[0].options.body);
  const fields = Object.keys(sent).sort().join(",");
  if (fields !== "description,expectedHead") throw new Error("the save sent " + fields);
  if (sent.description !== editor.value) throw new Error("the save sent " + JSON.stringify(sent.description));
  if (sent.expectedHead !== "head-1") throw new Error("the save named the head " + sent.expectedHead);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the description read and edit modes: %v\n%s", err, output)
	}
}

// An image in a description is a reference to an attachment of the task, and
// the attachment list arrives on the poll. So a screenshot uploaded in the
// panel below appears in the description that already named it, on the next
// poll, with no reload and no edit to a word of the text.
//
// The text itself does not follow the poll and must not: no field on this form
// does, which is what keeps a reader's edits under their caret.
func TestHandlerClientDescriptionFollowsThePollAndItsAttachments(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(markdownTaskID, "Formatted task", core.StatusReady, core.PriorityMedium)
	task.Head = "head-1"
	task.Description = "Before ![the diagram](attachment:" + markdownPNGID + ")"
	// The page is rendered from a task with nothing attached, so the reference
	// resolves to nothing and is drawn as the text it is.
	arrived := task
	arrived.Attachments = markdownAttachments()
	arrived.Head = "head-2"
	program := threadPageProgram(t, []core.Task{task}, markdownHelpers+`
setTimeout(async () => {
  const pane = descriptionPane();
  const before = shapeOf(markdownClean(pane, "the description"));
  if (before !== 'p["Before ![the diagram](attachment:`+markdownPNGID+`)"]') {
    throw new Error("an unresolvable reference rendered as " + before);
  }
  taskResponse = `+string(mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: []core.Task{arrived},
		Presentation: presentationForTasks([]core.Task{arrived}),
	}))+`;
  await intervalCallback();
  const after = shapeOf(markdownClean(descriptionPane(), "the description"));
  if (after !== 'p["Before ",img]') throw new Error("the uploaded attachment did not appear: " + after);
}, 0);
`)
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the description poll: %v\n%s", err, output)
	}
}

// A card renders the inline half of the subset and nothing that makes a block.
// The clamp on a card description measures lines of one box: a heading or a
// list inside it would make a card's height depend on what somebody typed.
func TestHandlerClientCardsRenderOnlyInlineMarkdown(t *testing.T) {
	node := requireNode(t)
	task := clientPlacementTask(markdownTaskID, "Formatted task", core.StatusReady, core.PriorityMedium)
	task.Description = "# Heading\n- item **bold** `code` [docs](https://example.test/x)\n> quote\n\n```\ncode\n```"
	task.Attachments = markdownAttachments()
	tasks := []core.Task{task}
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	document := mustJSON(t, TasksDocument{
		Format: "workbook.tasks", Version: 1, Tasks: tasks, Presentation: presentationForTasks(tasks),
	})
	program := clientDOMHarness("/", string(document)) + renderedClientScript(t, response.Body.String()) +
		markdownHelpers + `
setTimeout(() => {
  const card = boardCard(` + strconv.Quote(markdownTaskID) + `);
  if (!card) throw new Error("the board drew no card");
  const description = findElement(card, (element) => element.tagName === "P" && !hasDataKey(element, "taskFailure"));
  if (!description) throw new Error("the card drew no description");
  markdownClean(description, "the card description");
  // The inline half: emphasis and code are elements. The block half is not
  // here at all — the hash, the bullet, the quote marker and the fence are the
  // characters they are, and a link is its words.
  // No break either: a card runs its lines together the way it always has, so
  // a description that is seven lines of source is the same two clamped lines
  // of card it was before this renderer existed.
  const built = elementsUnder(description).map((element) => element.tagName).join(",");
  if (built !== "STRONG,CODE") {
    throw new Error("the card built " + built);
  }
  ["P", "H3", "UL", "OL", "LI", "BLOCKQUOTE", "PRE", "A", "IMG", "BR"].forEach((tag) => {
    if (elementsUnder(description).some((element) => element.tagName === tag)) {
      throw new Error("the card built a " + tag);
    }
  });
  const reading = description.textContent;
  ["# Heading", "- item", "> quote", "\u0060\u0060\u0060"].forEach((literal) => {
    if (reading.indexOf(literal) < 0) throw new Error("the card lost " + JSON.stringify(literal));
  });
  if (reading.indexOf("docs") < 0) throw new Error("the card lost a link's words");
  if (reading.indexOf("https://example.test/x") >= 0) throw new Error("the card drew a link's address");
}, 0);
`
	if output, err := nodeCommand(node, program).CombinedOutput(); err != nil {
		t.Fatalf("execute the card markdown render: %v\n%s", err, output)
	}
}

// The page publishes the download route's own inline allow-list, and the client
// reads it from there. Two copies of this set is how a type could come to be
// drawn as an image on a page that the route would only ever hand back as a
// download.
func TestHandlerPublishesTheInlineAttachmentMediaTypes(t *testing.T) {
	body := boardPage(t)
	want := `data-inline-media="` + strings.Join(InlineAttachmentMediaTypes(), " ") + `"`
	if !strings.Contains(body, want) {
		t.Errorf("GET / body does not carry %q", want)
	}
	for media := range inlineAttachmentMediaTypes {
		if !strings.Contains(body, media) {
			t.Errorf("GET / body does not publish the inline media type %q", media)
		}
	}
	// The client must not hold a second copy of the set under any spelling.
	if strings.Contains(body, `"image/png", "image/`) {
		t.Error("the client script carries its own list of inline media types")
	}
}

// What the stylesheet has to say about rendered markdown, pinned as text.
//
// None of it is drawn by the client, so it is asserted against the served page
// the way the board's own chrome is: a fake DOM with no layout engine could not
// read a rule out of it.
func TestHandlerPinsMarkdownBlockRules(t *testing.T) {
	body := boardPage(t)
	for _, fragment := range []string{
		// A code block keeps its own line breaks and scrolls inside its own box.
		// Both halves are load-bearing: without the scroll a long line of code
		// widens the page, and without the overflow-wrap reset the container's
		// `anywhere` breaks code at an arbitrary column.
		`.markdown pre { margin: 0 0 .55rem; padding: .5rem .6rem; border: 1px solid #d5deea; border-radius: 4px; background: #f6f8fc; overflow-x: auto; overflow-wrap: normal; white-space: pre; }`,
		// Prose with a 900-character word in it is not a reason for the page to
		// scroll sideways.
		`.markdown { min-width: 0; overflow-wrap: anywhere; }`,
		// An attachment drawn inline is bounded by its column rather than by its
		// own pixels.
		`.markdown img { display: block; max-width: 100%; height: auto;`,
		// Headings live between h3 and h6, which is the range the renderer maps
		// every level into. A rule for h1 or h2 here would be a rule for an
		// element that cannot arrive.
		`.markdown h3 {`,
		`.markdown h6 {`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the stylesheet does not contain %q", fragment)
		}
	}
	for _, stale := range []string{`.markdown h1`, `.markdown h2`, `.comment__body { margin: 0; font-size: .84rem; line-height: 1.45; overflow-wrap: anywhere; white-space: pre-wrap; }`} {
		if strings.Contains(body, stale) {
			t.Errorf("the stylesheet still contains %q", stale)
		}
	}
	// A card's formatting may not change how tall a card is: the rules it gets
	// are weight, slant and the box around inline code, and none of them is a
	// margin, a display or a line-height.
	//
	// The reset at the front of the code rule is the load-bearing part and the
	// reason it is pinned here. `.task-card code` above it is the copy control
	// that holds the task's identifier — one token, one line, an ellipsis — and
	// it matches every <code> on a card, a rendered description's included.
	// Without the reset, and without it being written after, inline code in a
	// description cannot wrap: measured at 350px of content in a 202px box.
	for _, fragment := range []string{
		`.task-card p strong { font-weight: 800; }`,
		`.task-card p em { font-style: italic; }`,
		`.task-card p code { overflow: visible; text-overflow: clip; white-space: normal; padding: 0 .18rem;`,
		// And a card may never be wider than the column it is in, whatever its
		// description holds: a grid item's automatic minimum is its content,
		// and one 46-character identifier drew a 374px card in a 268px column.
		`.task-card { min-width: 0;`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("the card's inline formatting does not contain %q", fragment)
		}
	}
	// The reset only works where it is. A stylesheet that put the copy
	// control's rule after this one would silently take the wrap away again.
	if strings.Index(body, `.task-card code {`) > strings.Index(body, `.task-card p code {`) {
		t.Error("the copy control's code rule is now written after the description's, which undoes it")
	}
}

// The download control in an attachment's row keeps a floor on its width. A
// file named `..` — which core allows, forbidding only a path separator and a
// NUL — drew an eight pixel target without it, and this is the control that
// downloads the file.
func TestHandlerPinsAttachmentLinkTargetSize(t *testing.T) {
	if body := boardPage(t); !strings.Contains(body, `.attachment__name a { display: inline-block; min-width: 1.5rem; padding: .2rem 0;`) {
		t.Error("the attachment link no longer pins a minimum target size")
	}
}
