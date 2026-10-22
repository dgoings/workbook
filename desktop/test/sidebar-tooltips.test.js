'use strict'

// Native tooltips in the sidebar have to survive electron/electron#49843, so
// every titled element in the sidebar is the only node under the pointer for
// its own tooltip; test/support/tooltips.js has the guard and says why, and
// "One node under the pointer" in styles.css has the rule. This runs the guard
// over the real page and the rows projectRow and categoryGroup build, in every
// state a header can be in, in the rail and expanded.

const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { initials } = require('../src/renderer/sidebarmodel')
const cascade = require('./support/cascade')
const { page, stylesheet, fn, FakeElement, adopt, walk, title, crossings, stranded, rest } = require('./support/tooltips')

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

describe('the version line boot writes is one node under the pointer', () => {
  // The page above stands in for boot's version line with a title alone. This
  // runs boot itself against index.html, through an el() whose textContent
  // replaces an element's children the way the DOM's does, so writing the
  // words onto the line rather than its span leaves a bare text node under the
  // line's tooltip, and the guard sees it.
  function live (node) {
    return {
      set textContent (value) {
        node.children = []
        node.text = String(value)
      },
      set title (value) { node.attributes.set('title', String(value)) },
      set value (value) { node.attributes.set('value', String(value)) },
      classList: { add: (...names) => { for (const name of names) node.classes.add(name) } }
    }
  }

  async function booted (version) {
    const root = cascade.parseHTML(page)
    const context = vm.createContext({
      document: {
        documentElement: { classList: { add () {} }, style: { setProperty () {} } }
      },
      api: {
        platform: 'mac',
        titleRowHeight: 36,
        railWidth: 88,
        getTheme: async () => ({ theme: 'system' }),
        getSidebar: async () => ({ collapsed: false }),
        version
      },
      el: (id) => {
        const node = cascade.byId(root, id)
        assert.ok(node, `boot asked for #${id}, which index.html does not have`)
        return live(node)
      },
      state: { projects: [] },
      paintTheme () {},
      paintSidebar () {},
      showPathNotice () {},
      readStoredLimit: () => 1,
      loadProjects: async () => {},
      setView () {}
    })
    await vm.runInContext(`${fn('boot')}\nboot()`, context)
    return root
  }

  const found = async () => ({ version: '1.2.3', bundled: true, path: '/Applications/Workbench.app/workbook' })
  const missing = async () => { throw new Error('workbook is not on PATH') }

  for (const [outcome, version, words] of [['found', found, 'workbook 1.2.3'], ['not found', missing, 'workbook not found']]) {
    test(`with the CLI ${outcome}, the words go in the span and the line keeps no text of its own`, async () => {
      const root = await booted(version)
      const line = cascade.byId(root, 'version')
      assert.ok(title(line), 'boot gave the version line no tooltip')
      assert.equal(line.text, '', 'boot wrote text straight onto the version line')
      const span = cascade.byId(line, 'version-text')
      assert.ok(span, 'the version line lost its span')
      assert.equal(span.text, words)
      for (const collapsed of [false, true]) {
        if (collapsed) root.classes.add('sidebar-collapsed')
        const options = { env: {}, state: cascade.interaction(), memo: new Map() }
        assert.deepEqual(crossings(stylesheet, cascade.byId(root, 'sidebar'), options), [], `collapsed: ${collapsed}`)
      }
    })
  }
})

describe('the guard sees a regression', () => {
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

  test('it fails a titled element that ellipsizes the span that ignores the pointer', () => {
    // The version line as it was: the line, not its span, cut the text short.
    const before = stylesheet.concat(cascade.parseStylesheet('.version { text-overflow: ellipsis; }')
      .map((rule) => ({ ...rule, order: stylesheet.length })))
    const { sidebar } = sidebarPage({ collapsed: false, platform: 'mac' })
    assert.match(crossings(before, sidebar, rest()).join('\n'), /^div#version\.version takes the pointer and ellipsizes its children/m)
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
