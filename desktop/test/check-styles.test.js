'use strict'

// check-styles.js requires a rule for every class the renderer applies. A
// class built from data keeps only its static part, so a modifier such as
// `next-row__priority--${task.priority}` harvests as next-row__priority--: a
// prefix, which any rule whose class starts with it satisfies.

const fs = require('node:fs')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const { definedClasses, usedClasses, danglingSelectors, missingClasses } = require('../scripts/check-styles')

const renderer = path.join(__dirname, '..', 'src', 'renderer')
const read = (name) => fs.readFileSync(path.join(renderer, name), 'utf8')

function missing (css, js, html = '') {
  return missingClasses(usedClasses(html, js), definedClasses(css))
}

describe('check-styles', () => {
  test('the real renderer has a rule for every class it applies', () => {
    const css = read('styles.css')
    assert.deepEqual(danglingSelectors(css), [])
    assert.deepEqual(missingClasses(usedClasses(read('index.html'), read('app.js')), definedClasses(css)), [])
  })

  test('the Next view builds its priority modifier from data', () => {
    // The prefix rule is exercised by real renderer code, not only by the
    // fixtures below.
    assert.ok(usedClasses('', read('app.js')).has('next-row__priority--'))
  })

  test('a template-literal modifier harvests as a prefix ending in --', () => {
    const used = usedClasses('', 'chip.className = `label chip--${task.priority}`')
    assert.deepEqual([...used].sort(), ['chip--', 'label'])
  })

  test('a prefix is satisfied by a rule whose class starts with it', () => {
    const js = 'chip.className = `chip chip--${task.priority}`'
    assert.deepEqual(missing('.chip { } .chip--high { color: red; }', js), [])
  })

  test('a prefix with no matching rule is missing', () => {
    const js = 'chip.className = `chip chip--${task.priority}`'
    // .chip and .chip-wide start with "chip" but not with "chip--", and a rule
    // for a different block's modifier does not count either.
    assert.deepEqual(missing('.chip { } .chip-wide { } .other--high { }', js), ['chip--'])
  })

  test('a rule for the bare prefix does not satisfy it', () => {
    const js = 'chip.className = `chip chip--${task.priority}`'
    // .chip-- styles no modifier any value could produce, so the prefix is
    // still missing until a rule names a modifier after it.
    assert.deepEqual(missing('.chip { } .chip-- { }', js), ['chip--'])
  })

  test('a prefix in a comment does not satisfy it', () => {
    const js = 'chip.className = `chip chip--${task.priority}`'
    assert.deepEqual(missing('.chip { } /* .chip--high */', js), ['chip--'])
  })

  test('a whole name is still matched exactly, not as a prefix', () => {
    const js = "row.className = 'row'"
    assert.deepEqual(missing('.row-wide { }', js), ['row'])
  })
})
