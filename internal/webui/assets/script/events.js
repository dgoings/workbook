      window.addEventListener("popstate", renderRoute);
      document.addEventListener("click", async (event) => {
        if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        // A click outside an open chooser closes it, which is what every menu on
        // every page does and the only thing that makes one dismissable by a
        // reader who has changed their mind. A click inside one closes nothing:
        // the chooser the click landed in is exempt, so the button that opens a
        // menu does not immediately close it again and a tick leaves the menu
        // standing for the next one.
        closeChoosersExcept(event.target && event.target.closest && event.target.closest("[data-filter-chooser]"));
        const copyControl = event.target && event.target.closest && event.target.closest("[data-copy-task-id]");
        if (copyControl) {
          event.preventDefault();
          if (copyControl.dataset.copyTaskId === copySuppressedTaskID) {
            copySuppressedTaskID = null;
            if (copySuppressionTimer !== null) window.clearTimeout(copySuppressionTimer);
            copySuppressionTimer = null;
            return;
          }
          const copyGroup = copyControl.closest(".task-id-copy-group");
          const copyStatus = copyGroup && copyGroup.querySelector("[data-copy-status]");
          if (!copyStatus) return;
          await copyTaskID(copyControl.dataset.copyTaskId, copyStatus);
          return;
        }
        const anchor = event.target && event.target.closest && event.target.closest("a[href]");
        if (!anchor || (anchor.target && anchor.target !== "_self") || anchor.hasAttribute("download")) return;
        const destination = new URL(anchor.href, window.location.href);
        if (destination.origin !== window.location.origin) return;
        event.preventDefault();
        navigate(destination.pathname + destination.search + destination.hash);
      });
      // Heard at the document because that is the only place a paste can be
      // heard: it is dispatched at whatever has focus, and the attachment panel
      // never has any. The handler answers only a paste carrying a file, and
      // only while a surface that takes one is on screen.
      document.addEventListener("paste", attachmentPaste);
      // Escape closes the open chooser and hands the caret back to the button
      // that opened it, so a reader who opened a menu with the keyboard is left
      // where they were rather than at the top of the document. Heard at the
      // document because the caret may have left the menu since — the reader can
      // tab out of it — and a menu nobody can dismiss is worse than no menu.
      //
      // Heard after the box inside the Labels menu, because neither listener
      // captures and a bubbling event reaches its target first. That is what lets
      // that box claim the first Escape to empty itself and leave this one the
      // second, with stopPropagation as the whole of the arrangement.
      document.addEventListener("keydown", (event) => {
        if (event.key !== "Escape") return;
        // Only while the row is on screen. Off the board there is no menu of ours
        // to dismiss, and claiming Escape there would take it from whatever does
        // want it — the relationship combobox closes its listbox with it.
        if (!filterRow || filterRow.hidden) return;
        let dismissed = null;
        filterChoosers.forEach((chooser) => { if (chooserIsOpen(chooser)) dismissed = chooser; });
        if (!dismissed) return;
        event.preventDefault();
        closeChoosersExcept(null);
        if (dismissed.button) dismissed.button.focus();
      });
      document.addEventListener("dragstart", (event) => {
        const item = event.target.closest(".task-card");
        if (!item) return;
        // No card is draggable at the phone width (see cardDraggable), so a
        // drag that starts there anyway is refused rather than carried out.
        if (phoneLayout()) { event.preventDefault(); return; }
        // A card drags out of a column or out of the unknown-status region, and
        // out of nothing else. The region has no data-status of its own — it is
        // a display rather than a seventh column, and giving it one would make
        // it a drop target — so the two are asked separately and a card from it
        // carries no source status. Nothing downstream needs one: the only use
        // is the no-op check below, and a drop into a real column is never a
        // no-op for a card that was not in one.
        //
        // A card in the Deleted column drags too, and dropping it on a column
        // is what restores it there — the same gesture, in the other direction.
        const source = item.closest("[data-status]");
        if (!source && !unknownList.contains(item) && !(deletedList && deletedList.contains(item))) return;
        if (source && !source.dataset.dropStatus) return;
        // The pending stand-in is marked undraggable, so this is the belt to
        // that brace: a task with no ID has nothing a placement could be
        // recorded against.
        if (item.dataset.pendingCreate === "true") return;
        activeDrag = {
          taskId: item.dataset.taskId,
          priority: item.dataset.priority,
          sourceStatus: source ? source.dataset.status : ""
        };
        copySuppressedTaskID = item.dataset.taskId;
        // A new gesture remembers nothing of the last one. The loop starts on
        // the first dragover, when there is a column under the cursor for it to
        // scroll.
        stopDragScroll();
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", activeDrag.taskId);
        item.classList.add("is-dragging");
      });
      document.addEventListener("dragend", (event) => {
        const item = event.target.closest(".task-card");
        if (item) item.classList.remove("is-dragging");
        activeDrag = null;
        clearPlacement();
        // The renderer left the dragged node where it was for as long as the
        // gesture lasted, even when a poll during it dropped the node's task,
        // and it held back the refusal report on it for the same reason. Both
        // are settled here rather than left to the next poll, because the next
        // poll may well be a 304: the poll that dropped the task already brought
        // the board up to date, and nothing after it would redraw.
        rerender();
        // dragend arrives however the gesture ended — dropped, cancelled with
        // Escape, or released over another window — so this is the stop that
        // cannot be missed, and the one that guarantees no frame loop outlives
        // a drag.
        stopDragScroll();
        const suppressedTaskID = copySuppressedTaskID;
        if (copySuppressedTaskID !== null) {
          if (copySuppressionTimer !== null) window.clearTimeout(copySuppressionTimer);
          copySuppressionTimer = window.setTimeout(() => {
            if (copySuppressedTaskID === suppressedTaskID) copySuppressedTaskID = null;
            copySuppressionTimer = null;
          }, 250);
        }
      });
      // Where this drag may be dropped, or null if it may not be dropped here.
      // Asked by dragenter and dragover alike, because a browser decides
      // whether to deliver the drop at all from whichever of the two it
      // dispatched last, and two handlers answering the same question in two
      // places is how they come to disagree.
      function dragDestination(event) {
        const target = dropTarget(event);
        if (!target || !activeDrag) return null;
        // A file dragged in from the desktop is not a card, whatever it is
        // over. activeDrag above is usually enough to say so — this page is the
        // only thing that sets it — but "usually" is doing real work there: a
        // dragend that never arrived leaves it standing, and then the next file
        // dropped on a column would move a card the reader had finished with.
        // The drag says what it is carrying, so that is what decides.
        if (draggingFiles(event)) return null;
        if (dropsIntoDeleted(target)) {
          // A card already in the column has nowhere to arrive: deleting a
          // tombstone is not a change anyone asked for.
          return draggedTaskIsDeleted() ? null : target;
        }
        // Same set as the one that decides `movable`: a card is draggable out of
        // exactly the columns the server rendered, so those are exactly the
        // columns it may be dropped into.
        return lists.has(target.dataset.dropStatus) ? target : null;
      }
      // A drop is delivered only where the page said yes, and dragenter is one
      // of the two events that can say it. Chrome dispatches dragenter and
      // dragleave rather than dragover whenever the element under the cursor
      // changes — which is what scrolling a column does, several times a second,
      // under a cursor that has not moved. Measured on Chrome 151: ten drag
      // updates at identical coordinates while a column was autoscrolling
      // produced ten dragenters, ten dragleaves and no dragover at all.
      //
      // A page that answered only dragover therefore had its acceptance decided
      // by whichever event happened to land last, and a card released while the
      // column was still moving was silently discarded: no drop event, no
      // write, the card back where it started and nothing said about it.
      // Answering both events with the same rule is what closes that.
      //
      // Both listeners also draw the line, for the same reason both answer.
      // While the board track is sliding, the column under a stationary cursor
      // changes several times a second and dragenter is the only word the page
      // gets about it — a page that drew the line on dragover alone would leave
      // it in a column that has since moved out from under the reader, or leave
      // it nowhere at all after the dragleave that came with the churn.
      function answerDrag(event, target) {
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        showDragPlacement(target, event.clientY);
      }
      document.addEventListener("dragenter", (event) => {
        const target = dragDestination(event);
        trackDragScroll(target, event);
        if (target) answerDrag(event, target);
      });
      document.addEventListener("dragover", (event) => {
        const target = dragDestination(event);
        trackDragScroll(target, event);
        if (target) answerDrag(event, target);
      });
      // Whether the card under the cursor is one the board is drawing as
      // deleted. It reads the projected model rather than the raw one because
      // the projection is what is on screen: a queued delete has already moved
      // the card into the Deleted column for the reader.
      function draggedTaskIsDeleted() {
        if (!activeDrag) return false;
        const drawn = projectTasks(latestTasks).find((task) => task.id === activeDrag.taskId);
        return Boolean(drawn && drawn.deleted);
      }
      document.addEventListener("dragleave", (event) => {
        // Wherever this leave came from and whatever it turns out to mean, it
        // is a report of where the cursor is, and the track answers for the
        // whole window rather than for any one column. A leave that names a
        // column the loop is not scrolling is ignored below, and it still
        // carries the news that the cursor has gone somewhere the track has
        // nothing to do — without this the loop would keep sliding on a
        // position nothing had corrected.
        if (activeDrag) {
          if (typeof event.clientX === "number") dragPointerX = event.clientX;
          if (typeof event.clientY === "number") dragPointerY = event.clientY;
        }
        const target = dropTarget(event);
        if (!target || target.contains(event.relatedTarget)) return;
        // A leave whose own cursor is still inside the column is not a
        // departure at all, so nothing about it is answered: not the scroll,
        // and not the line either. Erasing the line there was wrong twice over
        // — it took away a promise the reader is still being made, and it did
        // it by mutating the document under a drag cursor, which is what makes
        // a browser fire the next dragleave. The clear moved below the test
        // rather than above it.
        if (cursorWithin(target, event)) return;
        clearPlacement();
        // Whether the cursor has left the column is asked of where this event
        // says the cursor is, as well as of what it says the cursor moved to.
        //
        // A browser fires dragleave whenever the element under the cursor
        // changes, and scrolling a column changes it several times a second
        // under a cursor that has not moved at all — that is this very loop's
        // doing. On Chrome those leaves name the card the cursor moved onto, so
        // the relatedTarget check above already tells them from a departure:
        // measured on Chrome 151, every leave during an autoscroll named a
        // relatedTarget inside the same list, and the only null one was the
        // genuine exit from the window. Firefox and Safari report no
        // relatedTarget on a dragleave at all, and there the check above cannot
        // tell the two apart; reading a churn leave as a departure would stop
        // the scroll this loop had just caused, with the cursor still sitting
        // there and no dragover coming to start it again. So the cursor's own
        // position decides, which is a fact every browser reports.
        //
        // Two things are not a departure. A cursor still inside the column's
        // box has not left it. Nor has one shoved past its top or bottom, which
        // is the gesture asking it to keep scrolling — and a cursor that leaves
        // the window that way is making the same request, so it is answered the
        // same way and dragend ends it, as it ends everything.
        //
        // What is left is a cursor gone sideways past the column's edge with no
        // dragover to report where it went. The column stops — but where the
        // cursor went may still be asking the board track to slide, and the
        // gutter at the track's own edge is precisely that, so this leave's own
        // coordinates are worth keeping and the loop is the board's to end.
        if (target !== dragScroller) return;
        if (overshootsVertically(target, event)) return;
        releaseDragScroller();
      });
      document.addEventListener("drop", async (event) => {
        // The gesture is over the moment something is dropped, whatever the
        // drop turns out to mean, so nothing is left scrolling while the write
        // this handler may await is in flight.
        stopDragScroll();
        const target = dropTarget(event);
        if (!target || !activeDrag) return;
        // Asked again here, for the reason dragDestination asks it: what a drop
        // is carrying is what decides whether it is a card being moved.
        if (draggingFiles(event)) return;
        if (dropsIntoDeleted(target)) return dropIntoDeleted(event, activeDrag.taskId);
        if (!lists.has(target.dataset.dropStatus)) return;
        event.preventDefault();
        const taskId = activeDrag.taskId;
        const placement = placementFor(target, event.clientY);
        clearPlacement();
        // Whether this drop changes anything is asked of the status the card is
        // drawn under *now*, not the one it was drawn under when the drag
        // started. A poll can re-resolve a card mid-drag — another clone's
        // status change lands, or the reader's own earlier move settles — and
        // reconciliation moves the node into its new column while the cursor is
        // still holding it. Dropping it back on the column it has just arrived
        // in is a gesture that changes nothing, and answering it from the
        // drag-start value would send a write for it: a settlement commit
        // nobody asked for, or a stale-head banner over a move the reader did
        // not make.
        //
        // A card that is still stranded compares against a status no column
        // has, so a drag out of the unknown-status region is always a real move
        // — which is the point of making those cards draggable at all.
        //
        // It reads the projected model rather than the raw one because the
        // projection is what is on screen: a queued intent has already moved
        // the card for the reader, and a no-op is a question about what they
        // can see.
        const drawn = projectTasks(latestTasks).find((task) => task.id === taskId);
        // A card dragged out of the Deleted column is never a no-op, whatever
        // status it is filed under: a tombstone dropped on the column it was
        // deleted from is a restore into that column, which is the whole
        // gesture. The status comparison below is about a card that is already
        // in the columns.
        const restoring = Boolean(drawn && drawn.deleted);
        const currentStatus = drawn ? drawn.status : activeDrag.sourceStatus;
        if (!restoring && placement.status === currentStatus && !placement.before && !placement.after) return;

        const body = { status: placement.status };
        if (placement.before) body.before = placement.before;
        if (placement.after) body.after = placement.after;
        const observed = latestTasks.find((task) => task.id === taskId);
        return queueIntent(taskId, observed && observed.head, {
          // The card lands in its new column immediately. Ordering within the
          // column still comes from the server, because rank is computed from
          // the whole board rather than from the two neighbours the drop names.
          apply: (task) => Object.assign({}, task, restoring
            ? { deleted: false, status: placement.status }
            : { status: placement.status }),
          // Whether the server's task is in the column it was dropped on,
          // which is what a refused resend is checked against (see
          // heldChangeOutcome). The rank within the column is the server's.
          landed: (task) => Boolean(task) && !task.deleted && task.status === placement.status,
          // One write either way, and the same body either way: a restore that
          // names a destination is the drop the position route already
          // describes, so the drop position becomes the same before/after and
          // the server records the return and the placement as one operation
          // pack — one history entry, and one refusal that leaves the task
          // tombstoned rather than restored somewhere nobody asked for.
          send: (expectedHead) => mutateTask(
            restoring ? "POST" : "PATCH",
            "/api/tasks/" + encodeURIComponent(taskId) + (restoring ? "/restore" : "/position"),
            expectedHead ? Object.assign({}, body, { expectedHead }) : body
          ),
        });
      });
      // A live card dropped on the Deleted column: one queued delete, carrying
      // the head the card was drawn from so a task another clone has changed
      // since is refused rather than tombstoned out from under them.
      function dropIntoDeleted(event, taskId) {
        const drawn = projectTasks(latestTasks).find((task) => task.id === taskId);
        // Already there. dragover refuses the drop, so this is the belt to that
        // brace rather than the guard.
        if (drawn && drawn.deleted) return;
        event.preventDefault();
        clearPlacement();
        const observed = latestTasks.find((task) => task.id === taskId);
        return queueIntent(taskId, observed && observed.head, {
          apply: (task) => Object.assign({}, task, { deleted: true }),
          // A board that no longer carries the task has taken it out of the
          // columns, which is what a delete does.
          landed: (task) => !task || Boolean(task.deleted),
          send: (expectedHead) => mutateTask(
            "DELETE",
            "/api/tasks/" + encodeURIComponent(taskId),
            expectedHead ? { expectedHead } : undefined
          ),
        });
      }
      renderRoute();
      refresh();
      refreshSyncState();
      renderSyncAge();
      // One timer, not two. The sync age is redrawn on the poll's own tick
      // rather than on a second interval of its own, so a board left alone
      // still ages visibly, whatever the poll answers, and there is only ever
      // one scheduled callback to reason about. The poll's promise is
      // returned, not swallowed: callers await this tick to know the refresh
      // has finished.
      window.setInterval(() => { renderSyncAge(); return refresh(); }, 1000);
    })();
