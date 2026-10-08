package webui

import (
	"regexp"
	"strings"
	"testing"
)

// Inside the Workbench desktop app the board sits beside the app's sidebar, and
// the line between them has to stop at the bottom of the board's header so the
// header reads as one band with the sidebar's head. Only the board knows how
// tall its header is, so the board draws that line below it, on every element
// after the header, under a class the app's board preload puts on the root. A
// browser never sets the class, and these hold both halves: the line is drawn
// under it, in the token the header's own bottom border uses, and nothing
// outside it draws a line at the left of anything below the header.

// cssLeftBorder matches a declaration that would draw at an element's left
// edge: border-left in any of its longhands, the logical inline-start, or the
// shorthand that draws all four sides.
var cssLeftBorder = regexp.MustCompile(`(?:^|[;{\s])border(?:-left|-inline-start|-inline)?(?:-width|-style|-color)?\s*:`)

// cssBelowHeader matches the last compound of a selector that can land on an
// element of body's column below the header: the filter row, either notice,
// main, or a universal selector, which is what a sibling combinator after the
// header is written with.
var cssBelowHeader = regexp.MustCompile(`^(?:main|\.filter-row|\.notice|\*)(?:[\[:.].*)?$`)

// cssInWorkbench matches a selector whose first compound is the root carrying
// the class and nothing else, so the rule applies only where the class is set.
// A mention of the class anywhere else — `:not(.in-workbench) main`, or the
// class on some descendant — would still apply in a browser, and does not count.
var cssInWorkbench = regexp.MustCompile(`^(?::root|html)?\.in-workbench\s`)

func TestHandlerDrawsTheSidebarDividerUnderTheHeaderOnlyInsideWorkbench(t *testing.T) {
	t.Parallel()
	rules := styleRules(t, boardPage(t))

	drawn := 0
	for _, rule := range rules {
		for _, selector := range strings.Split(rule.selector, ",") {
			selector = strings.TrimSpace(selector)
			if !cssInWorkbench.MatchString(selector) {
				continue
			}
			if !strings.Contains(selector, ".app-header ~") {
				continue
			}
			if !strings.Contains(rule.declarations, "border-left: 1px solid var(--wb-border);") {
				t.Errorf("%s draws %q, not the header's own hairline token", selector, strings.TrimSpace(rule.declarations))
				continue
			}
			if rule.condition != "" {
				t.Errorf("%s is written inside %q, so the line is drawn only at the sizes that query names", selector, rule.condition)
				continue
			}
			drawn++
		}
	}
	if drawn == 0 {
		t.Error("no rule scoped to .in-workbench draws `border-left: 1px solid var(--wb-border)` on what follows .app-header, so inside Workbench nothing separates the board from the sidebar below the header")
	}
}

func TestHandlerDrawsNoLeftLineBelowTheHeaderInABrowser(t *testing.T) {
	t.Parallel()
	for _, rule := range styleRules(t, boardPage(t)) {
		if !cssLeftBorder.MatchString(rule.declarations) {
			continue
		}
		for _, selector := range strings.Split(rule.selector, ",") {
			selector = strings.TrimSpace(selector)
			if cssInWorkbench.MatchString(selector) {
				continue
			}
			fields := strings.Fields(selector)
			last := fields[len(fields)-1]
			if cssBelowHeader.MatchString(last) || strings.Contains(selector, ".app-header ~") || strings.Contains(selector, ".app-header +") {
				t.Errorf("%s declares %q outside .in-workbench, so a browser opening the board draws a line at its left edge", selector, strings.TrimSpace(rule.declarations))
			}
		}
	}
}
