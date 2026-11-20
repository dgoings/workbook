'use strict'

// The sidebar's remove questions, from the keyboard. Pressing a row's × draws
// the question in the row's place, which destroys the button that had the
// focus; without help the focus falls to the page and the reader starts over
// from the top. So the question takes the focus on its safe answer, is named
// as a group for a screen reader, answers Escape, and gives the focus back to
// the × on No; once the row is gone the focus goes to its neighbor's control.
//
// renderProjects, the rows and the removal flow are run from app.js's source
// against a fake DOM that tracks the focused element and drops it, as a page
// does, when the focused element is taken out of the document.

const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { neighborAfterRemoval, initials } = require('../src/renderer/sidebarmodel')
const { fn: appFn, FakeElement } = require('./support/tooltips')

function harness ({ forget, deleteCategory, layout } = {}) {
  const doc = { activeElement: null, body: null }

  class Element extends FakeElement {
    constructor (tag) {
      super(tag)
      this.style = {}
    }

    focus () { doc.activeElement = this }
    contains (node) {
      for (let at = node; at; at = at.parentNode) if (at === this) return true
      return false
    }

    set innerHTML (value) {
      assert.equal(value, '')
      if (doc.activeElement && this.contains(doc.activeElement) && doc.activeElement !== this) {
        doc.activeElement = doc.body
      }
      for (const child of this.children) child.parentNode = null
      this.children = []
    }

    querySelector (selector) { return this.querySelectorAll(selector)[0] ?? null }
    querySelectorAll (selector) {
      const all = []
      const visit = (node) => {
        for (const child of node.children) {
          if (!(child instanceof FakeElement)) continue
          const match = selector === '[data-focus]'
            ? child.dataset.focus !== undefined
            : selector.startsWith('.') && child.classList.contains(selector.slice(1))
          if (match) all.push(child)
          visit(child)
        }
      }
      visit(this)
      return all
    }
  }

  doc.body = new Element('body')
  const elements = new Map()
  const el = (id) => {
    if (!elements.has(id)) {
      const node = new Element(id === 'project-list' ? 'ul' : 'div')
      node.id = id
      elements.set(id, node)
    }
    return elements.get(id)
  }

  const projects = [
    { id: 'a', key: 'A', name: 'alpha', path: '/src/alpha', status: 'running' },
    { id: 'b', key: 'B', name: 'beta', path: '/src/beta', status: 'running' },
    { id: 'c', key: 'C', name: 'gamma', path: '/src/gamma', status: 'running' }
  ]
  const calls = { forgets: [], deletes: [], errors: [] }
  const context = vm.createContext({
    document: {
      createElement: (tag) => new Element(tag),
      get activeElement () { return doc.activeElement },
      documentElement: { classList: { contains: () => false } }
    },
    state: {
      projects: projects.map((project) => ({ ...project })),
      layout: layout ?? {
        items: [
          { kind: 'project', id: 'a' },
          { kind: 'category', id: 'k', name: 'Clients', collapsed: false, projects: ['b'] },
          { kind: 'project', id: 'c' }
        ]
      },
      view: 'import',
      activeProjectId: null,
      editingCategoryId: null,
      removingProjectId: null,
      removingCategoryId: null,
      removals: new Set(),
      missingProjectId: null,
      sidebarFocus: null,
      identity: null
    },
    redraw: { held: false, owed: false },
    drag: { current: null },
    sidebarModel: { neighborAfterRemoval, initials },
    api: {
      forgetProject: (id) => {
        calls.forgets.push(id)
        return forget ? forget(id) : Promise.resolve(null)
      },
      deleteCategory: (id) => {
        calls.deletes.push(id)
        return deleteCategory ? deleteCategory(id) : Promise.resolve(null)
      },
      setCategoryCollapsed: () => Promise.resolve(),
      showChrome () {}
    },
    el,
    justDragged: () => false,
    openProject () {},
    setView () {},
    hideIdentityForm () {},
    startRename () {},
    nameEditor () { throw new Error('rename is not under test') },
    remoteMessage: (error) => String(error.message),
    queueMicrotask,
    console: { error: (...args) => calls.errors.push(args) }
  })
  // What the main process's broadcast does: the sidebar redrawn without it.
  context.adoptProjects = ({ projects: list, layout: next }) => {
    context.state.projects = list
    context.state.layout = next
    context.renderProjects()
  }
  const names = [
    'renderProjects', 'projectRow', 'categoryGroup', 'removeConfirmation', 'removeProjectConfirmation',
    'confirmRemoval', 'askRemoval', 'cancelRemoval', 'removeProject', 'removeCategory',
    'sidebarFocusAfterCategory', 'showSidebarError', 'clearSidebarError', 'buttonText', 'hideMissing'
  ]
  vm.runInContext(`${names.map(appFn).join('\n')}\n${names.map((name) => `globalThis.${name} = ${name}`).join('\n')}`, context)
  context.renderProjects()
  const list = el('project-list')
  const control = (key) => list.querySelectorAll('[data-focus]').find((node) => node.dataset.focus === key) ?? null
  return { context, state: context.state, doc, el, list, control, calls }
}

