      // The Deleted column, built here rather than served because it is the one
      // column that is not a status: the server renders this project's
      // vocabulary, and this is a view of the tasks the vocabulary no longer
      // holds. It is a real column otherwise — a section.column in the board's
      // grid, taking the same track the others take — so it widens, scrolls and
      // wraps with them and has no sizing of its own to keep in step.
      function buildDeletedColumn() {
        const section = document.createElement("section");
        section.className = "column column--deleted";
        section.dataset.deletedColumn = "";
        section.setAttribute("aria-label", "Deleted tasks");
        const header = document.createElement("header"); header.className = "column__header";
        const heading = document.createElement("h2"); heading.className = "column__heading";
        // The same shape the server gives a status column's heading: the name as
        // the heading's own text, then the count. The space at the end of the
        // name is what the server writes between the two, and it is what keeps
        // the heading's accessible name "Deleted 7" rather than "Deleted7" — a
        // trailing space at the end of a line box draws nothing, so the two
        // headings read alike and look alike.
        text(heading, "Deleted ");
        const count = document.createElement("span"); count.className = "count"; count.dataset.deletedCount = ""; text(count, "0");
        heading.append(count);
        header.append(heading);
        const list = document.createElement("div");
        list.className = "task-list";
        list.dataset.deletedList = "";
        // The column takes drops and names no status, which is why it carries a
        // marker of its own rather than a data-drop-status nothing could hold:
        // a project may name a status anything, so there is no sentinel value
        // that could not also be a column.
        list.dataset.dropDeleted = "true";
        const empty = document.createElement("p");
        empty.className = "column__empty";
        empty.dataset.deletedEmpty = "";
        empty.hidden = true;
        text(empty, "No deleted tasks.");
        // First child, ahead of every card, so the renderer's insertions never
        // have to reckon with it: reconcile() places cards by index among the
        // nodes that carry a task ID, and this one carries none.
        list.append(empty);
        section.append(header, list);
        return { section, list, count, empty };
      }
      // Shows or hides the column, and does nothing else to the board.
      //
      // That is the whole contract. Every other column and every card node is
      // exactly the node it was on both sides of a toggle, because a card node
      // is where the reader's work in flight lives — an open form, a change
      // staged against a head, a refusal they have not read. A toggle that
      // rebuilt the board to add one column would destroy all of it, which is
      // the same reason a vocabulary change offers a reload instead of taking
      // one.
      function showDeletedColumn(shown) {
        if (shown === deletedColumnShown) {
          renderDeletedToggle();
          return;
        }
        deletedColumnShown = shown;
        // What is known about the deleted tasks was read under the other
        // setting, so it is not an answer to this one until a poll asks again.
        deletedRead = false;
        // And that poll asks in full. Switching back before any poll lands
        // would otherwise find the tag for this address still held, and a 304
        // redraws nothing, so the column would go on withholding what it has
        // to say about a model that was in fact read for it.
        boardValidator = null;
        if (shown) {
          const built = buildDeletedColumn();
          deletedSection = built.section;
          deletedList = built.list;
          deletedCount = built.count;
          deletedEmpty = built.empty;
          board.append(deletedSection);
        } else {
          deletedSection.remove();
          deletedSection = null;
          deletedList = null;
          deletedCount = null;
          deletedEmpty = null;
        }
        renderDeletedToggle();
        // The model still holds whatever the last poll read. Drawing it now
        // fills a column that was just shown with the deleted tasks a queued
        // intent has already put there, and empties nothing that was hidden:
        // a task the board is no longer drawing is left in the model until the
        // next poll replaces it.
        rerender();
        // Asked for immediately rather than on the next tick, because a column
        // that takes a second to fill reads as a column with nothing in it. Not
        // before the first load, where the poll the page starts with is already
        // going to ask in this mode and a second read would only race it.
        if (shown && tasksLoaded) refresh();
      }
      function renderDeletedToggle() {
        if (!deletedToggle) return;
        // The column's own state is the only thing this switch changes, so its
        // address carries whatever else the row is filtering by: turning the
        // column off over a searched board leaves the search alone.
        deletedToggle.href = boardAddress(!deletedColumnShown, boardFilters);
        deletedToggle.setAttribute("aria-checked", deletedColumnShown ? "true" : "false");
        text(deletedLabel, deletedColumnShown ? "Hide Deleted" : "Show Deleted");
        deletedToggle.title = deletedColumnShown
          ? "The board is drawing its Deleted column. Click to hide it."
          : "The board is drawing no Deleted column. Click to show it.";
      }
      // The row's own controls, drawn from the filters the address is holding.
      //
      // Clear keeps the Deleted column: it is the one control in this row whose
      // state is a column rather than a narrowing, and a reader who cleared a
      // search should not also lose the tombstones they were reading.
      function renderFilterRow(deleted) {
        if (!filterRow) return;
        // Never written over by the render the typing itself caused. The address
        // is behind the typing by a keystroke at most, and a render that assigned
        // this value would move the caret to the end of whatever it assigned.
        // Every other render does write it, which is what makes Back and Forward
        // correct the box rather than leave a stale term standing over a board
        // that is no longer narrowed by it.
        if (filterSearch && filterSearch.value !== boardFilters.q && !typingIntoSearch) {
          filterSearch.value = boardFilters.q;
        }
        if (filterClear) {
          filterClear.hidden = !filtersActive(boardFilters);
          filterClear.href = boardAddress(deleted, emptyFilters());
        }
      }
      // The filters with one group replaced by one value, which is the state an
      // option's count is a count of: "how many cards would be left if this were
      // the only thing ticked in this group, with every other filter as it is".
      // That is what makes the numbers in one menu add up to something a reader
      // can act on — a count computed against no other filter would promise a
      // board the search is about to take away again.
      function withOnly(filters, group, value) {
        const narrowed = Object.assign({}, filters);
        narrowed[group] = new Set([value]);
        return narrowed;
      }
      // What one chooser lists: everything the group offers, plus anything this
      // group already has ticked. Every group answers from the project rather than
      // from the board except the labels, which are not a vocabulary: nobody
      // declares a label, so the only list there is the one the cards carry.
      //
      // The union is what keeps a ticked value reachable. What a group offers
      // moves as the board does — a label is offered only while a card the board
      // is drawing carries it, so hiding the Deleted column takes away every label
      // that only a tombstone had — and an option that left the menu used to take
      // its row with it while leaving the filter in the address: the badge went on
      // saying "1 selected", the columns went on being empty, and there was no row
      // left to untick. Listing what is ticked as well as what is offered also
      // gives a token the project has never heard of a row that names it, so
      // /?priority=bogus or an address shared before a rename says which word
      // emptied the board instead of presenting an empty board and no reason.
      function chooserOptions(group, drawn) {
        const chosen = boardFilters[group];
        if (group === "priority") {
          // In vocabulary order, which is the order the board sorts by and the
          // order the create form offers. Sorting them here would be a second
          // opinion about a sequence the project has already stated.
          const offered = priorityDefinitions.map(([token, label]) => ({ value: token, name: label }));
          return offered.concat(tickedOptionsBeyond(offered, chosen));
        }
        if (group === "key") {
          // Retired keys included, and said to be retired. Their tasks exist and
          // are on the board, so a chooser that listed only the keys that can
          // mint would be a chooser that cannot reach half the cards.
          const offered = publishedKeys.map((entry) => ({
            value: entry.key,
            name: entry.key + (entry.state === "retired" ? " (retired)" : ""),
          }));
          return offered.concat(tickedOptionsBeyond(offered, chosen));
        }
        const present = new Set();
        drawn.forEach((task) => (task.labels || []).forEach((label) => present.add(label)));
        // Sorted in with the rest rather than appended, because a label has no
        // order of its own for a stray one to be kept out of.
        chosen.forEach((value) => present.add(value));
        return [...present].sort().map((value) => ({ value, name: value }));
      }
      // A ticked value the group does not offer, named as the address spells it.
      // There is no display name to give it — that is exactly what the project
      // does not have for this token — and the token is the one thing that tells
      // the reader what they are looking at. Appended after the offered options
      // and sorted among themselves, so the order the project stated stays the
      // order the reader sees at the top of the menu.
      function tickedOptionsBeyond(offered, chosen) {
        const known = new Set(offered.map((option) => option.value));
        return [...chosen]
          .filter((value) => !known.has(value))
          .sort()
          .map((value) => ({ value, name: value }));
      }
      // What a menu says when the group has nothing to list. A blank menu reads
      // as a menu that failed to draw; this says which of the two it is.
      function chooserEmptyLine(group) {
        if (group === "priority") return "No priorities";
        if (group === "key") return "No keys";
        return "No labels";
      }
      function buildChooserOption(chooser, group, option) {
        const label = document.createElement("label");
        label.className = "filter-option";
        const input = document.createElement("input");
        input.type = "checkbox";
        input.dataset.filterOption = "";
        input.value = option.value;
        const name = document.createElement("span");
        text(name, option.name);
        const count = document.createElement("span");
        count.className = "count";
        // A tick is a navigation, pushed rather than replaced: it is a board the
        // reader chose, the way the Deleted switch is, and Back should undo it.
        // The ticks in one group are alternatives — a card matching any of them
        // passes — so this adds to or removes from that group's set and leaves
        // every other group alone.
        input.addEventListener("change", () => {
          const next = Object.assign({}, boardFilters);
          const chosen = new Set(boardFilters[group]);
          if (input.checked) chosen.add(option.value);
          else chosen.delete(option.value);
          next[group] = chosen;
          navigate(boardAddress(deletedColumnShown, next));
        });
        label.append(input, name, count);
        // The name is kept as well as the box and the count, because the box at
        // the top of a menu narrows by what a row says: see applyChooserSearch.
        chooser.options.set(option.value, { label, input, name, count });
        return label;
      }
      // Narrow a menu's rows to the ones whose name contains what the reader
      // typed into it. Menu state, not board state: nothing here touches the
      // address, and closing the menu clears it. A ticked row is always shown,
      // so a filter the reader set can always be unset from where it was set.
      function applyChooserSearch(chooser, group) {
        if (!chooser.search) return;
        const needle = foldFindCase(chooser.search.value.trim());
        let shown = 0;
        chooser.options.forEach((parts, value) => {
          const ticked = boardFilters[group].has(value);
          const matches = needle === "" || foldFindCase(parts.name.textContent).includes(needle);
          const visible = ticked || matches;
          if (parts.label.hidden !== !visible) parts.label.hidden = !visible;
          if (visible) shown += 1;
        });
        if (chooser.noMatch) {
          const none = needle !== "" && shown === 0 && chooser.options.size > 0;
          if (chooser.noMatch.hidden !== !none) chooser.noMatch.hidden = !none;
        }
      }
      // One chooser's options, drawn in place.
      //
      // The nodes are keyed by value and reused, and only a changed option set
      // costs new ones. This runs on every poll: a menu that replaced its
      // children every second would take the caret out from under a reader on
      // their way to a tick, once a second, for as long as they left it open.
      // A count that has changed is written; a count that has not is left alone,
      // for the reason every other count on this board is.
      //
      // The rows go into the options container rather than into the menu, because
      // the Labels menu holds two things the render must not throw away: the box
      // the reader is typing into and the line that says it has matched nothing.
      function renderChooserOptions(chooser, group, options) {
        const list = chooser.optionsList;
        if (!list) return;
        const wanted = options.map((option) => option.value).join("\n");
        if (chooser.builtFrom !== wanted) {
          chooser.builtFrom = wanted;
          chooser.options = new Map();
          if (options.length === 0) {
            const empty = document.createElement("p");
            empty.className = "filter-option filter-option--none";
            text(empty, chooserEmptyLine(group));
            list.replaceChildren(empty);
          } else {
            list.replaceChildren(...options.map((option) => buildChooserOption(chooser, group, option)));
          }
        }
        options.forEach((option) => {
          const parts = chooser.options.get(option.value);
          if (!parts) return;
          const ticked = boardFilters[group].has(option.value);
          if (parts.input.checked !== ticked) parts.input.checked = ticked;
          const reading = String(option.count);
          if (parts.count.textContent !== reading) text(parts.count, reading);
          // An option that would leave nothing is drawn as one and stays
          // tickable: ticking it answers "none of them", which is the honest
          // answer, and the columns say "No matches." rather than the reader
          // wondering whether the control worked.
          parts.label.classList.toggle("filter-option--none", option.count === 0 && !ticked);
        });
        // Applied after the loop rather than when the reader types, so a poll that
        // adds or drops a label keeps the narrowing: the rows may be new nodes,
        // and what the box says about them is the same either way.
        applyChooserSearch(chooser, group);
      }
      // The choosers, drawn from the board they are narrowing.
      //
      // `allTasks` is everything the board holds rather than what it is drawing,
      // because every count here is a count of what a tick would leave — a
      // question about cards the filters are currently hiding as much as about
      // the ones they are not.
      function renderChoosers(allTasks, search) {
        // The tombstones count only while the column that draws them is shown,
        // exactly as that column's own total does. With it hidden they are in the
        // model only because the poll that read it asked for them.
        const drawn = allTasks.filter((task) => !task.deleted || Boolean(deletedList));
        filterChoosers.forEach((chooser, group) => {
          const chosen = boardFilters[group];
          // A project with one key has nothing to choose between, so the chooser
          // is not offered at all. Retired keys count towards the two: their
          // tasks are on the board and the chooser is how a reader reaches them.
          //
          // Unless a key is ticked. ?key=ZZ on a one-key project is an address
          // someone can reach — a typo, or a link from a project that has since
          // dropped a key — and hiding the chooser there would hide the only row
          // that says what is emptying the board, which is the defect the union in
          // chooserOptions exists to prevent. The chooser comes back the moment
          // the reader unticks it.
          if (group === "key") chooser.root.hidden = publishedKeys.length < 2 && boardFilters.key.size === 0;
          const options = chooserOptions(group, drawn).map((option) => Object.assign({}, option, {
            count: drawn.filter((task) => passesFilters(task, withOnly(boardFilters, group, option.value), search)).length,
          }));
          renderChooserOptions(chooser, group, options);
          if (chooser.badge) chooser.badge.hidden = chosen.size === 0;
          if (chooser.count) {
            // Written only when it has changed, the way every other count on this
            // board is: this runs every second, and a node rewritten with the
            // number it already held is a mutation a screen reader may announce.
            const reading = String(chosen.size);
            if (chooser.count.textContent !== reading) text(chooser.count, reading);
          }
          chooser.root.classList.toggle("filter-chooser--active", chosen.size > 0);
        });
      }
