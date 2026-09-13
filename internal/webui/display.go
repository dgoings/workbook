package webui

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

// DisplayChange is what one save on the board settings form proposes.
//
// It states the whole configuration rather than the members it changes: the
// form is three fields and one Save, so what the reader is proposing is the
// three values as they now read, and an empty field is a setting they have
// cleared. A partial body would make an omitted member and an emptied one the
// same request, which is exactly the distinction this form exists to offer.
//
// What that costs is a server that has to work out what actually changed, and
// that is deliberate too — see the capability's own diff. A save with nothing
// edited in it must record nothing at all, because a display operation carries a
// generation-two marker that parks every older clone on this project for good.
type DisplayChange struct {
	Name         string
	PrimaryColor string
	TextColor    string
	// ExpectedHead is the configuration ledger tip the client composed this
	// change against, required for the reason a status change requires one:
	// these are project-wide decisions, and one made against a configuration
	// somebody else has already replaced is not the change its author meant.
	ExpectedHead string
}

// DisplaySettingsWriter records a project's display settings. A board built
// without one draws no board settings section and answers the route the way
// every other unwired capability is answered.
type DisplaySettingsWriter func(context.Context, DisplayChange) (DisplayMutation, error)

// DisplayMutation is what one save produced: the settings as they now stand and
// the tip they stand at, in the same state the vocabulary is read through
// because they are read from the same commit.
type DisplayMutation struct {
	State    VocabularyState
	Warnings []core.Warning
}

// DisplayDocument is a project's display settings as the board reads them.
//
// It is a document of its own rather than three members on the vocabulary's,
// because it is also what the mutation answers with and what a stale write hands
// back — and every one of those needs the head, which is the thing the client's
// next change has to name.
//
// Each value is omitted when it is not configured. A default is a read-time
// answer here exactly as it is in the ledger: an absent name is what
// core.DefaultProjectName is for, and absent colors are the stylesheet's own.
type DisplayDocument struct {
	Format       string `json:"format"`
	Version      int    `json:"version"`
	Head         string `json:"head"`
	Name         string `json:"name,omitempty"`
	PrimaryColor string `json:"primaryColor,omitempty"`
	TextColor    string `json:"textColor,omitempty"`
	// Theme is the `:root` override these settings ask for, composed here by the
	// same boardTheme the page's own `<style>` block is rendered from, and empty
	// for a project that has chosen no colors.
	//
	// It rides on the document for the reason PriorityVocabularyDocument.Ink
	// does: a page that adopts a save adopts what the save looks like, by
	// replacing the text of that element, so the board is drawn in the new
	// colors without a reload. And it is the composed CSS rather than the colors
	// it was composed from for the same reason too — every byte of it is
	// answered for by boardTheme, and a client handed the colors could vouch for
	// none of it.
	//
	// Unlike the three settings above it is not omitted when empty: clearing a
	// color is a change the board has to draw, and a client handed no member
	// would leave the accent it was opened with in place.
	Theme string `json:"theme"`
}

// DisplayMutationDocument is what a save answers with, mirroring
// VocabularyMutationDocument: the whole document the read serves, so a client
// renders the result of a change through the code that rendered the page.
type DisplayMutationDocument struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Display  DisplayDocument `json:"display"`
	Warnings []core.Warning  `json:"warnings,omitempty"`
}

// DisplayErrorDocument is the error envelope with the settings a refused save
// should be recomposed against, the sibling of VocabularyErrorDocument and for
// the same reason: a stale write means somebody else has already configured this
// project, and answering with what they configured saves the client the refetch
// it would otherwise need before it could tell the reader anything.
type DisplayErrorDocument struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Error   ErrorBody        `json:"error"`
	Display *DisplayDocument `json:"display,omitempty"`
}

// displayDocument renders one read of the project's display settings. The head
// is the vocabulary's own, because both come from the one VocabularyState a
// request resolves: a board that read the statuses and then the name could be
// answered from either side of a fetch and would draw itself out of two
// configurations.
func displayDocument(state VocabularyState) DisplayDocument {
	return DisplayDocument{
		Format:       "workbook.display",
		Version:      1,
		Head:         state.Head,
		Name:         state.Display.Name,
		PrimaryColor: state.Display.PrimaryColor,
		TextColor:    state.Display.TextColor,
		// The same composer the page's own override is rendered from, called on
		// the same settings, so a saved board is drawn by the code that drew the
		// board it was saved from.
		Theme: string(boardTheme(state.Display)),
	}
}

// setDisplayRequest is the board settings form as it arrives.
//
// Every value is a plain string rather than a pointer, because this body states
// the whole configuration: an omitted member and an emptied one are the same
// request, which is what "clear this setting by emptying the field" means. The
// head is a pointer for the reason a status change's is — a client that cannot
// say what it composed the change against does not know what it is changing,
// and an empty head is a real answer that a missing member is not.
type setDisplayRequest struct {
	Name         string  `json:"name"`
	PrimaryColor string  `json:"primaryColor"`
	TextColor    string  `json:"textColor"`
	ExpectedHead *string `json:"expectedHead"`
}

// updateDisplay records what this project calls its board and the colors it
// draws it in.
//
// It is one route with one body for three settings because the form is one
// Save: a reader renaming the board and lightening its ink has made one
// decision, and three requests would leave a project half configured the moment
// the second was refused. What is actually recorded is the capability's to
// decide — see the board's own diff, and why a save that changes nothing must
// record nothing.
func (handler *handler) updateDisplay(writer http.ResponseWriter, request *http.Request) {
	if handler.SetDisplay == nil {
		handler.writeError(writer, core.Errorf(core.CategoryOperational, "display configuration is not configured"))
		return
	}
	var body setDisplayRequest
	if err := decodeRequest(request.Body, &body); err != nil {
		handler.writeError(writer, decodeRequestError("decode display settings", err))
		return
	}
	head, err := displayHead(body.ExpectedHead)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	mutation, err := handler.SetDisplay(request.Context(), DisplayChange{
		Name:         body.Name,
		PrimaryColor: body.PrimaryColor,
		TextColor:    body.TextColor,
		ExpectedHead: head,
	})
	if err != nil {
		handler.writeDisplayError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, DisplayMutationDocument{
		Format:   "workbook.display-mutation",
		Version:  1,
		Display:  displayDocument(mutation.State),
		Warnings: mutation.Warnings,
	})
}

