      // The board's filters, read from and written to the address. Values are
      // repeated parameters rather than a joined list because a label may hold
      // any character, a comma included. Written in one fixed order with each
      // group sorted, so one filter state has one address.
      //
      // The text search is the predicate core's MatchesFind is, and the one
      // `list --find` narrows by: every whitespace-separated term has to appear
      // somewhere in the title or the description, case folded, or name the start
      // of the task's ID. It is kept between the two markers below and depends on
      // nothing else on this page — the keys it needs are passed in — so a parity
      // test can lift it out and ask it what core is asked.
      //
      // The markers are strings rather than comments because the parity test asks
      // the page the handler served, the way every other test here does, and
      // html/template strips the comments out of that page: a marker that existed
      // only in this file would be one the test could not find.
      "find:begin";
      // Folded one code point at a time, because Go folds one rune at a time.
      // JavaScript's toLowerCase reads the text around a letter and Go's
      // strings.ToLower cannot: a browser answers "ΟΔΟΣ".toLowerCase() with
      // "οδος", applying the Greek final-sigma rule, while Go answers "οδοσ", so
      // a reader searching a board for "οδοσ" found nothing the CLI found. Taking
      // each code point on its own takes that context away, which is the only
      // thing the two matchers disagreed about here.
      //
      // The one pair they still cannot agree on is "İ" (U+0130). JavaScript's
      // special casing lowercases it to "i" followed by a combining dot above and
      // Go's simple case mapping answers a bare "i", and reconciling that would
      // mean shipping a case-mapping table to the browser. The parity test names
      // the pair and asserts it still diverges, so whoever changes either side is
      // told about it.
      //
      // An all-ASCII string folds the same both ways, and the whole-string call is
      // the one the engine has a fast path for, so it takes it: no ASCII code
      // point has a special or a context-sensitive lowercase mapping — final sigma
      // needs a Greek sigma and the dotted capital I is not ASCII — so with
      // nothing outside ASCII in the string there is no context left to read.
      // Measured on a 2 KB description: 29.3 us per code point against 0.7 us
      // this way, which is what makes a searched board of long descriptions cost
      // a fraction of a frame rather than several of them.
      const nonAsciiFind = /[^\x00-\x7F]/;
      function foldFindCase(text) {
        if (!nonAsciiFind.test(text)) return text.toLowerCase();
        return [...text].map((character) => character.toLowerCase()).join("");
      }
      // The text a task is searched over: its title and its description, joined
      // with a newline so a term cannot straddle the two, folded once.
      function foldTaskHaystack(task) {
        return foldFindCase(String(task.title || "") + "\n" + String(task.description || ""));
      }
      // The characters Go's unicode.IsSpace accepts, which is what strings.Fields
      // splits a query on — and only those. JavaScript's \s is a different set in
      // both directions: it splits on U+FEFF, which Go does not call whitespace,
      // and it does not split on U+0085, which Go does. Either difference turns
      // one query into two terms on one side and one term on the other.
      const findSpace = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/;
      function findTerms(query) {
        return foldFindCase(String(query || "")).split(findSpace).filter(Boolean);
      }
      // Whether a term names the start of a task ID under one of these keys: the
      // key, a dash, and at least one more character, answered upper-cased
      // because that is the case an ID is written in. core.FindKeyPrefixTerm is
      // the same function and the parity test holds the two together.
      //
      // "WB" and "WB-" are words rather than prefixes, and an ID is matched only
      // from its beginning — a word that happens to occur inside a ULID must
      // never drag in a card nobody was looking for. findTerms has already
      // lower-cased the term; upper-casing it again here is what makes a pasted
      // ID work whatever case it arrived in.
      // The term is checked before it is upper-cased, and only ASCII letters,
      // digits and dashes pass. Upper case is not a per-character operation and
      // the two languages expand differently: "ß".toUpperCase() here is "SS" and
      // Go's strings.ToUpper leaves the letter alone, so "wb-01m2ßkb" named the ID
      // WB-01M2SSKB… on this board and named nothing to `list --find`. Refusing
      // the term rather than folding it is the fix that needs no case table in the
      // browser — with nothing outside ASCII left there is nothing the two can
      // fold differently. A term that is refused here still has the whole text
      // route open to it.
      //
      // Both loops walk code points against the letters and digits they accept,
      // the way core.FindKeyPrefixTerm does, rather than testing a pattern: a
      // pattern here would be a second spelling of a rule that lives in Go, and
      // the page deliberately spells none of those. Neither is the grammar of a
      // key's name — that one the page never reads at all, and the server answers.
      function findKeyPrefixTerm(term, keys) {
        const text = String(term);
        for (const character of text) {
          const upperLetter = character >= "A" && character <= "Z";
          const lowerLetter = character >= "a" && character <= "z";
          const digit = character >= "0" && character <= "9";
          if (!upperLetter && !lowerLetter && !digit && character !== "-") return null;
        }
        const upper = text.toUpperCase();
        for (const key of keys) {
          if (upper.length <= key.length + 1 || !upper.startsWith(key + "-")) continue;
          // Only a second dash can fail here, now that the term is ASCII; the
          // check is written as the alphabet it is so that it stays true of the
          // rule rather than of what the validation above happens to leave.
          let alphabet = true;
          for (const character of upper.slice(key.length + 1)) {
            const letter = character >= "A" && character <= "Z";
            const digit = character >= "0" && character <= "9";
            if (!letter && !digit) {
              alphabet = false;
              break;
            }
          }
          if (alphabet) return upper;
        }
        return null;
      }
      // The predicate over work already done: a haystack this task's text has
      // already been folded into, and terms the query has already been split into.
      // An empty term list matches, which is what makes an empty query cost
      // nothing — no caller has to fold a haystack to be told it matches.
      //
      // The ID is taken alongside the haystack rather than folded into it, since
      // the two are matched by different rules: the text anywhere, the ID only
      // from its start.
      function matchesFoldedFind(foldedHaystack, terms, id, keys) {
        if (terms.length === 0) return true;
        const names = keys || [];
        return terms.every((term) => {
          if (foldedHaystack.includes(term)) return true;
          const prefix = findKeyPrefixTerm(term, names);
          return prefix !== null && String(id || "").startsWith(prefix);
        });
      }
      // The whole predicate, folding for itself. This is the one the parity test
      // asks and the one every caller outside a render uses; a render that is
      // about to ask the same task about twenty-five options folds once and goes
      // through matchesFoldedFind instead, which is the same answer arrived at
      // without doing the same work twenty-five times.
      function matchesFind(task, query, keys) {
        const terms = findTerms(query);
        if (terms.length === 0) return true;
        return matchesFoldedFind(foldTaskHaystack(task), terms, task.id, keys);
      }
      "find:end";
      function emptyFilters() {
        return { q: "", priority: new Set(), label: new Set(), key: new Set() };
      }
      function filtersFromURL(url) {
        const params = url.searchParams;
        return {
          q: (params.get("q") || "").trim(),
          priority: new Set(params.getAll("priority").filter(Boolean)),
          label: new Set(params.getAll("label").filter(Boolean)),
          // Upper-cased, because a key is upper-case everywhere it is written:
          // in a task's ID, in the chooser's own options, and in `workbook key`.
          // ?key=wb is a reader typing an address by hand, and answering it with
          // an empty board would be answering a question they did not ask.
          key: new Set(params.getAll("key").filter(Boolean).map((value) => value.toUpperCase())),
        };
      }
      // The board's whole address, built from nothing but the column setting and
      // the filters. It drops the fragment and every parameter these two do not
      // own, which is deliberate: this is the one function that says what a board
      // address is, and a landing that carried an unrecognized parameter through
      // would be promising to honor something no control here reads. Nothing on
      // `/` uses a fragment or any other parameter today, so nothing is being
      // taken away; a future one has to be named here to survive.
      function boardAddress(deleted, filters) {
        const params = new URLSearchParams();
        if (deleted) params.set("deleted", "1");
        if (filters.q) params.set("q", filters.q);
        [...filters.priority].sort().forEach((value) => params.append("priority", value));
        [...filters.label].sort().forEach((value) => params.append("label", value));
        [...filters.key].sort().forEach((value) => params.append("key", value));
        const query = params.toString();
        return "/" + (query ? "?" + query : "");
      }
      function filtersActive(filters) {
        return Boolean(filters.q) || filters.priority.size > 0 || filters.label.size > 0 || filters.key.size > 0;
      }
      // Whether two readings of the address are the same filter state.
      //
      // A navigation that changed only the filters is the same route at the same
      // pathname, and nothing else in the board's render would notice it: the
      // columns are re-attached rather than rebuilt, and the Deleted column's
      // render runs only when that column appears or goes. So this is what tells
      // the route there is anything to redraw.
      function sameFilterValues(left, right) {
        if (left.size !== right.size) return false;
        for (const value of left) {
          if (!right.has(value)) return false;
        }
        return true;
      }
      function sameFilters(left, right) {
        return left.q === right.q &&
          sameFilterValues(left.priority, right.priority) &&
          sameFilterValues(left.label, right.label) &&
          sameFilterValues(left.key, right.key);
      }
      // The key a task's ID was minted under: the part before the first dash. It
      // is read off the ID rather than asked of the server for the reason the
      // search is computed here — the board already holds everything the filters
      // need, and a filter that waited on a round trip would lag the typing.
      // Upper-cased for the reason the key filter is: the two are compared, so
      // they have to be folded the same way or the folding is only half done.
      function taskKey(task) {
        const id = String(task.id || "");
        const dash = id.indexOf("-");
        return dash > 0 ? id.slice(0, dash).toUpperCase() : "";
      }
      // Every key this project has ever minted under, which is the list the
      // search compares an ID-prefix term against. All states count: a retired
      // key mints nothing, but everything it ever minted is still on the board,
      // so a pasted ID under one still has to find its card.
      function searchKeyNames() {
        return publishedKeys.map((entry) => entry.key);
      }
      function passesFilters(task, filters, search) {
        if (filters.priority.size > 0 && !filters.priority.has(task.priority)) return false;
        if (filters.label.size > 0 && !(task.labels || []).some((label) => filters.label.has(label))) return false;
        if (filters.key.size > 0 && !filters.key.has(taskKey(task))) return false;
        // Handed a render's folded text, the search is a substring test over work
        // already done. The query it was folded for is carried with it and checked
        // here rather than assumed: every caller that passes one is asking about
        // the same query it was built from — withOnly replaces one group and never
        // the search — and a caller that ever is not gets the slow, correct answer
        // instead of a wrong fast one.
        if (search && search.folded && search.query === filters.q) {
          const folded = search.folded.get(task.id);
          if (folded !== undefined) return matchesFoldedFind(folded, search.terms, task.id, search.keys);
        }
        // The render's own list where there is one, since this path is the only
        // one an unsearched board takes and it is taken once per task per option
        // per poll — building a fresh array of key names for each of those asks
        // is work nobody reads. foldedSearch carries the list even when it folds
        // nothing, which is exactly the empty-query case.
        return matchesFind(task, filters.q, (search && search.keys) || searchKeyNames());
      }
      // Everything one render needs to answer the search with: the query, its
      // terms split once, the project's keys read once, and each task's text
      // folded once.
      //
      // It exists because of what the same render used to cost. passesFilters is
      // asked about every task once for the columns and once more for every option
      // in every chooser, and each of those asks used to fold that task's whole
      // description again — 29.3 us of it on a 2 KB description, and the counts
      // are recomputed on every poll. Folding once per task per render and handing
      // the result to both halves took a 500-task board of 2 KB descriptions with
      // a search standing from 37.4 ms a second to 0.7 ms.
      //
      // An empty query folds nothing at all: matchesFoldedFind answers an empty
      // term list before it looks at a haystack, so an unsearched board — which is
      // most boards, most of the time — pays none of this.
      function foldedSearch(tasks) {
        const query = boardFilters.q;
        const terms = findTerms(query);
        const keys = searchKeyNames();
        if (terms.length === 0) return { query, terms, keys, folded: null };
        return { query, terms, keys, folded: new Map(tasks.map((task) => [task.id, foldTaskHaystack(task)])) };
      }
      let boardFilters = emptyFilters();
      function route(url = new URL(window.location.href)) {
        // The board with its Deleted column shown is the board at another
        // address rather than a page of its own, which is what makes the row's
        // toggle an ordinary link: it is shareable, it is bookmarkable, and Back
        // and Forward walk it like any other navigation. Every filter beside it
        // in that row is read from the same address for exactly that reason.
        if (url.pathname === "/") {
          return { view: "board", deleted: url.searchParams.get("deleted") === "1", filters: filtersFromURL(url) };
        }
        // A board built without the four vocabulary mutations was served none of
        // the configuration page, so this address is not a route on it. The
        // server answers it with a 404 for the same reason; a client that drew
        // the route anyway would be offering controls the page has no markup
        // for. The address used to be /statuses and nothing forwards it: it is
        // an unknown path here exactly as it is on the server.
        if (url.pathname === "/config") {
          return vocabularyPanel ? { view: "config" } : { view: "unknown" };
        }
        // The relic pages are routes only on a board that was served their
        // view, which is a board that can read relics; on any other the
        // addresses are unknown here exactly as they are on the server.
        if (url.pathname === "/relics" || url.pathname.startsWith("/relics/")) {
          return relicView ? relicRoute(url) : { view: "unknown" };
        }
        if (url.pathname === "/tasks/new") {
          const requestedStatus = url.searchParams.get("status");
          return { view: "new", status: statuses.has(requestedStatus) ? requestedStatus : defaultStatus };
        }
        const match = url.pathname.match(/^\/tasks\/([^/]+)$/);
        if (!match) return { view: "unknown" };
        try { return { view: "detail", id: decodeURIComponent(match[1]) }; }
        catch (_) { return { view: "unknown" }; }
      }
      function taskPath(taskID) {
        return "/tasks/" + encodeURIComponent(taskID);
      }
      function newTaskPath(status) {
        return statuses.has(status)
          ? "/tasks/new?status=" + encodeURIComponent(status)
          : "/tasks/new";
      }
      // replace is for a destination that supersedes the entry it leaves rather
      // than following it: re-arming the New Task form is the same route with a
      // spent form behind it, and pushing those would make Back walk backwards
      // through every task the user just created.
      function navigate(path, { replace = false } = {}) {
        if (replace) history.replaceState({}, "", path);
        else history.pushState({}, "", path);
        renderRoute();
      }
