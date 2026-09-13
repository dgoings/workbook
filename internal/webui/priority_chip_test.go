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

// What a priority's label has to be readable against, measured rather than
// looked at.
//
// A priority is drawn in its own color on the card. Where that color can be read
// on the card it is left there, and where it cannot — the report this answers is
// a pale yellow at 1.4:1 on a white card — it is given a background of its own
// to be read on. Which of the two a priority gets is decided per color and per
// scheme, so a board's priorities deliberately do not all look alike: the same
// yellow is a filled pill on a white card and a bare word on a near-black one,
// because that is what each of those cards can carry.
//
// So there are two claims here rather than one, and which of them applies is
// itself the thing being tested:
//
//   - Where no chip is drawn, the label clears the threshold against the card.
//     That is the measurement that decided it, so it is asserted rather than
//     assumed.
//   - Where a chip is drawn, the label clears the stronger target the chip is
//     composed to, and the chip is far enough from the card that a reader can
//     see there is one.
//
// Both are measured off the served stylesheet: the declarations are read out of
// the block, `var()` and `color-mix()` are resolved the way a browser resolves
// them, and the colors are put through the WCAG contrast formula. Eyeballing a
// badge is how the defect shipped, so nothing here is eyeballed.

const (
	// The threshold: a label that clears this against the card is left on the
	// card, and one that does not is given a chip. It is 3:1 rather than AA, and
	// priorityInkContrast is where that is argued — this line decides whether a
	// color somebody chose needs help, not whether text is compliant.
	chipDecisionBar = 3.
	// The floor a label drawn *on a chip* clears against it: WCAG 2 AA for text
	// below 18pt, which a .64rem label certainly is. A chip is composed by this
	// project rather than chosen by a person, so it is held to the bar the
	// threshold is deliberately lenient about.
	chipContrastBar = 4.5
	// And the bar every color this project itself ships clears against its card,
	// which is the same AA number for the same reason. See
	// TestShippedPriorityColorsClearAAAgainstTheCard.
	shippedColorBar = 4.5
	// What a chip, where one is drawn at all, is composed to: WCAG AAA. See
	// priorityChipContrast for why a chip aims past the bar that called for it.
	chipTarget = 7.
	// How far a chip has to sit from the card behind it. WCAG 1.4.11 asks 3:1 of
	// a non-text boundary, and a chip is one — "so light as to not be adding
	// anything" is the verdict this number exists to fail.
	chipSeparation = 3.
	// Except where the only legible chip is on the card's own side of the ramp,
	// which is what happens to the saturated blues in the dark scheme: their chip
	// goes deeper than a #161c26 card, and there is no more room below it than
	// that. See priorityChip. A chip that close is still a well the eye can see —
	// the wash this change replaced was 1.02:1.
	chipDeepSeparation = 1.2
)

// The colors the board was reviewed on, and the branch each of them takes.
//
// The first five are the acceptance cases: the red a project set Critical to,
// the pale yellow from the report, pure red — the color that moved the
// threshold, legible to the eye and refused by AA at 3.998:1 — the board's own
// amber, and a violet-blue whose dark reading sits at 3.74:1, nearest the
// threshold of anything here. The last two are the ends of the lightness range,
// where the chip search has least room.
//
// Only the light scheme now answers both ways. The violet-blue was here to show
// the dark card losing a color, and at this threshold the dark card loses none:
// see the sweep below for why that is a fact about the lift rather than a thin
// table.
//
// The branches are stated rather than logged. The complaint that produced this
// rule was that every priority looked alike; a change that quietly went back to
// giving every one of them a background would pass a test that only measured
// contrast, and fails this one.
var chipColorCases = []struct {
	name, color string
	// light and dark are the branch each scheme's reading takes: true where the
	// priority is given a chip, false where its label is left on the card.
	light, dark bool
}{
	{"a red, set to Critical — reads on the card in both schemes", "#d92d20", false, false},
	{"the pale yellow the report was written about", "#f5e6a3", true, false},
	{"pure red, 3.998:1 on white — under AA, over the threshold, and left bare", "#ff0000", false, false},
	{"the built-in amber, which needs nothing in either scheme", "#b45309", false, false},
	{"a violet-blue, whose lifted dark reading is the closest call in this table", "#3b3bd4", false, false},
	{"a near-white", "#fbfbf7", true, false},
	{"a near-black", "#06070b", false, false},
}

