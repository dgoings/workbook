      // Status administration.
      //
      // A vocabulary change is the one write in this client that does not go
      // through the optimistic queue, and that is a decision rather than an
      // omission. The queue's bargain is that an intent can be drawn before it
      // is durable because a refusal costs one card a rollback. A refused
      // status change costs the whole board: the columns, the counts, the form
      // that offers the statuses, and the region holding every task whose
      // status suddenly matches no column. Repainting all of that to take back
      // a change is worse than the round trip the queue would have hidden. So
      // the page disables its controls, sends, and re-renders itself from the
      // answer.
      //
      // Two rules follow from being outside the queue rather than in it.
      //
      // The change waits for the queue to be empty. A pending intent can be
      // carrying the very status this change retires, and it was composed
      // against the columns the reader was looking at; sending the status change
      // past it would race a task write the reader has already made against a
      // vocabulary that no longer has somewhere to put it. The queue is not
      // emptied by walking to another route — an intent started on the board
      // outlives the navigation, as it must — so this is a page that can open
      // over a queue that is still draining. The control says it is waiting and
      // then proceeds, which is honest about a delay nobody would otherwise be
      // able to explain.
      //
      // A stale write is terminal. There is no re-base here and no retry: a
      // status is somebody's decision about how the project works, and two
      // people renaming the same column mean two different things. The refusal
      // carries the statuses as they now stand, so the page adopts them and
      // asks the reader to look again.
      const statusTagNames = vocabularyPanel
        ? (vocabularyPanel.dataset.statusTags || "").split(" ").filter(Boolean)
        : [];
      // The statuses the panel is drawing and the ledger head it read them at,
      // which is what every change it sends names. It is read from the server
      // rather than taken from the page: the page carries a token and a label
      // per column and nothing else, and its head may already be minutes old.
      let panelVocabulary = null;
      let panelHead = "";
      let panelBusy = false;
      let panelReading = false;
      // The row form the reader has open, as {status, kind}. It is held out here
      // rather than in the DOM because every answer rebuilds the list, and a
      // form that survived a change to a different status would be editing a
      // definition nobody is looking at any more.
      let panelForm = null;
      let panelDragStatus = null;
      // The controls a change disables while it is in flight, and the rows a
      // drag marks. Both are rebuilt with the list, because the list is what
      // holds them.
      let panelControls = [];
      let panelStatusRows = [];
      // What a change in flight says about itself: that it is behind the board's
      // own writes, or that it has gone and is waiting for an answer. One
      // constant each, because the render that draws this page has to be able to
      // say the same thing a change already said — a reader who leaves mid-change
      // and comes back is met by a list whose every control is disabled, and a
      // page that explained itself once and cannot explain itself again is a
      // dead grey page with nothing on it to read.
      const panelWaitingMessage = "Finishing the changes already in flight on the board before this one.";
      const panelSendingMessage = "Sending a change to this project's statuses.";
      // What the page has to say about the last thing it did: one sentence,
      // and one line per warning the server attached. Warnings are shown rather
      // than folded into the sentence — `docs-refresh-incomplete` and
      // `auto-sync-incomplete` are about the repository rather than about the
      // change, and they are the reason a later command behaves oddly.
      function reportInto(region, kind, message, warnings = []) {
        if (!region) return;
        region.dataset.kind = kind;
        const lines = [message].concat(warnings.filter(Boolean));
        region.replaceChildren(...lines.map((line, index) => {
          const paragraph = document.createElement("p");
          if (index > 0) paragraph.dataset.panelWarning = "";
          text(paragraph, line);
          return paragraph;
        }));
      }
      function panelReport(kind, message, warnings = []) {
        reportInto(vocabularyPanelStatus, kind, message, warnings);
      }
      // A control the panel disables for the length of a change, and one it
      // disables for a reason of its own — the Up control on the first row, a
      // Save with an empty name beside it. The two are separate states because
      // a change finishing must not enable a control that was never enabled.
      // Busy is the route's rather than the section's: every section of the
      // configuration route writes the same ref against the same head, so a
      // change in any of them disables the controls of all of them.
      function applyPanelBusy() {
        panelControls.forEach((control) => {
          control.disabled = configBusy() || control.dataset.blocked === "true";
        });
      }
      function blockPanelControl(control, blocked) {
        control.dataset.blocked = blocked ? "true" : "false";
        control.disabled = configBusy() || blocked;
      }
      // Settles when no task has work in flight.
      //
      // Each queue's drain loop publishes the promise it settles on, so this
      // waits on the loops rather than on a timer, and a queue that grows while
      // it waits is waited on again. A queue with no loop to wait on cannot be
      // waited for at all, so it returns rather than spinning.
      async function settledIntents() {
        while (pendingIntents.size > 0) {
          const draining = [...pendingIntents.values()]
            .map((entry) => entry.settled)
            .filter(Boolean);
          if (draining.length === 0) return;
          await Promise.all(draining);
        }
      }
      // Takes a vocabulary document as the panel's own, from a read or from any
      // answer that carries one.
      //
      // Wholesale, never patched: a status change can move a status nobody
      // named — a tag is exclusive, a removal retires a token, a rename
      // forwards one — so a client that folded a description of one change into
      // its own model would disagree with the server about the rest.
      //
      // The head it adopts is also the news the board needs. The columns the
      // reader left behind were built from an older one, and the standing notice
      // — which sits outside main and so is readable from this route as well as
      // from the board — is what says so. Nothing here rebuilds a column, and
      // the render that returns to the board re-attaches the very node it left,
      // so the reader comes back to the board they had and the offer to reload
      // it rather than to a repaint they did not ask for.
      function adoptVocabulary(vocabulary) {
        panelVocabulary = vocabulary;
        panelHead = typeof vocabulary.head === "string" ? vocabulary.head : "";
        noteVocabularyChange(panelHead, typeof vocabulary.shape === "string" ? vocabulary.shape : "");
        // The display settings ride in the same document, from the same commit,
        // which is why the board settings form never asks for them separately: a
        // second read could be answered from either side of a change and would
        // offer a Save composed against a configuration nobody was shown. A
        // document with no display member is a project that has configured none
        // of them, which is a real state and not a missing answer.
        adoptDisplay(vocabulary.display || { head: panelHead });
        renderVocabularyPanel();
        // The priorities ride in the same document, from the same commit, and
        // are adopted with it for the reason the display settings are. The
        // member is always present — a project that has configured none is
        // answered with the built-in three, because that is what the board has
        // to draw either way — so there is no "not configured" state to branch
        // on here, and nothing this panel does with what it reads may amount to
        // writing it back.
        renderPriorityPanel();
        // The keys ride in the same document too, from the same commit, and are
        // adopted with it for the reason the priorities are — with one thing
        // more riding on it. They are deliberately not part of the shape above,
        // so no board is asked to reload for a key change; this is therefore the
        // only way a key a teammate added reaches a page that is already open,
        // and it is what the create form's chooser reads.
        adoptKeys(vocabulary.keys);
        renderKeyPanel();
        // And the board behind this page is redrawn in the colors the document
        // carries. Every priority change moves this stylesheet, not only a
        // recolor: the color a priority with none of its own is drawn in is
        // derived from its position among its peers, so adding or removing one
        // restyles every uncolored priority on the board. The server composed
        // the whole block for the priorities this answer carries, which is why
        // the arithmetic behind that derivation lives in one place.
        redrawBoardStyle(boardPriorityInkStyle, vocabulary.priorities && vocabulary.priorities.ink);
      }
      async function readVocabulary() {
        panelReading = true;
        renderVocabularyPanel();
        renderPriorityPanel();
        renderKeyPanel();
        try {
          const response = await fetch("/api/vocabulary", { cache: "no-store" });
          const payload = await response.json();
          if (!response.ok || payload.format !== "workbook.vocabulary" ||
              payload.version !== 1 || !Array.isArray(payload.statuses)) {
            throw new Error(payload.error && payload.error.message ||
              "This project's statuses could not be read.");
          }
          panelReading = false;
          adoptVocabulary(payload);
        } catch (error) {
          panelReading = false;
          panelVocabulary = null;
          renderVocabularyPanel();
          renderPriorityPanel();
          renderKeyPanel();
          const said = error instanceof Error
            ? error.message
            : "This project's statuses could not be read.";
          panelReport("error", said);
          // Every section comes out of this one read, so each has to say that it
          // failed. A section left silent over an empty list is a section that
          // looks like a project with nothing in it.
          priorityReport("error", said);
          keyReport("error", said);
        }
      }
      // The two sections a vocabulary change can be sent from, and everything
      // about one that the other does not share.
      //
      // The protocol is one protocol: the statuses and the priorities are
      // sections of one ledger, written against one head, so a change to either
      // waits for the board to settle, greys every control on the page, and
      // adopts the whole document its answer carries. What is not shared is what
      // a section has to say for itself — its own live region, so a refused
      // priority change cannot blank a label somebody is typing into a status
      // row, its own busy flag, its own open form, and its own words, so a
      // reader is told which half of their configuration did not move.
      const statusChanges = {
        format: "workbook.vocabulary-mutation",
        report: (kind, message, warnings) => panelReport(kind, message, warnings),
        busy: (running) => { panelBusy = running; },
        closeForm: () => { panelForm = null; },
        unsent: "This status change could not be sent. Nothing about this project's statuses has changed.",
        stale: "This project's statuses changed in another clone, so this change was not applied. This page now shows them as they stand; review them and make the change again.",
        refused: "This status change was not applied."
      };
      const priorityChanges = {
        format: "workbook.priority-mutation",
        report: (kind, message, warnings) => priorityReport(kind, message, warnings),
        busy: (running) => { priorityBusy = running; },
        closeForm: () => { priorityForm = null; },
        unsent: "This priority change could not be sent. Nothing about this project's priorities has changed.",
        stale: "This project's priorities changed in another clone, so this change was not applied. This page now shows them as they stand; review them and make the change again.",
        refused: "This priority change was not applied."
      };
      // The keys are the third, and the only difference is that it has no form
      // to close: a key row holds no editor, because there is nothing about a
      // key to edit — a key is added and then only ever changes state — and the
      // add form is rebuilt by the render every answer triggers.
      const keyChanges = {
        format: "workbook.key-mutation",
        report: (kind, message, warnings) => keyReport(kind, message, warnings),
        busy: (running) => { keyBusy = running; },
        closeForm: () => {},
        unsent: "This key change could not be sent. Nothing about this project's keys has changed.",
        stale: "This project's keys changed in another clone, so this change was not applied. This page now shows them as they stand; review them and make the change again.",
        refused: "This key change was not applied."
      };
      // The one path every change either panel makes takes.
      //
      // `describe` is called with the answer rather than composed before the
      // send, because a removal's sentence prices the change in tasks and only
      // the answer knows that number.
      //
      // It hands back what happened — whether the change landed, the answer it
      // landed or was refused with, and the sentence it reported — because a
      // priority change can be the first half of a pair. A Save that renames a
      // priority and takes the default is two of these in order, and the second
      // is sent only if the first landed and has to be able to say so when it is
      // itself refused.
      async function changeVocabulary(method, path, body, describe, section = statusChanges) {
        if (configBusy() || !panelVocabulary) return null;
        section.busy(true);
        applyConfigBusy();
        let answer = null;
        try {
          // Quiescence. A pending intent may be carrying the very status this
          // change retires, so the change waits for the board to be settled and
          // says that it is waiting.
          if (pendingIntents.size > 0) {
            section.report("", panelWaitingMessage);
            await settledIntents();
          }
          const response = await fetch(path, {
            method,
            headers: { "Content-Type": "application/json" },
            // The head the panel read, exactly as it read it. An empty one is a
            // project whose configuration ledger has never been seeded, which
            // is a real state and an administrable one, so it is sent rather
            // than withheld.
            body: JSON.stringify(Object.assign({}, body, { expectedHead: panelHead }))
          });
          const payload = await response.json();
          answer = {
            applied: response.ok && payload.format === section.format &&
              payload.version === 1,
            payload
          };
        } catch (_) {
          // Only the exchange is guarded, so a defect in the rendering below
          // cannot be reported as a request that never went.
          answer = null;
        }
        section.busy(false);
        if (!answer) {
          applyConfigBusy();
          section.report("error", section.unsent);
          return { applied: false, payload: null, message: section.unsent };
        }
        const payload = answer.payload;
        if (!answer.applied) {
          const stale = payload.error && payload.error.category === "stale-write";
          // A refusal that carries the vocabulary is a stale write, and the
          // vocabulary it carries is the current one. Adopting it is the
          // whole of the recovery: no refetch, no re-base, no retry.
          if (payload.vocabulary) {
            section.closeForm();
            adoptVocabulary(payload.vocabulary);
          }
          applyConfigBusy();
          // A conflict is described rather than quoted, as it is everywhere
          // else in this client: the server's sentence names the head the
          // request carried, which is this client's bookkeeping. Every other
          // refusal is the reader's own to read — an unknown tag, a name that
          // is already taken, a removal with nowhere to send its tasks — so it
          // is quoted exactly as the command would have printed it.
          const message = stale
            ? section.stale
            : payload.error && payload.error.message || section.refused;
          section.report("error", message);
          return { applied: false, payload, message };
        }
        section.closeForm();
        adoptVocabulary(payload.vocabulary);
        applyConfigBusy();
        const said = describe(payload);
        section.report("", said, (payload.warnings || []).map((warning) => warning.message));
        return { applied: true, payload, message: said };
      }
      // `controls` is the busy-list the control joins, which is the statuses
      // panel's unless a caller says otherwise — the same parameter
      // panelSubmitButton and panelTextInput already take, for the same reason:
      // each section's list is rebuilt with that section's own render, and a
      // control on the list of a section that is re-rendering without it would
      // be left enabled through the next change.
      function panelButton(caption, description, onClick, { modifier = "", blocked = false, controls = panelControls } = {}) {
        const control = document.createElement("button");
        control.type = "button";
        control.className = modifier ? "admin-button " + modifier : "admin-button";
        text(control, caption);
        if (description) control.setAttribute("aria-label", description);
        control.addEventListener("click", onClick);
        controls.push(control);
        blockPanelControl(control, blocked);
        return control;
      }
      // The control that sends a panel form. It carries no click handler of its
      // own: the form's submit does the work, so pressing Enter in a field and
      // pressing the button are the same gesture rather than two paths that
      // could drift, and a browser that fires both would not send twice.
      //
      // `controls` is the list the control joins, which is the statuses panel's
      // unless a caller says otherwise. The board settings section keeps its own
      // list because its form is rebuilt on its own schedule; both lists are
      // disabled together, which is what configBusy above is for.
      function panelSubmitButton(caption, description, { modifier = "", blocked = false, controls = panelControls } = {}) {
        const control = document.createElement("button");
        control.type = "submit";
        control.className = modifier ? "admin-button " + modifier : "admin-button";
        text(control, caption);
        if (description) control.setAttribute("aria-label", description);
        controls.push(control);
        blockPanelControl(control, blocked);
        return control;
      }
      function panelTextInput(id, value, placeholder, controls = panelControls) {
        const control = document.createElement("input");
        control.id = id;
        control.value = value || "";
        if (placeholder) control.placeholder = placeholder;
        controls.push(control);
        return control;
      }
      // `chosen` is the option a caller wants standing when the control is
      // drawn, for a list whose sensible default is not the one it reads first.
      // A placement list runs in the order the members are drawn, so its default
      // is the bottom of it rather than the top, and the alternative — ordering
      // the list to put the default first — would make the control disagree with
      // the list it is about.
      function panelSelect(id, options, controls = panelControls, chosen = null) {
        const control = document.createElement("select");
        control.id = id;
        options.forEach(([value, label], index) => {
          const option = document.createElement("option");
          option.value = value;
          text(option, label);
          option.selected = chosen === null ? index === 0 : value === chosen;
          control.append(option);
        });
        controls.push(control);
        return control;
      }
      // `labelled` is the control the caption points at, for the field whose
      // control is a group holding the real input rather than the input itself.
      function panelField(labelText, control, labelled = control) {
        const wrapper = document.createElement("div");
        wrapper.className = "admin-field";
        const caption = document.createElement("label");
        caption.htmlFor = labelled.id;
        text(caption, labelText);
        wrapper.append(caption, control);
        return wrapper;
      }
      // The tag checkboxes, built from the roles the server rendered into the
      // panel rather than from a list the script keeps. Nothing here enforces
      // what a tag means or how many statuses may carry one: the vocabulary
      // does, in the same words `workbook status` uses, and a client that
      // refused first would refuse in words of its own.
      function panelTagChoices(prefix, tags) {
        const region = document.createElement("fieldset");
        region.className = "admin-tags";
        const caption = document.createElement("legend");
        text(caption, "Tags");
        region.append(caption);
        const boxes = statusTagNames.map((tag) => {
          const choice = document.createElement("label");
          choice.className = "admin-tag-choice";
          const box = document.createElement("input");
          box.type = "checkbox";
          box.id = prefix + "-tag-" + tag;
          box.dataset.statusTag = tag;
          box.checked = (tags || []).includes(tag);
          const name = document.createElement("span");
          text(name, tag);
          choice.append(box, name);
          region.append(choice);
          panelControls.push(box);
          return box;
        });
        return {
          region,
          values: () => boxes.filter((box) => box.checked).map((box) => box.dataset.statusTag)
        };
      }
      // What a vocabulary document calls a status, falling back to the token
      // for one it does not carry — which is what a removal's destination is
      // once the answer has taken it out.
      function statusLabelIn(vocabulary, status) {
        const definition = (vocabulary && vocabulary.statuses || [])
          .find((candidate) => candidate.status === status);
        return definition ? definition.label : status;
      }
      // Whether a form's tags differ from the ones the status carries. Both are
      // in the canonical order — the document stores them sorted and the
      // checkboxes are built in the order the server rendered them — so the two
      // are comparable as they stand.
      function sameTags(chosen, held) {
        return chosen.join(" ") === (held || []).join(" ");
      }
      function statusEditForm(definition) {
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.vocabularyEdit = definition.status;
        const name = panelTextInput("status-name-" + definition.status, definition.status);
        const label = panelTextInput("status-label-" + definition.status, definition.label);
        const tags = panelTagChoices("status-" + definition.status, definition.tags);
        const save = panelSubmitButton("Save", "Save changes to " + definition.label,
          { modifier: "admin-button--primary" });
        const guard = () => blockPanelControl(save, !name.value.trim());
        name.addEventListener("input", guard);
        guard();
        function submit() {
          // Only the members this form changed, because a member is sent to be
          // set: re-asserting a label nobody edited is indistinguishable from
          // editing it, and a rename that carried the current label with it
          // would overwrite a relabel that landed in between. A form that
          // changed nothing is finished rather than broken, so it says so
          // instead of sending a change with nothing in it for the server to
          // refuse.
          const change = {};
          const renamed = name.value.trim();
          if (renamed !== definition.status) change.name = renamed;
          if (label.value !== definition.label) change.label = label.value;
          const chosen = tags.values();
          if (!sameTags(chosen, definition.tags)) change.tags = chosen;
          if (Object.keys(change).length === 0) {
            panelReport("", "Nothing about " + definition.label + " was changed.");
            return;
          }
          changeVocabulary(
            "PATCH",
            "/api/vocabulary/statuses/" + encodeURIComponent(definition.status),
            change,
            // Named as it now stands rather than as it stood: a rename and a
            // relabel are one change here, so reporting the label this form
            // opened with would tell a reader who renamed Blocked to "Waiting
            // On" that Blocked was updated — the one name the project no longer
            // has. The status is looked up under the name the change gave it,
            // which is the old one when the change did not give it a new one.
            (payload) => "Updated " + statusLabelIn(payload.vocabulary, change.name || definition.status) + "."
          );
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        form.append(
          panelField("Name", name),
          panelField("Label", label),
          tags.region,
          save,
          panelButton("Cancel", "Stop editing " + definition.label, closePanelForm)
        );
        return form;
      }
      function statusDeleteForm(definition, definitions) {
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.vocabularyDelete = definition.status;
        const elsewhere = definitions.filter((candidate) => candidate.status !== definition.status);
        // Where the tasks in this column belong. It is required and never
        // guessed, exactly as `workbook status delete --into` is, so a project
        // with one status has no removal to offer rather than one that would
        // strand everything in it.
        const into = panelSelect(
          "status-into-" + definition.status,
          elsewhere.map((candidate) => [candidate.status, candidate.label])
        );
        function submit() {
          if (!into.value) return;
          changeVocabulary(
            "DELETE",
            "/api/vocabulary/statuses/" + encodeURIComponent(definition.status),
            { into: into.value },
            (answer) => removalSummary(definition, into.value, answer.tasks)
          );
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        const remove = panelSubmitButton("Remove", "Remove " + definition.label,
          { modifier: "admin-button--danger", blocked: elsewhere.length === 0 });
        if (elsewhere.length === 0) {
          const note = document.createElement("p");
          note.className = "admin-note";
          text(note, "This is the only status this project has, so there is nowhere for its tasks to go.");
          form.append(note);
        } else {
          form.append(panelField("Move its tasks to", into));
        }
        form.append(remove, panelButton("Cancel", "Keep " + definition.label, closePanelForm));
        return form;
      }
      // What a removal cost, in the terms the person removing a column cares
      // about and in the same terms `workbook status delete` reports: how many
      // tasks moved, and how many of those `workbook next` can claim where they
      // landed.
      function removalSummary(definition, into, counts) {
        const name = statusLabelIn(panelVocabulary, into);
        const affected = counts && counts.affected || 0;
        if (affected === 0) return "Removed " + definition.label + ". No tasks were in it.";
        const claimable = counts && counts.claimableAfter || 0;
        return "Removed " + definition.label + ". " +
          affected + (affected === 1 ? " task moved to " : " tasks moved to ") + name +
          ", " + claimable + " of them claimable by workbook next there.";
      }
      function addStatusForm(definitions) {
        const region = document.createElement("div");
        region.className = "admin-add";
        const heading = document.createElement("h3");
        heading.className = "admin-add__title";
        text(heading, "Add a status");
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.vocabularyAdd = "";
        const name = panelTextInput("status-new-name", "", "triage");
        const label = panelTextInput("status-new-label", "", "derived from the name");
        const tags = panelTagChoices("status-new", []);
        // One control for the placement, because a placement is one decision,
        // and the server refuses a change naming both neighbours.
        //
        // One option per position rather than one per status, and every option
        // names a column. A list of n statuses has n+1 positions, and naming
        // both neighbours of every one of them spells most of those twice:
        // "Before X" is the position "After the one before X" already names. So
        // the first column is the only status something is offered before, and
        // everything else is an "After".
        //
        // The end of the list is "After <the last column>" rather than a
        // standing "At the end", for the reason the priorities panel gives: an
        // entry shaped unlike the rest reads as a member's name to somebody
        // whose project happens to use those words.
        //
        // The list runs in board order and the end is the default, because that
        // is where a column somebody has not thought about the position of
        // belongs.
        const placements = (definitions.length
          ? [["before:" + definitions[0].status, "Before " + definitions[0].label]]
          : []
        ).concat(definitions.map((candidate) =>
          ["after:" + candidate.status, "After " + candidate.label]));
        const placement = panelSelect("status-new-placement", placements, panelControls,
          definitions.length ? "after:" + definitions[definitions.length - 1].status : null);
        const add = panelSubmitButton("Add status", "",
          { modifier: "admin-button--primary", blocked: true });
        const guard = () => blockPanelControl(add, !name.value.trim());
        name.addEventListener("input", guard);
        function submit() {
          const status = name.value.trim();
          if (!status) return;
          const addition = { status };
          // An omitted label is the server deriving one from the name, which is
          // what `workbook status add` does without --label. An empty string
          // would be a blank label, which the vocabulary refuses.
          if (label.value.trim()) addition.label = label.value.trim();
          const chosen = tags.values();
          if (chosen.length) addition.tags = chosen;
          const [side, neighbour] = placement.value ? placement.value.split(":") : ["", ""];
          if (neighbour) addition[side] = neighbour;
          changeVocabulary("POST", "/api/vocabulary/statuses", addition,
            () => "Added " + status + ".");
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        form.append(
          panelField("Name", name),
          panelField("Label", label),
          tags.region,
          panelField("Position", placement),
          add
        );
        region.append(heading, form);
        return region;
      }
      function closePanelForm() {
        panelForm = null;
        renderVocabularyPanel();
      }
      function openPanelForm(status, kind) {
        panelForm = panelForm && panelForm.status === status && panelForm.kind === kind
          ? null
          : { status, kind };
        renderVocabularyPanel();
      }
      // Where this drag may be dropped on the status list, or null if it may not
      // be dropped here. Asked by dragenter, dragover and drop alike.
      //
      // A browser decides whether to deliver a drop at all from whichever of
      // dragenter and dragover it dispatched last, and Blink dispatches
      // dragenter rather than dragover whenever the hit-test target under the
      // cursor changes — which is what any content moving under a stationary
      // cursor causes. The board's autoscroll made exactly that happen and lost
      // drops by it, because the board answered only dragover.
      //
      // Nothing moves under the cursor on this panel today, so this is a class
      // being closed rather than a bug being fixed. What keeps it closed is
      // having one rule instead of two that can drift apart: a later change that
      // animates a row, scrolls this list, or reflows it under a held cursor
      // will find the acceptance already in the right place, rather than
      // discovering the hard way that dragover alone was never enough.
      //
      // Every row is a drop target. The rows that are not droppable are the ones
      // this returns null for, and they are the rows a handler asking the easier
      // question — "is there a row here?" — would wrongly accept.
      function panelDropDestination(event, row, status) {
        // A reader dropping a file here is not reordering anything, and the
        // panel must neither take it nor swallow it: the flag that says "I will
        // take this" is the same flag that stops the drop reaching whatever
        // else on the page wants it. Same question the board's own handlers
        // ask, through the same function.
        if (draggingFiles(event)) return null;
        // No panel drag is live: this is somebody else's gesture passing over,
        // a board card or a drag from another window, and the list is not its
        // business.
        if (!panelDragStatus) return null;
        // The row being dragged has nowhere to arrive: dropping a column on
        // itself is not a change anyone asked for.
        if (panelDragStatus === status) return null;
        return row;
      }
      function statusRow(definition, index, definitions) {
        const item = document.createElement("li");
        item.className = "admin-status";
        item.dataset.vocabularyStatus = definition.status;
        const editing = Boolean(panelForm && panelForm.status === definition.status);
        // A row with a form open is not draggable. Selecting the text in an
        // input is a press and a drag, and inside a draggable ancestor that is
        // the gesture that starts the drag; a reader correcting a label would
        // find themselves reordering the columns instead. The row is still
        // reorderable while its form is open — by the Up and Down controls,
        // which is the path a keyboard has always had to take.
        item.draggable = !editing;
        const name = document.createElement("div");
        name.className = "admin-status__name";
        const label = document.createElement("span");
        text(label, definition.label);
        const token = document.createElement("code");
        token.className = "admin-status__token";
        text(token, definition.status);
        name.append(label, token);
        (definition.tags || []).forEach((tag) => {
          const chip = document.createElement("span");
          chip.className = "admin-tag";
          chip.dataset.statusTag = tag;
          text(chip, tag);
          name.append(chip);
        });
        const controls = document.createElement("div");
        controls.className = "admin-status__controls";
        // Reordering has a keyboard path as well as a drag, and both send the
        // same one request: the whole order, once per gesture. A drag is not an
        // input everybody has, and a column order nobody can change without a
        // mouse is a column order half the readers of this board cannot change.
        controls.append(
          panelButton("Up", "Move " + definition.label + " earlier", () => moveStatus(definition.status, -1),
            { blocked: index === 0 }),
          panelButton("Down", "Move " + definition.label + " later", () => moveStatus(definition.status, 1),
            { blocked: index === definitions.length - 1 }),
          panelButton("Edit", "Edit " + definition.label, () => openPanelForm(definition.status, "edit")),
          panelButton("Delete", "Delete " + definition.label, () => openPanelForm(definition.status, "delete"))
        );
        item.append(name, controls);
        if (editing) {
          item.append(panelForm.kind === "delete"
            ? statusDeleteForm(definition, definitions)
            : statusEditForm(definition));
        }
        item.addEventListener("dragstart", (event) => {
          if (panelBusy || editing) return;
          panelDragStatus = definition.status;
          item.dataset.dragging = "true";
          if (event.dataTransfer) {
            event.dataTransfer.effectAllowed = "move";
            event.dataTransfer.setData("text/plain", definition.status);
          }
        });
        item.addEventListener("dragend", () => {
          panelDragStatus = null;
          clearPanelDropMarks();
          delete item.dataset.dragging;
        });
        // Both events, one rule. dragenter is the one that was missing, and it
        // is the one Blink reads when the target under the cursor churns.
        const answerPanelDrag = (event) => {
          if (!panelDropDestination(event, item, definition.status)) return;
          event.preventDefault();
          if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
          // Only when it is not already the marked row. Rewriting the same
          // attributes on every event is a mutation under a drag cursor for no
          // change, and mutations under a drag cursor are what make a browser
          // re-run its hit test and dispatch the churn this is about.
          if (item.dataset.dropTarget !== "true") {
            clearPanelDropMarks();
            item.dataset.dropTarget = "true";
          }
        };
        item.addEventListener("dragenter", answerPanelDrag);
        item.addEventListener("dragover", answerPanelDrag);
        // A leave that is not a departure leaves the mark alone.
        //
        // A browser pairs a dragleave with every dragenter it sends: the board's
        // own measurement in this file is ten dragenters, ten dragleaves and no
        // dragover at all. A bare handler here would erase, on each of those
        // leaves, the mark the matching dragenter had just drawn — so under the
        // churn this whole change is about, the reader would still be shown
        // nothing about where the row is going to land.
        //
        // It does not take churn, either. A row is a name, some chips and four
        // buttons, so a cursor crossing any of them fires dragenter on the
        // child and dragleave on the row, and the mark would blink out with the
        // cursor still sitting inside the row it names.
        //
        // Two questions, because it takes both to cover the browsers.
        // relatedTarget names what the cursor moved onto and settles it on
        // Chrome; Firefox and Safari send none on a dragleave, and there the
        // cursor's own coordinates decide, which every browser reports. What is
        // left — a cursor gone somewhere else entirely — is the departure, and
        // that still clears the mark.
        item.addEventListener("dragleave", (event) => {
          if (item.contains(event.relatedTarget)) return;
          if (cursorWithin(item, event)) return;
          delete item.dataset.dropTarget;
        });
        item.addEventListener("drop", (event) => {
          // The same rule again rather than a second reading of it. A drop this
          // panel refuses is left alone entirely — not marked, not prevented,
          // and above all not turned into a reorder, which is what a file
          // dropped here over a stale drag would otherwise become.
          if (!panelDropDestination(event, item, definition.status)) return;
          event.preventDefault();
          clearPanelDropMarks();
          const dragged = panelDragStatus;
          panelDragStatus = null;
          return moveStatusOnto(dragged, definition.status);
        });
        return item;
      }
      function clearPanelDropMarks() {
        panelStatusRows.forEach((row) => { delete row.dataset.dropTarget; });
      }
      // The order this panel is drawing, which is the order a reorder sends: the
      // whole list, once per gesture, because the server takes the whole order
      // and a sequence of pairwise moves would be several commits describing one
      // decision.
      function panelOrder() {
        return (panelVocabulary && panelVocabulary.statuses || []).map((definition) => definition.status);
      }
      function sendPanelOrder(statuses) {
        return changeVocabulary("PUT", "/api/vocabulary/order", { statuses },
          () => "Reordered this project's columns.");
      }
      function moveStatus(status, offset) {
        const order = panelOrder();
        const from = order.indexOf(status);
        const to = from + offset;
        if (from < 0 || to < 0 || to >= order.length) return;
        order.splice(to, 0, ...order.splice(from, 1));
        return sendPanelOrder(order);
      }
      // A row dropped on another takes that row's place: downward it lands
      // after the row it was dropped on, upward it lands before it, which is
      // what the two gestures look like they are asking for.
      function moveStatusOnto(dragged, target) {
        const order = panelOrder();
        const from = order.indexOf(dragged);
        const to = order.indexOf(target);
        if (from < 0 || to < 0 || from === to) return;
        order.splice(from, 1);
        order.splice(to, 0, dragged);
        return sendPanelOrder(order);
      }
      function renderVocabularyPanel() {
        if (!vocabularyPanel || !vocabularyPanelBody) return;
        // A rebuild drops every control in the panel, and the one the reader
        // pressed is usually among them. Focus is caught and parked on the panel
        // itself — but only when it was inside the panel to begin with, since a
        // reader who has moved on is not to be dragged back.
        const held = document.activeElement;
        const inside = Boolean(held && vocabularyPanel.contains(held));
        panelControls = [];
        panelStatusRows = [];
        // The row a drag started on is one of the nodes about to be dropped, and
        // a drag whose row no longer exists is not a drag any more. Nothing
        // reaches this today — a change disables the rows before it answers, and
        // dragstart refuses while one is in flight — but a drop honoured against
        // an order the reader never saw is not a failure worth leaving a route
        // to.
        panelDragStatus = null;
        if (!panelVocabulary) {
          const message = document.createElement("p");
          message.className = "admin-note";
          message.dataset.vocabularyPanelEmpty = "";
          text(message, panelReading
            ? "Reading this project's statuses."
            : "This project's statuses are not available.");
          vocabularyPanelBody.replaceChildren(message);
          restorePanelFocus(inside);
          return;
        }
        const definitions = panelVocabulary.statuses || [];
        const list = document.createElement("ul");
        list.className = "admin-statuses";
        list.setAttribute("role", "list");
        definitions.forEach((definition, index) => {
          const row = statusRow(definition, index, definitions);
          panelStatusRows.push(row);
          list.append(row);
        });
        vocabularyPanelBody.replaceChildren(list, addStatusForm(definitions));
        applyPanelBusy();
        restorePanelFocus(inside);
      }
      function restorePanelFocus(inside) {
        if (!inside || !vocabularyPanel || vocabularyPanel.contains(document.activeElement)) return;
        if (typeof vocabularyPanel.focus === "function") vocabularyPanel.focus();
      }
