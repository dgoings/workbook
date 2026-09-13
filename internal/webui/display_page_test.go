package webui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a project's display settings do to the page the server hands over.
//
// Every claim here is about the served bytes rather than about what the client
// would do with them, because that is where the two rules this feature rests on
// live: a project that has configured nothing is served the board it was always
// served, and a project that has chosen a colour is served that colour as CSS
// rather than as the ZgotmplZ html/template writes for a value it cannot vouch
// for.

// displayBoardPage renders a board for a project with these display settings.
func displayBoardPage(t *testing.T, settings core.DisplaySettings, repository string) string {
	t.Helper()
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{Vocabulary: core.DefaultVocabulary(), Head: "head-1", Display: settings}, nil
		},
		RepoName: repository,
		List:     func(context.Context) ([]core.Task, error) { return nil, nil },
	})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// themeBlock returns the served override stylesheet, or the empty string when
// the page carries none. It is found by its own marker rather than by looking
// for `:root`, since the stylesheet above it declares one too — which is the
// whole arrangement: the defaults are always there and the override is what a
// project adds to them.
func themeBlock(t *testing.T, body string) string {
	t.Helper()
	const marker = `<style data-board-theme>`
	at := strings.Index(body, marker)
	if at < 0 {
		return ""
	}
	end := strings.Index(body[at:], "</style>")
	if end < 0 {
		t.Fatal("the served theme is never closed")
	}
	return body[at+len(marker) : at+end]
}

// A named project says its own name in every place the page names itself, and
// the eyebrow says which checkout this is — which is what distinguishes two
// boards a reader has open at once.
func TestHandlerDrawsTheProjectsOwnNameAndItsCheckout(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{Name: "Atlas"}, "atlas-web")

	for _, want := range []string{
		"<title>Atlas</title>",
		`<h1>Atlas</h1>`,
		`<p class="eyebrow">Repository: atlas-web</p>`,
		// The client titles every other route from this, and reads it here
		// rather than holding core's fallback itself.
		`data-project-name="Atlas"`,
		`data-title-suffix="Atlas"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
}

// An unnamed project is the board every Workbook board has always been, and the
// two names it carries are different words on purpose: "New task · Workbook
// board" would read as a board called "New task".
func TestHandlerDrawsTheGenericNameForAnUnnamedProject(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{}, "workbook")

	for _, want := range []string{
		"<title>Workbook board</title>",
		`<h1>Workbook board</h1>`,
		`data-project-name="Workbook board"`,
		`data-title-suffix="Workbook"`,
		`<p class="eyebrow">Repository: workbook</p>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
}

// A board built without a repository name keeps the words it had. A colon with
// nothing after it is worse than the generic sentence it replaced.
func TestHandlerKeepsTheGenericEyebrowWithoutARepositoryName(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{}, "")

	if !strings.Contains(body, `<p class="eyebrow">Repository workbench</p>`) {
		t.Error("a board with no repository name does not draw the generic eyebrow")
	}
	if strings.Contains(body, "Repository: <") {
		t.Error("the eyebrow names a repository it was not given")
	}
}

// The header is one element for every route, so the name and the eyebrow are
// pageData's rather than a route's. This is the same promise
// TestHandlerServesOneHeaderToEveryRoute makes, restated for a named project so
// that a change which starts drawing the name per-route fails here too.
func TestHandlerServesOneNamedHeaderToEveryRoute(t *testing.T) {
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: core.DefaultVocabulary(), Head: "head-1",
				Display: core.DisplaySettings{Name: "Atlas"},
			}, nil
		},
		RepoName: "atlas-web",
		List:     func(context.Context) ([]core.Task, error) { return nil, nil },
		AddStatus: func(context.Context, VocabularyStatusAddition) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		EditStatus: func(context.Context, core.Status, VocabularyStatusEdit) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		RemoveStatus: func(context.Context, core.Status, VocabularyStatusRemoval) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
		ReorderStatus: func(context.Context, VocabularyOrder) (VocabularyMutation, error) {
			return VocabularyMutation{}, nil
		},
	})

	board := ""
	for _, path := range []string{"/", "/config", "/tasks/new"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		header := headerElement(t, response.Body.String())
		if !strings.Contains(header, "<h1>Atlas</h1>") {
			t.Fatalf("GET %s draws a header without the project's name:\n%s", path, header)
		}
		if board == "" {
			board = header
			continue
		}
		if header != board {
			t.Errorf("GET %s serves a different header from the board's:\n%s\n\nwant\n%s", path, header, board)
		}
	}
}

