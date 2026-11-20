    (() => {
      const dropMarker = document.createElement("div");
      dropMarker.className = "drop-marker";
      dropMarker.setAttribute("aria-hidden", "true");
      const main = document.querySelector("main");
      const boardView = document.querySelector("[data-board-view]");
      // The grid the columns are tracks of. The client holds it because the
      // Deleted column is appended to it and taken off it again, and for
      // nothing else: the vocabulary columns inside it are the server's and
      // are never rebuilt.
      const board = boardView.querySelector("[data-board]");
      const statusColumns = [...boardView.querySelectorAll("[data-status]")];
      const lists = new Map(statusColumns.map((element) => [element.dataset.status, element]));
      // The project's statuses, read out of the columns the server rendered
      // rather than kept as a list of its own.
      //
      // There used to be a constant here holding the six statuses Workbook
      // shipped, and a project may define any statuses it likes now, so a
      // constant would be a second answer to a question the server has already
      // answered on this page — one that would be wrong for every project that
      // customized anything. Everything that needs a status now reads these
      // columns: the form's select, the /tasks/new?status= check, and the drop
      // targets, which already did. The order is the columns' order, which is
      // the vocabulary's.
      const statusDefinitions = statusColumns.map((element) => [element.dataset.status, element.dataset.statusLabel || element.dataset.status]);
      const statuses = new Set(statusDefinitions.map(([status]) => status));
      // Where a new task lands when nothing named a status: the project's
      // default-tagged status, which only the server knows. Falling back to the
      // first column keeps a page served by an older build usable rather than
      // sending it to a status that may not exist.
      const defaultStatus = boardView.dataset.defaultStatus || (statusDefinitions.length ? statusDefinitions[0][0] : "");
      // The project's priorities, read out of the attribute the server
      // published them in rather than kept as a list of its own.
      //
      // There used to be three answers here to questions the server had already
      // answered on this page: an order, a set of options and a default, all
      // three naming the priorities Workbook ships with. A project may define
      // any priorities it likes, so each of those was wrong for every project
      // that added or renamed one — a fourth priority could not be assigned from
      // the board at all, sorted against nothing when a card was dragged, and a
      // new task landed on a priority the project may not even define.
      //
      // A page carrying no such attribute — served by an older build — leaves
      // this empty rather than substituting three. Offering priorities nobody
      // has been told this project has is the defect, not the cure.
      const publishedPriorities = (() => {
        try {
          const published = JSON.parse(boardView.dataset.priorities || "[]");
          if (!Array.isArray(published)) return [];
          return published.filter((definition) => definition && typeof definition.priority === "string" && definition.priority !== "");
        } catch (error) {
          return [];
        }
      })();
      // [token, label] pairs, the shape statusDefinitions has, because the form
      // builds both selects the same way and a select is where a person reads a
      // name rather than a token.
      const priorityDefinitions = publishedPriorities.map((definition) => [definition.priority, definition.label || definition.priority]);
      // The priorities this project can resolve, which is the question a task's
      // stored priority has to be asked before a select is told to show it.
      // `statuses` is the same set for the same reason.
      const priorities = new Set(priorityDefinitions.map(([priority]) => priority));
      // Where a priority sits, for the one thing that needs to know: placing a
      // dragged card in a column that holds no card of its own priority.
      //
      // The position in the published array is the whole of it. The server
      // deliberately publishes no rank — the array is already in the
      // vocabulary's order — so this is the order the client was handed rather
      // than one it re-derived and could disagree with the server about.
      const priorityOrder = new Map(publishedPriorities.map((definition, index) => [definition.priority, index]));
      // Where a new task lands when nothing named a priority: the priority this
      // project tagged default, which is the server's answer for the reason
      // defaultStatus is. The role is among the tags published above, and going
      // looking for it there would mean spelling the name of a role core owns —
      // the same rule that keeps this script from naming a status tag. Falling
      // back to the first keeps a page served by an older build usable rather
      // than creating tasks at no priority at all.
      const defaultPriority = boardView.dataset.defaultPriority || (priorityDefinitions.length ? priorityDefinitions[0][0] : "");
      const counts = new Map([...boardView.querySelectorAll("[data-count]")].map((element) => [element.dataset.count, element]));
      const stale = boardView.querySelector("[data-stale]");
      // The region for tasks whose status matches no column. It is part of the
      // page whether or not it currently holds anything, because the status
      // that strands a task arrives on a poll rather than on the first paint.
      const unknownSection = boardView.querySelector("[data-unknown-section]");
      const unknownList = boardView.querySelector("[data-unknown-list]");
      const unknownCount = boardView.querySelector("[data-unknown-count]");
      const updated = document.querySelector("[data-updated]");
      const updatedText = document.querySelector("[data-updated-text]");
      const syncDot = document.querySelector("[data-sync-dot]");
      const notice = document.querySelector("[data-notice]");
      // The standing announcement that this page's columns are out of date, and
      // the control that acts on it. See the element itself for why a reload is
      // offered instead of taken.
      const vocabularyNotice = document.querySelector("[data-vocabulary-notice]");
      const vocabularyReload = document.querySelector("[data-vocabulary-reload]");
      // The two stylesheets the server composed for this board: the colors the
      // project chose, and the ink each of its priorities is drawn in. Held so
      // that a configuration change can redraw the board it was made from.
      const boardThemeStyle = document.querySelector("style[data-board-theme]");
      const boardPriorityInkStyle = document.querySelector("style[data-board-priority-ink]");
      // Redraws one of them from a stylesheet the server composed.
      //
      // What is written is the answer's own bytes, and nothing here reads what
      // they say. That is the whole of why a color a project chose can reach the
      // board without a reload: the server validated it, parsed it to three
      // integers and re-rendered it, and every other byte of the block is a
      // property name it wrote or a number it formatted. A client that was
      // handed the color instead — and composed `--wb-…: ` around it — would be
      // writing a stylesheet with none of that behind it, out of a stored string
      // that had reached this page as text. So this function composes nothing,
      // and it sets textContent, which cannot be markup, rather than innerHTML,
      // which can.
      //
      // A member that is not a string is a document that does not carry one, and
      // the board keeps what it is drawing: only a real answer moves the board,
      // and an empty string is a real answer — it is what a project that has
      // cleared its colors is served.
      function redrawBoardStyle(element, composed) {
        if (!element || typeof composed !== "string") return;
        element.textContent = composed;
      }
      // The statuses section of the configuration route and its parts, served
      // with the page and mounted into main by the render for that route. Every
      // one of them is absent on a board built without the four vocabulary
      // mutations, which is why each is asked for separately and everything
      // below is guarded: such a page has no configuration route at all — the
      // server answers the address with a 404 and route() below answers it as a
      // page that does not exist — and the script must not depend on markup it
      // was not served.
      const vocabularyPanel = document.querySelector("[data-vocabulary-panel]");
      const vocabularyPanelBody = document.querySelector("[data-vocabulary-panel-body]");
      const vocabularyPanelStatus = document.querySelector("[data-vocabulary-panel-status]");
      // The priorities section, beside the statuses on the same route and asked
      // for separately for the same reason: a board may be built with the four
      // status mutations and without the priority ones, in which case the page
      // has columns to administer and no priorities to change.
      const priorityPanel = document.querySelector("[data-priority-panel]");
      const priorityPanelBody = document.querySelector("[data-priority-panel-body]");
      const priorityPanelStatus = document.querySelector("[data-priority-panel-status]");
      // The board settings section, beside the statuses and the priorities on
      // the same route. It is asked for separately from the sections above
      // because a board may be built with the vocabulary mutations and without
      // this one, in which case the page has a vocabulary to administer and no
      // settings to change.
      const displayPanel = document.querySelector("[data-display-panel]");
      const displayPanelBody = document.querySelector("[data-display-panel-body]");
      const displayPanelStatus = document.querySelector("[data-display-panel-status]");
      // The keys section, beside the statuses and the priorities on the same
      // route and asked for separately for the same reason: a board may be
      // built with the status mutations and without the key ones, in which case
      // the page has columns to administer and no keys to change.
      const keyPanel = document.querySelector("[data-key-panel]");
      const keyPanelBody = document.querySelector("[data-key-panel-body]");
      const keyPanelStatus = document.querySelector("[data-key-panel-status]");
      // This project's keys, read out of the attribute the server published
      // them in rather than kept as a list of its own — the same rule the
      // priorities follow, and for the same reason: a project that added a key
      // is described by the server and nowhere in this script.
      //
      // They are variables where the priorities are constants, and that is the
      // one way this reading differs. The keys are deliberately not part of the
      // vocabulary shape: a key change moves no column and no priority, so no
      // board is ever asked to reload for one. That makes this attribute the
      // page's only copy of them — so every vocabulary this page receives
      // replaces it, and the create form's chooser reads it at the moment it is
      // drawn. Nothing polls for a key; the reads are the ones the page already
      // makes, which is the configuration route's read on entry and the answer
      // to every change made there.
      let publishedKeys = readPublishedKeys(boardView.dataset.keys);
      let currentKey = boardView.dataset.currentKey || "";
      // A key entry is a name, a state and whether new tasks are minted under
      // it. An entry without a name is not a key, whatever else it carries, so
      // it is dropped here rather than drawn as a row nothing can address.
      function keyEntries(published) {
        if (!Array.isArray(published)) return [];
        return published.filter((entry) => entry && typeof entry.key === "string" && entry.key !== "");
      }
      // A page carrying no such attribute — served by an older build — leaves
      // this empty rather than inventing a key. Offering a key nobody has been
      // told this project has is the defect, not the cure; with none the create
      // form offers no choice and a task is minted under the current key, which
      // is what the server does with a create that names none.
      function readPublishedKeys(encoded) {
        try {
          return keyEntries(JSON.parse(encoded || "[]"));
        } catch (error) {
          return [];
        }
      }
      // The keys that can mint, which is the only set the create form offers: a
      // retired key mints nothing and the service refuses a create naming one in
      // core's own words, so listing it would be offering a refusal.
      //
      // "retired" is the one state word this script spells. The set is core's
      // (core.KeyState), there are two of them, and a page that asked the server
      // to publish the list would be carrying an attribute to distinguish a key
      // that mints from one that does not.
      function activeKeyEntries() {
        return publishedKeys.filter((entry) => entry.state !== "retired");
      }
      // Takes the keys out of a vocabulary document, wholesale, the way the rest
      // of that document is adopted. A document with no keys member is a board
      // built by something that does not serve them, which is not an answer that
      // this project has none — so the page keeps what it was served with, the
      // rule redrawBoardStyle follows for the same reason.
      function adoptKeys(keys) {
        if (!keys || typeof keys !== "object") return;
        publishedKeys = keyEntries(keys.keys);
        currentKey = typeof keys.current === "string" ? keys.current : "";
      }
      // What this board is called, and what every route that is not the board
      // appends to its own name. Both are the server's answers rather than
      // derived here: the fallbacks are core's, and a client that spelled them
      // would go on saying the old word the day core's changed.
      const boardName = boardView.dataset.projectName || "Workbook board";
      // What this board would be called if its name were cleared, which is what
      // the settings form's own placeholder says. It is not boardName: a project
      // called Atlas whose name field is emptied gets the generic heading, so a
      // placeholder reading "Atlas" would be advertising the value the field no
      // longer holds — and would go on doing so after a save that cleared it.
      const boardDefaultName = boardView.dataset.defaultProjectName || "Workbook board";
      const boardTitleSuffix = " · " + (boardView.dataset.titleSuffix || "Workbook");
      // The configuration ledger tip these columns were built from. The server
      // renders it into the page and repeats it on every poll, so the poll can
      // notice a change without fetching the vocabulary itself once a second.
      // An empty value is a project that has recorded no statuses, which is a
      // real state and not a missing answer, so the comparison is against
      // whatever this page was served with rather than against emptiness.
      //
      // It moves: a head that arrives over a configuration this page is already
      // drawing is recorded here, so the next answer carrying it is not
      // compared all over again. See noteVocabularyChange.
      let renderedVocabularyHead = boardView.dataset.vocabularyHead || "";
      // And what that head stands for: the digest of the columns and the
      // priorities this page is drawing, which is what a change is actually
      // compared against. Empty from a build that states none, which reads as
      // "nothing here can be compared" — see noteVocabularyChange.
      const renderedVocabularyShape = boardView.dataset.vocabularyShape || "";
      // The ceiling on one attached file, as the server renders it into the
      // page. The upload control refuses a file over it before spending the
      // encode and the round trip on one the server would refuse anyway, and it
      // reads the number here rather than holding one, for the reason the
      // columns and the status tags are read out of the page: core owns this
      // ceiling, and a copy in this script would go on naming the old number the
      // day core's changed. A page served without it simply asks the server,
      // which is the answer that was always authoritative.
      const attachmentFileLimit = Number(boardView.dataset.attachmentLimit) || 0;
      // The ceiling on what one task's live file attachments add up to, read out
      // of the page for the same reason and refused in the same place. This is
      // the one a create form cannot do without: the task does not exist yet, so
      // a reader staging more than a task may hold has no server to be refused
      // by until after the task has been made.
      const attachmentTotalLimit = Number(boardView.dataset.attachmentTotalLimit) || 0;
      // The ceiling on a file attachment's name, in bytes. Bytes is the reason
      // it is read from here rather than counted here: a name is stored as
      // UTF-8, so an 85-character Japanese file name is 255 bytes' worth of
      // ceiling and 85 of anything a reader — or a naive `length` — would count.
      const attachmentNameLimit = Number(boardView.dataset.attachmentNameLimit) || 0;
      // The ceilings on a link's display text and its destination, in bytes and
      // read from the page for the same two reasons: core owns them, and a
      // staged link is refused by the server only after the task has been made.
      const attachmentLabelLimit = Number(boardView.dataset.attachmentLabelLimit) || 0;
      const attachmentURLLimit = Number(boardView.dataset.attachmentUrlLimit) || 0;
      // The media types the attachment route hands back inline, read out of the
      // page for exactly the reason the ceiling above is. A markdown attachment
      // reference becomes an <img> only for a type on this list; every other
      // attachment becomes a link to the same route, which serves it as a
      // download. A page served without the attribute renders no image at all,
      // which is the safe end of the failure.
      const inlineImageMedia = new Set((boardView.dataset.inlineMedia || "").split(/\s+/).filter(Boolean));
      // The identity this board records an assignment against, which is the
      // checkout's own `user.email` — the same one a commit made in this
      // worktree carries, and the same one `workbook update --assign self`
      // resolves. It is read from the page rather than asked for, because it is
      // fixed for the life of the server and the assignment form has to be able
      // to name it before anybody has typed anything.
      //
      // Empty is the whole gate, and the routes ask the same question, so a page
      // that draws no control is served by a board that would refuse the write.
      // A board built without either assignment mutation, or without an identity
      // to write them as, renders the attribute empty and this page draws no
      // assignment control at all — the read-only section this board shipped
      // with. The one node that differs is taskPanel's empty-state paragraph,
      // which the shared scaffolding always appends: on this path it is hidden,
      // never given text, and never unhidden.
      const assignIdentity = boardView.dataset.assignIdentity || "";
      // How long an assignment has stood, in the words `workbook show` prints:
      // "assigned just now", then minutes, hours and days. The rule is
      // presentation.AssignedAgo's, and it arrives here as the table that
      // function reads — each band's exclusive upper bound and unit in
      // milliseconds, and its phrases — rather than as a copy written into this
      // script, so the day the rule changes this page says the new thing.
      //
      // It is applied here rather than on the server because the phrase moves
      // with the clock and nothing else. Carried in the polled document, it
      // changed that document each time a fresh assignment's age crossed a
      // minute, and every open board read the whole task list again for a
      // phrase. The document carries the recorded time instead, which never
      // moves, and this page words it on its own tick.
      //
      // The clock is this page's, where it was the server's. For the board
      // `workbook serve` opens on this machine they are the same clock; for a
      // browser elsewhere the two can disagree by the few seconds clones'
      // clocks already disagree by, which moves a phrase's change by those few
      // seconds and no more. A time ahead of this clock reads as the first
      // band, "just now", exactly as it does in Go. A table that cannot be read words nothing, and a row then
      // shows when it was recorded and stops there. `{count}` is the table's
      // own marker for where a number goes (presentation.AgeCountPlaceholder),
      // part of its format rather than of the rule; assignment_age_test.go
      // words every boundary here and in Go and fails if the two part ways.
      const assignedAgoSteps = (() => {
        try {
          const steps = JSON.parse(boardView.dataset.assignedAgo || "[]");
          return Array.isArray(steps) ? steps : [];
        } catch (_) {
          return [];
        }
      })();
      function assignedAgo(createdAt, now) {
        const recorded = Date.parse(createdAt);
        if (Number.isNaN(recorded)) return "";
        const elapsed = now - recorded;
        for (const step of assignedAgoSteps) {
          if (step.below && elapsed >= step.below) continue;
          if (!step.unit) return step.one || "";
          const count = Math.floor(elapsed / step.unit);
          return count === 1 ? step.one || "" : String(step.many || "").split("{count}").join(String(count));
        }
        return "";
      }
      let vocabularyChanged = false;
      const syncToggle = document.querySelector("[data-sync-toggle]");
      const descriptionToggle = document.querySelector("[data-description-toggle]");
      const deletedToggle = document.querySelector("[data-deleted-toggle]");
      // The board's filter row, and the two controls in it that are not a
      // switch. The row is what a route reveals and hides, because every
      // control in it is a view of the columns; the Deleted switch inside it is
      // no longer hidden on its own.
      const filterRow = document.querySelector("[data-filter-row]");
      const filterSearch = document.querySelector("[data-filter-q]");
      const filterClear = document.querySelector("[data-filter-clear]");
      // The row's three choosers, collected once and held by the group each one
      // narrows: "priority", "label", "key". Each is a button, the badge on it
      // that counts what is ticked, and the menu the ticks are drawn into.
      //
      // `options` is this chooser's option nodes by value, and `builtFrom` the
      // option set they were built for. Both are here rather than rebuilt per
      // render because this runs every second: see renderChooserOptions.
      const filterChoosers = new Map([...document.querySelectorAll("[data-filter-chooser]")].map((root) => [root.dataset.filterChooser, {
        root,
        button: root.querySelector("[data-filter-chooser-button]"),
        badge: root.querySelector("[data-filter-chooser-badge]"),
        // The number inside the badge. It is a node of its own because the badge
        // also holds a word for screen readers, and writing the count into the
        // badge itself would throw that word away on every render.
        count: root.querySelector("[data-filter-chooser-count]"),
        menu: root.querySelector("[data-filter-chooser-menu]"),
        // The box that narrows this menu's rows, the line the menu says when it
        // has narrowed them all away, and the container the rows are drawn into.
        // Only the Labels menu is served a box and a line — it is the one chooser
        // whose list is as long as the project's cards make it — so both are null
        // on the other two, and every branch that touches them asks first. The
        // options container is served for all three, so the render has one path.
        search: root.querySelector("[data-filter-chooser-search]"),
        noMatch: root.querySelector("[data-filter-chooser-nomatch]"),
        optionsList: root.querySelector("[data-filter-chooser-options]"),
        options: new Map(),
        builtFrom: null,
      }]));
      // One menu open at a time, and a menu open only while the reader is in it.
      // `except` is the chooser the reader is acting inside, which is left alone:
      // a tick navigates, and the menu they are working through has to survive
      // the render that navigation causes.
      //
      // A menu that closes forgets what was typed into it. The box is menu state
      // rather than board state, so a reader who reopens the Labels menu is
      // offered the labels the board has and not the three a search they have
      // since forgotten left standing — and this is the one place every close
      // path goes through: the button, a click outside, Escape, and a route that
      // leaves the board.
      function closeChoosersExcept(except) {
        filterChoosers.forEach((chooser, group) => {
          if (chooser.root === except) return;
          if (chooser.menu) chooser.menu.hidden = true;
          if (chooser.button) chooser.button.setAttribute("aria-expanded", "false");
          if (chooser.search && chooser.search.value !== "") {
            chooser.search.value = "";
            applyChooserSearch(chooser, group);
          }
        });
      }
      function chooserIsOpen(chooser) {
        return Boolean(chooser.menu) && chooser.menu.hidden === false;
      }
      filterChoosers.forEach((chooser, group) => {
        if (!chooser.button || !chooser.menu) return;
        chooser.button.addEventListener("click", () => {
          const opening = chooser.menu.hidden;
          closeChoosersExcept(opening ? chooser.root : null);
          chooser.menu.hidden = !opening;
          chooser.button.setAttribute("aria-expanded", opening ? "true" : "false");
          // A menu with a box opens with the caret in it, so a reader who came
          // here to find one label out of forty types rather than scrolls. It is
          // focused after the menu is shown: a browser will not put the caret in
          // something that is not on screen yet.
          if (opening && chooser.search) chooser.search.focus();
        });
        if (!chooser.search) return;
        chooser.search.addEventListener("input", () => applyChooserSearch(chooser, group));
        chooser.search.addEventListener("keydown", (event) => {
          if (event.key !== "Escape") return;
          // Escape empties the box first and closes the menu second, which is the
          // order of what the reader is undoing: the narrowing they typed, and
          // then the menu they opened. The first press is claimed here and kept
          // from the document, which is where the second one is heard — the box is
          // empty by then, so this listener lets it through.
          if (chooser.search.value === "") return;
          event.stopPropagation();
          chooser.search.value = "";
          applyChooserSearch(chooser, group);
        });
      });
      const schemeToggle = document.querySelector("[data-scheme-toggle]");
      const schemeLabel = document.querySelector("[data-scheme-label]");
      // The words in front of each switch. They are written to instead of the
      // control itself, which now holds a track and a knob that writing its
      // text would throw away.
      const syncLabel = document.querySelector("[data-sync-label]");
      const descriptionLabel = document.querySelector("[data-description-label]");
      const deletedLabel = document.querySelector("[data-deleted-label]");
      // A switch answers Space as well as Enter, and the one that is an anchor
      // gets Enter from the browser and Space from nobody — the browser scrolls
      // the page with it. The click this raises is the click the anchor would
      // have raised, so it reaches the same document listener that turns every
      // link on this page into a render.
      function activateSwitchOnSpace(element) {
        if (!element) return;
        element.addEventListener("keydown", (event) => {
          if (event.key !== " " && event.key !== "Spacebar") return;
          event.preventDefault();
          element.click();
        });
      }
      activateSwitchOnSpace(deletedToggle);
      // Typing narrows the board by writing the address, which is what makes a
      // narrowed board a link. It replaces the entry rather than pushing one:
      // Back through nine keystrokes of "dependency" is not a history anybody
      // asked for, and the entry the reader came from is the one Back should
      // reach. The value is trimmed on the way out so that a trailing space
      // never becomes an address of its own, and the render the keystroke causes
      // leaves the control alone, so nothing rewrites what is being typed.
      //
      // That exemption is this flag rather than "the box has the caret", which
      // is the difference between a render caused by the typing and any other
      // render. A reader who presses Back over a search still has the caret in
      // the box, and a render that skipped the box there would leave the typed
      // text sitting over an address that no longer holds it — a board drawing
      // every card under a search box that says otherwise.
      let typingIntoSearch = false;
      if (filterSearch) {
        filterSearch.addEventListener("input", () => {
          const next = Object.assign({}, boardFilters, { q: filterSearch.value.trim() });
          typingIntoSearch = true;
          try {
            navigate(boardAddress(deletedColumnShown, next), { replace: true });
          } finally {
            typingIntoSearch = false;
          }
        });
      }
      // Whether the board is drawing its Deleted column, and the parts of that
      // column while it is. The column is board state rather than route state:
      // a reader who opens a task from a board showing it comes back to a board
      // showing it, and the address they came from is what Back restores.
      //
      // It stays set while they are on that task's page, and the poll goes on
      // asking for the deleted tasks the whole time. That is deliberate: the
      // board is still in the document behind the form, and a poll that stopped
      // asking would empty the column and leave it empty until a second after
      // they came back. What it costs is the tombstones in every poll of a route
      // that draws no columns, which the relationship picker on that very page
      // is reading anyway.
      //
      // deletedRead says whether the model in hand came from a poll that asked
      // for the deleted tasks. Until one has, the column knows nothing rather
      // than nothing-to-know, so it says neither — an empty state written over
      // a column that is about to fill is a wrong answer, not an early one.
      let deletedColumnShown = false;
      let deletedRead = false;
      let deletedSection = null;
      let deletedList = null;
      let deletedCount = null;
      let deletedEmpty = null;
      let syncState = null;
      let activeDrag = null;
      // Where the cursor is during a drag, and which column's scroller it is
      // over. dragover is the only report of a cursor position HTML5 drag and
      // drop offers, and it stops arriving the moment the cursor holds still,
      // so a column scrolled from dragover alone would stop under a reader who
      // has pushed into its bottom edge and is waiting for it to come to them.
      // The position is remembered here instead and a frame loop reads it, so
      // holding still at an edge keeps scrolling exactly as it does everywhere
      // else a drag scrolls something.
      //
      // Both coordinates are kept, because the same held cursor drives two
      // scrollers: the column's own list down its cards, and the board track
      // sideways toward the columns the window is too narrow to show. Near a
      // corner the reader is asking for both at once.
      let dragScroller = null;
      let dragPointerY = 0;
      let dragPointerX = 0;
      // The frame the loop has asked for, and the timestamp of the one before
      // it. Both are null while no loop is running, which is what makes
      // starting it idempotent and stopping it verifiable.
      let dragScrollFrame = null;
      let dragScrollLast = null;
      // The travel a frame asked for and the scroller did not keep.
      //
      // A browser rounds an assigned scroll offset to the nearest whole pixel —
      // Chrome reads 1.25 back as 1, and 1.75 back as 2 — which quantizes every
      // frame's step to a whole pixel and so bends the rate the reader gets
      // away from the rate the ramp names, downward or upward depending on
      // where the fraction falls. Below half a pixel a frame it rounds to
      // nothing at all, which at 60Hz is the innermost couple of pixels of the
      // zone: a strip that looks like part of the ramp and does nothing.
      // Whatever the scroller would not take is held here and added to the next
      // frame, so the ramp is the rate on a browser that rounds and on one that
      // does not.
      // Each scroller keeps its own, because two scrollers rounding
      // independently would otherwise trade each other's lost fractions and
      // neither would deliver the rate its ramp named.
      let dragScrollCarry = 0;
      let boardScrollCarry = 0;
      const copyFeedback = new Map();
      const copyFeedbackTimers = new Map();
      let copySuppressionTimer = null;
      let copySuppressedTaskID = null;
      let latestTasks = [];
      let latestPresentation = new Map();
      const pendingIntents = new Map();
      // What a refused board change has to say, keyed by the task it concerns.
      //
      // It lives out here for the same reason a pending intent does: the model
      // is replaced wholesale by every poll, so a report written into a task
      // would be erased a second later. Kept here and folded in at render time,
      // it outlives every poll and leaves only when the reader dismisses it or
      // the board stops carrying the card it belongs to.
      //
      // One report per card, newest wins, and the reader's next change to that
      // card takes it away: a refused intent holds nothing — it rolled back,
      // and the card shows what the server holds — so a second one is the same
      // news about the same card, and changing the card again is the answer to
      // the news. A refused create's report stacks instead, because each one
      // can hold the only copy of what was typed.
      //
      // Each entry holds two wordings of the same event: `card`, which speaks
      // about the card it sits on, and `notice`, which has to name the task
      // because it is read after the board has stopped drawing it.
      const taskFailureReports = new Map();
      let tasksLoaded = false;
      let taskLoadFailed = false;
      let latestDeletedTasks = [];
      // What the board may tell the server it already holds, so an unchanged
      // poll is answered 304 with no body instead of the whole document again.
      //
      // One for each model rather than one for each address. The board model is
      // read from two addresses — with and without the deleted tasks — and a
      // 304 says "you hold what you asked about", so a tag remembered for the
      // other address would vouch for a model that was not read from it. So the
      // board's validator holds the address its document was read from beside
      // the tag, and the tag is sent only to that address.
      //
      // A tag is recorded only where its document becomes the model, and
      // dropped wherever the model is changed by anything else: from then on the
      // model is not the document the tag names, and the next read is a full one.
      let boardValidator = null;
      let deletedTasksTag = null;
      let activeDependencyController = null;
      let taskRefreshGeneration = 0;
      let relationshipRefreshGeneration = 0;
      let currentTaskRefresh = null;
      let pendingDetailRelationshipContextTaskID = null;
      const pendingRelationshipDrafts = new Map();
      // The assignments a create staged and could not save, by the task it
      // made, for that task's page to draw and retry. The counterpart of the
      // relationship drafts above.
      const pendingAssignmentDrafts = new Map();
      const pendingTaskMessages = new Map();
      let createdTaskRecovery = null;
      // What a New Task form is still holding for a task it already made: the
      // attachments its create could not attach, named rather than carried.
      //
      // The files themselves cannot be published here. A staged file is a File
      // the browser handed one node, and nothing outside that node can hold it
      // usefully — which is exactly why this exists: the form is the only place
      // those bytes are, so a render that replaces it has to say so on the way
      // past rather than let them go without a word.
      let outstandingAttachmentReport = null;
      let pendingRelationshipRecoveryFocus = null;
      // The New Task form's "Create more" preference. It lives out here because
      // a save rebuilds the route it is drawn on: state held by the form would
      // be discarded by the very save it is supposed to describe.
      let createMoreTasks = false;
      // Staged by a save that re-arms the New Task form, consumed by the render
      // that re-arms it.
      let pendingNewTaskFocus = false;
      // The same staging for a save that lands on the board: the ID of the card
      // that should hold the caret once the board is back on screen.
      let pendingBoardFocusTaskID = null;
      // Creates the board is drawing before the server has accepted them, keyed
      // by the provisional ID their card is drawn under.
      const pendingCreations = new Map();
      let creationSequence = 0;
      // The stand-in that was retired while it held the caret, and the card that
      // replaced it. Written by the retirement and consumed by the render that
      // drops the node, which is the only pair of moments that know both halves.
      let retiredCreationHeir = null;
      // What a New Task form should open holding: the values of a refused
      // create, and the reason it was refused.
      let pendingNewTaskDraft = null;
      let pendingNewTaskMessage = "";
      // Reads the values the New Task form on screen is holding, or null when
      // the route on screen is not one. Set by the render that builds the form,
      // because a restore replaces that node and has to lift what was typed
      // into it out first.
      let readOpenNewTaskValues = null;
      // The detail form on screen, or null when the route on screen is not one.
      // Set by the render that builds the form and retired by renderRoute, so a
      // late arrival — a board intent refused seconds after the form opened —
      // corrects the form the reader is looking at or nothing at all, and never
      // a node that has already left the document.
      let openDetailForm = null;
      // The comment thread and attachment list on the open task page, retired by
      // renderRoute beside the form they sit under. They follow the poll in
      // place: this route is not re-rendered while the reader stands in it, so a
      // comment a teammate wrote would otherwise not arrive until the reader
      // left the page and came back.
      let openThreadPanels = null;

