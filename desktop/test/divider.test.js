'use strict'

// The hairline between the sidebar and what lies beside it stops, beside a
// board, at the bottom of the board's header, so the sidebar head and the
// header read as one layer across the window's title row. Two documents share
// the job. That edge is one only the board can measure, so the board draws the
// line (internal/webui holds that half) once board.js has marked its page, and
// the shell draws none. Beside the shell's own views there is no header to
// stop at, and the shell draws it from the window's top edge. These read the
// source, comments removed, the way layout-wiring.test.js does, because none
// of the three files loads without Electron or a DOM.

const fs = require('node:fs')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const src = path.join(__dirname, '..', 'src')

function code (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8')
    .replace(/\r\n/g, '\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

// The declarations of every rule block whose selector list is exactly
// `selector`.
function bodies (selector) {
  const found = []
  for (const match of code('renderer', 'styles.css').matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (match[1].trim().replace(/\s+/g, ' ') === selector) found.push(match[2])
  }
  return found
}

function fn (text, name) {
  const match = text.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name}`)
  return match[0]
}

describe('the sidebar divider in the shell', () => {
  // A border runs the sidebar's full height beside a board too, through the
  // title row.
  test('the sidebar draws no border of its own', () => {
    for (const match of code('renderer', 'styles.css').matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (!/#sidebar\b(?!::)/.test(match[1])) continue
      assert.doesNotMatch(match[2], /(?:^|[;\s])border(?:-(?:right|inline-end))?(?:-(?:width|style|color))?\s*:/,
        `${match[1].trim()} declares a border on the sidebar; the divider is #sidebar::after`)
    }
  })

  test('the divider starts at the top edge and takes the theme\'s rule color', () => {
    const blocks = bodies('#sidebar::after')
    assert.equal(blocks.length, 1, 'expected exactly one rule whose selector is #sidebar::after')
    const block = blocks[0]
    assert.match(block, /(?:^|[;\s])top\s*:\s*0\s*;/,
      'the divider must start at top: 0, with no strip to clear')
    for (const match of code('renderer', 'styles.css').matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (!match[1].includes('#sidebar::after') || match[1].trim() === '#sidebar::after') continue
      assert.doesNotMatch(match[2], /(?:^|[;\s])top\s*:/, `${match[1].trim()} moves the divider's top`)
    }
    assert.match(block, /(?:^|[;\s])bottom\s*:\s*0\b/, 'the divider must run to the bottom')
    assert.match(block, /(?:^|[;\s])background\s*:\s*var\(--wb-rule\)\s*;/,
      'the divider must be drawn in var(--wb-rule)')
    assert.doesNotMatch(block, /#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(/,
      'the divider rule must not hold a literal color')
    assert.match(code('renderer', 'styles.css'), /(?:^|\})\s*#sidebar\s*\{[^{}]*position\s*:\s*relative\b/,
      '#sidebar must be position: relative, or the divider is placed against the window')
  })

  test('beside a board the shell draws no divider', () => {
    const blocks = bodies(':root.board-active #sidebar::after')
    assert.equal(blocks.length, 1, 'expected one rule for :root.board-active #sidebar::after')
    assert.match(blocks[0], /(?:^|[;\s])(?:content\s*:\s*none|display\s*:\s*none)\b/,
      'the board-active rule must remove the divider')
  })

  // The project view also holds the wait for a board, a failed start and the
  // Git identity form, none of which has a board beside the sidebar, so the
  // class follows the main process, which knows when a view is laid in.
  test('the main process says when a board is laid in, and when it is not', () => {
    const main = code('main', 'main.js')
    const open = fn(main, 'openProject')
    const laidIn = open.match(/\n {2}activeProjectId = projectId\n {2}layout\(\)\n([\s\S]*)$/)
    assert.ok(laidIn, 'openProject must set activeProjectId and call layout() at its top level')
    assert.match(laidIn[1], /toChrome\('board:showing',\s*true\)/,
      "openProject must send board:showing true after it lays the view in")
    assert.doesNotMatch(laidIn.input.slice(0, laidIn.index), /board:showing/,
      'openProject must not say anything about board:showing before the view is laid in, or a switch between boards flickers')
    assert.doesNotMatch(open, /toChrome\('board:showing',\s*false\)/,
      'openProject must never send false: the old board stays drawn until the new one is in')
    assert.match(fn(main, 'showChrome'), /activeProjectId = null\n {2}layout\(\)\n[\s\S]*toChrome\('board:showing',\s*false\)/,
      'showChrome must send board:showing false after it parks the board')
    assert.equal([...main.matchAll(/board:showing/g)].length, 2,
      'board:showing must be sent from exactly openProject and showChrome')
  })

  test('the renderer follows that message, and no longer the view', () => {
    assert.match(code('preload', 'preload.js'), /ipcRenderer\.on\('board:showing',/,
      'preload.js must listen for board:showing')
    const app = code('renderer', 'app.js')
    assert.match(app,
      /api\.onBoardShowing\(\(showing\) => \{\s*document\.documentElement\.classList\.toggle\('board-active',\s*showing === true\)/,
      'app.js must toggle board-active from api.onBoardShowing')
    assert.equal([...app.matchAll(/'board-active'/g)].length, 1,
      'board-active must be set in one place only, the board:showing handler')
  })

  // Every path that leaves the project view without a board in it reaches
  // showChrome, which is what sends false.
  test('the identity form and every other view hand the window back to the shell', () => {
    const app = code('renderer', 'app.js')
    assert.match(fn(app, 'askForIdentity'), /api\.showChrome\(\)/,
      'askForIdentity must call api.showChrome() before showing the form')
    assert.match(fn(app, 'setView'), /if \(view !== 'project'\) api\.showChrome\(\)/,
      'setView must call api.showChrome() for every view that is not a project')
    assert.match(code('preload', 'preload.js'), /showChrome:\s*\(\)\s*=>\s*ipcRenderer\.invoke\('project:showChrome'\)/,
      'the preload must route showChrome to project:showChrome')
    assert.match(code('main', 'main.js'), /ipcMain\.handle\('project:showChrome',\s*async \(\) => \{ showChrome\(\) \}\)/,
      'project:showChrome must call showChrome()')
  })
})

describe('the sidebar divider beside a board', () => {
  // The board's stylesheet draws the line only under this class, so a browser
  // that opens the board draws none; this preload is the only place it is set.
  test('the board preload marks the page as drawn inside Workbench', () => {
    assert.match(code('preload', 'board.js'),
      /document\.documentElement\.classList\.add\('in-workbench'\)/,
      "board.js must add in-workbench to document.documentElement")
  })
})
