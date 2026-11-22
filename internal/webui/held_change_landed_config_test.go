package webui

import (
	"strconv"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// A configuration change a proxy passed to the board and then answered with a
// wait anyway.
//
// The page holds the change and sends it again, and the resend names a head
// the first attempt moved, so the board refuses it as stale. Reported as it
// reads, that refusal tells the reader a change that is recorded was not, and
// invites them to make it again. What is pinned here, for every kind of change
// the configuration page sends: when the configuration the refusal carries
// holds the change, the page says it was made; when it does not, the page says
// what a stale refusal always said; and when the page cannot read the
// configuration at all, it says the change may already have been made.

// landingProxy stands in front of the board for the page's configuration
// writes. The first write reaches the board — the board's configuration
// becomes `landed`, when the test gives one — and the page is told to wait a
// second anyway. Every write after that is refused as stale, carrying the
// configuration as it then stands, which is what the board does with a write
// against a head it has moved past.
const landingProxy = `
const proxy = { landed: null, moved: null, writes: [], readsFail: false, carries: true };
const beneathProxy = globalThis.fetch;
globalThis.fetch = async (url, options = {}) => {
  const method = (options.method || "GET").toUpperCase();
  const configuration = url.startsWith("/api/vocabulary") || url === "/api/display";
  if (!configuration) return beneathProxy(url, options);
  if (method === "GET") {
    if (proxy.readsFail) throw new TypeError("Failed to fetch");
    return beneathProxy(url, options);
  }
  proxy.writes.push({ url, method, body: JSON.parse(options.body) });
  if (proxy.writes.length === 1) {
    // The board took it, or somebody else moved the head meanwhile.
    vocabularyRead = proxy.landed || proxy.moved || vocabularyRead;
    return { ok: false, status: 429,
      headers: { get: (name) => (name.toLowerCase() === "retry-after" ? "1" : null) },
      json: async () => JSON.parse("<html><head><title>429 Too Many Requests</title></head></html>") };
  }
  const refusal = { format: "workbook.error", version: 1,
    error: { category: "stale-write", message: "this project's configuration changed since head-1; reload and try again" } };
  if (proxy.carries) {
    if (url === "/api/display") { refusal.display = vocabularyRead.display; refusal.shape = vocabularyRead.shape; }
    else refusal.vocabulary = vocabularyRead;
  }
  return { ok: false, status: 409, headers: { get: () => null }, json: async () => refusal };
};
// The configuration as read, and a copy to change into what the board holds
// once a change lands.
function configurationCopy(head) {
  const copy = JSON.parse(JSON.stringify(vocabularyRead));
  copy.head = head;
  return copy;
}
// Ends the one-second wait the page was told to make, once it has made it.
async function releaseHold() {
  for (let turn = 0; turn < 400; turn += 1) {
    const wait = windowTimeouts.find((timer) => !timer.canceled && !timer.ran && timer.delay === 1000);
    if (wait) {
      if (heldChangeNotice.hidden) throw new Error("the page held the change without saying so");
      wait.ran = true;
      wait.callback();
      for (let more = 0; more < 20; more += 1) await settle();
      return;
    }
    await settle();
  }
  throw new Error("the page never held the change to send it again");
}
`

// configurationChange is one kind of change the configuration page sends.
type configurationChange struct {
	name string
	// land turns `landed`, a copy of the configuration as read, into what the
	// board holds once the change is recorded.
	land string
	// drive makes the change the way a reader does.
	drive string
	// messages names the section's own report.
	messages string
	// saved is what the section says once the change is found to have landed.
	saved string
	// stale is what it says when the change is not there.
	stale string
}

func configurationChanges() []configurationChange {
	const statusStale = "This project's statuses changed in another clone, so this change was not applied."
	const priorityStale = "This project's priorities changed in another clone, so this change was not applied."
	const keyStale = "This project's keys changed in another clone, so this change was not applied."
	return []configurationChange{
		{
			name: "a status added",
			land: `landed.statuses.push({ status: "triage", label: "Triage", rank: "4/1", tags: [] });`,
			drive: `const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);`,
			messages: "panelMessages", saved: "Added triage.", stale: statusStale,
		},
		{
			name: "a status relabeled",
			land: `landed.statuses.find((definition) => definition.status === "icebox").label = "Deep Freeze";`,
			drive: `panelControl(panelRow("icebox"), "Edit Icebox").eventListeners.click();
  const form = panelForm("icebox", "vocabularyEdit");
  findElement(form, (element) => element.id === "status-label-icebox").value = "Deep Freeze";
  await submitPanelForm(form);`,
			messages: "panelMessages", saved: "Updated Deep Freeze.", stale: statusStale,
		},
		{
			name: "a status removed",
			land: `landed.statuses = landed.statuses.filter((definition) => definition.status !== "shipped");`,
			drive: `panelControl(panelRow("shipped"), "Delete Shipped").eventListeners.click();
  const form = panelForm("shipped", "vocabularyDelete");
  chooseOption(findElement(form, (element) => element.id === "status-into-shipped"), "icebox");
  await submitPanelForm(form);`,
			messages: "panelMessages", saved: "This status change was saved.", stale: statusStale,
		},
		{
			name: "the statuses reordered",
			land: `const icebox = landed.statuses.shift(); landed.statuses.splice(1, 0, icebox);`,
			drive: `panelControl(panelRow("icebox"), "Move Icebox later").eventListeners.click();
  await settle();`,
			messages: "panelMessages", saved: "Reordered this project's columns.", stale: statusStale,
		},
		{
			name: "a priority added",
			land: `landed.priorities.priorities.push({ priority: "whenever", label: "Whenever", rank: "4/1", tags: [] });`,
			drive: `const form = priorityAdd();
  const name = findElement(form, (element) => element.id === "priority-new-name");
  name.value = "whenever";
  name.eventListeners.input();
  await submitPriorityForm(form);`,
			messages: "priorityMessages", saved: "Added whenever.", stale: priorityStale,
		},
		{
			name: "a priority relabeled",
			land: `landed.priorities.priorities.find((definition) => definition.priority === "high").label = "Urgent";`,
			drive: `await openPriorityForm("high", "Edit High");
  const form = priorityForm("high", "priorityEdit");
  findElement(form, (element) => element.id === "priority-label-high").value = "Urgent";
  await submitPriorityForm(form);`,
			messages: "priorityMessages", saved: "Updated Urgent.", stale: priorityStale,
		},
		{
			name: "a priority recolored",
			land: `landed.priorities.priorities.find((definition) => definition.priority === "high").color = "#7c3aed";`,
			drive: `await openPriorityForm("high", "Edit High");
  const form = priorityForm("high", "priorityEdit");
  findElement(form, (element) => element.id === "priority-color-high").value = "#7C3AED";
  await submitPriorityForm(form);`,
			messages: "priorityMessages", saved: "Recolored High.", stale: priorityStale,
		},
		{
			name: "a priority made the default",
			land: `landed.priorities.default = "high";`,
			drive: `await openPriorityForm("high", "Edit High");
  const form = priorityForm("high", "priorityEdit");
  findElement(form, (element) => element.dataset.priorityDefault === "high").checked = true;
  await submitPriorityForm(form);`,
			messages: "priorityMessages", saved: "New tasks now land at High.", stale: priorityStale,
		},
		{
			name: "a priority removed",
			land: `landed.priorities.priorities = landed.priorities.priorities.filter((definition) => definition.priority !== "high");`,
			drive: `await openPriorityForm("high", "Delete High");
  const form = priorityForm("high", "priorityDelete");
  chooseOption(findElement(form, (element) => element.id === "priority-into-high"), "medium");
  await submitPriorityForm(form);`,
			messages: "priorityMessages", saved: "This priority change was saved.", stale: priorityStale,
		},
		{
			name: "a priority moved",
			land: `const medium = landed.priorities.priorities.splice(1, 1)[0]; landed.priorities.priorities.unshift(medium);`,
			drive: `panelControl(priorityRow("medium"), "Move Medium earlier").eventListeners.click();
  await settle();`,
			messages: "priorityMessages", saved: "Moved Medium up.", stale: priorityStale,
		},
		{
			name: "a key added",
			land: `landed.keys.keys.push({ key: "THIRD", state: "active", current: false });`,
			drive: `const form = keyAdd();
  const name = findElement(form, (element) => element.id === "key-new-name");
  name.value = "THIRD";
  if (name.eventListeners.input) name.eventListeners.input();
  await submitKeyForm(form);`,
			messages: "keyMessages", saved: "Added THIRD.", stale: keyStale,
		},
		{
			name: "a key made current",
			land: `landed.keys.current = "SPARE";
  landed.keys.keys.forEach((entry) => { entry.current = entry.key === "SPARE"; });`,
			drive: `panelControl(keyPanelRow("SPARE"), "Make current").eventListeners.click();
  await settle();`,
			messages: "keyMessages", saved: "New tasks are now minted under SPARE.", stale: keyStale,
		},
		{
			name: "a key retired",
			land: `landed.keys.keys.find((entry) => entry.key === "SPARE").state = "retired";`,
			drive: `panelControl(keyPanelRow("SPARE"), "Retire").eventListeners.click();
  await settle();`,
			messages: "keyMessages", saved: "Retired SPARE.", stale: keyStale,
		},
		{
			name: "a key reactivated",
			land: `landed.keys.keys.find((entry) => entry.key === "WB").state = "active";`,
			drive: `panelControl(keyPanelRow("WB"), "Reactivate").eventListeners.click();
  await settle();`,
			messages: "keyMessages", saved: "Reactivated WB.", stale: keyStale,
		},
	}
}

func TestClientSettlesAConfigurationChangeThatLandedBeforeItsResend(t *testing.T) {
	t.Parallel()
	// Production mutation: a page that reports the refused resend as it reads
	// says a recorded change was not applied, and invites the reader to make
	// it again.
	for _, change := range configurationChanges() {
		for _, landed := range []bool{true, false} {
			name := change.name + ", landed"
			if !landed {
				name = change.name + ", not landed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				outcome := `
  const said = ` + change.messages + `();
  if (proxy.writes.length !== 2) throw new Error("the change was sent " + proxy.writes.length + " times, want once and one resend");
  if (JSON.stringify(proxy.writes[0].body) !== JSON.stringify(proxy.writes[1].body)) throw new Error("the resend was not the same change");
  if (!heldChangeNotice.hidden) throw new Error("the held-change notice stayed up");
`
				if landed {
					outcome += `
  if (said.join(" ").indexOf(` + strconv.Quote(change.saved) + `) < 0) throw new Error("a change that landed reads " + JSON.stringify(said));
  if (/not applied|another clone|may already/.test(said.join(" "))) throw new Error("a change that landed is reported as refused: " + JSON.stringify(said));
`
				} else {
					outcome += `
  if (said.join(" ").indexOf(` + strconv.Quote(change.stale) + `) < 0) throw new Error("a change that did not land reads " + JSON.stringify(said));
`
				}
				setup := `proxy.moved = configurationCopy("head-other");`
				if landed {
					setup = `const landed = configurationCopy("head-2");
  ` + change.land + `
  proxy.landed = landed;`
				}
				runKeyPanelClient(t, "a configuration change that "+name, "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
`+priorityFetchHarness+landingProxy+`
  await openStatuses();
  `+setup+`
  `+change.drive+`
  await releaseHold();
`+outcome)
			})
		}
	}
}

