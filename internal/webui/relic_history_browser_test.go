package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// A relic's history and its diffs, in a real browser, over fakes.

// historyFake answers the history and diff reads beside a relic page fake,
// and records what the diff was asked for.
type historyFake struct {
	history core.RelicHistory
	diff    core.RelicDiff
	diffErr error

	mu    sync.Mutex
	diffs []historyDiffCall
}

type historyDiffCall struct {
	Address  string
	Versions []string
}

func (fake *historyFake) apply(options *Options) {
	options.RelicHistory = func(_ context.Context, address string, asked core.RelicHistoryOptions) (core.RelicHistory, error) {
		history := fake.history
		if _, part, found := strings.Cut(address, "#"); found {
			history.Part = part
		}
		if !asked.All && len(history.Changes) > asked.Limit {
			history.Changes = history.Changes[len(history.Changes)-asked.Limit:]
		}
		history.Showing = len(history.Changes)
		return history, nil
	}
	options.RelicDiff = func(_ context.Context, address string, versions []string, _ core.RelicDiffOptions) (core.RelicDiff, error) {
		fake.mu.Lock()
		fake.diffs = append(fake.diffs, historyDiffCall{address, versions})
		fake.mu.Unlock()
		if fake.diffErr != nil {
			return core.RelicDiff{}, fake.diffErr
		}
		diff := fake.diff
		if _, part, found := strings.Cut(address, "#"); found {
			diff.Part = part
		}
		return diff, nil
	}
}

// handler is a board that reads the spec relic, its history and its diffs.
func (fake *historyFake) handler(t *testing.T) http.Handler {
	t.Helper()
	options := newPhoneBoard().options(t)
	specRelicFake(t).apply(&options)
	fake.apply(&options)
	return NewHandler(options)
}

func historyVersion(index int) string { return fmt.Sprintf("01M8EVZ9BQ20NDMR0F09K%05d", index) }

func historyChange(index int) core.RelicChange {
	return core.RelicChange{
		Commit: fmt.Sprintf("commit-%03d", index), Actor: fmt.Sprintf("writer%d@example.com", index),
		WallTime: pageRelicStamp.Add(time.Duration(index) * time.Minute), LogicalClock: uint64(index),
		Summary: fmt.Sprintf("title.set %d", index),
		Operations: []core.RelicChangeOperation{{RelicOperation: core.RelicOperation{
			ID: historyVersion(index), Type: core.RelicOperationTitleSet, Title: fmt.Sprintf("Title %d", index),
		}}},
	}
}

// historyOf is a history of count changes, oldest first as core lists them.
func historyOf(count int) core.RelicHistory {
	history := core.RelicHistory{Relic: core.RelicSummary{ID: pageRelicID, Title: "Compaction spec", Type: "spec"}, Total: count}
	for index := range count {
		history.Changes = append(history.Changes, historyChange(index+1))
	}
	return history
}

type historyRow struct {
	Version, Text, Compare, CompareText string
}

const historyRows = `
const historyRows = () => [...document.querySelectorAll("[data-relic-change]")].map((row) => {
  const compare = row.querySelector("a[data-relic-compare]");
  return {
    version: row.dataset.relicChange, text: row.textContent,
    compare: compare ? compare.getAttribute("href") : "", compareText: compare ? compare.textContent : "",
  };
});
const diffRoot = () => document.querySelector("[data-diff-state]");
const waitForDiffState = (want) => waitFor(() => diffRoot() && diffRoot().dataset.diffState === want,
  "the diff to be " + want + " (it is " + (diffRoot() && diffRoot().dataset.diffState) + ")");
`

