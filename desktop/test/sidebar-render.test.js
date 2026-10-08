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
  assert.match(fn('endDrag'), /drag\.current = null[\s\S]*if \(justEnded\) drag\.endedAt = performance\.now\(\)/)
})

test('a drag is closed by its drop, not only by a dragend its redrawn source may never send', () => {
  const wiring = fn('wireDragAndDrop')
  const drop = wiring.slice(wiring.indexOf("'drop'"), wiring.indexOf("'dragend'"))
  assert.match(drop, /endDrag\(\)/)
  assert.match(wiring, /'dragend', \(\) => \{ endDrag\(\) \}/)
  // The backstop: a press means no drag is on, and it must not eat its own click.
  assert.match(wiring, /document\.addEventListener\('mousedown', \(\) => \{ endDrag\(\{ justEnded: false \}\) \}, true\)/)
})

test('only a drag this list started is taken for a sidebar move', () => {
  assert.match(fn('wireDragAndDrop'), /setData\(DRAG_TYPE,/)
  assert.match(fn('dropAt'), /if \(!event\.dataTransfer\?\.types\?\.includes\(DRAG_TYPE\)\) return null/)
})

test('the drop row is found by height, not by the element under the pointer', () => {
  const body = fn('dropAt')
  assert.match(body, /sidebarModel\.rowAt\(rows, event\.clientY\)/)
  assert.doesNotMatch(body, /event\.target/)
})

test('a redraw asked for while a button is down waits for the click', () => {
  assert.match(fn('renderProjects'), /^\s*\n?function renderProjects \(\) \{\s*if \(redraw\.held\) \{\s*redraw\.owed = true\s*return/)
  assert.match(app, /document\.addEventListener\('mousedown', \(event\) => \{\s*holdRedraw\(\)/)
  assert.match(app, /document\.addEventListener\('mouseup', \(\) => \{ setTimeout\(releaseRedraw, 0\) \}, true\)/)
  assert.match(fn('holdRedraw'), /setTimeout\(releaseRedraw, \d+\)/)
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

test('a drag lets go of held redraws only after it has started, and a redrawn source stays dimmed', () => {
  const wiring = fn('wireDragAndDrop')
  const start = wiring.slice(wiring.indexOf("'dragstart'"), wiring.indexOf("'dragover'"))
  // Synchronously, the owed redraw would replace the row Chromium is starting
  // the drag from, and the drag would be cancelled.
  assert.doesNotMatch(start, /(?<!setTimeout\()releaseRedraw\(\)/)
  assert.match(start, /setTimeout\(releaseRedraw, 0\)/)
  assert.match(fn('projectRow'), /drag\.current\?\.kind === 'project' && drag\.current\.id === project\.id\) item\.classList\.add\('dragging'\)/)
  assert.match(fn('categoryGroup'), /drag\.current\?\.kind === 'category' && drag\.current\.id === category\.id\) group\.classList\.add\('dragging'\)/)
})
