      function dependencyCombobox(config) {
        const state = { query: "", selectedID: "", activeIndex: -1, candidates: [] };
        const region = document.createElement("div");
        region.className = "relationship-editor";
        // The caption is the input's accessible name and is drawn clipped: the
        // group heading directly above it already says which set is being added
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
        const add = document.createElement("button");
        add.type = "button";
        add.disabled = true;
        text(add, "Add dependency");
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
          input.value = candidate.title;
          hideListbox();
          renderOptions();
        }
        function renderOptions() {
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
          if (activeOptionID && !listbox.hidden) {
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
        input.addEventListener("focus", openListbox);
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
          renderOptions();
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
          setCandidates(candidates) {
            state.candidates = candidates;
            if (!selectedCandidate()) state.selectedID = "";
            state.activeIndex = -1;
            renderOptions();
          },
          setBusy(busy) {
            input.disabled = busy;
            renderOptions();
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
      function relationshipRow(relationship, onRemove, isDraft = false, onRetry = null) {
        const row = document.createElement("article");
        row.className = "relationship-row relationship-row--compact";
        row.dataset.relationshipRow = "";
        row.dataset.relationshipId = relationship.id;
        if (isDraft) row.dataset.relationshipDraft = "";
        const heading = document.createElement("h4");
        if (relationship.task && !relationship.task.deleted) {
          const link = document.createElement("a");
          link.href = "/tasks/" + encodeURIComponent(relationship.id);
          text(link, relationship.task.title);
          heading.append(link);
        } else {
          text(heading, relationship.task ? relationship.task.title : "Unavailable task");
        }
        const metadata = document.createElement("p");
        metadata.className = "relationship-row__metadata";
        const metadataParts = [];
        if (relationship.task) {
          metadataParts.push(relationship.task.status, relationship.task.priority);
        }
        metadataParts.push(relationship.id);
        text(metadata, metadataParts.join(" · "));
        // Everything there is to read goes in one grid item, and everything
        // there is to press goes in the other. The row is a two-column grid,
        // and it used to hand each part to that grid loose: only the Remove
        // button named a column, so which part landed where came out of how
        // many parts this branch appended. A failed draft's error note, its
        // fourth part, auto-placed into the controls column and narrowed the
        // title, and the row with no button at all crushed its title into a
        // vertical line of single characters.
        const textColumn = document.createElement("div");
        textColumn.className = "relationship-row__text";
        textColumn.append(heading, metadata);
        const controls = document.createElement("div");
        controls.className = "relationship-row__controls";
        if (isDraft) {
          const draft = document.createElement("strong");
          draft.className = "relationship-state";
          text(draft, "Not saved");
          textColumn.append(draft);
          if (relationship.error) {
            const error = document.createElement("p");
            error.className = "relationship-row__note";
            text(error, relationship.error);
            textColumn.append(error);
          }
        } else if (relationship.task && relationship.task.deleted) {
          const deleted = document.createElement("strong");
          deleted.className = "relationship-state";
          text(deleted, "Deleted");
          textColumn.append(deleted);
        }
        if (isDraft && onRetry) {
          const retry = document.createElement("button");
          retry.type = "button";
          retry.className = "relationship-retry";
          text(retry, "Retry");
          retry.addEventListener("click", () => onRetry(relationship));
          controls.append(retry);
          row.retryButton = retry;
        }
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "relationship-remove";
        text(remove, "Remove");
        remove.addEventListener("click", () => onRemove(relationship));
        controls.append(remove);
        row.removeButton = remove;
        row.append(textColumn, controls);
        return row;
      }
      function taskRelationships(taskID, draftState = null) {
        const region = document.createElement("section");
        region.className = "task-relationships";
        region.setAttribute("aria-label", "Task relationships");
        const status = document.createElement("p");
        status.className = "form-status";
        status.setAttribute("role", "status");
        status.setAttribute("aria-live", "polite");
        const groups = new Map();
        let controller;
        [
          ["dependsOn", "depends-on", "Depends On", "No prerequisites.", "Find a prerequisite", "Prerequisite"],
          ["blocks", "blocks", "Blocks", "No tasks are blocked by this task.", "Find a task to block", "Task to block"]
        ].forEach(([name, direction, headingText, emptyText, labelText, placeholderText]) => {
          const group = document.createElement("section");
          group.className = "relationship-group";
          const heading = document.createElement("h3");
          heading.id = "dependency-" + direction + "-heading";
          text(heading, headingText);
          const list = document.createElement("div"); list.className = "relationship-list";
          const message = document.createElement("p");
          message.className = "relationship-message";
          message.setAttribute("role", "status");
          message.setAttribute("aria-live", "polite");
          const groupController = {
            group, list, message, emptyText, direction, busy: false, removeButtons: [],
            setBusy(busy) {
              this.busy = busy;
              this.combobox.setBusy(busy);
              this.removeButtons.forEach((button) => { button.disabled = busy; });
            },
            setMessage(value) { text(message, value); }
          };
          groupController.combobox = dependencyCombobox({
            id: "dependency-" + direction,
            label: labelText,
            placeholder: placeholderText,
            isBusy: () => groupController.busy,
            onAdd: (selectedID) => {
              if (controller.busy) return;
              if (!taskID) {
                const selectedTask = reconciledRelationshipTasks().active.find((task) => task.id === selectedID);
                if (!selectedTask) return;
                const draftMap = direction === "depends-on" ? draftState.dependsOn : draftState.blocks;
                draftMap.set(selectedID, draftEntry(selectedTask));
                controller.update();
                return;
              }
              const dependentID = direction === "depends-on" ? taskID : selectedID;
              const prerequisiteID = direction === "depends-on" ? selectedID : taskID;
              return mutateRelationship("PUT", dependentID, prerequisiteID, controller, groupController);
            }
          });
          group.append(heading, list, groupController.combobox.region, message);
          region.append(group);
          groups.set(name, groupController);
        });
        region.append(status);
        controller = {
          region, status, groups, taskID, draftState, busy: false, recoveryActions: [],
          // Replaced by the detail form so an edge written to this task can
          // hand back the head it moved to. The sidebar is also mounted on the
          // new-task form, which has no head to move, hence the default.
          adoptTaskHead() {},
          setBusy(busy) {
            this.busy = busy;
            groups.forEach((group) => group.setBusy(busy));
          },
          update() {
            this.recoveryActions = [];
            const state = taskID ? dependencyState(taskID) : { dependsOn: [], blocks: [] };
            if (taskID && !state) return;
            [["dependsOn", state.dependsOn], ["blocks", state.blocks]].forEach(([name, persistedRelationships]) => {
              const group = groups.get(name);
              const draftMap = draftState ? (name === "dependsOn" ? draftState.dependsOn : draftState.blocks) : null;
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
              group.combobox.setCandidates(relationshipCandidates(taskID, group.direction, draftState));
              group.removeButtons = [];
              if (relationships.length === 0) {
                const empty = document.createElement("p");
                empty.className = "relationship-row__note";
                text(empty, group.emptyText);
                group.list.replaceChildren(empty);
                return;
              }
              group.list.replaceChildren(...relationships.map((relationship) => {
                const isDraft = Boolean(
                  draftMap &&
                  draftMap.has(relationship.id) &&
                  !persistedIDs.has(relationship.id)
                );
                const retryDraft = isDraft && taskID && relationship.error
                  ? () => retryRelationshipDraft(
                    name,
                    relationship,
                    draftMap,
                    controller,
                    group
                  )
                  : null;
                const row = relationshipRow(relationship, () => {
                  if (controller.busy) return;
                  if (isDraft) {
                    if (taskID && !ownsDependencyController(controller)) return;
                    draftMap.delete(relationship.id);
                    if (taskID && !hasRelationshipDrafts(draftState)) {
                      pendingRelationshipDrafts.delete(taskID);
                    }
                    controller.update();
                    return;
                  }
                  const dependentID = name === "dependsOn" ? taskID : relationship.id;
                  const prerequisiteID = name === "dependsOn" ? relationship.id : taskID;
                  return mutateRelationship("DELETE", dependentID, prerequisiteID, controller, group);
                }, isDraft, retryDraft);
                const remove = row.removeButton;
                if (remove) {
                  remove.disabled = group.busy;
                  group.removeButtons.push(remove);
                }
                const retry = row.retryButton;
                if (retry) {
                  retry.disabled = group.busy;
                  group.removeButtons.push(retry);
                }
                if (isDraft) {
                  const recoveryAction = retry || remove;
                  if (recoveryAction) controller.recoveryActions.push(recoveryAction);
                }
                return row;
              }));
            });
          }
        };
        controller.update();
        return controller;
      }