// displayHead reads the configuration tip a save was composed against, refusing
// one that names none. It is vocabularyHead's sibling and says the same thing
// about a different decision: what is required is that the member is there, not
// that it says something, because a project whose ledger has never been seeded
// honestly has no head to name.
func displayHead(expected *string) (string, error) {
	if expected == nil {
		return "", core.Errorf(core.CategoryValidation,
			"expectedHead is required; it names the project configuration this change was composed against")
	}
	return *expected, nil
}

// writeDisplayError reports a refused save, and hands back the settings the
// client should recompose it against when the refusal was that it was looking at
// old ones.
//
// It is writeVocabularyError's sibling, down to what it does when the read it
// would answer with fails: the client loses its re-render, not its refusal.
func (handler *handler) writeDisplayError(writer http.ResponseWriter, request *http.Request, err error) {
	body := errorBody(err)
	if body.Category != core.CategoryStaleWrite {
		handler.writeError(writer, err)
		return
	}
	state, _, readErr := handler.vocabulary(request)
	if readErr != nil {
		handler.writeError(writer, err)
		return
	}
	document := displayDocument(state)
	writeJSON(writer, statusForError(body.Category), DisplayErrorDocument{
		Format:  "workbook.error",
		Version: 1,
		Error:   body,
		Display: &document,
	})
}

// projectName is what this board calls itself: the project's own name, or the
// generic one every unnamed Workbook board carries.
func projectName(settings core.DisplaySettings) string {
	if settings.Name == "" {
		return core.DefaultProjectName
	}
	return settings.Name
}

// boardTitleSuffix is what a route that is not the board appends to its own
// name. It is the project's name where there is one and the product's where
// there is not: "New task · Workbook board" would read as a board called "New
// task", which is the one thing a title is for.
func boardTitleSuffix(settings core.DisplaySettings) string {
	if settings.Name == "" {
		return "Workbook"
	}
	return settings.Name
}

// boardEyebrow names the checkout this board is serving.
//
// A board built without a repository name keeps the words every board carried
// before a project could be named — the line was never blank, and a colon with
// nothing after it is worse than the generic sentence it replaced.
func boardEyebrow(repository string) string {
	if repository == "" {
		return "Repository workbench"
	}
	return "Repository: " + repository
}

// The custom properties the stylesheet reads for every color a project may
// choose, each with the literal it defaults to.
//
// The defaults are the exact colors this page was drawn in before any of this
// existed, so a project that has configured nothing renders what it always
// rendered — and the derivation below is only ever asked about a color somebody
// chose, which is why generate(#2457d6) has to land near this family without
// having to reproduce it.
//
// Only three families are here. The primary blue is the board's accent and the
// hairline and the ink follow the reader's choice with it; the neutral greys and
// the semantic red, amber and green stay literal, because a project that picks a
// green accent has not thereby decided that its danger colour is green.
// `.priority--low` keeps its own static blue for the same reason: it sits in a
// triad with the danger red and the warning amber, and a red-ish accent must not
// make "low" read as "high".
type themeToken struct {
	property string
	// legacy is the literal the stylesheet declares as this property's default.
	legacy string
	// derive answers what a configured color makes of this property.
	derive func(themeColor) string
}

// The accent family: the primary itself, the two darker steps a filled control
// takes, the two translucent rings, four pale surfaces, a chip, a muted border,
// a desaturated ink, and the hairline every rule on the page is drawn in.
var primaryThemeTokens = []themeToken{
	{"--wb-primary", "#2457d6", func(color themeColor) string { return color.hex() }},
	{"--wb-primary-hover", "#1d49b7", func(color themeColor) string { return color.scaled(.865, .848) }},
	{"--wb-primary-edge", "#173f9e", func(color themeColor) string { return color.scaled(.758, .724) }},
	{"--wb-primary-glow", "rgba(36,87,214,.18)", func(color themeColor) string { return color.alpha(".18") }},
	{"--wb-primary-glow-strong", "rgba(36,87,214,.28)", func(color themeColor) string { return color.alpha(".28") }},
	{"--wb-primary-tint-1", "#edf3ff", func(color themeColor) string { return color.toned(.15, .9647) }},
	{"--wb-primary-tint-2", "#eef3ff", func(color themeColor) string { return color.toned(.15, .9667) }},
	{"--wb-primary-tint-3", "#f3f7ff", func(color themeColor) string { return color.toned(.15, .9765) }},
	{"--wb-primary-tint-4", "#f8faff", func(color themeColor) string { return color.toned(.15, .9863) }},
	{"--wb-primary-chip", "#d3e0fa", func(color themeColor) string { return color.toned(.219, .9039) }},
	{"--wb-primary-muted", "#b7c9ea", func(color themeColor) string { return color.toned(.287, .8176) }},
	{"--wb-primary-ink", "#526b9b", func(color themeColor) string { return color.toned(.410, .4647) }},
	{"--wb-hairline", "#d5deea", func(color themeColor) string { return color.toned(.118, .8765) }},
}

// The ink family: the page's text colour and the four shadows cast in it. The
// shadows are the same colour at four opacities, so they are derived by opacity
// alone — a shadow that did not follow the ink would stay blue-black under
// brown prose.
var textThemeTokens = []themeToken{
	{"--wb-text", "#172033", func(color themeColor) string { return color.hex() }},
	{"--wb-text-shadow-strong", "rgba(23,32,51,.35)", func(color themeColor) string { return color.alpha(".35") }},
	{"--wb-text-shadow", "rgba(23,32,51,.12)", func(color themeColor) string { return color.alpha(".12") }},
	{"--wb-text-shadow-soft", "rgba(23,32,51,.08)", func(color themeColor) string { return color.alpha(".08") }},
	{"--wb-text-shadow-faint", "rgba(23,32,51,.05)", func(color themeColor) string { return color.alpha(".05") }},
}

// schemeToken is a colour the board is drawn in that no project chooses.
//
// The three families above follow a reader's chosen accent and ink. These do
// not, and that is the same decision stated from the other side: the neutral
// greys and the semantic red, amber and green are properties of the colour
// scheme the board is rendered in, not of the project rendered on it.
//
// They carry no derive for that reason. They are declared here anyway, rather
// than living only in the stylesheet, so the guard that holds the derived
// families to their defaults can hold these to the same claim — declared once
// as the default, and written out nowhere else — which is what keeps one
// control from being missed when the palette next moves.
type schemeToken struct {
	property string
	legacy   string
	// dark is what this property becomes where the reader asked for a dark
	// scheme. Stated rather than derived: these are not a project's colours to
	// choose, so there is no chosen colour to derive them from.
	dark string
}