// Every color a project can choose is drawn so that its label can be read: on
// the card where the card carries it, and on a chip where it does not.
func TestPriorityChipIsDrawnOnlyForAColorTheCardCannotCarry(t *testing.T) {
	for _, test := range chipColorCases {
		t.Run(test.name, func(t *testing.T) {
			block := priorityInkBlock(t, priorityInkBoardPage(t, fourPriorityVocabulary(t, test.color), nil))
			for _, scheme := range []string{"light", "dark"} {
				drawn := drawnPriority(t, block, "high", scheme)
				reading := measurePriority(t, drawn, scheme)
				t.Logf("%s in %s: %s", test.color, scheme, reading)
				want := test.light
				if scheme == "dark" {
					want = test.dark
				}
				if drawn.chipped != want {
					t.Errorf("a %s priority in %s is drawn %s, want %s — %s",
						test.color, scheme, branchName(drawn.chipped), branchName(want), reading)
				}
			}
		})
	}
}

// The same claims across the whole hue circle, at every lightness a person can
// land on. The report was about one pale yellow; what is actually being fixed is
// that nothing about the old rule depended on the color, so nothing about the
// new one may depend on the color either.
//
// Both branches have to appear — in the light scheme. The dark scheme takes one
// branch throughout, and that is a measured property of the lift rather than a
// hole in the sweep, so it is asserted as such instead of being papered over
// with a color picked to manufacture the other branch.
//
// A chosen color is lifted to a fixed lightness before the dark card draws it,
// and the darkest color that transform can produce anywhere in sRGB is #5c5cff
// — from a pure #0000a3 — at 3.61:1 on the #161c26 card. Nothing a person can
// choose lands under 3:1 there. So at this threshold the dark board draws no
// chips at all, and the light board is where the decision actually runs both
// ways: a sweep that found dark chips would mean the lift or the threshold had
// moved.
func TestPriorityChipDecidesEachHueOnItsOwn(t *testing.T) {
	for _, scheme := range []string{"light", "dark"} {
		card := schemePalette(scheme)["--wb-surface"]
		bare, chips := 0, 0
		worstBare, worstLabel, worstSeparation := math.Inf(1), math.Inf(1), math.Inf(1)
		var barest, faintest, closest string
		for hue := 0; hue < 360; hue += 15 {
			for _, light := range []float64{.04, .2, .4, .5, .6, .8, .96} {
				for _, chroma := range []float64{.2, 1} {
					chosen := renderColor(float64(hue), chroma, light)
					block := string(priorityInk(fourPriorityVocabulary(t, chosen)))
					drawn := drawnPriority(t, block, "high", scheme)
					measurePriority(t, drawn, scheme)
					if !drawn.chipped {
						bare++
						if ratio := contrastRatio(t, drawn.ink, card); ratio < worstBare {
							worstBare, barest = ratio, chosen
						}
						continue
					}
					chips++
					if ratio := contrastRatio(t, drawn.ink, drawn.chip); ratio < worstLabel {
						worstLabel, faintest = ratio, chosen
					}
					if ratio := contrastRatio(t, drawn.chip, card); ratio < worstSeparation {
						worstSeparation, closest = ratio, chosen
					}
				}
			}
		}
		if bare == 0 {
			t.Errorf("the %s sweep drew every one of its %d labels on a chip — the board of identical pills this rule exists to end",
				scheme, chips)
		}
		switch scheme {
		case "light":
			if chips == 0 {
				t.Errorf("the light sweep drew all %d labels bare — at %.0f:1 a pale color on a white card still has to earn a chip",
					bare, chipDecisionBar)
			}
		default:
			if chips != 0 {
				t.Errorf("the dark sweep drew %d labels on a chip; the lift leaves the worst color anyone can choose at 3.61:1 "+
					"on the dark card, so at %.0f:1 none of them should need one — the lift or the threshold has moved",
					chips, chipDecisionBar)
			}
		}
		if chips == 0 {
			t.Logf("%s: %d bare (worst %.2f:1 on the card, %s), no chips", scheme, bare, worstBare, barest)
			continue
		}
		t.Logf("%s: %d bare (worst %.2f:1 on the card, %s), %d chips (worst label %.2f:1, %s; closest to the card %.2f:1, %s)",
			scheme, bare, worstBare, barest, chips, worstLabel, faintest, worstSeparation, closest)
	}
}

