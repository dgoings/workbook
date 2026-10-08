'use strict'

// The sidebar's order: which projects sit at the top level, which sit inside a
// named category, and in what order, kept out of registry.js and main.js so it
// can be tested without Electron or a disk.
//
// A layout is a plain value:
//
//   { items: [
//       { kind: 'project', id },
//       { kind: 'category', id, name, collapsed, projects: [id, ...] }
//   ] }
//
// Categories are one level deep: a category holds project ids, never another
// category. Every function here is pure. None mutates the layout it is given;
// each returns a new one. Nothing here mints an id either — the caller passes
// a category's id in (main.js uses crypto.randomUUID()), so the same call with
// the same arguments always gives the same answer.
//
// The mutations expect a layout normalize() has already produced, and throw on
// an id the layout does not hold or an argument of the wrong type rather than
// guessing: the IPC boundary validates first, and a throw here is the backstop
// that keeps a bad request from saving a layout nobody asked for.

/**
 * The layout a registry should be read as, given the projects it holds.
 *
 * Read strictly, like the registry's other fields. A registry written by a
 * build from before categories has no layout at all (`undefined`), and that
 * reads as every project at the top level in its stored order; so does any
 * value that is not an object with an `items` array. Within a layout that does
 * have the right shape:
 *
 * - a project id the registry no longer holds is dropped, wherever it sat;
 * - a project id that appears twice keeps only its first place;
 * - a project the layout does not mention is appended at the top level, in the
 *   order `projectIds` gives (an import lands at the bottom);
 * - a category with no usable id (a non-empty string not already taken by an
 *   earlier category) or no usable name (a non-empty string) is dropped, and
 *   its projects return to the top level where it stood, as deleteCategory
 *   does; a category's `collapsed` reads true only for the boolean true, and a
 *   `projects` that is not an array reads as empty;
 * - an item of any other kind, or a project item whose id is not a string, is
 *   dropped.
 *
 * @param {unknown} layout the stored value, of any shape
 * @param {string[]} projectIds every project the registry holds, in its order
 * @returns {{ items: object[] }} a new layout
 */
function normalize (layout, projectIds) {
  const known = new Set(projectIds)
  const placed = new Set()
  const categoryIds = new Set()
  const items = []

  /** The project id, if it may still be placed; marks it placed. */
  const claim = (id) => {
    if (typeof id !== 'string' || !known.has(id) || placed.has(id)) return null
    placed.add(id)
    return id
  }

  const stored = isObject(layout) && Array.isArray(layout.items) ? layout.items : []
  for (const item of stored) {
    if (!isObject(item)) continue
    if (item.kind === 'project') {
      const id = claim(item.id)
      if (id !== null) items.push({ kind: 'project', id })
      continue
    }
    if (item.kind !== 'category') continue

    const members = Array.isArray(item.projects) ? item.projects : []
    const usable = typeof item.id === 'string' && item.id !== '' &&
      !categoryIds.has(item.id) && isName(item.name)
    if (!usable) {
      for (const member of members) {
        const id = claim(member)
        if (id !== null) items.push({ kind: 'project', id })
      }
      continue
    }
    categoryIds.add(item.id)
    const projects = []
    for (const member of members) {
      const id = claim(member)
      if (id !== null) projects.push(id)
    }
    items.push({
      kind: 'category',
      id: item.id,
      name: item.name.trim(),
      collapsed: item.collapsed === true,
      projects
    })
  }

  for (const id of projectIds) {
    if (claim(id) !== null) items.push({ kind: 'project', id })
  }
  return { items }
}

/**
 * Every project id in sidebar order, top to bottom: the order Cmd+1 to Cmd+9,
 * Previous/Next Project and the Next view follow. A collapsed category's
 * projects are counted where the category stands, so folding one never
 * renumbers anything.
 *
 * @param {{ items: object[] }} layout
 * @returns {string[]}
 */
function flatten (layout) {
  const ids = []
  for (const item of layout.items) {
    if (item.kind === 'project') ids.push(item.id)
    else if (item.kind === 'category') ids.push(...item.projects)
  }
  return ids
}

/**
 * Move a project to the top level or into a category.
 *
 * `target` is `{ kind: 'top', index }` or `{ kind: 'category', id, index }`.
 * `index` is the gap to drop into, counted in the destination list as it
 * stands before the move: 0 is before its first entry, its length is after its
 * last. That is what a drop indicator between two rows names, and it makes a
 * drop on either gap beside the project's own row leave the layout as it was.
 * An index past either end is clamped to that end. A collapsed category takes
 * the project and stays collapsed.
 *
 * @returns {{ items: object[] }} a new layout
 */
