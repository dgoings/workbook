'use strict'

// Where the board views sit in the window and how tall the window's title row
// is, kept out of main.js so it can be tested: main.js needs Electron, and this
// needs nothing at all. It is also required by the shell's preload, so the row
// the main process places the window controls in and the row the renderer
// draws the sidebar head's first line in come from the one constant below and
// cannot drift apart.

/**
 * How tall the window's title row is, in CSS pixels: the row the window
 * controls sit in, which the sidebar head's first line (the wordmark and the
 * chevron) and the board's own header share.
 *
 * macOS: the traffic lights are pinned at TRAFFIC_LIGHT_POSITION, centered on
 * the row, so its vertical center, 18, is the lights' center. Windows: the
 * titleBarOverlay's height, so the overlay controls fill the same row. Linux
 * keeps its native decorations and has no controls in the page at all; the
 * head still draws its first line this tall so the sidebar looks the same.
 */
const TITLE_ROW_HEIGHT = 36

/**
 * How big one traffic-light button is, and how far apart their left edges are,
 * in points. Electron places the top of the button frame at
 * trafficLightPosition.y, so the frame, not the drawn circle, is what centers.
 * Measured with AppKit on macOS 27 (NSWindow.standardWindowButton: 14x14
 * frames at x 9, 32, 55); the sizes vary between macOS releases, so these are
 * this release's numbers and the eye is the final judge.
 */
const TRAFFIC_LIGHT_SIZE = 14
const TRAFFIC_LIGHT_SPACING = 23

/**
 * Where macOS puts the traffic lights, from the window's top-left corner, in
 * points. y is TITLE_ROW_HEIGHT less one button's height, halved, so the
 * buttons are centered on the row; x is close to the inset macOS uses itself.
 */
const TRAFFIC_LIGHT_POSITION = Object.freeze({ x: 14, y: (TITLE_ROW_HEIGHT - TRAFFIC_LIGHT_SIZE) / 2 })

/** Where the last traffic light ends, from the window's left edge. */
const TRAFFIC_LIGHTS_END = TRAFFIC_LIGHT_POSITION.x + 2 * TRAFFIC_LIGHT_SPACING + TRAFFIC_LIGHT_SIZE

/**
 * How wide the collapsed sidebar is, in CSS pixels. A rail rather than nothing
 * at all, and on macOS the traffic lights sit over it, so it is as wide as the
 * lights plus the same clearance on their right as on their left: 74 + 14 =
 * 88. Any narrower and the line at the rail's right edge, or the board beside
 * it, runs into the zoom button. The main process lays the boards out beside
 * this width and the shell's preload hands the same number to the stylesheet,
 * so the two cannot drift apart.
 */
const RAIL_WIDTH = TRAFFIC_LIGHTS_END + TRAFFIC_LIGHT_POSITION.x

/**
 * The rectangle the showing board view fills: right of the sidebar, from the
 * window's top edge to its right and bottom edges. The board's own header is
 * the top of the window there, and the window is dragged by it.
 *
 * Clamped at zero rather than trusting the window's minimum size, which a
 * window can be below for a moment while it is created or restored; a negative
 * size is an error to setBounds.
 *
 * @param {{ width: number, height: number }} content the window's content size.
 * @param {number} sidebarWidth the sidebar's width, expanded or the rail.
 * @returns {{ x: number, y: number, width: number, height: number }}
 */
function boardBounds ({ width, height }, sidebarWidth) {
  return {
    x: sidebarWidth,
    y: 0,
    width: Math.max(0, width - sidebarWidth),
    height: Math.max(0, height)
  }
}

module.exports = { TITLE_ROW_HEIGHT, TRAFFIC_LIGHT_SIZE, TRAFFIC_LIGHT_POSITION, TRAFFIC_LIGHTS_END, RAIL_WIDTH, boardBounds }
