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
  keys: new Map(),
  // The open waiting on a Git identity, while the form asking for one is up:
  // { projectId, retry }. Null otherwise.
  identity: null,
  // The Next view: how many per project, the last payload read, the payload
  // the DOM currently shows, the poll timer while it is showing, the timer that
  // lets the limit control settle before reading, a counter that tells a stale
  // answer from the current one, the read that is out — the request itself,
  // because a flag beside it can be cleared by something that is not the
  // request and then no longer means what it says — whether a forced read is
  // owed once that one lands, and whether any read has ever settled.
  nextLimit: 1,
  next: null,
  nextDrawn: null,
  nextTimer: null,
  nextLimitTimer: null,
  nextGeneration: 0,
  nextPending: null,
  nextReloadWanted: false,
  nextSettled: false
}

const el = (id) => document.getElementById(id)

// --- view switching --------------------------------------------------------

function setView (view, projectId = null) {
  state.view = view
  state.activeProjectId = projectId

  for (const name of ['import', 'next', 'project']) {
    el(`view-${name}`).hidden = name !== view
  }
  for (const button of document.querySelectorAll('.rail-item')) {
    button.classList.toggle('active', button.dataset.view === view)
  }
  for (const item of document.querySelectorAll('.project-item')) {
    item.classList.toggle('active', item.dataset.projectId === projectId)
  }
  // After that loop, not before it: the Next entry carries .project-item and
  // has no data-project-id, so the loop above has just cleared it.
  el('next-item').classList.toggle('active', view === 'next')
  if (view === 'next') startNextPolling()
  else stopNextPolling()

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

  // Two or more: with one project its board already answers "what is next".
  const hideNext = state.projects.length < 2
  el('next-item').hidden = hideNext
  // Forgetting projects down to one takes the rail entry away underneath a
  // reader who is standing on it, leaving a view with nothing that points at it
  // and no way back. The import view is where a second project comes from, so
  // that is where they land. setView does not render the project list, so this
  // cannot come back around.
  if (hideNext && state.view === 'next') setView('import')

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
  hideIdentityForm()
  const project = state.projects.find((candidate) => candidate.id === projectId)
  el('board-state').textContent = `Starting the board for ${project?.name ?? projectId}…`
  if (await askForIdentity(projectId, () => openProject(projectId))) return
  try {
    await api.openProject(projectId)
    el('board-state').textContent = ''
  } catch (error) {
    el('board-state').textContent = `Could not start this board: ${error.message}`
  }
  loadProjects()
}

// --- git identity -----------------------------------------------------------

/**
 * Ask for a Git identity instead of starting a board that would refuse one.
 *
 * `workbook serve` will not start in a checkout with no user.email, and all it
 * says is that `git config --get user.email` failed. So before a board that is
 * not already running is started, the shell asks Git what it would use, and if
 * there is no email it shows a form in place of the board. `retry` is the open
 * that was interrupted, run again once the identity is saved. An email is the
 * whole requirement; the form's name field is offered, not demanded.
 *
 * Resolves true when the form was shown (or the open went stale while Git was
 * asked), false when the board should go ahead and start. A running board
 * already has an identity, so it is not asked about; and a failure to ask is
 * not a reason to stop — the start that follows will report the real problem.
 */
async function askForIdentity (projectId, retry) {
  const project = state.projects.find((candidate) => candidate.id === projectId)
  if (!project || project.status === 'running') return false

  let identity
  try {
    identity = await api.getIdentity(projectId)
  } catch (error) {
    console.error('workbench: could not read the Git identity', error)
    return false
  }
  if (identity.complete) return false
  // Another project was picked while Git answered; that open owns the view now.
  if (state.view !== 'project' || state.activeProjectId !== projectId) return true

  state.identity = { projectId, retry }
  // A board opened before this one may still be showing, and it is a native
  // view drawn over this document: the form would be underneath it.
  api.showChrome()
  el('board-state').textContent = ''
  el('identity-repo').textContent = project.path
  el('identity-name').value = identity.name ?? ''
  el('identity-email').value = identity.email ?? ''
  showIdentityError('name', '')
  showIdentityError('email', '')
  el('identity-status').textContent = ''
  el('identity-save').disabled = false
  el('identity-form').hidden = false
  // Straight to the email, which is the half that is missing and the only half
  // a board needs; a name Git can sometimes make up for itself.
  el('identity-email').focus()
  return true
}

