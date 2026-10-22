'use strict'

// Whenever the shell's own project-view content is what the reader should see
// (a failed start, a view of the shell's own, a board that stopped), no board
// view may be laid over it, and the shell is told none is showing so the
// sidebar's divider follows. divider.test.js holds the source to its shape;
// these run main.js itself, against a stand-in Electron and stand-ins for the
// registry and the board servers, and look at what ends up laid in.

const EventEmitter = require('node:events')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { describe, test } = require('node:test')
const assert = require('node:assert/strict')

const MAIN = path.join(__dirname, '..', 'src', 'main', 'main.js')

const PROJECTS = ['A', 'B', 'C'].map((id, index) => ({
  id,
  key: `K${id}${id}`,
  name: `Project ${id}`,
  path: path.join(os.tmpdir(), `workbench-park-${id}`),
  url: `http://127.0.0.1:${7331 + index}/`
}))

function deferred () {
  let resolve, reject
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

async function settle () {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setImmediate(resolve))
}

/**
 * Load a fresh main.js and bring its window up. Board servers start at the
 * project's address unless `starts` says otherwise, and pages load at once
 * unless `loads` does.
 */
async function launch () {
  const h = {
    sent: [],
    views: [],
    handlers: new Map(),
    starts: new Map(),
    loads: new Map(),
    supervisor: null,
    chrome: null
  }

  class FakeView {
    constructor (options = {}) {
      this.board = path.basename(options.webPreferences?.preload ?? '') === 'board.js'
      this.visible = true
      this.bounds = null
      this.requested = null
      const view = this
      this.webContents = {
        url: '',
        send (channel, payload) { if (view === h.chrome) h.sent.push({ channel, payload }) },
        loadFile () {},
        async loadURL (url) {
          view.requested = url
          const pending = h.loads.get(url)
          if (pending) await pending()
          this.url = url
        },
        getURL () { return this.url },
        focus () {},
        reload () {},
        close () { view.closed = true },
        setWindowOpenHandler () {},
        openDevTools () {},
        isDestroyed: () => false
      }
      if (this.board) h.views.push(this)
      else h.chrome = this
    }

    setVisible (visible) { this.visible = visible }
    setBounds (bounds) { this.bounds = bounds }
  }

  class FakeWindow {
    constructor () {
      this.contentView = { addChildView () {}, removeChildView () {} }
      h.window = this
    }

    getContentBounds () { return { x: 0, y: 0, width: 1280, height: 820 } }
    on () {}
    once () {}
    setBackgroundColor () {}
    static getAllWindows () { return h.window ? [h.window] : [] }
  }

  const electron = {
    app: {
      getPath: () => os.tmpdir(),
      requestSingleInstanceLock: () => true,
      on () {},
      quit () {},
      whenReady: () => Promise.resolve()
    },
    BaseWindow: FakeWindow,
    WebContentsView: FakeView,
    ipcMain: {
      handle (channel, handler) { h.handlers.set(channel, handler) },
      on (channel, handler) { h.handlers.set(channel, handler) }
    },
    dialog: {},
    shell: { openExternal () {} },
    nativeTheme: { shouldUseDarkColors: false, on () {} },
    Menu: { setApplicationMenu () {}, buildFromTemplate: (template) => template },
    session: { defaultSession: { webRequest: { onCompleted () {} } } }
  }

  class FakeRegistry {
    constructor () {
      this.projects = PROJECTS
      this.theme = 'system'
      this.sidebarCollapsed = false
      this.sidebarLayout = {}
      this.scanRoots = []
    }

    get orderedProjects () { return this.projects }
    find (id) { return this.projects.find((project) => project.id === id) }
    async load () {}
  }

  class FakeSupervisor extends EventEmitter {
    constructor () {
      super()
      h.supervisor = this
    }

    start (project) {
      const start = h.starts.get(project.id)
      return start ? start() : Promise.resolve(project.url)
    }

    status () { return { status: 'running' } }
    stop () {}
    stopAll () {}
    reapOrphans () { return [] }
  }

  const stubs = {
    electron,
    './registry': { Registry: FakeRegistry },
    './supervisor': { Supervisor: FakeSupervisor },
    './updater': { setupUpdater () {} },
    './projectkeys': {
      createKeyRefresher: () => ({ refresh: async () => {} }),
      isKeyChange: () => false
    }
  }

  const load = Module._load
  Module._load = function (request, parent, isMain) {
    if (parent?.filename === MAIN && Object.hasOwn(stubs, request)) return stubs[request]
    return load.call(this, request, parent, isMain)
  }
  try {
    delete require.cache[MAIN]
    require(MAIN)
  } finally {
    Module._load = load
    delete require.cache[MAIN]
  }
  await settle()
  assert.ok(h.chrome, 'the window never came up')

  const ipc = (channel, args) => h.handlers.get(channel)({ sender: h.chrome.webContents }, args)
  return {
    h,
    open: (projectId) => ipc('project:open', { projectId }),
    showChrome: () => ipc('project:showChrome'),
    close: (projectId) => ipc('project:close', { projectId }),
    exit: (projectId) => h.supervisor.emit('exited', { projectId, code: 1, wasRunning: true }),
    startWith: (projectId, start) => h.starts.set(projectId, start),
    loadWith: (projectId, load) => {
      h.loads.set(PROJECTS.find((project) => project.id === projectId).url, load)
    },
    // The projects whose board view is laid in: open, visible and given a size.
    shown: () => h.views
      .filter((view) => !view.closed && view.visible && view.bounds && view.bounds.width > 0 && view.bounds.height > 0)
      .map((view) => PROJECTS.find((project) => view.requested?.startsWith(project.url))?.id),
    showing: () => h.sent.filter((message) => message.channel === 'board:showing').map((message) => message.payload)
  }
}

