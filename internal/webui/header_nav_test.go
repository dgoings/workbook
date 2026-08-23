package webui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// What the header promises: the routes are in one place and the settings in
// another, and the two never move each other.
//
// They were one row once. The row was right-aligned and its contents changed
// with the route — the Deleted setting belongs to the board, so leaving the
// board took it out of the row and slid every link to its left along to fill the
// gap. A reader who had just aimed the cursor at Board arrived to find Config
// underneath it. Nothing here is about pixels, because a Go test has no layout:
// what is pinned is the structural fact that produces the layout, which is that
// nothing route-dependent is drawn in the same box as the links.
//
// The settings themselves are switches now rather than sentences. A switch says
// two things at once, and the whole design of this header turns on their not
// contradicting each other: the words say what the next click does, and the knob
// and aria-checked say what is true this second.

// headerElement returns the served page's header, which is where every claim in
// this file is made.
func headerElement(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "<header")
	end := strings.Index(body, "</header>")
	if start < 0 || end < 0 || end < start {
		t.Fatal("the served page has no header")
	}
	return body[start : end+len("</header>")]
}

// switchElement returns the whole switch carrying marker — its opening tag, its
// label, its track and its knob — because half of what this file claims is about
// what a switch is made of rather than what its outermost tag says.
func switchElement(t *testing.T, body, marker string) string {
	t.Helper()
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatalf("the header carries no element marked %s", marker)
	}
	start := strings.LastIndexByte(body[:at], '<')
	if start < 0 {
		t.Fatalf("the element marked %s is not inside a tag", marker)
	}
	closing := "</button>"
	if strings.HasPrefix(body[start:], "<a ") {
		closing = "</a>"
	}
	end := strings.Index(body[start:], closing)
	if end < 0 {
		t.Fatalf("the element marked %s is never closed", marker)
	}
	return body[start : start+end+len(closing)]
}

// The links come first, and nothing that a route can take away is drawn before
// them or beside them. This is the whole of the fix: a control that disappears
// can only close a gap that is after it.
func TestHandlerHeaderDrawsTheRouteLinksBeforeEverySettingThatComesAndGoes(t *testing.T) {
	header := headerElement(t, administrableBoardPage(t, core.DefaultVocabulary()))

	navStart := strings.Index(header, "<nav")
	navEnd := strings.Index(header, "</nav>")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("the header draws no navigation element for the routes")
	}
	nav := header[navStart : navEnd+len("</nav>")]
	for _, route := range []string{`href="/"`, `href="/config"`} {
		if !strings.Contains(nav, route) {
			t.Errorf("the header's navigation does not carry %s: %s", route, nav)
		}
	}
	// A hidden element inside this box is a gap waiting to open: the ones that
	// ship hidden are exactly the ones a route reveals and hides again.
	if strings.Contains(nav, "hidden") {
		t.Errorf("the header's navigation holds something a route can take away: %s", nav)
	}
	for _, setting := range []string{"data-deleted-toggle", "data-description-toggle", "data-sync-toggle"} {
		at := strings.Index(header, setting)
		if at < 0 {
			t.Fatalf("the header carries no %s", setting)
		}
		if at < navEnd {
			t.Errorf("%s is drawn before the routes are finished, so hiding it moves them", setting)
		}
	}
	// The reading of the board's freshness is the one thing here whose width
	// changes on its own, so it is not in the settings' row either.
	updated := strings.Index(header, "data-updated")
	switches := strings.Index(header, "header-switches")
	if updated < 0 || switches < 0 {
		t.Fatal("the header no longer carries both the freshness reading and the settings row")
	}
	if updated > switches {
		t.Error("the freshness reading is drawn inside or after the settings row, where its ticking clock moves them")
	}
}

