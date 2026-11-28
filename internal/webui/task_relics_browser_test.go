package webui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// The Relics section of a task's page, in a real browser, over fakes.

// taskRelicsFake is the phone board with the relic page's reads and a task
// relic read answering links and warnings for the phone task, and counts how
// often it was asked.
func taskRelicsFake(t *testing.T, links []core.TaskRelicLink, warnings []core.Warning) (Options, *atomic.Int64) {
	t.Helper()
	options := newPhoneBoard().options(t)
	specRelicFake(t).apply(&options)
	reads := &atomic.Int64{}
	options.TaskRelics = func(_ context.Context, taskID string) ([]core.TaskRelicLink, []core.Warning, error) {
		reads.Add(1)
		if taskID != phoneTaskID {
			return nil, nil, nil
		}
		return slices.Clone(links), slices.Clone(warnings), nil
	}
	return options, reads
}

// taskRelicLinks is a link from a whole relic and one from a part of it.
func taskRelicLinks() []core.TaskRelicLink {
	removed := pageRelicStamp
	return []core.TaskRelicLink{
		{Relic: pageRelicID, Title: "Compaction spec", Type: "spec", Link: "l1", Rel: "specifies", Retired: true},
		{Relic: pageRelicID, Title: hostileText, Type: hostileText, Part: pagePartHTML, PartType: "page", Link: "l2",
			Rel: hostileText, Deleted: true, RemovedAt: &removed},
	}
}

const taskRelicHelpers = relicPageHelpers + `
const taskRelics = () => document.querySelector("[data-task-relics]");
const waitForTaskRelics = () => waitFor(() => taskRelics() && taskRelics().isConnected && taskRelics().dataset.taskRelicsState === "ready",
  "the task's relics to be read");
const taskRelicRows = () => [...document.querySelectorAll("[data-task-relic]")].map((row) => ({
  text: row.textContent,
  href: row.querySelector("a") ? row.querySelector("a").getAttribute("href") : "",
  badges: [...row.querySelectorAll("[data-relic-badge]")].map((badge) => badge.textContent),
}));
`

type taskRelicRow struct {
	Text, Href string
	Badges     []string
}