// The neutral surfaces, the rules drawn on them, the ink written on them, the
// semantic three, and the priority triad. Surfaces are numbered rather than
// named because they are one ramp of six near-identical greys, the way the
// primary tints already are.
//
// Three literals appear more than once, because a value that carries two roles
// has to be two properties before either role can move:
//
//	#fff     a surface where it is a background (--wb-surface), and ink where
//	         it is a colour — on a saturated accent fill (--wb-on-accent) or on
//	         the derived --wb-text (--wb-on-text). The surface moves with the
//	         scheme; each ink answers to whatever it is written on, and
//	         --wb-text is derived from a colour a project chose, so the two
//	         inks cannot be one property.
//	#8496b0  a quiet rule (--wb-border-quiet) in one place and quiet ink
//	         (--wb-ink-quiet) in another.
//	#2457d6  the accent a project may replace (--wb-primary) and the triad's
//	         blue that no project reaches (--wb-priority-low). These two agree
//	         only for as long as nobody chooses an accent.
//
//	#8f1d1d  the edge under --wb-danger (--wb-danger-edge) and its pressed fill
//	         (--wb-danger-hover). Both go darker than the fill in light and part
//	         company in dark, where an edge darkens and a press lightens.
//
// --wb-danger-strong is gone. It was both the fill under --wb-danger and error
// ink on a pale surface, which in a dark scheme pull in opposite directions;
// the ink sites read --wb-danger-ink now, and what was left of the fill role
// split again into the edge and the press above.
var schemeTokens = []schemeToken{
	{"--wb-surface", "#fff", "#161c26"},
	{"--wb-on-accent", "#fff", "#0f141c"},
	// Ink on the derived --wb-text, which a dark scheme lifts to a pale grey. So
	// this goes the other way with it: dark ink on a light chip, the same
	// inversion --wb-on-accent makes under a lifted accent.
	{"--wb-on-text", "#fff", "#0f141c"},
	{"--wb-surface-1", "#fbfcfe", "#151b24"},
	{"--wb-surface-2", "#fafbfd", "#151b24"},
	{"--wb-surface-3", "#f6f8fc", "#141a22"},
	{"--wb-surface-4", "#f2f5f9", "#141a22"},
	{"--wb-surface-5", "#f1f4f8", "#131820"},
	{"--wb-surface-6", "#eef2f8", "#131820"},
	{"--wb-ground", "#e9eef5", "#0f141c"},
	{"--wb-column-ground", "rgba(255,255,255,.36)", "rgba(22,28,38,.36)"},
	{"--wb-column-ground-deleted", "rgba(232,237,244,.5)", "rgba(15,20,28,.5)"},

	{"--wb-border", "#b9c6d8", "#2f3a4a"},
	{"--wb-border-strong", "#9eafc5", "#3d4b5f"},
	{"--wb-border-firm", "#aab8cc", "#3d4b5f"},
	{"--wb-border-soft", "#cbd5e2", "#2f3a4a"},
	{"--wb-border-muted", "#e1e7f0", "#232c39"},
	{"--wb-border-underline", "#a9b7ca", "#3d4b5f"},
	{"--wb-border-quiet", "#8496b0", "#3d4b5f"},

	{"--wb-ink", "#34425a", "#c2ccdc"},
	{"--wb-ink-muted", "#56647a", "#8593ab"},
	{"--wb-ink-soft", "#4e5d73", "#9aa7bd"},
	{"--wb-ink-faint", "#5f6c85", "#9aa7bd"},
	{"--wb-ink-dim", "#526075", "#8593ab"},
	{"--wb-ink-quiet", "#8496b0", "#8593ab"},

	{"--wb-danger", "#b42318", "#f0806c"},
	{"--wb-danger-ink", "#9c2f25", "#f0806c"},
	// The edge and the pressed fill were one property, because in a light scheme
	// both go darker than --wb-danger and one value served. A dark scheme pulls
	// them apart the way the accent family already shows: --wb-primary-edge goes
	// darker than its fill and --wb-primary-hover goes lighter, in opposite
	// directions, and a single value between them is 1.01:1 against the fill —
	// an invisible border and a hover that does not read as a press.
	//
	// They keep one literal in light, where they genuinely agree.
	{"--wb-danger-edge", "#8f1d1d", "#ea5035"},
	{"--wb-danger-hover", "#8f1d1d", "#f4a496"},
	{"--wb-danger-surface", "#fdecea", "#2a1614"},
	// One unit of blue from --wb-danger-surface, which is much more likely to
	// be a slip than a decision. It is kept as its own property because
	// collapsing the two would change what the board renders, and this change
	// deliberately changes nothing.
	{"--wb-danger-surface-alt", "#fdeceb", "#2a1614"},
	{"--wb-danger-glow", "rgba(180,35,24,.28)", "rgba(240,128,108,.32)"},

	{"--wb-warning", "#b45309", "#e0a45c"},
	{"--wb-warning-ink", "#7c3b08", "#e8c07a"},
	{"--wb-warning-surface", "#fff6e8", "#241d12"},
	{"--wb-warning-border", "#e0b483", "#5a4520"},

	// --wb-success shares its dark reading with --wb-success-ink, the way
	// --wb-danger shares one with --wb-danger-ink: a fill and an ink of the same
	// family land in the same band once both have to read against a dark ground.
	{"--wb-success", "#1a7f4b", "#7fc79b"},
	{"--wb-success-ink", "#14663c", "#7fc79b"},
	{"--wb-success-surface", "#e8f4ec", "#12241a"},

	// The priority triad. It does not follow a project's accent in either scheme
	// — a scheme property is not derived from one — and what it could not go on
	// doing is staying three mid tones on a near-black card, which is the one
	// thing a colour scheme has to be able to say about it.
	//
	// High and medium take the readings --wb-danger and --wb-warning take, which
	// is the relationship they already had in light: the triad is drawn in the
	// semantic colours without being derived from them, so the two families move
	// together and stay separate properties.
	{"--wb-priority-high", "#b42318", "#f0806c"},
	{"--wb-priority-medium", "#b45309", "#e0a45c"},
	{"--wb-priority-low", "#2457d6", "#6f9bf5"},
}

