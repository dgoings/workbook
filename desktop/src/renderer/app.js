'use strict'

const api = window.workbench

const state = {
  view: 'import',
  projects: [],
  // The sidebar's order and categories, as the main process last announced
  // them; state.projects is in the same order. See adoptProjects.
  layout: { items: [] },
  // The category whose name is being typed, and the one whose removal is
  // being asked about, so a redraw in the middle keeps either open.
  editingCategoryId: null,
  editingDraft: null,
  editingSelection: null,
  editingRefused: false,
  removingCategoryId: null,
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
  // { projectId, retry, needs } — `needs` being what Git could not supply, which
  // is what the save is held to. Null otherwise.
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
  const { projects, layout } = await api.listProjects()
  adoptProjects({ projects, layout })
}

/**
 * Take a project list and layout from the main process and draw them.
 *
 * The only way either changes here. A drag, a fold or a rename asks the main
 * process and waits for its sidebar:layoutChanged answer rather than moving
 * rows itself: the menu numbers Cmd+1 to Cmd+9 from the main process's order,
 * and a sidebar redrawn ahead of it would show one order while the shortcuts
 * and Previous/Next walked another.
 */
function adoptProjects ({ projects, layout }) {
  state.projects = projects
  state.layout = layout ?? { items: projects.map((project) => ({ kind: 'project', id: project.id })) }
  renderProjects()
}

// A redraw between a press and its release replaces the row being pressed,
// and the release then lands on a new element, so the click never happens:
// clicking a project while a category name is open (its blur saves and
// redraws), or while a board starting reloads the list, took two clicks. So
// a redraw asked for while a button is down waits until the click has been
// handled, with a cap so a press that never releases cannot freeze the list.
const redraw = { held: false, owed: false, timer: null }

function holdRedraw () {
  redraw.held = true
  clearTimeout(redraw.timer)
  redraw.timer = setTimeout(releaseRedraw, 1000)
}

function releaseRedraw () {
  clearTimeout(redraw.timer)
  redraw.held = false
  if (redraw.owed) {
    redraw.owed = false
    renderProjects()
  }
}

function renderProjects () {
  if (redraw.held) {
    redraw.owed = true
    return
  }
  const list = el('project-list')
  // A category name being typed survives the redraw — the list is redrawn
  // whenever a board starts or stops, which can be mid-word — with its text and
  // its caret, so the rebuilt field carries on where the old one was.
  const typing = list.querySelector('.category-name-input')
  if (typing && state.editingCategoryId !== null) {
    state.editingDraft = typing.value
    state.editingSelection = [typing.selectionStart, typing.selectionEnd]
    state.editingRefused = typing.classList.contains('invalid')
  }
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

  // Drawn from the layout, which holds categories as well as projects, in the
  // order state.projects already has. An empty category is still drawn, so it
  // can be dropped into.
  const byId = new Map(state.projects.map((project) => [project.id, project]))
  for (const item of state.layout.items) {
    if (item.kind === 'project') {
      const project = byId.get(item.id)
      if (project) list.append(projectRow(project))
    } else if (item.kind === 'category') {
      list.append(categoryGroup(item, byId))
    }
  }

  if (list.children.length === 0) {
    const empty = document.createElement('li')
    empty.className = 'empty'
    empty.style.padding = '4px 9px'
    empty.textContent = 'None yet.'
    list.append(empty)
  }
}

function projectRow (project) {
  const item = document.createElement('li')
  item.className = 'project-item'
  item.dataset.projectId = project.id
  item.draggable = true
  if (project.id === state.activeProjectId) item.classList.add('active')
  // Redrawn mid-drag (the drop's broadcast, a board starting): still dimmed.
  if (drag.current?.kind === 'project' && drag.current.id === project.id) item.classList.add('dragging')
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
  item.addEventListener('click', () => {
    // Letting go of a drag is not a click on the row it was dragged from.
    if (justDragged()) return
    openProject(project.id)
  })
  return item
}

/**
 * A category: a header (disclosure, name, and rename and remove controls that
 * show on hover) over a nested list of its projects, hidden while folded. In
 * the rail the header is a thin divider labeled with the name's initials.
 */