func TestBrowserTaskPageListsItsRelics(t *testing.T) {
	t.Parallel()
	options, _ := taskRelicsFake(t, taskRelicLinks(),
		[]core.Warning{{Code: "relic-links-unread", Message: "One relic's links could not be read " + hostileText}})
	var result struct {
		Shown                     bool
		Heading, Warning, Order   string
		Rows                      []taskRelicRow
		Strays                    []string
		Hostile                   bool
		Address, Back             string
		Current                   []string
		Top, Height               float64
		BackRows                  int
		RelicsAfterThreadInHeader bool
	}
	openInBrowser(t, NewHandler(options), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID,
		BeforeLoad: hostileBeforeLoad, Script: taskRelicHelpers + `
(async () => {
  await waitForRelicState("ready");
  [...document.querySelectorAll("[data-relic-link] a")].find((anchor) => anchor.getAttribute("href") === "/tasks/` + phoneTaskID + `").click();
  await waitForTaskRelics();
  const section = taskRelics();
  const result = {
    shown: section.getClientRects().length > 0,
    heading: section.querySelector("h3").textContent,
    warning: [...section.querySelectorAll("[data-task-relics-warning]")].map((note) => note.textContent).join("|"),
    order: [...section.parentElement.children].map((child) => child.dataset.taskPanel || child.className).join(","),
    rows: taskRelicRows(),
    strays: [...section.querySelectorAll("*")].map((element) => element.tagName)
      .filter((tag) => !["H3", "UL", "LI", "A", "SPAN", "P", "TIME"].includes(tag)),
    hostile: window.__hostile !== undefined || window.__relicWatch.events.length > 0,
  };
  [...section.querySelectorAll("[data-task-relic] a")].find((anchor) => anchor.getAttribute("href").includes("/parts/")).click();
  await waitForRelicState("ready");
  const part = partSection("` + pagePartHTML + `");
  await waitFor(() => part.querySelector("[data-body-state]").dataset.bodyState === "drawn", "the part's body");
  const rect = part.getBoundingClientRect();
  result.address = location.pathname;
  result.current = [...document.querySelectorAll("[data-relic-part][aria-current=\"true\"]")].map((element) => element.dataset.relicPart);
  result.top = rect.top;
  result.height = innerHeight;
  history.back();
  await waitFor(() => location.pathname === "/tasks/` + phoneTaskID + `", "Back to the task");
  await waitForTaskRelics();
  result.back = location.pathname;
  result.backRows = taskRelicRows().length;
  return result;
})()`}, &result)
	if !result.Shown || result.Heading != "Relics" {
		t.Errorf("section shown %v, heading %q", result.Shown, result.Heading)
	}
	if !strings.Contains(result.Order, "comments,task-relics,task-history") {
		t.Errorf("the section is not below the thread: %q", result.Order)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %+v", result.Rows)
	}
	whole, part := result.Rows[0], result.Rows[1]
	if whole.Href != "/relics/"+pageRelicID {
		t.Errorf("the whole relic's row links to %q", whole.Href)
	}
	for _, want := range []string{"Compaction spec", "specifies", "spec"} {
		if !strings.Contains(whole.Text, want) {
			t.Errorf("the whole relic's row %q does not say %q", whole.Text, want)
		}
	}
	if !slices.Equal(whole.Badges, []string{"Retired"}) {
		t.Errorf("the retired relic's marks = %v", whole.Badges)
	}
	if part.Href != "/relics/"+pageRelicID+"/parts/"+pagePartHTML {
		t.Errorf("the part's row links to %q", part.Href)
	}
	shown := strings.ReplaceAll(hostileText, "‮", " ")
	if strings.Count(part.Text, shown) < 3 || !strings.Contains(part.Text, "page") || strings.Contains(part.Text, "‮") {
		t.Errorf("the part's row = %q, want its title, rel, relic type as text and its part type", part.Text)
	}
	if len(part.Badges) != 2 || part.Badges[0] != "Deleted" || !strings.HasPrefix(part.Badges[1], "Removed on ") {
		t.Errorf("the part's marks = %v", part.Badges)
	}
	if !strings.Contains(result.Warning, "could not be read "+shown) {
		t.Errorf("warning note = %q", result.Warning)
	}
	if len(result.Strays) != 0 || result.Hostile {
		t.Errorf("teammate text became markup: strays %v, hostile %v", result.Strays, result.Hostile)
	}
	if want := "/relics/" + pageRelicID + "/parts/" + pagePartHTML; result.Address != want {
		t.Errorf("address = %q, want %q", result.Address, want)
	}
	if !slices.Equal(result.Current, []string{pagePartHTML}) || result.Top < 0 || result.Top >= result.Height {
		t.Errorf("the part is not in view: current %v, top %.0f of %.0f", result.Current, result.Top, result.Height)
	}
	if result.BackRows != 2 {
		t.Errorf("Back to the task page drew %d rows", result.BackRows)
	}
}

func TestBrowserTaskPageReadsRelicsOnce(t *testing.T) {
	t.Parallel()
	options, reads := taskRelicsFake(t, taskRelicLinks(), nil)
	updates := &atomic.Int64{}
	update := options.Update
	options.Update = func(ctx context.Context, id string, input core.UpdateInput) (core.MutationResult, error) {
		updates.Add(1)
		return update(ctx, id, input)
	}
	var result struct {
		Requests, Rows int
	}
	openInBrowser(t, NewHandler(options), browserPage{Width: 1440, Height: 900, Path: "/tasks/" + phoneTaskID, Script: taskRelicHelpers + `
(async () => {
  await waitForTaskRelics();
  const asked = (suffix) => performance.getEntriesByType("resource").filter((entry) => new URL(entry.name).pathname.endsWith(suffix)).length;
  // Two polls of the board after each change, either of which would have read
  // the relics again if a poll did.
  const twoPolls = async () => {
    const polled = asked("/api/tasks");
    await waitFor(() => asked("/api/tasks") >= polled + 2, "two more polls");
  };
  // A change that stays on the page.
  const body = document.querySelector("#comment-body");
  body.value = "Read the spec";
  body.closest("form").requestSubmit();
  await waitFor(() => [...document.querySelectorAll(".comment")].some((comment) => comment.textContent.includes("Read the spec")), "the comment");
  await twoPolls();
  const rows = taskRelicRows().length;
  // And a title edit, whose save returns to the board.
  const title = document.querySelector("#task-title");
  title.value = "A new title";
  title.dispatchEvent(new Event("input", { bubbles: true }));
  title.closest("form").requestSubmit();
  await waitFor(() => location.pathname === "/", "the title to be saved");
  await twoPolls();
  return { requests: asked("/api/tasks/` + phoneTaskID + `/relics"), rows };
})()`}, &result)
	if updates.Load() != 1 {
		t.Errorf("the title edit made %d updates", updates.Load())
	}
	if result.Requests != 1 || reads.Load() != 1 {
		t.Errorf("the relics were requested %d times and read %d times, want once", result.Requests, reads.Load())
	}
	if result.Rows != 2 {
		t.Errorf("rows = %d", result.Rows)
	}
}

