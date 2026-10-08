'use strict'

// The sidebar's drag-and-drop arithmetic and the rail's category label, kept
// out of app.js so they can be tested without a DOM: app.js is a page script
// that only runs inside Electron, and this file is loaded by the same page
// (as window.sidebarModel) and by node --test (through module.exports).
//
// Nothing here touches the layout. It answers one question — "if the pointer
// were let go here, what would the main process be asked to do?" — and the
// renderer asks it, sends the request, and redraws only from the answer the
// main process broadcasts.

;(function (root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  else root.sidebarModel = api
})(typeof self !== 'undefined' ? self : this, function () {
  /**
   * The top quarter of a category's header drops a project above the category,
   * at the top level; the rest drops it into the category. A project usually
   * means to go in, so "in" gets the larger share; the sliver keeps "just
   * above this group" reachable without a separate target.
   */
  const ABOVE_CATEGORY = 0.25

  /**
   * What letting go here would do.
   *
   * @param {{ items: object[] }} layout the layout as last broadcast
   * @param {{ kind: 'project'|'category', id: string }} dragged
   * @param {{ kind: 'project'|'category', id: string } | { kind: 'end' }} over
   *   the row under the pointer: a project row (top level or inside a
   *   category), a category's header, or the empty list below the last row
   * @param {number} fraction how far down that row the pointer is, 0 to 1
   * @returns {null | {
   *   action: 'moveProject', target: object, indicator: object
   * } | {
   *   action: 'moveCategory', index: number, indicator: object
   * }} null where a drop is refused. `target` and `index` are what the
   *   sidebar:moveProject and sidebar:moveCategory channels take: a gap counted
   *   before the move. `indicator` is what to draw: `{ type: 'line', key,
   *   edge: 'before'|'after' }` against the row named by key
   *   ('project:<id>' or 'category:<id>'; null for an empty list), or
   *   `{ type: 'into', key: 'category:<id>' }` to light a category's header.
   */
  function dropTarget (layout, dragged, over, fraction) {
    const items = layout.items
    const before = fraction < 0.5
    const edge = before ? 'before' : 'after'
    const isCategory = dragged.kind === 'category'
    const toTop = (index, indicator) => isCategory
      ? { action: 'moveCategory', index, indicator }
      : { action: 'moveProject', target: { kind: 'top', index }, indicator }

    if (over.kind === 'end') {
      const last = items.at(-1)
      const key = last ? `${last.kind}:${last.id}` : null
      return toTop(items.length, { type: 'line', key, edge: 'after' })
    }

    if (over.kind === 'project') {
      const top = items.findIndex((item) => item.kind === 'project' && item.id === over.id)
      if (top !== -1) {
        return toTop(before ? top : top + 1, { type: 'line', key: `project:${over.id}`, edge })
      }
      const category = items.find((item) => item.kind === 'category' && item.projects.includes(over.id))
      if (!category) return null
      // Categories are one level deep: one cannot go among another's projects.
      if (isCategory) return null
      const child = category.projects.indexOf(over.id)
      return {
        action: 'moveProject',
        target: { kind: 'category', id: category.id, index: before ? child : child + 1 },
        indicator: { type: 'line', key: `project:${over.id}`, edge }
      }
    }

    if (over.kind === 'category') {
      const top = items.findIndex((item) => item.kind === 'category' && item.id === over.id)
      if (top === -1) return null
      const key = `category:${over.id}`
      if (isCategory) return toTop(before ? top : top + 1, { type: 'line', key, edge })
      if (fraction < ABOVE_CATEGORY) return toTop(top, { type: 'line', key, edge: 'before' })
      // Into the category, at its end — collapsed or not: a collapsed one
      // takes the project and stays folded.
      return {
        action: 'moveProject',
        target: { kind: 'category', id: over.id, index: items[top].projects.length },
        indicator: { type: 'into', key }
      }
    }
    return null
  }

  /**
   * The rail's label for a category: the first letter of each of its first two
   * words, upper-cased ("Client work" is "CW", "Personal" is "P"). The rail is
   * 76px wide and the full name is on the divider's tooltip.
   */
  function initials (name) {
    const words = String(name).trim().split(/[\s\-_/.]+/).filter(Boolean)
    const letters = words.slice(0, 2).map((word) => Array.from(word)[0].toUpperCase())
    return letters.join('') || '·'
  }

  /**
   * Which row the pointer is over, by height alone.
   *
   * The list has side padding and a category's projects are indented, so the
   * element under the pointer is often a list rather than a row; asking which
   * row's band holds the pointer's Y is what a reader means by "here". A
   * pointer in the gap between two rows is before the lower one, and one below
   * the last row is the end of the list.
   *
   * @param {Array<{ kind: 'project'|'category', id: string, top: number, bottom: number }>} rows
   *   the visible project rows and category headers, top to bottom
   * @param {number} y the pointer's Y, in the same coordinates as the rows
   * @returns {{ over: object, fraction: number }} arguments for dropTarget
   */
  function rowAt (rows, y) {
    for (const row of rows) {
      if (y < row.top) return { over: { kind: row.kind, id: row.id }, fraction: 0 }
      if (y < row.bottom) {
        const height = row.bottom - row.top
        return { over: { kind: row.kind, id: row.id }, fraction: height > 0 ? (y - row.top) / height : 0.5 }
      }
    }
    return { over: { kind: 'end' }, fraction: 1 }
  }

  return { dropTarget, rowAt, initials, ABOVE_CATEGORY }
})