function categoryGroup (category, byId) {
  const group = document.createElement('li')
  group.className = 'category'
  group.dataset.categoryId = category.id
  if (category.collapsed) group.classList.add('collapsed')
  if (drag.current?.kind === 'category' && drag.current.id === category.id) group.classList.add('dragging')

  const head = document.createElement('div')
  head.className = 'category-head'
  head.dataset.categoryId = category.id
  head.draggable = true
  head.title = category.name

  const toggle = document.createElement('button')
  toggle.type = 'button'
  toggle.className = 'category-toggle'
  toggle.textContent = '▾'
  toggle.setAttribute('aria-expanded', String(!category.collapsed))
  toggle.setAttribute('aria-label', `${category.collapsed ? 'Expand' : 'Collapse'} ${category.name}`)
  toggle.addEventListener('click', () => {
    if (justDragged()) return
    api.setCategoryCollapsed(category.id, !category.collapsed).catch((error) => {
      console.error('workbench: could not fold the category', error)
    })
  })

  const initials = document.createElement('span')
  initials.className = 'category-initials'
  initials.textContent = sidebarModel.initials(category.name)

  head.append(toggle, initials)
  if (state.editingCategoryId === category.id) {
    head.draggable = false
    head.append(nameEditor(category))
  } else if (state.removingCategoryId === category.id) {
    head.append(...removeConfirmation(category))
  } else {
    const name = document.createElement('span')
    name.className = 'category-name'
    name.textContent = category.name
    name.addEventListener('dblclick', () => startRename(category.id))

    const rename = document.createElement('button')
    rename.type = 'button'
    rename.className = 'category-action'
    rename.textContent = '✎'
    rename.title = 'Rename category'
    rename.setAttribute('aria-label', `Rename ${category.name}`)
    rename.addEventListener('click', () => startRename(category.id))

    const remove = document.createElement('button')
    remove.type = 'button'
    remove.className = 'category-action'
    remove.textContent = '×'
    remove.title = 'Remove category'
    remove.setAttribute('aria-label', `Remove ${category.name}`)
    remove.addEventListener('click', () => {
      state.removingCategoryId = category.id
      renderProjects()
    })
    head.append(name, rename, remove)
  }

  const children = document.createElement('ul')
  children.className = 'category-projects'
  children.hidden = category.collapsed
  for (const id of category.projects) {
    const project = byId.get(id)
    if (project) children.append(projectRow(project))
  }

  group.append(head, children)
  return group
}

function startRename (categoryId) {
  state.removingCategoryId = null
  state.editingCategoryId = categoryId
  state.editingDraft = null
  state.editingSelection = null
  renderProjects()
}

/**
 * The inline name field: Enter or leaving it saves, Escape puts the old name
 * back, and an empty name is refused — Enter keeps the field open and marks it,
 * leaving it cancels.
 */
function nameEditor (category) {
  const input = document.createElement('input')
  input.className = 'category-name-input'
  input.type = 'text'
  // A redraw while the field is open (a board starting reloads the list)
  // rebuilds it; what was typed so far comes back with it.
  input.value = state.editingDraft ?? category.name
  input.setAttribute('aria-label', 'Category name')
  if (state.editingRefused) input.classList.add('invalid')
  let settled = false
  const finish = (commit) => {
    if (settled) return
    const name = input.value.trim()
    if (commit && name === '') {
      input.classList.add('invalid')
      return
    }
    settled = true
    state.editingCategoryId = null
    state.editingDraft = null
    state.editingSelection = null
    state.editingRefused = false
    if (commit && name !== category.name) {
      api.renameCategory(category.id, name).catch((error) => {
        console.error('workbench: could not rename the category', error)
        renderProjects()
      })
    }
    // Drawn back with the name it had; a rename redraws again when its
    // broadcast lands.
    renderProjects()
  }
  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') { event.preventDefault(); finish(true) }
    if (event.key === 'Escape') { event.preventDefault(); finish(false) }
  })
  input.addEventListener('blur', () => {
    // Taken out of the page by a redraw is not the user leaving the field,
    // and nor is the window losing focus to another app: the field is still
    // open, and still focused, when they come back.
    if (!input.isConnected || !document.hasFocus()) return
    finish(input.value.trim() !== '')
  })
  // Focused once it is in the document: renderProjects appends it first. Opened
  // fresh, the whole name is selected to be typed over; rebuilt by a redraw,
  // the caret goes back where it was.
  const selection = state.editingSelection
  queueMicrotask(() => {
    input.focus()
    if (selection) input.setSelectionRange(selection[0], selection[1])
    else input.select()
  })
  return input
}

