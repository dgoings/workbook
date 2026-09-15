'use strict'

// Runs inside every board view, before the board's own script.
//
// The board has a Dark Mode switch of its own, and a preference of its own:
// light, dark, or absent for "follow the system", kept in the board's
// localStorage. Every board is a different origin (one server, one port), so
// left alone each board answers the question for itself and two of them can
// disagree, with the shell holding a third opinion. This script is what makes
// one answer for the whole window.
//
// Two directions. When this board's switch is clicked, the choice is reported
// to the main process, which repaints the shell and passes it to every other
// board. When another board chose, this board is aligned by clicking its own
// switch, so the board's stored preference and the switch's rendering stay the
// board's business; nothing here reaches into its script. The board's rule
// that choosing the scheme the system already shows means "follow the system"
// is preserved untouched, because the click goes through that rule.

const { ipcRenderer } = require('electron')

// The board's own key and values, from internal/webui/assets/index.html.
const PREFERENCE_KEY = 'workbook.board.scheme'
const SCHEMES = ['', 'light', 'dark']

function stored () {
  try {
    const value = window.localStorage.getItem(PREFERENCE_KEY)
    return SCHEMES.includes(value) ? value : ''
  } catch {
    return ''
  }
}

function writePreference (value) {
  try {
    if (value) window.localStorage.setItem(PREFERENCE_KEY, value)
    else window.localStorage.removeItem(PREFERENCE_KEY)
  } catch {
    // No storage: the board falls back to following the system, as it does.
  }
}

function systemScheme () {
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
    ? 'dark'
    : 'light'
}

/** What the reader is looking at: the board's own definition. */
function effectiveScheme () {
  return stored() || systemScheme()
}

// Before the board's script reads its preference: a board opened after a
// choice was made elsewhere starts in that mode rather than in whatever it
// remembered, and never flips after first paint. Synchronous because the page
// script runs the moment this file ends.
const initial = ipcRenderer.sendSync('theme:current')
writePreference(initial === 'system' ? '' : initial)

// The board writes its preference and then sets data-scheme on the root, so
// by the time the attribute changes the stored value is the new one.
function report () {
  ipcRenderer.send('board:scheme', { scheme: stored() })
}

function watch () {
  const root = document.documentElement
  if (!root) return
  new MutationObserver(report).observe(root, { attributes: true, attributeFilter: ['data-scheme'] })
}
if (document.documentElement) watch()
else document.addEventListener('DOMContentLoaded', watch, { once: true })

/**
 * Bring this board to the shell's choice.
 *
 * `theme` is 'light', 'dark' or 'system'. Clicking the board's switch when the
 * scheme on screen is not the one wanted lets the board's own inference decide
 * what to store: an explicit value when the wanted scheme differs from the
 * system's, nothing when it matches, which is exactly how the shell's 'system'
 * is spelled in the board's terms. A board already showing the wanted scheme
 * is left alone, which is also what keeps the report this click triggers from
 * bouncing back and forth.
 */
function align (theme) {
  const wanted = theme === 'system' ? systemScheme() : theme
  if (effectiveScheme() === wanted) return
  const toggle = document.querySelector('[data-scheme-toggle]')
  if (toggle) {
    toggle.click()
    return
  }
  // A route without the header still has to agree with the rest.
  writePreference(wanted === systemScheme() ? '' : wanted)
  if (stored()) document.documentElement.dataset.scheme = stored()
  else delete document.documentElement.dataset.scheme
}

ipcRenderer.on('board:align', (_event, { theme }) => align(theme))
