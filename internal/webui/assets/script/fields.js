      function field(labelText, control, wide = false, modifier = "", action = null) {
        const classes = ["field"];
        if (wide) classes.push("field--wide");
        if (modifier) classes.push(modifier);
        const wrapper = document.createElement("div"); wrapper.className = classes.join(" ");
        const label = document.createElement("label");
        // A composite control is a region with no id a caption can point at, so
        // it names the focusable element inside it and clicking the caption
        // still lands the caret where typing goes.
        label.htmlFor = control.dataset.fieldFor || control.id;
        text(label, labelText);
        // An action belonging to the field sits on the caption's own line rather
        // than on a row of its own, so the field keeps the two grid tracks it
        // has always had: the caption and the control that stretches under it.
        if (action) {
          const header = document.createElement("div");
          header.className = "field__header";
          header.append(label, action);
          wrapper.append(header, control);
        } else {
          wrapper.append(label, control);
        }
        return wrapper;
      }
      // Labels are a set, so the form edits them as one chiclet per label
      // rather than as a line of commas: the input holds only the label being
      // typed, each entry carries its own remove control, and nothing has to be
      // re-read out of a sentence the author stopped proofreading three labels
      // ago. What the server sees is unchanged — an array of strings.
      function labelEditor(initial) {
        const values = [];
        const region = document.createElement("div");
        region.className = "label-editor";
        const chiclets = document.createElement("ul");
        chiclets.className = "label-chips";
        // A flex list is not a list to a screen reader unless it says so.
        chiclets.setAttribute("role", "list");
        // Deliberately unnamed: this input holds the label being typed, not the
        // field's value, and the set it belongs to is read from the chiclets.
        const input = document.createElement("input");
        input.id = "task-labels";
        input.placeholder = "Add a label";
        input.setAttribute("aria-describedby", "task-labels-hint");
        region.dataset.fieldFor = input.id;
        const hint = document.createElement("p");
        hint.id = "task-labels-hint";
        hint.className = "label-editor__hint";
        text(hint, "Press Enter or comma to add a label.");
        const announcement = document.createElement("p");
        announcement.className = "label-editor__status";
        announcement.setAttribute("role", "status");
        announcement.setAttribute("aria-live", "polite");
        // Saved chiclets sit below the input: the eye rests where typing
        // happens, and the set grows downward away from the caret.
        region.append(input, chiclets, hint, announcement);

        // Chiclets are reconciled, never rebuilt. Pressing a remove control
        // blurs the input, and the blur commits pending text and re-renders
        // before the click lands; a render that replaced every node would
        // detach the very button the press started on, the click would go to
        // the surviving list instead, and the removal would be silently
        // dropped. Shift+Tab out of an input holding text loses focus the same
        // way. So a chiclet already on screen is never moved, only removed
        // where it stands, and a new one is appended. The row reads in the
        // order its labels reached this form rather than in the order of the
        // set: labels the reader typed appear in the order they typed them, and
        // a set replaced by a correction keeps the survivors ahead of whatever
        // the correction brought in. Only the order of `values` reaches a save.
        const chicletsByLabel = new Map();
        function renderChiclets() {
          chicletsByLabel.forEach((chiclet, label) => {
            if (values.includes(label)) return;
            chiclet.remove();
            chicletsByLabel.delete(label);
          });
          values.forEach((label) => {
            if (chicletsByLabel.has(label)) return;
            const chiclet = document.createElement("li");
            chiclet.className = "label-chip";
            chiclet.dataset.label = label;
            const name = document.createElement("span");
            name.className = "label-chip__name";
            text(name, label);
            const remove = document.createElement("button");
            remove.type = "button";
            remove.className = "label-chip__remove";
            remove.dataset.removeLabel = label;
            // The visible × is not a name, and "button" is not one either.
            remove.setAttribute("aria-label", "Remove label " + label);
            text(remove, "×");
            remove.addEventListener("click", () => removeLabel(label));
            chiclet.append(name, remove);
            chicletsByLabel.set(label, chiclet);
            chiclets.append(chiclet);
          });
        }
        function addLabel(candidate) {
          const label = String(candidate).trim();
          if (!label) return false;
          if (values.includes(label)) {
            text(announcement, "Label " + label + " is already on this task.");
            return false;
          }
          values.push(label);
          renderChiclets();
          text(announcement, "Added label " + label + ".");
          return true;
        }
        function removeLabel(label) {
          const at = values.indexOf(label);
          if (at < 0) return;
          values.splice(at, 1);
          renderChiclets();
          text(announcement, "Removed label " + label + ".");
          // The button that took the click no longer exists. Without this the
          // focus falls to the document body and a keyboard user loses the
          // control they were working in.
          input.focus();
        }
        // Enter, a comma, and Save all mean the same thing about the text in
        // the input: it is a label, or several.
        function commitInput() {
          const entered = input.value;
          input.value = "";
          entered.split(",").forEach(addLabel);
        }
        input.addEventListener("input", () => {
          // Everything before the last comma is finished; the tail is still
          // being typed. A paste of "web, docs, api" lands as two chiclets and
          // a word in progress.
          if (!input.value.includes(",")) return;
          const parts = input.value.split(",");
          const tail = parts.pop();
          parts.forEach(addLabel);
          input.value = tail;
        });
        input.addEventListener("keydown", (event) => {
          // An IME delivers the Enter that confirms a candidate, and the
          // Backspace that edits one, as keydown with isComposing set. Reading
          // either as a label would commit the unconverted reading — the kana
          // rather than the word — and no amount of retyping would ever get
          // the converted label in.
          if (event.isComposing) return;
          if (event.key === "Enter") {
            // Enter here adds a label. Letting it through would submit the
            // form instead, which is the one thing the user did not ask for.
            event.preventDefault();
            commitInput();
            return;
          }
          if (event.key === "Backspace" && !input.value && values.length) {
            event.preventDefault();
            removeLabel(values[values.length - 1]);
          }
        });
        // Leaving the field is as much a commit as pressing Enter, so the
        // chiclets always show what a save would send.
        input.addEventListener("blur", () => { commitInput(); });

        function replaceValues(next) {
          values.splice(0, values.length);
          (next || []).forEach((label) => {
            const value = String(label).trim();
            if (value && !values.includes(value)) values.push(value);
          });
          renderChiclets();
        }
        replaceValues(initial);
        return {
          region,
          // A label typed but never committed is still a label the user
          // entered. Saving folds it in rather than discarding their last
          // keystrokes for want of an Enter.
          values() {
            const entered = [...values];
            input.value.split(",").forEach((part) => {
              const label = part.trim();
              if (label && !entered.includes(label)) entered.push(label);
            });
            return entered;
          },
          // The set the chiclets are showing, with nothing folded in from the
          // input. It answers a different question than values(): not "what
          // would a save send" but "has the reader decided anything about this
          // set", which three letters on the way to a word have not.
          committed() { return [...values]; },
          // Puts a different set on screen without disturbing the input. It is
          // how a form corrects a label set it is displaying on someone else's
          // authority; the label being typed is the reader's and is left alone.
          replace: replaceValues
        };
      }
      // Makes a select show a value, or reports that it cannot. A select only
      // shows values it has an option for, so the answer is returned rather
      // than assumed: a value with no option of its own would otherwise leave
      // the control displaying whatever it displayed before.
      function showSelected(control, value) {
        const options = [...control.children];
        const wanted = options.find((option) => option.value === value);
        if (!wanted) return false;
        options.forEach((option) => { option.selected = false; });
        wanted.selected = true;
        return true;
      }
      // What a select says about a value this project's vocabulary cannot
      // resolve, status and priority alike: the value, and then why it is not
      // one of the options.
      //
      // The value leads because a collapsed select clips the end. The properties
      // column gives one about 200px, and the sentence this replaced — "Choose
      // one of this project's statuses (current: ready)" — spent all of it on
      // an instruction the field's own label already gives, and cut off the one
      // fact nothing else on the page carries. The explanation is the half the
      // clip may take. The two vocabularies are deliberately symmetric, so one
      // sentence serves both and the two selects cannot drift apart.
      function strandedPlaceholderText(value) {
        return "Current: " + value + " (not in this project)";
      }
      // The status a task carries can be one this client has no option for —
      // written by a newer Workbook, or by a project that added a status —
      // which is exactly the value showSelected cannot show. The list grows a
      // disabled placeholder naming it, so the field reports the status the
      // server holds instead of displaying a canonical neighbour that would
      // read as saved state. Selecting the placeholder sends nothing: an
      // untouched control the client cannot render is not a request to change
      // the status (changedTaskValues).
      function statusPlaceholder() {
        const option = document.createElement("option");
        option.value = "";
        option.disabled = true;
        return option;
      }
      // Makes a status select show a status, whether or not this page has a
      // column for it, so it is also how a select already on screen adopts one.
      // The placeholder is added and removed as the status needs it, because
      // one naming a status the task has since left is a worse label than none.
      //
      // What the placeholder says is strandedPlaceholderText's, shared with the
      // priority select below.
      function showStatus(control, status) {
        const first = control.firstElementChild;
        let placeholder = first && first.value === "" ? first : null;
        if (statuses.has(status)) {
          if (placeholder) placeholder.remove();
          showSelected(control, status);
          return;
        }
        if (!placeholder) {
          placeholder = statusPlaceholder();
          control.insertBefore(placeholder, control.firstElementChild);
        }
        text(placeholder, strandedPlaceholderText(status));
        showSelected(control, "");
      }
      // The priority half of the same problem. A task's priority can be one this
      // project's vocabulary cannot resolve — a teammate on a newer build
      // authored it and filed a task under it — and the select, built from the
      // priorities the server published, has no option for it.
      //
      // Left alone that is worse here than a blank field. A select showing none
      // of its options shows the first one, so the form would display a priority
      // nobody chose, and the next save would send it: changedTaskValues asks
      // whether the control disagrees with the task, and a control the client
      // could not draw always does. A reader editing the title would silently
      // reassign the priority. Selecting the placeholder sends nothing instead.
      function priorityPlaceholder() {
        const option = document.createElement("option");
        option.value = "";
        option.disabled = true;
        return option;
      }
      // Makes a priority select show a priority, whether or not this project
      // defines it, so it is also how a select already on screen adopts one.
      // The placeholder is added and removed as the priority needs it, for the
      // reason showStatus's is: one naming a priority the task has since left is
      // a worse label than none.
      //
      // It says what showStatus's says, in the same words, through the same
      // function.
      function showPriority(control, priority) {
        const first = control.firstElementChild;
        let placeholder = first && first.value === "" ? first : null;
        if (priorities.has(priority)) {
          if (placeholder) placeholder.remove();
          showSelected(control, priority);
          return;
        }
        if (!placeholder) {
          placeholder = priorityPlaceholder();
          control.insertBefore(placeholder, control.firstElementChild);
        }
        text(placeholder, strandedPlaceholderText(priority));
        showSelected(control, "");
      }
      function select(name, options, value) {
        const control = document.createElement("select"); control.id = "task-" + name; control.name = name;
        options.forEach(([optionValue, label]) => {
          const option = document.createElement("option"); option.value = optionValue; text(option, label); option.selected = optionValue === value; control.append(option);
        });
        return control;
      }
