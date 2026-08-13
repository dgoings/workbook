package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board's own chrome — what a column header carries and how the stylesheet
// sizes and decorates it. None of it is drawn by the client, so it is asserted
// against the served page rather than through the Node harness: the page is the
// only artifact that exists, and a fake DOM with no layout engine could not read
// a rule out of it anyway.

// boardPage renders the board with the standard task set and returns its HTML.
func boardPage(t *testing.T) string {
	t.Helper()
	tasks := boardTasks()
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return tasks, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// A column header names a status a reader recognizes. The Git ref that status
// is stored under is an implementation detail of the tool, not something the
// reader can act on, and printing six copies of it cost a whole row of every
// header for no decision anyone makes from the board.
func TestHandlerBoardColumnsOmitWorkbookRefPaths(t *testing.T) {
	body := boardPage(t)
	for _, definition := range core.LegacyVocabulary().Definitions() {
		if want := "refs/workbook/status/" + string(definition.Status); strings.Contains(body, want) {
			t.Errorf("GET / body still prints the ref path %q in a column header", want)
		}
	}
	if strings.Contains(body, "ref-label") {
		t.Error("GET / body still carries the ref-path element or its styling")
	}
	// The header is otherwise unchanged: the label, the count, and the link that
	// files a new task under this column all remain.
	for _, fragment := range []string{
		`class="column__header"`,
		`class="count" data-count="ready"`,
		`href="/tasks/new?status=ready"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("GET / body does not contain %q", fragment)
		}
	}
}

// Six columns of underlined titles read as a page of rules. The underline is
// carrying nothing here — a card is one link and the card itself takes focus —
// so the title is plain text that turns blue under the pointer.
func TestHandlerBoardCardTitlesAreNotUnderlined(t *testing.T) {
	body := boardPage(t)
	for _, fragment := range []string{
		`.task-card h3 a { color: #172033; text-decoration: none; }`,
		`.task-card h3 a:hover { color: #2457d6; }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("card title styling does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `.task-card h3 a { color: #172033; text-decoration-color`) {
		t.Error("card titles still draw an underline")
	}
}

// Six columns dividing whatever the window is wide gave an ordinary desktop
// browser a column too narrow to read a task in. The columns keep a minimum
// width instead and the board scrolls sideways when they do not all fit, which
// is the same behavior the narrow-screen rules already relied on.
func TestHandlerBoardColumnsHoldAMinimumWidthAndScroll(t *testing.T) {
	body := boardPage(t)
	for _, fragment := range []string{
		`--board-column-min: 16rem;`,
		`grid-auto-columns: minmax(var(--board-column-min), var(--board-column-max))`,
		`min-width: var(--board-column-min)`,
		`overflow-x: auto`,
		// The phone layout still widens the column to the viewport rather than
		// inheriting the desktop minimum.
		`--board-column-min: min(18rem, calc(100vw - 2.5rem));`,
		// The unknown-status strip draws the same cards, so it takes the same
		// floor: eight of them fell to 12rem tracks while the columns held 16rem.
		`grid-auto-columns: minmax(var(--board-column-min), 18rem)`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("board column styling does not contain %q", fragment)
		}
	}
	// Every place a card is given a minimum width reads the one property. A
	// literal left behind in any of them is how they could disagree again, so
	// the guard covers the shape of the declaration rather than one spelling of
	// it — `minmax(12rem` catches both the old track size and the strip's.
	for _, stale := range []string{`minmax(12rem`, `min-width: 12rem`, `min-width: min(18rem`} {
		if strings.Contains(body, stale) {
			t.Errorf("a board card still carries the hardcoded minimum width %q", stale)
		}
	}
}

// A minimum on its own left the board holding it far past the point the window
// could afford more: five columns fit their minimum at about 1371px and the
// track list named six, so the sixth track — empty, because no project defines
// it — took the width the five would have grown into, and nothing widened until
// about 1640px. The tracks are counted off the columns now and carry a maximum,
// so they widen from the moment the window can afford it and stop at a width a
// card is still readable at.
func TestHandlerBoardColumnsGrowBetweenAMinimumAndAMaximum(t *testing.T) {
	body := boardPage(t)
	rule := boardRules(t, body)
	for _, fragment := range []string{
		// One track per column present, created by the flow rather than counted
		// into a track list the server would have to keep correct.
		`grid-auto-flow: column`,
		`grid-auto-columns: minmax(var(--board-column-min), var(--board-column-max))`,
		// The leftover past the maximum sits on the right, which is also the
		// only alignment that leaves the first column reachable when the board
		// is too wide to fit and scrolls.
		`justify-content: start`,
	} {
		if !strings.Contains(rule, fragment) {
			t.Errorf("the board's rules %q do not contain %q", rule, fragment)
		}
	}
	// Deleting the property and giving it an unbounded value are the same
	// failure and read the same way: the columns stop nowhere.
	if !strings.Contains(body, `--board-column-max: 26rem;`) {
		t.Error("the stylesheet does not define a bounded column maximum")
	}
	// A track list is how the count got hardcoded, and repeat() is how it would
	// come back — including the repeat(0, …) a project with no live statuses
	// would produce, which is not a stylesheet at all.
	if strings.Contains(rule, "grid-template-columns") || strings.Contains(rule, "repeat(") {
		t.Errorf("the board's rules %q name a track count again", rule)
	}
	// 1fr grows without limit, which is the absence this fixes.
	if strings.Contains(rule, "1fr") {
		t.Errorf("the board's rules %q size a column with an unbounded 1fr", rule)
	}
}

// The tracks come from the columns, so a project gets as many as it defines —
// three, eight, or, for a ledger tip that forwards a status and defines none,
// zero. The last one is the case a track list could not survive: repeat(0, …)
// is invalid, and a server that stamped the count would have had to special-case
// it.
//
// A count is not the whole of it, though, and this is the sharp edge of taking
// the track count from the DOM: every direct child of the board is a track, so
// anything the markup grows there that is not a column is a track holding width
// the columns wanted, silently. So the assertion is what the children are, not
// how many of them there are.
func TestHandlerBoardDrawsOneColumnPerConfiguredStatus(t *testing.T) {
	for name, vocabulary := range map[string]core.Vocabulary{
		"default": core.DefaultVocabulary(),
		"three":   handlerVocabulary(t),
		"eight":   wideVocabulary(t),
		"none":    statuslessVocabulary(t),
		"one":     singleStatusVocabulary(t),
	} {
		body := boardPageWith(t, vocabulary)
		children := boardChildren(t, body)
		if want := len(vocabulary.Definitions()); len(children) != want {
			t.Errorf("%s vocabulary drew %d board tracks, want %d: %q", name, len(children), want, children)
		}
		for index, child := range children {
			if child != `<section class="column">` {
				t.Errorf("%s vocabulary drew a board track that is not a column at %d: %q", name, index, child)
			}
		}
		// Whatever the count, the stylesheet is the same one and still names no
		// number of its own.
		if strings.Contains(boardRules(t, body), "repeat(") {
			t.Errorf("%s vocabulary rendered a board rule with a track count", name)
		}
	}
}

// boardRules returns every `.board` declaration block on a rendered page joined
// together, which is what these tests assert against rather than the whole
// stylesheet: `repeat(` and `1fr` are ordinary elsewhere on the page and only
// mean something here. All of them rather than the one that sizes the tracks,
// because the board is styled by more than one rule and which of them a
// declaration sits in is not something a test should hold still.
func boardRules(t *testing.T, body string) string {
	t.Helper()
	const selector = ".board {"
	var rules []string
	for rest := body; ; {
		start := strings.Index(rest, selector)
		if start < 0 {
			break
		}
		rest = rest[start:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			t.Fatalf("a .board rule is unterminated: %q", rest)
		}
		rules = append(rules, rest[:end+1])
		rest = rest[end+1:]
	}
	if len(rules) == 0 {
		t.Fatal("the rendered page has no .board rule")
	}
	return strings.Join(rules, "\n")
}

// boardChildren returns the opening tag of every direct child of the rendered
// board element, in document order.
//
// It walks the markup rather than counting a substring because a count only
// sees the elements it was told to look for: a spurious div added to the board
// would leave the column count right and the track count wrong. Nesting is
// tracked by <section> alone, which is the element the board's children are, so
// the header, headings, links and cards inside a column are passed over without
// the helper needing to know which tags close themselves.
func boardChildren(t *testing.T, body string) []string {
	t.Helper()
	const opening = `<section class="board"`
	start := strings.Index(body, opening)
	if start < 0 {
		t.Fatal("the rendered page has no board element")
	}
	rest := body[start+len(opening):]
	tagEnd := strings.IndexByte(rest, '>')
	if tagEnd < 0 {
		t.Fatal("the board element's opening tag is unterminated")
	}
	rest = rest[tagEnd+1:]
	children := []string{}
	depth := 0
	for {
		next := strings.IndexByte(rest, '<')
		if next < 0 {
			t.Fatal("the board element is never closed")
		}
		rest = rest[next:]
		if strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest, "-->")
			if end < 0 {
				t.Fatal("a comment inside the board element is unterminated")
			}
			rest = rest[end+len("-->"):]
			continue
		}
		if strings.HasPrefix(rest, "</section>") {
			rest = rest[len("</section>"):]
			if depth == 0 {
				return children
			}
			depth--
			continue
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			t.Fatalf("an element inside the board has an unterminated tag: %q", rest)
		}
		tag := rest[:end+1]
		rest = rest[end+1:]
		if strings.HasPrefix(tag, "</") {
			continue
		}
		if depth == 0 {
			children = append(children, tag)
		}
		if strings.HasPrefix(tag, "<section") {
			depth++
		}
	}
}

