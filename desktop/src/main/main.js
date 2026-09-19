'use strict'

const { app, BaseWindow, WebContentsView, ipcMain, dialog, shell, nativeTheme } = require('electron')
const path = require('node:path')

const { Registry } = require('./registry')
const { Supervisor } = require('./supervisor')
const discovery = require('./discovery')
const repoinfo = require('./repoinfo')
const workbook = require('./workbook')
const { setupUpdater } = require('./updater')

const SIDEBAR_WIDTH = 260
// The collapsed sidebar is a rail rather than nothing at all, and 76 is the
// narrowest it can be while the macOS inset traffic lights still sit over shell
// chrome instead of over the board.
const RAIL_WIDTH = 76
const MIN_WIDTH = 1000
const MIN_HEIGHT = 680

/** @type {BaseWindow|null} */
let window = null
/** @type {WebContentsView|null} */
let chromeView = null
/** Board views, one per project, kept warm once opened. @type {Map<string, WebContentsView>} */
const boardViews = new Map()
let activeProjectId = null

const registry = new Registry(app.getPath('userData'))
const supervisor = new Supervisor(app.getPath('userData'))

/** @type {{check: (options?: {silent?: boolean}) => Promise<object>}|null} */
let updater = null

function sidebarWidth () {
  return registry.sidebarCollapsed ? RAIL_WIDTH : SIDEBAR_WIDTH
}

function boardBounds () {
  const { width, height } = window.getContentBounds()
  const sidebar = sidebarWidth()
  return { x: sidebar, y: 0, width: Math.max(0, width - sidebar), height }
}

function layout () {
  if (!window) return
  const { width, height } = window.getContentBounds()
  chromeView?.setBounds({ x: 0, y: 0, width, height })
  const bounds = boardBounds()
  for (const [projectId, view] of boardViews) {
    // Views for projects that are not showing are parked off-screen rather than
    // detached, so switching back does not reload the board or lose its state.
    view.setBounds(projectId === activeProjectId ? bounds : { x: 0, y: 0, width: 0, height: 0 })
  }
}

function toChrome (channel, payload) {
  chromeView?.webContents.send(channel, payload)
}

function createWindow () {
  const dark = resolveDark()
  const isMac = process.platform === 'darwin'
  const isWindows = process.platform === 'win32'

  window = new BaseWindow({
    width: 1280,
    height: 820,
    minWidth: MIN_WIDTH,
    minHeight: MIN_HEIGHT,
    title: 'Workbench',
    // macOS keeps its inset traffic lights over the sidebar. Windows 11 hides
    // the title bar and draws native overlay controls instead, which is what
    // keeps Snap Layouts working; anything else loses them. Linux takes the
    // ordinary decorations its desktop draws.
    titleBarStyle: isMac ? 'hiddenInset' : isWindows ? 'hidden' : 'default',
    ...(isWindows && {
      titleBarOverlay: {
        color: '#00000000',
        symbolColor: dark ? '#e4e9f2' : '#34425a',
        height: 36
      }
    }),
    // macOS reads its icon from the bundle; the other two need to be told.
    ...(isMac ? {} : { icon: path.join(__dirname, '..', '..', 'assets', 'icon.png') }),
    backgroundColor: dark ? '#0f141c' : '#e9eef5'
  })

  chromeView = new WebContentsView({
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false
    }
  })
  window.contentView.addChildView(chromeView)
  chromeView.webContents.loadFile(path.join(__dirname, '..', 'renderer', 'index.html'))
  watchSidebarShortcut(chromeView.webContents)

  window.on('resize', layout)
  layout()

  if (process.argv.includes('--dev')) {
    chromeView.webContents.openDevTools({ mode: 'detach' })
  }
}

// --- theme -----------------------------------------------------------------

/**
 * Whether the app is currently dark.
 *
 * This answers for the two pieces of chrome no stylesheet reaches: the window's
 * own background color and, on Windows, the native title bar overlay. 'system'
 * defers to the OS, which is why nativeTheme is consulted rather than
 * remembered — the OS can flip while the app runs, and a remembered answer
 * would leave the window background and those controls painted for the mode the
 * app started in.
 */
function resolveDark () {
  const choice = registry.theme
  if (choice === 'dark') return true
  if (choice === 'light') return false
  return nativeTheme.shouldUseDarkColors
}

