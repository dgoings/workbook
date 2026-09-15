'use strict'

const { app, BaseWindow, WebContentsView, ipcMain, dialog, shell, nativeTheme } = require('electron')
const fs = require('node:fs')
const path = require('node:path')

const { Registry } = require('./registry')
const { Supervisor } = require('./supervisor')
const { darkPrimaryRamp, DEFAULT_PRIMARY } = require('./boardtheme')
const discovery = require('./discovery')
const repoinfo = require('./repoinfo')
const people = require('./people')
const workbook = require('./workbook')
const { setupUpdater } = require('./updater')

const SIDEBAR_WIDTH = 260
const MIN_WIDTH = 1000
const MIN_HEIGHT = 680

/** @type {BaseWindow|null} */
let window = null
/** @type {WebContentsView|null} */
let chromeView = null
/** Board views, one per project, kept warm once opened. @type {Map<string, WebContentsView>} */
const boardViews = new Map()
let activeProjectId = null

// The dark overlay for the boards. Read once: it is injected into and removed
// from every board view as the theme changes, and re-reading it per view would
// only add a filesystem round trip to a theme switch.
const BOARD_DARK_CSS = fs.readFileSync(
  path.join(__dirname, '..', 'renderer', 'board-dark.css'), 'utf8'
)

/** Keys returned by insertCSS, so the overlay can be removed again. */
const boardDarkKeys = new Map()

const registry = new Registry(app.getPath('userData'))
const supervisor = new Supervisor(app.getPath('userData'))

/** @type {{check: (options?: {silent?: boolean}) => Promise<object>}|null} */
let updater = null

function boardBounds () {
  const { width, height } = window.getContentBounds()
  return { x: SIDEBAR_WIDTH, y: 0, width: Math.max(0, width - SIDEBAR_WIDTH), height }
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
 * 'system' defers to the OS, which is why nativeTheme is consulted rather than
 * remembered: the OS can flip while the app runs, and a remembered answer would
 * leave the shell and the boards disagreeing with each other.
 */
function resolveDark () {
  const choice = registry.theme
  if (choice === 'dark') return true
  if (choice === 'light') return false
  return nativeTheme.shouldUseDarkColors
}

/**
 * Put one board view in the current mode.
 *
 * The overlay is inserted and removed rather than toggled by a class, because
 * the board's own document is not ours to add classes to: it is re-rendered by
 * its own client on every poll, and anything written into it would be lost.
 * Injected CSS survives that, and survives a reload.
 */
async function applyThemeToBoard (projectId, view) {
  const dark = resolveDark()
  const existing = boardDarkKeys.get(projectId)

  if (dark && !existing) {
    try {
      // Read the colour this project chose before overriding anything, so the
      // derived ramp is built from the board's own accent rather than replacing
      // it. A board that never set one reports Workbook's default.
      let primary = DEFAULT_PRIMARY
      try {
        primary = await view.webContents.executeJavaScript(
          `getComputedStyle(document.documentElement).getPropertyValue('--wb-primary').trim()`
        ) || DEFAULT_PRIMARY
      } catch {
        // A board that cannot be queried yet gets the default ramp; the
        // did-finish-load pass re-derives it against the loaded document.
      }
      const css = `${BOARD_DARK_CSS}\n${darkPrimaryRamp(primary)}`
      boardDarkKeys.set(projectId, await view.webContents.insertCSS(css))
    } catch (error) {
      // A view still loading gets the overlay from did-finish-load instead, so
      // this is recoverable — but it is reported rather than swallowed, because
      // a silent failure here looks exactly like a board that ignored the theme.
      console.warn(`workbench: could not darken board ${projectId}: ${error.message}`)
    }
  } else if (!dark && existing) {
    try {
      await view.webContents.removeInsertedCSS(existing)
    } catch (error) {
      // The view reloaded and dropped it already, which is the outcome we want.
      console.warn(`workbench: could not undarken board ${projectId}: ${error.message}`)
    }
    boardDarkKeys.delete(projectId)
  }
}

/** Put the whole app in the current mode: the window, the shell, the boards. */
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
  await Promise.all(
    [...boardViews].map(([projectId, view]) => applyThemeToBoard(projectId, view))
  )
}