/** The second step of removing a category: say what happens, and ask. */
function removeConfirmation (category) {
  const question = document.createElement('span')
  question.className = 'category-confirm'
  question.textContent = 'Remove category?'

  const confirm = document.createElement('button')
  confirm.type = 'button'
  confirm.className = 'category-action category-confirm-yes'
  confirm.textContent = 'Yes'
  confirm.title = 'Its projects move to the top level'
  confirm.addEventListener('click', () => {
    state.removingCategoryId = null
    api.deleteCategory(category.id).catch((error) => {
      console.error('workbench: could not remove the category', error)
      renderProjects()
    })
  })

  const cancel = document.createElement('button')
  cancel.type = 'button'
  cancel.className = 'category-action'
  cancel.textContent = 'No'
  cancel.title = 'Keep the category'
  cancel.addEventListener('click', () => {
    state.removingCategoryId = null
    renderProjects()
  })
  return [question, confirm, cancel]
}

async function createCategory () {
  try {
    const { categoryId } = await api.createCategory('New category')
    // Its broadcast has drawn it already; open its name for typing.
    startRename(categoryId)
  } catch (error) {
    console.error('workbench: could not create a category', error)
  }
}

// --- sidebar drag and drop ---------------------------------------------------

// What is being dragged ({ kind, id }) while a drag is on, and when the last
// one ended, so the click that can follow a drop does not open a project.
const drag = { current: null, endedAt: 0 }

// The type dragstart puts on a drag of ours. Anything dragged over the list
// without it — a file from Finder, text from another app — is not a sidebar
// move, whatever drag.current says.
const DRAG_TYPE = 'application/x-workbench-sidebar'

function justDragged () {
  return drag.current !== null || performance.now() - drag.endedAt < 300
}

/**
 * Close the drag: forget what was dragged, note when, and undim it.
 *
 * Called from drop as well as dragend, because dragend goes to the element
 * the drag started on, and the drop's own broadcast — or a board starting —
 * can redraw the list and take that element out before dragend arrives. A
 * dragend on a detached element never reaches the list, so a drag closed only
 * there would stay open, and every click on a project would be taken for the
 * end of it.
 */
function endDrag ({ justEnded = true } = {}) {
  if (drag.current === null) return
  drag.current = null
  // Stamped only when the drag really ended now, so the click a drop can
  // produce is ignored; the stale-drag backstop below must not swallow the
  // very click whose press found it.
  if (justEnded) drag.endedAt = performance.now()
  clearDropIndicator()
  for (const node of el('project-list').querySelectorAll('.dragging')) node.classList.remove('dragging')
}

function clearDropIndicator () {
  for (const node of el('project-list').querySelectorAll('.drop-before, .drop-after, .drop-into')) {
    node.classList.remove('drop-before', 'drop-after', 'drop-into')
  }
}

/** The element a key from sidebarModel.dropTarget names. */
function rowForKey (key, into) {
  if (key === null) return null
  const separator = key.indexOf(':')
  const kind = key.slice(0, separator)
  const id = CSS.escape(key.slice(separator + 1))
  if (kind === 'project') return el('project-list').querySelector(`.project-item[data-project-id="${id}"]`)
  return el('project-list').querySelector(into
    ? `.category-head[data-category-id="${id}"]`
    : `.category[data-category-id="${id}"]`)
}

/**
 * What letting go here would do, in sidebarModel.dropTarget's terms. The row
 * is found by the pointer's height against every visible row, not by the
 * element under it: beside an indented category child, or in the list's side
 * padding, the element is a list, and the drop still means the row level with
 * the pointer.
 */
function dropAt (event) {
  if (!drag.current) return null
  if (!event.dataTransfer?.types?.includes(DRAG_TYPE)) return null
  const rows = []
  for (const row of el('project-list').querySelectorAll('.project-item[data-project-id], .category-head')) {
    // A folded category's projects take no room and cannot be dropped beside.
    if (row.offsetParent === null) continue
    const box = row.getBoundingClientRect()
    rows.push(row.classList.contains('category-head')
      ? { kind: 'category', id: row.dataset.categoryId, top: box.top, bottom: box.bottom }
      : { kind: 'project', id: row.dataset.projectId, top: box.top, bottom: box.bottom })
  }
  const { over, fraction } = sidebarModel.rowAt(rows, event.clientY)
  return sidebarModel.dropTarget(state.layout, drag.current, over, fraction)
}

