package webui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a priority's chip has to be readable against, measured rather than
// looked at.
//
// A priority is drawn as a badge: its label in the priority's own color, on a
// background this package derives from that color. The background is the whole
// of the fix — the label used to sit on the card's own white surface, which
// works for the three colors that were chosen against white and fails for every
// pale color a project can now pick. A chip whose label is #f5e6a3 on white is
// 1.4:1, which is not a chip anybody can read.
//
// So the claim these tests make is the accessibility bar this project states,
// WCAG AA at 4.5:1, held for any color a person can choose and in both schemes.
// It is measured off the served stylesheet: the declarations are read out of the
// block, `var()` and `color-mix()` are resolved the way a browser resolves them,
// and the two colors are put through the WCAG contrast formula. Eyeballing a
// badge is how the defect shipped, so nothing here is eyeballed.

// The bar: WCAG 2 AA for text below 18pt, which a .64rem chip certainly is.
const chipContrastBar = 4.5

// A priority whose color a project chose, and the ones worth choosing: the pale
// yellow from the report, the two ends of the lightness range, a saturated
// mid-tone, and one of the built-in three so a regression there is visible.
var chipColorCases = []struct{ name, color string }{
	{"the pale yellow the report was written about", "#f5e6a3"},
	{"a near-white", "#fbfbf7"},
	{"a near-black", "#06070b"},
	{"a saturated mid-tone", "#2457d6"},
	{"one of the built-in three", "#b42318"},
}

// Every color a project can choose leaves its chip's label readable on the chip,
// in the scheme the reader is in and in the other one.
func TestPriorityChipMeetsTheContrastBarForAChosenColor(t *testing.T) {
	for _, test := range chipColorCases {
		t.Run(test.name, func(t *testing.T) {
			block := priorityInkBlock(t, priorityInkBoardPage(t, fourPriorityVocabulary(t, test.color), nil))
			for _, scheme := range []string{"light", "dark"} {
				ink, chip := drawnPriority(t, block, "high", scheme)
				ratio := contrastRatio(t, ink, chip)
				t.Logf("%s in %s: label %s on chip %s = %.2f:1", test.color, scheme, ink, chip, ratio)
				if ratio < chipContrastBar {
					t.Errorf("a %s chip in %s draws %s on %s, %.2f:1, under the %.1f:1 bar",
						test.color, scheme, ink, chip, ratio, chipContrastBar)
				}
			}
		})
	}
}

// The same claim across the whole hue circle, at every lightness a person can
// land on. The report was about one pale yellow; what is actually being fixed is
// that nothing about the old rule depended on the color, so nothing about the
// new one may depend on the color either.
func TestPriorityChipMeetsTheContrastBarAroundTheHueCircle(t *testing.T) {
	worst := math.Inf(1)
	var worstCase string
	for hue := 0; hue < 360; hue += 15 {
		for _, light := range []float64{.04, .2, .4, .5, .6, .8, .96} {
			for _, chroma := range []float64{.2, 1} {
				chosen := renderColor(float64(hue), chroma, light)
				block := string(priorityInk(fourPriorityVocabulary(t, chosen)))
				for _, scheme := range []string{"light", "dark"} {
					ink, chip := drawnPriority(t, block, "high", scheme)
					ratio := contrastRatio(t, ink, chip)
					if ratio < worst {
						worst, worstCase = ratio, fmt.Sprintf("%s in %s: %s on %s", chosen, scheme, ink, chip)
					}
					if ratio < chipContrastBar {
						t.Errorf("a %s chip in %s draws %s on %s, %.2f:1, under the %.1f:1 bar",
							chosen, scheme, ink, chip, ratio, chipContrastBar)
					}
				}
			}
		}
	}
	t.Logf("worst of the sweep: %.2f:1 (%s)", worst, worstCase)
}

