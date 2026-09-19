'use strict'

const api = window.workbench

const state = {
  view: 'import',
  projects: [],
  activeProjectId: null,
  scan: { root: null, repositories: [] },
  // Search text and the New/Imported filter are view state, not scan state:
  // they survive a rescan, so refining a search does not lose the filter.
  query: '',
  filter: 'all',
  // Selections are keyed by path so they survive re-rendering under a filter —
  // a repository checked and then filtered out must still import.
  selected: new Set(),
  keys: new Map()
}

const el = (id) => document.getElementById(id)

// --- view switching --------------------------------------------------------

function setView (view, projectId = null) {
  state.view = view
  state.activeProjectId = projectId

  for (const name of ['import', 'project']) {
    el(`view-${name}`).hidden = name !== view
  }
  for (const button of document.querySelectorAll('.rail-item')) {
    button.classList.toggle('active', button.dataset.view === view)
  }
  for (const item of document.querySelectorAll('.project-item')) {
    item.classList.toggle('active', item.dataset.projectId === projectId)
  }

  // The board is a separate top-level view owned by the main process. Any view
  // that is not a project must hide it, or it would cover this document.
  if (view !== 'project') api.showChrome()
}

// --- projects --------------------------------------------------------------

async function loadProjects () {
  const { projects } = await api.listProjects()
  state.projects = projects
  renderProjects()
}

function renderProjects () {
  const list = el('project-list')
  list.innerHTML = ''

  if (state.projects.length === 0) {
    const empty = document.createElement('li')
    empty.className = 'empty'
    empty.style.padding = '4px 9px'
    empty.textContent = 'None yet.'
    list.append(empty)
    return
  }

  for (const project of state.projects) {
    const item = document.createElement('li')
    item.className = 'project-item'
    item.dataset.projectId = project.id
    if (project.id === state.activeProjectId) item.classList.add('active')
    // In the rail the tile is the key and nothing else, so the whole of what
    // the row says when expanded has to be reachable by hovering it. The status
    // is part of that and goes on the tile too: a title of its own on the dot
    // would win the hover over the dot and show the status alone, hiding the
    // name and path exactly where the pointer is most likely to land.
    const status = project.status ?? 'stopped'
    const health = project.error ? `${status} — ${project.error}` : status
    item.title = `${project.name}\n${project.path}\n${health}`

    const dot = document.createElement('span')
    dot.className = `dot ${status}`

    const key = document.createElement('span')
    key.className = 'project-key'
    key.textContent = project.key

    const name = document.createElement('span')
    name.className = 'project-name'
    name.textContent = project.name

    item.append(dot, key, name)
    item.addEventListener('click', () => openProject(project.id))
    list.append(item)
  }
}

async function openProject (projectId) {
  setView('project', projectId)
  const project = state.projects.find((candidate) => candidate.id === projectId)
  el('board-state').textContent = `Starting the board for ${project?.name ?? projectId}…`
  try {
    await api.openProject(projectId)
    el('board-state').textContent = ''
  } catch (error) {
    el('board-state').textContent = `Could not start this board: ${error.message}`
  }
  loadProjects()
}

// --- import wizard ---------------------------------------------------------

/**
 * Does one repository match the current search?
 *
 * Everything on the row is searchable — name, path, stacks, authors, key,
 * branch — because a user looking for "the Go one" and a user looking for
 * "the one Dylan works on" are both looking at this list.
 */
function matchesQuery (repository, query) {
  if (!query) return true
  const haystack = [
    repository.name,
    repository.relativePath,
    repository.key,
    repository.suggestedKey,
    repository.branch,
    ...(repository.stacks ?? []),
    ...(repository.authors ?? []).map((author) => author.name)
  ].filter(Boolean).join(' ').toLowerCase()
  return query.toLowerCase().split(/\s+/).filter(Boolean)
    .every((term) => haystack.includes(term))
}

function visibleRepositories () {
  return state.scan.repositories.filter((repository) => {
    if (state.filter === 'new' && repository.imported) return false
    if (state.filter === 'imported' && !repository.imported) return false
    return matchesQuery(repository, state.query)
  })
}

function chip (text, className = 'label') {
  const node = document.createElement('span')
  node.className = className
  node.textContent = text
  return node
}

