package webui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// What a drag does to the column it is held over. A column taller than its own
// viewport can only be reordered across if it scrolls while a card is being
// carried, and the cursor position that decides whether it scrolls arrives on
// dragover — an event that stops the moment the cursor holds still. Everything
// here is about the frame loop that bridges that gap: where it scrolls, how
// fast, what it does to the drop line while the cards move under a cursor that
// has not, and that it is never running once the gesture is over.
//
// The fake DOM has no layout engine, so each test states the geometry a browser
// would have computed and moves it itself. That is enough to pin the arithmetic
// and the lifecycle; that a real Chrome delivers the gesture is checked by
// hand, as it has to be.

const (
	// The column the tests scroll: 30 cards of the same priority, so nothing in
	// the placement clamp is in play and the drop line is free to sit in any
	// gap the cursor reaches.
	dragScrollColumnCards = 30
	dragScrollCarriedID   = "WB-01J00000000000000000000900"
)

// dragScrollTasks is one card to carry, in a column of its own, and a long
// column to carry it into. Everything is medium priority: this file is about
// how far a drag can reach, and a priority boundary would answer a different
// question — one the placement tests already ask.
func dragScrollTasks() []core.Task {
	stamp := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	carried := clientPlacementTask(dragScrollCarriedID, "Carried", core.StatusReady, core.PriorityMedium)
	carried.CreatedAt = stamp
	carried.UpdatedAt = stamp
	carried.Head = "head-carried"
	tasks := []core.Task{carried}
	for i := 0; i < dragScrollColumnCards; i++ {
		task := clientPlacementTask(
			fmt.Sprintf("WB-01J%023d", 910+i),
			fmt.Sprintf("Deep %02d", i),
			core.StatusInProgress,
			core.PriorityMedium,
		)
		task.Rank = fmt.Sprintf("%d/1", i+1)
		task.CreatedAt = stamp
		task.UpdatedAt = stamp
		task.Head = fmt.Sprintf("head-deep-%02d", i)
		tasks = append(tasks, task)
	}
	return tasks
}

// The geometry a browser would have computed, and the two questions every test
// here asks of the drop line. It is prepended to each program body rather than
// folded into the shared board prelude, because only these tests give a column
// a size.
const dragScrollHarness = `
// A card is 80 tall with 20 of gap under it, which is the stack the stylesheet
// draws. The numbers matter only in that the tests do the same arithmetic the
// browser would.
const cardHeight = 80;
const cardPitch = 100;
const columnCards = (list) => list.querySelectorAll(".task-card");
// Give a scroller a size and a window position, and give its cards the boxes a
// browser would report for them.
//
// The boxes are getters rather than values because a scrolled column moves its
// cards up the window and changes nothing else, which is the situation these
// tests exist for: a value captured at setup would describe a column that had
// never moved. The drop marker is drawn with no height of its own — height: 0
// with a negative margin — so inserting it between two cards moves neither of
// them here, exactly as it moves neither of them on the page.
function furnishColumn(list, top, height) {
  list.rect = { top, bottom: top + height, left: 0, right: 320, width: 320 };
  list.clientHeight = height;
  list.scrollHeight = columnCards(list).length * cardPitch;
  columnCards(list).forEach((card) => {
    Object.defineProperty(card, "rect", {
      configurable: true,
      get() {
        const index = columnCards(list).indexOf(card);
        const cardTop = top + index * cardPitch - list.scrollTop;
        return { top: cardTop, bottom: cardTop + cardHeight, left: 0, right: 320, width: 320 };
      }
    });
  });
  return list;
}
// Which gap the drop line is sitting in, counted in cards above it.
function markerGap(list) {
  const at = list.children.findIndex((child) => child.className === "drop-marker");
  if (at < 0) return -1;
  return list.children.slice(0, at).filter((child) => hasClassToken(child, "task-card")).length;
}
// Whether the line is in the gap the cursor is actually in, which is the
// promise the line makes and the drop has to keep: every card above it is one
// the cursor has passed the middle of, and the card below it is one it has not.
// Returns what is wrong, or "" when nothing is.
function markerFollowsCursor(list, pointerY) {
  const gap = markerGap(list);
  if (gap < 0) return "the column drew no drop marker";
  const cards = columnCards(list);
  const middle = (card) => {
    const box = card.getBoundingClientRect();
    return (box.top + box.bottom) / 2;
  };
  if (gap > 0 && middle(cards[gap - 1]) > pointerY) {
    return "the line sits below a card the cursor has not reached";
  }
  if (gap < cards.length && middle(cards[gap]) <= pointerY) {
    return "the line sits above a card the cursor has already passed";
  }
  return "";
}
const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
// The board track's own geometry, and the columns laid across it the way the
// grid lays them out.
//
// Every box here is a getter, for the reason the cards' boxes are: the whole
// point of the track is that the columns move under a cursor that does not, and
// a box captured at setup would describe a board that had never slid. The
// column boxes follow the track's scrollLeft and the card boxes follow both
// that and their own column's scrollTop, which is the corner these tests exist
// to pin.
const boardTrackLeft = 100;
const boardTrackWidth = 480;
const boardColumnWidth = 200;
const boardColumnGap = 12;
const boardTrackTravel = () => boardElement.scrollWidth - boardElement.clientWidth;
function furnishTrack(top, height) {
  boardElement.rect = { left: boardTrackLeft, right: boardTrackLeft + boardTrackWidth, top, bottom: top + height, width: boardTrackWidth };
  boardElement.clientWidth = boardTrackWidth;
  boardElement.scrollWidth = boardLists.length * boardColumnWidth + (boardLists.length - 1) * boardColumnGap;
  boardLists.forEach((list, index) => {
    const boxOf = () => {
      const left = boardTrackLeft + index * (boardColumnWidth + boardColumnGap) - boardElement.scrollLeft;
      return { left, right: left + boardColumnWidth, top, bottom: top + height, width: boardColumnWidth };
    };
    Object.defineProperty(list, "rect", { configurable: true, get: boxOf });
    list.clientHeight = height;
    list.scrollHeight = columnCards(list).length * cardPitch;
    columnCards(list).forEach((card) => {
      Object.defineProperty(card, "rect", {
        configurable: true,
        get() {
          const at = columnCards(list).indexOf(card);
          const box = boxOf();
          const cardTop = top + at * cardPitch - list.scrollTop;
          return { left: box.left, right: box.right, top: cardTop, bottom: cardTop + cardHeight, width: box.width };
        }
      });
    });
  });
}
// Which column the board would hand a cursor at this point, asked the way the
// page asks it — through the document's own hit test rather than through
// arithmetic the test did itself.
const columnAt = (x, y) => {
  const under = document.elementFromPoint(x, y);
  return under ? under.closest("[data-drop-status]") : null;
};
// A drag event that names both coordinates, and lands on whatever is really
// under them. Page chrome stands in for everywhere the board is not.
const trackEvent = (x, y) => ({ target: columnAt(x, y) || main, clientX: x, clientY: y, dataTransfer, preventDefault() {} });
// Park the track so this column sits under a given window x, a stated distance
// in from its own left edge. It is how a test puts one particular column inside
// the track's edge zone without having to know where the project's vocabulary
// happens to place it.
function parkColumnUnder(list, x, inset = 40) {
  boardElement.scrollLeft = 0;
  boardElement.scrollLeft = list.getBoundingClientRect().left - (x - inset);
  return list.getBoundingClientRect();
}
// Where the drop line is drawn on the board, counted in cards above it, or -1
// when the board is drawing none anywhere.
const dropMarkerGapAnywhere = () => {
  for (const list of boardLists) {
    const at = markerGap(list);
    if (at >= 0) return at;
  }
  return -1;
};
// Turn a scroller into one that rounds an assigned offset to the nearest whole
// pixel, the way Chrome does on both axes.
function roundScroller(element, axis) {
  const size = axis === "scrollLeft" ? () => element.scrollWidth - element.clientWidth : () => element.scrollHeight - element.clientHeight;
  let offset = 0;
  Object.defineProperty(element, axis, {
    configurable: true,
    get: () => offset,
    set(value) { offset = Math.round(Math.max(0, Math.min(Math.max(0, size()), Number(value) || 0))); }
  });
}
// Every furnished column here spans x 0..320, so a cursor at 160 is over the
// column's width and one at 400 is beside it — which is the difference between
// a card shoved past a column's end and one carried away from it sideways.
const overColumn = 160;
const besideColumn = 400;
const dragEvent = (target, clientY, clientX = overColumn) => ({ target, clientY, clientX, dataTransfer, preventDefault() {} });
const close = (got, want) => Math.abs(got - want) < 0.0001;
`