// schemeVariant is what one derived property becomes under a colour scheme
// other than the one the stylesheet is written in.
//
// It is a type of its own rather than two more fields on themeToken because the
// two answer different questions. themeToken says what a project's colour makes
// of a property; this says what the *scheme* makes of it, and the scheme is not
// something a project chooses. Keeping them apart also keeps the light families
// exactly as they were, which is worth more than the shared struct: they are the
// board as it renders today.
type schemeVariant struct {
	property string
	// dark is the literal the stylesheet declares for a project that has chosen
	// no colour of its own.
	dark string
	// derive answers what a configured color makes of this property in dark.
	derive func(themeColor) string
}

// The accent family, lifted.
//
// A blue that reads on white does not read on near-black: #2457d6 sits at 49%
// lightness and disappears into a dark card. So every solid step states its
// lightness outright, in the band a dark ground leaves legible, and keeps the
// hue the project chose — a themed board stays itself in dark mode rather than
// being flattened to one generic blue.
//
// The tints invert their role rather than their value. On white they are pale
// surfaces a shade off the page; on black they are dark surfaces a shade off it,
// which is the same relationship rendered in the other direction.
var darkPrimaryVariants = []schemeVariant{
	{"--wb-primary", "#5c8bff", func(color themeColor) string { return color.toned(1, .68) }},
	{"--wb-primary-hover", "#8aabff", func(color themeColor) string { return color.toned(1, .77) }},
	{"--wb-primary-edge", "#3669e8", func(color themeColor) string { return color.toned(1, .56) }},
	{"--wb-primary-glow", "rgba(92,139,255,.28)", func(color themeColor) string { return color.tonedAlpha(1, .68, ".28") }},
	{"--wb-primary-glow-strong", "rgba(92,139,255,.40)", func(color themeColor) string { return color.tonedAlpha(1, .68, ".40") }},
	{"--wb-primary-tint-1", "#141f3b", func(color themeColor) string { return color.toned(.22, .155) }},
	{"--wb-primary-tint-2", "#131d37", func(color themeColor) string { return color.toned(.20, .145) }},
	{"--wb-primary-tint-3", "#121c32", func(color themeColor) string { return color.toned(.18, .135) }},
	{"--wb-primary-tint-4", "#121a2e", func(color themeColor) string { return color.toned(.16, .125) }},
	{"--wb-primary-chip", "#28375d", func(color themeColor) string { return color.toned(.30, .26) }},
	{"--wb-primary-muted", "#33487a", func(color themeColor) string { return color.toned(.40, .34) }},
	{"--wb-primary-ink", "#7c99de", func(color themeColor) string { return color.toned(.55, .68) }},
	{"--wb-hairline", "#282f3e", func(color themeColor) string { return color.toned(.118, .20) }},
}

// The ink family, lifted — and its shadows are not derived at all.
//
// On white, a shadow is the ink at a low opacity, because what a page casts is
// its own darkness. On a dark ground the darkness is already there and a shadow
// made of the ink would be a light smear; what separates a raised surface from
// the one behind it is black. So the four shadows are stated rather than
// derived, and stay black under prose of any colour.
var darkTextVariants = []schemeVariant{
	{"--wb-text", "#dee3ed", func(color themeColor) string { return color.toned(.55, .90) }},
	{"--wb-text-shadow-strong", "rgba(0,0,0,.55)", func(themeColor) string { return "rgba(0,0,0,.55)" }},
	{"--wb-text-shadow", "rgba(0,0,0,.45)", func(themeColor) string { return "rgba(0,0,0,.45)" }},
	{"--wb-text-shadow-soft", "rgba(0,0,0,.35)", func(themeColor) string { return "rgba(0,0,0,.35)" }},
	{"--wb-text-shadow-faint", "rgba(0,0,0,.30)", func(themeColor) string { return "rgba(0,0,0,.30)" }},
}

// boardTheme renders the `:root` block a project's chosen colors ask for, and
// nothing at all for a project that has chosen none.
//
// It is composed here rather than interpolated into the stylesheet a value at a
// time because html/template filters an interpolated CSS value it cannot vouch
// for and writes ZgotmplZ in its place, which would silently unstyle the board.
// Composing it in Go makes that vouching real rather than assumed: every byte of
// the result is either one of these property names or a number this file
// formatted, and the only thing a configured value contributes is three integers
// parsed out of a string core has already validated as `#rrggbb`. A value that
// does not parse contributes nothing and the family keeps its defaults.
func boardTheme(settings core.DisplaySettings) template.CSS {
	declarations := make([]string, 0, len(primaryThemeTokens)+len(textThemeTokens))
	declarations = append(declarations, themeDeclarations(settings.PrimaryColor, primaryThemeTokens)...)
	declarations = append(declarations, themeDeclarations(settings.TextColor, textThemeTokens)...)
	if len(declarations) == 0 {
		return ""
	}

	// And the same families again for a reader in dark mode.
	//
	// This block is not optional garnish. The stylesheet states its own dark
	// palette in a media query, but this override is served in a later <style>
	// element, and a media query buys no specificity — so a project that chose a
	// colour would otherwise have its light accent win in dark mode, which is
	// the one place that accent cannot be read.
	dark := make([]string, 0, len(darkPrimaryVariants)+len(darkTextVariants))
	dark = append(dark, schemeDeclarations(settings.PrimaryColor, darkPrimaryVariants)...)
	dark = append(dark, schemeDeclarations(settings.TextColor, darkTextVariants)...)

	// The same two selectors the stylesheet states its own dark palette in, and
	// for the same reason. A reader who chose light has to beat their system's
	// dark, and one who chose dark has to beat a system that did not. An
	// override written against a bare `:root` inside the media query wins over
	// the stylesheet in exactly the case the reader asked it not to, and the
	// board comes out with light neutrals under a dark accent.
	return template.CSS(":root { " + strings.Join(declarations, " ") + " }" +
		` @media (prefers-color-scheme: dark) { :root:not([data-scheme="light"]) { ` + strings.Join(dark, " ") + " } }" +
		` :root[data-scheme="dark"] { ` + strings.Join(dark, " ") + " }")
}