// And for a priority that stores no color at all, whose ink is the one its
// position among its peers derives. Those are written as references to the
// scheme's own triad rather than as literals, so the chip behind them is written
// as a mix of the same reference — and that mix has to clear the bar for every
// position a vocabulary of any size can produce.
func TestPriorityChipMeetsTheContrastBarForADerivedColor(t *testing.T) {
	worst := math.Inf(1)
	var worstCase string
	for count := 1; count <= core.MaxPriorityCount; count++ {
		block := string(priorityInk(uncoloredPriorities(t, count)))
		for index := range count {
			token := fmt.Sprintf("p%d", index)
			for _, scheme := range []string{"light", "dark"} {
				ink, chip := drawnPriority(t, block, token, scheme)
				ratio := contrastRatio(t, ink, chip)
				if ratio < worst {
					worst, worstCase = ratio, fmt.Sprintf("%d of %d in %s: %s on %s", index+1, count, scheme, ink, chip)
				}
				if ratio < chipContrastBar {
					t.Errorf("priority %d of %d in %s draws %s on %s, %.2f:1, under the %.1f:1 bar",
						index+1, count, scheme, ink, chip, ratio, chipContrastBar)
				}
			}
		}
	}
	t.Logf("worst of the derived family: %.2f:1 (%s)", worst, worstCase)
}

// A chip is a background the card's own surface does not provide, so it has to
// be a color of its own rather than the surface repeated: a "badge" the same
// color as the card is the state this fix exists to leave.
func TestPriorityChipIsOffsetFromTheSurfaceItSitsOn(t *testing.T) {
	for _, test := range chipColorCases {
		t.Run(test.name, func(t *testing.T) {
			block := string(priorityInk(fourPriorityVocabulary(t, test.color)))
			for _, scheme := range []string{"light", "dark"} {
				_, chip := drawnPriority(t, block, "high", scheme)
				surface := schemePalette(scheme)["--wb-surface"]
				if ratio := contrastRatio(t, chip, surface); ratio < 1.03 {
					t.Errorf("a %s chip in %s is %s on a %s card, %.3f:1 — the badge is invisible",
						test.color, scheme, chip, surface, ratio)
				}
			}
		})
	}
}

// uncoloredPriorities is a vocabulary of `count` priorities, none of which
// stores a color, so every one of them is drawn in the color its position
// derives.
func uncoloredPriorities(t *testing.T, count int) core.PriorityVocabulary {
	t.Helper()
	definitions := make([]core.PriorityDefinition, 0, count)
	for index := range count {
		definitions = append(definitions, core.PriorityDefinition{
			Priority: core.Priority(fmt.Sprintf("p%d", index)),
			Label:    fmt.Sprintf("P%d", index),
			Rank:     fmt.Sprintf("%d/1", index+1),
			Tags:     []core.PriorityTag{},
		})
	}
	definitions[0].Tags = []core.PriorityTag{core.PriorityTagDefault}
	vocabulary, err := core.NewPriorityVocabulary(definitions, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return vocabulary
}

// drawnPriority is the pair of colors a browser would paint one priority's chip
// in: the label's color and the background behind it, as `#rrggbb`.
//
// It reads them out of the served stylesheet rather than out of the composer, so
// what is measured is what is served — the declarations for the scheme asked
// for, resolved through the scheme's own palette exactly as `var()` and
// `color-mix()` resolve.
func drawnPriority(t *testing.T, block, token, scheme string) (string, string) {
	t.Helper()
	palette := schemePalette(scheme)
	for property, value := range blockDeclarations(t, block, ":root") {
		palette[property] = value
	}
	if scheme == "dark" {
		for property, value := range blockDeclarations(t, block, `:root[data-scheme="dark"]`) {
			palette[property] = value
		}
	}
	ink := ruleValue(t, block, ".priority--"+token, "color")
	chip := ruleValue(t, block, ".priority--"+token, "background")
	return resolveColor(t, ink, palette), resolveColor(t, chip, palette)
}

// schemePalette is the scheme's own colors — the ones no project chooses — in
// the reading this scheme gives them, read from the table the stylesheet is
// generated from so the measurement cannot drift from the board.
func schemePalette(scheme string) map[string]string {
	palette := map[string]string{}
	for _, token := range schemeTokens {
		if scheme == "dark" {
			palette[token.property] = token.dark
			continue
		}
		palette[token.property] = token.legacy
	}
	return palette
}

// blockDeclarations reads the properties one `:root` rule in a composed block
// sets. The dark selector appears twice — once inside the media query and once
// for a reader who chose it — and either statement answers, because
// TestHandlerStylesheetStatesBothDarkSchemesIdentically is what holds them
// together.
func blockDeclarations(t *testing.T, block, selector string) map[string]string {
	t.Helper()
	declarations := map[string]string{}
	opening := selector + " {"
	start := strings.Index(block, opening)
	if start < 0 {
		return declarations
	}
	rest := block[start+len(opening):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("the %s block in the composed stylesheet is never closed: %s", selector, block)
	}
	for _, declaration := range strings.Split(rest[:end], ";") {
		property, value, found := strings.Cut(declaration, ":")
		if !found {
			continue
		}
		declarations[strings.TrimSpace(property)] = strings.TrimSpace(value)
	}
	return declarations
}

// ruleValue is what one rule in a composed block sets one property to.
func ruleValue(t *testing.T, block, selector, property string) string {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(selector) + ` \{ ` + regexp.QuoteMeta(property) + `: ([^;]+); \}`)
	found := pattern.FindStringSubmatch(block)
	if found == nil {
		t.Fatalf("the composed stylesheet sets no %s for %s: %s", property, selector, block)
	}
	return found[1]
}