// The column under the cursor scrolls toward whichever edge the cursor is held
// against, at a rate the reader sets by how far past the edge they push, and
// not at all while the cursor is anywhere in the middle.
func TestHandlerClientScrollsAColumnHeldAgainstItsEdges(t *testing.T) {
	runBoardClient(t, "drag edge scrolling", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  if (columnCards(deep).length !== `+strconv.Itoa(dragScrollColumnCards)+`) {
    throw new Error("the deep column drew " + columnCards(deep).length + " cards");
  }
  // 600 tall, so the edge zone is the 72px ceiling rather than the quarter:
  // the top zone is 100..172 and the bottom zone is 628..700.
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // The first frame of a loop measures no elapsed time, because the timestamp
  // it is handed is a page clock rather than a stopwatch. Nothing moves on it.
  documentEventListeners.dragover(dragEvent(deep, 700));
  runAnimationFrame(16);
  if (deep.scrollTop !== 0) throw new Error("the first frame of the drag jumped the column to " + deep.scrollTop);

  // From here the loop is running, so each probe below is one measured frame.
  const step = (clientY, from, milliseconds) => {
    deep.scrollTop = from;
    documentEventListeners.dragover(dragEvent(deep, clientY));
    runAnimationFrame(milliseconds);
    return deep.scrollTop - from;
  };

  // 900px per second at the outer edge: a 16ms frame moves 14.4px, which is a
  // 600px column in about two thirds of a second.
  const atEdge = step(700, 0, 16);
  if (!close(atEdge, 14.4)) throw new Error("a frame at the bottom edge scrolled " + atEdge + ", want 14.4");
  // Half as far into the zone, half the speed.
  const halfway = step(664, 0, 16);
  if (!close(halfway, 7.2)) throw new Error("a frame halfway into the bottom zone scrolled " + halfway + ", want 7.2");
  // And the ramp between them is linear rather than a step: three quarters of
  // the way out to the edge is three quarters of the speed.
  const quarter = step(682, 0, 16);
  if (!close(quarter, 10.8)) throw new Error("a frame 18px from the bottom edge scrolled " + quarter + ", want 10.8");

  // The zone's inner boundary is where scrolling starts, not where it is
  // already running.
  if (step(628, 0, 16) !== 0) throw new Error("the column scrolled at the inner edge of the bottom zone");
  if (step(629, 0, 16) <= 0) throw new Error("the column stood still one pixel inside the bottom zone");
  if (step(172, 800, 16) !== 0) throw new Error("the column scrolled at the inner edge of the top zone");
  if (step(171, 800, 16) >= 0) throw new Error("the column stood still one pixel inside the top zone");

  // The middle of the column is a place the cursor can rest.
  [200, 300, 400, 500, 600].forEach((clientY) => {
    if (step(clientY, 800, 16) !== 0) throw new Error("the column scrolled with the cursor at " + clientY);
  });

  // Upward, at the same rate, from wherever the column has reached.
  const upward = step(100, 800, 16);
  if (!close(upward, -14.4)) throw new Error("a frame at the top edge scrolled " + upward + ", want -14.4");
  // A cursor past the top of the column is asking for the top edge at full
  // speed, not for somewhere between the two zones. This is the arithmetic
  // only; that a cursor shoved out there still reaches this loop at all is a
  // question about which events route where, and is pinned separately in
  // TestHandlerClientKeepsScrollingAColumnShovedPast.
  const above = step(40, 800, 16);
  if (!close(above, -14.4)) throw new Error("a frame above the column scrolled " + above + ", want -14.4");

  // A frame nobody watched — a backgrounded tab, a busy main thread — is capped
  // at 50ms of travel rather than scrolling the column past everything on it.
  const stalled = step(700, 0, 500);
  if (!close(stalled, 45)) throw new Error("a 500ms frame scrolled " + stalled + ", want the 50ms cap of 45");

  // Neither end runs off. 30 cards at 100px in a 600px window is 2400px of
  // travel and no more.
  deep.scrollTop = 2380;
  documentEventListeners.dragover(dragEvent(deep, 700));
  for (let frame = 0; frame < 20; frame += 1) runAnimationFrame(16);
  if (deep.scrollTop !== 2400) throw new Error("the column ran past its end to " + deep.scrollTop);
  deep.scrollTop = 20;
  documentEventListeners.dragover(dragEvent(deep, 100));
  for (let frame = 0; frame < 20; frame += 1) runAnimationFrame(16);
  if (deep.scrollTop !== 0) throw new Error("the column ran past its start to " + deep.scrollTop);

  documentEventListeners.dragend({ target: carried });
`)
}

// A browser rounds an assigned scroll offset to the nearest whole pixel, so
// every frame's step is quantized and the rate the reader gets drifts from the
// rate the ramp names — and where the step falls below half a pixel it rounds
// to nothing at all, turning the innermost strip of the zone into a part of the
// ramp that does nothing. That strip is what this pins, because it is the one
// place where the quantization is not a wobble but a stall.
func TestHandlerClientDoesNotLoseFractionsOfAPixelToARoundingScroller(t *testing.T) {
	runBoardClient(t, "drag scrolling on a rounding scroller", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  // Chrome reads an assigned 1.25 back as 1 and an assigned 1.75 back as 2, so
  // this column rounds to nearest the way Chrome does.
  let offset = 0;
  Object.defineProperty(deep, "scrollTop", {
    configurable: true,
    get: () => offset,
    set(value) {
      const limit = Math.max(0, deep.scrollHeight - deep.clientHeight);
      offset = Math.round(Math.max(0, Math.min(limit, Number(value) || 0)));
    }
  });

  documentEventListeners.dragstart({ target: carried, dataTransfer });
  // 71px from the bottom edge of a 72px zone: one seventy-second of 900px per
  // second is 12.5px a second, which at 60Hz is a fifth of a pixel a frame —
  // and a fifth of a pixel rounds to nothing, every frame, forever.
  documentEventListeners.dragover(dragEvent(deep, 629));
  for (let frame = 0; frame < 240; frame += 1) runAnimationFrame(16);
  // 240 frames of 16ms is 3.84 seconds, so 48px of travel is owed.
  if (deep.scrollTop < 47 || deep.scrollTop > 49) {
    throw new Error("a rounding scroller kept " + deep.scrollTop + "px of the 48px the ramp asked for");
  }
  documentEventListeners.dragend({ target: carried });
`)
}