// priorityInk renders the ink every one of a project's priorities is drawn in:
// one custom property per priority and the rule that reads it.
//
// It is a stylesheet of its own rather than more of boardTheme because the two
// answer different questions. A theme is what a project's *chosen* colors ask
// for, and a project that chose none is served none — that is the rule
// boardTheme's own comment states and its tests hold it to. A priority's ink is
// not a choice a project has to have made: every board has priorities, the
// stylesheet above can only name three of them by hand, and a project that
// added a fourth was drawing it in the meta row's dim ink with nothing chosen
// or wrong anywhere.
//
// It is composed in Go rather than interpolated for the reason boardTheme gives,
// and it is written into the page's markup by the same `template.CSS` route,
// which bypasses contextual escaping by design. So every byte of what follows is
// answered for here:
//
//   - The property and class names are built from a priority token, which core
//     validates as lowercase letters and digits separated by single hyphens —
//     exactly the charset a CSS identifier takes unescaped, which is the reason
//     priorityTokenPattern's own comment gives for the rule. A name that does
//     not pass that check is dropped rather than written, so a token a corrupted
//     or hostile peer put in the ledger cannot close a declaration and open a
//     rule of its own.
//   - A stored color is parsed by parseThemeColor and then *re-rendered* from
//     the three integers it yielded, so what reaches the page is a hex triple
//     this file formatted rather than the stored string. A value core's
//     ValidateThemeColor would not have accepted does not parse, contributes
//     nothing, and leaves that priority on the color its position derives.
//   - A retired name's rule is built from a token out of the forwarding chains
//     rather than out of the priority list, so it is put through the same
//     ValidatePriorityToken check rather than assumed to have had one. Those
//     chains are normalized against that very function when a document is
//     authored, but a vocabulary decoded from a checkpoint is indexed without
//     re-normalizing, so a peer's corrupted chain arrives here unchecked the
//     same way a corrupted priority list would.
//   - Everything else is a property name from this file or a number formatted
//     here.
func priorityInk(priorities core.PriorityVocabulary) template.CSS {
	// The effective reading, which is what the board draws either way: a project
	// that configured no priorities is using the built-in three.
	document := priorities.EffectiveDocument()
	live := make([]core.PriorityDefinition, 0, len(document.Priorities))
	for _, definition := range document.Priorities {
		if core.ValidatePriorityToken(definition.Priority) == nil {
			live = append(live, definition)
		}
	}
	if len(live) == 0 {
		return ""
	}

	declarations := make([]string, 0, 2*len(live))
	rules := make([]string, 0, 2*len(live))
	dark := make([]string, 0, 2*len(live))
	declared := make(map[core.Priority]struct{}, len(live))
	for index, definition := range live {
		property := priorityInkProperty(definition.Priority)
		chip := priorityChipProperty(definition.Priority)
		if color, parsed := parseThemeColor(definition.Color); parsed {
			declarations = append(declarations, property+": "+color.hex()+";")
			declarations = append(declarations, chip+": "+priorityChip(color)+";")
			// A stored color is one value, and the board has three palette
			// statements. A mid-toned red chosen against white is the very thing
			// that disappears into a near-black card, so it is lifted for a dark
			// ground the way a chosen accent is — the same transform, for the
			// reason the priority triad's own comment above gives.
			lifted := color.tonedColor(1, .68)
			dark = append(dark, property+": "+lifted.hex()+";")
			// And the chip is derived from the ink as the scheme reads it, not
			// lifted along with it: the offset that makes a label legible runs
			// the other way once the label itself has moved.
			dark = append(dark, chip+": "+priorityChip(lifted)+";")
		} else {
			declarations = append(declarations, property+": "+derivedPriorityInk(index, len(live))+";")
			declarations = append(declarations, chip+": "+derivedPriorityChip(property, priorityChipLightWash)+";")
			dark = append(dark, chip+": "+derivedPriorityChip(property, priorityChipDarkWash)+";")
		}
		rules = append(rules, ".priority--"+string(definition.Priority)+" { color: var("+property+"); }")
		rules = append(rules, ".priority--"+string(definition.Priority)+" { background: var("+chip+"); }")
		declared[definition.Priority] = struct{}{}
	}
	rules = append(rules, forwardedPriorityRules(priorities, document, declared)...)

	block := ":root { " + strings.Join(declarations, " ") + " }"
	if len(dark) > 0 {
		// Both dark selectors, for the reason boardTheme states both. A derived
		// ink is deliberately absent from them: it is written as a reference to
		// the triad's properties, which the scheme already moves, so restating it
		// here would be a second copy of a reading that is already correct.
		block += ` @media (prefers-color-scheme: dark) { :root:not([data-scheme="light"]) { ` + strings.Join(dark, " ") + " } }" +
			` :root[data-scheme="dark"] { ` + strings.Join(dark, " ") + " }"
	}
	return template.CSS(block + " " + strings.Join(rules, " "))
}

