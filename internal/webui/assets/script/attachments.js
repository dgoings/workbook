      // A file's bytes as base64, which is what the upload endpoint takes.
      //
      // JSON rather than multipart, and that is the same-origin guard's decision
      // rather than a taste: every mutation on this board must declare
      // application/json, because multipart/form-data is one of the three types
      // a cross-site form can send with no preflight — a multipart upload would
      // be the one address here a drive-by page could post to.
      function readFileAsBase64(file) {
        return new Promise((resolve, reject) => {
          const reader = new FileReader();
          reader.onerror = () => reject(reader.error || new Error("The file could not be read."));
          reader.onload = () => {
            const result = String(reader.result || "");
            const comma = result.indexOf(",");
            if (comma < 0) {
              reject(new Error("The file could not be read."));
              return;
            }
            resolve(result.slice(comma + 1));
          };
          reader.readAsDataURL(file);
        });
      }
      // The calendar day a dropped file was removed, as the server wrote it.
      function droppedOn(attachment) {
        return String((attachment.dropped && attachment.dropped.at) || "").slice(0, 10);
      }
      // A file's name as this board draws it: control and bidi-control
      // characters become spaces and runs of space collapse, which is what the
      // command line's core.DisplayLine does to the same names.
      //
      // The name is somebody else's text, and the reader decides from it
      // whether to open the file. U+202E RIGHT-TO-LEFT OVERRIDE draws the rest
      // of a name backwards, so "invoice<U+202E>fdp.exe" would read here as
      // "invoiceexe.pdf" while the route hands it back as an .exe. The stored
      // name is untouched — the download is still named by the server — and
      // only what is drawn changes.
      function shownFileName(name) {
        return String(name || "").replace(/[\p{Cc}\p{Bidi_Control}]/gu, " ").split(/\s+/).filter(Boolean).join(" ");
      }
      // What an attachment's row names it as: a link's label, or its URL when
      // it has none, and a file's name.
      function attachmentShownName(attachment) {
        if (attachment.kind === "link") return attachment.label || attachment.url || "Unnamed link";
        return shownFileName(attachment.name) || "Unnamed file";
      }
      // An attachment's metadata, as the one line its card draws and as the
      // fuller tooltip on that line. The line is held to what fits a sidebar
      // card: the kind, a file's size, the day it was added and who added it.
      // The tooltip keeps everything the line used to say — a file's media type
      // and the exact time — so nothing a reader could read before is gone,
      // only a hover away. A link's destination is in neither: it is the name
      // above when the link has no label, and the name's tooltip either way,
      // so a reader deciding whether to follow one can still see where it goes.
      function attachmentMeta(attachment) {
        const kind = attachment.kind === "link" ? "link" : "file";
        const line = [kind];
        const full = [kind];
        if (attachment.kind !== "link") {
          if (attachment.media) full.push(attachment.media);
          line.push(attachmentSize(attachment.size));
          full.push(attachmentSize(attachment.size));
          // A file compaction dropped is still listed, so a reader knows it
          // existed; the day is the one the command line and the download
          // route's refusal both name.
          if (attachment.dropped) {
            const note = "Removed by compaction on " + droppedOn(attachment);
            line.push(note);
            full.push(note);
          }
        }
        line.push(attachmentAddedOn(attachment.addedAt));
        if (attachment.author) line.push(attachment.author);
        full.push("added " + attribution(attachment.author, attachment.addedAt));
        return { line: line.join(" · "), full: full.join(" · ") };
      }
      // The calendar day an attachment was added, in the reader's own format.
      function attachmentAddedOn(wallTime) {
        const when = new Date(wallTime);
        return Number.isNaN(when.getTime()) ? String(wallTime || "") : when.toLocaleDateString();
      }
      // ─── Adding attachments ─────────────────────────────────────────────────
      //
      // Two surfaces add attachments and they are not the same operation. A
      // task's own page sends each one the moment it is chosen. A New Task form
      // cannot: an attachment's bytes live in the commit that adds them, on the
      // task's own ref, so there is nothing to add them to until the task
      // exists. It stages what the other one would have sent and sends it after
      // the create, once the server has named the task.
      //
      // What the two share is everything a reader touches — the same two forms,
      // the same refusals, the same rows — so those are built once, here.
      //
      // Both surfaces expose acceptFiles(): take these File objects and do
      // whatever this surface does with them. That is the whole seam a drop
      // target needs, and it is why the pointer half of this can be added later
      // without either surface knowing it happened.
      function attachmentAdder(config) {
        const region = document.createElement("div");
        region.className = "attachment-add";
        // Two forms rather than one with two buttons in it. A form is one
        // intent, and pressing Return is a submit: with both controls in one
        // form, a reader who had chosen a file and pressed Return submitted the
        // *link* — answered "A link needs a URL", with no request sent and the
        // file still unattached. Each control now submits the thing it is for.
        const fileGroup = document.createElement("form");
        fileGroup.className = "attachment-add__group";
        fileGroup.dataset.attachmentFileForm = "";
        const fileLabel = document.createElement("label");
        fileLabel.setAttribute("for", "attachment-file");
        text(fileLabel, "Attach a file");
        const file = document.createElement("input");
        file.type = "file";
        file.id = "attachment-file";
        file.name = "attachment-file";
        file.className = "attachment-add__file";
        // The browser's own chooser draws a gray "Choose File" and a "No file
        // chosen" beside it, in its own type and colors. The input stays the
        // control — clipped out of sight, still in the tab order, still what a
        // screen reader names, still the thing Space and Enter open — and this
        // label is what is seen of it: a button like the sidebar's others.
        // Clicking a label activates its input, so a press opens the real
        // picker, multiple selection included; the pending list below is the
        // feedback for what was chosen.
        const choose = document.createElement("label");
        choose.className = "thread-button attachment-add__choose";
        choose.htmlFor = file.id;
        choose.dataset.attachmentChoose = "";
        text(choose, config.multiple ? "Choose files" : "Choose file");
        // Offered only where more than one can be taken at a time. A task page
        // sends each file as its own write against a head the one before it
        // moved; a form that is only staging holds a list already, so choosing
        // five at once costs it nothing.
        if (config.multiple) file.multiple = true;
        const fileActions = document.createElement("div");
        fileActions.className = "attachment-add__actions";
        const addFile = threadButton(config.fileAction, true);
        addFile.type = "submit";
        fileActions.append(addFile);
        // What has been chosen and not yet added, held here rather than read
        // back off the input. An input's FileList is read-only, so a file the
        // reader changes their mind about could not be taken out of it; this
        // list can lose one, and the input is reset after every choice so the
        // same file chosen again is a change the browser reports.
        //
        // The chooser and Add file take turns: nothing chosen draws only the
        // chooser, and anything chosen draws the list, each name with a × that
        // takes it back out, and the Add file that sends or stages them.
        // Taking the last one out returns the chooser.
        let pending = [];
        const pendingList = document.createElement("ul");
        pendingList.className = "attachment-pending";
        pendingList.dataset.attachmentPending = "";
        function drawPending() {
          pendingList.replaceChildren(...pending.map((chosen) => {
            const row = document.createElement("li");
            row.className = "attachment-pending__item";
            const name = document.createElement("span");
            name.className = "attachment-pending__name";
            text(name, chosen.name);
            const remove = rowRemoveButton("Remove " + chosen.name);
            remove.className += " attachment-pending__remove";
            remove.addEventListener("click", () => {
              const at = pending.indexOf(chosen);
              pending = pending.filter((held) => held !== chosen);
              drawPending();
              // The × just pressed is gone, and a caret left on it would fall
              // to the page body. It goes where the reader is likely to go
              // next: the × of the file that moved into this one's place, the
              // one before it when this was the last, or back to the chooser
              // once there is nothing left.
              const next = pending.length
                ? findPendingRemove(Math.min(at, pending.length - 1))
                : file;
              if (next) next.focus();
            });
            row.append(name, remove);
            return row;
          }));
          const holding = pending.length > 0;
          pendingList.hidden = !holding;
          fileActions.hidden = !holding;
          file.hidden = holding;
          choose.hidden = holding;
        }
        function findPendingRemove(at) {
          const row = pendingList.children[at];
          return row ? row.children[row.children.length - 1] : null;
        }
        file.addEventListener("change", () => {
          const chosen = [...(file.files || [])];
          file.value = "";
          if (chosen.length === 0) return;
          // A chooser that takes one file at a time is answered with the one
          // it took, so choosing again replaces the choice rather than
          // queueing a second file the surface would send one by one.
          pending = config.multiple ? pending.concat(chosen) : chosen;
          drawPending();
          // The choice hides the chooser the reader is on, so the caret moves
          // to the Add file that took its place rather than to the page body.
          addFile.focus();
        });
        drawPending();
        // A drop target nobody knows about is a chooser. Said once, in the group
        // it belongs to, rather than drawn as a dashed box taking up room the
        // sidebar has not got.
        const dropHint = document.createElement("p");
        dropHint.className = "attachment-add__hint";
        dropHint.dataset.attachmentDropHint = "";
        text(dropHint, "Or drop files anywhere in this panel.");
        fileGroup.append(fileLabel, file, choose, pendingList, dropHint, fileActions);
        const linkGroup = document.createElement("form");
        linkGroup.className = "attachment-add__group";
        linkGroup.dataset.attachmentLinkForm = "";
        // The browser must not vet this field, because a URL it refuses is a URL
        // nobody is told about: constraint validation cancels the submit, and
        // this page's only way to say anything is the panel's own line. Left on,
        // typing "not a url" and pressing Attach did nothing at all — no request,
        // no message. The refusal is said out loud instead, in the words core
        // uses, and http-or-https is core's rule either way.
        linkGroup.noValidate = true;
        const linkLabel = document.createElement("label");
        linkLabel.setAttribute("for", "attachment-url");
        text(linkLabel, "Attach a link");
        const url = document.createElement("input");
        url.type = "url";
        url.id = "attachment-url";
        url.name = "attachment-url";
        url.placeholder = "https://";
        const caption = document.createElement("input");
        caption.type = "text";
        caption.id = "attachment-label";
        caption.name = "attachment-label";
        caption.placeholder = "Label (optional)";
        caption.setAttribute("aria-label", "Link label");
        // The button that attaches sits on the URL's line, as every other add
        // control in the sidebar sits beside the box it takes, and the
        // optional label has the full width of the line under them. The order
        // in the document is the order on screen, so Tab goes URL, Attach,
        // label; Enter in either box submits the form, so a reader who fills
        // in the label never has to go back up for the button. It reads one
        // word and is named for what it does.
        const linkLine = document.createElement("div");
        linkLine.className = "sidebar-inline";
        linkLine.dataset.attachmentLinkLine = "";
        const addLink = threadButton(config.linkAction, true);
        addLink.type = "submit";
        addLink.dataset.attachmentAddLink = "";
        addLink.setAttribute("aria-label", config.linkName);
        linkLine.append(url, addLink);
        linkGroup.append(linkLabel, linkLine, caption);
        region.append(fileGroup, linkGroup);
        fileGroup.addEventListener("submit", (event) => {
          event.preventDefault();
          const chosen = [...pending];
          if (chosen.length === 0) return config.report("Choose a file to attach.");
          return config.acceptFiles(chosen);
        });
        // The button is disabled while the link is sent, which takes the
        // caret off it. A link that was attached hands it back to the URL box,
        // emptied for the next; one that was refused hands it back to the
        // control that sent it, with what was typed still in the boxes.
        linkGroup.addEventListener("submit", async (event) => {
          event.preventDefault();
          const settle = holdSidebarFocus(event.submitter || document.activeElement || addLink, null, null, url);
          const destination = url.value.trim();
          if (!destination) return config.report("A link needs a URL.");
          settle((await config.acceptLink(destination, caption.value.trim())) === true);
        });
        return {
          region, file, url, caption, addFile, addLink,
          // Called once the chosen files are sent or staged. That hides the
          // Add file the reader pressed, so a caret on it — or one already
          // dropped to the body — goes back to the chooser. A caret somewhere
          // else entirely, as it is after a drop or a paste, is left alone.
          clearFile() {
            const caret = document.activeElement;
            const ours = !caret || caret === document.body || fileGroup.contains(caret);
            file.value = "";
            pending = [];
            drawPending();
            if (ours) file.focus();
          },
          clearLink() { url.value = ""; caption.value = ""; }
        };
      }
      // ─── Dropping files onto the attachment controls ────────────────────────
      //
      // A drop is the same intent as choosing a file, so it arrives at the same
      // door: acceptFiles(). Both surfaces already had one — the task page
      // uploads what it is handed, the create form stages it — and neither had
      // to learn anything to gain this. Every ceiling a chosen file is asked, a
      // dropped file is asked, because they are the same call.
      //
      // Whether a drag is one this zone takes is decided in one place and asked
      // by dragenter and dragover alike. That is not tidiness. A browser takes
      // the accept flag from whichever of the two it dispatched last, and Chrome
      // dispatches dragenter rather than dragover whenever the element under the
      // cursor changes — which, inside a panel made of rows, inputs and buttons,
      // is most of the time a cursor is moving. This repo has already paid for
      // that once on the board, where a card released while its column was
      // scrolling was silently discarded; dragDestination() carries the
      // measurement. Two handlers answering one question in two places is how
      // they come to disagree, so there is one question here.
      // Asked of a DataTransfer rather than of an event, because a clipboard is
      // one too: a paste carries its flavours in exactly the shape a drag does,
      // and the two gestures must not come to hold different ideas of what a
      // transfer carrying a file looks like.
      function transferHasFiles(transfer) {
        if (!transfer || !transfer.types) return false;
        // A DOMStringList in one browser and a frozen array in another. indexOf
        // through Array's prototype answers both, which `includes` does not.
        return Array.prototype.indexOf.call(transfer.types, "Files") >= 0;
      }
      function draggingFiles(event) {
        return transferHasFiles(event.dataTransfer);
      }
      // The files a transfer is carrying — a drop's or a paste's — with folders
      // counted rather than mistaken for them.
      //
      // A folder arrives in `files` looking like a file — it has a name, and on
      // some platforms a size — and only the entry API tells them apart. Left
      // undetected it stages, uploads, and fails on the read: a loud failure,
      // several seconds later, over something this page could have answered the
      // instant it arrived. A clipboard is asked the same question because a
      // folder can be copied as readily as dragged, and because a paste's item
      // list holds the text flavours too, which are skipped here by the same
      // kind check that has always been here.
      function transferredFiles(transfer) {
        const files = [];
        let folders = 0;
        const items = transfer && transfer.items;
        if (items && items.length) {
          for (const item of items) {
            if (item.kind !== "file") continue;
            const entry = typeof item.webkitGetAsEntry === "function" ? item.webkitGetAsEntry() : null;
            if (entry && entry.isDirectory) {
              folders += 1;
              continue;
            }
            const file = item.getAsFile();
            if (file) files.push(file);
          }
          return { files, folders };
        }
        // No item list to ask. Every file still arrives, and a folder among them
        // is refused by the size check the way an empty file is — later than
        // here, but refused, and never attached.
        return { files: [...((transfer && transfer.files) || [])], folders: 0 };
      }
      function skippedFolderNote(folders) {
        return folders === 1
          ? "One folder was skipped: a folder cannot be attached, only the files inside it."
          : folders + " folders were skipped: a folder cannot be attached, only the files inside it.";
      }
      // What a surface says while a run is already walking its list. Both
      // gestures are refused for the same reason and say so in the same words,
      // naming only the gesture the reader is to try again.
      function attachmentBusyRefusal(gesture) {
        return "Those attachments are still being sent; wait for that to finish and " +
          gesture + " again.";
      }
      // Files a gesture handed over, given to the surface and then accounted
      // for. The same call for a drop and for a paste, because the door is the
      // same door and the accounting is the same accounting.
      async function takeTransferredFiles(config, transferred) {
        await config.acceptFiles(transferred.files);
        if (!transferred.folders) return;
        // Said after the files are dealt with, because acceptFiles clears this
        // line as it starts — and never over the top of what it wrote there. A
        // file that was too large to attach is a better thing to be told about
        // than a folder that was never attachable at all.
        if (!config.status()) config.report(skippedFolderNote(transferred.folders));
      }
      // How long a leave that might be a cancel waits to find out.
      //
      // Child churn is followed by the dragenter for whatever the cursor moved
      // onto, in the same gesture and immediately — so anything longer than a
      // frame or two settles it, and this is not a timeout anybody waits on.
      const attachmentDropDepartureGrace = 250;
      // And the backstop, for the drag that ends without telling the page
      // anything at all.
      //
      // Measured on Chrome 151 through Blink's own drag pipeline: a refused
      // hover ends with dragenter, dragover, dragover, dragleave — a leave the
      // rule above catches — but a cancelled drag delivers nothing whatsoever,
      // no leave and no dragend, because a file dragged in from the desktop
      // belongs to the desktop and this page is never told it was dropped
      // somewhere else. Only time can tell it.
      //
      // It is deliberately generous, because the same measurement says a drag
      // hovering still emits no dragover to keep it alive — one dispatched
      // dragover, then nothing at 0.5s, 1s, 1.5s and 2s, which is the same
      // thing the board's autoscroll had to build a frame loop around. Anything
      // brisk would go dark under a reader who paused to read the panel.
      //
      // Nothing rests on it being right. The highlight is decoration: whether a
      // drop is accepted is decided fresh on every dragenter and dragover, so
      // the worst this can do is take a highlight down early and put it back on
      // the next movement.
      const attachmentDropIdleGrace = 3000;
      function attachmentDropZone(zone, config) {
        zone.dataset.attachmentDrop = "";
        // What a refused zone is saying, held so it can be taken back down
        // without taking down anything written since.
        const busyRefusal = attachmentBusyRefusal("drop");
        let departure = null;
        // The one decision. "" is a drag this zone has nothing to do with — a
        // card being dragged across the page, or text — and it is left entirely
        // alone, so the board's own machinery sees exactly what it saw before.
        function dropDecision(event) {
          if (!draggingFiles(event)) return "";
          return config.busy() ? "refused" : "active";
        }
        function showState(state) {
          const previous = zone.dataset.dropState || "";
          if (state === previous) return;
          if (state) zone.dataset.dropState = state;
          else delete zone.dataset.dropState;
          // The refusal is said as the zone turns red, and not one moment later.
          //
          // A refused drop is never delivered at all. dropEffect "none" makes
          // Blink cancel the drag then and there, so the sequence a refusal
          // actually produces is dragenter, dragover…, dragleave — and no drop,
          // ever. A refusal announced from a drop handler is therefore a
          // refusal nobody is told about, over a zone that would sit there red
          // for good. So it is announced where the decision is made, and taken
          // down with the state it explains — and only if it is still the thing
          // on screen, because anything written since belongs to somebody else.
          if (state === "refused") config.report(busyRefusal);
          else if (previous === "refused" && config.status() === busyRefusal) config.report("");
        }
        function cancelDeparture() {
          if (departure === null) return;
          window.clearTimeout(departure);
          departure = null;
        }
        // One timer, two deadlines: the short one for a leave that might be the
        // end, the long one for a gesture that has said nothing for so long
        // that it cannot still be here.
        function armDeparture(delay) {
          cancelDeparture();
          departure = window.setTimeout(() => {
            departure = null;
            showState("");
          }, delay);
        }
        function answerDrag(event) {
          // Any drag event at all is this gesture still going, which calls off
          // a departure booked by an ambiguous leave and pushes the backstop
          // out again.
          cancelDeparture();
          const decision = dropDecision(event);
          if (!decision) return;
          armDeparture(attachmentDropIdleGrace);
          // Answered even when it is refused. Without preventDefault the browser
          // handles the drop itself and navigates the window to the file — which
          // over a create form holding staged files is the worst outcome
          // available, and it is what happens by default.
          event.preventDefault();
          if (event.dataTransfer) {
            event.dataTransfer.dropEffect = decision === "active" ? "copy" : "none";
          }
          showState(decision);
        }
        zone.addEventListener("dragenter", answerDrag);
        zone.addEventListener("dragover", answerDrag);
        zone.addEventListener("dragleave", (event) => {
          // Three answers, and the third is the one that matters.
          //
          // A leave naming somewhere inside this zone is child churn: the rows
          // are replaced as the list changes, and a cursor crossing from one to
          // the next leaves a child without leaving the zone. Asked first and on
          // its own, because the coordinates reported with a leave are not
          // always the cursor's — Chrome reports a leave at the origin often
          // enough that a check reading only those would call this a departure.
          if (zone.contains(event.relatedTarget)) return;
          // A cursor outside the zone's box has left it, however the leave was
          // reported and whatever it named.
          if (!cursorWithin(zone, event)) {
            cancelDeparture();
            showState("");
            return;
          }
          // What is left is a leave that named nothing while the cursor is
          // still inside, and two entirely different things produce it. Firefox
          // and Safari report every churn this way. Every browser reports a
          // cancelled drag this way — including the one Blink cancels the
          // instant this zone answers "none", which is the only way a refusal
          // ever ends.
          //
          // So it is not decided here. It is decided by whether the gesture
          // says anything more: a churn is followed by the dragenter for what
          // the cursor moved onto, immediately, and a drag that has ended is
          // followed by nothing at all. The departure is booked and any further
          // drag event calls it off.
          //
          // This is what clears the highlight after a refusal, which is the one
          // ending Blink does report this way — and the backstop above is what
          // clears it after the endings Blink reports no way at all.
          armDeparture(attachmentDropDepartureGrace);
        });
        zone.addEventListener("drop", (event) => {
          if (!draggingFiles(event)) return;
          event.preventDefault();
          cancelDeparture();
          showState("");
          if (config.busy()) {
            // Not reachable while dropEffect is "none" — Blink cancels the drag
            // rather than delivering this — and kept because it is the guard
            // rather than the message. A browser that delivered the drop anyway
            // must not put a file into a list a run is already walking.
            return;
          }
          const dropped = transferredFiles(event.dataTransfer);
          if (dropped.files.length === 0) {
            config.report(dropped.folders
              ? skippedFolderNote(dropped.folders)
              : "That drop carried no file to attach.");
            return;
          }
          return takeTransferredFiles(config, dropped);
        });
      }
      // ─── Pasting a file onto the attachment controls ────────────────────────
      //
      // Cmd+V of a screenshot is the same intent as dropping the file, so it
      // ends at the same door — acceptFiles() — and is asked every ceiling by
      // the same call, refused mid-run by the same rule, and told about folders
      // in the same words.
      //
      // What it cannot share is where it is heard. A paste is dispatched at
      // whatever has focus, and a panel of rows and buttons never has any, so a
      // listener on the zone itself would fire only while the caret sat in one
      // of the two fields inside it — which is the one moment a reader pasting
      // a screenshot is least likely to be in. The page listens once, at the
      // document, and the surface that would have taken a drop takes the paste.
      //
      // Only one surface is ever on screen: a task page draws one attachment
      // panel, a New Task form draws one staging panel, and the route that
      // replaces either takes its panel with it. So the latest registration is
      // the current one — and it is checked against the document before it is
      // used, so a panel that has been rendered away takes nothing and is let
      // go of rather than held for the life of the page.
      let attachmentPasteSurface = null;
      function attachmentPasteTarget(zone, config) {
        attachmentPasteSurface = { zone, config };
      }
      function livePasteSurface() {
        const surface = attachmentPasteSurface;
        if (!surface) return null;
        if (main.contains(surface.zone)) return surface;
        attachmentPasteSurface = null;
        return null;
      }
      // Whether the caret is somewhere that takes typed text, which decides who
      // a paste belongs to below. Asked of what the paste was dispatched at,
      // which is that element or something inside it.
      function takesTypedText(element) {
        for (let node = element; node; node = node.parentElement) {
          if (node.isContentEditable) return true;
          if (node.tagName === "TEXTAREA") return true;
          if (node.tagName === "INPUT") {
            const kind = String(node.type || "text").toLowerCase();
            return kind !== "checkbox" && kind !== "radio" && kind !== "file" &&
              kind !== "button" && kind !== "submit" && kind !== "reset" &&
              kind !== "range" && kind !== "color";
          }
        }
        return false;
      }
      // A paste that carries text as well as files belongs to the caret.
      //
      // A clipboard does not hold a file or text; it holds one thing in several
      // flavours at once, and ordinary copies carry both. A range of spreadsheet
      // cells is text and a picture of itself; a copied region of a web page is
      // markup and a picture of itself. A page that attached every paste
      // carrying an image would answer Cmd+V in the description field by
      // attaching a PNG of the words the reader meant to paste — silently, since
      // the field would stay empty.
      //
      // A screenshot carries no text flavour at all, which is what makes the
      // discrimination cheap: the paste that is only a file is only ever an
      // attachment, wherever the caret is, and the paste that is also text is
      // left to the field the caret is in.
      function pasteBelongsToTheCaret(event, clipboard) {
        if (!takesTypedText(event.target)) return false;
        return Array.prototype.some.call(clipboard.types || [],
          (flavour) => String(flavour).indexOf("text/") === 0);
      }
      // What a pasted image is called, since nobody called it anything.
      //
      // A file copied out of a folder arrives named. An image put on the
      // clipboard by a screenshot key does not: the engines invent the same
      // placeholder for it, so a task that took three screenshots would hold
      // three rows called image.png and a reader would have to open each one to
      // tell them apart. Those are named for the moment they were pasted, which
      // is the only thing that distinguishes them and the only thing this page
      // knows about them.
      //
      // The rule is deliberately narrow — no name at all, or exactly the
      // placeholder on something that really is an image — so the one file it
      // takes a name from is a real image.png copied out of a folder, which
      // keeps its bytes, its type and its place in the list and loses only a
      // name that reads as an invention anyway.
      const synthesizedPasteName = /^image\.[a-z0-9]+$/i;
      function needsPastedName(file) {
        const name = String((file && file.name) || "").trim();
        if (!name) return true;
        return synthesizedPasteName.test(name) && String(file.type || "").indexOf("image/") === 0;
      }
      // 2026-08-16T13:10:57.412Z → 20260816-131057: sortable, readable, and
      // legal in a file name on every platform an attachment may be saved to.
      function pastedNameStamp(when) {
        return when.toISOString().slice(0, 19).replace(/[-:]/g, "").replace("T", "-");
      }
      function pastedNameExtension(file) {
        const name = String((file && file.name) || "");
        const dot = name.lastIndexOf(".");
        if (dot > 0) return name.slice(dot).toLowerCase();
        // No name to take one from, so the media type is the only description of
        // these bytes there is. A subtype may carry a suffix — image/svg+xml —
        // and only the part before it names the format.
        const subtype = String((file && file.type) || "").split("/")[1] || "";
        const extension = subtype.split("+")[0].replace(/[^a-z0-9]/gi, "").toLowerCase();
        return extension ? "." + extension : "";
      }
      // A File's name is read-only, so naming one is building a new one over the
      // same bytes. A browser that will not build one keeps the file it had: an
      // unhelpful name is worth far less than the attachment.
      function namedFile(file, name) {
        if (typeof File !== "function") return file;
        try {
          return new File([file], name, {
            type: file.type || "",
            lastModified: file.lastModified || Date.now()
          });
        } catch (error) {
          return file;
        }
      }
      function namePastedFiles(files, when) {
        let named = 0;
        return files.map((file) => {
          if (!needsPastedName(file)) return file;
          named += 1;
          // One paste happens at one moment, so a second file named by it would
          // otherwise take the same name to the second.
          return namedFile(file, "pasted-" + pastedNameStamp(when) +
            (named > 1 ? "-" + named : "") + pastedNameExtension(file));
        });
      }
      function attachmentPaste(event) {
        const clipboard = event.clipboardData;
        // Every other paste on this page — text into the title, a URL into the
        // link field, anything at all on the board — is left exactly as it was.
        if (!transferHasFiles(clipboard)) return;
        const surface = livePasteSurface();
        if (!surface) return;
        if (pasteBelongsToTheCaret(event, clipboard)) return;
        // Answered whether or not it is taken, for the reason a refused drag is
        // answered: left to the browser, a pasted file lands wherever the caret
        // is, and over a description that is a screenshot's bytes where a
        // sentence should be.
        event.preventDefault();
        const config = surface.config;
        if (config.busy()) {
          // Said out loud, because a paste has no outline to turn red and no
          // disabled button to have been stopped by: silence would read as the
          // file having been taken.
          config.report(attachmentBusyRefusal("paste"));
          return;
        }
        const pasted = transferredFiles(clipboard);
        if (pasted.files.length === 0) {
          config.report(pasted.folders
            ? skippedFolderNote(pasted.folders)
            : "That paste carried no file to attach.");
          return;
        }
        return takeTransferredFiles(config, {
          files: namePastedFiles(pasted.files, new Date()),
          folders: pasted.folders
        });
      }
      // What a task's attachments weigh, which is the unit the total ceiling is
      // in: a link stores nothing.
      function attachedFileBytes(attachments) {
        return (attachments || []).reduce(
          (total, attachment) => total +
            (attachment.kind === "link" || attachment.dropped ? 0 : Number(attachment.size) || 0), 0);
      }
      // The refusals a browser can make on its own, asked before a file is read,
      // encoded or sent — so a file the server would refuse costs nobody the
      // encode and the upload. The server enforces every one of these and is the
      // authority; this is the reader being told now rather than a megabyte and
      // a round trip later. Both numbers are the server's own, rendered into the
      // page, so there is no second copy of either here to go stale.
      //
      // `held` is what already counts against the total ceiling: what the server
      // holds, on a task's page, and what is already staged, on a create form.
      // Same rule and the same sentence whichever surface asks — and on a create
      // form it is the only place the rule can be asked at all, because the task
      // it would be asked of does not exist yet.
      // What a stored string weighs, which is the unit every one of core's text
      // ceilings is in. Counted through the encoder rather than by `length`
      // because these are stored as UTF-8: a name, a label or a URL written in
      // kanji or emoji is several bytes a character, so `length` would wave
      // through exactly the values these ceilings exist to refuse — and only
      // those, because anything long enough to fail by character count is long
      // enough that nobody types it by accident.
      function attachmentByteLength(value) {
        return new TextEncoder().encode(value || "").length;
      }
      function attachmentFileRefusal(chosen, held) {
        if (attachmentFileLimit && chosen.size > attachmentFileLimit) {
          const [size, ceiling] = comparableSizes(chosen.size, attachmentFileLimit);
          return chosen.name + " is " + size + " and must not exceed " + ceiling +
            "; attach a link instead.";
        }
        if (chosen.size === 0) {
          return chosen.name + " is empty, so there is nothing to attach.";
        }
        if (attachmentTotalLimit && held + chosen.size > attachmentTotalLimit) {
          const [total, ceiling] = comparableSizes(held + chosen.size, attachmentTotalLimit);
          return "Attaching " + chosen.name + " would bring this task to " + total +
            " of attached files and must not exceed " + ceiling + "; attach a link instead.";
        }
        // Asked last, because core asks it last. The sizes above are priced in
        // fileAttachmentOperation, before the bytes are even hashed; the name's
        // *length* is not asked there at all — only its presence and its shape
        // are — and the ceiling on it is a fold-time rule, reached after every
        // size refusal has already had its say. A file that is three megabytes
        // *and* over-named is refused for its size by the server, so a client
        // that led with "rename it" would be handing out advice that does not
        // help against an answer it cannot see.
        if (attachmentNameLimit) {
          const named = attachmentByteLength(chosen.name);
          if (named > attachmentNameLimit) {
            return chosen.name + " has a name of " + named + " bytes and must not exceed " +
              attachmentNameLimit + "; rename it before attaching it.";
          }
        }
        return "";
      }
      // The refusals a link can be given before its task exists, in the order
      // core gives them: the URL's length, then its scheme, then the label
      // beside it. That order is core's own — ValidateAttachmentURL measures
      // before it parses, and the label is bounded later, when the attachment is
      // folded in — and it is worth matching, because a link that is wrong twice
      // should be told the same thing here that it would be told there.
      //
      // Asked at all only where staging happens. A task page sends a link and
      // hears back; a create form would hear back after the task had been made,
      // which is a task created and an attachment missing over a label somebody
      // pasted.
      function stagedLinkRefusal(destination, label) {
        if (attachmentURLLimit) {
          const measured = attachmentByteLength(destination);
          if (measured > attachmentURLLimit) {
            return "That link's URL is " + measured + " bytes and must not exceed " +
              attachmentURLLimit + "; shorten it before attaching it.";
          }
        }
        if (!safeLinkURL(destination)) {
          return "That link must use http or https.";
        }
        if (attachmentLabelLimit && label) {
          const measured = attachmentByteLength(label);
          if (measured > attachmentLabelLimit) {
            return "That link's label is " + measured + " bytes and must not exceed " +
              attachmentLabelLimit + "; shorten it or leave it out.";
          }
        }
        return "";
      }
      // The attachment list: both kinds in one list, because the task holds one
      // list and somebody attaching something is doing one thing.
      function taskAttachments(taskID, task, head, owner) {
        const panel = taskPanel(taskID, "attachments", "Attachments", owner);
        panel.adoptHead = head.adopt;
        // This surface sends. Each file goes as its own write the moment it
        // arrives, which is what a task that already exists can do and a create
        // form cannot.
        const adder = attachmentAdder({
          fileAction: "Add file",
          linkAction: "Attach",
          linkName: "Attach link",
          report: (message) => panel.report(message),
          acceptFiles: (files) => acceptFiles(files),
          acceptLink: (destination, label) => attachLink(destination, label)
        });
        panel.section.append(adder.region, panel.status);
        // The whole section takes a drop, not just the chooser inside it: a
        // reader dragging a screenshot out of Finder aims at the list of things
        // already attached, which is the biggest and most obvious part of this.
        // A paste is the same surface reached with the keyboard, so it is given
        // the same terms rather than a second copy of them.
        const intake = {
          busy: () => panel.busy,
          report: (message) => panel.report(message),
          status: () => panel.status.textContent,
          acceptFiles: (files) => acceptFiles(files)
        };
        attachmentDropZone(panel.section, intake);
        attachmentPasteTarget(panel.section, intake);

        let attachments = [];
        function attachmentPath(attachmentID) {
          return "/api/tasks/" + encodeURIComponent(taskID) + "/attachments/" + encodeURIComponent(attachmentID);
        }
        function attachmentRow(attachment) {
          const row = document.createElement("li");
          row.className = "attachment sidebar-card";
          row.dataset.attachmentId = attachment.id;
          row.rowKey = attachment.id;
          const name = document.createElement("p");
          name.className = "attachment__name";
          if (attachment.kind === "link") {
            const destination = safeLinkURL(attachment.url);
            const shown = attachment.label || attachment.url || "Unnamed link";
            if (destination) {
              const link = document.createElement("a");
              link.href = destination;
              // A new tab and no referrer: an attached link is somebody else's
              // address, and this board has no business handing it either this
              // window or the address the reader is on.
              link.target = "_blank";
              link.rel = "noopener noreferrer nofollow";
              if (attachment.url) link.title = attachment.url;
              text(link, shown);
              name.append(link);
            } else {
              if (attachment.url) name.title = attachment.url;
              text(name, shown);
            }
          } else if (attachment.dropped) {
            // Its bytes are gone, so there is nothing to link to: the route
            // would only answer 410.
            text(name, attachmentShownName(attachment));
          } else {
            const link = document.createElement("a");
            link.href = attachmentPath(attachment.id);
            // The download route decides whether this opens or saves, and it
            // opens in a tab of its own either way so the task stays where it is.
            link.target = "_blank";
            link.rel = "noopener noreferrer";
            text(link, attachmentShownName(attachment));
            name.append(link);
          }
          const meta = document.createElement("p");
          meta.className = "attachment__meta";
          const described = attachmentMeta(attachment);
          text(meta, described.line);
          meta.title = described.full;
          const remove = panel.claim(rowRemoveButton("Remove " + attachmentShownName(attachment)));
          remove.className += " attachment__remove";
          remove.addEventListener("click", async () => {
            const settle = holdSidebarFocus(remove, panel.list, row, adder.url);
            settle(Boolean(await writeThreadChange(
              panel, "DELETE", attachmentPath(attachment.id), headed(head, {}), "that removal")));
          });
          row.removeButton = remove;
          row.append(name, meta, remove);
          return row;
        }
        // What the list was last drawn from. Every read of the board that
        // changed any task brings this panel the task again, and rebuilding
        // rows that had not changed destroyed the × under a keyboard reader's
        // caret — the one the write before it had just put there. Unchanged,
        // the rows stand, as the dependency and assignment lists' do.
        let drawn = null;
        function render() {
          const signature = JSON.stringify(attachments);
          if (signature === drawn) return;
          drawn = signature;
          panel.resetControls();
          const rows = attachments.map(attachmentRow);
          panel.claim(adder.addFile);
          panel.claim(adder.addLink);
          // Nothing attached is the heading and the controls: no count of
          // nothing, and no sentence explaining an empty list.
          panel.setCount(attachments.length === 0
            ? ""
            : attachments.length + " attached · " + attachmentSize(attachedFileBytes(attachments)) + " of files");
          panel.empty.hidden = true;
          panel.list.replaceChildren(...rows);
        }
        // Every file this panel is handed, sent one at a time in the order they
        // arrived. The chooser only ever hands it one; a drop hands it as many
        // as were dragged, and each write moves the head the next one names, so
        // they go in sequence rather than at once. A file that is refused ends
        // the run: the ones before it are attached, the rest are not, and the
        // reader is told which one stopped it rather than being handed a run of
        // identical refusals.
        async function acceptFiles(files) {
          if (panel.busy) return;
          for (const chosen of files) {
            const refusal = attachmentFileRefusal(chosen, attachedFileBytes(attachments));
            if (refusal) {
              panel.report(refusal);
              return;
            }
            panel.setBusy(true);
            let encoded;
            try {
              encoded = await readFileAsBase64(chosen);
            } catch (error) {
              panel.setBusy(false);
              if (panel.owns()) panel.report(error instanceof Error ? error.message : "The file could not be read.");
              return;
            }
            panel.setBusy(false);
            const answer = await writeThreadChange(
              panel, "POST", "/api/tasks/" + encodeURIComponent(taskID) + "/attachments",
              // No media type. The file name decides it, through the table core
              // keeps, because a media type is written into shared history and
              // the browser's guess for the same bytes differs from machine to
              // machine — two clones attaching one file have to label it once.
              headed(head, { kind: "file", name: chosen.name, content: encoded }),
              "that attachment");
            if (!answer) return;
          }
          adder.clearFile();
        }
        async function attachLink(destination, label) {
          if (panel.busy) return;
          const values = { kind: "link", url: destination };
          if (label) values.label = label;
          const answer = await writeThreadChange(
            panel, "POST", "/api/tasks/" + encodeURIComponent(taskID) + "/attachments",
            headed(head, values), "that link");
          if (!answer) return false;
          adder.clearLink();
          return true;
        }
        panel.follow = (held) => {
          attachments = (held && held.attachments) || [];
          render();
        };
        panel.follow(task);
        return panel;
      }
      // ─── Attachments staged on a New Task form ──────────────────────────────
      //
      // A staged attachment is a File the browser handed this page, held by the
      // form the reader is standing in and written nowhere else. That is the
      // whole of the storage, and it is why the failure path below never
      // navigates: a typed draft can be handed back through a notice, and a File
      // cannot — it leaves with the node that holds it.
      function newAttachmentDraftState() {
        return { items: [], sequence: 0 };
      }
      function hasAttachmentDrafts(draftState) {
        return Boolean(draftState) && draftState.items.length > 0;
      }
      function stagedFileBytes(draftState) {
        return draftState.items.reduce(
          (total, item) => total + (item.kind === "link" ? 0 : item.size), 0);
      }
      // Every staged attachment, sent to the task the create just made.
      //
      // Sequential, in the order they were staged, because each write moves the
      // task's ref and because a reader who staged a screenshot before a log
      // gets them back in that order. One failure does not stop the run: the
      // ceiling that refuses the third file has nothing to say about the fourth,
      // and a reader is owed a reason for every item rather than one reason and
      // a queue of untried ones.
      //
      // What lands leaves the list; what fails stays in it, in place, carrying
      // the reason it failed. So the list after this call is exactly the work
      // still outstanding, which is what makes a retry send each thing once.
      async function persistAttachmentDrafts(taskID, draftState) {
        const path = "/api/tasks/" + encodeURIComponent(taskID) + "/attachments";
        const warnings = [];
        const failures = [];
        // Walked over a copy: a landed item is taken out of the list as it
        // lands, and a walk over the live array would step past its neighbour.
        for (const item of [...draftState.items]) {
          let values;
          if (item.kind === "link") {
            values = { kind: "link", url: item.url };
            if (item.label) values.label = item.label;
          } else {
            try {
              // No media type, for the reason the task page sends none: the
              // name decides it through the table core keeps, and the browser's
              // guess for the same bytes differs from machine to machine.
              values = { kind: "file", name: item.name, content: await readFileAsBase64(item.file) };
            } catch (error) {
              item.error = error instanceof Error ? error.message : "The file could not be read.";
              failures.push(item);
              continue;
            }
          }
          try {
            // No expectedHead. The task was made moments ago by this page and
            // nobody else has heard of it yet, so there is no concurrent version
            // to be refused against — exactly as the relationship drafts beside
            // these are written.
            const answer = await mutateTask("POST", path, values);
            (answer.warnings || []).forEach((warning) => warnings.push(warning.message));
            item.error = "";
            draftState.items = draftState.items.filter((held) => held !== item);
          } catch (error) {
            item.error = error instanceof Error ? error.message : "That attachment was not attached.";
            failures.push(item);
          }
        }
        return { failures, warnings };
      }
      // The staged list, drawn as the panel a task's own page draws, in the
      // sidebar a New Task form has instead of one.
      function taskAttachmentDrafts(draftState) {
        // It owns itself: this panel is destroyed with the route it is in, and
        // no poll writes to it, so there is no other node it could be reporting
        // on behalf of.
        const panel = taskPanel("", "attachments", "Attachments", () => true);
        panel.section.className += " task-panel--staged";
        const adder = attachmentAdder({
          multiple: true,
          fileAction: "Add file",
          linkAction: "Add",
          linkName: "Add link",
          report: (message) => panel.report(message),
          acceptFiles: (files) => acceptFiles(files),
          acceptLink: (destination, label) => acceptLink(destination, label)
        });
        panel.section.append(adder.region, panel.status);
        // The same zone over the same controls, and the same paste. A drop or a
        // paste here stages, because that is what acceptFiles does on this
        // surface — and either is refused while a create run is walking the
        // list, for the reason a mid-run add is: the walk has already read past
        // anything that arrives now.
        const intake = {
          busy: () => panel.busy,
          report: (message) => panel.report(message),
          status: () => panel.status.textContent,
          acceptFiles: (files) => acceptFiles(files)
        };
        attachmentDropZone(panel.section, intake);
        attachmentPasteTarget(panel.section, intake);
        // Take these files and stage them. The entry point a drop target hands
        // a DataTransfer's files to, and the one the chooser goes through, so
        // both arrive by the same door and are judged by the same rules.
        function acceptFiles(files) {
          if (panel.busy) return;
          panel.report("");
          // Each file is judged against what is already staged, so five files
          // that are each under the per-file ceiling are still refused at the
          // one that takes the task over the total. The refused file is named
          // and everything staged before it stays staged: a reader who chose a
          // folder is told which one is the problem rather than losing all of
          // them.
          for (const chosen of files) {
            const refusal = attachmentFileRefusal(chosen, stagedFileBytes(draftState));
            if (refusal) {
              panel.report(refusal);
              break;
            }
            draftState.items.push({
              key: "staged-" + (++draftState.sequence),
              kind: "file",
              name: chosen.name,
              size: chosen.size,
              file: chosen,
              error: ""
            });
          }
          adder.clearFile();
          render();
        }
        function acceptLink(destination, label) {
          if (panel.busy) return false;
          panel.report("");
          // Asked here rather than left to the server, which is the opposite of
          // what the task page does and for a reason that only exists here: the
          // server's refusal would arrive after the create, so a link core will
          // never store would cost the reader a task made and an attachment
          // missing. The rules, their order and their words are core's own.
          const refusal = stagedLinkRefusal(destination, label);
          if (refusal) {
            panel.report(refusal);
            return false;
          }
          draftState.items.push({
            key: "staged-" + (++draftState.sequence),
            kind: "link",
            url: destination,
            label,
            size: 0,
            error: ""
          });
          adder.clearLink();
          render();
          return true;
        }
        function draftRow(item) {
          const row = document.createElement("li");
          row.className = "attachment sidebar-card";
          row.dataset.attachmentDraft = item.key;
          row.rowKey = item.key;
          const name = document.createElement("p");
          name.className = "attachment__name";
          // Drawn as text rather than as a link, both kinds of it. A staged file
          // has no route to download from, and a staged link is a destination
          // this page has not stored yet — offering either as something to
          // follow would be offering a state that does not exist.
          const shown = item.kind === "link" ? (item.label || item.url) : item.name;
          text(name, shown);
          if (item.kind === "link") name.title = item.url;
          const meta = document.createElement("p");
          meta.className = "attachment__meta";
          text(meta, item.kind === "link"
            ? "link"
            : "file · " + attachmentSize(item.size));
          const remove = panel.claim(rowRemoveButton("Remove " + shown));
          remove.className += " attachment__remove";
          remove.addEventListener("click", () => {
            // Asked as well as disabled, for the reason the adders ask it: a
            // run in flight is walking this list as it stood when it started,
            // so a row taken out mid-walk is a row that gets attached anyway
            // while the reader watches it leave the screen. The disabled
            // attribute is what a browser honours; this is what makes it true.
            if (panel.busy) return;
            const settle = holdSidebarFocus(remove, panel.list, row, adder.url);
            draftState.items = draftState.items.filter((held) => held !== item);
            panel.report("");
            render();
            settle(true);
          });
          row.removeButton = remove;
          row.append(name, meta, remove);
          if (item.error) {
            const failure = document.createElement("p");
            failure.className = "attachment__failure";
            failure.dataset.attachmentFailure = "";
            text(failure, item.error);
            row.append(failure);
          }
          return row;
        }
        function render() {
          panel.resetControls();
          const rows = draftState.items.map(draftRow);
          panel.claim(adder.addFile);
          panel.claim(adder.addLink);
          // An empty list says nothing, as the task page's does.
          panel.setCount(draftState.items.length === 0
            ? ""
            : draftState.items.length + " staged · " +
              attachmentSize(stagedFileBytes(draftState)) + " of files");
          panel.empty.hidden = true;
          panel.list.replaceChildren(...rows);
          // Announced after the list is drawn, so whatever the form does with
          // this is deciding against a list that is already on the screen.
          controller.onChange();
        }
        const controller = {
          section: panel.section,
          render,
          acceptFiles,
          setBusy: (value) => panel.setBusy(value),
          // What the form wants to know when this list changes under it. The
          // default is the answer for a list nothing else is watching.
          onChange() {}
        };
        render();
        return controller;
      }
