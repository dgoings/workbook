      function renderRoute() {
        const current = route();
        if (current.view !== "detail" && current.view !== "new") {
          activeDependencyController = null;
        }
        // Whatever New Task form was on screen is about to be replaced, so its
        // reader is retired here and re-published only by a render that builds
        // one. Anything that had to be saved out of it was saved before the
        // navigation that led here. The detail form's handle is retired for the
        // same reason: the node it writes into is about to go.
        readOpenNewTaskValues = null;
        openDetailForm = null;
        openThreadPanels = null;
        // Said on the way past, because after this render there is nobody left
        // to say it: the form holding those files is about to stop existing, and
        // a reader who pressed Back would otherwise learn nothing at all about
        // attachments they watched fail a moment ago.
        if (outstandingAttachmentReport) {
          reportAbandonedAttachments(outstandingAttachmentReport);
          outstandingAttachmentReport = null;
        }
        // A drag cannot outlive the page it was carrying a card across. A route
        // change rebuilds the board out from under the gesture and takes the
        // card node with it, and no dragend follows something the reader never
        // released — so the gesture is retired here rather than left holding a
        // task nobody can see, drawing a line into a detached column, and
        // driving a frame loop with nothing left to stop it.
        //
        // Measured before this, on a drag carried across a client-side
        // navigation: one live requestAnimationFrame callback still queued and
        // 129 frames run after the route had changed, and Chrome refusing to
        // start the next gesture because the last one had never ended.
        if (activeDrag) {
          activeDrag = null;
          clearPlacement();
          // Redrawn for the reason dragend redraws: the node the gesture was
          // holding may name a task the model has since dropped, and no dragend
          // is coming to collect it. The board kept here is the one a return
          // re-attaches without redrawing, so it has to be right before it goes.
          rerender();
        }
        stopDragScroll();
        // The description setting draws cards, and only the board draws cards.
        // Beside a task's own page it would be a control that visibly does
        // nothing, so it travels with the route it acts on. The markup ships it
        // hidden and this reveals it, so a page that never runs this script
        // offers no setting rather than a dead one that lies about the state of
        // a board it can no longer change.
        //
        // The filter row travels the same way and for the same reason: every
        // control in it is a view of the columns — the Deleted switch adds one,
        // the search narrows them all — and a page that draws no columns offering
        // to do either would be naming a state the reader cannot see. The row is
        // what hides rather than each control in it, because the row is the thing
        // whose whole purpose is the board.
        if (descriptionToggle) descriptionToggle.hidden = current.view !== "board";
        if (filterRow) filterRow.hidden = current.view !== "board";
        // A menu goes with the row. Hiding the row leaves an open one open — the
        // reader cannot see it, so nothing closes it — and that is a menu that is
        // still standing when Back brings the board back, and an Escape this page
        // claims on a route where the relationship combobox needs it.
        if (current.view !== "board") closeChoosersExcept(null);
        // The configuration page's bodies are served elements rather than built
        // ones, so leaving that route has to put them away — the render below
        // would drop them from the document either way, and what they were
        // drawing has to go with them.
        if (current.view !== "config") leaveConfigRoute();
        // The relic pages stop reading bodies for a page nobody is on.
        if (!relicRouteView(current.view)) leaveRelicRoute();
        // A task's relics are read again the next time its page is opened.
        if (current.view !== "detail") leaveTaskRelics();
        if (current.view === "board") {
          document.title = boardName;
          // The board is re-attached rather than rebuilt, which is what makes
          // coming back from the configuration page cost nothing: every card node the
          // reader left is the node they return to, holding whatever it was
          // holding. The columns are the ones this page was served with even
          // when the statuses have moved on since — the standing notice above
          // says so and offers the reload, exactly as it does for a change
          // another clone made.
          //
          // The filters and the Deleted column are the two things about the board
          // this route decides. The filters are read first, so the render that
          // showing or hiding that column triggers already draws the narrowing
          // this address asked for; appending or removing that one section is all
          // the call below does.
          const nextFilters = current.filters || emptyFilters();
          const filtersChanged = !sameFilters(boardFilters, nextFilters);
          boardFilters = nextFilters;
          renderFilterRow(current.deleted === true);
          showDeletedColumn(current.deleted === true);
          // A navigation that changed nothing but the filters changes no column
          // and no route, and nothing above would have redrawn a card: the board
          // is re-attached rather than rebuilt, and the call above renders only
          // when the Deleted column itself appears or goes. So the narrowing asks
          // for its own redraw.
          if (filtersChanged) rerender();
          if (main.firstElementChild !== boardView) main.replaceChildren(boardView);
          focusCreatedCard();
          return;
        }
        if (current.view === "config") {
          document.title = "Configuration" + boardTitleSuffix;
          // Mounted once per visit, the way the board is re-attached rather than
          // rebuilt: this render runs again for the same route when the first
          // task refresh lands, and a reader who had a form open on a hard load
          // would otherwise watch it disappear a moment later.
          if (!main.contains(vocabularyPanel)) {
            main.replaceChildren(configView());
            // Read on entry rather than once. This page is visited seldom and a
            // clone can have changed the statuses since the last look, and a
            // change composed against what this client happens to remember is
            // the stale write it would rather not have to report.
            readVocabulary();
          }
          return;
        }
        // The relic list and a relic's page. They are drawn before the task
        // data is asked about, because they need none of it.
        if (current.view === "relics" || current.view === "relic") {
          renderRelicRoute(current);
          return;
        }
        // A relic's history and its diffs, which need no task data either.
        if (current.view === "relic-history" || current.view === "relic-diff") {
          renderRelicVersionsRoute(current);
          return;
        }
        if (current.view === "unknown") {
          document.title = "Page not found" + boardTitleSuffix;
          main.replaceChildren(routeMessage("Page not found", "This Workbook route does not exist."));
          return;
        }
        if (!tasksLoaded) {
          if (taskLoadFailed) {
            document.title = "Unable to load task" + boardTitleSuffix;
            main.replaceChildren(routeMessage("Unable to load task", "Task data could not be loaded. Retry now or return to the board.", true));
            return;
          }
          document.title = "Loading task" + boardTitleSuffix;
          main.replaceChildren(routeMessage("Loading task", "Reading the latest task state from this checkout."));
          return;
        }
        if (current.view === "new") {
          document.title = "New task" + boardTitleSuffix;
          // A refused create's values are handed to the next New Task render
          // and consumed by it, so a Back or a reload opens an empty form
          // rather than resurrecting a draft the user already dealt with.
          const draft = pendingNewTaskDraft;
          pendingNewTaskDraft = null;
          const form = taskForm("new", draft ||
            { title: "", description: "", status: current.status, priority: defaultPriority, labels: [] });
          main.replaceChildren(form);
          focusRearmedNewTask(form);
          return;
        }
        // The detail view projects pending intents too. Opening a card right
        // after moving it on the board would otherwise show the status the
        // server still holds, which reads as the move having been lost.
        //
        // A deleted task is not one of them, whether or not the board is
        // carrying it: a tombstone cannot be edited, so a form over one would
        // offer a Save the server refuses and a Delete that has already
        // happened. This is the answer that address gave before the Deleted
        // column existed and it is the answer it gives now — the column shows
        // the task and restores it, and restoring is what makes the form
        // openable again.
        const task = projectTasks(latestTasks).find((candidate) => candidate.id === current.id && !candidate.deleted);
        if (!task) {
          document.title = "Task not found" + boardTitleSuffix;
          main.replaceChildren(routeMessage("Task not found", "No task has the full ID " + current.id + "."));
          return;
        }
        document.title = withoutDirectionControls(task.title + boardTitleSuffix);
        const detail = taskForm("detail", task);
        const controller = activeDependencyController;
        const relationshipGeneration = relationshipRefreshGeneration;
        main.replaceChildren(detail);
        focusPendingRelationshipRecovery(
          task.id,
          controller,
          relationshipGeneration
        );
      }