/**
 * Put the window and the shell in the current mode.
 *
 * The boards are not painted from here. Each board has a Dark Mode switch and
 * a stored preference of its own, and the choice the app carries is that
 * preference, made once and copied to every board: see `board:scheme` below
 * and src/preload/board.js. Electron's own theme source is left on the OS, so
 * "follow the system" means the same thing in every view.
 */
async function applyTheme () {
  const dark = resolveDark()
  window?.setBackgroundColor(dark ? '#0f141c' : '#e9eef5')
  if (process.platform === 'win32' && window?.setTitleBarOverlay) {
    // Native overlay controls are painted by Windows, not by the page, so they
    // do not follow the stylesheet and have to be repainted by hand.
    window.setTitleBarOverlay({
      color: '#00000000',
      symbolColor: dark ? '#e4e9f2' : '#34425a',
      height: 36
    })
  }
  toChrome('theme:changed', { theme: registry.theme, dark })
}

// --- sidebar ---------------------------------------------------------------

/**
 * Collapse the sidebar to a rail, or expand it again.
 *
 * The main process owns this the way it owns the theme, and for a sharper
 * reason: a board is a native view positioned from here, so a sidebar that
 * changed width in the page alone would leave every board sitting over the new
 * width or short of it. The state is stored, the views are moved, and only then
 * is the shell told, so the page restyles against bounds that already match.
 */
async function setSidebarCollapsed (collapsed) {
  if (collapsed === registry.sidebarCollapsed) return
  await registry.setSidebarCollapsed(collapsed)
  layout()
  // Announced from the store rather than from the argument: the setter rolls
  // back if the write fails, and the shell must never be painted for a state
  // the registry refused.
  toChrome('sidebar:changed', { collapsed: registry.sidebarCollapsed })
}

function toggleSidebar () {
  return setSidebarCollapsed(!registry.sidebarCollapsed)
}

/**
 * Watch one view's web contents for the collapse chord.
 *
 * Every view needs its own listener, which is why this is a function and not a
 * single hook. It cannot live in the shell page: when a board is showing,
 * keyboard focus is inside that board's native view, and a listener on the
 * shell's page would never hear the chord. An application menu would reach both
 * but would also put a menu on Windows and Linux, which this window does not
 * have.
 */
function watchSidebarShortcut (webContents) {
  webContents.on('before-input-event', (event, input) => {
    // `key` is what the layout produces, so on a Cyrillic, Greek, Hebrew or
    // Arabic layout the physical B key reports another character entirely and
    // the chord would never match. `code` names the physical key instead. Both
    // are accepted rather than just the code, so someone on Dvorak who reaches
    // for the letter still gets it.
    if (input.type !== 'keyDown') return
    if (input.code !== 'KeyB' && input.key.toLowerCase() !== 'b') return
    // A held-down chord would otherwise flap the sidebar open and shut.
    if (input.isAutoRepeat) return
    // Cmd+B on macOS, Ctrl+B elsewhere, and nothing near it: any other modifier,
    // the other platform's modifier included, means a different chord was meant.
    if (input.shift || input.alt) return
    const chord = process.platform === 'darwin'
      ? input.meta && !input.control
      : input.control && !input.meta
    if (!chord) return
    event.preventDefault()
    // Nothing awaits this listener, so a failed save would otherwise be an
    // unhandled rejection and the chord would look like it simply did nothing.
    toggleSidebar().catch((error) => {
      console.error('workbench: could not toggle the sidebar', error)
    })
  })
}

/**
 * Show one project's board, starting its server if it is not already running.
 *
 * Each board is its own WebContentsView loading the child server's real
 * address. That is what keeps Workbook's same-origin guard satisfied: the Host
 * header names the address the listener bound, and the Origin the board sees is
 * its own. A proxy or an iframe would break one or both.
 */
