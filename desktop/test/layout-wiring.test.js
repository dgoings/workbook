'use strict'

// layout.js holds the title row's height, where the traffic lights sit and the
// board's bounds, and test/layout.test.js holds those to their numbers. That
// says nothing about whether anything uses them: main.js, the preload and the
// renderer can each fall back to a literal, or stop calling in, and every
// number in layout.js would still be right while the lights drifted off the
// wordmark's line or the overlay grew taller than the row. None of the three
// files can be loaded without Electron or a DOM, so these read their source,
// comments removed, the way drag-region.test.js reads the stylesheet.

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

describe('the title row comes from layout.js everywhere', () => {
  const main = code('main', 'main.js')

  test('main.js lays the boards out from the top edge', () => {
    assert.match(fn(main, 'layout'), /boardBounds\(content,\s*sidebarWidth\(\)\)/,
      'layout() must call boardBounds(content, sidebarWidth()) with no strip height')
  })

  test('macOS pins the traffic lights at the shared position', () => {
    assert.match(fn(main, 'createWindow'), /trafficLightPosition:\s*TRAFFIC_LIGHT_POSITION\b/,
      'createWindow must pass trafficLightPosition: TRAFFIC_LIGHT_POSITION')
    assert.equal([...main.matchAll(/trafficLightPosition/g)].length, 1,
      'trafficLightPosition must be set in one place, from layout.js')
  })

  test('the Windows overlay is the row\'s height when created and when repainted', () => {
    const overlays = [
      ['createWindow', fn(main, 'createWindow').match(/titleBarOverlay:\s*\{([^}]*)\}/)],
      ['applyTheme', fn(main, 'applyTheme').match(/setTitleBarOverlay\(\{([^}]*)\}\)/)]
    ]
    for (const [name, overlay] of overlays) {
      assert.ok(overlay, `${name} must set the Windows overlay`)
      assert.match(overlay[1], /(?:^|[\s,])height:\s*TITLE_ROW_HEIGHT\s*(?:,|$)/,
        `${name} must give the overlay height: TITLE_ROW_HEIGHT`)
    }
  })

  test('main.js takes both from layout.js', () => {
    assert.match(main, /const \{[^}]*\bTITLE_ROW_HEIGHT\b[^}]*\bTRAFFIC_LIGHT_POSITION\b[^}]*\} = require\('\.\/layout'\)/,
      'main.js must require TITLE_ROW_HEIGHT and TRAFFIC_LIGHT_POSITION from ./layout')
  })

  test('the preload hands the renderer the same constant, not a number of its own', () => {
    const preload = code('preload', 'preload.js')
    assert.match(preload, /titleRowHeight:\s*TITLE_ROW_HEIGHT\b/,
      'preload.js must expose titleRowHeight: TITLE_ROW_HEIGHT')
    assert.match(preload, /const \{ TITLE_ROW_HEIGHT \} = require\('\.\.\/main\/layout'\)/,
      'preload.js must require TITLE_ROW_HEIGHT from ../main/layout')
  })

  test('the renderer sets --title-row-height from it at boot', () => {
    assert.match(fn(code('renderer', 'app.js'), 'boot'),
      /setProperty\('--title-row-height', `\$\{api\.titleRowHeight\}px`\)/,
      'boot() must set --title-row-height from api.titleRowHeight')
  })
})

describe('the board view on Windows', () => {
  // The overlay controls sit over the board's header, and only the board can
  // pad its header, so the board's preload tells it the platform.
  test('the board preload marks a Windows page, and only a Windows one', () => {
    assert.match(code('preload', 'board.js'),
      /if \(process\.platform === 'win32'\) document\.documentElement\.classList\.add\('in-workbench-win32'\)/,
      "board.js must add in-workbench-win32 when process.platform === 'win32'")
  })
})
