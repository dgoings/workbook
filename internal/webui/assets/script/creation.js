      // The tag an answer carries, or null. Read defensively because an answer
      // with no headers at all is one that names no document, not a broken one.
      function responseTag(response) {
        const tag = response.headers && typeof response.headers.get === "function" ? response.headers.get("ETag") : null;
        return tag || null;
      }
      function conditionalRead(tag) {
        return tag ? { cache: "no-store", headers: { "If-None-Match": tag } } : { cache: "no-store" };
      }
      // The deleted tasks and the tag naming them, for the caller to adopt
      // together once it knows it has not been superseded. A 304 hands back the
      // tasks that were held when the tag was sent, because those are the
      // document the server has just vouched for.
      async function refreshDeletedTasks() {
        const sentTag = deletedTasksTag;
        const sentTasks = latestDeletedTasks;
        const response = await fetch("/api/tasks?deleted=true", conditionalRead(sentTag));
        if (response.status === 304) {
          // A 304 to a read that named nothing vouches for nothing.
          if (!sentTag) throw new Error("Deleted task data could not be loaded.");
          return { tasks: sentTasks, tag: sentTag };
        }
        const document = await response.json();
        if (!response.ok || document.format !== "workbook.tasks" || document.version !== 1 || !Array.isArray(document.tasks)) {
          throw new Error(document.error && document.error.message || "Deleted task data could not be loaded.");
        }
        return { tasks: document.tasks, tag: responseTag(response) };
      }
      async function refreshRelationshipContext(controller, generation) {
        try {
          const deleted = await refreshDeletedTasks();
          if (generation !== relationshipRefreshGeneration || activeDependencyController !== controller) {
            return { status: "superseded" };
          }
          latestDeletedTasks = deleted.tasks;
          deletedTasksTag = deleted.tag;
          text(controller.status, "");
          controller.update();
          return { status: "applied" };
        } catch (error) {
          if (generation === relationshipRefreshGeneration && activeDependencyController === controller) {
            const message = error instanceof Error ? error.message : "Deleted task data could not be loaded.";
            controller.update();
            text(controller.status, message);
            return { status: "failed", message };
          }
          return { status: "superseded" };
        }
      }
      function reconciledRelationshipTasks() {
        const deletedByID = new Map();
        latestTasks.forEach((task) => {
          if (task.deleted) deletedByID.set(task.id, task);
        });
        latestDeletedTasks.forEach((task) => deletedByID.set(task.id, task));
        const active = latestTasks.filter((task) => !task.deleted && !deletedByID.has(task.id));
        return { active, deleted: [...deletedByID.values()] };
      }
      function dependencyState(taskID) {
        const snapshot = reconciledRelationshipTasks();
        const current = snapshot.active.find((task) => task.id === taskID);
        if (!current) return null;
        const known = new Map([...snapshot.active, ...snapshot.deleted].map((task) => [task.id, task]));
        const dependsOn = (current.dependencies || []).map((id) => ({ id, task: known.get(id) || null }));
        // Blocks lists live tasks only. A deleted task that depended on this one
        // still carries the edge, but on its own record, which a tombstone does
        // not let anyone change; and the edge is inert, because readiness skips
        // a deleted task before it reads its dependencies. Listing it offered a
        // row nobody could act on, so it comes back when the task is restored.
        const blocks = snapshot.active
          .filter((task) => task.id !== taskID && (task.dependencies || []).includes(taskID))
          .map((task) => ({ id: task.id, task }));
        return { current, dependsOn, blocks };
      }
      function newRelationshipDraftState() {
        return {
          dependsOn: new Map(),
          blocks: new Map()
        };
      }
      function hasRelationshipDrafts(draftState) {
        return draftState &&
          (draftState.dependsOn.size > 0 || draftState.blocks.size > 0);
      }
      function hasAssignmentFailures(recovery) {
        return Boolean(recovery.assignmentFailures && recovery.assignmentFailures.length);
      }
      // Hands what a create could not write to the task's own page: the rows
      // its sidebar retries, and the sentence that says why they are there.
      function stageCreatedTaskDrafts(recovery) {
        if (hasRelationshipDrafts(recovery.failures)) {
          pendingRelationshipDrafts.set(recovery.taskID, recovery.failures);
        }
        if (hasAssignmentFailures(recovery)) {
          pendingAssignmentDrafts.set(recovery.taskID, recovery.assignmentFailures);
        }
      }
      function applyCreatedTaskRecovery(recovery) {
        if (!recovery) return;
        stageCreatedTaskDrafts(recovery);
        const said = [];
        if (hasRelationshipDrafts(recovery.failures)) {
          said.push("Task created, but some relationships were not saved.");
          pendingRelationshipRecoveryFocus = { taskID: recovery.taskID };
        }
        if (hasAssignmentFailures(recovery)) {
          // The caret goes to the first failed row, as it does for a failed
          // dependency, unless a dependency failed too: the relationships come
          // first in the sidebar, so their Retry is the first one to reach.
          if (!said.length) pendingRelationshipRecoveryFocus = { taskID: recovery.taskID, assignments: true };
          said.push(said.length
            ? "Some assignments were not saved either."
            : "Task created, but some assignments were not saved.");
        }
        if (said.length || recovery.warnings.length) {
          pendingTaskMessages.set(recovery.taskID, [...said, ...recovery.warnings]);
        }
        pendingDetailRelationshipContextTaskID = recovery.taskID;
      }
      // Where a finished create leaves the user, and what has to be staged for
      // the route it lands on. Saving a new task returns to the board, or
      // re-arms a clean New Task form when "Create more" is on; either way the
      // task that was just saved is behind the user, not in front of them.
      //
      // The exception is a create that has something to report: a warning, or a
      // relationship the client could not write. Those messages and their retry
      // actions only exist on the task's own detail route, so that create opens
      // the task instead of dropping what it has to say.
      function createdTaskLanding(recovery) {
        if (recovery.warnings.length || hasRelationshipDrafts(recovery.failures) ||
            hasAssignmentFailures(recovery)) {
          return { path: taskPath(recovery.taskID), stageRecovery: true, replace: false };
        }
        if (createMoreTasks) {
          return { path: newTaskPath(recovery.status), stageRecovery: false, replace: true, focusTitle: true };
        }
        // The board the reader left, filters and Deleted column and all, rather
        // than the bare board: they filed this task from a board they had narrowed
        // down, and landing them on an unfiltered one would throw away the state
        // they were working in and make them set it up again.
        return { path: boardAddress(deletedColumnShown, boardFilters), stageRecovery: false, replace: false, focusTaskID: recovery.taskID };
      }
      // A save that re-arms the New Task form destroys the node the user was
      // standing on, and a browser blurs an element on its way out of the
      // document. The replacement claims the caret so a run of tasks stays on
      // the keyboard. A New Task route the user opened themselves is left
      // alone: nothing was taken from them there.
      function focusRearmedNewTask(form) {
        if (!pendingNewTaskFocus) return;
        pendingNewTaskFocus = false;
        const title = form.querySelector("#task-title");
        if (title && typeof title.focus === "function") title.focus();
      }
      // The board landing loses the caret the same way, and has less to fall
      // back on: the form is gone, no card was focused before the create, and
      // restoreCardFocus only returns focus to a card that already had it. So a
      // keyboard user would land on the document body and have to tab in from
      // the top. The card that was just created takes the caret instead, which
      // is also the only confirmation this path gives a screen reader: the card
      // announces the task's title and column. A board the user reached any
      // other way is left alone.
      function focusCreatedCard() {
        const taskID = pendingBoardFocusTaskID;
        if (!taskID) return;
        pendingBoardFocusTaskID = null;
        const node = findCardNode(taskID);
        if (node && typeof node.focus === "function") node.focus();
      }
      // The single exit from a durable create. Everything before it decides what
      // happened; this decides where that leaves the user.
      function finishCreatedTask(recovery) {
        const landing = createdTaskLanding(recovery);
        if (landing.stageRecovery) applyCreatedTaskRecovery(recovery);
        createdTaskRecovery = null;
        leaveForLanding(landing);
      }
      function leaveForLanding(landing) {
        // Staged before the navigation, because the render it triggers is what
        // holds the new control. Written on every landing, not only the ones
        // that want focus, so a landing that does not cannot inherit a request
        // an earlier create left behind.
        pendingNewTaskFocus = Boolean(landing.focusTitle);
        pendingBoardFocusTaskID = landing.focusTaskID || null;
        navigate(landing.path, { replace: landing.replace });
      }
      // An optimistic create leaves before anything is known about it, so it
      // takes the landing createdTaskLanding gives a create with nothing to
      // report. What it turns out to have to say is reported where the user
      // ends up rather than by moving them again.
      function optimisticCreateLanding(creation) {
        if (createMoreTasks) {
          return { path: newTaskPath(creation.task.status), replace: true, focusTitle: true };
        }
        // The board the reader left, for the reason createdTaskLanding lands
        // there: this is the same landing, taken before the server has answered.
        return { path: boardAddress(deletedColumnShown, boardFilters), replace: false, focusTaskID: creation.key };
      }
      // The task a create draws before the server has named one. It carries no
      // head and no relationships, because those are the server's answers.
      function optimisticCreationTask(key, values) {
        return {
          id: key,
          title: values.title,
          description: values.description,
          status: values.status,
          priority: values.priority,
          labels: values.labels,
          dependencies: [],
          deleted: false
        };
      }
      // Save now, land now. The task is drawn from what was typed while the POST
      // is still open, so a create costs a render rather than a round trip.
      function createOptimistically(values) {
        const key = "pending-create-" + (++creationSequence);
        const creation = {
          key,
          values,
          task: optimisticCreationTask(key, values),
          taskID: "",
          // The board as it stood before this create. A task that was already
          // here cannot be the one this create is making, however exactly it
          // matches — someone saving a second copy of a task they already have
          // still gets a card of their own to watch.
          priorIDs: new Set(latestTasks.map((task) => task.id))
        };
        pendingCreations.set(key, creation);
        // Drawn before the navigation: the board landing hands the caret to this
        // card, and it can only be given to a card that exists.
        rerender();
        leaveForLanding(optimisticCreateLanding(creation));
        return settleCreation(creation);
      }
      // The returned promise settles once the create is durable and the board
      // has been read back from the server. Nothing in the browser awaits it —
      // that is the entire point — but it is the seam the client tests use to
      // observe a settled create.
      async function settleCreation(creation) {
        try {
          const document = await mutateTask("POST", "/api/tasks", creation.values);
          creation.taskID = document.task.id;
          const warnings = mutationWarnings(document);
          // Read before anything redraws, because a redraw is what retires the
          // stand-in card, and a card that leaves the document takes the
          // caret with it.
          const heldCaret = focusedCardID() === creation.key;
          // Drawn now, from the model already held, because the ID is the last
          // way to tie the stand-in to its task and the board may already carry
          // it: a poll that outran this POST and could not match it by value.
          // The refresh below cannot be relied on to finish that job — a board
          // the server has nothing new for is answered 304 and redraws nothing.
          // Where the model does not carry the task yet, this changes nothing
          // and the refresh, which then has a new document to read, retires it.
          rerender();
          await currentRefreshResult(refresh());
          if (heldCaret) restoreCardFocus(creation.taskID);
          if (warnings.length) reportCreatedTaskWarnings(creation.taskID, warnings);
        } catch (error) {
          // Read before the stand-in leaves the document, for the same reason
          // the durable path reads it: a card that is removed takes the caret
          // with it, and here there is no replacement card to hand it to.
          const heldCaret = focusedCardID() === creation.key;
          pendingCreations.delete(creation.key);
          rerender();
          const restore = reportRefusedCreation(creation, error);
          // The report is now the only copy of what was typed, so the keyboard
          // lands on the control that gets it back rather than on the document
          // body, a full tab run away from it.
          if (heldCaret && restore && typeof restore.focus === "function") restore.focus();
        }
      }
      function reportCreatedTaskWarnings(taskID, warnings) {
        // Staged as well as reported: the task's own form is where these
        // messages belong, and the notice is how someone who is no longer
        // standing there learns to go and read them.
        pendingTaskMessages.set(taskID, warnings);
        const open = document.createElement("a");
        open.href = taskPath(taskID);
        text(open, "Open task");
        showNotice("warning", "Task created. " + warnings.join(" "), open);
      }
      function reportRefusedCreation(creation, error) {
        return reportRecoverableDraft("error",
          nameDraft(creation.values) + " was not saved. " + creationFailureMessage(error),
          creation.values,
          creationFailureMessage(error));
      }
      // A create that outlived the form it was typed into. Only a create that
      // staged relationships can: one that staged none leaves the form the moment
      // Save is pressed, and reportRefusedCreation above already reports it from
      // wherever the reader landed. This one was still waiting on the ID the
      // server assigns when the reader gave up on it, and what left with the node
      // is not only the outcome but everything typed into it — which is the whole
      // difference from a detached save. A save's edits belong to a task that
      // still exists and cannot be handed back anywhere; a create's draft can be,
      // so it is, through the control a refused create has always carried.
      //
      // Nothing is staged on a task's form the way a detached save's message is.
      // The server refused to make the task, so there is no form to stage it on
      // and no route to offer back to; this report is the only copy of the draft
      // left, and only a dismissal or a restore takes it away.
      //
      // What the control cannot carry is the relationships staged beside those
      // fields: restoring renders a New Task form, and a New Task form starts
      // with an empty draft state. Said in both places rather than left to be
      // noticed, because a reader who restored and saved would otherwise create
      // the task without the prerequisite they staged for it.
      //
      // The reason goes last, where every detached report puts it: a server
      // message is a Go error, lower case and unpunctuated, and spliced between
      // two sentences of this client's own it runs straight into the one after.
      function reportDetachedCreationFailure(values, error) {
        reportRecoverableDraft("error",
          nameDraft(values) + " was not saved. The create was still open when you left the " +
            "form, and what you typed is kept here; the relationships staged with it are not. " +
            creationFailureMessage(error),
          values,
          "The relationships staged with this draft are not in this form; add them again if " +
            "you still want them. " + creationFailureMessage(error));
      }
      // A New Task form that was holding attachments for a task it had already
      // made is being replaced, and the files go with it.
      //
      // Every other draft this client can lose is offered back through the
      // notice; these cannot be, so the notice says what was lost, names the
      // task it belonged to, and points at the one place they can still be
      // attached. Reported as an error rather than a warning: something the
      // reader asked for is not going to happen, and the form that said so is
      // gone.
      function reportAbandonedAttachments(held) {
        const open = document.createElement("a");
        open.href = taskPath(held.taskID);
        text(open, "Open task");
        showNotice("error",
          "Task " + held.taskID + " was created, but " +
          (held.names.length === 1
            ? "one attachment was not attached to it"
            : held.names.length + " attachments were not attached to it") +
          " and left with the form: " + held.names.join(", ") +
          ". Attach them from the task itself.", open);
      }
      // A draft the user is still writing, set aside so a restore could have the
      // route. Nothing failed here, which is why it is reported as a warning and
      // says where the draft went rather than why it stopped.
      function reportDisplacedDraft(values) {
        reportRecoverableDraft("warning",
          nameDraft(values) + " was set aside to restore another draft.",
          values,
          "");
      }
      // A report that holds a draft: the message, and the one control that puts
      // the values back into a form. Returns that control, because a caller that
      // took the caret away to build this has to give it somewhere to land.
      function reportRecoverableDraft(kind, message, values, formMessage) {
        const restore = document.createElement("button");
        restore.type = "button";
        text(restore, "Restore draft");
        const report = showNotice(kind, message, restore);
        restore.addEventListener("click", () => {
          dismissNotice(report);
          restoreDraft(values, formMessage);
        });
        return restore;
      }
      // The title is what the user recognizes a draft by, and a create the
      // server refused for having none still has to be named something.
      function nameDraft(values) {
        return values.title ? "“" + values.title + "”" : "The task";
      }
      // A refusal costs nothing that was typed: the values go back into a New
      // Task form, which is where the message about them belongs too.
      function restoreRefusedCreation(creation, error) {
        restoreDraft(creation.values, creationFailureMessage(error));
      }
      // Restoring renders a New Task form over the current route, and a route
      // render replaces that node whole. With Create more on, the node it
      // replaces is usually the next task being typed, so what is in it is
      // lifted into a report of its own first: a restore that destroyed a draft
      // would only move the loss these reports exist to prevent.
      function restoreDraft(values, message) {
        preserveOpenNewTaskDraft();
        pendingNewTaskDraft = values;
        pendingNewTaskMessage = message;
        pendingNewTaskFocus = true;
        navigate(newTaskPath(values.status));
      }
      function preserveOpenNewTaskDraft() {
        if (!readOpenNewTaskValues) return;
        const values = readOpenNewTaskValues();
        // A form carrying only the status and priority it opened with is the
        // empty form, not a draft: reporting it would be noise standing between
        // the user and the reports that do hold something.
        if (!values.title && !values.description && values.labels.length === 0) return;
        reportDisplacedDraft(values);
      }
      function creationFailureMessage(error) {
        return error instanceof Error ? error.message : "Task creation failed.";
      }
      // The report of last resort. A save that has already returned to the board
      // has no form left to report into, and a refused board change whose card
      // the board no longer draws has no card left to report on. This surface
      // outlives both: it sits outside the routed region, and only a person
      // clears it.
      //
      // Reports stack rather than replace, because one of them can be holding
      // the only copy of a refused task. Nothing but a click removes that.
      //
      // Each report's own Dismiss button is kept beside it rather than returned
      // with it, so a caller that has to hand the caret somewhere can find it
      // without every other caller learning a new return shape: a report
      // written because the node holding the caret was removed has to have
      // somewhere to put that caret.
      const noticeDismissControls = new WeakMap();
      function showNotice(kind, message, action) {
        if (!notice) return null;
        const report = document.createElement("div");
        report.className = "notice__report";
        report.dataset.kind = kind;
        const copy = document.createElement("p");
        text(copy, message);
        const dismiss = document.createElement("button");
        dismiss.type = "button";
        text(dismiss, "Dismiss");
        dismiss.addEventListener("click", () => dismissNotice(report));
        report.append(...(action ? [copy, action, dismiss] : [copy, dismiss]));
        notice.append(report);
        notice.hidden = false;
        noticeDismissControls.set(report, dismiss);
        return report;
      }
      function focusNoticeReport(report) {
        const dismiss = report && noticeDismissControls.get(report);
        if (dismiss && typeof dismiss.focus === "function") dismiss.focus();
      }
      function dismissNotice(report) {
        if (!notice || !report) return;
        report.remove();
        if (notice.children.length === 0) notice.hidden = true;
      }
      function focusPendingRelationshipRecovery(taskID, controller, generation) {
        const request = pendingRelationshipRecoveryFocus;
        if (!request || request.taskID !== taskID) return;
        Promise.resolve().then(() => {
          if (pendingRelationshipRecoveryFocus !== request) return;
          if (request.assignments) {
            pendingRelationshipRecoveryFocus = null;
            const retry = openThreadPanels && openThreadPanels.taskID === taskID &&
              openThreadPanels.firstAssignmentRetry ? openThreadPanels.firstAssignmentRetry() : null;
            if (retry && main.contains(retry)) retry.focus();
            return;
          }
          if (generation !== relationshipRefreshGeneration ||
              !ownsDependencyController(controller) ||
              !main.contains(controller.region)) {
            pendingRelationshipRecoveryFocus = null;
            return;
          }
          const action = controller.recoveryActions[0];
          pendingRelationshipRecoveryFocus = null;
          if (action && typeof action.focus === "function") action.focus();
        });
      }
      function showCreatedTaskRefreshFailure(result, retryOperation) {
        text(result, "Task created durably, but the latest task state could not be refreshed. ");
        const retry = document.createElement("button");
        retry.type = "button";
        retry.className = "save-button";
        text(retry, "Retry");
        retry.addEventListener("click", async () => {
          retry.disabled = true;
          try {
            await retryOperation();
          } finally {
            retry.disabled = false;
          }
        });
        result.append(retry);
      }
      function draftEntry(task) {
        return {
          id: task.id,
          task: {
            id: task.id,
            title: task.title,
            status: task.status,
            priority: task.priority,
            deleted: false
          },
          error: ""
        };
      }
      function draftRelationshipOperations(taskID, draftState) {
        const operations = [];
        draftState.dependsOn.forEach((entry) => {
          operations.push({
            direction: "dependsOn",
            dependentID: taskID,
            prerequisiteID: entry.id,
            entry
          });
        });
        draftState.blocks.forEach((entry) => {
          operations.push({
            direction: "blocks",
            dependentID: entry.id,
            prerequisiteID: taskID,
            entry
          });
        });
        return operations;
      }
      async function persistRelationshipDrafts(taskID, draftState) {
        const failures = newRelationshipDraftState();
        const warnings = [];
        for (const operation of draftRelationshipOperations(taskID, draftState)) {
          const path = "/api/tasks/" +
            encodeURIComponent(operation.dependentID) +
            "/dependencies/" +
            encodeURIComponent(operation.prerequisiteID);
          try {
            const document = await mutateTask("PUT", path);
            (document.warnings || []).forEach((warning) =>
              warnings.push(warning.message));
          } catch (error) {
            const failed = {
              ...operation.entry,
              error: error instanceof Error
                ? error.message
                : "Dependency update failed."
            };
            failures[operation.direction].set(operation.entry.id, failed);
          }
        }
        return { failures, warnings };
      }
      function relationshipCandidates(taskID, direction, draftState = null) {
        const snapshot = reconciledRelationshipTasks();
        const activeTasks = snapshot.active;
        const current = taskID ? activeTasks.find((task) => task.id === taskID) : null;
        if (taskID && !current) return [];
        const excluded = new Set();
        if (taskID) excluded.add(taskID);
        if (taskID && direction === "depends-on") {
          (current.dependencies || []).forEach((id) => excluded.add(id));
        }
        if (taskID && direction === "blocks") {
          activeTasks.forEach((task) => {
            if ((task.dependencies || []).includes(taskID)) excluded.add(task.id);
          });
        }
        if (draftState) {
          const draftMap = direction === "depends-on"
            ? draftState.dependsOn
            : draftState.blocks;
          draftMap.forEach((_entry, id) => excluded.add(id));
        }
        return activeTasks.filter((task) => !excluded.has(task.id));
      }
      function relationshipListboxPlacement(anchor, bounds, desiredHeight = 288, gap = 5) {
        const belowTop = Math.max(bounds.top, anchor.bottom + gap);
        const aboveBottom = Math.min(bounds.bottom, anchor.top - gap);
        const belowSpace = Math.max(0, bounds.bottom - belowTop);
        const aboveSpace = Math.max(0, aboveBottom - bounds.top);
        const side = belowSpace >= aboveSpace ? "below" : "above";
        const maxHeight = Math.min(desiredHeight, side === "below" ? belowSpace : aboveSpace);
        if (side === "below") {
          return {
            side,
            top: belowTop,
            bottom: belowTop + maxHeight,
            maxHeight
          };
        }
        return {
          side,
          top: aboveBottom - maxHeight,
          bottom: aboveBottom,
          maxHeight
        };
      }
