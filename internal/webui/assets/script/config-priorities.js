      // This project's priorities: the statuses panel's sibling, built out of the
      // same parts and differing where a priority differs from a status.
      //
      // Three differences, and every one of them shows on the page.
      //
      // ONE ROLE, GIVEN RATHER THAN HELD. A status carries up to three tags with
      // lifecycle meaning; a priority carries `default`, exactly one does, and
      // the only operation is handing it to another priority — there is no
      // clearing counterpart, because a project with no default is a project
      // where a new task has nowhere to land. So the row that holds it is drawn
      // holding it with nothing to offer, and the constraint is rendered rather
      // than enforced: which priority is the default is the server's derivation,
      // read off the document, and a client that decided it could disagree.
      //
      // ONE OPERATION PER REQUEST. A status form is one PATCH; each of these
      // changes is its own route against its own head. A Save that renames a
      // priority AND takes the default is two requests, in order, the second
      // against the head the first answered with and addressed at the NEW name.
      // It can half-land, and then the rename stands and the role did not move.
      // There is no compensating write and this must not invent one: a board
      // undoing a change nobody asked it to undo is worse than a board that says
      // what happened.
      //
      // AND WHAT IS READ IS NOT WHAT MAY BE WRITTEN. The document carries the
      // EFFECTIVE priorities: a project that has configured none is answered
      // with the built-in three, because that is what the board must draw either
      // way, and nothing in the answer says which it is looking at. So this
      // section never sends back what it read. Every request carries one
      // operation a reader actually performed — the way the board settings form
      // sends only the fields that moved — because the first priority write on a
      // project records the built-in three as a decision it never made and
      // stamps the compatibility marker that parks every teammate on an older
      // build, in exchange for changing nothing.
      //
      // The role's name comes from the server, as the status tags' do and for
      // the same two reasons: the set belongs to the vocabulary, and a client
      // that spelled it would be a client naming a word that is also a status
      // tag. There is one today; a second would need a route of its own before
      // this section could offer it, so the first is the role this draws.
      const priorityTagNames = priorityPanel
        ? (priorityPanel.dataset.priorityTags || "").split(" ").filter(Boolean)
        : [];
      const priorityRoleName = priorityTagNames.length ? priorityTagNames[0] : "";
      let priorityForm = null;
      let priorityBusy = false;
      let priorityControls = [];
      // The priority a drag is carrying, and the rows that can be marked while
      // it is in flight. Its own pair rather than the statuses' — the two lists
      // are drawn from different vocabularies and reorder through different
      // routes, and keeping the state apart is what makes each list refuse the
      // other's gesture: with a column in flight this one has nothing of its
      // own being dragged, and answers no.
      let panelDragPriority = null;
      let panelPriorityRows = [];
      const prioritySendingMessage = "Sending a change to this project's priorities.";
      function priorityReport(kind, message, warnings = []) {
        reportInto(priorityPanelStatus, kind, message, warnings);
      }
      function applyPriorityBusy() {
        priorityControls.forEach((control) => {
          control.disabled = configBusy() || control.dataset.blocked === "true";
        });
      }
      // The priorities this section is drawing, and the one the server says is
      // the default. Both are read off the document rather than derived: the
      // order is the server's and the role is a rule, and a client that
      // re-derived either could disagree with the board about where a new task
      // lands.
      function panelPriorityList() {
        const priorities = panelVocabulary && panelVocabulary.priorities;
        return priorities && Array.isArray(priorities.priorities) ? priorities.priorities : [];
      }
      function priorityDefault() {
        const priorities = panelVocabulary && panelVocabulary.priorities;
        return priorities && priorities.default || "";
      }
      function priorityPath(priority) {
        return "/api/vocabulary/priorities/" + encodeURIComponent(priority);
      }
      // What a vocabulary document calls a priority, falling back to the token
      // for one it does not carry — which is what a removal's destination is once
      // the answer has taken it out, and what a renamed priority is under the
      // name it no longer has.
      function priorityLabelIn(vocabulary, priority) {
        const priorities = vocabulary && vocabulary.priorities;
        const definition = (priorities && priorities.priorities || [])
          .find((candidate) => candidate.priority === priority);
        return definition ? definition.label : priority;
      }
      // The one role a priority has, drawn as the list-wide constraint it is.
      //
      // The priority that holds it is shown holding it and is given nothing to
      // press: there is no operation that clears the role, so a control offering
      // to would be offering a request this client cannot make. That is a
      // control with nothing to do, which is what the Up on the first row
      // already is — not the client refusing a change in words of its own, which
      // is what the vocabulary is for.
      function priorityDefaultChoice(definition, holds) {
        const region = document.createElement("fieldset");
        region.className = "admin-tags";
        const caption = document.createElement("legend");
        text(caption, "Role");
        region.append(caption);
        const choice = document.createElement("label");
        choice.className = "admin-tag-choice";
        const box = document.createElement("input");
        box.type = "checkbox";
        box.id = "priority-" + definition.priority + "-default";
        box.dataset.priorityDefault = definition.priority;
        box.checked = holds;
        const name = document.createElement("span");
        text(name, priorityRoleName);
        choice.append(box, name);
        region.append(choice);
        priorityControls.push(box);
        blockPanelControl(box, holds);
        const hint = document.createElement("p");
        hint.className = "admin-hint";
        text(hint, holds
          ? "This is where a task created without a priority lands. Exactly one priority carries the role, and it is given rather than taken: make another priority the default to move it."
          : "Where a task created without a priority lands. Giving it here takes it off the priority that holds it now.");
        region.append(hint);
        return { region, taken: () => box.checked && !holds };
      }
      // A row's edit form: the name, the label, the color and the role.
      //
      // The color is a text field with a well beside it, through the same
      // colorWellGroup the board settings form's two colors use. The field is
      // the change and the well is only a way into it: `#rrggbb` is what the
      // ledger stores and what `workbook priority color` takes, and an empty
      // field is a color taken back off the priority — which a well, having no
      // empty value, could never say.
      function priorityEditForm(definition) {
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.priorityEdit = definition.priority;
        const name = panelTextInput("priority-name-" + definition.priority, definition.priority, "", priorityControls);
        const label = panelTextInput("priority-label-" + definition.priority, definition.label, "", priorityControls);
        // A priority with no stored color is drawn in one the board derives from
        // its position, and there is no stored default to put in the field: the
        // empty field is the state, and the placeholder says the shape rather
        // than naming a color this script would then be keeping a copy of.
        const colorID = "priority-color-" + definition.priority;
        const color = panelTextInput(colorID, definition.color || "", "#rrggbb", priorityControls);
        const colorWell = colorWellGroup(color, "Pick a color for " + definition.label,
          priorityControls, colorID);
        const holds = definition.priority === priorityDefault();
        const role = priorityDefaultChoice(definition, holds);
        const save = panelSubmitButton("Save", "Save changes to " + definition.label,
          { modifier: "admin-button--primary", controls: priorityControls });
        const guard = () => blockPanelControl(save, !name.value.trim());
        name.addEventListener("input", guard);
        guard();
        async function submit() {
          const renamed = name.value.trim();
          if (!renamed) return;
          // Only what this form actually changed, for the reason a status form
          // sends only what it changed and for one more: an edit that sets
          // nothing is refused, deliberately, because the first priority write
          // on a project costs it the compatibility marker — so a Save nobody
          // edited must not become one.
          const change = {};
          if (renamed !== definition.priority) change.name = renamed;
          if (label.value !== definition.label) change.label = label.value;
          const takes = role.taken();
          const edited = Object.keys(change).length > 0;
          // The color as it was typed, and the question of whether it is a
          // change asked of the trimmed one.
          //
          // Two decisions sit in that sentence. What is *sent* is untouched,
          // because what a color is belongs to the verb family that validates
          // it: this panel neither trims nor lowercases, so `#B42318` over a
          // stored `#b42318` goes, and the writer — which does lowercase before
          // it compares — answers that the priority already has that color, in
          // its own words.
          //
          // What is *compared* is trimmed, which settles the one case the route
          // leaves open: a field holding nothing but spaces. The writer trims
          // before it reads, so such a field already clears the color and
          // answers 200. Refusing it here would be this client refusing in words
          // of its own, which is the thing this section does not do — so a blank
          // field is the clearing it looks like. It cannot clear by accident:
          // the comparison is against what is stored, so spaces in the empty
          // field of a priority with no color are no change and send nothing,
          // and a clearing is only ever offered to a reader looking at a color
          // they set, which typing it back undoes.
          const recolored = color.value.trim() !== (definition.color || "");
          if (!edited && !takes && !recolored) {
            priorityReport("", "Nothing about " + definition.label + " was changed.");
            return;
          }
          // One operation per request, in order — there is no route that does
          // two, and there should not be one. What makes them one gesture is
          // that each is composed against the head the one before it answered
          // with, which is the head this page now holds because adopting that
          // answer is what took it.
          //
          // The color goes first, and that is not arbitrary. It is the only
          // value in this form whose *shape* the writer checks, so it is the
          // only one a reader can get wrong without knowing. Sent last, a
          // mistyped hex code would leave the rename recorded and the Save
          // refused — a half-landed change, with no compensating write, over a
          // typo. Sent first, it costs a second press and nothing else.
          //
          // Everything after it is addressed at the name the rename gives,
          // because the old one answers 404 once it is forwarded: a rename
          // forwards a name so that tasks filed under it still resolve, and a
          // forwarded name does not resolve as an address.
          const named = change.name || definition.priority;
          const steps = [];
          if (recolored) {
            steps.push({
              path: priorityPath(definition.priority) + "/color",
              body: { color: color.value },
              describe: (payload) => {
                const drawn = priorityLabelIn(payload.vocabulary, definition.priority);
                return color.value.trim()
                  ? "Recolored " + drawn + "."
                  : "Cleared the color on " + drawn + "; the board draws it in the color its position derives.";
              }
            });
          }
          if (edited) {
            steps.push({
              path: priorityPath(definition.priority),
              body: change,
              describe: (payload) => "Updated " + priorityLabelIn(payload.vocabulary, named) + "."
            });
          }
          if (takes) {
            steps.push({
              path: priorityPath(named) + "/default",
              body: {},
              describe: () => "New tasks now land at " + priorityLabelIn(panelVocabulary, named) + "."
            });
          }
          // What has already been recorded, carried forward so a refusal can be
          // reported beside it. Nothing here undoes a step that landed — there
          // is no write that would, and a board authoring a change nobody asked
          // for is worse than one that says what happened — so a half-landed
          // Save reports both halves and stops. The row has already been redrawn
          // from the priorities as they now stand, so re-opening its form offers
          // the operations that did not land and not the ones that did, which
          // would only be refused a second time.
          let landed = "";
          for (const step of steps) {
            const answer = await changeVocabulary("PATCH", step.path, step.body,
              (payload) => {
                const said = step.describe(payload);
                return landed ? landed + " " + said : said;
              },
              priorityChanges);
            if (!answer || !answer.applied) {
              if (answer && landed) priorityReport("error", landed + " " + answer.message);
              return;
            }
            landed = answer.message;
          }
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        form.append(
          panelField("Name", name),
          panelField("Label", label),
          panelField("Color", colorWell, color),
          role.region,
          save,
          panelButton("Cancel", "Stop editing " + definition.label, closePriorityForm,
            { controls: priorityControls })
        );
        return form;
      }
      function priorityDeleteForm(definition, definitions) {
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.priorityDelete = definition.priority;
        const elsewhere = definitions.filter((candidate) => candidate.priority !== definition.priority);
        // Where the tasks filed at this priority belong. It is required and never
        // guessed, exactly as `workbook priority delete --into` is, so a project
        // with one priority has no removal to offer rather than one that would
        // strand every task at a priority nothing resolves.
        const into = panelSelect(
          "priority-into-" + definition.priority,
          elsewhere.map((candidate) => [candidate.priority, candidate.label]),
          priorityControls
        );
        function submit() {
          if (!into.value) return;
          changeVocabulary("DELETE", priorityPath(definition.priority), { into: into.value },
            (answer) => priorityRemovalSummary(definition, into.value, answer.tasks),
            priorityChanges);
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        const remove = panelSubmitButton("Remove", "Remove " + definition.label,
          { modifier: "admin-button--danger", blocked: elsewhere.length === 0, controls: priorityControls });
        if (elsewhere.length === 0) {
          const note = document.createElement("p");
          note.className = "admin-note";
          text(note, "This is the only priority this project has, so there is nowhere for its tasks to go.");
          form.append(note);
        } else {
          form.append(panelField("Move its tasks to", into));
        }
        form.append(remove, panelButton("Cancel", "Keep " + definition.label, closePriorityForm,
          { controls: priorityControls }));
        return form;
      }
      // What a removal cost, in the one term a priority removal has: the tasks
      // that were filed at it and where they went.
      //
      // Not in claimability, which is the other half of what a status removal
      // reports. Whether `workbook next` can claim a task is decided by a
      // status's tags and the task's dependencies, and nothing about a priority
      // gates it — the number would be zero for every priority removal forever,
      // which is why the answer does not carry one.
      function priorityRemovalSummary(definition, into, counts) {
        const name = priorityLabelIn(panelVocabulary, into);
        const affected = counts && counts.affected || 0;
        if (affected === 0) return "Removed " + definition.label + ". No tasks were at it.";
        return "Removed " + definition.label + ". " +
          affected + (affected === 1 ? " task moved to " : " tasks moved to ") + name + ".";
      }
      function addPriorityForm(definitions) {
        const region = document.createElement("div");
        region.className = "admin-add";
        const heading = document.createElement("h3");
        heading.className = "admin-add__title";
        text(heading, "Add a priority");
        const form = document.createElement("form");
        form.className = "admin-form";
        form.dataset.priorityAdd = "";
        const name = panelTextInput("priority-new-name", "", "critical", priorityControls);
        const label = panelTextInput("priority-new-label", "", "derived from the name", priorityControls);
        // One control for the placement, because a placement is one decision and
        // the route takes one anchor. The writer refuses a change naming both.
        //
        // One option per position rather than one per priority. A list of n
        // priorities has n+1 positions, and naming both neighbours of every one
        // of them spells most of those twice: "Above X" is the position "Below
        // the one before X" already names. So the most urgent is the only
        // priority something is offered above, and everything else is a "Below".
        //
        // Every option names a priority, including the bottom of the list. A
        // standing "Least urgent" would be the one entry shaped differently from
        // the rest, and worse, it would read as a priority's name to anybody
        // whose project has one called Urgent — which is the first name people
        // reach for. "Below Low" says the same place and cannot be mistaken for
        // a member.
        //
        // The list runs in the order the rows are drawn, and the bottom is the
        // default because that is where a priority somebody has not thought
        // about the position of belongs.
        const placements = (definitions.length
          ? [["before:" + definitions[0].priority, "Above " + definitions[0].label]]
          : []
        ).concat(definitions.map((candidate) =>
          ["after:" + candidate.priority, "Below " + candidate.label]));
        const placement = panelSelect("priority-new-placement", placements, priorityControls,
          definitions.length ? "after:" + definitions[definitions.length - 1].priority : null);
        const add = panelSubmitButton("Add priority", "",
          { modifier: "admin-button--primary", blocked: true, controls: priorityControls });
        const guard = () => blockPanelControl(add, !name.value.trim());
        name.addEventListener("input", guard);
        function submit() {
          const priority = name.value.trim();
          if (!priority) return;
          const addition = { priority };
          // An omitted label is the server deriving one from the name, which is
          // what `workbook priority add` does without --label. An empty string
          // would be a blank label, which the vocabulary refuses.
          if (label.value.trim()) addition.label = label.value.trim();
          const [side, neighbor] = placement.value ? placement.value.split(":") : ["", ""];
          if (neighbor) addition[side] = neighbor;
          changeVocabulary("POST", "/api/vocabulary/priorities", addition,
            () => "Added " + priority + ".", priorityChanges);
        }
        form.addEventListener("submit", (event) => { event.preventDefault(); submit(); });
        form.append(
          panelField("Name", name),
          panelField("Label", label),
          panelField("Position", placement),
          add
        );
        region.append(heading, form);
        return region;
      }
      function closePriorityForm() {
        priorityForm = null;
        renderPriorityPanel();
      }
      function openPriorityForm(priority, kind) {
        priorityForm = priorityForm && priorityForm.priority === priority && priorityForm.kind === kind
          ? null
          : { priority, kind };
        renderPriorityPanel();
      }
      // Moving a priority one place names the neighbor it goes next to, because
      // that is what the route takes: there is no whole-order counterpart here,
      // and the asymmetry with the statuses is deliberate rather than an
      // omission. Up places this priority before the one above it; Down places it
      // after the one below.
      function movePriority(definition, index, offset, definitions) {
        const neighbor = definitions[index + offset];
        if (!neighbor) return;
        const anchor = offset < 0 ? { before: neighbor.priority } : { after: neighbor.priority };
        return changeVocabulary("PATCH", priorityPath(definition.priority) + "/position", anchor,
          () => "Moved " + definition.label + (offset < 0 ? " up." : " down."),
          priorityChanges);
      }
      // A row dropped on another takes that row's place: downward it lands after
      // the row it was dropped on, upward it lands before it — the same rule the
      // statuses read a drop by, because it is what both gestures look like they
      // are asking for.
      //
      // That rule is already an anchor. One neighbor and one side of it is
      // exactly what the move route takes, so a drop needs no whole-order plan
      // to be sent: this is the request Up and Down send, from a different
      // gesture.
      function movePriorityOnto(dragged, target) {
        const definitions = panelPriorityList();
        const from = definitions.findIndex((definition) => definition.priority === dragged);
        const to = definitions.findIndex((definition) => definition.priority === target);
        if (from < 0 || to < 0 || from === to) return;
        const falling = from < to;
        const anchor = falling ? { after: target } : { before: target };
        return changeVocabulary("PATCH", priorityPath(dragged) + "/position", anchor,
          () => "Moved " + definitions[from].label + (falling ? " down." : " up."),
          priorityChanges);
      }
      // Where this drag may be dropped on the priority list, or null if it may
      // not be dropped here. Asked by dragenter, dragover and drop alike, for
      // the reasons panelDropDestination above sets out: a browser takes its
      // answer from whichever of the first two it dispatched last, and one rule
      // is what keeps the three from drifting apart.
      //
      // The three refusals are that function's three, read against this list: a
      // file is not a reorder and must be neither taken nor swallowed, a
      // gesture that is not this list's — a board card, a column, a drag from
      // another window — is none of its business, and the row being dragged has
      // nowhere to arrive.
      function priorityDropDestination(event, row, priority) {
        if (draggingFiles(event)) return null;
        if (!panelDragPriority) return null;
        if (panelDragPriority === priority) return null;
        return row;
      }
      function clearPriorityDropMarks() {
        panelPriorityRows.forEach((row) => { delete row.dataset.dropTarget; });
      }
      function priorityRow(definition, index, definitions) {
        const item = document.createElement("li");
        item.className = "admin-status";
        item.dataset.vocabularyPriority = definition.priority;
        const editing = Boolean(priorityForm && priorityForm.priority === definition.priority);
        // A row with a form open is not draggable, for the reason a status row
        // with one open is not: selecting the text in an input is a press and a
        // drag, and inside a draggable ancestor that is the gesture that starts
        // the drag. The row is still reorderable while its form is open, by the
        // Up and Down controls.
        item.draggable = !editing;
        const name = document.createElement("div");
        name.className = "admin-status__name";
        const label = document.createElement("span");
        text(label, definition.label);
        const token = document.createElement("code");
        token.className = "admin-status__token";
        text(token, definition.priority);
        name.append(label, token);
        // The role chips, drawn from the roles the document says this priority
        // carries. Nothing here names a role: the set is the vocabulary's.
        (definition.tags || []).forEach((tag) => {
          const chip = document.createElement("span");
          chip.className = "admin-tag";
          chip.dataset.priorityTag = tag;
          text(chip, tag);
          name.append(chip);
        });
        const controls = document.createElement("div");
        controls.className = "admin-status__controls";
        // Reordering has a keyboard path as well as a drag, and both send the
        // same one request. A drag is not an input everybody has.
        controls.append(
          panelButton("Up", "Move " + definition.label + " earlier",
            () => movePriority(definition, index, -1, definitions),
            { blocked: index === 0, controls: priorityControls }),
          panelButton("Down", "Move " + definition.label + " later",
            () => movePriority(definition, index, 1, definitions),
            { blocked: index === definitions.length - 1, controls: priorityControls }),
          panelButton("Edit", "Edit " + definition.label,
            () => openPriorityForm(definition.priority, "edit"), { controls: priorityControls }),
          panelButton("Delete", "Delete " + definition.label,
            () => openPriorityForm(definition.priority, "delete"), { controls: priorityControls })
        );
        item.append(name, controls);
        if (editing) {
          item.append(priorityForm.kind === "delete"
            ? priorityDeleteForm(definition, definitions)
            : priorityEditForm(definition));
        }
        // The gesture, listener for listener as the statuses draw it, and for
        // the reasons written out over their handlers: both of dragenter and
        // dragover answer, through the one rule above; a leave that is not a
        // departure leaves the mark alone, since a browser pairs a dragleave
        // with every dragenter and a row is a name, some chips and four buttons
        // for a cursor to cross; and a drop this section refuses is left alone
        // entirely — not marked, not prevented, and above all not turned into a
        // reorder, which is what a file dropped here over a stale drag would
        // otherwise become.
        item.addEventListener("dragstart", (event) => {
          // The same condition the row's own controls are greyed by: a change
          // is in flight somewhere on this page, and a gesture that cannot be
          // sent is not one to start.
          if (configBusy() || editing) return;
          panelDragPriority = definition.priority;
          item.dataset.dragging = "true";
          if (event.dataTransfer) {
            event.dataTransfer.effectAllowed = "move";
            event.dataTransfer.setData("text/plain", definition.priority);
          }
        });
        item.addEventListener("dragend", () => {
          panelDragPriority = null;
          clearPriorityDropMarks();
          delete item.dataset.dragging;
        });
        const answerPriorityDrag = (event) => {
          if (!priorityDropDestination(event, item, definition.priority)) return;
          event.preventDefault();
          if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
          // Only when the mark is not already here: rewriting the same
          // attributes under a drag cursor is a mutation for no change, and it
          // is mutations under a drag cursor that make a browser re-run its hit
          // test and churn.
          if (item.dataset.dropTarget !== "true") {
            clearPriorityDropMarks();
            item.dataset.dropTarget = "true";
          }
        };
        item.addEventListener("dragenter", answerPriorityDrag);
        item.addEventListener("dragover", answerPriorityDrag);
        item.addEventListener("dragleave", (event) => {
          if (item.contains(event.relatedTarget)) return;
          if (cursorWithin(item, event)) return;
          delete item.dataset.dropTarget;
        });
        item.addEventListener("drop", (event) => {
          if (!priorityDropDestination(event, item, definition.priority)) return;
          event.preventDefault();
          clearPriorityDropMarks();
          const dragged = panelDragPriority;
          panelDragPriority = null;
          return movePriorityOnto(dragged, definition.priority);
        });
        return item;
      }
      function renderPriorityPanel() {
        if (!priorityPanel || !priorityPanelBody) return;
        // Focus is caught and parked the way the statuses panel catches it: the
        // control the reader pressed is one of the nodes this drops.
        const held = document.activeElement;
        const inside = Boolean(held && priorityPanel.contains(held));
        priorityControls = [];
        panelPriorityRows = [];
        // A rebuild drops every row a drag could be carrying or landing on, and
        // a drag whose row no longer exists is not a drag any more — the
        // statuses drop theirs here for the same reason.
        panelDragPriority = null;
        if (!panelVocabulary || !panelVocabulary.priorities) {
          const message = document.createElement("p");
          message.className = "admin-note";
          message.dataset.priorityPanelEmpty = "";
          text(message, panelReading
            ? "Reading this project's priorities."
            : "This project's priorities are not available.");
          priorityPanelBody.replaceChildren(message);
          restorePriorityFocus(inside);
          return;
        }
        const definitions = panelPriorityList();
        const list = document.createElement("ul");
        list.className = "admin-statuses";
        list.setAttribute("role", "list");
        definitions.forEach((definition, index) => {
          const row = priorityRow(definition, index, definitions);
          panelPriorityRows.push(row);
          list.append(row);
        });
        priorityPanelBody.replaceChildren(list, addPriorityForm(definitions));
        applyPriorityBusy();
        restorePriorityFocus(inside);
      }
      function restorePriorityFocus(inside) {
        if (!inside || !priorityPanel || priorityPanel.contains(document.activeElement)) return;
        if (typeof priorityPanel.focus === "function") priorityPanel.focus();
      }
      // Leaving the route takes the priorities with it, for the reason it takes
      // the statuses: the next visit reads them again rather than offering a
      // change composed against values that may be minutes old.
      function leavePrioritySection() {
        if (!priorityPanel || priorityPanel.hidden) return;
        priorityPanel.hidden = true;
        priorityPanel.remove();
        priorityForm = null;
        priorityPanelBody.replaceChildren();
        priorityPanelStatus.replaceChildren();
        priorityPanelStatus.dataset.kind = "";
      }