async function openProject (projectId, taskId = null) {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)

  const url = await supervisor.start(project)
  // The board has a route per task, so a row can open the thing it names
  // rather than the board it lives on.
  const target = taskId ? new URL(`/tasks/${encodeURIComponent(taskId)}`, url).href : url

  let view = boardViews.get(projectId)
  if (!view) {
    view = new WebContentsView({
      webPreferences: {
        // Carries the board's Dark Mode choice to the shell and the other
        // boards, and nothing else; the board's page never sees it.
        preload: path.join(__dirname, '..', 'preload', 'board.js'),
        contextIsolation: true,
        nodeIntegration: false
      }
    })
    // Links out of the board (a repository URL, say) belong in the browser, not
    // in a view that has no chrome to get back from.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      shell.openExternal(target)
      return { action: 'deny' }
    })
    boardViews.set(projectId, view)
    watchSidebarShortcut(view.webContents)
    window.contentView.addChildView(view)
    await view.webContents.loadURL(target)
  } else if (view.webContents.getURL() !== target) {
    // A warm view showing something else — another task, or the board root, or
    // an address from a server that has since restarted on a new port.
    await view.webContents.loadURL(target)
  }

  activeProjectId = projectId
  layout()
  return { url }
}

function showChrome () {
  activeProjectId = null
  layout()
}

function closeProject (projectId) {
  const view = boardViews.get(projectId)
  if (view) {
    window.contentView.removeChildView(view)
    view.webContents.close()
    boardViews.delete(projectId)
  }
  supervisor.stop(projectId)
  if (activeProjectId === projectId) showChrome()
}

// --- IPC -------------------------------------------------------------------

ipcMain.handle('workbook:version', async () => {
  const data = await workbook.version()
  return data
})

ipcMain.handle('registry:list', async () => ({
  projects: registry.projects.map((project) => ({
    ...project,
    ...supervisor.status(project.id)
  })),
  scanRoots: registry.scanRoots
}))

ipcMain.handle('discovery:pickFolder', async () => {
  const result = await dialog.showOpenDialog(window, {
    title: 'Choose a folder to scan for repositories',
    properties: ['openDirectory', 'createDirectory']
  })
  if (result.canceled || result.filePaths.length === 0) return null
  return result.filePaths[0]
})

ipcMain.handle('discovery:scan', async (_event, { root, maxDepth }) => {
  const repositories = await discovery.scan(root, { maxDepth })
  await registry.rememberScanRoot(root)

  const imported = new Map(registry.projects.map((project) => [project.path, project]))
  for (const repository of repositories) {
    repository.imported = imported.has(repository.path)
  }

  // Metadata is gathered after the filesystem walk rather than during it: the
  // walk is fast and the git calls are not, and a scan that reported nothing
  // until every repository had been described would feel broken on a large
  // tree.
  const described = await repoinfo.describeAll(
    repositories.map((repository) => repository.path),
    { onProgress: (progress) => toChrome('discovery:progress', progress) }
  )
  for (const repository of repositories) {
    Object.assign(repository, described.get(repository.path) ?? {})
  }

  return { root, repositories }
})

/**
 * Import the selected repositories.
 *
 * A repository that is already initialized is *adopted*, not bootstrapped: it
 * is registered from the identity it already carries and `setup` is never run.
 * That matters for two reasons. Its key cannot be changed — `setup` with a
 * different one fails with "repository is already initialized with project key"
 * — so re-running it can only either no-op or fail. And `setup` also rewrites
 * the managed agent documentation and the skill directory, which is not
 * something adding a repository to a list should do to a checkout the user
 * already configured by hand.
 *
 * Only a repository with no identity yet is bootstrapped, with
 * `--no-sync`: adding a repository to a list must not push refs to its remote
 * as a side effect. Failures are collected rather than thrown, so one bad
 * repository does not abandon the rest of the batch half-done.
 */
ipcMain.handle('import:apply', async (_event, { selections }) => {
  const results = []
  for (const selection of selections) {
    try {
      let project
      const existing = await discovery.inspectRepository(selection.path)

      if (existing.initialized) {
        project = {
          id: existing.projectId,
          key: existing.key,
          name: selection.name || existing.name,
          path: selection.path,
          importedAt: new Date().toISOString(),
          adopted: true
        }
      } else {
        if (!discovery.isValidKey(selection.key)) {
          throw new Error(`"${selection.key}" is not a valid project key (A-Z, 2-10 characters)`)
        }
        const data = await workbook.setup(selection.path, selection.key)
        project = {
          id: data.projectId,
          key: data.key,
          name: selection.name || path.basename(selection.path),
          path: selection.path,
          importedAt: new Date().toISOString()
        }
      }

      await registry.upsert(project)
      results.push({ ok: true, path: selection.path, project, adopted: Boolean(project.adopted) })
    } catch (error) {
      results.push({ ok: false, path: selection.path, error: error.message })
    }
    toChrome('import:progress', { done: results.length, total: selections.length })
  }
  return { results }
})

