      function threadButton(caption, primary = false) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = primary ? "thread-button thread-button--primary" : "thread-button";
        text(button, caption);
        return button;
      }
      // Every thread change names the tip this page read, exactly as a save from
      // the form beside it does, and leaves the member out when there is none.
      function headed(head, values) {
        const expected = head.read();
        return expected ? Object.assign({}, values, { expectedHead: expected }) : values;
      }
      // A panel on a task's page: a heading with a count, a list, a form, and one
      // line of feedback. Comments and attachments are the same shape and report
      // the same way, so everything but the rows is built once.
      //
      // They write directly rather than through the board's optimistic queue,
      // and the difference is deliberate. That queue exists so a drag paints
      // before the server answers, and it can afford to because a status change
      // is one field this client can predict. A comment's identity, author and
      // timestamps are minted by the operation that records it, and an
      // attachment's bytes are not on the board at all — an optimistic row for
      // either would be a row with no identity to edit or remove, drawn under an
      // attribution this client invented. So these write, wait, and draw what
      // came back, which is how the form on the same page has always saved.
      function taskPanel(taskID, name, headingText, owner) {
        const section = document.createElement("section");
        section.className = "task-panel task-panel--" + name;
        section.dataset.taskPanel = name;
        section.setAttribute("aria-label", headingText);
        const header = document.createElement("div");
        header.className = "task-panel__header";
        const heading = document.createElement("h3");
        text(heading, headingText);
        const count = document.createElement("p");
        count.className = "task-panel__count";
        count.dataset.panelCount = "";
        header.append(heading, count);
        const list = document.createElement("ul");
        list.className = "task-panel__list";
        list.dataset.panelList = "";
        // Drawn while empty rather than created when there is something to say,
        // for the reason the label editor's announcement is: an element created
        // in the same frame as its first message never announces it.
        const empty = document.createElement("p");
        empty.className = "task-panel__empty";
        empty.dataset.panelEmpty = "";
        const status = document.createElement("p");
        status.className = "form-status";
        status.dataset.panelStatus = "";
        status.setAttribute("role", "status");
        status.setAttribute("aria-live", "polite");
        const controls = [];
        const panel = {
          taskID, section, list, empty, status, owns: owner, busy: false,
          adoptHead() {},
          setCount(value) { text(count, value); },
          report(message) { text(status, message); },
          setBusy(value) {
            panel.busy = value;
            controls.forEach((control) => { control.disabled = value; });
          },
          // Every control the panel is currently drawing. The list is emptied by
          // each render and refilled from it, so a request in flight disables
          // the rows that arrived during it and nothing holds a node that has
          // left the document.
          claim(control) {
            controls.push(control);
            control.disabled = panel.busy;
            return control;
          },
          resetControls() { controls.length = 0; }
        };
        section.append(header, list, empty);
        return panel;
      }
      // One thread change: sent, waited on, and drawn from what came back.
      //
      // The refusals are the form's refusals, in the panel the change was made
      // in. A stale head is re-based against the version the server holds and
      // said out loud, because these panels have no queue to re-base them; a
      // change that outlives the page it was made on follows the reader to the
      // notice, exactly as a detached save does.
      async function writeThreadChange(panel, method, path, values, describe) {
        panel.setBusy(true);
        panel.report("");
        try {
          // Named `answer` rather than `document`, which is what the older call
          // sites in this file call it: a const by that name shadows the global
          // `document` for the whole function body, so any later — or textually
          // earlier — createElement in the same function throws rather than
          // building a node. Nothing here needs the global, and the next edit
          // that does should not have to notice.
          const answer = await mutateTask(method, path, values);
          panel.adoptHead(answer.task && answer.task.head || "");
          // The model holds what the server just answered with, so the card, the
          // next poll and this panel cannot disagree about the task in between.
          replaceProjectedTask(answer.task);
          rerender();
          if (!panel.owns()) return answer;
          // The answer travels beside the task because one panel is not drawn
          // from the task: an assignment row's value and its withdrawal are the
          // server's derivations, and the assignments route carries them in the
          // answer. Every other panel takes the task and ignores the
          // second argument, exactly as it did before there was one.
          panel.follow(answer.task, answer);
          const warnings = mutationWarnings(answer);
          if (warnings.length) panel.report("Saved durably. " + warnings.join(" "));
          return answer;
        } catch (error) {
          if (!panel.owns()) {
            reportDetachedThreadFailure(panel.taskID, describe, error);
            return null;
          }
          if (error && error.category === "stale-write") {
            const rebase = await refreshedTaskHead(panel.taskID);
            if (rebase.head) panel.adoptHead(rebase.head);
            if (!panel.owns()) {
              reportDetachedThreadFailure(panel.taskID, describe, error);
              return null;
            }
            const held = projectTasks(latestTasks).find((candidate) => candidate.id === panel.taskID);
            if (held) panel.follow(held);
            panel.report(rebase.boardRead
              ? "That task changed elsewhere, so " + describe + " was not saved. This list now shows the version the server holds; try again."
              : "That task changed elsewhere and the board could not be refreshed, so " + describe + " was not saved. Try again once the board is current.");
            return null;
          }
          panel.report(error instanceof Error ? error.message : "That change failed.");
          return null;
        } finally {
          panel.setBusy(false);
        }
      }
      // A thread change can outlive the panel it was made in, the way a save can
      // outlive its form: the reader leaves the task while the request is open.
      // The panel it would report into is gone by then, so the outcome follows
      // them to the notice, which outlives every route.
      function reportDetachedThreadFailure(taskID, describe, error) {
        const open = document.createElement("a");
        open.href = taskPath(taskID);
        text(open, "Open task");
        showNotice("error",
          "On task " + nameTask(taskID) + ", " + describe + " was not saved. The request was still open " +
            "when you left the task, and anything it carried is gone. " +
            (error && error.category === "stale-write"
              ? "The task changed elsewhere, so the change was refused."
              : (error instanceof Error ? error.message : "The change failed.")),
          open);
      }
      // The control that names who to assign: one field, the hint that says
      // what a blank means, and one button. A task's own page sends what it is
      // given and a New Task form stages it, so both build it here and each
      // decides what its submit does.
      function assignmentAdder(action) {
        const form = document.createElement("form");
        form.className = "assignment-add";
        form.dataset.assignmentForm = "";
        const label = document.createElement("label");
        label.setAttribute("for", "assignment-to");
        text(label, "Assign someone");
        const field = document.createElement("input");
        field.type = "text";
        field.id = "assignment-to";
        field.name = "assignee";
        // The identity this board writes as, offered rather than filled in. A
        // placeholder says "leave this alone and it is you" without putting a
        // value in the field that a reader who meant somebody else would have
        // to clear first — and the empty value is what core resolves, so who
        // is acting is decided in one place rather than two.
        field.placeholder = assignIdentity;
        const hint = document.createElement("p");
        hint.className = "assignment-add__hint";
        text(hint, "Leave blank to assign yourself. Add a /label to name one agent, as in " +
          assignIdentity + "/impl-1.");
        const actions = document.createElement("div");
        actions.className = "assignment-add__actions";
        const submit = threadButton(action, true);
        submit.type = "submit";
        actions.append(submit);
        form.append(label, field, hint, actions);
        return { form, field, submit };
      }
      // Who a staged value names, drawn the way a recorded assignment is: the
      // address, and the agent label styled apart from it. A blank value is
      // this board's identity, which is what core will record it as. The split
      // is for drawing only; the value sent is the one the reader typed.
      function stagedAssignmentWho(value) {
        const who = document.createElement("p");
        who.className = "assignment__who";
        const shown = value || assignIdentity;
        const slash = shown.indexOf("/");
        const principal = document.createElement("span");
        principal.className = "assignment__principal";
        text(principal, slash < 0 ? shown : shown.slice(0, slash));
        who.append(principal);
        if (slash >= 0) {
          const label = document.createElement("span");
          label.className = "assignment__label";
          text(label, shown.slice(slash));
          who.append(label);
        }
        return who;
      }
      // An assignment that is not recorded yet: staged on a New Task form, or
      // handed to a task's page by a create that could not save it. The second
      // kind says so and why, and offers the retry a failed dependency offers.
      function assignmentDraftRow(panel, draft, actions) {
        const item = document.createElement("li");
        item.className = "assignment";
        item.dataset.assignmentDraft = draft.key || draft.value;
        item.append(stagedAssignmentWho(draft.value));
        if (draft.error !== undefined && actions.retry) {
          const state = document.createElement("strong");
          state.className = "relationship-state";
          text(state, "Not saved");
          item.append(state);
          if (draft.error) {
            const failure = document.createElement("p");
            failure.className = "assignment__failure";
            text(failure, draft.error);
            item.append(failure);
          }
        }
        const controls = document.createElement("div");
        controls.className = "assignment__actions";
        if (actions.retry) {
          const retry = panel.claim(threadButton("Retry"));
          retry.addEventListener("click", () => actions.retry());
          controls.append(retry);
          if (actions.retryControl) actions.retryControl(retry);
        }
        const remove = panel.claim(threadButton("Remove"));
        remove.addEventListener("click", () => actions.remove());
        controls.append(remove);
        item.append(controls);
        return item;
      }
      // ─── Assignments staged on a New Task form ──────────────────────────────
      //
      // An assignment is recorded on the task's own ref, so a create form holds
      // what it is given and sends it once the create has answered with an ID,
      // exactly as it holds a dependency or an attachment.
      function newAssignmentDraftState() {
        return { items: [], sequence: 0 };
      }
      function hasAssignmentDrafts(draftState) {
        return Boolean(draftState) && draftState.items.length > 0;
      }
      // Every staged assignment, sent in the order it was staged to the task
      // the create just made. Through the assignment route the task page uses,
      // so what is recorded is what that page's control records; with no
      // expectedHead, for the reason the dependency and attachment drafts send
      // none — nobody else has heard of this task yet. One refusal does not stop
      // the run: each item is owed its own answer.
      // A staged value as the server records it: principal[/label], split at
      // the first separator as core splits it, with a blank principal standing
      // for this board's identity.
      function stagedAssignmentHolder(value) {
        const cut = value.indexOf("/");
        const principal = (cut < 0 ? value : value.slice(0, cut)) || assignIdentity;
        return { principal, value: principal + (cut < 0 ? "" : value.slice(cut)) };
      }
      // Whether an "also assigned to" answer names only assignees this form
      // staged. The server's warning is prose — "task X is also assigned to"
      // and the other holders' values joined with commas — so the holders are
      // read from the task the same answer carries instead: core names as
      // others every assignment under a principal this write did not name, and
      // each is compared by the value the warning would have spelled. Anything
      // this cannot account for counts as news.
      function sharedOnlyWithStaged(answer, item, staged) {
        const assignments = answer && answer.task && answer.task.assignments;
        if (!Array.isArray(assignments)) return false;
        const principal = stagedAssignmentHolder(item.value).principal;
        return assignments
          .filter((held) => held.principal !== principal)
          .every((held) => staged.has(held.principal + (held.label ? "/" + held.label : "")));
      }
      async function persistAssignmentDrafts(taskID, draftState) {
        const path = "/api/tasks/" + encodeURIComponent(taskID) + "/assignments";
        const failures = [];
        const warnings = [];
        const staged = new Set(draftState.items.map((item) => stagedAssignmentHolder(item.value).value));
        for (const item of [...draftState.items]) {
          try {
            const answer = await mutateTask("POST", path, { to: item.value });
            // Every warning, except a "shared" one naming nobody but the
            // assignees staged here: the reader staged them together, so one
            // landing beside another is what they asked for. Anybody else it
            // names — an agent that claimed the task between the create and
            // this write — is news the reader did not cause, and is said the
            // way the task page says it.
            (answer.warnings || [])
              .filter((warning) => warning.code !== "assignment-shared" || !sharedOnlyWithStaged(answer, item, staged))
              .forEach((warning) => warnings.push(warning.message));
          } catch (error) {
            failures.push({
              key: item.key,
              value: item.value,
              error: error instanceof Error ? error.message : "That assignment was not saved."
            });
          }
        }
        draftState.items = [];
        return { failures, warnings };
      }
      // The staged list, drawn in the New Task form's sidebar as the section a
      // task's own page draws there.
      function taskAssignmentDrafts(draftState) {
        // It owns itself: no poll writes to it, and it leaves with the route.
        const panel = taskPanel("", "assignments", "Assignments", () => true);
        panel.section.className += " task-panel--staged";
        panel.empty.hidden = true;
        const adder = assignmentAdder("Assign");
        panel.section.append(adder.form, panel.status);
        adder.form.addEventListener("submit", (event) => {
          event.preventDefault();
          if (panel.busy) return;
          panel.report("");
          // Trimmed as the task page trims it, and judged no further: whether a
          // value is an assignment is core's to say, and it says so after the
          // create, in the failure path below. A repeat is the one thing asked
          // here, because it is a question about this list rather than about
          // assignments.
          const value = adder.field.value.trim();
          if (draftState.items.some((item) => item.value === value)) {
            panel.report("That assignment is already staged.");
            return;
          }
          draftState.items.push({ key: "staged-assignment-" + (++draftState.sequence), value });
          adder.field.value = "";
          render();
        });
        function render() {
          panel.resetControls();
          panel.claim(adder.submit);
          panel.setCount(draftState.items.length ? draftState.items.length + " staged" : "");
          panel.list.replaceChildren(...draftState.items.map((item) => assignmentDraftRow(panel, item, {
            remove: () => {
              if (panel.busy) return;
              draftState.items = draftState.items.filter((held) => held !== item);
              panel.report("");
              render();
            }
          })));
        }
        render();
        return {
          section: panel.section,
          render,
          // The field goes with the button, as the dependency inputs go with
          // theirs: a field that takes typing beside an Assign that does nothing
          // reads as a control that is broken rather than one that is off.
          setBusy: (value) => {
            panel.setBusy(value);
            adder.field.disabled = value;
          }
        };
      }
      // Who holds this task, and — on a board that has an identity to write one
      // against — who is about to.
      //
      // The section shipped read-only, under a reason that was true of the
      // browser and not of the server: an assignment is recorded against an
      // asserted Git identity, and a page has none. What it has is a server
      // bound to one worktree, which asserts exactly the identity a commit made
      // in that worktree asserts. So the identity is the checkout's, the write
      // goes through core's own assign and unassign mutations, and what this
      // board records is indistinguishable from what `workbook update --assign`
      // records from the same directory.
      //
      // Whether any of that is drawn is one question, asked once: assignIdentity
      // is empty for a board built without both mutations or without an
      // identity, and an empty one leaves this section exactly what it was — no
      // form, no control, no route behind it, and nothing at all on a task
      // nobody holds.
      //
      // It is built out of taskPanel, like the two panels below it, because it
      // now has what a writing panel has: a busy state, a status line, and a
      // control register a request in flight disables. The one thing it does not
      // take from taskPanel is the decision to be in the document at all — a
      // section nobody can act on removes itself when there is nothing to read,
      // which no other panel on this page does.
      function taskAssignments(taskID, head, owner) {
        const assignable = Boolean(assignIdentity);
        const panel = taskPanel(taskID, "assignments", "Assignments", owner);
        panel.adoptHead = head.adopt;
        const path = "/api/tasks/" + encodeURIComponent(taskID) + "/assignments";
        let field = null;
        let submit = null;
        if (assignable) {
          const adder = assignmentAdder("Assign");
          field = adder.field;
          submit = adder.submit;
          panel.section.append(adder.form, panel.status);
          adder.form.addEventListener("submit", async (event) => {
            event.preventDefault();
            if (panel.busy) return;
            // A field trimmed to nothing is the empty value, which is this
            // board's own identity. Nothing else is judged here: every rule
            // about what an assignment may be is core's, and a client that
            // pre-judged one would be a second copy of a grammar that can only
            // drift from the one that decides.
            const answer = await writeThreadChange(
              panel, "POST", path, headed(head, { to: field.value.trim() }), "your assignment");
            if (answer) field.value = "";
          });
        }
        // An empty section is its heading and its form. It used to say so in a
        // sentence as well, and to count its nothing; the form is the whole of
        // what a task nobody holds needs, so neither is drawn.
        panel.empty.hidden = true;
        // The assignments a create staged for this task and could not save,
        // handed over by the create so they are retried here, against the task
        // that now exists — the way a failed dependency draft is.
        const drafts = pendingAssignmentDrafts.get(taskID) || [];
        // Each failed draft's Retry, in the order the rows are drawn, so a
        // create that lands here can put the caret on the first of them.
        let draftRetries = [];
        let lastHeld = [];
        function retireDraft(draft) {
          const at = drafts.indexOf(draft);
          if (at >= 0) drafts.splice(at, 1);
          if (!drafts.length) pendingAssignmentDrafts.delete(taskID);
          draw(lastHeld);
        }
        async function retryDraft(draft) {
          if (panel.busy || !panel.owns()) return;
          const answer = await writeThreadChange(
            panel, "POST", path, headed(head, { to: draft.value }), "that assignment");
          if (answer) {
            retireDraft(draft);
            return;
          }
          if (!panel.owns()) return;
          draft.error = panel.status.textContent || draft.error;
          draw(lastHeld);
        }
        // What the section was last drawn from. A poll that changed nothing
        // costs one string comparison and touches no node, which is what keeps
        // the rows from being rebuilt under a reader's caret once a second — the
        // same contract a card holds through cardSignature.
        //
        // Each row's "assigned 5 minutes ago" is not part of it. That phrase is
        // worded from this page's clock (assignedAgo) and moves about once a
        // minute for a recent assignment with nothing about the assignment
        // having changed; redrawing the rows for it would rebuild every row on
        // that tick. That was invisible while the rows held only text, but a row
        // now holds a focusable Unassign button, and destroying it takes a
        // keyboard reader's focus to <body> mid-tab. So the phrase is written
        // into the existing meta lines instead, by age() below.
        let signature = null;
        // The meta line of each row currently drawn and the time it was
        // recorded, in the order the rows are in, so the phrase can be
        // re-worded in place without walking the list back out of the document.
        let ages = [];
        function assignmentSignature(held) {
          return JSON.stringify([held.map((assignment) => [
            assignment.value, assignment.principal, assignment.label || "",
            assignment.createdAt, assignment.removable === true
          ]), drafts.map((draft) => [draft.value, draft.error])]);
        }
        function assignmentMetaText(createdAt, now) {
          const phrase = assignedAgo(createdAt, now);
          return phrase ? recordedAt(createdAt) + " · " + phrase : recordedAt(createdAt);
        }
        // Re-words every drawn row against one reading of the clock, so two
        // assignments recorded in the same second never read as two ages. It
        // writes only a line whose words changed: the tick that calls it comes
        // once a second, and nearly every one of them changes nothing.
        function age() {
          const now = Date.now();
          for (const row of ages) {
            const next = assignmentMetaText(row.createdAt, now);
            if (row.meta.textContent !== next) text(row.meta, next);
          }
        }
        // Where the section belongs when there is something to draw: this parent,
        // immediately before this sibling. Held rather than looked up so that a
        // task assigned, unassigned and assigned again from the CLI returns to
        // the same place on the page every time.
        let parent = null;
        let anchor = null;
        function draw(assignments) {
          const held = Array.isArray(assignments) ? assignments : [];
          lastHeld = held;
          const next = assignmentSignature(held);
          if (next === signature) {
            age();
            return;
          }
          signature = next;
          // A board that cannot assign draws nothing where there is nothing to
          // read — not a heading over an empty list, which is the convention the
          // comment thread's empty message deliberately does not extend to a
          // section a reader cannot act on. A board that can assign keeps the
          // section, because the form is the thing a task nobody holds needs.
          if (!held.length && !drafts.length && !assignable) {
            panel.section.remove();
            panel.list.replaceChildren();
            ages = [];
            return;
          }
          panel.resetControls();
          panel.setCount(held.length === 0 ? "" : held.length === 1 ? "1 assignment" : held.length + " assignments");
          ages = [];
          draftRetries = [];
          const now = Date.now();
          panel.list.replaceChildren(
            ...held.map((assignment) => assignmentRow(assignment, now)),
            ...drafts.map((draft) => assignmentDraftRow(panel, draft, {
              retry: () => retryDraft(draft),
              retryControl: (control) => draftRetries.push(control),
              remove: () => { if (!panel.busy) retireDraft(draft); }
            })));
          if (assignable) panel.claim(submit);
          if (parent && !parent.contains(panel.section)) parent.insertBefore(panel.section, anchor);
        }
        function assignmentRow(assignment, now) {
          const item = document.createElement("li");
          item.className = "assignment";
          // The principal and the label, drawn as the one value somebody types
          // into `--unassign` and still visibly two things: the slash is the
          // separator core composes the value with, and the label is styled
          // apart from the address so a reader can see which agent of that
          // person holds the task without having to parse the string.
          //
          // Both are somebody else's asserted identity, unverified by design,
          // and both reach the document through text(). A principal that is
          // markup is drawn as the characters it is, exactly as a title is.
          const who = document.createElement("p");
          who.className = "assignment__who";
          const principal = document.createElement("span");
          principal.className = "assignment__principal";
          text(principal, assignment.principal);
          who.append(principal);
          if (assignment.label) {
            const label = document.createElement("span");
            label.className = "assignment__label";
            text(label, "/" + assignment.label);
            who.append(label);
          }
          // The exact time it was recorded and how long ago that was. The
          // phrase is presentation.AssignedAgo's — the same words `workbook
          // show` prints, by the rule the server rendered into this page — and
          // it is re-worded on every tick, so an open page's hint ages without
          // a reload and without a poll having to bring anything. Nothing
          // expires and this is not a warning: an assignment three weeks old is
          // a fact for people to settle between themselves.
          const meta = document.createElement("p");
          meta.className = "assignment__meta";
          text(meta, assignmentMetaText(assignment.createdAt, now));
          ages.push({ meta, createdAt: assignment.createdAt });
          item.append(who, meta);
          // Withdrawal is offered only where the server said this board may make
          // one. The rule is core's — the assignee may release what they hold,
          // and so may whoever recorded it — and it is decided there rather than
          // by comparing addresses here, so every control this page draws is one
          // the service would accept. A row without it is not a row anybody is
          // stuck with: the identity it names withdraws it from their own clone,
          // which is what makes the rule a rule rather than a lock.
          if (assignable && assignment.removable) {
            const actions = document.createElement("div");
            actions.className = "assignment__actions";
            // The value the route takes is the server's own composition, sent
            // down beside the two parts it is made of. Re-assembling it here
            // would put a copy of core's separator on the client, and a copy
            // that disagreed about where the first slash falls would withdraw a
            // different assignment than the row this button sits in.
            const withdraw = panel.claim(threadButton("Unassign"));
            withdraw.addEventListener("click", () => writeThreadChange(
              panel, "DELETE", path, headed(head, { from: assignment.value }), "that withdrawal"));
            actions.append(withdraw);
            item.append(actions);
          }
          return item;
        }
        // How a write redraws this section. It takes the answer rather than the
        // task because the task does not carry what a row draws: the value and
        // the withdrawal are the server's composition and the server's rule,
        // and the assignment routes send both back beside the task they changed.
        // A refusal has no answer to draw from and falls back to the last poll's
        // view, which is the truth about the task the write did not change.
        panel.follow = (held, answer) => {
          if (answer && Array.isArray(answer.assignments)) {
            draw(answer.assignments);
            return;
          }
          const view = latestPresentation.get(taskID);
          if (view) draw(view.assignments);
        };
        return {
          section: panel.section,
          // Mounted by its owner rather than appended by it, because whether the
          // section is in the document at all is this controller's answer and it
          // changes with every poll.
          mountBefore(into, before) { parent = into; anchor = before; },
          follow(view) { draw(view && view.assignments); },
          // The first failed draft's Retry while it is in the document, for
          // the create that handed the drafts over to focus.
          firstDraftRetry() {
            const retry = draftRetries[0];
            return retry && panel.section.contains(retry) ? retry : null;
          },
          age
        };
      }
      function commentAttribution(comment) {
        const written = attribution(comment.author, comment.createdAt);
        return comment.editedAt ? written + " · edited " + recordedAt(comment.editedAt) : written;
      }
      // The comment thread: oldest first, which is the order the server sends —
      // a comment's identifier is the ULID of the operation that recorded it, so
      // identifier order is creation order on every clone.
      function taskComments(taskID, task, head, owner) {
        const panel = taskPanel(taskID, "comments", "Comments", owner);
        panel.adoptHead = head.adopt;
        const form = document.createElement("form");
        form.className = "comment-add";
        const label = document.createElement("label");
        label.setAttribute("for", "comment-body");
        text(label, "Add a comment");
        const body = document.createElement("textarea");
        body.id = "comment-body";
        body.name = "comment";
        const actions = document.createElement("div");
        actions.className = "comment-add__actions";
        const submit = threadButton("Comment", true);
        submit.type = "submit";
        actions.append(submit);
        form.append(label, body, actions);
        panel.section.append(form, panel.status);

        let comments = [];
        // What this task has attached, as of the last poll. The thread holds it
        // because an image in a comment is an attachment reference and resolves
        // against this list: a comment written before the screenshot was
        // uploaded draws the reference as text and draws the image the moment
        // the upload lands, with no edit to the comment.
        let attachments = (task && task.attachments) || [];
        // A comment the reader is editing keeps its row across every render.
        // Rebuilding it would take the sentence they are in the middle of with
        // it, and a poll lands once a second.
        const editing = new Map();
        function commentPath(commentID) {
          return "/api/tasks/" + encodeURIComponent(taskID) + "/comments/" + encodeURIComponent(commentID);
        }
        function commentRow(comment) {
          const row = document.createElement("li");
          row.className = "comment";
          row.dataset.commentId = comment.id;
          const written = document.createElement("p");
          written.className = "comment__attribution";
          text(written, commentAttribution(comment));
          // A div rather than a paragraph, because what goes in it may be
          // several paragraphs, a list or a fenced block, and none of those may
          // nest inside a <p>. The class and the marker are unchanged.
          const bodyText = document.createElement("div");
          bodyText.className = "comment__body markdown";
          bodyText.dataset.commentBody = "";
          // Nodes, never innerHTML. A comment body is text somebody typed; the
          // renderer above builds elements out of it and draws everything it
          // does not recognize as the characters it is. Images in a comment
          // resolve against this task's own attachments, which is the only
          // place an image on this page can come from.
          renderMarkdown(bodyText, comment.body || "", { taskID, attachments });
          const rowActions = document.createElement("div");
          rowActions.className = "comment__actions";
          const edit = panel.claim(threadButton("Edit"));
          edit.addEventListener("click", () => beginEdit(comment));
          // Removal is immediate, like every other removal on this page: the
          // Delete beside it, a label chiclet, a relationship row. What makes
          // that safe here is that a comment is history — removing one hides it
          // from the thread and the change log still holds what it said.
          const remove = panel.claim(threadButton("Remove"));
          remove.addEventListener("click", () => writeThreadChange(
            panel, "DELETE", commentPath(comment.id), headed(head, {}), "that removal"));
          rowActions.append(edit, remove);
          row.append(written, bodyText, rowActions);
          return row;
        }
        function beginEdit(comment) {
          if (panel.busy || editing.has(comment.id)) return;
          const row = document.createElement("li");
          row.className = "comment";
          row.dataset.commentId = comment.id;
          row.dataset.commentEditing = "";
          const written = document.createElement("p");
          written.className = "comment__attribution";
          text(written, commentAttribution(comment));
          const editor = document.createElement("div");
          editor.className = "comment__editor";
          const area = document.createElement("textarea");
          area.setAttribute("aria-label", "Edit comment");
          area.dataset.commentEditor = "";
          area.value = comment.body || "";
          const note = document.createElement("p");
          note.className = "comment__note";
          const editActions = document.createElement("div");
          editActions.className = "comment__actions";
          const save = threadButton("Save", true);
          const cancel = threadButton("Cancel");
          save.addEventListener("click", async () => {
            if (panel.busy) return;
            if (!area.value.trim()) {
              panel.report("A comment cannot be blank.");
              return;
            }
            const answer = await writeThreadChange(
              panel, "PATCH", commentPath(comment.id), headed(head, { body: area.value }), "that edit");
            if (!answer) return;
            editing.delete(comment.id);
            render();
          });
          cancel.addEventListener("click", () => {
            editing.delete(comment.id);
            render();
          });
          editActions.append(save, cancel);
          editor.append(area, editActions);
          row.append(written, editor, note);
          editing.set(comment.id, { row, area, note, buttons: [save, cancel] });
          render();
          area.focus();
        }
        function render() {
          panel.resetControls();
          const known = new Set(comments.map((comment) => comment.id));
          const rows = comments.map((comment, at) => {
            const entry = editing.get(comment.id);
            if (!entry) return commentRow(comment);
            // Where this comment stands in the thread, remembered while it is
            // still in it. It is what puts the row back in the same place if it
            // stops being in it, below.
            entry.at = at;
            text(entry.note, "");
            entry.buttons.forEach((button) => panel.claim(button));
            return entry.row;
          });
          // A comment being edited that has left the thread — removed by another
          // clone, or by this reader somewhere else — keeps its row rather than
          // taking the words in it away mid-sentence. It says what happened, and
          // a save from it is refused by the server, which is the truth about it.
          //
          // It keeps its place, too. Appended to the end, the row jumped down the
          // page under the caret of somebody mid-sentence, which is the one thing
          // keeping the row was for.
          editing.forEach((entry, id) => {
            if (known.has(id)) return;
            text(entry.note, "This comment was removed elsewhere. Your edit is kept here, but saving it would be refused.");
            entry.buttons.forEach((button) => panel.claim(button));
            const at = typeof entry.at === "number" ? Math.min(entry.at, rows.length) : rows.length;
            rows.splice(at, 0, entry.row);
          });
          panel.claim(submit);
          panel.setCount(comments.length === 1 ? "1 comment" : comments.length + " comments");
          text(panel.empty, "Nothing has been said about this task yet.");
          panel.empty.hidden = rows.length > 0;
          panel.list.replaceChildren(...rows);
        }
        form.addEventListener("submit", async (event) => {
          event.preventDefault();
          if (panel.busy) return;
          if (!body.value.trim()) {
            panel.report("A comment cannot be blank.");
            return;
          }
          const answer = await writeThreadChange(
            panel, "POST", "/api/tasks/" + encodeURIComponent(taskID) + "/comments",
            headed(head, { body: body.value }), "your comment");
          if (answer) body.value = "";
        });
        panel.follow = (held) => {
          comments = (held && held.comments) || [];
          attachments = (held && held.attachments) || [];
          render();
        };
        panel.follow(task);
        return panel;
      }