// resolveColor computes what a browser paints for a value the composer wrote:
// a hex literal, a `var()` reference into the palette, or a `color-mix()` of
// either in oklab.
func resolveColor(t *testing.T, value string, palette map[string]string) string {
	t.Helper()
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "#"):
		return expandHex(t, value)
	case strings.HasPrefix(value, "var("):
		property := strings.TrimSuffix(strings.TrimPrefix(value, "var("), ")")
		declared, found := palette[strings.TrimSpace(property)]
		if !found {
			t.Fatalf("the stylesheet reads %s, which nothing declares", property)
		}
		return resolveColor(t, declared, palette)
	case strings.HasPrefix(value, "color-mix("):
		return resolveMix(t, value, palette)
	}
	t.Fatalf("the measurement cannot resolve %q", value)
	return ""
}

// resolveMix computes `color-mix(in oklab, A [p%], B [q%])` the way CSS Color 5
// defines it: both colors converted to oklab, interpolated by the normalized
// weights, converted back and clipped into sRGB.
func resolveMix(t *testing.T, value string, palette map[string]string) string {
	t.Helper()
	inner := strings.TrimSuffix(strings.TrimPrefix(value, "color-mix("), ")")
	parts := splitArguments(inner)
	if len(parts) != 3 || strings.TrimSpace(parts[0]) != "in oklab" {
		t.Fatalf("the measurement only knows oklab mixes, not %q", value)
	}
	first, firstWeight := mixTerm(t, parts[1], palette)
	second, secondWeight := mixTerm(t, parts[2], palette)
	switch {
	case firstWeight < 0 && secondWeight < 0:
		firstWeight, secondWeight = .5, .5
	case firstWeight < 0:
		firstWeight = 1 - secondWeight
	case secondWeight < 0:
		secondWeight = 1 - firstWeight
	}
	total := firstWeight + secondWeight
	if total == 0 {
		t.Fatalf("a mix of nothing: %q", value)
	}
	weight := secondWeight / total

	from, to := oklabOf(t, first), oklabOf(t, second)
	mixed := [3]float64{}
	for channel := range mixed {
		mixed[channel] = from[channel] + (to[channel]-from[channel])*weight
	}
	return hexFromOklab(mixed)
}

// mixTerm reads one side of a mix: the color, and the percentage it was given,
// or -1 where it was given none.
func mixTerm(t *testing.T, term string, palette map[string]string) (string, float64) {
	t.Helper()
	term = strings.TrimSpace(term)
	weight := -1.0
	if at := strings.LastIndex(term, " "); at > 0 && strings.HasSuffix(term, "%") {
		percentage, err := strconv.ParseFloat(strings.TrimSuffix(term[at+1:], "%"), 64)
		if err != nil {
			t.Fatalf("unreadable mix percentage in %q: %v", term, err)
		}
		weight = percentage / 100
		term = strings.TrimSpace(term[:at])
	}
	return resolveColor(t, term, palette), weight
}

