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
	body := displayBoardPage(t, core.DisplaySettings{}, "workbook")

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
		if got := strings.Count(body, token.legacy); got != expectedLegacyMentions(token.legacy) {
			t.Errorf("the page writes %s out %d times, want %d — once as this property's default%s",
				token.legacy, got, expectedLegacyMentions(token.legacy), priorityException(token.legacy))
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
// Counted per literal rather than per token, because two of them are declared
// twice on purpose: see schemeTokens for why #fff and #8496b0 each carry two
// properties.
func TestHandlerStylesheetDeclaresTheSchemePaletteAsItsDefaults(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{}, "workbook")

	// Counted across every table that declares a default, not just this one:
	// #2457d6 is the accent's default as well as the priority triad's blue, so a
	// count taken from schemeTokens alone would call the second one a stray.
	declarations := map[string]int{}
	for _, token := range append(append([]themeToken{}, primaryThemeTokens...), textThemeTokens...) {
		declarations[token.legacy]++
	}
	for _, token := range schemeTokens {
		declarations[token.legacy]++
		if declaration := token.property + ": " + token.legacy + ";"; !strings.Contains(body, declaration) {
			t.Errorf("the stylesheet does not declare %q", declaration)
		}
	}

	for literal, declared := range declarations {
		if got := countColorLiteral(body, literal); got != declared {
			t.Errorf("the page writes %s out %d times, want %d — once for each property that declares it as its default",
				literal, got, declared)
		}
	}
}

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

// expectedLegacyMentions is how many times a legacy literal may still appear in
// the served page: once as the property's own default, and — for the accent —
// once more in `.priority--low`, which keeps a static blue on purpose.
func expectedLegacyMentions(literal string) int {
	switch literal {
	case priorityLowBlue:
		return 2
	default:
		return 1
	}
}

func priorityException(literal string) string {
	if literal == priorityLowBlue {
		return ", and once in the priority triad"
	}
	return ""
}

// priorityLowBlue is the accent, and the one place in this stylesheet that keeps
// it written out.
const priorityLowBlue = "#2457d6"

// `.priority--low` does not follow a project's accent, and this is the only
// place in the stylesheet that is true of.
//
// It is not an oversight to be tidied up later. The three priority colours are a
// triad read against each other — the danger red for high, the warning amber for
// medium, this blue for low — and a project that picks a red-ish accent would
// make "low" read as "high" on every card on the board. The rule is stated here
// because the conversion that took every other occurrence of this literal was
// mechanical, and the next such sweep will reach this one too.
func TestHandlerStylesheetKeepsThePriorityTriadOffTheProjectsAccent(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{PrimaryColor: "#b42318"}, "workbook")

	const rule = ".priority--low { color: var(--wb-priority-low); }"
	if !strings.Contains(body, rule) {
		t.Errorf("the stylesheet no longer carries %q, so a red-ish accent makes \"low\" read as \"high\"", rule)
	}
	// And the property it reads is still this blue, rather than a second name
	// for the accent — which is the whole of the claim.
	if !strings.Contains(body, "--wb-priority-low: "+priorityLowBlue+";") {
		t.Errorf("--wb-priority-low is no longer declared as %s", priorityLowBlue)
	}
	// The other two are held off the accent for the same reason, and are here so
	// the triad is asserted as a triad. They read scheme tokens rather than
	// literals now, which does not weaken the claim: a scheme token is not
	// derived from a project's colour either, so a red-ish accent still leaves
	// all three where they are. This test proves that directly — the accent it
	// configures is the very red --wb-danger resolves to.
	for _, sibling := range []string{
		".priority--high { color: var(--wb-danger); }",
		".priority--medium { color: var(--wb-warning); }",
	} {
		if !strings.Contains(body, sibling) {
			t.Errorf("the stylesheet no longer carries %q", sibling)
		}
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
