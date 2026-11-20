package webui

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The task sidebar's rhythm, measured in a real browser. Every section —
// Properties, Dependencies, Assignments and Attachments — is meant to space
// itself from one small set of tokens, and every row in it is meant to be the
// same card. Those are claims about computed geometry, which the Node harness
// cannot see, so they are read back from headless Chrome at two desktop
// viewports, on a task holding one of each kind of row.

// rhythmTaskID is the task the rhythm tests open.
const rhythmTaskID = "WB-01J0000000000000000000RH00"

// rhythmPrerequisiteTitle is long enough that, beside a wide text button, it
// wrapped early onto a second and third line.
const rhythmPrerequisiteTitle = "Run the board behind a sign-in proxy so a team can share one served board"

// rhythmBoard serves a board whose task rhythmTaskID depends on one task, is
// held by this board's identity, and carries one link and one file.
func rhythmBoard(t *testing.T) http.Handler {
	t.Helper()
	target := clientPlacementTask(rhythmTaskID, "Polish the task sidebar", core.StatusInProgress, core.PriorityHigh)
	target.Head = "head-1"
	target.Description = "A task with one of every sidebar row."
	prerequisite := clientPlacementTask("WB-01J0000000000000000000RH01", rhythmPrerequisiteTitle, core.StatusReady, core.PriorityMedium)
	target.Dependencies = []string{prerequisite.ID}
	target.Assignments = []core.Assignment{
		{Principal: boardIdentity, Creator: boardIdentity, CreatedAt: assignmentStamp},
	}
	target.Attachments = []core.Attachment{
		{
			ID: "att-link", Author: boardIdentity, AddedAt: assignmentStamp,
			AttachmentData: core.AttachmentData{Kind: core.AttachmentLink, URL: "https://ersc.io/blog/what-comes-after-git"},
		},
		{
			ID: "att-file", Author: boardIdentity, AddedAt: assignmentStamp,
			AttachmentData: core.AttachmentData{Kind: core.AttachmentFile, Name: "screenshot.png", Media: "image/png", Size: 48213, Blob: "0123456789abcdef0123456789abcdef01234567"},
		},
	}
	board := &assignableBoard{task: target}
	options := board.options(t, []core.Task{target, prerequisite})
	options.History = func(context.Context, string) (core.TaskDetail, error) {
		detail := historyDetail()
		detail.Task = target
		return detail, nil
	}
	return NewHandler(options)
}

// rhythmSidebarReady waits for a task page's sidebar to hold every row the
// rhythm board gives it.
const rhythmSidebarReady = `
const sidebarReady = () => {
  const sidebar = document.querySelector(".task-route--history .task-sidebar");
  return sidebar &&
    sidebar.querySelectorAll("[data-relationship-row]").length === 1 &&
    sidebar.querySelectorAll('[data-task-panel="assignments"] .assignment').length === 1 &&
    sidebar.querySelectorAll('[data-task-panel="attachments"] .attachment').length === 2;
};
`

// sidebarRhythm is what the page reports about its sidebar.
type sidebarRhythm struct {
	Viewport struct{ Width, Height float64 }
	// Cards maps each row kind to its computed card box.
	Cards map[string]struct {
		Padding, Border, Radius string
	}
	// RowGaps maps each list to its computed gap between cards, and
	// AttachmentCardGap is the gap measured between the two attachment cards.
	RowGaps           map[string]string
	AttachmentCardGap float64
	// HeadingGaps maps each section to the distance from its heading to its
	// first content, and the rest are the other distances the rhythm names.
	HeadingGaps  map[string]float64
	AboveRule    map[string]float64
	BelowRule    map[string]float64
	ControlsGaps map[string]float64
	LabelGaps    map[string]float64
	// Inline maps each inline row to its input and button boxes.
	Inline map[string]struct {
		Input, Button struct{ Top, Height float64 }
	}
	// TitleShare is the dependency title's width over its card's inner width.
	TitleShare float64
	// MetaLines maps each attachment to how many lines its metadata takes.
	MetaLines map[string]int
	// DependencyRules lists every border a Dependencies element draws on one
	// side only — the rule that used to sit under the heading.
	DependencyRules []string
	// RemoveSizes are the x buttons' boxes, by accessible name.
	RemoveSizes map[string]struct{ Width, Height float64 }
	// AssignmentText is everything the assignment card says.
	AssignmentText string
}