function hideIdentityForm () {
  state.identity = null
  el('identity-form').hidden = true
}

function showIdentityError (field, message) {
  const node = el(`identity-${field}-error`)
  node.textContent = message
  node.hidden = !message
  el(`identity-${field}`).classList.toggle('invalid', Boolean(message))
}

// An invoke that rejects arrives as "Error invoking remote method 'x': Error:
// the message". Only the message is for the user.
function remoteMessage (error) {
  return String(error?.message ?? error)
    .replace(/^Error invoking remote method '[^']+': (?:Error: )?/, '')
}

async function saveIdentity (event) {
  event.preventDefault()
  const pending = state.identity
  if (!pending) return

  const name = el('identity-name').value.trim()
  const email = el('identity-email').value.trim()
  const scope = document.querySelector('input[name="identity-scope"]:checked')?.value ?? 'global'

  // The obvious mistake is caught here so it can sit beside its field. The main
  // process checks again, properly, and anything it refuses lands in the status
  // line below. The address is all that is checked because it is all that is
  // required: a blank name is saved as no name at all.
  const addressLooksRight = /^[^\s@]+@[^\s@]+$/.test(email)
  showIdentityError('name', '')
  showIdentityError('email', addressLooksRight ? '' : 'Enter an address like you@example.com.')
  if (!addressLooksRight) return

  el('identity-save').disabled = true
  el('identity-status').textContent = 'Saving…'
  let identity
  try {
    identity = await api.setIdentity(pending.projectId, { name, email, scope })
  } catch (error) {
    el('identity-status').textContent = remoteMessage(error)
    el('identity-save').disabled = false
    return
  }
  // The reader moved on while Git wrote; the identity is saved all the same.
  if (state.identity !== pending) return
  if (!identity.complete) {
    el('identity-status').textContent = 'Git still reports no identity for this repository.'
    el('identity-save').disabled = false
    return
  }
  hideIdentityForm()
  pending.retry()
}

el('identity-form').addEventListener('submit', saveIdentity)

// --- next -------------------------------------------------------------------

const NEXT_POLL_MS = 5000

// How long the limit control has to settle before its value is read. Holding a
// number input's arrow fires a change per step, and each one would otherwise be
// a sweep of `workbook next` across every project.
const NEXT_LIMIT_DEBOUNCE_MS = 300

function readStoredLimit () {
  try {
    const stored = Number(localStorage.getItem('next.limit'))
    if (Number.isInteger(stored) && stored >= 1 && stored <= 20) return stored
  } catch {
    // Storage can be unavailable or throw outright; one per project is the
    // answer whenever we cannot tell what was chosen last.
  }
  return 1
}

function storeLimit (limit) {
  try { localStorage.setItem('next.limit', String(limit)) } catch {
    // A per-viewer convenience. Losing it costs one number next launch.
  }
}

function startNextPolling () {
  stopNextPolling()
  // The first read is the one the user is waiting for, so it goes out whatever
  // else is still settling; the ticks after it wait their turn.
  loadNext(true)
  state.nextTimer = setInterval(() => loadNext(false), NEXT_POLL_MS)
}

function stopNextPolling () {
  if (state.nextTimer) clearInterval(state.nextTimer)
  state.nextTimer = null
  // An answer already on its way belongs to a view nobody is looking at any
  // more: moving the generation on drops it when it lands.
  state.nextGeneration += 1
  // A forced read owed to a view nobody is looking at any more is owed to
  // nothing: leaving it armed would fire a sweep on the next view's first tick.
  state.nextReloadWanted = false
  // state.nextPending is deliberately left alone. The request really is still
  // out — one `workbook next` per project, running whatever this view does
  // next — and clearing it here would be this function claiming otherwise, so
  // the very next forced read would start a second sweep beside it. It is the
  // request's own finally that says the request is over.
}

