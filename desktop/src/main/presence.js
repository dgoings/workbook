'use strict'

// Whether each imported project's repository is still where the registry says
// it is.
//
// A repository can be deleted, moved, or sit on a drive that is not mounted
// right now. Its board then cannot start, and what `workbook serve` says about
// a working directory that is not there is `spawn .../workbook ENOENT`, which
// reads as though the CLI were missing. So the app looks first, cheaply — two
// stats, no Git and no child process — and the sidebar marks the project as
// missing and the board area says so plainly.
//
// Looking never removes anything. A drive that is not mounted comes back, and
// the project with it, the next time anything looks: when a board starts, when
// a project is opened, and when the window comes back into focus. There is no
// polling loop.

const fs = require('node:fs/promises')
const path = require('node:path')

const PRESENT = Object.freeze({ missing: false, reason: null, message: null })

/**
 * Look for a checkout at `dir`.
 *
 * Missing only for an answer that is certain: nothing at the path (ENOENT), a
 * path that runs through a file (ENOTDIR), a file where the checkout was, or a
 * folder with no `.git` in it. A `.git` file counts, since a worktree or a
 * submodule has one. Any other failure — permission denied, an I/O error — is
 * not called missing: it is not known to be, and the board's own start reports
 * the real problem.
 *
 * @param {unknown} dir
 * @param {{ stat?: (target: string) => Promise<import('node:fs').Stats> }} [options]
 * @returns {Promise<{ missing: boolean, reason: null|'not-found'|'not-a-repository', message: string|null }>}
 */
async function checkRepository (dir, { stat = fs.stat } = {}) {
  if (typeof dir !== 'string' || dir === '') {
    return { missing: true, reason: 'not-found', message: 'Repository not found: no path recorded' }
  }
  const notFound = { missing: true, reason: 'not-found', message: `Repository not found at ${dir}` }
  let info
  try {
    info = await stat(dir)
  } catch (error) {
    return certainlyAbsent(error) ? notFound : PRESENT
  }
  if (!info.isDirectory()) return notFound
  try {
    await stat(path.join(dir, '.git'))
  } catch (error) {
    if (!certainlyAbsent(error)) return PRESENT
    return { missing: true, reason: 'not-a-repository', message: `No Git repository at ${dir}` }
  }
  return PRESENT
}

function certainlyAbsent (error) {
  return error?.code === 'ENOENT' || error?.code === 'ENOTDIR'
}

/**
 * What the app last found for each project, and the means to look again.
 *
 * @param {object} options
 * @param {() => Array<{ id: string, path: string }>} options.projects the registry's projects
 * @param {(dir: string) => Promise<object>} [options.check] checkRepository, or a stand-in
 * @param {() => void} options.onChanged called once after a refresh that changed anything
 */
function createPresenceTracker ({ projects, check = checkRepository, onChanged }) {
  /** @type {Map<string, { missing: boolean, reason: string|null, message: string|null }>} */
  const known = new Map()

  return {
    /** What was last found for `projectId`; present until something looks. */
    get (projectId) {
      return known.get(projectId) ?? PRESENT
    },

    /**
     * Look again at the named projects, or at every one. Resolves whether
     * anything changed; never rejects. A project no longer listed by the time
     * its answer arrives — removed while it was being looked at — is not
     * recorded, so nothing is kept for a project the app has let go of.
     *
     * @param {string[]} [ids]
     * @returns {Promise<boolean>}
     */
    async refresh (ids) {
      const wanted = ids ? new Set(ids) : null
      const targets = projects().filter((project) => wanted === null || wanted.has(project.id))
      const answers = await Promise.all(targets.map(async (project) => {
        try {
          return [project, await check(project.path)]
        } catch {
          return [project, PRESENT]
        }
      }))
      let changed = false
      for (const [project, found] of answers) {
        const still = projects().find((entry) => entry.id === project.id)
        if (!still || still.path !== project.path) continue
        const before = known.get(project.id) ?? PRESENT
        const after = found.missing
          ? { missing: true, reason: found.reason ?? 'not-found', message: found.message ?? null }
          : PRESENT
        if (after.missing) known.set(project.id, after)
        else known.delete(project.id)
        if (before.missing !== after.missing || before.message !== after.message) changed = true
      }
      if (changed) onChanged()
      return changed
    },

    /** Drop what is known about a project that has been removed. */
    forget (projectId) {
      known.delete(projectId)
    }
  }
}

module.exports = { checkRepository, createPresenceTracker }
