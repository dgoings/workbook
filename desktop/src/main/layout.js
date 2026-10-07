'use strict'

// Where the board views sit in the window, kept out of main.js so it can be
// tested: main.js needs Electron, and this needs nothing at all. It is also
// required by the shell's preload, so the height the main process lays the
// boards out against and the height the renderer draws its title strip at
// come from the one function below and cannot drift apart.

/**
 * How tall the shell's title strip is on `platform`, in CSS pixels.
 *
 * The strip stands in for the title bar the window hides, so it is as tall as
 * the row the platform's window controls occupy. macOS: 34, the inset the
 * sidebar head already keeps clear of the traffic lights, so the strip and the
 * head line up as one bar. Windows: 36, the titleBarOverlay's height, which
 * main.js takes from here too. Anywhere else the window keeps its native
 * decorations, which already move it, so there is no strip at all.
 *
 * @param {string} platform a `process.platform` value.
 * @returns {number}
 */
function titleStripHeight (platform) {
  if (platform === 'darwin') return 34
  if (platform === 'win32') return 36
  return 0
}

/**
 * The rectangle the showing board view fills: right of the sidebar and below
 * the title strip, to the window's right and bottom edges.
 *
 * Clamped at zero rather than trusting the window's minimum size, which a
 * window can be below for a moment while it is created or restored; a negative
 * size is an error to setBounds.
 *
 * @param {{ width: number, height: number }} content the window's content size.
 * @param {number} sidebarWidth the sidebar's width, expanded or the rail.
 * @param {number} stripHeight titleStripHeight() for this platform.
 * @returns {{ x: number, y: number, width: number, height: number }}
 */
function boardBounds ({ width, height }, sidebarWidth, stripHeight) {
  return {
    x: sidebarWidth,
    y: stripHeight,
    width: Math.max(0, width - sidebarWidth),
    height: Math.max(0, height - stripHeight)
  }
}

module.exports = { titleStripHeight, boardBounds }
