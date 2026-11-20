'use strict'

// Whether a scanned repository is already imported is asked of the projects
// Workbench has now, not read from the scan. The scan's answer is the one from
// when it ran: a project removed since was still drawn "imported", its box
// disabled, until a Rescan. renderScan and adoptProjects are run from app.js's
// source against the fake DOM in support/tooltips.js.

const path = require('node:path')
const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { fn: appFn, FakeElement } = require('./support/tooltips')

const alpha = path.join(path.sep, 'src', 'alpha')
const beta = path.join(path.sep, 'src', 'beta')

function importView ({ projects, scanned, view = 'import', filter = 'all' }) {
  class Element extends FakeElement {
    constructor (tag) {
      super(tag)
      this.style = {}
    }

    get childElementCount () { return this.children.length }
  }
  const elements = new Map()
  const el = (id) => {
    if (!elements.has(id)) elements.set(id, new Element('div'))
    return elements.get(id)
  }
  const context = vm.createContext({
    document: { createElement: (tag) => new Element(tag) },
    state: {
      projects,
      layout: null,
      view,
      filter,
      query: '',
      selected: new Set(),
      keys: new Map(),
      scan: { root: path.join(path.sep, 'src'), repositories: scanned }
    },
    el,
    keyNoteDismissed: () => true,
    renderProjects () {}
  })
  const names = ['adoptProjects', 'isImported', 'visibleRepositories', 'matchesQuery', 'chip', 'renderScan', 'updateSelectionStatus']
  vm.runInContext(`${names.map(appFn).join('\n')}\n${names.map((name) => `globalThis.${name} = ${name}`).join('\n')}`, context)
  const rows = () => el('scan-results').children.map((row) => ({
    name: row.find('repo-name')[0].textContent,
    disabled: row.children[0].disabled,
    note: row.children.at(-1).textContent
  }))
  return { context, rows }
}

const repository = (dir, imported) => ({ path: dir, name: path.basename(dir), relativePath: path.basename(dir), imported, suggestedKey: 'AB' })

describe('the import list asks the current projects', () => {
  test('a repository the scan called imported, since removed, can be picked again', () => {
    const { context, rows } = importView({
      projects: [{ id: 'a', path: alpha }],
      scanned: [repository(alpha, true), repository(beta, true)]
    })
    context.renderScan()
    assert.deepEqual(rows(), [
      { name: 'alpha', disabled: true, note: 'imported' },
      { name: 'beta', disabled: false, note: 'new' }
    ])
  })

  test('a removal redraws an open import list, without a Rescan', () => {
    const { context, rows } = importView({
      projects: [{ id: 'a', path: alpha }, { id: 'b', path: beta }],
      scanned: [repository(alpha, true), repository(beta, true)]
    })
    context.renderScan()
    assert.equal(rows()[1].disabled, true)
    context.adoptProjects({ projects: [{ id: 'a', path: alpha }], layout: { items: [] } })
    assert.deepEqual(rows()[1], { name: 'beta', disabled: false, note: 'new' })
  })

  test('the Imported and New filters follow the current projects too', () => {
    const { context, rows } = importView({
      projects: [{ id: 'a', path: alpha }],
      scanned: [repository(alpha, false), repository(beta, true)],
      filter: 'imported'
    })
    context.renderScan()
    assert.deepEqual(rows().map((row) => row.name), ['alpha'])
    context.state.filter = 'new'
    context.renderScan()
    assert.deepEqual(rows().map((row) => row.name), ['beta'])
  })
})