func TestBrowserRelicHistoryListsChangesNewestFirst(t *testing.T) {
	t.Parallel()
	fake := &historyFake{history: historyOf(120), diff: core.RelicDiff{Relic: pageRelicID,
		From: &core.RelicDiffPoint{Version: historyVersion(3)}, To: core.RelicDiffPoint{Version: historyVersion(7)}}}
	var result struct {
		Windowed            []historyRow
		AllVersions         []string
		Oldest              historyRow
		AllAddress, Address string
		ShowAllText         string
		Picked              string
		Gone                bool
	}
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID[:12] + "/history", Script: relicPageHelpers + historyRows + `
(async () => {
  await waitForRelicState("ready");
  const address = location.pathname + location.search;
  const windowed = historyRows();
  const more = document.querySelector("a[data-relic-show-all]");
  const showAllText = more.textContent;
  more.click();
  await waitFor(() => location.search === "?all=1" && relicState() === "ready" && historyRows().length > 100, "every change");
  // Every row at once is more than the driver reads back in one answer.
  const every = historyRows();
  const allVersions = every.map((row) => row.version);
  const oldest = every[every.length - 1];
  const allAddress = location.pathname + location.search;
  const gone = !document.querySelector("a[data-relic-show-all]");
  const pick = (side, version) => {
    const radio = document.querySelector("[data-relic-change=\"" + version + "\"] input[data-relic-pick=\"" + side + "\"]");
    radio.click();
  };
  pick("from", "` + historyVersion(3) + `");
  pick("to", "` + historyVersion(7) + `");
  await waitFor(() => location.pathname.endsWith("/diff"), "the chosen pair's diff");
  await waitForDiffState("ready");
  return { address, windowed, showAllText, allVersions, oldest, allAddress, gone, picked: location.pathname + location.search };
})()`}, &result)
	if want := "/relics/" + pageRelicID + "/history"; result.Address != want {
		t.Errorf("address = %q, want %q", result.Address, want)
	}
	check := func(rows []historyRow, newest, oldest int) {
		t.Helper()
		if len(rows) != newest-oldest+1 {
			t.Fatalf("%d rows, want %d", len(rows), newest-oldest+1)
		}
		for position, row := range rows {
			index := newest - position
			if row.Version != historyVersion(index) {
				t.Fatalf("row %d is %s, want change %d", position, row.Version, index)
			}
			for _, want := range []string{fmt.Sprintf("writer%d@example.com", index), fmt.Sprintf("title.set %d", index), "2026"} {
				if !strings.Contains(row.Text, want) {
					t.Errorf("row %d lacks %q: %s", position, want, row.Text)
				}
			}
			wantHref, wantText := "/relics/"+pageRelicID+"/diff?from="+historyVersion(index-1)+"&to="+historyVersion(index), "Compare with the change before"
			if index == oldest {
				wantHref, wantText = "/relics/"+pageRelicID+"/diff?from="+historyVersion(index), "Compare with the newest"
			}
			if row.Compare != wantHref || row.CompareText != wantText {
				t.Errorf("row %d compares with %q %q, want %q %q", position, row.Compare, row.CompareText, wantHref, wantText)
			}
		}
	}
	check(result.Windowed, 120, 21)
	if len(result.AllVersions) != 120 || result.AllVersions[0] != historyVersion(120) || result.AllVersions[119] != historyVersion(1) {
		t.Errorf("all=1 lists %d changes: %v", len(result.AllVersions), result.AllVersions)
	}
	check([]historyRow{result.Oldest}, 1, 1)
	if !strings.Contains(result.ShowAllText, "Show all") || !result.Gone || result.AllAddress != "/relics/"+pageRelicID+"/history?all=1" {
		t.Errorf("show all: %q, gone %t, address %q", result.ShowAllText, result.Gone, result.AllAddress)
	}
	if want := "/relics/" + pageRelicID + "/diff?from=" + historyVersion(3) + "&to=" + historyVersion(7); result.Picked != want {
		t.Errorf("the radios went to %q, want %q", result.Picked, want)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.diffs) == 0 || !slices.Equal(fake.diffs[0].Versions, []string{historyVersion(3), historyVersion(7)}) {
		t.Errorf("diffs asked for %+v", fake.diffs)
	}
}

