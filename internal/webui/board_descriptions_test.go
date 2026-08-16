package webui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A board card is an identifier, a title, and its labels. The description is the
// one field with no length a card can rely on, so showing six clamped lines of
// it pushes the next card off the screen. The board hides it and offers the
// previous behavior as a remembered setting.

// descriptionPreferenceKey is the storage key the board remembers the setting
// under. It is part of the page's contract with the browser: renaming it makes
// every board forget what its reader chose.
const descriptionPreferenceKey = "workbook.board.descriptions"

func TestHandlerHidesCardDescriptionsUntilTheBoardIsAskedForThem(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	// The description is rendered and then hidden by the stylesheet rather than
	// left out of the markup. The setting then reveals text the page already
	// holds, and the first paint of a hidden board never shows a description it
	// is about to take away.
	if !strings.Contains(body, "Build the board surface.") {
		t.Fatal("GET / did not render the card description at all")
	}
	if !strings.Contains(body, "data-description-toggle") {
		t.Fatal("GET / did not render the description setting")
	}
	if rule := cssRule(t, body, ".task-card p"); !strings.Contains(rule, "display: none") {
		t.Fatalf("card descriptions are not hidden by default: %s", rule)
	}
	shown := cssRule(t, body, `.board-view[data-descriptions="shown"] .task-card p`)
	if !strings.Contains(shown, "display: -webkit-box") {
		t.Fatalf("a board asked for descriptions does not show them: %s", shown)
	}
}

// The header reveals this setting from renderRoute(), exactly as it reveals the
// publishing control beside it, so the served markup ships it hidden. Every
// route is served the same shell, and a browser that never reaches renderRoute()
// — scripting switched off, a policy that refuses an inline script, an exception
// thrown earlier in it — would otherwise be left with an enabled
// "Descriptions: hidden" button on a task's own page, which draws no cards for
// it to act on and which clicking does nothing to. Shipping it hidden makes
// that degraded page one with no control rather than one with a control that
// lies.
func TestHandlerShipsTheDescriptionSettingHiddenUntilItsRouteRevealsIt(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	for _, path := range []string{"/", "/tasks/new"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		tag := openingTag(t, response.Body.String(), "data-description-toggle")
		if !strings.Contains(tag, " hidden") {
			t.Errorf("GET %s served the description setting unhidden: %s", path, tag)
		}
	}
}

// The button's name carries the state, so aria-pressed must not carry it too.
// Both flip together, and they flip in opposite directions: after turning the
// setting off a screen reader reaches "Descriptions: hidden, toggle button, not
// pressed", which reads as "hidden is not on" — the opposite of what just
// happened. A toggle keeps a fixed name when aria-pressed speaks for it; this
// one keeps the speaking name, the same choice the publishing control beside it
// makes.
func TestHandlerLeavesTheDescriptionSettingStateToItsNameAlone(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return boardTasks(), nil })

	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if tag := openingTag(t, body, "data-description-toggle"); strings.Contains(tag, "aria-pressed") {
		t.Errorf("the description setting states itself twice in the markup: %s", tag)
	}
	if strings.Contains(body, `descriptionToggle.setAttribute("aria-pressed"`) {
		t.Error("the client script still states the description setting twice")
	}
}

// openingTag returns the opening tag of the element carrying marker, so a test
// can state what the served markup gives that element rather than that the page
// mentions a name somewhere in several kilobytes of script.
func openingTag(t *testing.T, body, marker string) string {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("the page has no element marked %s", marker)
	}
	start := strings.LastIndex(body[:at], "<")
	end := strings.Index(body[at:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("the element marked %s is not inside a tag", marker)
	}
	tag := body[start : at+end+1]
	// The marker names an attribute, so the first place it appears has to be the
	// element itself and not the script that later looks the element up. Which
	// element it is is the caller's question — these markers sit on a button and
	// on an anchor — so what is checked here is that a tag is what was found.
	if !strings.HasPrefix(tag, "<button") && !strings.HasPrefix(tag, "<a ") {
		t.Fatalf("the first %s is not an element: %s", marker, tag)
	}
	return tag
}

// cssRule returns the declarations of the page rule written for exactly this
// selector, so a test can state what the stylesheet does rather than that it
// mentions a name somewhere.
func cssRule(t *testing.T, body, selector string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		declarations, found := strings.CutPrefix(strings.TrimSpace(line), selector+" {")
		if !found {
			continue
		}
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(declarations), "}"))
	}
	t.Fatalf("the page has no rule for %s", selector)
	return ""
}