function renderScan () {
  const container = el('scan-results')
  container.innerHTML = ''

  const all = state.scan.repositories
  const visible = visibleRepositories()

  el('filter-bar').hidden = all.length === 0
  el('key-note').hidden = all.length === 0 || keyNoteDismissed()
  el('import-actions').hidden = all.length === 0
  el('rescan').disabled = !state.scan.root

  el('result-count').textContent = all.length === 0
    ? ''
    : `${visible.length}/${all.length}`

  if (state.scan.root && all.length === 0) {
    container.innerHTML = '<div class="empty">No Git repositories found under that folder.</div>'
    return
  }
  if (all.length > 0 && visible.length === 0) {
    container.innerHTML = '<div class="empty">Nothing matches that search.</div>'
    updateSelectionStatus()
    return
  }

  for (const repository of visible) {
    const row = document.createElement('div')
    row.className = 'repo'
    const alreadyImported = repository.imported

    const check = document.createElement('input')
    check.type = 'checkbox'
    check.disabled = alreadyImported
    check.checked = state.selected.has(repository.path)
    check.addEventListener('change', () => {
      if (check.checked) state.selected.add(repository.path)
      else state.selected.delete(repository.path)
      updateSelectionStatus()
    })

    const label = document.createElement('div')
    const name = document.createElement('div')
    name.className = 'repo-name'
    name.textContent = repository.name
    const location = document.createElement('div')
    location.className = 'repo-path'
    location.textContent = repository.relativePath
    label.append(name, location)

    const meta = document.createElement('div')
    meta.className = 'repo-meta'
    for (const stack of repository.stacks ?? []) meta.append(chip(stack))
    if (repository.lastCommitRelative) {
      meta.append(chip(repository.lastCommitRelative, 'repo-fact'))
    } else if (repository.empty) {
      meta.append(chip('no commits', 'repo-fact'))
    }
    const [author] = repository.authors ?? []
    if (author) meta.append(chip(author.name, 'repo-fact'))
    if (repository.branch && repository.branch !== 'main' && repository.branch !== 'master') {
      meta.append(chip(repository.branch, 'repo-fact'))
    }
    if (meta.childElementCount > 0) label.append(meta)

    const key = document.createElement('input')
    key.type = 'text'
    key.value = state.keys.get(repository.path) ?? repository.suggestedKey ?? ''
    key.maxLength = 10
    // A repository that already carries an identity has a key that cannot be
    // changed: `setup` with a different one is refused outright. Showing it as
    // editable would only offer an import that fails.
    key.disabled = alreadyImported || repository.initialized
    if (repository.initialized) key.title = 'Already minted — a project key cannot be changed'
    key.addEventListener('input', () => {
      key.value = key.value.toUpperCase()
      key.classList.toggle('invalid', !/^[A-Z][A-Z0-9]{1,9}$/.test(key.value))
      state.keys.set(repository.path, key.value)
    })

    const note = document.createElement('span')
    note.className = 'label'
    if (alreadyImported) {
      note.textContent = 'imported'
      row.classList.add('done')
    } else if (repository.initialized) {
      note.textContent = 'adopt'
      note.title = `Already set up as ${repository.key}; it will be added without re-running setup`
    } else if (repository.partiallyInitialized) {
      note.textContent = 'partial setup'
    } else {
      note.textContent = 'new'
    }

    row.append(check, label, key, note)
    container.append(row)
  }
  updateSelectionStatus()
}

function updateSelectionStatus () {
  const count = state.selected.size
  el('do-import').disabled = count === 0
  el('do-import').textContent = count === 0 ? 'Import selected' : `Import ${count}`
}

async function pickFolder () {
  const root = await api.pickFolder()
  if (!root) return
  await runScan(root)
}

async function runScan (root) {
  el('scan-root').textContent = `Scanning ${root}…`
  el('pick-folder').disabled = true
  el('rescan').disabled = true
  try {
    const depth = Number(el('scan-depth').value) || 4
    const result = await api.scan(root, depth)
    state.scan = result
    // A rescan re-reads identities from disk, so selections for repositories
    // that have since been imported are dropped rather than re-attempted.
    for (const repository of result.repositories) {
      if (repository.imported) state.selected.delete(repository.path)
    }
    el('scan-root').textContent = `${result.repositories.length} repositories under ${root}`
    renderScan()
  } catch (error) {
    el('scan-root').textContent = `Scan failed: ${error.message}`
  } finally {
    el('pick-folder').disabled = false
    el('rescan').disabled = !state.scan.root
  }
}

async function doImport () {
  const selections = []
  for (const repository of state.scan.repositories) {
    if (!state.selected.has(repository.path)) continue
    selections.push({
      path: repository.path,
      key: state.keys.get(repository.path) ?? repository.suggestedKey,
      name: repository.name
    })
  }

  if (selections.length === 0) {
    el('import-status').textContent = 'Nothing selected.'
    return
  }

  el('do-import').disabled = true
  el('import-status').textContent = `Importing 0/${selections.length}…`

  const { results } = await api.importRepositories(selections)
  const failed = results.filter((result) => !result.ok)
  const adopted = results.filter((result) => result.ok && result.adopted).length
  const added = results.length - failed.length

  const detail = adopted > 0 ? ` (${adopted} adopted, ${added - adopted} bootstrapped)` : ''
  el('import-status').textContent = failed.length === 0
    ? `Imported ${added}${detail}.`
    : `Imported ${added}${detail}, failed ${failed.length}: ` +
      failed.map((result) => `${result.path.split('/').pop()} — ${result.error}`).join('; ')

  for (const result of results) {
    if (result.ok) state.selected.delete(result.path)
  }

  await loadProjects()
  if (state.scan.root) await runScan(state.scan.root)
}

// --- the project-key caution ------------------------------------------------