func TestBrowserRelicHistoryIsReachedFromTheRelicPage(t *testing.T) {
	t.Parallel()
	history := historyOf(1)
	fake := &historyFake{history: history, diff: core.RelicDiff{Relic: pageRelicID, To: core.RelicDiffPoint{Version: historyVersion(1)}}}
	var result struct {
		Whole, Part   string
		Rows          []historyRow
		Operations    string
		Created       string
		BackToRelic   string
		PartLinkCount int
	}
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID, Script: relicPageHelpers + historyRows + `
(async () => {
  await waitForRelicState("ready");
  const partLinks = [...document.querySelectorAll("a[data-relic-part-history]")];
  const part = partSection("` + pagePartHTML + `").querySelector("a[data-relic-part-history]").getAttribute("href");
  const link = document.querySelector("a[data-relic-history]");
  const whole = link.getAttribute("href");
  link.click();
  await waitFor(() => location.pathname.endsWith("/history"), "the history");
  await waitForRelicState("ready");
  const rows = historyRows();
  const operations = document.querySelector("[data-relic-change] [data-relic-operations]").textContent;
  const back = document.querySelector("a[data-relic-back]").getAttribute("href");
  document.querySelector("a[data-relic-compare]").click();
  await waitFor(() => location.pathname.endsWith("/diff"), "the creation's diff");
  await waitForDiffState("ready");
  return { whole, part, rows, operations, backToRelic: back, created: location.pathname + location.search, partLinkCount: partLinks.length };
})()`}, &result)
	if want := "/relics/" + pageRelicID + "/history"; result.Whole != want {
		t.Errorf("the relic's history link = %q, want %q", result.Whole, want)
	}
	if want := "/relics/" + pageRelicID + "/history?part=" + pagePartHTML; result.Part != want || result.PartLinkCount != 3 {
		t.Errorf("the part's history link = %q (%d links), want %q", result.Part, result.PartLinkCount, want)
	}
	if len(result.Rows) != 1 || !strings.Contains(result.Operations, "title.set") || !strings.Contains(result.Operations, "Title 1") {
		t.Errorf("rows = %+v, operations %q", result.Rows, result.Operations)
	}
	if result.BackToRelic != "/relics/"+pageRelicID {
		t.Errorf("the history's way back = %q", result.BackToRelic)
	}
	// The only change is drawn as a diff by the route that names no version.
	if want := "/relics/" + pageRelicID + "/diff"; result.Created != want {
		t.Errorf("the only change's diff = %q, want %q", result.Created, want)
	}
}

