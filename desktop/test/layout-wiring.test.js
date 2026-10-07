'use strict'

// layout.js holds the title strip's height and the board's bounds, and
// test/layout.test.js holds those to their numbers. That says nothing about
// whether anything uses them: main.js, the preload and the renderer can each
// fall back to a literal, or stop calling in, and every number in layout.js
// would still be right while the board covered the strip or left a gap above
// it. None of the three files can be loaded without Electron or a DOM, so these
// read their source, comments removed, the way drag-region.test.js reads the
// stylesheet.

const fs = require('node:fs')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const src = path.join(__dirname, '..', 'src')

// The file's text with LF endings and its comments gone, so a call that has
// been commented out no longer counts as a call. Good enough for these files:
// none of the lines matched below holds a string with `//` or `/*` in it.
function code (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8')
    .replace(/\r\n/g, '\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

// A top-level function's text, from its declaration to the first line that
// closes it at column zero.
function fn (text, name) {
  const match = text.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name}`)
  return match[0]
}

function css () {
  return fs.readFileSync(path.join(src, 'renderer', 'styles.css'), 'utf8')
    .replace(/\r\n/g, '\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
}

describe('the title strip height reaches every user', () => {
  test('main.js lays the boards out under the strip for this platform', () => {
    const layout = fn(code('main', 'main.js'), 'layout')
    assert.match(layout, /boardBounds\([^;]*?,\s*titleStripHeight\(process\.platform\)\s*\)/,
      'layout() must pass titleStripHeight(process.platform) to boardBounds as the strip height')
  })

  test('the preload hands the renderer the same function\'s answer, not a number of its own', () => {
    assert.match(code('preload', 'preload.js'), /titleStripHeight:\s*titleStripHeight\(process\.platform\)/,
      'preload.js must expose titleStripHeight: titleStripHeight(process.platform)')
  })

  test('the renderer hides the strip exactly where there is none', () => {
    assert.match(code('renderer', 'app.js'), /el\('title-strip'\)\.hidden\s*=\s*api\.titleStripHeight\s*===\s*0\b/,
      "app.js must set el('title-strip').hidden from api.titleStripHeight === 0")
  })
})

describe('the title strip names what is showing', () => {
  const app = code('renderer', 'app.js')

  test('switching views renames it', () => {
    assert.match(fn(app, 'setView'), /\bpaintTitle\(\)/, 'setView must call paintTitle()')
  })

  test('reloading the project list renames it', () => {
    assert.match(fn(app, 'renderProjects'), /\bpaintTitle\(\)/, 'renderProjects must call paintTitle()')
  })
})

describe('the title strip on Windows', () => {
  // The strip starts at the sidebar's edge, not the window's, so the controls'
  // width is the viewport's width less the free area's right edge; a
  // percentage would be of #main and come out short by the sidebar.
  test('the name is kept clear of the overlay controls by viewport arithmetic', () => {
    const block = css().match(/(?:^|\})\s*\.title-strip\s*\{([^{}]*)\}/)
    assert.ok(block, 'no .title-strip rule')
    const padding = block[1].match(/padding-right\s*:\s*([^;]*);/)
    assert.ok(padding, '.title-strip must set padding-right')
    assert.match(padding[1], /100vw/, 'the padding must be measured against 100vw')
    assert.match(padding[1], /env\(titlebar-area-x\b/, 'the padding must read env(titlebar-area-x)')
    assert.match(padding[1], /env\(titlebar-area-width\b/, 'the padding must read env(titlebar-area-width)')
    assert.doesNotMatch(padding[1], /100%/, 'a percentage is of #main, not the window')
  })
})