describe('a board view is never left over the shell\'s own message', () => {
  test('a board that fails to start parks the board that was showing', async () => {
    const app = await launch()
    await app.open('A')
    assert.deepEqual(app.shown(), ['A'])

    app.startWith('B', () => Promise.reject(new Error('serve exited with code 1')))
    await assert.rejects(app.open('B'), /serve exited with code 1/)
    assert.deepEqual(app.shown(), [], "A's view must not cover 'Could not start this board'")
    assert.equal(app.showing().at(-1), false, 'the shell must be told no board is showing')

    // Asked again once it can start, it shows.
    app.startWith('B', () => Promise.resolve(PROJECTS[1].url))
    await app.open('B')
    assert.deepEqual(app.shown(), ['B'])
    assert.equal(app.showing().at(-1), true)
  })

  test('a board whose page fails to load parks too', async () => {
    const app = await launch()
    await app.open('A')
    app.loadWith('B', () => Promise.reject(new Error('ERR_CONNECTION_REFUSED')))
    await assert.rejects(app.open('B'), /ERR_CONNECTION_REFUSED/)
    assert.deepEqual(app.shown(), [])
    assert.equal(app.showing().at(-1), false)
  })

  test('a failure an open has since overtaken leaves the newer board alone', async () => {
    const app = await launch()
    await app.open('A')
    const start = deferred()
    app.startWith('B', () => start.promise)
    const openB = app.open('B')
    await app.open('C')
    assert.deepEqual(app.shown(), ['C'])

    start.reject(new Error('serve exited with code 1'))
    await assert.rejects(openB, /serve exited with code 1/)
    assert.deepEqual(app.shown(), ['C'])
    assert.equal(app.showing().at(-1), true)
  })

  test('a board still starting when another view is chosen stays parked', async () => {
    const app = await launch()
    await app.open('A')
    const start = deferred()
    app.startWith('B', () => start.promise)
    const openB = app.open('B')

    await app.showChrome()
    assert.deepEqual(app.shown(), [])
    start.resolve(PROJECTS[1].url)
    await openB
    assert.deepEqual(app.shown(), [], "B must not be laid over the view the reader moved to")
    assert.equal(app.showing().at(-1), false)
  })

  test('a board still loading when another view is chosen stays parked', async () => {
    const app = await launch()
    await app.open('A')
    const load = deferred()
    app.loadWith('B', () => load.promise)
    const openB = app.open('B')
    await settle()

    await app.showChrome()
    load.resolve()
    await openB
    assert.deepEqual(app.shown(), [])
    assert.equal(app.showing().at(-1), false)
  })

  test('a board that stops is parked, and the shell told', async () => {
    const app = await launch()
    await app.open('A')
    app.exit('A')
    assert.deepEqual(app.shown(), [], "A's dead page must not cover 'This board stopped'")
    assert.equal(app.showing().at(-1), false)
    assert.ok(app.h.sent.some((message) => message.channel === 'project:exited' &&
      message.payload.projectId === 'A'), 'the shell must still hear that A stopped')
  })

  test('a board that stops while not showing changes nothing', async () => {
    const app = await launch()
    await app.open('A')
    await app.open('B')
    app.exit('A')
    assert.deepEqual(app.shown(), ['B'])
    assert.equal(app.showing().at(-1), true)
  })

  test('a board that stops does not abandon the open that is replacing it', async () => {
    const app = await launch()
    await app.open('A')
    const start = deferred()
    app.startWith('B', () => start.promise)
    const openB = app.open('B')

    app.exit('A')
    assert.deepEqual(app.shown(), [])
    start.resolve(PROJECTS[1].url)
    await openB
    assert.deepEqual(app.shown(), ['B'], 'the reader asked for B, and B must still arrive')
    assert.equal(app.showing().at(-1), true)
  })

  test('closing the showing board does not abandon the open that is replacing it', async () => {
    const app = await launch()
    await app.open('A')
    const start = deferred()
    app.startWith('B', () => start.promise)
    const openB = app.open('B')

    await app.close('A')
    assert.deepEqual(app.shown(), [])
    assert.equal(app.showing().at(-1), false)
    start.resolve(PROJECTS[1].url)
    await openB
    assert.deepEqual(app.shown(), ['B'])
  })
})
