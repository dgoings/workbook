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
 * A view's contents may already have been destroyed by the time this runs, and
 * closing contents that are gone throws, so they are asked first. A view with
 * no contents at all — one whose open failed partway — is nothing to close and
 * nothing to complain about either.
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

/**
 * Start every imported project's board server, all at once.
 *
 * Using the desktop app is meant to replace running `workbook serve` in every
 * repository by hand: each server carries the five-second synchronization loop
 * that keeps its project current with origin, so a project that is imported but
 * not yet clicked would otherwise sit unsynchronized until it was. One project
 * failing to bind must not stop the others, so each failure is logged and
 * reported beside its project rather than thrown.
 *
 * @param {{ projects: Array<{id: string, path: string}>, start: (project: object) => Promise<string>, log?: (line: string) => void }} input
 * @returns {Promise<{ started: string[], failed: Array<{ projectId: string, error: string }> }>}
 */
async function startEveryProject ({ projects, start, log = () => {} }) {
  const outcomes = await Promise.allSettled(projects.map((project) => start(project)))
  const started = []
  const failed = []
  outcomes.forEach((outcome, index) => {
    const project = projects[index]
    if (outcome.status === 'fulfilled') {
      started.push(project.id)
      return
    }
    const error = outcome.reason?.message ?? String(outcome.reason)
    failed.push({ projectId: project.id, error })
    log(`workbench: could not start the board for ${project.id}: ${error}`)
  })
  return { started, failed }
}

/**
 * The theme Cmd+Shift+D should land on next, given the theme in force and
 * whether the system is currently dark.
 *
 * A cycle over the three stored values (system, light, dark) has a dead
 * press built in: whichever of "system" and "light" or "system" and "dark"
 * currently look the same on screen, stepping between them changes nothing
 * a reader can see, so one press in three does nothing. The board's own
 * switch never has this problem because it does not cycle stored values at
 * all — it flips the scheme that is showing and lets the result be "follow
 * the system" when that flip lands on what the system already shows. This
 * does the same: read which scheme `current` puts on screen (light, dark,
 * or the system's own when `current` is 'system' or anything else), flip
 * it, and store 'system' if the flipped scheme is what the system shows,
 * so every press changes the display and following the system is still one
 * press away.
 */
function toggleTheme (current, systemDark) {
  const visible = current === 'dark' ? true : current === 'light' ? false : systemDark
  const flipped = !visible
  if (flipped === systemDark) return 'system'
  return flipped ? 'dark' : 'light'
}

module.exports = { THEMES, schemeToTheme, releaseClosedWindow, startEveryProject, toggleTheme }
