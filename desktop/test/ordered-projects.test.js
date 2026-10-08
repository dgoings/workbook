'use strict'

// Every place that shows or numbers projects reads them in sidebar order.
//
// registry.projects is still there, in storage (import) order, and is the
// right thing to read for a lookup by path or for starting every server. But a
// list the user sees or counts read from it would disagree with the sidebar
// once anything is dragged: Cmd+3 would open a project the sidebar shows
// third from somewhere else, and the Next view would list them in an order
// nobody chose. main.js cannot be loaded without Electron, so this reads its
// source, comments removed, the way layout-wiring.test.js does.

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

const main = code('main', 'main.js')

/** A top-level function's text, from its declaration to its closing brace. */
function fn (name) {
  const match = main.match(new RegExp(`\\n(?:async )?function ${name} \\([\\s\\S]*?\\n\\}\\n`))
  assert.ok(match, `no top-level function ${name} in main.js`)
  return match[0]
}

/** An ipcMain.handle registration's text, up to the next blank line. */
function handler (channel) {
  const at = main.indexOf(`ipcMain.handle('${channel}'`)
  assert.notEqual(at, -1, `main.js does not handle ${channel}`)
  const end = main.indexOf('\n\n', at)
  return main.slice(at, end === -1 ? undefined : end)
}

test('the shared project list reads orderedProjects', () => {
  const body = fn('listedProjects')
  assert.match(body, /registry\.orderedProjects\b/)
  assert.doesNotMatch(body, /registry\.projects\b/)
})

for (const channel of ['registry:list', 'next:load']) {
  test(`${channel} lists projects in sidebar order`, () => {
    const body = handler(channel)
    assert.match(body, /projects: listedProjects\(\)/)
    assert.doesNotMatch(body, /registry\.projects\b/)
  })
}

test('the menu numbers projects in sidebar order', () => {
  const body = fn('installMenu')
  assert.match(body, /projects: registry\.orderedProjects\b/)
  assert.doesNotMatch(body, /registry\.projects\b/)
})

test('a layout edit rebuilds the menu and announces the ordered list', () => {
  const wiring = main.match(/createSidebarCommands\(\{[\s\S]*?\n\}\)/)
  assert.ok(wiring, 'main.js does not create the sidebar commands')
  assert.match(wiring[0], /listProjects: listedProjects\b/)
  assert.match(wiring[0], /rebuildMenu: \(\) => installMenu\(\)/)
  assert.match(wiring[0], /broadcast: \(payload\) => toChrome\('sidebar:layoutChanged', payload\)/)
  assert.match(wiring[0], /mintId: \(\) => crypto\.randomUUID\(\)/)
})

test('every sidebar channel goes to its command', () => {
  const commands = {
    'sidebar:layout': 'layout',
    'sidebar:moveProject': 'moveProject',
    'sidebar:moveCategory': 'moveCategory',
    'sidebar:createCategory': 'createCategory',
    'sidebar:renameCategory': 'renameCategory',
    'sidebar:setCategoryCollapsed': 'setCategoryCollapsed',
    'sidebar:deleteCategory': 'deleteCategory'
  }
  for (const [channel, command] of Object.entries(commands)) {
    assert.match(handler(channel), new RegExp(`sidebarCommands\\.${command}\\(`), channel)
  }
})

test('the sidebar commands never read the storage-order list', () => {
  assert.doesNotMatch(code('main', 'sidebarcommands.js'), /registry\.projects\b/)
})