// boardPageWith renders an empty board for a project with these statuses.
func boardPageWith(t *testing.T, vocabulary core.Vocabulary) string {
	t.Helper()
	handler := NewHandler(Options{
		Vocabulary: staticVocabulary(vocabulary, "head-1"),
		List:       func(context.Context) ([]core.Task, error) { return nil, nil },
	})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// wideVocabulary is a project with more statuses than Workbook ever shipped.
func wideVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	definitions := make([]core.StatusDefinition, 0, 8)
	for index := 1; index <= 8; index++ {
		definition := core.StatusDefinition{
			Status: core.Status("stage-" + strconv.Itoa(index)),
			Label:  "Stage " + strconv.Itoa(index),
			Rank:   strconv.Itoa(index) + "/1",
			Tags:   []core.StatusTag{},
		}
		if index == 1 {
			definition.Tags = []core.StatusTag{core.StatusTagDefault}
		}
		definitions = append(definitions, definition)
	}
	vocabulary, err := core.NewVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// singleStatusVocabulary is a project configured down to one column, which is
// the narrowest board that still has a grid.
func singleStatusVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary([]core.StatusDefinition{
		{Status: "open", Label: "Open", Rank: "1/1", Tags: []core.StatusTag{core.StatusTagDefault}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	return vocabulary
}

// statuslessVocabulary defines no statuses and forwards one, which is what a
// hand-edited or foreign ledger tip arrives as. It is not the zero vocabulary,
// so the handler does not substitute the built-in statuses and the column count
// is genuinely zero — the same fixture the terminal renderer guards against.
func statuslessVocabulary(t *testing.T) core.Vocabulary {
	t.Helper()
	vocabulary, err := core.NewVocabulary(nil, nil, []core.RetiredStatus{{Status: "ghost", Destination: "gone"}})
	if err != nil {
		t.Fatalf("NewVocabulary() error = %v", err)
	}
	if vocabulary.IsZero() {
		t.Fatal("the fixture is the zero vocabulary, so the handler substitutes the built-in statuses and proves nothing")
	}
	return vocabulary
}

// A column header is a heading and a link in all six columns, so the six are
// the same height without a floor under them. The floor that used to be here
// reserved the ref path's row, sat below the natural height once that left, and
// could not have levelled the headers anyway: min-height raises a short header,
// it never pulls the other five up to one that wrapped.
func TestHandlerBoardColumnHeadersCarryNoInertMinimumHeight(t *testing.T) {
	body := boardPage(t)
	if !strings.Contains(body, `.column__header { padding: .7rem .75rem .6rem;`) {
		t.Error("the column header rule no longer opens with its padding")
	}
	if strings.Contains(body, `.column__header { min-height:`) {
		t.Error("the column header reserves a height again")
	}
}
