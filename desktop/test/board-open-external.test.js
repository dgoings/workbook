'use strict'

// A relic body opened from the board ("Open in new tab") must not open inside
// the app: a window there would be an Electron window with the board's
// session, and the body is served for a browser tab of its own. The board
// view's window-open handler sends every new window to the default browser
// and refuses it in the app. This runs main.js against a stand-in Electron, as
// board-park.test.js does, opens a board, and calls the handler the board view
// was given.

const EventEmitter = require('node:events')
const Module = require('node:module')
const os = require('node:os')
const path = require('node:path')
const { test } = require('node:test')
const assert = require('node:assert/strict')

const MAIN = path.join(__dirname, '..', 'src', 'main', 'main.js')

const PROJECT = {
  id: 'A',
  key: 'KAA',
  name: 'Project A',
  path: path.join(os.tmpdir(), 'workbench-open-external-A'),
  url: 'http://127.0.0.1:7331/'
}

async function settle () {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setImmediate(resolve))
}

async function launch () {
  const h = { handlers: new Map(), views: [], chrome: null, external: [] }

  class FakeView {
    constructor (options = {}) {
      this.board = path.basename(options.webPreferences?.preload ?? '') === 'board.js'
      const view = this
      this.webContents = {
        url: '',
        send () {},
        loadFile () {},
        async loadURL (url) { this.url = url },
        getURL () { return this.url },
        focus () {},
        reload () {},
        close () {},
        setWindowOpenHandler (handler) { view.windowOpenHandler = handler },
        openDevTools () {},
        isDestroyed: () => false
      }
      if (this.board) h.views.push(this)
      else h.chrome = this
    }

    setVisible () {}
    setBounds () {}
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
    shell: { openExternal (url) { h.external.push(url) } },
    nativeTheme: { shouldUseDarkColors: false, on () {} },
    Menu: { setApplicationMenu () {}, buildFromTemplate: (template) => template },
    session: { defaultSession: { webRequest: { onCompleted () {} } } }
  }

  class FakeRegistry {
    constructor () {
      this.projects = [PROJECT]
      this.theme = 'system'
      this.sidebarCollapsed = false
      this.sidebarLayout = {}
      this.scanRoots = []
    }

    get orderedProjects () { return this.projects }
    find (id) { return this.projects.find((project) => project.id === id) ?? null }
    async load () {}
    async remove () { return false }
  }

  class FakeSupervisor extends EventEmitter {
    start (project) { return Promise.resolve(project.url) }
    status () { return { status: 'running' } }
    stop () {}
    stopAll () {}
    reapOrphans () { return [] }
  }

  // The real tracker, told every repository is present.
  const { createPresenceTracker } = require('../src/main/presence')
  const stubs = {
    electron,
    './presence': {
      createPresenceTracker: (options) => createPresenceTracker({
        ...options,
        check: async () => ({ missing: false, reason: null, message: null })
      })
    },
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
  const open = (projectId) => h.handlers.get('project:open')({ sender: h.chrome.webContents }, { projectId })
  return { h, open }
}

test('a relic body opened from a board goes to the default browser, not an app window', async () => {
  const app = await launch()
  await app.open('A')
  assert.equal(app.h.views.length, 1, 'no board view was made')
  const handler = app.h.views[0].windowOpenHandler
  assert.equal(typeof handler, 'function', 'the board view was given no window-open handler')

  const body = 'http://127.0.0.1:7331/relics/WB-R-01M8EVZ9BQR7GNFYPYHSVT6FRC/parts/01M8EVZ9BQ20NDMR0F09K3CGXB/body'
  const answer = handler({ url: body, frameName: '_blank', features: 'noopener', disposition: 'foreground-tab' })
  assert.deepEqual(answer, { action: 'deny' })
  assert.deepEqual(app.h.external, [body])
})
