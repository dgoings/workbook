package webui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What a color set on the configuration page does to the board behind it.
//
// The board draws every color it has from two server-rendered stylesheets, and
// until now a mutation answered with the new configuration and not with the
// stylesheet — so the page kept the ink it was opened with until it was
// reloaded. What is pinned here is the other half of the exchange: the answer
// carries the same stylesheet the same composer would have rendered into the
// page, and the client swaps it in place.
//
// The composition stays on the server, and that is the whole safety argument.
// A stored color is validated at the CLI boundary and re-rendered from three
// parsed integers before it reaches a stylesheet, so the stored string never
// reaches the page; a client that was handed a color and composed CSS from it
// would be an injection route with none of that behind it. So these tests
// assert the answer carries composed CSS, and that the client composes none.

// Every priority mutation carries the ink the board would be served, not just
// the one that changed a color: adding or removing a priority restyles every
// priority that stores no color of its own, because the color a position
// derives depends on how many positions there are.
func TestHandlerPriorityMutationsAnswerWithTheInkTheBoardDraws(t *testing.T) {
	result := priorityMutationResult(t)
	want := string(priorityInk(result.State.Priorities))
	if want == "" {
		t.Fatal("the fixture composes no ink at all, so this test could not fail")
	}
	for _, test := range priorityRoutes("head-current") {
		t.Run(test.name, func(t *testing.T) {
			recorded := &recordedPriorityMutations{}
			handler := priorityMutationHandler(t, recorded, result, nil)
			response := requestJSON(t, handler, test.method, test.target, test.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s = %d, want %d; body = %s",
					test.method, test.target, response.Code, http.StatusOK, response.Body.String())
			}
			var document VocabularyPriorityMutationDocument
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
			}
			if document.Vocabulary.Priorities.Ink != want {
				t.Fatalf("the answer carries ink %q, want the stylesheet the page would have been served, %q",
					document.Vocabulary.Priorities.Ink, want)
			}
		})
	}
}

// And the read route carries it too, because a client that adopts a document
// adopts one document: the answer a mutation makes and the answer a fresh read
// makes are the same shape, so the page has one path that swaps the ink.
func TestHandlerVocabularyReadAnswersWithTheInkTheBoardDraws(t *testing.T) {
	priorities := configuredPriorities(t)
	handler := prioritiesAdministrableHandler(handlerVocabulary(t), priorities, "head-7", nil)
	response := request(t, handler, http.MethodGet, "/api/vocabulary")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/vocabulary = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	var document VocabularyDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode vocabulary document: %v; body = %s", err, response.Body.String())
	}
	if want := string(priorityInk(priorities)); document.Priorities.Ink != want {
		t.Fatalf("the read carries ink %q, want %q", document.Priorities.Ink, want)
	}
}

