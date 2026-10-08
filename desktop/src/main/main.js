'use strict'

const { app, BaseWindow, WebContentsView, ipcMain, dialog, shell, nativeTheme, Menu } = require('electron')
const crypto = require('node:crypto')
const fs = require('node:fs/promises')
const path = require('node:path')

const { Registry } = require('./registry')
const { Supervisor } = require('./supervisor')
const discovery = require('./discovery')
const lifecycle = require('./lifecycle')
const repoinfo = require('./repoinfo')
const gitidentity = require('./gitidentity')
const workbook = require('./workbook')
const nextview = require('./nextview')
const clipath = require('./clipath')
const { TITLE_ROW_HEIGHT, TRAFFIC_LIGHT_POSITION, RAIL_WIDTH, boardBounds } = require('./layout')
const { buildMenuTemplate } = require('./menu')
const { createSidebarCommands } = require('./sidebarcommands')
const { setupUpdater } = require('./updater')

const SIDEBAR_WIDTH = 260
// The collapsed sidebar's width, RAIL_WIDTH, comes from layout.js: it is
// derived from where the traffic lights end, and the renderer draws the rail
// from the same number.
const MIN_WIDTH = 1000
const MIN_HEIGHT = 680

/** @type {BaseWindow|null} */
let window = null
/** @type {WebContentsView|null} */
let chromeView = null
/** Board views, one per project, kept warm once opened. @type {Map<string, WebContentsView>} */
const boardViews = new Map()
let activeProjectId = null
/**
 * How many opens have been asked for. Each openProject() call takes the next
 * number and stands down if a newer one has started; see the comment there.
 */
let openGeneration = 0
/**
 * The PATH install, started at launch and asked about once by `path:notice`.
 * @type {Promise<object>|null}
 */
let cliPathInstall = null

const registry = new Registry(app.getPath('userData'))
const supervisor = new Supervisor(app.getPath('userData'))

function sidebarWidth () {
  return registry.sidebarCollapsed ? RAIL_WIDTH : SIDEBAR_WIDTH
}