const click = (node) => {
  for (const listener of node.listeners.get('click') ?? []) listener({ target: node })
}

function key (node, name) {
  let prevented = false
  const event = { key: name, target: node, preventDefault () { prevented = true }, stopPropagation () {} }
  for (let at = node; at; at = at.parentNode) {
    for (const listener of at.listeners.get('keydown') ?? []) listener(event)
  }
  return prevented
}

const groupAround = (node) => {
  for (let at = node; at; at = at.parentNode) if (at.getAttribute?.('role') === 'group') return at
  return null
}

const tick = () => new Promise((resolve) => setImmediate(resolve))

for (const [kind, id, name] of [['project', 'a', 'alpha'], ['category', 'k', 'Clients']]) {
  describe(`removing a ${kind}, from the keyboard`, () => {
    test('asking moves the focus to No, inside a group named for the question', () => {
      const { control, doc } = harness()
      control(`remove:${kind}:${id}`).focus()
      click(control(`remove:${kind}:${id}`))
      const no = control(`no:${kind}:${id}`)
      assert.ok(no, 'the question has no No')
      assert.equal(doc.activeElement, no, 'the focus fell out of the row')
      const group = groupAround(no)
      assert.ok(group, 'the question is not a group a screen reader announces')
      assert.match(group.getAttribute('aria-label'), new RegExp(`Remove ${name}`))
      assert.ok(groupAround(control(`yes:${kind}:${id}`)) === group, 'Yes and No are not in the same group')
    })

    test('No puts the question away and gives the focus back to the ×', () => {
      const { control, doc, state } = harness()
      click(control(`remove:${kind}:${id}`))
      click(control(`no:${kind}:${id}`))
      assert.equal(state[`removing${kind === 'project' ? 'Project' : 'Category'}Id`], null)
      assert.equal(control(`no:${kind}:${id}`), null)
      assert.equal(doc.activeElement, control(`remove:${kind}:${id}`))
    })

    test('Escape answers No', () => {
      const { control, doc, calls } = harness()
      click(control(`remove:${kind}:${id}`))
      assert.equal(key(control(`no:${kind}:${id}`), 'Escape'), true, 'Escape was not handled')
      assert.equal(control(`no:${kind}:${id}`), null, 'Escape left the question up')
      assert.equal(doc.activeElement, control(`remove:${kind}:${id}`))
      assert.deepEqual(calls.forgets, [])
      assert.deepEqual(calls.deletes, [])
    })

    test('Escape from Yes answers No too', () => {
      const { control, doc } = harness()
      click(control(`remove:${kind}:${id}`))
      key(control(`yes:${kind}:${id}`), 'Escape')
      assert.equal(doc.activeElement, control(`remove:${kind}:${id}`))
    })

    test('a redraw while the question is up keeps the focus where it was', () => {
      const { context, control, doc } = harness()
      click(control(`remove:${kind}:${id}`))
      control(`yes:${kind}:${id}`).focus()
      context.renderProjects()
      assert.equal(doc.activeElement, control(`yes:${kind}:${id}`))
    })
  })
}