// A board reloads on navigation and on a server restart, and injected CSS is
// dropped when it does. Re-inserting on load is what keeps a board dark across
// its own lifecycle rather than only at the moment it was opened.
function watchBoardReloads (projectId, view) {
  view.webContents.on('did-finish-load', () => {
    boardDarkKeys.delete(projectId)
    applyThemeToBoard(projectId, view)
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
      webPreferences: { contextIsolation: true, nodeIntegration: false }
    })
    // Links out of the board (a repository URL, say) belong in the browser, not
    // in a view that has no chrome to get back from.
    view.webContents.setWindowOpenHandler(({ url: target }) => {
      shell.openExternal(target)
      return { action: 'deny' }
    })
    boardViews.set(projectId, view)
    watchBoardReloads(projectId, view)
    window.contentView.addChildView(view)
    await view.webContents.loadURL(target)
    await applyThemeToBoard(projectId, view)
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
    boardDarkKeys.delete(projectId)
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
      invalidateDirectory()
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

ipcMain.handle('theme:get', async () => ({ theme: registry.theme, dark: resolveDark() }))

ipcMain.handle('theme:set', async (_event, { theme }) => {
  if (!['system', 'light', 'dark'].includes(theme)) throw new Error(`unknown theme: ${theme}`)
  await registry.setTheme(theme)
  await applyTheme()
  return { theme, dark: resolveDark() }
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
  invalidateDirectory()
})

/**
 * The merged queue: every project's tasks in one ranked list.
 *
 * This is the read Workbook has no single command for, because `list` is bound
 * to the repository at the working directory. Running it once per repository
 * and merging is the whole trick, and distinct project keys are what make the
 * merged rows tell you where each task lives.
 */
ipcMain.handle('queue:load', async () => {
  const projects = registry.projects
  const settled = await Promise.all(projects.map(async (project) => {
    try {
      // The vocabulary comes along on the first load of each project and is
      // cached after: a status means something by its tags — `next` is what
      // `next` picks from, `done` is what satisfies a dependency — and a name
      // alone cannot say which, since a project may rename its own columns.
      const [tasks] = await Promise.all([
        workbook.listTasks(project.path),
        cachedStatuses(project).catch(() => null)
      ])
      return { project, tasks, error: null }
    } catch (error) {
      return { project, tasks: [], error: error.message }
    }
  }))

  const tasks = []
  const failures = []
  for (const entry of settled) {
    if (entry.error) {
      failures.push({ project: entry.project.name, error: entry.error })
      continue
    }

    // Every task in the project, including the done ones, so a dependency can
    // be resolved to a title and a status. The done ones are then dropped from
    // the queue itself — they are context for what blocks, not work to show.
    const byId = new Map(entry.tasks.map((task) => [task.id, task]))

    // And the reverse: who is waiting on each task. A task's own dependencies
    // say why it cannot start; this says what starts when it finishes, which is
    // the half that decides what to pick up first.
    const blocking = new Map()
    for (const task of entry.tasks) {
      if (task.deleted) continue
      for (const dependencyId of task.dependencies ?? []) {
        if (!blocking.has(dependencyId)) blocking.set(dependencyId, [])
        blocking.get(dependencyId).push({ id: task.id, title: task.title, status: task.status })
      }
    }

    for (const task of entry.tasks) {
      if (task.status === 'done' || task.deleted) continue

      // What is actually holding this task up. Workbook's own `next` considers
      // a task eligible when every dependency sits in a status tagged done, so
      // an unfinished dependency is the difference between "queued" and
      // "cannot be started" — which is worth saying on the row rather than
      // leaving to whoever opens the board.
      const blockedBy = []
      for (const dependencyId of task.dependencies ?? []) {
        const dependency = byId.get(dependencyId)
        if (!dependency) {
          // A dependency in another project, or one since deleted: it cannot be
          // resolved here, and claiming it is satisfied would be a guess.
          blockedBy.push({ id: dependencyId, title: null, status: 'unknown' })
          continue
        }
        if (dependency.status !== 'done') {
          blockedBy.push({
            id: dependencyId, title: dependency.title, status: dependency.status
          })
        }
      }
      tasks.push({
        id: task.id,
        title: task.title,
        status: task.status,
        priority: task.priority,
        labels: task.labels ?? [],
        updatedAt: task.updatedAt,
        dependencies: (task.dependencies ?? []).length,
        blockedBy,
        blocked: blockedBy.length > 0,
        // Only the unfinished waiters count: a done task is not waiting.
        blocks: (blocking.get(task.id) ?? []).filter((waiter) => waiter.status !== 'done'),
        // Workbook records an assignment as an email address; the principal is
        // the person it names, the creator the person who recorded it.
        assignees: (task.assignments ?? []).map((assignment) => assignment.principal),
        statusTags: statusCache.get(entry.project.id)?.byName.get(task.status)?.tags ?? [],
        statusOrder: statusCache.get(entry.project.id)?.byName.get(task.status)?.order ?? 0,
        projectId: entry.project.id,
        projectName: entry.project.name,
        projectKey: entry.project.key
      })
    }
  }

  const order = { high: 0, medium: 1, low: 2 }
  tasks.sort((a, b) =>
    (order[a.priority] ?? 3) - (order[b.priority] ?? 3) ||
    String(b.updatedAt).localeCompare(String(a.updatedAt))
  )

  // "Mine" has to mean every address that is me, not just the one configured:
  // a task assigned from a repository whose user.email differs is still mine.
  const directory = await peopleDirectory()
  const me = registry.defaultAssignee
    ? people.findPerson(directory, registry.defaultAssignee)
    : null
  const myEmails = me ? me.emails : (registry.defaultAssignee ? [registry.defaultAssignee] : [])

  for (const task of tasks) {
    task.mine = task.assignees.some((email) => myEmails.includes(email.toLowerCase()))
  }
  return { tasks, failures, myEmails }
})

/**
 * Assign a task, asking about a collision rather than deciding one.
 *
 * Workbook refuses with exit 10 when somebody else already holds the task, and
 * that refusal is deliberate: whether a second person should hold it too is a
 * question about people, not about software. So it is put to the user, and
 * --force is only ever sent because they said yes.
 *
 * The command runs in the project's own checkout, which is also what decides
 * the creator recorded against the assignment — the repository's user.email,
 * not Workbench's idea of who you are.
 */
// A project's statuses, read the first time a picker needs them.
//
// Cached because a vocabulary is edited rarely and read often, and lazily
// because most sessions never open a status picker at all — reading twelve
// projects' statuses on every queue load would undo the work that got the queue
// down to one subprocess per project.
const statusCache = new Map()

async function cachedStatuses (project, refresh = false) {
  if (!refresh && statusCache.has(project.id)) return statusCache.get(project.id)
  const vocabulary = await workbook.listStatuses(project.path)
  // Indexed by name as well, so a task can find its own status without a scan.
  vocabulary.byName = new Map(vocabulary.statuses.map((status) => [status.status, status]))
  statusCache.set(project.id, vocabulary)
  return vocabulary
}

ipcMain.handle('project:statuses', async (_event, { projectId, refresh }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)
  const vocabulary = await cachedStatuses(project, refresh)
  // byName is a Map and does not survive the IPC boundary; the array does.
  return { default: vocabulary.default, statuses: vocabulary.statuses }
})