async function loadNext (force = false) {
  // One read at a time. A project whose `next` takes longer than the interval
  // would otherwise stack another process every tick, and each answer would be
  // discarded by the one behind it — the view would freeze on the last payload
  // it managed to draw while the machine kept spawning processes.
  if (state.nextPending) {
    if (!force) return
    // A forced read — returning to the view, or a new limit — supersedes the
    // sweep that is out rather than running beside it. Beside it would spawn
    // `workbook next` a second time in every project and then discard whichever
    // answer lost the race, which is the pile-up this guard exists to prevent.
    // So the want is recorded and the sweep that is out re-runs once it lands.
    state.nextReloadWanted = true
    return
  }
  const generation = ++state.nextGeneration
  // Only before the first read settles: a project that fails every tick would
  // otherwise flicker between 'Loading…' and its error forever.
  if (!state.nextSettled) el('next-state').textContent = 'Loading…'
  try {
    // Held in state while it is out, so anything asking for a read can see
    // there is one rather than being told by a flag somebody else may have
    // reset. Nothing but the finally below puts it back.
    state.nextPending = api.loadNext(state.nextLimit)
    const payload = await state.nextPending
    // A slower answer arriving after a newer one must not paint over it.
    if (generation !== state.nextGeneration) return
    state.next = payload
    el('next-state').textContent = `Updated ${new Date().toLocaleTimeString()}`
    renderNext()
  } catch (error) {
    if (generation !== state.nextGeneration) return
    el('next-state').textContent = `Could not read next tasks: ${error.message}`
  } finally {
    // Whatever became of the answer — drawn, discarded as stale, or an outright
    // failure — the request is over, so the gate opens here and only here. No
    // generation test guards this: there is only ever one read out, because
    // that is what the gate above enforces, and a read that could not clear its
    // own record of itself would shut the view's polling down for good.
    state.nextPending = null
    state.nextSettled = true
    // Now the forced read that arrived while this one was out runs — once,
    // however many arrived, and with the limit as it now stands. Only if the
    // view is still showing: a read owed to a view somebody has left is owed to
    // nothing, and starting one would spawn a process per project for a list
    // nobody can see.
    if (state.nextReloadWanted) {
      state.nextReloadWanted = false
      if (state.view === 'next') loadNext(true)
    }
  }
}

function renderNext () {
  const container = el('next-groups')
  const payload = state.next
  if (!payload) return

  // Tick to tick this list usually says the same thing, and #next-groups is the
  // scrolling container itself: rebuilding it clamps the scroll back to the top,
  // drops hover, and takes the focused row out from under the keyboard. So an
  // unchanged payload redraws nothing at all.
  const drawn = JSON.stringify(payload)
  if (drawn === state.nextDrawn) return
  state.nextDrawn = drawn

  // A redraw that does have to happen keeps the reader's place and their
  // keyboard focus, which is on a task rather than on a position in the list.
  const scrollTop = container.scrollTop
  const focused = document.activeElement?.closest?.('.next-row')?.dataset.taskId ?? null

  container.innerHTML = ''

  for (const project of payload.projects) {
    const group = document.createElement('section')
    group.className = 'next-group'

    const heading = document.createElement('h2')
    heading.className = 'next-group__name'
    // The same dot the sidebar draws, for the same reason it draws it there:
    // this list is only as current as that project's server, and a group under
    // a stopped one is the last thing it answered rather than what is next now.
    // The word goes on the heading, not on the dot: a title of its own on a
    // .4rem circle is the hardest target on the view to land the pointer on.
    const status = project.status ?? 'stopped'
    heading.title = status

    const dot = document.createElement('span')
    dot.className = `dot ${status}`

    const key = document.createElement('span')
    key.className = 'project-key'
    key.textContent = project.key
    heading.append(dot, key, document.createTextNode(` ${project.name}`))
    group.append(heading)

    for (const task of project.tasks) group.append(renderNextRow(project, task))

    const foot = document.createElement('p')
    foot.className = 'next-group__foot'
    if (project.error) {
      foot.classList.add('next-group__foot--error')
      // A CLI too old for --limit answers with its entire usage block, and the
      // footer is one line in a group: the first line is the part that names
      // the problem, and the whole of it is on the hover rather than pushing
      // every other project off the view.
      const first = project.error.split('\n')[0]
      foot.textContent = first.length > 140 ? `${first.slice(0, 139)}…` : first
      foot.title = project.error
    } else if (project.tasks.length === 0) {
      foot.textContent = 'Nothing eligible.'
    } else if (project.eligible > project.tasks.length) {
      const more = project.eligible - project.tasks.length
      foot.textContent = `${more} more eligible`
    } else {
      foot.hidden = true
    }
    group.append(foot)
    container.append(group)
  }

  container.scrollTop = scrollTop
  // Compared in JavaScript rather than matched by a selector: a task id is
  // Workbook's, not ours, and building a selector out of one puts its escaping
  // on the critical path of a redraw that happens every five seconds.
  if (focused) {
    [...container.querySelectorAll('.next-row')]
      .find((row) => row.dataset.taskId === focused)
      ?.focus()
  }
}