func TestBrowserRelicHistoryOfACompactedRelicSaysWhereItStarts(t *testing.T) {
	t.Parallel()
	checkpoint := historyVersion(900)
	history := historyOf(2)
	history.Compacted = &core.RelicHistoryCompaction{Version: checkpoint, RunID: "run", At: time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC),
		Boundary: "retired", Changes: 5, From: pageRelicStamp.Add(-48 * time.Hour), To: pageRelicStamp.Add(-24 * time.Hour)}
	fake := &historyFake{history: history, diff: core.RelicDiff{Relic: pageRelicID, To: core.RelicDiffPoint{Version: historyVersion(2)}}}
	script := relicPageHelpers + historyRows + `
(async () => {
  await waitForRelicState("ready");
  const checkpoint = document.querySelector("[data-relic-checkpoint]");
  return { rows: historyRows(), note: checkpoint ? checkpoint.textContent : "", year: new Date("2026-10-03T12:00:00Z").toLocaleString() };
})()`
	type answer struct {
		Rows       []historyRow
		Note, Year string
	}
	var result answer
	openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID + "/history", Script: script}, &result)
	if len(result.Rows) != 3 || result.Rows[2].Version != checkpoint {
		t.Fatalf("rows = %+v, want two changes and the checkpoint", result.Rows)
	}
	for _, want := range []string{"Earlier changes were dropped when this relic was compacted", result.Year, "5"} {
		if !strings.Contains(result.Note, want) {
			t.Errorf("the checkpoint row lacks %q: %s", want, result.Note)
		}
	}
	base := "/relics/" + pageRelicID + "/diff?"
	if result.Rows[1].Compare != base+"from="+checkpoint+"&to="+historyVersion(1) || result.Rows[1].CompareText != "Compare with the change before" {
		t.Errorf("the change above the checkpoint compares with %q", result.Rows[1].Compare)
	}
	if result.Rows[2].Compare != base+"from="+checkpoint || result.Rows[2].CompareText != "Compare with the newest" {
		t.Errorf("the checkpoint compares with %q %q", result.Rows[2].Compare, result.Rows[2].CompareText)
	}

	// A window that leaves changes out does not reach the checkpoint.
	windowed := historyOf(101)
	windowed.Compacted = history.Compacted
	var cut answer
	openInBrowser(t, (&historyFake{history: windowed}).handler(t),
		browserPage{Width: 1440, Height: 900, Path: "/relics/" + pageRelicID + "/history", Script: script}, &cut)
	if len(cut.Rows) != 100 || cut.Note != "" {
		t.Errorf("a window of %d rows draws the checkpoint %q", len(cut.Rows), cut.Note)
	}
}

func relicBody(format string, bytes int64) *core.RelicBody {
	return &core.RelicBody{Format: format, Version: "v", Bytes: bytes}
}

// richDiff differs in every way a diff can.
func richDiff(t *testing.T) core.RelicDiff {
	t.Helper()
	return core.RelicDiff{
		Relic:   pageRelicID,
		From:    &core.RelicDiffPoint{Version: historyVersion(1), Actor: "ana@example.com", WallTime: pageRelicStamp},
		To:      core.RelicDiffPoint{Version: historyVersion(2), Actor: "bo@example.com", WallTime: pageRelicStamp.Add(time.Hour)},
		Changes: []core.RelicValueChange{{Name: "title", Kind: core.ChangeSet, From: rawJSON(t, "Old title"), To: rawJSON(t, "New title")}},
		Properties: []core.RelicValueChange{
			{Name: "status", Kind: core.ChangeAdded, To: rawJSON(t, []string{"draft"})},
		},
		Parts: []core.RelicPartDiff{
			{ID: pagePartMD, Type: "section", Kind: core.RelicPartChanged,
				Moved:  &core.RelicPartMove{From: "a", To: "c", FromIndex: 0, ToIndex: 2},
				Fields: []core.RelicValueChange{{Name: "heading", Kind: core.ChangeSet, From: rawJSON(t, "Aims"), To: rawJSON(t, "Goals")}},
				Body: &core.RelicBodyDiff{From: relicBody("md", 20), To: relicBody("md", 22), Text: true, Approximate: true,
					Hunks: []core.RelicDiffHunk{{FromStart: 7, FromLines: 3, ToStart: 9, ToLines: 3, Lines: []core.RelicDiffLine{
						{Kind: core.DiffEqual, Text: "same", From: 7, To: 9},
						{Kind: core.DiffDelete, Text: "old line", From: 8},
						{Kind: core.DiffInsert, Text: "new line", To: 10},
						{Kind: core.DiffInsert, Text: "added line", To: 11},
						{Kind: core.DiffDelete, Text: "last line", From: 9, NoNewline: true},
					}}}}},
			{ID: pagePartBin, Type: "blob", Kind: core.RelicPartChanged,
				Body: &core.RelicBodyDiff{From: relicBody("bin", 3), To: relicBody("bin", 5)}},
		},
		Links: &core.RelicLinksDiff{
			Added: []core.RelicLink{{ID: "l2", To: core.RelicLinkTarget{Task: pageRelicTask}, Rel: "tracks"}},
			Removed: []core.RelicLink{{ID: "l1", From: pagePartMD, To: core.RelicLinkTarget{Relic: pageRelicPlan}, Rel: "follows",
				Pointer: json.RawMessage(`{"lines":"pointer-in-the-diff"}`)}},
		},
	}
}

