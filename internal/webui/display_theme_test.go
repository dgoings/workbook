package webui

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a chosen color makes of the board.
//
// The generator is never asked about the legacy blue — a project that has
// configured nothing keeps the literals the stylesheet declares — so it does not
// have to reproduce that family byte for byte. What it does have to do is land
// on it: the ratios and lightnesses in the table are read off the palette a
// designer drew, and a generator that answered a visibly different family for
// the very color the palette was drawn from would be answering a different
// question for every other color too.

// channelDistance is the largest per-channel difference between two colors,
// which is how far apart two swatches actually are to look at. Alpha values are
// compared as text, since a ring's opacity is carried through unchanged.
func channelDistance(t *testing.T, got, want string) int {
	t.Helper()
	if strings.HasPrefix(want, "rgba(") {
		if got != want {
			t.Fatalf("derived %s, want %s", got, want)
		}
		return 0
	}
	if len(got) != 7 || len(want) != 7 {
		t.Fatalf("derived %q against %q, which is not a pair of hex colors", got, want)
	}
	worst := 0
	for index := 0; index < 3; index++ {
		left, err := strconv.ParseUint(got[1+2*index:3+2*index], 16, 8)
		if err != nil {
			t.Fatalf("derived %q, which is not a hex color", got)
		}
		right, err := strconv.ParseUint(want[1+2*index:3+2*index], 16, 8)
		if err != nil {
			t.Fatalf("legacy %q is not a hex color", want)
		}
		worst = max(worst, int(math.Abs(float64(left)-float64(right))))
	}
	return worst
}

// The whole family, derived from the color the legacy palette was drawn in,
// lands on that palette. Six units per channel is under a fortieth of the range
// and below what a reader can pick out of two swatches side by side.
func TestBoardThemeDerivesTheLegacyFamilyFromItsOwnPrimary(t *testing.T) {
	const tolerance = 6
	color, parsed := parseThemeColor("#2457d6")
	if !parsed {
		t.Fatal("the legacy primary does not parse as a color")
	}
	for _, token := range primaryThemeTokens {
		got := token.derive(color)
		if distance := channelDistance(t, got, token.legacy); distance > tolerance {
			t.Errorf("%s derives %s from the legacy primary, want within %d of %s (off by %d)",
				token.property, got, tolerance, token.legacy, distance)
		}
	}
	ink, parsed := parseThemeColor("#172033")
	if !parsed {
		t.Fatal("the legacy text color does not parse as a color")
	}
	for _, token := range textThemeTokens {
		got := token.derive(ink)
		if distance := channelDistance(t, got, token.legacy); distance > tolerance {
			t.Errorf("%s derives %s from the legacy text color, want within %d of %s (off by %d)",
				token.property, got, tolerance, token.legacy, distance)
		}
	}
}

// A project that has configured nothing gets no override at all, which is what
// keeps its board byte-identical to the one it was served before any of this
// existed.
func TestBoardThemeIsEmptyForAnUnconfiguredProject(t *testing.T) {
	for name, settings := range map[string]core.DisplaySettings{
		"nothing configured": {},
		"a name alone":       {Name: "Atlas"},
	} {
		if theme := boardTheme(settings); theme != "" {
			t.Errorf("%s produced a theme: %s", name, theme)
		}
	}
}

// Each family is overridden on its own. A project that chose an ink and no
// accent keeps every blue the stylesheet declares.
func TestBoardThemeOverridesOnlyTheFamiliesAProjectChose(t *testing.T) {
	primary := string(boardTheme(core.DisplaySettings{PrimaryColor: "#1a7f4b"}))
	if !strings.Contains(primary, "--wb-primary: #1a7f4b;") {
		t.Errorf("a chosen accent is not the accent: %s", primary)
	}
	if strings.Contains(primary, "--wb-text") {
		t.Errorf("a chosen accent moved the ink: %s", primary)
	}
	ink := string(boardTheme(core.DisplaySettings{TextColor: "#3b2a1a"}))
	if !strings.Contains(ink, "--wb-text: #3b2a1a;") || !strings.Contains(ink, "--wb-text-shadow: rgba(59,42,26,.12);") {
		t.Errorf("a chosen ink does not carry its shadows: %s", ink)
	}
	if strings.Contains(ink, "--wb-primary") {
		t.Errorf("a chosen ink moved the accent: %s", ink)
	}
}