// The loop exists for exactly as long as the gesture does. It starts when the
// cursor first reaches a column, it survives the cursor holding still, and it
// is gone after a drop, after a dragend, and after the cursor leaves the board
// — three separate endings, because a drag has three separate ways to end and
// a frame loop that outlived one of them would run for the life of the page.
func TestHandlerClientRunsTheDragScrollLoopOnlyWhileTheDragLasts(t *testing.T) {
	runBoardClient(t, "drag scroll loop lifecycle", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const ready = listFor("ready");
  const carried = cardIn(ready, `+strconv.Quote(dragScrollCarriedID)+`);
  if (pendingAnimationFrames() !== 0) throw new Error("the board asked for frames before any drag");

  // A drag that has not reached a column yet has nothing to scroll, so the loop
  // waits for the first dragover rather than spinning from dragstart.
  documentEventListeners.dragstart({ target: carried, dataTransfer });
  if (pendingAnimationFrames() !== 0) throw new Error("dragstart alone started a frame loop");

  documentEventListeners.dragover(dragEvent(deep, 700));
  if (pendingAnimationFrames() !== 1) throw new Error("the first dragover did not start one frame loop");
  // One loop, however many dragovers arrive.
  documentEventListeners.dragover(dragEvent(deep, 690));
  documentEventListeners.dragover(dragEvent(deep, 695));
  if (pendingAnimationFrames() !== 1) throw new Error("dragover started a second frame loop");
  // And it keeps itself alive across frames with no further dragover, which is
  // the whole point: a cursor held at the edge stops reporting itself.
  for (let frame = 0; frame < 5; frame += 1) {
    if (runAnimationFrame(16) !== 1) throw new Error("the loop stopped running frames mid-drag");
  }
  if (deep.scrollTop <= 0) throw new Error("a cursor held at the edge did not scroll the column");

  const dropped = documentEventListeners.drop(dragEvent(deep, 690));
  if (pendingAnimationFrames() !== 0) throw new Error("the drop left a frame loop running");
  const landed = deep.scrollTop;
  if (runAnimationFrame(16) !== 0) throw new Error("a frame ran after the drop");
  if (deep.scrollTop !== landed) throw new Error("the column kept scrolling after the drop");
  await dropped;
  documentEventListeners.dragend({ target: carried });
  if (pendingAnimationFrames() !== 0) throw new Error("dragend after a drop started a frame loop");

  // A drag cancelled rather than dropped — Escape, or a release outside the
  // window — ends at dragend, and that is the stop that cannot be missed.
  const again = boardCard(`+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: again, dataTransfer });
  documentEventListeners.dragover(dragEvent(deep, 700));
  if (pendingAnimationFrames() !== 1) throw new Error("the second gesture started no frame loop");
  documentEventListeners.dragend({ target: again });
  if (pendingAnimationFrames() !== 0) throw new Error("dragend left a frame loop running");

  // The cursor is carried sideways out of the window while the drag is still
  // live. That is the one departure no dragover reports — nothing else was
  // entered for one to fire on — and the leave that says so carries a cursor
  // outside the column's width.
  documentEventListeners.dragstart({ target: again, dataTransfer });
  documentEventListeners.dragover(dragEvent(deep, 700));
  runAnimationFrame(16);
  const held = deep.scrollTop;
  documentEventListeners.dragleave({ target: deep, relatedTarget: null, clientX: besideColumn, clientY: 400 });
  if (pendingAnimationFrames() !== 0) throw new Error("leaving the window sideways left a frame loop running");
  if (runAnimationFrame(16) !== 0) throw new Error("a frame ran after the cursor left the window");
  if (deep.scrollTop !== held) throw new Error("the column scrolled after the cursor left the window");

  // Coming back starts it again.
  documentEventListeners.dragover(dragEvent(deep, 700));
  if (pendingAnimationFrames() !== 1) throw new Error("returning to the column did not restart the loop");

  // A leave fired because the column scrolled a new card under a cursor that
  // has not moved. Chrome names the card it moved onto, which the
  // contains(relatedTarget) guard already reads as no departure at all — this
  // is the shape measured on Chrome 151, where every leave during an autoscroll
  // named a relatedTarget inside the same list.
  documentEventListeners.dragleave({ target: columnCards(deep)[3], relatedTarget: columnCards(deep)[4], clientX: overColumn, clientY: 700 });
  if (pendingAnimationFrames() !== 1) throw new Error("a Chrome-shaped retarget inside the column stopped the loop");
  if (runAnimationFrame(16) !== 1) throw new Error("a Chrome-shaped retarget ended the loop a frame later");
  // The same churn as Firefox and Safari report it: no relatedTarget at all, so
  // the guard above cannot tell it from a departure and the cursor's own
  // position has to. This is the case the coordinate rule exists for, and the
  // scroll this loop had just caused must survive it.
  documentEventListeners.dragleave({ target: columnCards(deep)[3], relatedTarget: null, clientX: overColumn, clientY: 700 });
  if (pendingAnimationFrames() !== 1) throw new Error("a Firefox-shaped retarget inside the column stopped the loop");
  if (runAnimationFrame(16) !== 1) throw new Error("a Firefox-shaped retarget ended the loop a frame later");
  documentEventListeners.dragleave({ target: deep, relatedTarget: null, clientX: overColumn, clientY: 400 });
  if (pendingAnimationFrames() !== 1) throw new Error("a leave with the cursor still in the column stopped the loop");
  // Dragging away sideways, over page chrome that takes no drop at all.
  documentEventListeners.dragover(dragEvent(main, 400, besideColumn));
  if (pendingAnimationFrames() !== 0) throw new Error("dragging off every drop target left a frame loop running");

  // A gesture that begins while the last one's state is still standing. dragend
  // has always cleared it, so this is the belt to that brace: a drag whose
  // dragend was never delivered must not leave the next one scrolling a column
  // its cursor has not reached.
  documentEventListeners.dragover(dragEvent(deep, 700));
  if (pendingAnimationFrames() !== 1) throw new Error("the loop did not restart before the stale-state check");
  documentEventListeners.dragstart({ target: again, dataTransfer });
  if (pendingAnimationFrames() !== 0) throw new Error("a new dragstart left the last gesture's frame loop running");
  documentEventListeners.dragend({ target: again });
  documentEventListeners.dragend({ target: again });
`)
}

// A drop is delivered only where the page said yes, and a browser takes that
// answer from whichever of dragenter and dragover it dispatched last. Chrome
// dispatches dragenter and dragleave rather than dragover whenever the element
// under the cursor changes, which is exactly what a scrolling column does under
// a cursor that has not moved — measured on Chrome 151, ten drag updates at
// identical coordinates during an autoscroll produced ten dragenters, ten
// dragleaves and no dragover at all. A page that answered only dragover had its
// card silently discarded when it was released while the column was moving.
//
// The fake DOM has no accept flag of its own, so what is pinned here is the
// property that closes it: for every target either event can land on, the two
// give the same answer.
func TestHandlerClientAcceptsADragOnEnterAsWellAsOnOver(t *testing.T) {
	runBoardClient(t, "drag acceptance on enter and over", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // dragenter draws the line as well as answering for the drop. While the board
  // track slides, dragenter can be the only word the page gets about where the
  // cursor is — measured in Chrome, a card carried to a column off the right of
  // the window arrived on three dragenters and no dragover at all — and a page
  // that drew the line only on dragover would leave it wherever the last one
  // put it, promising a position the drop would not keep.
  documentEventListeners.dragover(dragEvent(deep, 200));
  const drawnByOver = markerGap(deep);
  if (drawnByOver < 0) throw new Error("a dragover over the column drew no line at all");
  documentEventListeners.dragenter(dragEvent(deep, 500));
  const drawnByEnter = markerGap(deep);
  if (drawnByEnter === drawnByOver) {
    throw new Error("a dragenter 300px further down the column left the line in gap " + drawnByOver);
  }
  const misplaced = markerFollowsCursor(deep, 500);
  if (misplaced) throw new Error("after a dragenter, " + misplaced);

  const answer = (name, target, clientY, clientX) => {
    let prevented = false;
    dataTransfer.dropEffect = "";
    documentEventListeners[name]({ target, clientY, clientX, dataTransfer, preventDefault() { prevented = true; } });
    return prevented + "/" + dataTransfer.dropEffect;
  };
  // A column carrying a status this board has no column for. A vocabulary
  // change can strand one mid-drag, and it is one of the two rows below that
  // tells the question "is there a drop target here" from the question this
  // listener actually has to ask, "may this drag be dropped here" — the rest
  // are targets where the two answers agree, and a handler that asked the
  // easier question would pass on them alone.
  const stranded = listFor("done");
  stranded.dataset.dropStatus = "a-status-this-board-has-no-column-for";
  [
    ["a column this drag can be dropped into", deep, 400, overColumn, "true/move"],
    ["a card inside that column", columnCards(deep)[2], 400, overColumn, "true/move"],
    ["page chrome that takes no drop", main, 400, besideColumn, "false/"],
    ["a column whose status this board has no column for", stranded, 400, overColumn, "false/"],
  ].forEach(([what, target, clientY, clientX, want]) => {
    const entered = answer("dragenter", target, clientY, clientX);
    const over = answer("dragover", target, clientY, clientX);
    if (entered !== want) throw new Error("dragenter on " + what + " answered " + entered + ", want " + want);
    if (over !== want) throw new Error("dragover on " + what + " answered " + over + ", want " + want);
  });
  documentEventListeners.dragend({ target: carried });

  // And a gesture whose first word over a column is a dragenter still starts
  // the scroll, because during the churn that is the only word there is.
  const again = boardCard(`+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: again, dataTransfer });
  if (pendingAnimationFrames() !== 0) throw new Error("dragstart alone started a frame loop");
  documentEventListeners.dragenter(dragEvent(deep, 690));
  if (pendingAnimationFrames() !== 1) throw new Error("dragenter over a column started no frame loop");
  for (let frame = 0; frame < 4; frame += 1) runAnimationFrame(16);
  if (deep.scrollTop <= 0) throw new Error("a drag known only from dragenter did not scroll the column");
  documentEventListeners.dragend({ target: again });
  if (pendingAnimationFrames() !== 0) throw new Error("dragend left the dragenter-started loop running");
`)
}

// The gesture the story actually describes: "dragging a task above or below the
// viewport of the column". Past a column's end there is the board's background,
// a heading, or the page — none of which takes a drop, so the dragover that
// lands there names no destination and every other case of that stops the
// scroll. This one must not: the reader has shoved the card past the fold and
// is waiting for the column to come to them, and the column they shoved it out
// of is the only thing that can answer.
//
// Sideways is the opposite and must still stop, because a cursor beside the
// column has gone somewhere that answers for itself.
func TestHandlerClientKeepsScrollingAColumnShovedPast(t *testing.T) {
	runBoardClient(t, "scrolling a column the card is shoved past", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // Nothing to shove past yet: an overshoot before the cursor has ever reached
  // a column names no column to keep scrolling.
  documentEventListeners.dragover(dragEvent(main, 760));
  if (pendingAnimationFrames() !== 0) throw new Error("an overshoot with no column behind it started a frame loop");

  documentEventListeners.dragover(dragEvent(deep, 690));
  runAnimationFrame(16);
  const measure = (event, frames) => {
    const from = deep.scrollTop;
    documentEventListeners.dragover(event);
    for (let frame = 0; frame < frames; frame += 1) runAnimationFrame(16);
    return deep.scrollTop - from;
  };

  // 60px below the column's bottom edge, over page chrome that takes no drop.
  deep.scrollTop = 500;
  const below = measure(dragEvent(main, 760), 4);
  if (pendingAnimationFrames() !== 1) throw new Error("a card shoved below the column stopped its scroll");
  if (!close(below, 4 * 14.4)) throw new Error("a card shoved below the column scrolled " + below + ", want " + (4 * 14.4));

  // And 60px above its top edge, the other half of the same gesture.
  deep.scrollTop = 500;
  const above = measure(dragEvent(main, 40), 4);
  if (pendingAnimationFrames() !== 1) throw new Error("a card shoved above the column stopped its scroll");
  if (!close(above, -4 * 14.4)) throw new Error("a card shoved above the column scrolled " + above + ", want " + (-4 * 14.4));

  // Sideways past the column's own width is a departure, whatever the height.
  deep.scrollTop = 500;
  const sideways = measure(dragEvent(main, 760, besideColumn), 4);
  if (pendingAnimationFrames() !== 0) throw new Error("carrying the card away sideways left a frame loop running");
  if (sideways !== 0) throw new Error("carrying the card away sideways scrolled the column " + sideways);

  // A column whose status this board has no column for is a departure too, and
  // it is asked in the same place. A vocabulary change can strand one mid-drag.
  documentEventListeners.dragover(dragEvent(deep, 690));
  if (pendingAnimationFrames() !== 1) throw new Error("the loop did not restart before the stranded-column check");
  const status = deep.dataset.dropStatus;
  deep.dataset.dropStatus = "a-status-this-board-has-no-column-for";
  documentEventListeners.dragover(dragEvent(deep, 690));
  if (pendingAnimationFrames() !== 0) throw new Error("a column this board cannot drop into kept scrolling");
  deep.dataset.dropStatus = status;

  // The reader comes back into the column and the scroll settles where they
  // left it, which is the point of having kept it alive out there.
  documentEventListeners.dragover(dragEvent(deep, 690));
  deep.scrollTop = 900;
  const returned = measure(dragEvent(deep, 400), 4);
  if (returned !== 0) throw new Error("coming back into the middle of the column kept scrolling it " + returned);
  if (pendingAnimationFrames() !== 1) throw new Error("coming back into the column stopped the loop");

  // A leave out through the top or bottom of the window is the same request as
  // any other overshoot, and dragend is what ends it.
  documentEventListeners.dragleave({ target: deep, relatedTarget: null, clientX: overColumn, clientY: 940 });
  if (pendingAnimationFrames() !== 1) throw new Error("leaving the window downward stopped the column");
  documentEventListeners.dragend({ target: carried });
  if (pendingAnimationFrames() !== 0) throw new Error("dragend left the shoved-past loop running");
`)
}

// The line showing where the card will land is a claim about a column that is
// moving. It has to be recomputed as the cards scroll under a cursor that has
// not moved — there may never be another dragover to correct it — and the drop
// has to land where the line was, not where it was drawn one dragover ago.
func TestHandlerClientKeepsTheDropMarkerOnTheCardsScrollingUnderTheCursor(t *testing.T) {
	runBoardClient(t, "drop marker recomputation under a still cursor", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  const writes = [];
  const answer = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") writes.push({ url, method: options.method, body: JSON.parse(options.body) });
    return answer(url, options);
  };

  documentEventListeners.dragstart({ target: carried, dataTransfer });
  // Held just inside the bottom edge and never moved again.
  const pointerY = 690;
  documentEventListeners.dragover(dragEvent(deep, pointerY));
  const opening = markerGap(deep);
  if (opening !== 6) throw new Error("the unscrolled column put the drop line in gap " + opening + ", want 6");
  let wrong = markerFollowsCursor(deep, pointerY);
  if (wrong) throw new Error("before scrolling, " + wrong);

  // Frames only. No dragover reports the cursor again, because it has not moved.
  for (let frame = 0; frame < 60; frame += 1) runAnimationFrame(16);
  if (deep.scrollTop < 600) throw new Error("a second of held cursor scrolled only " + deep.scrollTop + "px");
  const reached = markerGap(deep);
  if (reached <= opening) throw new Error("the drop line stayed in gap " + reached + " while the column scrolled under it");
  wrong = markerFollowsCursor(deep, pointerY);
  if (wrong) throw new Error("after scrolling, " + wrong);

  // What the line is promising, read off the board the way a reader reads it.
  const promised = columnCards(deep)[reached];
  if (!promised) throw new Error("the drop line promised a card the column does not have");

  const dropped = documentEventListeners.drop(dragEvent(deep, pointerY));
  await dropped;
  if (writes.length !== 1) throw new Error("the drop sent " + writes.length + " writes");
  const sent = writes[0];
  if (sent.method !== "PATCH" || sent.url !== "/api/tasks/" + encodeURIComponent(`+strconv.Quote(dragScrollCarriedID)+`) + "/position") {
    throw new Error("the drop sent " + sent.method + " " + sent.url);
  }
  const want = { status: "in-progress", before: promised.dataset.taskId, expectedHead: "head-carried" };
  if (JSON.stringify(sent.body) !== JSON.stringify(want)) {
    throw new Error("the drop asked for " + JSON.stringify(sent.body) + ", want the line's own position " + JSON.stringify(want));
  }
  documentEventListeners.dragend({ target: carried });
`)
}

// A poll lands every second, mid-drag as often as not. It must not stop the
// column scrolling, reset where it has scrolled to, or cost the gesture the
// node it is carrying — the guarantees the board already makes for a drag,
// extended to the scroll the drag is now driving.
func TestHandlerClientKeepsScrollingAColumnAcrossAMidDragPoll(t *testing.T) {
	runBoardClient(t, "drag scrolling across a poll", dragScrollTasks(), dragScrollHarness+`
  const deep = furnishColumn(listFor("in-progress"), 100, 600);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  const sampled = columnCards(deep)[10];
  const pointerY = 690;

  documentEventListeners.dragstart({ target: carried, dataTransfer });
  documentEventListeners.dragover(dragEvent(deep, pointerY));
  for (let frame = 0; frame < 30; frame += 1) runAnimationFrame(16);
  const before = deep.scrollTop;
  const gap = markerGap(deep);
  if (before <= 0) throw new Error("the column did not scroll before the poll");

  await intervalCallback();

  if (deep.scrollTop !== before) throw new Error("a poll moved the column from " + before + " to " + deep.scrollTop);
  if (markerGap(deep) !== gap) throw new Error("a poll moved the drop line out of gap " + gap);
  if (columnCards(deep)[10] !== sampled) throw new Error("a poll rebuilt a card in the column being scrolled");
  if (!carried.parentElement) throw new Error("a poll detached the card being carried");
  if (!carried.classList.contains("is-dragging")) throw new Error("a poll cleared the drag state");
  if (pendingAnimationFrames() !== 1) throw new Error("a poll left " + pendingAnimationFrames() + " frame loops running, want 1");

  // And it keeps going from where it was, with no dragover to restart it.
  for (let frame = 0; frame < 30; frame += 1) runAnimationFrame(16);
  if (deep.scrollTop <= before) throw new Error("the column stopped scrolling after the poll");
  const wrong = markerFollowsCursor(deep, pointerY);
  if (wrong) throw new Error("after the poll, " + wrong);

  const dropped = documentEventListeners.drop(dragEvent(deep, pointerY));
  if (pendingAnimationFrames() !== 0) throw new Error("the drop after a poll left a frame loop running");
  await dropped;
  documentEventListeners.dragend({ target: carried });
`)
}

// The edge zone is a proportion of the column with a ceiling, not a fixed strip,
// so that a short viewport still leaves somewhere the cursor can rest. Whatever
// the column's height, the still middle is at least half of it and sits in the
// middle of it — and both ends still scroll.
func TestHandlerClientLeavesAStillMiddleAtEveryColumnHeight(t *testing.T) {
	runBoardClient(t, "edge zone proportions", dragScrollTasks(), dragScrollHarness+`
  const deep = listFor("in-progress");
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // 240 is about what a column keeps on a 390x844 phone window under the page's
  // own chrome; 1200 is a tall desktop one. The rest are the ground between.
  [200, 240, 320, 600, 760, 1200].forEach((height) => {
    const top = 100;
    furnishColumn(deep, top, height);
    // Parked in the middle of the travel, so a probe can move the column in
    // either direction and be seen doing it.
    const parked = 1000;
    const moves = (clientY) => {
      deep.scrollTop = parked;
      documentEventListeners.dragover(dragEvent(deep, clientY));
      runAnimationFrame(16);
      runAnimationFrame(16);
      return deep.scrollTop - parked;
    };
    let first = null;
    let last = null;
    for (let clientY = top; clientY <= top + height; clientY += 1) {
      if (moves(clientY) !== 0) continue;
      if (first === null) first = clientY;
      last = clientY;
    }
    if (first === null) throw new Error("a " + height + "px column has nowhere the cursor can rest");
    const still = last - first;
    if (still < height / 2) {
      throw new Error("a " + height + "px column leaves only " + still + "px still, want at least " + (height / 2));
    }
    // Every position in the run, not just its ends: a still region with a hole
    // in it would pass the measurement above and fail the reader.
    for (let clientY = first; clientY <= last; clientY += 1) {
      if (moves(clientY) !== 0) throw new Error("a " + height + "px column scrolled at " + clientY + ", inside its still middle");
    }
    const above = first - top;
    const below = top + height - last;
    if (Math.abs(above - below) > 1) {
      throw new Error("a " + height + "px column has a " + above + "px top zone and a " + below + "px bottom zone");
    }
    if (moves(top) >= 0) throw new Error("a " + height + "px column did not scroll up at its top edge");
    if (moves(top + height) <= 0) throw new Error("a " + height + "px column did not scroll down at its bottom edge");
  });

  documentEventListeners.dragend({ target: carried });
`)
}

// The Deleted column is a scroller like any other and scrolls like any other.
// Nothing about the drop needs the reader to reach a particular row — the
// column orders itself and draws no line — but a column that will not move
// under a cursor pushed into its edge reads as a page that has stopped
// responding, and a reader looking for what they deleted can only see the top
// of it.
func TestHandlerClientScrollsTheDeletedColumnUnderADrag(t *testing.T) {
	stamp := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	live := clientPlacementTask("WB-01J00000000000000000000800", "Live", core.StatusInProgress, core.PriorityMedium)
	live.CreatedAt = stamp
	live.UpdatedAt = stamp
	live.Head = "head-live"
	active := []core.Task{live}
	all := []core.Task{live}
	for i := 0; i < dragScrollColumnCards; i++ {
		task := clientPlacementTask(
			fmt.Sprintf("WB-01J%023d", 810+i),
			fmt.Sprintf("Tombstoned %02d", i),
			core.StatusReady,
			core.PriorityMedium,
		)
		task.Deleted = true
		task.CreatedAt = stamp
		task.UpdatedAt = stamp.Add(time.Duration(dragScrollColumnCards-i) * time.Minute)
		task.Head = fmt.Sprintf("head-tomb-%02d", i)
		all = append(all, task)
	}
	script := deletedColumnClientScript(t)

	program := clientDOMHarness("/?deleted=1", tasksDocumentJSON(t, active)) + script + `
includedTaskResponse = ` + tasksDocumentJSON(t, all) + `;
setTimeout(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
` + dragScrollHarness + `
  const removed = deletedList();
  if (!removed) throw new Error("the board drew no Deleted column");
  if (columnCards(removed).length !== ` + strconv.Itoa(dragScrollColumnCards) + `) {
    throw new Error("the Deleted column drew " + columnCards(removed).length + " cards");
  }
  furnishColumn(removed, 100, 600);
  const card = boardCard(` + strconv.Quote(live.ID) + `);
  if (!card) throw new Error("the board drew no live card to drag");

  documentEventListeners.dragstart({ target: card, dataTransfer });
  // The Deleted column answers a dragenter exactly as it answers a dragover,
  // which is what keeps a restore from being discarded when the card is
  // released while the column is still moving.
  const answer = (name) => {
    let prevented = false;
    dataTransfer.dropEffect = "";
    documentEventListeners[name]({ target: removed, clientY: 400, clientX: 160, dataTransfer, preventDefault() { prevented = true; } });
    return prevented + "/" + dataTransfer.dropEffect;
  };
  if (answer("dragenter") !== "true/move") throw new Error("the Deleted column refused a dragenter carrying a live card");
  if (answer("dragover") !== "true/move") throw new Error("the Deleted column refused a dragover carrying a live card");
  documentEventListeners.dragend({ target: card });

  // The other way round: a tombstone carried over the column it is already in.
  // There is a drop target under the cursor and the drop is still refused,
  // because deleting a tombstone is not a change anyone asked for — so this is
  // the row that tells a listener asking "is there a drop target here" from one
  // asking "may this drag be dropped here", and both events must refuse it.
  const tombstone = columnCards(removed)[0];
  documentEventListeners.dragstart({ target: tombstone, dataTransfer });
  if (answer("dragenter") !== "false/") throw new Error("the Deleted column accepted a dragenter carrying one of its own tombstones");
  if (answer("dragover") !== "false/") throw new Error("the Deleted column accepted a dragover carrying one of its own tombstones");
  documentEventListeners.dragend({ target: tombstone });
  documentEventListeners.dragstart({ target: card, dataTransfer });

  documentEventListeners.dragover(dragEvent(removed, 700));
  if (pendingAnimationFrames() !== 1) throw new Error("the Deleted column started no frame loop");
  for (let frame = 0; frame < 30; frame += 1) runAnimationFrame(16);
  if (removed.scrollTop < 400) throw new Error("the Deleted column scrolled only " + removed.scrollTop + "px");
  // Still no line. The column's order is not the drop's to name, scrolled or
  // not, so nothing here draws a promise the drop would not keep.
  if (markerGap(removed) >= 0) throw new Error("scrolling the Deleted column drew a placement marker");

  documentEventListeners.dragend({ target: card });
  if (pendingAnimationFrames() !== 0) throw new Error("the Deleted column left a frame loop running");
}, 0);
`
	runDeletedColumnClient(t, "deleted column drag scrolling", program)
}

// The page the server serves is what these tests drive, so the guard is here
// rather than in the stylesheet's own file: a column that stopped being its own
// scroller would leave the frame loop scrolling something with nothing to
// scroll, and every test above would still pass.
func TestBoardColumnListIsItsOwnScroller(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	rule := cssRule(t, response.Body.String(), ".task-list")
	if !strings.Contains(rule, "overflow-y: auto") {
		t.Fatalf(".task-list = %q, which does not scroll its own cards", rule)
	}
}

// The same guard for the other axis. The board track is what carries a card to
// a column the window is too narrow to show, and a track that stopped being its
// own scroller would leave the frame loop assigning scrollLeft to an element
// that has none — silently, with every test below still passing.
func TestBoardTrackIsItsOwnHorizontalScroller(t *testing.T) {
	handler := listHandler(t, func(context.Context) ([]core.Task, error) { return nil, nil })
	response := request(t, handler, http.MethodGet, "/")
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", response.Code, http.StatusOK)
	}
	// Every .board rule rather than the first, because the track's placement in
	// the page's own grid is declared separately from how it behaves when the
	// columns outrun the window.
	var declared []string
	for _, line := range strings.Split(response.Body.String(), "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), ".board {")
		if !found {
			continue
		}
		declared = append(declared, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "}")))
	}
	if len(declared) == 0 {
		t.Fatalf("the page has no rule for .board")
	}
	for _, rule := range declared {
		if strings.Contains(rule, "overflow-x: auto") {
			return
		}
	}
	t.Fatalf(".board is declared as %q, none of which scrolls its columns sideways", declared)
}

