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

const cascade = require('./support/cascade')

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

// Where the traffic lights end: three 14px buttons on 23px spacing from the
// pinned x, as layout.js derives it.
const MAC_INSET_MIN = require('../src/main/layout').TRAFFIC_LIGHTS_END

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
    assert.match(bodies(read('styles.css'), '.version').join(';'), /grid-column\s*:\s*1\s*\/\s*-1\s*;/,
      'the version line must span both columns, under the chevron as well')
    assert.equal(place('.version').row, '2')
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

// The shell's own views, import and Next, and the project view while no board
// is laid in, fill the main column from the window's top edge, and with the
// title strip gone nothing there was marked: only the sidebar head moved the
// window. A band as tall as the title row now opens the column, under the
// board when one shows, and the heads of import and Next carry the drag on
// from it to the end of their headings, as the board's own header does beside
// a board. These read the real page through the cascade, so a rule anywhere
// that reaches one of these elements counts.
describe('the shell\'s own views drag by their top row', () => {
  // Only the rules that mark a region: the cascade throws on selectors it
  // cannot evaluate, such as the notes column's :has(), and none of those
  // rules can change the answer.
  const sheet = cascade.parseStylesheet(read('styles.css'))
    .filter((rule) => rule.declarations.some((declaration) => declaration.property === '-webkit-app-region'))
  const envs = cascade.combinations(cascade.conditions(cascade.parseStylesheet(read('styles.css'))))

  function page ({ board = false, collapsed = false, platform = 'mac' } = {}) {
    const root = cascade.parseHTML(read('index.html'))
    for (const view of ['view-import', 'view-next', 'view-project']) {
      cascade.byId(root, view).attributes.delete('hidden')
    }
    root.classes.add(`is-${platform}`)
    if (collapsed) root.classes.add('sidebar-collapsed')
    if (board) root.classes.add('board-active')
    return root
  }

  // Every way app.js marks the root: each platform preload.js names, the
  // sidebar expanded and collapsed to the rail, with and without a board.
  const states = ['mac', 'windows', 'linux'].flatMap((platform) =>
    [false, true].flatMap((collapsed) =>
      [false, true].map((board) => ({ platform, collapsed, board }))))

  // The region a point over `element` falls in. Electron applies the marked
  // boxes in document order, adding a drag box and cutting out a no-drag one,
  // so what decides is the nearest box around the element that is marked.
  function region (element, env) {
    for (let at = element; at; at = at.parent) {
      const win = cascade.cascaded(sheet, at, '-webkit-app-region', { env })
      if (win) return { value: win.declaration.value, from: cascade.describe(at), selector: win.selector }
    }
    return { value: 'none', from: null, selector: null }
  }

  function * elements (root) {
    yield root
    for (const child of root.children) yield * elements(child)
  }

  const heads = (root) => ['view-import', 'view-next'].map((id) => {
    const head = cascade.byId(root, id).children[0]
    assert.ok(head && head.classes.has('view-head'),
      `#${id} must open with its .view-head, so the head starts where the band ends`)
    return head
  })

  test('a band as tall as the title row opens the main column and drags', () => {
    const main = cascade.byId(page(), 'main')
    const band = main.children[0]
    assert.ok(band && band.classes.has('main-title-row'),
      '<main id="main"> must open with the .main-title-row band, above every view')
    assert.equal(band.attributes.get('aria-hidden'), 'true', 'the band is chrome, not content')
    assert.ok(main.children.slice(1).every((child) => child.classes.has('view')),
      'every view must sit under the band, inside #main')
    for (const state of states.filter((s) => !s.board)) {
      const unboarded = cascade.byId(page(state), 'main').children[0]
      for (const env of envs) {
        assert.equal(region(unboarded, env).value, 'drag',
          `with no board showing the band must drag under ${JSON.stringify({ ...state, ...env })}`)
      }
    }
    assert.match(bodies(read('styles.css'), '.main-title-row').join(';'),
      /(?:^|[;\s])flex\s*:\s*0\s+0\s+var\(--title-row-height\b[^;]*\)\s*;/,
      'the band must be exactly the title row\'s height, from the shared --title-row-height')
    // Only that block sizes or shows the band; any other rule that reaches it
    // may mark its region and nothing else.
    const bare = read('styles.css').replace(/\/\*[\s\S]*?\*\//g, '')
    for (const match of bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const selectors = match[1].split(',').map((s) => s.trim().replace(/\s+/g, ' '))
      if (!selectors.some((s) => /\.main-title-row\b/.test(s) && s !== '.main-title-row')) continue
      const properties = match[2].split(';').map((d) => d.split(':')[0].trim()).filter(Boolean)
      assert.deepEqual(properties.filter((p) => p !== '-webkit-app-region'), [],
        `${match[1].trim()} may set only -webkit-app-region on the band`)
    }
  })

  // The shell's view covers the whole window, the board's view lies over it,
  // and Electron 44 unions the drag regions of every visible view: a no-drag in
  // the board does not cut a hole in a drag region of the shell beneath it
  // (main.js layout()). So while a board shows, nothing in the shell's main
  // column may drag, or the board's skip link and any header control in the
  // top row would move the window instead of clicking.
  test('beside a board the main column adds no drag region under the board', () => {
    for (const state of states.filter((s) => s.board)) {
      const root = page(state)
      const main = cascade.byId(root, 'main')
      for (const top of [main.children[0], cascade.byId(root, 'view-project')]) {
        for (const element of elements(top)) {
          for (const env of envs) {
            const { value, from, selector } = region(element, env)
            assert.notEqual(value, 'drag',
              `${cascade.describe(element)} resolves to drag from ${from} (${selector}) beside a board under ${JSON.stringify({ ...state, ...env })}`)
          }
        }
      }
    }
  })

  test('each view starts under the band rather than beside it', () => {
    const block = bodies(read('styles.css'), '.view').join(';')
    const padding = block.match(/(?:^|[;\s])padding\s*:\s*([^;]+);/)
    assert.ok(padding, '.view must set its padding')
    assert.equal(padding[1].trim().split(/\s+/)[0], '0',
      'a view keeps no top padding: the band above it is the room the window controls take')
    assert.doesNotMatch(block, /padding-top/, '.view must not add top padding back')
  })

  test('the heads of import and Next carry the drag on from the band', () => {
    for (const head of heads(page())) {
      for (const env of envs) {
        const { value, from, selector } = region(head, env)
        assert.equal(value, 'drag',
          `the head of #${head.parent.id} resolves to ${value} from ${from} (${selector}) under ${JSON.stringify(env)}`)
      }
    }
  })

  test('every control, tooltip and path in the band and those heads still takes the pointer', () => {
    const root = page()
    const titled = new Set([...read('app.js').matchAll(/el\('([\w-]+)'\)\.title\s*=/g)].map((m) => m[1]))
    const needs = (element) => /^(button|a|input|select|textarea|code)$/.test(element.tag) ||
      element.attributes.has('title') || (element.id !== '' && titled.has(element.id))
    const checked = []
    for (const top of [cascade.byId(root, 'main').children[0], ...heads(root)]) {
      for (const element of elements(top)) {
        if (element === top || !needs(element)) continue
        checked.push(element.id)
        for (const env of envs) {
          const { value, from, selector } = region(element, env)
          assert.equal(value, 'no-drag',
            `${cascade.describe(element)} in ${cascade.describe(top)} resolves to ${value} from ${from} (${selector}) under ${JSON.stringify(env)}`)
        }
      }
    }
    assert.ok(checked.includes('dismiss-path-note'), 'expected the import head to hold the PATH notice\'s dismiss button')
    assert.ok(checked.includes('dismiss-key-note'), 'expected the import head to hold the key caution\'s dismiss button')
    assert.ok(checked.includes('path-note-directory'),
      'expected the import head to hold the PATH notice\'s directory, which must stay selectable')
  })
})
