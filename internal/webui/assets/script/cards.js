      // The visible ID inside a copy control, so a card that survives a render
      // can have a shortened prefix rewritten without rebuilding the control.
      const copyIDText = new WeakMap();
      function taskIDCopyControl(taskID, visibleID) {
        const group = document.createElement("span");
        group.className = "task-id-copy-group";
        const control = document.createElement("button");
        control.type = "button";
        control.className = "task-id-copy";
        control.dataset.copyTaskId = taskID;
        control.setAttribute("aria-label", "Copy full task ID " + taskID);
        const id = document.createElement("code");
        text(id, visibleID);
        control.append(id);
        const status = document.createElement("span");
        status.className = "copy-status";
        status.dataset.copyStatus = "";
        status.setAttribute("role", "status");
        status.setAttribute("aria-live", "polite");
        status.setAttribute("aria-atomic", "true");
        group.append(control, status);
        copyIDText.set(group, id);
        const activeFeedback = copyFeedback.get(taskID);
        if (activeFeedback) {
          activeFeedback.statuses.add(status);
          renderCopyStatus(status, activeFeedback);
        }
        return group;
      }
      function renderCopyStatus(status, feedback) {
        status.dataset.kind = feedback.kind;
        status.setAttribute("aria-label", feedback.announcement);
        text(status, feedback.visibleMessage);
      }
      function clearCopyStatus(status) {
        text(status, "");
        status.setAttribute("aria-label", "");
        delete status.dataset.kind;
      }
      function showCopyStatus(taskID, status, visibleMessage, announcement, kind) {
        const activeTimer = copyFeedbackTimers.get(taskID);
        if (activeTimer !== undefined) window.clearTimeout(activeTimer);
        const previousFeedback = copyFeedback.get(taskID);
        const statuses = previousFeedback ? previousFeedback.statuses : new Set();
        statuses.add(status);
        const feedback = { visibleMessage, announcement, kind, statuses };
        copyFeedback.set(taskID, feedback);
        statuses.forEach((candidate) => renderCopyStatus(candidate, feedback));
        const timer = window.setTimeout(() => {
          if (copyFeedback.get(taskID) !== feedback) return;
          statuses.forEach(clearCopyStatus);
          copyFeedback.delete(taskID);
          copyFeedbackTimers.delete(taskID);
        }, 4000);
        copyFeedbackTimers.set(taskID, timer);
      }
      async function copyTaskID(taskID, status) {
        try {
          if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") {
            throw new Error("clipboard unavailable");
          }
          await navigator.clipboard.writeText(taskID);
          showCopyStatus(taskID, status, "Copied", "Copied task ID " + taskID + ".", "success");
        } catch (_) {
          showCopyStatus(
            taskID,
            status,
            "Copy failed",
            "Could not copy task ID " + taskID + ".",
            "error"
          );
        }
      }
      // A card is built once and then updated in place for the rest of its life.
      // Node identity is the contract the board rests on: a card node that
      // survives a poll keeps its keyboard focus, its drag state, and the
      // browser's scroll anchor, and one that is destroyed and rebuilt loses all
      // three. cardParts holds the handles an update needs plus a signature of
      // everything the node was last drawn from, so an unchanged card costs one
      // string comparison.
      const cardParts = new WeakMap();
      function cardSignature(task, view, report) {
        return JSON.stringify([
          task.title || "",
          task.description || "",
          task.priority || "",
          task.status || "",
          task.labels || [],
          Boolean(task.deleted),
          view.idPrefix || "",
          view.dependenciesTotal || 0,
          view.dependenciesComplete || 0,
          Boolean(view.waitingOnDependencies),
          // The chip row the server derived, and what it left out. Both are part
          // of what the card is drawn from, so an assignment made from the CLI
          // redraws exactly the card it names on the next poll and no other.
          view.assignmentChips || [],
          view.moreAssignments || 0,
          Array.isArray(view.setAside) ? view.setAside.length : 0,
          report || ""
        ]);
      }
      // The card's report of a refused change. It is built with the card and
      // kept for its whole life rather than rebuilt beside the description and
      // the labels, because it is the one part of a card that holds a control:
      // a rebuild would drop the Dismiss button out from under the caret, and
      // its live region has to be in the document before the text arrives for
      // the text to be announced at all.
      function cardFailureReport(taskID) {
        const region = document.createElement("div");
        region.className = "task-card__failure";
        region.dataset.taskFailure = "";
        region.dataset.visible = "false";
        const message = document.createElement("span");
        message.dataset.taskFailureMessage = "";
        message.setAttribute("role", "status");
        message.setAttribute("aria-live", "polite");
        message.setAttribute("aria-atomic", "true");
        const dismiss = document.createElement("button");
        dismiss.type = "button";
        dismiss.dataset.taskFailureDismiss = "";
        text(dismiss, "Dismiss");
        // Every card can hold one of these, so a reader moving between buttons
        // needs to hear which card this one clears, the same way the copy
        // control names the ID it copies.
        dismiss.setAttribute("aria-label", "Dismiss the report on task " + taskID);
        // The task ID is the reconciliation key and never changes for a node,
        // so one listener bound at build time stays correct for every card this
        // node goes on to draw.
        dismiss.addEventListener("click", () => dismissTaskFailure(taskID));
        region.append(message, dismiss);
        return { region, message };
      }
      function card(task, view) {
        const article = document.createElement("article"); article.className = "task-card"; article.tabIndex = 0; article.dataset.taskId = task.id;
        const meta = document.createElement("div"); meta.className = "task-card__meta";
        const priority = document.createElement("span");
        let idText = null;
        if (view.pending) {
          // A task the server has not accepted yet has no ID to copy and no
          // detail route to open, so its card offers neither. It is not
          // draggable either: a placement request would have to name it.
          article.dataset.pendingCreate = "true";
          const saving = document.createElement("span");
          saving.className = "task-card__pending";
          text(saving, "Saving…");
          meta.append(saving, priority);
        } else {
          const copyGroup = taskIDCopyControl(task.id, view.idPrefix);
          idText = copyIDText.get(copyGroup);
          meta.append(copyGroup, priority);
        }
        article.append(meta);
        const title = document.createElement("h3");
        const link = document.createElement(view.pending ? "span" : "a");
        title.append(link); article.append(title);
        const failure = cardFailureReport(task.id);
        article.append(failure.region);
        // The card's Restore control, built once with the card and kept for its
        // whole life, for the reason the failure report is: it holds a control,
        // and a rebuild would drop the button out from under the caret. It is
        // the card's last child and stays that way — applyCard inserts the
        // optional sections *before* it rather than appending past it — so a
        // task that is deleted, restored and deleted again never moves it.
        const restore = document.createElement("button");
        restore.type = "button";
        restore.className = "restore-button";
        restore.dataset.restoreTask = task.id;
        text(restore, "Restore");
        // The queue's promise is returned rather than dropped, as the drop
        // handler returns it: a listener's result is discarded by the browser,
        // and it is the seam the client tests observe a settled board through.
        restore.addEventListener("click", () => restoreTask(task.id));
        article.append(restore);
        cardParts.set(article, { priority, link, idText, failure, restore, signature: null });
        applyCard(article, task, view);
        return article;
      }
      // Everything a card can be redrawn from. The task ID never changes — it is
      // the reconciliation key — so the copy control and the detail link are
      // built once and only their visible text is rewritten.
      function applyCard(article, task, view) {
        const parts = cardParts.get(article);
        if (!parts) return article;
        // The report is part of what the card is drawn from, so it takes part in
        // the signature: a poll that changes neither the task nor its report
        // still costs one string comparison, and one that clears the report
        // redraws exactly this card.
        const held = taskFailureReports.get(task.id);
        const report = held ? held.card : "";
        const signature = cardSignature(task, view, report);
        if (parts.signature === signature) return article;
        parts.signature = signature;
        article.dataset.priority = task.priority;
        article.dataset.idPrefix = view.idPrefix;
        // Every card drags, including one in the unknown-status region. A card
        // there has no column to be dropped *into* — the region takes no drops —
        // but dragging it into a column is an ordinary status change, and the
        // one the reader most needs: it is the whole recovery from a status this
        // project cannot resolve, expressed the same way every other status
        // change is. The write it sends names a status the project defines, so
        // the mutation boundary has nothing to refuse.
        //
        // A pending stand-in is the one card that does not drag: its save is
        // still in flight, it has no ID a placement could be recorded against,
        // and it retires the moment the real card arrives.
        //
        // The label still distinguishes the two, because "out of the
        // unrecognized status X" is the only place a screen reader is told the
        // card is stranded at all. It asks `lists` — the columns the server
        // rendered onto this page — because `lists` is already what decides
        // which region the card lands in, and a label that disagreed with the
        // region would be the one thing worse than no label.
        //
        // Specifically not the node's current parent, which is the near miss:
        // this runs before card() has inserted the node anywhere, so a
        // parent-derived answer strands every card on its first render.
        // The status decides, which is also why a card carries the right answer
        // with it when a poll moves it between the region and a column.
        const stranded = !lists.has(task.status);
        // The priority half asks `priorities`, the set the form's select is
        // built from, for the reason the status half asks `lists`: the chip and
        // the select must not disagree about whether a value is this project's.
        // A page that published no priorities at all — an older server — has
        // nothing to judge a chip against, and marks none.
        const priorityStranded = priorities.size > 0 && !priorities.has(task.priority);
        article.draggable = !view.pending;
        // The label is where a screen reader hears that the priority is not one
        // of this project's, as it is for the status: the card names itself, so
        // nothing inside it is read on focus. A tombstone's label says it too,
        // because its chip is marked for the readers who can see it.
        const priorityNote = priorityStranded ? ", at the unrecognized priority " + task.priority : "";
        // Cleaned as the server's card template cleans it, so a card the poll
        // redraws names its task the way the card the page loaded with did.
        article.setAttribute("aria-label", withoutDirectionControls(view.pending
          ? "Task " + task.title + ", saving"
          : task.deleted
            // A deleted card drags too, and the only place it can go is a
            // column — which restores it there. That is what the label says,
            // because the status it is filed under is not where it is drawn.
            ? "Restore task " + task.title + " by moving it to a column" + priorityNote
            : (stranded
              ? "Move task " + task.title + " out of the unrecognized status " + task.status
              : "Move task " + task.title + " from " + task.status) + priorityNote));
        // A tombstone has no detail page: the route answers for the tasks the
        // board holds, and a deleted task cannot be edited. The heading keeps
        // the same node either way — an anchor with no href is text, to the
        // reader and to the click handler that only follows a[href] — so a card
        // deleted and restored again is the same node throughout.
        if (!view.pending) {
          if (task.deleted) parts.link.removeAttribute("href");
          else parts.link.href = "/tasks/" + encodeURIComponent(task.id);
        }
        parts.restore.hidden = !task.deleted;
        parts.restore.setAttribute("aria-label", withoutDirectionControls("Restore task " + task.title));
        if (parts.idText) text(parts.idText, view.idPrefix);
        // The chip is the priority's token, not this project's label for it,
        // and the server's card template says the same so the two never
        // disagree about a card the poll redraws. The token is what `--priority`
        // takes, it sits beside the ID prefix a person copies into a command,
        // and the chip is a compact uppercase element that a label chosen as
        // prose would not fit. A project reads its own label in the form's
        // select and on the configuration page, where a name is what is wanted.
        parts.priority.className = "priority priority--" + task.priority; text(parts.priority, task.priority);
        // A token this project does not define is still drawn as itself, and
        // marked: the stylesheet draws a marked chip in the warning family the
        // unknown-status region uses, which no priority is drawn in, and the
        // tooltip says why. A status has a region to be shown in; a priority
        // has no column to be moved out of, so the chip is the place. It is an
        // attribute rather than a class because `priority--` belongs to the
        // project's tokens, and a project may name a priority anything.
        //
        // The judgment is the page's vocabulary, which is the board's rule for
        // statuses too: a poll that moves the task onto a live priority clears
        // the mark on this node, and a vocabulary change raises the reload
        // notice rather than redrawing the board under the reader.
        if (priorityStranded) {
          parts.priority.dataset.priorityUnresolved = "";
          parts.priority.setAttribute("title", "Priority " + task.priority + " is not one of this project's");
        } else {
          delete parts.priority.dataset.priorityUnresolved;
          parts.priority.removeAttribute("title");
        }
        text(parts.link, task.title);
        text(parts.failure.message, report);
        parts.failure.region.dataset.visible = report ? "true" : "false";
        // The optional sections are rebuilt together rather than patched
        // individually, so their order stays fixed by construction instead of by
        // the order in which they happened to appear. They start after the meta
        // row, the title, and the failure report, which are all built once — and
        // they are inserted before the Restore button, which is built once too
        // and is the card's last child, so rebuilding them never moves it.
        [...article.children].slice(3).forEach((child) => { if (child !== parts.restore) child.remove(); });
        // The card's description carries the inline half of the markdown subset
        // and no part of the block half. The clamp above it measures lines of
        // one box: a heading, a list or a code block inside it would either
        // escape the clamp or collapse it, and either way a card's height would
        // start depending on what somebody typed. So bold, italic and code are
        // drawn, a link is drawn as its words, and everything else is drawn as
        // the characters it is — which is exactly what a card drew before.
        // The flag for edits the server's sync set aside, first so it is the
        // first thing read after the title. The task's page shows them.
        const setAside = Array.isArray(view.setAside) ? view.setAside.length : 0;
        if (setAside > 0) { const flag = document.createElement("div"); flag.className = "task-card__set-aside"; flag.dataset.setAsideFlag = ""; text(flag, setAsideCount(setAside)); article.insertBefore(flag, parts.restore); }
        if (task.description) { const description = document.createElement("p"); renderInlineMarkdown(description, task.description); article.insertBefore(description, parts.restore); }
        if (task.labels && task.labels.length) { const labels = document.createElement("div"); labels.className = "labels"; task.labels.forEach((label) => { const item = document.createElement("span"); item.className = "label"; text(item, label); labels.append(item); }); article.insertBefore(labels, parts.restore); }
        // The chip row, drawn only for a task somebody holds — so a board where
        // nobody has claimed anything is the board that has always been drawn,
        // node for node and pixel for pixel.
        //
        // The chips are the server's: presentation.AssignmentChip decided the
        // short form and webui's assignmentRow decided how many of them fit, and
        // this draws what it was handed. A client that re-derived either would be
        // a second copy of both rules, and the cards the server rendered into the
        // page would change the moment the first poll landed.
        const assignees = view.assignmentChips || [];
        if (assignees.length) {
          const row = document.createElement("div");
          row.className = "assignees";
          row.dataset.assignees = "";
          // Named for a reader who cannot see that this row is styled unlike the
          // labels above it. The chips themselves are text and nothing else: a
          // principal is somebody else's asserted identity and may hold anything
          // at all, so it reaches the document through text() like a title does.
          row.setAttribute("role", "group");
          row.setAttribute("aria-label", "Assignees");
          assignees.forEach((chip) => {
            const item = document.createElement("span");
            item.className = "assignee";
            text(item, "@" + chip);
            row.append(item);
          });
          const more = view.moreAssignments || 0;
          if (more > 0) {
            const overflow = document.createElement("span");
            overflow.className = "assignee assignee--more";
            text(overflow, "+" + more + " more");
            row.append(overflow);
          }
          article.insertBefore(row, parts.restore);
        }
        if (view.dependenciesTotal > 0) {
          const progress = document.createElement("div");
          progress.className = "dependency-progress";
          progress.dataset.dependencyProgress = "";
          const count = document.createElement("span");
          text(count, view.dependenciesComplete + " of " + view.dependenciesTotal + " prerequisites " + "complete");
          progress.append(count);
          if (view.waitingOnDependencies) {
            const waiting = document.createElement("strong");
            text(waiting, "Waiting on dependencies");
            progress.append(waiting);
          }
          article.insertBefore(progress, parts.restore);
        }
        return article;
      }