ipcMain.handle('update:check', async () => {
  if (!updater) return { skipped: 'not ready' }
  // Not silent: this one was asked for, so "you are up to date" is an answer,
  // not noise.
  return updater.check({ silent: false })
})

ipcMain.handle('update:install', async () => {
  if (!updater) return { skipped: 'not ready' }
  return updater.install()
})

const THEMES = ['system', 'light', 'dark']

ipcMain.handle('theme:get', async () => ({ theme: registry.theme, dark: resolveDark() }))

ipcMain.handle('sidebar:get', async () => ({ collapsed: registry.sidebarCollapsed }))

ipcMain.handle('sidebar:toggle', async () => {
  await toggleSidebar()
  return { collapsed: registry.sidebarCollapsed }
})

// A board's preload asks this before the board's own script runs, so a board
// opened after a choice was made starts in that mode. Synchronous on purpose:
// the page script runs the moment the preload ends, and a promise would land
// after the board had already read its preference.
ipcMain.on('theme:current', (event) => {
  event.returnValue = THEMES.includes(registry.theme) ? registry.theme : 'system'
})

/**
 * A board's Dark Mode switch was clicked: its choice becomes the window's.
 *
 * The board reports its stored preference, in its own terms: 'light', 'dark',
 * or '' for "follow the system", which the shell has always spelled 'system'.
 * The shell repaints itself and every other open board is told to align. The
 * reporting board is not told; it is already there, and a board that is told
 * and finds itself already aligned does nothing, which is what stops the
 * alignment's own click from reporting back into a loop.
 */
ipcMain.on('board:scheme', async (event, { scheme }) => {
  const theme = scheme === '' ? 'system' : scheme
  if (!THEMES.includes(theme) || theme === registry.theme) return
  await registry.setTheme(theme)
  await applyTheme()
  for (const [, view] of boardViews) {
    if (view.webContents !== event.sender) view.webContents.send('board:align', { theme })
  }
})

ipcMain.handle('project:open', async (_event, { projectId, taskId }) =>
  openProject(projectId, taskId ?? null))
ipcMain.handle('project:showChrome', async () => { showChrome() })
ipcMain.handle('project:close', async (_event, { projectId }) => { closeProject(projectId) })

ipcMain.handle('project:forget', async (_event, { projectId }) => {
  // Only Workbench's registry entry is dropped. The repository keeps its
  // refs/workbook/* and its .workbook/config.json: removing a project from a
  // list is not a reason to destroy its task history.
  closeProject(projectId)
  await registry.remove(projectId)
})

// --- lifecycle -------------------------------------------------------------

supervisor.on('exited', ({ projectId, wasRunning }) => {
  if (wasRunning) toChrome('project:exited', { projectId, ...supervisor.status(projectId) })
})

nativeTheme.on('updated', () => {
  if (registry.theme === 'system') applyTheme()
})

// A second copy would start a second server per project and both would write
// the same refs. Git's compare-and-swap keeps that safe, but it is still two of
// everything for no benefit.
if (!app.requestSingleInstanceLock()) {
  app.quit()
} else {
  app.on('second-instance', () => {
    if (window) {
      if (window.isMinimized()) window.restore()
      window.focus()
    }
  })
}

app.whenReady().then(async () => {
  // Before anything else starts a server: clear out any left by a run that did
  // not get to shut down.
  const reaped = supervisor.reapOrphans()
  if (reaped.length > 0) {
    console.log(`workbench: stopped ${reaped.length} board server(s) left by a previous run`)
  }

  await registry.load()

  createWindow()
  updater = setupUpdater({
    // A quiet announcement: the interface decides how to show it, and nothing
    // is put in front of the user until they act on it.
    onAvailable: ({ version }) => toChrome('update:available', { version })
  })
  app.on('activate', () => {
    if (BaseWindow.getAllWindows().length === 0) createWindow()
  })
})

// The platform convention: closing the last window does not quit on macOS, and
// the dock icon brings it back. Quit is how you quit.
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit()
})

// Child servers hold listeners; leaking them would leave ports bound after the
// app is gone.
app.on('before-quit', () => supervisor.stopAll())
process.on('exit', () => supervisor.stopAll())