// And a priority that stores no color at all, whose ink is the one its position
// among its peers derives, is never given a chip.
//
// That is a measurement rather than a policy. The derived family is the triad
// this board has always drawn priorities in, or a mix of two of them, and every
// one of those was chosen to be read on the card it sits on — the worst is the
// amber at 5.02:1 on white. So the rule answers "no chip" for all of them, which
// is also what keeps this family out of the guards that count every literal the
// page writes: there is nothing to compose in Go, because there is nothing to
// compose.
//
// How far above the threshold each of them sits is asserted next door, by
// TestShippedPriorityColorsClearAAAgainstTheCard, and not twice here: clearing
// the threshold is what "no chip" means, and clearing AA is a separate and
// stricter promise this project makes about the colors it ships.
func TestPriorityChipLeavesADerivedInkOnTheCard(t *testing.T) {
	worst := math.Inf(1)
	var worstCase string
	for count := 1; count <= core.MaxPriorityCount; count++ {
		block := string(priorityInk(uncoloredPriorities(t, count)))
		for index := range count {
			token := fmt.Sprintf("p%d", index)
			for _, scheme := range []string{"light", "dark"} {
				card := schemePalette(scheme)["--wb-surface"]
				drawn := drawnPriority(t, block, token, scheme)
				if drawn.chipped {
					t.Errorf("priority %d of %d in %s is given the chip %s, but a derived ink reads on the card unaided",
						index+1, count, scheme, drawn.chip)
					continue
				}
				ratio := contrastRatio(t, drawn.ink, card)
				if ratio < worst {
					worst, worstCase = ratio, fmt.Sprintf("%d of %d in %s: %s on %s", index+1, count, scheme, drawn.ink, card)
				}
			}
		}
	}
	t.Logf("worst of the derived family: %.2f:1 (%s)", worst, worstCase)
}

// Every color this project ships clears WCAG AA against the card it is drawn on,
// in both schemes.
//
// This is deliberately stricter than priorityInkContrast, the 3:1 threshold a
// few hundred lines from here that decides whether a label gets a chip, and the
// two are not in tension: leniency there is about a color a person chose for
// their own board, and strictness here is about the colors this project hands
// somebody who chose nothing. We are standards-compliant in what we ship and
// lenient about what a person selects. A default that only cleared the threshold
// would be this project using, on someone else's behalf, a latitude that exists
// for their choices and not for ours.
//
// Nothing needs recoloring as this is written — the tightest reading below is
// over 5:1. The test is here because with the threshold at 3 nothing else would
// notice a shipped color drifting to 3.2, and a default nobody chose is exactly
// the color that has to be right without anybody looking.
//
// What "ships" covers is both families, because a project gets either without
// choosing anything:
//
//   - The built-in three. A project that has configured no priorities is drawn
//     in high, medium and low, which is the zero vocabulary below.
//   - The derived ramp, which is what a project with four or ten priorities gets
//     at every position: the triad, or an oklab mix of two of it. Every position
//     at every vocabulary size is measured, the way the hue sweep measures every
//     hue, because a mix between two colors that each clear AA need not.
func TestShippedPriorityColorsClearAAAgainstTheCard(t *testing.T) {
	tightest, tightestCase := math.Inf(1), ""
	measure := func(t *testing.T, what, block, token, scheme string) {
		t.Helper()
		card := schemePalette(scheme)["--wb-surface"]
		drawn := drawnPriority(t, block, token, scheme)
		ratio := contrastRatio(t, drawn.ink, card)
		if ratio < tightest {
			tightest, tightestCase = ratio, fmt.Sprintf("%s in %s: %s on %s", what, scheme, drawn.ink, card)
		}
		if ratio < shippedColorBar {
			t.Errorf("%s draws %s on the %s card %s at %.2f:1, under the %.1f:1 this project holds its own colors to — "+
				"the %.0f:1 threshold beside it is latitude for a color somebody chose, not for one we ship",
				what, drawn.ink, scheme, card, ratio, shippedColorBar, chipDecisionBar)
		}
	}

	t.Run("the built-in three", func(t *testing.T) {
		block := string(priorityInk(core.PriorityVocabulary{}))
		for _, priority := range []core.Priority{core.PriorityHigh, core.PriorityMedium, core.PriorityLow} {
			for _, scheme := range []string{"light", "dark"} {
				measure(t, string(priority), block, string(priority), scheme)
			}
		}
	})

	t.Run("the derived ramp", func(t *testing.T) {
		for count := 1; count <= core.MaxPriorityCount; count++ {
			block := string(priorityInk(uncoloredPriorities(t, count)))
			for index := range count {
				for _, scheme := range []string{"light", "dark"} {
					measure(t, fmt.Sprintf("position %d of %d", index+1, count), block, fmt.Sprintf("p%d", index), scheme)
				}
			}
		}
	})

	t.Logf("tightest color this project ships: %.2f:1 (%s)", tightest, tightestCase)
}