// Every route is served the same shell, and that is what makes the links land in
// the same place on all of them. Stated here so that a later change which starts
// drawing the header per-route has to come back and say why.
func TestHandlerServesOneHeaderToEveryRoute(t *testing.T) {
	handler := administrableHandler(core.DefaultVocabulary(), "head-1", boardTasks())

	board := ""
	for _, path := range []string{"/", "/config", "/tasks/new"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		header := headerElement(t, response.Body.String())
		if board == "" {
			board = header
			continue
		}
		if header != board {
			t.Errorf("GET %s serves a different header from the board's:\n%s\n\nwant\n%s", path, header, board)
		}
	}
}

// A switch is a control with two halves. The words are a child rather than the
// control's own text because the control now holds a track and a knob that
// writing over its text would throw away, and role/aria-checked are on the
// control itself because that is the half a screen reader reads.
func TestHandlerHeaderDrawsEverySettingAsASwitch(t *testing.T) {
	header := headerElement(t, administrableBoardPage(t, core.DefaultVocabulary()))

	for marker, label := range map[string]string{
		"data-deleted-toggle":     "data-deleted-label",
		"data-description-toggle": "data-description-label",
		"data-sync-toggle":        "data-sync-label",
	} {
		element := switchElement(t, header, marker)
		for _, want := range []string{
			`class="nav-switch"`,
			`role="switch"`,
			// Off until the client says otherwise. A page whose script never ran
			// must not draw a knob that claims a setting is on.
			`aria-checked="false"`,
			// The words, and the two halves of the switch a reader sees.
			label,
			`class="nav-switch__label`,
			`class="nav-switch__track"`,
			`class="nav-switch__knob"`,
			// The track is decoration for the state the control already states.
			// Announced twice it would be announced once too often.
			`aria-hidden="true"`,
			// Revealed by the render that has something for it to act on.
			" hidden",
		} {
			if !strings.Contains(element, want) {
				t.Errorf("the %s switch does not carry %s: %s", marker, want, element)
			}
		}
		// aria-pressed is the vocabulary of a toggle button. A switch that
		// carried both would state itself twice, in two roles' words.
		if strings.Contains(element, "aria-pressed") {
			t.Errorf("the %s switch states itself twice: %s", marker, element)
		}
	}
}

// The Deleted setting stays an anchor. The state it sets is the address, so it
// has to keep being one a browser can cmd-click, bookmark and walk with Back;
// what role="switch" changes is what it is called, not where it goes.
func TestHandlerDeletedSwitchIsStillTheAddressItSets(t *testing.T) {
	header := headerElement(t, administrableBoardPage(t, core.DefaultVocabulary()))

	element := switchElement(t, header, "data-deleted-toggle")
	if !strings.HasPrefix(element, "<a ") {
		t.Errorf("the Deleted setting is no longer an anchor: %s", element)
	}
	if !strings.Contains(element, `href="/?deleted=1"`) {
		t.Errorf("the Deleted setting does not name the address that shows the column: %s", element)
	}
}

// The knob has to move, or the control is a sentence with a picture beside it.
func TestHandlerStylesheetMovesTheSwitchKnobWithItsState(t *testing.T) {
	body := administrableBoardPage(t, core.DefaultVocabulary())

	off := cssRule(t, body, ".nav-switch__knob")
	on := cssRule(t, body, `.nav-switch[aria-checked="true"] .nav-switch__knob`)
	if !strings.Contains(off, "left: .13rem") {
		t.Errorf("the knob does not start at one end of its track: %s", off)
	}
	if !strings.Contains(on, "left: calc(") {
		t.Errorf("a switch turned on does not move its knob: %s", on)
	}
	if track := cssRule(t, body, `.nav-switch[aria-checked="true"] .nav-switch__track`); !strings.Contains(track, "background: var(--wb-primary)") {
		t.Errorf("a switch turned on does not fill its track: %s", track)
	}
	// Reachable by keyboard and visibly so, like every other control here.
	if focus := cssRule(t, body, ".nav-switch:focus-visible"); !strings.Contains(focus, "outline: 3px solid var(--wb-primary)") {
		t.Errorf("a focused switch is not outlined the way this page outlines focus: %s", focus)
	}
}