// The board track slides toward whichever side the cursor is held against, on
// the same ramp a column scrolls down on, and not at all while the cursor is
// anywhere in the middle of the track.
func TestHandlerClientSlidesTheBoardHeldAgainstItsSideEdges(t *testing.T) {
	runBoardClient(t, "board edge scrolling", dragScrollTasks(), dragScrollHarness+`
  // The track spans x 100..580 and y 100..500. 480 wide, so the edge zone is
  // the 72px ceiling rather than the quarter: the left zone is 100..172 and the
  // right zone is 508..580.
  furnishTrack(100, 400);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  const travel = boardTrackTravel();
  if (!(travel > 200)) throw new Error("the track has only " + travel + "px of travel to test with");
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // Nothing on the loop's first frame, for the reason nothing happens on a
  // column's: the timestamp is a page clock rather than a stopwatch.
  documentEventListeners.dragover(trackEvent(580, 300));
  runAnimationFrame(16);
  if (boardElement.scrollLeft !== 0) throw new Error("the first frame of the drag slid the track to " + boardElement.scrollLeft);

  const step = (clientX, from, milliseconds, clientY = 300) => {
    boardElement.scrollLeft = from;
    documentEventListeners.dragover(trackEvent(clientX, clientY));
    runAnimationFrame(milliseconds);
    return boardElement.scrollLeft - from;
  };

  // 900px a second at the outer edge, so a 16ms frame slides 14.4px.
  const atEdge = step(580, 0, 16);
  if (!close(atEdge, 14.4)) throw new Error("a frame at the right edge slid " + atEdge + ", want 14.4");
  // Half as far into the zone, half the speed; three quarters out, three
  // quarters of it. The ramp is linear, not a step.
  const halfway = step(544, 0, 16);
  if (!close(halfway, 7.2)) throw new Error("a frame halfway into the right zone slid " + halfway + ", want 7.2");
  const quarter = step(562, 0, 16);
  if (!close(quarter, 10.8)) throw new Error("a frame 18px from the right edge slid " + quarter + ", want 10.8");

  // The zone's inner boundary is where sliding starts, not where it is already
  // running.
  if (step(508, 0, 16) !== 0) throw new Error("the track slid at the inner edge of the right zone");
  if (step(509, 0, 16) <= 0) throw new Error("the track stood still one pixel inside the right zone");
  if (step(172, 300, 16) !== 0) throw new Error("the track slid at the inner edge of the left zone");
  if (step(171, 300, 16) >= 0) throw new Error("the track stood still one pixel inside the left zone");

  // The middle of the track is somewhere the cursor can rest.
  [200, 250, 300, 400, 450].forEach((clientX) => {
    if (step(clientX, 300, 16) !== 0) throw new Error("the track slid with the cursor at x " + clientX);
  });

  // Leftward at the same rate, from wherever the track has reached.
  const leftward = step(100, 300, 16);
  if (!close(leftward, -14.4)) throw new Error("a frame at the left edge slid " + leftward + ", want -14.4");

  // Past the window's own edge is the loudest way to ask, not a way to ask for
  // nothing: the cursor is past the track's edge, and asks for the full rate.
  const pastRight = step(760, 0, 16);
  if (!close(pastRight, 14.4)) throw new Error("a frame past the window's right edge slid " + pastRight + ", want 14.4");
  const pastLeft = step(20, 300, 16);
  if (!close(pastLeft, -14.4)) throw new Error("a frame past the window's left edge slid " + pastLeft + ", want -14.4");

  // Out through the track's top or bottom is a departure rather than a push,
  // whatever the cursor is doing horizontally.
  if (step(580, 0, 16, 90) !== 0) throw new Error("the track slid with the cursor above it");
  if (step(580, 0, 16, 510) !== 0) throw new Error("the track slid with the cursor below it");

  // A frame nobody watched is capped at 50ms of travel. The band probes above
  // ended the loop, so it is started again first: a loop's first frame measures
  // no time at all and would report nothing here for the wrong reason.
  boardElement.scrollLeft = 0;
  documentEventListeners.dragover(trackEvent(580, 300));
  runAnimationFrame(16);
  const stalled = step(580, 0, 500);
  if (!close(stalled, 45)) throw new Error("a 500ms frame slid " + stalled + ", want the 50ms cap of 45");

  // Neither end runs off.
  boardElement.scrollLeft = travel - 20;
  documentEventListeners.dragover(trackEvent(580, 300));
  for (let frame = 0; frame < 20; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft !== travel) throw new Error("the track ran past its end to " + boardElement.scrollLeft);
  boardElement.scrollLeft = 20;
  documentEventListeners.dragover(trackEvent(100, 300));
  for (let frame = 0; frame < 20; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft !== 0) throw new Error("the track ran past its start to " + boardElement.scrollLeft);

  documentEventListeners.dragend({ target: carried });
`)
}

