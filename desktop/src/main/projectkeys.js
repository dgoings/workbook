'use strict'

// The key each project is labeled with — its sidebar row, its rail tile, its
// menu item, its Next view heading — kept at the key the project mints new
// tasks under now.
//
// A project can have several keys, and `workbook key current` (or the board's
// Keys section) moves which one is current. The registry entry is written once,
// at import, from `.workbook/config.json` or from what `setup` reported, and
// config.json keeps the founding key for good; left alone, the label would show
// that key forever. So the current key is read from the project itself, through
// `workbook key list`, whenever something suggests it may have moved, and the
// registry keeps the last answer as the label to show until the next one — and
// for good, if the repository has gone missing. The runner and the registry are
// parameters so all of this can be tested without a binary or Electron.

const workbook = require('./workbook')
const { isValidKey } = require('./discovery')

/**
 * How many projects' keys are read at once. Each read is its own `workbook`
 * process, and the window taking focus asks for every project, so without a cap
 * a long sidebar would start one process per project on every alt-tab.
 */
const KEY_READ_LIMIT = 4

/**
 * The key a project mints new tasks under, from
 * `workbook key list --no-counts --json`.
 *
 * A read: `key list` never fetches, so this costs one local process and
 * changes nothing. --no-counts leaves out how many tasks each key minted, the
 * one part of the listing that reads every task, so the answer comes from the
 * project's configuration alone. An answer that is not a key at all rejects
 * rather than labeling a project with it, and so does a binary too old to know
 * --no-counts; either way the project keeps the last key it was known by.
 *
 * @param {string} repoPath
 * @param {{ runJSON?: typeof workbook.runJSON }} [options]
 * @returns {Promise<string>}
 */
async function currentKey (repoPath, { runJSON = workbook.runJSON } = {}) {
  const data = await runJSON(repoPath, ['key', 'list', '--no-counts'])
  const key = data?.current
  if (typeof key !== 'string' || !isValidKey(key)) {
    throw new Error(`workbook key list in ${repoPath} reported no current key`)
  }
  return key
}

/**
 * Promise.allSettled over `items`, with at most `limit` calls of `fn` running
 * at once. The outcomes are in the order of `items`, whatever order they
 * finished in.
 *
 * @template T, R
 * @param {T[]} items
 * @param {number} limit
 * @param {(item: T) => Promise<R>} fn
 * @returns {Promise<PromiseSettledResult<R>[]>}
 */
async function settleLimited (items, limit, fn) {
  const outcomes = new Array(items.length)
  let next = 0
  async function worker () {
    while (next < items.length) {
      const index = next++
      try {
        outcomes[index] = { status: 'fulfilled', value: await fn(items[index]) }
      } catch (reason) {
        outcomes[index] = { status: 'rejected', reason }
      }
    }
  }
  const workers = Math.max(1, Math.min(limit, items.length))
  await Promise.all(Array.from({ length: workers }, worker))
  return outcomes
}

/**
 * Read every project's current key, a few at a time, and return the ones that
 * differ from the key it is labeled with.
 *
 * A project that cannot be read — its repository moved or deleted, a binary
 * too old to answer — is left out, so it keeps the last key it was known by,
 * and the others are still read.
 *
 * @param {{ projects: Array<{ id: string, key: string, path: string }>, read?: (repoPath: string) => Promise<string>, limit?: number }} input
 * @returns {Promise<Array<{ id: string, key: string }>>}
 */
async function staleKeys ({ projects, read = currentKey, limit = KEY_READ_LIMIT }) {
  const outcomes = await settleLimited(projects, limit, (project) => read(project.path))
  const changes = []
  projects.forEach((project, index) => {
    const outcome = outcomes[index]
    if (outcome.status !== 'fulfilled') return
    if (outcome.value === project.key) return
    changes.push({ id: project.id, key: outcome.value })
  })
  return changes
}

/**
 * Re-read keys on request, one pass at a time.
 *
 * Several things ask at once — every board starting at launch, the window
 * taking focus, a board changing a key — so requests that arrive while a pass
 * is running are gathered into the one pass that follows it. That keeps a
 * project from being read twice at once, and keeps an older read from landing
 * after a newer one and putting a key back that has already moved on. The
 * projects are looked up when a pass starts, not when it is asked for, so a
 * project forgotten in between is skipped and a moved one is read where it is.
 *
 * @param {object} options
 * @param {() => Array<{ id: string, key: string, path: string }>} options.projects
 *   every project the registry holds, read fresh for each pass
 * @param {(repoPath: string) => Promise<string>} [options.read]
 * @param {(changes: Array<{ id: string, key: string }>) => Promise<Array<{ id: string, key: string }>>} options.apply
 *   store the changes and resolve with the ones actually made (registry.setKeys)
 * @param {(applied: Array<{ id: string, key: string }>) => void} options.onChanged
 *   redraw whatever shows a key; called only when a key really changed
 * @param {(line: string) => void} [options.log]
 */
function createKeyRefresher ({ projects, read = currentKey, apply, onChanged, log = () => {} }) {
  /** What the next pass reads: null for nothing, ALL, or a Set of ids. */
  let wanted = null
  /** @type {Promise<void>|null} */
  let running = null
  const ALL = 'all'

  async function pass (ids) {
    const list = projects().filter((project) => ids === ALL || ids.has(project.id))
    if (list.length === 0) return
    const changes = await staleKeys({ projects: list, read })
    if (changes.length === 0) return
    let applied
    try {
      applied = await apply(changes)
    } catch (error) {
      // The label stays at the last key that was stored; the next request
      // tries again.
      log(`workbench: could not store the projects' current keys: ${error.message}`)
      return
    }
    if (applied.length > 0) onChanged(applied)
  }

  async function drain () {
    try {
      while (wanted !== null) {
        const ids = wanted
        wanted = null
        await pass(ids)
      }
    } finally {
      running = null
    }
  }

  return {
    /**
     * Re-read the named projects' keys, or every project's with no argument.
     * Resolves once the pass that covers this request has finished.
     *
     * @param {string[]} [ids]
     * @returns {Promise<void>}
     */
    refresh (ids) {
      if (ids === undefined || wanted === ALL) {
        wanted = ALL
      } else {
        wanted = new Set([...(wanted ?? []), ...ids])
      }
      if (!running) running = drain()
      return running
    }
  }
}

/**
 * Whether a request a board made changed its project's keys: a successful add
 * (`POST /api/vocabulary/keys`) or edit (`PATCH /api/vocabulary/keys/<key>`),
 * which is how the board's Keys section makes a key current. A refusal changed
 * nothing, and a read never does.
 *
 * @param {{ method?: string, url?: string, statusCode?: number }} details
 *   the shape of Electron's webRequest completion details
 */
function isKeyChange ({ method, url, statusCode } = {}) {
  if (method !== 'POST' && method !== 'PATCH') return false
  if (!(statusCode >= 200 && statusCode < 300)) return false
  let pathname
  try {
    pathname = new URL(url).pathname
  } catch {
    return false
  }
  return pathname === '/api/vocabulary/keys' || pathname.startsWith('/api/vocabulary/keys/')
}

module.exports = { KEY_READ_LIMIT, currentKey, staleKeys, createKeyRefresher, isKeyChange }
