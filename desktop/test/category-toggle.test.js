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
  // of the page; without this, a second Enter would land on nothing.
  test('renderProjects gives the focus back to the toggle it rebuilt', () => {
    const body = fn('renderProjects')
    const before = body.indexOf("list.innerHTML = ''")
    const remembered = body.search(/document\.activeElement[\s\S]*?category-toggle/)
    assert.ok(remembered !== -1 && remembered < before,
      'renderProjects must note a focused .category-toggle before it empties the list')
    assert.match(body.slice(before), /\.category-toggle[\s\S]*?dataset\.categoryId === [\s\S]*?\.focus\(\)/,
      'renderProjects must focus the rebuilt toggle for the same category')
    assert.match(fn('categoryGroup'), /toggle\.dataset\.categoryId = category\.id/)
  })
})

describe('the rail draws the toggle as a group header', () => {
  test('the rail does not hide the toggle', () => {
    for (const [list, body] of rules) {
      if (!/display\s*:\s*none/.test(body)) continue
      for (const selector of selectorsOf(list)) {
        assert.notEqual(selector, ':root.sidebar-collapsed .category-toggle',
          'the rail hides .category-toggle, leaving a folded category unreachable')
      }
    }
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
