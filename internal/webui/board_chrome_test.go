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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
		// The Labels menu narrows itself: a box at the top of it, and the line it
		// says when the box has hidden every row. Both are served, because the
		// client reads them out of the chooser's own root the way it reads the
		// button and the menu.
		"data-filter-chooser-search",
		"data-filter-chooser-nomatch",
		`placeholder="Find a label"`,
		// The rows go into a container of their own in every menu, so the client
		// draws them down one path — and so that rebuilding a menu's rows cannot
		// take the box above them with it.
		"data-filter-chooser-options",
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
	if !strings.Contains(row, `aria-label="Search title, description or task ID"`) {
		t.Errorf("the search box has no accessible name: %s", row)
	}
	// And it says the ID too, since a pasted ID is a search a reader has no other
	// way to know the box takes.
	if !strings.Contains(row, `placeholder="Search title, description or task ID"`) {
		t.Errorf("the search box does not offer to search by task ID: %s", row)
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
	// One box, in one menu. The Priority and Key menus list a vocabulary the
	// project states — short by construction — and a box over three priorities is
	// a control asking to be used where the list could simply be read. A substring
	// check is satisfied by any one of the three menus carrying it, so the claim is
	// the count.
	if got := strings.Count(row, "data-filter-chooser-search"); got != 1 {
		t.Errorf("%d menus carry a search box, want the Labels menu alone: %s", got, row)
	}
	if got := strings.Count(row, "data-filter-chooser-nomatch"); got != 1 {
		t.Errorf("%d menus carry a no-match line, want the Labels menu alone: %s", got, row)
	}
	// All three menus have somewhere to draw their rows, though, which is what
	// gives the client one path for them.
	if got := strings.Count(row, "data-filter-chooser-options"); got != 3 {
		t.Errorf("%d of the three menus hold an options container: %s", got, row)
	}
	// The box the search sits in is inside the Labels menu rather than beside it in
	// the row: what it narrows is that menu's rows, and a box in the row would read
	// as a second search over the board.
	labelMenuAt := strings.Index(row, `id="filter-menu-label"`)
	searchAt := strings.Index(row, "data-filter-chooser-search")
	if labelMenuAt < 0 || searchAt < labelMenuAt {
		t.Errorf("the Labels menu's search box is not inside the Labels menu: %s", row)
	}
	// The box says what it finds, to a reader and to a screen reader alike, for the
	// reason the row's own search box does: a placeholder is not a label.
	if !strings.Contains(row, `aria-label="Find a label"`) {
		t.Errorf("the Labels menu's search box has no accessible name: %s", row)
	}
	// The line reads as the sentence it is, and it is a different sentence from the
	// one an unlabelled board gets: a search that matched nothing, not a project
	// with nothing to match.
	if !strings.Contains(row, `data-filter-chooser-nomatch hidden>No labels match</p>`) {
		t.Errorf("the Labels menu's no-match line is not served hidden with its own words: %s", row)
	}
	// A live region, so a reader who cannot see the rows go is told that the letter
	// they just typed left nothing. It stands in the document whether or not it is
	// showing, for the reason every live region on this page does: one created in
	// the same frame as its own first message announces nothing.
	if !strings.Contains(row, `aria-live="polite" data-filter-chooser-nomatch`) {
		t.Errorf("the Labels menu's no-match line is not a live region: %s", row)
	}
	// The rule that draws the box, without which it is an input the page never
	// styled sitting on top of a menu.
	if !strings.Contains(body, ".filter-chooser__search { display: block;") {
		t.Error("the stylesheet does not draw the Labels menu's search box")
	}
	// The rows are what scrolls, and the menu is not: the box that narrows these
	// rows would otherwise sit inside the scroller it narrows, and a reader
	// scrolling down a long list pushed it off the top of the menu.
	options := cssRules(t, body, ".filter-chooser__options {")
	for _, fragment := range []string{"max-height: 18rem", "overflow-y: auto"} {
		if !strings.Contains(options, fragment) {
			t.Errorf("the options container %q does not contain %q", options, fragment)
		}
	}
	if menu := cssRules(t, body, ".filter-chooser__menu {"); strings.Contains(menu, "overflow-y") ||
		strings.Contains(menu, "max-height") {
		t.Errorf("the menu is still the scroller, so its search box scrolls away with the rows: %s", menu)
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

// The filter row draws no rule under itself and main pads no top, which are one
// change rather than two: the row is what separates the header from the columns
// now, so a hairline a few pixels under the header's own read as a thickened
// header border, and an inset under that stood the columns a row's height below
// the control that narrows them. The routes that draw no columns get that inset
// back on the one condition that says they are not the board — the row being
// hidden — so nothing of theirs sits flush against the header.
func TestHandlerFilterRowDrawsNoRuleAndMainPadsNoTop(t *testing.T) {
	t.Parallel()
	body := boardPage(t)
	if rule := cssRules(t, body, ".filter-row {"); strings.Contains(rule, "border-bottom") {
		t.Errorf("the filter row still draws a rule under itself: %s", rule)
	}
	if !strings.Contains(body, "main { flex: 1 1 auto; min-height: 0; overflow: auto; padding: 0 1.25rem 1.25rem; }") {
		t.Errorf("main no longer pads its sides and its bottom alone: %s", cssRules(t, body, "main {"))
	}
	// One bare main rule in the whole stylesheet. The scheme blocks redefine colors
	// and nothing else, and a second main rule inside one of them would be a top
	// padding that came back in one palette. Counted at the indentation the
	// stylesheet writes a top-level selector at, so the one rule below — a main with
	// a condition in front of it — is not one of these.
	if got := strings.Count(body, "\n    main {"); got != 1 {
		t.Errorf("the stylesheet holds %d unconditional main rules, want the one", got)
	}
	// A task's page, the configuration page and a route message are each a bordered
	// card, and each wants the inset main gave up. The condition is the filter row
	// being hidden, which is exactly the condition of being off the board, so the
	// state is read off the page rather than kept a second time.
	//
	// Padding on main rather than a margin on the card, because main is the
	// scroller: a margin adds to what is inside it, so every route whose shell is
	// `height: auto; min-height: 100%` — the configuration page, a task page with
	// its history open, a phone, a short window — would have gained a margin's worth
	// of scroll on a page that fitted. Padding comes off the height those
	// percentages resolve against instead.
	inset := cssRules(t, body, ".filter-row[hidden] ~ main {")
	if !strings.Contains(inset, "padding-top: 1.25rem") {
		t.Errorf("the routes that draw no columns take no inset back: %s", inset)
	}
	if shell := cssRules(t, body, ".task-route {"); strings.Contains(shell, "margin: 1.25rem auto 0") {
		t.Errorf("the route shell insets itself with a margin the scroller has to find room for: %s", shell)
	}
}

// Every class the filter row toggles `hidden` on hides when it is hidden.
//
// The hidden attribute hides a thing by the user agent's own `display: none`,
// which any display in this stylesheet outranks: a rule as ordinary as
// `display: flex` on the class leaves the attribute doing nothing but flipping a
// property, and the element keeps its place on screen. That is what happened to
// the Labels menu's rows — the box narrowed them by setting hidden on each one,
// every row stayed drawn, and the search read as a control that did nothing at
// all.
//
// The client tests cannot catch it. Their DOM has no layout and no stylesheet,
// so a row that sets hidden is a row they see hidden, which is why they passed
// while the page did not. The rule is the thing to pin, and it is pinned here for
// every class on this row the client hides rather than only for the one that was
// found missing it.
func TestHandlerEveryHiddenToggledClassHidesWhenHidden(t *testing.T) {
	t.Parallel()
	body := boardPage(t)
	// Each of these the client hides at some point: the row itself off the board,
	// the Clear link with nothing to clear, the Key chooser on a one-key project,
	// a count badge with nothing counted, a menu that is closed, and an option row
	// the menu's own search box has narrowed away.
	for _, class := range []string{
		".filter-row",
		".filter-row__clear",
		".filter-chooser",
		".filter-chooser__badge",
		".filter-chooser__menu",
		".filter-option",
	} {
		rule := class + "[hidden] { display: none; }"
		if !strings.Contains(body, rule) {
			t.Errorf("the stylesheet carries no %q, so setting hidden on a %s leaves it drawn", rule, class)
		}
	}
}