// The ink an answer carries is composed, never a stored value passed through.
// A color is re-rendered out of the three integers it parsed to, which is what
// keeps a stylesheet answerable for every byte in it — the property this whole
// approach rests on, asserted on the wire rather than on the composer.
func TestHandlerPriorityInkOnAnAnswerIsComposedRatherThanCarried(t *testing.T) {
	result := priorityMutationResult(t)
	recorded := &recordedPriorityMutations{}
	handler := priorityMutationHandler(t, recorded, result, nil)
	response := requestJSON(t, handler, http.MethodPatch,
		"/api/vocabulary/priorities/urgent/color", `{"color":"#b42318","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("recolor = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var document VocabularyPriorityMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	ink := document.Vocabulary.Priorities.Ink
	// The declaration and the rule are both there, which is what makes it a
	// stylesheet rather than a color the client would have to do something with.
	for _, want := range []string{
		"--wb-priority-ink-urgent: #b42318;",
		".priority--urgent { color: var(--wb-priority-ink-urgent); }",
		// A priority with no color of its own is carried as the reference its
		// position derives, so the client is never handed arithmetic to repeat.
		"--wb-priority-ink-low: var(--wb-priority-low);",
	} {
		if !strings.Contains(ink, want) {
			t.Errorf("the answer's ink does not carry %q: %s", want, ink)
		}
	}
}

// A save of the board settings carries the theme the same way.
func TestHandlerDisplayMutationAnswersWithTheThemeTheBoardDraws(t *testing.T) {
	recorded := &recordedDisplayChange{}
	result := displayMutationResult()
	handler := displayMutationHandler(t, recorded, result, nil)

	response := requestJSON(t, handler, http.MethodPatch, "/api/display",
		`{"name":"Beta","primaryColor":"#7f1a4b","textColor":"#3b2a1a","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/display = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	var document DisplayMutationDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	want := string(boardTheme(result.State.Display))
	if want == "" {
		t.Fatal("the fixture composes no theme at all, so this test could not fail")
	}
	if document.Display.Theme != want {
		t.Fatalf("the answer carries theme %q, want the stylesheet the page would have been served, %q",
			document.Display.Theme, want)
	}
	if !strings.Contains(document.Display.Theme, "--wb-primary: #7f1a4b;") {
		t.Errorf("the answer's theme does not carry the chosen accent: %s", document.Display.Theme)
	}
}

// A project that cleared its colors is answered with an empty theme rather than
// with no member at all, because clearing is a change the board has to draw: a
// client handed nothing would leave the accent it was opened with in place.
func TestHandlerDisplayMutationCarriesAnEmptyThemeForClearedColors(t *testing.T) {
	recorded := &recordedDisplayChange{}
	handler := displayMutationHandler(t, recorded, DisplayMutation{
		State: VocabularyState{Head: "head-written", Display: core.DisplaySettings{Name: "Beta"}},
	}, nil)

	response := requestJSON(t, handler, http.MethodPatch, "/api/display",
		`{"name":"Beta","primaryColor":"","textColor":"","expectedHead":"head-current"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH /api/display = %d, want %d; body = %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode mutation document: %v; body = %s", err, response.Body.String())
	}
	display, ok := raw["display"].(map[string]any)
	if !ok {
		t.Fatalf("the answer carries no display member: %s", response.Body.String())
	}
	theme, present := display["theme"]
	if !present {
		t.Fatalf("a cleared theme is omitted rather than answered, so the board keeps its old accent: %s",
			response.Body.String())
	}
	if theme != "" {
		t.Fatalf("theme = %#v, want the empty stylesheet a project with no colors is served", theme)
	}
}

// Both stylesheets are served as elements whatever the project configured, so
// the client always has one to write into. A project that has chosen no colors
// is still served nothing — the element is empty, which is the same board it
// always had — but the element is there, because the alternative is a client
// that creates a <style> and decides where in the cascade it belongs.
func TestHandlerServesBothStylesheetElementsForAProjectThatChoseNothing(t *testing.T) {
	body := displayBoardPage(t, core.DisplaySettings{}, "atlas-web")

	for _, marker := range []string{"<style data-board-theme>", "<style data-board-priority-ink>"} {
		if !strings.Contains(body, marker) {
			t.Errorf("the page carries no %s for the client to write into", marker)
		}
	}
	if theme := themeBlock(t, body); theme != "" {
		t.Errorf("a project that chose nothing is served the theme %q", theme)
	}
}

// The client composes no CSS of its own.
//
// This is the line the whole approach is drawn against. The stylesheets are
// safe because every byte of them is a property name the server wrote or a
// number it formatted, out of three integers parsed from a value core had
// already validated. A script that named one of those properties would be
// composing a stylesheet with none of that behind it, and the value it composed
// from would have had to travel as text.
func TestClientComposesNoBoardStylesheetOfItsOwn(t *testing.T) {
	script := renderedClientScript(t, displayBoardPage(t, core.DisplaySettings{PrimaryColor: "#1a7f4b"}, "atlas-web"))

	for _, composed := range []string{"--wb-priority-ink-", "--wb-primary", "--wb-text", ".priority--"} {
		if strings.Contains(script, composed) {
			t.Errorf("the client script writes %q, so it is composing a stylesheet rather than swapping one", composed)
		}
	}
}

// widenedPriorities is what an addition answers with: a fourth priority below
// the three configuredPriorities names. Every priority that stores no color of
// its own moves, because the color a position derives depends on how many
// positions there are — which is why an addition has to carry the ink too.
func widenedPriorities(t *testing.T) core.PriorityVocabulary {
	t.Helper()
	priorities, err := core.NewPriorityVocabulary([]core.PriorityDefinition{
		{Priority: "urgent", Label: "Drop everything", Rank: "1/1", Tags: []core.PriorityTag{}, Color: "#b42318"},
		{Priority: "soon", Label: "Soon", Rank: "2/1", Tags: []core.PriorityTag{core.PriorityTagDefault}},
		{Priority: core.PriorityLow, Label: "Low", Rank: "3/1", Tags: []core.PriorityTag{}},
		{Priority: "someday", Label: "Someday", Rank: "4/1", Tags: []core.PriorityTag{}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewPriorityVocabulary() error = %v", err)
	}
	return priorities
}

// A recolor redraws the board's ink where it stands. Nothing is reloaded, and
// the stylesheet the page now carries is the one the server composed.
func TestClientRedrawsThePriorityInkWhenAColorChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	recolored := recoloredPriorities(t, "#7c3aed")
	runPriorityPanelClient(t, "redrawing the ink on a recolor", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, recolored, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  const before = boardPriorityInkStyle.textContent;
  await openPriorityForm("urgent", "Edit Drop everything");
  const form = priorityForm("urgent", "priorityEdit");
  const color = findElement(form, (element) => element.id === "priority-color-urgent");
  color.value = "#7c3aed";
  await submitPriorityForm(form);

  const want = `+quotedJSON(string(priorityInk(recolored)))+`;
  if (boardPriorityInkStyle.textContent === before) {
    throw new Error("the recolor left the board drawing the ink it was opened with");
  }
  if (boardPriorityInkStyle.textContent !== want) {
    throw new Error("the ink is now " + JSON.stringify(boardPriorityInkStyle.textContent) + ", want " + JSON.stringify(want));
  }
  if (reloadCalls !== 0) throw new Error("the page reloaded rather than redrawing in place");
`)
}

// Adding a priority redraws it too, because the color every uncolored priority
// is drawn in depends on how many of them there are. This is the case a client
// that patched only the priority it was told about would get wrong.
func TestClientRedrawsThePriorityInkWhenAPriorityIsAdded(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	priorities := configuredPriorities(t)
	widened := widenedPriorities(t)
	runPriorityPanelClient(t, "redrawing the ink on an addition", vocabulary, priorities, "head-7", nil, `
  vocabularyRead = `+priorityVocabularyJSON(t, vocabulary, priorities, "head-7")+`;
  priorityAnswers.push({ body: `+priorityMutationJSON(t, vocabulary, widened, "head-8", VocabularyPriorityTaskCounts{}, nil)+` });
  await openStatuses();

  const before = boardPriorityInkStyle.textContent;
  const form = priorityAdd();
  const name = findElement(form, (element) => element.id === "priority-new-name");
  if (!name) throw new Error("the add form offers no name field");
  name.value = "someday";
  name.eventListeners.input();
  await submitPriorityForm(form);

  const want = `+quotedJSON(string(priorityInk(widened)))+`;
  if (boardPriorityInkStyle.textContent === before) {
    throw new Error("adding a priority left every other priority drawn in the color it was opened with");
  }
  if (boardPriorityInkStyle.textContent !== want) {
    throw new Error("the ink is now " + JSON.stringify(boardPriorityInkStyle.textContent) + ", want " + JSON.stringify(want));
  }
  if (reloadCalls !== 0) throw new Error("the page reloaded rather than redrawing in place");
`)
}

// And a save of the project's accent redraws the theme, so the configuration
// page behaves one way rather than two.
func TestClientRedrawsTheThemeWhenTheAccentChanges(t *testing.T) {
	vocabulary := handlerVocabulary(t)
	saved := core.DisplaySettings{Name: "Atlas", PrimaryColor: "#7f1a4b", TextColor: "#3b2a1a"}
	runConfigClient(t, "redrawing the theme on a save", vocabulary, "head-1", `
  vocabularyRead = `+configuredVocabularyJSON(t, vocabulary, "head-9")+`;
  await openStatuses();

  const before = boardThemeStyle.textContent;
  displayField("primaryColor").value = "#7f1a4b";
  displayField("textColor").value = "#3b2a1a";
  displayAnswer = { body: `+displayMutationJSON(t, VocabularyState{Vocabulary: vocabulary, Head: "head-10", Display: saved})+` };
  await saveDisplay();

  const want = `+quotedJSON(string(boardTheme(saved)))+`;
  if (boardThemeStyle.textContent === before) {
    throw new Error("the save left the board drawing the accent it was opened with");
  }
  if (boardThemeStyle.textContent !== want) {
    throw new Error("the theme is now " + JSON.stringify(boardThemeStyle.textContent) + ", want " + JSON.stringify(want));
  }
  if (reloadCalls !== 0) throw new Error("the page reloaded rather than redrawing in place");
`)
}
