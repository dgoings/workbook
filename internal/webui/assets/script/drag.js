      // The list a drop would land in: a status column, or the Deleted column,
      // which names no status and so carries a marker of its own. Asked as two
      // lookups rather than one selector because they are two different
      // answers, and the caller has to know which it got.
      function dropTarget(event) {
        return event.target.closest("[data-drop-status]") || event.target.closest("[data-drop-deleted]");
      }
      function dropsIntoDeleted(target) {
        return Boolean(target && target.dataset.dropDeleted);
      }
      function placementFor(list, clientY) {
        // A card the server has not accepted yet is not a placement neighbour:
        // naming it in a before/after would ask the server to order against a
        // task it has never heard of.
        const cards = [...list.querySelectorAll(".task-card")]
          .filter((item) => item.dataset.taskId !== activeDrag.taskId &&
            item.dataset.pendingCreate !== "true");
        let intended = cards.findIndex((item) => {
          const bounds = item.getBoundingClientRect();
          return clientY < (bounds.top + bounds.bottom) / 2;
        });
        if (intended < 0) intended = cards.length;

        const peers = cards.filter((item) => item.dataset.priority === activeDrag.priority);
        if (peers.length === 0) {
          const draggedOrder = priorityOrder.get(activeDrag.priority);
          let index = cards.findIndex((item) => priorityOrder.get(item.dataset.priority) > draggedOrder);
          if (index < 0) index = cards.length;
          return { status: list.dataset.dropStatus, reference: cards[index] || null };
        }

        const first = cards.indexOf(peers[0]);
        const last = cards.indexOf(peers[peers.length - 1]);
        const index = Math.max(first, Math.min(intended, last + 1));
        if (index <= first) {
          return {
            status: list.dataset.dropStatus,
            before: peers[0].dataset.taskId,
            reference: peers[0]
          };
        }
        if (index > last) {
          return {
            status: list.dataset.dropStatus,
            after: peers[peers.length - 1].dataset.taskId,
            reference: cards[index] || null
          };
        }
        return {
          status: list.dataset.dropStatus,
          before: cards[index].dataset.taskId,
          reference: cards[index]
        };
      }
      // Idempotent on purpose, because the frame loop asks for this on every
      // frame it moves a scroller and the answer is usually the one already on
      // screen.
      //
      // Taking the line out of the document and putting it back exactly where
      // it was is still a mutation under a drag cursor, and a mutation under a
      // drag cursor is what makes a browser re-run its drag hit test and
      // dispatch another dragenter/dragleave pair. Sixty of those a second, for
      // a line that has not moved, is work asked of the browser for nothing —
      // and the pair is not harmless, because a gesture whose last word from
      // the browser is a dragleave has had its drop target withdrawn and the
      // release that follows is discarded.
      //
      // How much of that reaches a reader is not settled. Refusals were seen
      // here on Chrome 151 while the line was rebuilt every frame and not seen
      // after, but a second harness driving fewer drag events per gesture saw
      // none either way, so the counts are an observation and not a
      // demonstration. The reason to do it is the one above: not mutating the
      // document sixty times a second to say nothing.
      function showPlacement(list, placement) {
        const reference = placement.reference || null;
        if (dropMarker.parentElement === list && dropMarker.nextElementSibling === reference) return;
        dropMarker.remove();
        list.insertBefore(dropMarker, reference);
      }
      function clearPlacement() {
        dropMarker.remove();
      }
      // How deep into a column's top or bottom edge the cursor has to be before
      // the drag scrolls it, and how fast that scroll runs.
      //
      // The zone is a fraction of the scroller with a ceiling on it rather than
      // a fixed number of pixels. A fixed edge zone is a different proposition
      // on every viewport: 72px of a tall column is a strip, and 72px of each
      // end of a short one leaves a reader on a phone-height window with almost
      // nowhere the cursor can rest without the column moving. Taking a quarter
      // of the scroller from each end keeps the still middle at least half the
      // column at every height, and the ceiling stops a very tall column from
      // turning most of itself into an edge.
      const dragScrollEdgeFraction = 0.25;
      const dragScrollEdgeMax = 72;
      // Pixels per second at the outer edge, ramped linearly from nothing at
      // the zone's inner boundary. Speed comes from how far the reader pushes
      // rather than from how long they wait, so a long reorder is one shove and
      // a short one is a nudge. At this rate a full 900px column passes in
      // about a second.
      const dragScrollMaxSpeed = 900;
      // A frame longer than this is one nobody watched — a backgrounded tab, or
      // a busy main thread — and scrolling for its whole duration would jump the
      // column past everything the reader meant to drop between.
      const dragScrollMaxStep = 50;
      // How hard the cursor is pushing into one of a scroller's two edges, as a
      // signed fraction of the way across the edge zone: -1 at the near edge
      // and anywhere past it, +1 at the far edge and anywhere past it, and 0
      // through the whole still middle between them.
      //
      // Both axes ask this one function. A board that slid sideways on a
      // different ramp from the one a column scrolls down on would be two
      // behaviours a reader has to learn separately, and near a corner they
      // would be two behaviours happening at once.
      //
      // A cursor a hair past either end counts as being at that end rather than
      // as being in neither zone. The two zones cannot overlap and leave a gap
      // between them, because each is at most a quarter of the span.
      function dragScrollRate(pointer, near, far) {
        const span = far - near;
        if (!(span > 0)) return 0;
        const zone = Math.min(dragScrollEdgeMax, span * dragScrollEdgeFraction);
        const intoNear = pointer - near;
        const intoFar = far - pointer;
        if (intoNear < zone) return -Math.min(1, (zone - intoNear) / zone);
        if (intoFar < zone) return Math.min(1, (zone - intoFar) / zone);
        return 0;
      }
      // What a rate is worth in pixels over a frame of this length.
      function dragScrollTravel(rate, elapsed) {
        return rate * dragScrollMaxSpeed * (elapsed / 1000);
      }
      // The distance this column should travel this frame: negative up,
      // positive down, and zero anywhere in the still middle.
      function dragScrollDistance(scroller, pointerY, elapsed) {
        const bounds = scroller.getBoundingClientRect();
        return dragScrollTravel(dragScrollRate(pointerY, bounds.top, bounds.bottom), elapsed);
      }
      // How hard the held cursor is asking the board track to slide sideways,
      // and whether it is asking at all.
      //
      // The frame of reference is the track's own box rather than the window's.
      // The track is the thing that scrolls, and its left and right edges are
      // where the columns the window cannot show begin — so a cursor pushed out
      // past the window's edge is past the track's edge too and asks for the
      // full rate, exactly as a cursor shoved past a column's fold asks for
      // that column's full rate. Neither is a gesture anything under the cursor
      // could answer, and both are the reader saying "keep going".
      //
      // Which column the cursor happens to be over decides nothing here. The
      // zone is a property of the track's viewport, so it is the same 72px
      // whether the columns are 16rem or 26rem wide and whether the cursor is
      // over a column, over the gutter between two of them, or past the last
      // one — a reader dragging toward the right edge is asking for the same
      // thing in all three places, and a rule that read the column under the
      // cursor would stall the gesture in the gaps.
      //
      // Out through the track's top or bottom is a departure rather than a
      // push, and stops it. A reader who has carried the card up over the
      // header or down below the track has left the board, and a board still
      // sliding under them would be answering a gesture nobody is making. That
      // is the column loop's sideways rule, turned a quarter turn.
      //
      // Sideways, the box is clipped to the window before the zone is measured
      // off it, because the box can hang off the screen.
      //
      // The page has a floor — body { min-width: 20rem } — and once the window
      // is narrower than that floor the document itself scrolls sideways and
      // carries the track's right edge off the screen with it. The floor is in
      // rem, so it moves with the reader's own default font size rather than
      // sitting at one pixel width: 320px at the usual 16px root, 480px at the
      // 24px root Chrome calls "Very Large". A zone measured from the raw box
      // would then be somewhere no cursor can reach, and the readers it fails
      // are the ones on the narrowest windows and the largest type.
      //
      // Measured on this branch with no clip, 800ms held at the rightmost pixel
      // the cursor can occupy: 712px of travel at a 320px viewport, 92px at
      // 240px, 0px at 220px; and at 390x844, 109px at a 24px root and 0px at a
      // 28px one. With the clip every one of those is back to the full rate,
      // and every case that already worked is unchanged.
      //
      // Nothing clips the vertical axis, because nothing can move it off the
      // screen: body is height:100dvh with overflow hidden, so the document
      // never scrolls down and the track's top and bottom are the window's.
      function boardScrollRate() {
        const bounds = board.getBoundingClientRect();
        if (dragPointerY < bounds.top || dragPointerY > bounds.bottom) return 0;
        return dragScrollRate(
          dragPointerX,
          Math.max(0, bounds.left),
          Math.min(window.innerWidth, bounds.right)
        );
      }
      // Draw the line this destination would take, or take none away.
      //
      // One rule, asked by the listeners answering an event and by the frame
      // loop answering for a cursor no event is reporting. The loop's case is
      // the one that needs saying: the cards have moved under a cursor that has
      // not, so a line drawn on the last dragover is answering a question about
      // a column that no longer looks like that, and there may never be another
      // dragover to correct it.
      //
      // Every caller hands it a destination dragDestination() has already
      // approved, so there is no column to refuse here — the refusing is done
      // before the line is ever asked for.
      function showDragPlacement(target, pointerY) {
        if (dropsIntoDeleted(target)) {
          // No drop marker. The column orders itself by when each task was
          // deleted, and a line drawn between two cards would promise a
          // position this drop does not name and the column would not keep.
          clearPlacement();
          return;
        }
        showPlacement(target, placementFor(target, pointerY));
      }
      // Which column the held cursor is over now, after the track has slid a
      // different one under it.
      //
      // A column scrolling under a still cursor changes which cards are there;
      // a track sliding under one changes which column is there, and that is a
      // harder question because the page's answer to it comes only from a
      // dragenter. A browser dispatches those when it feels like it, and a drop
      // does not consult them at all — it hit-tests for itself and lands where
      // the cursor actually is. So the loop asks the document the same question
      // the drop will ask, through the same dragDestination() the listeners
      // use, and the line, the column being scrolled and the drop then agree by
      // construction rather than by luck. Measured before this: a card carried
      // to a column three tracks off the right of the window landed in that
      // column while the line was still drawn in the column the slide started
      // over.
      function retargetDragScroller() {
        const under = document.elementFromPoint(dragPointerX, dragPointerY);
        const target = under ? dragDestination({ target: under }) : null;
        // A different column is a different scroller, and the fraction of a
        // pixel the last one would not take is not owed to this one.
        if (target !== dragScroller) dragScrollCarry = 0;
        dragScroller = target;
        // Over the gutter between two columns, or past the last one: nothing to
        // scroll down and no landing place to promise, while the track keeps
        // sliding toward the one the reader is heading for.
        if (!target) {
          clearPlacement();
          return;
        }
        showDragPlacement(target, dragPointerY);
      }
      // One frame, both axes, from the one remembered cursor.
      //
      // Near a corner the reader is asking for both at once — the column coming
      // up under the card while the track slides the column they are heading
      // for into view — and they are different scrollers on different axes, so
      // neither is ever in the other's way.
      function dragScrollTick(timestamp) {
        dragScrollFrame = null;
        // The gesture ended between frames. Returning without asking for
        // another is what stops the loop.
        if (!activeDrag) return;
        const elapsed = dragScrollLast === null
          ? 0
          : Math.min(dragScrollMaxStep, Math.max(0, timestamp - dragScrollLast));
        dragScrollLast = timestamp;
        const scroller = dragScroller;
        if (scroller) {
          const distance = dragScrollDistance(scroller, dragPointerY, elapsed);
          if (distance !== 0) {
            const limit = Math.max(0, scroller.scrollHeight - scroller.clientHeight);
            const from = scroller.scrollTop;
            const wanted = Math.max(0, Math.min(limit, from + distance + dragScrollCarry));
            scroller.scrollTop = wanted;
            // Read back rather than assumed: the carry is whatever this scroller
            // would not take, which at either end of its travel is nothing.
            dragScrollCarry = wanted - scroller.scrollTop;
            if (scroller.scrollTop !== from) showDragPlacement(scroller, dragPointerY);
          }
        }
        // The rate rather than the distance decides whether the track is being
        // asked for, because the first frame of a loop measures no elapsed time
        // and every distance on it is zero.
        const rate = boardScrollRate();
        if (rate !== 0) {
          const distance = dragScrollTravel(rate, elapsed);
          const limit = Math.max(0, board.scrollWidth - board.clientWidth);
          const from = board.scrollLeft;
          const wanted = Math.max(0, Math.min(limit, from + distance + boardScrollCarry));
          board.scrollLeft = wanted;
          // Chrome rounds an assigned scrollLeft exactly as it rounds an
          // assigned scrollTop, so the track needs the same carry the column
          // needs, kept apart from it.
          boardScrollCarry = wanted - board.scrollLeft;
          // A whole column may have changed under the cursor, not just the
          // cards in one, so this asks the harder question rather than
          // redrawing the line where it already was. It also puts back a line a
          // churn dragleave cleared.
          //
          // Asked on every frame the reader is pushing, not only on frames the
          // track actually moved. At either end of the travel the track stops
          // while the reader is still asking, and the board underneath does not
          // hold still with it: a poll re-render can put a different column
          // under a cursor that has not moved, over a track that cannot. That
          // was observed — cursor in the track's bottom padding at the left
          // edge, a re-render sliding a column under it, and its scrollTop
          // sitting at 0 for 400ms with nothing left to notice. The cost is one
          // hit test a frame, and the line it draws is idempotent, so a frame
          // where nothing changed touches nothing.
          retargetDragScroller();
        }
        // Nothing is asking for another frame: no column is being tracked and
        // the cursor is nowhere near the track's edges. An event starts the
        // loop again the moment the reader pushes somewhere that wants it, so
        // idling on here would be a loop with no reason to live.
        if (!scroller && rate === 0) {
          stopDragScroll();
          return;
        }
        dragScrollFrame = requestAnimationFrame(dragScrollTick);
      }
      function startDragScroll() {
        if (dragScrollFrame !== null) return;
        // No elapsed time until a second frame has been seen. The first
        // timestamp is a page-lifetime clock, and treating the whole of it as
        // one frame would scroll the capped step before the reader had moved.
        dragScrollLast = null;
        dragScrollCarry = 0;
        boardScrollCarry = 0;
        dragScrollFrame = requestAnimationFrame(dragScrollTick);
      }
      // Whether an event's cursor is inside this scroller's own box.
      function cursorWithin(scroller, event) {
        const bounds = scroller.getBoundingClientRect();
        return event.clientX >= bounds.left && event.clientX <= bounds.right &&
          event.clientY >= bounds.top && event.clientY <= bounds.bottom;
      }
      // Whether the cursor has been pushed past the top or the bottom of this
      // scroller while still over its width.
      //
      // This is the gesture the story is about — "drag a task above or below
      // the viewport of the column" — and it is the one gesture nothing under
      // the cursor can answer: past the end of a column there is the board's
      // background, or a heading, or the page itself, and none of them takes a
      // drop. Only the column the card came out over can keep moving, so it
      // does, at the full speed of the edge it was pushed past.
      //
      // Sideways is the opposite case and is left alone: a cursor beside the
      // column has gone to another column or to the gutter, and whatever it
      // lands on answers for itself.
      function overshootsVertically(scroller, event) {
        if (typeof event.clientY !== "number") return false;
        const bounds = scroller.getBoundingClientRect();
        // A missing clientX is not taken as a reason to refuse: the vertical
        // test is the one carrying the meaning, and every browser reports both.
        if (typeof event.clientX === "number" &&
          (event.clientX < bounds.left || event.clientX > bounds.right)) return false;
        return event.clientY < bounds.top || event.clientY > bounds.bottom;
      }
      function stopDragScroll() {
        if (dragScrollFrame !== null) cancelAnimationFrame(dragScrollFrame);
        dragScrollFrame = null;
        dragScrollLast = null;
        dragScrollCarry = 0;
        boardScrollCarry = 0;
        dragScroller = null;
      }
      // The column under the cursor stops being scrolled. The loop only stops
      // with it when the board has no reason of its own to keep running.
      //
      // This is the difference the second axis makes. A cursor that has left a
      // column used to be a cursor that had stopped asking for anything, so the
      // one stop did for both. Now a reader carrying a card into the gutter
      // between two columns while pushing at the track's right edge is still
      // asking loudly — and the track sliding a fresh column under a stationary
      // cursor is itself what takes the cursor off the column it was on, so a
      // stop here would cancel the very scroll that caused it, several times a
      // second.
      function releaseDragScroller() {
        dragScroller = null;
        dragScrollCarry = 0;
        if (activeDrag && boardScrollRate() !== 0) {
          startDragScroll();
          return;
        }
        stopDragScroll();
      }
      // Remember where the cursor is, and run the loop for as long as the drag
      // is asking anything to move.
      //
      // The position is recorded wherever the cursor has got to, including
      // somewhere that takes no drop at all, because the board track answers
      // for the whole window and the places a card is carried to on the way to
      // an off-screen column — the gutter, the page beyond the last column, the
      // strip past the window's edge — are exactly those places.
      //
      // Handed a column, that is the column. Handed nothing, the column it was
      // scrolling is released, and whether the loop goes with it is the board's
      // to say. The exception is the one the vertical story was about: a cursor
      // shoved past the top or bottom of the column it was already scrolling is
      // still asking for that column, and there is nothing under it that could
      // say so.
      function trackDragScroll(scroller, event) {
        if (event && typeof event.clientY === "number") dragPointerY = event.clientY;
        if (event && typeof event.clientX === "number") dragPointerX = event.clientX;
        if (scroller) {
          // A different column is a different scroller, and the fraction of a
          // pixel the last one would not take is not owed to this one.
          if (scroller !== dragScroller) dragScrollCarry = 0;
          dragScroller = scroller;
          startDragScroll();
          return;
        }
        if (activeDrag && dragScroller && event && overshootsVertically(dragScroller, event)) return;
        releaseDragScroller();
      }