// Dismissal is a per-viewer convenience, so it lives in localStorage rather
// than in the registry: it is not state anyone else needs, and losing it only
// costs one reading of a three-line notice.
const KEY_NOTE_STORAGE = 'workbench.keyNoteDismissed'

function keyNoteDismissed () {
  try {
    return localStorage.getItem(KEY_NOTE_STORAGE) === 'true'
  } catch {
    // Storage can be unavailable or throw outright; showing the caution is the
    // safe answer when we cannot tell.
    return false
  }
}

function dismissKeyNote () {
  el('key-note').hidden = true
  try {
    localStorage.setItem(KEY_NOTE_STORAGE, 'true')
  } catch {
    // Dismissed for this session, which is the part the user asked for.
  }
}

// --- theme -----------------------------------------------------------------

/**
 * Reflect the chosen mode on the document.
 *
 * The choice is made on a board, with its Dark Mode switch, and arrives here
 * from the main process; the shell has no control of its own. The attribute
 * is set for an explicit choice only. 'system' removes it, which leaves the
 * stylesheet's prefers-color-scheme rule to answer — the difference between
 * "follow the OS" and "be light", which a boolean could not express.
 */
function paintTheme ({ theme }) {
  if (theme === 'system') {
    document.documentElement.removeAttribute('data-theme')
  } else {
    document.documentElement.setAttribute('data-theme', theme)
  }
}

// --- sidebar ---------------------------------------------------------------

/**
 * Reflect the collapsed state on the document.
 *
 * One class carries it: every rail rule hangs off `.sidebar-collapsed` on the
 * root element, so collapsing is a single toggle rather than a walk over the
 * sidebar's parts. The button's label names what the next click will do, not
 * what the sidebar currently is, and names the chord with it — the chord works
 * from a board, where this button is not even on screen.
 */
function paintSidebar ({ collapsed }) {
  document.documentElement.classList.toggle('sidebar-collapsed', collapsed)
  const chord = api.platform === 'mac' ? '⌘B' : 'Ctrl+B'
  const label = `${collapsed ? 'Expand' : 'Collapse'} sidebar (${chord})`
  el('sidebar-toggle').setAttribute('aria-label', label)
  el('sidebar-toggle').title = label
}

// --- wiring ----------------------------------------------------------------

// The click only asks. The main process owns the collapsed state and moves
// every board view before it announces the change, so painting here would
// restyle the page against bounds the boards have not reached yet; the paint
// arrives from onSidebarChanged below instead.
el('sidebar-toggle').addEventListener('click', () => {
  api.toggleSidebar().catch((error) => {
    console.error('workbench: could not toggle the sidebar', error)
  })
})

api.onThemeChanged(paintTheme)
api.onSidebarChanged(paintSidebar)

for (const button of document.querySelectorAll('.rail-item')) {
  button.addEventListener('click', () => setView(button.dataset.view))
}

el('dismiss-key-note').addEventListener('click', dismissKeyNote)
el('pick-folder').addEventListener('click', pickFolder)
el('rescan').addEventListener('click', () => {
  if (state.scan.root) runScan(state.scan.root)
})
el('do-import').addEventListener('click', doImport)

el('search').addEventListener('input', () => {
  state.query = el('search').value.trim()
  renderScan()
})

for (const button of document.querySelectorAll('.chip')) {
  button.addEventListener('click', () => {
    state.filter = button.dataset.filter
    for (const other of document.querySelectorAll('.chip')) {
      other.classList.toggle('active', other === button)
    }
    renderScan()
  })
}

// Selection acts on what is shown, which is the only set the user can see.
el('select-visible').addEventListener('click', () => {
  for (const repository of visibleRepositories()) {
    if (!repository.imported) state.selected.add(repository.path)
  }
  renderScan()
})

el('select-none').addEventListener('click', () => {
  state.selected.clear()
  renderScan()
})

api.onScanProgress(({ done, total }) => {
  el('scan-root').textContent = `Reading repositories… ${done}/${total}`
})

api.onImportProgress(({ done, total }) => {
  el('import-status').textContent = `Importing ${done}/${total}…`
})

api.onProjectExited(({ projectId }) => {
  loadProjects()
  if (state.activeProjectId === projectId) {
    el('board-state').textContent = 'This board stopped. Select it again to restart it.'
  }
})

async function boot () {
  // Drives the one piece of chrome that differs by platform: the space macOS
  // needs above the sidebar for its inset traffic lights.
  document.documentElement.classList.add(`is-${api.platform}`)
  paintTheme(await api.getTheme())
  paintSidebar(await api.getSidebar())
  try {
    const version = await api.version()
    el('version').textContent = `workbook ${version.version ?? ''}`.trim()
    // Which binary is driving these repositories is the first thing worth
    // knowing when the app and a terminal disagree about a project, and the
    // version line is the only place left that can say so: the build on one
    // line, where it came from on the next.
    const build = version.bundled ? 'Bundled build' : 'Installed build'
    el('version').title = `${build}\n${version.path}`
  } catch (error) {
    el('version').textContent = 'workbook not found'
    el('version').title = error.message
    el('version').classList.add('missing')
  }
  await loadProjects()
  setView('import')
}

boot()