type diffLineCell struct {
	From, To, Marker, Text, Kind string
}

const diffHelpers = `
const hunkRows = () => [...document.querySelectorAll("[data-hunk] tr")].map((row) => {
  const cell = (selector) => { const found = row.querySelector(selector); return found ? found.textContent : ""; };
  return [...row.querySelectorAll("[data-line-text]")].map((text) => ({
    kind: text.dataset.lineText,
    from: cell("[data-line-number=\"from\"]"), to: cell("[data-line-number=\"to\"]"),
    marker: text.previousElementSibling.textContent,
    text: text.textContent,
    left: Math.round(text.getBoundingClientRect().left),
  }));
});
const mode = () => diffRoot().dataset.diffMode;
const toggle = (name) => document.querySelector("button[data-diff-toggle=\"" + name + "\"]");
`

type drawnLine struct {
	Kind, From, To, Marker, Text string
	Left                         int
}

func TestBrowserRelicDiffDrawsHunksSideBySideAndInline(t *testing.T) {
	t.Parallel()
	address := "/relics/" + pageRelicID + "/diff?from=" + historyVersion(1) + "&to=" + historyVersion(2)
	read := relicPageHelpers + historyRows + diffHelpers + `
(async () => {
  await waitForRelicState("ready");
  await waitForDiffState("ready");
  const text = diffRoot().textContent.replace(/\s+/g, " ");
  return { mode: mode(), rows: hunkRows(), text, toggleShown: !!toggle("inline") && toggle("inline").offsetParent !== null,
    links: [...diffRoot().querySelectorAll("[data-relic-link]")].map((item) => item.textContent),
    hrefs: [...diffRoot().querySelectorAll("[data-relic-link] a")].map((anchor) => anchor.getAttribute("href")) };
})()`
	type answer struct {
		Mode, Text  string
		Rows        [][]drawnLine
		ToggleShown bool
		Links       []string
		Hrefs       []string
	}
	open := func(t *testing.T, fake *historyFake, page browserPage) answer {
		t.Helper()
		var result answer
		openInBrowser(t, fake.handler(t), page, &result)
		return result
	}
	t.Run("wide", func(t *testing.T) {
		t.Parallel()
		result := open(t, &historyFake{diff: richDiff(t)}, browserPage{Width: 1440, Height: 900, Path: address, Script: read})
		if result.Mode != "side" || !result.ToggleShown {
			t.Errorf("mode = %q, toggle shown %t", result.Mode, result.ToggleShown)
		}
		// Each delete is paired with the insert across from it.
		var rows []string
		for _, row := range result.Rows {
			var sides []string
			for _, side := range row {
				sides = append(sides, side.Kind+" "+side.Marker+" "+side.Text)
			}
			rows = append(rows, strings.Join(sides, " | "))
		}
		want := []string{
			"equal   same | equal   same",
			"delete - old line | insert + new line",
			"empty    | insert + added line",
			"delete - last lineNo newline at end | empty   ",
		}
		if !slices.Equal(rows, want) {
			t.Errorf("rows =\n%q\nwant\n%q", rows, want)
		}
		if len(result.Rows) == 4 {
			first := result.Rows[0]
			if first[0].Left >= first[1].Left {
				t.Errorf("the hunk is not two columns: %d and %d", first[0].Left, first[1].Left)
			}
			numbers := [][2]string{{"7", "9"}, {"8", "10"}, {"", "11"}, {"9", ""}}
			for index, row := range result.Rows {
				if row[0].From != numbers[index][0] || row[0].To != numbers[index][1] {
					t.Errorf("row %d numbers = %q %q, want %v", index, row[0].From, row[0].To, numbers[index])
				}
			}
		}
		for _, want := range []string{
			"Old title", "New title", "status", "(none)", "draft", "Aims", "Goals", "moved from 0 to 2", "approximate",
			"not text: 3 B → 5 B", "Links added", "tracks", "Links removed", "follows", "ana@example.com", "bo@example.com",
		} {
			if !strings.Contains(result.Text, want) {
				t.Errorf("the diff lacks %q: %s", want, result.Text)
			}
		}
		if len(result.Links) != 2 {
			t.Errorf("links drawn = %q", result.Links)
		}
		if strings.Count(result.Text, "pointer-in-the-diff") != 1 || strings.Count(result.Text, "from part "+pagePartMD) != 1 {
			t.Errorf("the removed link's pointer and source part are not each drawn once: %s", result.Text)
		}
		if !slices.Equal(result.Hrefs, []string{"/tasks/" + pageRelicTask, "/relics/" + pageRelicPlan}) {
			t.Errorf("the diff's links go to %q", result.Hrefs)
		}
	})
	t.Run("narrow", func(t *testing.T) {
		t.Parallel()
		result := open(t, &historyFake{diff: richDiff(t)}, browserPage{Width: 390, Height: 844, Phone: true, Path: address, Script: read})
		if result.Mode != "inline" || result.ToggleShown {
			t.Errorf("mode = %q, toggle shown %t", result.Mode, result.ToggleShown)
		}
		var rows []string
		for _, row := range result.Rows {
			if len(row) != 1 {
				t.Fatalf("a row has %d text columns, want 1", len(row))
			}
			line := row[0]
			rows = append(rows, strings.Join([]string{line.From, line.To, line.Marker, line.Text}, ":"))
		}
		want := []string{"7:9: :same", "8::-:old line", ":10:+:new line", ":11:+:added line", "9::-:last lineNo newline at end"}
		if !slices.Equal(rows, want) {
			t.Errorf("rows =\n%q\nwant\n%q", rows, want)
		}
	})
	choose := relicPageHelpers + historyRows + diffHelpers + `
(async () => { await waitForRelicState("ready"); await waitForDiffState("ready"); toggle("inline").click(); await waitFor(() => mode() === "inline", "inline"); })()`
	t.Run("the choice is kept", func(t *testing.T) {
		t.Parallel()
		result := open(t, &historyFake{diff: richDiff(t)}, browserPage{Width: 1440, Height: 900, Path: address, Setup: choose, Reload: true, Script: read})
		if result.Mode != "inline" || len(result.Rows) != 5 {
			t.Errorf("after a reload the mode is %q with %d rows", result.Mode, len(result.Rows))
		}
	})
	t.Run("a browser that refuses storage", func(t *testing.T) {
		t.Parallel()
		var result struct{ First, Chosen, Back string }
		openInBrowser(t, (&historyFake{diff: richDiff(t)}).handler(t), browserPage{Width: 1440, Height: 900, Path: address,
			BeforeLoad: `Object.defineProperty(window, "localStorage", { configurable: true, get() { throw new Error("refused"); } });`,
			Script: relicPageHelpers + historyRows + diffHelpers + `
(async () => {
  await waitForRelicState("ready");
  await waitForDiffState("ready");
  const first = mode();
  toggle("inline").click();
  const chosen = mode();
  toggle("side").click();
  return { first, chosen, back: mode() };
})()`}, &result)
		if result.First != "side" || result.Chosen != "inline" || result.Back != "side" {
			t.Errorf("modes = %+v", result)
		}
	})
	for _, same := range []struct{ name, path, want string }{
		{"the same relic", address, "These two versions hold the same relic."},
		{"the same part", address + "&part=" + pagePartMD, "These two versions hold the same part."},
	} {
		t.Run(same.name, func(t *testing.T) {
			t.Parallel()
			diff := richDiff(t)
			diff.Changes, diff.Properties, diff.Parts, diff.Links = nil, nil, nil, nil
			result := open(t, &historyFake{diff: diff}, browserPage{Width: 1440, Height: 900, Path: same.path, Script: read})
			if !strings.Contains(result.Text, same.want) {
				t.Errorf("text = %q, want %q", result.Text, same.want)
			}
		})
	}
	t.Run("a refused version", func(t *testing.T) {
		t.Parallel()
		fake := &historyFake{diffErr: core.Errorf(core.CategoryNotFound, "change X of relic %s was dropped when it was compacted", pageRelicID)}
		var result struct{ Notice, State string }
		openInBrowser(t, fake.handler(t), browserPage{Width: 1440, Height: 900, Path: address, Script: relicPageHelpers + historyRows + `
(async () => {
  await waitForRelicState("not-found");
  return { notice: relicView().querySelector("[data-relic-notice]").textContent, state: diffRoot().dataset.diffState };
})()`}, &result)
		if !strings.Contains(result.Notice, "was dropped when it was compacted") || result.State != "not-found" {
			t.Errorf("notice %q, diff state %q", result.Notice, result.State)
		}
	})
}

