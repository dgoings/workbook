      // "Create more" is read when a save finishes rather than when it starts,
      // so a user who changes their mind while the create is in flight gets the
      // answer they left the box on.
      function createMoreToggle() {
        // The words first and the box after them, on the line Save is on: it
        // reads as a setting on that button rather than as a control of its own.
        const wrapper = document.createElement("label");
        wrapper.className = "create-more";
        const control = document.createElement("input");
        control.type = "checkbox";
        control.id = "task-create-more";
        control.checked = createMoreTasks;
        control.addEventListener("change", () => { createMoreTasks = control.checked; });
        const caption = document.createElement("span");
        text(caption, "Create more");
        wrapper.append(caption, control);
        return wrapper;
      }
      // The key a new task is minted under, when more than one of this
      // project's keys can mint it — and nothing at all when one can.
      //
      // That gate is the whole rule. A project with one active key has one
      // answer, and a select with a single option asks a question nobody can
      // answer differently, so a project that has never added a key is drawn
      // exactly the form it has always been drawn. Retired keys are not offered
      // either: a retired key mints nothing and the service refuses a create
      // naming one in core's own words, so listing it would be offering a
      // refusal.
      //
      // It reads the keys at the moment it is drawn rather than a set captured
      // when the page loaded, because a key change deliberately does not move
      // the vocabulary shape and so no board is ever asked to reload for one.
      // Every vocabulary this page receives replaces them — the read the
      // configuration route makes on entry, and the answer to every change made
      // there — so the next form opened offers the keys the page has heard
      // about. Nothing polls for them.
      //
      // `requested` is the key a form is being re-armed with, which is a refused
      // create's own choice: it stands if it can still mint, exactly as the
      // status and the priority that came back with it do.
      function newTaskKeyChooser(requested) {
        const active = activeKeyEntries();
        if (active.length < 2) return null;
        const wanted = requested || currentKey;
        const chosen = active.some((entry) => entry.key === wanted) ? wanted : active[0].key;
        return select("key", active.map((entry) => [entry.key, entry.key]), chosen);
      }
      function taskForm(mode, task) {
        const isNew = mode === "new";
        const relationshipDrafts = isNew
          ? newRelationshipDraftState()
          : null;
        // Attachments a create is carrying. Only a New Task form has them: on a
        // task that exists the panel below sends what it is given, because there
        // is a ref to send it to.
        const attachmentDrafts = isNew
          ? newAttachmentDraftState()
          : null;
        // Assignments a create is carrying, on a board that can assign at all:
        // one that cannot offers no control on a task's own page either.
        const assignmentDrafts = isNew && assignIdentity
          ? newAssignmentDraftState()
          : null;
        // Back is the board the reader came from, which is the same board every
        // landing in this client goes to: a reader who narrowed the board down,
        // opened a task and then changed their mind should not be punished for
        // not having saved. Computed as this form is built, and this form is
        // built by the render of the route it is on, so it is the state the
        // address was in when the reader left the board — or the bare board,
        // when they opened this page by its own address and there is no board
        // state to return to. The header's close, the footer's Back and a New
        // Task form's Cancel are all this one way out.
        const leaveAddress = boardAddress(deletedColumnShown, boardFilters);
        const section = document.createElement("section"); section.className = "task-route"; section.setAttribute("aria-labelledby", "task-form-title");
        const header = document.createElement("header"); header.className = "task-route__header task-route__header--form";
        const headingBlock = document.createElement("div"); headingBlock.className = "task-route__heading";
        // A new task has no name yet, so its header is a band that says what
        // the page is; the heading is still the route's name for the region.
        const heading = document.createElement("h2"); heading.id = "task-form-title"; text(heading, isNew ? "New task" : task.title);
        if (isNew) {
          header.className += " task-route__header--compact";
          headingBlock.append(heading);
        } else {
          const eyebrow = document.createElement("p"); eyebrow.className = "eyebrow"; text(eyebrow, "Task detail");
          headingBlock.append(eyebrow, heading, taskIDCopyControl(task.id, task.id));
        }
        // A link, as Back is, so it leaves exactly the way Back does: the
        // document listener turns it into the same render.
        const close = document.createElement("a");
        close.className = "task-route__close";
        close.dataset.taskClose = "";
        close.href = leaveAddress;
        close.setAttribute("aria-label", "Close");
        close.title = "Close";
        text(close, "×");
        header.append(headingBlock, close);

        const form = document.createElement("form"); form.className = "task-layout";
        const title = document.createElement("input"); title.id = "task-title"; title.name = "title"; title.required = true; title.value = task.title || "";
        const description = document.createElement("textarea"); description.id = "task-description"; description.name = "description"; description.value = task.description || "";
        // The description has two faces and one value.
        //
        // Read mode draws the markdown; edit mode is the textarea this form has
        // always had, with the same id, the same name and the same value. The
        // switch changes nothing else: the save still reads description.value,
        // still sends the field only when it differs from the task this form
        // rendered, and still carries the tip it rendered against. A reader who
        // never presses Edit is looking at a rendering of exactly the string a
        // save would send.
        const descriptionView = document.createElement("div");
        descriptionView.className = "description-read markdown";
        descriptionView.dataset.descriptionRead = "";
        // Focusable because it scrolls: the pane keeps the form's footer on the
        // screen by scrolling its own content, and a region only a pointer can
        // scroll is one a keyboard cannot read the end of.
        descriptionView.tabIndex = 0;
        descriptionView.setAttribute("aria-label", "Description");
        const descriptionToggleControl = document.createElement("button");
        descriptionToggleControl.type = "button";
        // Named for what it does rather than for what it sits beside. The
        // board's header already carries a data-description-toggle — the
        // setting that shows card descriptions — and two controls answering one
        // selector is a bug waiting for whichever of them querySelector reaches
        // first.
        descriptionToggleControl.className = "description-edit-toggle";
        descriptionToggleControl.dataset.descriptionEdit = "";
        // A new task has nothing to read, so it opens in the editor. An existing
        // one opens in read mode, which is what the page is for.
        let descriptionEditing = isNew;
        function renderDescriptionView() {
          const source = description.value || "";
          if (!source.trim()) {
            const empty = document.createElement("p");
            empty.className = "description-read__empty";
            text(empty, "No description yet.");
            descriptionView.replaceChildren(empty);
            return;
          }
          renderMarkdown(descriptionView, source, { taskID: isNew ? "" : task.id, attachments: task.attachments || [] });
        }
        // The attachment list the description's images resolve against, brought
        // up to the read that just landed. Only that list is adopted: the
        // description itself is a field on this form and no field follows a
        // poll, so the words on the screen stay the reader's own while the
        // pictures they name arrive.
        function adoptDescriptionAttachments(held) {
          const arrived = (held && held.attachments) || [];
          task = Object.assign({}, task, { attachments: arrived });
          if (!descriptionEditing) renderDescriptionView();
        }
        function renderDescriptionMode() {
          description.hidden = !descriptionEditing;
          descriptionView.hidden = descriptionEditing;
          text(descriptionToggleControl, descriptionEditing ? "Preview" : "Edit");
          descriptionToggleControl.setAttribute("aria-label",
            descriptionEditing ? "Preview the description" : "Edit the description");
          if (!descriptionEditing) renderDescriptionView();
        }
        descriptionToggleControl.addEventListener("click", () => {
          descriptionEditing = !descriptionEditing;
          renderDescriptionMode();
          // The control the reader asked for takes the caret. Leaving it on the
          // button would put the next keystroke nowhere on the way in, and
          // the preview is what the reader wants to see on the way out.
          if (descriptionEditing) description.focus(); else descriptionView.focus();
        });
        const status = select("status", statusDefinitions, task.status); status.required = true;
        showStatus(status, task.status);
        const priority = select("priority", priorityDefinitions, task.priority);
        showPriority(priority, task.priority);
        // Only a create chooses a key. A task that exists was minted under one
        // and its ID carries that key forever, so there is nothing here for a
        // task's own page to offer.
        const key = isNew ? newTaskKeyChooser(task.key) : null;
        const labels = labelEditor(task.labels || []);
        // Named, because the relationship region carries a form-status of its
        // own and "the first .form-status in the route" is the wrong element
        // as often as it is the right one.
        const result = document.createElement("p"); result.className = "form-status"; result.dataset.saveStatus = ""; result.setAttribute("role", "status"); result.setAttribute("aria-live", "polite");
        if (!isNew && pendingTaskMessages.has(task.id)) {
          text(result, pendingTaskMessages.get(task.id).join(" "));
          pendingTaskMessages.delete(task.id);
        }
        // A form opened to carry a refused create's values says why it is back.
        if (isNew && pendingNewTaskMessage) {
          text(result, pendingNewTaskMessage);
          pendingNewTaskMessage = "";
        }
        const actions = document.createElement("div"); actions.className = "form-actions";
        const primaryActions = document.createElement("div"); primaryActions.className = "form-primary-actions";
        const save = document.createElement("button"); save.className = "save-button"; save.type = "submit"; text(save, "Save");
        // A New Task form calls it Cancel and puts it in the corner a task's
        // Delete stands in, because leaving abandons what was typed; a task's
        // page keeps Back beside Save, because nothing there is lost by it.
        const back = document.createElement("a"); back.className = "board-link"; back.href = leaveAddress; text(back, isNew ? "Cancel" : "Back");
        primaryActions.append(save);
        if (isNew) primaryActions.append(createMoreToggle());
        else primaryActions.append(back);
        actions.append(primaryActions);
        // The way out of the one state this form does not leave on its own: the
        // task was created and some of its attachments were not attached. It is
        // shipped hidden and revealed there, because until then it would be a
        // link to a task that does not exist.
        const openCreatedTask = document.createElement("a");
        openCreatedTask.className = "board-link";
        openCreatedTask.dataset.openCreatedTask = "";
        openCreatedTask.hidden = true;
        text(openCreatedTask, "Open task");
        if (isNew) {
          primaryActions.append(openCreatedTask);
          const cancel = document.createElement("div"); cancel.className = "form-cancel";
          cancel.append(back); actions.append(cancel);
        }
		if (!isNew) {
		  const danger = document.createElement("div"); danger.className = "form-danger";
		  const remove = document.createElement("button"); remove.className = "delete-button"; remove.type = "button"; text(remove, "Delete");
		  remove.addEventListener("click", async () => {
		    remove.disabled = true; text(result, "");
		    // The board with its Deleted column shown, which is where the task
		    // now is: the reader lands looking at what they just deleted, with
		    // the Restore that undoes it on the card. Built rather than written
		    // out, so the filters the reader had set come along: the column is
		    // the only thing this landing decides.
		    try { await mutateTask("DELETE", "/api/tasks/" + encodeURIComponent(task.id)); navigate(boardAddress(true, boardFilters)); }
		    catch (error) { text(result, error instanceof Error ? error.message : "Task deletion failed."); }
		    finally { remove.disabled = false; }
		  });
		  danger.append(remove); actions.append(danger);
		}
        const editor = document.createElement("div"); editor.className = "task-editor";
        const descriptionField = field("Description", description, true, "field--description", descriptionToggleControl);
        // The read pane sits in the field beside the textarea rather than in a
        // wrapper around it: the textarea stays a direct child of
        // .field--description, which is the element that gives it its height,
        // and exactly one of the two is ever displayed, so the field still lays
        // out as the two rows it always was.
        descriptionField.insertBefore(descriptionView, description);
        renderDescriptionMode();
        editor.append(
          field("Title", title, true),
          descriptionField
        );
        // The sidebar is the same on both pages, in the order a reader reaches
        // for its parts: the task's own properties, the tasks it depends on and
        // blocks, who holds it, and what is attached to it. On a New Task form
        // the last two are staged and sent once the task exists; on a task's
        // page they write as they are used. Only what grows without bound —
        // the comment thread and the change log — goes below the form.
        const sidebar = document.createElement("aside"); sidebar.className = "task-sidebar"; sidebar.setAttribute("aria-label", "Task properties, relationships, assignments and attachments");
        const properties = document.createElement("section"); properties.className = "task-properties";
        const propertiesHeading = document.createElement("h3"); text(propertiesHeading, "Properties");
        properties.append(
          propertiesHeading,
          field("Status", status),
          field("Priority", priority)
        );
        if (key) properties.append(field("Key", key));
        properties.append(field("Labels", labels.region, true, "field--labels"));
        const relationshipMount = document.createElement("div"); relationshipMount.dataset.relationshipMount = "";
        const assignmentDraftPanel = assignmentDrafts ? taskAssignmentDrafts(assignmentDrafts) : null;
        const attachmentDraftPanel = isNew ? taskAttachmentDrafts(attachmentDrafts) : null;
        // What a departure would announce follows the list it is about, so the
        // Remove beside a failed row retires the report along with the row.
        if (attachmentDraftPanel) {
          attachmentDraftPanel.onChange = () => publishOutstandingAttachments();
        }
        sidebar.append(properties, relationshipMount);
        if (assignmentDraftPanel) sidebar.append(assignmentDraftPanel.section);
        if (attachmentDraftPanel) sidebar.append(attachmentDraftPanel.section);
        const footer = document.createElement("footer"); footer.className = "task-actions";
        footer.append(result, actions);
        form.append(editor, sidebar, footer);

        let taskWasCreated = false;
        // The task this form made, and whether anything it staged is still
        // owed to it. Together they are the one state a New Task form stays on
        // screen in: the create succeeded, some attachments did not, and the
        // only work left is to send those — to that task, never to another.
        let createdTaskID = "";
        let outstandingAttachments = false;
        let retryingAttachments = false;
        // The tip this form was rendered from. A save names it so a form opened
        // before someone else's change is refused rather than reverting them,
        // and a refusal re-bases it so a deliberate retry lands on the version
        // that exists.
        let expectedHead = isNew ? "" : task.head || "";
        const controls = { title, description, status, priority, labels, key };
        // Published for the length of this render, so a restore that is about to
        // replace this form can save what is in it. renderRoute clears it, so
        // the reader never outlives the node it reads from.
        if (isNew) readOpenNewTaskValues = () => allTaskValues(controls);
        // Re-reads this form against the task the board now holds, in place. A
        // field the save would not send is a field the reader has not been near:
        // it is displaying exactly what this form rendered, so it follows the
        // board. A field the save would send is an edit in progress and stays
        // as typed, down to the caret.
        //
        // The baseline moves either way, so the next save still carries only
        // the fields this reader changed — now measured against the version
        // that exists rather than against one the server refused.
        function reviseAgainstTask(held) {
          const edited = changedTaskValues(task, controls);
          if (!("title" in edited)) title.value = held.title || "";
          if (!("description" in edited)) description.value = held.description || "";
          // Labels are asked a different question than the rest. A save folds
          // the text still in the input into the set, so changedTaskValues
          // calls the field edited the moment a reader types a letter into it —
          // and a reader partway through a word has decided nothing about the
          // set. Left at that, an untouched set would keep the refused value
          // while the baseline moved to the server's, and the next save would
          // delete a label this reader was never shown. The chiclets are what
          // they have actually decided; the word in progress stays in the
          // input either way.
          if (sameLabels(labels.committed(), task.labels || [])) labels.replace(held.labels || []);
          // The heading names the task and so does the browser tab. Both are a
          // rendering of the task rather than of this form's draft — neither
          // has ever followed the title field's keystrokes — so they follow the
          // task the board holds even while the field carries an edit.
          text(heading, held.title || "");
          document.title = (held.title || "") + boardTitleSuffix;
          if (!("status" in edited)) showStatus(controls.status, held.status);
          // Both vocabulary fields are always showable: each builds a
          // placeholder naming a value this project cannot resolve, so the
          // control and the baseline agree whatever the board hands over. That
          // agreement is the whole point of routing through these rather than
          // showSelected — a control still displaying the value this form
          // rendered while the baseline had moved is exactly what turns a
          // correction nobody asked for into an edit the next save sends.
          if (!("priority" in edited)) showPriority(controls.priority, held.priority);
          // A copy, because the board holds this task too: the baseline this
          // form measures its next save against is the form's own.
          task = Object.assign({}, held);
          // Drawn after the task is adopted, so the pane follows both halves of
          // what a poll can move: the description itself, when this reader has
          // not touched the field, and the attachment list an image reference
          // resolves against — a screenshot uploaded in the panel below appears
          // in the description that referenced it on the next poll, with no edit
          // to the text at all. In edit mode it renders nothing; the textarea is
          // what is on the screen and the pane is redrawn when Done returns to
          // it.
          if (!descriptionEditing) renderDescriptionView();
          // Re-based the way a refused save re-bases this form: it proposes the
          // tip that now exists, so the reader's next save applies their edits
          // to it instead of being refused for naming a head that has moved on.
          expectedHead = held.head || "";
        }
        if (!isNew) {
          openDetailForm = {
            taskID: task.id,
            revise: reviseAgainstTask,
            report: (message) => text(result, message)
          };
        }
        // The task exists and some of its attachments do not. Sends exactly what
        // is still staged, to exactly the task this form made — which is what
        // keeps a retry from creating a second one, and why the create path is
        // not re-entered at all.
        async function retryOutstandingAttachments() {
          // Asked as well as disabling the button, because a disabled submit
          // does not stop a Return pressed in the title field: implicit
          // submission fires the form's listener either way, and a second run
          // over a list the first is still walking would send an item twice.
          if (retryingAttachments) return;
          retryingAttachments = true;
          save.disabled = true;
          text(result, "");
          attachmentDraftPanel.setBusy(true);
          try {
            const outcome = await persistAttachmentDrafts(createdTaskID, attachmentDrafts);
            attachmentDraftPanel.setBusy(false);
            attachmentDraftPanel.render();
            if (outcome.failures.length) {
              publishOutstandingAttachments();
              reportOutstandingAttachments(createdTaskID, outcome, null);
              return;
            }
            outstandingAttachments = false;
            // Retired before the navigation below, which is the render that
            // would otherwise announce a loss that did not happen.
            outstandingAttachmentReport = null;
            // Everything this create set out to do has happened, so it lands on
            // the task rather than on the board. The reader has spent this whole
            // detour on these attachments and the page that lists them is the
            // only honest confirmation that they are finally there.
            navigate(taskPath(createdTaskID));
            await refresh();
          } finally {
            retryingAttachments = false;
            attachmentDraftPanel.setBusy(false);
            if (outstandingAttachments) save.disabled = false;
          }
        }
        // The create succeeded and some of its attachments did not.
        //
        // Nothing navigates and nothing is discarded. The task exists — said
        // first, and by ID, because that is the part a reader must not have to
        // guess at — and the files that did not land are still in the list with
        // the reason each one failed beside it. Save now means "send those
        // again". Landing on the board and reporting from there was the obvious
        // alternative and it is not available: a File leaves with the node
        // holding it, so there would be nothing left to retry with.
        async function holdForOutstandingAttachments(recovery, outcome) {
          createdTaskID = recovery.taskID;
          outstandingAttachments = true;
          createdTaskRecovery = null;
          // The relationships this create could not write are staged on the
          // task's own form, where the sidebar that retries them is. They are
          // named here too, because here is where the reader is standing.
          stageCreatedTaskDrafts(recovery);
          text(save, "Retry attachments");
          openCreatedTask.href = taskPath(recovery.taskID);
          openCreatedTask.hidden = false;
          // Handed back to the reader: the walk is over, and the rows it left
          // behind are ones they have to be able to remove or add to. Released
          // before the render, because a render claims its controls against
          // whatever the panel's busy state is at the time.
          attachmentDraftPanel.setBusy(false);
          attachmentDraftPanel.render();
          publishOutstandingAttachments();
          reportOutstandingAttachments(recovery.taskID, outcome, recovery);
          // The board is one Back away and has a task on it it has not read.
          await refresh();
        }
        // Names what is still owed, for the render that replaces this form to
        // report. Names only: the files are unreachable once this node is, and
        // publishing a handle to them would be publishing a handle to nothing.
        function publishOutstandingAttachments() {
          if (!outstandingAttachments || !hasAttachmentDrafts(attachmentDrafts)) {
            // Nothing is owed, so a departure has nothing to announce. Said as a
            // clearing rather than as a skip, because this runs again every time
            // the list changes: a reader who removes the last failed row has
            // decided against it, and leaving after that is not a loss.
            outstandingAttachmentReport = null;
            return;
          }
          outstandingAttachmentReport = {
            taskID: createdTaskID,
            names: attachmentDrafts.items.map((item) =>
              item.kind === "link" ? (item.label || item.url) : item.name)
          };
        }
        function reportOutstandingAttachments(taskID, outcome, recovery) {
          const failed = outcome.failures.length;
          const said = ["Task " + taskID + " was created, but " + failed +
            (failed === 1 ? " attachment was not attached." : " attachments were not attached.") +
            " Each one says why below and is still staged; press Retry attachments to send them to" +
            " that task, or open it and attach them there."];
          if (recovery && hasRelationshipDrafts(recovery.failures)) {
            said.push("Some relationships were not saved either; open the task to retry those.");
          }
          if (recovery && hasAssignmentFailures(recovery)) {
            said.push("Some assignments were not saved either; open the task to retry those.");
          }
          (recovery ? recovery.warnings : outcome.warnings).forEach((warning) => said.push(warning));
          text(result, said.join(" "));
        }
        form.addEventListener("submit", async (event) => {
          event.preventDefault();
          if (isNew && taskWasCreated) {
            // The server has made this task. The form is only still here at all
            // because attachments are owed to it, and Save is the control that
            // sends them.
            if (outstandingAttachments) return retryOutstandingAttachments();
            return;
          }
          const values = isNew ? allTaskValues(controls) : changedTaskValues(task, controls);
          if (!isNew) {
            if (Object.keys(values).length === 0) {
              // The server refuses an update that changes nothing, and it is
              // right to: there is no operation to record. Saving an untouched
              // form is not an error though, it is finished, so it leaves the
              // way an accepted save does — onto the board the reader came from,
              // filters and all.
              navigate(boardAddress(deletedColumnShown, boardFilters));
              return;
            }
            if (expectedHead) values.expectedHead = expectedHead;
          }
          // A create that staged nothing beside the task itself is answered by
          // the board rather than by this form, and leaves immediately. One that
          // staged relationships or attachments waits: those writes need the ID
          // the server has not assigned yet, and the controls that report and
          // retry their failures are destroyed by an instant landing. An
          // attachment has more riding on that than a relationship does — its
          // bytes exist nowhere but this node — so it is the same rule for the
          // same reason, held harder.
          if (isNew && !hasRelationshipDrafts(relationshipDrafts) &&
              !hasAssignmentDrafts(assignmentDrafts) &&
              !hasAttachmentDrafts(attachmentDrafts)) {
            taskWasCreated = true;
            return createOptimistically(values);
          }
          let createdDurably = false;
          save.disabled = true;
          if (isNew) relationshipController.setBusy(true);
          // Frozen for the whole run, exactly as the retry below freezes it and
          // for a sharper reason. persistAttachmentDrafts walks the list as it
          // stood when it started, so a file staged while it is running is one
          // it will never see: it would sit in the list reading "3 staged",
          // upload nothing, fail nothing — and a run with no failures navigates,
          // which takes the File with the node and reports not one word. A list
          // that cannot change under the walk cannot lose anything that way.
          if (isNew) attachmentDraftPanel.setBusy(true);
          if (assignmentDraftPanel) assignmentDraftPanel.setBusy(true);
          text(result, "");
          try {
            const document = await mutateTask(isNew ? "POST" : "PATCH", isNew ? "/api/tasks" : "/api/tasks/" + encodeURIComponent(task.id), values);
            if (isNew) {
              createdDurably = true;
              taskWasCreated = true;
              const taskID = document.task.id;
              const relationshipOutcome = await persistRelationshipDrafts(
                taskID,
                relationshipDrafts
              );
              relationshipDrafts.dependsOn.clear();
              relationshipDrafts.blocks.clear();
              // Who holds it, after the edges and before the uploads, for the
              // reason the edges go first: a cheap write should not wait behind
              // a megabyte. What fails is handed to the task's own page.
              const assignmentOutcome = assignmentDrafts
                ? await persistAssignmentDrafts(taskID, assignmentDrafts)
                : { failures: [], warnings: [] };
              if (assignmentDraftPanel) assignmentDraftPanel.render();
              // After the relationships and never before the create. The task
              // has to exist to have a ref to attach to, and the relationships
              // are cheap writes that should not be left untried behind a
              // megabyte of upload.
              const attachmentOutcome = await persistAttachmentDrafts(taskID, attachmentDrafts);
              const recovery = {
                taskID,
                status: document.task.status,
                failures: relationshipOutcome.failures,
                assignmentFailures: assignmentOutcome.failures,
                warnings: [
                  ...mutationWarnings(document),
                  ...relationshipOutcome.warnings,
                  ...assignmentOutcome.warnings,
                  ...attachmentOutcome.warnings
                ]
              };
              if (attachmentOutcome.failures.length) {
                return holdForOutstandingAttachments(recovery, attachmentOutcome);
              }
              createdTaskRecovery = recovery;
              const creationOwner = {
                route: section,
                controller: relationshipController
              };
              const ownsCreation = () =>
                main.firstElementChild === creationOwner.route &&
                ownsDependencyController(creationOwner.controller) &&
                createdTaskRecovery === recovery;
              const abandonDetachedRecovery = () => {
                if (createdTaskRecovery === recovery) {
                  createdTaskRecovery = null;
                }
              };
              const retryCreatedTaskRefresh = async () => {
                const retried = await currentRefreshResult(refresh());
                if (!ownsCreation()) {
                  abandonDetachedRecovery();
                  return;
                }
                if (retried.status !== "applied" ||
                    retried.relationshipContext.status !== "applied") {
                  return;
                }
                finishCreatedTask(recovery);
              };
              const refreshed = await currentRefreshResult(refresh());
              if (!ownsCreation()) {
                abandonDetachedRecovery();
                return;
              }
              if (refreshed.status !== "applied" ||
                  refreshed.relationshipContext.status !== "applied") {
                // The one exit that leaves this form standing after a create
                // that owed nothing. Every attachment landed and the list is
                // empty, but the run drew nothing after emptying it, so the
                // rows were still on the screen — under a panel the freeze had
                // no other way out of, because the two paths that release it
                // are the two this is not. Read as a create that lost the
                // files it is still showing, held by controls that would never
                // work again. So the list is drawn as it now stands, which is
                // empty, and handed back.
                attachmentDraftPanel.setBusy(false);
                attachmentDraftPanel.render();
                showCreatedTaskRefreshFailure(result, retryCreatedTaskRefresh);
                return;
              }
              finishCreatedTask(recovery);
            } else if (main.firstElementChild !== section) {
              // The save landed after the reader had gone somewhere else.
              // Returning them to the board now would take away the route they
              // deliberately chose while this request was open, and an accepted
              // save has never been announced in this client anyway: the board
              // showing the new value is the report. So this only makes sure it
              // is showing it.
              await refresh();
            } else {
              // The board the reader came from rather than the bare one: an
              // accepted save is not a reason to undo the narrowing they were
              // reading the board through.
              navigate(boardAddress(deletedColumnShown, boardFilters));
              await refresh();
            }
          } catch (error) {
            // A save can outlive the form it was made in: the reader stops
            // waiting on a slow request and goes back to the board, or a
            // restore renders a New Task form over this route. The node this
            // handler reports into is gone by then, and returning quietly is
            // how the entire outcome used to disappear — the reader left
            // believing a save was on its way and nothing ever contradicted
            // them. It follows them instead, to the two surfaces that outlive a
            // route.
            //
            // A create reaching here outlived its form the same way, and has more
            // to lose by it: the server made no task, so its outcome has no card
            // and no task form to reach, and the draft it carried left with the
            // node. The report it goes to is therefore the only copy of that
            // draft, and it hands it back rather than only naming what failed.
            if (main.firstElementChild !== section) {
              if (isNew) reportDetachedCreationFailure(values, error);
              else reportDetachedSaveFailure(task.id, error);
              return;
            }
            if (!isNew && error && error.category === "stale-write") {
              // The edits stay in the form. Re-rendering it from the refreshed
              // task would discard the work the refusal just protected, and the
              // baseline stays where it was so a retry still sends only the
              // fields this user changed — the concurrent edit that caused the
              // refusal survives it.
              const rebase = await refreshedTaskHead(task.id);
              // A refresh that read the board and found no head for this task
              // is the board saying the task is gone: `/api/tasks` carries only
              // tasks that have not been tombstoned, and the likeliest refusal
              // of all is one another clone deleted. A refresh that could not
              // read the board proves nothing of the kind, which is why both
              // halves are asked.
              const deleted = rebase.boardRead && !rebase.head;
              // The form can go while that refresh is in flight, and a re-base
              // has nothing left to re-base. The outcome still has somewhere to
              // be, so it goes there rather than being dropped one await later
              // than it would have been above — and it takes what the refresh
              // learned with it, because the report is about to decide whether
              // there is a task left to offer.
              if (main.firstElementChild !== section) {
                reportDetachedSaveFailure(task.id, error, deleted);
                return;
              }
              if (rebase.head) expectedHead = rebase.head;
              // Only a refresh that read the board can invite the retry.
              // Without one this form is still proposing the head the server
              // just refused, and "save again to apply them to the version the
              // server holds" would be sending the reader into the identical
              // refusal on the strength of a version nobody has read. What it
              // says instead is what is true: the version to save against has
              // not been read yet. The poll is already working on that, and the
              // banner says so while it is; the next refusal whose refresh
              // reads the board re-bases the form and brings the invitation
              // back with it.
              //
              // A refresh that read the board and found the task deleted may
              // not invite it either, for the opposite reason: there is nothing
              // to re-base onto, so the head stays the refused one and the
              // retry is refused identically for as long as the task is gone.
              // Neither wording above is true here — the version the server
              // holds does not exist, and the board is not behind — so this
              // says what is left. The edits are named as kept but unsavable
              // rather than offered back into a New Task form: the only
              // machinery that hands values back creates a *different* task,
              // which is a decision for the reader and not a consolation to
              // hand them mid-sentence.
              text(result, deleted
                ? "That task was deleted elsewhere, so the server holds no version of it to save to. Your edits are kept here, but another save would be refused too; copy anything you want to keep before you leave this form."
                : rebase.boardRead
                  ? "That task changed elsewhere. Your edits are kept; save again to apply them to the version the server holds."
                  : "That task changed elsewhere, and the latest version could not be loaded. Your edits are kept; save again once the board is current.");
              return;
            }
            text(result, error instanceof Error
              ? error.message
              : isNew ? "Task creation failed." : "Task update failed.");
          } finally {
            if (!createdDurably) {
              save.disabled = false;
              if (isNew) relationshipController.setBusy(false);
              // The server made no task, so everything staged is still a draft
              // this form is holding and the reader has to be able to change it
              // before saving again.
              if (isNew) attachmentDraftPanel.setBusy(false);
              if (assignmentDraftPanel) assignmentDraftPanel.setBusy(false);
            } else if (outstandingAttachments) {
              // The form is staying, so its Save has to work — as the retry it
              // now says it is. The relationship sidebar does not come back: its
              // drafts have been written or handed to the task's own form, and
              // adding another edge here would compose it against a create that
              // is over.
              save.disabled = false;
            }
          }
        });
        section.append(header, form);
        if (!isNew) {
          section.className = "task-route task-route--history";
          // The thread panels write to this task, so they move the very head
          // this form proposes and they read it back through the same handle a
          // dependency edge does: a comment added here must not make the next
          // save a conflict with a change the reader made seconds earlier.
          const head = {
            read: () => expectedHead,
            adopt: (value) => { if (value) expectedHead = value; }
          };
          const owner = () => main.firstElementChild === section;
          const attachments = taskAttachments(task.id, task, head, owner);
          const comments = taskComments(task.id, task, head, owner);
          const assignments = taskAssignments(task.id, head, owner);
          openThreadPanels = {
            taskID: task.id,
            // The description above these panels follows the same read, for the
            // one thing it can follow: an image in it is a reference to an
            // attachment of this task, and the attachment list is what the poll
            // brings. So a screenshot uploaded in the panel below appears in
            // the description that already named it, without a reload and
            // without an edit to a word of the text. The text itself does not
            // follow the poll — no field on this form does, which is what keeps
            // a reader's edits under their caret.
            // The assignments section follows the same read, and follows it
            // through the presentation rather than the task: a row's value and
            // its withdrawal are derived on the server, so what a poll brings
            // this section is the view beside the task rather than the task
            // itself. A CLI assign or unassign made
            // elsewhere therefore appears here within a poll tick — and, like
            // every other panel on this page, it moves nothing the form above it
            // is holding, because no field on that form follows the poll at all.
            follow: (held, view) => { attachments.follow(held); comments.follow(held); adoptDescriptionAttachments(held); assignments.follow(view); },
            // How long each assignment has stood, re-worded on the page's own
            // tick whatever the poll answered: a board left idle is answered
            // 304 and brings this section nothing, and its rows still age.
            age: () => assignments.age()
          };
          // Attachments and assignments stand in the sidebar, as they do on a
          // New Task form: both are bounded at fifty rows, and both are part of
          // what the task is rather than what has been said about it. The
          // assignments go between the relationships and the attachments, and
          // are inserted there again whenever a CLI assign lands on a task this
          // page drew without them. Below the form are only the comment thread
          // and the change log, the two things with no length a page can rely
          // on.
          sidebar.append(attachments.section);
          assignments.mountBefore(sidebar, attachments.section);
          section.append(comments.section, taskHistory(task.id));
          assignments.follow(latestPresentation.get(task.id));
        }
        const failedDrafts = isNew
          ? relationshipDrafts
          : pendingRelationshipDrafts.get(task.id) || null;
        const relationshipController = taskRelationships(isNew ? "" : task.id, failedDrafts);
        // A "Depends On" edge is stored on the dependent, which is this task, so
        // adding or removing one from this sidebar moves the very head this form
        // proposes. Following it keeps the next save from being refused as a
        // conflict with a change the user made here seconds earlier.
        relationshipController.adoptTaskHead = (head) => { if (head) expectedHead = head; };
        relationshipMount.append(relationshipController.region);
        activeDependencyController = relationshipController;
        if (!isNew && pendingDetailRelationshipContextTaskID === task.id) {
          pendingDetailRelationshipContextTaskID = null;
        } else {
          refreshRelationshipContext(
            relationshipController,
            ++relationshipRefreshGeneration
          );
        }
        return section;
      }
      function allTaskValues(controls) {
        const values = {
          title: controls.title.value,
          description: controls.description.value,
          status: controls.status.value,
          priority: controls.priority.value,
          labels: controls.labels.values()
        };
        // The key, and only when the reader chose one other than the current
        // key. An absent key is the current key — that is what the route does
        // with a create naming none, and what every client predating several
        // keys sends — so a form that offered no choice, and one whose choice
        // was the standing default, send byte for byte the create they always
        // sent.
        if (controls.key && controls.key.value && controls.key.value !== currentKey) {
          values.key = controls.key.value;
        }
        return values;
      }
      // An update carries the fields this form changed and no others. Sending
      // all five on every save is last-writer-wins even when the heads agree:
      // re-asserting the title this form rendered is indistinguishable from
      // editing it, so a description-only save would revert a teammate's rename
      // that landed in between.
      function changedTaskValues(task, controls) {
        const values = {};
        if (controls.title.value !== (task.title || "")) values.title = controls.title.value;
        if (controls.description.value !== (task.description || "")) values.description = controls.description.value;
        // A status the client does not know renders as an unselected
        // placeholder. Leaving it alone is not a request to clear the status.
        if (controls.status.value && controls.status.value !== task.status) values.status = controls.status.value;
        // And a priority this project cannot resolve renders as the same
        // unselected placeholder, for the same reason: leaving it alone is not a
        // request to move the task off the priority it was filed under.
        if (controls.priority.value && controls.priority.value !== task.priority) values.priority = controls.priority.value;
        const labels = controls.labels.values();
        if (!sameLabels(labels, task.labels || [])) values.labels = labels;
        return values;
      }
      // Labels are a set: the server sorts and de-duplicates them
      // (normalizeLabels), so retyping "docs, web" as "web, docs" changes
      // nothing. Comparing positionally would call that an edit, send it, and
      // land the server's "update does not change task" verbatim in the form —
      // the raw error the empty-diff guard exists to keep out of it.
      function sameLabels(entered, rendered) {
        const left = [...new Set(entered)].sort();
        const right = [...new Set(rendered)].sort();
        return left.length === right.length && left.every((label, at) => label === right[at]);
      }