describe('after a removal, the focus lands on what is left', () => {
  test('a removed project hands the focus to the next row\'s ×', async () => {
    const { control, doc, state } = harness({
      forget: async (id) => {
        state.projects = state.projects.filter((project) => project.id !== id)
        state.layout = { items: state.layout.items.filter((item) => item.id !== id) }
        return { removed: true, projects: state.projects, layout: state.layout }
      }
    })
    click(control('remove:project:a'))
    click(control('yes:project:a'))
    await tick()
    assert.equal(control('remove:project:a'), null)
    assert.equal(doc.activeElement, control('remove:project:b'))
    assert.notEqual(doc.activeElement, doc.body)
  })

  test('the last project in the list hands it to the one above', async () => {
    const { control, doc, state } = harness({
      forget: async (id) => {
        state.projects = state.projects.filter((project) => project.id !== id)
        state.layout = { items: state.layout.items.filter((item) => item.id !== id) }
        return { removed: true, projects: state.projects, layout: state.layout }
      }
    })
    click(control('remove:project:c'))
    click(control('yes:project:c'))
    await tick()
    assert.equal(doc.activeElement, control('remove:project:b'))
  })

  test('the only project hands it to New category, never the page', async () => {
    const { control, doc, el } = harness({
      layout: { items: [{ kind: 'project', id: 'a' }] },
      forget: async () => ({ removed: true, projects: [], layout: { items: [] } })
    })
    click(control('remove:project:a'))
    click(control('yes:project:a'))
    await tick()
    assert.equal(doc.activeElement, el('new-category'))
  })

  test('a removed category hands it to the first project it held', async () => {
    const { control, doc, state } = harness({
      deleteCategory: async () => {
        state.layout = {
          items: [{ kind: 'project', id: 'a' }, { kind: 'project', id: 'b' }, { kind: 'project', id: 'c' }]
        }
        return { layout: state.layout, projects: state.projects }
      }
    })
    click(control('remove:category:k'))
    click(control('yes:category:k'))
    await tick()
    assert.equal(control('toggle:category:k'), null)
    assert.equal(doc.activeElement, control('remove:project:b'))
  })

  test('an empty category hands it to the item after it', async () => {
    const { control, doc, state } = harness({
      layout: {
        items: [
          { kind: 'project', id: 'a' },
          { kind: 'category', id: 'k', name: 'Clients', collapsed: false, projects: [] },
          { kind: 'project', id: 'c' }
        ]
      },
      deleteCategory: async () => {
        state.layout = { items: [{ kind: 'project', id: 'a' }, { kind: 'project', id: 'c' }] }
        return { layout: state.layout, projects: state.projects }
      }
    })
    click(control('remove:category:k'))
    click(control('yes:category:k'))
    await tick()
    assert.equal(doc.activeElement, control('remove:project:c'))
  })
})

describe('a removal that fails says so in the sidebar', () => {
  test('a project: the message names it, and the focus goes back to its ×', async () => {
    const { control, doc, el, calls } = harness({ forget: async () => { throw new Error('disk full') } })
    click(control('remove:project:a'))
    click(control('yes:project:a'))
    await tick()
    const notice = el('sidebar-error')
    assert.equal(notice.hidden, false, 'the failure was not shown')
    assert.equal(notice.textContent, 'Could not remove alpha: disk full')
    assert.equal(calls.errors.length, 1)
    assert.ok(control('remove:project:a'), 'the row is still there')
    assert.equal(doc.activeElement, control('remove:project:a'))
  })

  test('a category: the same treatment', async () => {
    const { control, doc, el } = harness({ deleteCategory: async () => { throw new Error('disk full') } })
    click(control('remove:category:k'))
    click(control('yes:category:k'))
    await tick()
    assert.equal(el('sidebar-error').hidden, false)
    assert.equal(el('sidebar-error').textContent, 'Could not remove Clients: disk full')
    assert.equal(doc.activeElement, control('remove:category:k'))
  })

  test('asking again puts the old message away', async () => {
    const { control, el } = harness({ forget: async () => { throw new Error('disk full') } })
    click(control('remove:project:a'))
    click(control('yes:project:a'))
    await tick()
    click(control('remove:project:a'))
    assert.equal(el('sidebar-error').hidden, true)
  })
})