// The stylesheet declares every property this build derives, and declares it as
// the exact literal the page was drawn in before any of this existed. This is
// what makes an unconfigured board the board it always was: the theming is an
// override that is simply absent, not a computation the default goes through.
//
// It is asserted against the same table the generator reads, so a property
// added to one and not the other is caught here rather than in a browser.
func TestHandlerStylesheetDeclaresTheLegacyPaletteAsItsDefaults(t *testing.T) {
	body := pageWithoutStyleComments(t, displayBoardPage(t, core.DisplaySettings{}, "workbook"))
	declared := declaredLiteralCounts()

	for _, token := range append(append([]themeToken{}, primaryThemeTokens...), textThemeTokens...) {
		declaration := token.property + ": " + token.legacy + ";"
		if !strings.Contains(body, declaration) {
			t.Errorf("the stylesheet does not declare %q", declaration)
		}
		// And nothing still writes the literal out where the property should be
		// read, which is what would make one control stop following a project's
		// choice while every other one followed it. Counted exactly rather than
		// bounded: a literal that stopped appearing is a default that stopped
		// being declared, which is the other half of the same claim.
		if got, want := countColorLiteral(body, token.legacy), declared[token.legacy]; got != want {
			t.Errorf("the page writes %s out %d times, want %d — once for each property that declares it as its default",
				token.legacy, got, want)
		}
	}
}

// The same claim, for the palette no project chooses: every scheme token is
// declared as the literal the board was drawn in, and that literal is written
// out nowhere else on the page.
//
// A token missing from the stylesheet would render the property unset, which
// `var()` answers with nothing at all rather than with the old colour — a
// missing surface is an invisible card, not a slightly wrong one. A literal
// still written out is the other failure: one control that keeps its light
// colour when the scheme moves, which is exactly the class of miss this table
// exists to make impossible.
//
// Counted per literal rather than per token, because three of them are declared
// more than once on purpose: see schemeTokens for why #fff carries three
// properties and #8496b0 and #2457d6 two each.
func TestHandlerStylesheetDeclaresTheSchemePaletteAsItsDefaults(t *testing.T) {
	body := pageWithoutStyleComments(t, displayBoardPage(t, core.DisplaySettings{}, "workbook"))
	declared := declaredLiteralCounts()

	for _, token := range schemeTokens {
		if declaration := token.property + ": " + token.legacy + ";"; !strings.Contains(body, declaration) {
			t.Errorf("the stylesheet does not declare %q", declaration)
		}
		if got, want := countColorLiteral(body, token.legacy), declared[token.legacy]; got != want {
			t.Errorf("the page writes %s out %d times, want %d — once for each property that declares it as its default",
				token.legacy, got, want)
		}
	}
}

// declaredLiteralCounts is how many times each literal is allowed to appear on
// the served page: once for every property, in any of the three tables, that
// declares it as its default.
//
// Spanning all three is what makes this a count rather than a licence. A
// literal two families both name — #2457d6 is the accent and the priority
// triad's blue — appears twice legitimately, and a per-table count would have
// to be told so by hand. That hand-kept exception is what used to live here,
// and it was the same shape as the thing these guards exist to prevent.
func declaredLiteralCounts() map[string]int {
	declared := map[string]int{}
	for _, token := range primaryThemeTokens {
		declared[token.legacy]++
	}
	for _, token := range textThemeTokens {
		declared[token.legacy]++
	}
	for _, token := range schemeTokens {
		declared[token.legacy]++
	}
	return declared
}

// The guard above runs one way: it proves the literals the table names are not
// written out anywhere else. It says nothing about a literal the table has
// never heard of, and that is the gap a colour arrives through. The relative
// sync indicator walked straight into it — of the three literals it added, two
// collided with tokens and were caught, and its green was simply new, so
// nothing objected to a raw colour sitting in the stylesheet.
//
// So this asserts the other direction, and asserts it as a closed set rather
// than a budget: outside the two `:root` blocks that declare the palette, the
// stylesheet writes no colour at all. There is no exception list to keep in
// step — a rule that wants a colour has to name a property, and a colour that
// has no property has to become one before it can be used.
func TestHandlerStylesheetWritesNoColourOutsideTheRootBlocks(t *testing.T) {
	body := pageWithoutStyleComments(t, displayBoardPage(t, core.DisplaySettings{}, "workbook"))

	rules := rootBlocks.ReplaceAllString(styleSheet(t, body), "")
	if written := colorLiteral.FindAllString(rules, -1); len(written) > 0 {
		t.Errorf("the stylesheet writes %v outside :root — every colour a rule uses has to be read from a property, so that one block moves the whole board",
			written)
	}
}

// Every palette property the stylesheet declares is one of the tables', and the
// tables are where a dark reading comes from.
//
// This is the other half of the dark-reading guard above. That one walks the
// tables and demands a dark declaration for each; without this one, a property
// declared straight into the light `:root` is in no table, so nothing demands
// anything of it — and `var()` answers an unset property with nothing at all,
// so in dark it is an invisible control rather than a wrong-coloured one. The
// two together close the loop: a colour cannot enter the stylesheet without
// entering a table, and cannot enter a table without stating what it becomes.
func TestHandlerStylesheetDeclaresNoPalettePropertyOutsideItsTables(t *testing.T) {
	body := pageWithoutStyleComments(t, displayBoardPage(t, core.DisplaySettings{}, "workbook"))

	stray := map[string]bool{}
	for _, block := range lightRootBlocks.FindAllStringSubmatch(styleSheet(t, body), -1) {
		for _, declaration := range paletteProperty.FindAllStringSubmatch(block[1], -1) {
			stray[declaration[1]] = true
		}
	}
	for _, token := range schemeTokens {
		delete(stray, token.property)
	}
	for _, token := range append(append([]themeToken{}, primaryThemeTokens...), textThemeTokens...) {
		delete(stray, token.property)
	}
	for property := range stray {
		t.Errorf("the stylesheet declares %s but no table does, so nothing gives it a dark reading", property)
	}
}

