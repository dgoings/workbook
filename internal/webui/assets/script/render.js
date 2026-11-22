      function cardNodes(list) {
        return [...list.children].filter((child) => child.dataset && child.dataset.taskId);
      }
      // Every list a card can be in, columns and the unknown-status region
      // alike. A card there is as real as any other and has to be found by the
      // same lookups, or focus restoration and reconciliation would treat it as
      // a node that no longer exists.
      function cardLists() {
        const all = [...lists.values(), unknownList];
        if (deletedList) all.push(deletedList);
        return all;
      }
      // The line a column shows when the filters, and not the board, are what
      // left it with nothing in it. An empty column and a column the reader has
      // filtered down to nothing look identical, and the second one is the reader
      // asking a question the board has to answer.
      //
      // Built once per list and kept first in it, the way the Deleted column's own
      // empty state is: reconcile() places cards by index among the nodes that
      // carry a task ID and passes over the ones that do not, so a paragraph at
      // the top costs the renderer nothing. The record is weak because the Deleted
      // column's list is a new element every time that column is shown.
      const filterEmpties = new WeakMap();
      function filterEmptyFor(list) {
        const known = filterEmpties.get(list);
        if (known) return known;
        const empty = document.createElement("p");
        empty.className = "column__empty";
        empty.dataset.filterEmpty = "";
        empty.hidden = true;
        text(empty, "No matches.");
        list.insertBefore(empty, list.firstElementChild);
        filterEmpties.set(list, empty);
        return empty;
      }
      // Whether a column says "No matches.", and whether a count is drawn as the
      // "3 / 9" reading. Both are written only when the answer has changed, which
      // is the rule every count on this board follows and for the same reason:
      // this runs on every poll, and a property set to the value it already holds
      // is a mutation an assistive technology may announce — once a second, for as
      // long as the reader leaves the board open.
      function setFilterEmpty(list, emptied) {
        const empty = filterEmptyFor(list);
        if (empty.hidden === !emptied) return;
        empty.hidden = !emptied;
      }
      function setFilteredCount(count, label) {
        const filtered = label.includes("/");
        if (count.classList.contains("column__count--filtered") === filtered) return;
        count.classList.toggle("column__count--filtered", filtered);
      }
      // When a task was deleted, which is what orders the Deleted column. A
      // tombstone is the last thing that can happen to a task, so the time it
      // was last updated is the time it was deleted.
      function deletedAt(task) {
        const at = Date.parse(task.updatedAt || "");
        return Number.isNaN(at) ? 0 : at;
      }
      function findCardNode(taskID) {
        for (const list of cardLists()) {
          const node = cardNodes(list).find((candidate) => candidate.dataset.taskId === taskID);
          if (node) return node;
        }
        return null;
      }
      // Reconcile one column against the entries it should hold, touching only
      // what differs: new IDs are built, survivors are updated in place, and a
      // survivor is moved only when its position is actually wrong. Survivors
      // are looked up across the whole board rather than within this column, so
      // a task that changed status moves its own node between columns instead of
      // being destroyed in one and rebuilt in the other.
      function reconcile(list, entries, survivors) {
        const placed = cardNodes(list);
        entries.forEach((entry, index) => {
          const survivor = survivors.get(entry.task.id);
          const node = survivor ? applyCard(survivor, entry.task, entry.view) : card(entry.task, entry.view);
          if (placed[index] === node) return;
          const at = placed.indexOf(node);
          if (at >= 0) placed.splice(at, 1);
          list.insertBefore(node, placed[index] || null);
          placed.splice(index, 0, node);
        });
      }
      function focusedCardID() {
        const active = document.activeElement;
        if (!active || typeof active.closest !== "function") return null;
        const focused = active.closest(".task-card");
        return focused ? focused.dataset.taskId : null;
      }
      // Reconciliation keeps a focused card's node, so this is a backstop rather
      // than the mechanism. The guarantee should not depend on the diff being
      // perfect: a card that did have to be rebuilt takes its focus with it.
      function restoreCardFocus(taskID) {
        if (!taskID || focusedCardID() === taskID) return;
        const node = findCardNode(taskID);
        if (node && typeof node.focus === "function") node.focus();
      }
      function entriesFor(tasks, presentation) {
        return tasks.map((task) => ({ task, view: presentation.get(task.id) }));
      }
      function render(tasks, presentation, allTasks = tasks, search = null) {
        // A caller that folded already hands it over; one that did not gets the
        // fold done here, so this function answers for itself either way.
        const searching = search || foldedSearch(allTasks);
        const grouped = new Map([...lists.keys()].map((status) => [status, []]));
        // A task whose status matches no column is shown under its own heading
        // rather than dropped. The presentation layer computes the set;
        // a task that is invisible reads as a task that was deleted, which is a
        // worse lie than a task that is merely unsorted. Two clones on different
        // Workbook versions produce this on their own. See
        // internal/presentation/parity_test.go for the standing decision.
        const stranded = [];
        // The Deleted column's cards, when it is shown. A deleted task is filed
        // under a status the whole time it is tombstoned, and drawing it in that
        // status's column would put a task nobody can edit among the ones they
        // can — so the deleted flag decides the column before the status is
        // consulted at all. With the column hidden the task is not drawn
        // anywhere: it is in the model only because the poll that read it asked
        // for it, and the next poll that does not will take it back out.
        const removed = [];
        // How many cards each column would hold with nothing filtered, counted
        // the same way the groups below are so that the two readings can only
        // differ by what the filters removed. Without filters every total equals
        // its group and no count says anything extra.
        const totals = new Map([...lists.keys()].map((status) => [status, 0]));
        let totalStranded = 0;
        let totalRemoved = 0;
        allTasks.forEach((task) => {
          if (task.deleted) {
            if (deletedList) totalRemoved += 1;
            return;
          }
          if (totals.has(task.status)) totals.set(task.status, totals.get(task.status) + 1);
          else totalStranded += 1;
        });
        tasks.forEach((task) => {
          if (task.deleted) {
            if (deletedList) removed.push(task);
            return;
          }
          const items = grouped.get(task.status);
          if (items) items.push(task); else stranded.push(task);
        });
        // Most recently deleted first, which is the order a reader looking for
        // something they just deleted wants. Ties break on ID so two tombstones
        // written in the same second keep one order across every poll rather
        // than swapping places under the reader.
        removed.sort((left, right) =>
          deletedAt(right) - deletedAt(left) ||
          (left.id < right.id ? -1 : left.id > right.id ? 1 : 0));
        const focusedID = focusedCardID();
        // Every card node on the board, collected before a single column is
        // touched, so a survivor can be claimed by whichever column now wants it.
        // A node that predates this renderer — the server's initial HTML — has no
        // builder record and cannot be updated in place, so it is dropped here
        // and rebuilt once.
        const survivors = new Map();
        cardLists().forEach((list) => {
          cardNodes(list).forEach((node) => {
            const taskID = node.dataset.taskId;
            if (survivors.has(taskID) || !cardParts.has(node)) { node.remove(); return; }
            survivors.set(taskID, node);
          });
        });
        // Where the new model puts every card it still names, resolved before a
        // single column is reconciled so that a card which has left one can be
        // detached from it first. reconcile() compares each entry with the node
        // standing at that index, so a card still attached to a column that has
        // stopped naming it — moved to another column, or deleted outright —
        // pushes every card below it one index down and costs each of them an
        // insertBefore to correct an offset the model never asked for. A browser
        // blurs a node it re-inserts, so on a long column that is a
        // focusout/focusin for every reader parked below the card that left, over
        // a change that did not concern their card. Sweeping first makes a
        // departure cost its neighbours nothing; the departing node is re-inserted
        // by whichever column now wants it, exactly as before.
        const destinations = new Map();
        grouped.forEach((items, status) => {
          const list = lists.get(status);
          items.forEach((task) => destinations.set(task.id, list));
        });
        stranded.forEach((task) => destinations.set(task.id, unknownList));
        removed.forEach((task) => destinations.set(task.id, deletedList));
        survivors.forEach((node, taskID) => {
          // The node the browser is dragging is left exactly where it is, because
          // detaching it mid-drag is the defect this renderer exists to remove.
          // Reconciliation still moves it to a column the model has moved it to,
          // and the render dragend makes — or the route change that retires a
          // gesture no dragend will end — collects one the model has dropped.
          if (activeDrag && activeDrag.taskId === taskID) return;
          if (destinations.get(taskID) === node.parentElement) return;
          node.remove();
        });
        const filtering = filtersActive(boardFilters);
        grouped.forEach((items, status) => {
          const list = lists.get(status);
          reconcile(list, entriesFor(items, presentation), survivors);
          const count = counts.get(status);
          // "3 / 9" only while the filters are actually holding something back.
          // A column the filters did not touch reads the way it always has.
          const total = totals.get(status) || 0;
          const label = filtering && items.length !== total ? items.length + " / " + total : String(items.length);
          if (count.textContent !== label) text(count, label);
          setFilteredCount(count, label);
          setFilterEmpty(list, filtering && items.length === 0 && total > 0);
        });
        // Reconciled from the same survivor pool as the columns, so a status
        // that becomes recognized — or stops being — moves the card the reader
        // is already looking at instead of destroying it and building a stranger.
        reconcile(unknownList, entriesFor(stranded, presentation), survivors);
        const strandedLabel = filtering && stranded.length !== totalStranded
          ? stranded.length + " / " + totalStranded
          : String(stranded.length);
        if (unknownCount.textContent !== strandedLabel) text(unknownCount, strandedLabel);
        setFilteredCount(unknownCount, strandedLabel);
        setFilterEmpty(unknownList, filtering && stranded.length === 0 && totalStranded > 0);
        // The region stays on screen while the filters are what emptied it, so a
        // task stranded on a status no column matches cannot be filtered into
        // silence: it says "No matches." for the reason a column does.
        unknownSection.dataset.visible = stranded.length > 0 || (filtering && totalStranded > 0) ? "true" : "false";
        // Reconciled from the same survivor pool again, so a card the reader
        // deletes moves its own node out of its column and into this one, and a
        // card they restore moves the same node back.
        if (deletedList) {
          reconcile(deletedList, entriesFor(removed, presentation), survivors);
          const removedLabel = filtering && removed.length !== totalRemoved
            ? removed.length + " / " + totalRemoved
            : String(removed.length);
          if (deletedCount.textContent !== removedLabel) text(deletedCount, removedLabel);
          setFilteredCount(deletedCount, removedLabel);
          const filteredOut = filtering && removed.length === 0 && totalRemoved > 0;
          // "No deleted tasks." is a claim about what the server holds, so it
          // waits for a poll that actually asked. Until one has landed the
          // column says nothing rather than something it has not read — and it
          // says nothing over a column the filters emptied either, because there
          // the true answer is the other line.
          deletedEmpty.hidden = removed.length > 0 || !deletedRead || filteredOut;
          setFilterEmpty(deletedList, filteredOut);
        }
        // A report whose card the board no longer draws has nowhere left to be
        // read, and letting it go with the card would put the reader back where
        // this started. The likeliest way to get here is also the one that most
        // needs saying: another clone deleted the task, this board's change was
        // refused because of it, and the next poll takes the card away. So the
        // report moves to the notice, which outlives every route and leaves only
        // when someone dismisses it, in the wording that names the task rather
        // than the wording that pointed at the card.
        taskFailureReports.forEach((report, taskID) => {
          if (destinations.has(taskID)) return;
          // A dragged node is still on the board even though the model stopped
          // naming it, exactly as the sweep above decided. Lifting its report
          // would print the same sentence twice — once on the card under the
          // cursor and once in the notice — and the render dragend makes, or
          // the one a route change makes when it retires the gesture, does this
          // properly.
          if (activeDrag && activeDrag.taskId === taskID) return;
          taskFailureReports.delete(taskID);
          const lifted = showNotice("warning", report.notice);
          // The card that was holding the caret has just been removed, and the
          // caret went to the document body with it. The report that outlived
          // the card is what the reader has left to act on, so it takes the
          // caret, the same way a refused create hands it to Restore draft.
          if (focusedID === taskID) focusNoticeReport(lifted);
        });
        // A card that was standing in for a create is gone from this render, so
        // the caret it was holding follows the task rather than falling to the
        // document body.
        const heir = retiredCreationHeir;
        retiredCreationHeir = null;
        restoreCardFocus(heir && heir.key === focusedID ? heir.taskID : focusedID);
        // Drawn here rather than beside the rest of the row, because what a
        // chooser lists and what each of its options would leave are readings of
        // the board this render just drew. Every path that renders the board
        // comes through here, so neither the poll nor a navigation can draw
        // cards the choosers have not been told about.
        renderChoosers(allTasks, searching);
        // The phone's row of columns shows each column's count, so it is
        // brought up to date with them.
        syncStatusTabs();
      }
      // The outcome of the newest refresh that has superseded the one this
      // caller started, which is what waiting on your own refresh is actually
      // asking for: an answer no older than the write that asked for it.
      // Everything the result says is that newest refresh's, `boardRead`
      // included — so a caller handed a failed one is being told the board
      // could not be read *now*, which is also what the banner is saying.
      async function currentRefreshResult(operation) {
        let owner = operation;
        let result = await owner;
        while (result.status === "superseded") {
          if (!currentTaskRefresh || currentTaskRefresh === owner) return result;
          owner = currentTaskRefresh;
          result = await owner;
        }
        return result;
      }
      // Announces that the project's statuses have moved on from the ones this
      // page is drawing, once, and then leaves the board alone.
      //
      // Leaving the board alone is the whole point. Rebuilding the columns
      // would mean building new card nodes, and a card node is where the
      // reader's work in flight lives — an open form, a change staged against a
      // head, a refusal they have not read yet. None of that can be moved to a
      // new node, and all of it is lost with the old one. A reload loses it
      // too, which is exactly why the reader chooses when.
      //
      // What raises it is the configuration that arrived differing from the one
      // this page is drawing, not the head having moved. Those were the same
      // question until a change could be drawn without a reload; a color can
      // be, because every answer carries the stylesheet the server would have
      // served and the page swaps it in place — so a head-based notice asked
      // the reader to reload for a board they were already looking at. The
      // server states the shape of what a head stands for, the columns and the
      // priorities by membership, order and label, and a head that arrives over
      // a shape this page already has is recorded and passed over in silence.
      //
      // By content rather than by who made the change, which is what makes it
      // right for a teammate's: their recolor is as quiet as the reader's own,
      // and their rename is as loud.
      //
      // A change that states no shape — an older build, or a document composed
      // without one — is announced. Nothing here can tell whether it is on
      // screen, and the end of that failure the reader can act on is the notice.
      function noteVocabularyChange(head, shape) {
        if (vocabularyChanged || head === renderedVocabularyHead) return;
        if (shape && shape === renderedVocabularyShape) {
          // Current, so the head this page answers for moves up to it. Without
          // that, every later answer carrying the same head would be compared
          // again — and a poll that fell back to a document with no shape on it
          // would then announce a change this page had already accepted.
          renderedVocabularyHead = head;
          return;
        }
        vocabularyChanged = true;
        vocabularyNotice.hidden = false;
      }
      vocabularyReload.addEventListener("click", () => { window.location.reload(); });
      // The thread the open task page is drawing, brought up to the read that
      // just landed. A page showing another task, or no task, has nothing here
      // to follow.
      // The presentation travels with the tasks because one of the panels is
      // drawn from it rather than from the task: an assignment's chip and its
      // withdrawal are derived on the server, and the page must not compose
      // either from the raw values it happens to be holding.
      function followOpenThreadPanels(tasks, presentation) {
        if (!openThreadPanels) return;
        const held = tasks.find((candidate) => candidate.id === openThreadPanels.taskID);
        if (held) openThreadPanels.follow(held, presentation ? presentation.get(held.id) : null);
      }
      function refresh() {
        const wasLoaded = tasksLoaded;
        const generation = ++taskRefreshGeneration;
        // Read once, here, rather than again when the answer lands: a toggle
        // during the round trip must not let a document that carries no deleted
        // tasks be recorded as an answer about them.
        const includingDeleted = deletedColumnShown;
        const operation = (async () => {
          // Whether this refresh's read of the board reached the model, carried
          // beside the status because the two answer different questions. The
          // status is about the refresh as a whole, and a refresh that read the
          // board, replaced the model with it and cleared the banner still
          // answers "superseded" when the relationship context it goes on to
          // refresh below is overtaken — which an ordinary route render is
          // enough to do, because rendering one drops the controller this is
          // holding. Folded into one word, that reads to a caller as "the
          // server could not be reached", over a board that is showing exactly
          // what the server holds. So the read reports for itself, and every
          // consumer of the status keeps reading the status.
          let boardRead = false;
          // What a read that reached the model goes on to do, whether it
          // replaced the model or was told the model already is the server's:
          // draw the route a first read was waiting on, or bring the open task
          // page's relationship sidebar up to date. Shared so a 304 answers
          // every caller exactly as an applied read of the same document would.
          const followBoardRead = async () => {
            let relationshipContext = { status: "not-applicable" };
            if (!wasLoaded) renderRoute();
            else if (activeDependencyController) {
              const controller = activeDependencyController;
              controller.update();
              relationshipContext = await refreshRelationshipContext(controller, ++relationshipRefreshGeneration);
              if (relationshipContext.status === "superseded") return { status: "superseded", boardRead };
            }
            return { status: "applied", boardRead, relationshipContext };
          };
          try {
            // One poll for the whole board. The Deleted column is fed by the
            // same document as every other column, so it is exactly as fresh
            // as they are and there is no second read to disagree with.
            const url = includingDeleted ? "/api/tasks?deleted=include" : "/api/tasks";
            const sentTag = boardValidator && boardValidator.url === url ? boardValidator.etag : null;
            const response = await fetch(url, conditionalRead(sentTag));
            if (response.status === 304) {
              // The server still holds the document the model was read from, so
              // this tick is a sync that changed nothing: no body to parse, no
              // model to replace, nothing to redraw. It still counts as one —
              // the banner comes down and the age starts over — and the read is
              // reported as having reached the model, because the model already
              // is what this read would have put there.
              //
              // A 304 to a read that named no tag vouches for nothing, and one
              // cannot arrive before the first applied read, which is the only
              // place a tag comes from. Either is a broken answer, and is
              // treated as the failed read it is rather than as a board.
              if (!sentTag || !wasLoaded) throw new Error("unrequested 304");
              if (generation !== taskRefreshGeneration) return { status: "superseded", boardRead };
              boardRead = true;
              stale.dataset.visible = "false"; markSynchronized();
              return await followBoardRead();
            }
            const document = await response.json();
            if (!response.ok || document.format !== "workbook.tasks" || document.version !== 1 || !Array.isArray(document.tasks) || !Array.isArray(document.presentation)) throw new Error("invalid task document");
            const presentation = new Map(document.presentation.map((view) => [view.taskId, view]));
            if (presentation.size !== document.tasks.length || document.tasks.some((task) => !presentation.has(task.id))) throw new Error("invalid task presentation");
            if (generation !== taskRefreshGeneration) return { status: "superseded", boardRead };
            latestTasks = document.tasks;
            latestPresentation = presentation;
            // Recorded with the model it names, and only here: a read that lost
            // the race above never became the model, so its tag names nothing
            // this page holds.
            const tag = responseTag(response);
            boardValidator = tag ? { url, etag: tag } : null;
            tasksLoaded = true;
            taskLoadFailed = false;
            // Set with the model it describes: what the Deleted column may say
            // about itself is decided by whether this read asked for it, not by
            // whether the column happens to be on screen now.
            deletedRead = includingDeleted;
            // Set with the assignment it describes, and after the generation
            // check above: a refresh that lost the race never touches the
            // model, so it never has a read to report.
            boardRead = true;
            noteVocabularyChange(
              typeof document.vocabularyHead === "string" ? document.vocabularyHead : "",
              typeof document.vocabularyShape === "string" ? document.vocabularyShape : "");
            // A poll deliberately does not touch a pending queue's head. The
            // queue re-bases when a write is actually refused, where the answer
            // is known to be newer than the head that failed; a poll that left
            // before the last confirmation returned carries an older head, and
            // adopting it would refuse the next intent for no reason.
            const projected = projectTasks(document.tasks);
            // Projected once and handed to both, so the cards and the open task
            // page are drawn from one answer rather than from two derivations of
            // the same read.
            const projectedPresentation = projectPresentation(presentation, projected);
            // The filters narrow what the columns draw and nothing else: the open
            // task page is a page about one task, not a view of the board, and a
            // poll that stopped feeding it because the board is searched would
            // break a form the reader is filling in.
            const search = foldedSearch(projected);
            const shown = shownTasks(projected, new Set(document.tasks.map((task) => task.id)), search);
            render(shown, projectedPresentation, projected, search); stale.dataset.visible = "false"; markSynchronized();
            // The open task page's thread and attachments follow this read in
            // place, the way the relationship sidebar below does. Nothing else
            // about that form moves on a poll: a field is corrected only by a
            // refusal, which is what reviseAgainstTask is for.
            followOpenThreadPanels(projected, projectedPresentation);
            return await followBoardRead();
          } catch (_) {
            if (generation !== taskRefreshGeneration) return { status: "superseded", boardRead };
            text(stale, "Task refresh failed. Showing the last successful board."); stale.dataset.visible = "true";
            if (!tasksLoaded) {
              taskLoadFailed = true;
              renderRoute();
            }
            return { status: "failed", boardRead };
          }
        })();
        currentTaskRefresh = operation;
        return operation;
      }
