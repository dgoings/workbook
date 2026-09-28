'use strict'

// boardcommand.js is the part of the board preload a shortcut reaches: given a
// command, find the control on the board page that already does that thing
// and drive it, so the page's own router and handlers run. The document here
// is a hand-made stand-in with exactly the queries the module makes.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { runBoardCommand } = require('../src/preload/boardcommand')

function fakeDocument (nodes) {
  return { querySelector: (selector) => nodes[selector] || null }
}
function clickable () { const node = { clicks: 0, click () { node.clicks += 1 } }; return node }
function focusable (hiddenRow) { const node = { focused: 0, focus () { node.focused += 1 }, closest: () => ({ hidden: hiddenRow }) }; return node }

describe('runBoardCommand', () => {
  test('new-task clicks the first New Task link', () => {
    const link = clickable()
    assert.equal(runBoardCommand('new-task', { document: fakeDocument({ 'a.new-task-link': link }), history: {} }), true)
    assert.equal(link.clicks, 1)
  })
  test('new-task does nothing off the board', () => {
    assert.equal(runBoardCommand('new-task', { document: fakeDocument({}), history: {} }), false)
  })
  test('search focuses the filter box only while its row is shown', () => {
    const box = focusable(false)
    assert.equal(runBoardCommand('search', { document: fakeDocument({ '[data-filter-q]': box }), history: {} }), true)
    assert.equal(box.focused, 1)
    const hidden = focusable(true)
    assert.equal(runBoardCommand('search', { document: fakeDocument({ '[data-filter-q]': hidden }), history: {} }), false)
    assert.equal(hidden.focused, 0)
  })
  test('config clicks the header link when the board offers one', () => {
    const link = clickable()
    assert.equal(runBoardCommand('config', { document: fakeDocument({ 'a.header-link[href="/config"]': link }), history: {} }), true)
    assert.equal(link.clicks, 1)
    assert.equal(runBoardCommand('config', { document: fakeDocument({}), history: {} }), false)
  })
  test('back and forward use history', () => {
    const calls = []
    const history = { back: () => calls.push('back'), forward: () => calls.push('forward') }
    assert.equal(runBoardCommand('back', { document: fakeDocument({}), history }), true)
    assert.equal(runBoardCommand('forward', { document: fakeDocument({}), history }), true)
    assert.deepEqual(calls, ['back', 'forward'])
  })
  test('an unknown command is refused quietly', () => {
    assert.equal(runBoardCommand('explode', { document: fakeDocument({}), history: {} }), false)
  })
})