// The light `:root` blocks: the ones whose selector carries no scheme qualifier.
var lightRootBlocks = regexp.MustCompile(`(?s):root \{(.*?)\}`)

// A palette property. Scoped to the --wb- prefix so the board's own layout
// properties — --board-column-min and its sibling — are not colours to answer
// for.
var paletteProperty = regexp.MustCompile(`(--wb-[a-z0-9-]+):`)

// The `:root` blocks are where the palette is allowed to say a colour out loud:
// the derived families' defaults, the scheme block beneath them, and the dark
// readings of both.
//
// A dark block is still a palette block, so the selector may carry the
// attribute and `:not()` qualifiers that decide which scheme it answers to —
// `:root[data-scheme="dark"]`, `:root:not([data-scheme="light"])`. What it may
// not carry is a descendant: `:root[data-scheme="dark"] .nav-switch__knob` is a
// rule about one control, not a palette, and a literal written there is exactly
// the kind this guard exists to refuse. So the qualifiers are matched and a
// space before the brace is not.
var rootBlocks = regexp.MustCompile(`(?s):root(?::not\(\[[^\]]*\]\)|\[[^\]]*\])*\s*\{.*?\}`)

// Hex in any of its lengths, and the functional notations. Deliberately not
// anchored to a property, because the point is to find a colour wherever it was
// written rather than only where one was expected.
var colorLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|(?:rgba?|hsla?|color-mix|oklch|lab)\([^)]*\)`)

// styleSheet returns the board's own stylesheet — the first <style> element,
// which is the one this file is about.
//
// A served page now carries three. The theme block a configured project gets is
// the second, and the per-priority ink is the third; both are composed in Go
// rather than written here, and both are guarded by the tests that compose
// them rather than by the ones below. The distinction is deliberate and worth
// stating, because "the page declares no color outside the root blocks" reads
// like a claim about the whole document and is a claim about this element: a
// guard that silently stopped covering what it was written for would be worse
// than no guard, and a reader deciding where a new rule belongs needs to know
// which of the three they are looking at.
func styleSheet(t *testing.T, body string) string {
	t.Helper()
	open := strings.Index(body, "<style>")
	shut := strings.Index(body, "</style>")
	if open < 0 || shut < open {
		t.Fatal("the page serves no stylesheet")
	}
	return body[open+len("<style>") : shut]
}

// pageWithoutStyleComments is the served page with the stylesheet's comments
// taken out. Every guard in this file counts colour literals, and a colour
// named in prose is not one the browser draws — the block above `:root` says
// which literals are declared twice and why, and would otherwise be caught
// declaring them a third time.
func pageWithoutStyleComments(t *testing.T, body string) string {
	t.Helper()
	open := strings.Index(body, "<style>")
	shut := strings.Index(body, "</style>")
	if open < 0 || shut < open {
		t.Fatal("the page serves no stylesheet")
	}
	return body[:open] + cssComment.ReplaceAllString(body[open:shut], "") + body[shut:]
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// countColorLiteral counts a colour literal without counting a longer one that
// starts with it. `strings.Count` cannot do this here: #fff is a prefix of
// #fff6e8, so counting it naively finds the warning surface as well as the
// white one. RE2 has no lookahead, so the boundary is spelled as "not another
// hex digit, or the end of the page".
func countColorLiteral(body, literal string) int {
	return len(regexp.MustCompile(regexp.QuoteMeta(literal)+`([^0-9a-fA-F]|$)`).FindAllStringIndex(body, -1))
}

// Every property the board is drawn from has a reading in dark, and they are
// all in one place.
//
// This is the test that earns the whole shape of this change. A dark mode
// written as a sheet of per-selector overrides drifts the moment a rule is
// added, and drifts silently — the board still renders, with one light patch
// nobody notices until a screenshot. Here a property with no dark reading is a
// test failure, and the check is a set comparison rather than a reading of the
// board, so it cannot miss a route the way an eye can.
func TestHandlerStylesheetGivesEveryPalettePropertyADarkReading(t *testing.T) {
	dark := darkSchemeBlock(t, displayBoardPage(t, core.DisplaySettings{}, "workbook"))

	if !strings.Contains(dark, "color-scheme: dark;") {
		t.Error("the dark block does not set color-scheme, so the browser draws its own widgets light")
	}
	for _, token := range schemeTokens {
		if !strings.Contains(dark, token.property+": "+token.dark+";") {
			t.Errorf("the dark block does not declare %q as %s", token.property, token.dark)
		}
	}
	for _, variant := range append(append([]schemeVariant{}, darkPrimaryVariants...), darkTextVariants...) {
		if !strings.Contains(dark, variant.property+": "+variant.dark+";") {
			t.Errorf("the dark block does not declare %q as %s", variant.property, variant.dark)
		}
	}
}

// The two statements of the dark palette agree, property for property.
//
// CSS cannot share a declaration block across a media boundary, so the palette
// is written twice: once for a reader whose system asks for dark, and once for
// a reader who asked for it outright. A copy is exactly the thing that drifts —
// somebody adds a property to the block they are looking at, the other keeps
// the light value, and the board is correct in one direction and wrong in the
// other for whoever chose the scheme by hand rather than by system.
//
// Both are generated from the same tables, and this is what makes that a fact
// rather than an intention.
func TestHandlerStylesheetStatesBothDarkSchemesIdentically(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{}, "workbook")

	system := declarationsIn(t, darkSchemeBlock(t, body), `:root:not([data-scheme="light"])`)
	chosen := declarationsIn(t, body, `:root[data-scheme="dark"]`)

	if len(system) == 0 {
		t.Fatal("the system dark block declares nothing")
	}
	if len(system) != len(chosen) {
		t.Errorf("the two dark schemes declare %d and %d properties", len(system), len(chosen))
	}
	for property, value := range system {
		switch other, declared := chosen[property]; {
		case !declared:
			t.Errorf("a reader who chose dark is never told %s", property)
		case other != value:
			t.Errorf("%s is %s by system and %s by choice", property, value, other)
		}
	}
	for property := range chosen {
		if _, declared := system[property]; !declared {
			t.Errorf("%s is declared for a chosen dark scheme but not for a system one", property)
		}
	}
}

// declarationsIn reads the properties one rule sets, given the selector that
// opens it.
func declarationsIn(t *testing.T, body, selector string) map[string]string {
	t.Helper()
	opening := selector + " {"
	start := strings.Index(body, opening)
	if start < 0 {
		t.Fatalf("the stylesheet has no rule for %s", selector)
	}
	rest := body[start+len(opening):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("the rule for %s is never closed", selector)
	}
	declarations := map[string]string{}
	for _, declaration := range strings.Split(rest[:end], ";") {
		property, value, found := strings.Cut(declaration, ":")
		if !found {
			continue
		}
		declarations[strings.TrimSpace(property)] = strings.TrimSpace(value)
	}
	return declarations
}

// darkSchemeBlock is the stylesheet's `prefers-color-scheme: dark` block.
func darkSchemeBlock(t *testing.T, body string) string {
	t.Helper()
	const opening = "@media (prefers-color-scheme: dark) {"
	start := strings.Index(body, opening)
	if start < 0 {
		t.Fatal("the stylesheet has no dark block at all")
	}
	depth, i := 0, start+len(opening)-1
	for ; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return body[start : i+1]
			}
		}
	}
	t.Fatal("the dark block is never closed")
	return ""
}

// A project that chose a colour is served that colour in both schemes.
//
// The stylesheet's own dark block cannot answer for this one: the override is
// served in a later <style> element, and a media query buys no specificity, so
// a light accent declared there would win in dark mode over a dark accent
// declared in the stylesheet. The override has to carry its own dark half.
func TestBoardThemeServesADarkReadingOfAChosenColor(t *testing.T) {
	theme := string(boardTheme(core.DisplaySettings{PrimaryColor: "#2457d6"}))

	if !strings.Contains(theme, "@media (prefers-color-scheme: dark)") {
		t.Fatalf("a configured project is served no dark reading:\n%s", theme)
	}
	light, dark, found := strings.Cut(theme, "@media (prefers-color-scheme: dark)")
	if !found {
		t.Fatal("the two schemes did not separate")
	}
	if !strings.Contains(light, "--wb-primary: #2457d6;") {
		t.Errorf("the light half does not carry the chosen colour:\n%s", light)
	}
	// The point of the dark half is that it is not the chosen colour: #2457d6
	// sits at 49% lightness and disappears into a dark card.
	if strings.Contains(dark, "--wb-primary: #2457d6;") {
		t.Errorf("the dark half serves the unlifted colour:\n%s", dark)
	}
	lifted, ok := parseThemeColor(declaredValue(t, dark, "--wb-primary"))
	if !ok {
		t.Fatal("the dark accent is not a colour")
	}
	chosen, _ := parseThemeColor("#2457d6")
	if lifted.light <= chosen.light {
		t.Errorf("the dark accent is no lighter than the colour it lifts: %v vs %v", lifted.light, chosen.light)
	}
	// A lifted accent is a light fill, so the ink on it has to be the dark one.
	// That is the property split the palette carries for exactly this.
	if !strings.Contains(darkSchemeBlock(t, displayBoardPage(t, core.DisplaySettings{}, "workbook")), "--wb-on-accent: #0f141c;") {
		t.Error("dark mode keeps white ink on a light accent fill")
	}
}

// A themed project's override answers the reader's chosen scheme, not only
// their system's.
//
// This is a regression test for a board that came out half converted. The
// stylesheet holds its dark palette off a reader who chose light, but the
// override served after it was written against a bare `:root` inside the media
// query — so on a dark system a reader who asked for light got light neutrals
// with the project's dark accent still sitting on them: a pale page with a
// near-black panel in the middle of it. Nothing failed; it just looked wrong,
// which is exactly the kind of defect a selector this easy to write invites.
func TestBoardThemeAnswersTheSchemeTheReaderChose(t *testing.T) {
	theme := string(boardTheme(core.DisplaySettings{PrimaryColor: "#2457d6"}))

	// Held off a reader who asked for light, whatever their system says.
	if !strings.Contains(theme, `@media (prefers-color-scheme: dark) { :root:not([data-scheme="light"]) {`) {
		t.Errorf("the override's system-dark block does not stand aside for a reader who chose light:\n%s", theme)
	}
	// And applied to one who asked for dark on a system that did not.
	if !strings.Contains(theme, `:root[data-scheme="dark"] {`) {
		t.Errorf("the override never answers a reader who chose dark:\n%s", theme)
	}
	// The two dark statements are the same palette, for the reason the
	// stylesheet's two are.
	system := declarationsIn(t, theme, `:root:not([data-scheme="light"])`)
	chosen := declarationsIn(t, theme, `:root[data-scheme="dark"]`)
	if len(system) == 0 {
		t.Fatal("the override's dark block declares nothing")
	}
	for property, value := range system {
		if other := chosen[property]; other != value {
			t.Errorf("the override says %s is %s by system and %s by choice", property, value, other)
		}
	}
	// And the light block stays unconditional: it is what a reader who chose
	// light falls back to, and what a light system gets with no choice at all.
	if !strings.HasPrefix(theme, ":root { --wb-primary: #2457d6;") {
		t.Errorf("the override no longer opens with the project's light palette:\n%s", theme)
	}
}

// And a project that chose nothing is still served nothing at all, in either
// scheme — the stylesheet's own defaults are the whole answer.
func TestBoardThemeServesNoDarkReadingForAnUnconfiguredProject(t *testing.T) {
	if theme := boardTheme(core.DisplaySettings{}); theme != "" {
		t.Errorf("an unconfigured project is served %q", theme)
	}
}

func declaredValue(t *testing.T, block, property string) string {
	t.Helper()
	_, after, found := strings.Cut(block, property+": ")
	if !found {
		t.Fatalf("%s is not declared in %q", property, block)
	}
	value, _, _ := strings.Cut(after, ";")
	return value
}

// priorityLowBlue is the accent, and also the triad's blue. Two properties
// declare it, and they agree only until a project chooses an accent.
const priorityLowBlue = "#2457d6"

// The priority triad does not follow a project's accent.
//
// It is not an oversight to be tidied up later. The three priority colours are a
// triad read against each other — a red for high, an amber for medium, a blue
// for low — and a project that picks a red-ish accent would make "low" read as
// "high" on every card on the board.
//
// The triad used to make that claim by writing its blue out where no derivation
// could reach it. It has its own scheme family now, which does not weaken the
// claim but does move where the claim lives: a scheme property is not derived
// from a project's colour either, so what has to be true is that these three
// resolve to the triad's own properties and that nothing overrides them. Both
// halves are asserted below, against a project whose accent is the very red the
// high priority is drawn in.
func TestHandlerStylesheetKeepsThePriorityTriadOffTheProjectsAccent(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{PrimaryColor: "#b42318"}, "workbook")

	for _, rule := range []string{
		".priority--high { color: var(--wb-priority-high); }",
		".priority--medium { color: var(--wb-priority-medium); }",
		".priority--low { color: var(--wb-priority-low); }",
	} {
		if !strings.Contains(body, rule) {
			t.Errorf("the stylesheet no longer carries %q, so the triad is no longer read as a triad", rule)
		}
	}
	// And nothing a project chose reaches those three properties. This is the
	// half the literal used to carry on its own: a rule that reads
	// --wb-priority-low is only held off the accent for as long as the theme
	// block declines to declare it.
	for _, property := range []string{"--wb-priority-high", "--wb-priority-medium", "--wb-priority-low"} {
		if strings.Contains(themeBlock(t, body), property+":") {
			t.Errorf("the theme block declares %s, so a project's accent now reaches the priority triad", property)
		}
	}
	// The blue in particular is still the blue, rather than having quietly
	// become the accent when it stopped being written at the rule.
	if !strings.Contains(body, "--wb-priority-low: "+priorityLowBlue+";") {
		t.Errorf("the scheme no longer declares --wb-priority-low as %s, so a red-ish accent makes \"low\" read as \"high\"", priorityLowBlue)
	}
	// And the accent this project chose really is in force elsewhere, so the
	// rule above is an exception rather than a board that ignored the setting.
	if !strings.Contains(themeBlock(t, body), "--wb-primary: #b42318;") {
		t.Error("the project's accent did not reach the theme, so nothing here is an exception to anything")
	}
}

// A project that has chosen nothing is served no override at all.
func TestHandlerServesNoThemeForAnUnconfiguredProject(t *testing.T) {
	for name, settings := range map[string]core.DisplaySettings{
		"nothing configured": {},
		"a name alone":       {Name: "Atlas"},
	} {
		if theme := themeBlock(t, displayBoardPage(t, settings, "workbook")); theme != "" {
			t.Errorf("%s was served a theme: %s", name, theme)
		}
	}
}

// A project that has chosen a colour is served real CSS.
//
// This is the claim the whole approach turns on. html/template filters a CSS
// value it cannot vouch for and writes ZgotmplZ in its place, which would leave
// the board unstyled and every test that only checked pageData passing. So the
// assertion is against the bytes that reach the browser.
func TestHandlerServesAChosenColorAsCSSRatherThanZgotmplZ(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{PrimaryColor: "#1a7f4b", TextColor: "#3b2a1a"}, "atlas-web")
	theme := themeBlock(t, body)

	if theme == "" {
		t.Fatal("a project that chose its colours was served no theme")
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Fatalf("the theme was filtered rather than emitted: %s", theme)
	}
	if want := string(boardTheme(core.DisplaySettings{PrimaryColor: "#1a7f4b", TextColor: "#3b2a1a"})); theme != want {
		t.Fatalf("theme = %q, want %q", theme, want)
	}
	for _, want := range []string{
		"--wb-primary: #1a7f4b;",
		"--wb-text: #3b2a1a;",
		// Derived, not merely carried: the family follows the choice.
		"--wb-primary-hover: #",
		"--wb-hairline: #",
		"--wb-text-shadow: rgba(59,42,26,.12);",
	} {
		if !strings.Contains(theme, want) {
			t.Errorf("the served theme does not carry %q: %s", want, theme)
		}
	}
	// It comes after the stylesheet it overrides, which is the only place a rule
	// of the same specificity wins.
	if strings.Index(body, "<style data-board-theme>") < strings.Index(body, "</style>") {
		t.Error("the theme is served before the stylesheet it overrides")
	}
}

// The board settings section is served for a board that can write them and for
// no other, the way the statuses section is served only for a board that can
// change those. A board with statuses to administer and no display writer would
// otherwise draw a Save that could only ever be refused.
func TestHandlerServesTheBoardSettingsSectionOnlyWhenItCanBeWritten(t *testing.T) {
	markers := []string{"data-display-panel", "data-display-panel-body", "data-display-panel-status"}

	without := boardMarkup(t, administrableBoardPage(t, core.DefaultVocabulary()))
	for _, marker := range markers {
		if strings.Contains(without, marker) {
			t.Errorf("a board that cannot write its display settings carries %q", marker)
		}
	}

	with := boardMarkup(t, displaySettingsBoardPage(t, nil))
	for _, marker := range markers {
		if !strings.Contains(with, marker) {
			t.Errorf("a board that can write its display settings does not carry %q", marker)
		}
	}
	panel := elementTag(t, with, "data-display-panel ")
	for _, attribute := range []string{
		`<div`,
		`class="admin"`,
		// Shipped hidden and outside main, mounted by the render for the route.
		`hidden`,
		`tabindex="-1"`,
		`role="group"`,
		`aria-labelledby="display-title"`,
	} {
		if !strings.Contains(panel, attribute) {
			t.Errorf("the board settings section %q does not carry %q", panel, attribute)
		}
	}
	if at := strings.Index(with, "</main>"); at < 0 || at > strings.Index(with, "data-display-panel ") {
		t.Error("the board settings section was rendered inside main, which the board occupies")
	}
}

// What a project's priority vocabulary does to the ink its cards are drawn in.
//
// The board used to name three priority colors and no more, so a project that
// added a fourth drew it in the meta row's ordinary dim ink. Two places already
// promised otherwise — docs/reference.md, where clearing a color "returns that
// priority to a color the board derives from its position", and
// core.PriorityDefinition.Color, which says the same — so what is asserted here
// is that promise rather than a scheme these tests invented.

// priorityInkBoardPage renders a board for a project with these priorities.
func priorityInkBoardPage(t *testing.T, priorities core.PriorityVocabulary, tasks []core.Task) string {
	t.Helper()
	handler := NewHandler(Options{
		Vocabulary: func(context.Context) (VocabularyState, error) {
			return VocabularyState{
				Vocabulary: core.DefaultVocabulary(),
				Head:       "head-1",
				Priorities: priorities,
			}, nil
		},
		RepoName: "workbook",
		List:     func(context.Context) ([]core.Task, error) { return tasks, nil },
	})
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	return response.Body.String()
}

// priorityInkBlock returns the served per-priority ink stylesheet, found by its
// own marker for the reason themeBlock is: the stylesheet above it declares
// `:root` too, and the display theme between them declares another.
func priorityInkBlock(t *testing.T, body string) string {
	t.Helper()
	const marker = `<style data-board-priority-ink>`
	at := strings.Index(body, marker)
	if at < 0 {
		return ""
	}
	end := strings.Index(body[at:], "</style>")
	if end < 0 {
		t.Fatal("the served priority ink is never closed")
	}
	return body[at+len(marker) : at+end]
}

// fourPriorityVocabulary is a project that added a priority above the built-in
// three — the case the board could not draw at all — with an optional stored
// color on `high`.
func fourPriorityVocabulary(t *testing.T, highColor string) core.PriorityVocabulary {
	t.Helper()
	vocabulary, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Urgent", Rank: "1/1", Tags: []core.PriorityTag{}},
		{Priority: core.PriorityHigh, Label: "High", Rank: "2/1", Tags: []core.PriorityTag{}, Color: highColor},
		{Priority: core.PriorityMedium, Label: "Medium", Rank: "3/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "4/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// Every priority a project configured is drawn in a color of its own, and the
// card the board renders carries the class that reads it.
func TestPriorityInkDrawsEveryPriorityInTheVocabulary(t *testing.T) {
	body := priorityInkBoardPage(t, fourPriorityVocabulary(t, ""), []core.Task{{
		ID: "WB-01J00000000000000000000009",
		TaskData: core.TaskData{
			Title:    "Ship it",
			Status:   core.StatusReady,
			Priority: "urgent",
		},
	}})
	block := priorityInkBlock(t, body)

	if block == "" {
		t.Fatal("a project with four priorities was served no per-priority ink at all")
	}
	for _, token := range []string{"urgent", "high", "medium", "low"} {
		if !strings.Contains(block, "--wb-priority-ink-"+token+":") {
			t.Errorf("the ink block declares nothing for %q: %s", token, block)
		}
		rule := ".priority--" + token + " { color: var(--wb-priority-ink-" + token + "); }"
		if !strings.Contains(block, rule) {
			t.Errorf("the ink block carries no %q, so nothing reads the property: %s", rule, block)
		}
	}
	// The rule above is only worth anything if the card really is marked with
	// the class it names — the fourth priority's badge included.
	if !strings.Contains(body, `class="priority priority--urgent"`) {
		t.Error("the card at the fourth priority carries no class the ink block can reach")
	}
	// And it comes after the stylesheet it overrides, which is the only place a
	// rule of the same specificity wins.
	if strings.Index(body, "<style data-board-priority-ink>") < strings.Index(body, "</style>") {
		t.Error("the priority ink is served before the stylesheet it overrides")
	}
}

// A stored color is what the priority is drawn in; a priority with none is
// drawn in the color its position derives. This is the documented contract:
// clearing a color returns a priority to a derived one because nothing stores a
// default to go back to.
func TestPriorityInkPrefersAStoredColorOverThePositionItDerives(t *testing.T) {
	body := priorityInkBoardPage(t, fourPriorityVocabulary(t, "#1a7f4b"), nil)
	block := priorityInkBlock(t, body)

	if want := "--wb-priority-ink-high: #1a7f4b;"; !strings.Contains(block, want) {
		t.Errorf("the ink block does not carry %q, so a stored color is not what the board draws: %s", want, block)
	}
	// The uncolored three name the triad's own properties rather than a literal,
	// which is what gives them a dark reading without stating one.
	for _, want := range []string{
		"--wb-priority-ink-urgent: var(--wb-priority-high);",
		"--wb-priority-ink-low: var(--wb-priority-low);",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the ink block does not derive %q from the position: %s", want, block)
		}
	}
	// A stored color is one value and the board has three palette statements, so
	// the stored one is lifted for a dark ground the way the accent is. Both
	// dark selectors say so, for the reason boardTheme states both.
	for _, selector := range []string{`:root:not([data-scheme="light"])`, `:root[data-scheme="dark"]`} {
		dark := declarationsIn(t, block, selector)
		value, declared := dark["--wb-priority-ink-high"]
		if !declared {
			t.Errorf("%s gives the stored color no dark reading, so it is drawn as chosen on a near-black card", selector)
			continue
		}
		if value == "#1a7f4b" {
			t.Errorf("%s repeats the stored color rather than lifting it for a dark ground", selector)
		}
		if _, derived := dark["--wb-priority-ink-urgent"]; derived {
			t.Errorf("%s restates a derived ink, which already follows the triad into dark", selector)
		}
	}
}

// A project that configured no priorities at all is drawn exactly as it always
// was. The built-in three are positions one, two and three of three, so the
// derivation has to land on the triad itself — otherwise the first `workbook
// priority` verb, which writes those same three into the ledger, would silently
// recolor a board nobody asked to change.
func TestPriorityInkKeepsTheBuiltInThreeOnTheirTriad(t *testing.T) {
	block := priorityInkBlock(t, priorityInkBoardPage(t, core.PriorityVocabulary{}, nil))

	for _, want := range []string{
		"--wb-priority-ink-high: var(--wb-priority-high);",
		"--wb-priority-ink-medium: var(--wb-priority-medium);",
		"--wb-priority-ink-low: var(--wb-priority-low);",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the ink block does not carry %q, so seeding the built-in three would recolor the board: %s", want, block)
		}
	}
}

// A derived ink is written as a reference, never as a literal.
//
// This is what keeps the per-priority family inside the guards above rather
// than exempt from them: those count every color literal the page writes and
// demand a dark reading for every palette property, and a family that states no
// literal and declares no table property is answerable to neither. It is also
// why a derived ink is correct in dark without being stated twice — it resolves
// through the triad, which the scheme already moves.
func TestPriorityInkWritesNoLiteralForAPriorityWithNoStoredColor(t *testing.T) {
	block := priorityInkBlock(t, priorityInkBoardPage(t, fourPriorityVocabulary(t, ""), nil))

	for _, literal := range colorLiteral.FindAllString(block, -1) {
		if !strings.HasPrefix(literal, "color-mix(") {
			t.Errorf("the ink block writes the literal %s for a priority that stores no color: %s", literal, block)
		}
	}
	for _, token := range schemeTokens {
		if strings.Contains(block, token.property+":") {
			t.Errorf("the ink block declares %s, which belongs to the scheme's own palette", token.property)
		}
	}
}

// forwardedPriorityVocabulary is a project that renamed `low` to `later` and
// removed `someday` into it, plus a chain that ends nowhere: `ancient` was
// renamed to `gone`, and `gone` is not a priority this project has.
func forwardedPriorityVocabulary(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	vocabulary, err := core.NewPriorityVocabulary(
		[]core.PriorityDefinition{
			{Priority: "urgent", Label: "Urgent", Rank: "1/1", Tags: []core.PriorityTag{}},
			{Priority: core.PriorityHigh, Label: "High", Rank: "2/1", Tags: []core.PriorityTag{}},
			{Priority: core.PriorityMedium, Label: "Medium", Rank: "3/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
			{Priority: "later", Label: "Later", Rank: "4/1", Tags: []core.PriorityTag{}},
		},
		[]core.PriorityAlias{{From: core.PriorityLow, To: "later"}, {From: "ancient", To: "gone"}},
		[]core.RetiredPriority{{Priority: "someday", Destination: "later"}},
	)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// A card is rendered with the priority it is stored under, and a rename or a
// removal retires that name out from under it: the board an open page is
// showing is deliberately not rebuilt when the vocabulary changes, and a page
// served to a clone that has not folded the rename carries the old name too. So
// a retired name is drawn in the ink of the priority it now means, rather than
// matching no rule at all and dropping to the meta row's dim ink.
func TestPriorityInkKeepsACardColoredThroughARenameOrARemoval(t *testing.T) {
	block := priorityInkBlock(t, priorityInkBoardPage(t, forwardedPriorityVocabulary(t), nil))

	if block == "" {
		t.Fatal("a project with four priorities was served no per-priority ink at all")
	}
	for name, meaning := range map[string]string{
		"low":     "renamed to later",
		"someday": "removed into later",
	} {
		rule := ".priority--" + name + " { color: var(--wb-priority-ink-later); }"
		if !strings.Contains(block, rule) {
			t.Errorf(
				"the ink block carries no %q, so a card rendered at %q (%s) matches no rule "+
					"and falls to the meta row's dim ink: %s",
				rule, name, meaning, block,
			)
		}
	}
	// The forwarding is a rule of its own, never a second declaration: a retired
	// name owns no color, it borrows the live priority's.
	if strings.Contains(block, "--wb-priority-ink-low:") || strings.Contains(block, "--wb-priority-ink-someday:") {
		t.Errorf("the ink block declares a property for a priority that is no longer live: %s", block)
	}
}

// A retired name whose chain ends outside the live set is given no rule at all.
// There is no property to point it at, and a rule reading one this file never
// declared would leave the card exactly as dim while putting a dangling
// reference on every board that carries it.
func TestPriorityInkPointsNoRuleAtAPropertyItNeverDeclared(t *testing.T) {
	block := priorityInkBlock(t, priorityInkBoardPage(t, forwardedPriorityVocabulary(t), nil))

	if strings.Contains(block, ".priority--ancient") {
		t.Errorf("the ink block draws a priority whose forwarding chain reaches no live priority: %s", block)
	}
	for _, reference := range priorityInkReference.FindAllStringSubmatch(block, -1) {
		if !strings.Contains(block, reference[1]+":") {
			t.Errorf("the ink block reads %s, which nothing in it declares: %s", reference[1], block)
		}
	}
}

// priorityInkReference finds every per-priority property a rule in the ink
// block reads, so each can be held to having been declared there.
var priorityInkReference = regexp.MustCompile(`var\((--wb-priority-ink-[a-z0-9-]+)\)`)