// A switch with nothing to set has to look like one. The knob is the promise
// this control makes, and a knob that cannot move must not be drawn as though a
// click were about to move it — so the words dim, the pointer stops offering a
// click, and the track stops lighting up under the cursor.
func TestHandlerStylesheetDrawsASwitchWithNothingToSetAsUnavailable(t *testing.T) {
	body := administrableBoardPage(t, core.DefaultVocabulary())

	unavailable := cssRule(t, body, `.nav-switch[aria-disabled="true"]`)
	if !strings.Contains(unavailable, "cursor: default") {
		t.Errorf("an unavailable switch still offers the pointer of a control that acts: %s", unavailable)
	}
	if !strings.Contains(unavailable, "color:") {
		t.Errorf("an unavailable switch reads as brightly as one that works: %s", unavailable)
	}
	if track := cssRule(t, body, `.nav-switch[aria-disabled="true"] .nav-switch__track`); !strings.Contains(track, "background:") {
		t.Errorf("an unavailable switch draws the track of a live one: %s", track)
	}
	// The hover highlight is an offer, and this control has nothing to offer.
	// Written after the plain hover rule so it wins the cascade rather than
	// relying on a reader to notice which came first.
	hover := `.nav-switch[aria-disabled="true"]:hover .nav-switch__track`
	if rule := cssRule(t, body, hover); !strings.Contains(rule, "border-color:") {
		t.Errorf("an unavailable switch lights its track under the cursor: %s", rule)
	}
	if strings.Index(body, hover) < strings.Index(body, ".nav-switch:hover .nav-switch__track") {
		t.Error("the unavailable switch's hover rule is written before the one it has to override")
	}
}

// The two groups are laid out apart, the settings stack their freshness reading
// above their row, and the row wraps instead of widening the document. The last
// is the rule a phone depends on: this row was 454px wide at a 390px viewport
// before it was allowed to wrap, and the whole page scrolled sideways with it.
func TestHandlerStylesheetHoldsTheTwoHeaderGroupsApart(t *testing.T) {
	body := administrableBoardPage(t, core.DefaultVocabulary())

	if lead := cssRule(t, body, ".app-header__lead"); !strings.Contains(lead, "display: flex") {
		t.Errorf("the title and the routes are not laid out together: %s", lead)
	}
	settings := cssRule(t, body, ".header-settings")
	if !strings.Contains(settings, "flex-direction: column") {
		t.Errorf("the freshness reading is not stacked above the settings: %s", settings)
	}
	if row := cssRule(t, body, ".header-switches"); !strings.Contains(row, "flex-wrap: wrap") {
		t.Errorf("the settings row cannot wrap, so a phone scrolls sideways over the header: %s", row)
	}
	if links := cssRule(t, body, ".header-links"); !strings.Contains(links, "flex-wrap: wrap") {
		t.Errorf("the routes cannot wrap: %s", links)
	}
	// Every label reads two ways and the two are not the same width, so each
	// reserves the width of its longer reading. The row is right-aligned, so a
	// label that shrank would drag the settings on its left along with it; on a
	// phone, where the row wraps and aligns left, it would push the ones after
	// it instead.
	for _, label := range []string{
		".nav-switch__label--deleted",
		".nav-switch__label--descriptions",
		".nav-switch__label--sync",
	} {
		if rule := cssRule(t, body, label); !strings.Contains(rule, "min-width:") {
			t.Errorf("%s reserves no width, so flipping it moves the settings beside it: %s", label, rule)
		}
	}
}

