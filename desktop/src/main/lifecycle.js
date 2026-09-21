'use strict'

// The decisions main.js's lifecycle paths make, taken out of main.js so they
// can be tested.
//
// main.js is the one module here that needs Electron, which means nothing in it
// can be loaded by a test and the only thing standing behind it is
// scripts/check-shell.js's parse check. Both of the things collected here are
// the kind of thing that deserves better than that: what to do with a window's
// views once the window is gone, and which theme a board's reported scheme
// actually asks for. Neither needs a window or a view to decide, only to be
// told about them, so both are plain functions over what they are handed.
//
// No `require` of anything, and no module state: check-shell.js loads every
// module in this directory outside Electron, and a decision that remembered
// something between calls would be a second place the window's state lived.

/** The themes the shell has, in the order the board's switch offers them. */
const THEMES = ['system', 'light', 'dark']

/**
 * The theme a board's reported scheme asks the shell to adopt, or null.
 *
 * A board reports its own stored preference in its own terms, so there is one
 * translation to make: it spells "follow the system" as an empty string where
 * the shell has always spelled it 'system'. Read strictly otherwise — the
 * value crosses IPC from a board's page, and anything that is not one of the
 * three themes asks for nothing rather than being stored as a theme nothing
 * can paint.
 *
 * A scheme naming the theme already in force asks for nothing either, and that
 * is load-bearing rather than an optimization: it is where the alignment sent
 * to the other boards stops. A board told to align reports its new preference
 * back like any other change, and answering that report with another round of
 * alignment is a loop.
 */
function schemeToTheme (scheme, current) {
  const theme = scheme === '' ? 'system' : scheme
  if (!THEMES.includes(theme)) return null
  if (theme === current) return null
  return theme
}

/**
 * Let go of every view a closed window held.
 *
 * Given the map and the chrome view and nothing else, which is the shape that
 * matters: by the time a window has emitted `closed` it is destroyed, so
 * `window.contentView.removeChildView(view)` — what closeProject does to a
 * view on a live window — would throw. Closing each view's web contents is all
 * there is left to do, and the caller's own references are the caller's to
 * drop.
 *
 * Every close is guarded and wrapped, and the map is cleared whatever happened.
 * A map half-emptied by one view that threw is the exact bug this is here to
 * fix: the next window would inherit the leftovers and lay out boards no window
 * holds, which is what made the first Cmd+B in a reopened window do nothing.
 * Failures are collected rather than thrown, the way clipath.js collects its
 * own, so the caller can say what went wrong having already lost nothing.
 */
function releaseClosedWindow ({ boardViews, chromeView }) {
  const dropped = []
  const errors = []

  for (const [projectId, view] of boardViews) {
    dropped.push(projectId)
    const error = closeContents(view)
    if (error) errors.push(`could not close the board view for ${projectId}: ${error.message}`)
  }
  boardViews.clear()

  const error = chromeView ? closeContents(chromeView) : null
  if (error) errors.push(`could not close the shell view: ${error.message}`)

  return { dropped, errors }
}

/**
 * Close one view's web contents, and report rather than throw.
 *
 * Electron has usually torn the contents down along with the window before this
 * runs, so `isDestroyed()` is the ordinary path and not an edge case; closing
 * contents that are already gone throws. A view with no contents at all — one
 * whose open failed partway — is nothing to close and nothing to complain
 * about either.
 */
function closeContents (view) {
  try {
    const contents = view?.webContents
    if (contents && !contents.isDestroyed()) contents.close()
    return null
  } catch (error) {
    return error
  }
}

module.exports = { THEMES, schemeToTheme, releaseClosedWindow }
