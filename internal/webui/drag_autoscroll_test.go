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

  const answer = (name, target, clientY, clientX) => {
    let prevented = false;
    dataTransfer.dropEffect = "";
    documentEventListeners[name]({ target, clientY, clientX, dataTransfer, preventDefault() { prevented = true; } });
    return prevented + "/" + dataTransfer.dropEffect;
  };
  [
    ["a column this drag can be dropped into", deep, 400, overColumn, "true/move"],
    ["a card inside that column", columnCards(deep)[2], 400, overColumn, "true/move"],
    ["page chrome that takes no drop", main, 400, besideColumn, "false/"],
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
