      // ─── A relic's history and its diffs ────────────────────────────────────
      //
      // The changes of a relic or of one part, and the comparison of two points
      // of that history. Both are drawn from the /api/relics documents alone:
      // the page computes no diff of its own. Everything a relic carries is
      // drawn as text, as on the relic page.
      //
      // The diff's root reports what it holds in data-diff-state, beside the
      // view's data-relic-state, and each is set only once the DOM for that
      // state is complete.

      // Which hunk layout the reader chose: kept per viewer, and held here as
      // well for a browser that will not store it.
      const relicDiffModeKey = "workbook.relic-diff-mode";
      const relicDiffWide = window.matchMedia ? window.matchMedia("(min-width: 900px)") : null;
      let relicDiffChoice = null;
      // Redraws the hunks of the diff on screen, for a window that crosses the
      // width the layout turns on.
      let relicDiffRedraw = null;
      // What the history or diff view is showing, for the read a returning tab
      // makes.
      let relicVersionsShown = null;

      function relicStoredDiffMode() {
        try {
          const stored = window.localStorage ? window.localStorage.getItem(relicDiffModeKey) : null;
          return stored === "side" || stored === "inline" ? stored : null;
        } catch (_) { return null; }
      }
      // Side by side needs the width for two columns of text, so a narrow
      // window is drawn inline whatever the reader chose for a wide one.
      function relicDiffMode() {
        if (relicDiffWide && !relicDiffWide.matches) return "inline";
        return relicDiffChoice || relicStoredDiffMode() || "side";
      }
      function chooseRelicDiffMode(mode) {
        relicDiffChoice = mode;
        try { if (window.localStorage) window.localStorage.setItem(relicDiffModeKey, mode); }
        catch (_) { /* The choice lasts as long as the page does. */ }
      }

      // What a history or diff address asks for beside the relic it names.
      function relicVersionsRoute(current, url) {
        const params = url.searchParams;
        if (current.view === "relic-history") {
          return { ...current, part: params.get("part") || "", all: params.get("all") === "1" };
        }
        if (current.view === "relic-diff") {
          return { ...current, part: params.get("part") || "", from: params.get("from") || "", to: params.get("to") || "" };
        }
        return current;
      }
      function relicQuery(pairs) {
        const params = new URLSearchParams();
        pairs.forEach(([name, value]) => { if (value) params.set(name, value); });
        const query = params.toString();
        return query ? "?" + query : "";
      }
      function relicHistoryAddress(relicID, part, all) {
        return relicPath(relicID) + "/history" + relicQuery([["part", part], ["all", all ? "1" : ""]]);
      }
      function relicDiffAddress(relicID, part, from, to) {
        return relicPath(relicID) + "/diff" + relicQuery([["from", from], ["to", to], ["part", part]]);
      }
      function relicAnchor(href, label, marker) {
        const anchor = relicElement("a", "", label);
        anchor.href = href;
        if (marker) anchor.dataset[marker] = "";
        return anchor;
      }
      // The way back to the relic, or to the part being looked at.
      function relicBackLine(relicID, part) {
        const line = relicElement("p", "relic-view__note");
        if (!relicAddressableID(relicID)) return line;
        const href = relicAddressableID(part) ? relicPartPath(relicID, part) : relicPath(relicID);
        line.append(relicAnchor(href, part ? "Back to the part" : "Back to the relic", "relicBack"));
        return line;
      }

      // ─── The history ───
      // The ID that names a change as a version: its last operation's.
      function relicChangeVersion(change) {
        const operations = Array.isArray(change.operations) ? change.operations : [];
        const last = operations[operations.length - 1];
        return last ? last.id || "" : "";
      }
      // One operation of a change: its type, and what it named.
      function relicOperationItem(operation) {
        const item = relicElement("li", "");
        item.append(relicElement("code", "", operation.type || ""));
        const details = [];
        if (operation.relic && operation.relic.title) details.push(operation.relic.title);
        if (operation.title) details.push(operation.title);
        if (operation.name) details.push(operation.name);
        if (operation.partType) details.push(operation.partType);
        if (operation.part) details.push("part " + operation.part);
        if (operation.body) {
          details.push("body " + (operation.body.format ? operation.body.format + ", " : "") + attachmentSize(operation.body.bytes));
        }
        if (operation.link) {
          const to = operation.link.to || {};
          const target = to.task || (to.part ? (to.relic || "") + "#" + to.part : to.relic || "");
          details.push((operation.link.rel || "") + " → " + target);
        }
        if (operation.linkId) details.push("link " + operation.linkId);
        if (operation.relicType) details.push(operation.relicType);
        if (operation.applied === false) details.push("not applied");
        if (details.length) item.append(relicElement("span", "", " " + details.join(" · ")));
        return item;
      }
      // The From and To radios of one row.
      function relicPickControls(version, what, choose) {
        return [["from", "From"], ["to", "To"]].map(([side, label]) => {
          const wrapper = relicElement("label", "relic-change__pick");
          const radio = document.createElement("input");
          radio.type = "radio";
          radio.name = "relic-pick-" + side;
          radio.value = version;
          radio.dataset.relicPick = side;
          radio.setAttribute("aria-label", withoutDirectionControls(label + ": " + what));
          radio.addEventListener("change", choose);
          wrapper.append(radio, relicElement("span", "", label));
          return wrapper;
        });
      }
      function relicCheckpointNotes(compacted) {
        const note = relicElement("p", "relic-change__summary", "Earlier changes were dropped when this relic was compacted on ");
        note.append(relicTime(compacted.at), document.createTextNode("."));
        const replaced = relicElement("p", "relic-change__meta",
          "History starts here. Changes dropped: " + (Number(compacted.changes) || 0) + ", recorded from ");
        replaced.append(relicTime(compacted.from), document.createTextNode(" to "), relicTime(compacted.to),
          document.createTextNode("."));
        return [note, replaced];
      }
      function drawRelicHistory(current, history_) {
        const relic = history_.relic || {};
        const relicID = relic.id || current.id;
        const part = history_.part || "";
        const title = relic.title || relicID;
        document.title = withoutDirectionControls("History of " + title + boardTitleSuffix);
        const lines = [relicBackLine(relicID, part)];
        if (part) {
          const line = relicElement("p", "relic-view__note", "Changes to part ");
          line.append(relicElement("code", "", part), document.createTextNode("."));
          lines.unshift(line);
        }
        const nodes = [relicViewHeader("History of " + title, ...lines)];
        if (history_.truncated) {
          nodes.push(relicElement("p", "relic-warning",
            "History stops at commit " + history_.truncated.commit + ": " + history_.truncated.message));
        }
        const changes = Array.isArray(history_.changes) ? history_.changes : [];
        const total = Number(history_.total) || 0;
        nodes.push(relicElement("p", "relic-view__note",
          "Showing " + changes.length + " of " + total + (total === 1 ? " change." : " changes.")));
        if (changes.length < total && relicAddressableID(relicID)) {
          const more = relicElement("p", "relic-view__note");
          more.append(relicAnchor(relicHistoryAddress(relicID, part, true), "Show all " + total + " changes", "relicShowAll"));
          nodes.push(more);
        }
        // Newest first. A history that starts at a checkpoint ends with it,
        // once every change above it is listed.
        const rows = changes.slice().reverse().map((change) => ({ version: relicChangeVersion(change), change }));
        const compacted = history_.compacted && changes.length >= total ? history_.compacted : null;
        if (compacted) rows.push({ version: compacted.version || "", change: null });
        if (!rows.length) {
          nodes.push(relicElement("p", "relic-view__empty", "No changes are recorded."));
          relicSettle(relicReady, ...nodes);
          return;
        }
        const list = relicElement("ol", "relic-history");
        list.setAttribute("aria-label", "Changes, newest first");
        const choose = () => {
          const picked = (side) => list.querySelector("input[data-relic-pick=\"" + side + "\"]:checked");
          const from = picked("from");
          const to = picked("to");
          if (from && to) navigate(relicDiffAddress(relicID, part, from.value, to.value));
        };
        rows.forEach((row, index) => {
          const item = relicElement("li", "relic-change");
          item.dataset.relicChange = row.version;
          let what = "the checkpoint";
          if (row.change) {
            const meta = relicElement("p", "relic-change__meta");
            meta.append(relicTime(row.change.wallTime), relicElement("span", "", row.change.actor || "unattributed"));
            what = row.change.summary || "change";
            const operations = relicElement("ul", "relic-change__operations");
            operations.dataset.relicOperations = "";
            (row.change.operations || []).forEach((operation) => operations.append(relicOperationItem(operation)));
            item.append(meta, relicElement("p", "relic-change__summary", what), operations);
            what += ", " + recordedAt(row.change.wallTime);
          } else {
            item.dataset.relicCheckpoint = "";
            item.append(...relicCheckpointNotes(compacted));
          }
          if (relicAddressableID(relicID) && relicAddressableID(row.version)) {
            const actions = relicElement("div", "relic-change__actions");
            if (rows.length > 1) actions.append(...relicPickControls(row.version, what, choose));
            const older = rows[index + 1];
            let href;
            let label;
            if (older) {
              href = relicDiffAddress(relicID, part, older.version, row.version);
              label = "Compare with the change before";
            } else if (rows.length === 1 && row.change && total === 1 && !history_.compacted) {
              // The only change has nothing to be compared with: the diff that
              // names no version shows what it did.
              href = relicDiffAddress(relicID, part, "", "");
              label = "Show this change";
            } else {
              href = relicDiffAddress(relicID, part, row.version, "");
              label = "Compare with the newest";
            }
            actions.append(relicAnchor(href, label, "relicCompare"));
            item.append(actions);
          }
          list.append(item);
        });
        nodes.push(list);
        relicSettle(relicReady, ...nodes);
      }

      // ─── The diff ───
      function relicDiffRoot(state) {
        const root = relicElement("div", "relic-diff");
        root.dataset.diffState = state;
        return root;
      }
      // One side of a value change, as the relic page draws a value.
      function relicDiffSide(change, side, label) {
        const block = relicElement("div", "relic-diff__side");
        block.append(relicElement("span", "relic-diff__side-label", label),
          Object.prototype.hasOwnProperty.call(change, side)
            ? relicValue(change[side])
            : relicElement("span", "relic-view__empty", "(none)"));
        return block;
      }
      function relicValueChanges(changes) {
        const list = relicElement("dl", "relic-values relic-diff__values");
        changes.forEach((change) => {
          const description = document.createElement("dd");
          description.dataset.relicValueChange = change.name || "";
          description.append(relicDiffSide(change, "from", "Before"), relicDiffSide(change, "to", "After"));
          list.append(relicElement("dt", "", change.name || ""), description);
        });
        return list;
      }
      // A line's cells: its number or numbers, its marker and its text. The
      // marker says what the color says.
      const relicDiffMarkers = { equal: " ", delete: "-", insert: "+" };
      function relicLineNumber(side, value) {
        const cell = relicElement("td", "relic-line__number", value || "");
        cell.dataset.lineNumber = side;
        return cell;
      }
      function relicLineCells(line) {
        const kind = line ? line.kind : "empty";
        const marker = relicElement("td", "relic-line__marker");
        marker.textContent = relicDiffMarkers[kind] || " ";
        const cell = relicElement("td", "relic-line__text");
        cell.dataset.lineText = kind;
        if (line) {
          cell.append(relicElement("span", "", line.text));
          if (line.noNewline) cell.append(relicElement("span", "relic-line__note", "No newline at end"));
        }
        return [marker, cell];
      }
      // The lines as side by side rows: an equal line on both sides, and each
      // run of deletes beside the run of inserts that follows it.
      function relicPairedLines(lines) {
        const rows = [];
        let deleted = [];
        let inserted = [];
        const flush = () => {
          for (let index = 0; index < Math.max(deleted.length, inserted.length); index += 1) {
            rows.push([deleted[index] || null, inserted[index] || null]);
          }
          deleted = [];
          inserted = [];
        };
        lines.forEach((line) => {
          if (line.kind === "delete") {
            if (inserted.length) flush();
            deleted.push(line);
          } else if (line.kind === "insert") {
            inserted.push(line);
          } else {
            flush();
            rows.push([line, line]);
          }
        });
        flush();
        return rows;
      }
      function relicHunkTable(hunk, mode) {
        const table = relicElement("table", "relic-hunk__table relic-hunk__table--" + mode);
        const columns = document.createElement("colgroup");
        const names = mode === "side" ? ["number", "marker", "text", "number", "marker", "text"] : ["number", "number", "marker", "text"];
        names.forEach((name) => columns.append(relicElement("col", "relic-hunk__column--" + name)));
        const body = document.createElement("tbody");
        const lines = Array.isArray(hunk.lines) ? hunk.lines : [];
        if (mode === "side") {
          relicPairedLines(lines).forEach(([from, to]) => {
            const row = document.createElement("tr");
            row.append(relicLineNumber("from", from && from.from), ...relicLineCells(from),
              relicLineNumber("to", to && to.to), ...relicLineCells(to));
            body.append(row);
          });
        } else {
          lines.forEach((line) => {
            const row = document.createElement("tr");
            row.dataset.lineKind = line.kind || "";
            row.append(relicLineNumber("from", line.from), relicLineNumber("to", line.to), ...relicLineCells(line));
            body.append(row);
          });
        }
        table.append(columns, body);
        return table;
      }
      function relicBodySide(body) {
        if (!body) return "none";
        return (body.format ? body.format + " " : "") + attachmentSize(body.bytes);
      }
      // A part's body on the two sides. Each hunk's place is filled by hunks,
      // which is called again when the layout changes.
      function relicBodyDiff(body, hunks) {
        const block = relicElement("div", "relic-diff__body");
        if (!body.text) {
          const size = (side) => (side ? attachmentSize(side.bytes) : "none");
          block.append(relicElement("p", "relic-view__note", "Body, not text: " + size(body.from) + " → " + size(body.to)));
          return block;
        }
        block.append(relicElement("p", "relic-view__note", "Body: " + relicBodySide(body.from) + " → " + relicBodySide(body.to)));
        if (body.approximate) {
          block.append(relicElement("p", "relic-warning",
            "This diff is approximate: too much changed to match line by line."));
        }
        (Array.isArray(body.hunks) ? body.hunks : []).forEach((hunk) => {
          const heading = relicElement("p", "relic-hunk__range",
            "@@ -" + hunk.fromStart + "," + hunk.fromLines + " +" + hunk.toStart + "," + hunk.toLines + " @@");
          const place = relicElement("div", "relic-hunk");
          place.dataset.hunk = "";
          // A box that scrolls sideways is reached by keyboard to be scrolled.
          place.tabIndex = 0;
          place.setAttribute("role", "group");
          place.setAttribute("aria-label", "Changed lines from line " + hunk.fromStart);
          hunks.push({ place, hunk });
          block.append(heading, place);
        });
        return block;
      }
      function relicPartDiff(relicID, part, hunks) {
        const section = relicElement("section", "relic-diff__part");
        section.dataset.relicDiffPart = part.id || "";
        const heading = relicElement("h4", "", (part.type || "Part") + " " + (part.kind || ""));
        const meta = relicElement("p", "relic-part__meta");
        if (relicAddressableID(relicID) && relicAddressableID(part.id)) {
          meta.append(relicAnchor(relicPartPath(relicID, part.id), part.id));
        } else {
          meta.append(relicElement("code", "", part.id || ""));
        }
        section.append(heading, meta);
        const moved = part.moved;
        if (moved && moved.fromIndex !== moved.toIndex) {
          section.append(relicElement("p", "relic-view__note", "moved from " + moved.fromIndex + " to " + moved.toIndex));
        }
        if (Array.isArray(part.fields) && part.fields.length) section.append(relicValueChanges(part.fields));
        if (part.body) section.append(relicBodyDiff(part.body, hunks));
        return section;
      }
      function relicDiffLinks(title, links) {
        const list = relicElement("ul", "relic-links");
        links.forEach((link) => {
          const item = relicLinkItem(link);
          if (link.from) item.append(relicElement("span", "", " (from part " + link.from + ")"));
          if (link.pointer !== undefined) item.append(relicValue(link.pointer));
          list.append(item);
        });
        return relicSection("relic-diff__links", title, list);
      }
      function relicDiffPointText(point) {
        const description = relicElement("span", "");
        if (!point) {
          text(description, "before the relic existed");
          return description;
        }
        description.append(relicElement("code", "", point.version || ""),
          relicElement("span", "", " · " + (point.actor || "unattributed") + " · "), relicTime(point.wallTime));
        return description;
      }
      function drawRelicDiff(current, diff) {
        const relicID = diff.relic || current.id;
        const part = diff.part || "";
        document.title = "Relic changes" + boardTitleSuffix;
        const subject = relicElement("p", "relic-view__note", part ? "Part " + part + " of relic " : "Relic ");
        subject.append(relicElement("code", "", relicID));
        const ways = relicBackLine(relicID, part);
        if (relicAddressableID(relicID)) {
          ways.append(document.createTextNode(" · "), relicAnchor(relicHistoryAddress(relicID, part, false), "History"));
        }
        const header = relicViewHeader(part ? "Changes to a part" : "Changes to a relic", subject, ways);

        const root = relicElement("div", "relic-diff");
        const points = relicElement("dl", "relic-facts");
        relicFact(points, "from", "From", relicDiffPointText(diff.from));
        relicFact(points, "to", "To", relicDiffPointText(diff.to));
        root.append(points);

        const hunks = [];
        const sections = [];
        if (Array.isArray(diff.changes) && diff.changes.length) {
          sections.push(relicSection("relic-diff__members", "Relic", relicValueChanges(diff.changes)));
        }
        if (Array.isArray(diff.properties) && diff.properties.length) {
          sections.push(relicSection("relic-diff__properties", "Properties", relicValueChanges(diff.properties)));
        }
        if (Array.isArray(diff.parts) && diff.parts.length) {
          sections.push(relicSection("relic-diff__parts", "Parts",
            ...diff.parts.map((changed) => relicPartDiff(relicID, changed, hunks))));
        }
        const links = diff.links || {};
        if (Array.isArray(links.added) && links.added.length) sections.push(relicDiffLinks("Links added", links.added));
        if (Array.isArray(links.removed) && links.removed.length) sections.push(relicDiffLinks("Links removed", links.removed));
        if (!sections.length) {
          sections.push(relicElement("p", "relic-view__empty",
            "These two versions hold the same " + (part ? "part." : "relic.")));
        }

        let toggle = null;
        if (hunks.length) {
          toggle = relicElement("div", "relic-diff__modes");
          toggle.setAttribute("role", "group");
          toggle.setAttribute("aria-label", "Line layout");
          [["side", "Side by side"], ["inline", "Inline"]].forEach(([mode, label]) => {
            const button = relicElement("button", "relic-diff__mode", label);
            button.type = "button";
            button.dataset.diffToggle = mode;
            button.addEventListener("click", () => { chooseRelicDiffMode(mode); redraw(); });
            toggle.append(button);
          });
          root.append(toggle);
        }
        const redraw = () => {
          const mode = relicDiffMode();
          hunks.forEach(({ place, hunk }) => place.replaceChildren(relicHunkTable(hunk, mode)));
          if (toggle) {
            toggle.hidden = !!relicDiffWide && !relicDiffWide.matches;
            toggle.querySelectorAll("button").forEach((button) => {
              button.setAttribute("aria-pressed", String(button.dataset.diffToggle === mode));
            });
          }
          root.dataset.diffMode = mode;
        };
        root.append(...sections);
        redraw();
        relicDiffRedraw = redraw;
        relicView.replaceChildren(header, root);
        root.dataset.diffState = relicReady;
        relicView.dataset.relicState = relicReady;
      }

      // ─── Reading ───
      function relicVersionsAPIPath(current) {
        const base = "/api/relics/" + encodeURIComponent(current.id);
        if (current.view === "relic-history") {
          return base + "/history" + relicQuery([["part", current.part], ["all", current.all ? "1" : ""]]);
        }
        return base + "/diff" + relicQuery([["part", current.part], ["from", current.from], ["to", current.to]]);
      }
      // The address with the full IDs the answer named, for the members the
      // address asked about.
      function relicFullVersions(current, answer) {
        const full = (asked, named) => (asked && relicAddressableID(named) ? named : asked);
        if (current.view === "relic-history") {
          const history_ = answer.history || {};
          return { ...current, id: full(current.id, (history_.relic || {}).id), part: full(current.part, history_.part) };
        }
        const diff = answer.diff || {};
        return {
          ...current, id: full(current.id, diff.relic), part: full(current.part, diff.part),
          from: full(current.from, (diff.from || {}).version), to: full(current.to, (diff.to || {}).version),
        };
      }
      function relicVersionsFailure(current, read) {
        const heading = current.view === "relic-history" ? "History unavailable" : "Changes unavailable";
        document.title = heading + boardTitleSuffix;
        // The server's own message says which relic, part or version it could
        // not find, which a notice naming the relic alone would not.
        const nodes = [relicViewHeader(heading, relicBackLine(current.id, "")), relicNotice(read.message)];
        const state = read.failure === "not-found" ? relicNotFound : relicError;
        if (current.view === "relic-diff") nodes.push(relicDiffRoot(state));
        relicSettle(state, ...nodes);
      }
      async function readRelicVersions(current, draw, quiet) {
        const read = await readRelicJSON(relicVersionsAPIPath(current), "This relic's history could not be read from this checkout.");
        if (draw !== relicDraws) return;
        if (read.failure) {
          if (!quiet) relicVersionsFailure(current, read);
          return;
        }
        const signature = JSON.stringify(read.document);
        if (quiet && relicVersionsShown && relicVersionsShown.signature === signature) return;
        const full = relicFullVersions(current, read.document);
        const address = full.view === "relic-history"
          ? relicHistoryAddress(full.id, full.part, full.all)
          : relicDiffAddress(full.id, full.part, full.from, full.to);
        if (relicAddressableID(full.id)) {
          if (address !== window.location.pathname + window.location.search) history.replaceState({}, "", address);
          relicDrawnAddress = address;
        }
        relicVersionsShown = { current: full, signature, draw };
        if (full.view === "relic-history") drawRelicHistory(full, read.document.history || {});
        else drawRelicDiff(full, read.document.diff || {});
      }
      // Draws a history or diff route, or keeps what is drawn when the route is
      // the one already on screen, as renderRelicRoute does.
      function renderRelicVersionsRoute(current) {
        if (relicsHeaderLink) relicsHeaderLink.setAttribute("aria-current", "page");
        const address = window.location.pathname + window.location.search;
        if (main.firstElementChild === relicView && address === relicDrawnAddress) return;
        stopRelicBodies();
        relicPinnedPart = null;
        relicDraws += 1;
        relicDrawnAddress = address;
        relicShown = null;
        relicVersionsShown = null;
        relicDiffRedraw = null;
        delete relicView.dataset.bodiesPending;
        relicView.hidden = false;
        if (main.firstElementChild !== relicView) main.replaceChildren(relicView);
        const heading = current.view === "relic-history" ? "Loading history" : "Loading changes";
        document.title = heading + boardTitleSuffix;
        const nodes = [relicViewHeader(heading), relicElement("p", "relic-view__note", "Reading relics from this checkout.")];
        if (current.view === "relic-diff") nodes.push(relicDiffRoot(relicLoading));
        relicSettle(relicLoading, ...nodes);
        main.scrollTop = 0;
        window.scrollTo(0, 0);
        readRelicVersions(current, relicDraws, false);
      }
      if (relicView) {
        if (relicDiffWide) relicDiffWide.addEventListener("change", () => { if (relicDiffRedraw) relicDiffRedraw(); });
        // A tab that comes back into sight reads its history or diff again. A
        // draw count that has moved on means the reader has left the view.
        document.addEventListener("visibilitychange", () => {
          if (document.visibilityState !== "visible" || !relicVersionsShown || relicVersionsShown.draw !== relicDraws ||
              main.firstElementChild !== relicView) return;
          readRelicVersions(relicVersionsShown.current, relicDraws, true);
        });
      }
