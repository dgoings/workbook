'use strict'

// projectkeys.js keeps a project's label at its current key, and
// test/projectkeys.test.js holds it to that. None of it reaches the sidebar
// unless main.js asks for a read when a key may have moved and announces what
// changed the way every other sidebar change is announced. main.js cannot be
// loaded without Electron, so this reads its source, comments removed, the way
// ordered-projects.test.js does.

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

/** The text of a top-level `const name = ...` statement, up to the next blank line. */
function constant (name) {
  const at = main.indexOf(`\nconst ${name} = `)
  assert.notEqual(at, -1, `no top-level const ${name} in main.js`)
  const end = main.indexOf('\n\n', at + 1)
  return main.slice(at, end === -1 ? undefined : end)
}

test('one key refresher stores into the registry and redraws the sidebar and the menu', () => {
  const body = constant('keyRefresher')
  assert.match(body, /projectkeys\.createKeyRefresher\(/)
  assert.match(body, /projects: \(\) => registry\.projects\b/)
  assert.match(body, /apply: \(changes\) => registry\.setKeys\(changes\)/)
  // The same broadcast every layout edit sends, so the renderer redraws the
  // sidebar, the rail and an open Next view from one answer.
  assert.match(body, /onChanged:[\s\S]*installMenu\(\)/)
  assert.match(body, /onChanged:[\s\S]*toChrome\('sidebar:layoutChanged', sidebarCommands\.layout\(\)\)/)
  assert.equal(main.match(/createKeyRefresher\(/g).length, 1)
})

test('a board starting reads its project\'s current key', () => {
  const body = fn('startProjectServer')
  assert.match(body, /finally \{[\s\S]*refreshKeys\(\[project\.id\]\)/)
})

test('opening a project reads its current key', () => {
  assert.match(fn('openProject'), /refreshKeys\(\[projectId\]\)/)
})

test('the window taking focus reads every project\'s current key', () => {
  // A key moved from a terminal, which is where `workbook key current` runs,
  // is noticed when the reader comes back to the window.
  assert.match(fn('createWindow'), /window\.on\('focus', \(\) => \{ refreshKeys\(\) \}\)/)
})

test('a board changing a key reads that project\'s current key', () => {
  assert.match(main, /session\.defaultSession\.webRequest\.onCompleted\(/)
  const watcher = main.slice(main.indexOf('webRequest.onCompleted('))
  assert.match(watcher.slice(0, 400), /projectkeys\.isKeyChange\(details\)/)
  assert.match(watcher.slice(0, 400), /boardProjectId\(details\.webContentsId\)/)
  // The project that made the request is the one re-read; an unknown board
  // re-reads everyone (undefined), never nobody.
  assert.match(watcher.slice(0, 400), /refreshKeys\(projectId \? \[projectId\] : undefined\)/)
  assert.match(fn('boardProjectId'), /for \(const \[projectId, view\] of boardViews\)/)
})

test('refreshKeys never lets a failed read become an unhandled rejection', () => {
  assert.match(fn('refreshKeys'), /keyRefresher\.refresh\(ids\)\.catch\(/)
})
