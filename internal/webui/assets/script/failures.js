      // A refused change is reported on the card it concerns, which is both
      // where the reader is looking and somewhere the report can stay.
      //
      // It is deliberately no longer written to the stale banner. That banner
      // says the board is showing state older than the server's, and a
      // successful poll clears it a second later — correctly, because the board
      // is current again. A conflict report parked there was erased inside a
      // second whether or not anyone had looked at it, which on a shared board
      // is the whole event going unnoticed.
      //
      // Two wordings are built here rather than one. The card's copy can point
      // at the card, because it is on it. The notice's copy is read after the
      // board has stopped drawing that card — the likeliest refusal is a task
      // another clone deleted — so it names the task instead of pointing at
      // something that is no longer there, and says the board no longer carries
      // it. `subject` is the caller's, because by the time a refusal is reported
      // the refresh it forced can already have taken the task out of the model.
      //
      // A conflict whose forced refresh never read the board is a third thing
      // to say. "The card shows the version the server holds" is exactly what
      // that card cannot claim — the request that would have fetched that
      // version is the one that failed — and the queue behind the refusal
      // stopped rather than send against a head the server had already refused,
      // so the report says both. It is the read that decides this and not the
      // refresh's status, so the sentence is never printed over a board that
      // was successfully replaced with the server's version. The lifted wording
      // is left alone: a report is only lifted once a poll has succeeded and
      // stopped drawing the card, and by then the board being unreadable is no
      // longer what the reader is looking at.
      function reportIntentFailure(taskID, subject, error, boardRead) {
        const changedElsewhere = Boolean(error && error.category === "stale-write");
        taskFailureReports.set(taskID, {
          card: changedElsewhere
            ? (boardRead
              ? "This task changed elsewhere. The card shows the version the server holds."
              : "This task changed elsewhere and the board could not be refreshed. The card may be out of date, and changes made here were not applied.")
            : error && error.waitedOut
              ? "The server kept asking this page to wait, and this change was not sent again. The card shows the version the server holds; make it again in a moment."
              : "Task update failed. The card shows the version the server holds.",
          notice: changedElsewhere
            ? "Task " + subject + " changed elsewhere and is no longer on the board. The change made here was not applied."
            : "Task " + subject + " could not be updated and is no longer on the board. The change made here was not applied."
        });
        rerender();
      }
      // A save from the detail form that was refused after its form left the
      // document. The form is where this belongs and the form is gone, so it is
      // said in the two places that outlive a route: the notice, which is read
      // from wherever the reader ended up, and the task's own form, which says
      // it again the next time that task is opened. A reader who navigated away
      // mid-save is the one reader who cannot see a refusal coming, and silence
      // here reads to them as a save that landed.
      //
      // The edits are named as lost rather than held anywhere. They went with
      // the node the moment the route changed, and a report offering to restore
      // them would be promising something this client cannot do.
      //
      // The conflict is described rather than quoted. The server's sentence
      // names the head the request carried, which is this client's bookkeeping;
      // what the reader needs is that someone else changed the task.
      //
      // Every other refusal is quoted, and quoted last. A server message is a
      // Go error — lower case, unpunctuated, written for a log — and spliced
      // between two sentences of this client's own it runs straight into the
      // one after it: "was not saved. cannot update a tombstoned task The save
      // was still open…". At the end it is a sentence the reader can stop at,
      // which is where a refused create puts its reason too.
      //
      // `deleted` is the caller's, and only the caller that waited on the
      // refresh a `stale-write` forces can pass it: a refresh that read the
      // board and no longer found the task is proof there is nothing to open.
      // The way back is only an offer while there is a task at the end of it —
      // followed for a deleted one it lands the reader on "Task not found",
      // which is the page this client goes out of its way not to drop anyone
      // on. So the link goes, the report says why it is not there, and the
      // reason names the deletion rather than the vaguer conflict the refusal's
      // own category would have described.
      function reportDetachedSaveFailure(taskID, error, deleted) {
        const reason = deleted
          ? "The task was deleted elsewhere, which is why the save was refused."
          : (error && error.category === "stale-write"
            ? "The task changed elsewhere, so the save was refused."
            : (error instanceof Error ? error.message : "Task update failed."));
        // Appended rather than assigned: a created task's warnings can already
        // be staged on this form, and they are as unread as this is. Staged for
        // a deleted task too: restoring it is what makes that form openable
        // again, and this is exactly what its next reader needs to know.
        const staged = pendingTaskMessages.get(taskID) || [];
        pendingTaskMessages.set(taskID, staged.concat([
          "A save from this form was still open when you left it, and it was not applied. " +
            "The edits it carried were not saved. " + reason
        ]));
        let open = null;
        if (!deleted) {
          open = document.createElement("a");
          open.href = taskPath(taskID);
          text(open, "Open task");
        }
        showNotice("error",
          "Task " + nameTask(taskID) + " was not saved. The save was still open when you left " +
            "the form, and the edits it carried are gone. " +
            (deleted ? "There is nothing left to open. " : "") + reason,
          open);
      }
      // What to call a task in a report that has outlived the card it was drawn
      // on. The ID prefix is what the card showed and what every other control
      // names a task by; the title is what the reader recognizes it as.
      function nameTask(taskID) {
        const view = latestPresentation.get(taskID);
        const task = latestTasks.find((candidate) => candidate.id === taskID);
        const prefix = (view && view.idPrefix) || taskID;
        return task && task.title ? prefix + " “" + task.title + "”" : prefix;
      }
      function dismissTaskFailure(taskID) {
        if (!taskFailureReports.delete(taskID)) return;
        rerender();
        // The control that was just clicked is hidden now, and the caret would
        // go to the document body with it. The card the report was about is the
        // nearest thing that still exists, so the caret lands there.
        const node = findCardNode(taskID);
        if (node && typeof node.focus === "function") node.focus();
      }
      // A pending intent can outlive the board view that queued it. When one
      // fails while its task's detail form is open, that form is displaying an
      // optimistic value the server refused, so the fields still displaying it
      // are corrected where they stand and the form says why they moved.
      //
      // What that buys is display accuracy, not safety. A save from this form
      // sends only the fields that differ from the ones it rendered
      // (changedTaskValues), and a control nobody has touched does not differ
      // from them, so the refused value was never reachable by a save either
      // way. The form was simply reading as saved state while showing a value
      // the server had thrown away.
      //
      // Corrected rather than re-rendered, because a rebuilt form would pay for
      // that accuracy with everything typed into it since it opened, and would
      // detach a save in flight — a detached save can only report from outside
      // the form it was made in (reportDetachedSaveFailure), so a reader who
      // was saving at that moment would read about their board change on the
      // form they are standing at and about their own save somewhere else
      // entirely, with the edits that save carried gone either way. The
      // correction skips the fields being edited and leaves the node, its
      // listeners, and the caret exactly where they are.
      //
      // A task the board has stopped carrying — the likeliest refusal of all is
      // one another clone deleted — has nothing to correct the form against.
      // The form stays as it is and says so, because a reader mid-sentence
      // needs their sentence more than they need a "task not found" page.
      //
      // `boardRead` is the caller's, and it decides the only thing this form can
      // claim about the fields it just moved. The correction reads them out of
      // the model, which the refresh a `stale-write` forces is what replaces
      // with the server's version — so when that refresh read the board, the
      // untouched fields are the server's and the form may say so. When it did
      // not read the board, the model is still the last successful poll's and
      // those fields are whatever it read; saying they "now show the version the
      // server holds" claims exactly the request that just failed. The outage is
      // named instead, in the wording the card beside it uses for the same
      // event, and the queue behind the refusal stopped rather than write to a
      // board it cannot see, so the changes it dropped are named with it. The
      // stale banner keeps its own single writer through all of this: the
      // failed refresh raised it, and this sentence is about these fields.
      //
      // It defaults to true for a caller with no refresh to report on. A refusal
      // that is not a `stale-write` forces none, which is why drainIntents
      // starts the flag there: nothing has told this board it is unreadable, and
      // the client treats the last poll as the server's version everywhere else.
      // The outage wording is the falser of the two in that state, because it
      // reports a read that was never attempted.
      function withdrawFailedIntentFromOpenDetail(taskID, error, boardRead = true) {
        const open = openDetailForm;
        if (!open || open.taskID !== taskID) return;
        // The same view the detail route renders from, so the form keeps
        // agreeing with the board: an intent still queued behind the failed one
        // is projected here exactly as it is drawn there.
        const held = projectTasks(latestTasks).find((candidate) => candidate.id === taskID);
        if (held) open.revise(held);
        open.report((error && error.category === "stale-write"
          ? "That task changed elsewhere."
          : "Task update failed.") + (held
          ? (boardRead
            ? " The fields you have not edited now show the version the server holds, and your edits are kept; the change made on the board was not applied."
            : " The board could not be refreshed, so the fields you have not edited may be out of date; your edits are kept, and changes made on the board were not applied.")
          : " The board no longer carries it, so the change made there was not applied. Your edits are kept here."));
      }
      function ownsDependencyController(controller) {
        return activeDependencyController === controller;
      }
      // A dependency edge is recorded on the dependent task. When that is the
      // task whose form is open, the edge is a write to it and its head has
      // moved; the form is told so it proposes the tip that now exists rather
      // than the one it rendered. The mirrored direction writes to the other
      // task and leaves this one alone, which is why the orientation is checked
      // rather than assumed.
      function adoptDependencyHead(controller, dependentID, document) {
        if (dependentID !== controller.taskID) return;
        controller.adoptTaskHead(document.task && document.task.head || "");
      }
      // Both answer whether the edge was saved, which is what the caret is
      // put back by (see holdSidebarFocus); a controller this page has left
      // answers nothing, because there is nowhere left to put it.
      async function retryRelationshipDraft(name, relationship, draftMap, controller) {
        if (!ownsDependencyController(controller)) return;
        const dependentID = name === "dependsOn" ? controller.taskID : relationship.id;
        const prerequisiteID = name === "dependsOn" ? relationship.id : controller.taskID;
        const path = "/api/tasks/" + encodeURIComponent(dependentID) +
          "/dependencies/" + encodeURIComponent(prerequisiteID);
        controller.setBusy(true);
        controller.setMessage("");
        try {
          const document = await mutateTask("PUT", path);
          adoptDependencyHead(controller, dependentID, document);
          const refreshed = await currentRefreshResult(refresh());
          if (!ownsDependencyController(controller)) return;
          draftMap.delete(relationship.id);
          if (!hasRelationshipDrafts(controller.draftState)) {
            pendingRelationshipDrafts.delete(controller.taskID);
          }
          if (refreshed.status === "superseded") return true;
          if (refreshed.status === "failed") {
            controller.update();
            controller.setMessage("The dependency was saved durably, but the latest task state could not be refreshed.");
            return true;
          }
          controller.update();
          const warnings = mutationWarnings(document);
          if (warnings.length) {
            controller.setMessage("Dependency saved durably. " + warnings.join(" "));
          }
          return true;
        } catch (error) {
          await refresh();
          if (!ownsDependencyController(controller)) return;
          relationship.error = error instanceof Error
            ? error.message
            : "Dependency update failed.";
          controller.update();
          controller.setMessage(relationship.error);
          return false;
        } finally {
          controller.setBusy(false);
        }
      }
      async function mutateRelationship(method, dependentID, prerequisiteID, controller) {
        const path = "/api/tasks/" + encodeURIComponent(dependentID) +
          "/dependencies/" + encodeURIComponent(prerequisiteID);
        controller.setBusy(true);
        controller.setMessage("");
        try {
          const document = await mutateTask(method, path);
          adoptDependencyHead(controller, dependentID, document);
          const messages = [];
          const warnings = Array.isArray(document.warnings) ? document.warnings : [];
          if (warnings.length) {
            messages.push("Dependency saved durably. " +
              warnings.map((warning) => warning.message).join(" "));
          }
          const refreshed = await currentRefreshResult(refresh());
          if (!ownsDependencyController(controller)) return;
          if (refreshed.status === "superseded") return true;
          if (refreshed.status === "failed") {
            messages.push("The dependency was saved durably, but the latest task state could not be refreshed.");
            controller.setMessage(messages.join(" "));
            return true;
          }
          if (refreshed.relationshipContext.status === "failed") {
            messages.push(refreshed.relationshipContext.message);
          }
          controller.update();
          if (method === "PUT") controller.combobox.clear();
          controller.setMessage(messages.join(" "));
          return true;
        } catch (error) {
          await refresh();
          if (!ownsDependencyController(controller)) return false;
          controller.setMessage(error instanceof Error ? error.message : "Dependency update failed.");
          return false;
        } finally {
          controller.setBusy(false);
        }
      }
      function routeMessage(titleText, message, retryable = false) {
        const section = document.createElement("section"); section.className = "task-route"; section.setAttribute("aria-labelledby", "route-message-title");
        const header = document.createElement("header"); header.className = "task-route__header";
        const heading = document.createElement("h2"); heading.id = "route-message-title"; text(heading, titleText); header.append(heading);
        const body = document.createElement("div"); body.className = "route-message";
        const copy = document.createElement("p"); text(copy, message);
        // The board the reader came from, for the reason the other two Back links
        // name it: the message is a dead end, and the way out of it is the board
        // they were reading rather than a board they were not.
        const back = document.createElement("a"); back.className = "board-link"; back.href = boardAddress(deletedColumnShown, boardFilters); text(back, "Back");
        body.append(copy);
        if (retryable) {
          const retry = document.createElement("button"); retry.className = "save-button"; retry.type = "button"; text(retry, "Retry");
          retry.addEventListener("click", refresh);
          body.append(retry);
        }
        body.append(back); section.append(header, body);
        return section;
      }