// splitArguments splits a function's arguments on the commas that are its own,
// leaving the ones inside a nested function alone.
func splitArguments(inner string) []string {
	parts := []string{}
	depth, start := 0, 0
	for index, character := range inner {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(inner[start:index]))
				start = index + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(inner[start:]))
}

func expandHex(t *testing.T, value string) string {
	t.Helper()
	if len(value) == 4 {
		return fmt.Sprintf("#%c%c%c%c%c%c", value[1], value[1], value[2], value[2], value[3], value[3])
	}
	if len(value) != 7 {
		t.Fatalf("the measurement cannot read the color %q", value)
	}
	return strings.ToLower(value)
}

// channels reads `#rrggbb` as three 0..1 values.
func channels(t *testing.T, hex string) [3]float64 {
	t.Helper()
	hex = expandHex(t, hex)
	values := [3]float64{}
	for index := range values {
		parsed, err := strconv.ParseUint(hex[1+2*index:3+2*index], 16, 8)
		if err != nil {
			t.Fatalf("the measurement cannot read the color %q: %v", hex, err)
		}
		values[index] = float64(parsed) / 255
	}
	return values
}

// contrastRatio is WCAG 2's, computed from the two colors as painted.
func contrastRatio(t *testing.T, first, second string) float64 {
	t.Helper()
	lighter, darker := relativeLuminance(t, first), relativeLuminance(t, second)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + .05) / (darker + .05)
}

func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	linear := linearChannels(channels(t, hex))
	return .2126*linear[0] + .7152*linear[1] + .0722*linear[2]
}

func linearChannels(values [3]float64) [3]float64 {
	linear := [3]float64{}
	for index, value := range values {
		if value <= .04045 {
			linear[index] = value / 12.92
			continue
		}
		linear[index] = math.Pow((value+.055)/1.055, 2.4)
	}
	return linear
}

// oklabOf converts `#rrggbb` to oklab, which is the space CSS mixes in.
func oklabOf(t *testing.T, hex string) [3]float64 {
	t.Helper()
	linear := linearChannels(channels(t, hex))
	red, green, blue := linear[0], linear[1], linear[2]
	long := math.Cbrt(.4122214708*red + .5363325363*green + .0514459929*blue)
	medium := math.Cbrt(.2119034982*red + .6806995451*green + .1073969566*blue)
	short := math.Cbrt(.0883024619*red + .2817188376*green + .6299787005*blue)
	return [3]float64{
		.2104542553*long + .7936177850*medium - .0040720468*short,
		1.9779984951*long - 2.4285922050*medium + .4505937099*short,
		.0259040371*long + .7827717662*medium - .8086757660*short,
	}
}

// hexFromOklab is the inverse, clipped into sRGB the way a browser clips a mix
// that lands outside the gamut.
func hexFromOklab(color [3]float64) string {
	long := color[0] + .3963377774*color[1] + .2158037573*color[2]
	medium := color[0] - .1055613458*color[1] - .0638541728*color[2]
	short := color[0] - .0894841775*color[1] - 1.2914855480*color[2]
	long, medium, short = long*long*long, medium*medium*medium, short*short*short
	linear := [3]float64{
		4.0767416621*long - 3.3077115913*medium + .2309699292*short,
		-1.2684380046*long + 2.6097574011*medium - .3413193965*short,
		-.0041960863*long - .7034186147*medium + 1.7076147010*short,
	}
	rendered := ""
	for _, value := range linear {
		encoded := value
		if encoded <= .0031308 {
			encoded *= 12.92
		} else {
			encoded = 1.055*math.Pow(encoded, 1/2.4) - .055
		}
		rendered += fmt.Sprintf("%02x", int(math.Round(math.Min(math.Max(encoded, 0), 1)*255)))
	}
	return "#" + rendered
}
