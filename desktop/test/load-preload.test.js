'use strict'

// load-preload.js evaluates each preload the way Electron would before any
// page script runs, with the browser and Electron stubbed out, so a preload
// that would fail to load fails `npm run check` instead of a smoke run. The
// board's preload is sandboxed, and the case that motivated this — a relative
// require in board.js, which Node resolves happily and a sandboxed preload
// refuses — has to fail here even though the file it names exists.

const { describe, test } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const { preloadsOf, loadPreload, sentToBoards } = require('../scripts/load-preload')

const root = path.join(__dirname, '..')
const mainPath = path.join(root, 'src', 'main', 'main.js')
const boardPath = path.join(root, 'src', 'preload', 'board.js')
const shellPath = path.join(root, 'src', 'preload', 'preload.js')

function readLf (file) {
  return fs.readFileSync(file, 'utf8').replace(/\r\n/g, '\n')
}

/** A directory holding `files`, so a relative require in one has something to find. */
function fixture (files) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'load-preload-'))
  for (const [name, text] of Object.entries(files)) fs.writeFileSync(path.join(directory, name), text)
  return directory
}

describe('preloadsOf', () => {
  test('finds both views in main.js and which of them keeps the sandbox', () => {
    assert.deepEqual(preloadsOf(readLf(mainPath)), [
      { name: 'preload.js', sandboxed: false },
      { name: 'board.js', sandboxed: true }
    ])
  })

  test('reads a CRLF checkout of main.js the same way', () => {
    const crlf = readLf(mainPath).replace(/\n/g, '\r\n')
    assert.deepEqual(preloadsOf(crlf), preloadsOf(readLf(mainPath)))
  })

  test('a comment that says sandbox: false does not turn the sandbox off', () => {
    const source = [
      'new WebContentsView({',
      '  webPreferences: {',
      "    // Deliberately not `sandbox: false`, unlike the shell's view.",
      "    preload: path.join(__dirname, '..', 'preload', 'board.js'),",
      '    contextIsolation: true',
      '  }',
      '})'
    ].join('\n')
    assert.deepEqual(preloadsOf(source), [{ name: 'board.js', sandboxed: true }])
  })
})

describe('loadPreload', () => {
  test('the board preload loads inside the sandbox', () => {
    const { listened, sent } = loadPreload(boardPath, { sandboxed: true })
    assert.deepEqual([...listened].sort(), ['board:align', 'board:command'])
    // Only what it sends while loading: board:scheme waits for a click.
    assert.deepEqual([...sent], ['theme:current'])
  })

  test('the shell preload loads and exposes nothing undefined', () => {
    const { exposed } = loadPreload(shellPath, { sandboxed: false })
    const api = exposed.get('workbench')
    assert.ok(api, 'workbench is exposed')
    assert.equal(typeof api.titleRowHeight, 'number')
    assert.equal(typeof api.railWidth, 'number')
  })

  test('a relative require fails in the sandbox although the file is there', () => {
    const directory = fixture({
      'helper.js': 'module.exports = { run () {} }\n',
      'board.js': "'use strict'\nconst { run } = require('./helper')\nrun()\n"
    })
    const file = path.join(directory, 'board.js')
    assert.throws(() => loadPreload(file, { sandboxed: true }), /'\.\/helper'.*sandboxed/s)
    // The same file is fine where Node's own require applies, so it is the
    // sandbox being modeled that refuses it, not a missing file.
    assert.doesNotThrow(() => loadPreload(file, { sandboxed: false }))
  })

  test('the sandbox allows events, timers and url and refuses other builtins', () => {
    for (const name of ['events', 'node:events', 'timers', 'node:timers', 'url', 'node:url']) {
      const file = path.join(fixture({ 'p.js': `require('${name}')\n` }), 'p.js')
      assert.doesNotThrow(() => loadPreload(file, { sandboxed: true }), name)
    }
    for (const name of ['fs', 'node:fs', 'path', 'child_process']) {
      const file = path.join(fixture({ 'p.js': `require('${name}')\n` }), 'p.js')
      assert.throws(() => loadPreload(file, { sandboxed: true }), new RegExp(`'${name}'`), name)
    }
  })

  test('an electron module a preload cannot have is refused, not undefined', () => {
    const file = path.join(fixture({ 'p.js': "const { ipcMain } = require('electron')\n" }), 'p.js')
    assert.throws(() => loadPreload(file, { sandboxed: true }), /ipcMain/)
  })

  test('a preload that throws while it loads is reported with its file', () => {
    const file = path.join(fixture({ 'p.js': 'undefinedHelper()\n' }), 'p.js')
    assert.throws(() => loadPreload(file, { sandboxed: true }), /p\.js.*undefinedHelper/s)
  })

  test('an exposed value that is undefined is reported by name', () => {
    const file = path.join(fixture({
      'p.js': "require('electron').contextBridge.exposeInMainWorld('api', { height: undefined, ok: 1 })\n"
    }), 'p.js')
    assert.throws(() => loadPreload(file, { sandboxed: false }), /api\.height/)
  })
})

describe('sentToBoards', () => {
  test("names the channels main.js pushes at board views and not the shell's", () => {
    assert.deepEqual([...sentToBoards(readLf(mainPath))].sort(), ['board:align', 'board:command'])
  })

  test('a channel renamed on the sending side shows up as a different name', () => {
    const renamed = readLf(mainPath).replace("send('board:align'", "send('board:realign'")
    assert.deepEqual([...sentToBoards(renamed)].sort(), ['board:command', 'board:realign'])
  })
})