// measurePriority holds one drawn priority to whichever claim its branch makes,
// and answers with the reading it took, so a test can log what decided it.
func measurePriority(t *testing.T, drawn drawnChip, scheme string) string {
	t.Helper()
	card := schemePalette(scheme)["--wb-surface"]
	if !drawn.chipped {
		ratio := contrastRatio(t, drawn.ink, card)
		if ratio < chipDecisionBar {
			t.Errorf("%s is drawn bare on the %s card %s at %.2f:1, under the %.1f:1 bar that decides it",
				drawn.ink, scheme, card, ratio, chipDecisionBar)
		}
		return fmt.Sprintf("bare, %s on the card %s = %.2f:1", drawn.ink, card, ratio)
	}

	label := contrastRatio(t, drawn.ink, drawn.chip)
	if label < chipContrastBar {
		t.Errorf("the chip %s draws %s at %.2f:1 in %s, under the %.1f:1 bar",
			drawn.chip, drawn.ink, label, scheme, chipContrastBar)
	}
	// A chip stops at the target, or at the end of the ramp where no color at all
	// is that far from the ink — pure black or pure white, which is the most that
	// color has to give. Anything in between is a chip that stopped short of a
	// target it could have reached.
	if label < chipTarget && drawn.chip != "#000000" && drawn.chip != "#ffffff" {
		t.Errorf("the chip %s draws %s at %.2f:1 in %s, under the %.1f:1 target, without having run out of ramp",
			drawn.chip, drawn.ink, label, scheme, chipTarget)
	}

	separation := contrastRatio(t, drawn.chip, card)
	floor := chipSeparation
	if (relativeLuminance(t, drawn.chip) > relativeLuminance(t, card)) !=
		(relativeLuminance(t, drawn.ink) > relativeLuminance(t, card)) {
		// The chip is on the far side of the card from the ink rather than beyond
		// the ink, which is the one case with no room to be further away.
		floor = chipDeepSeparation
	}
	if separation < floor {
		t.Errorf("the chip %s sits on the %s card %s at %.2f:1, under the %.1f:1 a reader needs to see it is there",
			drawn.chip, scheme, card, separation, floor)
	}
	return fmt.Sprintf("chip %s, label %s = %.2f:1, card %s = %.2f:1",
		drawn.chip, drawn.ink, label, card, separation)
}

func branchName(chipped bool) string {
	if chipped {
		return "on a chip"
	}
	return "bare on the card"
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

// drawnChip is one priority as a browser would paint it in one scheme: the
// label's color, and the background behind it where there is one.
type drawnChip struct {
	// ink is the label's color as `#rrggbb`.
	ink string
	// chipped is whether this priority is drawn on a chip in this scheme at all.
	// A priority whose label reads on the card is given no background, and a
	// priority given one in the other scheme declares this one `transparent`;
	// both are the card, and both answer false.
	chipped bool
	// chip is the background's color as `#rrggbb`, meaningful only where chipped.
	chip string
}

// drawnPriority is what a browser would paint for one priority.
//
// It reads the colors out of the served stylesheet rather than out of the
// composer, so what is measured is what is served — the declarations for the
// scheme asked for, resolved through the scheme's own palette exactly as `var()`
// and `color-mix()` resolve.
func drawnPriority(t *testing.T, block, token, scheme string) drawnChip {
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
	drawn := drawnChip{ink: resolveColor(t, ruleValue(t, block, ".priority--"+token, "color"), palette)}
	background, given := optionalRuleValue(block, ".priority--"+token, "background")
	if !given || strings.TrimSpace(background) == "transparent" {
		return drawn
	}
	property := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(background), "var("), ")")
	if declared, found := palette[strings.TrimSpace(property)]; found && strings.TrimSpace(declared) == "transparent" {
		return drawn
	}
	drawn.chipped = true
	drawn.chip = resolveColor(t, background, palette)
	return drawn
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
	value, found := optionalRuleValue(block, selector, property)
	if !found {
		t.Fatalf("the composed stylesheet sets no %s for %s: %s", property, selector, block)
	}
	return value
}

// optionalRuleValue is ruleValue for a property a rule may not set at all, which
// is what a background now is: most priorities are given none.
func optionalRuleValue(block, selector, property string) (string, bool) {
	pattern := regexp.MustCompile(regexp.QuoteMeta(selector) + ` \{ ` + regexp.QuoteMeta(property) + `: ([^;]+); \}`)
	found := pattern.FindStringSubmatch(block)
	if found == nil {
		return "", false
	}
	return found[1], true
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
