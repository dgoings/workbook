      function dependencyCombobox(config) {
        const state = { query: "", selectedID: "", activeIndex: -1, candidates: [] };
        const region = document.createElement("div");
        region.className = "relationship-editor";
        // The caption is the input's accessible name and is drawn clipped: the
        // kind toggle directly above it already says which set is being added
        // to, and the placeholder says it again inside the box, so nothing
        // visible is lost by not spending a row of the sidebar on it. Kept as a
        // real label rather than replaced by an aria-label, so the name stays
        // in the markup where the input is.
        //
        // The placeholder is the short form of the caption and a substring of
        // it, which is what WCAG's label-in-name asks: someone who says "click
        // prerequisite" names a control the page answers to. Spelling the
        // caption out in full here would only be truncated — the box shares its
        // row with the Add button.
        const label = document.createElement("label");
        label.className = "relationship-editor__label";
        label.htmlFor = config.id + "-input";
        text(label, config.label);
        const input = document.createElement("input");
        input.type = "search";
        input.id = config.id + "-input";
        input.placeholder = config.placeholder;
        input.setAttribute("role", "combobox");
        input.setAttribute("aria-autocomplete", "list");
        input.setAttribute("aria-controls", config.id + "-listbox");
        input.setAttribute("aria-expanded", "false");
        const listbox = document.createElement("div");
        listbox.id = config.id + "-listbox";
        listbox.className = "relationship-listbox";
        listbox.setAttribute("role", "listbox");
        listbox.hidden = true;
        const empty = document.createElement("p");
        empty.className = "relationship-empty";
        empty.setAttribute("role", "status");
        empty.setAttribute("aria-live", "polite");
        // "Add" on the button, so it keeps its natural width beside the box;
        // its accessible name says what it adds and contains the word shown,
        // as label-in-name asks.
        const add = threadButton("Add", true);
        add.disabled = true;
        add.dataset.relationshipAdd = "";
        add.setAttribute("aria-label", "Add dependency");
        region.append(label, input, add, listbox, empty);
        let tracksPosition = false;

        function positionListbox() {
          if (listbox.hidden) return;
          const anchor = input.getBoundingClientRect();
          const sidebar = region.closest(".task-sidebar");
          const sidebarRect = sidebar
            ? sidebar.getBoundingClientRect()
            : { top: 0, right: window.innerWidth, bottom: window.innerHeight, left: 0 };
          const bounds = {
            top: Math.max(8, sidebarRect.top),
            bottom: Math.min(window.innerHeight - 8, sidebarRect.bottom)
          };
          const placement = relationshipListboxPlacement(anchor, bounds);
          const regionRect = region.getBoundingClientRect();
          const availableWidth = Math.max(0, window.innerWidth - 16);
          const width = Math.min(regionRect.width || sidebarRect.width || 320, availableWidth);
          const left = Math.min(
            Math.max(8, regionRect.left || sidebarRect.left || 8),
            window.innerWidth - width - 8
          );
          listbox.style.left = left + "px";
          listbox.style.width = width + "px";
          listbox.style.maxHeight = placement.maxHeight + "px";
          if (placement.side === "above") {
            listbox.style.top = "auto";
            listbox.style.bottom = (window.innerHeight - placement.bottom) + "px";
          } else {
            listbox.style.top = placement.top + "px";
            listbox.style.bottom = "auto";
          }
        }
        function startPositioning() {
          if (tracksPosition) return;
          document.addEventListener("scroll", positionListbox, true);
          window.addEventListener("resize", positionListbox);
          tracksPosition = true;
        }
        function stopPositioning() {
          if (!tracksPosition) return;
          document.removeEventListener("scroll", positionListbox, true);
          window.removeEventListener("resize", positionListbox);
          tracksPosition = false;
        }
        function showListbox() {
          input.setAttribute("aria-expanded", "true");
          listbox.hidden = false;
          startPositioning();
          positionListbox();
        }
        function hideListbox() {
          input.setAttribute("aria-expanded", "false");
          listbox.hidden = true;
          stopPositioning();
        }

        function matchingCandidates() {
          const query = state.query.trim().toLocaleLowerCase();
          if (!query) return state.candidates;
          return state.candidates.filter((task) =>
            task.title.toLocaleLowerCase().includes(query) ||
            task.id.toLocaleLowerCase().includes(query));
        }
        function selectedCandidate() {
          return state.candidates.find((task) => task.id === state.selectedID) || null;
        }
        function selectCandidate(candidate) {
          if (!candidate) return;
          state.selectedID = candidate.id;
          state.query = candidate.title;
          state.activeIndex = -1;
          // The box is drawn text: it holds the title as the picked option
          // drew it, not the stored one with its direction controls.
          input.value = shownField(candidate.title);
          hideListbox();
          renderOptions();
        }
        // reveal scrolls the highlighted option into view. Only a key that
        // moved the highlight asks for it: the board's poll redraws this list
        // about once a second, and a redraw that scrolled would drag the list
        // back to the highlight from wherever the reader had scrolled it.
        function renderOptions(reveal = false) {
          const options = matchingCandidates();
          if (state.activeIndex >= options.length) state.activeIndex = options.length - 1;
          const active = state.activeIndex >= 0 ? options[state.activeIndex] : null;
          let activeOptionID = "";
          if (active) {
            const candidateIndex = state.candidates.findIndex((task) => task.id === active.id);
            activeOptionID = config.id + "-option-" + candidateIndex;
            input.setAttribute("aria-activedescendant", activeOptionID);
          } else {
            input.removeAttribute("aria-activedescendant");
          }
          listbox.replaceChildren(...options.map((task) => {
            const option = document.createElement("div");
            const candidateIndex = state.candidates.findIndex((candidate) => candidate.id === task.id);
            option.id = config.id + "-option-" + candidateIndex;
            option.className = "relationship-option" + (active && active.id === task.id ? " is-active" : "");
            option.dataset.candidateId = task.id;
            option.setAttribute("role", "option");
            option.setAttribute("aria-selected", String(state.selectedID === task.id));
            const title = document.createElement("strong");
            text(title, task.title);
            const metadata = document.createElement("span");
            text(metadata, task.status + " · " + task.priority + " · " + task.id);
            option.append(title, metadata);
            option.addEventListener("click", () => selectCandidate(task));
            return option;
          }));
          if (reveal && activeOptionID && !listbox.hidden) {
            const activeOption = [...listbox.children].find((option) => option.id === activeOptionID);
            if (activeOption) activeOption.scrollIntoView({ block: "nearest" });
          }
          text(empty, options.length ? "" : "No matching tasks.");
          add.disabled = config.isBusy() || !selectedCandidate();
        }
        input.addEventListener("input", () => {
          state.query = input.value;
          state.selectedID = "";
          state.activeIndex = -1;
          showListbox();
          renderOptions();
        });
        function openListbox() {
          showListbox();
          renderOptions();
        }
        // Set while the page hands the caret back to the box after a write,
        // so the list does not open over the sidebar under a reader who did
        // not ask for it. A press on the box still opens it.
        let quiet = false;
        input.focusQuietly = () => {
          quiet = true;
          try { input.focus(); } finally { quiet = false; }
        };
        input.addEventListener("focus", () => { if (!quiet) openListbox(); });
        // A press on an already focused input fires no focus event, and pointer
        // selection deliberately leaves focus in the input, so without this the
        // popup could only be reopened by first clicking somewhere else.
        input.addEventListener("click", openListbox);
        input.addEventListener("keydown", (event) => {
          const options = matchingCandidates();
          if (event.key === "ArrowDown") {
            event.preventDefault();
            state.activeIndex = Math.min(state.activeIndex + 1, options.length - 1);
            showListbox();
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            state.activeIndex = Math.max(state.activeIndex - 1, 0);
            showListbox();
          } else if (event.key === "Enter") {
            event.preventDefault();
            if (state.activeIndex >= 0) {
              selectCandidate(options[state.activeIndex]);
            }
          } else if (event.key === "Escape") {
            // Escape belongs to the combobox whether or not the popup is open.
            // This is a search field, so the native default is to clear it, and
            // that fires an input event whose handler reopens the popup: left
            // to the browser, a second Escape summons back the very popup the
            // first one dismissed. Claiming the key and clearing here keeps the
            // field and the combobox in step. preventDefault does not stop
            // propagation, so nothing else on the page loses Escape.
            if (listbox.hidden && !input.value) return;
            event.preventDefault();
            if (listbox.hidden) {
              state.query = "";
              state.selectedID = "";
              input.value = "";
            }
            state.activeIndex = -1;
            hideListbox();
          } else {
            return;
          }
          renderOptions(event.key === "ArrowDown" || event.key === "ArrowUp");
        });
        add.addEventListener("click", () => {
          if (config.isBusy()) return;
          const candidate = selectedCandidate();
          if (candidate) return config.onAdd(candidate.id);
        });
        // A pointer press inside the popup must not move focus. mousedown runs
        // before click, so a press that blurred the input would let the
        // dismissal below hide the option out from under the click that was
        // about to select it.
        //
        // This stays blanket rather than skipping the scrollbar gutter. A
        // scrollbar keeps dragging through a cancelled mousedown, so exempting
        // the gutter buys nothing; it only hands the press back its default,
        // which blurs the input and dismisses the popup out from under the drag
        // that was about to scroll it.
        listbox.addEventListener("mousedown", (event) => { event.preventDefault(); });
        // focusout rather than blur, because it bubbles: the popup survives
        // focus moving anywhere inside this editor and collapses the moment
        // focus leaves it, which is what clicking elsewhere on the page or
        // tabbing away looks like. It floats over the sidebar, so leaving it
        // open once it is no longer being used hides the rest of the task.
        region.addEventListener("focusout", (event) => {
          const next = event.relatedTarget;
          if (next && region.contains(next)) return;
          state.activeIndex = -1;
          hideListbox();
          renderOptions();
        });

        const controller = {
          region, input, listbox, empty, add,
          // A chosen task the new set no longer offers loses its selection.
          // A poll keeps its title in the box, so the reader sees what went
          // stale; a kind change clears the box too (clearStalePick), because
          // there the reader moved the toggle themselves, and a title left
          // beside a disabled Add would read as a pick that cannot be added
          // for no reason given.
          //
          // The highlight follows its task, not its place in the list. The
          // board hands the picker its candidates on every once-a-second poll,
          // and resetting the highlight there took it off the option a
          // keyboard reader had arrowed to, so Enter picked nothing. It is let
          // go of only when the new set no longer lists that task.
          setCandidates(candidates, { clearStalePick = false } = {}) {
            const previous = matchingCandidates();
            const highlighted = state.activeIndex >= 0 && previous[state.activeIndex]
              ? previous[state.activeIndex].id
              : "";
            state.candidates = candidates;
            if (state.selectedID && !selectedCandidate()) {
              state.selectedID = "";
              if (clearStalePick) {
                state.query = "";
                input.value = "";
              }
            }
            state.activeIndex = highlighted
              ? matchingCandidates().findIndex((task) => task.id === highlighted)
              : -1;
            renderOptions();
          },
          setBusy(busy) {
            input.disabled = busy;
            renderOptions();
          },
          // The caption and the placeholder say which kind of edge the box
          // adds, so they change with the kind toggle beside it.
          setPrompt(caption, placeholder) {
            text(label, caption);
            input.placeholder = placeholder;
          },
          clear() {
            state.query = "";
            state.selectedID = "";
            state.activeIndex = -1;
            input.value = "";
            hideListbox();
            renderOptions();
          }
        };
        return controller;
      }
      // kind is the entry in relationshipKinds the row is listed under. The
      // sidebar lists both kinds in one list, so each row says which it is in
      // words, not only in color.
      function relationshipRow(relationship, onRemove, isDraft = false, onRetry = null, kind = null) {
        const row = document.createElement("article");
        row.className = "relationship-row relationship-row--compact sidebar-card";
        row.dataset.relationshipRow = "";
        row.dataset.relationshipId = relationship.id;
        if (kind) row.dataset.relationshipKind = kind.direction;
        if (isDraft) row.dataset.relationshipDraft = "";
        // What names this row across the list's redraws, for the caret: a
        // draft and the recorded edge it becomes share it, because the
        // recorded row is the one in the draft's place.
        row.rowKey = (kind ? kind.direction : "") + ":" + relationship.id;
        // The task's ID is not printed in the row: it is a long run of
        // characters that wrapped a sidebar row onto three lines and says
        // nothing a reader scanning their dependencies needs. It stays one
        // hover away as the title's tooltip, and the link goes to the task.
        const heading = document.createElement("h4");
        const title = relationship.task ? relationship.task.title : "Unavailable task";
        if (relationship.task && !relationship.task.deleted) {
          const link = document.createElement("a");
          link.href = "/tasks/" + encodeURIComponent(relationship.id);
          link.title = relationship.id;
          text(link, title);
          heading.append(link);
        } else {
          heading.title = relationship.id;
          text(heading, title);
        }
        const metadata = document.createElement("p");
        metadata.className = "relationship-row__metadata";
        // A task nobody can find has no status or priority to give, so its
        // ID is the one thing there is to say about it.
        text(metadata, relationship.task
          ? relationship.task.status + " · " + relationship.task.priority
          : relationship.id);
        // Everything there is to read goes in one grid item, and the × goes
        // in the other, at the right end of the title's line. The row is a
        // two-column grid, and it used to hand each part to that grid loose:
        // only the Remove button named a column, so which part landed where
        // came out of how many parts this branch appended. A failed draft's
        // error note, its fourth part, auto-placed into the controls column
        // and narrowed the title, and the row with no button at all crushed
        // its title into a vertical line of single characters.
        //
        // A draft says so on a line of its own under both, across the whole
        // card: "Not saved", why, and the Retry that saves it. Beside the ×,
        // Retry and the note squeezed the title into what was left of the
        // card's width, which is the part of the row a reader came to read.
        const textColumn = document.createElement("div");
        textColumn.className = "relationship-row__text";
        if (kind) {
          // The tag leads the title's line rather than taking a line of its own.
          const titleLine = document.createElement("div");
          titleLine.className = "relationship-row__title";
          const tag = document.createElement("span");
          tag.className = "relationship-row__kind relationship-row__kind--" + kind.direction;
          text(tag, kind.label);
          titleLine.append(tag, heading);
          textColumn.append(titleLine, metadata);
        } else {
          textColumn.append(heading, metadata);
        }
        const controls = document.createElement("div");
        controls.className = "relationship-row__controls";
        let failure = null;
        if (isDraft) {
          failure = document.createElement("div");
          failure.className = "relationship-row__failure";
          const draft = document.createElement("strong");
          draft.className = "relationship-state";
          text(draft, "Not saved");
          failure.append(draft);
          if (relationship.error) {
            const error = document.createElement("p");
            error.className = "relationship-row__note";
            text(error, relationship.error);
            failure.append(error);
          }
        } else if (relationship.task && relationship.task.deleted) {
          const deleted = document.createElement("strong");
          deleted.className = "relationship-state";
          text(deleted, "Deleted");
          textColumn.append(deleted);
        }
        if (isDraft && onRetry) {
          const retry = threadButton("Retry");
          retry.className += " relationship-retry";
          retry.addEventListener("click", () => onRetry(relationship));
          failure.append(retry);
          row.retryButton = retry;
        }
        const remove = rowRemoveButton(kind && kind.direction === "blocks"
          ? "Remove blocked task " + title
          : "Remove dependency on " + title);
        remove.className += " relationship-remove";
        remove.addEventListener("click", () => onRemove(relationship));
        controls.append(remove);
        row.removeButton = remove;
        row.append(textColumn, controls);
        if (failure) row.append(failure);
        return row;
      }
      // The two kinds of edge the sidebar adds and lists. Both are one stored
      // edge seen from either end: a Depends On edge is recorded on this task,
      // and a Blocks edge on the other one.
      const relationshipKinds = [
        { name: "dependsOn", direction: "depends-on", label: "Depends On", caption: "Find a prerequisite", placeholder: "Prerequisite" },
        { name: "blocks", direction: "blocks", label: "Blocks", caption: "Find a task to block", placeholder: "Task to block" }
      ];
      // The task's dependencies in one section: one list holding both kinds,
      // each row tagged with its kind, and one task picker with a toggle that
      // says which kind the next entry is. It replaced a Depends On group and a
      // Blocks group, which were two headings, two empty lines, two pickers and
      // two message lines for what is one set of edges.
      function taskRelationships(taskID, draftState = null) {
        const region = document.createElement("section");
        region.className = "task-relationships";
        region.setAttribute("aria-labelledby", "dependency-heading");
        const heading = document.createElement("h3");
        heading.id = "dependency-heading";
        text(heading, "Dependencies");
        const list = document.createElement("div"); list.className = "relationship-list";
        // The kind of the next entry, as a radio group: arrow keys move the
        // choice, and Tab reaches the group once. It stays as set after an add,
        // so a run of the same kind is one choice and several picks.
        let kind = relationshipKinds[0];
        const kindGroup = document.createElement("div");
        kindGroup.className = "relationship-kind";
        kindGroup.setAttribute("role", "radiogroup");
        kindGroup.setAttribute("aria-labelledby", "dependency-kind-label");
        const kindLabel = document.createElement("span");
        kindLabel.className = "relationship-kind__label";
        kindLabel.id = "dependency-kind-label";
        text(kindLabel, "Add as");
        const kindOptions = document.createElement("span");
        kindOptions.className = "relationship-kind__options";
        const kindInputs = relationshipKinds.map((candidate) => {
          const input = document.createElement("input");
          input.type = "radio";
          input.className = "relationship-kind__input";
          input.name = "dependency-kind";
          input.id = "dependency-kind-" + candidate.direction;
          input.value = candidate.direction;
          input.checked = candidate === kind;
          const label = document.createElement("label");
          label.className = "relationship-kind__option";
          label.htmlFor = input.id;
          text(label, candidate.label);
          input.addEventListener("change", () => {
            if (!input.checked) return;
            kindInputs.forEach((other) => { if (other !== input) other.checked = false; });
            chooseKind(candidate);
          });
          // The radios sit inside the task form, where Enter on one submits
          // it: it would create a half-filled task, or save an edit and leave.
          // Enter chooses the option instead, the way Space does.
          input.addEventListener("keydown", (event) => {
            if (event.key !== "Enter") return;
            event.preventDefault();
            if (input.checked) return;
            input.checked = true;
            kindInputs.forEach((other) => { if (other !== input) other.checked = false; });
            chooseKind(candidate);
          });
          kindOptions.append(input, label);
          return input;
        });
        kindGroup.append(kindLabel, kindOptions);
        const message = document.createElement("p");
        message.className = "relationship-message";
        message.setAttribute("role", "status");
        message.setAttribute("aria-live", "polite");
        const status = document.createElement("p");
        status.className = "form-status";
        status.setAttribute("role", "status");
        status.setAttribute("aria-live", "polite");
        let controller;
        const combobox = dependencyCombobox({
          id: "dependency",
          label: kind.caption,
          placeholder: kind.placeholder,
          isBusy: () => controller.busy,
          // An add hands the caret back to the picker, cleared for the next
          // one; a refused one leaves it on Add, beside the pick it kept.
          onAdd: async (selectedID) => {
            if (controller.busy) return;
            const settle = holdSidebarFocus(combobox.add, list, null, combobox.input);
            if (!taskID) {
              const selectedTask = reconciledRelationshipTasks().active.find((task) => task.id === selectedID);
              if (!selectedTask) return;
              draftState[kind.name].set(selectedID, draftEntry(selectedTask));
              combobox.clear();
              controller.update();
              settle(true);
              return;
            }
            const dependentID = kind.name === "dependsOn" ? taskID : selectedID;
            const prerequisiteID = kind.name === "dependsOn" ? selectedID : taskID;
            settle(await mutateRelationship("PUT", dependentID, prerequisiteID, controller));
          }
        });
        function chooseKind(next) {
          kind = next;
          combobox.setPrompt(kind.caption, kind.placeholder);
          // What the picker offers depends on the kind: a task this one already
          // depends on is not offered for Depends On, and one it already blocks
          // is not offered for Blocks. Nor is a task whose edge would close a
          // cycle; see dependencyCycleCandidates.
          combobox.setCandidates(relationshipCandidates(taskID, kind.direction, draftState), { clearStalePick: true });
        }
        region.append(heading, list, kindGroup, combobox.region, message, status);
        // What the list was last drawn from. A poll that changed nothing about
        // the rows leaves them standing, the way the assignments section does:
        // every row holds a focusable ×, and rebuilding the list once a second
        // took a keyboard reader's caret off it and dropped it on <body>
        // before Enter could reach it.
        let rowSignature = null;
        controller = {
          region, status, list, message, combobox, kindInputs, taskID, draftState,
          busy: false, removeButtons: [], recoveryActions: [],
          // The kind the next entry is added as.
          get kind() { return kind; },
          // Replaced by the detail form so an edge written to this task can
          // hand back the head it moved to. The sidebar is also mounted on the
          // new-task form, which has no head to move, hence the default.
          adoptTaskHead() {},
          setBusy(busy) {
            this.busy = busy;
            combobox.setBusy(busy);
            this.removeButtons.forEach((button) => { button.disabled = busy; });
          },
          setMessage(value) { text(message, value); },
          update() {
            const previousRemoveButtons = this.removeButtons;
            const previousRecoveryActions = this.recoveryActions;
            const signatureParts = [];
            this.recoveryActions = [];
            this.removeButtons = [];
            const state = taskID ? dependencyState(taskID) : { dependsOn: [], blocks: [] };
            if (taskID && !state) return;
            // Depends On rows first and then Blocks, each in the order its own
            // list has always had.
            const rows = [];
            relationshipKinds.forEach((rowKind) => {
              const name = rowKind.name;
              const persistedRelationships = state[name];
              const draftMap = draftState ? draftState[name] : null;
              const persistedIDs = new Set(persistedRelationships.map((relationship) => relationship.id));
              if (draftMap && taskID && ownsDependencyController(controller)) {
                persistedIDs.forEach((id) => draftMap.delete(id));
                if (!hasRelationshipDrafts(draftState)) {
                  pendingRelationshipDrafts.delete(taskID);
                }
              }
              const relationships = draftMap
                ? [
                  ...persistedRelationships,
                  ...[...draftMap.values()].filter((relationship) => !persistedIDs.has(relationship.id))
                ]
                : persistedRelationships;
              relationships.forEach((relationship) => {
                const isDraft = Boolean(
                  draftMap &&
                  draftMap.has(relationship.id) &&
                  !persistedIDs.has(relationship.id)
                );
                // Both of a row's buttons rebuild the list they sit in, so
                // each asks for the caret back before it writes; see
                // holdSidebarFocus for where it goes.
                const retryDraft = isDraft && taskID && relationship.error
                  ? async () => {
                    const settle = holdSidebarFocus(row.retryButton, list, row, combobox.input);
                    settle(await retryRelationshipDraft(name, relationship, draftMap, controller));
                  }
                  : null;
                const task = relationship.task;
                signatureParts.push([
                  name, relationship.id, isDraft, relationship.error || "", Boolean(retryDraft),
                  task ? [task.title, task.status, task.priority, Boolean(task.deleted)] : null
                ]);
                const row = relationshipRow(relationship, async () => {
                  if (controller.busy) return;
                  const settle = holdSidebarFocus(row.removeButton, list, row, combobox.input);
                  if (isDraft) {
                    if (taskID && !ownsDependencyController(controller)) return;
                    draftMap.delete(relationship.id);
                    if (taskID && !hasRelationshipDrafts(draftState)) {
                      pendingRelationshipDrafts.delete(taskID);
                    }
                    controller.update();
                    settle(true);
                    return;
                  }
                  const dependentID = name === "dependsOn" ? taskID : relationship.id;
                  const prerequisiteID = name === "dependsOn" ? relationship.id : taskID;
                  settle(await mutateRelationship("DELETE", dependentID, prerequisiteID, controller));
                }, isDraft, retryDraft, rowKind);
                const remove = row.removeButton;
                if (remove) {
                  remove.disabled = controller.busy;
                  controller.removeButtons.push(remove);
                }
                const retry = row.retryButton;
                if (retry) {
                  retry.disabled = controller.busy;
                  controller.removeButtons.push(retry);
                }
                if (isDraft) {
                  const recoveryAction = retry || remove;
                  if (recoveryAction) controller.recoveryActions.push(recoveryAction);
                }
                rows.push(row);
              });
            });
            combobox.setCandidates(relationshipCandidates(taskID, kind.direction, draftState));
            const signature = JSON.stringify(signatureParts);
            if (signature === rowSignature && list.children.length) {
              this.removeButtons = previousRemoveButtons;
              this.recoveryActions = previousRecoveryActions;
              return;
            }
            rowSignature = signature;
            if (rows.length === 0) {
              const empty = document.createElement("p");
              empty.className = "relationship-row__note";
              text(empty, "No dependencies.");
              list.replaceChildren(empty);
              return;
            }
            list.replaceChildren(...rows);
          }
        };
        controller.update();
        return controller;
      }