// A task nothing links to looks as it did before the section existed. The
// desktop layout recording, which this board's pages are also held to where it
// runs, was made without one.
func TestBrowserTaskPageWithoutRelicsIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, size := range desktopLayoutSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			path := "/tasks/" + phoneTaskID
			without := newPhoneBoard().options(t)
			relicListFake().apply(&without)
			var before string
			openInBrowser(t, NewHandler(without), browserPage{Width: size.width, Height: size.height, Path: path, Script: desktopLayoutScript}, &before)

			// A warning with no links draws nothing either.
			options, reads := taskRelicsFake(t, nil, []core.Warning{{Code: "relic-links-unread", Message: "unread"}})
			var after string
			openInBrowser(t, NewHandler(options), browserPage{Width: size.width, Height: size.height, Path: path,
				Setup: `new Promise((resolve, reject) => {
  const deadline = Date.now() + 15000;
  const check = () => {
    const section = document.querySelector("[data-task-relics]");
    if (section && section.isConnected && section.dataset.taskRelicsState === "ready") {
      return section.hidden && section.getClientRects().length === 0 ? resolve() : reject(new Error("the section is drawn"));
    }
    if (Date.now() > deadline) return reject(new Error("the task's relics were never read"));
    setTimeout(check, 25);
  };
  check();
})`, Script: desktopLayoutScript}, &after)
			if reads.Load() != 1 {
				t.Errorf("the relics were read %d times", reads.Load())
			}
			if before != after {
				t.Errorf("the task page changed; first difference:\n%s", firstLineDifference(before, after))
			}
		})
	}
}

func TestBrowserTaskPageWithoutTaskRelicsMakesNoRequest(t *testing.T) {
	t.Parallel()
	options := newPhoneBoard().options(t)
	relicListFake().apply(&options)
	recorder := &requestRecorder{}
	var result struct {
		Sections int
	}
	openInBrowser(t, guardedRecordedBoard(options, recorder), browserPage{Width: 1440, Height: 900, Path: "/tasks/" + phoneTaskID, Script: pageGeometryHelpers + `
(async () => {
  await waitFor(() => document.querySelector("[data-change-list]") && document.querySelector(".comment"), "the task page");
  const polls = () => performance.getEntriesByType("resource").filter((entry) => new URL(entry.name).pathname === "/api/tasks").length;
  const polled = polls();
  await waitFor(() => polls() >= polled + 1, "a poll");
  return { sections: document.querySelectorAll("[data-task-relics]").length };
})()`}, &result)
	if result.Sections != 0 {
		t.Errorf("a board without a task relic read serves %d relic sections", result.Sections)
	}
	for _, seen := range recorder.seen() {
		if strings.Contains(seen, "relics") {
			t.Errorf("a board without a task relic read was asked %s", seen)
		}
	}
}

// The section is served only by a board that can both read a task's relics and
// show the relic its rows link to.
func TestTaskRelicsSectionIsServedWithItsReads(t *testing.T) {
	t.Parallel()
	const section = `data-task-relics`
	served := func(options Options) bool {
		body := request(t, NewHandler(options), http.MethodGet, "/tasks/"+phoneTaskID).Body.String()
		return strings.Contains(body, "<section class=\"task-relics\" "+section)
	}
	both, _ := taskRelicsFake(t, nil, nil)
	if !served(both) {
		t.Error("a board with both reads serves no section")
	}
	noPages := both
	noPages.ShowRelic = nil
	if served(noPages) {
		t.Error("a board that cannot show a relic serves the section")
	}
	noLinks := both
	noLinks.TaskRelics = nil
	if served(noLinks) {
		t.Error("a board without a task relic read serves the section")
	}
}

