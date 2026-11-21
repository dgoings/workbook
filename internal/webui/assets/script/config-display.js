      // The board's own settings: what this project calls itself, and the two
      // colors it is drawn in.
      //
      // It is the statuses panel's sibling in every way that matters — its
      // controls are disabled while a change is in flight, the change waits for
      // the board's own writes to settle first, a stale write is terminal and
      // the current settings are adopted from the refusal — and it differs in
      // the one way its change differs: a save is a full replacement. The three
      // fields say what the configuration should be, an empty field is a setting
      // cleared, and one Save sends all three.
      //
      // What the server does with that is the reason the form can be this
      // simple, and it is worth saying here: it records only the settings that
      // actually changed, and a save nobody edited records nothing at all. A
      // display operation marks this project's ledger as needing a newer
      // Workbook — permanently, for every clone that fetches it — so a Save
      // pressed twice must cost nothing the second time.
      //
      // All three sections write the same ref against the same head, so none of
      // them starts while another is in flight and each greys the others'
      // controls while it runs — which is what configBusy below is. They keep
      // their own live regions, though: a refused status or priority change must
      // not blank a name the reader has just typed, and a saved name must not
      // erase what that change had to say.
      let displaySettings = null;
      let displayBusy = false;
      let displayControls = [];
      // The fields, held out here so a save reads what is in them and a render
      // can put them back. They are rebuilt with the form.
      let displayFields = null;
      const displaySendingMessage = "Sending this project's board settings.";
      function configBusy() {
        return panelBusy || priorityBusy || displayBusy || keyBusy;
      }
      function displayReport(kind, message, warnings = []) {
        reportInto(displayPanelStatus, kind, message, warnings);
      }
      function applyDisplayBusy() {
        displayControls.forEach((control) => { control.disabled = configBusy(); });
      }
      function applyConfigBusy() {
        applyPanelBusy();
        applyPriorityBusy();
        applyDisplayBusy();
        applyKeyBusy();
      }
      function adoptDisplay(settings) {
        displaySettings = settings;
        renderDisplayPanel();
        // And the board behind this page is redrawn in the accent and the ink
        // the answer carries, composed by the server for the reason the
        // priority ink is. A document with no display member carries no theme,
        // and redrawBoardStyle leaves the board alone rather than clearing it:
        // that is a project that has configured no settings, not a project whose
        // settings were cleared — which arrives as an empty stylesheet and does
        // clear the board.
        redrawBoardStyle(boardThemeStyle, settings && settings.theme);
      }
      // Takes a display document as the panel's own, and takes its head as the
      // page's: there is one configuration ledger, so a display change moves the
      // tip a status change has to name, and the standing notice above main is
      // what tells the board it has moved.
      function adoptDisplayAtHead(settings, shape) {
        panelHead = typeof settings.head === "string" ? settings.head : "";
        // The answer states the shape of the configuration it wrote into, beside
        // the settings rather than in them, because one save moves the one
        // ledger tip and the board has to be told whether anything it is drawing
        // moved with it — which, for a board settings save, it did not.
        noteVocabularyChange(panelHead, typeof shape === "string" ? shape : "");
        adoptDisplay(settings);
      }
      // One save, sent as the whole configuration and against the head the page
      // read it at. The head is the statuses panel's, asked for rather than kept
      // twice, because one ledger has one tip.
      async function saveDisplaySettings() {
        if (configBusy() || !displaySettings || !displayFields) return;
        displayBusy = true;
        applyConfigBusy();
        const proposed = {
          // The name field shows the name as the board draws it, so a field
          // still reading that sends the stored name back exactly.
          name: storedUnlessEdited(displayFields.name.value, displaySettings.name).trim(),
          primaryColor: displayFields.primaryColor.value.trim(),
          textColor: displayFields.textColor.value.trim()
        };
        let answer = null;
        try {
          // Quiescence, for the reason a status change waits: a pending intent
          // was composed against this project's configuration, and a write that
          // moved the ledger tip out from under it would have that intent
          // refused as a stale write nothing was actually wrong with.
          if (pendingIntents.size > 0) {
            displayReport("", panelWaitingMessage);
            await settledIntents();
          }
          const response = await sendUnlessBusy("/api/display", {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(Object.assign({}, proposed, { expectedHead: panelHead }))
          });
          const payload = await response.json();
          answer = {
            applied: response.ok && payload.format === "workbook.display-mutation" &&
              payload.version === 1,
            payload
          };
        } catch (_) {
          // Only the exchange is guarded, so a defect in the rendering below
          // cannot be reported as a request that never went.
          answer = null;
        }
        displayBusy = false;
        if (!answer) {
          applyConfigBusy();
          displayReport("error", "These board settings could not be sent. Nothing about this project's configuration has changed.");
          return;
        }
        const payload = answer.payload;
        if (!answer.applied) {
          const stale = payload.error && payload.error.category === "stale-write";
          // A refusal that carries the settings is a stale write, and the
          // settings it carries are the current ones. Adopting them is the whole
          // of the recovery, exactly as it is for a status change — and the copy
          // names the configuration rather than the statuses, because what moved
          // may have been either.
          if (payload.display) adoptDisplayAtHead(payload.display, payload.shape);
          applyConfigBusy();
          displayReport("error", stale
            ? "This project's configuration changed in another clone, so these settings were not applied. This page now shows them as they stand; review them and save again."
            : payload.error && payload.error.message || "These board settings were not applied.");
          return;
        }
        adoptDisplayAtHead(payload.display, payload.shape);
        applyConfigBusy();
        displayReport("", describeDisplaySave(payload.display),
          (payload.warnings || []).map((warning) => warning.message));
      }
      // What a save says it did. It reports the settings as they now stand
      // rather than what changed, for the reason the statuses panel re-renders
      // wholesale: the server decides what is recorded, and a sentence composed
      // from what the form sent could describe a change the server declined to
      // make.
      function describeDisplaySave(settings) {
        const named = [];
        if (settings.name) named.push("named " + JSON.stringify(settings.name));
        if (settings.primaryColor) named.push("drawn in " + settings.primaryColor);
        if (settings.textColor) named.push("set in " + settings.textColor);
        return named.length === 0
          ? "This board has no settings of its own. Its colors are drawn as they now stand; reload the board to draw its name."
          : "This board is " + named.join(", ") + ". Its colors are drawn as they now stand; reload the board to draw its name.";
      }
      function renderDisplayPanel() {
        if (!displayPanel || !displayPanelBody) return;
        // Focus is caught and parked the way the statuses panel catches it: the
        // control the reader pressed is one of the nodes this drops.
        const held = document.activeElement;
        const inside = Boolean(held && displayPanel.contains(held));
        displayControls = [];
        displayFields = null;
        if (!displaySettings) {
          const message = document.createElement("p");
          message.className = "admin-note";
          message.dataset.displayPanelEmpty = "";
          text(message, "This project's board settings are not available.");
          displayPanelBody.replaceChildren(message);
          restoreDisplayFocus(inside);
          return;
        }
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.displayForm = "";
        form.addEventListener("submit", (event) => {
          event.preventDefault();
          saveDisplaySettings();
        });
        // Text inputs for the colors, each with a colour well beside it.
        // `#rrggbb` is what the ledger stores, what `workbook config set` takes
        // and what a brand's palette is written in — and a well has no empty
        // value, so the field stays the setting and an empty field stays a
        // colour taken back off the board. The well is the way in for a reader
        // without a hex code to paste: picking writes into the field, and the
        // save goes on reading the field alone. The placeholders say the shape
        // rather than naming this board's own blue, which is a literal the
        // stylesheet owns and the script must not keep a second copy of.
        displayFields = {
          name: displayInput("display-name", "name", shownField(displaySettings.name), boardDefaultName),
          primaryColor: displayInput("display-primary-color", "primaryColor", displaySettings.primaryColor, "#rrggbb"),
          textColor: displayInput("display-text-color", "textColor", displaySettings.textColor, "#rrggbb")
        };
        form.append(
          panelField("Project name", displayFields.name),
          panelField("Accent color", colorWellGroup(displayFields.primaryColor, "Pick an accent color", displayControls),
            displayFields.primaryColor),
          panelField("Text color", colorWellGroup(displayFields.textColor, "Pick a text color", displayControls),
            displayFields.textColor),
          panelSubmitButton("Save", "Save this project's board settings",
            { modifier: "admin-button--primary", controls: displayControls })
        );
        displayPanelBody.replaceChildren(form);
        applyDisplayBusy();
        restoreDisplayFocus(inside);
      }
      // One field of the settings form, marked with the member of the change it
      // carries so the save reads the form rather than three variables that
      // could fall out of step with it.
      function displayInput(id, member, value, placeholder) {
        const control = panelTextInput(id, value, placeholder, displayControls);
        control.dataset.displayField = member;
        return control;
      }
      // A color field and the well that fills it. The well opens the browser's
      // picker and only ever writes into the field, which stays the setting the
      // save reads; it follows the field when a complete color is typed, and
      // leaves an incomplete one alone rather than guessing at what is still
      // arriving. A field holding nothing leaves the well at the control's own
      // black default: the board's colors are the stylesheet's literals, and
      // the script keeps no copy of them.
      //
      // `controls` and `key` are the only things a caller outside the display
      // form has to supply: `controls` is the busy-list this well joins (the
      // display form passes its own), and `key` is what the well is named
      // after — the display form's fields already carry it, so its two
      // callers need not pass it.
      function colorWellGroup(field, description, controls = panelControls, key = field.dataset.displayField) {
        const group = document.createElement("div");
        group.className = "admin-color";
        const well = document.createElement("input");
        well.type = "color";
        well.className = "admin-color__well";
        well.dataset.displayWell = key;
        well.setAttribute("aria-label", description);
        well.value = "#000000";
        const follow = () => {
          const held = field.value.trim();
          if (/^#[0-9a-fA-F]{6}$/.test(held)) well.value = held.toLowerCase();
        };
        follow();
        well.addEventListener("input", () => { field.value = well.value; });
        field.addEventListener("input", follow);
        controls.push(well);
        group.append(field, well);
        return group;
      }
      function restoreDisplayFocus(inside) {
        if (!inside || !displayPanel || displayPanel.contains(document.activeElement)) return;
        if (typeof displayPanel.focus === "function") displayPanel.focus();
      }
      // Leaving the route takes the settings section with it, for the reason it
      // takes the statuses: the next visit reads them again rather than offering
      // a Save composed against values that may be minutes old.
      function leaveDisplaySection() {
        if (!displayPanel || displayPanel.hidden) return;
        displayPanel.hidden = true;
        displayPanel.remove();
        displaySettings = null;
        displayFields = null;
        displayPanelBody.replaceChildren();
        displayPanelStatus.replaceChildren();
        displayPanelStatus.dataset.kind = "";
      }
      // The configuration page, in the shell every other route is drawn in: a
      // header that names it and offers the way back to the board, and under it
      // the bodies the server rendered, each under a heading of its own.
      //
      // The headings are the page's, not the panels'. What the route holds is
      // whatever this board was built to administer — the project's statuses,
      // its priorities, the board's own settings — and a page whose h2 named one
      // of them would be a page that grew a section nobody announced.
      //
      // The bodies are moved rather than copied. Each is one element with one
      // identity for the length of the visit, which is what lets the list, the
      // live regions and the focus the rebuilds park on them survive a
      // re-render — and what a change that answers after the reader has left
      // writes into harmlessly, off the document, instead of into a second copy
      // of a page nobody is looking at.
      function configView() {
        const section = document.createElement("section");
        section.className = "task-route task-route--admin";
        section.setAttribute("aria-labelledby", "config-title");
        const header = document.createElement("header");
        header.className = "task-route__header";
        const naming = document.createElement("div");
        const eyebrow = document.createElement("p");
        eyebrow.className = "eyebrow";
        text(eyebrow, "Project configuration");
        const heading = document.createElement("h2");
        heading.id = "config-title";
        text(heading, "Configuration");
        naming.append(eyebrow, heading);
        // The way back, where a task's page keeps it: a link to the board, in
        // the same words and the same class, rather than a control that only
        // this page would have. It names the board the reader came from for the
        // reason a task's page does — this page is a detour, and a detour should
        // not cost them the board they were reading.
        const back = document.createElement("a");
        back.className = "board-link";
        back.href = boardAddress(deletedColumnShown, boardFilters);
        text(back, "Back");
        header.append(naming, back);
        section.append(header);
        // The statuses, under the heading the panel already points at with
        // aria-labelledby.
        section.append(sectionHeading("statuses-title", "Statuses"));
        vocabularyPanel.hidden = false;
        section.append(vocabularyPanel);
        // This project's priorities, on a board that was given the capabilities
        // to change them. A board without them is served no section and gets no
        // heading, rather than a heading over nothing.
        if (priorityPanel) {
          section.append(sectionHeading("priorities-title", "Priorities"));
          priorityPanel.hidden = false;
          section.append(priorityPanel);
        }
        // The board's own settings, on a board that was given the capability to
        // change them. A board without it is served no panel and gets no
        // heading, rather than a heading over nothing.
        if (displayPanel) {
          section.append(sectionHeading("display-title", "Board settings"));
          displayPanel.hidden = false;
          section.append(displayPanel);
        }
        // This project's keys, on a board that was given the capabilities to
        // change them. A board without them is served no section and gets no
        // heading, rather than a heading over nothing.
        if (keyPanel) {
          section.append(sectionHeading("keys-title", "Keys"));
          keyPanel.hidden = false;
          section.append(keyPanel);
        }
        // A change the reader left in flight is still in flight, and the render
        // below will draw its list with every control disabled. Leaving cleared
        // what the last visit had to say, so the reason is said again here
        // rather than leaving the reader in front of a page that refuses every
        // control and explains none of it.
        if (panelBusy) {
          panelReport("", pendingIntents.size > 0 ? panelWaitingMessage : panelSendingMessage);
        }
        if (priorityBusy) {
          priorityReport("", pendingIntents.size > 0 ? panelWaitingMessage : prioritySendingMessage);
        }
        if (displayBusy) displayReport("", displaySendingMessage);
        if (keyBusy) {
          keyReport("", pendingIntents.size > 0 ? panelWaitingMessage : keySendingMessage);
        }
        return section;
      }
      function sectionHeading(id, caption) {
        const heading = document.createElement("h3");
        heading.className = "admin__heading";
        heading.id = id;
        text(heading, caption);
        return heading;
      }
      // Leaving the configuration route. The bodies go back out of the document
      // with the render that replaces them, and what they were drawing goes with
      // them: the form the reader had open, and the statuses themselves, because
      // the next visit reads them again rather than drawing a list that may be
      // minutes old. A change composed against what this page happens to
      // remember is the stale write it would rather not have to report.
      //
      // A change still in flight is deliberately not cancelled. It finishes,
      // hands its head to noteVocabularyChange for the notice, and renders into
      // a body that is no longer in the document.
      function leaveConfigRoute() {
        leavePrioritySection();
        leaveDisplaySection();
        leaveKeySection();
        if (!vocabularyPanel || vocabularyPanel.hidden) return;
        vocabularyPanel.hidden = true;
        vocabularyPanel.remove();
        panelForm = null;
        panelVocabulary = null;
        // What the last visit had to say goes with it. A sentence about a change
        // the reader made, or a refusal they have already dealt with, would
        // otherwise be the first thing on the page the next time they walk here,
        // over a list read fresh from the server.
        vocabularyPanelBody.replaceChildren();
        vocabularyPanelStatus.replaceChildren();
        vocabularyPanelStatus.dataset.kind = "";
      }