function wireDragAndDrop () {
  const list = el('project-list')

  list.addEventListener('dragstart', (event) => {
    // No drag in the rail: its tiles are too small to aim between.
    if (document.documentElement.classList.contains('sidebar-collapsed')) {
      event.preventDefault()
      return
    }
    const source = event.target.closest?.('.project-item[data-project-id], .category-head')
    if (!source) return
    drag.current = source.classList.contains('category-head')
      ? { kind: 'category', id: source.dataset.categoryId }
      : { kind: 'project', id: source.dataset.projectId }
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData(DRAG_TYPE, `${drag.current.kind}:${drag.current.id}`)
    event.dataTransfer.setData('text/plain', `${drag.current.kind}:${drag.current.id}`)
    const dimmed = drag.current.kind === 'category' ? source.closest('.category') : source
    dimmed.classList.add('dragging')
    // A drag sends no mouseup, so the redraw its press held is let go here —
    // but on the next task, not now. The press can itself have asked for a
    // redraw (an open name field's blur, the remove question going away), and
    // running it now would replace the row being dragged while Chromium is
    // still starting the drag from it, which cancels the drag. The redrawn row
    // comes back dimmed: projectRow and categoryGroup read drag.current.
    setTimeout(releaseRedraw, 0)
  })

  list.addEventListener('dragover', (event) => {
    const drop = dropAt(event)
    clearDropIndicator()
    // Not prevented, so the pointer says the drop is refused.
    if (!drop) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
    const { indicator } = drop
    const row = rowForKey(indicator.key, indicator.type === 'into')
    if (row) row.classList.add(indicator.type === 'into' ? 'drop-into' : `drop-${indicator.edge}`)
  })

  list.addEventListener('dragleave', (event) => {
    if (!list.contains(event.relatedTarget)) clearDropIndicator()
  })

  list.addEventListener('drop', (event) => {
    const drop = dropAt(event)
    clearDropIndicator()
    if (!drop) return
    event.preventDefault()
    // Asked, not done: the rows move when the main process announces the
    // saved layout, so what is drawn is always what Cmd+N counts.
    const request = drop.action === 'moveCategory'
      ? api.moveCategory(drag.current.id, drop.index)
      : api.moveProject(drag.current.id, drop.target)
    // Closed here, not left to dragend: see endDrag.
    endDrag()
    request.catch((error) => {
      console.error('workbench: could not move that', error)
    })
  })

  list.addEventListener('dragend', () => { endDrag() })

  // A backstop for a drag that no drop or dragend closed — its source was
  // redrawn away and the pointer let go outside the list. No mousedown can
  // happen during a drag, so one arriving means any drag still on record is
  // over.
  document.addEventListener('mousedown', () => { endDrag({ justEnded: false }) }, true)
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
 * not already running is started, the shell asks the main process what Git can
 * supply here, and if anything is missing it shows a form in place of the board.
 * `retry` is the open that was interrupted, run again once the identity is saved.
 *
 * What is missing is a fact about the machine rather than a fixed rule: the
 * address is always needed when it is unset, while a name is needed only where
 * Git cannot derive one from the operating-system account. So the form is drawn
 * from `needs` — each field required or optional as Git reported it.
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

  const needs = identity.needs
  state.identity = { projectId, retry, needs }
  // A board opened before this one may still be showing, and it is a native
  // view drawn over this document: the form would be underneath it.
  api.showChrome()
  el('board-state').textContent = ''
  el('identity-repo').textContent = project.path
  el('identity-missing').textContent = missingSentence(needs)
  el('identity-name').value = identity.name ?? ''
  el('identity-email').value = identity.email ?? ''
  markIdentityField('name', needs.name, identity.name)
  markIdentityField('email', needs.email, identity.email)
  showIdentityError('name', '')
  showIdentityError('email', '')
  el('identity-status').textContent = ''
  el('identity-save').disabled = false
  el('identity-form').hidden = false
  // Straight to the first field that has to be filled in, which is the address
  // whenever that is one of them.
  el(needs.email ? 'identity-email' : 'identity-name').focus()
  return true
}

