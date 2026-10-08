'use strict'

// The sidebar's layout commands as the main process runs them: validate what
// the renderer sent, apply the pure edit from sidebarlayout.js, save it, and
// tell everyone. Kept out of main.js so it can be tested without Electron;
// main.js wires each `sidebar:*` IPC channel to one of these and supplies the
// side effects as callbacks.
//
// The renderer is not trusted to send a well-formed request, so each argument
// is checked here and a bad one rejects with a message naming it. Type checks
// run on arrival; whether an id still exists is checked against the layout the
// command actually edits, inside the queue below, because the command ahead of
// it may have just deleted that category.

const layouts = require('./sidebarlayout')

/**
 * @param {object} options
 * @param {{ sidebarLayout: object, orderedProjects: object[], find: Function, setSidebarLayout: Function }} options.registry
 * @param {() => string} options.mintId a fresh category id (crypto.randomUUID in main.js)
 * @param {() => object[]} options.listProjects the ordered project list to send, in the
 *   shape registry:list sends it
 * @param {() => void} options.rebuildMenu reinstall the application menu
 * @param {(payload: { layout: object, projects: object[] }) => void} options.broadcast
 *   send sidebar:layoutChanged to the shell page
 */
function createSidebarCommands ({ registry, mintId, listProjects, rebuildMenu, broadcast }) {
  // One command at a time. Each edit reads the layout, computes the next one
  // and saves it; two overlapping would both read the same layout and the
  // second save would silently undo the first, and a failed save's rollback
  // would restore a layout another command had already replaced. A drag fires
  // these back to back, so they queue: each starts only after the previous
  // one's save has settled, one way or the other.
  let queue = Promise.resolve()

  function enqueue (run) {
    const done = queue.then(run)
    // The next command waits on a promise that always settles, so one failed
    // save rejects its own caller and does not wedge every command after it.
    queue = done.catch(() => {})
    return done
  }

  /**
   * Apply `edit` to the current layout, save it, and announce the result.
   * Nothing is announced and the menu is left alone if the save fails:
   * setSidebarLayout has rolled the stored layout back, so the renderer's
   * picture, drawn from the last announcement, is still the true one.
   */
  function commit (edit, extra = {}) {
    return enqueue(async () => {
      const next = edit(registry.sidebarLayout)
      await registry.setSidebarLayout(next)
      rebuildMenu()
      const payload = snapshot()
      broadcast(payload)
      return { ...payload, ...extra }
    })
  }

  function snapshot () {
    return { layout: registry.sidebarLayout, projects: listProjects() }
  }

  // Each command is async, so a bad argument rejects the renderer's invoke the
  // same way a failed save does; its type checks still run synchronously, before
  // the command joins the queue, so a malformed request never waits its turn.
  // Every command resolves with what it broadcast: { layout, projects }.
  return {
    /** sidebar:layout */
    layout: () => snapshot(),

    /** sidebar:moveProject { projectId, target: { kind: 'top', index } | { kind: 'category', id, index } } */
    async moveProject (args) {
      const channel = 'sidebar:moveProject'
      const { projectId, target } = object(channel, args)
      string(channel, 'projectId', projectId)
      object(channel, target, 'target')
      if (target.kind !== 'top' && target.kind !== 'category') {
        throw new TypeError(`${channel}: target.kind must be "top" or "category"`)
      }
      if (target.kind === 'category') string(channel, 'target.id', target.id)
      integer(channel, 'target.index', target.index)
      return commit((layout) => {
        if (registry.find(projectId) === null) throw new Error(`${channel}: projectId ${JSON.stringify(projectId)} is not a project`)
        if (target.kind === 'category') existingCategory(channel, 'target.id', layout, target.id)
        const clean = target.kind === 'top'
          ? { kind: 'top', index: target.index }
          : { kind: 'category', id: target.id, index: target.index }
        return layouts.moveProject(layout, projectId, clean)
      })
    },

    /** sidebar:moveCategory { categoryId, index } */
    async moveCategory (args) {
      const channel = 'sidebar:moveCategory'
      const { categoryId, index } = object(channel, args)
      string(channel, 'categoryId', categoryId)
      integer(channel, 'index', index)
      return commit((layout) => {
        existingCategory(channel, 'categoryId', layout, categoryId)
        return layouts.moveCategory(layout, categoryId, index)
      })
    },

    /** sidebar:createCategory { name } — answers with the new category's id too */
    async createCategory (args) {
      const channel = 'sidebar:createCategory'
      const { name } = object(channel, args)
      const trimmed = nonEmptyName(channel, name)
      const categoryId = mintId()
      return commit((layout) => layouts.createCategory(layout, categoryId, trimmed), { categoryId })
    },

    /** sidebar:renameCategory { categoryId, name } */
    async renameCategory (args) {
      const channel = 'sidebar:renameCategory'
      const { categoryId, name } = object(channel, args)
      string(channel, 'categoryId', categoryId)
      const trimmed = nonEmptyName(channel, name)
      return commit((layout) => {
        existingCategory(channel, 'categoryId', layout, categoryId)
        return layouts.renameCategory(layout, categoryId, trimmed)
      })
    },

    /** sidebar:setCategoryCollapsed { categoryId, collapsed } */
    async setCategoryCollapsed (args) {
      const channel = 'sidebar:setCategoryCollapsed'
      const { categoryId, collapsed } = object(channel, args)
      string(channel, 'categoryId', categoryId)
      if (typeof collapsed !== 'boolean') throw new TypeError(`${channel}: collapsed must be a boolean`)
      return commit((layout) => {
        existingCategory(channel, 'categoryId', layout, categoryId)
        return layouts.setCategoryCollapsed(layout, categoryId, collapsed)
      })
    },

    /** sidebar:deleteCategory { categoryId } */
    async deleteCategory (args) {
      const channel = 'sidebar:deleteCategory'
      const { categoryId } = object(channel, args)
      string(channel, 'categoryId', categoryId)
      return commit((layout) => {
        existingCategory(channel, 'categoryId', layout, categoryId)
        return layouts.deleteCategory(layout, categoryId)
      })
    }
  }
}

function object (channel, value, name = 'arguments') {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError(`${channel}: ${name} must be an object`)
  }
  return value
}

function string (channel, name, value) {
  if (typeof value !== 'string' || value === '') throw new TypeError(`${channel}: ${name} must be a non-empty string`)
}

function integer (channel, name, value) {
  if (!Number.isInteger(value)) throw new TypeError(`${channel}: ${name} must be an integer`)
}

function nonEmptyName (channel, name) {
  if (typeof name !== 'string' || name.trim() === '') throw new TypeError(`${channel}: name must be a non-empty string`)
  return name.trim()
}

function existingCategory (channel, name, layout, id) {
  if (!layout.items.some((item) => item.kind === 'category' && item.id === id)) {
    throw new Error(`${channel}: ${name} ${JSON.stringify(id)} is not a category`)
  }
}

module.exports = { createSidebarCommands }
