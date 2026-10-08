'use strict'

// The window hides its native title bar on macOS and Windows, and with it gone
// nothing moves the window but an element a page marks as a drag region. The
// shell's is the sidebar head; beside it the board's own header is the other
// half of the window's title row (internal/webui holds that half). From the
// shell's first build the head's mark sat on a :root:not(.is-mac) rule, so the
// Windows build dragged and the Mac build had nothing to take hold of; these
// pin the mark to the head itself, in both layouts, keep every control inside
// it clickable, hold its first line to the title row the window controls sit
// in, and keep the title strip that once stood over the board from coming
// back.

const fs = require('node:fs')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const renderer = path.join(__dirname, '..', 'src', 'renderer')

function read (name) {
  return fs.readFileSync(path.join(renderer, name), 'utf8').replace(/\r\n/g, '\n')
}

// Every innermost rule block, as its selectors and its -webkit-app-region
// value. A block inside an @media is still found, since the pattern only
// matches a selector run that holds no brace of its own.
function rules (css) {
  const found = []
  const bare = css.replace(/\/\*[\s\S]*?\*\//g, '')
  for (const match of bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1].split(',').map((s) => s.trim().replace(/\s+/g, ' '))
    const region = match[2].match(/-webkit-app-region\s*:\s*([\w-]+)/)
    found.push({ selectors, region: region ? region[1] : null })
  }
  return found
}

// Every element inside the sidebar head's markup that needs the pointer: a
// control, which must click, or an element whose tooltip app.js sets, which
// must hover. A drag region swallows both.
function headControls (html, js) {
  const head = html.match(/<header class="sidebar-head">([\s\S]*?)<\/header>/)
  assert.ok(head, 'index.html has no <header class="sidebar-head">')
  const titled = new Set([...js.matchAll(/el\('([\w-]+)'\)\.title\s*=/g)].map((m) => m[1]))
  const controls = []
  for (const tag of head[1].matchAll(/<([a-z][\w-]*)\b([^>]*)>/g)) {
    const id = tag[2].match(/\bid="([^"]*)"/)
    const control = /^(button|a|input|select|textarea)$/.test(tag[1])
    if (!control && !(id && titled.has(id[1]))) continue
    const cls = tag[2].match(/class="([^"]*)"/)
    controls.push({ tag: tag[1], classes: cls ? cls[1].split(/\s+/).filter(Boolean) : [] })
  }
  return controls
}