// A switch answers Space. The one that is an anchor gets Enter from the browser
// and Space from nobody — the browser scrolls the page with it — so the client
// raises the click the anchor would have raised, which reaches the same document
// listener that turns every link on this page into a render.
func TestHandlerClientActivatesTheDeletedSwitchFromTheSpaceBar(t *testing.T) {
	runBoardClient(t, "the Deleted switch under the space bar", reconcileBoardTasks(), `
  if (deletedToggle.hidden) throw new Error("the board did not reveal the Deleted setting");
  if (!deletedToggle.eventListeners.keydown) throw new Error("the Deleted switch answers no key at all");

  // A key that is not Space is the browser's business, and Enter is already the
  // anchor's: neither may be swallowed here.
  let prevented = 0;
  deletedToggle.eventListeners.keydown({ key: "Enter", preventDefault() { prevented += 1; } });
  if (prevented !== 0) throw new Error("the Deleted switch took Enter away from the anchor");
  if (historyPaths.length !== 0) throw new Error("Enter navigated twice: " + JSON.stringify(historyPaths));

  deletedToggle.eventListeners.keydown({ key: " ", preventDefault() { prevented += 1; } });
  if (prevented !== 1) throw new Error("Space still scrolls the page under the switch");
  if (historyPaths.length !== 1 || historyPaths[0] !== "/?deleted=1") {
    throw new Error("Space did not show the column: " + JSON.stringify(historyPaths));
  }
  if (deletedLabel.textContent !== "Hide Deleted" || deletedToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("the switch did not flip: " + deletedLabel.textContent + " " + deletedToggle.getAttribute("aria-checked"));
  }

  deletedToggle.eventListeners.keydown({ key: " ", preventDefault() {} });
  if (historyPaths[1] !== "/") throw new Error("Space did not hide the column again: " + JSON.stringify(historyPaths));
  if (deletedLabel.textContent !== "Show Deleted" || deletedToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("the switch did not flip back: " + deletedLabel.textContent + " " + deletedToggle.getAttribute("aria-checked"));
  }
`)
}

// The publishing switch reports the mode the server is in, which is why it is
// read from the server rather than assumed: a board set to defer still publishes
// inline when no watcher answers.
//
// On is the mode where something else publishes for you — a watcher picks each
// change up just after it is recorded — so the word offered while it is off is
// "Publish", which is what turning it on arranges. Off is the mode where this
// board pushes the change itself before the response returns, so the word
// offered while it is on is "Push", which is what taking it back means and what
// the inline mode's own description calls it.
func TestHandlerClientDrawsThePublishingSwitchFromTheServersMode(t *testing.T) {
	runBoardClientWithSetup(t, "the publishing switch", reconcileBoardTasks(), syncFetchStub, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.hidden) throw new Error("a board told a publishing mode drew no switch for it");
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("a board handing publication to a watcher does not read as on: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-checked"));
  }
  // A watcher is answering, so the flip this switch offers is one it can make.
  if (syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("a switch with a mode to set says it is unavailable: " +
      syncToggle.getAttribute("aria-disabled"));
  }

  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes[syncModes.length - 1] !== "inline") {
    throw new Error("the switch asked for " + JSON.stringify(syncModes));
  }
  if (syncLabel.textContent !== "Publish" || syncToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("a board pushing inline does not read as off: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-checked"));
  }

  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes[syncModes.length - 1] !== "deferred") {
    throw new Error("the switch did not hand publication back: " + JSON.stringify(syncModes));
  }
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("the switch did not turn back on: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-checked"));
  }
`)
}

// A board with nobody to defer to cannot hand publication anywhere, and the
// switch says so rather than offering a flip that does nothing.
//
// This is what the control used to do instead: it read off, a click set the
// server's mode to inline, the mode it reports did not change because the mode
// was never what made it read off, and the next click set the mode back. A
// reader saw a knob that would not move and left the server's publishing
// preference wherever their clicking had landed it, to be discovered later by
// starting a watcher. So the switch is marked unavailable, names the reason,
// and refuses to write a setting whose effect nobody can see — and because a
// watcher can start at any moment, activating it asks the server again instead.
func TestHandlerClientRefusesThePublishingSwitchWhileNoWatcherAnswers(t *testing.T) {
	runBoardClientWithSetup(t, "a deferral no watcher answers", reconcileBoardTasks(), `