func TestBrowserRelicDiffDrawsHostileLinesAsText(t *testing.T) {
	t.Parallel()
	hostile := rawJSON(t, hostileText)
	diff := core.RelicDiff{
		Relic:   pageRelicID,
		From:    &core.RelicDiffPoint{Version: historyVersion(1), Actor: hostileText},
		To:      core.RelicDiffPoint{Version: historyVersion(2), Actor: hostileText},
		Changes: []core.RelicValueChange{{Name: hostileText, Kind: core.ChangeSet, From: hostile, To: hostile}},
		Parts: []core.RelicPartDiff{{ID: pagePartMD, Type: hostileText, Kind: core.RelicPartChanged,
			Body: &core.RelicBodyDiff{From: relicBody(hostileText, 1), To: relicBody(hostileText, 1), Text: true,
				Hunks: []core.RelicDiffHunk{{Lines: []core.RelicDiffLine{
					{Kind: core.DiffDelete, Text: hostileText, From: 1}, {Kind: core.DiffInsert, Text: hostileText, To: 1},
				}}}}}},
		Links: &core.RelicLinksDiff{Added: []core.RelicLink{{ID: "l", To: core.RelicLinkTarget{Relic: "javascript:window.__hostile=9"}, Rel: hostileText}}},
	}
	history := historyOf(2)
	history.Relic.Title = hostileText
	for index := range history.Changes {
		history.Changes[index].Actor, history.Changes[index].Summary = hostileText, hostileText
		history.Changes[index].Operations[0].Title = hostileText
		history.Changes[index].Operations[0].Name = hostileText
	}
	tags, err := json.Marshal(append(slices.Clone(relicViewTags), "SMALL"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/relics/" + pageRelicID + "/diff", "/relics/" + pageRelicID + "/history"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			var result struct {
				Watch     struct{ Errors, Events, Violations []string }
				Hostile   any
				Strays    []string
				Anchors   []string
				Resources []string
				Text      string
				Title     string
			}
			openInBrowser(t, (&historyFake{history: history, diff: diff}).handler(t), browserPage{Width: 1440, Height: 900, Path: path,
				BeforeLoad: hostileBeforeLoad, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  const allowed = ` + string(tags) + `.concat(["TABLE", "TBODY", "TR", "TD", "COLGROUP", "COL"]);
  const view = relicView();
  return {
    watch: window.__relicWatch,
    hostile: window.__hostile === undefined ? null : window.__hostile,
    strays: [...view.querySelectorAll("*")].filter((element) => !allowed.includes(element.tagName)).map((element) => element.tagName),
    anchors: [...view.querySelectorAll("a")].map((anchor) => anchor.getAttribute("href")),
    resources: performance.getEntriesByType("resource").map((entry) => entry.name),
    text: view.textContent, title: document.title,
  };
})()`}, &result)
			if len(result.Watch.Errors) != 0 || len(result.Watch.Events) != 0 || len(result.Watch.Violations) != 0 || result.Hostile != nil {
				t.Errorf("a hook fired: %+v, window.__hostile = %v", result.Watch, result.Hostile)
			}
			if len(result.Strays) != 0 {
				t.Errorf("the view drew elements outside its list: %v", result.Strays)
			}
			for _, href := range result.Anchors {
				if !strings.HasPrefix(href, "/relics/") && !strings.HasPrefix(href, "/tasks/") {
					t.Errorf("the view links to %q", href)
				}
			}
			for _, resource := range result.Resources {
				if strings.Contains(resource, "hostile") {
					t.Errorf("the page fetched %s", resource)
				}
			}
			if shown := strings.ReplaceAll(hostileText, "‮", " "); !strings.Contains(result.Text, shown) {
				t.Errorf("the hostile text is not drawn as text:\n%s", result.Text)
			}
			if strings.Contains(result.Text, "‮") || strings.Contains(result.Title, "‮") {
				t.Error("a direction override was drawn")
			}
		})
	}
}

func TestBrowserRelicHistoryFitsPhone(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 5000)
	diff := richDiff(t)
	diff.Parts[0].Body.Hunks[0].Lines[0].Text = long
	diff.Changes[0].To = rawJSON(t, long)
	history := historyOf(3)
	history.Changes[1].Summary, history.Changes[1].Actor = long, long
	history.Changes[1].Operations[0].Title = long
	fake := &historyFake{history: history, diff: diff}
	for _, path := range []string{"/relics/" + pageRelicID + "/history", "/relics/" + pageRelicID + "/diff"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			var result struct {
				ScrollWidth, InnerWidth int
				Hunk                    struct{ ScrollWidth, ClientWidth, Right int }
				Unnamed                 []string
				ShownHidden             []string
			}
			openInBrowser(t, fake.handler(t), browserPage{Width: 390, Height: 844, Phone: true, Path: path, Script: relicPageHelpers + `
(async () => {
  await waitForRelicState("ready");
  const hunk = document.querySelector("[data-hunk]");
  const name = (element) => (element.getAttribute("aria-label") || "").trim() ||
    (element.labels && element.labels.length ? [...element.labels].map((label) => label.textContent).join(" ").trim() : "") ||
    element.textContent.trim();
  return {
    scrollWidth: document.documentElement.scrollWidth, innerWidth,
    hunk: hunk ? { scrollWidth: hunk.scrollWidth, clientWidth: hunk.clientWidth, right: Math.round(hunk.getBoundingClientRect().right) } : { scrollWidth: 0, clientWidth: 0, right: 0 },
    unnamed: [...relicView().querySelectorAll("a, button, input")].filter((element) => !name(element)).map((element) => element.outerHTML.slice(0, 120)),
    shownHidden: [...document.querySelectorAll("[hidden]")].filter((element) => getComputedStyle(element).display !== "none").map((element) => element.outerHTML.slice(0, 120)),
  };
})()`}, &result)
			if result.ScrollWidth > result.InnerWidth {
				t.Errorf("the page scrolls sideways: %d > %d", result.ScrollWidth, result.InnerWidth)
			}
			if strings.HasSuffix(path, "/diff") && (result.Hunk.ScrollWidth <= result.Hunk.ClientWidth || result.Hunk.Right > result.InnerWidth) {
				t.Errorf("a long line does not scroll inside its hunk: %+v in %d", result.Hunk, result.InnerWidth)
			}
			if len(result.Unnamed) != 0 {
				t.Errorf("controls without an accessible name: %v", result.Unnamed)
			}
			if len(result.ShownHidden) != 0 {
				t.Errorf("hidden elements are drawn: %v", result.ShownHidden)
			}
		})
	}
}
