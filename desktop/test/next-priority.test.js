'use strict'

// A Next view row's priority chip carries a modifier built from the task's
// priority, so a stylesheet rule can color any priority a project defines.
// Priorities are each project's own vocabulary, so a value that is not a
// single class token draws a plain chip rather than a stray class or, through
// classList, a DOMException.

const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { fn, fakeDocument } = require('./support/tooltips')

function chip (priority) {
  const context = vm.createContext({
    document: fakeDocument,
    openNextTask: () => {},
    Date,
    Number,
    Math
  })
  const names = ['renderNextRow', 'relativeDate', 'buttonText']
  vm.runInContext(names.map(fn).join('\n'), context)
  const task = {
    id: 'WB-1',
    title: 'Task',
    priority,
    labels: [],
    assignees: [],
    updatedAt: '2026-09-26T10:00:00Z'
  }
  const row = context.renderNextRow({ id: 'a' }, task)
  const [found] = row.find('next-row__priority')
  assert.ok(found, 'the row drew no priority chip')
  return found
}

const classes = (element) => element.className.split(/\s+/).filter(Boolean)

describe('Next view priority chip', () => {
  for (const priority of ['critical', 'high', 'medium', 'low', 'p0', 'nice-to-have']) {
    test(`${priority} adds next-row__priority--${priority}`, () => {
      const element = chip(priority)
      assert.deepEqual(classes(element), ['label', 'next-row__priority', `next-row__priority--${priority}`])
      assert.equal(element.textContent, priority)
    })
  }

  for (const priority of ['', 'very high', 'high\tlow', 'a.b', '<b>']) {
    test(`${JSON.stringify(priority)} draws a plain chip`, () => {
      const element = chip(priority)
      assert.deepEqual(classes(element), ['label', 'next-row__priority'])
      assert.equal(element.textContent, priority)
    })
  }
})
