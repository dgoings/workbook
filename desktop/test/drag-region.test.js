'use strict'

// The window hides its native title bar on macOS and Windows, and with it gone
// nothing moves the window but an element the stylesheet marks as a drag
// region. Two elements are: the sidebar head, and the title strip across the
// top of the main area, which together make the window's whole top edge a
// handle. From the shell's first build the head's mark sat on a
// :root:not(.is-mac) rule, so the Windows build dragged and the Mac build had
// nothing to take hold of; these pin the mark to the head itself, in both
// layouts, and keep every control inside it clickable, and they pin the strip
// to the same rule and to the theme.

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

describe('the title strip', () => {
  test('the strip itself is a drag region, and nothing narrower takes it back', () => {
    const own = css.filter((rule) => rule.selectors.includes('.title-strip') && rule.region !== null)
    assert.deepEqual(own.map((rule) => rule.region), ['drag'],
      'a rule whose selector is exactly .title-strip must declare -webkit-app-region: drag')
    for (const rule of css) {
      for (const selector of rule.selectors) {
        if (!/\.title-strip$/.test(selector) || selector === '.title-strip') continue
        if (rule.region === null) continue
        assert.equal(rule.region, 'drag', `${selector} sets -webkit-app-region: ${rule.region} on the strip`)
      }
    }
  })

  // A drag region swallows clicks and hovers, so the strip is text only. A
  // control added to it later has to opt out the way the head's do, and has
  // to be thought about: this fails until it is.
  test('the strip holds nothing that needs the pointer', () => {
    const strip = read('index.html').match(/<header class="title-strip"[^>]*>([\s\S]*?)<\/header>/)
    assert.ok(strip, 'index.html has no <header class="title-strip">')
    const titled = new Set([...read('app.js').matchAll(/el\('([\w-]+)'\)\.title\s*=/g)].map((m) => m[1]))
    for (const tag of strip[1].matchAll(/<([a-z][\w-]*)\b([^>]*)>/g)) {
      assert.doesNotMatch(tag[1], /^(button|a|input|select|textarea)$/,
        `<${tag[1]}> in the title strip would be dragged, not clicked`)
      const id = tag[2].match(/\bid="([^"]*)"/)
      assert.ok(!(id && titled.has(id[1])),
        `#${id && id[1]} in the title strip has a tooltip the drag region would hide`)
    }
  })

  // Above every view, so it is there over the import and Next views as well as
  // over a board, and #main's flex column pushes each view down by its height.
  test('the strip is the first thing in the main area', () => {
    const main = read('index.html').match(/<main id="main">\s*(?:<!--[\s\S]*?-->\s*)*<([a-z]+)[^>]*class="([^"]*)"/)
    assert.ok(main, 'index.html has no <main id="main"> with an element in it')
    assert.equal(main[2], 'title-strip', 'the first element in #main must be the title strip')
  })

  // The strip continues the sidebar head across the window, so it follows the
  // theme the way the head does: through the tokens the dark blocks move, never
  // a color of its own that only one mode suits.
  test('the strip takes its colors from the theme tokens', () => {
    const blocks = bodies(read('styles.css'), '.title-strip')
    assert.equal(blocks.length, 1, 'expected exactly one rule whose selector is .title-strip')
    const block = blocks[0]
    assert.match(block, /(?:^|[;\s])background\s*:\s*var\(--wb-[\w-]+\)\s*;/,
      'the strip background must be a --wb-* token')
    assert.match(block, /(?:^|[;\s])color\s*:\s*var\(--wb-[\w-]+\)\s*;/,
      'the strip color must be a --wb-* token')
    assert.doesNotMatch(block, /#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(/,
      'the strip rule must not hold a literal color')
  })

  // The main process starts the boards titleStripHeight() down; a strip of any
  // other height is a gap above the board or a board over the strip.
  test('its height is the one the renderer copies from the main process', () => {
    const block = bodies(read('styles.css'), '.title-strip')[0]
    assert.match(block, /(?:^|[;\s])height\s*:\s*var\(--title-strip-height\b/,
      'the strip height must come from --title-strip-height')
    assert.match(read('app.js'), /setProperty\('--title-strip-height', `\$\{api\.titleStripHeight\}px`\)/,
      'app.js must set --title-strip-height from the preload\'s titleStripHeight')
  })
})