function renderNextRow (project, task) {
  const row = document.createElement('button')
  row.type = 'button'
  row.className = 'next-row'
  row.title = task.id
  // What a redraw looks the focused row up by, so the comparison is against a
  // value of our own rather than against whatever the tooltip happens to say.
  row.dataset.taskId = task.id
  row.addEventListener('click', () => openNextTask(project.id, task))

  const priority = document.createElement('span')
  priority.className = 'label next-row__priority'
  // Only high is colored, and only high is named here: a literal is what the
  // style checker can see, and a priority Workbook adds later cannot turn into
  // a class name — or, with whitespace in it, into a DOMException.
  if (task.priority === 'high') priority.classList.add('next-row__priority--high')
  priority.textContent = task.priority

  const title = document.createElement('span')
  title.className = 'next-row__title'
  title.textContent = task.title
  // The row is one line and ellipsizes; the row's own tooltip is the task id,
  // so the whole title has to be readable from the title itself.
  title.title = task.title

  const meta = document.createElement('span')
  meta.className = 'next-row__meta'
  for (const label of task.labels) {
    const chip = document.createElement('span')
    chip.className = 'label'
    chip.textContent = label
    meta.append(chip)
  }
  for (const email of task.assignees) {
    const chip = document.createElement('span')
    chip.className = 'label next-row__assignee'
    chip.textContent = email
    chip.title = 'Assigned'
    meta.append(chip)
  }
  const age = document.createElement('span')
  age.className = 'repo-fact'
  age.textContent = relativeDate(task.updatedAt)
  age.title = task.updatedAt
  meta.append(age)

  row.append(priority, title, meta)
  return row
}

/**
 * Short relative age, matching how the repository rows read.
 *
 * Mirrors shortDate in src/main/repoinfo.js; the renderer has no module loader
 * to share it.
 */
function relativeDate (iso) {
  const then = new Date(iso)
  if (Number.isNaN(then.getTime())) return ''
  const days = Math.floor((Date.now() - then.getTime()) / 86400000)
  if (days <= 0) return 'today'
  if (days === 1) return 'yesterday'
  if (days < 30) return `${days}d ago`
  if (days < 365) return `${Math.floor(days / 30)}mo ago`
  return `${Math.floor(days / 365)}y ago`
}

/** Open the board on this task, rather than on the board it lives in. */
async function openNextTask (projectId, task) {
  setView('project', projectId)
  hideIdentityForm()
  el('board-state').textContent = `Opening ${task.title}…`
  if (await askForIdentity(projectId, () => openNextTask(projectId, task))) return
  try {
    await api.openProject(projectId, task.id)
    el('board-state').textContent = ''
  } catch (error) {
    el('board-state').textContent = `Could not open this board: ${error.message}`
  }
  loadProjects()
}

el('next-item').addEventListener('click', () => setView('next'))

el('next-limit').addEventListener('change', () => {
  const input = el('next-limit')
  const wanted = Math.min(20, Math.max(1, Math.floor(Number(input.value)) || 1))
  input.value = String(wanted)
  state.nextLimit = wanted
  storeLimit(wanted)
  // Stepping the spinner from 1 to 8 is seven changes, and reading at each one
  // would sweep every project seven times for a number nobody stopped at. The
  // value is kept immediately — it is what the next read asks for — and only
  // the read waits for the control to settle.
  if (state.nextLimitTimer) clearTimeout(state.nextLimitTimer)
  state.nextLimitTimer = setTimeout(() => {
    state.nextLimitTimer = null
    if (state.view === 'next') startNextPolling()
  }, NEXT_LIMIT_DEBOUNCE_MS)
})

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
    // A repository that already carries an identity has a founding key that
    // cannot be changed: `setup` with a different one is refused outright.
    // Showing it as editable would only offer an import that fails. The
    // project can still add keys and move which one new tasks are minted
    // under; that is `workbook key`, not an import decision.
    key.disabled = alreadyImported || repository.initialized
    if (repository.initialized) key.title = 'Already minted — a founding key cannot be changed; workbook key add and workbook key current move where new tasks are minted'
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