func TestHandlerClientTogglesCardDescriptions(t *testing.T) {
	runBoardClient(t, "card description setting", reconcileBoardTasks(), `
  const ready = listFor("ready");
  const alpha = cardIn(ready, `+strconv.Quote(reconcileAlphaID)+`);
  if (!alpha) throw new Error("board did not render the Ready cards");
  if (boardView.dataset.descriptions !== "hidden") {
    throw new Error("the board did not open with descriptions hidden: " + boardView.dataset.descriptions);
  }
  // The words offer the next action and aria-checked reports what is true, so
  // the two halves of the announcement never contradict each other.
  if (descriptionLabel.textContent !== "Show Descriptions") {
    throw new Error("the setting did not offer the default's next step: " + descriptionLabel.textContent);
  }
  if (descriptionToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("the setting did not report itself off: " + descriptionToggle.getAttribute("aria-checked"));
  }
  // A switch states itself with aria-checked. An aria-pressed beside it would
  // state the same thing a second time, in the vocabulary of a different role.
  if (descriptionToggle.hasAttribute("aria-pressed")) {
    throw new Error("the setting states itself twice: " + descriptionToggle.getAttribute("aria-pressed"));
  }
  // The text stays on the card, so turning the setting on reveals what the
  // page already holds instead of waiting for the next poll to redraw it.
  if (!alpha.textContent.includes("Draw the first card.")) {
    throw new Error("hiding descriptions dropped the description text from the card");
  }

  // Raised the way a browser raises it from the keyboard, so the switch is
  // shown to answer Space and Enter and not only a mouse.
  descriptionToggle.click();
  if (boardView.dataset.descriptions !== "shown") {
    throw new Error("the setting did not show descriptions: " + boardView.dataset.descriptions);
  }
  if (descriptionLabel.textContent !== "Hide Descriptions") {
    throw new Error("the setting did not offer to undo itself: " + descriptionLabel.textContent);
  }
  if (descriptionToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("the setting did not report itself on: " + descriptionToggle.getAttribute("aria-checked"));
  }
  if (descriptionToggle.hasAttribute("aria-pressed")) {
    throw new Error("the setting states itself twice: " + descriptionToggle.getAttribute("aria-pressed"));
  }
  if (storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`) !== "shown") {
    throw new Error("showing descriptions was not remembered: " + storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`));
  }

  // The setting is not part of what a poll redraws, so a board left open keeps it.
  await intervalCallback();
  if (boardView.dataset.descriptions !== "shown") throw new Error("a poll reset the description setting");

  descriptionToggle.click();
  if (boardView.dataset.descriptions !== "hidden") {
    throw new Error("the setting did not hide descriptions again: " + boardView.dataset.descriptions);
  }
  if (descriptionLabel.textContent !== "Show Descriptions") {
    throw new Error("the setting did not offer to undo itself again: " + descriptionLabel.textContent);
  }
  if (descriptionToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("the setting did not report itself off again: " + descriptionToggle.getAttribute("aria-checked"));
  }
  if (descriptionToggle.hasAttribute("aria-pressed")) {
    throw new Error("the setting states itself twice: " + descriptionToggle.getAttribute("aria-pressed"));
  }
  if (storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`) !== "hidden") {
    throw new Error("hiding descriptions again was not remembered: " + storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`));
  }
`)
}

func TestHandlerClientRestoresTheRememberedDescriptionSetting(t *testing.T) {
	runBoardClientWithSetup(t, "remembered card description setting", reconcileBoardTasks(), `
storedPreferences.set(`+strconv.Quote(descriptionPreferenceKey)+`, "shown");
`, `
  if (boardView.dataset.descriptions !== "shown") {
    throw new Error("a remembered setting did not survive the reload: " + boardView.dataset.descriptions);
  }
  if (descriptionLabel.textContent !== "Hide Descriptions") {
    throw new Error("the setting did not report what was remembered: " + descriptionLabel.textContent);
  }
  if (descriptionToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("a remembered setting did not restore the knob: " + descriptionToggle.getAttribute("aria-checked"));
  }
  if (descriptionToggle.hasAttribute("aria-pressed")) {
    throw new Error("the setting states itself twice: " + descriptionToggle.getAttribute("aria-pressed"));
  }
`)
}

// The setting draws cards, and only the board draws cards. Offering it beside a
// task's own page would be a control that visibly does nothing, so the header
// carries it only where it means something. The Deleted column's toggle travels
// with the board for the same reason, so it is asked the same question here.
func TestHandlerClientOffersTheDescriptionSettingOnlyWhereItDrawsCards(t *testing.T) {
	runBoardClient(t, "description setting on the board", reconcileBoardTasks(), `
  if (descriptionToggle.hidden) throw new Error("the board withheld its own description setting");
  if (deletedToggle.hidden) throw new Error("the board withheld the Deleted column's toggle");
`)

	for _, route := range []struct{ name, url string }{
		{"a removed address", "/deleted"},
		{"a new task", "/tasks/new"},
		{"a task's own page", "/tasks/" + reconcileAlphaID},
	} {
		t.Run(route.name, func(t *testing.T) {
			runBoardClientAt(t, "description setting on "+route.name, route.url, reconcileBoardTasks(), "", `
  if (!descriptionToggle.hidden) {
    throw new Error("a route that draws no cards still offered the description setting");
  }
  if (!deletedToggle.hidden) {
    throw new Error("a route that draws no columns still offered the Deleted column's toggle");
  }
`)
		})
	}
}

// A browser can refuse storage entirely — Safari's private windows throw on the
// first read — and a board that cannot remember the setting must still draw.
func TestHandlerClientKeepsTheBoardWhenStorageIsUnavailable(t *testing.T) {
	runBoardClientWithSetup(t, "board without storage", reconcileBoardTasks(), `
Object.defineProperty(window, "localStorage", { get() { throw new Error("storage is unavailable"); } });
`, `
  const ready = listFor("ready");
  if (cardsIn(ready).length !== 3) throw new Error("unavailable storage cost the board its cards");
  if (boardView.dataset.descriptions !== "hidden") {
    throw new Error("unavailable storage lost the default setting: " + boardView.dataset.descriptions);
  }
  descriptionToggle.eventListeners.click({});
  if (boardView.dataset.descriptions !== "shown") {
    throw new Error("unavailable storage stopped the setting from applying: " + boardView.dataset.descriptions);
  }
`)
}