// The block is one `:root` rule naming every property of the families it
// overrides, so a property this build derives but the stylesheet no longer reads
// — or the other way round — is visible rather than silently inert.
func TestBoardThemeStatesEveryPropertyOfAChosenFamily(t *testing.T) {
	theme := string(boardTheme(core.DisplaySettings{PrimaryColor: "#1a7f4b", TextColor: "#3b2a1a"}))
	if !strings.HasPrefix(theme, ":root { ") || !strings.HasSuffix(theme, " }") {
		t.Fatalf("the theme is not one :root rule: %s", theme)
	}
	for _, token := range append(append([]themeToken{}, primaryThemeTokens...), textThemeTokens...) {
		if !strings.Contains(theme, token.property+": ") {
			t.Errorf("the theme does not state %s: %s", token.property, theme)
		}
	}
}

// A color no author could have stored contributes nothing rather than half a
// family. Nothing can reach this from the outside — every value is canonicalized
// and re-checked before it is recorded — which is exactly why the fallback is
// the legacy palette rather than a panic.
func TestBoardThemeIgnoresAColorItCannotRead(t *testing.T) {
	for _, value := range []string{"red", "#abc", "#12345g", "", "#1a7f4b "} {
		if theme := boardTheme(core.DisplaySettings{PrimaryColor: value}); theme != "" {
			t.Errorf("%q produced a theme: %s", value, theme)
		}
	}
}

// derivedFamily is every solid step a colour derives, keyed by property. The
// translucent rings are left out: they are the colour itself at an opacity, and
// the claims below are about the conversion.
func derivedFamily(t *testing.T, value string) map[string]string {
	t.Helper()
	color, parsed := parseThemeColor(value)
	if !parsed {
		t.Fatalf("%q does not parse as a color", value)
	}
	family := make(map[string]string, len(primaryThemeTokens))
	for _, token := range primaryThemeTokens {
		derived := token.derive(color)
		if strings.HasPrefix(derived, "rgba(") {
			continue
		}
		family[token.property] = derived
	}
	return family
}

// hexColor is what a derived colour has to be, and the reason this test exists
// as a shape rather than as a value: a channel that leaves the byte range is
// formatted as a negative number, and `#bd-5-5` is a declaration a browser drops
// silently — unstyling whatever read that property while every other assertion
// about the theme goes on passing.
var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Every derived colour is a colour, for accents at the corners of the space.
//
// These are ordinary brand colours and they are exactly the ones that overshoot:
// the edge step asks for less chroma than the accent has but at a much lower
// lightness, and a lightness has only so much room for chroma. Nothing but the
// bound stops the conversion putting one channel below zero.
func TestBoardThemeDerivesAWellFormedColorForEveryAccent(t *testing.T) {
	for _, accent := range []string{
		"#ff0000", "#00ff00", "#0000ff", "#ffff00", "#00ffff", "#ff00ff",
		"#ffffff", "#000000", "#fffffe", "#010100", "#808080",
		"#2457d6", "#1a7f4b", "#d6246f",
	} {
		for property, derived := range derivedFamily(t, accent) {
			if !hexColor.MatchString(derived) {
				t.Errorf("%s derives %s from %s, which is not a color a browser can read",
					property, derived, accent)
			}
		}
	}
}

