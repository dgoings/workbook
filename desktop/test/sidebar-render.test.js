'use strict'

// The sidebar draws what the main process last announced, in its order, and a
// drag only asks. app.js runs only in a page, so these read its source,
// comments removed, the way layout-wiring.test.js does. The drop arithmetic
// itself is tested in sidebarmodel.test.js.

const fs = require('node:fs')
const path = require('node:path')
const { test } = require('node:test')
const assert = require('node:assert/strict')

const src = path.join(__dirname, '..', 'src')

function code (...parts) {
  return fs.readFileSync(path.join(src, ...parts), 'utf8')
    .replace(/\r\n/g, '\n')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

const app = code('renderer', 'app.js')

function fn (name) {
  const match = app.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in app.js`)
  return match[0]
}

test('renderProjects draws from the layout, not from the bare project list', () => {
  const body = fn('renderProjects')
  assert.match(body, /for \(const item of state\.layout\.items\)/)
  assert.match(body, /categoryGroup\(item,/)
  assert.doesNotMatch(body, /for \(const \w+ of state\.projects\)/)
})

test('a project row does not open its project on the click that ends a drag', () => {
  const body = fn('projectRow')
  assert.match(body, /addEventListener\('click', \(\) => \{\s*if \(justDragged\(\)\) return\s*openProject\(/)
  const guard = fn('justDragged')
  assert.match(guard, /drag\.current !== null/)
  assert.match(guard, /performance\.now\(\) - drag\.endedAt < \d+/)
  assert.match(fn('wireDragAndDrop'), /'dragend'[\s\S]*drag\.endedAt = performance\.now\(\)/)
})

test('the project list and layout change only from what the main process sends', () => {
  // One assignment of each, in adoptProjects, which both the boot/import load
  // and the layout broadcast go through.
  assert.equal(app.match(/state\.projects = /g).length, 1)
  assert.equal(app.match(/state\.layout = /g).length, 1)
  const adopt = fn('adoptProjects')
  assert.match(adopt, /state\.projects = projects/)
  assert.match(adopt, /state\.layout = layout/)
  assert.match(fn('loadProjects'), /adoptProjects\(\{ projects, layout \}\)/)
  assert.match(app, /api\.onSidebarLayoutChanged\(\(payload\) => \{\s*adoptProjects\(payload\)/)
})

test('an open Next view is read again when the order changes', () => {
  assert.match(app, /api\.onSidebarLayoutChanged\(\(payload\) => \{\s*adoptProjects\(payload\)\s*if \(state\.view === 'next'\) loadNext\(true\)\s*\}\)/)
})

test('a drop asks the main process and redraws nothing itself', () => {
  const wiring = fn('wireDragAndDrop')
  const drop = wiring.slice(wiring.indexOf("'drop'"), wiring.indexOf("'dragend'"))
  assert.match(drop, /api\.moveCategory\(drag\.current\.id, drop\.index\)/)
  assert.match(drop, /api\.moveProject\(drag\.current\.id, drop\.target\)/)
  assert.doesNotMatch(drop, /renderProjects|state\.layout\b|appendChild|\.append\(|insertBefore/)
})

test('there is no drag in the rail', () => {
  assert.match(fn('wireDragAndDrop'),
    /'dragstart'[\s\S]*?classList\.contains\('sidebar-collapsed'\)\)\s*\{\s*event\.preventDefault\(\)/)
})

test('the page loads the drop arithmetic before app.js', () => {
  const html = fs.readFileSync(path.join(src, 'renderer', 'index.html'), 'utf8')
  const model = html.indexOf('<script src="sidebarmodel.js"></script>')
  assert.notEqual(model, -1)
  assert.ok(model < html.indexOf('<script src="app.js"></script>'))
})