/**
 * Move a task to a status.
 *
 * Workbook refuses a status the project does not define, which is the whole
 * validation this needs: the picker offers that project's own vocabulary, so a
 * refusal here means the vocabulary changed underneath it and the cache is
 * stale — worth reporting rather than retrying.
 */
ipcMain.handle('task:status', async (_event, { projectId, taskId, status }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)
  await workbook.setStatus(project.path, taskId, status)
  return { ok: true }
})

ipcMain.handle('task:assign', async (_event, { projectId, taskId, email }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)

  const first = await workbook.assign(project.path, taskId, email)
  if (first.ok) return { ok: true, forced: false }

  const { response } = await dialog.showMessageBox(window, {
    type: 'question',
    title: 'Already assigned',
    message: 'This task is already assigned to someone else.',
    detail: `${first.message}\n\nAssignments are additive — recording this one leaves theirs in place.`,
    buttons: ['Assign anyway', 'Cancel'],
    defaultId: 1,
    cancelId: 1
  })
  if (response !== 0) return { ok: false, cancelled: true }

  await workbook.assign(project.path, taskId, email, { force: true })
  return { ok: true, forced: true }
})

/**
 * Remove an assignment.
 *
 * Workbook allows this only for the person the assignment names or the person
 * who recorded it. That refusal is a rule about who may act, so it is reported
 * rather than retried with a flag — there is no flag.
 */
