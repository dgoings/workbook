      // A phone-sized screen shows the board one column at a time, under a row
      // of the columns' names and counts that switches between them. The
      // layout itself is the stylesheet's, under one width query: this is the
      // part only a script can do — remembering which column a phone was
      // last looking at, keeping the row's buttons and counts the same as the
      // columns, and leaving the cards undraggable at that width so a stroke
      // on one scrolls the page rather than starting a drag.
      //
      // The query is the stylesheet's own, written once more here because a
      // script cannot read a media rule's condition back out of the sheet in
      // every browser this board is opened in. phone_desktop_test.go and
      // phone_layout_test.go check both sides of the number.
      const phoneWidth = window.matchMedia ? window.matchMedia("(max-width: 560px)") : null;
      function phoneLayout() {
        return Boolean(phoneWidth && phoneWidth.matches);
      }
      const statusTabs = boardView.querySelector("[data-status-tabs]");
      // Where the column a phone was last looking at is kept, in this
      // browser's own storage: it is a fact about one phone, not about the
      // project, so it is never sent anywhere. A status is kept with a prefix
      // and the Deleted column as a word of its own, because a project may
      // name a status anything, deleted included. The name it is kept under is
      // the server's, and names the project (see phoneColumnStorageKey in
      // handler.go): every local board is served at the same address, and one
      // project's column is no answer for another's.
      const phoneColumnKey = boardView.dataset.phoneColumnKey || "workbook.board.phoneColumn";
      // The column a phone opens on before it has one of its own: the first
      // column `workbook next` draws from, which only the server can name.
      const openingColumn = "status:" + (boardView.dataset.openingStatus || (statusDefinitions.length ? statusDefinitions[0][0] : ""));
      let phoneColumn = null;
      // The column whose button the row was last slid to, so the row is slid
      // once each time the column shown changes — chosen, or put back on
      // opening — and never again by a render, which would undo a reader
      // drawing the row along to look at the other names.
      let revealedPhoneColumn = null;
      // Whether the reader has picked a column on this page, and the column
      // this phone remembered when the page opened.
      let phoneColumnPicked = false;
      const openingRemembered = rememberedPhoneColumn();
      function rememberedPhoneColumn() {
        try {
          return window.localStorage ? window.localStorage.getItem(phoneColumnKey) : null;
        } catch (_) {
          // Storage a browser refuses — a private window, or site data
          // blocked — leaves the board opening where it would on a first
          // visit, every time.
          return null;
        }
      }
      function rememberPhoneColumn(key) {
        try {
          if (window.localStorage) window.localStorage.setItem(phoneColumnKey, key);
        } catch (_) {
          // Not remembered, and nothing else changes: the column is still
          // the one shown until the page is left.
        }
      }
      // The columns the row offers, in board order: each status column the
      // server drew, then the Deleted column while it is shown.
      function phoneColumns() {
        const columns = statusDefinitions.map(([status, label]) => ({
          key: "status:" + status,
          status,
          label,
          section: lists.get(status).closest(".column"),
          count: counts.get(status)
        }));
        if (deletedSection) columns.push({ key: "deleted", status: "", label: "Deleted", section: deletedSection, count: deletedCount });
        return columns;
      }
      function statusTabKey(button) {
        return button.hasAttribute("data-status-tab-deleted") ? "deleted" : "status:" + button.dataset.statusTab;
      }
      function buildStatusTab(column) {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "status-tab";
        if (column.key === "deleted") button.setAttribute("data-status-tab-deleted", "");
        else button.dataset.statusTab = column.status;
        const label = document.createElement("span");
        label.className = "status-tab__label";
        text(label, column.label);
        const count = document.createElement("span");
        count.className = "status-tab__count";
        count.setAttribute("data-status-tab-count", "");
        button.append(label, document.createTextNode(" "), count);
        return button;
      }
      // Brings the row into line with the columns: one button per column in
      // the same order, each showing the count its column shows — filtered
      // counts included, "3 / 9" and all — and the shown column still one
      // that exists. The buttons are rebuilt only when the set of columns
      // changes, which is the Deleted switch and nothing else.
      function syncStatusTabs() {
        if (!statusTabs) return;
        const columns = phoneColumns();
        const buttons = [...statusTabs.children];
        if (buttons.map(statusTabKey).join("\n") !== columns.map((column) => column.key).join("\n")) {
          statusTabs.replaceChildren(...columns.map(buildStatusTab));
        }
        [...statusTabs.children].forEach((button, index) => {
          const shown = columns[index].count ? columns[index].count.textContent : "";
          const count = button.querySelector("[data-status-tab-count]");
          if (count && count.textContent !== shown) text(count, shown);
        });
        const keys = new Set(columns.map((column) => column.key));
        let wanted = phoneColumn;
        // Until the reader picks a column on this page, the one this phone
        // remembered is put back as soon as the board draws it. The Deleted
        // column is drawn by this script after the page's first render, so a
        // phone that was last looking at it comes back to it a moment after
        // the page opens rather than not at all.
        if (!phoneColumnPicked && openingRemembered && keys.has(openingRemembered)) wanted = openingRemembered;
        if (!wanted || !keys.has(wanted)) wanted = openingColumn;
        if (!keys.has(wanted) && columns.length) wanted = columns[0].key;
        showPhoneColumn(wanted, false);
      }
      // Shows one column on a phone and presses its button. Every column but
      // that one is hidden by the phone rules alone, through the attribute,
      // so a wider screen — which never reads it — is untouched by any of it.
      // The row slides to bring its button into view whenever the column
      // shown changes: smoothly when the reader chose it, unless they asked
      // for less motion, and at once when the board put a remembered column
      // back, which moves the row and never the page. When the reader chose
      // it, it is also remembered, and a page scrolled down the last column is
      // taken back to the top of this one.
      function showPhoneColumn(key, chosen) {
        phoneColumn = key;
        phoneColumns().forEach((column) => {
          const current = column.key === key;
          if (column.section.hasAttribute("data-phone-current") !== current) {
            if (current) column.section.setAttribute("data-phone-current", "");
            else column.section.removeAttribute("data-phone-current");
          }
        });
        let pressed = null;
        if (statusTabs) {
          [...statusTabs.children].forEach((button) => {
            const current = statusTabKey(button) === key;
            if (current) pressed = button;
            const value = current ? "true" : "false";
            if (button.getAttribute("aria-pressed") !== value) button.setAttribute("aria-pressed", value);
          });
        }
        if (chosen) rememberPhoneColumn(key);
        if (!phoneLayout() || !pressed) return;
        if (chosen || revealedPhoneColumn !== key) {
          revealedPhoneColumn = key;
          const left = Math.max(0, pressed.offsetLeft - (statusTabs.clientWidth - pressed.offsetWidth) / 2);
          const still = !chosen || (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
          if (still || typeof statusTabs.scrollTo !== "function") statusTabs.scrollLeft = left;
          else statusTabs.scrollTo({ left, behavior: "smooth" });
        }
        if (!chosen) return;
        const top = board.getBoundingClientRect().top + window.scrollY - statusTabs.offsetHeight;
        if (window.scrollY > top) window.scrollTo(0, top);
      }
      if (statusTabs) {
        statusTabs.addEventListener("click", (event) => {
          const button = event.target && event.target.closest && event.target.closest(".status-tab");
          if (!button || !statusTabs.contains(button)) return;
          phoneColumnPicked = true;
          showPhoneColumn(statusTabKey(button), true);
        });
      }
      // Whether a card can be dragged at this width. A phone's browser starts
      // an HTML drag from a long press on anything draggable, and a finger
      // that rests on a card before it moves is a finger scrolling the
      // column, so at the phone width no card is draggable at all: the drag
      // is never armed, and the stroke goes to the page. A status is changed
      // from the task's own page there. The pending stand-in of a task still
      // being created never drags, at any width.
      function cardDraggable(card) {
        return !phoneLayout() && card.dataset.pendingCreate !== "true";
      }
      function armCardDrags() {
        cardLists().forEach((list) => {
          cardNodes(list).forEach((card) => {
            const draggable = cardDraggable(card);
            if (card.draggable !== draggable) card.draggable = draggable;
          });
        });
      }
      // The notices' box on a phone is held against the bottom of the screen,
      // and the page keeps that much clear at the bottom whenever focus or a
      // script brings a control into view (scroll-padding-bottom, in the phone
      // rules): this keeps the number the same as the box's real height as
      // reports come and go and their text wraps. Wider than a phone the box
      // is no box at all and the number is left unset.
      const notices = document.querySelector("[data-notices]");
      function measureNotices() {
        if (!notices) return;
        const root = document.documentElement;
        if (!root || !root.style || typeof root.style.setProperty !== "function") return;
        const height = phoneLayout() ? Math.ceil(notices.getBoundingClientRect().height) : 0;
        if (height > 0) root.style.setProperty("--wb-notices-height", height + "px");
        else root.style.removeProperty("--wb-notices-height");
      }
      if (notices && typeof ResizeObserver === "function") new ResizeObserver(measureNotices).observe(notices);
      function phoneWidthChanged() {
        armCardDrags();
        // A row that was not drawn could not be slid; it is now.
        revealedPhoneColumn = null;
        syncStatusTabs();
        measureNotices();
      }
      if (phoneWidth) {
        if (typeof phoneWidth.addEventListener === "function") phoneWidth.addEventListener("change", phoneWidthChanged);
        else if (typeof phoneWidth.addListener === "function") phoneWidth.addListener(phoneWidthChanged);
      }
      armCardDrags();
      syncStatusTabs();
      // The rule the page's opening script added to show a remembered column
      // from the first frame (see body.html) has done its work: the column
      // shown is now the one data-phone-current marks.
      if (typeof document.querySelectorAll === "function") document.querySelectorAll("style[data-phone-opening]").forEach((rule) => rule.remove());
      measureNotices();
