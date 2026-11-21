      // The server's answer when it had no turn for a request: 503 with a
      // Retry-After, which the board sends only from its cap on how much work it
      // does at once and only before it has started on the request. Nothing was
      // read or recorded, so the very same request may be sent again. A 503
      // without the header is somebody else's — a proxy with no board behind it
      // — and says nothing about whether the change was made, so it is not
      // retried.
      function busyRefusal(response) {
        return Boolean(response && response.status === 503 && response.headers &&
          typeof response.headers.get === "function" && response.headers.get("Retry-After") !== null);
      }
      // How long the server asked for, in milliseconds, at least one second.
      function busyRetryDelay(response) {
        const seconds = Number(response.headers.get("Retry-After"));
        return Math.max(1, Number.isFinite(seconds) ? seconds : 2) * 1000;
      }
      function pause(milliseconds) {
        return new Promise((resolve) => { window.setTimeout(resolve, milliseconds); });
      }
      // How many of this page's changes are waiting on the server's cap right
      // now, which is what the busy notice is drawn from.
      let busyWrites = 0;
      function renderBusyNotice() {
        if (busyNotice) busyNotice.hidden = busyWrites === 0;
      }
      // How many times a refused change is sent again before the refusal is
      // reported, each wait twice the one before it: with the board's two
      // seconds, that is 2, 4 and 8 seconds, so a change is kept for about a
      // quarter of a minute of somebody else's flood before the reader is told
      // it did not go.
      const BUSY_RETRIES = 3;
      // Every write this page sends goes through here, so a change refused only
      // because the server was busy is kept and sent again rather than lost.
      // The card stays drawn as changed while it waits — the intent is still at
      // the head of its queue — and the notice says why nothing has confirmed
      // it yet. When the retries run out, the last refusal is handed back like
      // any other, and the caller reports it where it reports every refusal.
      async function sendUnlessBusy(path, options) {
        let waiting = false;
        try {
          for (let attempt = 0; ; attempt += 1) {
            const response = await fetch(path, options);
            if (!busyRefusal(response) || attempt >= BUSY_RETRIES) return response;
            if (!waiting) {
              waiting = true;
              busyWrites += 1;
              renderBusyNotice();
            }
            await pause(busyRetryDelay(response) * 2 ** attempt);
          }
        } finally {
          if (waiting) {
            busyWrites -= 1;
            renderBusyNotice();
          }
        }
      }
      async function mutateTask(method, path, values) {
        // Every mutation names its JSON media type, body or not: the server
        // rejects mutations without it, because an absent or form-safe
        // Content-Type is what a cross-site request looks like.
        const options = { method, headers: { "Content-Type": "application/json" } };
        if (values !== undefined) {
          options.body = JSON.stringify(values);
        }
        const response = await sendUnlessBusy(path, options);
        const document = await response.json();
        if (!response.ok || document.format !== "workbook.task-mutation" ||
            document.version !== 1 || !document.task) {
          const failure = new Error(document.error && document.error.message || "Task update failed.");
          // The category is what separates "your view was stale, reload" from
          // "that was not a legal change". Without it every failure looks the
          // same to the caller.
          failure.category = document.error && document.error.category || "";
          // Still refused by the server's cap once every resend had gone, which
          // a reporter words as the board being busy rather than as the change
          // being wrong.
          failure.busy = busyRefusal(response);
          throw failure;
        }
        return document;
      }
      function mutationWarnings(document) {
        return Array.isArray(document.warnings)
          ? document.warnings.map((warning) => warning.message)
          : [];
      }
      // Pending intents let the board show a change before it is durable.
      //
      // latestTasks is replaced wholesale by every poll, so intents are folded
      // in at render time rather than written into it. That is the whole reason
      // the overlay sits where it does: the one-second refresh can keep
      // overwriting the model and pending work survives untouched.
      function intentEntry(taskID, head) {
        let entry = pendingIntents.get(taskID);
        if (!entry) {
          // draining says a loop owns this queue, which is not the same as a
          // request being open: between two sends the loop still owns it. The
          // distinction matters because the head is re-based between sends.
          // settled is that loop's promise, so anything that has to wait for
          // this queue waits on the loop rather than on a timer.
          entry = { queue: [], draining: false, settled: null, head: head || "" };
          pendingIntents.set(taskID, entry);
        }
        return entry;
      }
      function projectTasks(tasks) {
        const projected = pendingIntents.size === 0 ? tasks : tasks.map((task) => {
          const entry = pendingIntents.get(task.id);
          if (!entry || entry.queue.length === 0) return task;
          return entry.queue.reduce((current, intent) => intent.apply(current), task);
        });
        if (pendingCreations.size === 0) return projected;
        return projected.concat(pendingCreationTasks(projected));
      }
      // A create is drawn until the board actually carries the task it made. The
      // POST returning is not enough: the model is replaced wholesale by every
      // poll, and one that left before the create landed would take the card
      // away again. Retiring on arrival rather than on confirmation is also what
      // makes a failed refresh harmless — the next poll finishes the job. It
      // still does when polls are answered 304: a poll is a 304 only when the
      // model already holds what the server does, and a model that already
      // carries the task was drawn either when it was read or, if the stand-in
      // could only be matched by ID, when settleCreation learned the ID.
      function pendingCreationTasks(tasks) {
        const landed = new Set(tasks.map((task) => task.id));
        // One task retires at most one stand-in, so two identical creates in
        // flight keep a card each until the board carries both.
        const claimed = new Set();
        const drawn = [];
        pendingCreations.forEach((creation, key) => {
          const arrived = creationArrival(creation, tasks, landed, claimed);
          if (!arrived) {
            drawn.push(creation.task);
            return;
          }
          claimed.add(arrived);
          pendingCreations.delete(key);
          // The node this retirement drops is often the one holding the caret,
          // and render() captures focus before it goes. Only the retirement
          // knows which card replaced it, so it is what names the heir.
          if (focusedCardID() === key) retiredCreationHeir = { key, taskID: arrived };
        });
        return drawn;
      }
      // The ID of the task a stand-in is now standing in for, or "" while it is
      // still the only thing drawing the create.
      function creationArrival(creation, tasks, landed, claimed) {
        if (creation.taskID) return landed.has(creation.taskID) ? creation.taskID : "";
        // The POST has not named the task yet and the board can already carry
        // it: the server commits a create and then publishes it, and a poll that
        // lands in between reads the committed task straight out of the
        // repository. What the two have in common is the values, so a task that
        // arrived after this create started carrying exactly what was typed
        // retires the stand-in instead of being drawn beside it and counted
        // twice.
        const arrival = tasks.find((task) =>
          !claimed.has(task.id) &&
          !creation.priorIDs.has(task.id) &&
          matchesCreatedValues(task, creation.values));
        return arrival ? arrival.id : "";
      }
      // Compared the way the server stores them: a title is trimmed on the way
      // in and labels are deduplicated and sorted, so an untrimmed title or a
      // repeated label would otherwise never match the task it made.
      function matchesCreatedValues(task, values) {
        return task.title === values.title.trim() &&
          task.status === values.status &&
          task.priority === values.priority &&
          (task.description || "") === (values.description || "") &&
          canonicalLabels(task.labels) === canonicalLabels(values.labels);
      }
      // Joined on a character the label input cannot hold, so no two label
      // sets can flatten to the same string.
      function canonicalLabels(labels) {
        return [...new Set(labels || [])].sort().join("\n");
      }
      // What the board knows about a task the server has not answered for: none
      // of what the server derives. pending is what draws the card as a
      // stand-in rather than as a task.
      const pendingCreationView = {
        idPrefix: "",
        dependenciesTotal: 0,
        dependenciesComplete: 0,
        waitingOnDependencies: false,
        // A task nobody has accepted yet is a task nobody can have been assigned
        // to: assignment is a mutation of a stored task, and this one has no ID
        // to record one against.
        assignmentChips: [],
        moreAssignments: 0,
        assignments: [],
        pending: true
      };
      // What the board can say about a task the server has derived nothing for.
      //
      // Every rendered task is supposed to have a view — /api/tasks is refused
      // outright if its presentation does not cover its tasks — but there is one
      // seam where the two come from different reads. A confirmation puts its
      // task back into the model (replaceProjectedTask), and if the poll before
      // it had already taken that task out — another clone deleted it while this
      // write was in flight — the presentation left behind does not name it. The
      // renderer then read `undefined.idPrefix` and threw, which killed the poll
      // loop: the board froze on the last frame and stayed frozen, over a card
      // that was about to disappear anyway.
      //
      // So the gap is filled rather than allowed to reach the renderer. The
      // prefix falls back to the whole task ID, which is always correct and
      // merely longer than the shortest unique one the server computes. The
      // dependency counts fall back to none: the client can count a task's own
      // dependencies but cannot tell which are done — that needs every other
      // task's status and the vocabulary's done tag — and "0 of 3 complete" over
      // three finished prerequisites is a worse answer than no row for one poll.
      // The next poll derives the real view or takes the card away.
      function underivedView(task) {
        return {
          idPrefix: task.id,
          dependenciesTotal: 0,
          dependenciesComplete: 0,
          waitingOnDependencies: false,
          // No chips for the poll this gap lasts, on the dependency counts'
          // reasoning: the short form of a principal and a row's withdrawal are
          // the server's, and a row this client invented from the raw values
          // would disagree with the row beside it. The next poll
          // derives the real view or takes the card away.
          assignmentChips: [],
          moreAssignments: 0,
          assignments: []
        };
      }
      function projectPresentation(presentation, tasks) {
        // A pending creation has its own stand-in view and must keep it: pending
        // is what draws it as a stand-in rather than as a task.
        const underived = tasks.filter((task) => !presentation.has(task.id) && !pendingCreations.has(task.id));
        if (pendingCreations.size === 0 && underived.length === 0) return presentation;
        const projected = new Map(presentation);
        underived.forEach((task) => projected.set(task.id, underivedView(task)));
        pendingCreations.forEach((_creation, key) => projected.set(key, pendingCreationView));
        return projected;
      }
      // Which of a projected model the columns actually draw. Everything that
      // renders the board goes through this, so the filters cannot be applied on
      // one path and forgotten on another.
      //
      // known is the model the server answered with. A create's stand-in is not in
      // it and is never filtered away: a card the reader just made must not vanish
      // under them because it does not match a search they typed before they made
      // it. Only a task the server has answered for can be filtered out.
      function shownTasks(projected, known, search) {
        if (!filtersActive(boardFilters)) return projected;
        return projected.filter((task) => !known.has(task.id) || passesFilters(task, boardFilters, search));
      }
      function rerender() {
        if (!tasksLoaded) return;
        // projectTasks runs first and retires the creations the board now
        // carries, so the presentation it is paired with names exactly the
        // stand-ins that survived.
        const tasks = projectTasks(latestTasks);
        // Folded once here and handed to both halves of the render, because both
        // ask the same question of the same text: which cards the columns draw,
        // and what each chooser option would leave.
        const search = foldedSearch(tasks);
        const shown = shownTasks(tasks, new Set(latestTasks.map((task) => task.id)), search);
        // Both readings are handed over — what the columns draw, and everything
        // the board holds — because a column that says three of nine has to be
        // told the nine. The presentation is derived over everything, so a task
        // the filters hide still has its view for whatever else is reading one.
        render(shown, projectPresentation(latestPresentation, tasks), tasks, search);
      }
      function replaceProjectedTask(task) {
        // The model stops being the document the board's tag names, so the next
        // poll has to read it in full rather than be told it already holds it.
        boardValidator = null;
        const index = latestTasks.findIndex((candidate) => candidate.id === task.id);
        if (index < 0) latestTasks = latestTasks.concat([task]);
        else latestTasks = latestTasks.map((candidate, at) => (at === index ? task : candidate));
      }
      // One task's intents are sent one at a time, because each carries the head
      // the one before it returned. Independent tasks drain concurrently, so a
      // slow write to one card never stalls the rest of the board.
      // The returned promise settles once this task's queue is empty. Nothing
      // on the board awaits it — an event listener's result is discarded — but
      // it is the seam the client tests use to observe a settled board without
      // the UI ever waiting on a round trip, and the seam a status change waits
      // on before it is allowed to go: a vocabulary change is not queued, and
      // sending one past a task write composed against the columns on screen is
      // the one ordering this client cannot recover from.
      function queueIntent(taskID, head, intent) {
        const entry = intentEntry(taskID, head);
        entry.queue.push(intent);
        // The reader has answered a standing refusal by changing this card
        // again, so the refusal goes: the report says the card shows the
        // version the server holds, which stops being true the moment this
        // intent draws an optimistic one over it. If this change is refused in
        // turn, it comes back saying so.
        //
        // It is the reader's next decision that clears a report, not the next
        // confirmation. An intent already queued when the refusal arrived was
        // never an answer to it — a placement refused as illegal while a second
        // placement of the same card succeeds is still news — so a confirmation
        // leaves the report alone.
        taskFailureReports.delete(taskID);
        rerender();
        return drainIntents(taskID);
      }
      // The Restore button on a deleted card, and the one restore that names no
      // destination: the task comes back to the status it was deleted from,
      // which is what the bare verb has always meant. It is a queued intent
      // like every other board change — optimistic paint, one write at a time
      // per task, re-base on a stale head, refusal reported on the card — and
      // the head it carries is the only thing in the body.
      //
      // The body-less branch is defensive rather than a case a server produces:
      // every task this board draws comes from /api/tasks with a head, so
      // `expectedHead` is populated in practice. It exists because the route
      // accepts no body at all and this is the one intent whose body would
      // otherwise be empty — an empty expectation opts out of the head check
      // on the server, so `{"expectedHead":""}` would behave identically; the
      // bare verb is simply the shape that sends no noise the route ignores.
      function restoreTask(taskID) {
        const observed = latestTasks.find((task) => task.id === taskID);
        // The button is drawn on every card and shown on the deleted ones, so
        // this is the belt to that brace: a task the board no longer sees as
        // deleted has nothing to restore.
        if (!observed || !observed.deleted) return Promise.resolve();
        return queueIntent(taskID, observed.head, {
          apply: (task) => Object.assign({}, task, { deleted: false }),
          send: (expectedHead) => mutateTask(
            "POST",
            "/api/tasks/" + encodeURIComponent(taskID) + "/restore",
            expectedHead ? { expectedHead } : undefined
          ),
        });
      }
      function drainIntents(taskID) {
        const entry = pendingIntents.get(taskID);
        if (!entry) return Promise.resolve();
        // A loop already running is the answer to "when is this queue empty",
        // so a second caller is handed the promise that loop settles rather
        // than one that resolves immediately — which is what this returned
        // before anything waited on it, and would have let a status change go
        // out over a board that was still writing.
        if (entry.draining) return entry.settled;
        entry.draining = true;
        entry.settled = drainQueue(entry, taskID);
        return entry.settled;
      }
      async function drainQueue(entry, taskID) {
        try {
          while (entry.queue.length > 0) {
            const intent = entry.queue[0];
            try {
              const confirmed = await intent.send(entry.head);
              entry.queue.shift();
              if (confirmed.task && confirmed.task.head) entry.head = confirmed.task.head;
              replaceProjectedTask(confirmed.task);
            } catch (error) {
              // Drop the intent that failed and keep the ones queued behind
              // it: those were separate decisions, and discarding a later
              // change because an earlier one was refused is the clobbering
              // this queue exists to avoid. One that genuinely depended on the
              // failed intent is refused in turn and rolls back on its own.
              entry.queue.shift();
              // Read before the refresh below rather than inside the report:
              // the likeliest refusal is a task another clone deleted, and that
              // refresh is what takes it out of the model. A report that has to
              // name a task it can no longer look up names nothing.
              const subject = nameTask(taskID);
              // Whether the board these reports are written against is the
              // server's. Only a refresh that read it makes it so, and only a
              // stale write forces one. Both surfaces below are written from the
              // same model and so turn on the same answer: the card, and the
              // detail form if this task's is open.
              let boardRead = true;
              if (error && error.category === "stale-write") {
                // The refusal means this queue is holding a head the server
                // has moved past. Waiting for the refresh it forces and
                // re-basing on it is what lets the intents behind this one
                // retry against current truth; sending them against the head
                // that was just refused would fail them identically, which is
                // the clobbering the rollback above is trying to avoid.
                const rebase = await refreshedTaskHead(taskID);
                if (rebase.head) entry.head = rebase.head;
                if (!rebase.boardRead) {
                  // A refresh that never read the board leaves this queue
                  // holding the refused head with nothing newer to put in its
                  // place, and the two ways to carry on are both worse than
                  // stopping. Sending the next intent against that head is the
                  // identical failure the re-base exists to prevent; sending it
                  // unguarded would overwrite the change that caused the
                  // refusal, which is the whole reason the guard is there.
                  //
                  // The intents after that next one are a real trade rather
                  // than a free one. Each refusal forces a refresh of its own,
                  // so a one-request outage could have ended in time for the
                  // third intent to land. They are dropped anyway: the outage
                  // is reported and the board rolls back to what it read, so
                  // the loss is visible and re-doable, which is worth more than
                  // a queue that keeps writing while it cannot see the board it
                  // is writing to.
                  //
                  // Nothing is stranded either way. The poll goes on running,
                  // the model converges as soon as one gets through, and the
                  // reader's next change is queued against the head it read.
                  boardRead = false;
                  entry.queue.length = 0;
                }
              }
              // Reported after that refresh rather than before it, so the
              // report is written against the board the reader ends up looking
              // at. The report itself survives every refresh and every poll
              // after it: it is held outside the model and drawn on the card,
              // and only a dismissal or the reader's next change to that card
              // takes it away.
              reportIntentFailure(taskID, subject, error, boardRead);
              withdrawFailedIntentFromOpenDetail(taskID, error, boardRead);
            }
            rerender();
          }
        } finally {
          entry.draining = false;
          if (entry.queue.length === 0) pendingIntents.delete(taskID);
          rerender();
        }
      }
      // What a refused write can re-base onto: the head the server holds for a
      // task once the refresh this caller forced has settled, and whether that
      // refresh read the board at all. A refresh superseded by a newer one is
      // followed rather than trusted, so the answer is never older than the
      // write that asked for it.
      //
      // Only a refresh that read the board can answer. One that did not leaves
      // the model at the last successful poll, whose head for this task is the
      // one the server has just refused — answering from it would re-base a
      // write onto the very head it was refused for, and the retry that
      // invites is refused identically.
      //
      // It is the read that is asked about rather than the status, because the
      // status also carries the fate of the relationship context refreshed
      // after it, and a board that has just been replaced with the server's
      // version is not an outage however that second half ended. What is left
      // in `boardRead` is the one question a re-base turns on: is what the
      // model now holds something this refresh read from the server. `head`
      // separately distinguishes "the server holds nothing for this task any
      // more" from "this board has not been able to ask".
      async function refreshedTaskHead(taskID) {
        const result = await currentRefreshResult(refresh());
        if (!result.boardRead) return { boardRead: false, head: "" };
        const refreshed = latestTasks.find((candidate) => candidate.id === taskID);
        return { boardRead: true, head: refreshed && refreshed.head || "" };
      }
      // The indicator says what the next mutation will do, which is why it is
      // read from the server rather than assumed: a board set to defer still
      // publishes inline when no watcher answers, and saying "deferred" then
      // would be a claim the board cannot keep.
      //
      // On is the mode where something else publishes for you: a watcher picks
      // each change up just after it is recorded, and the write returns without
      // waiting for the network. Off is the mode where this board pushes the
      // change itself, before the response returns — so the word the switch
      // offers when it is off is "Publish", the thing turning it on arranges,
      // and the word it offers when it is on is "Push", which is what taking it
      // back means here and what the inline mode's own description calls it.
      //
      // With nobody answering there is no third mode to report and no flip to
      // offer: both modes publish inline, so neither knob position is a lie and
      // neither is a choice. The switch used to read off and take the click
      // anyway — it set the server's mode, the reading did not change because
      // the mode was never what made it read off, and the next click set the
      // mode back. What the reader saw was a knob that would not move; what they
      // left behind was a publishing preference at whatever parity their
      // clicking landed on, for whoever started a watcher next to discover. So
      // the control says it is unavailable and why instead, in its words as well
      // as in aria-disabled, because a label still offering a flip would be
      // contradicting the rest of it.
      //
      // aria-disabled rather than the disabled property, because the reason is
      // the whole point of the state: a disabled button leaves the tab order and
      // takes its explanation with it, and a title on one is not reliably shown.
      // This one stays reachable, and toggleSyncMode refuses the write.
      function renderSyncState() {
        if (!syncToggle) return;
        if (!syncState) { syncToggle.hidden = true; return; }
        syncToggle.hidden = false;
        const answering = Boolean(syncState.watcher);
        const deferring = syncState.mode === "deferred" && answering;
        syncToggle.setAttribute("aria-checked", deferring ? "true" : "false");
        syncToggle.setAttribute("aria-disabled", answering ? "false" : "true");
        // The two ways to have nobody to defer to are not the same news, so they
        // do not get the same words. With no origin nothing is published in
        // either mode and no watcher can change that, so telling that reader to
        // wait for one to answer would send them after a fix that cannot work.
        // A server that names no reason is read as the watcher case, which is
        // the one a board with an origin is in.
        const originless = syncState.reason === "no-origin";
        text(syncLabel, answering ? (deferring ? "Push" : "Publish") : (originless ? "No origin" : "No watcher"));
        if (!answering) {
          // The server's own sentence names the reason, and the board adds what
          // follows from it, which the server has no way to know: that this
          // control is inert, and that activating it looks again rather than
          // setting anything. Both readings are worth re-reading, because an
          // origin can be added to a clone as easily as a watcher can be
          // started, and the server probes for both every time it is asked.
          syncToggle.title = originless
            ? (syncState.detail || "no origin is configured, so nothing is published") +
              ". Publication has nowhere to go until this clone has an origin; activate this to look again."
            : (syncState.detail || "no watcher is answering, so changes publish inline") +
              ". Publication cannot be handed to a watcher until one answers; activate this to look again.";
          return;
        }
        syncToggle.title = syncState.detail ||
          (deferring
            ? "A watcher publishes each change just after it is recorded. Click to wait for the push instead."
            : "Each change is pushed before the response returns. Click to hand publication to a watcher.");
      }
      // Every answer that would write syncState claims a number first and drops
      // itself if a later one has already landed. A stalled switch invites
      // repeated activation, so two reads in flight is the ordinary case now,
      // and without this the reading is whichever reply is slowest: a stale
      // watcher:false arriving after a fresh watcher:true would repaint a live
      // switch as stalled and cost the reader the flip they were making. The
      // write below claims a number too, so a read issued before it cannot
      // overwrite the mode the server just confirmed.
      let syncStateSequence = 0;
      async function refreshSyncState() {
        const sequence = ++syncStateSequence;
        let read = null;
        try {
          const response = await fetch("/api/sync", { cache: "no-store" });
          const document = await response.json();
          if (response.ok && document.format === "workbook.sync" && document.version === 1) read = document.sync;
        } catch (_) {
        }
        if (sequence !== syncStateSequence) return;
        // A re-read that failed says nothing about the server's mode, so it does
        // not get to change the reading — and it must not clear it. Nothing else
        // in this page reads /api/sync, so a syncState of null after a failure
        // would hide the switch for the life of the page, taking away the one
        // control the reader had just activated with no way back but a reload.
        // At load there is nothing to keep and the switch stays hidden, which is
        // the right reading for a server that answers no sync document at all.
        if (read) syncState = read;
        renderSyncState();
      }
      async function toggleSyncMode() {
        if (!syncState) return;
        // Nothing to set while nobody answers, so this asks rather than writes.
        // A watcher can start at any moment and the board reads this state when
        // it loads, so the activation a reader aims at a stalled switch is the
        // one moment worth reading it again: if one has appeared since, the
        // switch comes back live and the activation after it flips for real.
        if (!syncState.watcher) {
          await refreshSyncState();
          return;
        }
        const wanted = syncState.mode === "inline" ? "deferred" : "inline";
        const sequence = ++syncStateSequence;
        let written = null;
        try {
          const response = await sendUnlessBusy("/api/sync", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ mode: wanted }),
          });
          const document = await response.json();
          if (response.ok && document.format === "workbook.sync") written = document.sync;
        } catch (_) {
        }
        if (sequence !== syncStateSequence) return;
        if (written) syncState = written;
        renderSyncState();
      }
      if (syncToggle) syncToggle.addEventListener("click", toggleSyncMode);