// A rose is on the counter-clockwise side of the red sector, so its hue comes
// out of the conversion negative and is wrapped. Nothing else in this file
// reaches that wrap: every other accent here is at a hue the first arm reports
// positive.
//
// The family is pinned outright rather than described, because what an unwrapped
// hue produces is not an error — it is a different, plausible-looking family
// with the wrong colour in it.
func TestBoardThemeDerivesTheFamilyOfARoseAccent(t *testing.T) {
	family := derivedFamily(t, "#d6246f")
	for property, want := range map[string]string{
		"--wb-primary":        "#d6246f",
		"--wb-primary-hover":  "#b71d5e",
		"--wb-primary-edge":   "#9e1750",
		"--wb-primary-tint-1": "#ffedf5",
		"--wb-primary-tint-2": "#ffeef5",
		"--wb-primary-tint-3": "#fff3f8",
		"--wb-primary-tint-4": "#fff8fb",
		"--wb-primary-chip":   "#fad3e3",
		"--wb-primary-muted":  "#eab7cc",
		"--wb-primary-ink":    "#9b5271",
		"--wb-hairline":       "#ead5de",
	} {
		if family[property] != want {
			t.Errorf("%s derives %s from a rose accent, want %s", property, family[property], want)
		}
	}
}

// The family a colour derives approaches the grey family as the colour
// approaches grey, and this is the property HSL saturation does not have.
//
// One unit off white is hsl(60, 100%, 99.8%) — fully saturated yellow, because a
// span of 1/255 is all the chroma that lightness can hold — so scaling that
// saturation derived a screaming yellow family from a colour nobody could tell
// from white, while white itself derived greys. Chroma is the span, so it goes
// to zero with the colour and the two families meet.
func TestBoardThemeApproachesTheGreyFamilyContinuously(t *testing.T) {
	for _, pair := range []struct{ near, plain string }{
		{near: "#fffffe", plain: "#ffffff"},
		{near: "#010100", plain: "#000000"},
	} {
		nearly, exactly := derivedFamily(t, pair.near), derivedFamily(t, pair.plain)
		for property, derived := range nearly {
			if distance := channelDistance(t, derived, exactly[property]); distance > 2 {
				t.Errorf("%s derives %s from %s and %s from %s: one unit of colour moved it %d",
					property, derived, pair.near, exactly[property], pair.plain, distance)
			}
			// And each of those is a grey in its own right, which is the claim a
			// distance from another near-grey cannot make on its own.
			if spread := channelSpread(t, derived); spread > 4 {
				t.Errorf("%s derives %s from %s, whose channels are %d apart",
					property, derived, pair.near, spread)
			}
		}
	}
	// The other half of the claim, without which the one above is satisfied by a
	// generator that answers grey to everything: a colour that is a colour still
	// derives one.
	for property, derived := range derivedFamily(t, "#1a7f4b") {
		if property == "--wb-primary-tint-4" {
			// The palest surface is barely a colour by construction.
			continue
		}
		if channelSpread(t, derived) < 5 {
			t.Errorf("%s derives %s from a saturated accent, which is a grey", property, derived)
		}
	}
}

// channelSpread is how far a colour's furthest-apart channels are, which is how
// grey it is: zero is a grey exactly.
func channelSpread(t *testing.T, value string) int {
	t.Helper()
	if len(value) != 7 {
		t.Fatalf("%q is not a hex color", value)
	}
	high, low := 0, 255
	for index := 0; index < 3; index++ {
		channel, err := strconv.ParseUint(value[1+2*index:3+2*index], 16, 8)
		if err != nil {
			t.Fatalf("%q is not a hex color", value)
		}
		high, low = max(high, int(channel)), min(low, int(channel))
	}
	return high - low
}

// A grey has no hue to preserve, and every step of the family is still a grey
// rather than an accident of the sector arithmetic.
func TestBoardThemeDerivesAGreyWithoutInventingAHue(t *testing.T) {
	theme := string(boardTheme(core.DisplaySettings{PrimaryColor: "#808080"}))
	color, _ := parseThemeColor("#808080")
	for _, token := range primaryThemeTokens {
		derived := token.derive(color)
		if strings.HasPrefix(derived, "rgba(") {
			continue
		}
		red, green, blue := derived[1:3], derived[3:5], derived[5:7]
		if red != green || green != blue {
			t.Errorf("%s derives %s from a grey, which is not a grey", token.property, derived)
		}
	}
	if !strings.Contains(theme, "--wb-primary: #808080;") {
		t.Errorf("a grey accent is not the accent: %s", theme)
	}
}