// The track needs the carry the column needs, and its own: Chrome rounds an
// assigned scrollLeft exactly as it rounds an assigned scrollTop, so the inner
// end of the horizontal ramp would round to nothing every frame without it.
func TestHandlerClientDoesNotLoseFractionsOfAPixelToARoundingBoard(t *testing.T) {
	runBoardClient(t, "board sliding on a rounding scroller", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  roundScroller(boardElement, "scrollLeft");
  documentEventListeners.dragstart({ target: carried, dataTransfer });
  // 71px from the right edge of a 72px zone: one seventy-second of 900px a
  // second is 12.5px a second, which at 60Hz is a fifth of a pixel a frame —
  // and a fifth of a pixel rounds to nothing, every frame, forever.
  documentEventListeners.dragover(trackEvent(509, 300));
  for (let frame = 0; frame < 240; frame += 1) runAnimationFrame(16);
  // 240 frames of 16ms is 3.84 seconds, so 48px of travel is owed.
  if (boardElement.scrollLeft < 47 || boardElement.scrollLeft > 49) {
    throw new Error("a rounding track kept " + boardElement.scrollLeft + "px of the 48px the ramp asked for");
  }
  documentEventListeners.dragend({ target: carried });
`)
}

// The corner. A reader heading for the bottom of a column that is itself off
// the side of the window is asking for both scrollers at once, and one held
// cursor has to drive both — on one frame, from one position, with a remainder
// kept for each rather than shared between them.
func TestHandlerClientScrollsBothAxesFromOneHeldCursor(t *testing.T) {
	runBoardClient(t, "both axes from one cursor", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const deep = listFor("in-progress");
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  // A cursor at (560, 480) is over the deep column, 20px inside the track's
  // right-hand zone and 20px inside that column's own bottom zone at once. The
  // two insets are equal, so both axes are being asked for at exactly the same
  // fraction of exactly the same ramp and the frame owes them the same travel.
  documentEventListeners.dragstart({ target: carried, dataTransfer });
  parkColumnUnder(deep, 560);
  if (columnAt(560, 480) !== deep) {
    const box = deep.getBoundingClientRect();
    throw new Error("the corner this test uses is not over the deep column, which spans x " + box.left + ".." + box.right);
  }
  if (!(deep.scrollHeight - deep.clientHeight > 500)) throw new Error("the deep column has too little travel to see");

  documentEventListeners.dragover(trackEvent(560, 480));
  runAnimationFrame(16);
  const fromX = boardElement.scrollLeft;
  const fromY = deep.scrollTop;
  runAnimationFrame(16);
  const wanted = 900 * (52 / 72) * (16 / 1000);
  const slid = boardElement.scrollLeft - fromX;
  const scrolled = deep.scrollTop - fromY;
  if (!close(slid, wanted)) throw new Error("one frame slid the track " + slid + ", want " + wanted);
  if (!close(scrolled, wanted)) throw new Error("one frame scrolled the column " + scrolled + ", want " + wanted);

  // And they keep going together, neither one starving the other. Eight frames,
  // because past about sixteen the track has carried this column out from under
  // the cursor and handed the next one along the vertical axis.
  for (let frame = 0; frame < 8; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft <= slid) throw new Error("the track stopped while the column kept going");
  if (deep.scrollTop <= scrolled) throw new Error("the column stopped while the track kept going");
  if (pendingAnimationFrames() !== 1) throw new Error("the corner ran " + pendingAnimationFrames() + " frame loops, want 1");
  documentEventListeners.dragend({ target: carried });

  // Each remainder is its own. Both scrollers round, and both are asked for a
  // fifth of a pixel a frame — a shared accumulator would hand one of them the
  // other's fractions and neither would travel what its ramp owes.
  const again = boardCard(`+strconv.Quote(dragScrollCarriedID)+`);
  furnishTrack(100, 400);
  roundScroller(boardElement, "scrollLeft");
  roundScroller(deep, "scrollTop");
  // Parked so the deep column is the one under a cursor held 71px inside the
  // right zone and 71px inside the bottom zone at once.
  parkColumnUnder(deep, 509, 29);
  documentEventListeners.dragstart({ target: again, dataTransfer });
  if (columnAt(509, 429) !== deep) throw new Error("the slow corner is not over the deep column");
  const trackFrom = boardElement.scrollLeft;
  const columnFrom = deep.scrollTop;
  documentEventListeners.dragover(trackEvent(509, 429));
  for (let frame = 0; frame < 240; frame += 1) runAnimationFrame(16);
  const trackTravelled = boardElement.scrollLeft - trackFrom;
  const columnTravelled = deep.scrollTop - columnFrom;
  if (trackTravelled < 47 || trackTravelled > 49) {
    throw new Error("the track kept " + trackTravelled + "px of the 48px owed while a column was carrying its own remainder");
  }
  if (columnTravelled < 47 || columnTravelled > 49) {
    throw new Error("the column kept " + columnTravelled + "px of the 48px owed while the track was carrying its own remainder");
  }
  documentEventListeners.dragend({ target: again });
`)
}

