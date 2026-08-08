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
	handler := NewHandler(func(context.Context) ([]core.Task, error) { return boardTasks(), nil },
		unexpectedTaskCreate(t), unexpectedTaskUpdate(t), unexpectedStatusUpdate(t))

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
  if (descriptionToggle.textContent !== "Descriptions: hidden") {
    throw new Error("the setting did not report the default: " + descriptionToggle.textContent);
  }
  if (descriptionToggle.getAttribute("aria-pressed") !== "false") {
    throw new Error("the setting did not report the default to assistive technology");
  }
  // The text stays on the card, so turning the setting on reveals what the
  // page already holds instead of waiting for the next poll to redraw it.
  if (!alpha.textContent.includes("Draw the first card.")) {
    throw new Error("hiding descriptions dropped the description text from the card");
  }

  descriptionToggle.eventListeners.click({});
  if (boardView.dataset.descriptions !== "shown") {
    throw new Error("the setting did not show descriptions: " + boardView.dataset.descriptions);
  }
  if (descriptionToggle.textContent !== "Descriptions: shown") {
    throw new Error("the setting did not report itself as on: " + descriptionToggle.textContent);
  }
  if (descriptionToggle.getAttribute("aria-pressed") !== "true") {
    throw new Error("the setting did not report itself as on to assistive technology");
  }
  if (storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`) !== "shown") {
    throw new Error("showing descriptions was not remembered: " + storedPreferences.get(`+strconv.Quote(descriptionPreferenceKey)+`));
  }

  // The setting is not part of what a poll redraws, so a board left open keeps it.
  await intervalCallback();
  if (boardView.dataset.descriptions !== "shown") throw new Error("a poll reset the description setting");

  descriptionToggle.eventListeners.click({});
  if (boardView.dataset.descriptions !== "hidden") {
    throw new Error("the setting did not hide descriptions again: " + boardView.dataset.descriptions);
  }
  if (descriptionToggle.getAttribute("aria-pressed") !== "false") {
    throw new Error("the setting did not report itself as off to assistive technology");
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
  if (descriptionToggle.textContent !== "Descriptions: shown") {
    throw new Error("the setting did not report what was remembered: " + descriptionToggle.textContent);
  }
  if (descriptionToggle.getAttribute("aria-pressed") !== "true") {
    throw new Error("the setting did not report what was remembered to assistive technology");
  }
`)
}

// The setting draws cards, and only the board draws cards. Offering it beside
// the deleted list or a task's own page would be a control that visibly does
// nothing, so the header carries it only where it means something.
func TestHandlerClientOffersTheDescriptionSettingOnlyWhereItDrawsCards(t *testing.T) {
	runBoardClient(t, "description setting on the board", reconcileBoardTasks(), `
  if (descriptionToggle.hidden) throw new Error("the board withheld its own description setting");
`)

	for _, route := range []struct{ name, url string }{
		{"deleted tasks", "/deleted"},
		{"a new task", "/tasks/new"},
		{"a task's own page", "/tasks/" + reconcileAlphaID},
	} {
		t.Run(route.name, func(t *testing.T) {
			runBoardClientAt(t, "description setting on "+route.name, route.url, reconcileBoardTasks(), "", `
  if (!descriptionToggle.hidden) {
    throw new Error("a route that draws no cards still offered the description setting");
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
