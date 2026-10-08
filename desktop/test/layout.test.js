'use strict'

// layout.js is where the board views go and how tall the window's title row
// is. Both are pure, so they are tested here rather than through a window:
// main.js lays the boards out and places the window controls with them, and the
// shell's preload hands the same row height to the renderer, so a wrong number
// here is a board below a gap or a wordmark off the traffic lights' line.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { TITLE_ROW_HEIGHT, TRAFFIC_LIGHT_SIZE, TRAFFIC_LIGHT_POSITION, TRAFFIC_LIGHTS_END, boardBounds } = require('../src/main/layout')

describe('the title row', () => {
  test('is 36 pixels', () => {
    assert.equal(TITLE_ROW_HEIGHT, 36)
  })

  test('the traffic lights are pinned centered on it, by their 14px button frames', () => {
    assert.equal(TRAFFIC_LIGHT_SIZE, 14)
    assert.deepEqual({ ...TRAFFIC_LIGHT_POSITION }, { x: 14, y: 11 })
    assert.equal(TRAFFIC_LIGHT_POSITION.y + TRAFFIC_LIGHT_SIZE / 2, TITLE_ROW_HEIGHT / 2,
      'the lights\' center must be the row\'s center')
  })

  test('the lights end at 74: three 14px buttons on 23px spacing from x 14', () => {
    assert.equal(TRAFFIC_LIGHTS_END, 74)
  })

  test('the position cannot be changed by a caller that was handed it', () => {
    assert.ok(Object.isFrozen(TRAFFIC_LIGHT_POSITION))
  })
})

describe('boardBounds', () => {
  const content = { width: 1280, height: 820 }

  test('beside the expanded sidebar, from the window\'s top edge', () => {
    assert.deepEqual(boardBounds(content, 260), { x: 260, y: 0, width: 1020, height: 820 })
  })

  test('beside the collapsed rail, from the window\'s top edge', () => {
    assert.deepEqual(boardBounds(content, 76), { x: 76, y: 0, width: 1204, height: 820 })
  })

  test('a window narrower than the sidebar leaves an empty board, never a negative one', () => {
    assert.deepEqual(boardBounds({ width: 200, height: 20 }, 260), { x: 260, y: 0, width: 0, height: 20 })
    assert.deepEqual(boardBounds({ width: 200, height: -4 }, 260), { x: 260, y: 0, width: 0, height: 0 })
  })

  test('takes no strip or inset: a third argument changes nothing', () => {
    assert.equal(boardBounds.length, 2)
    assert.deepEqual(boardBounds(content, 260, 34), boardBounds(content, 260))
  })
})
