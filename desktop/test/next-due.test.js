'use strict'

// A Next view row draws a task's due date as a chip whose words say what it
// means today — Overdue, Due soon, or plain Due — so the state never rests on
// color alone, and draws nothing for a task with no due date.

const vm = require('node:vm')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { fn, fakeDocument } = require('./support/tooltips')

function row (extra) {
  const context = vm.createContext({ document: fakeDocument, openNextTask: () => {}, Date, Number, Math })
  vm.runInContext(['renderNextRow', 'relativeDate', 'buttonText'].map(fn).join('\n'), context)
  return context.renderNextRow({ id: 'a' }, {
    id: 'WB-1', title: 'Task', priority: 'medium', labels: [], assignees: [], updatedAt: '2026-09-26T10:00:00Z', ...extra
  })
}

describe('Next row due date', () => {
  test('an overdue task says so in words and carries the overdue modifier', () => {
    const [chip] = row({ due: '2026-11-01', dueState: 'overdue' }).find('next-row__due')
    assert.ok(chip, 'the row drew no due chip')
    assert.equal(chip.textContent, 'Overdue 2026-11-01')
    assert.match(chip.className, /\bnext-row__due--overdue\b/)
  })

  test('a task due soon says so in words', () => {
    const [chip] = row({ due: '2026-11-24', dueState: 'due-soon' }).find('next-row__due')
    assert.equal(chip.textContent, 'Due soon 2026-11-24')
    assert.match(chip.className, /\bnext-row__due--soon\b/)
  })

  test('a date with nothing to flag is drawn plainly', () => {
    const [chip] = row({ due: '2027-01-01', dueState: '' }).find('next-row__due')
    assert.equal(chip.textContent, 'Due 2027-01-01')
    assert.equal(chip.className, 'label next-row__due')
  })

  test('no due date draws no chip', () => {
    assert.deepEqual(row({ due: '', dueState: '' }).find('next-row__due'), [])
  })
})
