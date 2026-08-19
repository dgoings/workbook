package webui

import (
	"fmt"
	"math"
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

// printThemeFamily is the tuning aid the table above was read off. It is a test
// so it stays compilable, and it states nothing.
func TestBoardThemeFamilyIsPrintable(t *testing.T) {
	color, _ := parseThemeColor("#2457d6")
	for _, token := range primaryThemeTokens {
		t.Log(fmt.Sprintf("%-26s legacy %-22s derived %s", token.property, token.legacy, token.derive(color)))
	}
}
