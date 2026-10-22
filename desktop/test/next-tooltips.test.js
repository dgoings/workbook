'use strict'

// Native tooltips in the Next view have to survive electron/electron#49843 the
// way the sidebar's do, so every titled element in the view is the only node
// under the pointer for its own tooltip; test/support/tooltips.js has the
// guard and says why, and "One node under the pointer" in styles.css has the
// rule. This runs the guard over the real page and the groups renderNext
// draws: a project heading in each status, a footer in each of its states, and
// rows with and without labels and assignees.

const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const cascade = require('./support/cascade')
const { page, stylesheet, fn, FakeElement, fakeDocument, adopt, walk, title, crossings, stranded, rest } = require('./support/tooltips')

const task = (id, extra = {}) => ({
  id,
  title: `Task ${id}`,
  priority: 'medium',
  status: 'ready',
  labels: [],
  assignees: [],
  updatedAt: '2026-09-26T10:00:00Z',
  ...extra
})

// The payload nextview.js sends, one group for every way renderNext can draw
// one: tasks and more eligible, every task shown, nothing eligible, and a CLI
// that failed with its whole usage block.
const payload = {
  limit: 2,
  projects: [
    {
      id: 'a',
      key: 'WB',
      name: 'workbook',
      status: 'running',
      tasks: [
        task('WB-1', { priority: 'high', labels: ['desktop', 'bug'], assignees: ['me@example.com', 'you@example.com'] }),
        task('WB-2')
      ],
      eligible: 5,
      error: null
    },
    { id: 'b', key: 'SITE', name: 'site', status: 'stopped', tasks: [task('SITE-1', { labels: ['docs'] })], eligible: 1, error: null },
    { id: 'c', key: 'API', name: 'api', status: 'failed', tasks: [], eligible: 0, error: null },
    { id: 'd', key: 'OPS', name: 'ops', status: 'exited', tasks: [], eligible: 0, error: 'unknown flag: --limit\nUsage:\n  workbook next [flags]' }
  ]
}

// renderNext and the helpers it draws with, run against a fake #next-groups.
function rendered () {
  const container = new FakeElement('div')
  const context = vm.createContext({
    document: { ...fakeDocument, activeElement: null },
    el: (id) => {
      assert.equal(id, 'next-groups', `renderNext asked for #${id}`)
      return container
    },
    state: { next: payload, nextDrawn: null },
    openNextTask: () => {},
    JSON,
    Date,
    Number,
    Math,
    console
  })
  const names = ['renderNext', 'renderNextRow', 'relativeDate', 'buttonText']
  vm.runInContext(`${names.map(fn).join('\n')}\nrenderNext()`, context)
  assert.equal(container.children.length, payload.projects.length, 'renderNext drew no groups')
  return container
}

// --- the page, as the cascade reads it ---------------------------------------

// The Next view showing, with the groups renderNext drew in its list.
function nextPage ({ collapsed = false, platform = 'mac' } = {}) {
  const root = cascade.parseHTML(page)
  if (collapsed) root.classes.add('sidebar-collapsed')
  root.classes.add(`is-${platform}`)
  const view = cascade.byId(root, 'view-next')
  assert.ok(view, 'index.html has no #view-next')
  view.attributes.delete('hidden')
  const list = cascade.byId(root, 'next-groups')
  assert.ok(list, 'index.html has no #next-groups')
  cascade.append(list, ...rendered().children.map(adopt))
  return { root, view }
}

// Every way the view can stand that the cascade can tell apart: beside the
// rail and the expanded sidebar, on each platform, each media condition on and
// off, with and without a board, at rest and with the pointer over each
// titled element in turn.
function * scenarios (rules) {
  for (const collapsed of [true, false]) {
    for (const platform of ['mac', 'windows', 'linux']) {
      for (const env of cascade.combinations(cascade.conditions(rules))) {
        for (const board of [false, true]) {
          const { root, view } = nextPage({ collapsed, platform })
          if (board) root.classes.add('board-active')
          const hovers = [null]
          walk(view, (node) => { if (title(node) !== null) hovers.push(node) })
          for (const hover of hovers) {
            const state = cascade.interaction(hover ? { hover } : {})
            const label = JSON.stringify({ collapsed, platform, env, board, hover: hover && cascade.describe(hover) })
            yield { view, options: { env, state, memo: new Map() }, label }
          }
        }
      }
    }
  }
}

function first (root, name) {
  let found = null
  walk(root, (node) => { if (!found && node.classes.has(name)) found = node })
  assert.ok(found, `the view has no .${name}`)
  return found
}