// forwardedPriorityRules is the ink a card rendered under a priority that is no
// longer live is drawn in: the ink of the priority that name now means.
//
// It exists because a card carries the priority it was *stored* under, while the
// block above names only the priorities that are *live*. Those are the same set
// right up until somebody renames or removes one. The answer to a vocabulary
// change carries this stylesheet recomposed from the live definitions, and the
// board behind the page is deliberately not rebuilt, so without this every card
// at the old name matches no rule at all and falls to the meta row's dim ink
// until a reload — the exact state this stylesheet exists to end. The static
// rules for high, medium and low are why that was never seen on an ordinary
// project: a board can only lose a color this way at a name outside that triad.
//
// A rename and a removal ask the same question here, so both chains answer it
// together, and what is asked of a retired name is what the vocabulary already
// knows: what it resolves to. A renamed-away name resolves to the name that
// replaced it and a removed one to the priority its tasks went into; either way
// the card means that priority, so it is drawn in that priority's ink. The live
// window is only the nearest instance — this equally covers a client holding a
// page rendered before a rename this checkout has since folded.
//
// A name that resolves to nothing — a chain ending outside the live set, or one
// whose destination the block above dropped as unwritable — is given no rule,
// and that is the decision rather than an oversight. There is no property to
// point it at: `var(--wb-priority-ink-gone)` names nothing, which leaves the
// declaration invalid at computed-value time and `color` inheriting regardless,
// so such a rule would buy the card nothing and put a dangling reference on
// every board that carries it. A card stranded that way is drawn in the meta
// row's ordinary ink, which is the honest reading — this board has no priority
// that name means any more.
func forwardedPriorityRules(
	priorities core.PriorityVocabulary,
	document core.PriorityDocument,
	declared map[core.Priority]struct{},
) []string {
	retired := make([]core.Priority, 0, len(document.Aliases)+len(document.Retired))
	for _, alias := range document.Aliases {
		retired = append(retired, alias.From)
	}
	for _, entry := range document.Retired {
		retired = append(retired, entry.Priority)
	}

	rules := make([]string, 0, len(retired))
	seen := make(map[core.Priority]struct{}, len(retired))
	for _, name := range retired {
		if _, repeated := seen[name]; repeated {
			continue
		}
		seen[name] = struct{}{}
		if _, isLive := declared[name]; isLive {
			// A name forwarded away and then taken again by a new priority. The
			// loop above already wrote its rule, and the live reading is the one
			// that stands.
			continue
		}
		if core.ValidatePriorityToken(name) != nil {
			continue
		}
		destination, resolves := priorities.Resolve(name)
		if !resolves {
			continue
		}
		if _, written := declared[destination]; !written {
			continue
		}
		rules = append(rules, ".priority--"+string(name)+" { color: var("+priorityInkProperty(destination)+"); }")
		// And the chip behind it, for the same reason and from the same
		// priority: a card left at the old name is drawn as that priority, so it
		// is drawn on that priority's badge rather than on a bare card.
		rules = append(rules, ".priority--"+string(name)+" { background: var("+priorityChipProperty(destination)+"); }")
	}
	return rules
}

// priorityInkProperty is the custom property one priority's ink is declared in.
//
// The family is namespaced away from `--wb-priority-high` and its two siblings
// rather than reusing them, and that is load-bearing rather than tidy: those
// three are the scheme's own triad, which no project's color reaches, and a
// project whose priorities are literally named high, medium and low would
// otherwise be declaring them.
func priorityInkProperty(priority core.Priority) string {
	return "--wb-priority-ink-" + string(priority)
}

// priorityChipProperty is the custom property one priority's chip — the
// background its label is drawn on — is declared in.
//
// It is a family of its own beside the ink rather than a shade of it, because a
// chip is not a lighter version of a color: it is whatever that color can be
// read against, which for a pale priority is dark and for a dark one is pale.
func priorityChipProperty(priority core.Priority) string {
	return "--wb-priority-chip-" + string(priority)
}

// The contrast a chip's label has to clear against the chip behind it.
//
// WCAG AA for text below 18pt, which the .64rem chip certainly is, and the bar
// this project states. It is the whole reason the chip exists: a priority's
// label used to be drawn straight onto the card's own surface, which is legible
// for a color chosen against that surface and illegible for the pale ones a
// project can now choose — the report this answers was a pale yellow at 1.4:1.
const priorityChipContrast = 4.5

// How far past the bar a chip is taken before the search stops. A step is a
// whole 8-bit color either way, so landing exactly on 4.5 leaves a chip one
// rounding away from being under it — and a value that reads 4.499 in somebody
// else's checker is a defect report whatever this file computed.
const priorityChipMargin = .1

// How much of the chip's own color a chip keeps. A badge that went all the way
// to white or black to clear the bar would be a grey pill telling the reader
// nothing about which priority it is, so the chip carries a third of the ink's
// chroma — capped by clampChroma at whatever lightness it lands on, so a
// saturated ink gets all the color that lightness can hold and a near-grey one
// gets nearly none.
const priorityChipChroma = .34

// priorityChip is the background one priority's label is drawn on: the ink's own
// hue, moved far enough along lightness that the label clears AA against it.
//
// The direction is the hue's and the scheme's rather than a constant, which is
// the half a fixed tint cannot do. A dark ink is read on a pale chip and a light
// one on a deep chip, and "dark" here means the ink as *this scheme draws it* —
// the dark scheme lifts a chosen color, so the same project's chip goes pale in
// light and deep in dark off two different inks.
//
// The step is searched rather than stated, and stops at the first lightness that
// clears the bar, which is the most color a chip can carry and still be read:
// stating an offset would be the same eyeballing that shipped the defect, and
// would fail for the colors whose luminance leaves the least room.
func priorityChip(ink themeColor) string {
	toward := 1.
	if ink.light >= .5 {
		toward = -1
	}
	if chip, found := offsetPriorityChip(ink, toward); found {
		return chip
	}
	// A mid-luminance ink has little room on the side its lightness suggests —
	// #b45309 is 4.83:1 against white and 4.35:1 against black — so the other
	// side is tried rather than assumed.
	if chip, found := offsetPriorityChip(ink, -toward); found {
		return chip
	}
	// Unreachable, and stated anyway. Every color clears the bar against white
	// below a luminance of .1833 and against black above .175, and those two
	// bands overlap, so one end of the ramp always answers; this is what the
	// search returns if a rounding step ever lands between them.
	if luminance(ink) > .18 {
		return renderColor(ink.hue, 0, 0)
	}
	return renderColor(ink.hue, 0, 1)
}

// offsetPriorityChip walks the lightness ramp away from an ink in one direction
// and answers with the first step whose contrast against it clears the bar.
func offsetPriorityChip(ink themeColor, direction float64) (string, bool) {
	const step = .004
	for offset := step; offset <= 1; offset += step {
		light := ink.light + direction*offset
		if light < 0 || light > 1 {
			break
		}
		chip := ink.tonedColor(priorityChipChroma, light)
		if contrastBetween(ink, chip) >= priorityChipContrast+priorityChipMargin {
			return chip.hex(), true
		}
	}
	return "", false
}

// How much of a derived ink a derived chip is washed with, in whole percent, in
// each of the two schemes. See derivedPriorityChip for why these are so far
// apart, and why the light one is so small.
const (
	priorityChipLightWash = 5
	priorityChipDarkWash  = 18
)

