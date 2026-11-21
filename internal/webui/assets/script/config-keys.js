      // This project's task-ID keys: the statuses section's third sibling, built
      // out of the same parts and differing where a key differs from a status.
      //
      // Three differences, and every one of them shows on the page.
      //
      // NOTHING IS RENAMED, REORDERED OR REMOVED. A key is added, and after
      // that the only changes are which one mints new tasks and whether a key
      // mints at all. A task ID is a permanent name — every task minted under a
      // key keeps it forever — so there is no rename to forward and no removal
      // to price, and the list has no rank to move a row up and down through.
      // What the section draws instead is the order the keys were added, which
      // is the history rather than an arrangement anybody chose.
      //
      // BEING CURRENT IS NOT A STATE, AND IT IS GIVEN RATHER THAN TAKEN. The
      // document names the current key once, beside each key's active/retired
      // word, so the row that holds it is drawn holding it with nothing to
      // offer: there is no operation that stops a key minting without handing
      // the minting to another, because a project with no current key is a
      // project where a new task has nowhere to go.
      //
      // AND A KEY CHANGE COSTS NO TASKS. The answer carries no `tasks` member
      // at all, where a status or priority change prices the tasks it moved, so
      // nothing here reports a cost — and nothing here may invent one.
      let keyBusy = false;
      let keyControls = [];
      const keySendingMessage = "Sending a change to this project's keys.";
      function keyReport(kind, message, warnings = []) {
        reportInto(keyPanelStatus, kind, message, warnings);
      }
      function applyKeyBusy() {
        keyControls.forEach((control) => {
          control.disabled = configBusy() || control.dataset.blocked === "true";
        });
      }
      function keyPath(key) {
        return "/api/vocabulary/keys/" + encodeURIComponent(key);
      }
      // The three changes a key can undergo, each its own PATCH naming exactly
      // one intent — which is what the route takes, and it refuses a body naming
      // two or none.
      function makeKeyCurrent(entry) {
        return changeVocabulary("PATCH", keyPath(entry.key), { current: true },
          () => "New tasks are now minted under " + entry.key + ".", keyChanges);
      }
      function retireKey(entry) {
        return changeVocabulary("PATCH", keyPath(entry.key), { retire: true },
          () => "Retired " + entry.key + ". The tasks already minted under it keep their IDs.",
          keyChanges);
      }
      function reactivateKey(entry) {
        return changeVocabulary("PATCH", keyPath(entry.key), { reactivate: true },
          () => "Reactivated " + entry.key + ". It can mint new tasks again once it is made current.",
          keyChanges);
      }
      function keyRow(entry) {
        const item = document.createElement("li");
        item.className = "admin-status";
        item.dataset.vocabularyKey = entry.key;
        // Said rather than merely left unset, where a status row and a priority
        // row are both draggable: there is no order here for a drag to ask for,
        // and the shared row class carries the grab cursor those two earn. The
        // stylesheet takes the cursor back off the attribute this writes, the way
        // it does for the stand-in card a create has not placed yet.
        item.draggable = false;
        const name = document.createElement("div");
        name.className = "admin-status__name";
        // A key has no label beside its token, because the key IS the name: it
        // is the prefix every task ID minted under it carries, and there is no
        // second word a project could give it. So the row draws the token where
        // its siblings draw a label and a token.
        const token = document.createElement("code");
        token.className = "admin-status__token";
        text(token, entry.key);
        name.append(token);
        // What this key is, in the document's own word, drawn as the chips the
        // status and priority roles are drawn as. Nothing here decides the word.
        const state = document.createElement("span");
        state.className = "admin-tag";
        state.dataset.keyState = entry.state;
        text(state, entry.state);
        name.append(state);
        if (entry.current) {
          const badge = document.createElement("span");
          badge.className = "admin-tag";
          badge.dataset.keyCurrent = "";
          text(badge, "current");
          name.append(badge);
        }
        const controls = document.createElement("div");
        controls.className = "admin-status__controls";
        const active = entry.state !== "retired";
        // A row offers what its key can undergo and nothing else. The current
        // key offers neither: there is no operation that stops a key minting
        // without handing the minting to another, and retiring it would leave a
        // new task nowhere to go. What a project down to one key needs to read
        // is not a disabled control on the row — the current key is always
        // active, so "this is the only active key" can never be the reason a row
        // withholds one — it is the sentence renderKeyPanel draws under the list.
        if (active && !entry.current) {
          controls.append(panelButton("Make current", "Mint new tasks under " + entry.key,
            () => makeKeyCurrent(entry), { controls: keyControls }));
          controls.append(panelButton("Retire", "Stop minting new tasks under " + entry.key,
            () => retireKey(entry), { controls: keyControls }));
        }
        if (!active) {
          controls.append(panelButton("Reactivate", "Let " + entry.key + " mint new tasks again",
            () => reactivateKey(entry), { controls: keyControls }));
        }
        item.append(name, controls);
        return item;
      }
      // Adding a key, with the one setting the add route takes.
      //
      // "Mint new tasks under it" is a checkbox on this form rather than a
      // control of its own, because that is what `workbook key add --current`
      // is: one write that adds the key and moves the minting to it. Two
      // controls would be two ledger commits for one decision.
      //
      // Nothing here says what a key may be called. core.ValidateProjectKey is
      // the one reading of that, and the route answers a name that is not a key
      // in the sentence `workbook key add` prints — so a client that refused
      // first would be refusing in words of its own.
      function addKeyForm() {
        const region = document.createElement("div");
        region.className = "admin-add";
        const heading = document.createElement("h3");
        heading.className = "admin-add__title";
        text(heading, "Add a key");
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.keyAdd = "";
        const name = panelTextInput("key-new-name", "", "NEW", keyControls);
        // The wrapping label the status tag checkboxes are drawn in, so the
        // caption is associated with the box without an id to point at and the
        // section needs no styling of its own for a shape it already has.
        const choice = document.createElement("label");
        choice.className = "admin-tag-choice";
        const box = document.createElement("input");
        box.type = "checkbox";
        box.id = "key-new-current";
        const caption = document.createElement("span");
        text(caption, "Mint new tasks under it");
        choice.append(box, caption);
        keyControls.push(box);
        const add = panelSubmitButton("Add key", "",
          { modifier: "admin-button--primary", blocked: true, controls: keyControls });
        const guard = () => blockPanelControl(add, !name.value.trim());
        name.addEventListener("input", guard);
        function submit() {
          const key = name.value.trim();
          if (!key) return;
          // Read here rather than in the sentence below, because the render this
          // change triggers rebuilds this form: the node would still answer,
          // being detached rather than gone, but a report reading state out of a
          // control nobody can see any more is a report waiting to be wrong.
          const minting = box.checked;
          const wasCurrent = currentKey;
          const addition = { key };
          if (minting) addition.current = true;
          changeVocabulary("POST", "/api/vocabulary/keys", addition,
            () => minting
              ? "Added " + key + ". New tasks are now minted under it."
              : "Added " + key + ". New tasks are still minted under " + wasCurrent + ".",
            keyChanges);
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        form.append(panelField("Key", name), choice, add);
        region.append(heading, form);
        return region;
      }
      function renderKeyPanel() {
        if (!keyPanel || !keyPanelBody) return;
        // Focus is caught and parked the way the statuses section catches it:
        // the control the reader pressed is one of the nodes this drops.
        const held = document.activeElement;
        const inside = Boolean(held && keyPanel.contains(held));
        keyControls = [];
        // Gated on the read rather than on the attribute the page was served
        // with, for the reason the priorities section is gated: every change
        // this section sends names the head that read returned, so a list drawn
        // before it landed would be a list whose every control is dead.
        if (!panelVocabulary) {
          const message = document.createElement("p");
          message.className = "admin-note";
          message.dataset.keyPanelEmpty = "";
          text(message, panelReading
            ? "Reading this project's keys."
            : "This project's keys are not available.");
          keyPanelBody.replaceChildren(message);
          restoreKeyFocus(inside);
          return;
        }
        const entries = publishedKeys;
        const list = document.createElement("ul");
        list.className = "admin-statuses";
        list.setAttribute("role", "list");
        entries.forEach((entry) => list.append(keyRow(entry)));
        // The one row that offers nothing, explained where the reader is looking.
        //
        // A project that has never added a key has one key, it is current, and a
        // current key has no change to offer — so the list is a single row with
        // an empty control column, which reads as a section that is somehow
        // unavailable. This says what is actually true and what to do about it,
        // the way the only-priority removal says there is nowhere for its tasks
        // to go. It is drawn between the list and the form it points at.
        //
        // Exactly one key, rather than one active key: a project whose second key
        // is retired can reactivate it from the row above, so it has the move
        // this sentence is about.
        const parts = [list];
        if (entries.length === 1) {
          const note = document.createElement("p");
          note.className = "admin-note";
          note.dataset.keyPanelOnlyKey = "";
          text(note, "This is the only key this project has, so every new task is minted under it. " +
            "Add another key below to move the minting, or to retire this one.");
          parts.push(note);
        }
        keyPanelBody.replaceChildren(...parts, addKeyForm());
        applyKeyBusy();
        restoreKeyFocus(inside);
      }
      function restoreKeyFocus(inside) {
        if (!inside || !keyPanel || keyPanel.contains(document.activeElement)) return;
        if (typeof keyPanel.focus === "function") keyPanel.focus();
      }
      // Leaving the route takes this section's list with it, for the reason it
      // takes the statuses: the next visit reads them again rather than offering
      // a change composed against values that may be minutes old.
      //
      // What it does not take is the keys themselves. publishedKeys is what the
      // create form's chooser reads, on every route, and the page's most recent
      // reading of them is better than the one it was loaded with — which is the
      // whole reason they are adopted rather than re-read.
      function leaveKeySection() {
        if (!keyPanel || keyPanel.hidden) return;
        keyPanel.hidden = true;
        keyPanel.remove();
        keyPanelBody.replaceChildren();
        keyPanelStatus.replaceChildren();
        keyPanelStatus.dataset.kind = "";
      }
