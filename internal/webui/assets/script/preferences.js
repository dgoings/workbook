      // Text drawn into the page, without its direction controls.
      //
      // Nearly everything this board draws is text somebody else typed — a
      // title, a label, a comment, an author, a file or link name — and U+202E
      // RIGHT-TO-LEFT OVERRIDE and the other bidi controls (the embeddings,
      // isolates and directional marks) redraw whatever follows them in its
      // paragraph: "invoice<U+202E>fdp.exe" reads as "invoiceexe.pdf". Each
      // becomes a space, which is what the command line's core.DisplayLine does
      // to the same text. Line breaks and every other character are kept, so a
      // multi-line value still draws as written. The stored text is untouched.
      function withoutDirectionControls(value) {
        return String(value).replace(/\p{Bidi_Control}/gu, " ");
      }
      // One line of text somebody typed — a file's name, a link's label or
      // address — as this board draws it: control and bidi-control characters
      // become spaces and runs of space collapse, exactly core.DisplayLine.
      function shownLine(value) {
        return String(value == null ? "" : value).replace(/[\p{Cc}\p{Bidi_Control}]/gu, " ")
          .split(/\s+/).filter(Boolean).join(" ");
      }
      function text(element, value) { element.textContent = withoutDirectionControls(value || ""); return element; }

      // How long ago the board last read the repository successfully.
      //
      // A wall-clock stamp answered a question nobody asks: what matters is not
      // when the last poll landed but whether one landed recently. An age says
      // that directly, and says it without the reader doing arithmetic against
      // their own clock.
      //
      // The age is reported in five-second steps. Polls land every second, so a
      // live counter would be a digit flickering between 0 and 1 forever;
      // quantising it means the text only changes when something has actually
      // changed, and the steady state is a green dot beside a fixed word.
      const SYNC_STEP_MS = 5000;
      const SYNC_STALE_MS = 15000;   // two missed polls is noise; fifteen seconds is not
      const SYNC_LOST_MS = 60000;

      let lastSynchronizedAt = null;

      function markSynchronized() {
        lastSynchronizedAt = Date.now();
        renderSyncAge();
      }

      function describeSyncAge(age) {
        if (age < SYNC_STEP_MS) return "in sync";
        const seconds = Math.floor(age / SYNC_STEP_MS) * (SYNC_STEP_MS / 1000);
        if (seconds < 60) return seconds + "s ago";
        const minutes = Math.floor(seconds / 60);
        if (minutes < 60) return minutes + "m ago";
        return Math.floor(minutes / 60) + "h ago";
      }

      function renderSyncAge() {
        if (!updatedText) return;
        if (lastSynchronizedAt === null) {
          text(updatedText, "Loaded from this checkout");
          if (syncDot) syncDot.dataset.state = "stale";
          return;
        }
        const age = Date.now() - lastSynchronizedAt;
        text(updatedText, describeSyncAge(age));
        if (syncDot) {
          syncDot.dataset.state =
            age >= SYNC_LOST_MS ? "lost" : age >= SYNC_STALE_MS ? "stale" : "fresh";
        }
      }
      // Card descriptions are hidden by default and this setting brings them
      // back. It is a browser preference rather than project state: it says how
      // one reader wants this board drawn, so it is remembered per browser and
      // never published to the repository the way a task edit is. Storage can be
      // refused outright — a Safari private window throws on the first read — and
      // a board that cannot remember the choice still has to draw, so every
      // access is guarded and an unreadable preference simply means the default.
      // Which of the board's two palettes this reader sees.
      //
      // A browser preference, like the descriptions setting below, and for the
      // same reason stated more strongly: a colour scheme answers what the room
      // someone is sitting in needs, not what the project is. It is never
      // published to the repository, so one reader working at night does not
      // darken the board for everybody who fetches.
      //
      // It sits in the header beside the other two browser preferences rather
      // than on the configuration route. Appearance is a thing a reader changes
      // because the light in the room changed, which is not an errand worth a
      // page; and the configuration route is where a board's *published*
      // settings are decided, so a control that publishes nothing was reading
      // as though it might.
      //
      // The empty string is the third state and the default: follow the
      // system. It is the absence of a choice rather than a word of its own,
      // which is what lets the stylesheet's own media query answer for it. See
      // toggleColorScheme for how one switch reaches a state it has no
      // position for.
      const colorSchemePreferenceKey = "workbook.board.scheme";
      const colorSchemes = ["", "light", "dark"];
      function rememberedColorScheme() {
        try {
          const stored = window.localStorage ? window.localStorage.getItem(colorSchemePreferenceKey) : null;
          return colorSchemes.includes(stored) ? stored : "";
        } catch (_) {
          return "";
        }
      }
      function rememberColorScheme(value) {
        try {
          if (!window.localStorage) return;
          if (value) window.localStorage.setItem(colorSchemePreferenceKey, value);
          else window.localStorage.removeItem(colorSchemePreferenceKey);
        } catch (_) {
        }
      }
      // The attribute the stylesheet reads. Removed rather than emptied for the
      // system default, so `:root:not([data-scheme="light"])` is answered by the
      // absence of the attribute rather than by a value that has to be spelled.
      // Guarded like every other environment capability here: the root element
      // is the one node this function needs and the one it cannot assume, and a
      // board that cannot reach it still has to draw. Without a choice applied
      // the stylesheet's media query is already the right answer.
      function applyColorScheme(value) {
        const root = document.documentElement;
        if (!root || !root.dataset) return;
        if (value) root.dataset.scheme = value;
        else delete root.dataset.scheme;
      }
      applyColorScheme(rememberedColorScheme());

      // What the operating system is asking for right now.
      //
      // Guarded like every other environment capability here: a browser that
      // cannot answer is treated as a light one, which is what the stylesheet
      // draws when its media query never matches. The query object is kept
      // rather than rebuilt, because the listener below is on it.
      const darkSystemQuery = window.matchMedia
        ? window.matchMedia("(prefers-color-scheme: dark)")
        : null;
      function systemColorScheme() {
        return darkSystemQuery && darkSystemQuery.matches ? "dark" : "light";
      }
      // The scheme the reader is actually looking at: their choice if they made
      // one, and the system's answer if they did not.
      function effectiveColorScheme() {
        return rememberedColorScheme() || systemColorScheme();
      }
      // One switch for a setting with three states, and the third is reachable
      // without a third position.
      //
      // The stored preference is light, dark, or absent — and absent is
      // "follow the system", which is what makes the media query the answer
      // rather than a value that always wins. A three-way control would say
      // all of that out loud and cost a reader a decision they mostly do not
      // have: what they want is the other palette, now.
      //
      // So the switch offers the two palettes and infers the third state from
      // which way they are moving. Choosing the scheme their system is *not*
      // asking for is a real preference and is stored. Choosing the one it *is*
      // asking for is indistinguishable from having no preference at all — so
      // rather than store a value that happens to agree today and stops
      // agreeing the next time they change their system, the choice is cleared
      // and they go back to following it.
      //
      // The cost is that a reader cannot pin the scheme their system already
      // has. That state is unobservable until the system changes, and when it
      // does, following is what almost everybody meant.
      function toggleColorScheme() {
        const wanted = effectiveColorScheme() === "dark" ? "light" : "dark";
        rememberColorScheme(wanted === systemColorScheme() ? "" : wanted);
        applyColorScheme(rememberedColorScheme());
        renderColorSchemePreference();
      }
      // The label names the switch rather than the next action, unlike its
      // siblings. "Hide Descriptions" works because descriptions are a noun the
      // verb acts on; "Light Mode, switch, on" would be a control announcing
      // the palette it is not showing. So the words stay put and aria-checked
      // carries the state, which is how a switch is meant to say it.
      //
      // The title is where the third state is disclosed, because it is the part
      // a reader cannot see: whether they are following the system or have
      // stepped away from it, and — when they have — that the way back is to
      // choose the palette their system is already asking for.
      function renderColorSchemePreference() {
        if (!schemeToggle) return;
        const effective = effectiveColorScheme();
        const following = rememberedColorScheme() === "";
        schemeToggle.setAttribute("aria-checked", effective === "dark" ? "true" : "false");
        text(schemeLabel, "Dark Mode");
        schemeToggle.title = following
          ? "Following your system, which is " + effective + " right now. Click for " +
            (effective === "dark" ? "light" : "dark") + "."
          : "Fixed to " + effective + ", whatever your system asks for. Click for " +
            (effective === "dark" ? "light" : "dark") + " to follow your system again.";
      }
      renderColorSchemePreference();
      if (schemeToggle) schemeToggle.addEventListener("click", toggleColorScheme);
      // A reader who is following the system and whose system changes under
      // them has a board that repaints itself — the media query does that with
      // no help. What it cannot do is rewrite the switch, which is still
      // announcing the palette that was on screen a moment ago.
      if (darkSystemQuery && darkSystemQuery.addEventListener) {
        darkSystemQuery.addEventListener("change", renderColorSchemePreference);
      }

      const descriptionPreferenceKey = "workbook.board.descriptions";
      let descriptionsShown = false;
      function rememberedDescriptionPreference() {
        try {
          return window.localStorage ? window.localStorage.getItem(descriptionPreferenceKey) : null;
        } catch (_) {
          return null;
        }
      }
      function rememberDescriptionPreference(value) {
        try {
          if (window.localStorage) window.localStorage.setItem(descriptionPreferenceKey, value);
        } catch (_) {
        }
      }
      // The name and the state pull in the same direction now that they are
      // saying different things: the name is the next action and aria-checked is
      // what is true, so "Hide Descriptions, switch, on" reads as one sentence.
      // It was a name that carried the state — "Descriptions: shown" — and it
      // could carry no aria-pressed beside it, because "Descriptions: hidden,
      // not pressed" reads as the opposite of what just happened. aria-pressed
      // stays off it regardless: a switch states itself with aria-checked.
      function renderDescriptionPreference() {
        boardView.dataset.descriptions = descriptionsShown ? "shown" : "hidden";
        if (!descriptionToggle) return;
        descriptionToggle.setAttribute("aria-checked", descriptionsShown ? "true" : "false");
        text(descriptionLabel, descriptionsShown ? "Hide Descriptions" : "Show Descriptions");
        descriptionToggle.title = descriptionsShown
          ? "Cards show the first lines of their description. Click to hide them."
          : "Cards show titles only. Click to show the first lines of each description.";
      }
      descriptionsShown = rememberedDescriptionPreference() === "shown";
      renderDescriptionPreference();
      if (descriptionToggle) descriptionToggle.addEventListener("click", () => {
        descriptionsShown = !descriptionsShown;
        rememberDescriptionPreference(descriptionsShown ? "shown" : "hidden");
        renderDescriptionPreference();
      });
