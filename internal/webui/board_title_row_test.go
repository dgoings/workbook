package webui

import (
	"regexp"
	"strings"
	"testing"
)

// Inside the Workbench desktop app the window has no native title bar, and the
// board's header is the top of the window beside the app's sidebar, so it is
// the window's drag handle there. A drag region swallows clicks and hovers, so
// every control in the header has to opt back out, and a browser opening the
// board must never get a drag region at all: it would do nothing useful and is
// not the page's business. These hold all three, against the served page.

// cssAppRegion matches a -webkit-app-region declaration and captures its value.
var cssAppRegion = regexp.MustCompile(`(?:^|[;{\s])-webkit-app-region\s*:\s*([\w-]+)`)

// cssEmbeddedRoot matches a selector whose first compound is the root carrying
// one of the classes the app's board preload sets, and nothing else, so the
// rule applies only inside Workbench.
var cssEmbeddedRoot = regexp.MustCompile(`^(?::root|html)?\.in-workbench(?:-win32)?\s`)

// htmlComment matches a comment in the served markup.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// htmlStartTag matches an element's start tag and captures its name and its
// attributes.
var htmlStartTag = regexp.MustCompile(`<([a-z][\w-]*)\b([^>]*)>`)

// htmlAttribute matches one attribute, quoted or bare, and captures its name
// and value.
var htmlAttribute = regexp.MustCompile(`([\w-]+)(?:\s*=\s*"([^"]*)")?`)

// interactiveTags are the elements that take a click or a keystroke of their
// own, which a drag region would swallow.
var interactiveTags = map[string]bool{"a": true, "button": true, "input": true, "select": true, "textarea": true, "label": true, "summary": true}

// headerMarkup returns the markup inside the board's <header class="app-header">,
// comments taken out.
func headerMarkup(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<header class="app-header">`)
	if start < 0 {
		t.Fatal(`the board has no <header class="app-header">`)
	}
	end := strings.Index(body[start:], "</header>")
	if end < 0 {
		t.Fatal(`the board's <header class="app-header"> is never closed`)
	}
	return htmlComment.ReplaceAllString(body[start+len(`<header class="app-header">`):start+end], "")
}

// headerControl is an element in the header that needs the pointer, and every
// selector by which a rule written as `.app-header <selector>` would reach it.
type headerControl struct {
	tag   string
	names []string
}

func headerControls(t *testing.T, markup string) []headerControl {
	t.Helper()
	var controls []headerControl
	for _, tag := range htmlStartTag.FindAllStringSubmatch(markup, -1) {
		attributes := map[string]string{}
		for _, attribute := range htmlAttribute.FindAllStringSubmatch(tag[2], -1) {
			attributes[attribute[1]] = attribute[2]
		}
		role := attributes["role"]
		_, focusable := attributes["tabindex"]
		if !interactiveTags[tag[1]] && role == "" && !focusable {
			continue
		}
		names := []string{tag[1]}
		for _, class := range strings.Fields(attributes["class"]) {
			names = append(names, "."+class)
		}
		if role != "" {
			names = append(names, `[role="`+role+`"]`)
		}
		if focusable {
			names = append(names, "[tabindex]")
		}
		controls = append(controls, headerControl{tag: tag[0], names: names})
	}
	return controls
}

func TestHandlerMakesTheHeaderTheWindowsDragHandleOnlyInsideWorkbench(t *testing.T) {
	t.Parallel()
	drag := 0
	for _, rule := range styleRules(t, boardPage(t)) {
		region := cssAppRegion.FindStringSubmatch(rule.declarations)
		if region == nil || region[1] != "drag" {
			continue
		}
		for _, selector := range strings.Split(rule.selector, ",") {
			if strings.TrimSpace(selector) != ":root.in-workbench .app-header" {
				continue
			}
			if rule.condition != "" {
				t.Errorf("the header's drag region is written inside %q, so at other sizes the window cannot be moved by it", rule.condition)
				continue
			}
			drag++
		}
	}
	if drag != 1 {
		t.Errorf("found %d rules declaring `:root.in-workbench .app-header { -webkit-app-region: drag }`, want exactly 1", drag)
	}
}