`+syncFetchStub+`
syncWatcher = false;
syncReason = "no-watcher";
syncDetail = "no watcher is answering, so changes publish inline";
`, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.hidden) throw new Error("a board told a publishing mode drew no switch for it");
  if (syncToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("a deferral with nobody to defer to does not read as pushing: " +
      syncToggle.getAttribute("aria-checked"));
  }
  if (syncToggle.getAttribute("aria-disabled") !== "true") {
    throw new Error("a switch with nothing to set does not say it is unavailable: " +
      syncToggle.getAttribute("aria-disabled"));
  }
  // The words are the other half of a switch, and there is no next flip to
  // offer here, so they report the absence instead.
  if (syncLabel.textContent !== "No watcher") {
    throw new Error("the switch still offers a flip it cannot make: " + syncLabel.textContent);
  }
  if (!syncToggle.title.includes("no watcher is answering, so changes publish inline")) {
    throw new Error("the switch does not say why it cannot be flipped: " + syncToggle.title);
  }

  const readsBefore = syncReads;
  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes.length !== 0) {
    throw new Error("a switch that cannot move still changed the server's mode: " + JSON.stringify(syncModes));
  }
  if (syncReads !== readsBefore + 1) {
    throw new Error("activating the stalled switch did not ask the server again: " + syncReads);
  }

  // A watcher that starts while the page is open is found by the next
  // activation, which is what a reader who sees a dead control does anyway.
  syncWatcher = true;
  syncReason = "";
  syncDetail = "";
  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes.length !== 0) {
    throw new Error("the look-again click wrote a mode: " + JSON.stringify(syncModes));
  }
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-checked") !== "true") {
    throw new Error("a watcher that answered did not bring the switch back on: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-checked"));
  }
  if (syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("the switch is still unavailable with a watcher answering: " +
      syncToggle.getAttribute("aria-disabled"));
  }

  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes[syncModes.length - 1] !== "inline") {
    throw new Error("the switch that came back does not write: " + JSON.stringify(syncModes));
  }
`)
}

