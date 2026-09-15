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
  people: [],
  myEmails: [],
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

  for (const name of ['import', 'people', 'project']) {
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
  if (view === 'people') loadPeople()
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

    const dot = document.createElement('span')
    dot.className = `dot ${project.status ?? 'stopped'}`
    dot.title = project.error ?? project.status ?? 'stopped'

    const key = document.createElement('span')
    key.className = 'project-key'
    key.textContent = project.key

    const name = document.createElement('span')
    name.className = 'project-name'
    name.textContent = project.name
    name.title = project.path

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

// --- assigning --------------------------------------------------------------

/** The name to show for an assignee, falling back to the address itself. */
function assigneeLabel (email) {
  const person = state.people.find((candidate) => candidate.emails.includes(String(email).toLowerCase()))
  return person ? person.displayName : email
}

function closeAssignMenu () {
  el('assign-menu').hidden = true
}

/**
 * Open the picker for one task, anchored under the cell that opened it.
 *
 * Everyone in the directory is offered, not only the people already on this
 * project: assigning someone their first task in a repository is exactly when
 * they would not appear yet.
 */
function showAssignMenu (task, anchor) {
  const menu = el('assign-menu')
  menu.innerHTML = ''

  const assigned = new Set((task.assignees ?? []).map((email) => email.toLowerCase()))

  const act = async (run) => {
    closeAssignMenu()
    try {
      const result = await run()
      if (result && result.cancelled) return
    } catch (error) {
      // A refusal here is a rule, not a fault — Workbook lets only the person
      // an assignment names or the one who recorded it remove it. Nothing in
      // this build opens the picker, so there is no list to redraw and nowhere
      // on screen to say so yet.
      console.error(`workbench: ${error.message}`)
    }
  }

  for (const email of task.assignees ?? []) {
    const item = document.createElement('button')
    item.type = 'button'
    item.className = 'menu-item menu-item--remove'
    item.setAttribute('role', 'menuitem')
    item.textContent = `Unassign ${assigneeLabel(email)}`
    item.addEventListener('click', () =>
      act(() => api.unassignTask(task.projectId, task.id, email)))
    menu.append(item)
  }

  if (assigned.size > 0) {
    const separator = document.createElement('div')
    separator.className = 'menu-separator'
    separator.setAttribute('role', 'separator')
    menu.append(separator)
  }

  const candidates = state.people.filter((person) => !person.bot)
  // Me first: it is the assignment most often wanted, and the one the CLI
  // spells `self`.
  candidates.sort((a, b) => {
    const mineA = a.emails.some((email) => state.myEmails.includes(email))
    const mineB = b.emails.some((email) => state.myEmails.includes(email))
    return (mineB ? 1 : 0) - (mineA ? 1 : 0) || b.commits - a.commits
  })

  let offered = 0
  for (const person of candidates) {
    // The primary address is the one an assignment should use.
    const [email] = person.emails
    if (!email || assigned.has(email)) continue
    const item = document.createElement('button')
    item.type = 'button'
    item.className = 'menu-item'
    item.setAttribute('role', 'menuitem')
    item.dataset.email = email
    // Every address the person has, so searching a work address finds them
    // even when the primary one is personal.
    item.dataset.search = [person.displayName, ...person.emails].join(' ').toLowerCase()
    const name = document.createElement('span')
    name.textContent = state.myEmails.includes(email) ? `${person.displayName} (me)` : person.displayName
    const address = document.createElement('small')
    address.textContent = email
    item.append(name, address)
    item.addEventListener('click', () =>
      act(() => api.assignTask(task.projectId, task.id, email)))
    menu.append(item)
    offered += 1
  }

  // Anyone at all, not only the people git already knows about. Workbook
  // accepts any address, and the moment you most need that is the moment a
  // person has no commits here yet — which is exactly when the directory,
  // built from commit history, cannot offer them.
  if (offered > 0 || assigned.size > 0) {
    const separator = document.createElement('div')
    separator.className = 'menu-separator'
    separator.setAttribute('role', 'separator')
    menu.append(separator)
  }

  const custom = document.createElement('input')
  custom.type = 'text'
  custom.className = 'assign-custom'
  custom.placeholder = 'Type a name or email…'
  custom.setAttribute('aria-label', 'Assign to a person or email address')
  custom.setAttribute('autocomplete', 'off')
  custom.addEventListener('click', (event) => event.stopPropagation())
  menu.append(custom)

  // Typing narrows the people already listed, so the field is a filter first
  // and a free-text escape hatch second. Assigning someone usually means
  // finding a name, not remembering an address.
  const offeredItems = [...menu.querySelectorAll('.menu-item[data-email]')]

  function currentMatches () {
    const term = custom.value.trim().toLowerCase()
    if (!term) return offeredItems
    return offeredItems.filter((item) => item.dataset.search.includes(term))
  }

  function highlight (items) {
    for (const item of offeredItems) item.classList.remove('menu-item--active')
    if (items.length > 0) items[0].classList.add('menu-item--active')
  }

  function applyFilter () {
    custom.classList.remove('invalid')
    const matches = currentMatches()
    const shown = new Set(matches)
    for (const item of offeredItems) item.hidden = !shown.has(item)
    highlight(matches)
  }

  custom.addEventListener('input', applyFilter)

  custom.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter') return
    const matches = currentMatches()
    const typed = custom.value.trim().toLowerCase()

    // A name that matches somebody wins over treating the text as an address:
    // "tyler" is a person here, not a mailbox.
    if (matches.length > 0) {
      const email = matches[0].dataset.email
      if (assigned.has(email)) { closeAssignMenu(); return }
      act(() => api.assignTask(task.projectId, task.id, email))
      return
    }

    // Nothing matched, so this must be an address for somebody not yet known.
    if (!/^[^@\s]+@[^@\s]+$/.test(typed)) {
      custom.classList.add('invalid')
      custom.title = 'No one matches that. Type a full email address to assign someone new.'
      return
    }
    if (assigned.has(typed)) { closeAssignMenu(); return }
    act(() => api.assignTask(task.projectId, task.id, typed))
  })

  applyFilter()

  menu.hidden = false
  // Focusing here would steal the keyboard from a user who opened the picker to
  // click a name, so the field waits to be chosen.
  positionMenu(menu, anchor)
}

