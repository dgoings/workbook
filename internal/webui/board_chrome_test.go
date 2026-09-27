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
		`.task-card h3 a { color: var(--wb-text); text-decoration: none; }`,
		`.task-card h3 a:hover { color: var(--wb-primary); }`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("card title styling does not contain %q", fragment)
		}
	}
	if strings.Contains(body, `.task-card h3 a { color: var(--wb-text); text-decoration-color`) {
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
	//
	// It reads the rules that size a card or a column, not the whole stylesheet.
	// The literal is only a defect there: something else on the page may be
	// 12rem wide for a reason of its own — a chooser's menu is — and a guard over
	// the served body claimed those too, which is a guard that grows into an
	// obstacle rather than a rule.
	widths := strings.Join([]string{
		// Where the property is defined, desktop and phone alike: the phone's
		// override lives in a :root inside the narrow-screen media block.
		cssRules(t, body, ":root {"),
		boardRules(t, body),
		cssRules(t, body, ".column {"),
		cssRules(t, body, ".unknown-status__list {"),
		cssRules(t, body, ".task-card {"),
	}, "\n")
	for _, stale := range []string{`minmax(12rem`, `min-width: 12rem`, `min-width: min(18rem`} {
		if strings.Contains(widths, stale) {
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
		// The server never draws the Deleted column: it renders this project's
		// vocabulary, and that column is a view of what the vocabulary no longer
		// holds, appended by the client only while the reader asks for it.
		assertBoardTracks(t, name+" vocabulary", boardChildren(t, body), len(vocabulary.Definitions()), false)
		// Whatever the count, the stylesheet is the same one and still names no
		// number of its own.
		if strings.Contains(boardRules(t, body), "repeat(") {
			t.Errorf("%s vocabulary rendered a board rule with a track count", name)
		}
	}
}

// assertBoardTracks states what a direct child of the board element may be, in
// document order, and is the whole contract: every direct child of the board is
// a grid track, so anything that grows there and is not a column is a track
// holding width the columns wanted, silently.
//
// There are exactly two kinds. A vocabulary column, one per status the project
// defines. And the Deleted column — at most one of them, always last, and only
// while the reader has asked for it; hidden, the board is back to the columns
// alone, which is the state the server always serves.
//
// It takes the children as opening tags so both halves of the contract are
// checked by this one function: the served page's markup walked by
// boardChildren, and the client's live board reported by the DOM harness in
// deleted_column_client_test.go.
func assertBoardTracks(t *testing.T, subject string, children []string, columns int, deletedShown bool) {
	t.Helper()
	want := columns
	if deletedShown {
		want++
	}
	if len(children) != want {
		t.Errorf("%s drew %d board tracks, want %d: %q", subject, len(children), want, children)
	}
	for index, child := range children {
		if deletedShown && index == len(children)-1 {
			if child != `<section class="column column--deleted">` {
				t.Errorf("%s did not end its board with the Deleted column: %q", subject, child)
			}
			continue
		}
		if child != `<section class="column">` {
			t.Errorf("%s drew a board track that is not a column at %d: %q", subject, index, child)
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
	return cssRules(t, body, ".board {")
}

// cssRules returns every declaration block in the served stylesheet that opens
// with this selector, joined — the desktop rule and whatever the media queries
// restate, which for a property like --board-column-min is the whole of what the
// page says about it. It is the shape boardRules always had, named so that a
// claim about one family of rules can be scoped to them instead of being made
// over the entire body.
func cssRules(t *testing.T, body, selector string) string {
	t.Helper()
	var rules []string
	for rest := body; ; {
		start := strings.Index(rest, selector)
		if start < 0 {
			break
		}
		rest = rest[start:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			t.Fatalf("a %s rule is unterminated: %q", selector, rest)
		}
		rules = append(rules, rest[:end+1])
		rest = rest[end+1:]
	}
	if len(rules) == 0 {
		t.Fatalf("the rendered page has no %s rule", selector)
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

// filterRowElement returns the served page's filter row, which is where every
// claim about the board's filters is made. It is a region of the page rather than
// a part of the board — the board scrolls sideways and the filters must not go
// with it — so it is found by its own marker rather than by walking the columns.
func filterRowElement(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div class="filter-row"`)
	if start < 0 {
		t.Fatal("the served page has no filter row")
	}
	end := strings.Index(body[start:], "\n  </div>")
	if end < 0 {
		t.Fatal("the filter row is never closed")
	}
	return body[start : start+end+len("\n  </div>")]
}

// The board's filters are served rather than built: the row, the search box, the
// space the choosers are drawn into, and the Deleted switch inside it beside
// them, because it was already the one filter the board had. The row ships hidden
// and the board's render reveals it, so a page whose script never ran offers no
// filters rather than dead ones.
func TestHandlerDrawsTheDeletedSwitchInTheFilterRow(t *testing.T) {
	body := boardPage(t)
	row := filterRowElement(t, body)

	for _, marker := range []string{
		"data-filter-row",
		"data-filter-q",
		"data-filter-choosers",
		"data-deleted-toggle",
		"data-filter-clear",
		// The three choosers themselves. They are served rather than built, so a
		// page whose script never ran offers no control that cannot work — and
		// the group each one narrows is the attribute the client collects them
		// by, which is why the value is asserted rather than the bare name.
		`data-filter-chooser="priority"`,
		`data-filter-chooser="label"`,
		`data-filter-chooser="key"`,
		// The parts of a chooser. The client asks for each of these inside the
		// chooser's own root, and the fake DOM hand-builds them — so without
		// these three the served markup could lose a part and every client test
		// would go on passing against a harness that still had it.
		"data-filter-chooser-button",
		"data-filter-chooser-badge",
		"data-filter-chooser-menu",
		"data-filter-chooser-count",
		// A collapsed menu says so, which is the half of the state a sighted
		// reader gets from the menu not being there.
		`aria-expanded="false"`,
		// The button names the menu it opens rather than only claiming to have a
		// popup, so the two are one control to a screen reader.
		`aria-controls="filter-menu-priority"`,
		`aria-controls="filter-menu-label"`,
		`aria-controls="filter-menu-key"`,
		`id="filter-menu-priority"`,
		// The badge is a bare number on screen. The word beside it is there for a
		// reader who cannot see which chooser it is sitting on, and it reads the
		// same for one as for two — which is why the markup can own it rather
		// than the client having to choose a plural every render. Counted below,
		// once per chooser, because a substring check is satisfied by any one of
		// the three and would pass over two badges that had lost it.
		`<span class="visually-hidden"> selected</span>`,
		// A search box is a search box, which is what gives a reader the clear
		// affordance the browser draws in one.
		`type="search"`,
		// Clear ships hidden: an unfiltered board has nothing to clear, and the
		// client reveals it once the address holds a filter.
		"data-filter-clear hidden",
	} {
		if !strings.Contains(row, marker) {
			t.Errorf("the filter row carries no %s: %s", marker, row)
		}
	}
	// The search says what it searches, to a reader and to a screen reader alike:
	// a placeholder is not a label, and a box with no name is a box.
	if !strings.Contains(row, `aria-label="Search title and description"`) {
		t.Errorf("the search box has no accessible name: %s", row)
	}
	// The row's own hidden attribute, on its own opening tag. A substring check
	// over the whole row is satisfied by any hidden thing inside it — the Clear
	// anchor ships hidden too — which would have passed over a row that shipped
	// visible and offered the board's filters from a task's page.
	if !strings.HasPrefix(row, `<div class="filter-row" data-filter-row hidden>`) {
		t.Errorf("the filter row ships visible, so a route that draws no columns still offers it: %s", row)
	}
	// Every chooser's badge carries the word, not just one of them.
	const screenReaderWord = `<span class="visually-hidden"> selected</span>`
	if got := strings.Count(row, screenReaderWord); got != 3 {
		t.Errorf("%d of the three badges carry the screen-reader word: %s", got, row)
	}
	// aria-haspopup only claims that something opens; aria-controls names what,
	// which is what lets a screen reader treat the button and the menu as one
	// control. A page carrying both would be stating the weaker fact twice.
	if strings.Contains(row, "aria-haspopup") {
		t.Errorf("a chooser still claims a popup it does not name: %s", row)
	}
	// The visually-hidden word needs the rule that hides it, or it is a word in
	// the middle of the row.
	if !strings.Contains(body, ".visually-hidden { position: absolute;") {
		t.Error("the stylesheet does not hide the badge's screen-reader word")
	}
	// The Key chooser ships hidden as well, and for a reason of its own: most
	// projects have one key, and a chooser offering one alternative is a control
	// with nothing to choose. The client reveals it once a project has two.
	keyAt := strings.Index(row, `data-filter-chooser="key"`)
	if keyAt < 0 {
		t.Fatalf("the filter row carries no Key chooser: %s", row)
	}
	if !strings.HasPrefix(row[keyAt:], `data-filter-chooser="key" hidden>`) {
		t.Errorf("the Key chooser ships visible over a project that may have one key: %s", row)
	}
	// The switch is inside the row rather than back in the header beside the
	// settings, which an ordering claim over the whole body is what states.
	rowAt := strings.Index(body, "data-filter-row")
	switchAt := strings.Index(body, "data-deleted-toggle")
	headerEnd := strings.Index(body, "</header>")
	if rowAt < 0 || switchAt < 0 || headerEnd < 0 {
		t.Fatal("the page no longer carries a header, a filter row and a Deleted switch")
	}
	if switchAt < rowAt {
		t.Error("the Deleted switch is drawn before the filter row, so it is not in it")
	}
	if switchAt < headerEnd {
		t.Error("the Deleted switch is still drawn inside the header rather than in the filter row")
	}
}