func TestHandlerOptsEveryHeaderControlOutOfTheDragRegion(t *testing.T) {
	t.Parallel()
	body := boardPage(t)
	noDrag := map[string]bool{}
	for _, rule := range styleRules(t, body) {
		region := cssAppRegion.FindStringSubmatch(rule.declarations)
		if region == nil || region[1] != "no-drag" || rule.condition != "" {
			continue
		}
		for _, selector := range strings.Split(rule.selector, ",") {
			selector = strings.Join(strings.Fields(selector), " ")
			if name, ok := strings.CutPrefix(selector, ":root.in-workbench .app-header "); ok && !strings.Contains(name, " ") {
				noDrag[name] = true
			}
		}
	}

	skip := false
	for _, rule := range styleRules(t, body) {
		region := cssAppRegion.FindStringSubmatch(rule.declarations)
		if region != nil && region[1] == "no-drag" && rule.condition == "" && strings.TrimSpace(rule.selector) == ":root.in-workbench .skip-link" {
			skip = true
		}
	}
	if !skip {
		t.Error("no `:root.in-workbench .skip-link { -webkit-app-region: no-drag }` rule, so the skip link, fixed over the header when focused, drags the window instead of following")
	}

	controls := headerControls(t, headerMarkup(t, body))
	if len(controls) < 3 {
		t.Fatalf("found %d controls in the header, want at least the Board link and the Dark Mode and Show Descriptions switches; is the extractor reading the right element?", len(controls))
	}
	for _, control := range controls {
		covered := false
		for _, name := range control.names {
			if noDrag[name] {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s in the header is reached by no `:root.in-workbench .app-header <x> { -webkit-app-region: no-drag }` rule (tried %s), so inside Workbench a click on it drags the window", control.tag, strings.Join(control.names, ", "))
		}
	}
	// What the header holds today is not everything a route can put there: the
	// Deleted column's switch is an anchor with role="switch", and a later header
	// may grow a form control. Each kind is opted out by name.
	for _, name := range []string{"a", "button", "input", "select", "textarea", "label", `[role="switch"]`} {
		if !noDrag[name] {
			t.Errorf("no `:root.in-workbench .app-header %s { -webkit-app-region: no-drag }` rule", name)
		}
	}
}

func TestHandlerDeclaresNoDragRegionABrowserWouldSee(t *testing.T) {
	t.Parallel()
	found := 0
	for _, rule := range styleRules(t, boardPage(t)) {
		if !cssAppRegion.MatchString(rule.declarations) {
			continue
		}
		found++
		for _, selector := range strings.Split(rule.selector, ",") {
			selector = strings.TrimSpace(selector)
			if !cssEmbeddedRoot.MatchString(selector) {
				t.Errorf("%s declares -webkit-app-region outside .in-workbench, so a browser opening the board gets it", selector)
			}
		}
	}
	if found == 0 {
		t.Error("the stylesheet declares no -webkit-app-region at all; the header's drag rules are missing")
	}
}

func TestHandlerKeepsTheHeadersSettingsClearOfTheWindowsControls(t *testing.T) {
	t.Parallel()
	padded := 0
	for _, rule := range styleRules(t, boardPage(t)) {
		if strings.TrimSpace(rule.selector) != ":root.in-workbench-win32 .app-header" {
			continue
		}
		match := regexp.MustCompile(`padding-right\s*:\s*([^;]*);`).FindStringSubmatch(rule.declarations)
		if match == nil {
			t.Errorf("%s declares no padding-right", rule.selector)
			continue
		}
		for _, want := range []string{"100vw", "env(titlebar-area-x", "env(titlebar-area-width", "138px"} {
			if !strings.Contains(match[1], want) {
				t.Errorf("the Windows header padding %q does not use %s", match[1], want)
			}
		}
		padded++
	}
	if padded != 1 {
		t.Errorf("found %d `:root.in-workbench-win32 .app-header` rules with a padding-right, want 1", padded)
	}
}