/**
 * Anchor a popover under the control that opened it, pulled back inside the
 * window if that would put it off the bottom or the right edge.
 */
function positionMenu (menu, anchor) {
  const rect = anchor.getBoundingClientRect()
  const size = menu.getBoundingClientRect()
  const left = Math.min(rect.left, window.innerWidth - size.width - 8)
  const below = rect.bottom + 4
  const top = below + size.height > window.innerHeight
    ? Math.max(8, rect.top - size.height - 4)
    : below
  menu.style.left = `${Math.max(8, left)}px`
  menu.style.top = `${top}px`
}

// --- people ------------------------------------------------------------------

async function loadPeople () {
  const body = el('people-body')
  body.innerHTML = '<div class="empty">Reading commit history…</div>'

  const { people, defaultAssignee, mismatched } = await api.listPeople()
  state.people = people

  const warning = el('identity-warning')
  if (mismatched.length > 0) {
    warning.hidden = false
    // Worth surfacing loudly: an assignment made from one of these checkouts
    // is recorded against a different person, and nothing else would say so.
    warning.textContent =
      `These projects commit as an address that is not yours, so "assign self" ` +
      `there records someone else:\n` +
      mismatched.map((entry) => `  ${entry.key} — ${entry.email}`).join('\n')
  } else {
    warning.hidden = true
  }

  body.innerHTML = ''
  if (people.length === 0) {
    body.innerHTML = '<div class="empty">No contributors yet. Import a repository first.</div>'
    return
  }

  for (const person of people) {
    const row = document.createElement('div')
    row.className = 'person'
    const isMe = person.emails.includes(defaultAssignee)
    if (isMe) row.classList.add('person--me')
    if (person.bot) row.classList.add('person--bot')

    const identity = document.createElement('div')
    const name = document.createElement('div')
    name.className = 'person-name'
    const nameInput = document.createElement('input')
    nameInput.type = 'text'
    nameInput.value = person.displayName
    nameInput.setAttribute('aria-label', 'Display name')
    nameInput.addEventListener('change', async () => {
      await api.renamePerson(person.id, nameInput.value.trim())
      loadPeople()
    })
    name.append(nameInput)

    const emails = document.createElement('div')
    emails.className = 'person-emails'
    for (const email of person.emails) {
      const chip = document.createElement('span')
      chip.className = 'person-email' +
        (email === person.id ? ' person-email--primary' : '')
      chip.textContent = email
      // Splitting is per-address: the way to undo a wrong merge is to take the
      // address back out, not to rebuild the group.
      if (person.emails.length > 1) {
        chip.title = 'Click to separate this address into its own person'
        chip.style.cursor = 'pointer'
        chip.addEventListener('click', async () => {
          await api.splitPerson(email)
          loadPeople()
        })
      }
      emails.append(chip)
    }
    identity.append(name, emails)

    const stat = document.createElement('span')
    stat.className = 'person-stat'
    stat.textContent = `${person.commits} commits · ${person.repos.length} repo${person.repos.length === 1 ? '' : 's'}`
    stat.title = person.repos.join(', ')

    const action = document.createElement('button')
    action.type = 'button'
    action.className = isMe ? 'primary' : 'ghost inline'
    action.textContent = isMe ? 'This is me' : 'Set as me'
    action.disabled = isMe
    action.addEventListener('click', async () => {
      await api.setDefaultAssignee(person.id)
      loadPeople()
    })

    row.append(identity, stat, action)
    body.append(row)
  }
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

// --- menu ------------------------------------------------------------------

function menuOpen () {
  return !el('menu').hidden
}

function setMenu (open) {
  el('menu').hidden = !open
  el('menu-button').setAttribute('aria-expanded', String(open))
}

// --- theme -----------------------------------------------------------------

/**
 * Reflect the chosen mode on the document.
 *
 * The attribute is set for an explicit choice only. 'system' removes it, which
 * leaves the stylesheet's prefers-color-scheme rule to answer — the difference
 * between "follow the OS" and "be light", which a boolean could not express.
 */
function paintTheme ({ theme }) {
  if (theme === 'system') {
    document.documentElement.removeAttribute('data-theme')
  } else {
    document.documentElement.setAttribute('data-theme', theme)
  }
  for (const option of document.querySelectorAll('.theme-option')) {
    option.classList.toggle('active', option.dataset.theme === theme)
  }
}

// --- wiring ----------------------------------------------------------------

el('menu-button').addEventListener('click', (event) => {
  event.stopPropagation() // Or the document handler below closes it again.
  setMenu(!menuOpen())
})

// Any click outside dismisses, and Escape returns focus to the button — the
// two ways out a menu is expected to have.
document.addEventListener('click', (event) => {
  if (menuOpen() && !el('menu').contains(event.target)) setMenu(false)
  if (!el('assign-menu').hidden && !el('assign-menu').contains(event.target)) {
    closeAssignMenu()
  }
})

document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && menuOpen()) {
    setMenu(false)
    el('menu-button').focus()
  }
  if (event.key === 'Escape' && !el('assign-menu').hidden) closeAssignMenu()
})