// The declarations of every rule block whose selector list is exactly
// `selector`, comments removed.
function bodies (cssText, selector) {
  const bare = cssText.replace(/\/\*[\s\S]*?\*\//g, '')
  const found = []
  for (const match of bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (match[1].trim() === selector) found.push(match[2])
  }
  return found
}

const css = rules(read('styles.css'))

describe('the window drag region', () => {
  test('the sidebar head itself is the drag region, on every platform', () => {
    const own = css.filter((rule) => rule.selectors.includes('.sidebar-head') && rule.region !== null)
    assert.deepEqual(own.map((rule) => rule.region), ['drag'],
      'a rule whose selector is exactly .sidebar-head must declare -webkit-app-region: drag')
  })

  test('no platform or rail rule takes the head out of the drag region', () => {
    for (const rule of css) {
      for (const selector of rule.selectors) {
        if (!/\.sidebar-head$/.test(selector) || selector === '.sidebar-head') continue
        if (rule.region === null) continue
        assert.equal(rule.region, 'drag',
          `${selector} sets -webkit-app-region: ${rule.region} on the head`)
      }
    }
  })

  test('every control and tooltip in the head opts out, so a click still clicks and a hover still shows', () => {
    const noDrag = new Set()
    for (const rule of css) {
      if (rule.region !== 'no-drag') continue
      for (const selector of rule.selectors) {
        const only = selector.match(/^\.([\w-]+)$/)
        if (only) noDrag.add(only[1])
      }
    }
    assert.ok(noDrag.has('icon-button'), '.icon-button must declare -webkit-app-region: no-drag')

    const controls = headControls(read('index.html'), read('app.js'))
    assert.ok(controls.some((control) => control.classes.includes('sidebar-toggle')),
      'expected the head to hold the sidebar toggle')
    assert.ok(controls.some((control) => control.classes.includes('version')),
      'expected the head to hold the version line, whose tooltip app.js sets')
    for (const control of controls) {
      assert.ok(control.classes.some((name) => noDrag.has(name)),
        `<${control.tag} class="${control.classes.join(' ')}"> in the sidebar head has no class with a bare class rule declaring -webkit-app-region: no-drag`)
    }
  })
})

const MAC_INSET_MIN = (() => {
  // The traffic lights: three 12px circles 20px apart from the pinned x.
  const { TRAFFIC_LIGHT_POSITION } = require('../src/main/layout')
  return TRAFFIC_LIGHT_POSITION.x + 2 * 20 + 12
})()

describe('the sidebar head is the shell\'s half of the title row', () => {
  test('its first line is the title row, from the shared height', () => {
    const blocks = bodies(read('styles.css'), '.sidebar-head')
    assert.equal(blocks.length, 1, 'expected exactly one rule whose selector is .sidebar-head')
    assert.match(blocks[0], /(?:^|[;\s])display\s*:\s*grid\s*;/, 'the head must be a grid')
    assert.match(blocks[0], /grid-template-rows\s*:\s*var\(--title-row-height\b[^;]*\)\s+auto\s*;/,
      'the head\'s first row must be var(--title-row-height), the second the version line')
    assert.match(blocks[0], /(?:^|[;\s])align-items\s*:\s*center\s*;/,
      'the wordmark and the chevron must be centered on the row')
    assert.match(blocks[0], /(?:^|[;\s])padding\s*:\s*0\b/, 'the head must keep no top padding above the row')
  })

  test('the wordmark and the chevron share the first line, the version the second', () => {
    const place = (selector) => {
      const block = bodies(read('styles.css'), selector).join(';')
      return {
        column: (block.match(/grid-column\s*:\s*(\d+)/) || [])[1],
        row: (block.match(/grid-row\s*:\s*(\d+)/) || [])[1]
      }
    }
    assert.deepEqual(place('.wordmark'), { column: '1', row: '1' })
    assert.deepEqual(place('.sidebar-toggle'), { column: '2', row: '1' })
    assert.deepEqual(place('.version'), { column: '1', row: '2' })
    assert.match(bodies(read('styles.css'), '.sidebar-identity').join(';'), /display\s*:\s*contents/,
      'the identity block must be display: contents, or the wordmark and version are not grid items')
  })

  test('on macOS the wordmark starts clear of the traffic lights', () => {
    const blocks = bodies(read('styles.css'), ':root.is-mac .sidebar-head')
    assert.equal(blocks.length, 1, 'expected one rule for :root.is-mac .sidebar-head')
    const inset = blocks[0].match(/padding-left\s*:\s*(\d+)px\s*;/)
    assert.ok(inset, ':root.is-mac .sidebar-head must set padding-left in px')
    assert.ok(Number(inset[1]) >= MAC_INSET_MIN + 8,
      `padding-left ${inset[1]}px must clear the lights, which end at ${MAC_INSET_MIN}px, by 8px`)
  })

  // The lights fill the rail's title row on macOS, so there the chevron goes on
  // the line below; everywhere else it is centered on the row.
  test('the rail centers the chevron alone, under the lights on macOS', () => {
    const rail = bodies(read('styles.css'), ':root.sidebar-collapsed .sidebar-head').join(';')
    assert.match(rail, /justify-items\s*:\s*center/, 'the rail must center the chevron')
    assert.match(bodies(read('styles.css'), ':root.sidebar-collapsed .sidebar-toggle').join(';'), /grid-column\s*:\s*1\b/)
    assert.match(bodies(read('styles.css'), ':root.is-mac.sidebar-collapsed .sidebar-toggle').join(';'), /grid-row\s*:\s*2\b/)
  })
})

describe('the title strip is gone', () => {
  test('no markup, style, script or custom property names it', () => {
    for (const name of ['index.html', 'styles.css', 'app.js']) {
      assert.doesNotMatch(read(name), /title-strip|titleStrip|paintTitle/, `${name} still names the title strip`)
    }
    const preload = fs.readFileSync(path.join(__dirname, '..', 'src', 'preload', 'preload.js'), 'utf8')
    assert.doesNotMatch(preload, /titleStrip/, 'preload.js still exposes the strip height')
  })
})