// The same is true of the other parity. Watcher absence, not the mode, is what
// empties this control: a board already pushing inline cannot hand publication
// to a watcher that is not there either, so a click on it must not quietly
// leave the server configured to defer to nobody.
func TestHandlerClientRefusesTheWatcherlessPublishingSwitchInEitherMode(t *testing.T) {
	runBoardClientWithSetup(t, "an inline board with no watcher", reconcileBoardTasks(), `
`+syncFetchStub+`
syncMode = "inline";
syncWatcher = false;
`, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.getAttribute("aria-disabled") !== "true" || syncLabel.textContent !== "No watcher") {
    throw new Error("an inline board with no watcher offers a flip: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }
  // The stub says nothing about why, and the switch still has to explain
  // itself: the server's sentence is an addition to the reading, not the whole
  // of it.
  if (!syncToggle.title.toLowerCase().includes("watcher")) {
    throw new Error("the switch does not say why it cannot be flipped: " + syncToggle.title);
  }

  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes.length !== 0) {
    throw new Error("a watcherless inline board was configured to defer: " + JSON.stringify(syncModes));
  }
  if (syncToggle.getAttribute("aria-checked") !== "false") {
    throw new Error("the switch moved on a click it refused: " + syncToggle.getAttribute("aria-checked"));
  }
`)
}

// The only thing a stalled switch still does is ask the server again, so that
// activation is the one this control cannot afford to mishandle — and a read
// is the request most likely to fail, because the reader activates a stalled
// switch precisely when something is wrong. A failed re-read used to clear the
// board's copy of the state, and a cleared state hides the switch: the reader
// activated the control and it disappeared, with nothing to say why and nothing
// to bring it back, because this activation is the only thing on the page that
// reads /api/sync after load and a hidden switch cannot be activated. A reload
// was the only way back. A read that failed knows nothing about the server's
// mode, so it now changes nothing and the switch stays where it was, still
// stalled, still able to look again.
func TestHandlerClientKeepsTheStalledPublishingSwitchWhenTheLookAgainFails(t *testing.T) {
	runBoardClientWithSetup(t, "a stalled switch whose look-again fails", reconcileBoardTasks(), `
`+syncFetchStub+`
syncWatcher = false;
syncReason = "no-watcher";
syncDetail = "no watcher is answering, so changes publish inline";
let syncUnreachable = false;
const reachableSyncFetch = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  if (url === "/api/sync" && syncUnreachable) throw new Error("the server is unreachable");
  return reachableSyncFetch(url, options);
};
`, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.hidden || syncLabel.textContent !== "No watcher") {
    throw new Error("the board did not draw a stalled switch to begin with: " +
      syncToggle.hidden + " " + syncLabel.textContent);
  }

  syncUnreachable = true;
  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.hidden) {
    throw new Error("a failed look-again took the publishing switch off the header");
  }
  if (syncLabel.textContent !== "No watcher" || syncToggle.getAttribute("aria-disabled") !== "true") {
    throw new Error("a failed look-again changed the reading it learned nothing about: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }
  if (syncModes.length !== 0) {
    throw new Error("a look-again wrote a mode: " + JSON.stringify(syncModes));
  }

  // And the switch that survived is still the working control it was: the next
  // activation reaches a server that answers, and finds the watcher that
  // started meanwhile.
  syncUnreachable = false;
  syncWatcher = true;
  syncReason = "";
  syncDetail = "";
  syncToggle.click();
  for (let tick = 0; tick < 8; tick += 1) await Promise.resolve();
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("the switch that survived a failed look-again cannot come back: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }

  syncToggle.click();
  for (let tick = 0; tick < 8; tick += 1) await Promise.resolve();
  if (syncModes[syncModes.length - 1] !== "inline") {
    throw new Error("the recovered switch does not write: " + JSON.stringify(syncModes));
  }
`)
}