function moveProject (layout, id, target) {
  if (!isObject(target)) throw new TypeError('moveProject: target must be an object')
  const index = checkIndex(target.index)
  const items = copy(layout)

  // Where the project is now: [list, position] for the list that holds it.
  let source = null
  for (const item of items) {
    if (item.kind === 'project' && item.id === id) {
      source = [items, items.indexOf(item)]
      break
    }
    if (item.kind === 'category' && item.projects.includes(id)) {
      source = [item.projects, item.projects.indexOf(id)]
      break
    }
  }
  if (source === null) throw new Error(`moveProject: no project ${JSON.stringify(id)}`)

  let destination
  if (target.kind === 'top') {
    destination = items
  } else if (target.kind === 'category') {
    const category = findCategory(items, target.id)
    if (category === null) throw new Error(`moveProject: no category ${JSON.stringify(target.id)}`)
    destination = category.projects
  } else {
    throw new TypeError('moveProject: target.kind must be "top" or "category"')
  }

  const [list, position] = source
  let at = clamp(index, destination.length)
  list.splice(position, 1)
  if (list === destination && position < at) at -= 1
  // The top level holds item objects; a category holds bare ids.
  destination.splice(at, 0, destination === items ? { kind: 'project', id } : id)
  return { items }
}

/**
 * Move a category among the top-level items. `index` is a top-level gap,
 * counted before the move and clamped, as in moveProject.
 *
 * @returns {{ items: object[] }} a new layout
 */
function moveCategory (layout, id, index) {
  checkIndex(index)
  const items = copy(layout)
  const position = items.findIndex((item) => item.kind === 'category' && item.id === id)
  if (position === -1) throw new Error(`moveCategory: no category ${JSON.stringify(id)}`)
  let at = clamp(index, items.length)
  const [category] = items.splice(position, 1)
  if (position < at) at -= 1
  items.splice(at, 0, category)
  return { items }
}

/**
 * Append a new, empty, expanded category at the end of the top level.
 *
 * The caller mints `id`; it must be a non-empty string no category in the
 * layout already has. `name` is trimmed and must not be empty.
 *
 * @returns {{ items: object[] }} a new layout
 */
function createCategory (layout, id, name) {
  if (typeof id !== 'string' || id === '') throw new TypeError('createCategory: id must be a non-empty string')
  const items = copy(layout)
  if (findCategory(items, id) !== null) throw new Error(`createCategory: category ${JSON.stringify(id)} already exists`)
  items.push({ kind: 'category', id, name: checkName(name), collapsed: false, projects: [] })
  return { items }
}

/** Give a category a new name, trimmed and non-empty. @returns a new layout */
function renameCategory (layout, id, name) {
  const trimmed = checkName(name)
  const items = copy(layout)
  requireCategory(items, id, 'renameCategory').name = trimmed
  return { items }
}

/** Fold or unfold a category; `collapsed` must be a boolean. @returns a new layout */
function setCategoryCollapsed (layout, id, collapsed) {
  if (typeof collapsed !== 'boolean') throw new TypeError('setCategoryCollapsed: collapsed must be a boolean')
  const items = copy(layout)
  requireCategory(items, id, 'setCategoryCollapsed').collapsed = collapsed
  return { items }
}

/**
 * Remove a category. Its projects are not lost: they return to the top level
 * in their order, at the place the category stood, so the flattened order —
 * and with it every shortcut number — is unchanged.
 *
 * @returns {{ items: object[] }} a new layout
 */
function deleteCategory (layout, id) {
  const items = copy(layout)
  const position = items.findIndex((item) => item.kind === 'category' && item.id === id)
  if (position === -1) throw new Error(`deleteCategory: no category ${JSON.stringify(id)}`)
  const projects = items[position].projects.map((project) => ({ kind: 'project', id: project }))
  items.splice(position, 1, ...projects)
  return { items }
}

// A deep enough copy that nothing the caller holds is shared with the result:
// new item objects and new category project arrays.
function copy (layout) {
  return layout.items.map((item) => item.kind === 'category'
    ? { ...item, projects: [...item.projects] }
    : { ...item })
}

function findCategory (items, id) {
  return items.find((item) => item.kind === 'category' && item.id === id) ?? null
}

function requireCategory (items, id, caller) {
  const category = findCategory(items, id)
  if (category === null) throw new Error(`${caller}: no category ${JSON.stringify(id)}`)
  return category
}

function checkIndex (index) {
  if (!Number.isInteger(index)) throw new TypeError('index must be an integer')
  return index
}

function checkName (name) {
  if (!isName(name)) throw new TypeError('name must be a non-empty string')
  return name.trim()
}

function clamp (index, length) {
  return Math.min(Math.max(index, 0), length)
}

function isName (name) {
  return typeof name === 'string' && name.trim() !== ''
}

function isObject (value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

module.exports = {
  normalize,
  flatten,
  moveProject,
  moveCategory,
  createCategory,
  renameCategory,
  setCategoryCollapsed,
  deleteCategory
}
