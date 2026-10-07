'use strict'

// layout.js is where the board views go and how tall the title strip above
// them is. Both are pure, so they are tested here rather than through a
// window: main.js lays the boards out with them, and the shell's preload hands
// the same height to the renderer, so a wrong number here is a board that
// covers the strip or a gap between them.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { titleStripHeight, boardBounds } = require('../src/main/layout')

describe('titleStripHeight', () => {
  test('macOS matches the inset the sidebar head keeps under the traffic lights', () => {
    assert.equal(titleStripHeight('darwin'), 34)
  })

  test('Windows matches the window-controls overlay', () => {
    assert.equal(titleStripHeight('win32'), 36)
  })

  test('every other platform keeps its native title bar and has no strip', () => {
    for (const platform of ['linux', 'freebsd', 'openbsd', 'sunos', 'aix']) {
      assert.equal(titleStripHeight(platform), 0, platform)
    }
  })
})

describe('boardBounds', () => {
  const content = { width: 1280, height: 820 }

  test('beside the expanded sidebar and under the strip', () => {
    assert.deepEqual(boardBounds(content, 260, 34), { x: 260, y: 34, width: 1020, height: 786 })
  })

  test('beside the collapsed rail and under the strip', () => {
    assert.deepEqual(boardBounds(content, 76, 36), { x: 76, y: 36, width: 1204, height: 784 })
  })

  test('with no strip the board starts at the top of the window', () => {
    assert.deepEqual(boardBounds(content, 260, 0), { x: 260, y: 0, width: 1020, height: 820 })
  })

  test('a window smaller than the sidebar and strip leaves an empty board, never a negative one', () => {
    assert.deepEqual(boardBounds({ width: 200, height: 20 }, 260, 34), { x: 260, y: 34, width: 0, height: 0 })
  })
})
