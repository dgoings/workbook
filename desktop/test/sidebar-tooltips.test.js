'use strict'

// Native tooltips in the sidebar have to survive electron/electron#49843: on
// macOS, when the pointer moves onto a different node that has the same
// tooltip, Chromium clears the tooltip and sets the same text again, and macOS
// then rarely shows it after the first hover. So every titled element in the
// sidebar is the only node under the pointer for its own tooltip: whatever
// inside it only shows text or decoration ignores the pointer, and a button
// inside it carries a title of its own with different words. "One node under
// the pointer" in styles.css says why.
//
// The guard runs the real stylesheet's cascade over the real page and the
// rows projectRow and categoryGroup build, in every state a header can be in,
// in the rail and expanded. It is the cascade from test/support/cascade.js,
// not a browser, so it reads declarations rather than layout; a full-size
// overlay would be invisible to it, and so would a child moved outside its
// parent's box. app.js runs only in a page, so its functions are taken out of
// its source the way category-toggle.test.js takes them, and run against a
// small fake DOM.

const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { initials } = require('../src/renderer/sidebarmodel')
const cascade = require('./support/cascade')

const src = path.join(__dirname, '..', 'src')

function read (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8').replace(/\r\n/g, '\n')
}

function code (...parts) {
  return read(...parts)
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

const app = code('renderer', 'app.js')
const page = read('renderer', 'index.html')
const stylesheet = cascade.parseStylesheet(read('renderer', 'styles.css'))

function fn (name) {
  const match = app.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in app.js`)
  return match[0]
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
    this.value = ''
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

  // A tag name or a class, or a list of them: all the handlers here ask.
  closest (selector) {
    const tests = selector.split(',').map((part) => part.trim())
    for (const part of tests) {
      if (!/^\.?[\w-]+$/.test(part)) throw new Error(`the fake DOM cannot match ${part}`)
    }
    for (let at = this; at; at = at.parentNode) {
      if (tests.some((part) => part.startsWith('.') ? at.classList.contains(part.slice(1)) : at.tagName === part.toUpperCase())) return at
    }
    return null
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

// An event that bubbles from `target` up through its ancestors, as a real
// double-click does.
function fire (target, type) {
  const event = { type, target }
  for (let at = target; at; at = at.parentNode) {
    for (const listener of at.listeners.get(type) ?? []) listener(event)
  }
}

const project = (id, key, name) => ({ id, key, name, path: `/src/${name}`, status: 'running' })
const projects = new Map([
  ['a', project('a', 'WB', 'workbook')],
  ['b', project('b', 'SITE', 'site')],
  ['c', project('c', 'API', 'api')],
  ['d', project('d', 'OPS', 'ops')],
  ['e', project('e', 'DOC', 'docs')]
])

const open = { kind: 'category', id: 'cw', name: 'Client work', collapsed: false, projects: ['b'] }
const folded = { kind: 'category', id: 'p', name: 'Personal', collapsed: true, projects: ['c'] }
const editing = { kind: 'category', id: 'ed', name: 'Editing', collapsed: false, projects: ['d'] }
const removing = { kind: 'category', id: 'rm', name: 'Removing', collapsed: false, projects: ['e'] }

// projectRow, categoryGroup and the two header states they hand off to, run
// with the few names they touch. `rail` is what document.documentElement says
// about the sidebar while a handler runs.
function renderer ({ rail = false } = {}) {
  const renames = []
  const context = vm.createContext({
    document: {
      createElement: (tag) => new FakeElement(tag),
      documentElement: { classList: { contains: (name) => rail && name === 'sidebar-collapsed' } }
    },
    state: { editingCategoryId: 'ed', removingCategoryId: 'rm', activeProjectId: null, editingDraft: null, editingSelection: null, editingRefused: false },
    drag: { current: null },
    sidebarModel: { initials },
    api: {
      setCategoryCollapsed: () => Promise.resolve(),
      renameCategory: () => Promise.resolve(),
      deleteCategory: () => Promise.resolve()
    },
    justDragged: () => false,
    openProject: () => {},
    startRename: (id) => { renames.push(id) },
    renderProjects: () => {},
    // The name field focuses itself once it is in the page; nothing here is.
    queueMicrotask: () => {},
    console
  })
  const names = ['projectRow', 'categoryGroup', 'nameEditor', 'removeConfirmation', 'buttonText']
  const built = vm.runInContext(`${names.map(fn).join('\n')}\n({ projectRow, categoryGroup })`, context)
  return { ...built, renames }
}

// --- the page, as the cascade reads it ---------------------------------------

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

function walk (node, visit) {
  visit(node)
  for (const child of node.children) walk(child, visit)
}

// The sidebar as boot leaves it: the version line titled, the Next entry shown
// (it is, from two projects up), and the list holding a top-level project and
// a category in each state its header has.
function sidebarPage ({ collapsed, platform }) {
  const root = cascade.parseHTML(page)
  if (collapsed) root.classes.add('sidebar-collapsed')
  root.classes.add(`is-${platform}`)
  const version = cascade.byId(root, 'version')
  assert.ok(version, 'index.html has no #version')
  version.attributes.set('title', 'Bundled build\nworkbook 1.2.3\n/Applications/Workbench.app/workbook')
  const next = cascade.byId(root, 'next-item')
  assert.ok(next, 'index.html has no #next-item')
  next.attributes.delete('hidden')
  const list = cascade.byId(root, 'project-list')
  assert.ok(list, 'index.html has no #project-list')
  const { projectRow, categoryGroup } = renderer()
  const rows = [projectRow(projects.get('a')), ...[open, folded, editing, removing].map((category) => categoryGroup(category, projects))]
  cascade.append(list, ...rows.map(adopt))
  const sidebar = cascade.byId(root, 'sidebar')
  assert.ok(sidebar, 'index.html has no #sidebar')
  return { root, sidebar }
}

const CONTROLS = new Set(['button', 'input', 'select', 'textarea', 'a'])

function title (node) {
  return node.attributes.get('title') || null
}

// Whether the element has a box and takes the pointer: no ancestor is display
// none, it is not display: contents itself, and it is neither hidden nor
// ignoring the pointer. Opacity does not count: a transparent element still
// takes the pointer, and so still sits between the pointer and its tooltip.
function boxed (rules, node, options) {
  return cascade.ancestors(node).every((at) => cascade.computed(rules, at, 'display', options) !== 'none')
}

function takesPointer (rules, node, options) {
  return cascade.computed(rules, node, 'visibility', options) === 'visible' &&
    cascade.computed(rules, node, 'pointer-events', options) !== 'none'
}

function hittable (rules, node, options) {
  return boxed(rules, node, options) &&
    cascade.computed(rules, node, 'display', options) !== 'contents' &&
    takesPointer(rules, node, options)
}

// Every place in `sidebar` where the pointer could move onto a different node
// with the same tooltip, as sentences. Text counts: a text node is a node of
// its own to the hit test, so text directly inside a titled element, or inside
// an element that shares its tooltip, is a second node with that tooltip.
function crossings (rules, sidebar, options) {
  const found = []
  function visit (node, around) {
    const own = title(node)
    const tooltip = own ?? around?.title ?? null
    if (tooltip !== null) {
      const where = cascade.describe(node)
      if (around && hittable(rules, node, options)) {
        if (own === null) {
          found.push(`${where} takes the pointer inside ${cascade.describe(around.node)} with no title of its own, so it shares the tooltip "${around.title}"`)
        } else if (own === around.title) {
          found.push(`${where} takes the pointer inside ${cascade.describe(around.node)} and repeats its tooltip "${own}"`)
        }
      }
      if (node.text !== '' && boxed(rules, node, options) && takesPointer(rules, node, options)) {
        found.push(`the text "${node.text}" directly inside ${where} takes the pointer, a second node with the tooltip "${tooltip}"`)
      }
    }
    const next = own === null ? around : { node, title: own }
    for (const child of node.children) visit(child, next)
  }
  visit(sidebar, null)
  return found
}

// Every titled element and every control in the sidebar that is drawn but
// would not take the pointer: a tooltip nobody can hover, or a control nobody
// can press, is the over-correction this fix must not make.
function stranded (rules, sidebar, options) {
  const found = []
  walk(sidebar, (node) => {
    if (title(node) === null && !CONTROLS.has(node.tag)) return
    if (!boxed(rules, node, options)) return
    if (!hittable(rules, node, options)) found.push(`${cascade.describe(node)} is drawn but does not take the pointer`)
  })
  return found
}

// Every way the sidebar can stand that the cascade can tell apart: in the
// rail and expanded, on each platform, each media condition on and off, with
// and without a board beside it, at rest and with the pointer over each
// titled element in turn.
function * scenarios (rules) {
  for (const collapsed of [true, false]) {
    for (const platform of ['mac', 'windows', 'linux']) {
      for (const env of cascade.combinations(cascade.conditions(rules))) {
        for (const board of [false, true]) {
          const { root, sidebar } = sidebarPage({ collapsed, platform })
          if (board) root.classes.add('board-active')
          const hovers = [null]
          walk(sidebar, (node) => { if (title(node) !== null) hovers.push(node) })
          for (const hover of hovers) {
            const state = cascade.interaction(hover ? { hover } : {})
            const label = JSON.stringify({ collapsed, platform, env, board, hover: hover && cascade.describe(hover) })
            yield { sidebar, options: { env, state, memo: new Map() }, label }
          }
        }
      }
    }
  }
}

describe('every tooltip in the sidebar is one node under the pointer', () => {
  test('no node takes the pointer inside a titled element with the same tooltip', () => {
    let runs = 0
    for (const { sidebar, options, label } of scenarios(stylesheet)) {
      assert.deepEqual(crossings(stylesheet, sidebar, options), [], label)
      runs += 1
    }
    assert.ok(runs > 0)
  })

  test('every titled element and every control still takes the pointer', () => {
    for (const { sidebar, options, label } of scenarios(stylesheet)) {
      assert.deepEqual(stranded(stylesheet, sidebar, options), [], label)
    }
  })

  test('the page has the titled elements the guard is meant to cover', () => {
    // Without these, a page the guard read wrongly would pass with nothing in
    // it. The version line's title is set by boot, not the markup.
    assert.match(fn('boot'), /el\('version'\)\.title = /)
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    const titled = []
    walk(sidebar, (node) => { if (title(node) !== null) titled.push(cascade.describe(node)) })
    for (const expected of [
      'div#version.version',
      'button#sidebar-toggle.icon-button.sidebar-toggle',
      'button.rail-item',
      'button#new-category.icon-button.new-category',
      'button#next-item.project-item.next-item',
      'li.project-item',
      'div.category-head',
      'button.category-toggle',
      'button.category-action',
      'button.category-action.category-confirm-yes'
    ]) {
      assert.ok(titled.includes(expected), `expected ${expected} among the titled elements, found ${titled.join(', ')}`)
    }
  })
})

describe('the guard sees a regression', () => {
  // A memo of its own for every page and stylesheet: each test below changes
  // one or the other.
  const rest = () => ({ env: {}, state: cascade.interaction(), memo: new Map() })

  test('it fails the stylesheet without the rule that takes children off the pointer', () => {
    const without = stylesheet.filter((rule) => !rule.selectors.includes('.project-item > *'))
    assert.ok(without.length < stylesheet.length, 'no rule has the selector .project-item > *')
    // Both layouts: the rail hides the version line and the category names,
    // and the expanded sidebar hides the initials.
    const found = [true, false]
      .flatMap((collapsed) => crossings(without, sidebarPage({ collapsed, platform: 'mac' }).sidebar, rest()))
      .join('\n')
    for (const node of ['span.dot', 'span.project-key', 'span.category-chevron', 'span.category-initials', 'svg', 'span#version-text', 'span.glyph', 'span.category-name', 'span.category-confirm']) {
      // A dot also carries its status as a class: span.dot.running.
      assert.match(found, new RegExp(`^${node.replace(/[.#]/g, '\\$&')}(?:\\.[\\w-]+)* takes the pointer`, 'm'))
    }
  })

  test('it fails a button that repeats the tooltip around it', () => {
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    let toggle = null
    walk(sidebar, (node) => { if (!toggle && node.classes.has('category-toggle')) toggle = node })
    toggle.attributes.set('title', title(toggle.parent))
    assert.match(crossings(stylesheet, sidebar, rest()).join('\n'), /^button\.category-toggle takes the pointer inside div\.category-head and repeats its tooltip/m)
  })

  test('it fails a header whose name field shares its tooltip', () => {
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    let input = null
    walk(sidebar, (node) => { if (!input && node.classes.has('category-name-input')) input = node })
    assert.ok(input, 'the editing category has no name field')
    input.parent.attributes.set('title', 'Editing')
    assert.match(crossings(stylesheet, sidebar, rest()).join('\n'), /^input\.category-name-input takes the pointer inside div\.category-head with no title/m)
  })

  test('it fails text set straight onto a titled element', () => {
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    const version = cascade.byId(sidebar, 'version')
    version.children = []
    version.text = 'workbook 1.2.3'
    assert.match(crossings(stylesheet, sidebar, rest()).join('\n'), /^the text "workbook 1\.2\.3" directly inside div#version\.version takes the pointer/m)
  })

  test('it fails a titled element that stops taking the pointer itself', () => {
    const without = stylesheet.concat(cascade.parseStylesheet('.category-head { pointer-events: none; }')
      .map((rule) => ({ ...rule, order: stylesheet.length })))
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    assert.match(stranded(without, sidebar, rest()).join('\n'), /^div\.category-head is drawn but does not take the pointer/m)
  })
})

describe('a double-click on a category header renames it', () => {
  function header (category, { rail = false } = {}) {
    const built = renderer({ rail })
    const group = built.categoryGroup(category, projects)
    const [head] = group.find('category-head')
    assert.ok(head, 'the category has no .category-head')
    return { head, renames: built.renames }
  }

  test('anywhere on the header that is not a button', () => {
    const { head, renames } = header(open)
    fire(head, 'dblclick')
    assert.deepEqual(renames, ['cw'])
    // In a page the name ignores the pointer and the target is the header; a
    // target on the name itself, should one arrive, renames all the same.
    const [name] = head.find('category-name')
    fire(name, 'dblclick')
    assert.deepEqual(renames, ['cw', 'cw'])
    assert.equal(name.listeners.size, 0, 'the name has a listener of its own again')
  })

  test('not on the fold toggle, its chevron, or the rename and remove buttons', () => {
    const { head, renames } = header(open)
    const [toggle] = head.find('category-toggle')
    fire(toggle, 'dblclick')
    fire(toggle.find('category-chevron')[0], 'dblclick')
    const actions = head.find('category-action')
    assert.equal(actions.length, 2)
    for (const action of actions) {
      fire(action, 'dblclick')
      fire(action.children[0], 'dblclick')
    }
    assert.deepEqual(renames, [])
  })

  test('not in the rail, where the name is hidden', () => {
    const { head, renames } = header(open, { rail: true })
    fire(head, 'dblclick')
    assert.deepEqual(renames, [])
  })

  test('not while the name is being typed or the remove question is asked', () => {
    for (const category of [editing, removing]) {
      const { head, renames } = header(category)
      fire(head, 'dblclick')
      assert.deepEqual(renames, [], category.name)
    }
  })

  test('a header being renamed carries no tooltip for its field to share', () => {
    assert.equal(header(editing).head.title, '')
    assert.equal(header(removing).head.title, 'Removing')
    assert.equal(header(open).head.title, 'Client work')
  })
})