const sidebarRhythmScript = pageGeometryHelpers + rhythmSidebarReady + `
(async () => {
  await waitFor(sidebarReady, "the task page's sidebar and its rows");
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const sidebar = document.querySelector(".task-route--history .task-sidebar");
  const properties = sidebar.querySelector(".task-properties");
  const dependencies = sidebar.querySelector(".task-relationships");
  const assignments = sidebar.querySelector('[data-task-panel="assignments"]');
  const attachments = sidebar.querySelector('[data-task-panel="attachments"]');
  const sections = { properties, dependencies, assignments, attachments };
  for (const [name, section] of Object.entries(sections)) if (!section) throw new Error("the sidebar has no " + name + " section");
  const rect = (element) => element.getBoundingClientRect();
  const style = (element) => getComputedStyle(element);
  const heading = (section) => section.querySelector("h3");

  const cards = {
    dependency: dependencies.querySelector("[data-relationship-row]"),
    assignment: assignments.querySelector(".assignment"),
    link: attachments.querySelector('[data-attachment-id="att-link"]'),
    file: attachments.querySelector('[data-attachment-id="att-file"]')
  };
  const cardBoxes = {};
  for (const [name, card] of Object.entries(cards)) {
    if (!card) throw new Error("the sidebar has no " + name + " card");
    const computed = style(card);
    cardBoxes[name] = {
      padding: [computed.paddingTop, computed.paddingRight, computed.paddingBottom, computed.paddingLeft].join(" "),
      border: [computed.borderTopWidth, computed.borderRightWidth, computed.borderBottomWidth, computed.borderLeftWidth].join(" "),
      radius: computed.borderTopLeftRadius + " " + computed.borderBottomRightRadius
    };
  }
  const lists = {
    dependencies: dependencies.querySelector(".relationship-list"),
    assignments: assignments.querySelector("[data-panel-list]"),
    attachments: attachments.querySelector("[data-panel-list]")
  };
  const rowGaps = {};
  for (const [name, list] of Object.entries(lists)) rowGaps[name] = style(list).rowGap;

  const firstContent = {
    properties: properties.querySelector(".field"),
    dependencies: lists.dependencies,
    assignments: lists.assignments,
    attachments: lists.attachments
  };
  const headingGaps = {};
  for (const [name, section] of Object.entries(sections)) {
    headingGaps[name] = rect(firstContent[name]).top - rect(heading(section)).bottom;
  }
  // The bottom of the last thing drawn in a section, whatever it is.
  const drawnBottom = (section) => Math.max(...[...section.querySelectorAll("*")]
    .filter((element) => { const box = rect(element); return box.height > 0 && box.width > 0 && style(element).visibility !== "hidden"; })
    .map((element) => rect(element).bottom));
  const order = ["properties", "dependencies", "assignments", "attachments"];
  const aboveRule = {};
  const belowRule = {};
  for (let index = 1; index < order.length; index++) {
    const section = sections[order[index]];
    const ruleTop = rect(section).top;
    const ruleWidth = parseFloat(style(section).borderTopWidth);
    if (!(ruleWidth > 0)) throw new Error(order[index] + " draws no rule above it");
    aboveRule[order[index]] = ruleTop - drawnBottom(sections[order[index - 1]]);
    belowRule[order[index]] = rect(heading(section)).top - (ruleTop + ruleWidth);
  }
  const controlsGaps = {
    dependencies: rect(dependencies.querySelector(".relationship-kind")).top - rect(lists.dependencies).bottom,
    assignments: rect(assignments.querySelector("form.assignment-add")).top - rect(lists.assignments).bottom,
    attachments: rect(attachments.querySelector(".attachment-add")).top - rect(lists.attachments).bottom
  };
  const assignLabel = assignments.querySelector("form.assignment-add label");
  const linkLabel = attachments.querySelector('[data-attachment-link-form] label[for="attachment-url"]');
  const fileLabel = attachments.querySelector('[data-attachment-file-form] label[for="attachment-file"]:not(.thread-button)');
  const labelGaps = {
    dependencies: rect(dependencies.querySelector(".relationship-editor input")).top - rect(dependencies.querySelector(".relationship-kind")).bottom,
    assignments: rect(assignments.querySelector(".sidebar-inline")).top - rect(assignLabel).bottom,
    attachLink: rect(attachments.querySelector("#attachment-url")).top - rect(linkLabel).bottom,
    attachFile: rect(attachments.querySelector("[data-attachment-choose]")).top - rect(fileLabel).bottom
  };
  const pair = (input, button) => ({
    input: { top: rect(input).top, height: rect(input).height },
    button: { top: rect(button).top, height: rect(button).height }
  });
  const inline = {
    dependencies: pair(dependencies.querySelector(".relationship-editor input"), dependencies.querySelector("[data-relationship-add]")),
    assignments: pair(assignments.querySelector(".sidebar-inline input"), assignments.querySelector(".sidebar-inline button")),
    attachLink: pair(attachments.querySelector("#attachment-url"), attachments.querySelector("[data-attachment-add-link]"))
  };
  const card = cards.dependency;
  const cardStyle = style(card);
  const inner = card.clientWidth - parseFloat(cardStyle.paddingLeft) - parseFloat(cardStyle.paddingRight);
  const titleShare = rect(card.querySelector(".relationship-row__text")).width / inner;
  const lines = (element) => {
    const range = document.createRange();
    range.selectNodeContents(element);
    return new Set([...range.getClientRects()].filter((box) => box.width > 0).map((box) => Math.round(box.top))).size;
  };
  const metaLines = {
    link: lines(cards.link.querySelector(".attachment__meta")),
    file: lines(cards.file.querySelector(".attachment__meta"))
  };
  const dependencyRules = [];
  for (const element of [dependencies, ...dependencies.querySelectorAll("*")]) {
    if (element === dependencies) continue;
    const computed = style(element);
    const widths = ["Top", "Right", "Bottom", "Left"].map((side) => parseFloat(computed["border" + side + "Width"]) || 0);
    if (widths.some((width) => width > 0) && new Set(widths).size > 1) {
      dependencyRules.push((element.className || element.tagName) + " " + widths.join("/"));
    }
  }
  const removeSizes = {};
  for (const button of sidebar.querySelectorAll("[data-row-remove]")) {
    removeSizes[button.getAttribute("aria-label")] = { width: rect(button).width, height: rect(button).height };
  }
  return {
    viewport: { width: innerWidth, height: innerHeight },
    cards: cardBoxes, rowGaps,
    attachmentCardGap: rect(cards.file).top - rect(cards.link).bottom,
    headingGaps, aboveRule, belowRule, controlsGaps, labelGaps, inline, titleShare, metaLines,
    dependencyRules, removeSizes,
    assignmentText: cards.assignment.textContent
  };
})()
`

