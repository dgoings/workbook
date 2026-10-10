'use strict'

// A folded category's projects are hidden in the rail as in the sidebar, so
// the rail's divider has to be the fold control too, or those projects are out
// of the pointer's reach until the sidebar is expanded again. categoryGroup is
// run here against a small fake DOM: app.js only loads inside Electron, so the
// function is taken out of its source, comments removed, the way
// sidebar-render.test.js reads it, and run with the few names it touches.

const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { initials } = require('../src/renderer/sidebarmodel')
const cascade = require('./support/cascade')

const src = path.join(__dirname, '..', 'src')

function code (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8')
    .replace(/\r\n/g, '\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

const app = code('renderer', 'app.js')
const css = code('renderer', 'styles.css')

function fn (name) {
  const match = app.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in app.js`)
  return match[0]
}

// Every rule block as [selector list, declarations], selectors on one line.
const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
  .map((match) => [match[1].trim().replace(/\s+/g, ' '), match[2]])

function selectorsOf (list) {
  return list.split(',').map((selector) => selector.trim())
}

// --- the fake DOM -------------------------------------------------------------

class FakeElement {
  constructor (tagName) {
    this.tagName = tagName.toUpperCase()
    this.className = ''
    this.dataset = {}
    this.children = []
    this.parentNode = null
    this.attributes = new Map()
    this.listeners = new Map()
    this.textContent = ''
    this.title = ''
    this.hidden = false
    this.draggable = false
    this.type = ''
    const self = this
    this.classList = {
      add (...names) { self.className = [...new Set([...self.className.split(/\s+/).filter(Boolean), ...names])].join(' ') },
      contains (name) { return self.className.split(/\s+/).includes(name) }
    }
  }

  append (...nodes) {
    for (const node of nodes) {
      node.parentNode = this
      this.children.push(node)
    }
  }

  setAttribute (name, value) { this.attributes.set(name, String(value)) }
  getAttribute (name) { return this.attributes.has(name) ? this.attributes.get(name) : null }
  addEventListener (type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, [])
    this.listeners.get(type).push(listener)
  }

  // A native button turns Enter and Space into a click; dispatching the click
  // stands for all three.
  click () {
    for (const listener of this.listeners.get('click') ?? []) listener({ target: this })
  }

  // Descendants, depth first, that carry `name` among their classes.
  find (name) {
    const found = []
    for (const child of this.children) {
      if (child.classList.contains(name)) found.push(child)
      found.push(...child.find(name))
    }
    return found
  }
}

function render (category, { dragging = false } = {}) {
  const calls = []
  const context = vm.createContext({
    document: { createElement: (tag) => new FakeElement(tag) },
    state: { editingCategoryId: null, removingCategoryId: null },
    drag: { current: null },
    sidebarModel: { initials },
    api: {
      setCategoryCollapsed: (id, collapsed) => {
        calls.push([id, collapsed])
        return Promise.resolve()
      }
    },
    justDragged: () => dragging,
    projectRow: () => { throw new Error('no projects in these categories') },
    startRename: () => { throw new Error('rename is not under test') },
    nameEditor: () => { throw new Error('rename is not under test') },
    removeConfirmation: () => { throw new Error('remove is not under test') },
    renderProjects: () => {},
    console
  })
  const categoryGroup = vm.runInContext(`${fn('categoryGroup')}\ncategoryGroup`, context)
  const group = categoryGroup(category, new Map())
  const [toggle, ...others] = group.find('category-toggle')
  assert.ok(toggle, 'the category header has no .category-toggle')
  assert.equal(others.length, 0, 'one fold control per category, not one per mode')
  return { group, toggle, calls }
}

const open = { kind: 'category', id: 'cw', name: 'Client work', collapsed: false, projects: [] }
const folded = { kind: 'category', id: 'p', name: 'Personal', collapsed: true, projects: [] }

describe('the fold toggle is the rail\'s category divider', () => {
  test('it is a real button carrying the initials the rail shows', () => {
    const { toggle } = render(open)
    assert.equal(toggle.tagName, 'BUTTON')
    assert.equal(toggle.type, 'button')
    const labels = toggle.find('category-initials')
    assert.equal(labels.length, 1, 'the initials must be inside the toggle, so the divider is what clicks')
    assert.equal(labels[0].textContent, 'CW')
    assert.equal(toggle.find('category-chevron').length, 1, 'the toggle draws its fold state as a chevron')
  })

  test('it names the whole category, says whether it is open, and shows the name on hover', () => {
    const shown = render(open).toggle
    assert.equal(shown.getAttribute('aria-expanded'), 'true')
    assert.equal(shown.getAttribute('aria-label'), 'Collapse Client work')
    assert.equal(shown.title, 'Client work')

    const hidden = render(folded).toggle
    assert.equal(hidden.getAttribute('aria-expanded'), 'false')
    assert.equal(hidden.getAttribute('aria-label'), 'Expand Personal')
    assert.equal(hidden.title, 'Personal')
  })

  test('a click asks the main process to flip the fold, and opens nothing', () => {
    const unfold = render(folded)
    unfold.toggle.click()
    assert.deepEqual(unfold.calls, [['p', false]])

    const fold = render(open)
    fold.toggle.click()
    assert.deepEqual(fold.calls, [['cw', true]])
  })

  test('the click that ends a drag does not fold anything', () => {
    const { toggle, calls } = render(open, { dragging: true })
    toggle.click()
    assert.deepEqual(calls, [])
  })
})

describe('a keyboard fold keeps its focus', () => {
  // The fold's broadcast redraws the list, which takes the focused toggle out
  // of the page; without this, a second Enter would land on nothing. Run
  // against a list that swaps its toggles on every redraw, the way the real one
  // does, with the focus on one of the old toggles.
  function redraw ({ focusedId, rebuiltIds = ['cw', 'p'], focusOutsideList = false }) {
    const focusCalls = []
    const toggleFor = (id) => ({
      dataset: { categoryId: id },
      classList: { contains: (name) => name === 'category-toggle' },
      focus () { focusCalls.push(id) }
    })
    const old = focusedId === null ? null : toggleFor(focusedId)
    let current = old ? [old] : []
    const list = {
      children: [],
      contains: (node) => !focusOutsideList && current.includes(node),
      querySelector: () => null,
      querySelectorAll: (selector) => selector === '.category-toggle' ? current : [],
      append (node) { this.children.push(node) },
      set innerHTML (value) { current = []; this.children = [] }
    }
    const context = vm.createContext({
      redraw: { held: false, owed: false },
      state: { editingCategoryId: null, projects: [], view: 'board', layout: { items: rebuiltIds.map((id) => ({ kind: 'category', id })) } },
      document: { activeElement: old, createElement: () => ({ style: {} }) },
      el: (id) => id === 'project-list' ? list : {},
      setView: () => {},
      projectRow: () => { throw new Error('no projects here') },
      categoryGroup: (item) => {
        current.push(toggleFor(item.id))
        return { id: item.id }
      }
    })
    vm.runInContext(`${fn('renderProjects')}
renderProjects()`, context)
    return focusCalls
  }

  test('renderProjects gives the focus back to the toggle it rebuilt', () => {
    assert.deepEqual(redraw({ focusedId: 'p' }), ['p'])
  })

  test('it follows the category by id, wherever the redraw puts it', () => {
    assert.deepEqual(redraw({ focusedId: 'p', rebuiltIds: ['p', 'cw'] }), ['p'])
  })

  test('it takes nothing when no toggle was focused, or the category is gone', () => {
    assert.deepEqual(redraw({ focusedId: null }), [])
    assert.deepEqual(redraw({ focusedId: 'p', focusOutsideList: true }), [])
    assert.deepEqual(redraw({ focusedId: 'gone' }), [])
  })

  test('categoryGroup stamps each toggle with its category id', () => {
    assert.match(fn('categoryGroup'), /toggle\.dataset\.categoryId = category\.id/)
  })
})

describe('the rail draws the toggle as a group header', () => {
  test('the rail shows the initials the base rule hides', () => {
    // .category-initials is display: none everywhere else, so without a rail
    // rule the divider is a bare chevron with no label.
    const base = rules.filter(([list]) => selectorsOf(list).includes('.category-initials'))
    assert.ok(base.some(([, body]) => /display\s*:\s*none/.test(body)),
      'the base .category-initials rule no longer hides them in the sidebar')
    const shown = rules.filter(([list]) => selectorsOf(list).includes(':root.sidebar-collapsed .category-initials'))
    assert.ok(shown.length > 0, 'no rule for :root.sidebar-collapsed .category-initials')
    const body = shown.map(([, declarations]) => declarations).join(';')
    assert.match(body, /display\s*:\s*(?:block|inline|inline-block|flex)\b/,
      'the rail must show the initials')
    assert.doesNotMatch(body, /display\s*:\s*none/, 'the rail hides the initials')
  })

  test('the rail styles the toggle as a clickable header of its own', () => {
    const blocks = rules.filter(([list]) => selectorsOf(list).includes(':root.sidebar-collapsed .category-toggle'))
    assert.ok(blocks.length > 0, 'no rule for :root.sidebar-collapsed .category-toggle')
    const body = blocks.map(([, declarations]) => declarations).join(';')
    assert.match(body, /cursor\s*:\s*pointer/)
    // A bordered box is what a project key looks like; the header must not.
    assert.doesNotMatch(body, /(?:^|[;\s])border\s*:\s*1px solid/)
  })

  test('folding turns the chevron, not the whole toggle and its initials', () => {
    const turned = rules.filter(([, body]) => /transform\s*:\s*rotate/.test(body))
    assert.ok(turned.some(([list]) => selectorsOf(list).includes('.category.collapsed .category-chevron')),
      'no rule rotates .category.collapsed .category-chevron')
    for (const [list] of turned) {
      for (const selector of selectorsOf(list)) {
        assert.doesNotMatch(selector, /\.category-toggle$/, `${selector} rotates the initials with the chevron`)
      }
    }
  })
})

describe('the rail toggle is there to be hit', () => {
  // The real stylesheet's cascade, run on the real page collapsed to the rail
  // with categoryGroup's own markup in the project list. A rule that sets
  // display none, visibility, opacity 0 or pointer-events none on the toggle or
  // an ancestor fails this whatever its selector, and so does one that leaves
  // the declared minimum under the 24px WCAG 2.5.8 asks of a pointer target.
  // It reads declarations, not layout: a rule that clips the toggle (a zero
  // height with overflow hidden on its header), scales it away, or moves it
  // off screen passes, and only a measurement in the running app catches that.
  const stylesheet = cascade.parseStylesheet(fs.readFileSync(path.join(src, 'renderer', 'styles.css'), 'utf8'))
  const page = fs.readFileSync(path.join(src, 'renderer', 'index.html'), 'utf8')
  const TARGET = 24

  function adopt (fake) {
    const attributes = Object.fromEntries(fake.attributes)
    if (fake.type) attributes.type = fake.type
    if (fake.title) attributes.title = fake.title
    if (fake.hidden) attributes.hidden = ''
    if (fake.draggable) attributes.draggable = 'true'
    const element = cascade.node(fake.tagName, {
      classes: fake.className.split(/\s+/).filter(Boolean),
      attributes,
      text: fake.textContent
    })
    return cascade.append(element, ...fake.children.map(adopt))
  }

  function railPage ({ collapsed = true, platform, theme = null, board = false }) {
    const root = cascade.parseHTML(page)
    if (collapsed) root.classes.add('sidebar-collapsed')
    root.classes.add(`is-${platform}`)
    if (board) root.classes.add('board-active')
    if (theme) root.attributes.set('data-theme', theme)
    const list = cascade.byId(root, 'project-list')
    assert.ok(list, 'index.html has no #project-list')
    const groups = [open, folded].map((category) => adopt(render(category).group))
    cascade.append(list, ...groups)
    const toggles = groups.map((group) => group.children[0].children[0])
    for (const toggle of toggles) assert.ok(toggle.classes.has('category-toggle'))
    return { groups, toggles }
  }

  // Every way the page can stand: each media condition on and off, each
  // explicit theme and none, each platform, with and without a board showing,
  // and the toggle at rest, hovered and focused.
  function * scenarios () {
    for (const env of cascade.combinations(cascade.conditions(stylesheet))) {
      for (const theme of [null, 'light', 'dark']) {
        for (const platform of ['mac', 'windows', 'linux']) {
          for (const board of [false, true]) {
            for (const interaction of ['rest', 'hover', 'focus']) {
              yield { env, theme, platform, board, interaction }
            }
          }
        }
      }
    }
  }

  function check (scenario, inspect) {
    for (const toggle of railPage(scenario).toggles) {
      const state = scenario.interaction === 'rest'
        ? cascade.interaction()
        : cascade.interaction({ [scenario.interaction]: toggle })
      const where = `${cascade.describe(toggle)} in the rail, ${JSON.stringify(scenario)}`
      inspect(toggle, { env: scenario.env, state }, where)
    }
  }

  test('the rail shows the toggle and lets the pointer reach it', () => {
    for (const scenario of scenarios()) {
      check(scenario, (toggle, options, where) => {
        assert.equal(cascade.unreachable(stylesheet, toggle, options), null,
          `a folded category is out of reach: ${where}`)
      })
    }
  })

  test('the rail toggle is at least 24px tall and wide', () => {
    // A minimum or a fixed size holds whatever the glyphs measure: box-sizing
    // aside, the border box is never smaller than min-height or height, or than
    // min-width or width. Either way of asking for 24px passes.
    for (const scenario of scenarios()) {
      check(scenario, (toggle, options, where) => {
        const height = cascade.leastSize(stylesheet, toggle, 'height', options)
        const width = cascade.leastSize(stylesheet, toggle, 'width', options)
        assert.ok(height >= TARGET, `neither min-height nor height asks for ${TARGET}px, only ${height}px: ${where}`)
        assert.ok(width >= TARGET, `neither min-width nor width asks for ${TARGET}px, only ${width}px: ${where}`)
      })
    }
  })

  test('the guard sees what the rail does hide', () => {
    // Without this, a cascade that matched nothing would pass both tests above.
    const options = { env: {}, state: cascade.interaction() }
    const rail = railPage({ platform: 'mac' })
    const name = rail.groups[0].children[0].children[1]
    assert.ok(name.classes.has('category-name'))
    assert.match(cascade.unreachable(stylesheet, name, options), /display: none/)
    const tiles = rail.groups[1].children[1]
    assert.ok(tiles.classes.has('category-projects'))
    assert.match(cascade.unreachable(stylesheet, tiles, options), /display: none \(\[hidden\]\)/)

    const sidebar = railPage({ collapsed: false, platform: 'mac' })
    assert.equal(cascade.unreachable(stylesheet, sidebar.groups[0].children[0].children[1], options), null)
    const initials = sidebar.toggles[0].children[1]
    assert.ok(initials.classes.has('category-initials'))
    assert.match(cascade.unreachable(stylesheet, initials, options), /display: none/)
  })
})