ipcMain.handle('task:unassign', async (_event, { projectId, taskId, email }) => {
  const project = registry.find(projectId)
  if (!project) throw new Error(`unknown project: ${projectId}`)
  await workbook.unassign(project.path, taskId, email)
  return { ok: true }
})

/**
 * The people directory, derived from the imported repositories' commit history.
 *
 * Derived rather than stored: the history is the source of truth and it moves.
 * Only the user's corrections to it — which addresses are one person, and what
 * to call them — live in the registry.
 *
 * Cached, because it is not cheap and it barely changes. Building it runs
 * several git commands per repository, and the queue needs it on every load
 * just to know which addresses are the current user's: without a cache, opening
 * the queue over twelve projects cost eighty-four subprocesses to answer a
 * question whose answer had not moved since the last time it was asked.
 *
 * The key is the set of projects plus the user's mapping, so importing,
 * forgetting, merging or renaming rebuilds it and nothing else does. A refresh
 * is available for the case the key cannot see: new commits.
 */
let directoryCache = { key: null, people: null }

function directoryKey () {
  return JSON.stringify([
    registry.projects.map((project) => `${project.id}:${project.path}`).sort(),
    registry.peopleMapping
  ])
}

function invalidateDirectory () {
  directoryCache = { key: null, people: null }
}

async function peopleDirectory ({ refresh = false } = {}) {
  const key = directoryKey()
  if (!refresh && directoryCache.key === key && directoryCache.people) {
    return directoryCache.people
  }

  const projects = registry.projects
  const described = await repoinfo.describeAll(projects.map((project) => project.path))
  const directory = people.buildDirectory(
    projects.map((project) => ({
      path: project.path,
      name: project.name,
      allAuthors: described.get(project.path)?.allAuthors ?? []
    })),
    registry.peopleMapping
  )

  // The per-repository identity is read on the same pass, so the People view
  // does not have to run all of this a second time to report a mismatch.
  const configured = projects.map((project) => ({
    project: project.name,
    key: project.key,
    email: described.get(project.path)?.configuredEmail ?? null,
    name: described.get(project.path)?.configuredName ?? null
  }))

  directoryCache = { key, people: directory, configured }
  return directory
}

ipcMain.handle('people:list', async (_event, options = {}) => {
  const directory = await peopleDirectory({ refresh: Boolean(options.refresh) })
  const configured = directoryCache.configured ?? []

  const defaultAssignee = registry.defaultAssignee
  const mismatched = defaultAssignee
    ? configured.filter((entry) => entry.email && entry.email.toLowerCase() !== defaultAssignee)
    : []

  return { people: directory, defaultAssignee, configured, mismatched }
})

ipcMain.handle('people:setDefault', async (_event, { email }) => {
  await registry.setDefaultAssignee(email)
  return { defaultAssignee: registry.defaultAssignee }
})

ipcMain.handle('people:merge', async (_event, { emails }) => {
  await registry.mergePeople(emails)
  invalidateDirectory()
})

ipcMain.handle('people:split', async (_event, { email }) => {
  await registry.splitPerson(email)
  invalidateDirectory()
})

ipcMain.handle('people:rename', async (_event, { id, displayName }) => {
  await registry.renamePerson(id, displayName)
  invalidateDirectory()
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

  // Seed the default assignee from git's own identity. `--assign self` already
  // records this address, so adopting it makes "mine" correct before the user
  // has configured anything; leaving it null would make the filter silently
  // match nothing.
  if (!registry.defaultAssignee) {
    try {
      const identity = await repoinfo.globalGitEmail()
      if (identity) await registry.setDefaultAssignee(identity)
    } catch (error) {
      // A convenience, not a prerequisite. Failing here once took the whole
      // window with it, because this runs before createWindow in the same
      // promise chain.
      console.warn(`workbench: could not read a default identity: ${error.message}`)
    }
  }

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