for (const option of document.querySelectorAll('.theme-option')) {
  option.addEventListener('click', async () => {
    paintTheme(await api.setTheme(option.dataset.theme))
  })
}

api.onThemeChanged(paintTheme)

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
// The background check announces itself here rather than in a dialog: a native
// dialog is application-modal, and while one is open the app cannot quit.
api.onUpdateAvailable(({ version }) => {
  el('update-dot').hidden = false
  const item = el('install-update')
  item.hidden = false
  item.textContent = `Update to ${version}`
})

el('install-update').addEventListener('click', async () => {
  setMenu(false)
  // From here dialogs are fine: the user asked for this one.
  await api.installUpdate()
})

el('check-updates').addEventListener('click', async () => {
  setMenu(false)
  const result = await api.checkForUpdates()
  if (result?.skipped) el('import-status').textContent = ''
})

el('refresh').addEventListener('click', async () => {
  setMenu(false)
  // The directory is cached on the set of projects, which cannot notice a new
  // commit — so the explicit refresh is what re-reads history.
  await api.listPeople(true).then((r) => { state.people = r.people }).catch(() => {})
  await loadProjects()
  if (state.view === 'people') loadPeople()
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
  try {
    const version = await api.version()
    el('version').textContent = `workbook ${version.version ?? ''}`.trim()
    el('version').title = version.path
    // Which binary is driving these repositories is the first thing worth
    // knowing when the app and a terminal disagree about a project.
    el('binary-note').innerHTML = ''
    const label = document.createElement('strong')
    label.textContent = version.bundled ? 'Bundled build' : 'Installed build'
    el('binary-note').append(label, document.createElement('br'),
      document.createTextNode(version.path))
  } catch (error) {
    el('version').textContent = 'workbook not found'
    el('version').title = error.message
    el('version').classList.add('missing')
    el('binary-note').textContent = error.message
  }
  await loadProjects()
  try {
    state.people = (await api.listPeople()).people
  } catch {
    // A directory that cannot be built is not a reason to have no queue.
  }
  setView('import')
}

boot()
