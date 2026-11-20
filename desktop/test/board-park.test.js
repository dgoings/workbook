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

function holdPresence (h, projectId) {
  const gate = deferred()
  h.holds.set(PROJECTS.find((project) => project.id === projectId).path, () => gate.promise)
  return () => {
    h.holds.clear()
    gate.resolve()
  }
}

function serving (h, projectId) {
  let up = false
  for (const [event, id] of h.events) {
    if (id === projectId) up = event === 'up'
  }
  return up
}

async function settle () {
  for (let i = 0; i < 10; i++) await new Promise((resolve) => setImmediate(resolve))
}

/**
 * Load a fresh main.js and bring its window up. Board servers start at the
 * project's address unless `starts` says otherwise, and pages load at once
 * unless `loads` does.
 */
async function launch (before = () => {}) {
  const h = {
    sent: [],
    views: [],
    handlers: new Map(),
    starts: new Map(),
    loads: new Map(),
    supervisor: null,
    chrome: null,
    // Paths whose repository the presence check reports as gone, the board
    // starts asked for, the servers stopped, and the last menu installed.
    missing: new Set(),
    started: [],
    stopped: [],
    menu: null,
    // Presence checks held open until the test lets them go, by path, and the
    // order servers came up and were stopped in: 'up' when a start resolves
    // with an address, 'stop' when one is stopped. A stop that arrives before
    // its start resolves is modeled as lost, the worst a supervisor can do, so
    // only a stop after the last 'up' counts as stopped.
    holds: new Map(),
    events: [],
    // The window's event handlers, and every path the presence check looked at.
    windowEvents: new Map(),
    checked: []
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
    on (event, handler) {
      if (!h.windowEvents.has(event)) h.windowEvents.set(event, [])
      h.windowEvents.get(event).push(handler)
    }
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
    Menu: { setApplicationMenu (menu) { h.menu = menu }, buildFromTemplate: (template) => template },
    session: { defaultSession: { webRequest: { onCompleted () {} } } }
  }

  class FakeRegistry {
    constructor () {
      h.registry = this
      this.projects = [...PROJECTS]
      this.theme = 'system'
      this.sidebarCollapsed = false
      this.sidebarLayout = {}
      this.scanRoots = []
    }

    get orderedProjects () { return this.projects }
    find (id) { return this.projects.find((project) => project.id === id) ?? null }
    async load () {}
    async remove (id) {
      if (!this.find(id)) return false
      this.projects = this.projects.filter((project) => project.id !== id)
      return true
    }
  }

  class FakeSupervisor extends EventEmitter {
    constructor () {
      super()
      h.supervisor = this
    }

    start (project) {
      h.started.push(project.id)
      const start = h.starts.get(project.id)
      return (start ? start() : Promise.resolve(project.url)).then((url) => {
        h.events.push(['up', project.id])
        return url
      })
    }

    status () { return { status: 'running' } }
    stop (projectId) {
      h.stopped.push(projectId)
      h.events.push(['stop', projectId])
    }
    stopAll () {}
    reapOrphans () { return [] }
  }

  // The real tracker, looking at the paths the test says are gone instead of
  // the disk.
  const { createPresenceTracker } = require('../src/main/presence')
  const stubs = {
    electron,
    './presence': {
      createPresenceTracker: (options) => createPresenceTracker({
        ...options,
        check: async (dir) => {
          h.checked.push(dir)
          const hold = h.holds.get(dir)
          if (hold) await hold()
          return h.missing.has(dir)
            ? { missing: true, reason: 'not-found', message: `Repository not found at ${dir}` }
            : { missing: false, reason: null, message: null }
        }
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

  before(h)
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
    remove: (projectId) => ipc('project:forget', { projectId }),
    presence: (projectId) => ipc('project:presence', { projectId }),
    listed: () => ipc('registry:list'),
    focusWindow: () => { for (const handler of h.windowEvents.get('focus') ?? []) handler() },
    exit: (projectId) => h.supervisor.emit('exited', { projectId, code: 1, wasRunning: true }),
    startWith: (projectId, start) => h.starts.set(projectId, start),
    // Hold the presence check for a project open until the returned function
    // is called.
    holdPresence: (projectId) => holdPresence(h, projectId),
    // Whether a server for the project is up: it came up and nothing stopped
    // it after.
    serving: (projectId) => serving(h, projectId),
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

describe('removing a project from Workbench', () => {
  const sentOn = (app, channel) => app.h.sent.filter((message) => message.channel === channel).map((message) => message.payload)
  const menuNames = (app) => JSON.stringify(app.h.menu)

  test('the showing board is let go of, its server stopped, and the sidebar and menu told', async () => {
    const app = await launch()
    await app.open('A')
    assert.deepEqual(app.shown(), ['A'])
    assert.match(menuNames(app), /Project A/)

    const answer = await app.remove('A')
    assert.equal(answer.removed, true)
    assert.deepEqual(answer.projects.map((project) => project.id), ['B', 'C'])
    assert.deepEqual(app.shown(), [])
    assert.ok(app.h.views.every((view) => view.closed), 'the removed board\'s view is still open')
    assert.deepEqual(app.h.stopped, ['A'])
    assert.equal(app.showing().at(-1), false)
    const layouts = sentOn(app, 'sidebar:layoutChanged')
    assert.deepEqual(layouts.at(-1).projects.map((project) => project.id), ['B', 'C'])
    assert.doesNotMatch(menuNames(app), /Project A/)
    assert.match(menuNames(app), /Project B/)
  })

  test('a second removal, or one of an id never known, is a quiet no-op', async () => {
    const app = await launch()
    await app.remove('B')
    const before = app.h.sent.length
    assert.equal((await app.remove('B')).removed, false)
    assert.equal((await app.remove('nobody')).removed, false)
    assert.deepEqual(app.h.stopped, ['B'])
    assert.equal(app.h.sent.length, before, 'a no-op announced something')
  })

  test('a project whose repository is gone is marked, never started, and removed like any other', async () => {
    const app = await launch()
    app.h.missing.add(PROJECTS[1].path)
    const found = await app.presence('B')
    assert.equal(found.known, true)
    assert.equal(found.missing, true)
    assert.equal(found.message, `Repository not found at ${PROJECTS[1].path}`)
    const listed = (await app.listed()).projects.find((project) => project.id === 'B')
    assert.equal(listed.missing, true)
    assert.equal(listed.missingMessage, found.message)
    // The change was announced, so the sidebar marks it without a reload.
    assert.ok(sentOn(app, 'sidebar:layoutChanged').some((payload) =>
      payload.projects.find((project) => project.id === 'B')?.missing === true))

    const started = app.h.started.length
    await assert.rejects(app.open('B'), /Repository not found at/)
    assert.equal(app.h.started.length, started, 'a server was spawned for a missing repository')
    assert.deepEqual(app.shown(), [])

    assert.equal((await app.remove('B')).removed, true)
    assert.equal((await app.presence('B')).known, false)
  })

  test('project:presence refuses arguments that are not a project id, as the sidebar channels do', async () => {
    const app = await launch()
    const ask = (args) => app.h.handlers.get('project:presence')({ sender: app.h.chrome.webContents }, args)
    for (const args of [undefined, null, 'A', ['A']]) {
      await assert.rejects(async () => ask(args), /^TypeError: project:presence: arguments must be an object$/)
    }
    for (const projectId of [undefined, null, '', 42]) {
      await assert.rejects(async () => ask({ projectId }), /^TypeError: project:presence: projectId must be a non-empty string$/)
    }
    assert.equal((await ask({ projectId: 'A' })).known, true)
  })

  test('the window coming back into focus looks at every repository again', async () => {
    const app = await launch()
    app.h.missing.add(PROJECTS[1].path)
    app.h.checked.length = 0
    app.focusWindow()
    await settle()
    assert.deepEqual([...app.h.checked].sort(), PROJECTS.map((project) => project.path).sort())
    const listed = (await app.listed()).projects.find((project) => project.id === 'B')
    assert.equal(listed.missing, true, 'a repository gone while the app was in the background is not marked')
    assert.ok(sentOn(app, 'sidebar:layoutChanged').some((payload) =>
      payload.projects.find((project) => project.id === 'B')?.missing === true), 'the sidebar was not told')
  })

  test('a board that stops looks at its own repository again', async () => {
    const app = await launch()
    await app.open('A')
    app.h.missing.add(PROJECTS[0].path)
    app.h.checked.length = 0
    app.exit('A')
    await settle()
    assert.deepEqual(app.h.checked, [PROJECTS[0].path])
    const listed = (await app.listed()).projects.find((project) => project.id === 'A')
    assert.equal(listed.missing, true)
  })

  test('a repository that comes back is unmarked the next time anything looks', async () => {
    const app = await launch()
    app.h.missing.add(PROJECTS[2].path)
    assert.equal((await app.presence('C')).missing, true)
    app.h.missing.clear()
    assert.equal((await app.presence('C')).missing, false)
    await app.open('C')
    assert.deepEqual(app.shown(), ['C'])
  })

  test('a board still starting when its project is removed never lays in', async () => {
    const app = await launch()
    const start = deferred()
    app.startWith('A', () => start.promise)
    const opening = app.open('A')
    await settle()
    await app.remove('A')
    start.resolve(PROJECTS[0].url)
    await assert.rejects(opening, /removed from Workbench/)
    assert.deepEqual(app.shown(), [])
    assert.equal(app.h.views.length, 0, 'a view was made for a removed project')
    assert.equal(app.serving('A'), false, 'the removed project\'s server was left running')
  })

  test('an open still looking for its repository when the project is removed spawns nothing', async () => {
    const app = await launch()
    const started = app.h.started.length
    const release = app.holdPresence('A')
    const opening = app.open('A')
    await settle()
    await app.remove('A')
    release()
    await assert.rejects(opening, /removed from Workbench/)
    assert.deepEqual(app.h.started.slice(started), [], 'a server was spawned for a removed project')
    assert.equal(app.serving('A'), false)
    assert.deepEqual(app.shown(), [])
  })

  test('a launch start still looking for its repository when the project is removed spawns nothing', async () => {
    let release
    const app = await launch((h) => { release = holdPresence(h, 'A') })
    assert.deepEqual(app.h.started, ['B', 'C'])
    await app.remove('A')
    release()
    await settle()
    assert.deepEqual(app.h.started, ['B', 'C'], 'a server was spawned for a removed project')
    assert.equal(app.serving('A'), false)
  })

  test('a launch start still binding when the project is removed is stopped once it comes up', async () => {
    const start = deferred()
    const app = await launch((h) => { h.starts.set('A', () => start.promise) })
    await app.remove('A')
    start.resolve(PROJECTS[0].url)
    await settle()
    assert.equal(app.serving('A'), false, 'the removed project\'s server was left running')
  })

  test('a board still loading when its project is removed is let go of', async () => {
    const app = await launch()
    const load = deferred()
    app.loadWith('A', () => load.promise)
    const opening = app.open('A')
    await settle()
    await app.remove('A')
    load.resolve()
    await assert.rejects(opening, /removed from Workbench/)
    assert.deepEqual(app.shown(), [])
    assert.ok(app.h.views.every((view) => view.closed))
  })

  test('a project imported again after removal is a new one: no stale board, no stale mark', async () => {
    const app = await launch()
    app.h.missing.add(PROJECTS[0].path)
    await app.presence('A')
    await app.open('A').catch(() => {})
    await app.remove('A')
    app.h.missing.clear()
    // What import:apply does for the repository, without the discovery it
    // needs a real checkout for.
    app.h.registry.projects.push(PROJECTS[0])
    const listed = (await app.listed()).projects.find((project) => project.id === 'A')
    assert.equal(listed.missing, false)
    await app.open('A')
    assert.deepEqual(app.shown(), ['A'])
  })
})
