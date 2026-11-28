      // ─── A relic's page ───
      //
      // The enforced fields, the properties, the links, a contents list and
      // every part in order with its caption and fields. The relic's own read
      // carries no body text — a relic may hold megabytes of bodies — so each
      // body is read by itself as its part comes within a screen of being
      // seen, at most four at a time, and each once per visit.
      const relicBodyLimit = 4;
      // A part's caption: its heading field, else its title field, else its
      // type.
      function relicPartCaption(part) {
        const fields = part.fields || {};
        for (const name of ["heading", "title"]) {
          if (typeof fields[name] === "string" && fields[name].trim()) return fields[name];
        }
        return part.type || "Part";
      }
      function relicSchemaText(schema) {
        if (!schema) return "open";
        if (schema.version) return (schema.type || "") + " version " + schema.version;
        return (schema.type || "") + " " + (schema.id || "");
      }
      function relicConformanceText(relic) {
        switch (relic.conformance) {
          case "conforms": return "conforms";
          case "not-conforming": return "does not fit its schema: " + (relic.schemaProblem || "");
          case "unknown-schema": return "unknown schema";
          default: return "";
        }
      }
      // The relic's ID with a control that copies it, drawn as the task ID's
      // copy control is. It has a handler of its own rather than the task one,
      // which says "task ID" in what it announces.
      function relicIDCopyControl(relicID) {
        const group = relicElement("span", "task-id-copy-group");
        const control = relicElement("button", "task-id-copy");
        control.type = "button";
        control.setAttribute("aria-label", withoutDirectionControls("Copy full relic ID " + relicID));
        control.append(relicElement("code", "", relicID));
        const status = relicElement("span", "copy-status");
        status.setAttribute("role", "status");
        status.setAttribute("aria-live", "polite");
        control.addEventListener("click", async () => {
          try {
            if (!navigator.clipboard || typeof navigator.clipboard.writeText !== "function") throw new Error("clipboard unavailable");
            await navigator.clipboard.writeText(relicID);
            status.dataset.kind = "success";
            text(status, "Copied");
          } catch (_) {
            status.dataset.kind = "error";
            text(status, "Copy failed");
          }
        });
        group.append(control, status);
        return group;
      }
      function relicSection(className, title, ...nodes) {
        const section = relicElement("section", "relic-section " + className);
        section.append(relicElement("h3", "", title), ...nodes);
        return section;
      }
      // One part, with the place its body is drawn once read. The heading's
      // id is the part's place in the list rather than its ID, so nothing a
      // relic holds becomes an id.
      function relicPartSection(relic, part, index, links) {
        const section = relicElement("section", "relic-part");
        section.dataset.relicPart = part.id || "";
        const headingID = "relic-part-" + index;
        section.setAttribute("aria-labelledby", headingID);
        const header = relicElement("header", "relic-part__header");
        const heading = relicElement("h3", "", relicPartCaption(part));
        heading.id = headingID;
        const meta = relicElement("p", "relic-part__meta");
        meta.append(relicElement("span", "relic-part__type", part.type || ""), relicElement("code", "relic-part__id", part.id || ""));
        if (part.removed) meta.append(relicBadge("Removed"));
        header.append(heading, meta);
        section.append(header);
        const fields = part.fields || {};
        if (Object.keys(fields).length) section.append(relicValues(fields, "relicField"));
        if (links.length) section.append(relicLinkList(links));
        let body = null;
        if (part.body) {
          body = relicElement("div", "relic-body");
          body.dataset.bodyState = "waiting";
          body.append(relicElement("p", "relic-body__note", "The body is read when this part comes into view."));
          section.append(body);
          if (relicAddressableID(relic.id) && relicAddressableID(part.id)) {
            const footer = relicElement("p", "relic-body__open");
            const open = relicElement("a", "", "Open in new tab");
            open.href = relicBodyPath(relic.id, part.id);
            open.target = "_blank";
            open.rel = "noopener noreferrer";
            open.dataset.relicBodyOpen = "";
            open.setAttribute("aria-label", withoutDirectionControls("Open the body of " + relicPartCaption(part) + " in a new tab"));
            footer.append(open);
            section.append(footer);
          }
        }
        return { section, body };
      }
      // Draws a body once its read is back, and only then says what it is.
      function drawRelicBody(container, read) {
        const settle = (state, ...nodes) => {
          container.replaceChildren(...nodes);
          container.dataset.bodyState = state;
        };
        if (read.failure) {
          settle("error", relicElement("p", "relic-body__error", read.message));
          return;
        }
        const body = read.document.part && read.document.part.body;
        if (!body) {
          settle("error", relicElement("p", "relic-body__error", "This part has no body."));
          return;
        }
        if (typeof body.base64 === "string") {
          settle("binary", relicElement("p", "relic-body__note", "not text, " + attachmentSize(body.bytes)));
          return;
        }
        if (typeof body.text !== "string") {
          settle("error", relicElement("p", "relic-body__error", "This body could not be read."));
          return;
        }
        const format = String(body.format || "").trim().toLowerCase();
        if (format === "md" || format === "markdown") {
          const rendered = relicElement("div", "markdown relic-body__markdown");
          renderMarkdown(rendered, body.text, { tables: true });
          settle("drawn", rendered);
          return;
        }
        if (format === "html" || format === "htm") {
          const label = relicElement("p", "relic-body__label", "HTML, shown as source");
          label.dataset.bodyLabel = "";
          settle("drawn", label, relicElement("pre", "relic-body__source", body.text));
          return;
        }
        settle("drawn", relicElement("pre", "relic-body__source", body.text));
      }
      // Reads the bodies of one relic page as their parts near the screen.
      //
      // The parts are watched rather than their bodies, because a part off the
      // screen skips drawing its contents (content-visibility) and a watcher
      // inside it would be asked about a box that is not drawn. A part counts
      // as near within one screen above or below what is seen. A near part
      // waits in a queue; one that leaves before its read starts leaves the
      // queue, and a read once started is the part's only read this visit.
      // data-bodies-pending on the view counts the reads waiting or running.
      function relicBodyLoader(relicID, draw, drawn) {
        const watched = new Map();
        const queue = [];
        let running = 0;
        let stopped = false;
        // The board scrolls inside main on a wide screen and the document on a
        // phone, and the near zone is measured against whichever scrolls. That
        // changes when a window narrows or a phone turns, so the watcher is
        // made again then: one still measuring against a main that no longer
        // scrolls would find every part near, or none.
        const scrollerNow = () => (getComputedStyle(main).overflowY === "visible" ? null : main);
        let scroller = scrollerNow();
        let observer = null;
        const report = () => { relicView.dataset.bodiesPending = String(queue.length + running); };
        const start = async (entry) => {
          entry.started = true;
          observer.unobserve(entry.section);
          running += 1;
          entry.body.dataset.bodyState = "loading";
          const read = await readRelicJSON("/api/relics/" + encodeURIComponent(relicID) + "/parts/" + encodeURIComponent(entry.partID),
            "This body could not be read from this checkout.");
          running -= 1;
          if (stopped || draw !== relicDraws) return;
          drawRelicBody(entry.body, read);
          drawn();
          pump();
        };
        const pump = () => {
          while (!stopped && running < relicBodyLimit && queue.length) start(queue.shift());
          if (!stopped) report();
        };
        const noticed = (records) => {
          records.forEach((record) => {
            const entry = watched.get(record.target);
            if (!entry || entry.started) return;
            const queued = queue.indexOf(entry);
            if (record.isIntersecting && queued < 0) queue.push(entry);
            if (!record.isIntersecting && queued >= 0) queue.splice(queued, 1);
          });
          pump();
        };
        // Watches every part not yet read against what scrolls now. The queue
        // is emptied first: the new watcher reports each part afresh, and a
        // part the old one found near may not be.
        const watchParts = () => {
          if (observer) observer.disconnect();
          queue.length = 0;
          observer = new IntersectionObserver(noticed, { root: scroller, rootMargin: "100% 0px 100% 0px" });
          watched.forEach((entry) => { if (!entry.started) observer.observe(entry.section); });
          report();
        };
        const resized = () => {
          const now = scrollerNow();
          if (stopped || now === scroller) return;
          scroller = now;
          watchParts();
        };
        window.addEventListener("resize", resized);
        watchParts();
        return {
          watch(section, body, partID) {
            watched.set(section, { section, body, partID, started: false });
            observer.observe(section);
          },
          stop() {
            stopped = true;
            window.removeEventListener("resize", resized);
            observer.disconnect();
            queue.length = 0;
          },
        };
      }
      // The part an address names, by its full ID or a prefix only it has.
      function relicAddressedPart(parts, wanted) {
        if (!wanted) return null;
        const upper = wanted.toUpperCase();
        const exact = parts.find((part) => String(part.id || "").toUpperCase() === upper);
        if (exact) return exact;
        const matches = parts.filter((part) => String(part.id || "").toUpperCase().startsWith(upper));
        return matches.length === 1 ? matches[0] : null;
      }
      // The part an address brought into view, kept there while the bodies
      // above it draw and push it down, until the reader scrolls for
      // themselves. Scroll anchoring would be the browser's way to do this, and
      // it does not hold across parts that skip drawing their contents.
      let relicPinnedPart = null;
      for (const gesture of ["wheel", "touchmove", "keydown", "pointerdown"]) {
        window.addEventListener(gesture, () => { relicPinnedPart = null; }, { passive: true, capture: true });
      }
      function relicKeepPinnedPart() {
        if (relicPinnedPart && relicPinnedPart.isConnected) relicPinnedPart.scrollIntoView({ block: "start" });
      }
      // Marks the addressed part and brings it into view.
      function relicFocusPart(partID) {
        relicPinnedPart = null;
        relicView.querySelectorAll("[data-relic-part][aria-current]").forEach((section) => section.removeAttribute("aria-current"));
        if (!partID) return;
        const section = [...relicView.querySelectorAll("[data-relic-part]")].find((candidate) => candidate.dataset.relicPart === partID);
        if (!section) return;
        section.setAttribute("aria-current", "true");
        relicPinnedPart = section;
        section.scrollIntoView({ block: "start" });
      }
      function drawRelicPage(current, viewDocument, draw) {
        const relic = viewDocument.relic || {};
        const parts = Array.isArray(relic.parts) ? relic.parts : [];
        const links = Array.isArray(relic.links) ? relic.links : [];
        const title = relic.title || relic.id || "Untitled relic";
        document.title = withoutDirectionControls(title + boardTitleSuffix);
        const readonly = relicElement("p", "relic-view__note", "Relics are changed with ");
        readonly.dataset.relicReadonly = "";
        readonly.append(relicElement("code", "", "workbook relic update"), document.createTextNode("."));
        const lines = [readonly];
        if (relic.retired || relic.deleted) {
          const badges = relicElement("p", "relic-badges");
          if (relic.retired) badges.append(relicBadge("Retired"));
          if (relic.deleted) badges.append(relicBadge("Deleted"));
          lines.push(badges);
        }
        const nodes = [relicViewHeader(title, ...lines)];
        const addressed = relicAddressedPart(parts, current.part);
        if (current.part && !addressed) nodes.push(relicNotice("No part " + current.part + " in this relic."));
        (Array.isArray(viewDocument.warnings) ? viewDocument.warnings : []).forEach((warning) => {
          if (warning && warning.message) nodes.push(relicElement("p", "relic-warning", warning.message));
        });
        const facts = relicElement("dl", "relic-facts");
        relicFact(facts, "type", "Type", relic.type || "");
        relicFact(facts, "id", "ID", relicIDCopyControl(relic.id || ""));
        relicFact(facts, "author", "Author", relic.author || "");
        relicFact(facts, "created", "Created", relicTime(relic.createdAt));
        relicFact(facts, "updated", "Updated", relicTime(relic.updatedAt));
        relicFact(facts, "schema", "Schema", relicSchemaText(relic.schema));
        const conformance = relicConformanceText(relic);
        if (conformance) relicFact(facts, "conformance", "Conformance", conformance);
        nodes.push(facts);
        const properties = relic.properties || {};
        if (Object.keys(properties).length) nodes.push(relicSection("relic-properties", "Properties", relicValues(properties, "relicProperty")));
        // A link from a part is drawn under that part; every other one is the
        // relic's own.
        const partIDs = new Set(parts.map((part) => part.id));
        const ownLinks = links.filter((link) => !link.from || !partIDs.has(link.from));
        if (ownLinks.length) nodes.push(relicSection("relic-own-links", "Links", relicLinkList(ownLinks)));
        if (parts.length) {
          const contents = relicElement("nav", "relic-contents");
          contents.setAttribute("aria-label", "Parts");
          const list = relicElement("ol", "");
          parts.forEach((part) => {
            const item = relicElement("li", "");
            const caption = relicPartCaption(part);
            if (relicAddressableID(relic.id) && relicAddressableID(part.id)) {
              const anchor = relicElement("a", "", caption);
              anchor.href = relicPartPath(relic.id, part.id) + (current.removed ? "?removed=1" : "");
              item.append(anchor);
            } else {
              text(item, caption);
            }
            list.append(item);
          });
          contents.append(relicElement("h3", "", "Parts"), list);
          nodes.push(contents);
        }
        delete relicView.dataset.bodiesPending;
        const loader = relicBodyLoader(relic.id || current.id, draw, relicKeepPinnedPart);
        relicBodies = loader;
        const sections = parts.map((part, index) => {
          const drawn = relicPartSection(relic, part, index, links.filter((link) => link.from && link.from === part.id));
          if (drawn.body) loader.watch(drawn.section, drawn.body, part.id || "");
          return drawn.section;
        });
        nodes.push(...sections);
        relicView.replaceChildren(...nodes);
        relicFocusPart(addressed ? addressed.id : "");
        relicView.dataset.relicState = relicReady;
      }
      async function readRelicPage(current, draw, quiet) {
        const query = current.removed ? "?removed=include" : "";
        const read = await readRelicJSON("/api/relics/" + encodeURIComponent(current.id) + query,
          "This relic could not be read from this checkout.");
        if (draw !== relicDraws) return;
        if (read.failure) {
          if (!quiet) relicFailure(read, read.failure === "not-found" ? "No relic " + current.id + " in this checkout." : read.message);
          return;
        }
        const relic = read.document.relic || {};
        if (quiet && relicShown && relicShown.signature === relic.head) return;
        // A prefix is accepted wherever an ID is, and the address is put back
        // with the full IDs once they are known, so an address copied from here
        // always names them.
        const addressed = relicAddressedPart(Array.isArray(relic.parts) ? relic.parts : [], current.part);
        if (relicAddressableID(relic.id) && (!current.part || addressed)) {
          const full = (addressed ? relicPartPath(relic.id, addressed.id) : relicPath(relic.id)) + window.location.search;
          if (full !== window.location.pathname + window.location.search) history.replaceState({}, "", full);
          relicDrawnAddress = full;
        }
        if (quiet) stopRelicBodies();
        relicShown = { address: relicDrawnAddress, signature: relic.head, current, relicID: relic.id };
        drawRelicPage(current, read.document, draw);
      }
