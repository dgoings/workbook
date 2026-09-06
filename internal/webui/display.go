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

// The neutral surfaces, the rules drawn on them, the ink written on them, and
// the semantic three. Surfaces are numbered rather than named because they are
// one ramp of six near-identical greys, the way the primary tints already are.
//
// Two literals appear twice because they carry two roles. #fff is a surface
// where it is a background and the ink on a saturated fill where it is a
// colour: one has to move when the scheme does and the other must not, so they
// are separate properties that happen to agree today. #8496b0 is a quiet rule
// in one place and quiet ink in another, on the same terms.
var schemeTokens = []schemeToken{
	{"--wb-surface", "#fff", "#161c26"},
	{"--wb-on-accent", "#fff", "#0f141c"},
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
	{"--wb-danger-strong", "#8f1d1d", "#e08a7c"},
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

	{"--wb-success-ink", "#14663c", "#7fc79b"},
	{"--wb-success-surface", "#e8f4ec", "#12241a"},

	// The third of the priority triad. It was the one literal the stylesheet
	// deliberately kept, because it must not follow a project's accent — and it
	// still does not: a scheme property is not derived from one. What it could
	// not go on doing is staying a mid blue on a near-black card, which is the
	// one thing a colour scheme has to be able to say about it.
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
	return renderColor(color.hue, color.chroma*chroma, light)
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
