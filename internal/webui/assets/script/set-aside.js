      // Edits the server's background sync set aside.
      //
      // When this server's clone and another clone change the same task's
      // description at once and the other change reaches origin first, the
      // server's sync keeps origin's text and parks its own edit, and every
      // edit after it, in a ref that only the server holds. The poll's
      // presentation carries those edits (core.SetAsideEdit), read from the
      // parked ref itself, so they are shown to everyone who opens the board
      // and survive a restart of the server. They are shown until the task
      // carries them again — a person saves the same text — or somebody
      // dismisses them.
      const setAsideFields = {
        title: "Title",
        description: "Description",
        status: "Status",
        priority: "Priority",
        rank: "Position",
        labels: "Label",
        dependencies: "Depends on"
      };
      function setAsideCount(count) {
        return count === 1 ? "1 edit set aside" : count + " edits set aside";
      }
      // One operation of a set-aside edit as words: what changed, and the
      // value it would have had, drawn with the helper the board draws that
      // kind of value with everywhere else — a one-line value through
      // shownLine, a file's name through shownFileName, a link by its label
      // through attachmentShownName — so the notice shows nothing the rest
      // of the board would not. A value that is prose — a description or a
      // comment — is drawn as a block a reader can copy from, and keeps the
      // text exactly as it was saved in raw, which is what Copy puts on the
      // clipboard: copying the drawn text instead would save something that
      // never matches the set-aside edit.
      function setAsideChange(operation) {
        const field = setAsideFields[operation.field] || shownLine(operation.field) || "";
        const line = (label, value) => ({ label, value: shownLine(value), block: false });
        const prose = (label, value) => ({ label, value: value || "", raw: value || "", block: true });
        switch (operation.type) {
          case "field.set":
            return operation.field === "description" ? prose(field, operation.value) : line(field, operation.value);
          case "set.add":
            return line(field + " added", operation.value);
          case "set.remove":
            return line(field + " removed", operation.value);
          case "comment.add":
            return prose("Comment", operation.body);
          case "comment.edit":
            return prose("Comment edited", operation.body);
          case "comment.remove":
            return line("Comment removed", "");
          case "assign.add":
            return line("Assigned", operation.value);
          case "assign.remove":
            return line("Unassigned", operation.value);
          case "attachment.add":
            return { label: "Attachment added", value: operation.attachment ? attachmentShownName(operation.attachment) : "", block: false };
          case "attachment.remove":
            return line("Attachment removed", "");
          case "task.tombstone":
            return line("Deleted the task", "");
          case "task.restore":
            return line("Restored the task", "");
          default:
            return line(shownLine(operation.type) || "Change", operation.value);
        }
      }
      function setAsideTime(value) {
        const when = new Date(value);
        return Number.isNaN(when.getTime()) ? "" : when.toLocaleString();
      }
      // The task page's notice. It is built once with the page and follows
      // every poll, so a set-aside edit that arrives while the page is open
      // appears without a reload and one somebody dismissed elsewhere goes.
      function setAsideNotice(taskID) {
        const region = document.createElement("section");
        region.className = "set-aside";
        region.dataset.setAside = "";
        region.setAttribute("aria-labelledby", "set-aside-title");
        region.hidden = true;
        const heading = document.createElement("h3"); heading.id = "set-aside-title";
        const summary = document.createElement("p"); summary.className = "set-aside__summary";
        text(summary, "A different change to this task reached the shared repository first, so these edits were kept aside rather than applied. Copy back what you want to keep and save it, then dismiss them.");
        // The list is the notice's one scrolling region: each edit's text is
        // drawn whole inside it, so nothing scrolls within something else that
        // scrolls. It takes focus, so a keyboard can scroll it too.
        const list = document.createElement("div"); list.className = "set-aside__list"; list.tabIndex = 0;
        list.setAttribute("role", "region"); list.setAttribute("aria-label", "Set-aside edits");
        const status = document.createElement("p"); status.className = "set-aside__status"; status.setAttribute("role", "status"); status.setAttribute("aria-live", "polite");
        region.append(heading, summary, list, status);
        let signature = null;
        async function dismiss(parkedRef, parked, button) {
          button.disabled = true;
          text(status, "");
          try {
            const response = await fetch("/api/tasks/" + encodeURIComponent(taskID) + "/set-aside", {
              method: "DELETE",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ parkedRef, parked })
            });
            const document = await response.json();
            if (!response.ok || document.format !== "workbook.set-aside-dismissal" || document.version !== 1) {
              throw new Error(document.error && document.error.message || "The edits could not be dismissed.");
            }
            text(status, "Dismissed.");
            await refresh();
          } catch (failure) {
            button.disabled = false;
            text(status, failure && failure.message ? failure.message : "The edits could not be dismissed.");
          }
        }
        function follow(view) {
          const edits = view && Array.isArray(view.setAside) ? view.setAside : [];
          const next = JSON.stringify(edits);
          if (next === signature) return;
          signature = next;
          region.hidden = edits.length === 0;
          text(heading, setAsideCount(edits.length));
          list.replaceChildren();
          // Grouped by the parked ref that holds them, because that is what a
          // dismissal removes: the edits one conflict set aside go together.
          const groups = new Map();
          edits.forEach((edit) => {
            const key = edit.parkedRef + "\u0000" + edit.parked;
            if (!groups.has(key)) groups.set(key, { parkedRef: edit.parkedRef, parked: edit.parked, edits: [] });
            groups.get(key).edits.push(edit);
          });
          groups.forEach((group) => {
            const block = document.createElement("div"); block.className = "set-aside__group";
            group.edits.forEach((edit) => {
              const item = document.createElement("article"); item.className = "set-aside__edit"; item.dataset.setAsideEdit = edit.commit || "";
              const by = document.createElement("p"); by.className = "set-aside__by";
              const who = document.createElement("strong"); text(who, shownLine(edit.actor) || "Someone");
              by.append(who);
              const when = setAsideTime(edit.wallTime);
              if (when) { const at = document.createElement("span"); text(at, " · " + when); by.append(at); }
              item.append(by);
              (Array.isArray(edit.operations) ? edit.operations : []).forEach((operation) => {
                const change = setAsideChange(operation);
                const row = document.createElement("div"); row.className = "set-aside__change";
                const label = document.createElement("span"); label.className = "set-aside__label"; text(label, change.label);
                row.append(label);
                if (change.block) {
                  const value = document.createElement("pre"); value.className = "set-aside__text";
                  value.dataset.setAsideText = operation.field || operation.type || "";
                  text(value, change.value);
                  const copy = document.createElement("button"); copy.type = "button"; copy.className = "set-aside__copy";
                  text(copy, "Copy text");
                  copy.setAttribute("aria-label", "Copy the set-aside " + change.label.toLowerCase());
                  copy.addEventListener("click", async () => {
                    try {
                      if (!navigator.clipboard || !navigator.clipboard.writeText) throw new Error("clipboard unavailable");
                      await navigator.clipboard.writeText(change.raw);
                      text(status, "Copied.");
                    } catch (_) {
                      text(status, "Could not copy. Select the text and copy it instead.");
                    }
                  });
                  row.append(copy);
                  // The text holds direction controls, which every board draws
                  // as spaces. Copy carries the text as it was saved, so the
                  // drawing and the clipboard differ, and the reader is told.
                  if (shownField(change.raw) !== change.raw) {
                    const note = document.createElement("span"); note.className = "set-aside__note"; note.dataset.setAsideNote = "";
                    text(note, "Copy takes the text exactly as it was saved, including the direction controls drawn here as spaces.");
                    row.append(note);
                  }
                  row.append(value);
                } else if (change.value) {
                  const value = document.createElement("span"); value.className = "set-aside__value"; text(value, change.value);
                  row.append(value);
                }
                item.append(row);
              });
              block.append(item);
            });
            const dismissal = document.createElement("button"); dismissal.type = "button"; dismissal.className = "set-aside__dismiss";
            dismissal.dataset.setAsideDismiss = group.parkedRef;
            text(dismissal, group.edits.length === 1 ? "Dismiss this edit" : "Dismiss these edits");
            dismissal.addEventListener("click", () => dismiss(group.parkedRef, group.parked, dismissal));
            block.append(dismissal);
            list.append(block);
          });
        }
        return { region, follow };
      }