func TestClientSaysAConfigurationChangeMayHaveLandedWhenItCannotLook(t *testing.T) {
	t.Parallel()
	// Production mutation: with nothing to compare against, a page that falls
	// back to the stale refusal's own sentence tells the reader to make again
	// a change that may already be recorded.
	runKeyPanelClient(t, "a status added with the configuration unreadable", "/", projectKeys(t), nil, `
  vocabularyRead = `+keyPanelVocabularyJSON(t, projectKeys(t), "head-1")+`;
`+landingProxy+`
  await openStatuses();
  proxy.carries = false;
  proxy.readsFail = true;
  const form = panelAdd();
  const name = findElement(form, (element) => element.id === "status-new-name");
  name.value = "triage";
  name.eventListeners.input();
  await submitPanelForm(form);
  await releaseHold();
  const said = panelMessages().join(" ");
  if (said.indexOf("may already have been saved; check before making it again") < 0) {
    throw new Error("a change the page could not check reads " + JSON.stringify(said));
  }
  if (/another clone|make the change again/.test(said)) throw new Error("the page invited the change again: " + JSON.stringify(said));
`)
}

func TestClientSettlesBoardSettingsThatLandedBeforeTheirResend(t *testing.T) {
	t.Parallel()
	// Production mutation: the board settings report the refused resend as a
	// save that was not applied.
	vocabulary := handlerVocabulary(t)
	state := VocabularyState{Vocabulary: vocabulary, Head: "head-1", Display: core.DisplaySettings{Name: "Atlas"}}
	for _, landed := range []bool{true, false} {
		name := "landed"
		want := `"This board is named \"Atlas\", drawn in #123abc."`
		if !landed {
			name = "not landed"
			want = `"This project's configuration changed in another clone, so these settings were not applied."`
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			setup := `proxy.moved = configurationCopy("head-other");`
			if landed {
				setup = `const landed = configurationCopy("head-2");
  landed.display = Object.assign({}, landed.display, { head: "head-2", primaryColor: "#123abc" });
  proxy.landed = landed;`
			}
			runClientOverHandler(t, configurableHandler(vocabulary, "head-1", nil),
				"board settings that "+name, "/", "", vocabulary, "head-1", nil, `
  vocabularyRead = `+string(mustJSON(t, vocabularyDocument(state)))+`;
`+landingProxy+`
  await openStatuses();
  `+setup+`
  displayField("primaryColor").value = "#123ABC";
  const saving = saveDisplay();
  await releaseHold();
  await saving;
  const said = displayPanelStatus.textContent;
  if (said.indexOf(`+want+`) < 0) throw new Error("board settings that `+name+` read " + JSON.stringify(said));
  if (proxy.writes.length !== 2) throw new Error("the settings were sent " + proxy.writes.length + " times");
`)
		})
	}
}