// --- the PATH notice --------------------------------------------------------

/**
 * Say, once, that the CLI was copied somewhere permanent and put on PATH.
 *
 * Asked for rather than pushed: the install runs while this page is loading, so
 * a message sent the moment it finished could arrive before anything here was
 * listening. The main process answers null whenever there is nothing to say —
 * already said, nothing bundled to install, or nothing changed because an
 * earlier launch had already done it — and records that it has been said as it
 * answers. Dismissal therefore only hides it, and needs no storage of its own.
 */
async function showPathNotice () {
  let notice
  try {
    notice = await api.pathNotice()
  } catch (error) {
    // The least important thing on the page. Not being able to read it is not a
    // reason for anything else to go unpainted.
    console.error('workbench: could not read the PATH notice', error)
    return
  }
  if (!notice?.directory) return
  el('path-note-directory').textContent = notice.directory
  // A new terminal is enough everywhere except Windows, which caches the
  // environment until the user signs out: the two sentences are both in the
  // markup and the platform picks one, so neither is assembled in a string.
  const windows = api.platform === 'windows'
  el('path-note-terminal').hidden = windows
  el('path-note-terminal-windows').hidden = !windows
  el('path-note').hidden = false
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
// Nothing is stored: the main process recorded "said it" when it handed the
// notice over, so this launch is the only one that could ever show it anyway.
el('dismiss-path-note').addEventListener('click', () => { el('path-note').hidden = true })
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

// Each server start settles on its own; the sidebar's dot is drawn from the
// supervisor's status, so the list is reloaded rather than patched.
api.onProjectStarted(() => { loadProjects() })

// A menu shortcut asks for a view or a project; the renderer owns that state,
// so main only names what was asked and this decides what it means.
api.onShortcut((payload) => {
  if (!payload) return
  if (payload.kind === 'project') {
    const project = state.projects[payload.index]
    if (project) openProject(project.id)
    return
  }
  if (payload.kind === 'step') {
    if (state.projects.length === 0) return
    const at = state.projects.findIndex((project) => project.id === state.activeProjectId)
    // From a view that is not a project, Down goes to the first project and Up
    // to the last; from a project the step is clamped at either end.
    let next
    if (at < 0) next = payload.delta > 0 ? 0 : state.projects.length - 1
    else next = Math.min(state.projects.length - 1, Math.max(0, at + payload.delta))
    if (next !== at) openProject(state.projects[next].id)
    return
  }
  if (payload.kind === 'view') {
    if (payload.view === 'next' && state.projects.length < 2) return
    setView(payload.view)
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
    // line, the version it reports on the next, where it came from on the
    // third. The version is repeated here rather than left to the line above
    // because that line is one nowrap line inside a narrow sidebar and a
    // version like 0.6.0-rc1-16-ge935d54 is ellipsized away — the tooltip is
    // the only place the whole string can be read.
    const build = version.bundled ? 'Bundled build' : 'Installed build'
    el('version').title = [build, `workbook ${version.version ?? '(no version reported)'}`, version.path]
      .join('\n')
  } catch (error) {
    el('version').textContent = 'workbook not found'
    el('version').title = error.message
    el('version').classList.add('missing')
  }
  // Not awaited: the answer waits on the PATH install, which is copying a
  // binary and rewriting shell profiles, and the project list must not queue
  // behind a busy disk. It unhides itself whenever it arrives.
  showPathNotice()
  state.nextLimit = readStoredLimit()
  el('next-limit').value = String(state.nextLimit)
  await loadProjects()
  // Across several projects the first question is what to pick up; with one
  // there is nothing to compare, so the wizard is still the useful landing.
  setView(state.projects.length >= 2 ? 'next' : 'import')
}

boot()