// allEqual reports whether every value in values is within tolerance of the
// first, and names them all when one is not.
func allEqual(t *testing.T, what string, values map[string]float64, tolerance float64) {
	t.Helper()
	var first float64
	firstName := ""
	for name, value := range values {
		if firstName == "" {
			first, firstName = value, name
			continue
		}
		if math.Abs(value-first) > tolerance {
			t.Errorf("%s differ across the sidebar: %v", what, values)
			return
		}
	}
}

// Every sidebar section is spaced from one set of tokens and every row is the
// same card, at two desktop viewports.
func TestBrowserSpacesEverySidebarSectionAlike(t *testing.T) {
	t.Parallel()
	for _, viewport := range []struct{ width, height int }{{1440, 900}, {1024, 768}} {
		t.Run(fmt.Sprintf("%dx%d", viewport.width, viewport.height), func(t *testing.T) {
			t.Parallel()
			var rhythm sidebarRhythm
			openInBrowser(t, rhythmBoard(t), browserPage{
				Width: viewport.width, Height: viewport.height,
				Path:   "/tasks/" + rhythmTaskID,
				Script: sidebarRhythmScript,
			}, &rhythm)
			if rhythm.Viewport.Width != float64(viewport.width) || rhythm.Viewport.Height != float64(viewport.height) {
				t.Fatalf("the page was drawn at %vx%v", rhythm.Viewport.Width, rhythm.Viewport.Height)
			}

			// One card for every kind of row.
			reference := rhythm.Cards["dependency"]
			for name, card := range rhythm.Cards {
				if card != reference {
					t.Errorf("the %s card is drawn %+v, the dependency card %+v", name, card, reference)
				}
			}
			if rhythm.RowGaps["dependencies"] != rhythm.RowGaps["assignments"] || rhythm.RowGaps["assignments"] != rhythm.RowGaps["attachments"] {
				t.Errorf("the lists space their cards differently: %v", rhythm.RowGaps)
			}
			listGap, err := strconv.ParseFloat(strings.TrimSuffix(rhythm.RowGaps["attachments"], "px"), 64)
			if err != nil {
				t.Fatalf("read the attachment list's gap %q: %v", rhythm.RowGaps["attachments"], err)
			}
			if math.Abs(rhythm.AttachmentCardGap-listGap) > 0.5 {
				t.Errorf("two attachment cards are %vpx apart, want the list's gap of %vpx", rhythm.AttachmentCardGap, listGap)
			}

			// The same distances in every section.
			allEqual(t, "the gaps from heading to content", rhythm.HeadingGaps, 1)
			allEqual(t, "the gaps above each section's rule", rhythm.AboveRule, 1)
			allEqual(t, "the gaps below each section's rule", rhythm.BelowRule, 1)
			above, below := rhythm.AboveRule["assignments"], rhythm.BelowRule["assignments"]
			if math.Abs(above-below) > 1 {
				t.Errorf("the rule above Assignments has %vpx above it and %vpx below it", above, below)
			}
			allEqual(t, "the gaps from a list to its add controls", rhythm.ControlsGaps, 1)
			allEqual(t, "the gaps from a caption to its control", rhythm.LabelGaps, 1)

			// Every inline row lines its box and its button up.
			for name, row := range rhythm.Inline {
				if math.Abs(row.Input.Top-row.Button.Top) > 0.5 || math.Abs(row.Input.Height-row.Button.Height) > 0.5 {
					t.Errorf("the %s row's input is %+v and its button %+v; want one top and one height", name, row.Input, row.Button)
				}
			}

			// A dependency's title has the card, not a column beside a button.
			if rhythm.TitleShare < 0.8 {
				t.Errorf("the dependency title takes %.0f%% of its card's inner width, want at least 80%%", rhythm.TitleShare*100)
			}
			// An attachment's metadata reads as one line.
			for name, lines := range rhythm.MetaLines {
				if lines != 1 {
					t.Errorf("the %s attachment's metadata takes %d lines, want 1", name, lines)
				}
			}
			// Nothing in Dependencies draws a rule on one side only.
			if len(rhythm.DependencyRules) != 0 {
				t.Errorf("Dependencies draws one-sided rules: %v", rhythm.DependencyRules)
			}
			// The assignment says when, not how long ago, on the page the
			// server actually serves.
			if strings.Contains(rhythm.AssignmentText, "ago") || strings.Contains(rhythm.AssignmentText, "just now") {
				t.Errorf("the assignment card words a relative age: %q", rhythm.AssignmentText)
			}
			// Every x is a 24px target.
			if len(rhythm.RemoveSizes) != 4 {
				t.Errorf("the sidebar draws %d remove buttons, want 4: %v", len(rhythm.RemoveSizes), rhythm.RemoveSizes)
			}
			for name, size := range rhythm.RemoveSizes {
				if size.Width < 24 || size.Height < 24 {
					t.Errorf("%q is %vx%v, under a 24px target", name, size.Width, size.Height)
				}
			}
		})
	}
}
