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
 * The key a project mints new tasks under, from `workbook key list --json`.
 *
 * A read: `key list` never fetches, so this costs one local process and
 * changes nothing. An answer that is not a key at all — a binary from before
 * project keys could move, say — rejects rather than labeling a project with it.
 *
 * @param {string} repoPath
 * @param {{ runJSON?: typeof workbook.runJSON }} [options]
 * @returns {Promise<string>}
 */
async function currentKey (repoPath, { runJSON = workbook.runJSON } = {}) {
  const data = await runJSON(repoPath, ['key', 'list'])
  const key = data?.current
  if (typeof key !== 'string' || !isValidKey(key)) {
    throw new Error(`workbook key list in ${repoPath} reported no current key`)
  }
  return key
}

/**
 * Read every project's current key at once and return the ones that differ
 * from the key it is labeled with.
 *
 * A project that cannot be read — its repository moved or deleted, a binary
 * too old to answer — is left out, so it keeps the last key it was known by,
 * and the others are still read.
 *
 * @param {{ projects: Array<{ id: string, key: string, path: string }>, read?: (repoPath: string) => Promise<string> }} input
 * @returns {Promise<Array<{ id: string, key: string }>>}
 */
async function staleKeys ({ projects, read = currentKey }) {
  const outcomes = await Promise.allSettled(projects.map((project) => read(project.path)))
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

module.exports = { currentKey, staleKeys, createKeyRefresher, isKeyChange }