// The routing the second axis needs. A cursor that has left a column used to be
// a cursor that had stopped asking for anything, and the track makes that
// false: the gutter between two columns, the page past the last one and the
// strip beyond the window's edge are all places a reader passes through on the
// way to a column they cannot see, and the track has to keep sliding through
// every one of them.
func TestHandlerClientKeepsTheBoardSlidingWhereNoColumnAnswers(t *testing.T) {
	runBoardClient(t, "board sliding with no column under the cursor", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const deep = listFor("in-progress");
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  const slide = (event, frames) => {
    const from = boardElement.scrollLeft;
    documentEventListeners.dragover(event);
    for (let frame = 0; frame < frames; frame += 1) runAnimationFrame(16);
    return boardElement.scrollLeft - from;
  };
  // A gutter between two columns, held inside the track's right-hand zone. The
  // columns are 200 wide with a 12px gap, so sliding the track by 4 puts the
  // gap that starts at 512 across the cursor at 516.
  boardElement.scrollLeft = 4;
  if (columnAt(516, 300) !== null) throw new Error("the gutter this test uses has a column in it");
  documentEventListeners.dragover(trackEvent(516, 300));
  if (pendingAnimationFrames() !== 1) throw new Error("the gutter between two columns stopped the track");
  if (dropMarkerGapAnywhere() >= 0) throw new Error("the gutter drew a drop line for a drop it would not take");
  const wasAt = boardElement.scrollLeft;
  for (let frame = 0; frame < 4; frame += 1) runAnimationFrame(16);
  if (!(boardElement.scrollLeft > wasAt)) throw new Error("the track did not slide with the cursor over the gutter");

  // Page chrome past the last column, still inside the track's band.
  boardElement.scrollLeft = 0;
  const overChrome = slide({ target: main, clientX: 575, clientY: 300, dataTransfer, preventDefault() {} }, 4);
  if (pendingAnimationFrames() !== 1) throw new Error("page chrome inside the track's zone stopped the track");
  if (!close(overChrome, 4 * 900 * (67 / 72) * 0.016)) throw new Error("the track slid " + overChrome + " over page chrome at x 575");

  // Past the window's edge entirely, where no event names any target at all.
  boardElement.scrollLeft = 0;
  const pastWindow = slide({ target: main, clientX: 700, clientY: 300, dataTransfer, preventDefault() {} }, 4);
  if (pendingAnimationFrames() !== 1) throw new Error("a cursor past the window's edge stopped the track");
  if (!close(pastWindow, 4 * 14.4)) throw new Error("a cursor past the window's edge slid the track " + pastWindow + ", want " + (4 * 14.4));

  // Out through the track's top is a departure: no column under the cursor and
  // nothing left asking, so the loop ends rather than idling for the gesture.
  const above = slide({ target: main, clientX: 575, clientY: 40, dataTransfer, preventDefault() {} }, 4);
  if (above !== 0) throw new Error("a cursor above the track slid it " + above);
  if (pendingAnimationFrames() !== 0) throw new Error("a cursor above the track left a frame loop running");
  const below = slide({ target: main, clientX: 575, clientY: 560, dataTransfer, preventDefault() {} }, 4);
  if (below !== 0) throw new Error("a cursor below the track slid it " + below);
  if (pendingAnimationFrames() !== 0) throw new Error("a cursor below the track left a frame loop running");

  // The middle of the track with nothing under the cursor is asking for
  // nothing at all, and the loop ends there too.
  if (slide({ target: main, clientX: 300, clientY: 300, dataTransfer, preventDefault() {} }, 4) !== 0) {
    throw new Error("the middle of the track slid it");
  }
  if (pendingAnimationFrames() !== 0) throw new Error("the middle of the track left a frame loop running");

  // A leave that takes the cursor sideways off a column but leaves it in the
  // track's own zone stops the column and not the track. This is the leave the
  // sliding itself causes, several times a second.
  documentEventListeners.dragover(trackEvent(560, 300));
  if (pendingAnimationFrames() !== 1) throw new Error("the loop did not restart before the leave check");
  const held = boardElement.scrollLeft;
  documentEventListeners.dragleave({ target: deep, relatedTarget: null, clientX: 516, clientY: 300 });
  if (pendingAnimationFrames() !== 1) throw new Error("a sideways leave inside the track's zone stopped the track");
  for (let frame = 0; frame < 4; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft <= held) throw new Error("the track stopped sliding after a sideways leave");

  // The same leave with the cursor out of the track's band stops the sliding.
  // The leave does not have to name the column the loop is scrolling for that:
  // it is where the cursor is that decides, and a leave reports that whatever
  // else it reports. The loop itself lives on, because a column is still being
  // tracked and a tracked column keeps the loop through any amount of stillness
  // — as it always has, and dragend is what ends it.
  documentEventListeners.dragleave({ target: deep, relatedTarget: null, clientX: 516, clientY: 40 });
  const stopping = boardElement.scrollLeft;
  for (let frame = 0; frame < 4; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft !== stopping) throw new Error("the track slid on after the cursor left its band");
  documentEventListeners.dragend({ target: carried });
  if (pendingAnimationFrames() !== 0) throw new Error("dragend left a frame loop running");
`)
}

// What the reader is promised while the track slides under a cursor that has
// not moved. A whole column changes out there, not just the cards in one, and
// the line, the column being scrolled and the drop have to agree about which —
// the drop hit-tests for itself and lands where the cursor really is, so a line
// left in the column the slide began over is a promise nothing keeps.
func TestHandlerClientFollowsTheColumnSlidingUnderTheCursor(t *testing.T) {
	runBoardClient(t, "the column sliding under a held cursor", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  const writes = [];
  const answer = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    if ((options.method || "GET") !== "GET") writes.push({ url, method: options.method, body: JSON.parse(options.body) });
    return answer(url, options);
  };
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // Held at the right edge and never moved again. At rest the cursor is over
  // the third column; the track slides the later ones under it.
  const cursorX = 560;
  const cursorY = 300;
  const opening = columnAt(cursorX, cursorY);
  if (!opening) throw new Error("the cursor did not start over a column");
  documentEventListeners.dragover(trackEvent(cursorX, cursorY));
  if (markerGap(opening) < 0) throw new Error("the column the drag started over drew no line");

  // Frames only. No dragover reports the cursor again, because it has not moved.
  const seen = new Set([opening.dataset.dropStatus]);
  for (let frame = 0; frame < 90; frame += 1) {
    runAnimationFrame(16);
    const under = columnAt(cursorX, cursorY);
    if (under) seen.add(under.dataset.dropStatus);
    // Wherever the line is, it is in the column the cursor is actually over —
    // and nowhere at all when the cursor is over the gutter between two.
    const drawn = boardLists.filter((list) => markerGap(list) >= 0);
    if (drawn.length > 1) throw new Error("the line was drawn in " + drawn.length + " columns at once");
    if (under && drawn[0] !== under) {
      throw new Error("the line sat in " + (drawn[0] ? drawn[0].dataset.dropStatus : "no column") +
        " while the cursor was over " + under.dataset.dropStatus);
    }
    if (!under && drawn.length !== 0) {
      throw new Error("the line stayed in " + drawn[0].dataset.dropStatus + " with the cursor over the gutter");
    }
  }
  if (boardElement.scrollLeft !== boardTrackTravel()) {
    throw new Error("90 frames of a held cursor slid the track only " + boardElement.scrollLeft + " of " + boardTrackTravel());
  }
  if (seen.size < 2) throw new Error("the track never brought a different column under the cursor");

  // And the drop lands where the line was, in the column the line was in.
  const landing = columnAt(cursorX, cursorY);
  if (!landing) throw new Error("the cursor ended over no column");
  const gap = markerGap(landing);
  const promised = columnCards(landing)[gap];
  const dropped = documentEventListeners.drop({ target: landing, clientX: cursorX, clientY: cursorY, dataTransfer, preventDefault() {} });
  await dropped;
  if (writes.length !== 1) throw new Error("the drop sent " + writes.length + " writes");
  if (writes[0].body.status !== landing.dataset.dropStatus) {
    throw new Error("the drop asked for " + writes[0].body.status + ", want the column the line was in, " + landing.dataset.dropStatus);
  }
  if (promised && writes[0].body.before !== promised.dataset.taskId) {
    throw new Error("the drop asked for " + JSON.stringify(writes[0].body) + ", want the line's own neighbour " + promised.dataset.taskId);
  }
  documentEventListeners.dragend({ target: carried });
`)
}