function layout () {
  if (!window) return
  const content = window.getContentBounds()
  // The shell's view covers the whole window and draws the sidebar; the board
  // goes beside it, from the window's top edge, where its own header is the
  // part of the window's title row the window is dragged by.
  chromeView?.setBounds({ x: 0, y: 0, width: content.width, height: content.height })
  const bounds = boardBounds(content, sidebarWidth())
  for (const [projectId, view] of boardViews) {
    // Views for projects that are not showing are parked off-screen rather than
    // detached, so switching back does not reload the board or lose its state.
    // Parked views are hidden as well as shrunk: a board's header is a drag
    // region, and Electron's hit test unions every view's drag regions, skipping
    // a view only when it is not visible. A 0x0 view keeps its page laid out at
    // the window's origin, so its header would turn the top of the showing
    // board and of the sidebar into a handle. Visible first, then placed, so the
    // active one is never shown at the parked bounds.
    const active = projectId === activeProjectId
    view.setVisible(active)
    view.setBounds(active ? bounds : { x: 0, y: 0, width: 0, height: 0 })
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
    // Pinned rather than left to the default, so the lights are centered on
    // the title row the sidebar head's wordmark and chevron share, and the
    // head knows where they end.
    ...(isMac && { trafficLightPosition: TRAFFIC_LIGHT_POSITION }),
    ...(isWindows && {
      titleBarOverlay: {
        color: '#00000000',
        symbolColor: dark ? '#e4e9f2' : '#34425a',
        // The controls sit at the right end of the title row, over the
        // board's header, so they are the row's height.
        height: TITLE_ROW_HEIGHT
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

  window.on('resize', layout)

  // The window's views and the project showing in it are module state that
  // outlives the window it describes, and on macOS a closed window is not a
  // quit: `window-all-closed` deliberately does not quit there, and the dock
  // icon brings the app back through `activate`, which calls this function
  // again. A new window that inherited the old one's boardViews would lay out
  // views belonging to a destroyed window — which is what made Cmd+B do nothing
  // in a reopened window, since the layout inside toggleSidebar() threw.
  //
  // `once`, because a window closes once. There is no guard against `window`
  // having moved on to a newer one by the time this runs, because it cannot
  // have: createWindow() has two callers, and the `activate` one only builds a
  // window when none are left, which a window leaves by being destroyed — the
  // very thing that emits this.
  window.once('closed', () => {
    const { dropped, errors } = lifecycle.releaseClosedWindow({ boardViews, chromeView })
    window = null
    chromeView = null
    activeProjectId = null
    // No board is showing any more, so the board items go gray — the same call
    // showChrome() makes for the same reason. On macOS the menu outlives the
    // window, and without this Cmd+N would stay lit over nothing.
    installMenu()
    // Said only when there was something to let go of, the way the reaping log
    // speaks only when it reaped: every ordinary quit closes the window too,
    // and on Windows and Linux that makes this the last line of every session.
    // The board servers are left running on purpose — reopening from the dock
    // is meant to be quick, and quitting is what stops them (see
    // `before-quit`).
    if (dropped.length > 0) {
      console.log(`workbench: window closed; released ${dropped.length} board view(s): ` +
        dropped.join(', '))
    }
    // One line per failure, like the PATH install: a view that would not close
    // has already been let go of, and is still worth seeing.
    for (const error of errors) {
      console.error(`workbench: ${error}`)
    }
  })

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
      height: TITLE_ROW_HEIGHT
    })
  }
  toChrome('theme:changed', { theme: registry.theme, dark })
}

/**
 * Take on the theme a board chose: store it, repaint, align the other boards.
 *
 * Which theme a reported scheme asks for is lifecycle.schemeToTheme's to say;
 * this is only the doing of it. The reporting board is not told, because it is
 * already there — and a board that is told and finds itself already aligned
 * says nothing back, which is what keeps the alignment from reporting itself
 * round the loop again.
 *
 * Stored before anything is painted, like setSidebarCollapsed: a board's
 * switch has already moved in its own page, but the shell and every other board
 * must not follow a choice the registry refused to keep.
 */
async function adoptBoardScheme (theme, sender) {
  await registry.setTheme(theme)
  await applyTheme()
  for (const [, view] of boardViews) {
    if (view.webContents !== sender) view.webContents.send('board:align', { theme })
  }
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

// --- menu ------------------------------------------------------------------

/**
 * Install the application menu for the app's current state.
 *
 * Rebuilt rather than patched whenever the project list or the active board
 * changes: the project items carry names, and the board items are enabled
 * only while a board is showing, so the cheapest correct thing is to build the
 * whole menu again from the state it describes.
 */
function installMenu () {
  const template = buildMenuTemplate({
    platform: process.platform,
    // Numbered in sidebar order, so Cmd+1 is whatever the sidebar shows first.
    projects: registry.orderedProjects,
    activeProjectId,
    nextAvailable: registry.orderedProjects.length >= 2,
    actions: menuActions
  })
  Menu.setApplicationMenu(Menu.buildFromTemplate(template))
}

/** When each rate-limited action last ran. @type {Map<string, number>} */
const lastFired = new Map()

/**
 * Whether enough time has passed since `key` last fired to let it fire again.
 *
 * Holding a menu accelerator down repeats it, and the keystroke listener this
 * menu replaced refused an auto-repeat outright — `input.isAutoRepeat` told it
 * so. A menu click carries no such flag, so the two actions that flap when
 * repeated, the sidebar and the theme, are held to one firing per interval
 * instead. 250ms is longer than a key repeat (about 30ms once it starts) and
 * shorter than two deliberate presses.
 *
 * Only these two. Opening a project or driving a board is idempotent enough to
 * repeat harmlessly, and rate-limiting those would make a quick second press
 * look broken.
 */
function notRepeating (key, ms = 250) {
  const now = Date.now()
  const previous = lastFired.get(key)
  if (previous !== undefined && now - previous < ms) return false
  lastFired.set(key, now)
  return true
}

// What each menu item does. A view or a project is the renderer's state to
// change, so those are named to the shell page and decided there; a board
// action goes to that board's own preload; and the theme toggle goes through
// the same path a board's Dark Mode switch uses, so every board follows.
const menuActions = {
  selectProject: (index) => toChrome('shortcut', { kind: 'project', index }),
  stepProject: (delta) => toChrome('shortcut', { kind: 'step', delta }),
  showNext: () => toChrome('shortcut', { kind: 'view', view: 'next' }),
  showImport: () => toChrome('shortcut', { kind: 'view', view: 'import' }),
  toggleTheme: () => {
    if (!notRepeating('theme')) return
    // No sender to spare: the toggle is the shell's own choice, so every open
    // board is aligned to it. nativeTheme.shouldUseDarkColors is the same
    // system reading resolveDark falls back to for 'system'.
    adoptBoardScheme(lifecycle.toggleTheme(registry.theme, nativeTheme.shouldUseDarkColors), null).catch((error) => {
      console.error('workbench: could not toggle the theme', error)
    })
  },
  toggleSidebar: () => {
    if (!notRepeating('sidebar')) return
    toggleSidebar().catch((error) => { console.error('workbench: could not toggle the sidebar', error) })
  },
  reloadBoard: () => {
    const view = activeProjectId && boardViews.get(activeProjectId)
    if (!view) return
    // The keyboard goes with the board, the way opening one already takes it
    // there. An accelerator fires wherever focus happens to be, and any click
    // in the sidebar leaves it in the shell page; a reload that left it behind
    // would hand the fresh board back to a reader whose next keystroke still
    // went to the page behind it.
    view.webContents.focus()
    view.webContents.reload()
  },
  boardCommand: (command) => {
    const view = activeProjectId && boardViews.get(activeProjectId)
    if (!view) return
    // Focus first, for the same reason and more sharply: these commands open
    // things meant to be typed into. Fired with focus still in the shell page,
    // Cmd+N would open a New Task form nobody could type into and Cmd+F would
    // focus a search box the keystrokes never reached.
    view.webContents.focus()
    view.webContents.send('board:command', { command })
  }
}

/**
 * Start one project's board server and tell the shell how it went.
 *
 * `supervisor.start` already emits `started` on success; the failure branch
 * has no event of its own, so both are announced here as `project:started`
 * with the supervisor's status, and the sidebar repaints its dot from that.
 */
async function startProjectServer (project) {
  try {
    return await supervisor.start(project)
  } finally {
    toChrome('project:started', { projectId: project.id, ...supervisor.status(project.id) })
  }
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

  // Two project shortcuts in quick succession race, and the awaits below are
  // where they overtake each other: Cmd+1 on a cold project spawns a server and
  // waits seconds for it, Cmd+2 on a warm one is back almost at once, so the
  // cold open lands last and sets activeProjectId, the layout and the menu to
  // the project the reader has already moved off. The same nextGeneration
  // pattern the renderer's Next view uses settles it — each open takes a
  // number on the way in and, at every await, leaves the state alone if a newer
  // one has started. The url is still returned: the caller asked for this
  // project's address and that much is true whoever is showing.
  const generation = ++openGeneration

  const url = await supervisor.start(project)
  if (generation !== openGeneration) return { url }
  // The board has a route per task, so a row can open the thing it names
  // rather than the board it lives on.
  const target = taskId ? new URL(`/tasks/${encodeURIComponent(taskId)}`, url).href : url

  // A first open spawns `workbook serve` and waits for the address it bound,
  // which is the one await here long enough for a user to close the window
  // inside it. The window is checked rather than the view being added to
  // nothing: the invoke rejects into a renderer that has gone, which is the
  // right ending, where carrying on would put a view in boardViews that no
  // window holds — precisely the state the `closed` handler exists to prevent,
  // arriving just after it ran.
  if (!window) throw new Error('the window closed while the board was starting')

  let view = boardViews.get(projectId)
  if (!view) {
    view = new WebContentsView({
      webPreferences: {
        // Carries the board's Dark Mode choice to the shell and the other
        // boards, and a shortcut back the other way, and marks the page's root
        // `in-workbench` so the board draws the sidebar's divider under its
        // header; nothing else. The board's page never sees the preload itself.
        //
        // Deliberately not `sandbox: false`, which the shell's own view does
        // take: this is the view that renders text out of a repository, so it
        // is the one that keeps the OS renderer sandbox. board.js is written to
        // live inside it — see the inlined command handler there.
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
    // Into the window first and into the map second, so that a failure to
    // attach leaves nothing behind: an entry in boardViews is a promise that
    // the window holds that view, and the next window would inherit and lay
    // out anything that broke the promise.
    // Hidden until layout() shows it as the active board: a view that is
    // loading or that a later open supersedes must never contribute its
    // header's drag region while parked (see layout()).
    view.setVisible(false)
    window.contentView.addChildView(view)
    boardViews.set(projectId, view)
    await view.webContents.loadURL(target)
    // Superseded while the page loaded. The view stays in boardViews, parked
    // off-screen by the next layout() and warm for the next time this project
    // is asked for; what it must not do is take the window over now.
    if (generation !== openGeneration) return { url }
  } else if (view.webContents.getURL() !== target) {
    // A warm view showing something else — another task, or the board root, or
    // an address from a server that has since restarted on a new port.
    await view.webContents.loadURL(target)
    if (generation !== openGeneration) return { url }
  }

  activeProjectId = projectId
  layout()
  // A board is laid in now, and it draws the sidebar's divider below its own
  // header, so the shell stops drawing its own. Only once the view is in: a
  // switch from one board to another keeps the old one drawn until this point,
  // and nothing in between says false, so the divider never flickers.
  toChrome('board:showing', true)
  // The board items are enabled only while a board is showing, and one is
  // showing now.
  installMenu()
  // Keyboard focus follows the board. Clicking a row in the chrome document
  // leaves focus there, so the board arrives in front of a reader whose next
  // keystroke would have gone to the page behind it.
  view.webContents.focus()
  return { url }
}

function showChrome () {
  activeProjectId = null
  layout()
  // Whatever the shell shows now (a view of its own, the Git identity form,
  // a board that failed to start) has no board beside it to draw the
  // sidebar's divider, so the shell draws it again.
  toChrome('board:showing', false)
  // No board is showing, so the board items go gray.
  installMenu()
}

function closeProject (projectId) {
  const view = boardViews.get(projectId)
  if (view) {
    // Detached only while there is a window to detach it from. This is the one
    // place an IPC handler reaches into the window's contentView, and doing
    // that to a destroyed window throws: a close or a forget arriving as the
    // window goes would fail in the renderer over a view the next line lets go
    // of regardless.
    if (window) window.contentView.removeChildView(view)
    view.webContents.close()
    boardViews.delete(projectId)
  }
  supervisor.stop(projectId)
  if (activeProjectId === projectId) showChrome()
}

// --- the CLI on PATH -------------------------------------------------------

/**
 * Copy the bundled CLI somewhere permanent and put that somewhere on PATH.
 *
 * All of the deciding lives in clipath.js, which never throws: every step's
 * failure lands in `errors` and the rest still runs, so a read-only `.zshrc`
 * does not cost the user the binary copy. This wrapper exists to name the one
 * input the module cannot work out for itself — where the bundle put the
 * binary — and to say out loud what happened, the way the supervisor says what
 * it reaped.
 */
async function installCli () {
  // A development run (`npm start`) has no packaged Resources directory with a
  // `workbook` in it, so there is nothing to copy — which is also what keeps a
  // development run from writing to the developer's own shell profiles.
  if (!process.resourcesPath) {
    return { skipped: true, reason: 'not a packaged app', errors: [] }
  }

  const result = await clipath.install({
    bundled: path.join(process.resourcesPath, workbook.BINARY)
  })
  const errors = result.errors ?? []

  if (result.skipped) {
    console.log(`workbench: did not put workbook on PATH (${result.reason})`)
  } else {
    // `pathChanged`, not `changed`: `changed` is true for a binary copy alone,
    // and a launch that copied the binary but had every profile write fail must
    // not be logged as though PATH were now set up. The three endings below are
    // the three things that can actually have happened.
    const copy = result.copied
      ? `copied workbook to ${result.binary}`
      : `workbook at ${result.binary} was already current`
    let ending
    if (result.pathChanged) {
      ending = `added ${result.directory} to ${(await writtenForLog(result)).join(', ')}`
    } else if (errors.length > 0) {
      ending = `could not add ${result.directory} to PATH`
    } else {
      ending = `${result.directory} was already on PATH`
    }
    console.log(`workbench: ${copy}; ${ending}`)
  }
  // One line per failure, like the reaping log: a profile that could not be
  // written is worth seeing even though it did not stop anything.
  for (const error of errors) {
    console.error(`workbench: could not finish putting workbook on PATH: ${error}`)
  }

  // Arm the notice, on the launch that earned it, for whichever launch's page
  // gets around to asking. The registry refuses to re-arm once the notice has
  // been shown, so an app update that re-copies the binary stays quiet.
  //
  // Only a launch that wrote every target it chose gets to say it. A partial
  // write — `.zshrc` taken and fish's config refused, say — would otherwise
  // get the same sentence about PATH while the shell the user actually types
  // in was the one that was missed, and the notice is not repeatable: it would
  // be wrong once and then silent forever. A launch that tries again and
  // succeeds arms it then, since a target already carrying the block reports no
  // change and only the failed one has anything left to do.
  if (result.pathChanged && errors.length === 0) {
    try {
      await registry.setPendingPathNotice(result.directory)
    } catch (error) {
      // Logged rather than thrown: the PATH work itself succeeded and is worth
      // reporting above, and the only casualty is the sentence about it.
      console.error('workbench: could not record that the PATH notice is owed', error)
    }
  }
  return result
}

/**
 * The files a write actually landed in, named as the log should name them.
 *
 * A profile is very often a symlink into a dotfiles repository — the owner's
 * own `~/.zshrc` and `~/.config/fish/config.fish` both are — and the write
 * follows it, so the block appears as a change inside that repository. Naming
 * the resolved file is how someone reads the log and knows which file to go
 * look at. Best effort by construction: realpath failing, or a path vanishing
 * between the write and this line, falls back to the name we asked for rather
 * than turning a successful install into an error.
 */
async function writtenForLog (result) {
  const written = []
  for (const profile of result.profiles.filter((profile) => profile.changed)) {
    let real = profile.file
    try {
      real = await fs.realpath(profile.file)
    } catch {
      // Keep the name we wrote to.
    }
    written.push(real === profile.file ? profile.file : `${profile.file} (really ${real})`)
  }
  if (result.windows?.changed) written.push('the user PATH in the registry')
  return written
}

// --- IPC -------------------------------------------------------------------

ipcMain.handle('workbook:version', async () => {
  const data = await workbook.version()
  return data
})

/**
 * What, if anything, to tell the user about the CLI being on their PATH.
 *
 * Answered from the registry's pending directory rather than from this
 * launch's install result, which is what makes the notice reliable in both
 * directions. A launch whose `reg add` failed changed no PATH, armed nothing,
 * and says nothing — where reading `changed` would have claimed a profile was
 * edited and then never corrected itself. And a launch that did change PATH but
 * never got as far as answering this leaves the directory armed, so the next
 * launch says it instead of losing it.
 *
 * The renderer asks rather than being pushed to, because the install races the
 * shell page's load: a push can arrive before the page is listening, and a
 * question cannot. The awaited promise is only about *this* launch's arming
 * landing before the question is answered; an older launch's is already stored.
 * Marking it as the answer is handed over, rather than when the dismiss button
 * is clicked, is what makes it once: a user who quits without dismissing it has
 * still been told, and a dismissal must not be the thing that records it.
 */
ipcMain.handle('path:notice', async () => {
  if (registry.pathNoticeShown) return null
  await cliPathInstall
  const directory = registry.pendingPathNotice
  if (!directory) return null
  try {
    await registry.setPathNoticeShown()
  } catch (error) {
    // Not worth rejecting an invoke the page does not guard — boot() would stop
    // where it asked. The directory stays armed, so the next launch says it
    // again rather than never: one repeat beats one silence.
    console.error('workbench: could not record that the PATH notice was shown', error)
  }
  return { directory }
})

/**
 * Every project in sidebar order, each with its board server's status: the
 * list the shell draws and numbers. Sidebar order, not storage order, because
 * the menu numbers projects from the same list and the renderer resolves a
 * Cmd+N or a Previous/Next step against this one: the two have to agree.
 */
function listedProjects () {
  return registry.orderedProjects.map((project) => ({
    ...project,
    ...supervisor.status(project.id)
  }))
}

// The layout travels with the list so the sidebar can draw both from one
// answer, after an import or a forget as much as at boot.
ipcMain.handle('registry:list', async () => ({
  projects: listedProjects(),
  layout: registry.sidebarLayout,
  scanRoots: registry.scanRoots
}))

ipcMain.handle('next:load', async (_event, { limit } = {}) =>
  nextview.loadNext({
    // The board server's status travels with each project, the same way
    // registry:list sends it: what this view shows is only as fresh as the
    // server keeping that project synchronized, so the view has to be able to
    // say which projects have one running.
    projects: listedProjects(),
    limit
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
 * That matters for two reasons. Its founding key cannot be changed — `setup`
 * with a different one fails with "repository is already initialized with
 * project key" — so re-running it can only either no-op or fail. And `setup`
 * also rewrites the managed agent documentation and the skill directory, which
 * is not something adding a repository to a list should do to a checkout the
 * user already configured by hand.
 *
 * Only a repository with no identity yet is bootstrapped, with
 * `--no-sync`: adding a repository to a list must not push refs to its remote
 * as a side effect. Failures are collected rather than thrown, so one bad
 * repository does not abandon the rest of the batch half-done.
 *
 * `project.key` is a label, not an identity. For a bootstrapped repository it
 * is the key `setup` reports, which is the project's current key — the one a
 * new task is minted under. For an adopted repository it is the key in
 * `.workbook/config.json`, which is the project's founding key and stays that
 * whatever the configuration ledger later makes current. A project with
 * several keys therefore shows one of them in the sidebar; refreshing it from
 * the project's configuration is a follow-up, and nothing here depends on it.
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
      // Not awaited: an import that binds slowly must not hold the wizard's
      // progress, and startProjectServer already announces the outcome either
      // way via `project:started`.
      startProjectServer(project).catch(() => {})
      results.push({ ok: true, path: selection.path, project, adopted: Boolean(project.adopted) })
    } catch (error) {
      results.push({ ok: false, path: selection.path, error: error.message })
    }
    toChrome('import:progress', { done: results.length, total: selections.length })
  }
  // The project list just grew: its menu items carry the names.
  installMenu()
  return { results }
})

ipcMain.handle('theme:get', async () => ({ theme: registry.theme, dark: resolveDark() }))

ipcMain.handle('sidebar:get', async () => ({ collapsed: registry.sidebarCollapsed }))

ipcMain.handle('sidebar:toggle', async () => {
  await toggleSidebar()
  return { collapsed: registry.sidebarCollapsed }
})

// The sidebar's order and categories. Each edit is validated, applied, saved
// and announced by sidebarcommands.js, one at a time; the menu is rebuilt after
// every one because its Cmd+N items follow the sidebar's order.
const sidebarCommands = createSidebarCommands({
  registry,
  mintId: () => crypto.randomUUID(),
  listProjects: listedProjects,
  rebuildMenu: () => installMenu(),
  broadcast: (payload) => toChrome('sidebar:layoutChanged', payload)
})
ipcMain.handle('sidebar:layout', async () => sidebarCommands.layout())
ipcMain.handle('sidebar:moveProject', (_event, args) => sidebarCommands.moveProject(args))
ipcMain.handle('sidebar:moveCategory', (_event, args) => sidebarCommands.moveCategory(args))
ipcMain.handle('sidebar:createCategory', (_event, args) => sidebarCommands.createCategory(args))
ipcMain.handle('sidebar:renameCategory', (_event, args) => sidebarCommands.renameCategory(args))
ipcMain.handle('sidebar:setCategoryCollapsed', (_event, args) => sidebarCommands.setCategoryCollapsed(args))
ipcMain.handle('sidebar:deleteCategory', (_event, args) => sidebarCommands.deleteCategory(args))

// A board's preload asks this before the board's own script runs, so a board
// opened after a choice was made starts in that mode. Synchronous on purpose:
// the page script runs the moment the preload ends, and a promise would land
// after the board had already read its preference.
ipcMain.on('theme:current', (event) => {
  event.returnValue = lifecycle.THEMES.includes(registry.theme) ? registry.theme : 'system'
})

/**
 * A board's Dark Mode switch was clicked: its choice becomes the window's.
 *
 * The board reports its stored preference, in its own terms, and what that asks
 * for is lifecycle.schemeToTheme's to answer, and the doing of it is
 * adoptBoardScheme's. Synchronous over an async function, the way the sidebar
 * chord is, for the reason in the body.
 */
ipcMain.on('board:scheme', (event, payload) => {
  // Read rather than destructured: a message with no payload would throw
  // inside Electron's own dispatch, where nothing catches it.
  const theme = lifecycle.schemeToTheme(payload?.scheme, registry.theme)
  if (!theme) return
  // Nothing awaits an ipcMain.on listener, so a save that rejects — a full
  // disk, an unwritable userData directory — would otherwise be an unhandled
  // rejection in the main process, and the switch the user just clicked would
  // look like it simply did nothing.
  adoptBoardScheme(theme, event.sender).catch((error) => {
    console.error('workbench: could not take on the theme a board chose', error)
  })
})

ipcMain.handle('project:open', async (_event, { projectId, taskId }) =>
  openProject(projectId, taskId ?? null))
ipcMain.handle('project:showChrome', async () => { showChrome() })

/**
 * The Git identity a project's board would record changes against, and which
 * half of it Git cannot supply here.
 *
 * Asked by the shell before it starts a board that is not already running:
 * `workbook serve` refuses to start without a user.email, and the shell would
 * rather ask for one than show Git's exit status. Whether a name is wanted too
 * depends on the machine, so the answer carries `needs` and the form asks for
 * exactly that.
 */
ipcMain.handle('identity:get', async (_event, { projectId }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)
  return gitidentity.read(project.path)
})

// Written with `git config`, so the identity is Git's, not Workbench's: the
// user's own commits carry it too, and a terminal in the same checkout agrees.
ipcMain.handle('identity:set', async (_event, { projectId, name, email, scope }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)
  return gitidentity.write(project.path, { name, email, scope })
})
ipcMain.handle('project:close', async (_event, { projectId }) => { closeProject(projectId) })

ipcMain.handle('project:forget', async (_event, { projectId }) => {
  // Only Workbench's registry entry is dropped. The repository keeps its
  // refs/workbook/* and its .workbook/config.json: removing a project from a
  // list is not a reason to destroy its task history.
  closeProject(projectId)
  await registry.remove(projectId)
  // And shrank: the numbered items move up, and the last one goes away.
  installMenu()
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
const isPrimaryInstance = app.requestSingleInstanceLock()
if (!isPrimaryInstance) {
  app.quit()
} else {
  app.on('second-instance', () => {
    // Letting go of the window on close makes "running with no window" a real
    // state, and this deliberately does not answer it by building one. This
    // handler is armed from module scope, before `ready`, so it would have to
    // ask whether the app is up — and even then it races the startup's own
    // createWindow(): a copy arriving while registry.load() is awaited finds
    // the app ready and no window, both build one, and the orphan's `closed`
    // handler would later let go of the surviving window's views. Nothing is
    // lost by declining. Closing the window quits the app everywhere but
    // macOS, and on macOS the way back is the dock icon, which fires
    // `activate` and builds a window there.
    if (!window) return
    if (window.isMinimized()) window.restore()
    window.focus()
  })
}

app.whenReady().then(async () => {
  // A second copy of the app has already called quit() above, but quit() is not
  // instant: 'ready' can fire first, and everything below it would then run in
  // a process that is on its way out. Both of the things it starts with reach
  // outside this process — reapOrphans() would kill the *winning* instance's
  // board servers, and the PATH install would copy the binary and rewrite the
  // user's shell profiles alongside the winner doing the same, with two
  // setPathNoticeShown() writes racing for one registry file. Nothing here is
  // the losing instance's business.
  if (!isPrimaryInstance) return

  // Before anything else starts a server: clear out any left by a run that did
  // not get to shut down.
  const reaped = supervisor.reapOrphans()
  if (reaped.length > 0) {
    console.log(`workbench: stopped ${reaped.length} board server(s) left by a previous run`)
  }

  await registry.load()
  // After the registry, because the menu names the projects in it, and before
  // the window, so the first frame is drawn under the menu it will keep.
  installMenu()

  // Started here and deliberately not awaited: copying a binary and rewriting
  // shell profiles is filesystem work that has nothing to do with drawing the
  // window, and a slow or busy disk must not hold the window shut. The promise
  // is kept so `path:notice` can ask for an answer that may not have arrived by
  // the time the page boots. It runs after registry.load() because the notice's
  // "said it once" flag comes from the loaded registry, and before
  // createWindow() so the work is already under way when the page asks.
  cliPathInstall = installCli().catch((error) => {
    // clipath.install() collects its own failures and does not throw, so this
    // is the module itself failing to run at all.
    console.error('workbench: could not put workbook on PATH', error)
    return { skipped: true, reason: error.message, errors: [] }
  })

  createWindow()
  // Every imported project's server starts now, not on first click: using the
  // app is meant to replace running `workbook serve` in each repository by
  // hand, and each server carries the sync loop that keeps its project
  // current. Not awaited — the window is up and the sidebar's status dots
  // report each start as it settles.
  lifecycle.startEveryProject({
    projects: registry.projects,
    start: startProjectServer,
    log: (line) => console.error(line)
  })
  // The launch check still runs, but the shell page has nowhere to show what it
  // finds and no action to offer: when an update action returns it belongs in
  // the native application menu, which reaches a board's view as well as this
  // page. Until then the finding goes to the console, and the updater itself is
  // set up for that side effect alone — nothing can drive it, so nothing holds
  // on to what it returns.
  setupUpdater({
    onAvailable: ({ version }) => {
      console.log(`workbench: update available: v${version} ` +
        '(no update action yet; it will live in the application menu)')
    }
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