// derivedPriorityChip is the chip behind a priority that stores no color: its
// own ink, washed into the card's surface.
//
// It is composed in CSS rather than searched in Go because a derived ink is a
// reference to the scheme's triad rather than a value this file holds — that is
// what gives it a dark reading without stating one, and what keeps this family
// out of the guards that count every literal the page writes. Mixing against
// --wb-surface keeps that property: the surface is the card's own ground, so the
// wash is pale in light and deep in dark without either being stated as a color.
//
// The two percentages are far apart because the triad they wash has very
// different room in the two schemes. The amber is 4.83:1 against a white card to
// begin with, so a light wash of more than a few percent takes the board's own
// built-in three below AA — the chip is a hint there, and the badge's shape is
// what reads. Lifted onto a near-black card those inks have twice the headroom,
// and the chip can be a chip.
func derivedPriorityChip(inkProperty string, wash int) string {
	return "color-mix(in oklab, var(" + inkProperty + ") " + strconv.Itoa(wash) + "%, var(--wb-surface))"
}

// luminance is the WCAG relative luminance of a color, and contrastBetween the
// WCAG contrast ratio of two. They are here rather than in a test because the
// chip above is chosen by measuring rather than by eye: the composer has to be
// able to ask what it just derived.
func luminance(color themeColor) float64 {
	channels := [3]int{color.red, color.green, color.blue}
	weights := [3]float64{.2126, .7152, .0722}
	total := 0.
	for index, channel := range channels {
		value := float64(channel) / 255
		if value <= .04045 {
			value /= 12.92
		} else {
			value = math.Pow((value+.055)/1.055, 2.4)
		}
		total += weights[index] * value
	}
	return total
}

func contrastBetween(first, second themeColor) float64 {
	lighter, darker := luminance(first), luminance(second)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + .05) / (darker + .05)
}

// derivedPriorityInk is the color a priority with none stored is drawn in: the
// one its position among its peers derives.
//
// This is a documented promise rather than an invention. `workbook priority
// color` with no value clears a color, and both docs/reference.md and
// core.PriorityDefinition.Color say that returns the priority "to a color the
// board derives from its position" — there is no stored default to go back to.
//
// The derivation runs along the triad the stylesheet already states, because
// that triad is what a position *means* on this board: most urgent is the red,
// least urgent is the blue, and the middle is the amber between them. A
// vocabulary of three therefore lands exactly on today's three colors, which is
// what keeps the first `workbook priority` verb — which writes the built-in
// three into the ledger before anything else — from quietly recoloring a board
// nobody asked to change. A vocabulary of more lands between them.
//
// It is written as a reference to those properties rather than as a literal this
// function computed, and that buys two things at once. The scheme already states
// a dark reading for all three, so a derived ink follows the board into dark
// without this file stating anything twice; and a family that writes no color
// literal cannot collide with the counts the stylesheet's guards keep over every
// literal the page writes.
func derivedPriorityInk(index, count int) string {
	const (
		high   = "var(--wb-priority-high)"
		medium = "var(--wb-priority-medium)"
		low    = "var(--wb-priority-low)"
	)
	if count < 2 {
		// A single priority is both the most and the least urgent one, which is
		// no position at all; it takes the middle rather than an end.
		return medium
	}
	position := float64(index) / float64(count-1)
	switch {
	case position == 0:
		return high
	case position == 1:
		return low
	case position == .5:
		return medium
	case position < .5:
		// Mixed in oklab rather than sRGB: a straight channel average of the red
		// and the amber passes through a muddier, darker color than either, and
		// a perceptual space is what keeps the band between two priorities
		// reading as a step between them.
		return "color-mix(in oklab, " + high + ", " + medium + " " + mixWeight(2*position) + ")"
	default:
		return "color-mix(in oklab, " + medium + ", " + low + " " + mixWeight(2*position-1) + ")"
	}
}

// mixWeight is how much of the second color a mix takes, as a percentage.
// Rounded to whole points because the ceiling on a vocabulary is 24 priorities
// (core.MaxPriorityCount), so the smallest step between two positions is several
// points wide and a fraction of one would be precision nobody can see.
func mixWeight(fraction float64) string {
	return strconv.Itoa(int(math.Round(fraction*100))) + "%"
}

// schemeDeclarations is themeDeclarations for a scheme's variants, and refuses
// the same colour for the same reason: a value core could not validate
// contributes nothing, and the family keeps the defaults the stylesheet states.
func schemeDeclarations(value string, variants []schemeVariant) []string {
	color, parsed := parseThemeColor(value)
	if !parsed {
		return nil
	}
	declarations := make([]string, 0, len(variants))
	for _, variant := range variants {
		declarations = append(declarations, variant.property+": "+variant.derive(color)+";")
	}
	return declarations
}

func themeDeclarations(value string, tokens []themeToken) []string {
	color, parsed := parseThemeColor(value)
	if !parsed {
		return nil
	}
	declarations := make([]string, 0, len(tokens))
	for _, token := range tokens {
		declarations = append(declarations, token.property+": "+token.derive(color)+";")
	}
	return declarations
}

// themeColor is a chosen color in both the spaces a family is derived in: the
// channels a translucent ring is written from, and the hue, chroma and lightness
// every solid step moves along.
//
// A cylindrical space rather than a channel-wise darkening because the steps are
// relationships between colors rather than arithmetic on one. A hover that
// multiplies each channel by 0.85 drifts the hue of anything that is not already
// grey, and a pale tint mixed towards white loses the very hue that made it a
// tint of this project's accent rather than of blue.
//
// Chroma rather than HSL's saturation, and that is a correction rather than a
// preference. Saturation is chroma divided by the room the lightness leaves for
// it, so at the extremes the two go to zero together and their ratio does not:
// #fffffe is one unit off white and reads as *fully saturated* yellow, S = 100%,
// because a span of 1/255 is all the chroma a lightness of 99.8% can hold.
// Scaling that saturation therefore derived a screaming yellow family from a
// colour nobody could tell from white — while #ffffff, which is not a special
// case in any other way, derived greys. Chroma has no such discontinuity: it is
// the span itself, it goes to zero as the colour goes to white, and the family
// it derives approaches the grey family continuously.
//
// Every step converts back at its own lightness, where the chroma it asks for is
// capped at the room that lightness has — see clampChroma, which is also what
// keeps the arithmetic inside the byte range.
type themeColor struct {
	red, green, blue   int
	hue, chroma, light float64
}

