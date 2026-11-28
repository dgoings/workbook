      // ─── Relics ─────────────────────────────────────────────────────────────
      //
      // The relic pages: the list and a relic with its parts. Every string a
      // relic carries — its title, its type, its fields and properties, its
      // bodies, its author, the rel of its links — was written by a teammate,
      // so nothing here ever reads one as markup. Every node is built with
      // createElement and filled with text(), which drops direction controls,
      // or drawn by renderMarkdown, which builds nodes and nothing else. A
      // relic string reaches an attribute only as an href built from an ID
      // that has been checked to be one, or as a data-, aria- or title value.
      //
      // The view is read when its route is drawn and again when the tab comes
      // back into sight, and never on the board's poll: the board does not
      // fetch relics, so a poll would only ever read this checkout's own refs
      // again. The view reports what it has finished drawing in
      // data-relic-state — loading, ready, error or not-found — and each body in
      // data-body-state, set only once the DOM for that state is complete, so a
      // reader of the page (a test, an assistive tool) waits on a state rather
      // than on time.
      const relicView = document.querySelector("[data-relic-view]");
      // The view's states, written once as a list. One of them is spelled like
      // a status this board ships with, and the script names no status as a
      // literal of its own (see TestClientScriptNamesNoStatusOfItsOwn), so the
      // four are read out of one string rather than written four times.
      const [relicLoading, relicReady, relicError, relicNotFound] = "loading ready error not-found".split(" ");
      const relicsHeaderLink = document.querySelector("a.header-link[href=\"/relics\"]");
      // The address the view was last drawn for, and a count of the draws, so a
      // read that lands after the reader has moved on draws nothing.
      let relicDrawnAddress = null;
      let relicDraws = 0;
      // What the view is showing, for the read a returning tab makes: the
      // address it answers and a signature of the document it was drawn from.
      let relicShown = null;
      // The open relic page's body loader, stopped when the page goes.
      let relicBodies = null;

      // The views that are relic routes. The history and diff views are drawn
      // in their own place in renderRoute.
      function relicRouteView(view) {
        return view === "relics" || view === "relic" || view === "relic-history" || view === "relic-diff";
      }
      // An ID this page will build an address from: letters, digits and dashes,
      // which is every task, relic and part ID Workbook writes. Anything else is
      // drawn as text and linked nowhere.
      function relicAddressableID(id) {
        return typeof id === "string" && /^[A-Za-z0-9-]{1,200}$/.test(id);
      }
      function relicPath(relicID) { return "/relics/" + encodeURIComponent(relicID); }
      function relicPartPath(relicID, partID) { return relicPath(relicID) + "/parts/" + encodeURIComponent(partID); }
      function relicBodyPath(relicID, partID) { return relicPartPath(relicID, partID) + "/body"; }
      // The list's address for a filter state, written in one fixed order so one
      // state has one address.
      function relicsAddress(filters) {
        const params = new URLSearchParams();
        if (filters.type) params.set("type", filters.type);
        if (filters.retired) params.set("retired", "1");
        if (filters.deleted) params.set("deleted", "1");
        const query = params.toString();
        return "/relics" + (query ? "?" + query : "");
      }
      // The route an address under /relics names.
      function relicRoute(url) {
        const params = url.searchParams;
        if (url.pathname === "/relics") {
          return { view: "relics", type: params.get("type") || "", retired: params.get("retired") === "1", deleted: params.get("deleted") === "1" };
        }
        const segments = url.pathname.split("/").slice(2);
        let names;
        try { names = segments.map((segment) => decodeURIComponent(segment)); } catch (_) { return { view: "unknown" }; }
        if (names.some((name) => name === "")) return { view: "unknown" };
        const removed = params.get("removed") === "1";
        if (names.length === 1) return { view: "relic", id: names[0], part: "", removed };
        if (names.length === 3 && names[1] === "parts") return { view: "relic", id: names[0], part: names[2], removed };
        if (names.length === 2 && names[1] === "history") return { view: "relic-history", id: names[0] };
        if (names.length === 2 && names[1] === "diff") return { view: "relic-diff", id: names[0] };
        return { view: "unknown" };
      }

      // One read of a relic route's JSON. It answers the document, or what went
      // wrong: "not-found" for a 404 and "error" for anything else, with the
      // server's own message, which already names the relic and says to run
      // `workbook validate` for a damaged one.
      async function readRelicJSON(path, fallback) {
        let response;
        try { response = await fetch(path, { cache: "no-store" }); }
        catch (_) { return { failure: "error", message: fallback }; }
        let payload = null;
        try { payload = await response.json(); } catch (_) { payload = null; }
        if (response.ok && payload) return { document: payload };
        const message = payload && payload.error && typeof payload.error.message === "string" ? payload.error.message : fallback;
        return { failure: response.status === 404 ? "not-found" : "error", message };
      }

      // Small builders. Each fills its node with text() and nothing else.
      function relicElement(tagName, className, value) {
        const element = document.createElement(tagName);
        if (className) element.className = className;
        if (value !== undefined && value !== null) text(element, String(value));
        return element;
      }
      function relicBadge(label) {
        const badge = relicElement("span", "relic-badge", label);
        badge.dataset.relicBadge = "";
        return badge;
      }
      function relicNotice(message) {
        const notice = relicElement("p", "relic-notice", message);
        notice.dataset.relicNotice = "";
        notice.setAttribute("role", "alert");
        return notice;
      }
      // A time as this board draws a change's: the reader's own clock and
      // format, with the stored instant in the datetime attribute.
      function relicTime(value) {
        const when = relicElement("time", "", value ? recordedAt(value) : "");
        if (value) when.dateTime = String(value);
        return when;
      }
      // One row of a description list, its value marked with data-relic-fact
      // so a reader of the page can find it by name.
      function relicFact(list, name, label, value) {
        const term = relicElement("dt", "", label);
        const description = document.createElement("dd");
        description.dataset.relicFact = name;
        if (value instanceof Node) description.append(value);
        else text(description, value == null ? "" : String(value));
        list.append(term, description);
      }
      // A property's or a field's value, by its JSON type: a string as its text,
      // a number, a boolean or null as JSON, and an array or an object as
      // indented JSON in a block of its own. All of it is text.
      function relicValue(value) {
        if (typeof value === "string") return relicElement("span", "relic-value", value);
        if (value === null || typeof value !== "object") return relicElement("code", "relic-value", JSON.stringify(value));
        return relicElement("pre", "relic-value relic-value--json", JSON.stringify(value, null, 2));
      }
      // A <dl> of a relic's properties or a part's fields, in name order, each
      // value marked with marker so it can be found by name.
      function relicValues(values, marker) {
        const list = relicElement("dl", "relic-values");
        Object.keys(values || {}).sort().forEach((name) => {
          const description = document.createElement("dd");
          description.dataset[marker] = name;
          description.append(relicValue(values[name]));
          list.append(relicElement("dt", "", name), description);
        });
        return list;
      }

      // One link a relic or a part has, drawn as `<rel> → <target>`.
      //
      // This is the one place a link is drawn, so it is the one place to change
      // when links between relics become followable. A task target links to the
      // task's page, with its title when this board holds the task. A relic or a
      // part target is drawn as its address, as text, for now.
      function relicLinkItem(link) {
        const item = relicElement("li", "relic-link");
        item.dataset.relicLink = "";
        item.append(relicElement("span", "relic-link__rel", link.rel || ""), document.createTextNode(" → "));
        const to = link.to || {};
        if (to.task) {
          const task = relicAddressableID(to.task) ? latestTasks.find((candidate) => candidate.id === to.task) : null;
          const label = to.task + (task && task.title ? " " + task.title : "");
          if (relicAddressableID(to.task)) {
            const anchor = relicElement("a", "relic-link__target", label);
            anchor.href = taskPath(to.task);
            item.append(anchor);
          } else {
            item.append(relicElement("span", "relic-link__target", label));
          }
        } else {
          const address = to.part ? (to.relic || "") + "#" + to.part : (to.relic || "");
          item.append(relicElement("span", "relic-link__target", address));
        }
        if (link.targetRemoved) item.append(document.createTextNode(" (removed)"));
        return item;
      }
      function relicLinkList(links) {
        const list = relicElement("ul", "relic-links");
        links.forEach((link) => list.append(relicLinkItem(link)));
        return list;
      }

      // The view's header: its one h2, and what is said under it.
      function relicViewHeader(title, ...lines) {
        const header = relicElement("header", "relic-view__header");
        const heading = relicElement("h2", "", title);
        heading.id = "relic-view-title";
        header.append(heading, ...lines);
        return header;
      }
      // Puts the view into a state with what it draws for that state, in that
      // order: the DOM first and the state after, so a reader waiting on the
      // state finds everything the state promises.
      function relicSettle(state, ...nodes) {
        relicView.replaceChildren(...nodes);
        relicView.dataset.relicState = state;
      }
      function relicFailure(read, notFoundText) {
        if (read.failure === "not-found") {
          document.title = "Relic not found" + boardTitleSuffix;
          relicSettle(relicNotFound, relicViewHeader("Relic not found"), relicNotice(notFoundText));
          return;
        }
        document.title = "Relics unavailable" + boardTitleSuffix;
        relicSettle(relicError, relicViewHeader("Relics unavailable"), relicNotice(read.message));
      }

      // ─── The list ───
      function relicListFilters(current, types) {
        const form = relicElement("form", "relic-filters");
        form.setAttribute("role", "search");
        form.setAttribute("aria-label", "Filter relics");
        const typeLabel = relicElement("label", "relic-filters__field");
        typeLabel.append(relicElement("span", "", "Type"));
        const select = document.createElement("select");
        select.dataset.relicFilter = "type";
        const every = relicElement("option", "", "All types"); every.value = "";
        select.append(every);
        const offered = types.includes(current.type) || !current.type ? types : types.concat([current.type]);
        offered.forEach((type) => { const option = relicElement("option", "", type); option.value = type; select.append(option); });
        select.value = current.type;
        typeLabel.append(select);
        const toggle = (name, label, checked) => {
          const wrapper = relicElement("label", "relic-filters__toggle");
          const box = document.createElement("input");
          box.type = "checkbox";
          box.checked = checked;
          box.dataset.relicFilter = name;
          wrapper.append(box, relicElement("span", "", label));
          return wrapper;
        };
        form.append(typeLabel, toggle("retired", "Show retired", current.retired), toggle("deleted", "Show deleted", current.deleted));
        const chosen = () => ({
          type: select.value,
          retired: form.querySelector("[data-relic-filter=\"retired\"]").checked,
          deleted: form.querySelector("[data-relic-filter=\"deleted\"]").checked,
        });
        // A change navigates, so every filter state is an address and Back
        // walks them.
        form.addEventListener("change", () => navigate(relicsAddress(chosen())));
        form.addEventListener("submit", (event) => { event.preventDefault(); navigate(relicsAddress(chosen())); });
        return form;
      }
      function relicListRow(summary) {
        const row = relicElement("li", "relic-row");
        row.dataset.relicRow = summary.id || "";
        const title = summary.title || summary.id || "Untitled relic";
        const heading = relicElement(relicAddressableID(summary.id) ? "a" : "span", "relic-row__title", title);
        if (relicAddressableID(summary.id)) heading.href = relicPath(summary.id);
        const meta = relicElement("p", "relic-row__meta");
        meta.append(relicElement("span", "relic-row__type", summary.type || ""), relicElement("code", "relic-row__id", summary.id || ""));
        const parts = Number(summary.parts) || 0;
        meta.append(relicElement("span", "", parts === 1 ? "1 part" : parts + " parts"));
        if (summary.updatedAt) {
          const updated = relicElement("span", "", "updated ");
          updated.append(relicTime(summary.updatedAt));
          meta.append(updated);
        }
        row.append(heading);
        if (summary.retired || summary.deleted) {
          const badges = relicElement("span", "relic-badges");
          if (summary.retired) badges.append(relicBadge("Retired"));
          if (summary.deleted) badges.append(relicBadge("Deleted"));
          row.append(badges);
        }
        row.append(meta);
        return row;
      }
      function relicListAPIPath(current) {
        const params = new URLSearchParams();
        if (current.type) params.set("type", current.type);
        if (current.retired) params.set("retired", "include");
        if (current.deleted) params.set("deleted", "include");
        const query = params.toString();
        return "/api/relics" + (query ? "?" + query : "");
      }
      function drawRelicList(current, document_) {
        const relics = Array.isArray(document_.relics) ? document_.relics : [];
        const types = Array.isArray(document_.types) ? document_.types.filter((type) => typeof type === "string") : [];
        const note = relicElement("p", "relic-view__note", "Relics this checkout holds; run ");
        note.dataset.relicNote = "";
        note.append(relicElement("code", "", "workbook sync"), document.createTextNode(" to fetch teammates' changes."));
        const nodes = [relicViewHeader("Relics", note), relicListFilters(current, types)];
        if (relics.length === 0) {
          nodes.push(relicElement("p", "relic-view__empty", "No relics match."));
        } else {
          const list = relicElement("ul", "relic-list");
          list.setAttribute("aria-label", "Relics");
          relics.forEach((summary) => list.append(relicListRow(summary)));
          nodes.push(list);
        }
        relicSettle(relicReady, ...nodes);
      }
      async function readRelicList(current, draw, quiet) {
        const read = await readRelicJSON(relicListAPIPath(current), "The relics could not be read from this checkout.");
        if (draw !== relicDraws) return;
        if (read.failure) {
          if (!quiet) relicFailure(read, read.message);
          return;
        }
        const signature = JSON.stringify(read.document);
        if (quiet && relicShown && relicShown.signature === signature) return;
        relicShown = { address: relicDrawnAddress, signature, current };
        drawRelicList(current, read.document);
      }

      // ─── The route ───
      // Draws a relic route, or keeps what is drawn when the route is the one
      // already on screen: renderRoute runs again for the same address when the
      // board's first task read lands, and a reader halfway down a relic must
      // not be sent back to its top.
      function renderRelicRoute(current) {
        if (relicsHeaderLink) relicsHeaderLink.setAttribute("aria-current", "page");
        const address = window.location.pathname + window.location.search;
        if (main.firstElementChild === relicView && address === relicDrawnAddress) return;
        // Another part of the relic already drawn: the page is marked and
        // scrolled rather than read again.
        if (current.view === "relic" && main.firstElementChild === relicView && relicShown &&
            relicShown.current.view === "relic" && relicShown.relicID === current.id &&
            relicShown.current.removed === current.removed && relicView.dataset.relicState === relicReady) {
          const drawn = [...relicView.querySelectorAll("[data-relic-part]")].map((section) => ({ id: section.dataset.relicPart }));
          const addressed = relicAddressedPart(drawn, current.part);
          if (!current.part || addressed) {
            relicDrawnAddress = address;
            relicShown.current = current;
            relicFocusPart(addressed ? addressed.id : "");
            if (!addressed) { main.scrollTop = 0; window.scrollTo(0, 0); }
            return;
          }
        }
        stopRelicBodies();
        relicDraws += 1;
        relicDrawnAddress = address;
        relicShown = null;
        relicView.hidden = false;
        if (main.firstElementChild !== relicView) main.replaceChildren(relicView);
        document.title = (current.view === "relics" ? "Relics" : "Loading relic") + boardTitleSuffix;
        relicSettle(relicLoading, relicViewHeader(current.view === "relics" ? "Relics" : "Loading relic"),
          relicElement("p", "relic-view__note", "Reading relics from this checkout."));
        if (current.view === "relics") {
          readRelicList(current, relicDraws, false);
        } else {
          readRelicPage(current, relicDraws, false);
        }
      }
      // Leaving the relic routes: the loader stops, so no body read is started
      // for a page nobody is on, and the header link stops saying it is current.
      function leaveRelicRoute() {
        if (!relicView) return;
        stopRelicBodies();
        relicDraws += 1;
        relicDrawnAddress = null;
        relicShown = null;
        relicView.hidden = true;
        if (relicsHeaderLink) relicsHeaderLink.removeAttribute("aria-current");
      }
      function stopRelicBodies() {
        if (relicBodies) relicBodies.stop();
        relicBodies = null;
      }
      // A tab that comes back into sight reads its relic route again and
      // redraws only what changed, since a teammate's changes may have been
      // fetched meanwhile.
      if (relicView) {
        document.addEventListener("visibilitychange", () => {
          if (document.visibilityState !== "visible" || !relicShown || main.firstElementChild !== relicView) return;
          const current = relicShown.current;
          if (current.view === "relics") readRelicList(current, relicDraws, true);
          else readRelicPage(current, relicDraws, true);
        });
      }
