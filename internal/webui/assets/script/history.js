      const fieldLabels = new Map([
        ["task", "Task"], ["title", "Title"], ["status", "Status"], ["priority", "Priority"],
        ["description", "Description"], ["rank", "Rank"], ["labels", "Labels"], ["dependencies", "Dependencies"]
      ]);
      function fieldLabel(field) { return fieldLabels.get(field) || field; }
      // Wall times are attribution only, exactly as the CLI treats them. The
      // chain, not the clock, orders these rows, so a timestamp that reads out
      // of order after a reconciliation is shown as it is.
      function recordedAt(wallTime) {
        const when = new Date(wallTime);
        return Number.isNaN(when.getTime()) ? String(wallTime) : when.toLocaleString();
      }
      function attribution(actor, wallTime) {
        return actor ? recordedAt(wallTime) + " · " + actor : recordedAt(wallTime);
      }
      async function readTaskHistory(taskID) {
        const response = await fetch("/api/tasks/" + encodeURIComponent(taskID) + "/history", { cache: "no-store" });
        const payload = await response.json();
        if (!response.ok || payload.format !== "workbook.task-history" || payload.version !== 1 ||
            !Array.isArray(payload.lifecycle) || !payload.history || !Array.isArray(payload.history.changes)) {
          throw new Error(payload.error && payload.error.message || "Task history could not be loaded.");
        }
        return payload;
      }
      // lifecycleLane renders status as a lane rather than as another flat row
      // type. Status is by far the most common change, and ready to in-progress
      // to in-review to done tells a task's life in a way a chronological list
      // cannot. A stop no recorded change entered carries no attribution.
      function lifecycleLane(stages) {
        const lane = document.createElement("ol");
        lane.className = "lifecycle";
        lane.dataset.lifecycle = "";
        lane.setAttribute("aria-label", "Status lifecycle");
        stages.forEach((stage) => {
          const stop = document.createElement("li");
          stop.className = "lifecycle__stop";
          stop.dataset.status = stage.status;
          if (stage.current) {
            stop.dataset.current = "true";
            stop.setAttribute("aria-current", "step");
          }
          const label = document.createElement("span");
          label.className = "lifecycle__label";
          text(label, stage.label || stage.status);
          stop.append(label);
          if (stage.wallTime) {
            const meta = document.createElement("span");
            meta.className = "lifecycle__meta";
            text(meta, attribution(stage.actor, stage.wallTime));
            stop.append(meta);
          }
          lane.append(stop);
        });
        return lane;
      }
      // diffValue marks a scalar change the way `git diff --word-diff` does:
      // the description arrives as server-computed spans, and every other
      // scalar is short enough to read as old beside new.
      const diffElements = { delete: "del", insert: "ins" };
      function diffValue(field) {
        const value = document.createElement("span");
        value.className = "field-change__value";
        if (Array.isArray(field.diff) && field.diff.length) {
          field.diff.forEach((span) => {
            const element = document.createElement(diffElements[span.kind] || "span");
            text(element, span.text);
            value.append(element);
          });
          return value;
        }
        const before = document.createElement("del"); text(before, field.from || "(empty)");
        const arrow = document.createElement("span"); text(arrow, " → ");
        const after = document.createElement("ins"); text(after, field.to || "(empty)");
        value.append(before, arrow, after);
        return value;
      }
      function fieldChange(field) {
        const row = document.createElement("div");
        row.className = "field-change";
        row.dataset.field = field.field;
        const label = document.createElement("span");
        label.className = "field-change__label";
        text(label, fieldLabel(field.field));
        let value = null;
        if (field.kind === "set") {
          value = diffValue(field);
        } else {
          value = document.createElement("span");
          value.className = "field-change__value";
          if (field.kind === "reordered") text(value, "Reordered");
          else if (field.kind === "added") text(value, "+ " + field.to);
          else if (field.kind === "removed") text(value, "− " + field.from);
          else if (field.kind === "created") text(value, "Created " + (field.to || ""));
          else if (field.kind === "deleted") text(value, "Deleted");
          else if (field.kind === "restored") text(value, "Restored");
          else text(value, field.to || field.from || "");
        }
        row.append(label, value);
        return row;
      }
      // changeRow renders one operation pack as one row and expands in place
      // into the field-level comparison the server already computed for it,
      // description word diff included. Expanding needs no second request and
      // no route of its own, so it keeps working with the board offline.
      function changeRow(change) {
        const item = document.createElement("li");
        item.className = "change-row";
        item.dataset.changeRow = change.commit;
        const toggle = document.createElement("button");
        toggle.type = "button";
        toggle.className = "change-row__summary";
        toggle.setAttribute("aria-expanded", "false");
        const when = document.createElement("span");
        when.className = "change-row__when";
        text(when, recordedAt(change.wallTime));
        const who = document.createElement("span");
        who.className = "change-row__who";
        text(who, change.actor || "unattributed");
        const what = document.createElement("span");
        what.className = "change-row__what";
        text(what, change.summary || "recorded no visible change");
        toggle.append(when, who, what);
        const comparison = document.createElement("div");
        comparison.className = "change-comparison";
        comparison.hidden = true;
        comparison.setAttribute("aria-label", "Fields this change touched");
        const commit = document.createElement("code");
        commit.className = "change-comparison__commit";
        text(commit, change.commit);
        comparison.append(commit);
        (change.fields || []).forEach((field) => comparison.append(fieldChange(field)));
        if (!change.fields || change.fields.length === 0) {
          const empty = document.createElement("p");
          empty.className = "history-state";
          text(empty, "This change recorded no operator-visible field change.");
          comparison.append(empty);
        }
        toggle.addEventListener("click", () => {
          const expanded = toggle.getAttribute("aria-expanded") === "true";
          toggle.setAttribute("aria-expanded", expanded ? "false" : "true");
          comparison.hidden = expanded;
        });
        item.append(toggle, comparison);
        return item;
      }
      function changedOnlyStatus(change) {
        return Array.isArray(change.fields) && change.fields.length > 0 &&
          change.fields.every((field) => field.field === "status");
      }
      function historyContent(payload) {
        const log = payload.history;
        // The rows read newest first. That is still the parent chain's order,
        // read from the current tip backwards, rather than wall time's: after a
        // reconciliation the two legitimately disagree, and the chain is what
        // this view exists to carry. The lane keeps reading forwards, because a
        // lifecycle is a progression.
        const rows = log.changes.filter((change) => !changedOnlyStatus(change)).reverse();
        const parts = [lifecycleLane(payload.lifecycle)];
        const summary = document.createElement("p");
        summary.className = "change-summary";
        summary.dataset.changeSummary = "";
        const counted = log.total === 1 ? "1 change recorded." : log.total + " changes recorded.";
        const windowed = log.showing < log.total
          ? " Showing the " + log.showing + " most recent."
          : "";
        const laneOnly = log.changes.length - rows.length;
        const inLane = laneOnly === 0
          ? ""
          : laneOnly === 1
            ? " One of them changed only status and reads as the lifecycle above."
            : " " + laneOnly + " of them changed only status and read as the lifecycle above.";
        text(summary, counted + windowed + inLane);
        parts.push(summary);
        if (log.truncated) {
          const truncation = document.createElement("p");
          truncation.className = "history-truncation";
          text(truncation, "History stops at commit " + log.truncated.commit + ": " + log.truncated.message);
          parts.push(truncation);
        }
        if (rows.length === 0) {
          const empty = document.createElement("p");
          empty.className = "history-state";
          text(empty, log.total === 0
            ? "No changes are recorded for this task."
            : "Every recorded change is a status change.");
          parts.push(empty);
          return parts;
        }
        const list = document.createElement("ol");
        list.className = "change-list";
        list.dataset.changeList = "";
        list.setAttribute("aria-label", "Recorded changes, newest first");
        rows.forEach((change) => list.append(changeRow(change)));
        parts.push(list);
        return parts;
      }
      // taskHistory shows a task's change history by default, unlike the CLI
      // where it is opt in behind --history.
      function taskHistory(taskID) {
        const section = document.createElement("section");
        section.className = "task-history";
        section.dataset.taskHistory = taskID;
        section.setAttribute("aria-labelledby", "task-history-title");
        const heading = document.createElement("h3");
        heading.id = "task-history-title";
        text(heading, "History");
        const body = document.createElement("div");
        body.dataset.historyBody = "";
        const state = document.createElement("p");
        state.className = "history-state";
        state.setAttribute("role", "status");
        state.setAttribute("aria-live", "polite");
        section.append(heading, body);
        const load = async () => {
          delete state.dataset.kind;
          text(state, "Reading this task's history from this checkout.");
          body.replaceChildren(state);
          try {
            const payload = await readTaskHistory(taskID);
            if (!main.contains(section)) return;
            body.replaceChildren(...historyContent(payload));
          } catch (error) {
            if (!main.contains(section)) return;
            state.dataset.kind = "error";
            text(state, error instanceof Error ? error.message : "Task history could not be loaded.");
            const retry = document.createElement("button");
            retry.className = "save-button";
            retry.type = "button";
            text(retry, "Retry history");
            retry.addEventListener("click", load);
            body.replaceChildren(state, retry);
          }
        };
        load();
        return section;
      }