// What Git cannot supply, as a noun phrase: "no email address", "no name", or
// both. Used in the form's own sentence and in the refusal if a save leaves
// something still missing, so the two always name the same thing.
function missingIdentity (needs) {
  const parts = []
  if (needs.name) parts.push('no name')
  if (needs.email) parts.push('no email address')
  return parts.join(' and ')
}

// The sentence under the heading. A missing name is worth an extra clause: Git
// usually invents one from the computer account, so a reader on a machine where
// it cannot would otherwise wonder why they are being asked at all.
function missingSentence (needs) {
  const tail = needs.name ? ', and no account name to fall back on' : ''
  return `There is ${missingIdentity(needs)} set${tail}.`
}

const IDENTITY_LABELS = { name: 'Name', email: 'Email' }

// Label a field required or optional, and show the hint only where it is true:
// a blank name falls back to the account only on a machine where that works,
// and only when there is no configured name to keep.
function markIdentityField (field, required, configured) {
  el(`identity-${field}-label`).textContent = required
    ? IDENTITY_LABELS[field]
    : `${IDENTITY_LABELS[field]} (optional)`
  const hint = el(`identity-${field}-hint`)
  if (hint) hint.hidden = required || Boolean(configured)
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
  // line below. Only what Git cannot supply is demanded; a field that was filled
  // in anyway is still checked, because a bad address written over a good one
  // would break a checkout that worked.
  const nameMissing = pending.needs.name && !name
  const addressWanted = pending.needs.email || Boolean(email)
  const addressLooksRight = !addressWanted || /^[^\s@]+@[^\s@]+$/.test(email)
  showIdentityError('name', nameMissing ? 'Enter a name.' : '')
  showIdentityError('email', addressLooksRight ? '' : 'Enter an address like you@example.com.')
  if (nameMissing || !addressLooksRight) return

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
    // Names what is still missing rather than repeating the heading: whatever was
    // written, Git resolves the identity its own way, and a local value the
    // checkout already carried can shadow a global save.
    el('identity-status').textContent =
      `Git still has ${missingIdentity(identity.needs)} for this repository.`
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
// Every saved layout edit, from this page or not: the order shortcuts count in
// changes with it, so the list is replaced whole. The Next view lists projects
// in that order too, and it otherwise waits for its next tick to find out, so
// an open one is read again now.
api.onSidebarLayoutChanged((payload) => {
  adoptProjects(payload)
  if (state.view === 'next') loadNext(true)
})
el('new-category').addEventListener('click', () => { createCategory() })
wireDragAndDrop()
document.addEventListener('mousedown', (event) => {
  holdRedraw()
  // The remove question goes away when the user clicks anywhere but its row.
  if (state.removingCategoryId !== null &&
      event.target.closest?.('.category-head')?.dataset.categoryId !== state.removingCategoryId) {
    state.removingCategoryId = null
    renderProjects()
  }
}, true)
// After the click: a timer queued from mouseup runs once the click it
// produces has been dispatched.
document.addEventListener('mouseup', () => { setTimeout(releaseRedraw, 0) }, true)

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

// Beside a board, the board draws the sidebar's divider under its own header,
// so the shell's must go; see #sidebar::after in styles.css. The main process
// says when a board is actually laid in, which the project view alone does not:
// that view also holds the wait for a board, a failed start and the Git
// identity form, and each of those needs the shell's divider.
api.onBoardShowing((showing) => {
  document.documentElement.classList.toggle('board-active', showing === true)
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
  // Drives the chrome that differs by platform: the inset macOS needs at the
  // left of the sidebar head's first line for its traffic lights, and where
  // the chevron goes in the rail, which the lights fill on macOS.
  document.documentElement.classList.add(`is-${api.platform}`)
  // First and synchronous, so the head is never drawn against a title row of
  // the wrong height: the main process centers the traffic lights, or sizes the
  // overlay controls, on this same number from src/main/layout.js.
  document.documentElement.style.setProperty('--title-row-height', `${api.titleRowHeight}px`)
  // The rail's width likewise: the board beside it starts exactly here.
  document.documentElement.style.setProperty('--rail-width', `${api.railWidth}px`)
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
