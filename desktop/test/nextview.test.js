'use strict'

// nextview.js is the desktop's read of `workbook next --limit` across every
// imported project. The runner is injected so these cases never spawn a
// process: what is under test is the shaping, the ordering and the isolation
// of one project's failure, not the binary.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const nextview = require('../src/main/nextview')

const projects = [
  { id: 'p1', key: 'ONE', name: 'One', path: '/one' },
  { id: 'p2', key: 'TWO', name: 'Two', path: '/two' },
  { id: 'p3', key: 'THR', name: 'Three', path: '/three' }
]

function task (id, extra = {}) {
  return {
    id, title: `Task ${id}`, priority: 'medium', status: 'ready', labels: ['a'],
    assignments: [{ principal: 'me@example.com', creator: 'me@example.com', createdAt: '2026-09-26T09:00:00Z' }], updatedAt: '2026-09-26T10:00:00Z',
    description: 'not carried', rank: '1/1', ...extra
  }
}

describe('loadNext', () => {
  test('asks every project with the limit and keeps registry order', async () => {
    const calls = []
    const run = async (repoPath, limit) => {
      calls.push([repoPath, limit])
      return { tasks: [task(`${repoPath}-1`)], eligible: 4 }
    }
    const result = await nextview.loadNext({ projects, limit: 2, run })
    assert.deepEqual(calls, [['/one', 2], ['/two', 2], ['/three', 2]])
    assert.deepEqual(result.projects.map((entry) => entry.id), ['p1', 'p2', 'p3'])
    assert.equal(result.limit, 2)
    assert.deepEqual(result.projects[0], {
      id: 'p1', key: 'ONE', name: 'One', eligible: 4, error: null,
      tasks: [{
        id: '/one-1', title: 'Task /one-1', priority: 'medium', status: 'ready',
        labels: ['a'], assignees: ['me@example.com'], updatedAt: '2026-09-26T10:00:00Z'
      }]
    })
  })

  test('one project failing leaves the others answered', async () => {
    const run = async (repoPath) => {
      if (repoPath === '/two') throw new Error('workbook next exited 3')
      return { tasks: [], eligible: 0 }
    }
    const result = await nextview.loadNext({ projects, limit: 1, run })
    assert.deepEqual(result.projects.map((entry) => entry.error), [null, 'workbook next exited 3', null])
    assert.deepEqual(result.projects[1].tasks, [])
    assert.equal(result.projects[1].eligible, 0)
  })

  test('tolerates a task document missing optional members', async () => {
    const run = async () => ({ tasks: [{ id: 'X-1', title: 'Bare' }], eligible: 1 })
    const result = await nextview.loadNext({ projects: projects.slice(0, 1), limit: 1, run })
    assert.deepEqual(result.projects[0].tasks[0], {
      id: 'X-1', title: 'Bare', priority: '', status: '', labels: [], assignees: [], updatedAt: ''
    })
  })

  test('clamps the limit before asking', async () => {
    const seen = []
    const run = async (_repoPath, limit) => { seen.push(limit); return { tasks: [], eligible: 0 } }
    await nextview.loadNext({ projects: projects.slice(0, 1), limit: 99, run })
    await nextview.loadNext({ projects: projects.slice(0, 1), limit: 0, run })
    await nextview.loadNext({ projects: projects.slice(0, 1), limit: 'seven', run })
    assert.deepEqual(seen, [20, 1, 1])
  })
})

describe('clampLimit', () => {
  test('keeps whole numbers between 1 and 20 and defaults everything else to 1', () => {
    assert.equal(nextview.clampLimit(3), 3)
    assert.equal(nextview.clampLimit('7'), 7)
    assert.equal(nextview.clampLimit(20), 20)
    assert.equal(nextview.clampLimit(21), 20)
    assert.equal(nextview.clampLimit(0), 1)
    assert.equal(nextview.clampLimit(-4), 1)
    assert.equal(nextview.clampLimit(2.9), 2)
    assert.equal(nextview.clampLimit(undefined), 1)
    assert.equal(nextview.clampLimit('x'), 1)
  })
})
