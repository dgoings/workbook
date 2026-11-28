      // What one attached file weighs, in the units somebody prices a download
      // in. One decimal place until the number is large enough not to need one.
      function attachmentSize(bytes) {
        const count = Number(bytes) || 0;
        for (const [unit, size] of [["MiB", 1048576], ["KiB", 1024]]) {
          if (count >= size) {
            const value = count / size;
            return (value >= 10 ? Math.round(value) : Number(value.toFixed(1))) + " " + unit;
          }
        }
        return count + " B";
      }
      // Two sizes as a sentence can tell them apart. A file one byte over the
      // ceiling rounds to the ceiling's own unit — "enormous.bin is 1 MiB and
      // must not exceed 1 MiB" reads as a refusal of something allowed — so a
      // pair the unit cannot separate is given in bytes, which is what the
      // server's own refusal says.
      function comparableSizes(size, ceiling) {
        const shown = attachmentSize(size);
        const bound = attachmentSize(ceiling);
        if (shown !== bound) return [shown, bound];
        return [size + " bytes", ceiling + " bytes"];
      }
      // Whether a stored link may be drawn as a link.
      //
      // Core refuses everything but http and https when somebody attaches one,
      // and that is a rule about authoring rather than about stored documents:
      // a pack written by another clone can carry any URL at all, and this
      // client draws whatever the poll hands it. A `javascript:` href drawn
      // here would be script on the board's own origin, one click away — the
      // same stored cross-site scripting the download route's headers exist to
      // prevent, arriving through the other door. So the scheme is asked again,
      // of every link, and one that fails is drawn as text. A relative URL
      // fails too: parsed without a base it is not a URL, and resolving it
      // against this board would turn somebody else's text into a link to us.
      function safeLinkURL(raw) {
        if (typeof raw !== "string" || raw === "") return "";
        let parsed;
        try { parsed = new URL(raw); } catch (_) { return ""; }
        return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : "";
      }
      // ─── The markdown subset ────────────────────────────────────────────────
      //
      // A renderer over text somebody else typed, which is the whole reason it
      // is written the way it is.
      //
      // It builds nodes. There is no string of markup anywhere in here, no
      // innerHTML, no insertAdjacentHTML, and no place a character out of a task
      // description is ever anything but the content of a text node or the value
      // of one of five attributes this file writes by hand. That is not a
      // precaution against a parser bug — it is the absence of the thing a
      // parser bug would need in order to matter.
      //
      // It is small on purpose. Headings, bold, italic, inline code, fenced
      // code, unordered and ordered lists, blockquotes, links, and images that
      // are attachments of this task. Nothing else is syntax: raw HTML is text,
      // a table is text, a footnote is text, a bare URL is text. Anything the
      // parser does not recognize, and anything it recognizes but cannot
      // complete, is drawn exactly as it was typed. The failure mode is "you
      // see what you wrote", never "the page does something".
      //
      // There is one input that breaks the first half of that, and it is
      // written down rather than left to be discovered: a backslash. There are
      // no escapes here, so `\*text\*` is emphasis with two backslashes in it
      // rather than the literal somebody meant. It is the only place this
      // renderer draws something other than what was typed, the README says so,
      // and it is the first thing to fix if the subset grows.
      //
      // And it is linear. Every scan for a closing delimiter either finds one or
      // records that there is none left to find, so a body made of nine thousand
      // asterisks costs one pass rather than nine thousand of them; both
      // recursions carry a depth bound, so ten thousand nested quotes end in
      // paragraphs rather than in a stack overflow.

      // How deep a blockquote may nest before the rest is drawn as text, and how
      // deep emphasis and link text may. Both are bounds on recursion first and
      // features second: past them the input is drawn literally.
      const markdownQuoteDepth = 6;
      const markdownInlineDepth = 8;
      // Every element this renderer is allowed to build. It is here as a
      // sentence rather than as a check — the code below creates these tags and
      // no others, and the tests assert the rendered tree against this list:
      // p, h3, h4, h5, h6, strong, em, code, pre, ul, ol, li, blockquote, a,
      // img, br, and, only where the context asks for tables (a relic body),
      // table, thead, tbody, tr, th and td. Every attribute it is allowed to
      // set: href, rel and target on an anchor, src and alt on an image. No
      // class, no id, no data, no style, and no event listener is ever
      // attached to anything built here.

      // Every character of a rendering is drawn without its direction controls;
      // see withoutDirectionControls.
      function markdownText(value) { return document.createTextNode(withoutDirectionControls(value)); }
      function markdownNode(tagName, children) {
        const element = document.createElement(tagName);
        if (children && children.length) element.append(...children);
        return element;
      }
      // The attachment a reference names, or null.
      //
      // Files only, and by full identifier or by any prefix that names exactly
      // one of them — the same rule the command line resolves a comment or an
      // attachment identifier by, so a reference typed there reads here. A
      // prefix that matches two attachments names neither: an ambiguous
      // reference resolves to nothing and is drawn as the text it is. A file
      // compaction dropped is not one: its bytes are gone, so a reference to it
      // is drawn as text too.
      function markdownAttachment(attachments, reference) {
        const wanted = String(reference || "").trim().toUpperCase();
        if (!wanted) return null;
        const files = (attachments || []).filter(
          (attachment) => attachment && attachment.kind !== "link" && !attachment.dropped &&
            typeof attachment.id === "string");
        const exact = files.find((attachment) => attachment.id.toUpperCase() === wanted);
        if (exact) return exact;
        const matches = files.filter((attachment) => attachment.id.toUpperCase().startsWith(wanted));
        return matches.length === 1 ? matches[0] : null;
      }
      // An image, which on this page means an attachment of this task and
      // nothing else.
      //
      // `![alt](attachment:<id>)` is the only image syntax there is. An external
      // URL is not one — a board that fetched images named in task text would
      // report every reader of every card to whoever wrote the task, which is
      // the reason the design says attachments only — and neither is a data URI
      // or anything else.
      //
      // A reference that resolves to a file the download route serves inline
      // becomes an <img> pointing at that route. One that resolves to a file it
      // does not — an SVG, a PDF, a text file — becomes a link to the same
      // route instead, which answers those with `Content-Disposition:
      // attachment` and nosniff, so the file downloads rather than renders. That
      // degradation is the whole of the SVG question: an SVG is markup that can
      // carry script, the route already refuses to hand it back inline, and an
      // <img> at that address would draw nothing anyway. A reference that
      // resolves to nothing at all is drawn as the text somebody typed.
      function markdownImage(alt, target, context) {
        const reference = /^attachment:(.+)$/i.exec(target);
        if (!reference || !context || !context.taskID) return null;
        const attachment = markdownAttachment(context.attachments, reference[1]);
        if (!attachment) return null;
        const address = "/api/tasks/" + encodeURIComponent(context.taskID) +
          "/attachments/" + encodeURIComponent(attachment.id);
        const caption = shownLine(alt);
        if (!inlineImageMedia.has(attachment.media)) {
          // The link reads as the file's real name and nothing else. A reader
          // decides from that name whether to open what it downloads, and the
          // author's own words in its place would let
          // `![invoice.pdf](attachment:<id>)` draw "invoice.pdf" over a file
          // named "invoice<U+202E>fdp.exe" that saves as an .exe. What the
          // author wrote is kept beside it as a caption, in parentheses, when
          // it says anything the name does not.
          const shown = shownFileName(attachment.name) || "attachment";
          const link = markdownNode("a", [markdownText(shown)]);
          link.href = address;
          link.target = "_blank";
          link.rel = "noopener noreferrer nofollow";
          if (!caption || caption === shown) return [link];
          return [link, markdownText(" (" + caption + ")")];
        }
        const image = markdownNode("img");
        image.src = address;
        // The alternative text is the file's real name too, with what the
        // author wrote beside it in parentheses — the rule the link above
        // follows, and for the same reason: an alt is what a browser draws in
        // place of a file that does not decode as an image, so the author's
        // own words alone would let `![invoice.pdf](attachment:<id>)` caption a
        // broken image over a file that saves as an .exe.
        const named = shownFileName(attachment.name) || "attachment";
        image.alt = !caption || caption === named ? named : named + " (" + caption + ")";
        return [image];
      }
      // A link, or null when the target is not one this page will draw as a
      // link. safeLinkURL is the same gate the attachment list asks — http and
      // https and nothing else — and nofollow is added because this text is
      // somebody's and the board is not lending it any standing.
      function markdownLink(target) {
        const destination = safeLinkURL(target);
        if (!destination) return null;
        const link = markdownNode("a");
        link.href = destination;
        link.target = "_blank";
        link.rel = "noopener noreferrer nofollow";
        return link;
      }
      // One line's worth of inline content, as an array of nodes.
      //
      // `context.inline` is a card: it renders emphasis and code, and draws a
      // link or an image as its label text with no anchor and no image. A card
      // is itself a link to the task and is clamped to six lines; a second link
      // inside it is a mis-click, and an image inside it is a card of unknown
      // height.
      function markdownInline(source, context, depth, links) {
        const text = String(source);
        const nodes = [];
        const exhausted = new Set();
        let plain = "";
        let index = 0;
        function flush() {
          if (plain === "") return;
          nodes.push(markdownText(plain));
          plain = "";
        }
        // The next occurrence of a delimiter, remembering the ones that have run
        // out. A search that fails from here fails from anywhere later, because
        // every later search looks at less of the string.
        function closerAt(mark, from) {
          if (exhausted.has(mark)) return -1;
          const at = text.indexOf(mark, from);
          if (at < 0) exhausted.add(mark);
          return at;
        }
        // The label of a link or an image, or null when there is not one here.
        //
        // The label ends at the first `]` after the opener and nowhere else,
        // and it may hold no bracket of either kind. That rule is the whole of
        // this function and it exists because the looser one forged text: an
        // earlier version walked forward to the first `]` that happened to be
        // followed by a target, which let
        //
        //   Invoice from [ACME Corporation, Inc.] please see [details](https://evil.test)
        //
        // draw "ACME Corporation, Inc.] please see [details" as the caption of a
        // link to evil.test, and let the inner target of a nested pair take the
        // outer pair's words. Both are the one thing this renderer must never
        // do — show a reader something the author did not write — and both come
        // from letting a `]` pair with a `[` that is not its own.
        //
        // A refusal here is not a failure to find a link; it is the answer that
        // this was not one. Nothing is skipped and no cursor moves, so the
        // brackets that follow are read on their own terms: in the line above,
        // the first `[` is drawn as a bracket and `[details](https://evil.test)`
        // then becomes exactly the link it says it is.
        function bracketSpan(open) {
          const close = closerAt("]", open);
          if (close < 0 || text[close + 1] !== "(") return null;
          const label = text.slice(open, close);
          if (label.includes("[")) return null;
          const end = closerAt(")", close + 2);
          if (end < 0) return null;
          const target = text.slice(close + 2, end);
          // A target is one word. Whitespace in it, or a bracket of either
          // kind, means this was not a link — CommonMark would take an angled
          // target here and this subset does not, so it is drawn as text.
          if (/[\s()<>]/.test(target)) return null;
          return { label, target, end };
        }
        while (index < text.length) {
          // Past the bound, the rest of the line is what it says it is. This is
          // the floor under both recursions below.
          if (depth >= markdownInlineDepth) { plain += text.slice(index); break; }
          const character = text[index];
          if (character === "`") {
            let run = 1;
            while (text[index + run] === "`") run += 1;
            const fence = "`".repeat(run);
            const close = closerAt(fence, index + run);
            if (close > index + run) {
              flush();
              // The content of a code span is characters, full stop: no
              // emphasis inside it, no link inside it, and every marker in it is
              // one of its own characters. This is what makes "a link wrapping
              // code wrapping emphasis" resolve the only way it can.
              nodes.push(markdownNode("code", [markdownText(text.slice(index + run, close))]));
              index = close + run;
              continue;
            }
            plain += fence;
            index += run;
            continue;
          }
          if (character === "[" || (character === "!" && text[index + 1] === "[")) {
            const image = character === "!";
            const open = index + (image ? 2 : 1);
            const span = links ? bracketSpan(open) : null;
            if (span) {
              const drawn = image
                ? (context && context.inline ? null : markdownImage(span.label, span.target, context))
                : (context && context.inline ? null : markdownLink(span.target));
              if (image && drawn) {
                flush();
                nodes.push(...drawn);
                index = span.end + 1;
                continue;
              }
              if (!image) {
                // Link text is inline content of its own, with links switched
                // off inside it: a link inside a link is not a thing, and this
                // is also what keeps the recursion from finding new work at
                // every level.
                const label = markdownInline(span.label, context, depth + 1, false);
                flush();
                if (drawn) {
                  drawn.append(...(label.length ? label : [markdownText(span.target)]));
                  nodes.push(drawn);
                } else {
                  // A card, or a target this page will not link to. The words
                  // are drawn; the address is not.
                  nodes.push(...label);
                }
                index = span.end + 1;
                continue;
              }
              // An image on a card, or a reference that resolved to nothing: the
              // source text, exactly as typed.
              plain += text.slice(index, span.end + 1);
              index = span.end + 1;
              continue;
            }
            plain += character;
            index += 1;
            continue;
          }
          if (character === "*" || character === "_") {
            let run = 1;
            while (text[index + run] === character) run += 1;
            // Three or more of a delimiter in a row is not syntax in this
            // subset. It is drawn as the characters it is rather than guessed
            // at, which is the rule everywhere else in here.
            if (run > 2) { plain += character.repeat(run); index += run; continue; }
            // An underscore inside a word is an underscore: `some_long_name` is
            // an identifier, not an italic. An asterisk has no such problem and
            // is left able to emphasize part of a word.
            const intraword = character === "_" && /\w/.test(text[index - 1] || "");
            const mark = character.repeat(run);
            const from = index + run;
            const opens = !intraword && /\S/.test(text[from] || "");
            const close = opens ? closerAt(mark, from) : -1;
            if (close > from) {
              flush();
              nodes.push(markdownNode(run === 2 ? "strong" : "em",
                markdownInline(text.slice(from, close), context, depth + 1, links)));
              index = close + run;
              continue;
            }
            plain += mark;
            index += run;
            continue;
          }
          plain += character;
          index += 1;
        }
        flush();
        return nodes;
      }
      // Several lines of inline content, with the line breaks kept.
      //
      // A paragraph in this subset breaks where the author broke it. That is not
      // CommonMark, where a single newline is a space, and it is deliberate: the
      // descriptions and comments this renders were written in a textarea by
      // people who pressed Return, and every one of them was drawn with its line
      // breaks intact before this renderer existed. Losing them would be the one
      // regression a formatting change could not justify.
      //
      // It also bounds the inline scanner: no emphasis, code span or link spans
      // a newline, so the state each pass carries is one line long.
      //
      // `joined` is a card, where the breaks are dropped and the lines run
      // together with a space between them. That is not a taste, it is a
      // measurement: a card's description is clamped to six lines, and drawing
      // the breaks made a seven-line description take all six of them where the
      // same text collapsed took two — cards grew by up to 87px, and the column
      // holding the longest line grew with them, from 250px of card to 374px.
      // A card is a summary under a clamp; the line breaks are kept where there
      // is room for them, which is the task page and the thread.
      function markdownInlineLines(source, context, joined) {
        const lines = String(source).split("\n");
        const nodes = [];
        lines.forEach((line, at) => {
          if (at > 0) nodes.push(joined ? markdownText(" ") : markdownNode("br"));
          nodes.push(...markdownInline(line, context, 0, true));
        });
        return nodes;
      }
      // Whether a line closes a fence opened with this character and length.
      function markdownClosesFence(line, marker, length) {
        const body = line.replace(/^ {0,3}/, "");
        let run = 0;
        while (body[run] === marker) run += 1;
        return run >= length && body.slice(run).trim() === "";
      }
      const markdownBullet = /^[ \t]{0,7}[-*+][ \t]+(.*)$/;
      const markdownOrdered = /^[ \t]{0,7}\d{1,9}[.)][ \t]+(.*)$/;
      // A heading's text is taken with the blanks after it, which
      // markdownHeadingText then drops: a lazy match that stopped before them
      // would take the square of a long line's length.
      const markdownHeading = /^ {0,3}(#{1,6})(?:[ \t]+(.*))?$/;
      function markdownHeadingText(text) {
        let end = text ? text.length : 0;
        while (end > 0 && (text[end - 1] === " " || text[end - 1] === "\t")) end -= 1;
        return text ? text.slice(0, end) : "";
      }
      const markdownFence = /^ {0,3}(`{3,}|~{3,})(.*)$/;
      const markdownQuote = /^ {0,3}>/;
      // One cell of a pipe table's delimiter row: dashes, with an optional
      // colon at either end.
      const markdownTableDelimiterCell = /^:?-+:?$/;
      // The cells of one table row, without its outer pipes. There are no
      // escapes in this subset, so every pipe divides.
      function markdownTableCells(line) {
        let row = line.trim();
        if (row.startsWith("|")) row = row.slice(1);
        if (row.endsWith("|")) row = row.slice(0, -1);
        return row.split("|").map((cell) => cell.trim());
      }
      // Whether a line is a pipe table's delimiter row: delimiter cells
      // between optional outer pipes, an opening pipe indented by at most
      // three spaces. The row is cut into cells and each is judged by itself,
      // because one pattern for the whole row has runs of blanks it can divide
      // in many ways, and on a long line of a teammate's text it would try
      // them all on the page's own thread.
      function markdownIsTableDelimiter(line) {
        const first = line.search(/\S/);
        if (first < 0) return false;
        if (line[first] === "|" && (first > 3 || line.slice(0, first).includes("\t"))) return false;
        return markdownTableCells(line).every((cell) => markdownTableDelimiterCell.test(cell));
      }
      // Whether a table starts at this line: a row with a pipe in it, then a
      // delimiter row of the same width. Only a context that asks for tables
      // has them — a relic body does, and a task's description and comments do
      // not, so what they draw is what they always drew.
      function markdownStartsTable(lines, at, context) {
        if (!context || context.tables !== true || at + 1 >= lines.length) return false;
        if (!lines[at].includes("|") || !markdownIsTableDelimiter(lines[at + 1])) return false;
        return markdownTableCells(lines[at]).length === markdownTableCells(lines[at + 1]).length;
      }
      // The table that starts at this line, and the line after it. A row with
      // more cells than the header is cut to it. One with fewer is left short,
      // not padded: padding builds rows times columns cells, so a wide header
      // over many short rows would cost the square of the body's length. The
      // alignment colons are read and dropped, because an alignment would be
      // an attribute.
      function markdownTable(lines, at, context) {
        const width = markdownTableCells(lines[at]).length;
        const row = (line, tag) => markdownNode("tr", markdownTableCells(line).slice(0, width).map((cell) =>
          markdownNode(tag, cell ? markdownInline(cell, context, 0, true) : [])));
        const head = markdownNode("thead", [row(lines[at], "th")]);
        const rows = [];
        let scan = at + 2;
        while (scan < lines.length && lines[scan].trim() && lines[scan].includes("|") && !markdownStartsBlock(lines[scan])) {
          rows.push(row(lines[scan], "td"));
          scan += 1;
        }
        return { table: markdownNode("table", rows.length ? [head, markdownNode("tbody", rows)] : [head]), next: scan };
      }
      // Whether a line begins a block other than the paragraph it is being read
      // into, which is what ends that paragraph.
      function markdownStartsBlock(line) {
        return markdownFence.test(line) || markdownHeading.test(line) ||
          markdownQuote.test(line) || markdownBullet.test(line) || markdownOrdered.test(line);
      }
      // The blocks of a document, as an array of elements.
      function markdownBlocks(source, context, depth) {
        const lines = String(source).split(/\r\n|\r|\n/);
        const blocks = [];
        let at = 0;
        while (at < lines.length) {
          const line = lines[at];
          if (!line.trim()) { at += 1; continue; }
          const fence = markdownFence.exec(line);
          // A backtick fence whose info string holds a backtick is not a fence.
          // A tilde fence may hold anything, and the info string is discarded
          // either way: a language name would become a class attribute, and this
          // renderer sets no attribute it does not need.
          if (fence && !(fence[1][0] === "`" && fence[2].includes("`"))) {
            const marker = fence[1][0];
            const length = fence[1].length;
            const body = [];
            let scan = at + 1;
            while (scan < lines.length && !markdownClosesFence(lines[scan], marker, length)) {
              body.push(lines[scan]);
              scan += 1;
            }
            // An unclosed fence runs to the end of the text rather than
            // swallowing the page: everything after it is code and there is
            // nothing left to mis-parse. A second fence inside it is code too,
            // which is what makes interleaved fences boring.
            blocks.push(markdownNode("pre", [markdownNode("code", [markdownText(body.join("\n"))])]));
            at = scan < lines.length ? scan + 1 : scan;
            continue;
          }
          if (markdownStartsTable(lines, at, context)) {
            const drawn = markdownTable(lines, at, context);
            blocks.push(drawn.table);
            at = drawn.next;
            continue;
          }
          const heading = markdownHeading.exec(line);
          if (heading) {
            // Two levels down, so a description's top heading is an h3 under the
            // page's own h2 and a seventh `#` is not a heading at all — it fell
            // out of the pattern above and is a paragraph.
            const level = Math.min(6, heading[1].length + 2);
            blocks.push(markdownNode("h" + level, markdownInline(markdownHeadingText(heading[2]), context, 0, true)));
            at += 1;
            continue;
          }
          if (markdownQuote.test(line) && depth < markdownQuoteDepth) {
            const quoted = [];
            while (at < lines.length && markdownQuote.test(lines[at])) {
              quoted.push(lines[at].replace(/^ {0,3}> ?/, ""));
              at += 1;
            }
            blocks.push(markdownNode("blockquote", markdownBlocks(quoted.join("\n"), context, depth + 1)));
            continue;
          }
          const ordered = markdownOrdered.test(line);
          const pattern = ordered ? markdownOrdered : markdownBullet;
          if (pattern.test(line)) {
            const items = [];
            while (at < lines.length) {
              const item = pattern.exec(lines[at]);
              if (item) { items.push(item[1]); at += 1; continue; }
              // An indented line that is not itself an item continues the one
              // above it, which is how a wrapped sentence stays in its bullet.
              if (items.length && /^[ \t]{2,}\S/.test(lines[at]) && !markdownStartsBlock(lines[at])) {
                items[items.length - 1] += "\n" + lines[at].trim();
                at += 1;
                continue;
              }
              // A blank line ends the list unless the next thing is another item
              // of the same kind, which is how a list with air in it stays one
              // list rather than becoming three.
              if (!lines[at].trim()) {
                let ahead = at;
                while (ahead < lines.length && !lines[ahead].trim()) ahead += 1;
                if (ahead < lines.length && pattern.test(lines[ahead])) { at = ahead; continue; }
              }
              break;
            }
            blocks.push(markdownNode(ordered ? "ol" : "ul", items.map(
              (item) => markdownNode("li", markdownInlineLines(item, context)))));
            continue;
          }
          const paragraph = [];
          while (at < lines.length && lines[at].trim() &&
            !(paragraph.length && (markdownStartsBlock(lines[at]) || markdownStartsTable(lines, at, context)))) {
            paragraph.push(lines[at]);
            at += 1;
          }
          blocks.push(markdownNode("p", markdownInlineLines(paragraph.join("\n"), context)));
        }
        return blocks;
      }
      // Draws markdown into an element, replacing whatever was in it.
      //
      // The element is the caller's — it carries the class, the label and the
      // scrolling — and everything inside it is built here.
      function renderMarkdown(target, source, context) {
        target.replaceChildren(...markdownBlocks(source || "", context || {}, 0));
        return target;
      }
      // Draws the inline half of the subset into an element: a card.
      function renderInlineMarkdown(target, source) {
        target.replaceChildren(...markdownInlineLines(source || "", { inline: true }, true));
        return target;
      }