func TestBrowserTaskPageReportsAFailedRelicRead(t *testing.T) {
	t.Parallel()
	options, _ := taskRelicsFake(t, nil, nil)
	options.TaskRelics = func(context.Context, string) ([]core.TaskRelicLink, []core.Warning, error) {
		return nil, nil, core.Errorf(core.CategoryCorruptData, "relic %s cannot be read; run workbook validate", pageRelicID)
	}
	var result struct {
		Shown bool
		Text  string
	}
	openInBrowser(t, NewHandler(options), browserPage{Width: 1440, Height: 900, Path: "/tasks/" + phoneTaskID, Script: taskRelicHelpers + `
(async () => {
  await waitFor(() => taskRelics() && taskRelics().dataset.taskRelicsState === "error", "the failed read");
  return { shown: taskRelics().getClientRects().length > 0, text: taskRelics().textContent };
})()`}, &result)
	if !result.Shown || !strings.Contains(result.Text, pageRelicID) || !strings.Contains(result.Text, "workbook validate") {
		t.Errorf("shown %v: %q", result.Shown, result.Text)
	}
}

// A relic read that lands after the reader has moved to another task draws
// nothing there.
func TestBrowserTaskPageDropsAnotherTasksRelics(t *testing.T) {
	t.Parallel()
	options, _ := taskRelicsFake(t, nil, nil)
	options.TaskRelics = func(_ context.Context, taskID string) ([]core.TaskRelicLink, []core.Warning, error) {
		if taskID != phoneTaskID {
			return nil, nil, nil
		}
		time.Sleep(700 * time.Millisecond)
		return taskRelicLinks(), nil, nil
	}
	var result struct {
		Rows   int
		Hidden bool
	}
	openInBrowser(t, NewHandler(options), browserPage{Width: 1440, Height: 900, Path: "/tasks/" + phoneTaskID, Script: taskRelicHelpers + `
(async () => {
  const slow = "/api/tasks/` + phoneTaskID + `/relics";
  await waitFor(() => taskRelics() && taskRelics().isConnected && taskRelics().dataset.taskRelicsState === "loading", "the first read to start");
  history.pushState({}, "", "/tasks/` + phoneWideID + `");
  window.dispatchEvent(new PopStateEvent("popstate"));
  await waitForTaskRelics();
  await waitFor(() => performance.getEntriesByType("resource").some((entry) => entry.name.endsWith(slow)), "the first read to land");
  await new Promise((resolve) => setTimeout(resolve, 100));
  return { rows: taskRelicRows().length, hidden: taskRelics().hidden };
})()`}, &result)
	if result.Rows != 0 || !result.Hidden {
		t.Errorf("the other task shows %d rows, hidden %v", result.Rows, result.Hidden)
	}
}

func TestBrowserTaskPageRelicsFitPhone(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("unbroken", 40)
	links := []core.TaskRelicLink{
		{Relic: pageRelicID, Title: long, Type: long, Link: "l1", Rel: long, Retired: true},
		{Relic: pageRelicID, Title: "Compaction spec", Type: "spec", Part: pagePartHTML, PartType: long, Link: "l2", Rel: "specifies"},
	}
	options, _ := taskRelicsFake(t, links, []core.Warning{{Code: "relic-links-unread", Message: long}})
	var result struct {
		ScrollWidth, InnerWidth, Rows int
		Widest                        float64
	}
	openInBrowser(t, NewHandler(options), browserPage{Width: 390, Height: 844, Phone: true, Path: "/tasks/" + phoneTaskID, Script: taskRelicHelpers + `
(async () => {
  await waitForTaskRelics();
  const elements = [taskRelics(), ...taskRelics().querySelectorAll("*")];
  return {
    scrollWidth: document.documentElement.scrollWidth, innerWidth, rows: taskRelicRows().length,
    widest: Math.max(...elements.map((element) => element.getBoundingClientRect().right)),
  };
})()`}, &result)
	if result.Rows != 2 {
		t.Errorf("rows = %d", result.Rows)
	}
	if result.ScrollWidth > result.InnerWidth || result.Widest > float64(result.InnerWidth) {
		t.Errorf("the page is %d wide and the section reaches %.0f, in %d", result.ScrollWidth, result.Widest, result.InnerWidth)
	}
}