// parseThemeColor reads a stored `#rrggbb`. Anything else is not a color this
// build wrote, and the caller keeps the legacy family rather than deriving one
// from a value it could not read.
func parseThemeColor(value string) (themeColor, bool) {
	if len(value) != 7 || value[0] != '#' {
		return themeColor{}, false
	}
	channels := make([]int, 3)
	for index := range channels {
		parsed, err := strconv.ParseUint(value[1+2*index:3+2*index], 16, 8)
		if err != nil {
			return themeColor{}, false
		}
		channels[index] = int(parsed)
	}
	color := themeColor{red: channels[0], green: channels[1], blue: channels[2]}
	red, green, blue := float64(color.red)/255, float64(color.green)/255, float64(color.blue)/255
	high := math.Max(red, math.Max(green, blue))
	low := math.Min(red, math.Min(green, blue))
	color.light = (high + low) / 2
	span := high - low
	if span == 0 {
		return color, true
	}
	color.chroma = span
	switch high {
	case red:
		color.hue = math.Mod((green-blue)/span, 6)
	case green:
		color.hue = (blue-red)/span + 2
	default:
		color.hue = (red-green)/span + 4
	}
	color.hue *= 60
	// The red sector straddles zero, so a colour on its counter-clockwise side —
	// every pink, magenta and rose, hue 300 to 360 — comes out of that first arm
	// negative. It is wrapped here rather than left for the sector arithmetic to
	// cope with, because that arithmetic reads a negative sector as the first one
	// and answers with a negative green channel.
	if color.hue < 0 {
		color.hue += 360
	}
	return color, true
}

func (color themeColor) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", color.red, color.green, color.blue)
}

// alpha is the color itself at an opacity, which is what every ring and shadow
// in this stylesheet is.
func (color themeColor) alpha(opacity string) string {
	return fmt.Sprintf("rgba(%d,%d,%d,%s)", color.red, color.green, color.blue, opacity)
}

// tonedAlpha is toned() written as a ring rather than a fill: the colour a dark
// step lifts to, at an opacity. alpha() cannot answer this — it rings in the
// colour as chosen, which on a dark ground is the one lightness that does not
// show.
func (color themeColor) tonedAlpha(chroma, light float64, opacity string) string {
	red, green, blue := renderChannels(color.hue, clampChroma(color.chroma*chroma, light), light)
	return fmt.Sprintf("rgba(%d,%d,%d,%s)", red, green, blue, opacity)
}

// scaled moves both chroma and lightness by a factor, which is how the two
// darker steps of a filled control stay in proportion to the color they darken:
// a pale accent's hover has to be pale enough to still read as the same button.
func (color themeColor) scaled(chroma, light float64) string {
	return renderColor(color.hue, color.chroma*chroma, color.light*light)
}

// toned scales the chroma but states the lightness outright, which is what a
// surface needs: a tint has to be pale to be a tint, and a project that picks a
// near-black accent must not get a chip nobody can read a label on.
//
// A tint's chroma is asked for generously and then capped by clampChroma, which
// is what makes the pale end of the family behave: a saturated accent gets all
// the colour a 96%-light surface can hold, and a nearly-grey one gets nearly
// none, out of the same number.
func (color themeColor) toned(chroma, light float64) string {
	return color.tonedColor(chroma, light).hex()
}

// tonedColor is toned() answered as a color rather than as a declaration, which
// is what a step that has to be measured needs: the chip search asks what the
// contrast of the step it just derived is, and a formatted string cannot be
// asked.
func (color themeColor) tonedColor(chroma, light float64) themeColor {
	light = math.Min(math.Max(light, 0), 1)
	chroma = clampChroma(color.chroma*chroma, light)
	red, green, blue := renderChannels(color.hue, chroma, light)
	return themeColor{red: red, green: green, blue: blue, hue: color.hue, chroma: chroma, light: light}
}

func renderChannels(hue, chroma, light float64) (int, int, int) {
	// The lightness is brought into range before anything is asked of it, which
	// is what makes this function total rather than conditionally correct. No
	// step asks for one outside it today — every scaled factor is below one and
	// every stated lightness is inside — but the whole point of the bound below
	// is that a colour outside the range is written as a declaration a browser
	// drops in silence, and `light` is the other way to leave it: a lightness of
	// 1.02 renders `#104104104` at any chroma at all, including none.
	light = math.Min(math.Max(light, 0), 1)
	chroma = clampChroma(chroma, light)
	sector := math.Mod(hue/60, 6)
	middle := chroma * (1 - math.Abs(math.Mod(sector, 2)-1))
	var red, green, blue float64
	switch {
	case sector < 1:
		red, green, blue = chroma, middle, 0
	case sector < 2:
		red, green, blue = middle, chroma, 0
	case sector < 3:
		red, green, blue = 0, chroma, middle
	case sector < 4:
		red, green, blue = 0, middle, chroma
	case sector < 5:
		red, green, blue = middle, 0, chroma
	default:
		red, green, blue = chroma, 0, middle
	}
	base := light - chroma/2
	return channelByte(red + base), channelByte(green + base), channelByte(blue + base)
}

// renderColor is renderChannels written the way a solid declaration takes it.
func renderColor(hue, chroma, light float64) string {
	red, green, blue := renderChannels(hue, chroma, light)
	return fmt.Sprintf("#%02x%02x%02x", red, green, blue)
}

// clampChroma bounds a step's chroma by the room its lightness has for one, and
// it is what keeps every derived colour a colour.
//
// A lightness of L can hold at most 1-|2L-1| chroma; past that the conversion
// puts the low channel below zero and the high one above one, and the formatter
// writes those out as `#bd-4-4` — a declaration a browser drops, silently
// unstyling whatever read that property. It is reachable from ordinary input:
// the darker steps ask for chroma at a much lower lightness than the accent's,
// so any accent at full chroma — #ff0000, #00ff00, #ffff00 — overshoots it.
//
// The lightness is the caller's to bring into range — renderColor does, before
// it asks — and outside [0, 1] the room this computes from it goes negative,
// which would bound the chroma below zero and produce the same unreadable
// declaration by a longer route.
//
// With both bounded, the channel arithmetic in renderColor cannot leave the unit
// range, which is why channelByte rounds rather than clamps.
func clampChroma(chroma, light float64) float64 {
	return math.Min(math.Max(chroma, 0), 1-math.Abs(2*light-1))
}

func channelByte(value float64) int {
	return int(math.Round(value * 255))
}