describe('every tooltip in the Next view is one node under the pointer', () => {
  test('no node takes the pointer inside a titled element with the same tooltip', () => {
    let runs = 0
    for (const { view, options, label } of scenarios(stylesheet)) {
      assert.deepEqual(crossings(stylesheet, view, options), [], label)
      runs += 1
    }
    assert.ok(runs > 0)
  })

  test('every titled element and every control still takes the pointer', () => {
    for (const { view, options, label } of scenarios(stylesheet)) {
      assert.deepEqual(stranded(stylesheet, view, options), [], label)
    }
  })

  test('the view has the titled elements the guard is meant to cover', () => {
    // Without these, a view the guard read wrongly would pass with nothing in
    // it.
    const { view } = nextPage()
    const titled = []
    walk(view, (node) => { if (title(node) !== null) titled.push(cascade.describe(node)) })
    for (const expected of [
      'h2.next-group__name',
      'p.next-group__foot.next-group__foot--error',
      'button.next-row',
      'span.next-row__title',
      'span.label.next-row__assignee',
      'span.next-row__age'
    ]) {
      assert.ok(titled.includes(expected), `expected ${expected} among the titled elements, found ${titled.join(', ')}`)
    }
  })

  test('the words keep their tooltips: status, error, task id, title, assignee and age', () => {
    const { view } = nextPage()
    const heading = first(view, 'next-group__name')
    assert.equal(title(heading), 'running')
    assert.equal(title(first(view, 'next-group__foot--error')), payload.projects[3].error)
    const row = first(view, 'next-row')
    assert.equal(title(row), 'WB-1')
    assert.equal(title(first(row, 'next-row__title')), 'Task WB-1')
    assert.equal(title(first(row, 'next-row__assignee')), 'Assigned')
    assert.equal(title(first(row, 'next-row__age')), '2026-09-26T10:00:00Z')
  })
})

describe('the guard sees a regression in the Next view', () => {
  test('it fails the stylesheet without the rules that take children off the pointer', () => {
    const selectors = ['.next-group__name > *', '.next-row > *']
    const without = stylesheet.filter((rule) => !selectors.some((selector) => rule.selectors.includes(selector)))
    assert.equal(stylesheet.length - without.length, 1, `one rule should carry ${selectors.join(' and ')}`)
    const found = crossings(without, nextPage().view, rest()).join('\n')
    for (const node of ['span.dot', 'span.project-key', 'span.label.next-row__priority', 'span.next-row__meta']) {
      // A dot also carries its status as a class: span.dot.running.
      assert.match(found, new RegExp(`^${node.replace(/[.#]/g, '\\$&')}(?:\\.[\\w-]+)* takes the pointer`, 'm'))
    }
    // The project's name, in a span with no class of its own.
    assert.match(found, /^span takes the pointer inside h2\.next-group__name/m)
  })

  test('it fails a project name set straight onto the heading', () => {
    const { view } = nextPage()
    const heading = first(view, 'next-group__name')
    heading.children = heading.children.filter((child) => child.classes.size > 0)
    heading.text = ' workbook'
    assert.match(crossings(stylesheet, view, rest()).join('\n'), /^the text " workbook" directly inside h2\.next-group__name takes the pointer/m)
  })

  test('it fails an error footer that ellipsizes the span that ignores the pointer', () => {
    // The footer as it was: the footer, not its span, cut the text short.
    const before = stylesheet.concat(cascade.parseStylesheet('.next-group__foot { text-overflow: ellipsis; }')
      .map((rule) => ({ ...rule, order: stylesheet.length })))
    assert.match(crossings(before, nextPage().view, rest()).join('\n'), /^p\.next-group__foot\.next-group__foot--error takes the pointer and ellipsizes its children/m)
  })

  test('it fails a titled chip inside the row that stops taking the pointer', () => {
    const without = stylesheet.filter((rule) => !rule.selectors.includes('.next-row__assignee'))
    assert.ok(without.length < stylesheet.length, 'no rule has the selector .next-row__assignee')
    assert.match(stranded(without, nextPage().view, rest()).join('\n'), /^span\.label\.next-row__assignee is drawn but does not take the pointer/m)
  })
})

describe('a Next row still opens its task', () => {
  test('from anywhere on the row, which stays the one button', () => {
    const container = rendered()
    const rows = container.children.flatMap((group) => group.find('next-row'))
    assert.equal(rows.length, 3)
    for (const row of rows) {
      assert.equal(row.tagName, 'BUTTON')
      assert.equal(row.type, 'button')
      assert.equal(row.listeners.get('click')?.length, 1, `${row.dataset.taskId} has no click listener`)
    }
  })
})