// The line is rebuilt only when it moves. Taking it out of the document and
// putting it back where it already was is a mutation under a drag cursor, and a
// browser answers those by re-running its drag hit test and firing another
// dragenter/dragleave pair — the pair whose trailing leave withdraws the drop
// target and loses the release that follows.
func TestHandlerClientLeavesTheDropLineAloneWhenItHasNotMoved(t *testing.T) {
	runBoardClient(t, "the drop line is not rebuilt for nothing", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const deep = listFor("in-progress");
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  parkColumnUnder(deep, 560);
  let inserted = 0;
  const insert = deep.insertBefore.bind(deep);
  deep.insertBefore = (child, reference) => { if (child.className === "drop-marker") inserted += 1; return insert(child, reference); };
  documentEventListeners.dragstart({ target: carried, dataTransfer });
  if (columnAt(560, 300) !== deep) throw new Error("this probe is not over the column it is watching");

  // The same dragover twice over is one line, not two.
  documentEventListeners.dragover(trackEvent(560, 300));
  documentEventListeners.dragover(trackEvent(560, 300));
  if (inserted !== 1) throw new Error("two identical dragovers rebuilt the line " + inserted + " times");
  documentEventListeners.dragenter(trackEvent(560, 300));
  if (inserted !== 1) throw new Error("a dragenter naming the placement already on screen rebuilt the line");

  // And a track sliding under the cursor without ever leaving this column
  // rebuilds the line no more often. Parked 30px short of the end, so the track
  // takes up the last of its travel and stops with the same column under the
  // cursor it started with.
  boardElement.scrollLeft = boardTrackTravel() - 30;
  const settled = columnAt(560, 300);
  if (!settled) throw new Error("this probe was meant to park a column under the cursor");
  let rebuilt = 0;
  const put = settled.insertBefore.bind(settled);
  settled.insertBefore = (child, reference) => { if (child.className === "drop-marker") rebuilt += 1; return put(child, reference); };
  documentEventListeners.dragover(trackEvent(560, 300));
  for (let frame = 0; frame < 20; frame += 1) runAnimationFrame(16);
  if (boardElement.scrollLeft !== boardTrackTravel()) throw new Error("the track did not take up its last 30px, it is at " + boardElement.scrollLeft);
  if (columnAt(560, 300) !== settled) throw new Error("this probe was meant to keep one column under the cursor throughout");
  if (rebuilt > 1) throw new Error("20 frames of a track sliding under one column rebuilt the line " + rebuilt + " times");
  documentEventListeners.dragend({ target: carried });
`)
}

// A drag cannot outlive the page it was carrying a card across. A route change
// rebuilds the board out from under the gesture, and no dragend follows
// something the reader never released — so the gesture is retired with the
// route, or the loop runs for the life of the page and the browser will not
// start the next gesture at all.
func TestHandlerClientRetiresADragTheRouteChangeTookAway(t *testing.T) {
	runBoardClient(t, "a drag retired by a route change", dragScrollTasks(), dragScrollHarness+`
  furnishTrack(100, 400);
  const deep = listFor("in-progress");
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });
  documentEventListeners.dragover(trackEvent(560, 300));
  for (let frame = 0; frame < 4; frame += 1) runAnimationFrame(16);
  if (pendingAnimationFrames() !== 1) throw new Error("the gesture was not running a frame loop before the route changed");
  if (dropMarkerGapAnywhere() < 0) throw new Error("the gesture drew no line before the route changed");

  returnTo("/tasks/new");
  if (pendingAnimationFrames() !== 0) throw new Error("a route change left " + pendingAnimationFrames() + " frame loops running");
  if (runAnimationFrame(16) !== 0) throw new Error("a frame ran after the route changed");
  if (dropMarkerGapAnywhere() >= 0) throw new Error("a route change left a drop line drawn on a board nobody is looking at");

  // And the gesture really is over: a dragover arriving late from the drag the
  // browser still thinks is live starts nothing.
  documentEventListeners.dragover(trackEvent(560, 300));
  if (pendingAnimationFrames() !== 0) throw new Error("a dragover after the route changed restarted the loop");
  returnTo("/");