// A clone with no origin has nobody to defer to either, but for a reason no
// watcher can fix: nothing is published in either mode because there is nowhere
// to publish to. Reading that as "No watcher" names the wrong cause and sends
// the reader to start a watcher the server will go on ignoring, so the two
// cases are told apart by the reason the server reports and read in their own
// words. Looking again is still worth offering — an origin can be added to a
// clone while the board is open, and the server probes for one every time it is
// asked — but it is not offered as waiting for a watcher.
func TestHandlerClientNamesAMissingOriginRatherThanAMissingWatcher(t *testing.T) {
	runBoardClientWithSetup(t, "a board with no origin", reconcileBoardTasks(), `
`+syncFetchStub+`
syncWatcher = false;
syncReason = "no-origin";
syncDetail = "no origin is configured, so nothing is published";
`, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncToggle.hidden) throw new Error("a board told a publishing mode drew no switch for it");
  if (syncToggle.getAttribute("aria-disabled") !== "true") {
    throw new Error("a switch with nothing to set does not say it is unavailable: " +
      syncToggle.getAttribute("aria-disabled"));
  }
  if (syncLabel.textContent !== "No origin") {
    throw new Error("a clone with no origin reads as something else: " + syncLabel.textContent);
  }
  if (!syncToggle.title.includes("no origin is configured, so nothing is published")) {
    throw new Error("the switch does not say why it cannot be flipped: " + syncToggle.title);
  }
  // The clause the board adds is the board's own, so it is the board's job not
  // to promise a watcher will bring this switch back. One never can here.
  if (syncToggle.title.includes("watcher")) {
    throw new Error("a clone with no origin is told to wait for a watcher: " + syncToggle.title);
  }

  const readsBefore = syncReads;
  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncModes.length !== 0) {
    throw new Error("a board with nowhere to publish was configured anyway: " + JSON.stringify(syncModes));
  }
  if (syncReads !== readsBefore + 1) {
    throw new Error("activating the switch did not ask the server again: " + syncReads);
  }

  // An origin added while the board is open is found the same way a watcher is.
  syncWatcher = true;
  syncReason = "";
  syncDetail = "";
  syncToggle.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("an origin that appeared did not bring the switch back: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }
`)
}

// A stalled switch invites repeated activation, so two reads in flight is its
// ordinary case rather than a rare one, and the network does not promise to
// answer them in order. Whichever reply landed last used to win: a slow first
// reply still carrying watcher:false, arriving after a fast second that found
// the watcher, would repaint a live switch as stalled and cost the reader the
// flip they were about to make. Each answer now claims a number and drops
// itself if a later one has already landed.
func TestHandlerClientIgnoresAStalePublishingReadThatLandsLast(t *testing.T) {
	runBoardClientWithSetup(t, "publishing reads answered out of order", reconcileBoardTasks(), `
`+syncFetchStub+`
syncWatcher = false;
syncReason = "no-watcher";
syncDetail = "no watcher is answering, so changes publish inline";
`, `
  await Promise.resolve();
  await Promise.resolve();
  if (syncLabel.textContent !== "No watcher") {
    throw new Error("the board did not draw a stalled switch to begin with: " + syncLabel.textContent);
  }

  // Hold every read open, and snapshot the server as each one is issued, so the
  // two below can be answered in the order a network would pick rather than the
  // order they were asked in.
  const promptSyncFetch = globalThis.fetch;
  const heldReads = [];
  globalThis.fetch = (url, options = {}) => {
    if (url !== "/api/sync" || (options.method || "GET") !== "GET") return promptSyncFetch(url, options);
    syncReads += 1;
    const answered = {
      format: "workbook.sync",
      version: 1,
      sync: { mode: syncMode, watcher: syncWatcher, reason: syncReason, detail: syncDetail }
    };
    return new Promise((resolve) => heldReads.push(() => resolve({ ok: true, json: async () => answered })));
  };

  syncToggle.click();
  await Promise.resolve();
  syncWatcher = true;
  syncReason = "";
  syncDetail = "";
  syncToggle.click();
  await Promise.resolve();
  if (heldReads.length !== 2) {
    throw new Error("expected two reads in flight, held " + heldReads.length);
  }

  heldReads[1]();
  for (let tick = 0; tick < 8; tick += 1) await Promise.resolve();
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("the second read did not bring the switch back: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }

  heldReads[0]();
  for (let tick = 0; tick < 8; tick += 1) await Promise.resolve();
  if (syncLabel.textContent !== "Push" || syncToggle.getAttribute("aria-disabled") !== "false") {
    throw new Error("a stale read repainted a live switch as stalled: " +
      syncLabel.textContent + " " + syncToggle.getAttribute("aria-disabled"));
  }
`)
}

// syncFetchStub is a server with a publishing mode, wrapped around the harness's
// own fetch so that everything else this page asks for is answered as it always
// was. The harness answers /api/sync with the task document, which is not a sync
// document and leaves the switch hidden — which is the right answer for a test
// that is not about publishing, and no answer at all for one that is.
const syncFetchStub = `
let syncMode = "deferred";
let syncWatcher = true;
let syncReason = "";
let syncDetail = "";
const syncModes = [];
let syncReads = 0;
const boardFetch = globalThis.fetch;
const syncDocument = () => ({
  format: "workbook.sync",
  version: 1,
  sync: { mode: syncMode, watcher: syncWatcher, reason: syncReason, detail: syncDetail }
});
globalThis.fetch = async (url, options = {}) => {
  if (url !== "/api/sync") return boardFetch(url, options);
  if ((options.method || "GET") === "GET") syncReads += 1;
  else {
    syncMode = JSON.parse(options.body).mode;
    syncModes.push(syncMode);
  }
  return { ok: true, json: async () => syncDocument() };
};
`
