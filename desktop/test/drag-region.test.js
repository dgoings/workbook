'use strict'

// The window hides its native title bar on macOS and Windows, and with it gone
// nothing moves the window but an element the stylesheet marks as a drag
// region. The sidebar head is that element. From the shell's first build the
// mark sat on a :root:not(.is-mac) rule, so the Windows build dragged and the
// Mac build had nothing to take hold of; these pin the mark to the head
// itself, in both layouts, and keep every control inside it clickable.

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