`)
}

// The horizontal edge zone is a proportion of the track with a ceiling, exactly
// as the vertical one is a proportion of the column. Whatever the window's
// width, the still middle is at least half of it and sits in the middle of it,
// and both sides still slide — including the width at which the columns are
// narrower than the zones at either end of it, where the zone is still the
// track's business and not theirs.
func TestHandlerClientLeavesAStillMiddleAtEveryBoardWidth(t *testing.T) {
	runBoardClient(t, "board edge zone proportions", dragScrollTasks(), dragScrollHarness+`
  const carried = cardIn(listFor("ready"), `+strconv.Quote(dragScrollCarriedID)+`);
  documentEventListeners.dragstart({ target: carried, dataTransfer });

  // 200 is narrower than two 72px zones laid end to end, and narrower than one
  // column; 1300 is a wide desktop window. The rest is the ground between.
  [200, 260, 360, 480, 900, 1300].forEach((width) => {
    boardElement.rect = { left: boardTrackLeft, right: boardTrackLeft + width, top: 100, bottom: 500, width };
    boardElement.clientWidth = width;
    boardElement.scrollWidth = width + 2000;
    const parked = 1000;
    const moves = (clientX) => {
      boardElement.scrollLeft = parked;
      documentEventListeners.dragover({ target: main, clientX, clientY: 300, dataTransfer, preventDefault() {} });
      runAnimationFrame(16);
      runAnimationFrame(16);
      return boardElement.scrollLeft - parked;
    };
    let first = null;
    let last = null;
    for (let clientX = boardTrackLeft; clientX <= boardTrackLeft + width; clientX += 1) {
      if (moves(clientX) !== 0) continue;
      if (first === null) first = clientX;
      last = clientX;
    }
    if (first === null) throw new Error("a " + width + "px track has nowhere the cursor can rest");
    const still = last - first;
    if (still < width / 2) {
      throw new Error("a " + width + "px track leaves only " + still + "px still, want at least " + (width / 2));
    }
    for (let clientX = first; clientX <= last; clientX += 1) {
      if (moves(clientX) !== 0) throw new Error("a " + width + "px track slid at x " + clientX + ", inside its still middle");
    }
    const before = first - boardTrackLeft;
    const after = boardTrackLeft + width - last;
    if (Math.abs(before - after) > 1) {
      throw new Error("a " + width + "px track has a " + before + "px left zone and a " + after + "px right zone");
    }
    if (moves(boardTrackLeft) >= 0) throw new Error("a " + width + "px track did not slide left at its left edge");
    if (moves(boardTrackLeft + width) <= 0) throw new Error("a " + width + "px track did not slide right at its right edge");
  });

  documentEventListeners.dragend({ target: carried });
`)
}
