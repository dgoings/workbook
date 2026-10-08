'use strict'

// Where a drop in the sidebar lands, and the rail's category label. These are
// the renderer's only decisions about a drag: what it asks the main process to
// do. The main process does the move (sidebarlayout.js) and the sidebar redraws
// from its answer, so these are checked against the request alone.

const test = require('node:test')
const assert = require('node:assert/strict')

const { dropTarget, initials } = require('../src/renderer/sidebarmodel')
const { moveProject, moveCategory, flatten } = require('../src/main/sidebarlayout')

const top = (id) => ({ kind: 'project', id })

// a, [Work: b, c], d, [Home (collapsed): e], [Empty]
const layout = {
  items: [
    top('a'),
    { kind: 'category', id: 'work', name: 'Work', collapsed: false, projects: ['b', 'c'] },
    top('d'),
    { kind: 'category', id: 'home', name: 'Home', collapsed: true, projects: ['e'] },
    { kind: 'category', id: 'empty', name: 'Empty', collapsed: false, projects: [] }
  ]
}

const project = (id) => ({ kind: 'project', id })
const category = (id) => ({ kind: 'category', id })

test('a project over the top half of a top-level row goes above it, the bottom half below it', () => {
  assert.deepEqual(dropTarget(layout, project('a'), project('d'), 0.2), {
    action: 'moveProject',
    target: { kind: 'top', index: 2 },
    indicator: { type: 'line', key: 'project:d', edge: 'before' }
  })
  assert.deepEqual(dropTarget(layout, project('a'), project('d'), 0.8), {
    action: 'moveProject',
    target: { kind: 'top', index: 3 },
    indicator: { type: 'line', key: 'project:d', edge: 'after' }
  })
})

test('a project over a row inside a category goes between that category\'s children', () => {
  assert.deepEqual(dropTarget(layout, project('a'), project('c'), 0.1), {
    action: 'moveProject',
    target: { kind: 'category', id: 'work', index: 1 },
    indicator: { type: 'line', key: 'project:c', edge: 'before' }
  })
  assert.deepEqual(dropTarget(layout, project('d'), project('c'), 0.9).target, { kind: 'category', id: 'work', index: 2 })
})

test('a project over a category header goes into it, at its end', () => {
  assert.deepEqual(dropTarget(layout, project('a'), category('work'), 0.6), {
    action: 'moveProject',
    target: { kind: 'category', id: 'work', index: 2 },
    indicator: { type: 'into', key: 'category:work' }
  })
  assert.deepEqual(dropTarget(layout, project('a'), category('empty'), 0.9).target, { kind: 'category', id: 'empty', index: 0 })
})

test('a project over a collapsed category drops into it', () => {
  const drop = dropTarget(layout, project('a'), category('home'), 0.5)
  assert.deepEqual(drop.target, { kind: 'category', id: 'home', index: 1 })
  assert.equal(drop.indicator.type, 'into')
  // And the move it asks for lands there, with the folded category's
  // project still counted in order.
  assert.deepEqual(flatten(moveProject(layout, 'a', drop.target)), ['b', 'c', 'd', 'e', 'a'])
})

test('the top quarter of a category header drops a project above the category, at the top level', () => {
  assert.deepEqual(dropTarget(layout, project('d'), category('work'), 0.1), {
    action: 'moveProject',
    target: { kind: 'top', index: 1 },
    indicator: { type: 'line', key: 'category:work', edge: 'before' }
  })
  assert.equal(dropTarget(layout, project('d'), category('work'), 0.25).indicator.type, 'into')
})

test('a category over a header or a top-level row moves among the top-level items', () => {
  assert.deepEqual(dropTarget(layout, category('home'), category('work'), 0.3), {
    action: 'moveCategory',
    index: 1,
    indicator: { type: 'line', key: 'category:work', edge: 'before' }
  })
  assert.equal(dropTarget(layout, category('home'), category('work'), 0.7).index, 2)
  assert.deepEqual(dropTarget(layout, category('work'), project('a'), 0.2), {
    action: 'moveCategory',
    index: 0,
    indicator: { type: 'line', key: 'project:a', edge: 'before' }
  })
})

test('a category over another category\'s children is refused, and over its own', () => {
  assert.equal(dropTarget(layout, category('home'), project('b'), 0.5), null)
  assert.equal(dropTarget(layout, category('work'), project('c'), 0.5), null)
})

test('below the last row is the end of the top level', () => {
  assert.deepEqual(dropTarget(layout, project('b'), { kind: 'end' }, 1), {
    action: 'moveProject',
    target: { kind: 'top', index: 5 },
    indicator: { type: 'line', key: 'category:empty', edge: 'after' }
  })
  assert.equal(dropTarget(layout, category('work'), { kind: 'end' }, 1).index, 5)
  assert.deepEqual(dropTarget({ items: [] }, project('x'), { kind: 'end' }, 1).indicator,
    { type: 'line', key: null, edge: 'after' })
})

test('a row the layout does not hold is refused', () => {
  assert.equal(dropTarget(layout, project('a'), project('zz'), 0.5), null)
  assert.equal(dropTarget(layout, project('a'), category('zz'), 0.5), null)
})

test('dropping a row on either half of itself asks for a move that changes nothing', () => {
  for (const fraction of [0.1, 0.9]) {
    const drop = dropTarget(layout, project('d'), project('d'), fraction)
    assert.deepEqual(moveProject(layout, 'd', drop.target), layout)
    const inside = dropTarget(layout, project('b'), project('b'), fraction)
    assert.deepEqual(moveProject(layout, 'b', inside.target), layout)
    const self = dropTarget(layout, category('home'), category('home'), fraction)
    assert.deepEqual(moveCategory(layout, 'home', self.index), layout)
  }
})

test('initials take the first letter of the first two words', () => {
  assert.equal(initials('Client work'), 'CW')
  assert.equal(initials('personal'), 'P')
  assert.equal(initials('  open   source  projects '), 'OS')
  assert.equal(initials('side-projects'), 'SP')
  assert.equal(initials('élan vital'), 'ÉV')
  assert.equal(initials('😀 fun'), '😀F')
  assert.equal(initials('   '), '·')
})
