'use strict'

// What the project view says when a board fails to start, and which open gets
// to say it. openProject and openNextTask are run here against fakes: app.js
// only loads inside Electron, so the functions are taken out of its source,
// comments removed, the way category-toggle.test.js runs categoryGroup.

const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const { describe, test } = require('node:test')
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

// What Electron's ipcRenderer.invoke rejects with when the handler throws.
function invokeRejection (message) {
  return new Error(`Error invoking remote method 'project:open': Error: ${message}`)
}

// A shell whose board starts settle only when the test says so, one deferred
// promise per api.openProject call, in call order.
function shell () {
  const boardState = { textContent: '' }
  const starts = []
  const state = {
    view: 'next',
    activeProjectId: null,
    projects: [
      { id: 'b', name: 'Bravo', status: 'stopped' },
      { id: 'c', name: 'Charlie', status: 'stopped' }
    ]
  }
  const context = vm.createContext({
    state,
    el: (id) => {
      assert.equal(id, 'board-state', `the open touched #${id}`)
      return boardState
    },
    setView: (view, projectId = null) => {
      state.view = view
      state.activeProjectId = projectId
    },
    hideIdentityForm: () => {},
    askForIdentity: async () => false,
    loadProjects: () => {},
    api: {
      openProject: (projectId, taskId) => new Promise((resolve, reject) => {
        starts.push({ projectId, taskId, resolve, reject })
      })
    }
  })
  vm.runInContext(`${fn('openProject')}\n${fn('openNextTask')}\n${fn('stillOpening')}\n${fn('remoteMessage')}`, context)
  return { context, state, boardState, starts }
}

// Let every pending continuation run.
const settle = () => new Promise((resolve) => setImmediate(resolve))

describe('a board that fails to start', () => {
  test('says why in the server\'s words, not Electron\'s', async () => {
    const { context, boardState, starts } = shell()
    const opening = context.openProject('b')
    await settle()
    starts[0].reject(invokeRejection('workbook serve exited with status 1'))
    await opening
    assert.equal(boardState.textContent, 'Could not start this board: workbook serve exited with status 1')
  })

  test('opened on a task, says why the same way', async () => {
    const { context, boardState, starts } = shell()
    const opening = context.openNextTask('b', { id: 'WB-1', title: 'Fix it' })
    await settle()
    starts[0].reject(invokeRejection('workbook serve exited with status 1'))
    await opening
    assert.equal(boardState.textContent, 'Could not open this board: workbook serve exited with status 1')
  })
})

describe('an open another project overtook', () => {
  test('leaves the newer project\'s line alone when it fails', async () => {
    const { context, state, boardState, starts } = shell()
    const slow = context.openProject('b')
    await settle()
    context.openProject('c')
    await settle()
    assert.equal(state.activeProjectId, 'c')
    starts[0].reject(invokeRejection('workbook serve exited with status 1'))
    await slow
    assert.equal(boardState.textContent, 'Starting the board for Charlie…')
  })

  test('leaves the newer project\'s line alone when it succeeds', async () => {
    const { context, boardState, starts } = shell()
    const slow = context.openProject('b')
    await settle()
    context.openProject('c')
    await settle()
    starts[0].resolve({ url: 'http://127.0.0.1:1/' })
    await slow
    assert.equal(boardState.textContent, 'Starting the board for Charlie…')
  })

  test('opened on a task, leaves the newer project\'s line alone when it fails', async () => {
    const { context, boardState, starts } = shell()
    const slow = context.openNextTask('b', { id: 'WB-1', title: 'Fix it' })
    await settle()
    context.openProject('c')
    await settle()
    starts[0].reject(invokeRejection('workbook serve exited with status 1'))
    await slow
    assert.equal(boardState.textContent, 'Starting the board for Charlie…')
  })

  test('leaves the line alone once the reader has left for another view', async () => {
    const { context, state, boardState, starts } = shell()
    const slow = context.openProject('b')
    await settle()
    context.setView('next')
    boardState.textContent = 'left behind'
    starts[0].reject(invokeRejection('workbook serve exited with status 1'))
    await slow
    assert.equal(state.view, 'next')
    assert.equal(boardState.textContent, 'left behind')
  })

  test('the open still wanted reports its own failure', async () => {
    const { context, boardState, starts } = shell()
    context.openProject('b')
    await settle()
    const current = context.openProject('c')
    await settle()
    starts[1].reject(invokeRejection('no workbook on PATH'))
    await current
    assert.equal(boardState.textContent, 'Could not start this board: no workbook on PATH')
  })
})
